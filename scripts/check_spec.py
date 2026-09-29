#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
scripts/check_spec.py —— `spec/` 机读规格门禁（**清单执行器**）。

★★ 本脚本**不自持判据** —— 判据全部来自 `spec/checks.json`（唯一来源）。
   它只实现 `checks.json#primitives` 里的 **8 个原语引擎**，然后逐条执行 `checks`。

★ 为什么这么改（议题 `N-011` 的落地）：
 原实现把判据**硬编码在 Python 里**，Go 侧要用就得**按中文描述再实现一遍** ⇒
 只是「**第三份真相**」，必然漂（与 `docs/11 §5.1` vs `§5.2` 同源）。
 改为「判据＝数据 ＋ 少量原语」后：**清单是唯一真相**，两侧各实现一次小原语引擎即可。

★ 自证记录（本项目纪律：门禁有效性只能用**探针法**证明）：
 · 首跑即抓到 WorkBuddy 自己写的 3 处**中文引号误用**（打断 JSON）；
 · 探针：删流程线 / `ledger` 指向 `L10` / `route` 悬空 / 档位重叠 ⇒ 逐条报出；
 · 新增 S10/S11/S12 三条**跨文件**校验后，探针：`PR` 的 ledger 去掉 `L03`
   ⇒ **S12 拦下** —— 这正是 `R-02`「L03 恒空」静默缺陷的执行守卫。

