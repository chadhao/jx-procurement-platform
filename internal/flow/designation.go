package flow

// N-015 批 2 承载：`supervisor_approval` 节点同意 PR（采二/三档）时的「指定经办」填报。
//
// ★ 裁定锚点（COLLAB N-015 · WorkBuddy 2026-09-29 21:45）：
//   ① 必填时点＝ supervisor_approval 节点且操作＝approve（制度第十一条「当场指定」）；
//   ② 字段＝ designated_purchaser（必填）· designation_basis（必填）·
//      designated_by / designated_at（系统带入、不可填）；
//   ③ designated_purchaser ≠ applicant **不阻断、落标记**；超出可指定范围
//      （需求部门或综合运营部）**不阻断、落标记**；仅 tier2/tier3 需要
//      —— tier1 路由（purchase_tier1）**没有** supervisor_approval 节点 ⇒ 按节点判定天然满足；
//   ④ 其他操作（reject/transfer/rollback/addsign/cancel）一律不带此字段。
//
// ★ 通道差异（技术形态，mimo 定，回执已登记）：
//   - **我方页面两键**：handler 恒传非 nil fields ⇒ 本函数**执行必填**（缺 ⇒ ErrInvalidDesignation）；
//   - **飞书回调 / repair 重放**：fields == nil（非结构化通道，意见是自由文本）⇒ **豁免必填、
//     不写入**（不卡死回调与修复循环）。★ 豁免的后果＝该通道下 designated_* 为空 ——
//     由看板 r3 列级守卫保证「空时显示数据未接入而非 0」，缺口可见、不静默。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// ErrInvalidDesignation 指定经办填报缺失/非法（HTTP 映射 400）。
var ErrInvalidDesignation = errors.New("flow: 指定经办填报非法")

// designationNodeID 指定经办的唯一时点节点（N-015 裁定①逐字）。
const designationNodeID = "supervisor_approval"

// designationOutOfScopeDept 裁定③的第二个可指定部门（制度第十四条：综合运营部）。
const designationOutOfScopeDept = "综合运营部"

