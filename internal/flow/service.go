// Package flow 是我方审批核心的领域服务（架构转向 ③，04a §2 / §11）。
//
// 职责：提交、节点聚合判定、推进；状态机为**唯一真相源**。
//
// ★ 会签聚合在**我方**（04a §2.3）：节点是否通过，按 `node_id` 聚合该节点**全部任务**的
//
//	状态判定，**不依赖飞书 `task_list[].type`**。`type` 只影响飞书侧展示形态。
//
// ★ 不可能状态防御（本仓库头号红线＝静默）：
//   - 终态（APPROVED/REJECTED/CANCELED）**不得回退**、不得再推进（重复回调为幂等 no-op）；
//   - CANCELED 后不得推进；
//   - 未到达的节点**不得提前审批**（防"未来节点先批"）。
package flow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/config"
	"github.com/chadhao/jx-procurement-platform/internal/number"
	"github.com/chadhao/jx-procurement-platform/internal/observ"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// 实例状态（04a §2.1）。
const (
	InstancePending  = "PENDING"
	InstanceApproved = "APPROVED"
	InstanceRejected = "REJECTED"
	InstanceCanceled = "CANCELED"
)

// 任务状态（04a §2.2）。
const (
	TaskPending     = "PENDING"
	TaskApproved    = "APPROVED"
	TaskRejected    = "REJECTED"
	TaskTransferred = "TRANSFERRED"
	TaskDone        = "DONE"
)

// 操作类型（04a §1.1）。
const (
	OpSubmit  = "SUBMIT"
	OpApprove = "APPROVE"
	OpReject  = "REJECT"
	OpCancel  = "CANCEL"
)

// 任务释放状态（顺序会签 · 分段释放，04a §2.3 / 01a §4.3）。
const (
	// ReleaseHeld 未释放：飞书侧不推、不生成待办；「还没轮到」→ 不得办理。
	ReleaseHeld = "HELD"
	// ReleaseReleased 已释放：当前可办理。
	ReleaseReleased = "RELEASED"
)

// 领域错误（均为**可见失败**，不静默吞掉）。
var (
	// ErrInvalidSubmit 提交入参非法。
	ErrInvalidSubmit = errors.New("flow: 提交入参非法")
	// ErrIllegalTransition 非法状态迁移（终态回退 / 已终结任务再操作等）。
	ErrIllegalTransition = errors.New("flow: 非法状态迁移")
	// ErrNodeNotReached 操作了尚未到达的节点。
	ErrNodeNotReached = errors.New("flow: 节点尚未到达")
	// ErrTaskHeld 操作了尚未释放（HELD）的任务 —— 顺序会签下「还没轮到」。
	// 非静默：明确拒绝，绝不把「未轮到的办理」算作成功。
	ErrTaskHeld = errors.New("flow: 任务尚未释放（顺序会签未轮到）")
	// ErrDefinitionMissing 提交前校验：该 approval_code 在 t_approval_def 中**未注册**（04a §10 S7）。
	// ★ 必须**可见地失败**：定义缺失时若放行提交，结果是「提交看起来成功，但审批中心看不到数据」
	//   ——与 S1/S7 同类不可见故障、无任何异常（docs/11 R10）。
	ErrDefinitionMissing = errors.New("flow: 三方审批定义未注册")
	// ErrIdemReplay 同幂等键 + 同载荷：返回值携带**首次** biz_no，调用方 200 复用（d9）。
	ErrIdemReplay = errors.New("flow: 幂等重放（同键同载荷，返回首次结果）")
	// ErrIdemConflict 同幂等键 + 异载荷：调用方应 40900。
	ErrIdemConflict = errors.New("flow: 幂等键冲突（同键异载荷）")
)

// Approver 审批人。
type Approver struct {
	OpenID string
	Name   string
}

// NodeSpec 节点定义（提交时给出）。
type NodeSpec struct {
	NodeID    string
	NodeName  string
	Seq       int // 节点顺序（升序推进）
	Approvers []Approver
}

// AttachmentRef 附件引用（我方页面提交时携带；**只登记元数据，零网络 IO**，B39）。
type AttachmentRef struct {
	FileID   string
	FieldID  string
	FileName string
	Size     *int64
}

// SubmitInput 提交入参（我方页面校验通过后调用）。
type SubmitInput struct {
	DocType      string
	ApprovalCode string
	// PrevBizNo 重新发起时指向旧单号（撤回/驳回重提的因果链，04a §5.4）；首次提交留空。
	PrevBizNo       string
	ApplicantOpenID string
	ApplicantName   string
	Department      string
	AmountCents     *int64
	PurposeClassL1  string
	PurposeClassL2  string
	Supplier        string
	// BizFields 已映射的表单字段（键＝规范 `biz_field` 名，如 `contract_no`/`related_biz_no`/
	//   `amount_cents`/`supplier`/`purpose_class_l1`/`purpose_class_l2`/`department`/`biz_date`）。
	//   Submit 分流（决策 #18/#19）：**规范字段 → t_instance 规范列**；**非规范字段 → t_instance.ext_json**。
	//   ★ 不再写 `t_instance_field`（③ 下该表已弃用，见 docs/11 R03）。
	BizFields map[string]any
	// Attachments 附件引用（元数据在此登记；文件本体按需拉取，绝不在此触发网络 IO）。
	Attachments []AttachmentRef
	Nodes       []NodeSpec
	At          time.Time // 业务时刻（零值取 now），YYMM 由它决定
	// IdemKey / IdemPayloadHash 提交幂等（M4 d9，照抄 submission 模式）：
	//   两者皆空 ⇒ 不启用。占用冲突由 Submit 返回 ErrIdemReplay/ErrIdemConflict。
	IdemKey         string
	IdemPayloadHash string
	// OrgVerify 提交时实时回源标记（FR-M9-17 / M5）：
	//   服务端生成（客户端字段无法伪造 —— 合并在 applyBizFields **之后**，服务端值必胜），
	//   落 ext_json.org_verify；回源失败＝告警放行，标记 ok:false。
	OrgVerify map[string]any
	// StagingIDs 提交前上传的暂存附件 id（M6 / D4）：提交事务内绑定迁入 t_attachment；
	// 任一不可绑定（非本人/已绑定/过期）⇒ 整体回滚（绝不静默丢附件）。
	StagingIDs []string
}

