#!/usr/bin/env bash
# scripts/build.sh —— 一键构建：前端产物 → //go:embed 目录 → 单二进制。
#
# 步骤：
#   1) 前端构建（web/ → web/dist）
#   2) 复制产物到 internal/webui/dist（供 //go:embed 内嵌）
#   3) Go 构建单二进制到 bin/jxapproval（版本号由 git 描述注入）
#
# 环境：Go 1.24+ / Node 22+。SQLite 使用纯 Go 驱动（modernc.org/sqlite），无需 gcc。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

BUILD_VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
OUT_DIR="${OUT_DIR:-$ROOT/bin}"
mkdir -p "$OUT_DIR"

echo "==> [1/3] 前端构建 (web/)"
if [ -d web ]; then
  if [ ! -d web/node_modules ]; then
    echo "    npm install ..."
    ( cd web && npm install )
  fi
  ( cd web && npm run build )

  echo "==> [2/3] 复制前端产物到 internal/webui/dist"
  rm -rf internal/webui/dist
  mkdir -p internal/webui/dist
  cp -R web/dist/. internal/webui/dist/
else
  echo "    跳过：未发现 web/ 目录"
fi

echo "==> [3/3] Go 构建单二进制"
go build -trimpath \
  -ldflags "-s -w -X main.version=${BUILD_VERSION}" \
  -o "$OUT_DIR/jxapproval" ./cmd/jxapproval

echo "构建完成：$OUT_DIR/jxapproval (${BUILD_VERSION})"