// applyDesignationTx 在 supervisor_approval 同意时落 designated_* 四列（同事务）。
// fields == nil ⇒ 非结构化通道，豁免（见文件头）。非指定节点 / 非 PR ⇒ no-op。
func (s *Service) applyDesignationTx(ctx context.Context, tx *sql.Tx, inst *store.Instance,
	task *store.FlowTask, fields map[string]any, actor string, at time.Time) error {
	if task.NodeID != designationNodeID || inst.DocType != "PR" {
		return nil
	}
	if fields == nil {
		return nil // 飞书回调 / repair：豁免必填、不写入（不卡死）
	}
	purchaser := strings.TrimSpace(designationStr(fields, "designated_purchaser"))
	basis := strings.TrimSpace(designationStr(fields, "designation_basis"))
	if purchaser == "" || basis == "" {
		return fmt.Errorf("%w: 主管领导审批须当场指定经办人与指定依据（制度第十一条 · N-015 裁定①）", ErrInvalidDesignation)
	}
	// ★ N-035：`PR#no_self_purchaser_at_designation`（when=approval(supervisor_approval)
	//   severity=hard · carried_by=本文件）—— **自批自派自经办**：操作人与被指定人
	//   **同时**为需求提出人 ⇒ 拦（制度第十四条/第六十条的系统把关）。
	//   ★ 两个「允许」必须保留（裁定原文）：仅经办人＝提出人（上级领导指派）放行；
	//   仅操作人＝提出人而指定别人也放行 —— **只拦两者同时成立**。
	//   ★ 规则来源＝spec 该判据（carried_by 反向锚定本文件；锚定测试见
	//   `specload/spec_anchors_test.go`，spec 改动会使锚定测试转红、防两份真相）。
	if actor == inst.ApplicantOpenID && purchaser == inst.ApplicantOpenID {
		return fmt.Errorf("%w: 不得自批自派自经办（操作人与经办人同为需求提出人 —— PR#no_self_purchaser_at_designation）", ErrInvalidDesignation)
	}

	ext := map[string]any{}
	if strings.TrimSpace(inst.ExtJSON) != "" {
		if err := json.Unmarshal([]byte(inst.ExtJSON), &ext); err != nil {
			return fmt.Errorf("%w: 实例 ext_json 损坏，无法落指定经办: %w", ErrInvalidDesignation, err)
		}
	}
	// ② 四列：两必填 + 两系统带入（designated_by ＝ 本任务审批人本人 —— act 已鉴权）。
	ext["designated_purchaser"] = purchaser
	ext["designation_basis"] = basis
	ext["designated_by"] = actor
	ext["designated_at"] = at.Format(time.RFC3339)

	// ③ 落标记（一律不阻断）：
	//    is_self_designated —— 经办人＝提出人（看板 16「需求提出人任经办人」的分子）。
	ext["is_self_designated"] = purchaser == inst.ApplicantOpenID
	//    designation_out_of_scope —— 被指定人部门 ∉ {需求部门, 综合运营部}。
	//    （查不到人员档案 ⇒ 不落标记 —— 镜像缺失不判越界，与 orgsync 过滤同哲学。）
	if role, err := s.db.GetUserRoleTx(ctx, tx, purchaser); err == nil && role != nil {
		dept := strings.TrimSpace(role.Department)
		inScope := dept == strings.TrimSpace(inst.Department) || dept == designationOutOfScopeDept
		ext["designation_out_of_scope"] = !inScope
	}

	b, err := json.Marshal(ext)
	if err != nil {
		return fmt.Errorf("编码指定经办 ext 失败: %w", err)
	}
	inst.ExtJSON = string(b)
	// ★ 与任务推进同事务：本函数失败 ⇒ 任务状态不推进、ext 不落（一致性一体）。
	if err := s.db.UpsertInstanceTx(ctx, tx, inst); err != nil {
		return err
	}
	// ★ N-042 权限位点：同事务写 t_instance.designated_open_id（ASSIGNED 令牌的数据承载；
	//   规范列与 ext 双写 —— 避免「ext 有、列没写」的两处不一致；archive 侧由 finalize 同源取 ext）。
	return s.db.SetInstanceDesignatedTx(ctx, tx, inst.BizNo, purchaser)
}

// designationStr 从 fields 取字符串（非字符串类型视为空 —— 必填由调用方判）。
func designationStr(fields map[string]any, key string) string {
	v, _ := fields[key].(string)
	return v
}

// ---------------------------------------------------------------------------
// SS 节点时点字段（N-036 · 6 条真缺口之一 / 之二）
// ---------------------------------------------------------------------------

// ErrInvalidNodeField 节点时点必填字段缺失（HTTP 映射 400；与 ErrInvalidDesignation
// 分开 —— 错误文案与归因不同：一个缺指定经办、一个缺节点意见）。
var ErrInvalidNodeField = errors.New("flow: 节点时点必填字段非法")

// ssNodeFieldIDs SS 链上承载必填字段的节点（spec chain.json#routes.sole_source：
// seq2 tech_opinion · seq4 pgm_final —— 锚定测试钉住，改 spec 会红）。
const (
	ssTechOpinionNodeID = "tech_opinion"
	ssPgmFinalNodeID    = "pgm_final"
)

// applySSNodeFieldsTx 节点时点字段的**非空强制**与系统带入（N-036 缺口 5/6 ＋ PC 登记日期）——
// 规则表见 nodeFieldSpecFor：
//   - tech_opinion 节点：`tech_opinion` 必填；`tech_opinion_by/at` 系统带入（=审批人/时刻）；
//   - pgm_final 节点：`pgm_final_opinion` 必填（批复 A6 项目总经理终审）。
//
// 通道语义与 designation 同款：fields==nil（飞书回调/repair）⇒ 豁免必填、不写入
// （自由文本意见无法结构化）；非 SS 单据 / 其它节点 ⇒ no-op。
// ★ carried_by 待 spec 填报（kind=pending_implementation ⇒ 本函数落地后请 WB 指向本文件）。
func (s *Service) applySSNodeFieldsTx(ctx context.Context, tx *sql.Tx, inst *store.Instance,
	task *store.FlowTask, fields map[string]any, actor string, at time.Time) error {
	if fields == nil {
		return nil // 非结构化通道豁免（与 designation 同款）
	}
	return s.applyNodeFieldMapTx(ctx, tx, inst, task, fields, actor, at,
		nodeFieldSpecFor(inst.DocType, task.NodeID))
}

