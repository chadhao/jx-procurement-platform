package httpapi

// T2 验收（MIMO-NEXT-BATCH §4）：
//   3. 常量停用后：历史单据显示不变（值快照冻结）、新单据选不到（meta 过滤 + 提交拒绝）；
//   消费点：boot 播种 / admin CRUD（护栏）/ meta 下发 / 提交快照校验。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
)

func TestConstantLifecycleAndGuardrails(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	seedDefaultUsers(t, db,
		store.UserRole{OpenID: "ou_admin", Role: roleSysAdmin, Active: true},
	)
	admin := auth.Establish("ou_admin")

	// ① 新增（unit 表）
	rec, env := doRequest(e, http.MethodPost, "/api/admin/constants", admin,
		`{"table":"unit","value":"码"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("新增失败 %d：%s", rec.Code, env.Message)
	}
	// ② 重复 → 40900
	rec, env = doRequest(e, http.MethodPost, "/api/admin/constants", admin,
		`{"table":"unit","value":"码"}`)
	if rec.Code != http.StatusConflict || env.Code != 40900 {
		t.Errorf("重复新增应 409/40900，实为 %d/%d", rec.Code, env.Code)
	}
	// ③ ★ role_display_name 禁止新增角色（R-24 护栏）
	rec, env = doRequest(e, http.MethodPost, "/api/admin/constants", admin,
		`{"table":"role_display_name","value":"新角色"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("角色表新增应 400，实为 %d（%s）", rec.Code, env.Message)
	}
	// ④ 停用「码」（查 id）
	var id int64
	if err := db.QueryRowContext(context.Background(),
		`SELECT id FROM t_constant WHERE table_key='unit' AND value='码'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	rec, env = doRequest(e, http.MethodPut, "/api/admin/constants/"+itoaTest(id), admin,
		`{"status":"retired"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("停用失败 %d：%s", rec.Code, env.Message)
	}
	// ⑤ ★ DELETE 永远 409（只停用不删）
	rec, env = doRequest(e, http.MethodDelete, "/api/admin/constants/"+itoaTest(id), admin, "")
	if rec.Code != http.StatusConflict {
		t.Errorf("物理删除应 409，实为 %d", rec.Code)
	}
	// ⑥ 审计：create / update / delete_refused 三类都留痕
	for _, action := range []string{"constant_create", "constant_update", "constant_delete_refused"} {
		var n int
		if err := db.QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM t_audit_log WHERE action = ?`, action).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			t.Errorf("审计缺 action=%s", action)
		}
	}
}

func TestConstantSnapshotFreezesHistory(t *testing.T) {
	// ★ 快照纪律：提交时写 <字段>_snapshot ⇒ 之后改名/停用，fields 里的快照一字不变。
	ctx := context.Background()
	db := storetest.NewDB(t)
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SeedConstants(ctx, "unit", []string{"吨", "千克"}); err != nil {
		t.Fatal(err)
	}
	bundle := metaTestBundle(t)
	d := Deps{Spec: bundle, DB: db}
	form := bundle.Forms["PR"]

	fields := map[string]any{"unit": "吨"}
	if err := d.validateConstantRefs(ctx, form, fields); err != nil {
		t.Fatalf("合法常量被拒: %v", err)
	}
	if fields["unit_snapshot"] != "吨" {
		t.Fatalf("unit_snapshot = %v，应为值快照「吨」", fields["unit_snapshot"])
	}

	// 停用「吨」（不改名）：既有 fields 里的快照不受影响（字典可变、取值冻结）
	var tid int64
	if err := db.QueryRowContext(ctx,
		`SELECT id FROM t_constant WHERE table_key='unit' AND value='吨'`).Scan(&tid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.UpdateConstant(ctx, tid, "", nil, "retired"); err != nil {
		t.Fatal(err)
	}
	if fields["unit_snapshot"] != "吨" {
		t.Errorf("常量停用后历史快照被改动: %v", fields["unit_snapshot"])
	}

	// 新单据：停用值 → 拒；未登记值 → 拒
	fields2 := map[string]any{"unit": "吨"}
	if err := d.validateConstantRefs(ctx, form, fields2); err == nil || !strings.Contains(err.Error(), "停用") {
		t.Errorf("新单据选停用项应拒: %v", err)
	}
	fields3 := map[string]any{"unit": "英里"}
	if err := d.validateConstantRefs(ctx, form, fields3); err == nil || !strings.Contains(err.Error(), "未在") {
		t.Errorf("未登记值应拒: %v", err)
	}
}

func TestMetaConstantsOnlyActive(t *testing.T) {
	bundle := metaTestBundle(t)
	testDB := storetest.NewDB(t)
	t.Cleanup(func() { _ = testDB.Close() })
	if err := store.Migrate(context.Background(), testDB); err != nil {
		t.Fatal(err)
	}
	if _, err := testDB.SeedConstants(context.Background(), "unit", []string{"吨", "千克"}); err != nil {
		t.Fatal(err)
	}
	// 停用「千克」
	var id int64
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT id FROM t_constant WHERE table_key='unit' AND value='千克'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := testDB.UpdateConstant(context.Background(), id, "", nil, "retired"); err != nil {
		t.Fatal(err)
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/approval/meta", nil)
	rec := httptest.NewRecorder()
	d := Deps{Spec: bundle, DB: testDB}
	if err := d.handleApprovalMeta(e.NewContext(req, rec)); err != nil {
		t.Fatal(err)
	}
	var env Envelope
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	data, _ := env.Data.(map[string]any)
	constants, _ := data["constants"].(map[string]any)
	unitList, _ := constants["unit"].([]any)
	if len(unitList) != 1 || unitList[0] != "吨" {
		t.Errorf("meta constants.unit = %v，应只含 active 的「吨」（新单据选不到停用项）", unitList)
	}
}
