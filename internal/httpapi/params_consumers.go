package httpapi

// T1 · `spec/params.json` 消费端（N-025 / README 定案 #24「假配置」防线）。
//
// ★ 验收要求：每个参数**能指出消费函数** —— 本文件即那份「指得出」的清单：
//   - `reporting.monthly_cutoff_day`    → reimbursementReportingView（提交批次归集）
//   - `reporting.overdue_handling`      → reimbursementReportingView（pending 按建议值运行）
//   - `petty_cash.reimburse_deadline`   → pettyCashDeadlinePolicy（type=none ⇒ 不设时限分支）
//   - `contract.amount_over_pr_tolerance_percent` → contractAmountTolerance（CT 提交 hard 校验）
//   - `contract.over_tolerance_action`  → contractOverToleranceAction（同上）
//
// ★ 探针口径：改 params.json 的 value ⇒ 这些函数的返回随之变化（不写死）。

import (
	"fmt"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/chadhao/jx-procurement-platform/internal/specload"
)

// requireSpec 取参数登记册；未装配 ⇒ 503 可见失败（不静默降级为常量）。
func (d Deps) requireSpec(c echo.Context) (*specload.Bundle, bool) {
	if d.Spec == nil {
		_ = fail(c, http.StatusServiceUnavailable, codeNotReady, "机读规格/参数登记册未装配")
		return nil, false
	}
	return d.Spec, true
}

// batchPeriod 按「当月 cutoff 日（含）截止」归集批次：
//
//	day <= cutoff ⇒ 本月批次；day > cutoff ⇒ **归入次月批次**（不阻断、不处罚）。
//
// 返回 (YYYY-MM, 是否跨月归集)。
func batchPeriod(t time.Time, cutoffDay int) (string, bool) {
	y, m, _ := t.Date()
	rolled := cutoffDay > 0 && t.Day() > cutoffDay
	if rolled {
		m++
		if m > 12 {
			m = 1
			y++
		}
	}
	return fmt.Sprintf("%04d-%02d", y, m), rolled
}

// reimbursementReportingView 报销登记的参数消费视图（T1）。
//   - cutoff 读 `reporting.monthly_cutoff_day`；
//   - 超期处置读 `reporting.overdue_handling`：**待定 ⇒ 按建议值（归入次月）运行并标 pending**，
//     不把建议值固化成已定（params.json 的 value 仍为 null）。
func (d Deps) reimbursementReportingView(now time.Time) map[string]any {
	cutoff, ok := d.Spec.ParamInt("reporting.monthly_cutoff_day")
	if !ok {
		// 参数缺失/非整数：可见失败已在加载侧（[P] 系列）拦过；此处保守用 0 ⇒ 不跨月，
		// 但必须显式暴露，不能让调用方以为 cutoff 生效。
		cutoff = 0
	}
	period, rolled := batchPeriod(now, cutoff)
	pending, recommendation := d.Spec.ParamPending("reporting.overdue_handling")
	overdue := map[string]any{
		"status":  "pending",
		"pending": pending,
		// 建议值行为（归入次月）—— pending 期间的**运行口径**；用户定案后由
		// params.json#value 接管，此处逻辑随之改读 value。
		"in_effect": "auto_next_month",
	}
	if !pending {
		if v, ok2 := d.Spec.ParamString("reporting.overdue_handling"); ok2 {
			overdue["in_effect"] = v
			overdue["status"] = "已定"
		}
	} else if recommendation != "" {
		overdue["recommendation_note"] = "按建议值运行（待用户一句话确认）"
	}
	return map[string]any{
		"cutoff_day":       cutoff,
		"batch_period":     period,
		"rolled_to_next":   rolled,
		"overdue_handling": overdue,
	}
}

// pettyCashDeadlinePolicy 备付金报销时限（T1）——
// `petty_cash.reimburse_deadline`：**type=none ⇒ 随时报销、不设时限、零阻断**（用户 A2 定案）。
// ★ 这是与个人报销并列的另一套规则，**不得互相套用**（critical_note）。
func (d Deps) pettyCashDeadlinePolicy() map[string]any {
	entry, ok := d.Spec.ParamEntryByKey("petty_cash.reimburse_deadline")
	if !ok {
		return map[string]any{"loaded": false}
	}
	enforced := entry.Type == "int" && !entry.IsPending()
	return map[string]any{
		"loaded":        true,
		"type":          entry.Type,
		"enforced":      enforced, // none ⇒ false：**不做任何时限阻断**
		"status":        entry.Status,
		"cutoff_source": "spec/params.json#petty_cash.reimburse_deadline",
	}
}

// contractAmountTolerance 合同额 vs PR 预估额 容差（%，T1+T4 共用消费点）。
// ★ 阈值**读 params，不写死 10**（N-025 T4 验收点）。
func (d Deps) contractAmountTolerance() (int, bool) {
	return d.Spec.ParamInt("contract.amount_over_pr_tolerance_percent")
}

// contractOverToleranceAction 超容差处置动作（T1+T4 共用）。
func (d Deps) contractOverToleranceAction() string {
	s, ok := d.Spec.ParamString("contract.over_tolerance_action")
	if !ok {
		return "" // 未装配/待定 ⇒ 调用方按「可见失败」处理
	}
	return s
}
