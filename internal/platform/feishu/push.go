package feishu

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/chadhao/jx-procurement-platform/internal/observ"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// push.go —— 出方向推送（external_instances + update_time + t_push_record）—— 架构转向 ③（04a §3 / §13）。
//
// ★ `update_mode` 选型判据（04a §3.1）：**能 `UPDATE` 就不用 `REPLACE`**。
//   - 默认 `UPDATE`（增量；**仅当 `update_time` 变大才更新** → **机制上**消除 `S14` 陈旧快照覆盖回退）；
//   - `REPLACE` **仅限两类**：① **首次推实例**（唯一例外）② **需要"删掉飞书侧 task / 抄送"的场景**。
//     ★ 「转交 / 加签 / 新增抄送」**都不属于此类**（不需要删任何东西）。
//
// ★ 快照**只含 `release_state='RELEASED'` 的 task**；未释放**整体省略**（否则飞书侧会为未来节点生成待办）。
// ★ 超限：`task_list > 300` / `cc_list > 200` → **直接失败并告警，绝不静默截断**（S12）。

// 推送上限（04a §3.2 / §10 S12）。
const (
	MaxTaskList = 300
	MaxCCList   = 200
)

// 推送更新模式（external_instances.update_mode）。
const (
	UpdateModeUpdate  = "UPDATE"
	UpdateModeReplace = "REPLACE"
)

// ExternalTask 快照中的单个任务（仅含已 RELEASED 的）。
type ExternalTask struct {
	TaskID         string `json:"task_id"`
	NodeID         string `json:"node_id"`
	NodeName       string `json:"node_name"`
	AssigneeOpenID string `json:"assignee_open_id"`
	Status         string `json:"status"`
	// ActionContext 操作上下文（docs/16 §2-B）：**我方自定义**的压缩 JSON 字符串
	// `{"biz_no":"…","task_id":"…"}`，随待办下发、期望飞书在回调时**原样回传**——
	// 这是回调侧 `biz_no` 的**主读**来源（官方**不发**顶层 biz_no，docs/16 G-1）。
	// ★★ 「飞书原样回传 action_context」系官方文档表述、**尚未实测**（docs/16 §7 V-1），
	// 联调第一轮必须首验；若不回传/改写，回调侧由 `instance_id` 反解兜底（§2-A-2），
	// 链路仍通但须回 docs/09 台账记实测结果。
	// ★ 兼容：旧快照推的是纯 task_id 字符串（非 `{` 开头）——回调侧对非 JSON 的
	// action_context 直接忽略、走反解兜底，两侧约定**同批**落地（docs/16 §2-B 同批约束）。
	ActionContext string `json:"action_context,omitempty"`
}

// InstanceSnapshot external_instances 上报快照。
type InstanceSnapshot struct {
	ApprovalCode string         `json:"approval_code"`
	InstanceID   string         `json:"instance_id"`
	UpdateTime   int64          `json:"update_time"`
	Status       string         `json:"status"`
	TaskList     []ExternalTask `json:"task_list"`
	CCList       []string       `json:"cc_list,omitempty"`
}

// PushClient external_instances 推送端口（HTTP 实现 + 测试替身）。
type PushClient interface {
	UpsertExternalInstance(ctx context.Context, updateMode string, snap InstanceSnapshot) error
}

// PushResult 一次推送的结果。
type PushResult struct {
	Pushed       bool // 是否真的推了
	Skipped      bool // 因 update_time 未增大而跳过（幂等）
	UpdateMode   string
	PushSeq      int64
	SnapshotHash string
}

// ChooseUpdateMode 选型判据（04a §3.1）：首次推实例、或需删飞书侧数据时用 `REPLACE`；否则 `UPDATE`。
func ChooseUpdateMode(isFirstPush bool, needDelete bool) string {
	if isFirstPush || needDelete {
		return UpdateModeReplace
	}
	return UpdateModeUpdate
}

// BuildSnapshot 组装快照：**只含已 RELEASED 的 task**；未释放整体省略。超限返回错误（不截断）。
func BuildSnapshot(inst *store.Instance, tasks []store.FlowTask, ccList []string) (InstanceSnapshot, error) {
	if inst == nil {
		return InstanceSnapshot{}, fmt.Errorf("feishu: 组装快照失败: 实例为空")
	}
	snap := InstanceSnapshot{
		ApprovalCode: inst.ApprovalCode,
		InstanceID:   inst.InstanceCode,
		UpdateTime:   inst.UpdateTime,
		Status:       inst.Status,
		CCList:       ccList,
	}
	for _, t := range tasks {
		if t.ReleaseState != "RELEASED" {
			continue // ★ 未释放整体省略（不推、不生成待办）
		}
		// ★ action_context 承载 biz_no（docs/16 §2-B）：压缩 JSON `{"biz_no":…,"task_id":…}`，
		//   键名与回调解侧约定**必须同批**（解侧＝internal/httpapi/handlers_approval.go 的
		//   biz_no 主读路径）。map 序列化时 Go 按键名升序输出，形态确定可测。
		ac, err := json.Marshal(map[string]string{"biz_no": inst.BizNo, "task_id": t.TaskID})
		if err != nil {
			return InstanceSnapshot{}, fmt.Errorf("feishu: 组装 action_context 失败: %w", err)
		}
		snap.TaskList = append(snap.TaskList, ExternalTask{
			TaskID: t.TaskID, NodeID: t.NodeID, NodeName: t.NodeName,
			AssigneeOpenID: t.AssigneeOpenID, Status: t.Status,
			ActionContext: string(ac),
		})
	}
	if len(snap.TaskList) > MaxTaskList {
		return InstanceSnapshot{}, fmt.Errorf("feishu: task_list 超限 %d > %d（失败告警，绝不静默截断）",
			len(snap.TaskList), MaxTaskList)
	}
	if len(snap.CCList) > MaxCCList {
		return InstanceSnapshot{}, fmt.Errorf("feishu: cc_list 超限 %d > %d（失败告警，绝不静默截断）",
			len(snap.CCList), MaxCCList)
	}
	return snap, nil
}

