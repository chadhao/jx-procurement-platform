package httpapi

// M7（后端半）：POST /api/approval/preview —— 与 submit 共算、缺人可见暴露。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/chadhao/jx-procurement-platform/internal/access"
)

func postPreview(t *testing.T, e *echo.Echo, cookie, body string) (int, Envelope) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/approval/preview", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: access.CookieName, Value: cookie})
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	var env Envelope
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	return rec.Code, env
}

func TestPreviewBATier1WithApprovers(t *testing.T) {
	e, _, auth := newSubmitM4App(t, true)
	cookie := auth.Establish("ou_app")
	code, env := postPreview(t, e, cookie,
		`{"doc_type":"BA","amount_cents":50000,"usage_category_l1":"P01"}`)
	if code != http.StatusOK {
		t.Fatalf("preview 失败 %d：%s", code, env.Message)
	}
	d, _ := env.Data.(map[string]any)
	if d["tier"] != "purchase_tier1" {
		t.Errorf("tier = %v", d["tier"])
	}
	route, _ := d["route"].(map[string]any)
	if route["id"] != "purchase_tier1" {
		t.Errorf("route = %v", route["id"])
	}
	nodes, _ := d["nodes"].([]any)
	if len(nodes) != 6 {
		t.Errorf("nodes = %d，应为 6（全流程含非审批环节）", len(nodes))
	}
	// 缺人列表为空
	if ur, _ := d["unresolved_roles"].([]any); len(ur) != 0 {
		t.Errorf("unresolved_roles = %v，应为空", ur)
	}
	// 首个审批节点（approve_petty_cash）已解析出 ou_ops
	first := nodes[1].(map[string]any) // nodes[0]=record(动作)
	if first["source_node_id"] != "approve_petty_cash" {
		t.Fatalf("nodes[1] = %v", first["source_node_id"])
	}
	if first["resolved"] != true {
		t.Errorf("approve_petty_cash 未解析：%v", first)
	}
	apps, _ := first["approvers"].([]any)
	if len(apps) != 1 {
		t.Errorf("approvers = %d，应为 1", len(apps))
	}
}

func TestPreviewUnresolvedVisible(t *testing.T) {
	// 无审批人角色 ⇒ 预览必须暴露缺人（不阻断预览本身，R-g 缓解）
	e, _, auth := newSubmitM4App(t, false)
	cookie := auth.Establish("ou_app")
	code, env := postPreview(t, e, cookie,
		`{"doc_type":"BA","amount_cents":50000,"usage_category_l1":"P01"}`)
	if code != http.StatusOK {
		t.Fatalf("preview 应成功（缺人不算预览失败）：%d %s", code, env.Message)
	}
	d, _ := env.Data.(map[string]any)
	ur, _ := d["unresolved_roles"].([]any)
	if len(ur) == 0 {
		t.Fatal("缺人时 unresolved_roles 应非空")
	}
}

// TestPreviewUnresolvedRoleKeysN068 N-068 ①：unresolved_roles 元素键名须与契约
// {node_id,node_name,role,reason} 逐字一致（docs/05 §3.13 / preview 契约注释）。
// ★ 先红后绿：补 json tag 前实测键名为大写驼峰 ⇒ 本用例必红。
func TestPreviewUnresolvedRoleKeysN068(t *testing.T) {
	e, _, auth := newSubmitM4App(t, false) // 无角色 ⇒ 必有 unresolved
	cookie := auth.Establish("ou_app")
	code, env := postPreview(t, e, cookie,
		`{"doc_type":"BA","amount_cents":50000,"usage_category_l1":"P01"}`)
	if code != http.StatusOK {
		t.Fatalf("preview 应成功: %d %s", code, env.Message)
	}
	d, _ := env.Data.(map[string]any)
	ur, _ := d["unresolved_roles"].([]any)
	if len(ur) == 0 {
		t.Fatal("缺人时 unresolved_roles 应非空")
	}
	first, _ := ur[0].(map[string]any)
	for _, k := range []string{"node_id", "node_name", "role", "reason"} {
		if _, ok := first[k]; !ok {
			t.Errorf("契约键 %q 缺失（实测键: %v）—— 大写驼峰＝契约漂移", k, keysOf(first))
		}
	}
	for _, bad := range []string{"NodeID", "NodeName", "Role", "Reason"} {
		if _, ok := first[bad]; ok {
			t.Errorf("出现旧大写驼峰键 %q（与 docs/05 契约不一致）", bad)
		}
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestPreviewBadInput(t *testing.T) {
	e, _, auth := newSubmitM4App(t, true)
	cookie := auth.Establish("ou_app")
	// PR 落采一档 ⇒ 400（引导走 BA，N-012 过渡）
	code, env := postPreview(t, e, cookie,
		`{"doc_type":"PR","amount_cents":99999,"usage_category_l1":"P01"}`)
	if code != http.StatusBadRequest {
		t.Errorf("PR<1000 应 400，实为 %d（%s）", code, env.Message)
	}
	_ = env
}

// TestPreviewPaymentRoute T3：preview 下发 payment_route（有无合同切换路径）。
func TestPreviewPaymentRoute(t *testing.T) {
	e, _, auth := newSubmitM4App(t, true)
	cookie := auth.Establish("ou_app")
	// 无合同采一档 → 备付金
	code, env := postPreview(t, e, cookie,
		`{"doc_type":"BA","amount_cents":50000,"usage_category_l1":"P01"}`)
	if code != http.StatusOK {
		t.Fatalf("preview 失败 %d：%s", code, env.Message)
	}
	d, _ := env.Data.(map[string]any)
	if d["payment_route"] != "petty_cash" {
		t.Errorf("无合同采一档 payment_route = %v，应为 petty_cash", d["payment_route"])
	}
	// 有合同 → 公户（priority 1）
	code, env = postPreview(t, e, cookie,
		`{"doc_type":"BA","amount_cents":50000,"usage_category_l1":"P01","has_contract":true}`)
	if code != http.StatusOK {
		t.Fatalf("preview 失败 %d：%s", code, env.Message)
	}
	d, _ = env.Data.(map[string]any)
	if d["payment_route"] != "group_public_account" {
		t.Errorf("有合同 payment_route = %v，应为 group_public_account", d["payment_route"])
	}
	// 合同两级已插入（R-26 边界）
	if findNode(d, "contract_supervisor") == nil {
		t.Error("有合同时链上应含合同一级节点（R-26）")
	}
}
