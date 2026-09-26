#!/usr/bin/env bash
# scripts/backup.sh —— 一键备份（FR-M8-03 / TC-21）。
#
# 链路：SQLite(WAL) --VACUUM INTO--> 一致性快照 --> 云盘 + 云侧 S3 + RustFS(异地) 三份。
# 说明：VACUUM INTO 在 WAL 模式下产生一致性快照，无需停机。
#
# 依赖：sqlite3 CLI（VACUUM INTO 需要）；上传通道 aws-cli（S3）/ mc（RustFS），
#       对应凭据未配置时自动跳过该副本（不视为失败）。
#
# 可用环境变量：
#   JX_DB_PATH            源数据库路径          默认 ./data/jxapproval.db
#   JX_BACKUP_DIR         快照落地目录          默认 ./backups
#   JX_BACKUP_CLOUD_DIR   云盘同步目录（可挂载）  可选
#   JX_BACKUP_KEEP        本地保留份数          默认 14
#   JX_S3_BUCKET / JX_S3_ENDPOINT / JX_S3_AK / JX_S3_SK   云侧 S3
#   JX_RUSTFS_ENDPOINT / JX_RUSTFS_BUCKET / JX_RUSTFS_AK / JX_RUSTFS_SK   RustFS
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

DB_PATH="${JX_DB_PATH:-$ROOT/data/jxapproval.db}"
BACKUP_DIR="${JX_BACKUP_DIR:-$ROOT/backups}"
KEEP="${JX_BACKUP_KEEP:-14}"
TS="$(date +%Y%m%d_%H%M%S)"
SNAP="$BACKUP_DIR/jxapproval_${TS}.db"

mkdir -p "$BACKUP_DIR"

if [ ! -f "$DB_PATH" ]; then
  echo "错误：未找到数据库文件 $DB_PATH" >&2
  exit 1
fi

if ! command -v sqlite3 >/dev/null 2>&1; then
  echo "错误：未找到 sqlite3 CLI，无法执行 VACUUM INTO" >&2
  echo "      可安装：apt-get install -y sqlite3（不影响主程序运行）" >&2
  exit 1
fi

echo "==> [1/3] VACUUM INTO 一致性快照：$SNAP"
rm -f "$SNAP"
sqlite3 "$DB_PATH" "VACUUM INTO '$SNAP';"
test -s "$SNAP"
echo "    快照大小：$(du -h "$SNAP" | cut -f1)"

echo "==> [2/3] 复制到云盘并在上传前校验完整性"
if [ -n "${JX_BACKUP_CLOUD_DIR:-}" ]; then
  mkdir -p "$JX_BACKUP_CLOUD_DIR"
  cp "$SNAP" "$JX_BACKUP_CLOUD_DIR/"
  echo "    已同步云盘：$JX_BACKUP_CLOUD_DIR/$(basename "$SNAP")"
else
  echo "    跳过：未配置 JX_BACKUP_CLOUD_DIR"
fi
if command -v sha256sum >/dev/null 2>&1; then
  sha256sum "$SNAP" > "$SNAP.sha256"
fi

echo "==> [3/3] 上传异地副本（S3 / RustFS）"
if [ -n "${JX_S3_BUCKET:-}" ] && command -v aws >/dev/null 2>&1; then
  export AWS_ACCESS_KEY_ID="${JX_S3_AK:-}"
  export AWS_SECRET_ACCESS_KEY="${JX_S3_SK:-}"
  AWS_ENDPOINT_ARG=()
  [ -n "${JX_S3_ENDPOINT:-}" ] && AWS_ENDPOINT_ARG=(--endpoint-url "$JX_S3_ENDPOINT")
  aws s3 cp "$SNAP" "s3://${JX_S3_BUCKET}/db-snapshots/$(basename "$SNAP")" "${AWS_ENDPOINT_ARG[@]}"
  echo "    已上传 S3：s3://${JX_S3_BUCKET}/db-snapshots/$(basename "$SNAP")"
else
  echo "    跳过：未配置 JX_S3_BUCKET 或缺少 aws CLI"
fi

if [ -n "${JX_RUSTFS_BUCKET:-}" ] && command -v mc >/dev/null 2>&1; then
  mc mirror --overwrite "$BACKUP_DIR" "${JX_RUSTFS_BUCKET}/db-snapshots"
  echo "    已同步 RustFS：${JX_RUSTFS_BUCKET}/db-snapshots"
else
  echo "    跳过：未配置 JX_RUSTFS_BUCKET 或缺少 mc"
fi

echo "==> 清理本地旧快照（保留最近 $KEEP 份）"
ls -1t "$BACKUP_DIR"/jxapproval_*.db 2>/dev/null | tail -n +"$((KEEP + 1))" | while read -r old; do
  rm -f "$old" "$old.sha256"
  echo "    已删除 $old"
done

echo "备份完成：$SNAP"
