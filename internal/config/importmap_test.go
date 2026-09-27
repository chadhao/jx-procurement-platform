package config

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
)

// 本文件覆盖 Q1 闭合路径：配置映射（approval_code / field_id / ledger_type / threshold）的
// 校验与幂等导入。★ 关键性质：任一条不合规必须**整体拒绝**（不做部分导入）——因为模板订阅不到
// 事件时系统不会报错，只是永远没有数据，属"静默无数据"缺陷，必须在校验层拦死。

// fullSample 返回一份四类齐全、全部合法的导入载荷。
func fullSample() *ImportPayload {
	return &ImportPayload{
		ApprovalCode: []ImportApprovalCode{
			{Code: "APPROVAL-CODE-BA-01", DocType: "BA"},
			{Code: "APPROVAL-CODE-CT-01", DocType: "CT"},
		},
		FieldID: []ImportFieldID{
			{DocType: "BA", FieldID: "widget_ba_amount", FieldName: "申请金额", BizField: "amount_cents"},
			{DocType: "CT", FieldID: "widget_ct_supplier", FieldName: "供应商", BizField: "supplier_name"},
			{FieldID: "widget_common_remark", FieldName: "备注", BizField: "remark"}, // 全局映射
		},
		LedgerType: []ImportKV{
			{Key: "BA", Value: "L01"},
			{Key: "CT", Value: "L09"},
		},
		Threshold: []ImportKV{
			{Key: "split_supplier_month", Value: "1000"},
			{Key: "spot_check_range", Value: "800-1000"},
		},
		LedgerField: []ImportLedgerField{
			{LedgerType: "L01", FieldKey: "付款凭据号"},
			{LedgerType: "L01", FieldKey: "抽查状态", IsSensitive: false},
		},
	}
}

// TestImportValidateAcceptsValid 合法载荷必须通过校验。
func TestImportValidateAcceptsValid(t *testing.T) {
	if err := fullSample().Validate(); err != nil {
		t.Fatalf("合法载荷被拒: %v", err)
	}
}

