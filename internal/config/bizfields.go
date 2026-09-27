package config

import (
	"sort"
	"strings"
)

// 规范业务字段名（`t_config_mapping` 中 `map_kind='field_id'` 的 `map_value` 取值域）。
//
// ★ 为什么需要一份"取值域"：`field_id → biz_field` 映射的作用是把飞书表单控件的值
// **抽取到 `t_instance` 的规范列**（金额、供应商、用途分类、部门）。若 `biz_field` 写错一个字母
// （如 `amount_cnet`），后果不是报错，而是金额列**恒空** → 看板金额、防拆分「同供应商当月累计」、
// 800–1000 元抽查清单**全部失效**，且没有任何报错。故本层对可抽取字段名做显式登记，
// 导入时对未登记的名字给出提示（不阻断——业务可能需要新增字段）。
//
// 未登记的 `biz_field` 仍会落 `t_instance_field`（TC-23：落库但不进业务列），只是不参与抽取。
const (
	// BizFieldBizNo 业务单号（飞书流水号控件一般已直接给出 serial_number，此处为兜底）。
	BizFieldBizNo = "biz_no"
	// BizFieldAmount 金额（元，按分入库）。
	BizFieldAmount = "amount"
	// BizFieldAmountCents 金额别名（若模板直接给"分"）。
	BizFieldAmountCents = "amount_cents"
	// BizFieldSupplier 供应商名称。
	BizFieldSupplier = "supplier"
	// BizFieldSupplierName 供应商名称别名。
	BizFieldSupplierName = "supplier_name"
	// BizFieldPurposeL1 用途一级分类。
	BizFieldPurposeL1 = "purpose_class_l1"
	// BizFieldPurposeL2 用途二级分类。
	BizFieldPurposeL2 = "purpose_class_l2"
	// BizFieldDepartment 申请部门（部门控件若返回部门 id，则原样存文本）。
	BizFieldDepartment = "department"
	// BizFieldApplicantName 申请人姓名（open_id 以接口返回为准，不由此覆盖）。
	BizFieldApplicantName = "applicant_name"
	// BizFieldContractNo 关联合同号（如采购变更单 → 原合同）。★ 变更链回溯的**唯一键**（Q14 已定稿）。
	BizFieldContractNo = "contract_no"
	// BizFieldRelatedBizNo 关联单号：指向同链条上一张单据（如 QC 来料检验报告 → 其 GR 入库单）。
	// ★ 单据间「跨单据关联」一律用本键，不再逐单据各造键名（Q14 定稿）。
	BizFieldRelatedBizNo = "related_biz_no"
	// BizFieldInspectionResult 检验结论（QC 来料检验报告的判定结论）。
	// ★ L07 到货验收台账把 QC 的结论并到 GR 行展示，靠本键取值（Q14-B 第 1 项：各写一行 + 查询侧关联）。
	BizFieldInspectionResult = "inspection_result"
	// BizFieldRemark 备注。
	BizFieldRemark = "remark"
)

// ExtractableBizFields 会被抽取到 `t_instance` 规范列的 `biz_field` 名。
// 其余名字合法但只落 `t_instance_field`。
var ExtractableBizFields = map[string]bool{
	BizFieldBizNo:         true,
	BizFieldAmount:        true,
	BizFieldAmountCents:   true,
	BizFieldSupplier:      true,
	BizFieldSupplierName:  true,
	BizFieldPurposeL1:     true,
	BizFieldPurposeL2:     true,
	BizFieldDepartment:    true,
	BizFieldApplicantName: true,
}

// PassthroughBizFields 不进规范列、但要写入台账 `ext_json` 的 `biz_field` 名。
//
// ★ 为什么单独一类：变更单按**关联合同号**回溯（FR-M4-07 / 制度第五十二条），而合同号不是
// 规范列。它必须落 `t_ledger_archive.ext_json` 才能被检索到；若既不进列也不进
// `ext_json`，变更链会**恒为空且不报错**。故此类字段必须显式登记，不能靠"未登记就丢弃"。
//
// ★ Q14 定稿（2026-09-26）：单据间关联键收敛为**单一键名**——合同链用 `contract_no`，
// 其余上下游关系用 `related_biz_no`。键名自此是**契约**，不再做「任意键名等值匹配」。
var PassthroughBizFields = map[string]bool{
	BizFieldContractNo:       true,
	BizFieldRelatedBizNo:     true,
	BizFieldInspectionResult: true,
	BizFieldRemark:           true,
	"acceptors":              true,
	"assigned_open_id":       true,
	"invoice_count":          true,
	"handover_date":          true,
	"inspection_item":        true,
	"inspection_standard":    true,
	"delivery_date":          true,
}

// ReservedBizFields 已登记、但**当前没有任何消费端**的透传字段。
//
// ★ 为什么单独一类（2026-09-27 静默审计 C3 的处置）：这类名字既不是拼写错误、
// 也不是"不参与抽取"，而是**口径先行、功能后到**。若混在 PassthroughBizFields 里，
// 操作员把模板控件映射到它 → 值落进 ext_json → **却无人读取**，静默失效。
// 故单列一档，导入时给**专门的非阻断提示**（与 `ConsumedThresholdKeys` 的"假配置"
// 提示同一思路），让"配了但没生效"这件事**可见**。
//
// 移出本表 = 该字段已有消费端；移入本表 = 消费端被移除。
var ReservedBizFields = map[string]bool{
	// 询比价依据引用（询价单/比价单号）。将来做"报价依据可追溯"时启用。
	"quote_refs": true,
}

