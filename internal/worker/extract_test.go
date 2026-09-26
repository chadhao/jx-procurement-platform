package worker

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/config"
	"github.com/chadhao/jx-procurement-platform/internal/platform/feishu"
	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
)

// 本文件覆盖 FR-M2-08「规范字段抽取」——把表单控件值搬到 t_instance 的规范列。
//
// ★ 该步骤缺失时**不报错**，只是 amount_cents / supplier / purpose_class_* 恒为空，
// 导致看板金额、防拆分「同供应商当月累计」、800–1000 元抽查清单**全部静默失效**。
// 故用例重点断言"抽到了、且抽对列"。

// mapsWith 用配置映射导入构造 Maps（与生产同一条路径：先导入、再装载）。
func mapsWith(t *testing.T, payload *config.ImportPayload) *config.Maps {
	t.Helper()
	db := storetest.NewDB(t)
	if _, err := config.ImportMappings(context.Background(), db, payload); err != nil {
		t.Fatalf("导入配置映射失败: %v", err)
	}
	maps, err := config.LoadMaps(context.Background(), db)
	if err != nil {
		t.Fatalf("装载映射失败: %v", err)
	}
	return maps
}

// TestParseAmountCents 金额解析：货币符号 / 千分位 / 四舍五入 / 负数 / 拒绝非法值。
func TestParseAmountCents(t *testing.T) {
	cases := []struct {
		in    string
		want  int64
		valid bool
	}{
		{"1234.56", 123456, true},
		{"1,234.56", 123456, true},
		{"￥1,234.56 元", 123456, true},
		{"1234", 123400, true},
		{"1234.5", 123450, true},
		{"1234.567", 123457, true}, // 第三位四舍五入（进位）
		{"1234.564", 123456, true}, // 第三位四舍五入（舍去）
		{"0.005", 1, true},         // 半分进位到 1 分
		{"0", 0, true},             // 零合法（是否允许由业务层判）
		{"-12.34", -1234, true},    // 负数（红字冲销场景）
		{".5", 50, true},           // 省略整数部分
		{"", 0, false},
		{"   ", 0, false},
		{"一千", 0, false},
		{"12.3.4", 0, false},
		{"abc", 0, false},
		{"1e3", 0, false},                  // 科学计数法不接受（避免解析歧义）
		{"99999999999999999999", 0, false}, // 溢出
	}
	for _, c := range cases {
		got, ok := parseAmountCents(c.in)
		if ok != c.valid {
			t.Errorf("parseAmountCents(%q) 有效性 = %v, 期望 %v", c.in, ok, c.valid)
			continue
		}
		if ok && got != c.want {
			t.Errorf("parseAmountCents(%q) = %d, 期望 %d", c.in, got, c.want)
		}
	}
}

// TestParseAmountCentsObjectForm 飞书金额控件返回对象形态时也能取值。
func TestParseAmountCentsObjectForm(t *testing.T) {
	f := feishu.FieldValue{RawJSON: `{"amount":1234.56,"currency":"CNY"}`}
	if got, ok := ParseAmountCents(f); !ok || got != 123456 {
		t.Fatalf("对象形态取金额 = %d %v, 期望 123456 true", got, ok)
	}
	f = feishu.FieldValue{RawJSON: `{"value":"88.8"}`}
	if got, ok := ParseAmountCents(f); !ok || got != 8880 {
		t.Fatalf("对象字符串值取金额 = %d %v, 期望 8880 true", got, ok)
	}
	f = feishu.FieldValue{RawJSON: `{"currency":"CNY"}`}
	if _, ok := ParseAmountCents(f); ok {
		t.Fatalf("无金额键的对象不应解析出金额")
	}
}

