package httpapi

// N-061 T1：L01.核销后余额 补登记 —— 四判据（①正向 ②白名单反向 ③不回归 ④零结构改动）。
// ★ 既有库更新路径 ＝ 0021 幂等迁移（复用 H1 已落地的 0020 模式，只 UPDATE 规则行）。

import (
	"context"
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
	"github.com/chadhao/jx-procurement-platform/migrations"
	"github.com/labstack/echo/v4"
)

// newL01PatchApp 运营主管身份 ＋ L01 空行（SeedQ3Defaults ＋ 0021 后白名单含 核销后余额）。
func newL01PatchApp(t *testing.T) (*echo.Echo, *store.DB, *access.Authenticator, int64) {
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
		LedgerType: "L01", BizNo: "BA-2610-0500", InstanceCode: "I-BA-0500",
		SourceDocType: "BA", Department: "仓储部", ApplicantOpenID: "ou_ops",
		ExtJSON: "{}", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	var rowID int64
	if err := db.QueryRowContext(ctx,
		`SELECT id FROM t_ledger_archive WHERE ledger_type='L01' AND biz_no='BA-2610-0500'`).
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
		Auth: access.NewAuthenticator(db, access.NewStore("test-session-key", time.Hour), nil, true, nil),
	}
	api := e.Group("/api", d.requireSession)
	api.GET("/ledger/:table/:id", d.handleLedgerGet)
	api.PATCH("/ledger/:table/:id", d.handleLedgerPatch)
	return e, db, d.Auth, rowID
}

func TestL01WrittenOffBalancePatch(t *testing.T) {
	e, _, auth, rowID := newL01PatchApp(t)
	cookie := auth.Establish("ou_ops")
	path := fmt.Sprintf("/api/ledger/L01/%d", rowID)

	// ① 正向：写 核销后余额 ⇒ 200；GET 回读可见
	rec, env := doRequest(e, http.MethodPatch, path, cookie, patchBody(t, map[string]any{
		"核销后余额": "12,345.00",
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("① 写核销后余额应 200，实为 %d（%s）", rec.Code, env.Message)
	}
	grec, genv := doRequest(e, http.MethodGet, path, cookie, "")
	if grec.Code != http.StatusOK {
		t.Fatalf("① GET 应 200，实为 %d", grec.Code)
	}
	ops, _ := mustData(t, genv)["ops"].(map[string]any)
	if s, _ := ops["核销后余额"].(string); !strings.Contains(s, "12,345") {
		t.Errorf("① 回读 ops 缺 核销后余额：%v", ops)
	}

	// ② 反向：未登记键 ⇒ 403（白名单仍在承重）
	rec2, env2 := doRequest(e, http.MethodPatch, path, cookie, patchBody(t, map[string]any{"完全野键": "x"}))
	if rec2.Code != http.StatusForbidden {
		t.Errorf("② 未登记键应 403，实为 %d（%s）", rec2.Code, env2.Message)
	}

	// ③ 不回归：付款凭据号 / 抽查状态 仍可写
	rec3, env3 := doRequest(e, http.MethodPatch, path, cookie, patchBody(t, map[string]any{
		"付款凭据号": "V-2026-01",
		"抽查状态":  "已抽查",
	}))
	if rec3.Code != http.StatusOK {
		t.Errorf("③ 既存两键应 200，实为 %d（%s）", rec3.Code, env3.Message)
	}

	// ④ 零结构改动：0021 迁移文件不得含 DDL（只 UPDATE 规则行）
	raw, err := migrations.FS.ReadFile("0021_l01_written_off_balance.sql")
	if err != nil {
		t.Fatalf("④ 读取 0021 失败: %v", err)
	}
	up := strings.ToUpper(string(raw))
	if strings.Contains(up, "CREATE TABLE") || strings.Contains(up, "ALTER TABLE") {
		t.Errorf("④ 0021 只应 UPDATE 规则行、零 DDL，实含 DDL：%s", raw)
	}
	if !strings.Contains(string(raw), "核销后余额") || !strings.Contains(string(raw), "UPDATE t_permission_rule") {
		t.Errorf("④ 0021 应为对 t_permission_rule 的 UPDATE 且含目标键：%s", raw)
	}
}
