// Package specfs 将 spec/（WorkBuddy 维护的机读契约）以 //go:embed 内嵌进二进制。
//
// ★ 动因（COLLAB.md N-008 修正①）：spec 必须是构建产物的一部分 ——
//
//	① 生产环境漏拷 spec/ 目录不会导致拒启（可用性）；
//	② 运行时被改过的 spec/ 不会被静默采信（契约不可绕过）；
//	③ 与二进制同版本、不可篡改。
//	因此**禁止**运行时读外部文件；改 spec = 重新构建。
//
// ★ 放在仓库根而非 spec/ 内：go:embed 禁止 `..` 路径，根是唯一可行位置；
//
//	且 spec/ 归 WorkBuddy（COLLAB §3 位置纪律），mimo 不在其中添文件。
//	本文件是唯一的根级 .go 文件，零 internal 依赖。
package specfs

import "embed"

// FS 内嵌整个 spec/ 目录（含 forms/ 子目录与 README/RESOLUTIONS，
// 后两者仅随包携带、不被 Go 侧消费）。
//
//go:embed all:spec
var FS embed.FS