// TestImportValidateRejects 逐条注入非法值，确认全部被拒（整体拒绝，不部分导入）。
func TestImportValidateRejects(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*ImportPayload)
		want   string
	}{
		{"approval_code 空", func(p *ImportPayload) { p.ApprovalCode[0].Code = "  " }, "code 不能为空"},
		{"doc_type 不在白名单", func(p *ImportPayload) { p.ApprovalCode[0].DocType = "ZZ" }, "doc_type 必须是"},
		{"approval_code 重复", func(p *ImportPayload) { p.ApprovalCode[1].Code = p.ApprovalCode[0].Code }, "approval_code 重复"},
		{"approval_code 占位符", func(p *ImportPayload) { p.ApprovalCode[0].Code = "REPLACE_ME_APPROVAL_CODE_BA" }, "占位符"},
		{"field_id 占位符", func(p *ImportPayload) { p.FieldID[0].FieldID = "REPLACE_ME_WIDGET" }, "占位符"},
		{"field_id 空", func(p *ImportPayload) { p.FieldID[0].FieldID = "" }, "field_id 不能为空"},
		{"biz_field 空", func(p *ImportPayload) { p.FieldID[0].BizField = "" }, "biz_field 不能为空"},
		{"field_id doc_type 非法", func(p *ImportPayload) { p.FieldID[0].DocType = "QQ" }, "doc_type 必须为空或"},
		{"field_id 同 doc_type 重复", func(p *ImportPayload) {
			p.FieldID = append(p.FieldID, ImportFieldID{DocType: "BA", FieldID: "widget_ba_amount", BizField: "x"})
		}, "field_id 重复"},
		{"ledger_type key 非单据类型", func(p *ImportPayload) { p.LedgerType[0].Key = "L01" }, "key（doc_type）必须是"},
		{"ledger_type value 非 L 系列", func(p *ImportPayload) { p.LedgerType[0].Value = "purchase" }, "value 必须是"},
		{"ledger_type 指向派生台账", func(p *ImportPayload) { p.LedgerType[0].Value = "L11" }, "派生表"},
		{"ledger_type 指向未启用台账", func(p *ImportPayload) { p.LedgerType[0].Value = "L12" }, "本期不启用"},
		{"ledger_type 指向主数据台账", func(p *ImportPayload) { p.LedgerType[0].Value = "L08" }, "主数据"},
		{"ledger_type 指向汇总台账", func(p *ImportPayload) { p.LedgerType[0].Value = "L10" }, "只读汇总"},
		// ★ 一对多（B47）：同一 doc_type 配**不同**台账是合法的（PR → L02 + L03），
		//   只有 (doc_type, ledger_type) **完全相同**才拒。
		{"ledger_type 完全相同重复", func(p *ImportPayload) { p.LedgerType[1] = p.LedgerType[0] }, "ledger_type 重复"},
		{"threshold 非数字", func(p *ImportPayload) { p.Threshold[0].Value = "一千" }, "必须是数字或"},
		{"threshold 区间倒置", func(p *ImportPayload) { p.Threshold[1].Value = "1000-800" }, "必须是数字或"},
		{"threshold key 空", func(p *ImportPayload) { p.Threshold[0].Key = " " }, "key 不能为空"},
		{"threshold 重复", func(p *ImportPayload) { p.Threshold[1].Key = p.Threshold[0].Key }, "threshold 重复"},
		{"ledger_field 台账键非法", func(p *ImportPayload) { p.LedgerField[0].LedgerType = "L99" }, "ledger_type 必须是"},
		{"ledger_field 字段名空", func(p *ImportPayload) { p.LedgerField[0].FieldKey = " " }, "field_key 不能为空"},
		{"ledger_field 占位符", func(p *ImportPayload) { p.LedgerField[0].FieldKey = "REPLACE_ME_k" }, "占位符"},
		{"ledger_field 重复", func(p *ImportPayload) { p.LedgerField[1] = p.LedgerField[0] }, "ledger_field 重复"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := fullSample()
			c.mutate(p)
			err := p.Validate()
			if err == nil {
				t.Fatalf("非法载荷未被拒")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("错误信息未含 %q，实际: %v", c.want, err)
			}
		})
	}
}

// TestImportPayloadRejectsUnknownField 未知字段必须报错（防拼错键名导致的静默丢配置）。
func TestImportPayloadRejectsUnknownField(t *testing.T) {
	_, err := ParseImportPayload([]byte(`{"approval_codes":[{"code":"X","doc_type":"BA"}]}`))
	if err == nil {
		t.Fatalf("未知顶层字段未被拒")
	}
	if !strings.Contains(err.Error(), "解析配置映射失败") {
		t.Fatalf("错误信息不符: %v", err)
	}
}

// TestLoadImportFileReportsPath 读取失败时错误须带文件路径（便于定位拼错的路径）。
func TestLoadImportFileReportsPath(t *testing.T) {
	_, err := LoadImportFile(filepath.Join(t.TempDir(), "nope.json"))
	if err == nil {
		t.Fatalf("不存在的文件未被拒")
	}
	if !strings.Contains(err.Error(), "nope.json") {
		t.Fatalf("错误信息未含路径: %v", err)
	}

	// 内容非法时同样带上路径。
	p := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(p, []byte(`{"approval_code":[{"code":"A","doc_type":"ZZ"}]}`), 0o644); err != nil {
		t.Fatalf("写临时文件失败: %v", err)
	}
	_, err = LoadImportFile(p)
	if err == nil || !strings.Contains(err.Error(), "bad.json") {
		t.Fatalf("非法内容错误信息未含路径: %v", err)
	}
}

// TestParseImportPayloadRoundTrip JSON → 结构体 → 字段齐全。
func TestParseImportPayloadRoundTrip(t *testing.T) {
	raw, err := json.Marshal(fullSample())
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	p, err := ParseImportPayload(raw)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(p.ApprovalCode) != 2 || len(p.FieldID) != 3 || len(p.LedgerType) != 2 ||
		len(p.Threshold) != 2 || len(p.LedgerField) != 2 {
		t.Fatalf("往返丢字段: %+v", p)
	}
}