// TestExtractDetailCanonicalFields 抽取：金额 / 供应商 / 用途 / 部门 / 申请人姓名。
func TestExtractDetailCanonicalFields(t *testing.T) {
	maps := mapsWith(t, &config.ImportPayload{
		ApprovalCode: []config.ImportApprovalCode{{Code: "CODE-CT", DocType: "CT"}},
		FieldID: []config.ImportFieldID{
			{DocType: "CT", FieldID: "w_amount", BizField: config.BizFieldAmount},
			{DocType: "CT", FieldID: "w_supplier", BizField: config.BizFieldSupplier},
			{DocType: "CT", FieldID: "w_p1", BizField: config.BizFieldPurposeL1},
			{DocType: "CT", FieldID: "w_p2", BizField: config.BizFieldPurposeL2},
			{DocType: "CT", FieldID: "w_dept", BizField: config.BizFieldDepartment},
			{FieldID: "w_applicant", BizField: config.BizFieldApplicantName}, // 全局映射
			{DocType: "CT", FieldID: "w_free", BizField: config.BizFieldRemark},
		},
	})

	det := &feishu.InstanceDetail{
		InstanceCode: "I-1",
		ApprovalCode: "CODE-CT",
		Fields: []feishu.FieldValue{
			{FieldID: "w_amount", ValueText: "1,234.56"},
			{FieldID: "w_supplier", ValueText: "岳阳某化工有限公司"},
			{FieldID: "w_p1", ValueText: "生产采购"},
			{FieldID: "w_p2", ValueText: "原辅料"},
			{FieldID: "w_dept", ValueText: "生产部"},
			{FieldID: "w_applicant", ValueText: "张三"},
			{FieldID: "w_free", ValueText: "备注内容"},
		},
	}
	ExtractDetail(maps, "CT", det)

	if det.AmountCents == nil || *det.AmountCents != 123456 {
		t.Errorf("AmountCents = %v, 期望 123456", det.AmountCents)
	}
	if det.Supplier != "岳阳某化工有限公司" {
		t.Errorf("Supplier = %q", det.Supplier)
	}
	if det.PurposeClassL1 != "生产采购" || det.PurposeClassL2 != "原辅料" {
		t.Errorf("用途分类 = %q/%q", det.PurposeClassL1, det.PurposeClassL2)
	}
	if det.Department != "生产部" {
		t.Errorf("Department = %q", det.Department)
	}
	if det.ApplicantName != "张三" {
		t.Errorf("ApplicantName = %q", det.ApplicantName)
	}
}

// TestExtractDetailDoesNotOverrideAuthoritativeValues 接口权威值优先于表单值（只填空、不覆盖）。
func TestExtractDetailDoesNotOverrideAuthoritativeValues(t *testing.T) {
	maps := mapsWith(t, &config.ImportPayload{
		ApprovalCode: []config.ImportApprovalCode{{Code: "CODE-BA", DocType: "BA"}},
		FieldID: []config.ImportFieldID{
			{DocType: "BA", FieldID: "w_no", BizField: config.BizFieldBizNo},
			{DocType: "BA", FieldID: "w_amount", BizField: config.BizFieldAmount},
		},
	})

	apiAmount := int64(999)
	det := &feishu.InstanceDetail{
		InstanceCode: "I-2",
		ApprovalCode: "CODE-BA",
		BizNo:        "BA-2609-0007", // 流水号控件权威值
		AmountCents:  &apiAmount,     // 适配层已给值
		Fields: []feishu.FieldValue{
			{FieldID: "w_no", ValueText: "手填单号-不应覆盖"},
			{FieldID: "w_amount", ValueText: "8888.88"},
		},
	}
	ExtractDetail(maps, "BA", det)

	if det.BizNo != "BA-2609-0007" {
		t.Errorf("BizNo 被表单值覆盖: %q", det.BizNo)
	}
	if det.AmountCents == nil || *det.AmountCents != 999 {
		t.Errorf("AmountCents 被表单值覆盖: %v", det.AmountCents)
	}
}

// TestExtractDetailIgnoresUnmappedFields 未映射字段不参与抽取（不得按中文名猜）。
func TestExtractDetailIgnoresUnmappedFields(t *testing.T) {
	maps := mapsWith(t, &config.ImportPayload{
		ApprovalCode: []config.ImportApprovalCode{{Code: "CODE-PR", DocType: "PR"}},
		FieldID:      []config.ImportFieldID{{DocType: "PR", FieldID: "w_x", BizField: config.BizFieldRemark}},
	})

	det := &feishu.InstanceDetail{
		InstanceCode: "I-3",
		ApprovalCode: "CODE-PR",
		Fields: []feishu.FieldValue{
			// 字段名叫"采购金额"，但**未**在映射里登记 → 不得抽走。
			{FieldID: "w_unmapped", FieldName: "采购金额", ValueText: "5000"},
		},
	}
	ExtractDetail(maps, "PR", det)
	if det.AmountCents != nil {
		t.Fatalf("未映射字段被抽取为金额: %v", *det.AmountCents)
	}
}

// TestExtractDetailNilSafe 空输入不得 panic。
func TestExtractDetailNilSafe(t *testing.T) {
	ExtractDetail(nil, "CT", &feishu.InstanceDetail{})
	ExtractDetail(&config.Maps{}, "CT", &feishu.InstanceDetail{})
	ExtractDetail(&config.Maps{}, "CT", nil)
	ExtractDetail(&config.Maps{}, "CT", &feishu.InstanceDetail{Fields: []feishu.FieldValue{{FieldID: "a"}}})
}

