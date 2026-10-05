#!/usr/bin/env bash
# check_probes.sh —— **常驻探针回归跑**（会报级）
#
# ★★ 为什么需要它（2026-10-05 联调前总检查发现）：
#   仓库有 12 个常驻探针 `scripts/_probe*.py`（N-026/N-031/N-048/N-049/N-051/N-053/
#   N-057/N-058/N-059/S5cS6b/S5d/S6cS13，以及批 3 的 `_probe_batch3.py`）。
#   它们**带 `_` 前缀 ⇒ 不在任何门禁面内**，而 ★ **它们全部以 `rc=0` 退出**
#   （即便内部有 `✗` 失败也不改退出码）⇒ ★★ **探针变红没有任何机制能发现**。
#   实测后果：总检查当日发现 **3 个探针已长期报红**
#     · `_probe_n048` 9/10 —— 硬编码锚点条数 158，而索引已扩到 304+
#     · `_probe_n049` 35/38 —— 拿「历史索引」比「**当前树**重算」，规格一演进必红
#     · `_probe_n057` 19/20 —— 期望键集未随 S19 新增 `target_filter` 同步
#   ⇒ ★ **红着的回归钉 ≈ 没有回归钉**。本脚本把这条**可见性**补上。
#
# 判定口径（两条任一即算失败）：
#   ① 探针退出码非 0；② 探针输出里出现 `✗`（本项目全部探针统一用 `✗` 标失败项）。
#
# ★★★ 使用限制（2026-10-05 实测，必须遵守）：
#   **这些探针在原地改写真源（`spec/*.json`）再还原**，且**还原写在脚本末尾** ⇒
#   ★ 一旦探针中途失败/被打断，**污染会留在工作区**（实测已发生：`params.json` 的
#   报销截止日被改成 31、`institution-anchors.json` 被删一条锚点，均未还原）。
#   ⇒ 因此现阶段**只可"单次手工跑"，且跑前必须确认没有别的任务在动仓库**；
#   ★ **暂不接入 `check_all.sh`**（门禁会被反复/并发触发 ⇒ 会毁工作区）。
#   ⇒ 正解是**把探针放进隔离副本里跑**（`git worktree` / 整树复制），待改造后再入闸。
#   ★ 跑完**必查**：`git status --short` 与 `git diff --stat spec/`，确认零残留。
#
# 用法：bash scripts/check_probes.sh
# 退出码：0 = 全部探针通过；1 = 至少一个探针失败。
set -o pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
PY="${PY:-python}"

pass=0
fail=0
declare -a BAD

for f in scripts/_probe*.py; do
  [ -f "$f" ] || continue
  name="$(basename "$f")"
  out="$("$PY" "$f" 2>&1)"
  rc=$?
  mark="$(printf '%s\n' "$out" | grep -m1 '✗' | cut -c1-110)"
  if [ "$rc" -ne 0 ] || [ -n "$mark" ]; then
    fail=$((fail + 1))
    BAD+=("$name")
    printf '  ✗ %-26s rc=%d  %s\n' "$name" "$rc" "${mark:-（无 ✗ 行，但退出码非 0）}"
  else
    pass=$((pass + 1))
    # 摘一条该探针自己的汇总行，便于人扫一眼
    summ="$(printf '%s\n' "$out" | grep -E '通过|如期|鉴别力|已修复' | tail -1 | tr -d '\r' | cut -c1-64)"
    printf '  ✓ %-26s %s\n' "$name" "$summ"
  fi
done

echo
echo "合计：通过 ${pass} / 失败 ${fail} / 共 $((pass + fail)) 个常驻探针"
if [ "$fail" -gt 0 ]; then
  echo "★ 失败项：${BAD[*]}"
  echo "★ 处置纪律：探针红**不等于**产品有问题 —— 先判「真缺陷」还是「探针期望未随规格演进同步」"
  echo "  （本项目已两次踩到后者），★ 但**不许把红当成常态**：要么修规格、要么修探针期望。"
  exit 1
fi
