# -*- coding: utf-8 -*-
"""`N-048` 正式探针 —— 第 11 原语 `path_exists` 的**行为证明**（不是"跑一下没报错"）。

用法：`python scripts/_probe_n048.py`（★ 本文件不进任何门禁面：`_` 前缀不对齐 `check_spec.py`/`check_all.sh` 的入口；
        容器文件临时落在**仓库根**、`spec/**` 之外 ⇒ 不污染 `spec` 机读规格门禁）。

它证明三件事（★ 每条都是**可复现的实测**，不是推断）：
  1. **正向**：真 `spec/institution-anchors.json` 的 158 条指针**全量 0 报错**；
  2. **反向**：三类误写各**精确 1 条**，且**报错分类互不串**（"文件不存在" ≠ "命中 0"）；
  3. ★★ **两侧契约同序**：`[*]` 形态（`checks[*].id`）**必须命中** ——
     这一条正是 `N-048` 验收期实测逼出的 **Python/Go 归一顺序分歧**的**回归钉**
     （旧 Python 先 `replace("[*]","*")` 再拆尾缀 ⇒ `checks[*]` 折成 `checks*` ⇒ 命中 0 ⇒ 本用例红）。
"""
import json
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
import check_spec as C  # noqa: E402

TMP = os.path.join(C.ROOT, "_probe_n048_tmp.json")
RESULT = []


def _call(pointers):
    """把一批指针写进临时容器（仓库根、spec/ 之外），再调真原语。"""
    with open(TMP, "w", encoding="utf-8", newline="\n") as fh:
        json.dump({"p": pointers}, fh, ensure_ascii=False, indent=2)
    C._cache.clear()
    return C.prim_path_exists({"__cid__": "PROBE-N048",
                               "file": "_probe_n048_tmp.json",
                               "collect": "p[*]", "min_hits": 1})


def case(name, ptrs, want_n, must_have=None, must_not_have=None):
    probs = _call(ptrs)
    ok = len(probs) == want_n
    for s in (must_have or []):
        ok = ok and any(s in p for p in probs)
    for s in (must_not_have or []):
        ok = ok and not any(s in p for p in probs)
    RESULT.append((ok, name, len(probs), want_n, probs))
    print("%s %-28s 实得 %d 条（期望 %d）" % ("✓" if ok else "✗", name, len(probs), want_n))
    for p in probs:
        print("      · " + p)


def main():
    print("═══ N-048 探针 · path_exists（第 11 原语）═══\n")

    # ── 正向 1：真锚点全量（直接对真文件，不经临时容器）──
    C._cache.clear()
    refs = C.sel(C.load_json(C.abs_path("spec/institution-anchors.json")),
                 C.toks("clauses[*].spec[*].at"))
    probs = C.prim_path_exists({"__cid__": "PROBE-N048",
                                "file": "spec/institution-anchors.json",
                                "collect": "clauses[*].spec[*].at", "min_hits": 1})
    ok = (len(refs) == 158 and len(probs) == 0)
    RESULT.append((ok, "正向·真锚点全量", len(probs), 0, probs))
    print("%s %-28s 收集 %d 条指针 → %d 条报错（期望 158 / 0）"
          % ("✓" if ok else "✗", "正向·真锚点全量", len(refs), len(probs)))
    for p in probs[:5]:
        print("      · " + p)

    # ── 正向 2：★ [*] 形态（两侧归一顺序的回归钉）──
    case("正向·[*] 段（回归钉）", ["spec/checks.json#checks[*].id"], 0)

    # ── 正向 3/4：含点字面键 ＋ 选择器 ＋ 多选择器 ＋ 无 # ──
    case("正向·[字面键]含点", ["spec/params.json#params.[reporting.monthly_cutoff_day].critical_note"], 0)
    case("正向·选择器", ["spec/forms/GR.json#checks[id=no_approver_in_acceptance_group].origin_ref"], 0)
    case("正向·双选择器", ["spec/forms/BJ.json#sections[id=header].fields[name=applicant].rule"], 0)
    case("正向·省略 #", ["spec/checks.json"], 0)

    # ── 反向 A/B/C：三类误写 ──
    case("反向A·选择器值写错", ["spec/checks.json#sections[id=nope]"], 1,
         must_have=["命中 0 个节点"])
    case("反向B·文件不存在", ["spec/forms/NO_SUCH_FILE.json#a.b"], 1,
         must_have=["文件不存在"], must_not_have=["命中 0 个节点"])
    case("反向C·含点键写裸键", ["spec/params.json#params.reporting.monthly_cutoff_day.critical_note"], 1,
         must_have=["命中 0 个节点"])

    # ── 反向 D：min_hits 守卫（空清单 ≠ 通过）──
    case("反向D·min_hits 守卫", [], 1, must_have=["疑似清单声明有误"])

    os.remove(TMP)
    bad = [r for r in RESULT if not r[0]]
    print("\n═══ 汇总：%d/%d 用例通过 ═══" % (len(RESULT) - len(bad), len(RESULT)))
    for _, name, got, want, _ in bad:
        print("  ✗ %s（实得 %d，期望 %d）" % (name, got, want))
    return 1 if bad else 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    finally:
        if os.path.exists(TMP):
            os.remove(TMP)
