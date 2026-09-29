package httpapi

// M3 验收：GET /api/approval/meta —— spec_version 聚合、forms 恰为批 1 三类、
// 枚举与档位随 schema 下发；Spec 未装配可见失败。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"

	specfs "github.com/chadhao/jx-procurement-platform"
	"github.com/chadhao/jx-procurement-platform/internal/specload"
)

func metaTestBundle(t *testing.T) *specload.Bundle {
	t.Helper()
	b, err := specload.Load(specfs.FS)
	if err != nil {
		t.Fatalf("spec 加载失败: %v", err)
	}
	return b
}

func TestHandleApprovalMeta(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/approval/meta", nil)
	rec := httptest.NewRecorder()
	d := Deps{Spec: metaTestBundle(t)}

	if err := d.handleApprovalMeta(e.NewContext(req, rec)); err != nil {
		t.Fatalf("handler 返回错误: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	var env Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	data, _ := env.Data.(map[string]any)
	if data == nil {
		t.Fatalf("data 非对象: %s", rec.Body.String())
	}

	// spec_version（N-008③）
	sv, _ := data["spec_version"].(string)
	if sv != "chain=1.0;enums=1.1;ledger=1.0;forms=BA:1.0,PR:1.0,SA:1.0" {
		t.Errorf("spec_version = %q", sv)
	}
	// doc_types_available = 批 1 三类
	dts, _ := data["doc_types_available"].([]any)
	if len(dts) != 3 {
		t.Fatalf("doc_types_available = %v，应为 BA/PR/SA", dts)
	}
	// forms 含 sections 与 checks（驱动前端渲染）
	forms, _ := data["forms"].([]any)
	if len(forms) != 3 {
		t.Fatalf("forms = %d，应为 3", len(forms))
	}
	first, _ := forms[0].(map[string]any)
	if _, ok := first["sections"]; !ok {
		t.Error("forms[0] 缺 sections")
	}
	// enums 与 bands 下发
	if _, ok := data["enums"]; !ok {
		t.Error("缺 enums")
	}
	bands, _ := data["bands"].([]any)
	if len(bands) != 3 {
		t.Errorf("bands = %d，应为 3", len(bands))
	}
}

func TestHandleApprovalMetaNotAssembled(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/approval/meta", nil)
	rec := httptest.NewRecorder()
	d := Deps{} // Spec 未装配

	if err := d.handleApprovalMeta(e.NewContext(req, rec)); err != nil {
		t.Fatalf("fail() 应写响应而非返回错误: %v", err)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d，应为 503（可见失败）", rec.Code)
	}
	var env Envelope
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if env.Code != codeNotReady {
		t.Errorf("code = %d，应为 %d", env.Code, codeNotReady)
	}
}
