package worker

import (
	"context"
	"testing"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/config"
	"github.com/chadhao/jx-procurement-platform/internal/platform/feishu"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
)

// 本文件为 QA 独立验证（新增，不修改既有文件）：
//
//	B32 ① 走**真实写入路径** `Ingest`（而非直接 UpsertArchive）后，`t_ledger_archive.biz_date`
//	     必须非空且形如 `YYYY-MM-DD`。
//
// 反例（若 B32 回退）：`Ingest` 从不写 biz_date → 该列恒为 NULL → 「同供应商当月累计」
// 与看板全部按月指标按**整年**统计，且无任何报错（静默错数）。本用例读库直断该列。

// qaB32Config 构造一份最小但**合法**的配置映射（approval_code / field_id / ledger_type / threshold），
// 经真实导入通道落库，再由 LoadMaps 装载——避免在测试里硬编码内部结构体。
func qaB32Config(t *testing.T) *config.Maps {
	t.Helper()
	db := storetest.NewDB(t)
	payload := &config.ImportPayload{
		ApprovalCode: []config.ImportApprovalCode{{Code: "ac-qa-ct", DocType: "CT"}},
		FieldID: []config.ImportFieldID{
			{DocType: "CT", FieldID: "w_amt", FieldName: "合同金额", BizField: "amount_cents"},
			{DocType: "CT", FieldID: "w_sup", FieldName: "供应商", BizField: "supplier"},
			{DocType: "CT", FieldID: "w_contract", FieldName: "关联合同号", BizField: "contract_no"},
		},
		LedgerType: []config.ImportKV{{Key: "CT", Value: "L04"}},
		Threshold:  []config.ImportKV{{Key: "split_supplier_month", Value: "1000"}},
	}
	if _, err := config.ImportMappings(context.Background(), db, payload); err != nil {
		t.Fatalf("导入配置映射失败: %v", err)
	}
	maps, err := config.LoadMaps(context.Background(), db)
	if err != nil {
		t.Fatalf("装载映射失败: %v", err)
	}
	return maps
}

// TestQAB32IngestWritesBizDateISO 用真实 Ingest 证明 biz_date 被写入且为 YYYY-MM-DD。
func TestQAB32IngestWritesBizDateISO(t *testing.T) {
	// 独立建库（不依赖上面 helper 的临时库），以便读回同一库。
	db := storetest.NewDB(t)
	ctx := context.Background()

	payload := &config.ImportPayload{
		ApprovalCode: []config.ImportApprovalCode{{Code: "ac-qa-ct", DocType: "CT"}},
		FieldID: []config.ImportFieldID{
			{DocType: "CT", FieldID: "w_amt", FieldName: "合同金额", BizField: "amount_cents"},
			{DocType: "CT", FieldID: "w_sup", FieldName: "供应商", BizField: "supplier"},
			{DocType: "CT", FieldID: "w_contract", FieldName: "关联合同号", BizField: "contract_no"},
		},
		LedgerType: []config.ImportKV{{Key: "CT", Value: "L04"}},
		Threshold:  []config.ImportKV{{Key: "split_supplier_month", Value: "1000"}},
	}
	if _, err := config.ImportMappings(ctx, db, payload); err != nil {
		t.Fatalf("导入配置映射失败: %v", err)
	}
	maps, err := config.LoadMaps(ctx, db)
	if err != nil {
		t.Fatalf("装载映射失败: %v", err)
	}

	g := NewIngestor(db, maps, nil)
	occurred := time.Date(2026, 9, 15, 10, 30, 0, 0, time.UTC)
	det := &feishu.InstanceDetail{
		InstanceCode:    "I-QA-B32-1",
		ApprovalCode:    "ac-qa-ct",
		StatusRaw:       "APPROVED",
		BizNo:           "CT-2609-9001",
		ApplicantOpenID: "ou_a",
		Department:      "生产部",
		OccurredAt:      occurred,
		Fields: []feishu.FieldValue{
			{FieldID: "w_amt", ValueText: "1234.56"},
			{FieldID: "w_sup", ValueText: "某某五金"},
			{FieldID: "w_contract", ValueText: "CT-2609-9001"},
		},
	}
	if err := g.Ingest(ctx, det, SourceEvent); err != nil {
		t.Fatalf("Ingest 失败: %v", err)
	}

	var (
		bizDate  string
		amount   int64
		supplier string
		extJSON  string
	)
	if err := db.QueryRowContext(ctx,
		`SELECT COALESCE(biz_date,''), COALESCE(amount_cents,0), COALESCE(supplier,''), COALESCE(ext_json,'')
		 FROM t_ledger_archive WHERE ledger_type='L04' AND biz_no='CT-2609-9001'`).
		Scan(&bizDate, &amount, &supplier, &extJSON); err != nil {
		t.Fatalf("回读台账存档失败: %v", err)
	}
	if bizDate == "" {
		t.Fatalf("biz_date 为空 —— Ingest 未写入业务日期（B32 未生效 / 回退）")
	}
	if _, err := time.Parse("2006-01-02", bizDate); err != nil {
		t.Fatalf("biz_date=%q 不是 YYYY-MM-DD 格式: %v", bizDate, err)
	}
	if bizDate != "2026-09-15" {
		t.Errorf("biz_date=%q, 期望 2026-09-15（取实例发生日期）", bizDate)
	}
	// 顺带证明：映射结果确实被消费（规范列抽取 + ext_json 透传）。
	if amount != 123456 {
		t.Errorf("amount_cents=%d, 期望 123456（field_id→biz_field 抽取未被消费？）", amount)
	}
	if supplier != "某某五金" {
		t.Errorf("supplier=%q, 期望 某某五金", supplier)
	}
	if extJSON == "" || extJSON == "{}" {
		t.Errorf("ext_json=%q, 期望含已映射字段（变更链按合同号检索依赖它）", extJSON)
	}
}

