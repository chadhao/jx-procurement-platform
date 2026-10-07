package httpapi

// N-060 H1（R-35 · FR-M6-05 消费侧）：原件两键经既有 PATCH /api/ledger/L06/:id
// 登记（零迁移载体 · 零新端点 · 零新列）——四判据（①正向 ②白名单反向 ③既存键不破坏
// ④ t_submission 零结构改动）。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	specfs "github.com/chadhao/jx-procurement-platform"
	"github.com/chadhao/jx-procurement-platform/internal/access"
	"github.com/chadhao/jx-procurement-platform/internal/config"
	"github.com/chadhao/jx-procurement-platform/internal/observ"
	"github.com/chadhao/jx-procurement-platform/internal/permission"
	"github.com/chadhao/jx-procurement-platform/internal/seed"
	"github.com/chadhao/jx-procurement-platform/internal/specload"
	"github.com/chadhao/jx-procurement-platform/internal/store"
	storetest "github.com/chadhao/jx-procurement-platform/internal/store/storetest"
	"github.com/labstack/echo/v4"
)

// newL06PatchApp 运营主管身份 ＋ L06 空行（SeedQ3Defaults 含 N-060 新白名单）。
func newL06PatchApp(t *testing.T) (*echo.Echo, *store.DB, *access.Authenticator, int64) {
	t.Helper()
	db := storetest.NewDB(t)
	ctx := context.Background()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := seed.SeedQ3Defaults(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertUserRole(ctx, store.UserRole{
		OpenID: "ou_ops", Name: "运营", Role: "综合运营主管", Department: "综合运营部",
		Active: true, UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertArchive(ctx, db, &store.LedgerArchive{
		LedgerType: "L06", BizNo: "SUB-2610-0900", InstanceCode: "I-SUB-0900",
		SourceDocType: "SUB", Department: "综合运营部", ApplicantOpenID: "ou_ops",
		ExtJSON: "{}", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	var rowID int64
	if err := db.QueryRowContext(ctx,
		`SELECT id FROM t_ledger_archive WHERE ledger_type='L06' AND biz_no='SUB-2610-0900'`).
		Scan(&rowID); err != nil {
		t.Fatal(err)
	}
	bundle, err := specload.Load(specfs.FS)
	if err != nil {
		t.Fatal(err)
	}
	e := echo.New()
	d := Deps{
		Env: &config.Env{DevMode: true, InternalToken: testInternalToken, RunEnv: "test"},
		DB:  db, Log: observ.NewLogger("error", nil), Metrics: observ.NewMetrics(),
		Perm: permission.NewLoader(db), Maps: &config.Maps{}, Spec: bundle,
		Auth: access.NewAuthenticator(db, access.NewStore(db, "test-session-key", time.Hour), nil, true, nil),
	}
	api := e.Group("/api", d.requireSession)
	api.GET("/ledger/:table/:id", d.handleLedgerGet)
	api.PATCH("/ledger/:table/:id", d.handleLedgerPatch)
	return e, db, d.Auth, rowID
}

// patchBody 把 fields 包成 PATCH 载荷。
func patchBody(t *testing.T, fields map[string]any) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"fields": fields})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestOriginalsViaLedgerPatch(t *testing.T) {
	e, db, auth, rowID := newL06PatchApp(t)
	cookie := auth.Establish("ou_ops")
	path := fmt.Sprintf("/api/ledger/L06/%d", rowID)

	// ① 正向：写原件两键 ⇒ 200；GET 回读 ops 含两键
	rec, env := doRequest(e, http.MethodPatch, path, cookie, patchBody(t, map[string]any{
		"原件移交清单": "合同 CN-01 三份；增值税发票 FP-01～FP-03",
		"原件签收记录": "回执 RC-2610-001（签收人：王五）",
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("① 写原件两键应 200，实为 %d（%s）", rec.Code, env.Message)
	}
	grec, genv := doRequest(e, http.MethodGet, path, cookie, "")
	if grec.Code != http.StatusOK {
		t.Fatalf("① GET 应 200，实为 %d", grec.Code)
	}
	data := mustData(t, genv)
	ops, _ := data["ops"].(map[string]any)
	if s, _ := ops["原件移交清单"].(string); !strings.Contains(s, "CN-01") {
		t.Errorf("① 回读 ops 缺 原件移交清单：%v", ops)
	}
	if s, _ := ops["原件签收记录"].(string); !strings.Contains(s, "RC-2610") {
		t.Errorf("① 回读 ops 缺 原件签收记录：%v", ops)
	}

	// ② 反向：未登记键（不在 writable 白名单）⇒ 403 —— 白名单在承重
	rec2, env2 := doRequest(e, http.MethodPatch, path, cookie, patchBody(t, map[string]any{"完全野键": "x"}))
	if rec2.Code != http.StatusForbidden {
		t.Errorf("② 未登记键应 403，实为 %d（%s）", rec2.Code, env2.Message)
	}

	// ③ 既存三键仍可写（提交集团日期/集团流程编号/集团流程状态）
	rec3, env3 := doRequest(e, http.MethodPatch, path, cookie, patchBody(t, map[string]any{
		"提交集团日期": "2026-10-05",
		"集团流程编号": "G-9900",
		"集团流程状态": "办理中",
	}))
	if rec3.Code != http.StatusOK {
		t.Errorf("③ 既存三键应 200，实为 %d（%s）", rec3.Code, env3.Message)
	}

	// ④ t_submission 零结构改动：列集合 == 基线硬编码清单（出现新列即红）
	rows, err := db.QueryContext(context.Background(),
		`SELECT name FROM pragma_table_info('t_submission') ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	cols := map[string]bool{}
	nCols := 0
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		cols[n] = true
		nCols++
	}
	wantCols := []string{
		"id", "biz_no", "subject_type", "amount_cents", "pay_method", "hn_finish_date",
		"submit_date", "receipt_ref", "submit_state", "grp_accept_no", "grp_state",
		"paid_date", "reject_reason", "created_by", "created_at", "updated_at",
		"department", "applicant_open_id", "assigned_open_id", "acceptors",
	}
	for _, w := range wantCols {
		if !cols[w] {
			t.Errorf("④ t_submission 缺列 %s（结构被改？）", w)
		}
	}
	if nCols != len(wantCols) {
		t.Errorf("④ t_submission 列数 = %d, 期望 %d（零结构改动 —— 出现新列即红）", nCols, len(wantCols))
	}
}
