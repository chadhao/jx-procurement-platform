package seed

// 批 0 · A7：播种冒烟 —— 幂等（重复播种零新增）＋ 枚举与 permission 同源（A4）。

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/permission"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

func TestSeedQ3Idempotent(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "seed-a7.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}

	n1, err := SeedQ3Defaults(ctx, db)
	if err != nil {
		t.Fatalf("首次播种失败: %v", err)
	}
	if n1 <= 0 {
		t.Fatalf("首次播种应有新增，实为 %d", n1)
	}
	n2, err := SeedQ3Defaults(ctx, db)
	if err != nil {
		t.Fatalf("重复播种报错: %v", err)
	}
	if n2 != 0 {
		t.Errorf("重复播种应 0 新增（幂等），实为 %d", n2)
	}
}

func TestRolesReferencePermissionAuthority(t *testing.T) {
	// A4：seed.Roles/Resources 是 permission.Roles/Resources 的**引用**（同一切片），
	// 不是复制 —— 否则又出现两份枚举真相。
	if len(Roles) == 0 || len(Roles) != len(permission.Roles) {
		t.Fatalf("seed.Roles 与 permission.Roles 长度不一致: %d vs %d", len(Roles), len(permission.Roles))
	}
	for i := range Roles {
		if Roles[i] != permission.Roles[i] {
			t.Fatalf("seed.Roles[%d]=%q ≠ permission.Roles[%d]=%q", i, Roles[i], i, permission.Roles[i])
		}
	}
	if len(Resources) != len(permission.Resources) {
		t.Fatalf("Resources 长度不一致: %d vs %d", len(Resources), len(permission.Resources))
	}
}
