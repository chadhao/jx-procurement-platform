package feishu

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// external_check_body_test.go —— external_instances/check 请求体修复回归（本批；实测 99992402）：
//
//	实测：入参必须含 `instances[]`（每项 instance_id + update_time + tasks[]，
//	tasks[] 每项 task_id + update_time）；只发 {"approval_code":…} ⇒
//	`99992402 field_violations=[instances: instances is required]`。
//	本文件锁死修复后的请求体形态。

// TestCheckExternalInstancesRequestBodyHasInstances 请求体断言：approval_code + instances[]
// （每项 update_time + tasks[]，每项 task_id + update_time）。
func TestCheckExternalInstancesRequestBodyHasInstances(t *testing.T) {
	var mu sync.Mutex
	var rawBody string
	calls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/open-apis/auth/v3/tenant_access_token/internal", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"code":0,"msg":"ok","tenant_access_token":"t-test","expire":7200}`)
	})
	mux.HandleFunc("/open-apis/approval/v4/external_instances/check", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls++
		rawBody = string(b)
		mu.Unlock()
		// ★ 实测成功形态：{"code":0,"data":{"diff_instances":[]},"msg":"success"}。
		_, _ = io.WriteString(w, `{"code":0,"data":{"diff_instances":[]},"msg":"success"}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewHTTPClient("app-test", "secret-test", nil, nil)
	c.baseURL = srv.URL
	c.tokens.baseURL = srv.URL

	// 组装待比对集合：实例 1（RELEASED 任务 1 个 + HELD 任务 1 个——HELD 未推给平台，须排除）。
	inst := &store.Instance{
		InstanceCode: "app-test:PR-2609-0300", ApprovalCode: "code-pr", BizNo: "PR-2609-0300",
		Status: "PENDING", UpdateTime: 7,
		CreatedAt: notifyAt, UpdatedAt: notifyAt,
	}
	tasks := []store.FlowTask{
		{TaskID: "PR-2609-0300-n1-ou_m1-1-1", BizNo: "PR-2609-0300", Status: "PENDING",
			ReleaseState: "RELEASED", UpdatedAt: notifyAt},
		{TaskID: "PR-2609-0300-n2-ou_gm-1-1", BizNo: "PR-2609-0300", Status: "PENDING",
			ReleaseState: "HELD", UpdatedAt: notifyAt},
	}
	items := []ExternalCheckInstance{BuildCheckInstance(inst, tasks)}

	if _, err := c.CheckExternalInstances(context.Background(), "code-pr", items); err != nil {
		t.Fatalf("对账检查不应报错: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("check 调用次数 = %d, 期望 1", calls)
	}
	var body struct {
		ApprovalCode string `json:"approval_code"`
		Instances    []struct {
			InstanceID string `json:"instance_id"`
			UpdateTime string `json:"update_time"` // ★ 官方标 string（2026-09-28 与推送侧同批对齐）
			Tasks      []struct {
				TaskID     string `json:"task_id"`
				UpdateTime string `json:"update_time"`
			} `json:"tasks"`
		} `json:"instances"`
	}
	if err := json.Unmarshal([]byte(rawBody), &body); err != nil {
		t.Fatalf("解析请求体失败: %v（body=%s）", err, rawBody)
	}
	// ① 顶层：approval_code + instances 齐备（实测缺 instances ⇒ 99992402）。
	if body.ApprovalCode != "code-pr" {
		t.Errorf("approval_code = %q, 期望 code-pr", body.ApprovalCode)
	}
	if len(body.Instances) != 1 {
		t.Fatalf("instances 项数 = %d, 期望 1（body=%s）", len(body.Instances), rawBody)
	}
	it := body.Instances[0]
	// ② 实例项：instance_id + update_time（与推送侧同源同形：版本值转字符串）。
	if it.InstanceID != "app-test:PR-2609-0300" {
		t.Errorf("instances[0].instance_id = %q, 期望 app-test:PR-2609-0300", it.InstanceID)
	}
	if it.UpdateTime != "7" {
		t.Errorf("instances[0].update_time = %q, 期望 \"7\"（与推送侧同一版本值、字符串同形）", it.UpdateTime)
	}
	// ③ tasks[]：每项 task_id + update_time；★ HELD 任务未推给平台，不得出现在比对集合。
	if len(it.Tasks) != 1 {
		t.Fatalf("instances[0].tasks 项数 = %d, 期望 1（HELD 任务须排除）", len(it.Tasks))
	}
	if it.Tasks[0].TaskID != "PR-2609-0300-n1-ou_m1-1-1" {
		t.Errorf("tasks[0].task_id = %q, 期望 RELEASED 任务的 task_id", it.Tasks[0].TaskID)
	}
	if it.Tasks[0].UpdateTime == "" || it.Tasks[0].UpdateTime == "0" {
		t.Errorf("tasks[0].update_time = %q, 期望非空毫秒字符串（与推送侧同形）", it.Tasks[0].UpdateTime)
	}
}

// TestCheckExternalInstancesEmptyInstancesSkipsCall 无本地实例 ⇒ 不发请求
// （instances 必填，空数组必 99992402；无实例＝无可比对项，属正常态）。
func TestCheckExternalInstancesEmptyInstancesSkipsCall(t *testing.T) {
	calls := 0
	var mu sync.Mutex
	mux := http.NewServeMux()
	mux.HandleFunc("/open-apis/auth/v3/tenant_access_token/internal", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"code":0,"msg":"ok","tenant_access_token":"t-test","expire":7200}`)
	})
	mux.HandleFunc("/open-apis/approval/v4/external_instances/check", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		_, _ = io.WriteString(w, `{"code":0,"data":{"diff_instances":[]},"msg":"success"}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewHTTPClient("app-test", "secret-test", nil, nil)
	c.baseURL = srv.URL
	c.tokens.baseURL = srv.URL

	out, err := c.CheckExternalInstances(context.Background(), "code-pr", nil)
	if err != nil {
		t.Fatalf("空 instances 应为正常态（不报错）: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("空 instances 应返回空差异，实际 %+v", out)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 0 {
		t.Fatalf("空 instances 不得发请求，实际 %d 次", calls)
	}
}
