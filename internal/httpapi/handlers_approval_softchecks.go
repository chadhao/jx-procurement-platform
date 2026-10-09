package httpapi

// soft 判据的统一执行体（N-062 J2 · 「提示通道」）。
//
// ★ 根因（已实测）：evaluateHardChecks 只对 severity==hard 放行、其余 `continue`
//   ⇒ 所有 soft 判据从未被执行（「声明了却不发生」）。本文件补齐通道并保证四条：
//   ① 提交期被执行（注册表 submitSoftChecks，按 when 白名单与 hard 同款）；
//   ② 结果可见 —— SoftWarning[] 随提交成功响应 data.warnings 返回 ＋ 前端呈现；
//   ③ **不阻断**（soft 永不改变 200/400 判定 —— 「超期不予受理」≠「超期计入预警」，
//      SUB#submit_deadline_warning.critical_note 铁律）；
//   ④ 与 hard 严格分流（severity 驱动，互不串台）。
//
// ★ 未注册的 soft ⇒ **响亮可见但不阻断**：追加一条 id=该判据的 warning ＋ Error 日志
//   （与 hard 的 fail-closed 相对：hard 缺执行体＝拦截失效必须拒启；soft 缺执行体＝
//    提示失效，报 warning 而非拒绝提交 —— 拒绝提交本身就违反③）。
//
// SA#counterparty_conditional 的触发条件**只表达一半**（口径依 forms/SA.json#checks
// carried_by：「对外支付」＝payment_method_input=='对公直付' 可判；「需要开票」无字段
// ⇒ 本通道不猜测该分支，N-054 ② 同源、回执点名）。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/flow"
	"github.com/chadhao/jx-procurement-platform/internal/specload"
	"github.com/chadhao/jx-procurement-platform/internal/submission"
)

// SoftWarning 提交成功响应里的单条提示（data.warnings[*]）。
type SoftWarning struct {
	ID      string `json:"id"`
	Message string `json:"message"`
}

// softCheckFn soft 判据求值器：返回 (提示文案, 是否命中)。永不返回 error ——
// 求值失败按「不误报」跳过（可见性由判据自身数据质量负责），与 hard 的 fail-closed 分野。
type softCheckFn func(ctx context.Context, d Deps, body *approvalSubmitBody) (string, bool)

// submitSoftChecks soft 判据注册表（与 submitHardChecks 同族；id → 求值器）。
var submitSoftChecks = map[string]softCheckFn{
	"inspection_vs_conclusion_hint":  softGRInspectionHint,
	"no_duplicate_qc_for_same_batch": softQCDuplicateForSameBatch,
	"response_shortfall_warning":     softRFQResponseShortfall,
	"counterparty_conditional":       softSACounterpartyConditional,
	"fixed_asset_conflict":           softSSFixedAssetConflict,
	"submit_deadline_warning":        softSUBSubmitDeadline,
}

// evaluateSoftChecks 执行提交时点 soft 判据（唯一入口；severity/when 分流与 hard 同款）。
func (d Deps) evaluateSoftChecks(ctx context.Context, form specload.FormDoc, body *approvalSubmitBody) []SoftWarning {
	var out []SoftWarning
	for _, c := range form.Checks {
		if c.Severity != "soft" {
			continue
		}
		if !submitWhenEligible(c.When) {
			continue
		}
		fn, ok := submitSoftChecks[c.ID]
		if !ok {
			// 响亮可见但不阻断（见头注④）。
			if d.Log != nil {
				d.Log.Error("soft 判据未注册执行体（提示失效，须补 submitSoftChecks）", "id", c.ID)
			}
			out = append(out, SoftWarning{ID: c.ID,
				Message: "内部提示：soft 判据未注册执行体（" + c.ID + "）"})
			continue
		}
		if msg, hit := fn(ctx, d, body); hit {
			out = append(out, SoftWarning{ID: c.ID, Message: msg})
		}
	}
	return out
}