// Service 审批领域服务。
type Service struct {
	db       *store.DB
	gen      *number.Generator
	appID    string
	maps     *config.Maps // 台账映射（finalize 落账用；可为 nil＝不落台账）
	log      *slog.Logger
	subs     []Subscriber // 流程事件订阅者（通知 / 审计 / 推送 / 台账）
	advancer Advancer     // 回调异步推进端口（见 callback.go；nil＝仅落 op_log 不入队）
}

// New 构造审批服务。appID 用于生成 `instance_id = {app_id}:{biz_no}`（04a §3.3 / §6.3）。
// 未注入配置映射 → finalize 不落台账（不虚构口径）。
func New(db *store.DB, appID string) *Service {
	return NewWithConfig(db, appID, nil, nil)
}

// NewWithConfig 构造审批服务并注入台账映射与日志（finalize 落账需要 maps）。
func NewWithConfig(db *store.DB, appID string, maps *config.Maps, log *slog.Logger) *Service {
	if log == nil {
		log = observ.NewLogger("info", nil)
	}
	return &Service{db: db, gen: number.New(db), appID: appID, maps: maps, log: log}
}

// instanceCode 由单号派生我方实例标识（= 推给飞书的 instance_id）。
//
// ★ 加 `{app_id}:` 前缀（04a §3.3）：裸单号在多环境/多应用共用同一应用时会**静默撞 ID**
//
//	（撞 ID = 审批中心看不到数据、无任何异常）。appID 为空时退化为裸单号（仅开发/测试）。
func (s *Service) instanceCode(bizNo string) string {
	if strings.TrimSpace(s.appID) == "" {
		return bizNo
	}
	return s.appID + ":" + bizNo
}

