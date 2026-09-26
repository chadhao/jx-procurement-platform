package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/chadhao/jx-procurement-platform/internal/access"
	"github.com/chadhao/jx-procurement-platform/internal/config"
	"github.com/chadhao/jx-procurement-platform/internal/observ"
	"github.com/chadhao/jx-procurement-platform/internal/permission"
	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
)

// newM1M6App 装配一个**最小 Echo**（仅注册 M1 备付金 / M6 报送路由），
// 不依赖 router.go 的统一注册，避免与并行开发的看板路由相互影响。
func newM1M6App(t *testing.T) (*echo.Echo, *store.DB, *access.Authenticator) {
	t.Helper()
	db := storetest.NewDB(t)
	metrics := observ.NewMetrics()
	env := &config.Env{DevMode: true, RunEnv: "test"}
	perm := permission.NewLoader(db)
	sessions := access.NewStore("m1m6-test-key", time.Hour)
	auth := access.NewAuthenticator(db, sessions, nil, true, nil)

	d := Deps{
		Env: env, DB: db, Log: observ.NewLogger("error", io.Discard),
		Metrics: metrics, Health: observ.NewHealth("test"),
		Perm: perm, Auth: auth, Maps: &config.Maps{}, Version: "test",
	}
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true

	api := e.Group("/api", d.requireSession)
	api.GET("/petty-cash/balance", d.handlePettyCashBalance)
	api.POST("/petty-cash/receipt", d.handlePettyCashReceipt)
	api.POST("/petty-cash/monthly-close", d.handlePettyCashMonthlyClose)
	api.POST("/submission", d.handleCreateSubmission)
	api.GET("/submission", d.handleListSubmissions)
	api.GET("/submission/:id/package", d.handleSubmissionPackage)
	api.POST("/submission/:id/receipt", d.handleRegisterSubmissionReceipt)
	api.POST("/submission/:id/group", d.handleRegisterSubmissionGroup)
	api.POST("/submission/:id/reject", d.handleRejectSubmission)
	return e, db, auth
}

// m1m6Do 发送一次请求（可带会话 Cookie、JSON 请求体与自定义请求头，如 Idempotency-Key）。
func m1m6Do(e *echo.Echo, method, path, cookie, body string, headers map[string]string) (*httptest.ResponseRecorder, Envelope) {
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != "" {
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: access.CookieName, Value: cookie})
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	var env Envelope
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	return rec, env
}

// createSubmissionRaw 提交一条报送登记，返回记录 id。
func createSubmissionRaw(t *testing.T, e *echo.Echo, cookie, body string, headers map[string]string) int64 {
	t.Helper()
	rec, env := m1m6Do(e, http.MethodPost, "/api/submission", cookie, body, headers)
	if rec.Code != http.StatusOK || env.Code != codeOK {
		t.Fatalf("提交报送登记失败: http=%d code=%d body=%s", rec.Code, env.Code, rec.Body.String())
	}
	data := mustData(t, env)
	id, ok := data["id"].(float64)
	if !ok {
		t.Fatalf("响应缺少 id: %v", data)
	}
	return int64(id)
}

