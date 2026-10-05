package httpapi

// N-060 F5（FR-M9-11）：person 字段镜像命不中 ⇒ 阻断（服务端半边）。

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/specload"
	"github.com/chadhao/jx-procurement-platform/internal/store"
	storetest "github.com/chadhao/jx-procurement-platform/internal/store/storetest"
)

// personMirrorDeps 镜像 fixture：在职（ou_alive/张三）＋ 软删（ou_dead/已删的人 ——
// ListOrgUsers 含软删行 ⇒ 校验必须滤掉，否则阻断被打穿）。
func personMirrorDeps(t *testing.T) Deps {
	t.Helper()
	db := storetest.NewDB(t)
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	for _, u := range []store.OrgUser{
		{OpenID: "ou_alive", Name: "张三", EmployeeStatus: "在职", IsDeleted: false, Source: "sync", UpdatedAt: time.Now()},
		{OpenID: "ou_dead", Name: "已离职的人", EmployeeStatus: "离职", IsResigned: true, IsDeleted: true, Source: "sync", UpdatedAt: time.Now()},
	} {
		u := u
		if err := db.UpsertOrgUser(ctx, &u); err != nil {
			t.Fatal(err)
		}
	}
	return Deps{DB: db}
}

// personForm 合成表单：一个提交段 person 字段（照 n047Form 思路——真 schema 起底改 type 不便，
// 直接构造最小 FormDoc）。
func personForm() specload.FormDoc {
	return specload.FormDoc{
		DocType: "TEST",
		Sections: []specload.SectionDoc{{
			ID: "header", Label: "头", FilledAt: "",
			Fields: []specload.FieldDoc{
				{Name: "member_qc", Label: "质检", Source: "user", Type: "person", Required: false},
			},
		}},
	}
}

func TestPersonFieldsMirrorGate(t *testing.T) {
	// fixture：镜像含 open_id=ou_alive / name=张三（ListOrgUsers 只回 is_deleted=0）。
	d := personMirrorDeps(t)
	ctx := context.Background()
	form := personForm()

	// ① 命中（显示名）⇒ 放行
	if err := d.validatePersonFields(ctx, form, map[string]any{"member_qc": "张三"}); err != nil {
		t.Errorf("镜像命中（姓名）应放行：%v", err)
	}
	// ② 命中（open_id）⇒ 放行
	if err := d.validatePersonFields(ctx, form, map[string]any{"member_qc": "ou_alive"}); err != nil {
		t.Errorf("镜像命中（open_id）应放行：%v", err)
	}
	// ③ 未命中（野值）⇒ **阻断**（FR-M9-11 命不中 ⇒ 阻断）
	err := d.validatePersonFields(ctx, form, map[string]any{"member_qc": "已离职的人"})
	if err == nil {
		t.Fatal("未命中镜像应阻断（不静默放行）")
	}
	if !strings.Contains(err.Error(), "未命中") || !strings.Contains(err.Error(), "已离职的人") {
		t.Errorf("阻断文案应点名字段值：%v", err)
	}
	// ④ 空值 ⇒ 放行（归结构化 required 判据）
	if err := d.validatePersonFields(ctx, form, map[string]any{"member_qc": ""}); err != nil {
		t.Errorf("空值应放行（required 判据负责）：%v", err)
	}
	// ⑤ 无 person 字段的表单 ⇒ 直接过（不读镜像）
	if err := d.validatePersonFields(ctx, specload.FormDoc{DocType: "X"}, map[string]any{}); err != nil {
		t.Errorf("无 person 字段应直接过：%v", err)
	}
}
