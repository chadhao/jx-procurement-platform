package httpapi

// 字段值域目录（GET /api/admin/permission-column-catalog）用例。
//
// 覆盖交办验收判据①：200 且含 key/label；抽查与 spec/forms/*.json 的 name/label 一致；
// kinds 三分（money/data/writable/ledger）；label_conflict 如实登记；
// catalog ⊇ 既有规则全部取值（「与真实可填值一致」）。

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/seed"
	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
)

// catalogItems 取出目录 items（[]columnCatalogItem 形态，经 JSON 往返与线上一致）。
func catalogItems(t *testing.T, env Envelope) []columnCatalogItem {
	t.Helper()
	raw, err := json.Marshal(mustData(t, env)["items"])
	if err != nil {
		t.Fatalf("序列化 items 失败: %v", err)
	}
	var items []columnCatalogItem
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatalf("解析 items 失败: %v", err)
	}
	return items
}

func findCatalogItem(items []columnCatalogItem, key string) *columnCatalogItem {
	for i := range items {
		if items[i].Key == key {
			return &items[i]
		}
	}
	return nil
}

func hasKind(it *columnCatalogItem, kind string) bool {
	for _, k := range it.Kinds {
		if k == kind {
			return true
		}
	}
	return false
}

// TestAdminColumnCatalog 目录主体：200 / key+label / 抽查与 spec 一致 / kinds / 冲突登记。
func TestAdminColumnCatalog(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	if _, err := seed.SeedQ3Defaults(context.Background(), db); err != nil {
		t.Fatalf("播种默认口径失败: %v", err)
	}
	seedSysAdmin(t, db, "ou_admin")
	cookie := auth.Establish("ou_admin")

	rec, env := doRequest(e, http.MethodGet, "/api/admin/permission-column-catalog", cookie, "")
	if rec.Code != http.StatusOK || env.Code != codeOK {
		t.Fatalf("目录接口状态码 = %d / code=%d, 期望 200/0, body=%s", rec.Code, env.Code, rec.Body.String())
	}
	items := catalogItems(t, env)
	if len(items) < 100 {
		t.Fatalf("目录条目过少 = %d（聚合疑似失效，期望 ≥100：spec/forms 全量字段 199 个 name）", len(items))
	}

	// ① 每个条目都必须有 key；label 允许空串（ledger 键可能无中文标签）。
	for i, it := range items {
		if it.Key == "" {
			t.Fatalf("items[%d] 缺 key", i)
		}
	}

	// ② 抽查与 spec/forms/*.json 的 name/label 一致（实测样例）。
	//   ★ label＝**首个非空**中文标签，而 doc_type 按字典序归并 ⇒ BA 最先（BA=biz_no「备案单号」）。
	spot := []struct{ key, wantLabel string }{
		{"biz_no", "备案单号"},            // BA 首个非空标签（GR 的「验收单号」进 label_conflict）
		{"applicant", "需求人"},          // BA 首个；BJ/GR/RFQ 等同名不同标签 ⇒ 冲突
		{"member_purchaser", "采购经办人"}, // 仅 GR
		{"amount_cents", "金额"},        // BA「金额」/ SA「申请金额」同名不同标签 ⇒ 冲突
	}
	for _, s := range spot {
		it := findCatalogItem(items, s.key)
		if it == nil {
			t.Fatalf("目录缺字段 %s（与 spec/forms 不一致）", s.key)
		}
		if it.Label != s.wantLabel {
			t.Errorf("%s label = %q, 期望 %q（首个非空中文标签）", s.key, it.Label, s.wantLabel)
		}
		if !hasKind(it, kindData) {
			t.Errorf("%s 缺 data 类别（来自表单字段）: kinds=%v", s.key, it.Kinds)
		}
	}

	// ③ amount_cents：金额类 + 多表单。
	if it := findCatalogItem(items, "amount_cents"); it == nil || !hasKind(it, kindMoney) {
		t.Errorf("amount_cents 应为 money 类别: %+v", it)
	} else {
		if len(it.Forms) < 2 {
			t.Errorf("amount_cents 应出现在多个表单: forms=%v", it.Forms)
		}
		if len(it.LabelConflict) == 0 {
			t.Errorf("amount_cents 在 BA=金额 / SA=申请金额 同名不同标签 ⇒ label_conflict 不应为空")
		}
	}

	// ④ applicant 跨表单同名不同标签 ⇒ 冲突如实列出（不静默挑一个）。
	if it := findCatalogItem(items, "applicant"); it == nil || len(it.LabelConflict) == 0 {
		t.Errorf("applicant 跨 11 表单标签不一 ⇒ label_conflict 不应为空: %+v", it)
	}

	// ⑤ kinds_legend 齐四类且含 money 语义（*_cents）。
	raw, _ := json.Marshal(mustData(t, env)["kinds_legend"])
	var legend []columnCatalogKind
	if err := json.Unmarshal(raw, &legend); err != nil {
		t.Fatalf("解析 kinds_legend 失败: %v", err)
	}
	if len(legend) != 4 {
		t.Fatalf("kinds_legend = %d 类, 期望 4（money/data/writable/ledger）", len(legend))
	}
	for _, k := range legend {
		if k.Kind == kindMoney && !strings.Contains(k.Meaning, "_cents") {
			t.Errorf("money 图例未说明 *_cents 口径: %q", k.Meaning)
		}
	}
}