// TestSubmissionNoReceiptIsUnsubmitted TC-15：无移交凭证 → 状态判为「未提交」；补填后 → 「已提交」。
func TestSubmissionNoReceiptIsUnsubmitted(t *testing.T) {
	e, db, auth := newM1M6App(t)
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_ops", Role: roleOpsSupervisor, Active: true})
	cookie := auth.Establish("ou_ops")

	// ① 填写提交日期但不填写移交凭证。
	id := createSubmissionRaw(t, e, cookie,
		`{"biz_no":"SUB-2609-0001","subject_type":"公户付款","pay_method":"对公直付",
		  "hn_finish_date":"2026-09-20","submit_date":"2026-09-23",
		  "items":[{"item_biz_no":"PR-2609-0001","item_type":"PR"},{"item_biz_no":"CT-2609-0001","item_type":"CT"}]}`,
		nil)

	// ② 提交状态判为「未提交」。
	_, gEnv := m1m6Do(e, http.MethodGet, "/api/submission", cookie, "", nil)
	list := mustData(t, gEnv)
	items := list["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("列表条数 = %d, 期望 1", len(items))
	}
	row := items[0].(map[string]any)
	if row["submit_state"] != "未提交" {
		t.Fatalf("无凭证 submit_state = %v, 期望 未提交", row["submit_state"])
	}

	// ③ 月度「已提交未付款」清单不含无凭证记录。
	_, mEnv := m1m6Do(e, http.MethodGet, "/api/submission?state=已提交", cookie, "", nil)
	if total := mustData(t, mEnv)["total"].(float64); total != 0 {
		t.Fatalf("「已提交」清单条数 = %v, 期望 0（无凭证不得计入）", total)
	}

	// ④ 补填移交凭证 → 状态变为「已提交」。
	rrec, rEnv := m1m6Do(e, http.MethodPost, fmt.Sprintf("/api/submission/%d/receipt", id), cookie,
		`{"receipt_ref":"SIGN-2026-0001"}`, nil)
	if rrec.Code != http.StatusOK || rEnv.Code != codeOK {
		t.Fatalf("补填移交凭证失败: http=%d code=%d body=%s", rrec.Code, rEnv.Code, rrec.Body.String())
	}
	if st := mustData(t, rEnv)["submit_state"]; st != "已提交" {
		t.Fatalf("补填凭证后 submit_state = %v, 期望 已提交", st)
	}

	// ⑤ 「已提交」清单此时含 1 条。
	_, mEnv2 := m1m6Do(e, http.MethodGet, "/api/submission?state=已提交", cookie, "", nil)
	if total := mustData(t, mEnv2)["total"].(float64); total != 1 {
		t.Fatalf("补填后「已提交」清单条数 = %v, 期望 1", total)
	}
}

// TestSubmissionIdempotencySameKeySamePayloadReplays 同键 + 同载荷 → 200 且返回首次登记结果。
//
// 语义来源：docs/05-API.md §2.2「服务端命中则返回首次结果」/ §8「报送登记：命中返回首次结果」。
// 网络重试与客户端重复点击属同一意图的同一载荷，若一律 40900，调用方无法区分
// 「已成功」与「请求被改坏了」。
func TestSubmissionIdempotencySameKeySamePayloadReplays(t *testing.T) {
	e, db, auth := newM1M6App(t)
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_ops", Role: roleOpsSupervisor, Active: true})
	cookie := auth.Establish("ou_ops")

	headers := map[string]string{"Idempotency-Key": "idem-abc-001"}
	body := `{"biz_no":"SUB-2609-0002","subject_type":"公户付款","hn_finish_date":"2026-09-21",
	          "items":[{"item_biz_no":"PR-1","item_type":"PR"},{"item_biz_no":"CT-1","item_type":"CT"}]}`

	rec1, env1 := m1m6Do(e, http.MethodPost, "/api/submission", cookie, body, headers)
	if rec1.Code != http.StatusOK || env1.Code != codeOK {
		t.Fatalf("首次提交失败: http=%d code=%d body=%s", rec1.Code, env1.Code, rec1.Body.String())
	}
	d1 := mustData(t, env1)
	firstID, _ := d1["id"].(float64)
	if firstID == 0 {
		t.Fatalf("首次提交响应缺少 id: %v", d1)
	}
	if d1["idempotent_replay"] == true {
		t.Errorf("首次登记不应标记 idempotent_replay")
	}

	rec2, env2 := m1m6Do(e, http.MethodPost, "/api/submission", cookie, body, headers)
	if rec2.Code != http.StatusOK || env2.Code != codeOK {
		t.Fatalf("同键同载荷重放: http=%d code=%d body=%s, 期望 200/0", rec2.Code, env2.Code, rec2.Body.String())
	}
	d2 := mustData(t, env2)
	if got := d2["id"].(float64); got != firstID {
		t.Errorf("重放返回 id = %v, 期望与首次一致 %v", got, firstID)
	}
	if d2["idempotent_replay"] != true {
		t.Errorf("重放未标记 idempotent_replay=true")
	}
	// 首次结果的业务字段应原样返回。
	if d2["biz_no"] != d1["biz_no"] || d2["submit_state"] != d1["submit_state"] ||
		d2["overdue"] != d1["overdue"] {
		t.Errorf("重放响应与首次结果不一致: 首次=%v 重放=%v", d1, d2)
	}
	if n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_submission`); n != 1 {
		t.Errorf("同键同载荷后报送行数 = %d, 期望 1（不得重复落库）", n)
	}
}

// TestSubmissionIdempotencySameKeyDifferentPayloadConflicts 同键 + 异载荷 → 40900。
//
// 同一幂等键被用于不同载荷属真正的键复用冲突（代码缺陷 / 键串用），必须显式报错，
// 否则会静默丢弃第二次写入意图。
func TestSubmissionIdempotencySameKeyDifferentPayloadConflicts(t *testing.T) {
	e, db, auth := newM1M6App(t)
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_ops", Role: roleOpsSupervisor, Active: true})
	cookie := auth.Establish("ou_ops")

	headers := map[string]string{"Idempotency-Key": "idem-abc-002"}
	body1 := `{"biz_no":"SUB-2609-0003","subject_type":"公户付款","hn_finish_date":"2026-09-21"}`
	body2 := `{"biz_no":"SUB-2609-0003","subject_type":"公户付款","hn_finish_date":"2026-09-22"}` // 日期不同

	rec1, env1 := m1m6Do(e, http.MethodPost, "/api/submission", cookie, body1, headers)
	if rec1.Code != http.StatusOK || env1.Code != codeOK {
		t.Fatalf("首次提交失败: http=%d code=%d body=%s", rec1.Code, env1.Code, rec1.Body.String())
	}
	rec2, env2 := m1m6Do(e, http.MethodPost, "/api/submission", cookie, body2, headers)
	if rec2.Code != http.StatusConflict || env2.Code != codeConflict {
		t.Fatalf("同键异载荷: http=%d code=%d, 期望 409 / 40900", rec2.Code, env2.Code)
	}
	if n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_submission`); n != 1 {
		t.Errorf("同键异载荷后报送行数 = %d, 期望 1", n)
	}
}

