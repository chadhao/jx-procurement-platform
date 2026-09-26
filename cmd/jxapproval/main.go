// Command jxapproval 是采购与费用审批平台（自建侧）的可执行入口。
//
// 用法：
//
//	jxapproval serve        启动服务（默认子命令，供 systemd 使用）
//	jxapproval version      打印版本
//	jxapproval help         打印用法
//
// 启动顺序（架构 §7 / 硬约束）：
//
//	加载配置 → 单实例锁 → 打开 DB → 执行迁移 → 启动自检 → 起 worker →
//	起 Echo HTTP → 起飞书长连接 → 优雅退出（信号处理）。
//
// ★ 第二实例获取单实例锁失败必须以非 0 退出码拒绝启动（ADR-01 / TC-04）。
package main

import (
	"fmt"
	"os"
)

// version 构建版本号（可由 -ldflags 注入）。
var version = "0.2.0-s2"

const usage = `JX 采购与费用审批平台（自建侧）

用法:
  jxapproval serve     启动服务（默认）
  jxapproval seed      幂等播种 Q3 默认权限口径（可重复执行，不覆盖已改规则）
  jxapproval version   打印版本
  jxapproval help      打印本帮助

配置全部通过环境变量注入，见 .env.example 与部署文档（架构 §7）。`

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
	case "version", "-v", "--version":
		fmt.Printf("jxapproval %s\n", version)
	case "help", "-h", "--help":
		fmt.Println(usage)
	default:
		fmt.Fprintf(os.Stderr, "未知子命令：%s\n\n%s\n", cmd, usage)
		os.Exit(2)
	}
}
