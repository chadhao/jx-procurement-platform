package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/chadhao/jx-procurement-platform/internal/seed"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// 本文件为「静默缺陷」修复的负向断言（2026-09-27 全库排查 C6）。
//
// 每一条都对应一个「不报错但结果错」的旧行为。判据一律是**否定式**：
// 「坏数据不得被当成空数据」「不得覆盖既有字段」「不得静默升级语义」。

// doInternalRequest 调内部运维端点（带 X-Internal-Token）。
func doInternalRequest(e *echo.Echo, method, path, body string) (*httptest.ResponseRecorder, Envelope) {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set("X-Internal-Token", testInternalToken)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	var env Envelope
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	return rec, env
}

// TestC6MalformedBodyReturns400 请求体**格式错误**必须 400，不得被当成"未提供"。
//
// 反例（修复前 `_ = decodeBody`）：`{"approval_codes":"BA"}`（类型错）解析失败被丢弃 →
// req 为零值 → 分支走「订阅全部」→ 接口返回 200，对**全部** approval_code 发起订阅。
// 语义被静默升级，且消耗飞书 API 配额无人察觉。
func TestC6MalformedBodyReturns400(t *testing.T) {
	e, _, _, _ := newAdminTestApp(t)

	rec, env := doInternalRequest(e, http.MethodPost, "/internal/sync/subscribe", `{"approval_codes":"BA"}`)
	if rec.Code != http.StatusBadRequest || env.Code != codeBadRequest {
		t.Fatalf("非法请求体应 400/40000，实际 http=%d code=%d body=%s",
			rec.Code, env.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "results") {
		t.Errorf("非法请求体不得触发订阅动作（响应里出现了 results）: %s", rec.Body.String())
	}

	// 反向：**空 body 仍必须合法**（=订阅全部），不能因为收紧而把正常用法挡掉。
	rec2, _ := doInternalRequest(e, http.MethodPost, "/internal/sync/subscribe", "")
	if rec2.Code != http.StatusOK {
		t.Fatalf("空 body 应视为「未提供」并正常执行，实际 http=%d body=%s", rec2.Code, rec2.Body.String())
	}
}

// TestC6LedgerWriteRefusesCorruptOps 既有运营字段**解析失败**时，写接口必须拒绝。
//
// 反例（修复前）：`loadOps` 吞掉解析错误 → 返回空 map → 合入本次 fields → **整体写回**
// → 该行**既有的全部运营字段被抹掉**，而接口返回 200（静默数据丢失）。
func TestC6LedgerWriteRefusesCorruptOps(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	ctx := context.Background()
	if _, err := seed.SeedQ3Defaults(ctx, db); err != nil {
		t.Fatalf("播种默认口径失败: %v", err)
	}
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_ops", Role: roleOpsSupervisor, Active: true})

	seedArchiveExt(t, db, "L01", "BA-C6-A", "ou_a", "生产部", 100, `{}`)
	// 直接写一段**非法 JSON** 到 ops_json（模拟数据损坏）。
	const corrupt = `{"经办状态":`
	if err := db.UpsertOps(ctx, &store.LedgerOps{LedgerType: "L01", BizNo: "BA-C6-A", OpsJSON: corrupt}); err != nil {
		t.Fatalf("写损坏 ops 失败: %v", err)
	}
	var id int64
	if err := db.QueryRowContext(ctx, `SELECT id FROM t_ledger_archive WHERE ledger_type='L01' AND biz_no='BA-C6-A'`).Scan(&id); err != nil {
		t.Fatalf("取 id 失败: %v", err)
	}

	rec, env := doRequest(e, http.MethodPatch, "/api/ledger/L01/"+itoaTest(id), auth.Establish("ou_ops"),
		`{"fields":{"抽查状态":"已完成"}}`)
	if rec.Code != http.StatusInternalServerError || env.Code != codeInternal {
		t.Fatalf("既有 ops 损坏时应拒绝写入（500/50000），实际 http=%d code=%d body=%s",
			rec.Code, env.Code, rec.Body.String())
	}
	// ★ 负向断言：ops_json **必须原样保留**（不得被空对象覆盖）。
	var after string
	if err := db.QueryRowContext(ctx,
		`SELECT COALESCE(ops_json,'') FROM t_ledger_ops WHERE ledger_type='L01' AND biz_no='BA-C6-A'`).Scan(&after); err != nil {
		t.Fatalf("回读 ops 失败: %v", err)
	}
	if after != corrupt {
		t.Errorf("损坏的 ops_json 被改写了：%q → %q（拒绝写入时不得产生任何副作用）", corrupt, after)
	}
}

// TestC6LedgerRowMarksCorruptExt 扩展字段解析失败的行必须带**可见标记**。
//
// 反例（修复前）：`_ = json.Unmarshal` 吞错 → 该行 archive 为空对象 →
// 用户以为"本来就没填"，而不是"数据坏了"。
func TestC6LedgerRowMarksCorruptExt(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	ctx := context.Background()
	if _, err := seed.SeedQ3Defaults(ctx, db); err != nil {
		t.Fatalf("播种默认口径失败: %v", err)
	}
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_ops", Role: roleOpsSupervisor, Active: true})

	seedArchiveExt(t, db, "L01", "BA-C6-B", "ou_a", "生产部", 100, `{"broken":`)
	seedArchiveExt(t, db, "L01", "BA-C6-OK", "ou_a", "生产部", 100, `{"fine":"yes"}`)

	rec, env := doRequest(e, http.MethodGet, "/api/ledger/L01", auth.Establish("ou_ops"), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("台账列表应 200（单个坏行不该让整页失败），实际 %d: %s", rec.Code, rec.Body.String())
	}
	data := mustData(t, env)
	items, _ := data["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("期望 2 行，实际 %d", len(items))
	}
	var badMarked, okMarked int
	for _, it := range items {
		m := it.(map[string]any)
		_, marked := m["_data_warning"]
		switch m["biz_no"] {
		case "BA-C6-B":
			if marked {
				badMarked++
			}
		case "BA-C6-OK":
			if marked {
				okMarked++
			}
		}
	}
	if badMarked != 1 {
		t.Errorf("ext_json 损坏的行必须带 _data_warning 标记（否则用户以为是没填）")
	}
	if okMarked != 0 {
		t.Errorf("正常行不得出现 _data_warning（区分度丢失）")
	}
}

// TestC6DashboardReportsCorruptRows 看板必须把「坏行数」带进 warnings。
//
// 反例（修复前）：坏行的 ext/ops 被当空 → 指标**偏低却不报错**，
// 看数字只能看到"小了一点"，无从判断是业务少还是数据坏。
func TestC6DashboardReportsCorruptRows(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	ctx := context.Background()
	if _, err := seed.SeedQ3Defaults(ctx, db); err != nil {
		t.Fatalf("播种默认口径失败: %v", err)
	}
	seedRole(t, db, "ou_pm", "项目总经理", "")

	// 看板 14 会读 L03（buildSupervision）与 L04/L07（订单执行派生）。
	seedArchiveExt(t, db, "L03", "PR-C6-1", "ou_a", "生产部", 100, `{"broken":`)

	rec, env := doRequest(e, http.MethodGet, "/api/dashboard/14?period=2026-09", auth.Establish("ou_pm"), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("看板应 200，实际 %d: %s", rec.Code, rec.Body.String())
	}
	data := mustData(t, env)
	ws, _ := data["warnings"].([]any)
	if len(ws) == 0 {
		t.Fatal("存在坏行时看板必须给出 warnings（否则指标偏低无从察觉）")
	}
	joined := ""
	for _, w := range ws {
		joined += w.(string)
	}
	if !strings.Contains(joined, "无法解析") {
		t.Errorf("warnings 未说明原因，实际: %s", joined)
	}
}

// TestC6DashboardNoWarningsWhenClean 反向断言：数据干净时**不得**出现 warnings
// （否则 warnings 沦为噪声，等于没有）。
func TestC6DashboardNoWarningsWhenClean(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	ctx := context.Background()
	if _, err := seed.SeedQ3Defaults(ctx, db); err != nil {
		t.Fatalf("播种默认口径失败: %v", err)
	}
	seedRole(t, db, "ou_pm", "项目总经理", "")
	seedArchiveExt(t, db, "L03", "PR-C6-2", "ou_a", "生产部", 100, `{"assigned_open_id":"ou_h"}`)

	rec, env := doRequest(e, http.MethodGet, "/api/dashboard/14?period=2026-09", auth.Establish("ou_pm"), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("看板应 200，实际 %d: %s", rec.Code, rec.Body.String())
	}
	data := mustData(t, env)
	if ws, ok := data["warnings"].([]any); ok && len(ws) > 0 {
		t.Errorf("数据干净时不应有 warnings，实际: %v", ws)
	}
}
