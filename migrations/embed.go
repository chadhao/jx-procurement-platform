// Package migrations 以 embed 方式打包 SQL DDL，供启动迁移器读取。
// 迁移脚本按文件名升序执行（0001_init.sql …）。
package migrations

import "embed"

// FS 内嵌 migrations 目录下全部 .sql 脚本（不引第三方迁移库，见架构 §2）。
//
//go:embed *.sql
var FS embed.FS
