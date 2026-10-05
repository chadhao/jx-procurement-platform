package httpapi

// approval(<node_id>) 时点的通用求值器（N-062 J3 · conventions.checks_when 第 ① 条 ·
// N-065 T3）—— 按 <node_id> 查表分派，取代「逐单据代码特例」。
//
// ★ 触发点：我方页面 approve 动作（handlers_approval.go#approveReject）、
//   `Flow.Approve` **之前**（拦在事务外：不落库、不产生半程副作用）。
// ★ 分派口径（与 submit 引擎同构，三条）：
//   ① 只取 `severity == hard` 且 `when == "approval(<本任务节点 id>)"` 的判据；
//   ② `carried_by_kind == "manual"` ⇒ **跳过不执行**（manual 是显式声明「人工承载」，
//      与「声明了没实现」不同语义 —— 引擎不越权执行人工判据，亦不对其 fail-closed）；
//   ③ 其余（code / submit / pending_wiring …）⇒ 必须已注册求值器，
//      **未注册 ⇒ 可见失败**（返回点名错误 ⇒ approve 400 —— 绝不沿用「非提交时点
//      一律 continue」的静默缺席，那等于判据 else 的承诺永不发生）。
// ★ SS 两节点与 PR#no_self_purchaser_at_designation 的既有 flow 特例
//   （applySSNodeFieldsTx / applyDesignationTx）**本批未迁移、双层共存**：
//   本引擎先拦（文案与 flow 特例逐字同源 ⇒ HTTP 语义零变化），flow 特例仍是
//   同事务内的第二道防线（覆盖飞书回调/repair 等不经本引擎的通道）。
// ★ anti_split_check（actor=system、无任务）⇒ 引擎永不触发（可达时刻表见 N-065 回执）。