// snapshotHash 计算快照 hash（相同快照可跳过，不消耗 update_time）。
func snapshotHash(snap InstanceSnapshot) string {
	b, err := json.Marshal(snap)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Pusher 出方向推送服务。
type Pusher struct {
	db     *store.DB
	client PushClient
	log    *slog.Logger
}

// NewPusher 构造推送服务。
func NewPusher(db *store.DB, client PushClient, log *slog.Logger) *Pusher {
	if log == nil {
		log = observ.NewLogger("info", nil)
	}
	return &Pusher{db: db, client: client, log: log}
}

// Push 推送某实例当前快照。
//
// ★★ 落库纪律（docs/16 §2-D 纪律一）：**本方法是推实例的唯一入口**，其数据源就是本地
//
//	`t_instance`（GetInstanceByBizNo）与 `t_flow_task`（ListFlowTasks）——本地行是推送的
//	**前提**，而非推送的副产品。**禁止任何「只推飞书、不落本地」的生产路径**。
//	正规链路：`flow.Submit`（internal/flow/service.go）**同事务**写 `t_instance` +
//	`t_flow_task` + 状态史 + op_log，事务提交后经 `flow.emit` 分发 `flowPushSubscriber`
//	（cmd/jxapproval/bootstrap.go）才调到本方法 ⇒ 本地行**天然先于**推送存在，回调可命中。
//	手工 curl 直推 `external_instances` 仅限联调排障，且须知道该实例**本地不可回调**
//	（无 `t_instance` 行 ⇒ 回调报「无对应实例」，正是 docs/16 实测现象）。
//
// ★ 幂等 / 单调（04a §3.1）：仅当 `update_time` **大于**已推送的最大版本时才真正推送；
// 否则跳过（Skipped）——避免"落后快照 REPLACE 抹掉已推进数据"。
func (p *Pusher) Push(ctx context.Context, bizNo string) (PushResult, error) {
	if p.client == nil {
		return PushResult{}, fmt.Errorf("feishu: 推送失败: 未配置 PushClient")
	}
	inst, err := p.db.GetInstanceByBizNo(ctx, bizNo)
	if err != nil {
		return PushResult{}, err
	}
	records, err := p.db.ListPushRecords(ctx, bizNo)
	if err != nil {
		return PushResult{}, err
	}
	// 已推送的最大版本 + 是否首次（首推用 REPLACE）。
	var lastSeq int64
	first := true
	for _, r := range records {
		if r.Status == store.PushSent {
			first = false
		}
		if r.PushSeq > lastSeq {
			lastSeq = r.PushSeq
		}
	}
	if inst.UpdateTime <= lastSeq {
		return PushResult{Skipped: true, PushSeq: inst.UpdateTime}, nil
	}

	tasks, err := p.db.ListFlowTasks(ctx, bizNo)
	if err != nil {
		return PushResult{}, err
	}
	snap, err := BuildSnapshot(inst, tasks, nil)
	if err != nil {
		// 超限等：告警且**不写流水**（这是"请求有误"，非"平台不支持"；S12）。
		p.log.Error("推送组装失败（不静默截断）", "biz_no", bizNo, "error", err.Error())
		return PushResult{}, err
	}
	// ★★ 双 code 池消歧（docs/16 G-8 / §2-C）：读/推实例要走「真实 code」，而
	//   `approval_code` 字段名同指两物（实测：GET 读回的 approval_code ＝ 自定义 code）。
	//   ⇒ 推实例的 snap.ApprovalCode **优先取 t_approval_def.feishu_code**（Registry.Register
	//   落库的 POST 响应回填值）、为空则回退 inst.ApprovalCode（我方自定义 code）。
	//   ★ 双池归属未实测（docs/16 §7 V-4）⇒ **双写、不猜**；定义缺失时静默回退自定义 code
	//   （回退本身不吞错误：GetApprovalDef 的 ErrNotFound 属正常态，其余错误仅告警）。
	code := inst.ApprovalCode
	if def, derr := p.db.GetApprovalDef(ctx, inst.ApprovalCode); derr == nil {
		if fc := strings.TrimSpace(def.FeishuCode); fc != "" {
			code = fc
		}
	} else if !errors.Is(derr, store.ErrNotFound) {
		p.log.Warn("推送前读取审批定义失败（回退自定义 approval_code 推送）",
			"biz_no", bizNo, "approval_code", inst.ApprovalCode, "error", derr.Error())
	}
	snap.ApprovalCode = code
	// ★ 快照守卫（docs/16 §2-D 纪律三）：实例 PENDING 但 RELEASED 任务数为 0 ⇒
	//   无任务快照是异常态（推出去也没有可操作待办，多为任务链未建/释放态异常）。
	//   **只 warn、不拦截、不改变返回值语义**（REPLACE 首推等场景由上层判断）。
	//   字面量与 flow.InstancePending 同值（PENDING）；不反向依赖领域包。
	if inst.Status == "PENDING" && len(snap.TaskList) == 0 {
		p.log.Warn("★ 快照守卫：实例 PENDING 但 RELEASED 任务数为 0（无任务快照是异常态）",
			"biz_no", bizNo, "status", inst.Status, "update_time", inst.UpdateTime)
	}
	mode := ChooseUpdateMode(first, false)
	hash := snapshotHash(snap)

	if err := p.db.InsertPushRecord(ctx, &store.PushRecord{
		BizNo: bizNo, PushSeq: inst.UpdateTime, SnapshotHash: hash, Status: store.PushPending,
	}); err != nil {
		return PushResult{}, err
	}
	if err := p.client.UpsertExternalInstance(ctx, mode, snap); err != nil {
		// ★ C6 收口（P3·可观测性）：推送失败本身已 return 给调用方（知情）；
		//   真正静默的是「登记 Failed」这一步失败 —— 若它静默，推送记录会停在 Pending，
		//   而 Pending 同时意味「在途」与「失败但没记上」，监控层分不清。故显式捕获记账错误。
		if mErr := p.db.MarkPushResult(ctx, bizNo, inst.UpdateTime, store.PushFailed, err.Error()); mErr != nil {
			p.log.Warn("登记推送失败状态未写入（记录将停在 Pending，监控无法区分在途与失败）",
				"biz_no", bizNo, "push_seq", inst.UpdateTime,
				"mark_error", mErr.Error(), "push_error", err.Error())
		}
		return PushResult{}, err
	}
	if err := p.db.MarkPushResult(ctx, bizNo, inst.UpdateTime, store.PushSent, ""); err != nil {
		return PushResult{}, err
	}
	return PushResult{Pushed: true, UpdateMode: mode, PushSeq: inst.UpdateTime, SnapshotHash: hash}, nil
}

// ---------- HTTP 实现 ----------

// UpsertExternalInstance 推送实例快照（POST /external_instances）。
func (c *HTTPClient) UpsertExternalInstance(ctx context.Context, updateMode string, snap InstanceSnapshot) error {
	body := map[string]any{
		"approval_code": snap.ApprovalCode,
		"instance_id":   snap.InstanceID,
		"update_time":   snap.UpdateTime,
		"update_mode":   updateMode,
		"status":        snap.Status,
		"task_list":     snap.TaskList,
	}
	if len(snap.CCList) > 0 {
		body["cc_list"] = snap.CCList
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("feishu: 组装 external_instances 失败: %w", err)
	}
	_, _, err = c.doJSON(ctx, http.MethodPost,
		"/open-apis/approval/v4/external_instances", nil, bytes.NewReader(raw))
	return err
}

// 编译期断言：HTTPClient 实现 PushClient。
var _ PushClient = (*HTTPClient)(nil)

// ---------- 测试 / 开发替身 ----------

// FakePushClient 是 PushClient 的内存替身。
type FakePushClient struct {
	mu     sync.Mutex
	bodies []InstanceSnapshot
	modes  []string
	PushFn func(ctx context.Context, updateMode string, snap InstanceSnapshot) error
}

// NewFakePushClient 构造内存替身。
func NewFakePushClient() *FakePushClient { return &FakePushClient{} }

// UpsertExternalInstance 记录快照（可设 PushFn 覆盖以模拟失败）。
func (f *FakePushClient) UpsertExternalInstance(ctx context.Context, updateMode string, snap InstanceSnapshot) error {
	if f.PushFn != nil {
		return f.PushFn(ctx, updateMode, snap)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bodies = append(f.bodies, snap)
	f.modes = append(f.modes, updateMode)
	return nil
}

// Count 返回推送次数。
func (f *FakePushClient) Count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.bodies)
}

// Last 返回最近一次快照与模式。
func (f *FakePushClient) Last() (InstanceSnapshot, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.bodies) == 0 {
		return InstanceSnapshot{}, ""
	}
	return f.bodies[len(f.bodies)-1], f.modes[len(f.modes)-1]
}