// Submit 提交：同事务内生成单号 + 建实例（PENDING）+ 建首节点任务 + 留痕。
// 返回业务单号。失败即回滚，不产生半成品实例。
//
// ★★ 落库纪律（docs/16 §2-D 纪律一）：本方法**同事务**写 `t_instance` + `t_flow_task` +
//
//	状态史 + op_log；事务提交后经 `flow.emit` 分发订阅者（含 `flowPushSubscriber` →
//	`feishu.Pusher.Push`）才推飞书 ⇒ **本地行天然先于推送存在，回调可命中**。
//	推实例的唯一入口是 `Pusher.Push`，其数据源正是本方法落下的本地行——
//	**禁止任何「只推飞书、不落本地」的生产路径**（手工 curl 直推 external_instances
//	仅限联调排障，且该实例本地不可回调）。
func (s *Service) Submit(ctx context.Context, in SubmitInput) (string, error) {
	if err := validateSubmit(in); err != nil {
		return "", err
	}
	// ★ S7（04a §10）/ R10：**提交前校验审批定义存在**。
	//   定义缺失必须「可见地失败」（明确错误码），**绝不静默**：否则提交看似成功、
	//   而飞书审批中心因无对应定义**看不到任何数据**（S1/S7 同类不可见故障）。
	//   校验在事务之前 → 定义缺失时不产生任何半成品（无实例、无任务）。
	if _, err := s.db.GetApprovalDef(ctx, in.ApprovalCode); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "", fmt.Errorf("%w: approval_code=%q（请先在 t_approval_def 注册定义，04a S7）",
				ErrDefinitionMissing, in.ApprovalCode)
		}
		return "", fmt.Errorf("flow: 提交前校验审批定义失败: %w", err)
	}
	at := in.At
	if at.IsZero() {
		at = time.Now()
	}

	var (
		bizNo  string
		events []FlowEvent
	)
	err := s.db.WithTx(ctx, func(tx *sql.Tx) error {
		// ★ 幂等占位（d9）：必须**先占位、后建单**（语义详见 store.InsertApprovalIdemTx）；
		//   冲突 ⇒ 让路中止本事务（未写任何业务数据），事务外回读胜出者。
		if strings.TrimSpace(in.IdemKey) != "" {
			if err := s.db.InsertApprovalIdemTx(ctx, tx, in.IdemKey, in.ApplicantOpenID, in.IdemPayloadHash); err != nil {
				return err // store.ErrApprovalIdemTaken 或包装错误
			}
		}
		var err error
		bizNo, err = s.gen.AllocTx(ctx, tx, in.DocType, at)
		if err != nil {
			return err
		}
		inst := &store.Instance{
			InstanceCode:    s.instanceCode(bizNo),
			ApprovalCode:    in.ApprovalCode,
			DocType:         in.DocType,
			BizNo:           bizNo,
			PrevBizNo:       in.PrevBizNo,
			Status:          InstancePending,
			ApplicantOpenID: in.ApplicantOpenID,
			ApplicantName:   in.ApplicantName,
			Department:      in.Department,
			AmountCents:     in.AmountCents,
			PurposeClassL1:  in.PurposeClassL1,
			PurposeClassL2:  in.PurposeClassL2,
			Supplier:        in.Supplier,
			Source:          "flow",
			UpdateTime:      1, // 首次推送版本从 1 起（04a §3.1 必须严格递增）
			CreatedAt:       at,
			UpdatedAt:       at,
		}
		// ★ 表单字段分流（决策 #18/#19）：规范字段 → 规范列；非规范字段 → ext_json。
		extJSON, err := applyBizFields(inst, in.BizFields)
		if err != nil {
			return err
		}
		// ★ 回源标记服务端合并（M5）：置于 applyBizFields 之后 ⇒ 客户端伪造的
		//   ext_json.org_verify 一律被服务端权威值覆盖。
		if len(in.OrgVerify) > 0 {
			ext := map[string]any{}
			if extJSON != "" && extJSON != "{}" {
				if uErr := json.Unmarshal([]byte(extJSON), &ext); uErr != nil {
					// 旧 ext_json 非法：以空表重建（org_verify 仍须落，不静默丢标记）。
					ext = map[string]any{}
				}
			}
			ext["org_verify"] = in.OrgVerify
			if b, mErr := json.Marshal(ext); mErr == nil {
				extJSON = string(b)
			}
		}
		inst.ExtJSON = extJSON
		// ★ 单笔金额必须 > 0（决策 #39 / FR-M9-03）：0/负金额**拒绝提交**（M4 起由
		//   原「Warn + 置空」收紧为可见失败 —— 置空会让 0 元单既被接受又以 NULL 统计）。
		if inst.AmountCents != nil && *inst.AmountCents <= 0 {
			return fmt.Errorf("%w: 单笔金额必须 > 0，实为 %d 分", ErrInvalidSubmit, *inst.AmountCents)
		}
		// ★ P3 兜底：biz_no 唯一索引拦截「试图复用终态号」——命中即失败，绝不静默分配同号。
		if err := s.db.UpsertInstanceTx(ctx, tx, inst); err != nil {
			return fmt.Errorf("flow: 提交写实例失败（单号唯一兜底）: %w", err)
		}
		if err := s.createTasksTx(ctx, tx, bizNo, in.Nodes, at); err != nil {
			return err
		}
		// ★ 附件元数据登记（B39）：只登记、零网络 IO（文件本体按需拉取）。
		for _, ref := range in.Attachments {
			if strings.TrimSpace(ref.FileID) == "" {
				continue // 无 file_id 的引用无意义，跳过（不静默吞错：空引用本就不该提交）
			}
			if err := s.db.UpsertAttachmentTx(ctx, tx, &store.Attachment{
				FileID:       ref.FileID,
				InstanceCode: inst.InstanceCode,
				BizNo:        bizNo,
				FieldID:      ref.FieldID,
				FileName:     ref.FileName,
				SizeBytes:    ref.Size,
			}); err != nil {
				return err
			}
		}
		// ★ 暂存附件绑定（M6/D4）：owner/未绑定/未过期三项 guard 在 SQL 内，
		//   0 行 ⇒ 不可绑定 ⇒ 整体回滚（附件不静默丢失，提交可见失败）。
		for _, sid := range in.StagingIDs {
			if strings.TrimSpace(sid) == "" {
				continue
			}
			if _, err := s.db.BindStagingTx(ctx, tx, sid, in.ApplicantOpenID, inst.InstanceCode, bizNo); err != nil {
				return err
			}
		}
		// ★ 状态史（提交）：状态迁移点都要留痕（docs/11 R02），非仅终态。
		if _, _, err := s.db.AppendStatusHistory(ctx, tx, &store.StatusHistory{
			InstanceCode:   inst.InstanceCode,
			Status:         InstancePending,
			OperatorOpenID: in.ApplicantOpenID,
			Opinion:        "提交",
			OccurredAt:     at,
		}); err != nil {
			return err
		}
		_, err = s.db.InsertFlowOpLogTx(ctx, tx, &store.FlowOpLog{
			BizNo: bizNo, OpType: OpSubmit, ActorOpenID: in.ApplicantOpenID,
			ToStatus: InstancePending, Reason: in.PrevBizNo, CreatedAt: at,
		})
		if err != nil {
			return err
		}
		events = append(events, FlowEvent{
			Type: EventSubmitted, BizNo: bizNo, InstanceCode: inst.InstanceCode, DocType: in.DocType,
			ActorOpenID: in.ApplicantOpenID, Reason: in.PrevBizNo, At: at,
		})
		// ★ 幂等簿记回填（同事务）：建单成功才落 biz_no；事务失败整体回滚 ⇒ 键不被毒化。
		if strings.TrimSpace(in.IdemKey) != "" {
			if err := s.db.BackfillApprovalIdemTx(ctx, tx, in.IdemKey, in.ApplicantOpenID, bizNo, in.IdemPayloadHash); err != nil {
				return err
			}
		}
		return nil
	})
	if errors.Is(err, store.ErrApprovalIdemTaken) {
		// 让路：回读胜出者 —— 同载荷 ⇒ 返回首次 biz_no + ErrIdemReplay；异载荷 ⇒ 冲突。
		rec, found, ferr := s.db.FindApprovalIdem(ctx, in.IdemKey, in.ApplicantOpenID)
		if ferr != nil {
			return "", ferr
		}
		if !found {
			return "", fmt.Errorf("flow: 幂等键刚判定冲突却查不到记录（key=%s）", in.IdemKey)
		}
		if rec.PayloadHash != "" && rec.PayloadHash == in.IdemPayloadHash && rec.BizNo != "" {
			return rec.BizNo, ErrIdemReplay
		}
		return "", ErrIdemConflict
	}
	if err != nil {
		return "", err
	}
	s.emit(ctx, events...) // ★ 事务提交后统一分发（04a §2.5）
	return bizNo, nil
}

// Approve 同意某任务（会签：需该节点全员同意才推进，04a §2.3）。
// Approve 同意某任务。fields ＝ 审批时点结构化填报（N-015 指定经办）：
//   - 非 nil（我方页面两键通道，handler 恒传）⇒ supervisor_approval 的 PR 同意**必填**
//     designated_purchaser + designation_basis（缺 ⇒ ErrInvalidDesignation）；
//   - nil（飞书回调 / repair 重放等非结构化通道）⇒ 豁免必填、不写入（见 designation.go）。
func (s *Service) Approve(ctx context.Context, bizNo, taskID, actorOpenID, reason string, fields map[string]any) error {
	return s.act(ctx, bizNo, taskID, actorOpenID, OpApprove, reason, fields)
}

