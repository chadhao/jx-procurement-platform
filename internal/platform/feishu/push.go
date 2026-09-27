package feishu

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
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
		snap.TaskList = append(snap.TaskList, ExternalTask{
			TaskID: t.TaskID, NodeID: t.NodeID, NodeName: t.NodeName,
			AssigneeOpenID: t.AssigneeOpenID, Status: t.Status,
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