// nodeFieldRule 单个「单据×节点」的必填字段规则。
type nodeFieldRule struct {
	RequiredKey string // 必填字段（空 = 该节点无规则）
	WriteKey    string // 落 ext 的键（默认 = RequiredKey）
	ByKey       string // 系统带入「人」的键（空 = 不带）
	AtKey       string // 系统带入「时刻」的键
}

// nodeFieldSpecFor 按（单据, 节点）取规则 —— **表驱动**，新增节点字段只加一行。
//
//	SS×tech_opinion   → tech_opinion 必填 + by/at 系统带入（N-036 缺口 5）
//	SS×pgm_final      → pgm_final_opinion 必填（批复 A6；缺口 6）
//	PC×ledger_submit   → resubmitted_to_group_at 必填（PC rule：「综合运营主管登记实际
//	                      提交日期」⇒ 登记人＝ledger_submit 的 actor＝ops_supervisor）。
//	★ PC 的 section filled_at="node3_approval_and_node4_ledger" 含两节点 ——
//	  本规则按 **rule 的登记人** 落在 node4；node3 是否也需填报**请 spec 澄清**
//	  （回执已提，未澄清前不在 node3 加拦 —— 不抢跑）。
func nodeFieldSpecFor(docType, nodeID string) *nodeFieldRule {
	if docType == "SS" {
		switch nodeID {
		case ssTechOpinionNodeID:
			return &nodeFieldRule{RequiredKey: "tech_opinion", ByKey: "tech_opinion_by", AtKey: "tech_opinion_at"}
		case ssPgmFinalNodeID:
			return &nodeFieldRule{RequiredKey: "pgm_final_opinion"}
		}
		return nil
	}
	if docType == "PC" && nodeID == pcLedgerSubmitNodeID {
		return &nodeFieldRule{RequiredKey: "resubmitted_to_group_at"}
	}
	return nil
}

const pcLedgerSubmitNodeID = "ledger_submit"

func (s *Service) applyNodeFieldMapTx(ctx context.Context, tx *sql.Tx, inst *store.Instance,
	task *store.FlowTask, fields map[string]any, actor string, at time.Time, rule *nodeFieldRule) error {
	if rule == nil || rule.RequiredKey == "" {
		return nil
	}
	val := strings.TrimSpace(designationStr(fields, rule.RequiredKey))
	if val == "" {
		return fmt.Errorf("%w: %s 单据节点 %s 同意时必须填写 %s（spec 该节点时点必填 · N-036）",
			ErrInvalidNodeField, inst.DocType, task.NodeID, rule.RequiredKey)
	}
	ext := map[string]any{}
	if strings.TrimSpace(inst.ExtJSON) != "" {
		if err := json.Unmarshal([]byte(inst.ExtJSON), &ext); err != nil {
			return fmt.Errorf("%w: 实例 ext_json 损坏: %w", ErrInvalidNodeField, err)
		}
	}
	writeKey := rule.WriteKey
	if writeKey == "" {
		writeKey = rule.RequiredKey
	}
	ext[writeKey] = val
	if rule.ByKey != "" {
		ext[rule.ByKey] = actor
		ext[rule.AtKey] = at.Format(time.RFC3339)
	}
	b, err := json.Marshal(ext)
	if err != nil {
		return fmt.Errorf("编码节点字段 ext 失败: %w", err)
	}
	inst.ExtJSON = string(b)
	return s.db.UpsertInstanceTx(ctx, tx, inst)
}
