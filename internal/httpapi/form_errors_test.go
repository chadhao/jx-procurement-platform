package httpapi

// N-050 · form_errors 结构化错误契约测试（docs/05-API.md §4.6）。
// 四例（任务包 T5）：行内必填四要素 / rows scope（函数级）/ PR 金额 scope /
// 40010 对照（两类明细不混用）。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/specload"
)

// feOf 从 Envelope.Data 取 form_errors 数组（非数组 ⇒ nil）。
func feOf(t *testing.T, data any) []map[string]any {
	t.Helper()
	m, _ := data.(map[string]any)
	raw, ok := m["form_errors"]
	if !ok {
		return nil
	}
	arr, _ := raw.([]any)
	out := []map[string]any{}
	for _, x := range arr {
		if mm, ok := x.(map[string]any); ok {
			out = append(out, mm)
		}
	}
	return out
}

// 用例 1：重复段行内必填缺失 ⇒ 400 + form_errors[0] 四要素 + message 仍含「第 1 行」。
func TestSubmitFormErrorsRowRequired(t *testing.T) {
	e, _, auth := newPRSubmitApp(t)
	cookie := auth.Establish("ou_app")
	// 行有单价/数量（金额先不拦）但缺 material_name_spec ⇒ 走到 formcheck #6。
	badRow := `{"unit":"吨","quantity":1,"estimated_unit_price_cents":100000}`
	code, env := postSubmit(t, e, cookie, frontendShapePRBody(badRow, ""), "")
	if code != http.StatusBadRequest || env.Code != 40000 {
		t.Fatalf("行内必填缺失应 400/40000，实为 %d/%d（%s）", code, env.Code, env.Message)
	}
	fes := feOf(t, env.Data)
	if len(fes) != 1 {
		t.Fatalf("form_errors 应恰 1 元素（当前实现首个即返），实为 %d（data=%v）", len(fes), env.Data)
	}
	fe := fes[0]
	if fe["scope"] != "row" || fe["section_id"] != "detail" || fe["row_index"] != float64(1) || fe["field_name"] != "material_name_spec" {
		t.Errorf("四要素不符：scope=%v section_id=%v row_index=%v field_name=%v", fe["scope"], fe["section_id"], fe["row_index"], fe["field_name"])
	}
	if fe["kind"] != "required" {
		t.Errorf("kind = %v, 期望 required", fe["kind"])
	}
	// message 逐字保留（硬约束 1）：仍含「第 1 行」
	if !strings.Contains(env.Message, "第 1 行") {
		t.Errorf("message 必须仍含「第 1 行」（向后兼容）：%s", env.Message)
	}
	if !strings.Contains(env.Message, "表单校验失败: ") {
		t.Errorf("message 前缀应保持「表单校验失败: 」：%s", env.Message)
	}
}

// 用例 2：重复段缺失/非数组/空 ⇒ Scope=rows（函数级 —— ★ HTTP 出口当前不可达：
// PR 的 detail 缺失/非数组/空会先被 handlers_approval.go 的 PR 金额段
// （computePREstimatedTotal #7/#8）拦下、走 scope=amount；rows 三错误点要等
// 非 PR 的 repeating 单据接入后才在 HTTP 层现身。契约映射 #2/3/4 的结构化
// 产出在 validateRepeatingRows 已就位 ⇒ 此处函数级钉住。）
func TestValidateRepeatingRowsScopeRows(t *testing.T) {
	sec := repeatingDetailSec(t) // 真 spec 的 PR detail 段（含 required user 字段）
	cases := []struct {
		name string
		in   map[string]any
	}{
		{"缺失", map[string]any{}},
		{"非数组", map[string]any{"detail": "not-array"}},
		{"空数组", map[string]any{"detail": []any{}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateRepeatingRows(sec, c.in)
			if err == nil {
				t.Fatal("应 400 可见失败")
			}
			fe, ok := err.(formError)
			if !ok {
				t.Fatalf("应为 formError 结构化错误，实为 %T（%v）", err, err)
			}
			if fe.Scope != "rows" || fe.SectionID != "detail" || fe.RowIndex != 0 {
				t.Errorf("scope/section_id/row_index = %s/%s/%d, 期望 rows/detail/0", fe.Scope, fe.SectionID, fe.RowIndex)
			}
			if fe.Kind != "struct" {
				t.Errorf("kind = %s, 期望 struct", fe.Kind)
			}
		})
	}
}