// submitWhenEligible 提交时点白名单（与 evaluateHardChecks 同一判据 —— 抽出共用，
// 防两处漂移）："submit" / "submit …" 前缀 / 含「提交前」。
func submitWhenEligible(when string) bool {
	w := strings.TrimSpace(when)
	return w == "submit" || strings.HasPrefix(w, "submit ") || strings.Contains(w, "提交前")
}

// softGRInspectionHint GR#inspection_vs_conclusion_hint：
// 关联 QC 判「不合格」而本单验收结论＝「合格入库」⇒ 提示写差异说明。
// ★ 关联方向＝QC 指向 GR（qc.related_biz_no 从已提交 GR 带入）⇒ 首提的 GR 尚无 QC
//
//	可查；只在**重提**（PrevBizNo 非空）时按上一轮单号反查 —— 首提天然跳过（不误报）。
func softGRInspectionHint(ctx context.Context, d Deps, body *approvalSubmitBody) (string, bool) {
	if body.DocType != "GR" {
		return "", false
	}
	if strField(body.Fields, "acceptance_conclusion") != "合格入库" {
		return "", false
	}
	prev := strings.TrimSpace(body.PrevBizNo)
	if prev == "" {
		return "", false // 首提：GR 尚不存在 ⇒ 不可能有指向它的 QC
	}
	rows, err := d.DB.QueryContext(ctx, `
SELECT biz_no, COALESCE(ext_json,'{}') FROM t_instance
WHERE doc_type='QC' AND status != ? AND json_valid(ext_json)
  AND json_extract(ext_json,'$.related_biz_no') = ?`, flow.InstanceCanceled, prev)
	if err != nil {
		return "", false // 查询失败不误报
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var biz, extRaw string
		if err := rows.Scan(&biz, &extRaw); err != nil {
			return "", false
		}
		ext := map[string]any{}
		if json.Unmarshal([]byte(extRaw), &ext) != nil {
			continue
		}
		if s, _ := ext["inspection_result"].(string); s == "不合格" {
			return fmt.Sprintf("关联检验（%s）判定为不合格，本单判合格入库，请在差异说明中说明理由", biz), true
		}
	}
	return "", false
}

// softQCDuplicateForSameBatch QC#no_duplicate_qc_for_same_batch：
// 同 related_biz_no ＋ 同 batch_no 已存在未作废 QC ⇒ 提示确认是否重复。
// ★ 批号两空亦按字面等值（判据明写「两者表象相同只能提示」—— 一对多是设计、
//
//	重复录入是风险，不额外发明「批号非空才判」的限制）。
func softQCDuplicateForSameBatch(ctx context.Context, d Deps, body *approvalSubmitBody) (string, bool) {
	if body.DocType != "QC" {
		return "", false
	}
	related := strField(body.Fields, "related_biz_no")
	if related == "" {
		return "", false // 无关联锚点不误报（存在性由 hard 判据负责）
	}
	batch := strField(body.Fields, "batch_no")
	rows, err := d.DB.QueryContext(ctx, `
SELECT biz_no, COALESCE(ext_json,'{}') FROM t_instance
WHERE doc_type='QC' AND status != ? AND json_valid(ext_json)
  AND json_extract(ext_json,'$.related_biz_no') = ?`, flow.InstanceCanceled, related)
	if err != nil {
		return "", false
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var biz, extRaw string
		if err := rows.Scan(&biz, &extRaw); err != nil {
			return "", false
		}
		ext := map[string]any{}
		if json.Unmarshal([]byte(extRaw), &ext) != nil {
			continue
		}
		if b, _ := ext["batch_no"].(string); b == batch {
			return fmt.Sprintf("该批已有检验记录（%s），确认是否重复", biz), true
		}
	}
	return "", false
}

