package httpapi

// PC/SS 提交期系统字段注入（N-036 · 6 条真缺口的生产者侧）。
//
// ★ 这些字段 spec 标 `source=system/computed`（系统带入/计算），此前**没有任何生产者**
// ⇒ 「声明了却没人执行」同族缺口。注入点＝`evaluateHardChecks` 之后、`Flow.Submit`
// 之前（幂等指纹**包含**注入值 ⇒ 同载荷判定更稳；重试时 L09 尚未落账、聚合值不变）。
//
// PC（采购变更）：
//   - original_contract_amount_cents ← L04 合同行 amount_cents（rule：从合同台账带入、不可手改；
//     合同号＝L04 biz_no 等值，复用 checkPCContractExists 的口径 —— 它已由提交期 hard 拦过存在性）；
//   - related_biz_no ← 同行 ext.related_biz_no（CT 提交时的关联 PR，rule：由 contract_no 反查）；
//   - applicable_tier ← chain.ChangeTierOf（R-15 就高：max(change, original)）；
//   - change_count_to_date ← L09 历史行（同 contract_no，json_extract 等值）**含本次**；
//   - change_chain ← {count, cumulative_change_cents, tier}（R-21#7 三要素按合同号）；
//   - is_anomaly_listed ← 90 天内历史 ≥1 次（含本次 ≥2）⇒ true（rule 原文）；
//   - exception_type ← "采购变更"（L09 values_ref 唯一对应 PC）。
// SS（单一来源）：exception_type ← "独家采购"。
//
// SS 的 exception_type 同样注入；其余 SS 字段（tech_opinion 等）属**节点时点**，
// 由 flow/designation.go#applySSNodeFieldsTx 承载 —— 不在提交期。

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/chain"
	"github.com/chadhao/jx-procurement-platform/internal/specload"
)

// injectPCSSSystemFields 提交期注入（仅 PC/SS；其它单据 no-op）。
// contract_no 查不到 L04 ⇒ 返回错误（400）—— 正常应由提交期 hard 先拦，
// 此处 fail-closed 防「判据被绕过/误删」后静默注入垃圾。
func (d Deps) injectPCSSSystemFields(ctx context.Context, body *approvalSubmitBody) error {
	if body == nil || body.Fields == nil {
		return nil
	}
	switch body.DocType {
	case "SS":
		putSysField(body.Fields, "exception_type", "独家采购")
		return nil
	case "PC":
		return d.injectPCFields(ctx, body)
	default:
		return nil
	}
}

