#!/usr/bin/env bash
# scripts/preflight.sh —— systemd 启动前预检（ExecStartPre）。
#
# 由 unit 中的 `flock -n <lock> -c preflight.sh` 调用：先取得锁，再做前置校验。
# 校验项（任一失败即以非 0 退出，阻止启动，对应 TC-04 / TC-27）：
#   1) EnvironmentFile 存在（含飞书凭据）
#   2) 数据目录存在且可写（DB 与锁文件落在此）
#   3) 残留 stale 锁清理提示（进程已退出但锁文件遗留时，flock 会自然接管，此处仅告警）
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

ENV_FILE="${JX_ENV_FILE:-/etc/jxapproval/env}"
DATA_DIR="${JX_DATA_DIR:-$ROOT/data}"
LOCK_PATH="${JX_LOCK_PATH:-$DATA_DIR/jxapproval.lock}"

fail() {
  echo "preflight 失败：$1" >&2
  exit 1
}

# 1) 环境文件（生产必须存在；开发可用 shell 环境变量替代，故仅在显式要求时强制）
if [ "${JX_REQUIRE_ENV_FILE:-0}" = "1" ]; then
  [ -f "$ENV_FILE" ] || fail "缺少环境文件 $ENV_FILE"
fi

# 2) 数据目录可写
mkdir -p "$DATA_DIR" || fail "无法创建数据目录 $DATA_DIR"
if [ ! -w "$DATA_DIR" ]; then
  fail "数据目录不可写 $DATA_DIR"
fi

# 3) 锁文件残留提示（不阻断：flock 已持有，stale 锁由内核在进程退出时释放）
if [ -f "$LOCK_PATH" ]; then
  echo "preflight：检测到既有锁文件 $LOCK_PATH（由 flock 持有，属正常；进程退出后自动释放）"
fi

echo "preflight 通过：data_dir=$DATA_DIR lock=$LOCK_PATH"
