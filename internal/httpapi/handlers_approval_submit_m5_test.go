package httpapi

// M5 验收：提交实时回源（FR-M9-17）—— 成功快照 / 失败告警放行 / 未装配可见。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// fakeOrgVerifier 按脚本回放回源结果。
type fakeOrgVerifier struct {
	user *store.OrgUser
	err  error
}

func (f *fakeOrgVerifier) GetUser(_ context.Context, _ string) (*store.OrgUser, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.user, nil
}

// readOrgVerify 提交成功后读 ext_json.org_verify。
func readOrgVerify(t *testing.T, db *store.DB, bizNo string) map[string]any {
	t.Helper()
	inst, err := db.GetInstanceByBizNo(context.Background(), bizNo)
	if err != nil {
		t.Fatal(err)
	}
	ext := map[string]any{}
	if inst.ExtJSON != "" && inst.ExtJSON != "{}" {
		if err := json.Unmarshal([]byte(inst.ExtJSON), &ext); err != nil {
			t.Fatalf("ext_json 解析失败: %v", err)
		}
	}
	ov, _ := ext["org_verify"].(map[string]any)
	if ov == nil {
		t.Fatalf("ext_json 缺 org_verify：%s", inst.ExtJSON)
	}
	return ov
}

func TestSubmitOrgVerifySuccess(t *testing.T) {
	e, db, auth := newSubmitM4AppV(t, true, &fakeOrgVerifier{
		user: &store.OrgUser{OpenID: "ou_app", Name: "申请人甲", EmployeeStatus: "在职"},
	})
	cookie := auth.Establish("ou_app")
	code, env := postSubmit(t, e, cookie, baSubmitBody, "")
	if code != http.StatusOK {
		t.Fatalf("提交失败 %d：%s", code, env.Message)
	}
	d, _ := env.Data.(map[string]any)
	ov := readOrgVerify(t, db, d["biz_no"].(string))
	if ov["ok"] != true || ov["employee_status"] != "在职" {
		t.Errorf("回源标记 = %v，应 ok:true + 在职快照", ov)
	}
}

func TestSubmitOrgVerifyFailureAllowsSubmit(t *testing.T) {
	// 回源失败 ⇒ **告警放行**（提交仍成功）+ ok:false 标记（FR-M9-17/D6）
	e, db, auth := newSubmitM4AppV(t, true, &fakeOrgVerifier{
		err: errors.New("feishu 503"),
	})
	cookie := auth.Establish("ou_app")
	code, env := postSubmit(t, e, cookie, baSubmitBody, "")
	if code != http.StatusOK {
		t.Fatalf("回源失败不应拦提交，实为 %d：%s", code, env.Message)
	}
	d, _ := env.Data.(map[string]any)
	ov := readOrgVerify(t, db, d["biz_no"].(string))
	if ov["ok"] != false || ov["reason"] != "live_lookup_failed" {
		t.Errorf("回源失败标记 = %v", ov)
	}
}

func TestSubmitOrgVerifyNotAssembled(t *testing.T) {
	e, db, auth := newSubmitM4AppV(t, true, nil) // 端口未装配
	cookie := auth.Establish("ou_app")
	code, env := postSubmit(t, e, cookie, baSubmitBody, "")
	if code != http.StatusOK {
		t.Fatalf("提交失败 %d：%s", code, env.Message)
	}
	d, _ := env.Data.(map[string]any)
	ov := readOrgVerify(t, db, d["biz_no"].(string))
	if ov["ok"] != false || ov["reason"] != "verifier_not_assembled" {
		t.Errorf("未装配标记 = %v（应可见、不静默）", ov)
	}
}

func TestSubmitOrgVerifyDepartmentMismatch(t *testing.T) {
	e, db, auth := newSubmitM4AppV(t, true, &fakeOrgVerifier{
		user: &store.OrgUser{OpenID: "ou_app", Name: "申请人甲", EmployeeStatus: "在职",
			PrimaryDepartmentID: "od-live"},
	})
	// 镜像部门名 ≠ 提交部门（仓储部）⇒ 只记标记、不阻断
	if err := db.UpsertOrgDepartment(context.Background(), &store.OrgDepartment{
		OpenDepartmentID: "od-live", DepartmentID: "od-live", Name: "综合运营部",
		FirstSeenAt: time.Now(), LastSeenAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	cookie := auth.Establish("ou_app")
	code, env := postSubmit(t, e, cookie, baSubmitBody, "")
	if code != http.StatusOK {
		t.Fatalf("部门不一致不应阻断，实为 %d：%s", code, env.Message)
	}
	d, _ := env.Data.(map[string]any)
	ov := readOrgVerify(t, db, d["biz_no"].(string))
	if ov["department_mismatch"] != true {
		t.Errorf("department_mismatch 未标记：%v", ov)
	}
	if !strings.Contains("综合运营部", ov["live_department"].(string)) {
		t.Errorf("live_department = %v", ov["live_department"])
	}
}
