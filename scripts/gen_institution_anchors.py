#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""gen_institution_anchors.py —— 制度条款引用**计数面**的机械重算 + 与索引比对。

★ 由来（`COLLAB.md#N-049` 第 ① 项）：`spec/institution-anchors.json`（`N-006` 第二重机制：
制度条款 ↔ 机读规格双向索引）自 V1.0 起**只由我方的一次性原型脚本生成**，生成器**在仓库外**
（原 `known_gaps.generator_outside_repo`）⇒ 留下一个窗口：**改了 spec、索引未重生 ⇒ 索引失真**。
★ `S20`（`path_exists`）只抓「**指针指向的落点不存在**」，**抓不住「有新的引用没被索引」** ——
两者是不同的洞，不可互相替代。

★★ 本脚本解决**后半边**（计数面）：把索引里的 `citation_count` / `citation_by_file`（**全量计数**，
与优先级无关、纯机械）**重算并与索引比对**，任何漂移即非零退出 ⇒ 可接入门禁。
★ 计数口径（**由 V1.0 产物反推并逐条款验证**，与索引自述一致）：
  「**字符串里出现「第X条」即计一处**」＝ **每个字符串内、每条款计 1 处**
  （★ 实测依据：`spec/forms/CT.json#known_gaps[3].note` 同一字符串里含两处「第三十七条」，
   而索引只计 1 ⇒ 排除「按出现次数计」）。
★ 扫描面：`spec/**/*.json`，**排除本索引自身**（其正文含「第X条」字样 ⇒ 不排除会自增污染，
  V1.0 已实测：324 → 353）。
★ 条款号识别：`第[零一二三四五六七八九十百]+条`（中文数字）。

★ 等价性证据（V1.2 落盘时实测，可复跑）：
  排除 `spec/openapi.json` 后，本脚本与索引 **28/28 条款 · `citation_count` · `citation_by_file` 全等**。
  ★ `spec/openapi.json` 系批 7（`B6`）**机械生成**物、晚于索引生成时点 ⇒ 当时的差异**正是**本机制
  要抓的「索引失真」实证（2 处：新增条款「第二条」1 处 ＋ 「第五十二条」1 处）。

★ 明确不做（残留，如实登记，不声称已覆盖）：
  `clauses[].spec[]` 的**指针级**收录仍依赖原型的**优先级选择规则**（条款引用 > 8 处时取前 8 的
  排序依据）⇒ 本脚本**不重建指针**，只重建计数。详见 `spec/institution-anchors.json#known_gaps`。

用法：
  python scripts/gen_institution_anchors.py --check    # 与索引比对；有漂移 ⇒ rc=1（门禁用）
  python scripts/gen_institution_anchors.py            # 打印扫描结果（JSON，供人读/复核）
"""
from __future__ import annotations

import argparse
import collections
import glob
import json
import os
import re
import sys

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
INDEX_REL = "spec/institution-anchors.json"
CLAUSE_RE = re.compile("第[零一二三四五六七八九十百]+条")


def _strings(node):
    """递归产出 JSON 中的**全部字符串**（dict 值 / list 元素 / str）。"""
    if isinstance(node, str):
        yield node
    elif isinstance(node, dict):
        for v in node.values():
            yield from _strings(v)
    elif isinstance(node, list):
        for v in node:
            yield from _strings(v)


def scan(repo: str = REPO, exclude=()):
    """机械重算「条款 → 引用计数」。

    返回 (counts, by_file)：
      · counts[clause] = 全量引用计数（**每字符串每条款计 1**）
      · by_file[clause][relpath] = 该文件内计数
    """
    counts = collections.Counter()
    by_file: dict[str, collections.Counter] = collections.defaultdict(collections.Counter)
    pattern = os.path.join(repo, "spec", "**", "*.json")
    for path in sorted(glob.glob(pattern, recursive=True)):
        rel = os.path.relpath(path, repo).replace(os.sep, "/")
        if rel == INDEX_REL or rel in exclude:
            continue
        with open(path, encoding="utf-8") as fh:
            doc = json.load(fh)
        for s in _strings(doc):
            for clause in sorted(set(CLAUSE_RE.findall(s))):
                counts[clause] += 1
                by_file[clause][rel] += 1
    return counts, by_file


def load_index(repo: str = REPO) -> dict:
    with open(os.path.join(repo, INDEX_REL), encoding="utf-8") as fh:
        return json.load(fh)


def diff(index: dict, counts, by_file) -> list[str]:
    """索引 vs 重算 —— 返回差异描述列表（空 ＝ 一致）。"""
    problems: list[str] = []
    indexed = {c["clause"]: c for c in index.get("clauses", [])}
    for clause, entry in indexed.items():
        got_n = counts.get(clause, 0)
        if got_n != entry.get("citation_count"):
            problems.append(
                "[E1] %s `citation_count` 索引=%d，重算=%d"
                % (clause, entry.get("citation_count"), got_n)
            )
        got_f = dict(sorted(by_file.get(clause, {}).items()))
        exp_f = dict(sorted((entry.get("citation_by_file") or {}).items()))
        if got_f != exp_f:
            problems.append(
                "[E2] %s `citation_by_file` 索引=%s，重算=%s" % (clause, exp_f, got_f)
            )
    for clause in sorted(set(counts) - set(indexed)):
        problems.append(
            "[E3] %s 在 spec 中有 %d 处引用，但**不在索引** clauses 内（有新引用未被索引）"
            % (clause, counts[clause])
        )
    return problems


def main(argv=None) -> int:
    ap = argparse.ArgumentParser(description="制度条款引用计数面重算 / 与索引比对")
    ap.add_argument("--check", action="store_true", help="与索引比对；有漂移 ⇒ rc=1")
    ap.add_argument("--repo", default=REPO)
    args = ap.parse_args(argv)

    counts, by_file = scan(args.repo)
    if args.check:
        index = load_index(args.repo)
        problems = diff(index, counts, by_file)
        for p in problems:
            print(p)
        if problems:
            print(
                "[%s] 索引与 spec 不一致：%d 处（★ 索引失真 ⇒ 须重算计数并回填）"
                % (INDEX_REL, len(problems))
            )
            return 1
        print(
            "[%s] 计数面一致（%d 条款 / 全量 %d 处引用）"
            % (INDEX_REL, len(counts), sum(counts.values()))
        )
        return 0

    out = {
        "clauses": {
            c: {"citation_count": counts[c], "citation_by_file": dict(sorted(by_file[c].items()))}
            for c in sorted(counts)
        },
        "total": sum(counts.values()),
        "distinct_clauses": len(counts),
    }
    json.dump(out, sys.stdout, ensure_ascii=False, indent=1, sort_keys=False)
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
