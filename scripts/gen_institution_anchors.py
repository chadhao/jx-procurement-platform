#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""gen_institution_anchors.py —— 制度条款引用的**指针面 ＋ 计数面**机械重算 / 与索引比对 / 回填。

★ 由来（`COLLAB.md#N-049` 第 ① 项）：`spec/institution-anchors.json`（`N-006` 第二重机制：
制度条款 ↔ 机读规格双向索引）自 V1.0 起**只由我方的一次性原型脚本生成**，生成器**在仓库外**
（原 `known_gaps.generator_outside_repo`）⇒ 留下一个窗口：**改了 spec、索引未重生 ⇒ 索引失真**。
★ `S20`（`path_exists`）只抓「**指针指向的落点不存在**」，**抓不住「有新的引用没被索引」** ——
两者是不同的洞，不可互相替代。

★★ 本脚本现已覆盖**两面**：

  【计数面】（`N-049` ① 第一段，批 19 落地）
    `citation_count` / `citation_by_file` 是**全量计数**（与指针稳定性无关、纯机械）⇒ 可重算。
    口径：「**字符串里出现「第X条」即计一处**」＝ **每个字符串内、每条款计 1 处**
    （★ 实测依据：`spec/forms/CT.json#known_gaps[3].note` 同一字符串里含两处「第三十七条」，
     而索引只计 1 ⇒ 排除「按出现次数计」）。

  【指针面】（`N-049` ① 第二段，批 22 落地 —— 本版新增）
    把每条引用的**位置**按索引约定序列化，得到 `file#点路径`，并**逐条款全量**收录进 `clauses[].spec[]`。
    ★★ 规则（**全部由 V1.0–V1.2 产物反推并经逐条款实测**，非臆造）：
      (a) **路径段序列化**：对象键 → `.k`（★ 键名含 `.` 时用 `.[k]`，实证：`spec/params.json` 的
          5 个含点扁平键）；数组元素 → `[k=v]`（取该数组内**所有元素共有、且取值互异**的标量键，
          优先级 `id > name > key > code > version > clause > label`）。
      (b) **不可稳定化**：数组元素若**没有**这样的稳定键 ⇒ 该引用**只计数、不建指针**
          （★ 现行 22 处，全部落在 `known_gaps` / `open_items` / `payment_route_rule.decisions`
           这类**元素无稳定键**的数组里）⇒ 记为 `unstable_count`。
      (c) **`kind` 分类**（★ **实测 158/158 逐条吻合**）：路径含 `checks[` ⇒ `checks`；
          否则末段 ∈ {`origin_ref`, `critical_note`, `rule`} ⇒ `rule`；末段 == `note` ⇒ `note`；
          其余 ⇒ `prose`。
      (d) **排序**：`(kind_rank, at)`，`kind_rank` = `checks < rule < note < prose`
          （★ 实测：V1.0–V1.2 的每一条 `spec[]` 都恰是这个序 —— 故本版排序**沿用**，不是新发明）。
      (e) ★★ **`[:8]` 上限已退役**：V1.0–V1.2 的 `spec[]` 在此之上还套了一层「取前 8」的上限
          —— ★ **本版已实测复现该上限**（**28/29 条款逐项吻合**；第 29 条 ② 是索引已登记的
          「有计数、无指针」的 `第二条`）。本版**取消上限**，`spec[]` 收录**全部**稳定引用
          ⇒ ★ 不变式 **`len(spec[]) + unstable_count == citation_count`** 逐条款成立。

★ 扫描面：`spec/**/*.json`，**排除本索引自身**（其正文含「第X条」字样 ⇒ 不排除会自增污染，
  V1.0 已实测：324 → 353）。
★ 条款号识别：`第[零一二三四五六七八九十百]+条`（中文数字）。

★ 等价性证据（可复跑，见 `scripts/_probe_n049.py`）：
  排除 `spec/openapi.json`（批 7 `B6` **机械生成**物、晚于索引生成时点）后，本脚本与索引 V1.1
  **28/28 条款 · `citation_count` · `citation_by_file` 全等** ⇒ 口径**不是猜的**。

★ 明确不做（残留，如实登记，不声称已覆盖）：
  ① `quoted_phrases` 仍是**人工**收录（本脚本不生成、不覆盖）；
  ② `clauses[]` 的**增删**不由本脚本自动执行 —— 索引缺条款 / 有 spec 未引用的条款，
     门禁分别报 `E3` / `E6`，由人决定（★ **不臆造结构**）。

