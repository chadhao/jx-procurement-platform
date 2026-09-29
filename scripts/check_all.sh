#!/usr/bin/env bash
# scripts/check_all.sh —— **一条命令跑完全部门禁**。
#
# ★ 动因：门禁已散落多处（gofmt/build/vet/test · md 表格 · md 结构 · 静默缺陷 · 净检出），
#   **散落的门禁＝没人会全跑＝等于不存在**。本脚本把「跑什么、每项耗时、总判定」收拢到一处。
#
# ★ 依 README 定案 #63「必绿基线门禁 vs 会报既存问题的检查 必须分列」：
#   · 【必绿】当前语料**必定通过**的检查 —— 任一失败 → **总判定失败（exit 1）**。
#   · 【会报】**会报出既存问题**的检查 —— **不阻塞总判定**，只列命中清单并标注「需人处置」。
#   → 若把「会报」并进「必绿」，会让基线**常年见红 → 红成为常态 → 被忽略**。
#
# ★ 依项目既有教训：**用 `set -o pipefail`**（否则「管道吃掉退出码 → 静默假绿」）。
#   ★ 刻意**不用 `set -e`**：本脚本要**跑完所有项**再给总判定，不能一项失败就中止。
#
# 用法：bash scripts/check_all.sh
# 退出码：0 = 全部「必绿」通过；1 = 至少一项「必绿」失败。
set -o pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

PY="${PY:-python}"
GREEN_FAIL=0
GREEN_TOTAL=0
declare -a SUMMARY

_ms() { date +%s%N; }
_elapsed() { echo $(( ($(_ms) - $1) / 1000000 )); }

# ---- 必绿：判据 = 退出码 0 ----
green() {
  local label="$1"; shift
  local t0 out rc ms
  t0=$(_ms); out=$("$@" 2>&1); rc=$?; ms=$(_elapsed "$t0")
  GREEN_TOTAL=$((GREEN_TOTAL + 1))
  if [ "$rc" -eq 0 ]; then
    echo "  ✓ [必绿] ${label}  (${ms} ms)"
    SUMMARY+=("✓ 必绿  ${label}  ${ms}ms")
  else
    echo "  ✗ [必绿] ${label}  (${ms} ms) —— **失败**"
    printf '%s\n' "$out" | tail -n 25 | sed 's/^/        | /'
    SUMMARY+=("✗ 必绿  ${label}  ${ms}ms  ← 失败")
    GREEN_FAIL=$((GREEN_FAIL + 1))
  fi
}

# ---- 必绿（空输出判据）：如 `gofmt -l .` 退出码恒为 0，须"输出为空"才算过 ----
green_empty() {
  local label="$1"; shift
  local t0 out rc ms
  t0=$(_ms); out=$("$@" 2>&1); rc=$?; ms=$(_elapsed "$t0")
  GREEN_TOTAL=$((GREEN_TOTAL + 1))
  if [ "$rc" -eq 0 ] && [ -z "$out" ]; then
    echo "  ✓ [必绿] ${label}  (${ms} ms)"
    SUMMARY+=("✓ 必绿  ${label}  ${ms}ms")
  else
    echo "  ✗ [必绿] ${label}  (${ms} ms) —— **失败**（要求输出为空）"
    printf '%s\n' "$out" | head -n 25 | sed 's/^/        | /'
    SUMMARY+=("✗ 必绿  ${label}  ${ms}ms  ← 失败")
    GREEN_FAIL=$((GREEN_FAIL + 1))
  fi
}

# ---- 会报（不阻塞）：无论退出码都列输出、标注需人处置 ----
report() {
  local label="$1"; shift
  local t0 out rc ms
  t0=$(_ms); out=$("$@" 2>&1); rc=$?; ms=$(_elapsed "$t0")
  if [ "$rc" -eq 0 ]; then
    echo "  ○ [会报] ${label}  (${ms} ms) —— 无命中"
    SUMMARY+=("○ 会报  ${label}  ${ms}ms  无命中")
  else
    echo "  ● [会报] ${label}  (${ms} ms) —— **有命中，需人工处置（不阻塞总判定）**"
    printf '%s\n' "$out" | sed 's/^/        | /'
    SUMMARY+=("● 会报  ${label}  ${ms}ms  ← 需人处置")
  fi
}

