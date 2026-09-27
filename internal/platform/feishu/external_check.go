package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// external_check.go —— 出方向对账端口（架构转向 ③ · T03；04a §9.2）。
//
// 职责：调飞书 `POST /open-apis/approval/v4/external_instances/check`，取回**差异实例**
// （平台口径：`check` 只返回 `diff_instances`，**不返回全量**）。
//
// ★ 本包只上抛"平台观测到的差异"，**不在此判方向、不做补拉**：
// 方向判断与"是否重推"由上层 `internal/sync.ApprovalReconciler` 负责
// （R06：新对账器**独立于 ingest**、**不复用**旧 `Reconciler`；禁止"缺则补"的覆盖路径）。
//
// ★ 边界纪律：飞书 HTTP 只在本包发生；上层只依赖 `ExtCheckClient` 接口
//   （与 `ExternalApprovalClient` / `PushClient` 同式的独立小接口，不改动 `Client`）。
//
// ★★ 请求体已实测定稿（2026-09-27 实测，reference/README.md 台账；2026-09-28 修 body 缺陷）：
//
//	入参**必须含 `instances[]`**，每项含 `instance_id` + `update_time` + `tasks[]`
//	（tasks[] 每项含 `task_id` + `update_time`）；只给 instance_id ⇒
//	`99992402 field_violations=[instances: instances is required]`。
//	本实现此前只组装了 `{"approval_code":…}` ⇒ 启动对账恒报 99992402（本批修复）。
//	★ 字段表示**与推送侧完全一致**（BuildCheckInstance：实例 update_time＝t_instance.update_time
//	的同一 int64 版本值转**字符串**（官方字段表标 string，2026-09-28 与推送侧同批对齐）；
//	task update_time＝feishuMilli 毫秒字符串）—— check 是拿我方上报值
//	与平台**存储值**比对，表示形式必须与推送时相同，否则平台把"同值"误判为差异。
//	★ 数据源＝本地 `t_instance` / `t_flow_task`（由调用方经 ListInstancesByApprovalCode +
//	BuildCheckInstance 组装后传入；HTTP 客户端保持纯传输、不触库）。

// ExternalInstanceState 平台侧观测到的某实例状态（供上层做**方向判断**）。
type ExternalInstanceState struct {
	// InstanceID 平台侧实例标识（＝我方推送的 `instance_id` ＝ `InstanceCode`）。
	InstanceID string
	// UpdateTime 平台侧观测到的版本号（`.update_time`）。★ 缺失/不可判时为 0。
	UpdateTime int64
	// Status 平台侧观测到的状态（`.status`）。缺失时为空串。
	Status string
}

// ExternalCheckTask check 请求 instances[].tasks[] 每项。
//
// ★ update_time 用与推送侧（external_instances.task_list[*].update_time，实测口径）
// **完全相同的表示**——feishuMilli 毫秒字符串（见 BuildCheckInstance）。
type ExternalCheckTask struct {
	TaskID     string `json:"task_id"`
	UpdateTime string `json:"update_time"`
}

// ExternalCheckInstance check 请求 instances[] 每项。
//
// ★ 官方实测要求：每项必须含 `update_time` + `tasks`（只给 instance_id ⇒ 99992402）。
// instance update_time 与推送侧同源同形（t_instance.update_time 的 int64 版本值转
// **字符串**下发——官方字段表标 string，2026-09-28 推送侧对齐；★ 表示形式必须与
// 推送时相同，否则平台把"同值"误判为差异）。
type ExternalCheckInstance struct {
	InstanceID string              `json:"instance_id"`
	UpdateTime string              `json:"update_time"`
	Tasks      []ExternalCheckTask `json:"tasks"`
}

// flexInt64 宽容解析平台回显的数值/字符串版本值（`"7"` 与 `7` 都接受）。
//
// ★ 教训预防（2026-09-28 message_id 事故同源）：推送侧 update_time 由 int64 对齐官方
// string 后，平台 diff_instances 回显的 update_time 类型**未实测**——若按 int64 硬收
// 字符串会整包解析失败。两侧类型都接受，杜绝响应解析单点故障。
type flexInt64 int64

func (f *flexInt64) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return fmt.Errorf("feishu: 解析版本值失败: %w（raw=%s）", err, string(b))
	}
	*f = flexInt64(v)
	return nil
}