func (d Deps) injectPCFields(ctx context.Context, body *approvalSubmitBody) error {
	fields := body.Fields
	contractNo, _ := fields["contract_no"].(string)
	if contractNo == "" {
		return nil // 必填由提交期 hard（contract_no_required_and_exists）负责
	}
	// ① L04 合同行：amount ＋ ext.related_biz_no（json_valid 守卫 —— 同 repo_contract 先例）
	var amtCents int64
	var l04Supplier string
	var l04Ext string
	err := d.DB.QueryRowContext(ctx, `
SELECT amount_cents, COALESCE(supplier,''), COALESCE(ext_json,'{}') FROM t_ledger_archive
WHERE ledger_type='L04' AND biz_no = ?`, contractNo).Scan(&amtCents, &l04Supplier, &l04Ext)
	if err != nil {
		return fmt.Errorf("合同 %s 在合同台账（L04）无等值记录，不可注入原合同金额（不可手改字段无源）", contractNo)
	}
	putSysField(fields, "original_contract_amount_cents", amtCents)
	// N-039 项②：original_supplier ← L04.supplier（rule：从合同台账带入；
	// 空值不写 —— 不伪造「无供应商」为伪值）。
	if l04Supplier != "" {
		putSysField(fields, "original_supplier", l04Supplier)
	}
	ext := map[string]any{}
	if err := json.Unmarshal([]byte(l04Ext), &ext); err == nil {
		if rb, _ := ext["related_biz_no"].(string); rb != "" {
			putSysField(fields, "related_biz_no", rb)
		}
	}

	// ② applicable_tier（R-15 就高）—— flow 不能 import chain，故在 httpapi 层算。
	changeCents, _ := toFloat64(fields["change_amount_cents"])
	tier, terr := chain.ChangeTierOf(d.Spec, int64(changeCents), amtCents)
	if terr != nil {
		return fmt.Errorf("档位就高计算失败: %w", terr)
	}
	putSysField(fields, "applicable_tier", tier)

	// ③ 同合同历史变更聚合（L09 · json_extract 等值 + json_valid 守卫）
	var hist []histRow
	rows, qerr := d.DB.QueryContext(ctx, `
SELECT COALESCE(ext_json,'{}'), COALESCE(biz_date,'') FROM t_ledger_archive
WHERE ledger_type='L09' AND json_valid(ext_json) AND json_extract(ext_json,'$.contract_no') = ?
ORDER BY biz_no`, contractNo)
	if qerr != nil {
		return fmt.Errorf("变更历史查询失败: %w", qerr)
	}
	for rows.Next() {
		var h histRow
		if err := rows.Scan(&h.ext, &h.date); err != nil {
			rows.Close()
			return err
		}
		hist = append(hist, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	// 含本次：次数 = 历史 + 1；累计 = Σ历史 change + 本次（R-21#7 / rule 明写含本次）。
	count := len(hist) + 1
	cumulative := int64(changeCents)
	cutoff := time.Now().UTC().AddDate(0, 0, -90).Format("2006-01-02")
	recent := 0
	for _, h := range hist {
		hext := map[string]any{}
		if err := json.Unmarshal([]byte(h.ext), &hext); err == nil {
			if v, ok := toFloat64(hext["change_amount_cents"]); ok {
				cumulative += int64(v)
			}
		}
		if h.date != "" && h.date >= cutoff {
			recent++
		}
	}
	putSysField(fields, "change_count_to_date", count)
	// 90 天内（含本次）≥2 ⇒ 历史近期 ≥1
	putSysField(fields, "is_anomaly_listed", recent >= 1)
	putSysField(fields, "change_chain", map[string]any{
		"count": count, "cumulative_change_cents": cumulative, "tier": tier,
	})
	putSysField(fields, "exception_type", "采购变更")

	// ---- N-038 附一（已定口径 5 项，逐条按 spec rule）----
	// total_change_cents ＝ 历次变更差额累计 ＋ 本次差额（＝上面的 cumulative）
	putSysField(fields, "total_change_cents", cumulative)
	// new_total_cents ＝ original + total（rule 原文）
	putSysField(fields, "new_total_cents", amtCents+cumulative)
	// last_change_at ＝ 上一次变更时间；**无历次为空**（rule 原文 —— 不写伪值）
	if last := lastChangeAt(hist); last != "" {
		putSysField(fields, "last_change_at", last)
	}
	// is_reset_as_new_purchase ＝ new_total > original × 1.5（制度第五十二条 150%）
	newTotal := amtCents + cumulative
	putSysField(fields, "is_reset_as_new_purchase", float64(newTotal) > float64(amtCents)*1.5)
	// is_engineering_category ＝ usage_category_l1 ∈ {P05, P06}（用途分类；
	// 值从 L04.ext 的 usage_category_l1 带入 —— CT 已按 B6 从 PR 带入 ⇒ 合同行 ext 有）
	if l1, _ := ext["usage_category_l1"].(string); l1 != "" {
		putSysField(fields, "is_engineering_category", l1 == "P05" || l1 == "P06")
	}
	// N-039 项②：special_explanation_required ＝ total_change_cents > original × 0.3
	// （rule 原文；total＝cumulative＝Σ历史+本次，与 total_change_cents 注入同值）。
	putSysField(fields, "special_explanation_required",
		float64(cumulative) > float64(amtCents)*0.3)
	return nil
}

// histRow L09 历史行（ext 原文 + 落账日期）。
type histRow struct {
	ext  string
	date string
}

// lastChangeAt 取历史最近一次变更时间（按 biz_date 降序的首条；空 = 无历次）。
func lastChangeAt(hist []histRow) string {
	for i := len(hist) - 1; i >= 0; i-- {
		if hist[i].date != "" {
			return hist[i].date
		}
	}
	return ""
}

// putSysField 服务端权威**恒覆盖**（N-038 项①）：`source=system|computed` 字段的
// 权威值来自服务端计算 —— 客户端同名值一律被覆盖（OrgVerify 范式：服务端值在
// applyBizFields 之后覆盖 ⇒ 伪造无效）。
// ★ 原实现「不覆盖非空手填值」被 WB 变异探针实测打穿：applicable_tier 可降档为
// tier1、金额/次数/异常标记/例外类型均可伪造 —— 缺陷已被写进注释与测试 ④，一并反转。
func putSysField(fields map[string]any, key string, v any) {
	fields[key] = v
}

func toFloat64(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int64:
		return float64(x), true
	case int:
		return float64(x), true
	}
	return 0, false
}

// specloadUsed 防未用导入（SubmitInput 侧已带 Spec）。
var _ = specload.Bundle{}
