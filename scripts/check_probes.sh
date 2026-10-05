#!/usr/bin/env bash
# check_probes.sh —— **常驻探针回归跑**（必绿级；N-060 T1–T4）
#
# ★★ 为什么存在（2026-10-05 联调前总检查）：12 个常驻探针 `scripts/_probe*.py`
#   曾全部 rc=0、红了没人发现，且**原地改写真源再还原 ⇒ 中途失败留污染**。
#
# ★★★ T1 · 隔离化执行（本脚本的核心改造）：
#   探针一律在 **git worktree 隔离副本**（仓库内 `.probe_worktree/`，detached HEAD）
#   里跑 —— 探针改写的是**副本的 spec/**，主工作区在任何失败/中断下零变化；
#   跑完（含中途异常）trap 清理 worktree。★ 判据：某探针中途异常退出 ⇒
#   主工作区 `git status --short` 零变化（`.probe_worktree/` 已进 .gitignore）。
#
# 判定口径（两条任一即算失败）：
#   ① 探针退出码非 0（T2：各探针末尾按自身结果 sys.exit(0/1)）；
#   ② 探针输出里出现 `✗`（全项目探针统一用 `✗` 标失败项 —— 兜底老探针）。
#
# 用法：bash scripts/check_probes.sh
# 退出码：0 = 全部探针通过；1 = 至少一个探针失败。
set -o pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
PY="${PY:-python}"

# ---- T1：隔离 worktree（仓库内路径 ⇒ 符合工作范围约束；gitignore 见 .gitignore）----
WT="$ROOT/.probe_worktree"
cleanup() {
  git worktree remove --force "$WT" >/dev/null 2>&1 || true
  rm -rf "$WT" >/dev/null 2>&1 || true
}
trap cleanup EXIT
cleanup # 清掉上次中断可能残留的 worktree
if ! git worktree add --detach "$WT" HEAD >/dev/null 2>&1; then
  echo "✗ 无法创建隔离 worktree（$WT）"
  exit 1
fi
# ★ 探针以**主工作区最新版**为准（开发期探针补丁未提交时也能在副本里测到）；
#   spec 等真源保持 HEAD 版（干净基线，探针篡改落在副本上）。
for f in "$ROOT"/scripts/_probe*.py; do
  [ -f "$f" ] && cp "$f" "$WT/scripts/"
done

pass=0
fail=0
declare -a BAD

for f in "$WT"/scripts/_probe*.py; do
  [ -f "$f" ] || continue
  name="$(basename "$f")"
  # ★ 探针在 worktree 内执行（cd 进副本）：其读写全部落在副本，主工作区不动
  out="$(cd "$WT" && "$PY" "$f" 2>&1)"
  rc=$?
  mark="$(printf '%s\n' "$out" | grep -m1 '✗' | cut -c1-110)"
  if [ "$rc" -ne 0 ] || [ -n "$mark" ]; then
    fail=$((fail + 1))
    BAD+=("$name")
    printf '  ✗ %-26s rc=%d  %s\n' "$name" "$rc" "${mark:-（无 ✗ 行，但退出码非 0）}"
  else
    pass=$((pass + 1))
    summ="$(printf '%s\n' "$out" | grep -E '通过|如期|鉴别力|已修复' | tail -1 | tr -d '\r' | cut -c1-64)"
    printf '  ✓ %-26s %s\n' "$name" "$summ"
  fi
done

echo
echo "合计：通过 ${pass} / 失败 ${fail} / 共 $((pass + fail)) 个常驻探针（隔离副本执行）"
if [ "$fail" -gt 0 ]; then
  echo "★ 失败项：${BAD[*]}"
  echo "★ 处置纪律：探针红 ≠ 产品问题 —— 先判「真缺陷」还是「探针期望未随规格演进」；"
  echo "  但**不许把红当成常态**：要么修规格、要么修探针期望（不许降级为会报级绕过）。"
  exit 1
fi
