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

	// ① cutoff：真源读出 25；改内存副本为 31 ⇒ 同一时刻的批次归集结果不同
	got25, ok := b.ParamInt("reporting.monthly_cutoff_day")
	if !ok || got25 != 25 {
		t.Fatalf("cutoff 应从 spec 读出 25，实为 (%d,%v)", got25, ok)
	}
	at := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	period25, rolled25 := batchPeriod(at, got25)
	period31, rolled31 := batchPeriod(at, 31)
	if !rolled25 || rolled31 || period25 == period31 {
		t.Fatalf("改 cutoff 应改变行为：25→(%s,%v) / 31→(%s,%v)", period25, rolled25, period31, rolled31)
	}

	// ② 合同容差：真源 10；改内存副本为 5 ⇒ accessor 随之变化
	mutated := *b
	mutated.Params = &specload.ParamsDoc{Params: map[string]specload.ParamEntry{}}
	for k, v := range b.Params.Params {
		mutated.Params.Params[k] = v
	}
	tol10, _ := (Deps{Spec: b}).contractAmountTolerance()
	if tol10 != 10 {
		t.Fatalf("容差应读出 10，实为 %d", tol10)
	}
	raw5 := json.RawMessage("5")
	e := mutated.Params.Params["contract.amount_over_pr_tolerance_percent"]
	e.Value = raw5
	mutated.Params.Params["contract.amount_over_pr_tolerance_percent"] = e
	d := Deps{Spec: &mutated}
	tol5, ok2 := d.contractAmountTolerance()
	if !ok2 || tol5 != 5 {
		t.Fatalf("改 params 后容差应为 5，实为 (%d,%v) —— 疑似写死", tol5, ok2)
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
	if day, _ := reporting["cutoff_day"].(float64); int(day) != 25 {
		t.Errorf("cutoff_day = %v，应为 spec 里的 25", reporting["cutoff_day"])
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
	// 超期处置：待定 ⇒ pending + 建议值行为
	overdue, _ := reporting["overdue_handling"].(map[string]any)
	if overdue == nil || overdue["pending"] != true || overdue["in_effect"] != "auto_next_month" {
		t.Errorf("overdue_handling = %v（应 pending=true 且按建议值运行）", overdue)
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