// BuildCheckInstance 组装 check 请求的实例项（镜像推送侧字段口径，一处生成杜绝漂移）：
//   - InstanceID / UpdateTime ＝ 推实例时同一来源（t_instance.instance_code / update_time）；
//   - tasks[] **只含 RELEASED 任务**（与 BuildSnapshot 同口径：HELD 任务未推给平台，
//     平台侧不存在，报上去只会制造伪差异）；update_time ＝ feishuMilli(t.UpdatedAt)，
//     与推送快照的 task_list[*].update_time 同值同形。
func BuildCheckInstance(inst *store.Instance, tasks []store.FlowTask) ExternalCheckInstance {
	out := ExternalCheckInstance{
		InstanceID: inst.InstanceCode,
		// ★ 与推送侧（UpsertExternalInstance body）同值同形：int64 版本值转字符串。
		UpdateTime: strconv.FormatInt(inst.UpdateTime, 10),
		Tasks:      []ExternalCheckTask{},
	}
	for _, t := range tasks {
		if t.ReleaseState != "RELEASED" {
			continue // ★ 与推送快照同口径：未释放任务未推给平台，不参与比对
		}
		out.Tasks = append(out.Tasks, ExternalCheckTask{
			TaskID:     t.TaskID,
			UpdateTime: feishuMilli(t.UpdatedAt),
		})
	}
	return out
}

// ExtCheckClient 出方向对账端口：按 approval_code 取回需要关注的**差异实例**。
type ExtCheckClient interface {
	// CheckExternalInstances 提交我方该 approval_code 下的实例快照（instances[]），
	// 取回平台观测到的差异实例。
	CheckExternalInstances(ctx context.Context, approvalCode string, instances []ExternalCheckInstance) ([]ExternalInstanceState, error)
}

// CheckExternalInstances 调 external_instances/check 取差异实例。
//
// ★ instances 为空 ⇒ **不发请求**、直接返回空差异（官方要求 instances 必填，空数组
// 同样会 99992402；无本地实例＝无可比对项，属正常态而非错误）。
func (c *HTTPClient) CheckExternalInstances(ctx context.Context, approvalCode string, instances []ExternalCheckInstance) ([]ExternalInstanceState, error) {
	if strings.TrimSpace(approvalCode) == "" {
		return nil, fmt.Errorf("feishu: 对账检查失败: approval_code 为空")
	}
	if len(instances) == 0 {
		return []ExternalInstanceState{}, nil // 无本地实例可比对：不发空请求（instances 必填）
	}
	// ★ 实测定稿请求体（2026-09-27/28）：{"approval_code":…,"instances":[…]}，
	//   instances[] 每项含 instance_id + update_time + tasks[]（task_id + update_time）。
	body, err := json.Marshal(map[string]any{
		"approval_code": approvalCode,
		"instances":     instances,
	})
	if err != nil {
		return nil, fmt.Errorf("feishu: 组装 external_instances/check 失败: %w", err)
	}
	data, _, err := c.doJSON(ctx, http.MethodPost,
		"/open-apis/approval/v4/external_instances/check", nil, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	var raw struct {
		DiffInstances []struct {
			InstanceID string    `json:"instance_id"`
			UpdateTime flexInt64 `json:"update_time"` // ★ 数值/字符串都接受（见 flexInt64）
			Status     string    `json:"status"`
		} `json:"diff_instances"`
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, fmt.Errorf("feishu: 解析 external_instances/check 失败: %w", err)
		}
	}
	out := make([]ExternalInstanceState, 0, len(raw.DiffInstances))
	for _, d := range raw.DiffInstances {
		if strings.TrimSpace(d.InstanceID) == "" {
			continue
		}
		out = append(out, ExternalInstanceState{
			InstanceID: d.InstanceID, UpdateTime: int64(d.UpdateTime), Status: d.Status,
		})
	}
	return out, nil
}

// 编译期断言：HTTPClient 实现 ExtCheckClient。
var _ ExtCheckClient = (*HTTPClient)(nil)

// ---------- 测试 / 开发替身 ----------

// FakeExtCheckClient 是 ExtCheckClient 的内存替身（按 approval_code 覆盖保存差异实例）。
type FakeExtCheckClient struct {
	mu     sync.Mutex
	states map[string][]ExternalInstanceState
	// LastInstances 最近一次调用收到的 instances 入参（测试断言请求组装用）。
	LastInstances []ExternalCheckInstance
	// Err 非 nil 时，所有调用返回该错误（模拟平台不可用）。
	Err error
}

// NewFakeExtCheckClient 构造内存替身。
func NewFakeExtCheckClient() *FakeExtCheckClient {
	return &FakeExtCheckClient{states: map[string][]ExternalInstanceState{}}
}

// Set 设置某 approval_code 的差异实例（测试/开发用）。
func (f *FakeExtCheckClient) Set(approvalCode string, sts ...ExternalInstanceState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.states == nil {
		f.states = map[string][]ExternalInstanceState{}
	}
	f.states[approvalCode] = sts
}

// CheckExternalInstances 内存读取（记录 instances 入参供断言）。
func (f *FakeExtCheckClient) CheckExternalInstances(_ context.Context, approvalCode string, instances []ExternalCheckInstance) ([]ExternalInstanceState, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	f.mu.Lock()
	f.LastInstances = instances
	f.mu.Unlock()
	return f.states[approvalCode], nil
}