// TestSubmissionIdempotencyItemsOrderInsensitive 关联单据项顺序不同但集合相同 → 视为同一载荷（200 重放）。
func TestSubmissionIdempotencyItemsOrderInsensitive(t *testing.T) {
	e, db, auth := newM1M6App(t)
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_ops", Role: roleOpsSupervisor, Active: true})
	cookie := auth.Establish("ou_ops")

	headers := map[string]string{"Idempotency-Key": "idem-abc-003"}
	bodyA := `{"biz_no":"SUB-2609-0004","subject_type":"公户付款",
	           "items":[{"item_biz_no":"PR-1","item_type":"PR"},{"item_biz_no":"CT-1","item_type":"CT"}]}`
	bodyB := `{"biz_no":"SUB-2609-0004","subject_type":"公户付款",
	           "items":[{"item_biz_no":"CT-1","item_type":"CT"},{"item_biz_no":"PR-1","item_type":"PR"}]}`

	rec1, env1 := m1m6Do(e, http.MethodPost, "/api/submission", cookie, bodyA, headers)
	if rec1.Code != http.StatusOK || env1.Code != codeOK {
		t.Fatalf("首次提交失败: http=%d code=%d body=%s", rec1.Code, env1.Code, rec1.Body.String())
	}
	rec2, env2 := m1m6Do(e, http.MethodPost, "/api/submission", cookie, bodyB, headers)
	if rec2.Code != http.StatusOK || env2.Code != codeOK {
		t.Fatalf("项顺序不同: http=%d code=%d body=%s, 期望 200/0（载荷等价）",
			rec2.Code, env2.Code, rec2.Body.String())
	}
	if n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_submission`); n != 1 {
		t.Errorf("项顺序不同后报送行数 = %d, 期望 1", n)
	}
}

// TestSubmissionNoKeyRejectsDuplicateBizNo 不带幂等键时，业务单号重复 → 40900（唯一键路径）。
func TestSubmissionNoKeyRejectsDuplicateBizNo(t *testing.T) {
	e, db, auth := newM1M6App(t)
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_ops", Role: roleOpsSupervisor, Active: true})
	cookie := auth.Establish("ou_ops")

	body := `{"biz_no":"SUB-DUP","subject_type":"公户付款"}`
	if rec, env := m1m6Do(e, http.MethodPost, "/api/submission", cookie, body, nil); rec.Code != http.StatusOK || env.Code != codeOK {
		t.Fatalf("首次提交失败: http=%d code=%d body=%s", rec.Code, env.Code, rec.Body.String())
	}
	rec2, env2 := m1m6Do(e, http.MethodPost, "/api/submission", cookie, body, nil)
	if rec2.Code != http.StatusConflict || env2.Code != codeConflict {
		t.Fatalf("重复业务单号: http=%d code=%d, 期望 409 / 40900", rec2.Code, env2.Code)
	}
	if n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_submission`); n != 1 {
		t.Errorf("重复业务单号后报送行数 = %d, 期望 1", n)
	}
}