// TestAdminColumnCatalogWritableAndLedgerKinds 可写/台账两类来自规则侧（不硬编码清单）。
func TestAdminColumnCatalogWritableAndLedgerKinds(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	if _, err := seed.SeedQ3Defaults(context.Background(), db); err != nil {
		t.Fatalf("播种默认口径失败: %v", err)
	}
	seedSysAdmin(t, db, "ou_admin")
	cookie := auth.Establish("ou_admin")

	_, env := doRequest(e, http.MethodGet, "/api/admin/permission-column-catalog", cookie, "")
	items := catalogItems(t, env)

	// 可写业务列：seed 的 opsSupervisor/opsHandler/opsAcceptor 取值（中文列名）。
	for _, key := range []string{"经办状态", "检验结论", "是否按期"} {
		it := findCatalogItem(items, key)
		if it == nil {
			t.Fatalf("目录缺可写字段 %s（与规则 writable_fields 不一致）", key)
		}
		if !hasKind(it, kindWritable) {
			t.Errorf("%s 应为 writable 类别: kinds=%v", key, it.Kinds)
		}
		if it.Label != key {
			t.Errorf("%s 自标签 label = %q, 期望原值", key, it.Label)
		}
	}
	// 台账键：金额同义键 amount_display / formula_flags 不在表单中、但是合法 column_deny 值。
	for _, key := range []string{"amount_display", "formula_flags"} {
		it := findCatalogItem(items, key)
		if it == nil {
			t.Fatalf("目录缺台账键 %s（seed amountDeny 的合法值）", key)
		}
		if !hasKind(it, kindMoney) || !hasKind(it, kindLedger) {
			t.Errorf("%s 应同时为 money + ledger: kinds=%v", key, it.Kinds)
		}
	}
}

// TestAdminColumnCatalogCoversRuleValues catalog ⊇ 既有规则全部取值（真实可填值一致）。
func TestAdminColumnCatalogCoversRuleValues(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	if _, err := seed.SeedQ3Defaults(context.Background(), db); err != nil {
		t.Fatalf("播种默认口径失败: %v", err)
	}
	seedSysAdmin(t, db, "ou_admin")
	cookie := auth.Establish("ou_admin")

	rules, err := db.ListPermissionRules(context.Background())
	if err != nil {
		t.Fatalf("读取规则失败: %v", err)
	}
	_, env := doRequest(e, http.MethodGet, "/api/admin/permission-column-catalog", cookie, "")
	items := catalogItems(t, env)
	inCatalog := map[string]bool{}
	for _, it := range items {
		inCatalog[it.Key] = true
	}
	for _, r := range rules {
		for _, v := range append(append(append([]string{}, r.ColumnAllow...), r.ColumnDeny...), r.WritableFields...) {
			if v == "" {
				continue
			}
			if !inCatalog[v] {
				t.Errorf("规则取值 %q（%s × %s）不在目录中 ⇒ 用户无法从下拉重新选回", v, r.Resource, r.Role)
			}
		}
	}
}

// TestAdminColumnCatalogForbidden 非系统管理员 / 未登录 ⇒ 403 / 401。
func TestAdminColumnCatalogForbidden(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	if _, err := seed.SeedQ3Defaults(context.Background(), db); err != nil {
		t.Fatalf("播种默认口径失败: %v", err)
	}
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_lead", Role: "主管领导", Department: "生产部", Active: true})
	lead := auth.Establish("ou_lead")

	rec, env := doRequest(e, http.MethodGet, "/api/admin/permission-column-catalog", lead, "")
	if rec.Code != http.StatusForbidden || env.Code != codeForbidden {
		t.Fatalf("非管理员读目录：http=%d code=%d, 期望 403/40300", rec.Code, env.Code)
	}
	if n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_audit_log WHERE action='denied' AND result='deny'`); n < 1 {
		t.Errorf("非管理员越权调用未留痕（denied 记录数=%d）", n)
	}
	rec2, _ := doRequest(e, http.MethodGet, "/api/admin/permission-column-catalog", "", "")
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("未登录读目录：http=%d, 期望 401", rec2.Code)
	}
}
