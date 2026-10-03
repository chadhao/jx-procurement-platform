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
	var l04Ext string
	err := d.DB.QueryRowContext(ctx, `
SELECT amount_cents, COALESCE(ext_json,'{}') FROM t_ledger_archive
WHERE ledger_type='L04' AND biz_no = ?`, contractNo).Scan(&amtCents, &l04Ext)
	if err != nil {
		return fmt.Errorf("合同 %s 在合同台账（L04）无等值记录，不可注入原合同金额（不可手改字段无源）", contractNo)
	}
	putSysField(fields, "original_contract_amount_cents", amtCents)
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
	type histRow struct {
		ext  string
		date string
	}
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
	return nil
}

// putSysField 只写系统字段：**不覆盖非空手填值**（user 字段误塞同名时保用户值 ——
// system 字段本就不该由前端可信，但覆盖为空值优先）。
func putSysField(fields map[string]any, key string, v any) {
	if cur, ok := fields[key]; ok && cur != nil && cur != "" {
		return
	}
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
