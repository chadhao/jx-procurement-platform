package httpapi

// N-062 J1：BA 提交期系统字段注入 ＋ CT 审批层级派生的可机检判据。

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/chain"
	"github.com/chadhao/jx-procurement-platform/internal/flow"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// baBodyFor BA 提交体（照真实前端形状：supplier/usage 在 fields **且** usage 顶层双写
// —— Submit.vue:431 顶层传 usage_category_l2 ⇒ instance 规范列 purpose_class_l2 落值）。
func baBodyFor(amount int64, supplier string) string {
	return `{"doc_type":"BA","approval_code":"code-ba","amount_cents":` +
		strconv.FormatInt(amount, 10) + `,"usage_category_l1":"P01","usage_category_l2":"P01-01",` +
		`"fields":{"usage_category_l1":"P01","usage_category_l2":"P01-01","quantity":2,` +
		`"purpose":"班中劳保补货","arrival_date":"2026-10-31","supplier":"` + supplier + `"}}`
}

func extOf(t *testing.T, db *store.DB, bizNo string) map[string]any {
	t.Helper()
	inst, err := db.GetInstanceByBizNo(context.Background(), bizNo)
	if err != nil {
		t.Fatalf("读实例失败: %v", err)
	}
	m := map[string]any{}
	if inst.ExtJSON != "" {
		if err := json.Unmarshal([]byte(inst.ExtJSON), &m); err != nil {
			t.Fatalf("ext 解析失败: %v", err)
		}
	}
	return m
}

