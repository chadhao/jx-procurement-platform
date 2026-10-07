package feishu

// N-073 · cc_list 形状（联调实测 9499「Invalid parameter type in json: cc_list」）：
//   - 形状断言：cc_list 元素必须是**对象**且恰含飞书必填 6 键（cc_id/open_id/links/
//     read_status/create_time/update_time）—— 修前传字符串数组 ⇒ 本用例先红；
//   - 同源断言：links.pc_link 与**实例级 links**同源（externalLinks 唯一生成函数）；
//   - 反向断言：CCList 为空 ⇒ **不下发 cc_list 键**（空数组可能被平台当非法值）。

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// TestCCListNodeShapeN073 形状断言（本议题核心验收）：
// cc_list[0] 是对象、恰 6 键、read_status=UNREAD、cc_id=open_id、
// links.pc_link 与实例级 links 同源、create/update_time 为 feishuMilli 换算。
func TestCCListNodeShapeN073(t *testing.T) {
	inst := &store.Instance{
		BizNo: "BA-2610-0001", ApprovalCode: "code-ba", InstanceCode: "app:BA-2610-0001",
		DocType: "BA", UpdateTime: 7, Status: "PENDING",
		CreatedAt: snapCreatedAt, UpdatedAt: snapUpdatedAt, ApplicantOpenID: "ou_user",
	}
	snap, err := BuildSnapshot(inst, nil, []string{"ou_ops1", "ou_ops2"}, testDetailBase, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, jerr := json.Marshal(snap)
	if jerr != nil {
		t.Fatal(jerr)
	}
	// ⓪ cc_list 必须能按「对象数组」解析 —— 修前是字符串数组，unmarshal 即失败（先红点）。
	var probe struct {
		CCList []map[string]any `json:"cc_list"`
	}
	if uerr := json.Unmarshal(raw, &probe); uerr != nil {
		t.Fatalf("cc_list 应为对象数组，解析失败: %v\njson: %s", uerr, raw)
	}
	if len(probe.CCList) != 2 {
		t.Fatalf("cc_list 长度 = %d, 期望 2", len(probe.CCList))
	}
	node := probe.CCList[0]
	// ① 恰含 6 个飞书必填键（多一键少一键都属形状错）。
	wantKeys := []string{"cc_id", "open_id", "links", "read_status", "create_time", "update_time"}
	if len(node) != len(wantKeys) {
		t.Errorf("cc_list[0] 键数 = %d, 期望 %d（实键: %v）", len(node), len(wantKeys), keysOf(node))
	}
	for _, k := range wantKeys {
		if _, ok := node[k]; !ok {
			t.Errorf("cc_list[0] 缺键 %q", k)
		}
	}
	// ② read_status ＝ UNREAD（实测 code=0 的取值）。
	if node["read_status"] != "UNREAD" {
		t.Errorf("read_status = %v, 期望 UNREAD", node["read_status"])
	}
	// ③ cc_id 取 open_id（候选已去重 ⇒ 实例内唯一）。
	if node["cc_id"] != "ou_ops1" || node["open_id"] != "ou_ops1" {
		t.Errorf("cc_id/open_id = %v/%v, 期望均为 ou_ops1", node["cc_id"], node["open_id"])
	}
	// ④ links.pc_link 与实例级 links 同源（externalLinks 唯一生成函数，不另造 URL）。
	links, ok := node["links"].(map[string]any)
	if !ok {
		t.Fatalf("links 应为对象, 实为 %T", node["links"])
	}
	if links["pc_link"] != snap.Links.PCLink {
		t.Errorf("cc_list[0].links.pc_link = %v, 与实例级 links.pc_link = %q 不同源",
			links["pc_link"], snap.Links.PCLink)
	}
	wantDetail := strings.TrimRight(testDetailBase, "/") + "/approval/" + inst.BizNo
	if links["pc_link"] != wantDetail {
		t.Errorf("cc_list[0].links.pc_link = %v, 期望 %q", links["pc_link"], wantDetail)
	}
	// ⑤ 时间戳为 feishuMilli 换算的毫秒字符串（复用既有换算，不新写）。
	if node["create_time"] != feishuMilli(snapCreatedAt) {
		t.Errorf("create_time = %v, 期望 %q", node["create_time"], feishuMilli(snapCreatedAt))
	}
	if node["update_time"] != feishuMilli(snapUpdatedAt) {
		t.Errorf("update_time = %v, 期望 %q", node["update_time"], feishuMilli(snapUpdatedAt))
	}
}

// TestCCListEmptyOmitsKeyN073 反向用例：CCList 为空 ⇒ 实际发出的 body **不含 cc_list 键**
// （不是空数组 —— 空数组可能被平台当非法值）。走 httptest 捕获真实 body（组装层）。
func TestCCListEmptyOmitsKeyN073(t *testing.T) {
	var mu sync.Mutex
	var pushBody []byte
	mux := http.NewServeMux()
	mux.HandleFunc("/open-apis/auth/v3/tenant_access_token/internal", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":0,"msg":"ok","tenant_access_token":"t-test","expire":7200}`)
	})
	mux.HandleFunc("/open-apis/approval/v4/external_instances", func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("读请求体失败: %v", err)
		}
		mu.Lock()
		pushBody = b
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":0,"msg":"success"}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	hc := NewHTTPClient("cli-test", "secret-test", nil, nil)
	hc.baseURL = srv.URL
	hc.tokens = &tokenManager{appID: "cli-test", appSecret: "secret-test", baseURL: srv.URL, hc: hc.hc}

	inst := &store.Instance{
		BizNo: "BA-2610-0002", ApprovalCode: "code-ba", InstanceCode: "app:BA-2610-0002",
		DocType: "BA", UpdateTime: 7, Status: "PENDING",
		CreatedAt: snapCreatedAt, UpdatedAt: snapUpdatedAt, ApplicantOpenID: "ou_user",
	}
	// 空抄送（解析失败路径的常态）：nil 与空切片两种入参都不得下发 cc_list 键。
	for _, cc := range [][]string{nil, {}} {
		snap, err := BuildSnapshot(inst, nil, cc, testDetailBase, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := hc.UpsertExternalInstance(context.Background(), "OVERWRITE", snap); err != nil {
			t.Fatalf("推送失败: %v", err)
		}
	}
	mu.Lock()
	raw := pushBody
	mu.Unlock()
	if len(raw) == 0 {
		t.Fatal("假端点未收到 external_instances 请求体")
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if v, exists := body["cc_list"]; exists {
		t.Errorf("CCList 为空时不应下发 cc_list 键，实下发: %v", v)
	}
}

// TestUpsertCCListNodeWireBodyN073 body 组装层（UpsertExternalInstance）：有抄送时
// 实际发出的 cc_list[0] 亦为 6 键对象 —— 防「结构体对了但 body 组装写坏」。
func TestUpsertCCListNodeWireBodyN073(t *testing.T) {
	var mu sync.Mutex
	var pushBody []byte
	mux := http.NewServeMux()
	mux.HandleFunc("/open-apis/auth/v3/tenant_access_token/internal", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":0,"msg":"ok","tenant_access_token":"t-test","expire":7200}`)
	})
	mux.HandleFunc("/open-apis/approval/v4/external_instances", func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("读请求体失败: %v", err)
		}
		mu.Lock()
		pushBody = b
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":0,"msg":"success"}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	hc := NewHTTPClient("cli-test", "secret-test", nil, nil)
	hc.baseURL = srv.URL
	hc.tokens = &tokenManager{appID: "cli-test", appSecret: "secret-test", baseURL: srv.URL, hc: hc.hc}

	inst := &store.Instance{
		BizNo: "BA-2610-0003", ApprovalCode: "code-ba", InstanceCode: "app:BA-2610-0003",
		DocType: "BA", UpdateTime: 7, Status: "PENDING",
		CreatedAt: snapCreatedAt, UpdatedAt: snapUpdatedAt, ApplicantOpenID: "ou_user",
	}
	snap, err := BuildSnapshot(inst, nil, []string{"ou_ops1"}, testDetailBase, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := hc.UpsertExternalInstance(context.Background(), "OVERWRITE", snap); err != nil {
		t.Fatalf("推送失败: %v", err)
	}
	mu.Lock()
	raw := pushBody
	mu.Unlock()
	if len(raw) == 0 {
		t.Fatal("假端点未收到 external_instances 请求体")
	}
	var body struct {
		CCList []map[string]any `json:"cc_list"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("body.cc_list 应为对象数组: %v\nbody: %s", err, raw)
	}
	if len(body.CCList) != 1 {
		t.Fatalf("body.cc_list 长度 = %d, 期望 1", len(body.CCList))
	}
	wantKeys := []string{"cc_id", "open_id", "links", "read_status", "create_time", "update_time"}
	if len(body.CCList[0]) != len(wantKeys) {
		t.Errorf("body.cc_list[0] 键数 = %d, 期望 %d（实键: %v）",
			len(body.CCList[0]), len(wantKeys), keysOf(body.CCList[0]))
	}
	if body.CCList[0]["read_status"] != "UNREAD" {
		t.Errorf("body read_status = %v, 期望 UNREAD", body.CCList[0]["read_status"])
	}
}

// keysOf 返回对象键名（排序前的原序即可，仅用于报错展示）。
func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