T_ALL=$(_ms)
echo "===== 全门禁 run-all  (repo: ${ROOT}) ====="
echo
echo "[必绿基线]"
# ★ gofmt 项**排除 `_` 前缀目录**（与 `go build ./...` / `go vet ./...` 口径对齐）：
#   Go 工具链约定「`_` 前缀目录 与 `_`/`.` 前缀文件不参与构建」，`go build`/`go vet` 据此忽略之；
#   而 `gofmt -l .` 是**纯文件遍历**、会走进去 ⇒ 该必绿项会去检查「工具链根本不构建的文件」。
#   这是**门禁自身的不一致**（本仓库已有先例：审计探针 fixture `scripts/_probe_c5/`，其 .go 不参与构建）。
#   → 判据与工具链对齐：只对**真正参与构建**的 .go 做格式门禁。
#   · 在 `find` 端排除（而非事后 `grep -v` 过滤输出）：后者在「无匹配」时 `grep` 退出码为 1 → `green_empty` **假红**。
#   · `xargs -r`：无文件时不调用 gofmt（否则 gofmt 读空 stdin 会**挂起**）。
green_empty "gofmt -l .（排除 _ 前缀，对齐 go build/vet）" \
  bash -c 'find . -type f -name "*.go" -not -path "*/_*" -not -path "*/.git/*" -not -path "*/node_modules/*" -print0 | xargs -0 -r gofmt -l'
green       "go build ./..."          go build ./...
green       "go vet ./..."            go vet ./...
green       "go test ./... -count=1"  go test ./... -count=1
green       "md 表格列数门禁"          "$PY" scripts/check_md_tables.py
# ★ 2026-09-29 新增：`COLLAB.md`（双 Agent 协商台账）结构门禁。
#   动因＝用户要求"两个 agent 的协商机制要接门禁机器校验"；★ 该门禁已用**探针法自证**：
#   故意造 4 类违规（重复/未递增 ID、非法状态、相对时间、缺字段）⇒ 逐条报出；
#   并借此逼出过自身的 1 处**假绿**（把「待议＝## 1.」编号写死，而文件里实际是 `## 4.`），
#   已改为按章节标题关键词判定（对重编号免疫）。见 `scripts/check_collab.py` 头注。
green       "COLLAB 协商台账门禁"        "$PY" scripts/check_collab.py
# ★ 2026-09-29 新增：`spec/`（机读规格）门禁。
#   动因＝`spec/` 是交给 mimo code 的**机器可读契约**；若语法错 / 引用悬空 / 编号对不上，
#   开发会拿到**静默错误的前提**。★ 首跑即抓到 WorkBuddy 自己写的 3 处中文引号误用（打断 JSON）；
#   另已用探针自证（删流程线 / ledger 指向禁落账的 L10 / route 悬空 / 档位重叠 ⇒ 逐条报出）。
green       "spec 机读规格门禁"          "$PY" scripts/check_spec.py
green       "净检出可构建门禁"          bash scripts/check_head_buildable.sh
echo
echo "[会报既存问题]（不阻塞）"
report      "静默缺陷排查 audit_silent" "$PY" scripts/audit_silent.py
report      "md 结构与一致性门禁"        "$PY" scripts/check_md_structure.py

echo
echo "===== 汇总（本次跑了 $((${#SUMMARY[@]})) 项，总耗时 $(_elapsed "$T_ALL") ms）====="
for l in "${SUMMARY[@]}"; do
  echo "  ${l}"
done
echo
if [ "$GREEN_FAIL" -ne 0 ]; then
  echo "===== 总判定：**失败**（必绿基线 ${GREEN_FAIL}/${GREEN_TOTAL} 项失败，exit 1）====="
  exit 1
fi
echo "===== 总判定：**通过**（必绿基线 ${GREEN_TOTAL}/${GREEN_TOTAL} 全绿；会报项如需处置见上）====="
exit 0
