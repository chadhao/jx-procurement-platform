#!/usr/bin/env bash
# scripts/run-dev.sh —— 本地开发启动（免配置，DEV_MODE=true）。
#
# 用途：无飞书凭据时也能起服务并验证：
#   - 启动自检四项（/healthz、/readyz）
#   - 开发注入端点 POST /internal/dev/inject-event
#   - 业务接口（/api/*，需先经免登建立会话）
#
# 注意：DEV_MODE=true 时，/auth/feishu/callback 允许 ?open_id= 直连登录，
#       且 /internal/dev/inject-event 仅在 DEV_MODE 下注册，生产环境绝不注册。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
mkdir -p data logs

export JX_ENV="test"
export DEV_MODE="true"
export JX_LOG_LEVEL="${JX_LOG_LEVEL:-info}"
export JX_DATA_DIR="${JX_DATA_DIR:-$ROOT/data}"
export JX_DB_PATH="${JX_DB_PATH:-$JX_DATA_DIR/jxapproval.db}"
export JX_LOCK_PATH="${JX_LOCK_PATH:-$JX_DATA_DIR/jxapproval.lock}"
export JX_LISTEN_ADDR="${JX_LISTEN_ADDR:-127.0.0.1:8080}"
export JX_SESSION_KEY="${JX_SESSION_KEY:-dev-only-session-key-change-me}"
export JX_RECONCILE_INTERVAL_HOURS="${JX_RECONCILE_INTERVAL_HOURS:-24}"

echo "开发模式启动：listen=$JX_LISTEN_ADDR db=$JX_DB_PATH dev_mode=$DEV_MODE"
exec go run ./cmd/jxapproval serve