// Reject 拒绝某任务（节点驳回 → 实例驳回；reject 一律不带指定填报 —— N-015 裁定④）。
func (s *Service) Reject(ctx context.Context, bizNo, taskID, actorOpenID, reason string) error {
	return s.act(ctx, bizNo, taskID, actorOpenID, OpReject, reason, nil)
}

// Cancel 撤回（仅发起人；未终结前可撤）→ 实例 CANCELED、在途任务 DONE。
func (s *Service) Cancel(ctx context.Context, bizNo, actorOpenID, reason string) error {
	at := time.Now()
	var events []FlowEvent
	err := s.db.WithTx(ctx, func(tx *sql.Tx) error {
		inst, err := s.db.GetInstanceByBizNoTx(ctx, tx, bizNo)
		if err != nil {
			return err
		}
		// ★ 鉴权**先行**（域层硬校验，`04a §5.1`：撤回＝`applicant=me`）。
		//   ★★ 必须置于**幂等分支之前**：否则非申请人可借「已 `CANCELED` → no-op」**探得**该单
		//   是否已撤回（越权 + 信息泄漏）。被撤回的单据对非申请人**不得**暴露任何成功信号。
		//   ★ 为什么在域层而非仅 HTTP 层：§5.1 明标「服务端硬校验」；且 `flow` 接线后（`R09`）
		//   该路径变为可达 —— 见 README 定案 #58「装配/接线会改变可达性」。
		if inst.ApplicantOpenID != actorOpenID {
			return fmt.Errorf("%w: %s 非实例 %s 的申请人，不可撤回", ErrNotAssignee, actorOpenID, bizNo)
		}
		if inst.Status == InstanceCanceled {
			return nil // 幂等：重复撤回 = no-op（★ 已过鉴权，仅申请人本人可达）
		}
		if isTerminal(inst.Status) {
			return fmt.Errorf("%w: 实例 %s 已终态 %s，不可撤回", ErrIllegalTransition, bizNo, inst.Status)
		}
		// ★ 撤回前先算「应有通知集合」（撤回会把在途置 DONE，事后算不出"曾通过者"）→ 通知已通过者（01a §4.7）。
		preTasks, err := s.db.ListFlowTasksTx(ctx, tx, bizNo)
		if err != nil {
			return err
		}
		notifyTargets := ExpectedNotifyTargets(preTasks, actorOpenID)
		if err := s.terminalizeTx(ctx, tx, inst, InstanceCanceled, at, reason); err != nil {
			return err
		}
		// ★ 状态史（撤回也是一次状态迁移，必须留痕）。
		if _, _, err := s.db.AppendStatusHistory(ctx, tx, &store.StatusHistory{
			InstanceCode:   inst.InstanceCode,
			Status:         InstanceCanceled,
			OperatorOpenID: actorOpenID,
			Opinion:        reason,
			OccurredAt:     at,
		}); err != nil {
			return err
		}
		if _, err := s.db.InsertFlowOpLogTx(ctx, tx, &store.FlowOpLog{
			BizNo: bizNo, OpType: OpCancel, ActorOpenID: actorOpenID,
			FromStatus: InstancePending, ToStatus: InstanceCanceled, Reason: reason, CreatedAt: at,
		}); err != nil {
			return err
		}
		events = append(events, FlowEvent{
			Type: EventCanceled, BizNo: bizNo, InstanceCode: inst.InstanceCode, DocType: inst.DocType,
			ActorOpenID: actorOpenID, Reason: reason, At: at, NotifyTargets: notifyTargets,
		})
		return nil
	})
	if err != nil {
		return err
	}
	s.emit(ctx, events...)
	return nil
}

