#!/usr/bin/env bash
# scripts/archive-year.sh —— 年度归档（FR-M4-08；制度第五十九条「流水型台账增长」应对）。
#
# 做什么：把指定年度（默认「去年」）的流水型记录**导出**为可离线保存的归档件：
#          每表一份 CSV（带表头）+ 一份 SQL（INSERT 语句，便于按需回灌）+ 全量 sha256 清单。
# 不做什么：**不删除、不改写**任何在线数据 —— 归档后历史仍可正常查询（PRD FR-M4-08 验收标准）。
#          在线库的瘦身（删除已归档行）是**独立动作**，须经明确批准后另行执行，本脚本绝不代劳。
#
# 依赖：sqlite3 CLI。
# 输出：${JX_ARCHIVE_DIR}/<year>/{<table>.csv,<table>.sql,MANIFEST.txt,archives.tar.gz}
#
# 可用环境变量：
#   JX_DB_PATH       源数据库路径          默认 ./data/jxapproval.db
#   JX_ARCHIVE_DIR   归档落地目录          默认 ./archives
#   JX_ARCHIVE_YEAR  归档年度（YYYY）      默认「去年」
#   JX_ARCHIVE_KEEP  本地保留年度份数      默认 0（不清理；>0 时仅保留最近 N 个年度目录）
set -euo pipefail

SELF="${BASH_SOURCE[0]}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

DB_PATH="${JX_DB_PATH:-$ROOT/data/jxapproval.db}"
ARCHIVE_DIR="${JX_ARCHIVE_DIR:-$ROOT/archives}"
YEAR="${JX_ARCHIVE_YEAR:-$(date -d 'last year' +%Y 2>/dev/null || date -v-1y +%Y)}"
KEEP="${JX_ARCHIVE_KEEP:-0}"
DEST="$ARCHIVE_DIR/$YEAR"

if [ ! -f "$DB_PATH" ]; then
  echo "错误：未找到数据库文件 $DB_PATH" >&2
  exit 1
fi
if ! [[ "$YEAR" =~ ^[0-9]{4}$ ]]; then
  echo "错误：归档年度格式应为 YYYY，实际为 '$YEAR'" >&2
  exit 1
fi
if ! command -v sqlite3 >/dev/null 2>&1; then
  echo "错误：未找到 sqlite3 CLI" >&2
  exit 1
fi

mkdir -p "$DEST"

# 流水型表（按 created_at 年度切分）。★ 与 migrations/0001_init.sql 保持一致。
TABLES=(
  t_instance
  t_instance_field
  t_instance_status_history
  t_event_inbox
  t_worker_job
  t_deadletter
  t_ledger_archive
  t_ledger_ops
  t_petty_cash_receipt
  t_petty_cash_close
  t_expense_track
  t_submission
  t_submission_item
  t_audit_log
)

# ---- 锁号前提自检门禁（04a §6.4 / §10 S13；把纪律变成可执行门禁，比写在文档里靠人记可靠）----
# 单号「终态永久不复用」依赖三前提（P1–P3）。本脚本最容易破坏其中两条：
#   P1 `t_doc_seq` 不参与归档/清理（否则归档后游标归零 → 号被重发）；
#   P2 不做硬删除（否则旧行 UNIQUE(biz_no) 消失 → 锁号失效）。
# 二者被破坏的后果都是**静默**的（台账/审计出现「同号两笔」），故在此**主动拦截**：
# 违反即失败退出，绝不静默继续。
require_lock_number_preconditions() {
  # ① TABLES 数组不得包含 t_doc_seq（P1）。
  if printf '%s\n' "${TABLES[@]}" | grep -qx 't_doc_seq'; then
    echo "错误：TABLES 含 t_doc_seq → 破坏锁号前提 P1（04a §6.4），拒绝执行" >&2
    exit 1
  fi
  # ② 脚本正文不得含行首的 DELETE FROM / DROP TABLE（P2）。
  #    仅匹配「行首（可含缩进）即为该 SQL 语句」的行 —— 注释/字符串里提及同名字样不算。
  if grep -nE '^[[:space:]]*(DELETE[[:space:]]+FROM|DROP[[:space:]]+TABLE)\b' "$SELF" >/dev/null 2>&1; then
    echo "错误：脚本正文含 DELETE FROM / DROP TABLE → 破坏锁号前提 P2（04a §6.4），拒绝执行" >&2
    exit 1
  fi
}
require_lock_number_preconditions

echo "==> 年度归档 $YEAR → $DEST"
MANIFEST="$DEST/MANIFEST.txt"
{
  echo "年度归档清单"
  echo "生成时间：$(date -Iseconds)"
  echo "源数据库：$DB_PATH"
  echo "归档年度：$YEAR"
  echo ""
  printf '%-32s %10s\n' "表" "行数"
} > "$MANIFEST"

TOTAL=0
for t in "${TABLES[@]}"; do
  # 表不存在时跳过（结构随版本演进）。
  if ! sqlite3 "$DB_PATH" "SELECT name FROM sqlite_master WHERE type='table' AND name='$t';" | grep -q "^$t$"; then
    printf '%-32s %10s\n' "$t" "(跳过：表不存在)" >> "$MANIFEST"
    continue
  fi
  if ! sqlite3 "$DB_PATH" "PRAGMA table_info($t);" | grep -q '|created_at|'; then
    printf '%-32s %10s\n' "$t" "(跳过：无 created_at)" >> "$MANIFEST"
    continue
  fi

  WHERE="created_at >= '${YEAR}-01-01T00:00:00Z' AND created_at < '$((YEAR + 1))-01-01T00:00:00Z'"

  sqlite3 -header -csv "$DB_PATH" "SELECT * FROM $t WHERE $WHERE;" > "$DEST/$t.csv"
  # SQL 导出：保持可回灌（不导出 schema，仅数据）。
  {
    echo "-- $t · $YEAR 年度归档（仅数据）"
    echo "BEGIN;"
    sqlite3 "$DB_PATH" ".mode insert $t" "SELECT * FROM $t WHERE $WHERE;"
    echo "COMMIT;"
  } > "$DEST/$t.sql"

  n="$(sqlite3 "$DB_PATH" "SELECT COUNT(*) FROM $t WHERE $WHERE;")"
  TOTAL=$((TOTAL + n))
  printf '%-32s %10s\n' "$t" "$n" >> "$MANIFEST"
  echo "    $t: $n 行"
done

{
  echo ""
  echo "合计行数：$TOTAL"
  echo ""
  echo "说明：本归档为**只读导出**，未删除或改写任何在线数据（FR-M4-08）。"
  echo "      在线库瘦身属独立动作，须经明确批准后另行执行。"
} >> "$MANIFEST"

echo "==> 生成 sha256 清单"
( cd "$DEST" && sha256sum ./*.csv ./*.sql MANIFEST.txt > SHA256SUMS.txt )

echo "==> 打包 archives.tar.gz"
( cd "$DEST" && tar -czf archives.tar.gz ./*.csv ./*.sql MANIFEST.txt SHA256SUMS.txt )

echo "==> 归档完成：$DEST（合计 $TOTAL 行）"

if [ "$KEEP" -gt 0 ]; then
  echo "==> 清理旧年度目录（仅保留最近 $KEEP 个）"
  ls -1d "$ARCHIVE_DIR"/*/ 2>/dev/null | sort -r | tail -n +"$((KEEP + 1))" | while read -r old; do
    rm -rf "$old"
    echo "    已删除 $old"
  done
fi