用法：python scripts/check_spec.py [checks.json 路径]
退出码：0 = 全部 must-green 通过；1 = 有违规；2 = 清单/目录不可用
"""

import glob
import json
import os
import re
import sys

NAME = "check_spec.py"
ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

_cache = {}


# ---------------------------------------------------------------- 基础工具
def abs_path(p):
    return p if os.path.isabs(p) else os.path.join(ROOT, p)


def expand(pattern):
    return sorted(glob.glob(abs_path(pattern), recursive=True))


def load_json(path):
    if path not in _cache:
        with open(path, "r", encoding="utf-8") as fh:
            _cache[path] = json.load(fh)
    return _cache[path]


def rel(path):
    return os.path.relpath(path, ROOT).replace("\\", "/")


def toks(path):
    """点路径 → 段列表。`a[*]` 与 `a.*` 等价（均取全部子节点）；`**` 为递归下降。"""
    if not path:
        return []
    p = path.replace("[*]", ".*")
    return [x for x in p.split(".") if x != ""]


def _children(n):
    if isinstance(n, dict):
        return list(n.values())
    if isinstance(n, list):
        return list(n)
    return []


def sel(node, segs):
    """点路径取值，返回**值列表**。"""
    if not segs:
        return [node]
    head, rest = segs[0], segs[1:]
    out = []
    if head == "**":
        out += sel(node, rest)              # 在本层尝试匹配 rest
        for c in _children(node):           # 继续向下，仍带 **
            out += sel(c, segs)
        return out
    if head == "*":
        for c in _children(node):
            out += sel(c, rest)
        return out
    if isinstance(node, dict) and head in node:
        out += sel(node[head], rest)
    return out


def _all_strings(n):
    if isinstance(n, str):
        yield n
    elif isinstance(n, dict):
        for v in n.values():
            yield from _all_strings(v)
    elif isinstance(n, list):
        for v in n:
            yield from _all_strings(v)


def _dict_keys(n):
    if not isinstance(n, dict):
        return None
    return set(k for k in n.keys() if not k.startswith("_"))


# ---------------------------------------------------------------- 8 个原语引擎
#
# ★★ 2026-09-29 加固（**探针 C 逼出的第 4 例"门禁自身假绿"**）：
#   原实现里 `range_contiguous` 遇到"选中值不是 list"就 `continue` 跳过 ——
#   于是**清单里 collect 写错**（如把 `thresholds.purchase.bands` 误写成 `bands[*]`，
#   后者会被展开成 3 个 dict 而非 1 个 list）⇒ **判据一条都没跑，却报 OK**。
#   ⇒ 现为**所有 collect 型原语**加 `min_hits`（默认 1）：**选中数不足即报错**。
#   ★ 这条加固的意义与「S9 递归扫描」「B1 三则」同源：**不许把"声明写错"静默成"通过"**。
def _hits_guard(kind, collect, values, args):
    mn = args.get("min_hits", 1)
    if len(values) < mn:
        return ["[%s] `%s` 仅命中 %d 处（要求 ≥%d）—— **疑似清单声明有误**，不是「通过」"
                % (kind, collect, len(values), mn)]
    return []


def prim_json_parse(args):
    probs, files = [], expand(args["file"])
    if not files:
        probs.append("[S1] glob `%s` 未匹配到任何文件（规格为空？）" % args["file"])
    for f in files:
        try:
            load_json(f)
        except json.JSONDecodeError as e:
            probs.append("[S1] %s JSON 语法错误：%s" % (rel(f), e))
        except Exception as e:
            probs.append("[S1] %s 读取失败：%s" % (rel(f), e))
    return probs


def prim_required_keys(args):
    probs = []
    for f in expand(args["file"]):
        nodes = sel(load_json(f), toks(args.get("scope", "")))
        if not nodes:
            probs.append("[S2] %s scope `%s` 未命中节点" % (rel(f), args.get("scope", "")))
        for nd in nodes:
            ks = _dict_keys(nd)
            if ks is None:
                probs.append("[S2] %s scope `%s` 不是对象" % (rel(f), args.get("scope", "")))
                continue
            for k in args["keys"]:
                if k not in ks:
                    probs.append("[S2] %s 缺顶层键「%s」" % (rel(f), k))
    return probs


def prim_coverage(args):
    probs, req = [], set(args["required"])
    for f in expand(args["file"]):
        nodes = sel(load_json(f), toks(args["dict_path"]))
        probs += _hits_guard("S3/S4/S9", args["dict_path"], nodes, args)
        for nd in nodes:
            ks = _dict_keys(nd)
            if ks is None:
                probs.append("[S3/S4/S9] %s `%s` 不是对象" % (rel(f), args["dict_path"]))
                continue
            miss = sorted(req - ks)
            if miss:
                probs.append("[S3/S4/S9] %s `%s` 缺：%s" % (rel(f), args["dict_path"], ", ".join(miss)))
            if not args.get("allow_extra", True):
                extra = sorted(ks - req)
                if extra:
                    probs.append("[S3/S4/S9] %s `%s` 有多余项：%s" % (rel(f), args["dict_path"], ", ".join(extra)))
    return probs


def prim_enum_subset(args):
    probs, allowed = [], set(args["allowed"])
    for f in expand(args["file"]):
        vals = sel(load_json(f), toks(args["collect"]))
        probs += _hits_guard("S5", args["collect"], vals, args)
        for v in vals:
            if isinstance(v, str) and v not in allowed:
                probs.append("[S5] %s `%s` 取值非法：%r（允许 %s）"
                             % (rel(f), args["collect"], v, "/".join(sorted(allowed))))
    return probs


def prim_ref_exists(args):
    target = set(args.get("extra_allowed") or [])
    for tf in expand(args["target_file"]):
        for nd in sel(load_json(tf), toks(args["target_dict"])):
            ks = _dict_keys(nd)
            if ks:
                target |= ks
    probs = []
    for f in expand(args["file"]):
        vals = sel(load_json(f), toks(args["collect"]))
        probs += _hits_guard("S6/S10", args["collect"], vals, args)
        for v in vals:
            if isinstance(v, str) and v not in target:
                probs.append("[S6/S10] %s `%s` 指向不存在的目标：%s" % (rel(f), args["collect"], v))
    return probs


def prim_range_contiguous(args):
    probs = []
    for f in expand(args["file"]):
        arrs = sel(load_json(f), toks(args["collect"]))
        probs += _hits_guard("S7", args["collect"], arrs, args)
        for arr in arrs:
            if not isinstance(arr, list):
                # ★ 不再静默跳过：这几乎必然是 collect 声明写错（如误加了 [*]）
                probs.append("[S7] %s `%s` 选中的不是数组（而是 %s）—— "
                             "**疑似 collect 声明有误**（`[*]` 会展开元素；此处应指向数组本身）"
                             % (rel(f), args["collect"], type(arr).__name__))
                continue
            prev = None
            for i, b in enumerate(arr):
                if not isinstance(b, dict):
                    continue
                lo, hi = b.get(args["lower_key"]), b.get(args["upper_key"])
                if i == 0:
                    if lo is not None:
                        probs.append("[S7] %s 首项 lower 应为 null，实为 %r" % (rel(f), lo))
                elif lo != prev + 1:
                    probs.append("[S7] %s 档位不连续/有重叠：lower=%r，上一档 upper=%r（应 %r）"
                                 % (rel(f), lo, prev, prev + 1))
                prev = hi if hi is not None else float("inf")
            if arr and isinstance(arr[-1], dict) and arr[-1].get(args["upper_key"]) is not None:
                probs.append("[S7] %s 末项 upper 应为 null，实为 %r" % (rel(f), arr[-1].get(args["upper_key"])))
    return probs


def prim_pattern_absent(args):
    probs, pats = [], [re.compile(p) for p in args["forbidden_regex"]]
    for f in expand(args["file"]):
        vals = sel(load_json(f), toks(args["collect"]))
        probs += _hits_guard("S8/S11", args["collect"], vals, args)
        for v in vals:
            for s in _all_strings(v):
                for p in pats:
                    if p.search(s):
                        probs.append("[S8/S11] %s `%s` 出现禁止模式 /%s/：%r"
                                     % (rel(f), args["collect"], p.pattern, s))
                        break
    return probs


def prim_cross_equal_by_key(args):
    right = {}
    for tf in expand(args["right_file"]):
        for nd in sel(load_json(tf), toks(args["right_dict"])):
            if isinstance(nd, dict):
                for k, v in nd.items():
                    if not k.startswith("_") and isinstance(v, dict):
                        right[k] = sorted(v.get(args["right_value"]) or [])
    probs, compared = [], 0
    for f in expand(args["left_glob"]):
        d = load_json(f)
        key = d.get(args["left_key"])
        if not isinstance(key, str) or key not in right:
            continue
        compared += 1
        left = sorted(d.get(args["left_value"]) or [])
        if left != right[key]:
            probs.append("[S12] %s 的 %s=%s 与 %s.%s[%s]=%s **不一致**"
                         % (rel(f), args["left_value"], left,
                            rel(expand(args["right_file"])[0]), args["right_dict"], key, right[key]))
    if compared < args.get("min_hits", 1):
        probs.append("[S12] 仅比对到 %d 个单据（要求 ≥%d）—— 疑似左右两侧 key 对不上"
                     % (compared, args.get("min_hits", 1)))
    return probs


PRIMITIVES = {
    "json_parse": prim_json_parse,
    "required_keys": prim_required_keys,
    "coverage": prim_coverage,
    "enum_subset": prim_enum_subset,
    "ref_exists": prim_ref_exists,
    "range_contiguous": prim_range_contiguous,
    "pattern_absent": prim_pattern_absent,
    "cross_equal_by_key": prim_cross_equal_by_key,
}


# ---------------------------------------------------------------- 主流程
def check(cl_path):
    if not os.path.isfile(cl_path):
        print("FAIL [%s] 找不到判据清单：%s" % (NAME, cl_path))
        return 2
    try:
        cl = load_json(cl_path)
    except Exception as e:
        print("FAIL [%s] 判据清单不可解析：%s" % (NAME, e))
        return 2

    checks = cl.get("checks") or []
    decl_prims = set((cl.get("primitives") or {}).keys())
    problems = []

    # 清单自身也要能被信任
    unknown = [c.get("primitive") for c in checks if c.get("primitive") not in PRIMITIVES]
    if unknown:
        problems.append("[META] 清单引用了未实现的原语：%s" % ", ".join(sorted(set(unknown))))
    if decl_prims - set(PRIMITIVES):
        problems.append("[META] primitives 声明了未实现的引擎：%s" % ", ".join(sorted(decl_prims - set(PRIMITIVES))))
    if not checks:
        problems.append("[META] 清单里没有任何 checks（判据为空？）")

    ran = 0
    for c in checks:
        cid, prim = c.get("id", "?"), c.get("primitive")
        fn = PRIMITIVES.get(prim)
        if fn is None:
            continue
        try:
            ps = fn(c.get("args") or {})
        except Exception as e:
            ps = ["[%s] 执行原语 %s 时异常：%s" % (cid, prim, e)]
        ran += 1
        problems += ps

    if problems:
        print("FAIL [%s] 共 %d 处违规（清单 %s · 执行 %d/%d 条判据）："
              % (NAME, len(problems), rel(cl_path), ran, len(checks)))
        for p in problems:
            print("  " + p)
        return 1

    ids = " ".join(c.get("id", "?") for c in checks)
    nfiles = len(glob.glob(abs_path("spec/**/*.json"), recursive=True))
    print("OK [%s] spec/ 机读规格校验通过（清单 %s · %d 条判据 [%s] · %d 个 JSON · %d 个原语引擎）"
          % (NAME, rel(cl_path), ran, ids, nfiles, len(PRIMITIVES)))
    return 0


def main():
    cl_path = sys.argv[1] if len(sys.argv) > 1 else abs_path("spec/checks.json")
    return check(cl_path)


if __name__ == "__main__":
    sys.exit(main())