用法：
  python scripts/gen_institution_anchors.py --check     # 与索引比对（计数面 ＋ 指针面）；有漂移 ⇒ rc=1
  python scripts/gen_institution_anchors.py --rewrite   # 回填 clauses[].spec / citation_count /
                                                        #   citation_by_file / unstable_count（其余部分原样保留）
  python scripts/gen_institution_anchors.py             # 打印扫描结果（JSON，供人读/复核）
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

#: `kind` 的受控取值与其排序权重（★ 索引既有形态即此序，见头注 (d)）。
KIND_RANK = {"checks": 0, "rule": 1, "note": 2, "prose": 3}
#: 数组元素可用作 `[k=v]` 稳定选择器的候选键（★ 按此优先级取第一个「全元素共有且互异」者）。
SEL_KEYS = ("id", "name", "key", "code", "version", "clause", "label")


# --------------------------------------------------------------------------- 路径序列化

def _seg_str(segs) -> str:
    """把（内部表示的）段序列拼成索引约定的点路径。"""
    out = ""
    for kind, a, b in segs:
        if kind == "key":                 # 普通对象键
            out += ("." + a) if out else a
        elif kind == "litkey":            # 键名含 `.` ⇒ `.[k]`
            out += ("." if out else "") + "[%s]" % a
        elif kind == "sel":               # 数组元素（稳定选择器）`[k=v]`
            out += "[%s=%s]" % (a, b)
        elif kind == "idx":               # 数组元素（无稳定键）`[i]` —— 只计数、不建指针
            out += "[%d]" % a
    return out


def _sel_key(arr) -> str | None:
    """数组若有「全元素共有、取值互异」的标量候选键 ⇒ 返回该键名（可作 `[k=v]`）；否则 None。"""
    if not arr:
        return None
    dicts = [e for e in arr if isinstance(e, dict)]
    if len(dicts) != len(arr):
        return None
    for k in SEL_KEYS:
        if all(k in e and isinstance(e[k], (str, int, float, bool)) for e in dicts):
            if len({e[k] for e in dicts}) == len(dicts):
                return k
    return None


def _walk(node, segs, out):
    """递归产出（路径, 字符串, 是否不可稳定化）。★ 只产**值**（不含键名），与计数口径一致。"""
    if isinstance(node, str):
        out.append((_seg_str(segs), node, any(k == "idx" for k, _, _ in segs)))
        return
    if isinstance(node, dict):
        for k, v in node.items():
            _walk(v, segs + ([("litkey", k, "")] if "." in k else [("key", k, "")]), out)
        return
    if isinstance(node, list):
        key = _sel_key(node)
        for i, v in enumerate(node):
            if key is not None:
                _walk(v, segs + [("sel", key, v[key])], out)
            else:
                _walk(v, segs + [("idx", i, "")], out)


def kind_of(fragment: str) -> str:
    """按头注 (c) 由点路径判定 `kind`。"""
    leaf = fragment.split(".")[-1]
    if "checks[" in fragment:
        return "checks"
    if leaf in ("origin_ref", "critical_note", "rule"):
        return "rule"
    if leaf == "note":
        return "note"
    return "prose"


# --------------------------------------------------------------------------- 扫描

def _iter_spec_files(repo: str, exclude=()):
    for path in sorted(glob.glob(os.path.join(repo, "spec", "**", "*.json"), recursive=True)):
        rel = os.path.relpath(path, repo).replace(os.sep, "/")
        if rel == INDEX_REL or rel in exclude:
            continue
        yield rel, path


def scan_citations(repo: str = REPO, exclude=()):
    """逐条款产出**全部引用及其位置**。

    返回 dict：clause -> [ {file, at, kind, unstable}, ... ]（顺序＝文件遍历序，未排序）。
    ★ `at` 为**点路径**（不含文件前缀）；文件前缀在 `expected()` 里拼成 `<file>#<path>`。
    """
    cites: dict[str, list] = collections.defaultdict(list)
    for rel, path in _iter_spec_files(repo, exclude):
        with open(path, encoding="utf-8") as fh:
            doc = json.load(fh)
        out: list = []
        _walk(doc, [], out)
        for at, s, unstable in out:
            for clause in sorted(set(CLAUSE_RE.findall(s))):
                cites[clause].append({
                    "file": rel, "at": at,
                    "kind": kind_of(at), "unstable": unstable,
                })
    return cites