// TestSubmissionRequiresBizNo ★ 业务单号必填（P0：SQLite 的 UNIQUE 对 NULL 不生效）。
//
// 若不强制必填，`biz_no` 以 NULL 落库；而列级 `UNIQUE` 允许任意多个 NULL
// （已实测），业务唯一键形同虚设 → 可产生重复报送。
func TestSubmissionRequiresBizNo(t *testing.T) {
	e, db, auth := newM1M6App(t)
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_ops", Role: roleOpsSupervisor, Active: true})
	cookie := auth.Establish("ou_ops")

	rec, env := m1m6Do(e, http.MethodPost, "/api/submission", cookie,
		`{"subject_type":"公户付款"}`, nil)
	if rec.Code != http.StatusBadRequest || env.Code != codeBadRequest {
		t.Fatalf("缺业务单号: http=%d code=%d, 期望 400 / 40000", rec.Code, env.Code)
	}
	// 纯空白同样视为缺失。
	rec2, env2 := m1m6Do(e, http.MethodPost, "/api/submission", cookie,
		`{"biz_no":"   ","subject_type":"公户付款"}`, nil)
	if rec2.Code != http.StatusBadRequest || env2.Code != codeBadRequest {
		t.Fatalf("空白业务单号: http=%d code=%d, 期望 400 / 40000", rec2.Code, env2.Code)
	}
	if n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_submission`); n != 0 {
		t.Errorf("非法请求不得落库，报送行数 = %d", n)
	}
}

// TestSubmissionIdemKeyIsolatedPerActor 幂等键按调用方隔离，不得跨用户复用同一键读到他人记录。
func TestSubmissionIdemKeyIsolatedPerActor(t *testing.T) {
	e, db, auth := newM1M6App(t)
	seedDefaultUsers(t, db,
		store.UserRole{OpenID: "ou_ops_a", Role: roleOpsSupervisor, Active: true},
		store.UserRole{OpenID: "ou_ops_b", Role: roleOpsSupervisor, Active: true},
	)

	headers := map[string]string{"Idempotency-Key": "shared-key-001"}
	bodyA := `{"biz_no":"SUB-A-1","subject_type":"公户付款"}`
	bodyB := `{"biz_no":"SUB-B-1","subject_type":"公户付款"}`

	recA, envA := m1m6Do(e, http.MethodPost, "/api/submission", auth.Establish("ou_ops_a"), bodyA, headers)
	if recA.Code != http.StatusOK || envA.Code != codeOK {
		t.Fatalf("甲方提交失败: http=%d code=%d body=%s", recA.Code, envA.Code, recA.Body.String())
	}
	// 乙方复用同一键：应各自新建（键按 actor 隔离），而不是读到甲方的记录。
	recB, envB := m1m6Do(e, http.MethodPost, "/api/submission", auth.Establish("ou_ops_b"), bodyB, headers)
	if recB.Code != http.StatusOK || envB.Code != codeOK {
		t.Fatalf("乙方提交失败: http=%d code=%d body=%s（不得因他人用过的键而冲突/复用）",
			recB.Code, envB.Code, recB.Body.String())
	}
	dA, dB := mustData(t, envA), mustData(t, envB)
	if dA["id"] == dB["id"] {
		t.Errorf("跨调用方复用了同一报送记录: id=%v（幂等键未按 actor 隔离 → 信息泄漏）", dA["id"])
	}
	if dB["biz_no"] != "SUB-B-1" {
		t.Errorf("乙方拿到的是他人记录: biz_no=%v", dB["biz_no"])
	}
	if n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_submission`); n != 2 {
		t.Errorf("报送行数 = %d, 期望 2", n)
	}
}