// RemovedBizFields 曾经登记、**已确认不该由模板映射**的名字（保留说明供排查）。
//
// ★ 这两个不是"暂时没用"，而是**规范位置本来就不在模板**：
//   - `payment_ref`（L01 付款凭据号）→ 按 Q14 定稿它属**台账运营表可写字段**
//     （`t_ledger_field_def` 已为 L01 登记「付款凭据号」），应在台账页登记，
//     映射到模板只会写进**只读的存档 ext_json**，反而拿不到。
//   - `actual_arrival_date`（实际到货）→ L11/看板的「实际到货」由
//     **GR 的业务日期派生**（键名 `actual_arrival`，见 `handlers_ledger_derived.go`），
//     不是模板字段；映射它同样不会生效。
//
// 移除登记后，若有人仍映射这两个名字，导入会打「不参与规范列抽取」提示（可见），
// 而不是静默落库。
var RemovedBizFields = map[string]string{
	"payment_ref":         "属 L01 台账**运营表**可写字段（在台账页登记），不由模板映射",
	"actual_arrival_date": "「实际到货」由 GR 业务日期派生（键名 actual_arrival），不由模板映射",
}

// IsExtractableBizField 判断某 biz_field 是否参与规范列抽取。
func IsExtractableBizField(name string) bool {
	return ExtractableBizFields[strings.TrimSpace(name)]
}

// IsKnownBizField 判断某 biz_field 是否已被登记（抽取列 / 透传 ext_json / 预留）。
func IsKnownBizField(name string) bool {
	n := strings.TrimSpace(name)
	return ExtractableBizFields[n] || PassthroughBizFields[n] || ReservedBizFields[n]
}

// IsReservedBizField 判断某 biz_field 是否为「已登记但当前无消费端」。
func IsReservedBizField(name string) bool {
	return ReservedBizFields[strings.TrimSpace(name)]
}

// RemovedBizFieldReason 返回「已移除登记」的字段的成因说明（非本表则返回空串）。
func RemovedBizFieldReason(name string) string {
	return RemovedBizFields[strings.TrimSpace(name)]
}

// ExtractableBizFieldNames 返回全部可抽取的 biz_field 名（升序，供提示与文档同步）。
func ExtractableBizFieldNames() []string {
	out := make([]string, 0, len(ExtractableBizFields))
	for k := range ExtractableBizFields {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// PassthroughBizFieldNames 返回全部透传 ext_json 的 biz_field 名（升序）。
func PassthroughBizFieldNames() []string {
	out := make([]string, 0, len(PassthroughBizFields))
	for k := range PassthroughBizFields {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// NonExtractableBizFields 返回既未登记为抽取列、也未登记为透传的 `biz_field`（去重、保序）。
// 这类名字**多为拼写错误**，导入时应当提示。
func (p *ImportPayload) NonExtractableBizFields() []string {
	if p == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, e := range p.FieldID {
		name := strings.TrimSpace(e.BizField)
		if name == "" || seen[name] || IsKnownBizField(name) {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// ReservedUsedBizFields 返回载荷中使用的「已登记但无消费端」字段（去重、保序）。
func (p *ImportPayload) ReservedUsedBizFields() []string {
	return p.pickBizFields(IsReservedBizField)
}

// RemovedUsedBizFields 返回载荷中使用的「已确认为不该由模板映射」的字段（去重、保序）。
func (p *ImportPayload) RemovedUsedBizFields() []string {
	return p.pickBizFields(func(n string) bool { return RemovedBizFieldReason(n) != "" })
}

// pickBizFields 按谓词挑选载荷里出现过的 biz_field（去重、保序）。
func (p *ImportPayload) pickBizFields(pred func(string) bool) []string {
	if p == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, e := range p.FieldID {
		name := strings.TrimSpace(e.BizField)
		if name == "" || seen[name] || !pred(name) {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// ---------- 台账可写性口径（Q14 定稿 2026-09-26） ----------

// DerivedLedgerTypes 派生台账：**不接收实例级落账**，其行在查询时由其他台账聚合产生。
//
//	L11 订单执行台账 = 派生视图（数据源＝CT + GR）→ 不落行、无写入口；
//	（L10 用途分类汇总台账 = 只读汇总，维持现状、不做派生视图——汇总口径由看板承担；
//	  L12 预算执行台账 = 保留结构，本期不启用。）
//
// 与 `PerInstanceLedgerTypes` 互为呼应：导入层拒绝把单据映射到 L11，查询层则改为派生。
var DerivedLedgerTypes = []string{"L11"}

// IsDerivedLedger 判断某台账类型是否为派生视图。
func IsDerivedLedger(ledgerType string) bool {
	return inList(ledgerType, DerivedLedgerTypes)
}

// ReadOnlyLedgerTypes 无写入口的台账：派生视图 / 只读汇总 / 本期未启用。
var ReadOnlyLedgerTypes = []string{"L10", "L11", "L12"}

// IsReadOnlyLedger 判断某台账类型是否**不接受写入**（派生视图亦在内）。
func IsReadOnlyLedger(ledgerType string) bool {
	return inList(ledgerType, ReadOnlyLedgerTypes)
}