// act 是同意/拒绝的共用实现。
func (s *Service) act(ctx context.Context, bizNo, taskID, actor, opType, reason string, fields map[string]any) error {
	at := time.Now()
	var events []FlowEvent
	err := s.db.WithTx(ctx, func(tx *sql.Tx) error {
		inst, err := s.db.GetInstanceByBizNoTx(ctx, tx, bizNo)
		if err != nil {
			return err
		}

		task, err := s.db.GetFlowTaskTx(ctx, tx, taskID)
		if err != nil {
			return err
		}
		if task.BizNo != bizNo {
			return fmt.Errorf("%w: 任务 %s 不属于实例 %s", ErrIllegalTransition, taskID, bizNo)
		}

		// ★ 鉴权（域层硬校验）：**同意 / 拒绝必须由该任务的 assignee 本人执行**（`operator == assignee`）。
		//   ★★ 口径依据（用户第二轮口径 ⑥ **逐字**）：
		//      「加签是会签；**代理人可以转交或者退回**，但是需要通知这个审批单内已经审批通过的所有人。」
		//      → **代理人只能"转交 / 退回"，不能代替同意** → 故 `approve`/`reject` 一律要求**本人**；
		//        代理人若想让流程通过，只能走**转交**（把任务交给他人）或**回退** —— **不是代签**。
		//      （`04a §5.1` 原表只列"四操作"、未列"两键"准入 → 本条为准入裁定补入。）
		//   ★ 置于**幂等 / 终态 / 状态**判断**之前**：鉴权＝准入控制，须先于状态解释 —— 否则非本人
		//     可借"已 `APPROVED` → no-op""实例终态 → no-op"的**成功 / 无错差异**，探得任务 / 实例状态。
		//   ★ 重复回调不受影响：去重发生在**回调入口**（`HandleCallback` → 幂等落 `t_flow_op_log`），
		//     重复报文**根本到不了** `act`（见 callback.go）；故本校验**不会**把重复回调误判为越权。
		if task.AssigneeOpenID != actor {
			return fmt.Errorf("%w: %s 非任务 %s 的审批人，不可执行 %s（同意/拒绝须本人）",
				ErrNotAssignee, actor, taskID, opType)
		}

		// ① 幂等：对已处于目标状态的任务重复操作 = no-op（04a §4.3 状态机层第二道防线）。
		if opType == OpApprove && task.Status == TaskApproved {
			return nil
		}
		if opType == OpReject && task.Status == TaskRejected {
			return nil
		}

		// ② 终态防御：实例已终态则不得再推进。
		//    对「同意/拒绝」这类回调，重复到达应为**幂等 no-op**（不二次迁移，不报错给飞书）；
		//    但实例状态本身绝不回退。
		if isTerminal(inst.Status) {
			return nil
		}

		// ③ 任务必须处于可操作态。
		if task.Status != TaskPending {
			return fmt.Errorf("%w: 任务 %s 状态为 %s，不可执行 %s",
				ErrIllegalTransition, taskID, task.Status, opType)
		}

		// ④ 节点到达校验：只有"当前节点"可操作（防未来节点提前审批这一不可能状态）。
		//    ★ 先于释放门禁：**未来节点**的拒绝口径是 `ErrNodeNotReached`（节点未到达），
		//      与"本节点已到达、但还没轮到我"的 `ErrTaskHeld` 区分开，错误语义更精确。
		if err := s.ensureNodeReachedTx(ctx, tx, bizNo, task.NodeSeq); err != nil {
			return err
		}

		// ⑤ 顺序会签释放门禁（04a §2.3）：本节点已到达、但该任务尚未释放（HELD）→「还没轮到我」。
		//    ★ 必须**明确拒绝**（非静默成功）：把「未轮到的同意」当成有效会签票，
		//      会让节点在"后一位尚未收到通知"时就提前通过，直接静默破坏会签语义。
		if task.ReleaseState == ReleaseHeld {
			return fmt.Errorf("%w: 任务 %s（节点 %s）", ErrTaskHeld, taskID, task.NodeID)
		}

		// ⑥ 指定经办填报（N-015 批 2）：同事务落 designated_*；必填仅在结构化通道执行。
		//    位于幂等/终态/状态各早退**之后**、任务推进**之前** ——
		//    填报失败 ⇒ 任务不推进、ext 不落（与幂等键同一事务语义）。
		if opType == OpApprove {
			if err := s.applyDesignationTx(ctx, tx, inst, task, fields, actor, at); err != nil {
				return err
			}
			// ⑦ SS 节点时点字段（N-036 缺口 5/6）：tech_opinion / pgm_final 非空强制。
			if err := s.applySSNodeFieldsTx(ctx, tx, inst, task, fields, actor, at); err != nil {
				return err
			}
		}

		newTaskStatus := TaskApproved
		if opType == OpReject {
			newTaskStatus = TaskRejected
		}
		if err := s.db.UpdateFlowTaskStatusTx(ctx, tx, taskID, newTaskStatus, &at); err != nil {
			return err
		}
		// ★ 幂等键含 round（0011）：回退复用同一 task_id 后再次审批必须换键，
		//   否则 INSERT OR IGNORE 命中上一轮的 APPROVE 留痕 → 静默不推进。
		if _, err := s.db.InsertFlowOpLogTx(ctx, tx, &store.FlowOpLog{
			BizNo: bizNo, NodeID: task.NodeID, TaskID: taskID, OpType: opType,
			ActorOpenID: actor, FromStatus: TaskPending, ToStatus: newTaskStatus,
			Reason: reason, Round: task.Round, CreatedAt: at,
		}); err != nil {
			return err
		}
		if err := s.advanceTx(ctx, tx, inst, at); err != nil {
			return err
		}
		// ★ 状态史（每次迁移都留痕，非仅终态；docs/11 R02）：状态取推进后的实例状态，
		//   节点名用于时间线定位；同 (状态, 人, 意见) 全同则去重、不消耗 event_seq（#37）。
		if _, _, err := s.db.AppendStatusHistory(ctx, tx, &store.StatusHistory{
			InstanceCode:   inst.InstanceCode,
			Status:         inst.Status,
			TaskNode:       task.NodeName,
			OperatorOpenID: actor,
			Opinion:        reason,
			OccurredAt:     at,
		}); err != nil {
			return err
		}
		// ★ 收集流程事件（提交后统一分发，04a §2.5）。
		evType := EventTaskApproved
		if opType == OpReject {
			evType = EventTaskRejected
		}
		events = append(events, FlowEvent{
			Type: evType, BizNo: bizNo, InstanceCode: inst.InstanceCode, DocType: inst.DocType,
			NodeID: task.NodeID, NodeName: task.NodeName, TaskID: taskID, ActorOpenID: actor,
			Reason: reason, At: at,
		})
		switch inst.Status {
		case InstanceApproved:
			events = append(events, FlowEvent{Type: EventInstanceApproved, BizNo: bizNo,
				InstanceCode: inst.InstanceCode, DocType: inst.DocType, ActorOpenID: actor, At: at})
		case InstanceRejected:
			events = append(events, FlowEvent{Type: EventInstanceRejected, BizNo: bizNo,
				InstanceCode: inst.InstanceCode, DocType: inst.DocType, ActorOpenID: actor, At: at})
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.emit(ctx, events...)
	return nil
}

// createTasksTx 建该实例全部节点的任务（PENDING），并按**顺序会签**设置释放状态。
//
// ★ 为什么一次性建全链任务：`t_flow_task` 是唯一持久化"审批链"的地方（04a §1.1 无独立链路表），
//
//	推进时需据此判断"下一节点"与"下一个应释放的人"。为避免后续节点被提前操作，
//	`act` 用 `ensureNodeReachedTx` 做"只有当前节点可操作"的门禁。
//
// ★★ 顺序会签 · 分段释放（04a §2.3，用户定案「上个人审批之后，下个人才能收到通知」）：
//   - **提交时只释放「第一个节点里第一位审批人」这一个任务**（RELEASED）；
//   - **其余任务全部 HELD**（含后续节点）——否则后续节点的审批人会在「尚未轮到」时收到待办/通知，
//     直接违背顺序会签的核心制度直觉。
//   - 「同 node 内逐级、跨 node 顺序」由 advanceTx 的 releaseNextTx 在每次同意后推进。
//
// ★★ 不变量（可测）：**任一时刻，整个实例「可办理」（RELEASED）的任务至多 1 个**
//
//	（除终态；因审批链 node 间串行、node 内顺序会签 → 任一时刻恰有 1 个可办理）。
//	此不变量即「上一位通过后才释放下一位」的形式化表述（team-lead 裁决 A5.1）。
//
// ★ 同节点内次序＝`task_order`（审批人声明序，1-based）——★ 唯一顺序契约，
//
//	禁止任何实现改用 `rowid`/`task_id` 排序（前者随 REPLACE/VACUUM 漂移，后者字典序≠声明序）。
//
// ★ 幂等（快照重推）：`UpsertFlowTaskTx` 冲突即 `DO NOTHING` —— 同 `task_id` 重推
//
//	**绝不**把已 `RELEASED`/`APPROVED`/`REJECTED` 的任务置回 `HELD`（那是"被撞回起点"的静默缺陷）。
func (s *Service) createTasksTx(ctx context.Context, tx *sql.Tx, bizNo string, nodes []NodeSpec, at time.Time) error {
	ordered := make([]NodeSpec, len(nodes))
	copy(ordered, nodes)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Seq < ordered[j].Seq })

	idx := 0
	firstReleased := false
	for _, n := range ordered {
		order := 0 // 同节点内审批人声明序（1-based），★ 唯一释放次序契约（禁止 rowid/task_id）
		for _, ap := range n.Approvers {
			order++
			idx++
			// task_id 确定性生成（04a §3.3）：同快照重推得同一 ID，避免"审批中心看不到数据"。
			// ★ 必须含 biz_no：task_id 是全库 PK，而 node_id/assignee/idx **跨实例会重复**
			//   （每个实例都从 node_id=n1、idx=1 起）→ 若不含 biz_no，第二个实例的同名任务会撞
			//   `ON CONFLICT(task_id) DO NOTHING` 而**静默不建**（该实例审批链为空、永不推进）。
			taskID := fmt.Sprintf("%s-%s-%s-%d-%d", bizNo, n.NodeID, ap.OpenID, 1, idx)
			release := ReleaseHeld
			if !firstReleased {
				release = ReleaseReleased // 全链仅第一个任务在提交时释放
				firstReleased = true
			}
			t := &store.FlowTask{
				TaskID: taskID, BizNo: bizNo, NodeID: n.NodeID, NodeName: n.NodeName,
				NodeSeq: n.Seq, Round: 1, AssigneeOpenID: ap.OpenID, AssigneeName: ap.Name,
				Status: TaskPending, ReleaseState: release, TaskOrder: order,
				CreatedAt: at, UpdatedAt: at,
			}
			if err := s.db.UpsertFlowTaskTx(ctx, tx, t); err != nil {
				return err
			}
		}
	}
	return nil
}

// ensureNodeReachedTx 断言 seq 之前的所有节点均**已通过**，否则该节点尚未到达。
func (s *Service) ensureNodeReachedTx(ctx context.Context, tx *sql.Tx, bizNo string, seq int) error {
	tasks, err := s.db.ListFlowTasksTx(ctx, tx, bizNo)
	if err != nil {
		return err
	}
	for _, prev := range priorSeqs(tasks, seq) {
		if nodeDecision(tasks, prev) != nodeApproved {
			return fmt.Errorf("%w: 上游节点 seq=%d 未通过，节点 seq=%d 未到达", ErrNodeNotReached, prev, seq)
		}
	}
	return nil
}

// advanceTx 在任务状态变更后重算节点聚合与实例状态。
func (s *Service) advanceTx(ctx context.Context, tx *sql.Tx, inst *store.Instance, at time.Time) error {
	tasks, err := s.db.ListFlowTasksTx(ctx, tx, inst.BizNo)
	if err != nil {
		return err
	}

	// ① 任一任务被拒 → 实例驳回（"任一节点驳回"即终局，04a §2.1）。
	for _, t := range tasks {
		if t.Status == TaskRejected {
			return s.terminalizeTx(ctx, tx, inst, InstanceRejected, at, "")
		}
	}

	// ② 逐节点判定（按 seq 升序）：遇到未通过节点即停，不越过（"未推进"即"停在此节点"）。
	allApproved := true
	for _, seq := range distinctSeqs(tasks) {
		if nodeDecision(tasks, seq) == nodeApproved {
			continue
		}
		allApproved = false
		break
	}

	if allApproved {
		return s.terminalizeTx(ctx, tx, inst, InstanceApproved, at, "")
	}
	// ③ 顺序会签推进（04a §2.3）：释放「已到达且尚未通过」节点的下一个 HELD 任务。
	//   放在终态判定之后：一旦实例终态即不再释放（否则会给终态实例的后续任务"续命"）。
	if err := s.releaseNextTx(ctx, tx, inst.BizNo, tasks); err != nil {
		return err
	}
	// 仍在流转：仅推进版本号（每次状态变更都要推一次，04a §3.1 / S2）。
	return s.saveInstanceTx(ctx, tx, inst, at)
}

// releaseNextTx 顺序会签 · 逐级释放（04a §2.3）。
//
// 对每个「已到达且尚未通过」的节点，释放其**下一个应办理**的 HELD 任务：
//   - 「已到达」＝所有 seq 更小的节点均已通过（与 act 的 ensureNodeReachedTx 同一判据）；
//     未到达的节点整体保持 HELD —— 飞书侧不推、不生成待办（满足"下个人才收到通知"）。
//   - 「下一个应办理」＝该节点内按释放顺序**第一个未通过（PENDING）任务**，且其之前的任务全部
//     APPROVED；若之前出现 REJECTED，则不再释放（不越级、不给驳回节点续命）。
//   - 每次推进至多让每个节点多释放一个任务（顺序链一次只前进一步）。
func (s *Service) releaseNextTx(ctx context.Context, tx *sql.Tx, bizNo string, tasks []store.FlowTask) error {
	for _, seq := range distinctSeqs(tasks) {
		if nodeDecision(tasks, seq) != nodePending {
			continue // 已通过 / 已驳回：不再释放
		}
		if !nodeReached(tasks, seq) {
			continue // 节点尚未到达：保持 HELD
		}
		nodeID := nodeIDOfSeq(tasks, seq)
		if nodeID == "" {
			continue
		}
		nodeTasks, err := s.db.ListTasksByNodeTx(ctx, tx, bizNo, nodeID)
		if err != nil {
			return err
		}
		for _, t := range nodeTasks {
			if t.Status == TaskApproved {
				continue // 顺序链已走过该任务
			}
			if t.Status == TaskRejected {
				break // 前面有驳回 → 本节点剩余任务不再释放
			}
			// 该节点内第一个仍 PENDING 的任务：尚未释放则释放之，然后停止（其后再保持 HELD）。
			if t.ReleaseState == ReleaseHeld {
				if err := s.db.SetReleaseStateTx(ctx, tx, t.TaskID, ReleaseReleased); err != nil {
					return err
				}
			}
			break
		}
	}
	return nil
}

// terminalizeTx 把实例置为终态，并被动终结其余在途任务。
func (s *Service) terminalizeTx(ctx context.Context, tx *sql.Tx, inst *store.Instance, status string, at time.Time, reason string) error {
	inst.Status = status
	if status == InstanceCanceled {
		inst.CancelReason = reason
		c := at
		inst.CancelAt = &c
	}
	// 终态：把仍未终结的任务置 DONE（被动终结，04a §2.2）。
	if err := s.closeOpenTasksTx(ctx, tx, inst.BizNo, at); err != nil {
		return err
	}
	if err := s.saveInstanceTx(ctx, tx, inst, at); err != nil {
		return err
	}
	// ★ 终态落账（取代 worker/ingest.go:189 UpsertArchiveTx，04a §T02b）：台账在终态一次性显式写入。
	//   状态史不在此写（已在 Submit/act/Cancel 各迁移点写入）。
	return s.finalizeLedgersTx(ctx, tx, inst, at)
}

// closeOpenTasksTx 将该实例所有 PENDING 任务置 DONE。
func (s *Service) closeOpenTasksTx(ctx context.Context, tx *sql.Tx, bizNo string, at time.Time) error {
	tasks, err := s.db.ListFlowTasksTx(ctx, tx, bizNo)
	if err != nil {
		return err
	}
	for _, t := range tasks {
		if t.Status == TaskPending {
			if err := s.db.UpdateFlowTaskStatusTx(ctx, tx, t.TaskID, TaskDone, &at); err != nil {
				return err
			}
		}
	}
	return nil
}

// saveInstanceTx 持久化实例并令 update_time 单调 +1。
func (s *Service) saveInstanceTx(ctx context.Context, tx *sql.Tx, inst *store.Instance, at time.Time) error {
	inst.UpdatedAt = at
	inst.UpdateTime++
	return s.db.UpsertInstanceTx(ctx, tx, inst)
}

// ---------- 聚合与判定 ----------

// nodeState 节点聚合判定结果。
type nodeState int

const (
	nodePending  nodeState = iota // 未决（尚有 PENDING）
	nodeApproved                  // 通过（无 PENDING、无 REJECTED，且至少一个 APPROVED）
	nodeRejected                  // 驳回（存在任一 REJECTED）
)

// nodeDecision 对某个节点做**我方**聚合判定（会签语义，04a §2.3）。
//
// 规则：节点内任一任务 REJECTED → 驳回；否则当且仅当**无 PENDING** 且**至少一个 APPROVED**
// 时通过（TRANSFERRED/DONE 视为已终结但不提供"通过"）。空节点（无任务）视为未决，不误判通过。
func nodeDecision(tasks []store.FlowTask, seq int) nodeState {
	hasPending := false
	anyApproved := false
	hasTask := false
	for _, t := range tasks {
		if t.NodeSeq != seq {
			continue
		}
		hasTask = true
		switch t.Status {
		case TaskRejected:
			return nodeRejected
		case TaskPending:
			hasPending = true
		case TaskApproved:
			anyApproved = true
		}
	}
	if !hasTask || hasPending || !anyApproved {
		return nodePending
	}
	return nodeApproved
}

// distinctSeqs 返回任务中出现的全部 node_seq（升序去重）。
func distinctSeqs(tasks []store.FlowTask) []int {
	seen := map[int]bool{}
	var out []int
	for _, t := range tasks {
		if !seen[t.NodeSeq] {
			seen[t.NodeSeq] = true
			out = append(out, t.NodeSeq)
		}
	}
	sort.Ints(out)
	return out
}

// nodeReached 判断节点 seq 是否「已到达」：所有更小 seq 的节点均已通过（04a §2.3）。
func nodeReached(tasks []store.FlowTask, seq int) bool {
	for _, prev := range priorSeqs(tasks, seq) {
		if nodeDecision(tasks, prev) != nodeApproved {
			return false
		}
	}
	return true
}

// nodeIDOfSeq 返回某 node_seq 对应的 node_id（同节点内各任务 node_id 相同；无则空串）。
func nodeIDOfSeq(tasks []store.FlowTask, seq int) string {
	for _, t := range tasks {
		if t.NodeSeq == seq {
			return t.NodeID
		}
	}
	return ""
}

// priorSeqs 返回严格小于 seq 的全部已出现 node_seq（升序）。
func priorSeqs(tasks []store.FlowTask, seq int) []int {
	var out []int
	for _, s := range distinctSeqs(tasks) {
		if s < seq {
			out = append(out, s)
		}
	}
	return out
}

// isTerminal 判断实例是否处于终态（04a §2.1）。
func isTerminal(status string) bool {
	switch status {
	case InstanceApproved, InstanceRejected, InstanceCanceled:
		return true
	default:
		return false
	}
}

// validateSubmit 校验提交入参。
func validateSubmit(in SubmitInput) error {
	if strings.TrimSpace(in.DocType) == "" {
		return fmt.Errorf("%w: doc_type 为空", ErrInvalidSubmit)
	}
	if strings.TrimSpace(in.ApprovalCode) == "" {
		return fmt.Errorf("%w: approval_code 为空（无法校验三方定义）", ErrInvalidSubmit)
	}
	if len(in.Nodes) == 0 {
		return fmt.Errorf("%w: 审批链为空", ErrInvalidSubmit)
	}
	seenSeq := map[int]bool{}
	for _, n := range in.Nodes {
		if strings.TrimSpace(n.NodeID) == "" {
			return fmt.Errorf("%w: 节点 node_id 为空", ErrInvalidSubmit)
		}
		if len(n.Approvers) == 0 {
			return fmt.Errorf("%w: 节点 %s 无审批人", ErrInvalidSubmit, n.NodeID)
		}
		if seenSeq[n.Seq] {
			return fmt.Errorf("%w: 节点 seq=%d 重复", ErrInvalidSubmit, n.Seq)
		}
		seenSeq[n.Seq] = true
		for _, ap := range n.Approvers {
			if strings.TrimSpace(ap.OpenID) == "" {
				return fmt.Errorf("%w: 节点 %s 存在空审批人 open_id", ErrInvalidSubmit, n.NodeID)
			}
		}
	}
	return nil
}

// ---------- 表单字段分流（决策 #18/#19） ----------

// applyBizFields 把已映射表单字段分流（就地写入 inst）：
//   - 规范字段（amount_cents/amount、supplier/supplier_name、department、purpose_class_l1/l2）
//     → 对应 t_instance 规范列；
//   - 其余（含契约键 `contract_no`/`related_biz_no`、`biz_date` 等）→ ext_json。
//
// 返回 ext_json（JSON 字符串；无字段时 "{}"）。
// ★ 不写 `t_instance_field`（③ 下已弃用）；映射结果**必须被真正消费**，杜绝"映射无人读"的静默 P0。
// reservedInstanceIdentityKeys 身份类键 —— **永不**由提交 fields 写入 ext_json
// （N-038 项②：规范列 t_instance.applicant_open_id/department 才是权威，
// ext 残留同名值 = 同单两份矛盾身份）。新增身份键在此登记。
var reservedInstanceIdentityKeys = map[string]bool{
	"applicant":            true, // open_id（伪造实测路径②）
	"applicant_department": true, // 部门（伪造实测路径②）
}

func applyBizFields(inst *store.Instance, fields map[string]any) (string, error) {
	if inst == nil || len(fields) == 0 {
		return "{}", nil
	}
	ext := make(map[string]any, len(fields))
	for k, v := range fields {
		key := strings.ToLower(strings.TrimSpace(k))
		switch key {
		case config.BizFieldAmount, config.BizFieldAmountCents:
			if cents, ok := parseCentsAny(v, key == config.BizFieldAmountCents); ok && inst.AmountCents == nil {
				c := cents
				inst.AmountCents = &c
			} else {
				ext[key] = v // 无法解析 → 留痕于 ext_json（不静默丢）
			}
		case config.BizFieldSupplier, config.BizFieldSupplierName:
			if s := scalarString(v); s != "" && inst.Supplier == "" {
				inst.Supplier = s
			}
		case config.BizFieldDepartment:
			if s := scalarString(v); s != "" && inst.Department == "" {
				inst.Department = s
			}
		case config.BizFieldPurposeL1:
			if s := scalarString(v); s != "" && inst.PurposeClassL1 == "" {
				inst.PurposeClassL1 = s
			}
		case config.BizFieldPurposeL2:
			if s := scalarString(v); s != "" && inst.PurposeClassL2 == "" {
				inst.PurposeClassL2 = s
			}
		default:
			// ★ N-038 项②：客户端伪造的**规范列同义键**不得残留 ext_json ——
			//   否则同一张单据出现两份矛盾的「申请人」（规范列=真实身份、ext=伪造值）。
			//   applicant/applicant_department 由会话/组织权威解析（identityFrom），
			//   fields 里的同名值一律丢弃（OrgVerify 范式：服务端权威、客户端无效）。
			if key == "" || reservedInstanceIdentityKeys[key] {
				continue
			}
			ext[key] = v
		}
	}
	if len(ext) == 0 {
		return "{}", nil
	}
	b, err := json.Marshal(ext)
	if err != nil {
		return "", fmt.Errorf("%w: ext_json 序列化失败: %v", ErrInvalidSubmit, err)
	}
	return string(b), nil
}

// scalarString 取标量的字符串形态（非标量返回空串）。
func scalarString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(t)
	case json.Number:
		return strings.TrimSpace(t.String())
	case float64, int64, int, bool:
		return strings.TrimSpace(fmt.Sprint(t))
	default:
		return ""
	}
}

// parseCentsAny 把任意标量金额解析为「分」。
// centsKey 为真时数值形态视为已是分；否则视为「元」→ ×100 四舍五入。
func parseCentsAny(v any, centsKey bool) (int64, bool) {
	switch t := v.(type) {
	case int64:
		return t, true
	case int:
		return int64(t), true
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) {
			return 0, false
		}
		if centsKey {
			return int64(math.Round(t)), true
		}
		return int64(math.Round(t * 100)), true
	case json.Number:
		f, err := t.Float64()
		if err != nil {
			return 0, false
		}
		return parseCentsAny(f, centsKey)
	case string:
		s := strings.TrimSpace(amountNoise.Replace(t))
		if s == "" {
			return 0, false
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, false
		}
		return parseCentsAny(f, centsKey)
	default:
		return 0, false
	}
}

// amountNoise 金额文本中的噪声字符（货币符号 / 千分位 / 单位）。
var amountNoise = strings.NewReplacer(
	",", "", "，", "", " ", "", "\u00a0", "",
	"￥", "", "¥", "", "$", "", "元", "", "人民币", "",
	"RMB", "", "rmb", "", "CNY", "", "cny", "",
)