// TestSubmissionStateGuardRequiresReceipt ★「无凭证视为未提交」不得表现为「静默丢弃状态」。
//
// 原实现只拦「已提交」：无凭证时把「已付款 / 已驳回 / 办理中」写进库，
// 而读取路径的 ComputeSubmitState 又把它盖回「未提交」→ 写进去与查出来不一致。
// 修复后除「未提交」外的任何状态都必须先有移交凭证，否则 400。
func TestSubmissionStateGuardRequiresReceipt(t *testing.T) {
	e, db, auth := newM1M6App(t)
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_ops", Role: roleOpsSupervisor, Active: true})
	cookie := auth.Establish("ou_ops")

	// 无凭证的报送（不传 receipt_ref）。
	id := createSubmissionRaw(t, e, cookie,
		`{"biz_no":"SUB-NORCPT","subject_type":"公户付款","hn_finish_date":"2026-09-21"}`, nil)

	for _, st := range []string{"已提交", "办理中", "已付款", "已驳回"} {
		t.Run("group→"+st, func(t *testing.T) {
			rec, env := m1m6Do(e, http.MethodPost, fmt.Sprintf("/api/submission/%d/group", id), cookie,
				`{"submit_state":"`+st+`"}`, nil)
			if rec.Code != http.StatusBadRequest || env.Code != codeBadRequest {
				t.Fatalf("无凭证登记为「%s」: http=%d code=%d, 期望 400 / 40000", st, rec.Code, env.Code)
			}
		})
	}
	// 驳回处置同样必须先有凭证。
	rec, env := m1m6Do(e, http.MethodPost, fmt.Sprintf("/api/submission/%d/reject", id), cookie,
		`{"action":"驳回重走","reason":"材料不全"}`, nil)
	if rec.Code != http.StatusBadRequest || env.Code != codeBadRequest {
		t.Fatalf("无凭证登记驳回: http=%d code=%d, 期望 400 / 40000", rec.Code, env.Code)
	}

	// 状态未被写成「已付款」之类的不可达值（读取路径会盖回「未提交」）。
	_, lEnv := m1m6Do(e, http.MethodGet, "/api/submission", cookie, "", nil)
	row := mustData(t, lEnv)["items"].([]any)[0].(map[string]any)
	if row["submit_state"] != "未提交" {
		t.Errorf("无凭证报送的 submit_state = %v, 期望 未提交", row["submit_state"])
	}

	// 登记凭证后，同一状态登记应当成功。
	if rrec, rEnv := m1m6Do(e, http.MethodPost, fmt.Sprintf("/api/submission/%d/receipt", id), cookie,
		`{"receipt_ref":"SIGN-G-1"}`, nil); rrec.Code != http.StatusOK || rEnv.Code != codeOK {
		t.Fatalf("登记移交凭证失败: http=%d code=%d body=%s", rrec.Code, rEnv.Code, rrec.Body.String())
	}
	grec, gEnv := m1m6Do(e, http.MethodPost, fmt.Sprintf("/api/submission/%d/group", id), cookie,
		`{"submit_state":"办理中","grp_state":"已受理"}`, nil)
	if grec.Code != http.StatusOK || gEnv.Code != codeOK {
		t.Fatalf("有凭证登记为办理中失败: http=%d code=%d body=%s", grec.Code, gEnv.Code, grec.Body.String())
	}
	if st := mustData(t, gEnv)["submit_state"]; st != "办理中" {
		t.Errorf("submit_state = %v, 期望 办理中（有凭证时不得被盖回）", st)
	}
}

