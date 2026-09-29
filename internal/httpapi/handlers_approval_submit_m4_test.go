package httpapi

// M4 验收：submit 契约收敛 —— 服务端算链 / 五类可见拒绝 / Idempotency-Key 三态。

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	specfs "github.com/chadhao/jx-procurement-platform"
	"github.com/chadhao/jx-procurement-platform/internal/access"
	"github.com/chadhao/jx-procurement-platform/internal/chain"
	"github.com/chadhao/jx-procurement-platform/internal/config"
	"github.com/chadhao/jx-procurement-platform/internal/flow"
	"github.com/chadhao/jx-procurement-platform/internal/observ"
	"github.com/chadhao/jx-procurement-platform/internal/permission"
	"github.com/chadhao/jx-procurement-platform/internal/specload"
	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
)

// newSubmitM4App 装配 submit 测试路由：配置映射 + 定义 + 会话 + Spec/Chain 全就位。
// withRoles=false 时不配审批人角色（制造「链算不到人」）；verifier 为 nil ⇒ OrgVerifier 未装配。
func newSubmitM4App(t *testing.T, withRoles bool) (*echo.Echo, *store.DB, *access.Authenticator) {
	t.Helper()
	return newSubmitM4AppV(t, withRoles, nil)
}

func newSubmitM4AppV(t *testing.T, withRoles bool, verifier OrgVerifier) (*echo.Echo, *store.DB, *access.Authenticator) {
	t.Helper()
	ctx := context.Background()
	db := storetest.NewDB(t)

	// approval_code → doc_type 映射（code↔doc_type 校验的数据源）
	if err := db.UpsertConfigMapping(ctx, store.ConfigMappingRow{
		MapKind: "approval_code", MapKey: "code-ba", MapValue: "BA", DocType: "BA",
	}); err != nil {
		t.Fatal(err)
	}
	apprMap, err := config.LoadApprovalMap(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	maps := &config.Maps{Approval: apprMap, Ledger: map[string][]string{"BA": {"L01"}}}

	// 三方定义（flow.Submit 前置校验）
	if err := db.UpsertApprovalDef(ctx, &store.ApprovalDef{
		ApprovalCode: "code-ba", DocType: "BA", Name: "采购报备单", GroupName: "test",
		UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	// 申请人身份始终播种（会话解析所需）；withRoles 控制的是**审批人角色**（链解析所需）。
	if err := db.UpsertUserRole(ctx, store.UserRole{
		OpenID: "ou_app", Name: "申请人甲", Role: "申请人", Department: "仓储部",
		Active: true, UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if withRoles {
		seed := []store.UserRole{
			{OpenID: "ou_ops", Name: "运营主管", Role: "综合运营主管", Department: "综合运营部", Active: true, UpdatedAt: time.Now()},
			{OpenID: "ou_sup", Name: "主管乙", Role: "主管领导", Department: "仓储部", Active: true, UpdatedAt: time.Now()},
		}
		for _, r := range seed {
			if err := db.UpsertUserRole(ctx, r); err != nil {
				t.Fatal(err)
			}
		}
	}

	bundle, err := specload.Load(specfs.FS)
	if err != nil {
		t.Fatal(err)
	}
	metrics := observ.NewMetrics()
	env := &config.Env{DevMode: true, InternalToken: testInternalToken, RunEnv: "test"}
	perm := permission.NewLoader(db)
	sessions := access.NewStore("test-session-key", time.Hour)
	auth := access.NewAuthenticator(db, sessions, nil, true, nil)
	flowSvc := flow.NewWithConfig(db, "app", maps, nil)
	chainSvc := &chain.Service{B: bundle, Roles: chainRoleAdapterForTest{db: db}}

	e := NewRouter(Deps{
		Env: env, DB: db, Log: observ.NewLogger("error", io.Discard),
		Metrics: metrics, Health: observ.NewHealth("test"),
		Perm: perm, Auth: auth, Maps: maps, Version: "test",
		Flow: flowSvc, Spec: bundle, Chain: chainSvc, OrgVerifier: verifier,
	})
	return e, db, auth
}

// chainRoleAdapterForTest 测试内适配（与 bootstrap.chainRoleAdapter 同构）。
type chainRoleAdapterForTest struct{ db *store.DB }

func (a chainRoleAdapterForTest) Candidates(ctx context.Context, q chain.RoleQuery) ([]chain.RoleCandidate, error) {
	rows, err := a.db.ChainRoleCandidates(ctx, q.Role, q.Department, q.MatchDept)
	if err != nil {
		return nil, err
	}
	out := make([]chain.RoleCandidate, 0, len(rows))
	for _, r := range rows {
		out = append(out, chain.RoleCandidate{OpenID: r.OpenID, Name: r.Name})
	}
	return out, nil
}

const baSubmitBody = `{"doc_type":"BA","approval_code":"code-ba","amount_cents":50000,` +
	`"fields":{"usage_category_l1":"P01","usage_category_l2":"P01-01","quantity":2,` +
	`"purpose":"班中劳保补货","arrival_date":"2026-10-31","supplier":"甲供应商"}}`

func postSubmit(t *testing.T, e *echo.Echo, cookie, body, idemKey string) (int, Envelope) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/approval/submit", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: access.CookieName, Value: cookie})
	}
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	var env Envelope
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	return rec.Code, env
}

func TestSubmitRejectsCallerNodes(t *testing.T) {
	e, _, auth := newSubmitM4App(t, true)
	cookie := auth.Establish("ou_app")
	body := `{"doc_type":"BA","approval_code":"code-ba","amount_cents":50000,` +
		`"fields":{},"nodes":[{"node_id":"n1","seq":1,"approvers":[{"open_id":"ou_x"}]}]}`
	code, env := postSubmit(t, e, cookie, body, "")
	if code != http.StatusBadRequest || env.Code != 40000 {
		t.Fatalf("nodes 应 400/40000，实为 %d/%d", code, env.Code)
	}
}

func TestSubmitCodeDocTypeMismatch(t *testing.T) {
	e, _, auth := newSubmitM4App(t, true)
	cookie := auth.Establish("ou_app")
	body := `{"doc_type":"PR","approval_code":"code-ba","amount_cents":50000,"fields":{}}`
	code, env := postSubmit(t, e, cookie, body, "")
	if code != http.StatusBadRequest || env.Code != 40000 {
		t.Fatalf("code↔doc_type 不符应 400/40000，实为 %d/%d", code, env.Code)
	}
}

func TestSubmitZeroAmountRejected(t *testing.T) {
	e, _, auth := newSubmitM4App(t, true)
	cookie := auth.Establish("ou_app")
	body := `{"doc_type":"BA","approval_code":"code-ba","amount_cents":0,"fields":{}}`
	code, env := postSubmit(t, e, cookie, body, "")
	if code != http.StatusBadRequest {
		t.Fatalf("金额 0 应 400，实为 %d（%s）", code, env.Message)
	}
}

func TestSubmitUnresolvedRolesBlocked(t *testing.T) {
	// 不配任何角色 ⇒ 链算不到人（FR-M9-02 阻断）
	e, _, auth := newSubmitM4App(t, false)
	cookie := auth.Establish("ou_app")
	code, env := postSubmit(t, e, cookie, baSubmitBody, "")
	if code != http.StatusBadRequest || env.Code != 40000 {
		t.Fatalf("缺人应 400/40000，实为 %d/%d（%s）", code, env.Code, env.Message)
	}
	// error_detail.unresolved_roles 明细（N-018 过渡口径）
	raw, _ := json.Marshal(env.Data)
	if !strings.Contains(string(raw), "unresolved_roles") {
		t.Errorf("响应缺 unresolved_roles 明细：%s", raw)
	}
}

func TestSubmitIdempotencyThreeStates(t *testing.T) {
	e, db, auth := newSubmitM4App(t, true)
	cookie := auth.Establish("ou_app")

	// ① 首次提交 → 200 + biz_no
	code1, env1 := postSubmit(t, e, cookie, baSubmitBody, "key-abc")
	if code1 != http.StatusOK {
		t.Fatalf("首次提交失败 %d：%s", code1, env1.Message)
	}
	d1, _ := env1.Data.(map[string]any)
	bizNo1, _ := d1["biz_no"].(string)
	if bizNo1 == "" {
		t.Fatal("biz_no 为空")
	}

	// ② 同键同载荷 → 200 复用同一 biz_no
	code2, env2 := postSubmit(t, e, cookie, baSubmitBody, "key-abc")
	if code2 != http.StatusOK {
		t.Fatalf("重放失败 %d：%s", code2, env2.Message)
	}
	d2, _ := env2.Data.(map[string]any)
	if d2["biz_no"] != bizNo1 {
		t.Errorf("重放 biz_no = %v，应为 %s", d2["biz_no"], bizNo1)
	}
	if d2["idempotent_replay"] != true {
		t.Errorf("重放未标记 idempotent_replay")
	}

	// ③ 同键异载荷 → 40900
	other := `{"doc_type":"BA","approval_code":"code-ba","amount_cents":60000,` +
		`"fields":{"usage_category_l1":"P01","usage_category_l2":"P01-01","quantity":2,` +
		`"purpose":"换一个用途","arrival_date":"2026-10-31","supplier":"乙供应商"}}`
	code3, env3 := postSubmit(t, e, cookie, other, "key-abc")
	if code3 != http.StatusConflict || env3.Code != 40900 {
		t.Fatalf("异载荷应 409/40900，实为 %d/%d（%s）", code3, env3.Code, env3.Message)
	}

	// 全程只产生一单
	var n int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM t_instance WHERE doc_type='BA'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("t_instance BA 行数 = %d，应为 1（幂等只建一单）", n)
	}
}

func TestSubmitSuccessPath(t *testing.T) {
	e, _, auth := newSubmitM4App(t, true)
	cookie := auth.Establish("ou_app")
	code, env := postSubmit(t, e, cookie, baSubmitBody, "")
	if code != http.StatusOK {
		t.Fatalf("成功路径失败 %d：%s", code, env.Message)
	}
	d, _ := env.Data.(map[string]any)
	bizNo, _ := d["biz_no"].(string)
	if !strings.HasPrefix(bizNo, "BA-") {
		t.Errorf("biz_no = %q，应为 BA- 前缀", bizNo)
	}
}