// TestQAParseAmountCentsBoundaries 金额解析的边界（真实代码路径 ParseAmountCents）。
//
// 覆盖：分位（999.99 / 1000.00 / 1000.01 / 4999.99 / 5000.00 / 5000.01）、千分位/货币符号、
// 0（★ PRD Q21：0 被接受，不得谎报为已拒绝）、负数（解析层接受，拒绝发生在接口层）、非法文本、溢出。
func TestQAParseAmountCentsBoundaries(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		ok   bool
		note string
	}{
		{"999.99", 99999, true, ""},
		{"1000.00", 100000, true, ""},
		{"1000.01", 100001, true, ""},
		{"4999.99", 499999, true, ""},
		{"5000.00", 500000, true, ""},
		{"5000.01", 500001, true, ""},
		{"0", 0, true, "★ 0 被接受（PRD Q21）"},
		{"0.00", 0, true, "★ 0 被接受（PRD Q21）"},
		{"-1.00", -100, true, "解析层接受负数；拒绝在接口层（submission/reimbursement）"},
		{"1,234.56", 123456, true, "千分位"},
		{"￥1,234.56 元", 123456, true, "货币符号/单位"},
		{"123.456", 12346, true, "第 3 位小数四舍五入（进位）"},
		{"123.454", 12345, true, "第 3 位小数四舍五入（舍去）"},
		{"", 0, false, "空"},
		{"abc", 0, false, "非法文本"},
		{"1e5", 0, false, "科学计数（不支持）"},
		{"99999999999999999999", 0, false, "溢出"},
	}
	for _, c := range cases {
		got, ok := ParseAmountCents(feishu.FieldValue{ValueText: c.in})
		if ok != c.ok {
			t.Errorf("ParseAmountCents(%q) ok=%v, 期望 %v（%s）", c.in, ok, c.ok, c.note)
			continue
		}
		if ok && got != c.want {
			t.Errorf("ParseAmountCents(%q)=%d, 期望 %d（%s）", c.in, got, c.want, c.note)
		}
	}
}

var _ = qaB32Config // 保留 helper 供后续扩展；避免未使用告警