// TestSubmissionOverdueFlag 3 个工作日超期标记正确（FR-M6-03）。
func TestSubmissionOverdueFlag(t *testing.T) {
	e, db, auth := newM1M6App(t)
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_ops", Role: roleOpsSupervisor, Active: true})
	cookie := auth.Establish("ou_ops")

	overdueID := createSubmissionRaw(t, e, cookie,
		`{"biz_no":"SUB-OLD","subject_type":"公户付款","hn_finish_date":"2020-01-01"}`, nil)
	freshID := createSubmissionRaw(t, e, cookie,
		`{"biz_no":"SUB-NEW","subject_type":"公户付款","hn_finish_date":"2999-12-31"}`, nil)

	_, env := m1m6Do(e, http.MethodGet, "/api/submission", cookie, "", nil)
	byID := map[int64]map[string]any{}
	for _, it := range mustData(t, env)["items"].([]any) {
		m := it.(map[string]any)
		byID[int64(m["id"].(float64))] = m
	}
	if ov := byID[overdueID]["overdue"]; ov != true {
		t.Errorf("历史单据 overdue = %v, 期望 true", ov)
	}
	if ov := byID[freshID]["overdue"]; ov != false {
		t.Errorf("未来单据 overdue = %v, 期望 false", ov)
	}

	// overdue=true 过滤只应命中历史单据。
	_, fenv := m1m6Do(e, http.MethodGet, "/api/submission?overdue=true", cookie, "", nil)
	fdata := mustData(t, fenv)
	if total := fdata["total"].(float64); total != 1 {
		t.Fatalf("overdue=true 条数 = %v, 期望 1", total)
	}
	if got := int64(fdata["items"].([]any)[0].(map[string]any)["id"].(float64)); got != overdueID {
		t.Errorf("overdue=true 命中 id = %d, 期望 %d", got, overdueID)
	}
}

// TestSubmissionPackageExportAudit 凭证包导出写审计留痕（TC-26）+ 40400。
func TestSubmissionPackageExportAudit(t *testing.T) {
	e, db, auth := newM1M6App(t)
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_ops", Role: roleOpsSupervisor, Active: true})
	cookie := auth.Establish("ou_ops")

	id := createSubmissionRaw(t, e, cookie,
		`{"biz_no":"SUB-PKG","subject_type":"公户付款","amount_cents":480000,"pay_method":"对公直付",
		  "hn_finish_date":"2026-09-20","submit_date":"2026-09-23","receipt_ref":"SIGN-9",
		  "items":[{"item_biz_no":"PR-2609-0001","item_type":"PR"}]}`, nil)

	rec, _ := m1m6Do(e, http.MethodGet, fmt.Sprintf("/api/submission/%d/package?format=zip", id), cookie, "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("导出 zip 状态码 = %d, body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get(echo.HeaderContentType); !strings.Contains(ct, "zip") {
		t.Errorf("zip Content-Type = %q", ct)
	}
	if !bytes.HasPrefix(rec.Body.Bytes(), []byte("PK")) {
		t.Errorf("zip 文件头不合法")
	}

	pdfRec, _ := m1m6Do(e, http.MethodGet, fmt.Sprintf("/api/submission/%d/package?format=pdf", id), cookie, "", nil)
	if pdfRec.Code != http.StatusOK || !bytes.HasPrefix(pdfRec.Body.Bytes(), []byte("%PDF")) {
		t.Errorf("导出 pdf 失败: http=%d body=%q", pdfRec.Code, pdfRec.Body.String())
	}

	if n := storetest.Count(t, db,
		`SELECT COUNT(*) FROM t_audit_log WHERE action='export' AND resource='submission_package'`); n < 2 {
		t.Errorf("导出留痕条数 = %d, 期望 ≥2", n)
	}

	// 不存在的报送 → 40400。
	nrec, nenv := m1m6Do(e, http.MethodGet, "/api/submission/999999/package?format=zip", cookie, "", nil)
	if nrec.Code != http.StatusNotFound || nenv.Code != codeNotFound {
		t.Errorf("不存在记录: http=%d code=%d, 期望 404 / 40400", nrec.Code, nenv.Code)
	}
}

// TestSubmissionRoleForbidden 非授权角色调用报送接口 → 40300 并留痕。
func TestSubmissionRoleForbidden(t *testing.T) {
	e, db, auth := newM1M6App(t)
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_app", Role: "申请人", Active: true})
	cookie := auth.Establish("ou_app")

	rec, env := m1m6Do(e, http.MethodPost, "/api/submission", cookie,
		`{"biz_no":"SUB-X","subject_type":"公户付款"}`, nil)
	if rec.Code != http.StatusForbidden || env.Code != codeForbidden {
		t.Fatalf("越权调用: http=%d code=%d, 期望 403 / 40300", rec.Code, env.Code)
	}
	if n := storetest.Count(t, db,
		`SELECT COUNT(*) FROM t_audit_log WHERE action='denied' AND result='deny'`); n < 1 {
		t.Errorf("越权调用未留痕")
	}
}