def stable_of(citations) -> list:
    """取**稳定**引用并按 `(kind_rank, at)` 排序（★ 与索引既有形态一致）。"""
    rows = [
        {"at": "%s#%s" % (c["file"], c["at"]), "kind": c["kind"]}
        for c in citations if not c["unstable"]
    ]
    rows.sort(key=lambda r: (KIND_RANK[r["kind"]], r["at"]))
    return rows


def expected(repo: str = REPO, exclude=()):
    """索引 `clauses[]` 的**期望值**：clause -> {spec, citation_count, citation_by_file, unstable_count}。"""
    out = {}
    for clause, citations in scan_citations(repo, exclude).items():
        by_file = collections.Counter(c["file"] for c in citations)
        out[clause] = {
            "spec": stable_of(citations),
            "citation_count": len(citations),
            "citation_by_file": dict(sorted(by_file.items())),
            "unstable_count": sum(1 for c in citations if c["unstable"]),
        }
    return out


def scan(repo: str = REPO, exclude=()):
    """机械重算「条款 → 引用计数」（★ 兼容入口；口径由 `scan_citations` 派生，保证单一来源）。

    返回 (counts, by_file)：
      · counts[clause] = 全量引用计数（**每字符串每条款计 1**）
      · by_file[clause][relpath] = 该文件内计数
    """
    counts: collections.Counter = collections.Counter()
    by_file: dict[str, collections.Counter] = collections.defaultdict(collections.Counter)
    for clause, citations in scan_citations(repo, exclude).items():
        for c in citations:
            counts[clause] += 1
            by_file[clause][c["file"]] += 1
    return counts, by_file


def load_index(repo: str = REPO) -> dict:
    with open(os.path.join(repo, INDEX_REL), encoding="utf-8") as fh:
        return json.load(fh)


# --------------------------------------------------------------------------- 比对

def _first_diff(a, b):
    """返回两列表的首个差异位置与两侧取值（供报错定位）。"""
    n = min(len(a), len(b))
    for i in range(n):
        if a[i] != b[i]:
            return i, a[i], b[i]
    if len(a) != len(b):
        return n, (a[n] if len(a) > n else None), (b[n] if len(b) > n else None)
    return None, None, None


def _fmt(row) -> str:
    return "—" if row is None else "%s(%s)" % (row.get("at"), row.get("kind"))


def diff(index: dict, exp) -> list:
    """索引 vs 期望 —— 返回差异描述列表（空 ＝ 一致）。★ `exp` 由 `expected()` 产出。"""
    problems: list[str] = []
    indexed = {c["clause"]: c for c in index.get("clauses", [])}
    for clause, e in sorted(exp.items()):
        got = indexed.get(clause)
        if got is None:
            problems.append(
                "[E3] %s 在 spec 中有 %d 处引用，但**不在索引** clauses 内（有新引用未被索引）"
                % (clause, e["citation_count"])
            )
            continue
        if got.get("citation_count") != e["citation_count"]:
            problems.append(
                "[E1] %s `citation_count` 索引=%s，重算=%s"
                % (clause, got.get("citation_count"), e["citation_count"])
            )
        got_f = dict(sorted((got.get("citation_by_file") or {}).items()))
        if got_f != e["citation_by_file"]:
            problems.append(
                "[E2] %s `citation_by_file` 索引=%s，重算=%s"
                % (clause, got_f, e["citation_by_file"])
            )
        got_spec = [{"at": s.get("at"), "kind": s.get("kind")} for s in (got.get("spec") or [])]
        if got_spec != e["spec"]:
            i, ga, eb = _first_diff(got_spec, e["spec"])
            problems.append(
                "[E4] %s `spec[]` 指针列表失真：索引 %d 条 / 重算 %d 条 ⇒ 首个差异 @%s：索引 %s ｜ 重算 %s"
                % (clause, len(got_spec), len(e["spec"]), i, _fmt(ga), _fmt(eb))
            )
        if got.get("unstable_count", 0) != e["unstable_count"]:
            problems.append(
                "[E5] %s `unstable_count` 索引=%s，重算=%s（只计数、不建指针的那部分）"
                % (clause, got.get("unstable_count", 0), e["unstable_count"])
            )
    for clause in sorted(set(indexed) - set(exp)):
        problems.append(
            "[E6] %s 在**索引** clauses 内，但 spec 中已无任何引用 ⇒ 陈旧条目（须删除或更正）" % clause
        )
    return problems


def audit(repo: str = REPO) -> list:
    """一站式：重算 + 载入索引 + 比对。★ `check_spec.py` 的唯一入口（口径只在本文件）。

    ★ 每条问题**自带 `[META]` 前缀与主语**（`[META] 制度锚点索引失真：[Ex] …`）——
      使门禁输出**自描述**（谁在报、报的是哪一类），不必回读源码猜。
    """
    return ["[META] 制度锚点索引失真：%s" % p for p in diff(load_index(repo), expected(repo))]


