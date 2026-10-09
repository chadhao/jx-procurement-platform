package httpapi

// T1 验收（MIMO-NEXT-BATCH §4）：
//   1. 每个参数能指出消费函数（见 params_consumers.go 头注清单）；
//   2. 探针：改 params.json 的值 ⇒ 行为随之变化（证明没写死）。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/specload"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

func TestBatchPeriodCutoff(t *testing.T) {
	cases := []struct {
		name   string
		at     time.Time
		cutoff int
		want   string
		rolled bool
	}{
		{"25日当天_不跨月", time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC), 25, "2026-09", false},
		{"25日后_归次月", time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC), 25, "2026-10", true},
		{"跨年_12月26日", time.Date(2026, 12, 26, 12, 0, 0, 0, time.UTC), 25, "2027-01", true},
		{"截止31_永不跨月", time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC), 31, "2026-09", false},
	}
	for _, c := range cases {
		got, rolled := batchPeriod(c.at, c.cutoff)
		if got != c.want || rolled != c.rolled {
			t.Errorf("%s: batchPeriod = (%s,%v)，应为 (%s,%v)", c.name, got, rolled, c.want, c.rolled)
		}
	}
}

// TestParamsProbeChangeValueChangesBehavior ★ 探针：改参数值 ⇒ 行为变化（未写死）。
func TestParamsProbeChangeValueChangesBehavior(t *testing.T) {
	b := metaTestBundle(t)

	// ① cutoff：真源读出 20（2026-10-09 按制度第十七条由 25 改）；改内存副本为 31 ⇒ 同一时刻的批次归集结果不同
	gotCutoff, ok := b.ParamInt("reporting.monthly_cutoff_day")
	if !ok || gotCutoff != 20 {
		t.Fatalf("cutoff 应从 spec 读出 20，实为 (%d,%v)", gotCutoff, ok)
	}
	at := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	periodCutoff, rolledCutoff := batchPeriod(at, gotCutoff)
	period31, rolled31 := batchPeriod(at, 31)
	if !rolledCutoff || rolled31 || periodCutoff == period31 {
		t.Fatalf("改 cutoff 应改变行为：20→(%s,%v) / 31→(%s,%v)", periodCutoff, rolledCutoff, period31, rolled31)
	}

	// ② 合同容差：真源 5（2026-10-09 按制度第二十一条由 10 改）；改内存副本为 10 ⇒ accessor 随之变化
	mutated := *b
	mutated.Params = &specload.ParamsDoc{Params: map[string]specload.ParamEntry{}}
	for k, v := range b.Params.Params {
		mutated.Params.Params[k] = v
	}
	tolBase, _ := (Deps{Spec: b}).contractAmountTolerance()
	if tolBase != 5 {
		t.Fatalf("容差应读出 5，实为 %d", tolBase)
	}
	raw10 := json.RawMessage("10")
	e := mutated.Params.Params["contract.amount_over_pr_tolerance_percent"]
	e.Value = raw10
	mutated.Params.Params["contract.amount_over_pr_tolerance_percent"] = e
	d := Deps{Spec: &mutated}
	tolMut, ok2 := d.contractAmountTolerance()
	if !ok2 || tolMut != 10 {
		t.Fatalf("改 params 后容差应为 10，实为 (%d,%v) —— 疑似写死", tolMut, ok2)
	}

	// ③ 备付金时限：type=none ⇒ enforced=false（不设时限的分支由参数驱动）
	policy := Deps{Spec: b}.pettyCashDeadlinePolicy()
	if policy["loaded"] != true || policy["enforced"] != false || policy["type"] != "none" {
		t.Fatalf("备付金时限策略 = %v", policy)
	}
}

