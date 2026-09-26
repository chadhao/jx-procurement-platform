package worker

import (
	"encoding/json"
	"math"
	"strings"

	"github.com/chadhao/jx-procurement-platform/internal/config"
	"github.com/chadhao/jx-procurement-platform/internal/platform/feishu"
)

// 规范字段抽取（FR-M2-08）。
//
// ★ 为什么必须有这一步：`field_id → biz_field` 映射本身只是"给字段起了个规范名"，
// 并不会把值搬到 `t_instance` 的规范列上。若不做抽取，则 `amount_cents` / `supplier` /
// `purpose_class_*` **恒为空**，直接后果：
//
//	· 看板全部金额类指标为 0（M7）；
//	· 「同供应商当月累计」防拆分视图失效（D7 / 工具表·看板 14）；
//	· 800–1,000 元重点抽查清单为空（D7 抽查 ≥50%）；
//	· 用途分类汇总台账（L10）无数据。
//
// 而这些失效**都不会报错**——正是本项目最危险的"静默无数据"缺陷类。故抽取与映射同批交付。
//
// ★ 覆盖语义：**只填空、不覆盖**。接口已给出的权威值（`serial_number` → `BizNo`、
// `open_id` → `ApplicantOpenID`）优先于表单控件值，避免表单里手填的单号把系统流水号覆盖掉。
//
// ★ 匹配语义：只按 `t_config_mapping` 的 `biz_field` **精确匹配**（大小写不敏感、去空白），
// **绝不按字段中文名模糊猜**（如"含'金额'二字就当金额"）——那会在模板改名时静默抽错列。

// ExtractDetail 按配置映射把表单字段抽取到实例详情的规范字段上（就地修改 det）。
// docType 为空时仅使用全局映射（doc_type=""）。
func ExtractDetail(maps *config.Maps, docType string, det *feishu.InstanceDetail) {
	if maps == nil || maps.Field == nil || det == nil || len(det.Fields) == 0 {
		return
	}
	for _, f := range det.Fields {
		biz, ok := maps.Field.BizField(docType, f.FieldID)
		if !ok {
			continue
		}
		applyBizField(det, f, strings.ToLower(strings.TrimSpace(biz)))
	}
}

// applyBizField 把单个控件值写入对应规范字段（只填空）。
func applyBizField(det *feishu.InstanceDetail, f feishu.FieldValue, biz string) {
	switch biz {
	case config.BizFieldBizNo:
		if strings.TrimSpace(det.BizNo) == "" {
			det.BizNo = strings.TrimSpace(f.ValueText)
		}

	case config.BizFieldAmount, config.BizFieldAmountCents:
		if det.AmountCents != nil {
			return
		}
		cents, ok := ParseAmountCents(f)
		if !ok {
			return
		}
		// 说明：`amount` 与 `amount_cents` 走同一解析（无法从值本身区分「元」还是「分」），
		// 金额单位以模板实际控件为准；导入映射时须在 remark 写明单位。
		det.AmountCents = &cents

	case config.BizFieldSupplier, config.BizFieldSupplierName:
		if strings.TrimSpace(det.Supplier) == "" {
			det.Supplier = strings.TrimSpace(f.ValueText)
		}

	case config.BizFieldPurposeL1:
		if strings.TrimSpace(det.PurposeClassL1) == "" {
			det.PurposeClassL1 = strings.TrimSpace(f.ValueText)
		}

	case config.BizFieldPurposeL2:
		if strings.TrimSpace(det.PurposeClassL2) == "" {
			det.PurposeClassL2 = strings.TrimSpace(f.ValueText)
		}

	case config.BizFieldDepartment:
		if strings.TrimSpace(det.Department) == "" {
			det.Department = strings.TrimSpace(f.ValueText)
		}

	case config.BizFieldApplicantName:
		if strings.TrimSpace(det.ApplicantName) == "" {
			det.ApplicantName = strings.TrimSpace(f.ValueText)
		}
	}
}

// amountObjectKeys 飞书金额类控件可能返回对象形态时，按这些键取数（首个可解析者胜）。
var amountObjectKeys = []string{"amount", "value", "number", "num", "total", "amount_cents"}

// ParseAmountCents 把控件值解析为「分」。
//
// 支持取值形态：
//
//	字符串 "1234.56" / "1,234.56" / "￥1,234.56 元"
//	数字   1234.56（valueToText 后为 "1234.56"）
//	对象   {"amount":1234.56} / {"value":"1234.56"}（飞书部分金额控件形态）
//
// 一律**按十进制字符串做整数运算**，不用 float64 累加，避免分位误差。
func ParseAmountCents(f feishu.FieldValue) (int64, bool) {
	if cents, ok := parseAmountCents(f.ValueText); ok {
		return cents, true
	}
	if cents, ok := parseAmountObject(f.RawJSON); ok {
		return cents, true
	}
	return 0, false
}