# --------------------------------------------------------------------------- 回填

def rewrite(repo: str = REPO) -> dict:
    """把 `clauses[]` 的 `spec` / `citation_count` / `citation_by_file` / `unstable_count` 回填。

    ★ 其余部分（`version` / `institution_doc` / `conventions` / `known_gaps` / `change_log` /
      `quoted_phrases` / `topic`）**原样保留** —— 本脚本**不生成散文**。
    ★ 索引里**缺**的条款（spec 有引用、索引无条目）**不自动新增** ⇒ 返回在 `missing` 里如实报出。
    """
    exp = expected(repo)
    index = load_index(repo)
    changed, unchanged, missing = [], [], []
    for c in index.get("clauses", []):
        clause = c.get("clause")
        e = exp.get(clause)
        if e is None:
            missing.append(clause)
            continue
        before = (c.get("spec"), c.get("citation_count"), c.get("citation_by_file"),
                  c.get("unstable_count"))
        c["spec"] = e["spec"]
        c["citation_count"] = e["citation_count"]
        c["citation_by_file"] = e["citation_by_file"]
        c["unstable_count"] = e["unstable_count"]
        after = (c["spec"], c["citation_count"], c["citation_by_file"], c["unstable_count"])
        (changed if before != after else unchanged).append(clause)
    # 未在索引内的条款：如实报出，不臆造 `quoted_phrases`
    for clause in sorted(set(exp) - {c.get("clause") for c in index.get("clauses", [])}):
        missing.append(clause)
    with open(os.path.join(repo, INDEX_REL), "w", encoding="utf-8", newline="\n") as fh:
        fh.write(json.dumps(index, ensure_ascii=False, indent=2) + "\n")
    return {"changed": changed, "unchanged": unchanged, "missing": missing,
            "n_clauses": len(index.get("clauses", [])),
            "n_spec": sum(len(c.get("spec") or []) for c in index.get("clauses", [])),
            "n_unstable": sum(c.get("unstable_count", 0) for c in index.get("clauses", []))}


# --------------------------------------------------------------------------- CLI

def main(argv=None) -> int:
    ap = argparse.ArgumentParser(description="制度条款引用的指针面/计数面重算 · 与索引比对 · 回填")
    ap.add_argument("--check", action="store_true", help="与索引比对；有漂移 ⇒ rc=1")
    ap.add_argument("--rewrite", action="store_true", help="回填 clauses[] 的指针/计数四项")
    ap.add_argument("--repo", default=REPO)
    args = ap.parse_args(argv)

    if args.rewrite:
        res = rewrite(args.repo)
        print("[%s] 回填完成：条款 %d · spec[] %d 条 · unstable %d 处"
              % (INDEX_REL, res["n_clauses"], res["n_spec"], res["n_unstable"]))
        print("  改变 %d 条 · 未变 %d 条" % (len(res["changed"]), len(res["unchanged"])))
        if res["missing"]:
            print("  ★ 索引缺条目（**未自动新增**，须人工决定 quoted_phrases）：%s"
                  % ", ".join(res["missing"]))
        return 0

    if args.check:
        problems = audit(args.repo)
        for p in problems:
            print(p)
        if problems:
            print("[%s] 索引与 spec 不一致：%d 处（★ 索引失真 ⇒ 复现：`--rewrite` 回填）"
                  % (INDEX_REL, len(problems)))
            return 1
        exp = expected(args.repo)
        print("[%s] 指针面 ＋ 计数面一致（%d 条款 / 全量 %d 处引用 / 指针 %d 条 / 只计数 %d 处）"
              % (INDEX_REL, len(exp), sum(e["citation_count"] for e in exp.values()),
                 sum(len(e["spec"]) for e in exp.values()),
                 sum(e["unstable_count"] for e in exp.values())))
        return 0

    exp = expected(args.repo)
    out = {
        "clauses": {
            c: {"citation_count": e["citation_count"],
                "citation_by_file": e["citation_by_file"],
                "unstable_count": e["unstable_count"],
                "spec": e["spec"]}
            for c, e in sorted(exp.items())
        },
        "total": sum(e["citation_count"] for e in exp.values()),
        "distinct_clauses": len(exp),
    }
    json.dump(out, sys.stdout, ensure_ascii=False, indent=1, sort_keys=False)
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