// TestImportMappingsIdempotent 导入必须幂等：重跑两次不产生重复行，且可覆盖修正。
func TestImportMappingsIdempotent(t *testing.T) {
	db := storetest.NewDB(t)
	ctx := context.Background()
	p := fullSample()

	first, err := ImportMappings(ctx, db, p)
	if err != nil {
		t.Fatalf("首次导入失败: %v", err)
	}
	// 11 = 四类映射 9 条 + 台账字段定义 2 条（后者落 t_ledger_field_def，不落 t_config_mapping）。
	if first.Total() != 11 {
		t.Fatalf("首次导入条数 = %d, 期望 11", first.Total())
	}
	if n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_config_mapping`); n != 9 {
		t.Fatalf("首次导入后 t_config_mapping 行数 = %d, 期望 9", n)
	}
	if n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_ledger_field_def`); n != 2 {
		t.Fatalf("首次导入后 t_ledger_field_def 行数 = %d, 期望 2", n)
	}

	second, err := ImportMappings(ctx, db, p)
	if err != nil {
		t.Fatalf("二次导入失败: %v", err)
	}
	if second.Total() != 11 {
		t.Fatalf("二次导入条数 = %d, 期望 11", second.Total())
	}
	if n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_config_mapping`); n != 9 {
		t.Fatalf("二次导入后 t_config_mapping 行数 = %d, 期望仍为 9（幂等）", n)
	}
	if n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_ledger_field_def`); n != 2 {
		t.Fatalf("二次导入后 t_ledger_field_def 行数 = %d, 期望仍为 2（幂等）", n)
	}

	// 覆盖修正：同 key 改 value 后表内行数不变、值更新。
	p.LedgerType[0].Value = "L02"
	if _, err := ImportMappings(ctx, db, p); err != nil {
		t.Fatalf("覆盖导入失败: %v", err)
	}
	if n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_config_mapping`); n != 9 {
		t.Fatalf("覆盖导入后表内行数 = %d, 期望仍为 9", n)
	}
	ledger, err := loadSimpleMap(ctx, db, "ledger_type")
	if err != nil {
		t.Fatalf("回读 ledger_type 失败: %v", err)
	}
	if ledger["BA"] != "L02" {
		t.Fatalf("覆盖未生效: BA → %q, 期望 L02", ledger["BA"])
	}
}

// TestImportMappingsRejectsInvalidWithoutWriting 非法载荷必须一条都不写（整体拒绝）。
func TestImportMappingsRejectsInvalidWithoutWriting(t *testing.T) {
	db := storetest.NewDB(t)
	p := fullSample()
	p.Threshold[0].Value = "不是数字"

	if _, err := ImportMappings(context.Background(), db, p); err == nil {
		t.Fatalf("非法载荷未被拒")
	}
	if n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_config_mapping`); n != 0 {
		t.Fatalf("非法载荷写入了 %d 行, 期望 0 行（整体拒绝）", n)
	}
	if n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_ledger_field_def`); n != 0 {
		t.Fatalf("非法载荷写入了 %d 条字段定义, 期望 0 行（整体拒绝）", n)
	}
}

// TestImportMappingsReadback 导入后四类映射均可经 LoadMaps 正常反查——
// 这是「模板订阅不会静默无数据」的最后一道自证。
func TestImportMappingsReadback(t *testing.T) {
	db := storetest.NewDB(t)
	ctx := context.Background()
	if _, err := ImportMappings(ctx, db, fullSample()); err != nil {
		t.Fatalf("导入失败: %v", err)
	}

	maps, err := LoadMaps(ctx, db)
	if err != nil {
		t.Fatalf("装载映射失败: %v", err)
	}

	// ① approval_code → doc_type
	if dt, ok := maps.Approval.DocType("APPROVAL-CODE-BA-01"); !ok || dt != "BA" {
		t.Fatalf("approval_code 反查失败: %q %v", dt, ok)
	}
	if _, ok := maps.Approval.DocType("不存在的码"); ok {
		t.Fatalf("未配置的 approval_code 不应命中")
	}

	// ② (doc_type, field_id) → 业务字段；全局映射可兜底（TC-23 口径）
	if bf, ok := maps.Field.BizField("BA", "widget_ba_amount"); !ok || bf != "amount_cents" {
		t.Fatalf("field_id 精确匹配失败: %q %v", bf, ok)
	}
	if bf, ok := maps.Field.BizField("PR", "widget_common_remark"); !ok || bf != "remark" {
		t.Fatalf("field_id 全局兜底失败: %q %v", bf, ok)
	}
	if _, ok := maps.Field.BizField("PR", "widget_unknown"); ok {
		t.Fatalf("未配置字段不应命中")
	}

	// ③ doc_type → 台账类型（★ 一对多：B47）
	if lts := maps.LedgerTypesFor("CT"); len(lts) != 1 || lts[0] != "L09" {
		t.Fatalf("ledger_type 反查失败: %v", lts)
	}
	if lts := maps.LedgerTypesFor("QC"); len(lts) != 0 {
		t.Fatalf("未配置 doc_type 不应返回台账类型，实际 %v", lts)
	}
	if n := maps.LedgerMappingCount(); n != 2 {
		t.Fatalf("台账映射条数 = %d，期望 2", n)
	}

	// ④ 阈值（元 → 分）
	if c, ok := maps.ThresholdCents("split_supplier_month"); !ok || c != 100000 {
		t.Fatalf("阈值单值解析失败: %d %v", c, ok)
	}
	lo, hi, ok := maps.ThresholdRangeCents("spot_check_range")
	if !ok || lo != 80000 || hi != 100000 {
		t.Fatalf("阈值区间解析失败: %d-%d %v", lo, hi, ok)
	}
}

// TestUnconsumedThresholdKeys 「已登记但无消费端」的阈值键应被识别（假配置提示）。
func TestUnconsumedThresholdKeys(t *testing.T) {
	p := fullSample()
	got := p.UnconsumedThresholdKeys()
	if len(got) != 0 {
		t.Fatalf("样例阈值均已消费，不应有未消费项，实际 %v", got)
	}

	p.Threshold = append(p.Threshold, ImportKV{Key: "purchase_tier", Value: "1000-5000"})
	p.Threshold = append(p.Threshold, ImportKV{Key: "submit_workdays", Value: "3"})
	got = p.UnconsumedThresholdKeys()
	want := []string{"purchase_tier", "submit_workdays"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("UnconsumedThresholdKeys = %v, 期望 %v（升序）", got, want)
	}
	// 有消费端的键不得误报。
	for _, k := range got {
		if ConsumedThresholdKeys[k] {
			t.Fatalf("已消费的键 %s 被误报为未消费", k)
		}
	}
}

// TestLedgerTypesWhitelistMatchesDocTypes 台账映射的 key 域必须与 11 类单据一致（防两处白名单漂移）。
func TestLedgerTypesWhitelistMatchesDocTypes(t *testing.T) {
	if len(DocTypes) != 11 {
		t.Fatalf("DocTypes 应为 11 类，实际 %d", len(DocTypes))
	}
	if len(LedgerTypes) != 12 {
		t.Fatalf("LedgerTypes 应为 12 张，实际 %d", len(LedgerTypes))
	}
	seen := map[string]bool{}
	for _, d := range DocTypes {
		if seen[d] {
			t.Fatalf("DocTypes 有重复项: %s", d)
		}
		seen[d] = true
	}
	// 台账映射的 key 只接受 DocTypes：这里显式固化该约束。
	p := &ImportPayload{LedgerType: []ImportKV{{Key: "XX", Value: "L01"}}}
	if err := p.Validate(); err == nil {
		t.Fatalf("非单据类型 key 未被拒")
	}
}

// TestPerInstanceLedgerWhitelist 实例级台账白名单必须是 LedgerTypes 的真子集，
// 且恰好排除 L08 / L10 / L11 / L12（架构 §3.4 的非实例级台账）。
func TestPerInstanceLedgerWhitelist(t *testing.T) {
	if len(PerInstanceLedgerTypes) != 8 {
		t.Fatalf("实例级台账应为 8 张（L01–L07 + L09），实际 %d", len(PerInstanceLedgerTypes))
	}
	for _, lt := range PerInstanceLedgerTypes {
		if !inList(lt, LedgerTypes) {
			t.Fatalf("实例级台账 %s 不在 LedgerTypes 内", lt)
		}
	}
	for _, excluded := range []string{"L08", "L10", "L11", "L12"} {
		if inList(excluded, PerInstanceLedgerTypes) {
			t.Fatalf("非实例级台账 %s 不应在白名单内", excluded)
		}
		if aggregateLedgerHint[excluded] == "" {
			t.Fatalf("非实例级台账 %s 缺少成因说明（错误信息会变成无用的「不是实例级台账」）", excluded)
		}
	}
	// 每个非实例级台账都必须能被拦下。
	for _, excluded := range []string{"L08", "L10", "L11", "L12"} {
		p := &ImportPayload{LedgerType: []ImportKV{{Key: "BA", Value: excluded}}}
		if err := p.Validate(); err == nil {
			t.Fatalf("ledger_type 指向 %s 未被拒", excluded)
		}
	}
}

// 让 store 包在本文件有显式引用（避免仅经 storetest 间接依赖时被 goimports 误删）。
var _ = store.Migrate

// ---------- C3：登记了「无消费端」的 biz_field 必须**可见** ----------

// TestC3ReservedAndRemovedBizFieldsAreSurfaced 三档 biz_field 必须能被区分并**报出**。
//
// 背景（2026-09-27 静默审计 C3）：登记为透传但**无任何读取者**的字段，
// 被模板映射后值会落 ext_json 却无人消费 —— 典型的"配了却不生效"。
// 处置分两档：`Reserved`（口径先行，可见提示）与 `Removed`（规范位置本就不在模板）。
func TestC3ReservedAndRemovedBizFieldsAreSurfaced(t *testing.T) {
	// ① 预留档：仍属"已知字段"（不报拼写错），但要有**专门**提示
	if !IsKnownBizField("quote_refs") {
		t.Error("quote_refs 已登记为预留，应属已知字段（否则会被当成拼写错误）")
	}
	if !IsReservedBizField("quote_refs") {
		t.Error("quote_refs 应被识别为预留字段")
	}

	// ② 移除档：**不再**属已知字段 → 会落进「不参与规范列抽取」提示（可见）
	for _, name := range []string{"payment_ref", "actual_arrival_date"} {
		if IsKnownBizField(name) {
			t.Errorf("%s 已确认不该由模板映射，不应再是「已知字段」", name)
		}
		if RemovedBizFieldReason(name) == "" {
			t.Errorf("%s 缺少成因说明（提示会变成无意义的「未知字段」）", name)
		}
	}

	// ③ 正常透传字段：不得被误归入任一新档
	for _, name := range []string{"contract_no", "related_biz_no", "inspection_result"} {
		if IsReservedBizField(name) {
			t.Errorf("%s 有真实消费端，不该被判为预留", name)
		}
		if RemovedBizFieldReason(name) != "" {
			t.Errorf("%s 有真实消费端，不该被判为已移除", name)
		}
	}

	// ④ 载荷级：三档各自被挑出，互不混淆
	p := &ImportPayload{FieldID: []ImportFieldID{
		{DocType: "SS", FieldID: "w1", BizField: "quote_refs"},
		{DocType: "BA", FieldID: "w2", BizField: "payment_ref"},
		{DocType: "GR", FieldID: "w3", BizField: "actual_arrival_date"},
		{DocType: "CT", FieldID: "w4", BizField: "contract_no"},
		{DocType: "CT", FieldID: "w5", BizField: "amout"}, // 真拼写错误
	}}
	if got := p.ReservedUsedBizFields(); len(got) != 1 || got[0] != "quote_refs" {
		t.Errorf("预留字段挑选错误: %v", got)
	}
	got := p.RemovedUsedBizFields()
	if len(got) != 2 {
		t.Errorf("已移除字段挑选错误: %v", got)
	}
	// ★ 关键：`payment_ref` / `actual_arrival_date` **必须**出现在「不参与抽取」提示里
	//   （那是它们唯一能被看见的地方），而 `quote_refs` **不应**出现（它有自己的提示）。
	unknown := p.NonExtractableBizFields()
	for _, want := range []string{"payment_ref", "actual_arrival_date", "amout"} {
		if !inList(want, unknown) {
			t.Errorf("%s 应出现在「不参与规范列抽取」提示中，实际 %v", want, unknown)
		}
	}
	if inList("quote_refs", unknown) {
		t.Errorf("quote_refs 有专门提示，不该混进「不参与抽取」列表: %v", unknown)
	}
	if inList("contract_no", unknown) {
		t.Errorf("contract_no 有真实消费端，不该出现在提示里: %v", unknown)
	}
}

// ---------- B47：doc_type → ledger_type 一对多 ----------

// TestValidateAllowsLedgerOneToMany 同一 doc_type 配**多个不同**台账必须放行。
//
// ★ 这是 B47 的核心口径：工具表要求 `PR` 同时产生「采购需求与审批台账 L02」与
// 「采购经办登记台账 L03」两条记录。旧校验按 doc_type 唯一，会把第二条判为「重复」。
func TestValidateAllowsLedgerOneToMany(t *testing.T) {
	p := &ImportPayload{
		ApprovalCode: []ImportApprovalCode{{Code: "CODE-PR", DocType: "PR"}},
		LedgerType:   []ImportKV{{Key: "PR", Value: "L02"}, {Key: "PR", Value: "L03"}},
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("同一 doc_type 配两个台账应放行，实际被拒: %v", err)
	}
}

// TestImportLedgerOneToManyRoundTrip 一对多映射导入后必须**两条都在**，且可被读回。
func TestImportLedgerOneToManyRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := storetest.NewDB(t)

	p := &ImportPayload{
		ApprovalCode: []ImportApprovalCode{{Code: "CODE-PR", DocType: "PR"}},
		LedgerType: []ImportKV{
			{Key: "PR", Value: "L02"},
			{Key: "PR", Value: "L03"},
		},
	}
	res, err := ImportMappings(ctx, db, p)
	if err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	if res.LedgerType != 2 {
		t.Fatalf("导入计数 = %d，期望 2", res.LedgerType)
	}

	maps, err := LoadMaps(ctx, db)
	if err != nil {
		t.Fatalf("装载映射失败: %v", err)
	}
	lts := maps.LedgerTypesFor("PR")
	if len(lts) != 2 {
		t.Fatalf("PR 的台账 = %v，期望 2 条（L02 + L03）", lts)
	}
	if !inList("L02", lts) || !inList("L03", lts) {
		t.Fatalf("PR 的台账 = %v，期望同时含 L02 与 L03", lts)
	}
	// ★ 顺带钉住「台账映射条数」的计数口径：按 doc_type 计数会低报（1 ≠ 2）。
	if n := maps.LedgerMappingCount(); n != 2 {
		t.Fatalf("台账映射条数 = %d，期望 2（按 doc_type 计数会低报）", n)
	}
}

// TestImportNoStaleRowAfterValueChange 是本轮**最重要的回归断言**。
//
// 把 `map_value` 纳入唯一键后，「改 value」从「覆盖」变成「新增一行」。若不先清空，
// 旧行会残留 → 同一 doc_type 同时写两个台账（而配置文件里只写了一个）。
// 这条用例专门钉住「改 value 之后旧值必须消失」。
func TestImportNoStaleRowAfterValueChange(t *testing.T) {
	ctx := context.Background()
	db := storetest.NewDB(t)

	// 第一次：BA → L01
	p1 := &ImportPayload{
		ApprovalCode: []ImportApprovalCode{{Code: "CODE-BA", DocType: "BA"}},
		LedgerType:   []ImportKV{{Key: "BA", Value: "L01"}},
	}
	if _, err := ImportMappings(ctx, db, p1); err != nil {
		t.Fatalf("首次导入失败: %v", err)
	}

	// 第二次：BA → L02（改 value）
	p2 := &ImportPayload{
		ApprovalCode: []ImportApprovalCode{{Code: "CODE-BA", DocType: "BA"}},
		LedgerType:   []ImportKV{{Key: "BA", Value: "L02"}},
	}
	if _, err := ImportMappings(ctx, db, p2); err != nil {
		t.Fatalf("二次导入失败: %v", err)
	}

	maps, err := LoadMaps(ctx, db)
	if err != nil {
		t.Fatalf("装载映射失败: %v", err)
	}
	lts := maps.LedgerTypesFor("BA")
	if len(lts) != 1 || lts[0] != "L02" {
		t.Fatalf("改 value 后 BA 的台账 = %v，期望仅 [L02]（旧值 L01 必须被清掉，否则会同时写两个台账）", lts)
	}
	// 表内行数必须恒等于载荷规模（1 approval + 1 ledger = 2），不得因残留而 +1。
	if n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_config_mapping`); n != 2 {
		t.Fatalf("表内行数 = %d，期望 2（残留旧行说明全量替换失效）", n)
	}
}