// parseAmountObject 尝试从 JSON 对象形态中取出金额。
func parseAmountObject(raw string) (int64, bool) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "{") {
		return 0, false
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		return 0, false
	}
	for _, k := range amountObjectKeys {
		v, ok := obj[k]
		if !ok {
			continue
		}
		switch t := v.(type) {
		case string:
			if cents, ok := parseAmountCents(t); ok {
				return cents, true
			}
		case float64:
			if cents, ok := centsFromFloat(t, isCentsKey(k)); ok {
				return cents, true
			}
		case json.Number:
			if cents, ok := parseAmountCents(t.String()); ok {
				return cents, true
			}
		}
	}
	return 0, false
}

// isCentsKey 判断对象键是否已是"分"单位。
func isCentsKey(k string) bool { return strings.HasSuffix(k, "_cents") }

// centsFromFloat 数字形态 → 分；alreadyCents 为真时视为已是分。
func centsFromFloat(v float64, alreadyCents bool) (int64, bool) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	if alreadyCents {
		if math.Abs(v) > math.MaxInt64/2 {
			return 0, false
		}
		return int64(math.Round(v)), true
	}
	// 元 → 分：先按字符串科学计数展开会失真，故仅接受有限范围并四舍五入到分。
	if math.Abs(v) > 1e13 {
		return 0, false
	}
	return int64(math.Round(v * 100)), true
}

// amountNoise 金额文本中的噪声字符（货币符号、千分位、单位）。
var amountNoise = strings.NewReplacer(
	",", "", "，", "", " ", "", "\u00a0", "",
	"￥", "", "¥", "", "$", "", "元", "", "人民币", "",
	"RMB", "", "rmb", "", "CNY", "", "cny", "",
)

// parseAmountCents 十进制字符串 → 分（第三位小数四舍五入）。
func parseAmountCents(raw string) (int64, bool) {
	s := strings.TrimSpace(amountNoise.Replace(strings.TrimSpace(raw)))
	if s == "" {
		return 0, false
	}

	neg := false
	switch s[0] {
	case '-':
		neg, s = true, s[1:]
	case '+':
		s = s[1:]
	}
	if s == "" {
		return 0, false
	}

	intPart, fracPart := s, ""
	if i := strings.IndexByte(s, '.'); i >= 0 {
		intPart, fracPart = s[:i], s[i+1:]
	}
	if intPart == "" {
		intPart = "0"
	}
	if !allDigits(intPart) || (fracPart != "" && !allDigits(fracPart)) {
		return 0, false
	}

	cents := int64(0)
	for i := 0; i < len(intPart); i++ {
		d := int64(intPart[i] - '0')
		if cents > (math.MaxInt64-d)/10 {
			return 0, false
		}
		cents = cents*10 + d
	}
	if cents > math.MaxInt64/100 {
		return 0, false
	}
	cents *= 100

	// 补齐到 3 位，用第 3 位做四舍五入（避免 float 分位误差）。
	pad := fracPart + "000"
	cents += int64(pad[0]-'0')*10 + int64(pad[1]-'0')
	if pad[2] >= '5' {
		cents++
	}
	if neg {
		cents = -cents
	}
	return cents, true
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// BuildExtJSON 把**已映射**的表单字段汇总为台账存档行的 `ext_json`。
//
// ★ 为什么需要：台账的稳定列只覆盖金额/供应商/用途/部门等热字段，而**关联合同号**这类字段
// 不是规范列。它必须落 `ext_json` 才能被 `json_each` 检索——变更链回溯（FR-M4-07）正是按
// 关联合同号匹配（`store.ListChangesByContract`）。原实现写死 `ext_json = "{}"`，
// 导致变更链**恒为空且不报错**。
//
// ★ 只收已映射字段：未映射的控件不进 `ext_json`，避免把模板噪声全量灌进台账。
// ★ 文本值存为 JSON 字符串（保证 `json_each(...).value = '<合同号>'` 能等值命中）；
// 控件原始值本身是对象/数组时保留结构。
func BuildExtJSON(maps *config.Maps, docType string, fields []feishu.FieldValue) string {
	if maps == nil || maps.Field == nil || len(fields) == 0 {
		return "{}"
	}
	out := make(map[string]any, len(fields))
	for _, f := range fields {
		biz, ok := maps.Field.BizField(docType, f.FieldID)
		if !ok {
			continue
		}
		biz = strings.ToLower(strings.TrimSpace(biz))
		if biz == "" {
			continue
		}
		out[biz] = extValue(f)
	}
	if len(out) == 0 {
		return "{}"
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// extValue 取控件的可检索形态。
func extValue(f feishu.FieldValue) any {
	raw := strings.TrimSpace(f.RawJSON)
	if strings.HasPrefix(raw, "{") || strings.HasPrefix(raw, "[") {
		var v any
		if err := json.Unmarshal([]byte(raw), &v); err == nil {
			return v
		}
	}
	return f.ValueText
}
