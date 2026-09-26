#!/usr/bin/env bash
# scripts/restore.sh —— 一键恢复（FR-M8-04）。
#
# 从快照恢复数据库；默认取本地 backups/ 中最新一份，也可显式指定快照文件或 S3 对象。
# 安全：恢复前自动备份现有库（以免误操作），并优先停止服务（systemd）以释放锁/端口。
#
# 用法：
#   scripts/restore.sh                    # 用本地最新快照恢复
#   scripts/restore.sh /path/to/snap.db   # 用指定快照恢复
#
# 环境变量：JX_DB_PATH、JX_BACKUP_DIR、JX_SERVICE_NAME（默认 jxapproval）、
#           JX_S3_BUCKET 等（当传入 s3:// 前缀对象时经 aws-cli 下载）。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

DB_PATH="${JX_DB_PATH:-$ROOT/data/jxapproval.db}"
BACKUP_DIR="${JX_BACKUP_DIR:-$ROOT/backups}"
SERVICE_NAME="${JX_SERVICE_NAME:-jxapproval}"

SOURCE="${1:-}"
if [ -z "$SOURCE" ]; then
  SOURCE="$(ls -1t "$BACKUP_DIR"/jxapproval_*.db 2>/dev/null | head -n 1 || true)"
  if [ -z "$SOURCE" ]; then
    echo "错误：$BACKUP_DIR 下无可用快照，请显式传入快照路径" >&2
    exit 1
  fi
fi

# 支持从 S3 拉取
if [[ "$SOURCE" == s3://* ]]; then
  if ! command -v aws >/dev/null 2>&1; then
    echo "错误：需要 aws CLI 才能从 S3 下载" >&2
    exit 1
  fi
  TMP="$(mktemp -d)"
  aws s3 cp "$SOURCE" "$TMP/snap.db" ${JX_S3_ENDPOINT:+--endpoint-url "$JX_S3_ENDPOINT"}
  SOURCE="$TMP/snap.db"
fi

if [ ! -s "$SOURCE" ]; then
  echo "错误：快照文件不存在或为空：$SOURCE" >&2
  exit 1
fi

echo "==> [1/4] 停止服务（若由 systemd 托管）"
if command -v systemctl >/dev/null 2>&1 && systemctl is-active --quiet "$SERVICE_NAME" 2>/dev/null; then
  sudo systemctl stop "$SERVICE_NAME"
  echo "    已停止 $SERVICE_NAME"
else
  echo "    跳过：$SERVICE_NAME 未在运行或非 systemd 环境"
fi

echo "==> [2/4] 备份当前数据库（防误操作）"
if [ -f "$DB_PATH" ]; then
  SAFE="$DB_PATH.pre-restore.$(date +%Y%m%d_%H%M%S).bak"
  cp "$DB_PATH" "$SAFE"
  echo "    已保存：$SAFE"
fi

echo "==> [3/4] 用快照替换数据库（连同清理 WAL/SHM 残留）"
mkdir -p "$(dirname "$DB_PATH")"
rm -f "$DB_PATH" "$DB_PATH-wal" "$DB_PATH-shm"
cp "$SOURCE" "$DB_PATH"

if [ -f "$SOURCE.sha256" ] && command -v sha256sum >/dev/null 2>&1; then
  echo "    校验快照 SHA256…"
  ( cd "$(dirname "$SOURCE")" && sha256sum -c "$(basename "$SOURCE").sha256" )
fi

echo "==> [4/4] 重启服务"
if command -v systemctl >/dev/null 2>&1 && systemctl list-unit-files 2>/dev/null | grep -q "^${SERVICE_NAME}\.service"; then
  sudo systemctl start "$SERVICE_NAME"
  echo "    已启动 $SERVICE_NAME"
else
  echo "    请手动启动：jxapproval serve"
fi

echo "恢复完成：$DB_PATH <- $SOURCE"