// TestImportSameValueRepeatedIsIdempotent 同一文件重复导入必须幂等（行数不增）。
func TestImportSameValueRepeatedIsIdempotent(t *testing.T) {
	ctx := context.Background()
	db := storetest.NewDB(t)

	p := fullSample()
	if _, err := ImportMappings(ctx, db, p); err != nil {
		t.Fatalf("首次导入失败: %v", err)
	}
	first := storetest.Count(t, db, `SELECT COUNT(*) FROM t_config_mapping`)
	if _, err := ImportMappings(ctx, db, p); err != nil {
		t.Fatalf("重复导入失败: %v", err)
	}
	again := storetest.Count(t, db, `SELECT COUNT(*) FROM t_config_mapping`)
	if first != again {
		t.Fatalf("重复导入后行数 %d → %d，不幂等", first, again)
	}
}

// TestImportReportsEmptyKinds 载荷中为空的映射类必须被**报出**而非静默跳过。
//
// ★ 全量替换只作用于「载荷中出现的类」；某类为空时不动它 —— 这个决定必须是
// **可见的**，否则「以为清了其实没清」又是一次静默。
func TestImportReportsEmptyKinds(t *testing.T) {
	ctx := context.Background()
	db := storetest.NewDB(t)

	p := &ImportPayload{
		ApprovalCode: []ImportApprovalCode{{Code: "CODE-PR", DocType: "PR"}},
		LedgerType:   []ImportKV{{Key: "PR", Value: "L02"}},
	}
	res, err := ImportMappings(ctx, db, p)
	if err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	for _, want := range []string{"field_id", "threshold", "ledger_field"} {
		if !inList(want, res.EmptyKinds) {
			t.Errorf("空映射类 %s 未被报出，实际 %v", want, res.EmptyKinds)
		}
	}
	if inList("approval_code", res.EmptyKinds) || inList("ledger_type", res.EmptyKinds) {
		t.Errorf("非空的映射类不应出现在 EmptyKinds 中，实际 %v", res.EmptyKinds)
	}
	for _, want := range []string{"approval_code", "ledger_type"} {
		if !inList(want, res.ReplacedKinds) {
			t.Errorf("映射类 %s 应被记为已替换，实际 %v", want, res.ReplacedKinds)
		}
	}
}
