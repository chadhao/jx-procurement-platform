package httpapi

// M3 验收：GET /api/approval/meta —— spec_version 聚合、forms 恰为批 1 三类、
// 枚举与档位随 schema 下发；Spec 未装配可见失败。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	specfs "github.com/chadhao/jx-procurement-platform"
	"github.com/chadhao/jx-procurement-platform/internal/specload"
	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
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
	bundle := metaTestBundle(t)
	testDB := storetest.NewDB(t)
	if err := store.Migrate(context.Background(), testDB); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = testDB.Close() })
	d := Deps{Spec: bundle, DB: testDB}

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

	// spec_version（N-008③）—— ★ 期望串由真源（bundle.Forms）派生，不硬编码表单清单
	//（N-023：硬编码会把「批 1 临时范围」写成系统不变量，批 2 每落一张表单撞一次）。
	sv, _ := data["spec_version"].(string)
	wantSV := fmt.Sprintf("chain=%s;enums=%s;ledger=%s;forms=%s",
		bundle.Chain.Version, bundle.Enums.Version, bundle.Ledger.Version, joinFormsVersionFor(bundle))
	if sv != wantSV {
		t.Errorf("spec_version = %q，应由真源派生为 %q", sv, wantSV)
	}

	// doc_types_available：**⊇ 批 1 三张**（包含语义；新表单到来不红），且去重有序
	dts, _ := data["doc_types_available"].([]any)
	seen := map[string]bool{}
	var prev string
	for _, x := range dts {
		s, _ := x.(string)
		if seen[s] {
			t.Errorf("doc_types_available 重复: %s", s)
		}
		seen[s] = true
		if prev != "" && s < prev {
			t.Errorf("doc_types_available 未排序: %s 出现在 %s 之后", s, prev)
		}
		prev = s
	}
	for _, must := range []string{"BA", "PR", "SA"} {
		if !seen[must] {
			t.Errorf("doc_types_available 缺批 1 表单 %s：%v", must, dts)
		}
	}

	// forms：**每张都必须带 sections 与 checks** —— 这才是契约不变量
	//（"恰好 3 张"是批次临时范围；"新表单也必须结构完整"才是该守的）。
	forms, _ := data["forms"].([]any)
	if len(forms) < 3 {
		t.Fatalf("forms = %d，应 ≥3（至少含批 1 三张）", len(forms))
	}
	for i, f := range forms {
		fm, _ := f.(map[string]any)
		secs, hasSec := fm["sections"].([]any)
		if !hasSec || len(secs) == 0 {
			t.Errorf("forms[%d]（%v）缺非空 sections", i, fm["doc_type"])
		}
		chk, hasChk := fm["checks"].([]any)
		if !hasChk || len(chk) == 0 {
			t.Errorf("forms[%d]（%v）缺非空 checks", i, fm["doc_type"])
		}
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

// joinFormsVersionFor 测试侧独立拼接 spec_version 的 forms 段（与生产 buildSpecVersion
// 交叉验证，而非调用被测函数自证）。
func joinFormsVersionFor(b *specload.Bundle) string {
	dts := make([]string, 0, len(b.Forms))
	for dt := range b.Forms {
		dts = append(dts, dt)
	}
	sort.Strings(dts)
	parts := make([]string, 0, len(dts))
	for _, dt := range dts {
		parts = append(parts, dt+":"+b.Forms[dt].Version)
	}
	return strings.Join(parts, ",")
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
