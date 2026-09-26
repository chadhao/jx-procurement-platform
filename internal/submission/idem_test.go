package submission

import "testing"

// TestFingerprintStableAndOrderInsensitive 指纹需满足：同载荷稳定；项顺序不敏感；空白不敏感。
func TestFingerprintStableAndOrderInsensitive(t *testing.T) {
	base := IdemPayload{
		BizNo: "SUB-1", SubjectType: "公户付款", HasAmount: true, AmountCents: 480000,
		PayMethod: "对公直付", HNFinishDate: "2026-09-20", SubmitDate: "2026-09-23",
		ReceiptRef: "SIGN-1",
		Items: []IdemItem{
			{BizNo: "PR-1", Type: "PR"},
			{BizNo: "CT-1", Type: "CT"},
		},
	}
	fp := base.Fingerprint()

	if got := base.Fingerprint(); got != fp {
		t.Errorf("同一载荷两次计算不一致: %s vs %s", got, fp)
	}
	if len(fp) != 64 {
		t.Errorf("指纹长度 = %d, 期望 64（SHA-256 十六进制）", len(fp))
	}

	// 项顺序调换 → 同一指纹。
	reordered := base
	reordered.Items = []IdemItem{
		{BizNo: "CT-1", Type: "CT"},
		{BizNo: "PR-1", Type: "PR"},
	}
	if got := reordered.Fingerprint(); got != fp {
		t.Errorf("项顺序调换后指纹变化: %s", got)
	}

	// 前后空白 → 同一指纹。
	padded := base
	padded.BizNo = "  SUB-1  "
	padded.ReceiptRef = "SIGN-1 "
	if got := padded.Fingerprint(); got != fp {
		t.Errorf("前后空白导致指纹变化: %s", got)
	}
}

// TestFingerprintDistinguishesPayloads 关键字段变化必须改变指纹（否则幂等会误复用）。
func TestFingerprintDistinguishesPayloads(t *testing.T) {
	base := IdemPayload{BizNo: "SUB-1", SubjectType: "公户付款", HNFinishDate: "2026-09-20"}
	fp := base.Fingerprint()

	cases := map[string]IdemPayload{
		"业务单号":  {BizNo: "SUB-2", SubjectType: "公户付款", HNFinishDate: "2026-09-20"},
		"事项类型":  {BizNo: "SUB-1", SubjectType: "备付金支出", HNFinishDate: "2026-09-20"},
		"完成日期":  {BizNo: "SUB-1", SubjectType: "公户付款", HNFinishDate: "2026-09-21"},
		"新增金额":  {BizNo: "SUB-1", SubjectType: "公户付款", HNFinishDate: "2026-09-20", HasAmount: true, AmountCents: 1},
		"新增凭证号": {BizNo: "SUB-1", SubjectType: "公户付款", HNFinishDate: "2026-09-20", ReceiptRef: "SIGN-1"},
	}
	for name, p := range cases {
		if p.Fingerprint() == fp {
			t.Errorf("%s 变化后指纹未变，会误判为同一载荷", name)
		}
	}
}

// TestFingerprintAmountZeroVsAbsent 金额「未传」与「传 0」是不同意图，指纹必须不同。
func TestFingerprintAmountZeroVsAbsent(t *testing.T) {
	absent := IdemPayload{BizNo: "SUB-1", SubjectType: "公户付款"}
	zero := IdemPayload{BizNo: "SUB-1", SubjectType: "公户付款", HasAmount: true, AmountCents: 0}
	if absent.Fingerprint() == zero.Fingerprint() {
		t.Error("「未传金额」与「金额 0」指纹相同，无法区分登记意图")
	}
}

// TestFingerprintIgnoresEmptyItemNo 空单号的关联项不落库，也不应影响指纹（与处理器一致）。
func TestFingerprintIgnoresEmptyItemNo(t *testing.T) {
	base := IdemPayload{BizNo: "SUB-1", SubjectType: "公户付款",
		Items: []IdemItem{{BizNo: "PR-1", Type: "PR"}}}
	withEmpty := base
	withEmpty.Items = append([]IdemItem{{BizNo: "   ", Type: "XX"}}, base.Items...)
	if base.Fingerprint() != withEmpty.Fingerprint() {
		t.Error("空单号项不应参与指纹")
	}
}

// TestFingerprintNoFieldConcatenationCollision 字段间必须分隔，避免跨字段拼接歧义。
func TestFingerprintNoFieldConcatenationCollision(t *testing.T) {
	a := IdemPayload{BizNo: "AB", SubjectType: "C"}
	b := IdemPayload{BizNo: "A", SubjectType: "BC"}
	if a.Fingerprint() == b.Fingerprint() {
		t.Error("跨字段拼接产生歧义：\"AB\"+\"C\" 与 \"A\"+\"BC\" 指纹相同")
	}
}

// TestFingerprintNoItemDelimiterCollision 项内的分隔符不得与项间分隔符同形（否则指纹碰撞）。
//
// 反例（若项间用逗号拼接）：`[("A,B","C")]` 与 `[("A","B,C")]` 都会拼成 `A,B,C`，
// 两个**不同载荷**被判为同一载荷 → 幂等误复用，静默吞掉第二次写入。
// 修复：项内与项间统一用 0x1F（不可打印的单元分隔符）。
func TestFingerprintNoItemDelimiterCollision(t *testing.T) {
	cases := [][2]IdemPayload{
		{
			{BizNo: "SUB-1", Items: []IdemItem{{BizNo: "A,B", Type: "C"}}},
			{BizNo: "SUB-1", Items: []IdemItem{{BizNo: "A", Type: "B,C"}}},
		},
		{
			{BizNo: "SUB-1", Items: []IdemItem{{BizNo: "A", Type: "B"}, {BizNo: "C", Type: "D"}}},
			{BizNo: "SUB-1", Items: []IdemItem{{BizNo: "A", Type: "B\x1fC"}, {BizNo: "D", Type: ""}}},
		},
	}
	for i, pair := range cases {
		if pair[0].Fingerprint() == pair[1].Fingerprint() {
			t.Errorf("第 %d 组：不同载荷产生相同指纹（项分隔符歧义）→ 幂等会误复用", i)
		}
	}
}

// TestFingerprintItemCountMatters 项数不同必须改变指纹（避免并集被当成同一载荷）。
func TestFingerprintItemCountMatters(t *testing.T) {
	one := IdemPayload{BizNo: "SUB-1", Items: []IdemItem{{BizNo: "A", Type: "PR"}}}
	two := IdemPayload{BizNo: "SUB-1", Items: []IdemItem{{BizNo: "A", Type: "PR"}, {BizNo: "A", Type: "PR"}}}
	if one.Fingerprint() == two.Fingerprint() {
		t.Error("重复项未计入指纹：1 项与 2 项（同值）指纹相同，会误判为同一载荷")
	}
}
