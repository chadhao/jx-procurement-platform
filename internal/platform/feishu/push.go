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
	"strconv"
	"strings"
	"sync"
	"time"

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

// ExternalInstanceLink external_instances 的 links 对象——**实例级**与
// **task_list[*] 级**同构（2026-09-28 实测成功 body 形态：`{"pc_link":…,"mobile_link":…}`）。
// ★★ 两处必须由同一函数（externalLinks）生成，避免两处漂移。
type ExternalInstanceLink struct {
	PCLink     string `json:"pc_link"`
	MobileLink string `json:"mobile_link"`
}

// ExternalI18nText i18n_resources[].texts[] 单项——★ **数组形态** `[{"key":…,"value":…}]`。
//
// ★★ 教训（docs/reference/README.md 实测台账，勿删）：同一平台不同接口的 texts 形态
// **不一致**——`external_approvals` / `external_instances` 要求数组（传 map ⇒
// `9499 Invalid parameter type in json: texts`）；而 `approval/v1/message/send` 接受 map
// （官方示例即 map）。⇒ **不能假设"同一平台同一字段形态一致"**，逐接口按官方示例＋实测校准。
type ExternalI18nText struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// ExternalI18nResource i18n_resources[] 单项（实例级**必填**，2026-09-28 实测 99992402）。
//
// ★ locale 用官方枚举 `zh-CN`（实测飞书**不校验**枚举——传 `zh_cn` 被原样接受并读回
// ⇒ 静默缺陷，按语言匹配文案时取不到值，须自查）；is_default 须显式 true。
type ExternalI18nResource struct {
	Locale    string             `json:"locale"`
	IsDefault bool               `json:"is_default"`
	Texts     []ExternalI18nText `json:"texts"`
}

// I18nKeyInstanceTitle 实例展示名的国际化占位 key（官方要求 Key 以 `@i18n@` 开头，
// 值在 i18n_resources.texts 中按 Key:Value 赋值）。
const I18nKeyInstanceTitle = "@i18n@instance_title"

