package httpapi

// B6 · CT usage_category 从关联 PR 带入的预填端点验收：
//   GET /api/instances/:code/prefill?keys=...
//   ① 白名单（spec source=system 减 related 带入排除集）内键 → 回显 ext 值（缺省键不出现）；
//   ② 白名单外键（含排除集四件套与任意非 system 字段）→ 40000（不泄漏任意 ext 键）；
//   ③ keys 空 → 400；④ 越权/不存在 → 403/404（复用 instanceAllowed 口径）。

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/seed"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

func TestInstancePrefillUsageCategories(t *testing.T) {
	e, db, auth, _ := newAdminTestApp(t)
	if _, err := seed.SeedQ3Defaults(context.Background(), db); err != nil {
		t.Fatalf("播种默认口径失败: %v", err)
	}
	seedDefaultUsers(t, db,
		store.UserRole{OpenID: "ou_admin", Role: roleSysAdmin, Active: true},
		store.UserRole{OpenID: "ou_gm", Role: roleProjectGM, Active: true},
	)
	now := time.Now().UTC()
	if err := db.UpsertInstance(context.Background(), &store.Instance{
		InstanceCode: "I-PF-1", ApprovalCode: "code-ct", DocType: "CT", BizNo: "CT-2609-0001",
		Status: "PENDING", ApplicantOpenID: "ou_a", Department: "生产部", Source: "flow",
		ExtJSON:   `{"usage_category_l1":"P01","usage_category_l2":"P01-01","secret_note":"内部备注"}`,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("写实例失败: %v", err)
	}
	gm := auth.Establish("ou_gm")

	// ① 白名单内两键：有值回显、缺省键不出现（本例 l2 有值 ⇒ 都回显）
	rec, env := doRequest(e, http.MethodGet,
		"/api/instances/I-PF-1/prefill?keys=usage_category_l1,usage_category_l2", gm, "")
	if rec.Code != http.StatusOK || env.Code != codeOK {
		t.Fatalf("prefill: http=%d code=%d body=%s", rec.Code, env.Code, rec.Body.String())
	}
	pf, _ := mustData(t, env)["prefill"].(map[string]any)
	if pf["usage_category_l1"] != "P01" || pf["usage_category_l2"] != "P01-01" {
		t.Errorf("prefill = %v，期望 usage 两键来自 ext", pf)
	}
	// 缺省键不出现（只请求存在的键时另测）：
	rec, env = doRequest(e, http.MethodGet,
		"/api/instances/I-PF-1/prefill?keys=usage_category_l2", gm, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("单键 prefill %d", rec.Code)
	}
	pf, _ = mustData(t, env)["prefill"].(map[string]any)
	if _, has := pf["usage_category_l1"]; has {
		t.Errorf("未请求的键不应出现: %v", pf)
	}

	// ② 白名单外：排除集（related_biz_no）与任意非 system 键（同为探测 ext 的通道）均拒
	for _, bad := range []string{"related_biz_no", "secret_note", "biz_no"} {
		rec, env := doRequest(e, http.MethodGet,
			"/api/instances/I-PF-1/prefill?keys="+bad, gm, "")
		if rec.Code != http.StatusBadRequest || env.Code != codeBadRequest {
			t.Errorf("键 %q 应 400/40000，实为 %d/%d（白名单失效＝可探测任意 ext 键）",
				bad, rec.Code, env.Code)
		}
	}

	// ③ keys 空 → 400
	if rec, _ := doRequest(e, http.MethodGet, "/api/instances/I-PF-1/prefill", gm, ""); rec.Code != http.StatusBadRequest {
		t.Errorf("keys 空应 400，实为 %d", rec.Code)
	}

	// ④ 不存在 → 404（allowed 过后 GetInstance 失败）；先测越权身份 403
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_fin", Role: "集团财务", Active: true})
	rec, _ = doRequest(e, http.MethodGet,
		"/api/instances/I-PF-1/prefill?keys=usage_category_l1", auth.Establish("ou_fin"), "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("越权应 403，实为 %d", rec.Code)
	}
}