// 用例 3：PR 明细缺数量 ⇒ 400 + scope=amount + row_index 正确。
func TestSubmitFormErrorsAmountScope(t *testing.T) {
	e, _, auth := newPRSubmitApp(t)
	cookie := auth.Establish("ou_app")
	// 缺 quantity ⇒ computePREstimatedTotal #10（金额段先于表单校验）。
	noQty := `{"material_name_spec":"滤布 1200mm","unit":"吨","estimated_unit_price_cents":100000}`
	code, env := postSubmit(t, e, cookie, frontendShapePRBody(noQty, ""), "")
	if code != http.StatusBadRequest || env.Code != 40000 {
		t.Fatalf("缺数量应 400/40000，实为 %d/%d（%s）", code, env.Code, env.Message)
	}
	fes := feOf(t, env.Data)
	if len(fes) != 1 {
		t.Fatalf("form_errors 应恰 1 元素，实为 %d（data=%v）", len(fes), env.Data)
	}
	fe := fes[0]
	if fe["scope"] != "amount" || fe["section_id"] != "detail" || fe["row_index"] != float64(1) {
		t.Errorf("scope/section_id/row_index = %v/%v/%v, 期望 amount/detail/1", fe["scope"], fe["section_id"], fe["row_index"])
	}
	if fe["kind"] != "value" {
		t.Errorf("kind = %v, 期望 value", fe["kind"])
	}
	if !strings.Contains(env.Message, "缺单价") && !strings.Contains(env.Message, "数量") {
		t.Errorf("message 应逐字保留： %s", env.Message)
	}
}

// 用例 4（★ 对照 · 两类明细不混用）：审批链算不到人 ⇒ 40010 + unresolved_roles，
// 且 data.form_errors 不存在。
func TestSubmitUnresolvedRolesHasNoFormErrors(t *testing.T) {
	e, _, auth := newSubmitM4App(t, false) // 不配任何角色 ⇒ 链算不到人
	cookie := auth.Establish("ou_app")
	code, env := postSubmit(t, e, cookie, baSubmitBody, "")
	if code != http.StatusBadRequest || env.Code != 40010 {
		t.Fatalf("缺人应 400/40010，实为 %d/%d（%s）", code, env.Code, env.Message)
	}
	raw, _ := json.Marshal(env.Data)
	if !strings.Contains(string(raw), "unresolved_roles") {
		t.Errorf("应有 unresolved_roles：%s", raw)
	}
	if fes := feOf(t, env.Data); len(fes) != 0 {
		t.Errorf("40010 不得携带 form_errors（两类明细按 code 分流、不得混用）：%v", fes)
	}
	if m, _ := env.Data.(map[string]any); m != nil {
		if _, has := m["form_errors"]; has {
			t.Errorf("data 不得含 form_errors 键：%v", m)
		}
	}
}

// repeatingDetailSec 取真 spec 的 PR repeating 段（detail）。
func repeatingDetailSec(t *testing.T) specload.SectionDoc {
	t.Helper()
	form := metaTestBundle(t).Forms["PR"]
	var sec specload.SectionDoc
	for _, s := range form.Sections {
		if s.Repeating {
			sec = s
		}
	}
	if sec.ID == "" {
		t.Fatal("PR 表单缺 repeating section（detail）—— spec 结构变了？")
	}
	return sec
}