// ExternalTask 快照中的单个任务（仅含已 RELEASED 的）。
//
// ★★ 教训（2026-09-28 联调实测 99992402，勿删）：`task_list[*]` 的 `links` /
// `create_time` / `end_time` / `update_time` 是**必填**——缺任一即整单被拒：
//
//	{"code":99992402,"msg":"field validation failed",
//	 "error":{"field_violations":[
//	   {"field":"task_list[*].links","description":"task_list[*].links is required"}, …]}}
//
// 且审批人字段名是 **`open_id`**（官方字段表；与 `user_id` 二选一），**不是**
// `assignee_open_id`——字段名/必填项错会静默失败（未知字段被忽略 ⇒ 任务不被指派）
// 或 99992402（与 docs/reference/README.md 记载的 `assignees` 教训同类）。
// ★ 时间字段格式：**Unix 毫秒时间戳字符串**（如 "1790528336224"）；未完成任务
// `end_time` 传 **"0"**（换算纪律见 feishuMilli / taskEndMillis 注释）。
type ExternalTask struct {
	TaskID   string `json:"task_id"`
	NodeID   string `json:"node_id"`
	NodeName string `json:"node_name"`
	// OpenID 审批人（task_list[*].open_id）。★★ 历史缺陷：曾用 `assignee_open_id`
	//（未知字段被平台静默忽略 ⇒ 任务不被指派）；2026-09-28 实测后改正，语义不变。
	OpenID string `json:"open_id"`
	Status string `json:"status"`
	// CreateTime / EndTime / UpdateTime 任务三时间戳：Unix 毫秒**字符串**（必填，
	// 2026-09-28 实测 99992402；BuildSnapshot 内经 feishuMilli / taskEndMillis 换算，
	// 严禁把 store 侧 ISO8601 文本直接塞入）。
	CreateTime string `json:"create_time"`
	EndTime    string `json:"end_time"`
	UpdateTime string `json:"update_time"`
	// Links 任务详情链接（必填；与实例级 links 同构、同一函数 externalLinks 生成）。
	Links ExternalInstanceLink `json:"links"`
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
//
// ★★ 教训（2026-09-28 联调实测 99992402，勿删）：**飞书报文分「实例级（顶层）」与
// 「task_list[*] 级」两层，各有独立必填项；修完一层下一层才会暴露——必须按官方字段表
// 逐层核对**，不得以一次 `{"code":0}` 判通过。第 6 批补的是 task_list[*] 内的
// links / create_time / end_time / update_time；本批（2026-09-28 实测）暴露的是
// **实例级** start_time / end_time / i18n_resources。
//
// 实例级必填（官方《同步三方审批实例》字段表，2026-09-28 逐字段核对）：
// `approval_code` / `status` / `instance_id` / `links` / `start_time` / `end_time` /
// `update_time` / `i18n_resources`；另有**条件必填**：发起人 `open_id` / `user_id`
// **至少传一个**（字段表两项各标"否"，但注意事项明确二选一必传）。
type InstanceSnapshot struct {
	ApprovalCode string `json:"approval_code"`
	InstanceID   string `json:"instance_id"`
	UpdateTime   int64  `json:"update_time"`
	Status       string `json:"status"`
	// StartTime / EndTime 实例起止时刻：Unix 毫秒**字符串**（官方字段表标 string；
	// BuildSnapshot 内经 feishuMilli / instanceEndMillis 换算，严禁 ISO8601 直塞）。
	// EndTime 未终态传 "0"（官方：未结束的审批为 0）。
	StartTime string `json:"start_time"`
	EndTime   string `json:"end_time"`
	// OpenID 审批发起人（实例级）。官方 open_id / user_id 二选一必传；我方取
	// t_instance.applicant_open_id（缺失 ⇒ BuildSnapshot 记 warn，不编造）。
	OpenID   string               `json:"open_id,omitempty"`
	Links    ExternalInstanceLink `json:"links"`
	TaskList []ExternalTask       `json:"task_list"`
	CCList   []string             `json:"cc_list,omitempty"`
	// I18nResources 国际化文案（实例级必填；**数组形态**——与 message/send 的 map
	// 形态相反，见 ExternalI18nText 教训注释）。
	I18nResources []ExternalI18nResource `json:"i18n_resources"`
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

// externalLinks 统一生成**实例级**与 **task_list[*] 级** links（2026-09-28 实测两级均必填）。
// ★★ 唯一生成函数：两处调用同一实现，杜绝「实例级与任务级链接来源漂移」。
// 指向详情页 `<detailBase>/approval/<biz_no>`（与 NotifySender 的「查看详情」同一路由，
// web/src/router.js 的 approval-console）。
func externalLinks(detailBase, bizNo string) ExternalInstanceLink {
	detail := strings.TrimRight(strings.TrimSpace(detailBase), "/") + "/approval/" + bizNo
	return ExternalInstanceLink{PCLink: detail, MobileLink: detail}
}

// feishuMilli 把时刻转成飞书要求的 **Unix 毫秒时间戳字符串**（如 "1790528336224"）。
//
// ★ 换算纪律（2026-09-28 实测）：store 侧 `created_at`/`updated_at`/`closed_at` 以
// RFC3339 文本落库（Go 侧 time.Time），而 `t_instance.update_time` 是 int64 毫秒——
// **严禁把 ISO8601 文本直接塞进飞书时间字段**，必须经本函数换算。
// 零值时刻按 "0" 处理（与「未完成」同形态，不编造时间）。
func feishuMilli(t time.Time) string {
	if t.IsZero() {
		return "0"
	}
	return strconv.FormatInt(t.UnixMilli(), 10)
}

// taskEndMillis 任务级 end_time（2026-09-28 实测口径）：
//   - 终态（APPROVED / REJECTED）＝ `closed_at` 毫秒字符串；`closed_at` 为空 ⇒
//     **退化取 `updated_at` 并记 warn**（终态任务应有关闭时刻，缺失属数据异常；
//     不能推 "0"，否则飞书侧终态时间显示为 0）；
//   - 非终态 ＝ **"0"**（官方要求未完成任务传 "0"）。
func taskEndMillis(t store.FlowTask, log *slog.Logger) string {
	if t.Status != "APPROVED" && t.Status != "REJECTED" {
		return "0"
	}
	if t.ClosedAt != nil && !t.ClosedAt.IsZero() {
		return feishuMilli(*t.ClosedAt)
	}
	if log != nil {
		log.Warn("★ 终态任务缺 closed_at：end_time 退化取 updated_at（数据异常，应回查任务关闭链路）",
			"biz_no", t.BizNo, "task_id", t.TaskID, "status", t.Status)
	}
	return feishuMilli(t.UpdatedAt)
}

// instanceTerminal 实例是否终态（官方 status 枚举中"流程已结束"的取值；
// 字面量与官方枚举一致，不反向依赖领域包）。
func instanceTerminal(status string) bool {
	switch status {
	case "APPROVED", "REJECTED", "CANCELED", "DELETED":
		return true
	default:
		return false
	}
}

// instanceEndMillis 实例级 end_time（官方：必填；**未结束为 0**，Unix 毫秒字符串）。
//
//   - 非终态 ＝ **"0"**；
//   - 终态：CANCELED 优先取 `cancel_at`（撤回时刻，列级最准；t_instance 现有列，
//     migration 0007），缺失 ⇒ 退化取 `updated_at` 并记 warn；其余终态取 `updated_at`
//     （实例最后一次流转时刻）；两者皆缺 ⇒ **记 warn 并退 "0"**（★ 不编造时间；
//     终态时间显示为 0 属数据异常，应回查流转链路）。
func instanceEndMillis(inst *store.Instance, log *slog.Logger) string {
	if !instanceTerminal(inst.Status) {
		return "0"
	}
	t := inst.UpdatedAt
	if inst.Status == "CANCELED" {
		if inst.CancelAt != nil && !inst.CancelAt.IsZero() {
			t = *inst.CancelAt
		} else if log != nil {
			log.Warn("★ 撤回实例缺 cancel_at：end_time 退化取 updated_at（数据异常，应回查撤回链路）",
				"biz_no", inst.BizNo, "status", inst.Status)
		}
	}
	if t.IsZero() {
		if log != nil {
			log.Warn("★ 终态实例无任何结束时刻：end_time 退 \"0\"（不编造时间；数据异常）",
				"biz_no", inst.BizNo, "status", inst.Status)
		}
		return "0"
	}
	return feishuMilli(t)
}

// instanceI18nResources 实例级 i18n_resources（**必填**、**数组形态**）。
//
// 展示名优先取审批定义名（t_approval_def.name，由 Pusher.Push 经 GetApprovalDef
// 读出后传入）；定义缺失 ⇒ **明确降级**为 DocType → BizNo 并记 warn——
// **绝不静默发空**（必填字段发空数组即便被平台容忍，实例名也无从展示）。
func instanceI18nResources(defName, docType, bizNo string, log *slog.Logger) []ExternalI18nResource {
	name := strings.TrimSpace(defName)
	if name == "" {
		name = strings.TrimSpace(docType)
		if name == "" {
			name = bizNo
		}
		if log != nil {
			log.Warn("★ 审批定义缺失：i18n 文案降级取 DocType/BizNo（不静默发空；应回查定义注册链路）",
				"biz_no", bizNo, "doc_type", docType)
		}
	}
	return []ExternalI18nResource{{
		Locale:    "zh-CN",
		IsDefault: true,
		Texts:     []ExternalI18nText{{Key: I18nKeyInstanceTitle, Value: name}},
	}}
}

// BuildSnapshot 组装快照：**只含已 RELEASED 的 task**；未释放整体省略。超限返回错误（不截断）。
//
// ★ detailBase（JX_CALLBACK_DOMAIN）：实例级与 task_list[*] 级 links 的**唯一来源**
// （externalLinks 统一生成）。为空 ⇒ **可见失败**（links 两级均为必填，2026-09-28
// 实测 99992402；绝不静默编造 URL——与 NotifySender 同纪律）。
// ★ defName（t_approval_def.name，调用方经 GetApprovalDef 读出）：实例级
// i18n_resources 展示名的第一来源；为空 ⇒ 降级 DocType → BizNo 并记 warn
// （instanceI18nResources；不静默发空）。
// ★ log 仅用于数据异常的退化 warn（终态任务缺 closed_at / 终态实例缺结束时刻 /
// 实例缺 created_at / 缺 applicant_open_id / 定义缺失降级）；nil 时跳过 warn。
func BuildSnapshot(inst *store.Instance, tasks []store.FlowTask, ccList []string, detailBase string, defName string, log *slog.Logger) (InstanceSnapshot, error) {
	if inst == nil {
		return InstanceSnapshot{}, fmt.Errorf("feishu: 组装快照失败: 实例为空")
	}
	if strings.TrimSpace(detailBase) == "" {
		return InstanceSnapshot{}, fmt.Errorf("feishu: 组装快照失败: detailBase 为空，无法构造 links" +
			"（实例级与 task_list[*].links 均为必填，2026-09-28 实测 99992402；JX_CALLBACK_DOMAIN 未配置？）")
	}
	snap := InstanceSnapshot{
		ApprovalCode: inst.ApprovalCode,
		InstanceID:   inst.InstanceCode,
		UpdateTime:   inst.UpdateTime,
		Status:       inst.Status,
		// ★ 实例级 start_time / end_time（官方必填，2026-09-28 实测 99992402）：
		//   毫秒字符串换算，纪律同 task 三时间戳（feishuMilli / instanceEndMillis）。
		StartTime: feishuMilli(inst.CreatedAt),
		EndTime:   instanceEndMillis(inst, log),
		// ★ 实例级发起人 open_id（官方 open_id/user_id 二选一必传）。
		OpenID:        inst.ApplicantOpenID,
		Links:         externalLinks(detailBase, inst.BizNo),
		CCList:        ccList,
		I18nResources: instanceI18nResources(defName, inst.DocType, inst.BizNo, log),
	}
	if log != nil {
		if inst.CreatedAt.IsZero() {
			log.Warn("★ 实例缺 created_at：start_time 退 \"0\"（不编造时间；数据异常，应回查提交链路）",
				"biz_no", inst.BizNo, "status", inst.Status)
		}
		if strings.TrimSpace(inst.ApplicantOpenID) == "" {
			log.Warn("★ 实例缺 applicant_open_id：实例级发起人 open_id/user_id 官方二选一必传，" +
				"两者皆空可能被 99992402 拒绝（应回查提交链路的发起人落库）")
		}
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
			OpenID:        t.AssigneeOpenID, // ★ json tag 是 open_id（2026-09-28 实测，见 ExternalTask 注释）
			Status:        t.Status,
			CreateTime:    feishuMilli(t.CreatedAt),
			EndTime:       taskEndMillis(t, log),
			UpdateTime:    feishuMilli(t.UpdatedAt),
			Links:         externalLinks(detailBase, inst.BizNo), // ★ 与实例级同源（inst.BizNo 权威）
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
	// detailBase 详情页基址（JX_CALLBACK_DOMAIN）：links ＝ `<detailBase>/approval/<biz_no>`
	//（externalLinks 统一生成，实例级与 task_list[*] 级同源）。为空 ⇒ BuildSnapshot
	// **可见失败**（links 必填；不编造 URL——与 NotifySender 同纪律）。
	detailBase string
	log        *slog.Logger
}

// NewPusher 构造推送服务。
func NewPusher(db *store.DB, client PushClient, detailBase string, log *slog.Logger) *Pusher {
	if log == nil {
		log = observ.NewLogger("info", nil)
	}
	return &Pusher{
		db:         db,
		client:     client,
		detailBase: strings.TrimRight(strings.TrimSpace(detailBase), "/"),
		log:        log,
	}
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
	// ★★ 双 code 池消歧（docs/16 G-8 / §2-C）＋ i18n 文案源（官方字段表必填项）：
	//   定义读取**前移到 BuildSnapshot 之前**，一处读取双用——
	//   ① 推实例的 approval_code **优先取 t_approval_def.feishu_code**（Registry.Register
	//     落库的 POST 响应回填值）、为空回退 inst.ApprovalCode（我方自定义 code）；
	//   ② 实例级 i18n_resources 展示名取 t_approval_def.name。
	//   ★ 双池归属未实测（docs/16 §7 V-4）⇒ **双写、不猜**；定义缺失时静默回退自定义
	//   code、i18n 由 BuildSnapshot 明确降级（GetApprovalDef 的 ErrNotFound 属正常态，
	//   其余错误仅告警）。
	code := inst.ApprovalCode
	defName := ""
	def, derr := p.db.GetApprovalDef(ctx, inst.ApprovalCode)
	switch {
	case derr == nil:
		if fc := strings.TrimSpace(def.FeishuCode); fc != "" {
			code = fc
		}
		defName = def.Name
	case errors.Is(derr, store.ErrNotFound):
		// 正常态：定义未注册（docs/reference 实测 t_approval_def 可为 0 行）。
	default:
		p.log.Warn("推送前读取审批定义失败（回退自定义 approval_code 推送）",
			"biz_no", bizNo, "approval_code", inst.ApprovalCode, "error", derr.Error())
	}
	snap, err := BuildSnapshot(inst, tasks, nil, p.detailBase, defName, p.log)
	if err != nil {
		// 超限等：告警且**不写流水**（这是"请求有误"，非"平台不支持"；S12）。
		p.log.Error("推送组装失败（不静默截断）", "biz_no", bizNo, "error", err.Error())
		return PushResult{}, err
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
//
// ★ 2026-09-28 按官方字段表补齐实例级必填（真机 99992402：start_time / end_time /
// i18n_resources 缺失）：与 task_list[*] 级必填项**两层独立**，逐层核对（见
// InstanceSnapshot 注释）。
// ★ `update_time` 官方字段表标 **string**（Unix 毫秒/版本控制用递增值）：由
// int64 版本值转字符串下发；对账侧（external_check.go）同值同形，防"同值误判差异"。
// ★ open_id 条件必填（open_id/user_id 二选一）；为空则省略（BuildSnapshot 已 warn）。
func (c *HTTPClient) UpsertExternalInstance(ctx context.Context, updateMode string, snap InstanceSnapshot) error {
	body := map[string]any{
		"approval_code":  snap.ApprovalCode,
		"instance_id":    snap.InstanceID,
		"update_time":    strconv.FormatInt(snap.UpdateTime, 10),
		"update_mode":    updateMode,
		"status":         snap.Status,
		"start_time":     snap.StartTime,
		"end_time":       snap.EndTime,
		"links":          snap.Links,
		"task_list":      snap.TaskList,
		"i18n_resources": snap.I18nResources,
	}
	if strings.TrimSpace(snap.OpenID) != "" {
		body["open_id"] = snap.OpenID
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
