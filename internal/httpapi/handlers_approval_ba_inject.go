package httpapi

// BA/CT 提交期系统字段注入（N-062 J1 · 字段级 10 条中的 5 条生产者）。
//
// ★ 这些字段 spec 标 `source=system/computed` 且 `carried_by_kind=pending_implementation`
//   （「声明了却没人执行」）—— 本文件按 N-062 三选一裁定补执行体：
//   - BA.record_date            ← 提交（备案）当日；
//   - BA.anti_split_check_result ← 同供应商同二级当月累计（含本单）＋ 部门当月备案次数，
//                                   阈值取 spec chain.json#routes.purchase_tier1.nodes[id=anti_split_check].params
//                                   （same_supplier_same_category_monthly_cents / dept_monthly_max_count，
//                                    Go 侧不写死 —— spec 是唯一真相）；
//   - BA.is_monthly_supplier_rollover_warned ← 同一累计判定的布尔面（≥ 阈值 ⇒ 落入转档预警）；
//   - BA.is_key_sample_range     ← 金额 ∈ [800, 1000) 元（docs/03-TestCase.md TC-19 逐点预期：
//                                   799→否 / 800→是 / 999→是 / 1000→否〔属采二档〕）；
//   - CT.approval_levels         ← 链算结果派生（rule 正本：固定「主管领导 → 项目总经理」两级；
//                                   同一人 ⇒ 「一级（终审）」）。
//
// ★ 数据源 = t_instance（schema 注释：supplier 列即「防拆分累计依据」；索引
//   idx_instance_supplier(supplier, biz_no_yymm) 本就是为该查询而建）——
//   提交期查「先于本单的当月已备案单」，本单尚未落库、天然不含自身。
// ★ 注入点 = injectPCSSSystemFields 同排（compute 之前）；幂等指纹包含注入值（PC 同款先例）。

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/chain"
)

// antiSplitNodeID 防拆分检查节点（spec chain.json#routes.purchase_tier1.nodes[*]）。
const antiSplitNodeID = "anti_split_check"

// injectBASystemFields BA 提交期系统字段注入（其它单据 no-op）。
// amountCents = 定档金额（顶层 amount_cents，缺省回落表单 amount_cents）；
// usageL2 = 用途二级明细（拆单累计键）；dept = 部门（当月次数键）；at = 提交时刻。
func (d Deps) injectBASystemFields(ctx context.Context, body *approvalSubmitBody,
	amountCents *int64, usageL2, dept string, at time.Time) error {
	if body == nil || body.Fields == nil || body.DocType != "BA" {
		return nil
	}
	fields := body.Fields

	// ① 备案日期 ← 提交当日（L01 日期列同口径：优先 ext.biz_date、否则提交日期 ——
	//    finalize.go 同源；字段侧直接取提交日，规则=备案单提交即备案）。
	putSysField(fields, "record_date", at.Format("2006-01-02"))

	// ② 阈值 ← spec（fail-visible：节点/参数缺 ⇒ 报错，不静默降级为 0）。
	threshold, maxCount, perr := d.antiSplitParams()
	if perr != nil {
		return perr
	}

	// ③ 金额（定档金额优先；顶层缺省回落表单值 —— BA 金额必填由结构化校验兜底）。
	var amt int64
	if amountCents != nil {
		amt = *amountCents
	} else if v, ok := toFloat64(fields["amount_cents"]); ok {
		amt = int64(v)
	}

	// ④ 同供应商 + 同二级明细 当月累计（**先于本单**）。
	// ★ 月份键 ＝ `substr(biz_no,4,4)`（BA-YYMM-#### 前缀恒 2 位）—— ★ 不用
	//   `biz_no_yymm` 列：实测 flow 提交路径**不写该列**（NULL，仅 worker ingest 写），
	//   按列过滤会漏掉全部本系统提交的单据（观测已入回执，列填充属另案）。
	// ★ 取数与 applyBizFields 落列口径逐字对齐（inst.Supplier/PurposeClassL2 初值＝
	//   Submit 顶层值、空则由 fields 补入 ⇒ 此处同序 fallback；真实前端顶层传 usage
	//   —— Submit.vue:431）。
	yymm := at.Format("0601")
	supplier := firstNonEmptyStr(body.Supplier, strField(body.Fields, "supplier", "supplier_name"))
	l2 := firstNonEmptyStr(usageL2, strField(body.Fields, "usage_category_l2", "purpose_class_l2"))
	dept = firstNonEmptyStr(dept, strField(body.Fields, "department"))
	supplierOK := strings.TrimSpace(supplier) != ""
	var priorSum int64
	if supplierOK {
		if err := d.DB.QueryRowContext(ctx, `
SELECT COALESCE(SUM(amount_cents),0) FROM t_instance
WHERE doc_type='BA' AND supplier=? AND purpose_class_l2=? AND substr(biz_no,4,4)=?`,
			strings.TrimSpace(supplier), l2, yymm).Scan(&priorSum); err != nil {
			return fmt.Errorf("同供应商当月累计查询失败: %w", err)
		}
	}
	rollover := supplierOK && priorSum+amt >= threshold
	putSysField(fields, "is_monthly_supplier_rollover_warned", rollover)

	// ⑤ 重点抽查区间 [800 元, 1000 元) —— TC-19 逐点预期 1000→否（属采二档）。
	putSysField(fields, "is_key_sample_range", amt >= 80000 && amt < 100000)

	// ⑥ 部门当月备案次数（含本次）。
	var priorDept int64
	if err := d.DB.QueryRowContext(ctx, `
SELECT COUNT(*) FROM t_instance
WHERE doc_type='BA' AND department=? AND substr(biz_no,4,4)=?`, dept, yymm).Scan(&priorDept); err != nil {
		return fmt.Errorf("部门当月备案次数查询失败: %w", err)
	}
	deptCount := priorDept + 1

	// ⑦ 结果串（公式列 + 人工核对 —— 制度第十八条：两项不通过即转档或退回，
	//    必须在拨付款项之前完成；本字段只记录、不硬拦，known_cost 正本）。
	var supClause string
	switch {
	case !supplierOK:
		supClause = "无供应商，同供应商累计不适用"
	case rollover:
		supClause = fmt.Sprintf("同供应商同二级当月累计 %.2f 元（≥ 阈值 %.2f 元）：触发转档预警",
			float64(priorSum+amt)/100, float64(threshold)/100)
	default:
		supClause = fmt.Sprintf("同供应商同二级当月累计 %.2f 元（< 阈值 %.2f 元）：未触发",
			float64(priorSum+amt)/100, float64(threshold)/100)
	}
	deptClause := fmt.Sprintf("部门当月备案 %d/%d 次", deptCount, maxCount)
	if deptCount > maxCount {
		deptClause += "：超限"
	} else {
		deptClause += "：未超限"
	}
	putSysField(fields, "anti_split_check_result", supClause+"；"+deptClause)
	return nil
}

