#!/usr/bin/env bash
# check_head_buildable.sh —— 门禁：**净检出（fresh checkout）必须可构建 + 可测试**
#
# ★ 为什么需要（本项目已付代价的真实事故）：
#   本地工作区"全绿"，但 **HEAD 是坏的** —— 因为某些被提交的文件**依赖了未提交的文件**。
#   实例（2026-09-27）：
#     · `internal/flow/finalize.go`（已提交 `83eddb9`）调用 `config.Maps.LedgerTypesFor`
#       —— 该方法只存在于**未提交**的 `internal/config/maps.go` → `build failed`。
#     · `internal/config` 的测试依赖 `t_config_mapping.approval_code` 的唯一约束
#       —— 该约束对应的迁移缺失/未提交 → `ON CONFLICT clause does not match any PRIMARY KEY or UNIQUE constraint`。
#   → 后果：任何人 clone 下来都**构建不过**；CI/评审/联调全部失真。
#
# ★ 与既有纪律的关系：
#   · 定案 #48「无位点 ＝ 不会被执行」
#   · 定案 #50「给了位点但位点不存在 ＝ 换一种形式的无位点」
#   · 本门禁对应的是第三种变体：**「本地绿 ≠ 入库绿」** ——
#     本地靠**未提交的补丁**才绿，入库的形态是坏的，而且**没有任何本地信号会报警**。
#
# 用法：
#   bash scripts/check_head_buildable.sh [git-ref]     # 默认 HEAD
# 退出码：0 = 净检出可构建且可测试；非 0 = 不可（**并打印完整失败输出**）
set -o pipefail

REF="${1:-HEAD}"

REPO="$(git rev-parse --show-toplevel 2>/dev/null)"
if [ -z "$REPO" ]; then
  echo "✗ 不在 git 仓库内" >&2
  exit 2
fi

WT="$(dirname "$REPO")/_headcheck_$$"
cleanup() {
  git -C "$REPO" worktree remove --force "$WT" >/dev/null 2>&1 || true
  rm -rf "$WT" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

echo "===== 净检出构建/测试门禁 ====="
echo "仓库: $REPO"
echo "引用: $REF  →  $(git -C "$REPO" log --oneline -1 "$REF" 2>/dev/null)"

if ! git -C "$REPO" worktree add --detach "$WT" "$REF" >/dev/null 2>&1; then
  echo "✗ 无法创建临时净检出（worktree add 失败）" >&2
  exit 2
fi
echo "净检出: $WT"
echo

fail=0

echo "----- go build ./... -----"
if ! (cd "$WT" && go build ./... 2>&1); then
  echo "✗ 净检出**构建失败**：入库的代码依赖了未提交（或不存在的）东西。" >&2
  fail=1
else
  echo "✓ 构建通过"
fi
echo

if [ "$fail" -eq 0 ]; then
  echo "----- go test ./... -count=1 -----"
  TEST_OUT="$(cd "$WT" && go test ./... -count=1 2>&1)"
  TEST_RC=$?
  echo "$TEST_OUT" | grep -E '^(ok|FAIL|---|\?)' || true
  if [ "$TEST_RC" -ne 0 ]; then
    echo "✗ 净检出**测试失败**" >&2
    echo "$TEST_OUT" | grep -vE '^ok |no test files' | head -40 >&2
    fail=1
  else
    echo "✓ 测试通过"
  fi
  echo
fi

if [ "$fail" -ne 0 ]; then
  echo "===== 结论：净检出不达标（exit 1）====="
  echo "★ 处置：不要用「本地 go build/test 通过」当结论 —— 它可能靠的是**未提交的补丁**。"
  echo "★        把缺失的依赖文件**一并提交**（或在同一提交里包含），然后重跑本门禁。"
  exit 1
fi

echo "===== 结论：净检出可构建且可测试（exit 0）====="
exit 0
