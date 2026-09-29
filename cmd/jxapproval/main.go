// Command jxapproval 是采购与费用审批平台（自建侧）的可执行入口。
//
// 用法：
//
//	jxapproval serve        启动服务（默认子命令，供 systemd 使用）
//	jxapproval version      打印版本
//	jxapproval help         打印用法
//
// 启动顺序（实际装配序见 bootstrap.go 分段注释 ①–⑪；本注释与之对齐 —— A6）：
//
//	加载配置 → 单实例锁 → spec 内嵌装载（S1–S12 校验失败拒启，M1）→ 打开 DB →
//	执行迁移 → 播种权限 → 启动自检 → 配置映射/定义自检 → 装配（链计算烟测）→
//	回收卡死作业 → 起飞书长连接 → 起 worker → 起 Echo HTTP → 优雅退出（信号处理）。
//
// ★ 第二实例获取单实例锁失败必须以非 0 退出码拒绝启动（ADR-01 / TC-04）。
package main

import (
	"fmt"
	"os"
)

// version 构建版本号（可由 -ldflags 注入）。
// ★ 0.3.5-s3（2026-09-28）：**开发冻结点**。含 B64/R32「作业认领改 CAS 原子化」
// （`store.ClaimDueJobs`，修「多 worker 重复执行同一作业」）+ 本轮全部文档收口
// （`docs/18` 状态快照与冻结说明 · `docs/06 §T` · `docs/08 §4.6(c)`/§12 V1.11 ·
// `docs/03` V1.14 `TC-91`~`TC-93` · `docs/11` V1.20 `R32`/`R33` ·
// `docs/14` V1.6 §9.2 / V1.7 §9.3 · `docs/README` 定案 `#83`/`#84`）。
// ★ 冻结期间不再新增代码；恢复开发时先读 `docs/18`。
var version = "0.3.5-s3"

const usage = `江熙新材审批系统（自建侧）

用法:
  jxapproval serve                       启动服务（默认）
  jxapproval seed                        幂等播种 Q3 默认权限口径（可重复执行，不覆盖已改规则）
  jxapproval import-config <config.json>  导入五类配置（approval_code / field_id / ledger_type / threshold / ledger_field）
  jxapproval import-config --check <config.json>
                                        只校验配置文件、不写入库（填完样例后先自检）
  jxapproval version                     打印版本
  jxapproval help                        打印本帮助

配置全部通过环境变量注入，见 .env.example 与部署文档（架构 §7）。
配置映射样例见 docs/reference/config-mapping.sample.json；模板建立步骤见 docs/07-Template-Build-Guide.md。`

func main() {
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}

	switch cmd {
	case "serve":
		if err := run(version); err != nil {
			fmt.Fprintf(os.Stderr, "启动失败：%v\n", err)
			os.Exit(1)
		}
	case "seed":
		if err := runSeed(); err != nil {
			fmt.Fprintf(os.Stderr, "播种失败：%v\n", err)
			os.Exit(1)
		}
	case "import-config":
		// 支持：jxapproval import-config <file.json>
		//      jxapproval import-config --check <file.json>   （只校验、不落库）
		args := os.Args[2:]
		checkOnly := false
		if len(args) > 0 && args[0] == "--check" {
			checkOnly = true
			args = args[1:]
		}
		var path string
		if len(args) > 0 {
			path = args[0]
		}
		if err := runImportConfig(checkOnly, path); err != nil {
			fmt.Fprintf(os.Stderr, "导入配置映射失败：%v\n", err)
			os.Exit(1)
		}
	case "version", "-v", "--version":
		fmt.Printf("jxapproval %s\n", version)
	case "help", "-h", "--help":
		fmt.Println(usage)
	default:
		fmt.Fprintf(os.Stderr, "未知子命令：%s\n\n%s\n", cmd, usage)
		os.Exit(2)
	}
}