// TestReimbursementReportingWired 报销登记的参数消费接线（handler 端）。
func TestReimbursementReportingWired(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	seedDefaultUsers(t, db,
		store.UserRole{OpenID: "ou_ops", Role: roleOpsSupervisor, Active: true},
	)
	cookie := auth.Establish("ou_ops")
	rec, env := doRequest(e, http.MethodPost, "/api/reimbursement", cookie,
		`{"src_biz_no":"SA-2609-0001","actual_cents":12345,"invoice_count":1}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("登记失败 %d：%s", rec.Code, env.Message)
	}
	data, _ := env.Data.(map[string]any)
	reporting, _ := data["reporting"].(map[string]any)
	if reporting == nil {
		t.Fatal("响应缺 reporting（T1 参数消费未接线）")
	}
	if day, _ := reporting["cutoff_day"].(float64); int(day) != 20 {
		t.Errorf("cutoff_day = %v，应为 spec 里的 20", reporting["cutoff_day"])
	}
	// 批次与独立重算一致
	b := metaTestBundle(t)
	cutoff, _ := b.ParamInt("reporting.monthly_cutoff_day")
	wantPeriod, wantRolled := batchPeriod(time.Now(), cutoff)
	if got, _ := reporting["batch_period"].(string); got != wantPeriod {
		t.Errorf("batch_period = %q，应为 %q", got, wantPeriod)
	}
	if got, _ := reporting["rolled_to_next"].(bool); got != wantRolled {
		t.Errorf("rolled_to_next = %v，应为 %v", got, wantRolled)
	}
	// 超期处置（N-029）：已定案 ⇒ 按**行为**断言，不锁瞬态的 pending 标志 ——
	//   ① status=已定；② in_effect=auto_next_month；③ 超期**不阻断**（请求 200 即证）
	//   且归次月标注自洽（rolled ⇒ batch_period 为次月）。断言"待定与否"注定在定案日转红、
	//   且没守住任何业务不变量（N-026 同族）。
	overdue, _ := reporting["overdue_handling"].(map[string]any)
	if overdue == nil || overdue["status"] != "已定" || overdue["in_effect"] != "auto_next_month" {
		t.Errorf("overdue_handling = %v（应 status=已定 / in_effect=auto_next_month）", overdue)
	}
	if overdue["pending"] == true {
		t.Errorf("overdue_handling 已定案，pending 不应为 true：%v", overdue)
	}
	if rolled, _ := reporting["rolled_to_next"].(bool); rolled {
		// 超期场景（今天 >20，2026-10-09 按制度第十七条由 25 改）：批次必须已归次月（「已归入次月批次」标注的数据基础）
		thisMonth, _ := batchPeriod(time.Now(), 0) // cutoff=0 ⇒ 永不跨月＝本月
		if got, _ := reporting["batch_period"].(string); got == thisMonth {
			t.Errorf("超期（rolled）时 batch_period 应归次月，仍为本月 %q", got)
		}
	}
}

// TestPettyCashDeadlinePolicyWired 备付金月核销响应带时限策略（type=none 零阻断）。
func TestPettyCashDeadlinePolicyWired(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	seedDefaultUsers(t, db,
		store.UserRole{OpenID: "ou_ops", Role: roleOpsSupervisor, Active: true},
	)
	cookie := auth.Establish("ou_ops")
	rec, env := doRequest(e, http.MethodPost, "/api/petty-cash/monthly-close", cookie,
		`{"period":"2026-08","issued_cents":100,"spent_cents":40,"balance_cents":60,"remark":"t1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("月核销失败 %d：%s", rec.Code, env.Message)
	}
	data, _ := env.Data.(map[string]any)
	policy, _ := data["reimburse_deadline"].(map[string]any)
	if policy == nil || policy["loaded"] != true {
		t.Fatalf("响应缺 reimburse_deadline 参数消费：%v", data)
	}
	if policy["type"] != "none" || policy["enforced"] != false {
		t.Errorf("备付金时限策略 = %v，应 type=none/enforced=false（A2 定案）", policy)
	}
	if !strings.Contains(policy["cutoff_source"].(string), "petty_cash.reimburse_deadline") {
		t.Errorf("cutoff_source 应指向 params.json 键：%v", policy["cutoff_source"])
	}
}