import (
	"context"
	"fmt"
	"strings"

	"github.com/chadhao/jx-procurement-platform/internal/specload"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// approvalCheckFn 单条 approval(<node>) hard 判据的求值器。
// 返回 nil＝通过；非 nil＝拦截（错误串须与承载先例同源，保持 HTTP 语义稳定）。
type approvalCheckFn func(ctx context.Context, d Deps, form specload.FormDoc,
	inst *store.Instance, fields map[string]any, nodeID, actor string) error

// approvalCheckFns 注册表：判据 id → 求值器（与 submitHardChecks 同族、按 id 索引）。
var approvalCheckFns = map[string]approvalCheckFn{
	"receipt_per_purchase":             checkReceiptPerPurchase,
	"no_self_purchaser_at_designation": checkNoSelfPurchaserAtDesignation,
	"tech_opinion_required_at_node2":   checkSSTechOpinionAtNode,
	"pgm_final_required":               checkSSPgmFinalAtNode,
}

// evaluateApprovalChecksFor 内层引擎（不触库 —— 便于合成表单做 fail-closed 判据）。
func evaluateApprovalChecksFor(ctx context.Context, form specload.FormDoc, inst *store.Instance,
	fields map[string]any, nodeID, actor string, d Deps) error {
	want := "approval(" + nodeID + ")"
	for _, c := range form.Checks {
		if c.Severity != "hard" {
			continue
		}
		if strings.TrimSpace(c.When) != want {
			continue
		}
		if c.CarriedByKind == "manual" {
			continue // 人工承载：引擎不执行、不 fail-closed（可达时刻表口径）
		}
		fn, ok := approvalCheckFns[c.ID]
		if !ok {
			return fmt.Errorf("approval 判据 %q（节点 %s）未注册求值器 —— 声明了没执行，可见失败（请在 approvalCheckFns 注册）",
				c.ID, nodeID)
		}
		if err := fn(ctx, d, form, inst, fields, nodeID, actor); err != nil {
			return err
		}
	}
	return nil
}

// evaluateApprovalChecks 按任务取节点与实例，跑该节点的 approval(<node>) hard 判据。
func (d Deps) evaluateApprovalChecks(ctx context.Context, taskID, actor string, fields map[string]any) error {
	if d.Spec == nil {
		return fmt.Errorf("机读规格未装配，approval 判据无法求值（可见失败）")
	}
	task, err := d.DB.GetFlowTask(ctx, taskID)
	if err != nil {
		return fmt.Errorf("读任务 %s 失败: %w", taskID, err)
	}
	inst, err := d.DB.GetInstanceByBizNo(ctx, task.BizNo)
	if err != nil {
		return fmt.Errorf("读实例 %s 失败: %w", task.BizNo, err)
	}
	form, ok := d.Spec.Forms[inst.DocType]
	if !ok {
		return nil // 无表单单据 ⇒ 无判据
	}
	return evaluateApprovalChecksFor(ctx, form, inst, fields, task.NodeID, actor, d)
}

// ---- 已注册求值器（文案与承载先例逐字同源，保持 HTTP 语义零变化）----

// checkReceiptPerPurchase BA#receipt_per_purchase（N-065 T2/T3 的核心判据）：
// 回交凭据节点同意时 payment_receipt_no 与 payment_receipt_file 均非空，否则不得闭合。
func checkReceiptPerPurchase(_ context.Context, _ Deps, _ specload.FormDoc,
	inst *store.Instance, fields map[string]any, nodeID, _ string) error {
	if inst.DocType != "BA" || nodeID != "return_receipt" {
		return nil
	}
	if fields == nil {
		return fmt.Errorf("回交凭据未完成：payment_receipt_no 与 payment_receipt_file 均非空（BA#receipt_per_purchase）")
	}
	if strings.TrimSpace(strField(fields, "payment_receipt_no")) == "" ||
		strings.TrimSpace(strField(fields, "payment_receipt_file")) == "" {
		return fmt.Errorf("回交凭据未完成：payment_receipt_no 与 payment_receipt_file 均非空（BA#receipt_per_purchase）")
	}
	return nil
}

// checkNoSelfPurchaserAtDesignation PR#no_self_purchaser_at_designation：
// 与 flow/designation.go#applyDesignationTx 的 N-035 检查同条件（双层共存、文案同源）——
// 仅拦「操作人与被指定人同时为需求提出人」；两个「允许」分支保留。
func checkNoSelfPurchaserAtDesignation(_ context.Context, _ Deps, _ specload.FormDoc,
	inst *store.Instance, fields map[string]any, nodeID, actor string) error {
	if inst.DocType != "PR" || nodeID != "supervisor_approval" || fields == nil {
		return nil
	}
	purchaser := strings.TrimSpace(strField(fields, "designated_purchaser"))
	if actor == inst.ApplicantOpenID && purchaser == inst.ApplicantOpenID {
		return fmt.Errorf("不得自批自派自经办（操作人与经办人同为需求提出人 —— PR#no_self_purchaser_at_designation）")
	}
	return nil
}

// checkSSTechOpinionAtNode SS#tech_opinion_required_at_node2：
// 与 flow 特例 applyNodeFieldMapTx 同文案（N-036 · ErrInvalidNodeField 家族语义）。
func checkSSTechOpinionAtNode(_ context.Context, _ Deps, _ specload.FormDoc,
	inst *store.Instance, fields map[string]any, nodeID, _ string) error {
	if inst.DocType != "SS" || nodeID != "tech_opinion" || fields == nil {
		return nil // fields==nil ＝ 非结构化通道豁免（与 flow 特例同款；HTTP 通道恒非 nil）
	}
	if strings.TrimSpace(strField(fields, "tech_opinion")) == "" {
		return fmt.Errorf("SS 单据节点 tech_opinion 同意时必须填写 tech_opinion（spec 该节点时点必填 · N-036）")
	}
	return nil
}

// checkSSPgmFinalAtNode SS#pgm_final_required —— 同上（批复 A6 · N-036 缺口 6）。
func checkSSPgmFinalAtNode(_ context.Context, _ Deps, _ specload.FormDoc,
	inst *store.Instance, fields map[string]any, nodeID, _ string) error {
	if inst.DocType != "SS" || nodeID != "pgm_final" || fields == nil {
		return nil
	}
	if strings.TrimSpace(strField(fields, "pgm_final_opinion")) == "" {
		return fmt.Errorf("SS 单据节点 pgm_final 同意时必须填写 pgm_final_opinion（spec 该节点时点必填 · N-036）")
	}
	return nil
}
