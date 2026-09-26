package main

import (
	"context"
	"fmt"
	"os"
	"strings"

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

// runImportConfig 从 JSON 文件导入五类配置（approval_code / field_id / ledger_type / threshold / ledger_field）。
//
// ★ 为什么需要它：11 张审批模板必须**人工在飞书审批后台建**，建完才会拿到 `approval_code`
// 与各控件 `field_id`。这两组值按纪律 7/8 不得硬编码进代码，只能落 `t_config_mapping`。
// 手写 SQL 是「错一处就全线静默无数据」的地方（模板订阅不到事件时系统不会报错，只是永远没有数据），
// 故提供带严格校验的导入：**任一条不合规即整体拒绝，不做部分导入**。
//
// checkOnly = true 时只校验不落库（用于填完样例后先自检，避免把错配置写进生产库）。
// 幂等：同 (map_kind, map_key, doc_type) 覆盖更新，可反复执行以修正。
func runImportConfig(checkOnly bool, path string) error {
	if path == "" {
		return fmt.Errorf("用法：jxapproval import-config [--check] <config.json>")
	}

	payload, err := config.LoadImportFile(path)
	if err != nil {
		return err
	}
	reportNonExtractable(payload)
	reportUnconsumedThresholds(payload)
	if checkOnly {
		fmt.Printf("校验通过：approval_code %d / field_id %d / ledger_type %d / threshold %d / ledger_field %d，合计 %d 条（未写入库）\n",
			len(payload.ApprovalCode), len(payload.FieldID), len(payload.LedgerType), len(payload.Threshold),
			len(payload.LedgerField),
			len(payload.ApprovalCode)+len(payload.FieldID)+len(payload.LedgerType)+
				len(payload.Threshold)+len(payload.LedgerField))
		if len(payload.ApprovalCode) < len(config.DocTypes) {
			fmt.Printf("提示：本次只有 %d/%d 张模板的 approval_code；未导入的模板即使建好也不会推送事件（需先补齐映射并调「订阅审批事件」）。\n",
				len(payload.ApprovalCode), len(config.DocTypes))
		}
		return nil
	}

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

	res, err := config.ImportMappings(ctx, db, payload)
	if err != nil {
		return err
	}
	fmt.Printf("配置映射导入完成：approval_code %d / field_id %d / ledger_type %d / threshold %d / ledger_field %d，合计 %d 条；db=%s\n",
		res.ApprovalCode, res.FieldID, res.LedgerType, res.Threshold, res.LedgerField, res.Total(), env.DBPath)
	if res.LedgerField > 0 {
		fmt.Printf("台账字段定义 %d 条已写入 t_ledger_field_def：写接口据此校验 fields 键名（该台账无登记则不校验）\n",
			res.LedgerField)
	}

	// 回读自证：导入后必须能按 approval_code 反查到单据类型，否则模板订阅会静默无数据。
	maps, err := config.LoadMaps(ctx, db)
	if err != nil {
		return fmt.Errorf("回读校验失败: %w", err)
	}
	missing := 0
	for _, e := range payload.ApprovalCode {
		if _, ok := maps.Approval.DocType(e.Code); !ok {
			fmt.Fprintf(os.Stderr, "  ! approval_code %s 回读未命中 doc_type\n", e.Code)
			missing++
		}
	}
	if missing > 0 {
		return fmt.Errorf("回读校验失败：%d 条 approval_code 未生效", missing)
	}
	fmt.Printf("回读校验通过：%d 条 approval_code 均可反查单据类型\n", len(payload.ApprovalCode))

	if len(payload.ApprovalCode) < len(config.DocTypes) {
		fmt.Printf("提示：本次仅导入 %d/%d 张模板的 approval_code；未导入的模板即使建好也不会推送事件（需先补齐映射并调「订阅审批事件」）。\n",
			len(payload.ApprovalCode), len(config.DocTypes))
	}
	return nil
}

// reportNonExtractable 提示「合法但不参与规范列抽取」的 biz_field（多为拼写错误）。
//
// ★ 不阻断：业务可能需要新增字段名。但必须可见——因为若把 `amount` 误写成 `amout`，
// 后果是金额列**恒空**（看板金额、防拆分、抽查清单全部失效）且系统不报错。
func reportNonExtractable(p *config.ImportPayload) {
	unknown := p.NonExtractableBizFields()
	if len(unknown) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "注意：以下 %d 个 biz_field 合法但**不参与规范列抽取**（只落 t_instance_field）：%s\n",
		len(unknown), strings.Join(unknown, ", "))
	fmt.Fprintf(os.Stderr, "      可抽取的名字：%s\n", strings.Join(config.ExtractableBizFieldNames(), ", "))
	fmt.Fprintf(os.Stderr, "      若其中本意是抽取金额/供应商等，请核对拼写；确认新字段则忽略本提示。\n")
}

// reportUnconsumedThresholds 提示「已登记但当前无消费端」的阈值键（假配置）。
//
// ★ 不阻断（口径可先行登记），但必须可见——否则运维会以为设了 `purchase_tier` 就实现了分档，
// 而代码从未读过它。
func reportUnconsumedThresholds(p *config.ImportPayload) {
	unconsumed := p.UnconsumedThresholdKeys()
	if len(unconsumed) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "注意：以下 %d 个阈值键已登记但**当前无消费端**（写入后不改变任何行为）：%s\n",
		len(unconsumed), strings.Join(unconsumed, ", "))
	fmt.Fprintf(os.Stderr, "      当前真正生效的阈值键：split_supplier_month, spot_check_range\n")
	fmt.Fprintf(os.Stderr, "      这是「假配置」提示，非错误；口径可先行登记，待对应功能实现后自动生效。\n")
}