// antiSplitParams 从 spec 取防拆分两阈值（routes.purchase_tier1.nodes[id=anti_split_check].params）。
func (d Deps) antiSplitParams() (threshold int64, maxCount int64, err error) {
	route, ok := d.Spec.Chain.Routes["purchase_tier1"]
	if !ok {
		return 0, 0, fmt.Errorf("spec 缺路由 routes.purchase_tier1（防拆分阈值无源）")
	}
	for i := range route.Nodes {
		n := &route.Nodes[i]
		if n.ID != antiSplitNodeID {
			continue
		}
		if n.Params == nil {
			return 0, 0, fmt.Errorf("spec 缺节点参数 routes.purchase_tier1.nodes[id=%s].params", antiSplitNodeID)
		}
		t, ok1 := n.Params["same_supplier_same_category_monthly_cents"]
		c, ok2 := n.Params["dept_monthly_max_count"]
		if !ok1 || !ok2 || t <= 0 || c <= 0 {
			return 0, 0, fmt.Errorf("spec 节点参数不完整（%s.params）", antiSplitNodeID)
		}
		return int64(t), int64(c), nil
	}
	return 0, 0, fmt.Errorf("spec 缺节点 routes.purchase_tier1.nodes[id=%s]", antiSplitNodeID)
}

// strField 依次取 fields 中第一个非空字符串键值（与 applyBizFields 的多键 fallback 同序）。
func strField(fields map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := fields[k].(string); ok && strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// approvalLevelsOf CT 审批层级（forms/CT.json#approval_levels.rule 正本：
// 固定记「主管领导 → 项目总经理」两级；主管领导即为总经理（同一人走完全链）⇒ 「一级（终审）」）。
// rc.Spec 仅含生成任务的审批节点（applicant/system 节点不入 Spec）；EnsureResolvable 已过 ⇒ 有人。
func approvalLevelsOf(rc *chain.ResolvedChain) string {
	if rc == nil || len(rc.Spec) == 0 {
		return ""
	}
	uniq := map[string]struct{}{}
	for _, n := range rc.Spec {
		for _, a := range n.Approvers {
			if a.OpenID != "" {
				uniq[a.OpenID] = struct{}{}
			}
		}
	}
	if len(uniq) == 1 {
		return "一级（终审）"
	}
	return "主管领导 → 项目总经理"
}
