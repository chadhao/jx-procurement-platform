package httpapi

import (
	"net/http"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
)

// TestPettyCashReceiptCloseAndBalance M1：签领登记 → 月核销 → 余额只读。
// 覆盖：③ 同账期重复核销 → 40900；④ 余额接口只读、无写入路径。
func TestPettyCashReceiptCloseAndBalance(t *testing.T) {
	e, db, auth := newM1M6App(t)
	seedDefaultUsers(t, db,
		store.UserRole{OpenID: "ou_ops", Role: roleOpsSupervisor, Active: true},
		store.UserRole{OpenID: "ou_lead", Role: roleDeptLead, Active: true},
	)
	opsCookie := auth.Establish("ou_ops")
	leadCookie := auth.Establish("ou_lead")

	// ④-A 余额接口只读：路由表中不存在改余额的写路由。
	for _, r := range e.Routes() {
		if r.Path == "/api/petty-cash/balance" && r.Method != http.MethodGet {
			t.Errorf("备付金余额存在写入路由: %s %s", r.Method, r.Path)
		}
	}
	// 对余额路径发 POST → 不得成功（405/404）。
	wrec, _ := m1m6Do(e, http.MethodPost, "/api/petty-cash/balance", opsCookie, `{"balance_cents":1}`, nil)
	if wrec.Code == http.StatusOK {
		t.Fatalf("余额接口不应接受写请求，实际 http=%d", wrec.Code)
	}

	// 签领登记（采一档「先领款、后购买」的凭据）。
	rrec, rEnv := m1m6Do(e, http.MethodPost, "/api/petty-cash/receipt", opsCookie,
		`{"biz_no":"BA-2609-0001","receiver_open_id":"ou_ops","receiver_name":"张三",
		  "amount_cents":100000,"received_date":"2026-09-10"}`, nil)
	if rrec.Code != http.StatusOK || rEnv.Code != codeOK {
		t.Fatalf("签领登记失败: http=%d code=%d body=%s", rrec.Code, rEnv.Code, rrec.Body.String())
	}
	if _, ok := mustData(t, rEnv)["id"]; !ok {
		t.Fatalf("签领登记响应缺少 id")
	}

	// 月核销。
	closeBody := `{"period":"2026-09","issued_cents":100000,"spent_cents":80000,"balance_cents":20000,"remark":"首月核销"}`
	crec, cEnv := m1m6Do(e, http.MethodPost, "/api/petty-cash/monthly-close", opsCookie, closeBody, nil)
	if crec.Code != http.StatusOK || cEnv.Code != codeOK {
		t.Fatalf("月核销失败: http=%d code=%d body=%s", crec.Code, cEnv.Code, crec.Body.String())
	}

	// ③ 同账期重复核销 → 40900。
	drec, dEnv := m1m6Do(e, http.MethodPost, "/api/petty-cash/monthly-close", opsCookie, closeBody, nil)
	if drec.Code != http.StatusConflict || dEnv.Code != codeConflict {
		t.Fatalf("重复核销: http=%d code=%d, 期望 409 / 40900", drec.Code, dEnv.Code)
	}
	if n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_petty_cash_close WHERE period='2026-09'`); n != 1 {
		t.Errorf("账期行数 = %d, 期望 1（唯一账期一条）", n)
	}

	// 余额只读：综合运营主管。
	brec, bEnv := m1m6Do(e, http.MethodGet, "/api/petty-cash/balance?period=2026-09", opsCookie, "", nil)
	if brec.Code != http.StatusOK {
		t.Fatalf("余额查询失败: http=%d", brec.Code)
	}
	bd := mustData(t, bEnv)
	if bd["balance_cents"].(float64) != 20000 || bd["issued_cents"].(float64) != 100000 || bd["spent_cents"].(float64) != 80000 {
		t.Errorf("余额三段值不符: %+v", bd)
	}
	if bd["as_of"] == "" {
		t.Errorf("余额缺少 as_of")
	}

	// 主管领导：只读余额可查。
	lrec, _ := m1m6Do(e, http.MethodGet, "/api/petty-cash/balance?period=2026-09", leadCookie, "", nil)
	if lrec.Code != http.StatusOK {
		t.Errorf("主管领导查询余额状态码 = %d, 期望 200（只读）", lrec.Code)
	}
	// 主管领导不得签领登记（写权限仅综合运营主管）。
	lsrec, lsenv := m1m6Do(e, http.MethodPost, "/api/petty-cash/receipt", leadCookie,
		`{"amount_cents":100,"received_date":"2026-09-10"}`, nil)
	if lsrec.Code != http.StatusForbidden || lsenv.Code != codeForbidden {
		t.Errorf("主管领导签领: http=%d code=%d, 期望 403 / 40300", lsrec.Code, lsenv.Code)
	}
}

// TestPettyCashBalanceValidation 账期格式与金额校验。
func TestPettyCashBalanceValidation(t *testing.T) {
	e, db, auth := newM1M6App(t)
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_ops", Role: roleOpsSupervisor, Active: true})
	cookie := auth.Establish("ou_ops")

	rec, env := m1m6Do(e, http.MethodGet, "/api/petty-cash/balance?period=2026-9", cookie, "", nil)
	if rec.Code != http.StatusBadRequest || env.Code != codeBadRequest {
		t.Errorf("非法账期: http=%d code=%d, 期望 400 / 40000", rec.Code, env.Code)
	}

	rec2, env2 := m1m6Do(e, http.MethodPost, "/api/petty-cash/receipt", cookie,
		`{"amount_cents":0,"received_date":"2026-09-10"}`, nil)
	if rec2.Code != http.StatusBadRequest || env2.Code != codeBadRequest {
		t.Errorf("非正金额: http=%d code=%d, 期望 400 / 40000", rec2.Code, env2.Code)
	}

	// 默认当月（未传 period）应返回 200。
	rec3, _ := m1m6Do(e, http.MethodGet, "/api/petty-cash/balance", cookie, "", nil)
	if rec3.Code != http.StatusOK {
		t.Errorf("默认账期查询状态码 = %d, 期望 200", rec3.Code)
	}
}
