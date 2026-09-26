package main

import (
	"context"
	"fmt"

	"github.com/chadhao/jx-procurement-platform/internal/config"
	"github.com/chadhao/jx-procurement-platform/internal/seed"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// runSeed 幂等播种 Q3 默认权限口径（角色 × 资源），可重复执行且不覆盖管理员已改规则。
//
// 用途：① 首次部署初始化默认口径；② 独立子命令 `jxapproval seed` 手动重跑。
// 与 serve 共用同一数据目录配置（JX_DATA_DIR / JX_DB_PATH）。
func runSeed() error {
	env, err := config.LoadEnv()
	if err != nil {
		return fmt.Errorf("加载配置失败: %w", err)
	}
	ctx := context.Background()
	db, err := store.Open(env.DBPath)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	if err := store.Migrate(ctx, db); err != nil {
		return err
	}
	n, err := seed.SeedQ3Defaults(ctx, db)
	if err != nil {
		return err
	}
	fmt.Printf("Q3 默认权限口径播种完成：本次新增 %d 行（已存在的规则不覆盖）；db=%s\n", n, env.DBPath)
	return nil
}