// TestIngestExtractsIntoInstanceColumns 端到端：入库后规范列确有值（这是看板/防拆分的数据前提）。
func TestIngestExtractsIntoInstanceColumns(t *testing.T) {
	maps := mapsWith(t, &config.ImportPayload{
		ApprovalCode: []config.ImportApprovalCode{{Code: "CODE-CT", DocType: "CT"}},
		FieldID: []config.ImportFieldID{
			{DocType: "CT", FieldID: "w_amount", BizField: config.BizFieldAmount},
			{DocType: "CT", FieldID: "w_supplier", BizField: config.BizFieldSupplier},
			{DocType: "CT", FieldID: "w_p1", BizField: config.BizFieldPurposeL1},
		},
		LedgerType: []config.ImportKV{{Key: "CT", Value: "L04"}},
	})

	db := storetest.NewDB(t)
	ctx := context.Background()
	ing := NewIngestor(db, maps, nil)

	det := &feishu.InstanceDetail{
		InstanceCode:    "I-E2E-1",
		ApprovalCode:    "CODE-CT",
		StatusRaw:       "APPROVED",
		BizNo:           "CT-2609-0009",
		ApplicantOpenID: "ou_applicant",
		Fields: []feishu.FieldValue{
			{FieldID: "w_amount", ValueText: "1,000.00"},
			{FieldID: "w_supplier", ValueText: "岳阳某化工有限公司"},
			{FieldID: "w_p1", ValueText: "生产采购"},
		},
	}
	if err := ing.Ingest(ctx, det, SourceEvent); err != nil {
		t.Fatalf("入库失败: %v", err)
	}

	inst, err := db.GetInstance(ctx, "I-E2E-1")
	if err != nil {
		t.Fatalf("读取实例失败: %v", err)
	}
	if inst.AmountCents == nil || *inst.AmountCents != 100000 {
		t.Errorf("入库后 AmountCents = %v, 期望 100000（1,000 元 → 分）", inst.AmountCents)
	}
	if inst.Supplier != "岳阳某化工有限公司" {
		t.Errorf("入库后 Supplier = %q", inst.Supplier)
	}
	if inst.PurposeClassL1 != "生产采购" {
		t.Errorf("入库后 PurposeClassL1 = %q", inst.PurposeClassL1)
	}
	if inst.DocType != "CT" {
		t.Errorf("入库后 DocType = %q", inst.DocType)
	}

	// 台账存档：doc_type→ledger_type 已配置 → 应落一行 L04。
	if n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_ledger_archive WHERE ledger_type='L04' AND biz_no='CT-2609-0009'`); n != 1 {
		t.Errorf("台账存档行数 = %d, 期望 1", n)
	}
}

// TestBuildExtJSONCarriesMappedFields ext_json 必须携带已映射字段（变更链按合同号检索的前提）。
func TestBuildExtJSONCarriesMappedFields(t *testing.T) {
	maps := mapsWith(t, &config.ImportPayload{
		ApprovalCode: []config.ImportApprovalCode{{Code: "CODE-PC", DocType: "PC"}},
		FieldID: []config.ImportFieldID{
			{DocType: "PC", FieldID: "w_contract", BizField: config.BizFieldContractNo},
			{DocType: "PC", FieldID: "w_amount", BizField: config.BizFieldAmount},
		},
	})
	fields := []feishu.FieldValue{
		{FieldID: "w_contract", ValueText: "CT-2609-0009"},
		{FieldID: "w_amount", ValueText: "500.50"},
		{FieldID: "w_unmapped", ValueText: "不应出现"},
	}
	got := BuildExtJSON(maps, "PC", fields)

	var m map[string]any
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatalf("ext_json 不是合法 JSON: %q (%v)", got, err)
	}
	if m["contract_no"] != "CT-2609-0009" {
		t.Errorf("ext_json.contract_no = %v, 期望 CT-2609-0009", m["contract_no"])
	}
	if _, ok := m["不存在的名字"]; ok {
		t.Errorf("未映射字段不应进 ext_json")
	}
	if _, ok := m["amount"]; !ok {
		t.Errorf("已映射的金额应进 ext_json")
	}
	if _, ok := m["remark"]; ok {
		t.Errorf("未映射字段不应进 ext_json: %v", m)
	}
	if BuildExtJSON(nil, "PC", fields) != "{}" {
		t.Errorf("无映射时应返回 {}")
	}
	if BuildExtJSON(maps, "PC", nil) != "{}" {
		t.Errorf("无字段时应返回 {}")
	}
}

// TestIngestArchiveExtJSONQueryable 端到端：变更单入库后，可按关联合同号经 json_each 检索到。
// 这直接决定 M4 变更链回溯是否**恒为空**。
func TestIngestArchiveExtJSONQueryable(t *testing.T) {
	maps := mapsWith(t, &config.ImportPayload{
		ApprovalCode: []config.ImportApprovalCode{{Code: "CODE-PC", DocType: "PC"}},
		FieldID: []config.ImportFieldID{
			{DocType: "PC", FieldID: "w_contract", BizField: config.BizFieldContractNo},
			{DocType: "PC", FieldID: "w_change", BizField: "change_cents"},
		},
		LedgerType: []config.ImportKV{{Key: "PC", Value: "L09"}},
	})
	db := storetest.NewDB(t)
	ing := NewIngestor(db, maps, nil)

	for _, bizNo := range []string{"PC-2609-0001", "PC-2609-0002"} {
		det := &feishu.InstanceDetail{
			InstanceCode: "I-" + bizNo, ApprovalCode: "CODE-PC", StatusRaw: "APPROVED",
			BizNo: bizNo, ApplicantOpenID: "ou_pc",
			Fields: []feishu.FieldValue{
				{FieldID: "w_contract", ValueText: "CT-2609-0009"},
				{FieldID: "w_change", ValueText: "200"},
			},
		}
		if err := ing.Ingest(context.Background(), det, SourceEvent); err != nil {
			t.Fatalf("入库 %s 失败: %v", bizNo, err)
		}
	}

	// ★ 按关联合同号检索：**必须走生产的查询入口**，而不是在测试里另写一段 SQL（P2-2）。
	//   旧写法自造了一段「任意键名匹配」的 SQL —— 那正是本轮已废弃的旧口径，
	//   测试通过也证明不了生产路径可用。
	got, err := db.ListArchiveByExtKey(context.Background(), "L09", store.KeyContractNo, "CT-2609-0009", "", nil)
	if err != nil {
		t.Fatalf("按合同号检索失败: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("按合同号检索到 %d 笔变更, 期望 2（ext_json 未携带合同号时此处会是 0）", len(got))
	}
	// 反向：键名不合约定（值相同但键名错）不得命中 —— 键名是契约。
	bad, err := db.ListArchiveByExtKey(context.Background(), "L09", store.KeyRelatedBizNo, "CT-2609-0009", "", nil)
	if err != nil {
		t.Fatalf("反向检索失败: %v", err)
	}
	if len(bad) != 0 {
		t.Fatalf("非约定键却命中 %d 笔，期望 0", len(bad))
	}
}

// TestNonExtractableBizFields 既未登记抽取、也未登记透传的名字应被提示（多为拼写错误）。
func TestNonExtractableBizFields(t *testing.T) {
	p := &config.ImportPayload{
		FieldID: []config.ImportFieldID{
			{FieldID: "a", BizField: config.BizFieldAmount},     // 已登记抽取
			{FieldID: "b", BizField: config.BizFieldContractNo}, // 已登记透传
			{FieldID: "c", BizField: "amout"},                   // 拼错
			{FieldID: "d", BizField: "amout"},                   // 重复，应去重
		},
	}
	got := p.NonExtractableBizFields()
	if len(got) != 1 || got[0] != "amout" {
		t.Fatalf("NonExtractableBizFields = %v, 期望 [amout]", got)
	}
	if !config.IsKnownBizField(config.BizFieldContractNo) {
		t.Fatalf("contract_no 应被登记为已知字段（否则每次导入都误报）")
	}
}

// TestIngestNoLedgerWhenUnmapped doc_type→ledger_type 未配置时不写台账（不虚构口径）。
func TestIngestNoLedgerWhenUnmapped(t *testing.T) {
	maps := mapsWith(t, &config.ImportPayload{
		ApprovalCode: []config.ImportApprovalCode{{Code: "CODE-RFQ", DocType: "RFQ"}},
	})
	db := storetest.NewDB(t)
	ing := NewIngestor(db, maps, nil)

	det := &feishu.InstanceDetail{
		InstanceCode: "I-E2E-2", ApprovalCode: "CODE-RFQ", StatusRaw: "APPROVED",
		BizNo: "RFQ-2609-0001", ApplicantOpenID: "ou_x",
	}
	if err := ing.Ingest(context.Background(), det, SourceEvent); err != nil {
		t.Fatalf("入库失败: %v", err)
	}
	if n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_ledger_archive`); n != 0 {
		t.Errorf("未配置 ledger_type 却写入台账 %d 行", n)
	}
	// 但实例本身必须落库。
	if n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_instance WHERE instance_code='I-E2E-2'`); n != 1 {
		t.Errorf("实例未落库")
	}
}
