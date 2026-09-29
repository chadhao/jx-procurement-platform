package store

// M2 后半：ChainRoleCandidates 的行为锚 —— active 过滤 / 镜像在用过滤 /
// 镜像缺失保留 / 部门（含 extra_depts）匹配。

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func newChainRoleDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "chain-role.db"))
	if err != nil {
		t.Fatalf("打开库失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	return db
}

func upsertRole(t *testing.T, db *DB, openID, name, role, dept string, extra []string, active bool) {
	t.Helper()
	if err := db.UpsertUserRole(context.Background(), UserRole{
		OpenID: openID, Name: name, Role: role, Department: dept,
		ExtraDepts: extra, Active: active, UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("upsert role %s: %v", openID, err)
	}
}

func TestChainRoleCandidates(t *testing.T) {
	ctx := context.Background()
	db := newChainRoleDB(t)

	upsertRole(t, db, "ou_pgm", "总一", "项目总经理", "综合运营部", nil, true)
	upsertRole(t, db, "ou_sup1", "主管甲", "主管领导", "综合运营部", []string{"仓储部"}, true)
	upsertRole(t, db, "ou_sup2", "主管乙", "主管领导", "生产部", nil, true)
	upsertRole(t, db, "ou_off", "停用者", "主管领导", "仓储部", nil, false)

	t.Run("active过滤", func(t *testing.T) {
		got, err := db.ChainRoleCandidates(ctx, "主管领导", "", false)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 {
			t.Errorf("在职主管领导应 2 人（含停用者 1 人被滤），实为 %d：%v", len(got), got)
		}
	})

	t.Run("部门匹配含extra_depts", func(t *testing.T) {
		got, err := db.ChainRoleCandidates(ctx, "主管领导", "仓储部", true)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].OpenID != "ou_sup1" {
			t.Errorf("分管仓储部的主管领导应为 ou_sup1，实为 %v", got)
		}
	})

	t.Run("部门不匹配为空", func(t *testing.T) {
		got, err := db.ChainRoleCandidates(ctx, "主管领导", "质检技术部", true)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Errorf("无分管部门时应为空，实为 %v", got)
		}
	})

	t.Run("镜像缺失_保留", func(t *testing.T) {
		got, err := db.ChainRoleCandidates(ctx, "项目总经理", "", false)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].OpenID != "ou_pgm" {
			t.Errorf("镜像缺失不应被滤，实为 %v", got)
		}
	})

	t.Run("镜像离职_剔除", func(t *testing.T) {
		if err := db.UpsertOrgUser(ctx, &OrgUser{
			OpenID: "ou_pgm", Name: "总一", IsResigned: true,
			FirstSeenAt: time.Now(), LastSeenAt: time.Now(),
		}); err != nil {
			t.Fatalf("upsert org: %v", err)
		}
		got, err := db.ChainRoleCandidates(ctx, "项目总经理", "", false)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Errorf("镜像离职应被滤除，实为 %v", got)
		}
	})
}