// softRFQResponseShortfall RFQ#response_shortfall_warning：
// responded_count < 3（采三档）⇒ 提示（缺该字段＝无数据不误报；字面判据、不看
// shortfall_note 是否已填 —— 提示本身即 else 文案）。
func softRFQResponseShortfall(_ context.Context, _ Deps, body *approvalSubmitBody) (string, bool) {
	if body.DocType != "RFQ" {
		return "", false
	}
	v, ok := toFloat64(body.Fields["responded_count"])
	if !ok {
		return "", false
	}
	if v >= 3 {
		return "", false
	}
	return "实际报价不足 3 家，请说明原因（若确属市场独家，应考虑改走单一来源或竞争性谈判）", true
}

// softSACounterpartyConditional SA#counterparty_conditional：
// 对外支付（payment_method_input='对公直付'）下 counterparty 为空 ⇒ 提示。
// ★ 「需要开票」分支无字段承载（forms/SA.json#checks carried_by · N-054 ② 同源）
//
//	⇒ 不猜测、不实现该分支（回执点名）。
func softSACounterpartyConditional(_ context.Context, _ Deps, body *approvalSubmitBody) (string, bool) {
	if body.DocType != "SA" {
		return "", false
	}
	pm := firstNonEmptyStr(body.PaymentMethodInput, strField(body.Fields, "payment_method_input"))
	if pm != "对公直付" {
		return "", false
	}
	if strField(body.Fields, "counterparty") != "" {
		return "", false
	}
	return "对外支付（对公直付）下对方单位为空，请补填交易对手（提示不阻断）", true
}

// softSSFixedAssetConflict SS#fixed_asset_conflict：
// assert ＝ NOT(is_fixed_asset ∧ amount_cents>50000000) ⇒ 两条件同时成立才提示
// （金额 50000000 分 = 50 万元；单条件不触发 —— 严格按 assert，不按 else 文案的口语）。
func softSSFixedAssetConflict(_ context.Context, _ Deps, body *approvalSubmitBody) (string, bool) {
	if body.DocType != "SS" {
		return "", false
	}
	if !boolFromBodyField(body.Fields, "is_fixed_asset") {
		return "", false
	}
	var amt int64
	if body.AmountCents != nil {
		amt = *body.AmountCents
	} else if v, ok := toFloat64(body.Fields["amount_cents"]); ok {
		amt = int64(v)
	}
	if amt <= 50000000 {
		return "", false
	}
	return "超 20 万元或固定资产类应走招标／竞谈，不应使用单一来源", true
}

// softSUBSubmitDeadline SUB#submit_deadline_warning：
// 距 hunan_completed_at 超 deadline_workdays 个工作日 ⇒ 提示计入异常预警（绝不阻断）。
// ★ 基线 hunan_completed_at 随本次载荷带入（SUB 同步存档列 —— forms/SUB.json#ledger_resolution
//
//	「由本单提交时一次性写入」）；缺失/不可解析/阈值未装配 ⇒ 跳过（不误报，同 G2 面）。
//
// ★ 工作日口径复用 submission.AddWorkingDays（跳周六日＋HolidayChecker 挂点，Q18）；
//
//	阈值取 doc_chains.SUB.deadline_workdays（spec 唯一真相，不写死 3）。
func softSUBSubmitDeadline(_ context.Context, d Deps, body *approvalSubmitBody) (string, bool) {
	if body.DocType != "SUB" {
		return "", false
	}
	done := strField(body.Fields, "hunan_completed_at")
	if done == "" {
		return "", false
	}
	d0, ok := submission.ParseDate(done)
	if !ok {
		return "", false
	}
	deadline := d.Spec.Chain.DocChains["SUB"].DeadlineWorkdays
	if deadline <= 0 {
		return "", false // 未装配不猜
	}
	if !time.Now().After(submission.AddWorkingDays(d0, deadline)) {
		return "", false
	}
	return fmt.Sprintf("已超 %d 个工作日未提交集团，将计入异常预警", deadline), true
}