func seedBAInstance(t *testing.T, db *store.DB, code, bizNo, dept, supplier, l2, yymm string, amount int64) {
	t.Helper()
	now := time.Now()
	amt := amount
	in := &store.Instance{
		InstanceCode: code, ApprovalCode: "code-ba", DocType: "BA",
		BizNo: bizNo, BizNoPrefix: "BA", BizNoYYMM: yymm, BizNoSeq: "0900",
		Status: "PENDING", ApplicantOpenID: "ou_seed", ApplicantName: "种子",
		Department: dept, AmountCents: &amt,
		PurposeClassL1: "P01", PurposeClassL2: l2, Supplier: supplier,
		Source: "event", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.UpsertInstance(context.Background(), in); err != nil {
		t.Fatalf("seed 实例失败: %v", err)
	}
}

// TestBASystemFieldsInjected 端到端：提交即注入 4 个 system 字段。
// 覆盖：备案日期=提交日 · 重点抽查区间 TC-19 可达三点（79900 否 / 80000 是 / 99900 是；
// 1000 元点被采一档档位门禁先行拦截、结构不可达 —— 如实登记）· 空库首单 1/10 未超限 ·
// 拆单累计触发（seed 同供应商先于本单 70000 ＋本单 50000 ≥ 100000）与部门超限 20/15。
func TestBASystemFieldsInjected(t *testing.T) {
	e, db, auth := newSubmitM4App(t, true)
	cookie := auth.Establish("ou_app")
	ctx := context.Background()
	today := time.Now().Format("2006-01-02")
	yymm := time.Now().Format("0601")

	// ---- S1：左端点 800 元（TC-19 → 是）；空库首单 1/10 未超限、未触发 ----
	code1, env1 := postSubmit(t, e, cookie, baBodyFor(80000, "边界家"), "")
	if code1 != http.StatusOK {
		t.Fatalf("S1 提交应 200, 实为 %d（%s）", code1, env1.Message)
	}
	d1, _ := env1.Data.(map[string]any)
	biz1, _ := d1["biz_no"].(string)
	ext1 := extOf(t, db, biz1)
	if ext1["record_date"] != today {
		t.Errorf("record_date = %v, 期望 %s（提交当日）", ext1["record_date"], today)
	}
	if ext1["is_key_sample_range"] != true {
		t.Errorf("is_key_sample_range(80000) = %v, 期望 true（TC-19 左端点 800→是）", ext1["is_key_sample_range"])
	}
	if ext1["is_monthly_supplier_rollover_warned"] != false {
		t.Errorf("rollover(80000 首单) = %v, 期望 false（80000 < 100000）", ext1["is_monthly_supplier_rollover_warned"])
	}
	res1, _ := ext1["anti_split_check_result"].(string)
	if !strings.Contains(res1, "未触发") || !strings.Contains(res1, "1/10") || !strings.Contains(res1, "未超限") {
		t.Errorf("anti_split S1 = %q, 期望含 未触发 / 1/10 / 未超限", res1)
	}
	// S1 实例的部门 = 会话部门（后续种子对齐用）
	inst1, err := db.GetInstanceByBizNo(ctx, biz1)
	if err != nil {
		t.Fatal(err)
	}
	dept := inst1.Department

	// ---- 种子：同供应商先于本单 70000（甲供应商·P01-01）＋ 部门当月 15 单 ----
	seedBAInstance(t, db, "SEED-A-01", "BA-2610-0901", "种子部", "甲供应商", "P01-01", yymm, 40000)
	seedBAInstance(t, db, "SEED-A-02", "BA-2610-0902", "种子部", "甲供应商", "P01-01", yymm, 30000)
	for i := 0; i < 15; i++ {
		seedBAInstance(t, db, "SEED-D-"+strconv.Itoa(i), "BA-2610-19"+strconv.Itoa(i),
			dept, "别家供应商", "P02-01", yymm, 10000)
	}

	// ---- S2：TC-19 四点其余可达点（独立供应商 ⇒ 累计互不污染）----
	// ★ TC 第四点「1,000 元 → 否」在提交面**结构不可达**：BA 仅适用采一档（<1,000 元），
	//   1,000 元单在链算层即被档位门禁拦下（实测 400「仅适用采一档」）⇒ 区间右端点
	//   的排除语义（<100000）无法经 BA 提交端到端触达，回执如实登记；
	//   false 侧由 79900/50000 承担鉴别力，true 侧由 80000/99900 承担。
	for _, tc := range []struct {
		amt  int64
		want bool
		sup  string
	}{{79900, false, "边界家799"}, {99900, true, "边界家999"}} {
		code, env := postSubmit(t, e, cookie, baBodyFor(tc.amt, tc.sup), "")
		if code != http.StatusOK {
			t.Fatalf("S2(%d) 提交应 200, 实为 %d（%s）", tc.amt, code, env.Message)
		}
		d, _ := env.Data.(map[string]any)
		biz, _ := d["biz_no"].(string)
		ext := extOf(t, db, biz)
		if ext["is_key_sample_range"] != tc.want {
			t.Errorf("is_key_sample_range(%d) = %v, 期望 %v（TC-19 逐点预期）",
				tc.amt, ext["is_key_sample_range"], tc.want)
		}
		// 单笔 < 阈值 ⇒ 不触发（阈值正向由 S3 的累计触发承担）
		if ext["is_monthly_supplier_rollover_warned"] != false {
			t.Errorf("rollover(%d 独立供应商) = %v, 期望 false（单笔 < 阈值 100000）",
				tc.amt, ext["is_monthly_supplier_rollover_warned"])
		}
	}

	// ---- S3：同供应商累计触发 ＋ 部门超限 ----
	code3, env3 := postSubmit(t, e, cookie, baBodyFor(50000, "甲供应商"), "")
	if code3 != http.StatusOK {
		t.Fatalf("S3 提交应 200, 实为 %d（%s）", code3, env3.Message)
	}
	d3, _ := env3.Data.(map[string]any)
	biz3, _ := d3["biz_no"].(string)
	ext3 := extOf(t, db, biz3)
	if ext3["is_monthly_supplier_rollover_warned"] != true {
		t.Errorf("rollover(70000+50000) = %v, 期望 true（≥ 阈值 100000）", ext3["is_monthly_supplier_rollover_warned"])
	}
	if ext3["is_key_sample_range"] != false {
		t.Errorf("is_key_sample_range(50000) = %v, 期望 false", ext3["is_key_sample_range"])
	}
	res3, _ := ext3["anti_split_check_result"].(string)
	if !strings.Contains(res3, "触发转档预警") {
		t.Errorf("anti_split S3 = %q, 期望含 触发转档预警", res3)
	}
	if !strings.Contains(res3, "超限") || strings.Contains(res3, "未超限") {
		t.Errorf("anti_split S3 = %q, 期望部门超限（20/15）", res3)
	}
	if !strings.Contains(res3, "19/10") {
		t.Errorf("anti_split S3 = %q, 期望含 19/10（S1+S2 三真单 + 15 种子 + 本单；★ 2026-10-09 阈值 15→10）", res3)
	}
}

// TestApprovalLevelsOf CT 审批层级派生（rule 正本：两级；同一人 ⇒ 一级（终审））。
func TestApprovalLevelsOf(t *testing.T) {
	two := &chain.ResolvedChain{Spec: []flow.NodeSpec{
		{NodeID: "contract_supervisor", Approvers: []flow.Approver{{OpenID: "ou_a", Name: "甲"}}},
		{NodeID: "contract_pgm", Approvers: []flow.Approver{{OpenID: "ou_b", Name: "乙"}}},
	}}
	if got := approvalLevelsOf(two); got != "主管领导 → 项目总经理" {
		t.Errorf("两级 = %q", got)
	}
	one := &chain.ResolvedChain{Spec: []flow.NodeSpec{
		{NodeID: "contract_supervisor", Approvers: []flow.Approver{{OpenID: "ou_a", Name: "甲"}}},
		{NodeID: "contract_pgm", Approvers: []flow.Approver{{OpenID: "ou_a", Name: "甲"}}},
	}}
	if got := approvalLevelsOf(one); got != "一级（终审）" {
		t.Errorf("同一人 = %q, 期望 一级（终审）", got)
	}
	if got := approvalLevelsOf(nil); got != "" {
		t.Errorf("nil = %q, 期望空串（不伪造层级）", got)
	}
}
