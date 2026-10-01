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
 · ★ **2026-09-29 第 5 例（判据 ID 挂错名字）**：新增 `S5c/S6b` 后跑「探针自证」，
   断言「报错里含 `S5c`」**失败** —— 但门禁其实**已拦下**，只是报成了 `[S8/S11]`。
   根因：`pattern_absent` 等原语把判据 ID **硬编码**；同一原语被多条判据复用后必然串名。
   **定性：这比"不报错"更坏 —— 报错本身在误导排查方向。**
   ⇒ 改为执行器注入 `args["__cid__"]`，并加 `_self_audit_cid_literals()` 自审（禁止回潮）。
 · ★ 探针同时提供**缺口存在性反证**：把新判据从清单摘掉后，同样篡改必须**静默放行**
   （`S5c`↔`L10` / `S6b`↔悬空档位路由）—— 只有「有它即拦」＋「无它即漏」两者齐备，
   才能证明缺口真实存在且是该判据关掉的。见 `scripts/_probe_s5c_s6bc.py`。
 · ★ **引擎能力是两侧共同契约**：本次一度给 `ref_exists` 加了 `split` 参数（为校验
   `route_by_condition` 管道串），**Go 侧加载 spec 时直接 panic** —— 单侧扩展引擎会立刻
   把"清单唯一真相"变成"清单说的与 Go 做的不是一回事"。⇒ 已撤，改由 **N-021** 双方同时引入。

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


def _scalar_str(v):
    """
    标量 → 规范字符串（供 `set_covers` 比较用）。★ 与 Go 侧 `scalarString` **同口径**：
      · 字符串原样；整数 / 整数值浮点 → 去小数点的十进制（`8.0` → `"8"`）；布尔 → `"true"/"false"`；
      · **非标量（dict/list/None）返回 None**（调用方跳过，不报错 —— 与 Go 的 `default: return "", false` 一致）。
    ★ 为什么必须两边口径一致：`required` 里可能写 `1..8` 的数字，而 JSON 解出来一边是 int、一边是 float64；
      若两侧规范化不同，就会出现「同一份清单，我这边绿、你那边红」。这正是"两侧共同契约"要防的漂移。
    """
    if isinstance(v, bool):          # ★ 必须排在 int 之前（Python 里 bool 是 int 的子类）
        return "true" if v else "false"
    if isinstance(v, int):
        return str(v)
    if isinstance(v, float):
        return str(int(v)) if v == int(v) else str(v)
    if isinstance(v, str):
        return v
    return None


# ---------------------------------------------------------------- 8 个原语引擎
#
# ★★ 2026-09-29 加固（**探针 C 逼出的第 4 例"门禁自身假绿"**）：
#   原实现里 `range_contiguous` 遇到"选中值不是 list"就 `continue` 跳过 ——
#   于是**清单里 collect 写错**（如把 `thresholds.purchase.bands` 误写成 `bands[*]`，
#   后者会被展开成 3 个 dict 而非 1 个 list）⇒ **判据一条都没跑，却报 OK**。
#   ⇒ 现为**所有 collect 型原语**加 `min_hits`（默认 1）：**选中数不足即报错**。
#   ★ 这条加固的意义与「S9 递归扫描」「B1 三则」同源：**不许把"声明写错"静默成"通过"**。
#
# ★★ 2026-09-29 再加固（**探针逼出的第 5 例：判据 ID 挂错名字**）：
#   原实现把判据 ID **硬编码在每个原语里**（如 `pattern_absent` 一律打印 `[S8/S11]`）。
#   一旦同一原语被**多条判据**复用（现已有 S1–S12 共 16 条复用在 8 个原语上），
#   就会**报出别人的名字** —— 例：`S5c` 拦下了违规，报错却写 `[S8/S11]`；
#   排查者照着错误 ID 去查，必然被带偏。**这比"不报错"更坏：报错本身是误导。**
#   ⇒ 执行器把真实 `cid` 注入 `args["__cid__"]`，**所有原语一律取它作前缀**。
#     任何新原语**不得再自持 ID 字面量**。
def _cid(args):
    """取执行器注入的真实判据 ID（缺失即说明执行路径不对，显式暴露而不是静默）。"""
    return args.get("__cid__") or "?"


def _hits_guard(cid, collect, values, args):
    mn = args.get("min_hits", 1)
    if len(values) < mn:
        return ["[%s] `%s` 仅命中 %d 处（要求 ≥%d）—— **疑似清单声明有误**，不是「通过」"
                % (cid, collect, len(values), mn)]
    return []


def prim_json_parse(args):
    cid, probs, files = _cid(args), [], expand(args["file"])
    if not files:
        probs.append("[%s] glob `%s` 未匹配到任何文件（规格为空？）" % (cid, args["file"]))
    for f in files:
        try:
            load_json(f)
        except json.JSONDecodeError as e:
            probs.append("[%s] %s JSON 语法错误：%s" % (cid, rel(f), e))
        except Exception as e:
            probs.append("[%s] %s 读取失败：%s" % (cid, rel(f), e))
    return probs


def prim_required_keys(args):
    cid, probs = _cid(args), []
    for f in expand(args["file"]):
        nodes = sel(load_json(f), toks(args.get("scope", "")))
        if not nodes:
            probs.append("[%s] %s scope `%s` 未命中节点" % (cid, rel(f), args.get("scope", "")))
        for nd in nodes:
            ks = _dict_keys(nd)
            if ks is None:
                probs.append("[%s] %s scope `%s` 不是对象" % (cid, rel(f), args.get("scope", "")))
                continue
            for k in args["keys"]:
                if k not in ks:
                    probs.append("[%s] %s 缺顶层键「%s」" % (cid, rel(f), k))
    return probs


def prim_coverage(args):
    cid, probs, req = _cid(args), [], set(args["required"])
    for f in expand(args["file"]):
        nodes = sel(load_json(f), toks(args["dict_path"]))
        probs += _hits_guard(cid, args["dict_path"], nodes, args)
        for nd in nodes:
            ks = _dict_keys(nd)
            if ks is None:
                probs.append("[%s] %s `%s` 不是对象" % (cid, rel(f), args["dict_path"]))
                continue
            miss = sorted(req - ks)
            if miss:
                probs.append("[%s] %s `%s` 缺：%s" % (cid, rel(f), args["dict_path"], ", ".join(miss)))
            if not args.get("allow_extra", True):
                extra = sorted(ks - req)
                if extra:
                    probs.append("[%s] %s `%s` 有多余项：%s" % (cid, rel(f), args["dict_path"], ", ".join(extra)))
    return probs


def prim_enum_subset(args):
    cid, probs, allowed = _cid(args), [], set(args["allowed"])
    for f in expand(args["file"]):
        vals = sel(load_json(f), toks(args["collect"]))
        probs += _hits_guard(cid, args["collect"], vals, args)
        for v in vals:
            if isinstance(v, str) and v not in allowed:
                probs.append("[%s] %s `%s` 取值非法：%r（允许 %s）"
                             % (cid, rel(f), args["collect"], v, "/".join(sorted(allowed))))
    return probs


def prim_ref_exists(args):
    # ★ `split`（议题 N-021）：某些字段把多个引用**串在一个字符串里**（管道串），
    #   如 `route_by_condition: "expense_sales | expense_mgmt_advance | expense_mgmt_direct"`
    #   ⇒ 须先按分隔符拆段、逐段 TrimSpace、**空段跳过**（容忍多余空格/尾随分隔符）再逐段校验。
    #   ★ **未设 split ⇒ 行为与原先逐字节一致**（向后兼容，不影响任何既有判据）。
    #   ★ 语义与 Go 侧 `argStringOpt` 分支**逐字对齐**（两侧共同契约，见 spec/checks.json#consumer_obligations）。
    cid, split = _cid(args), args.get("split")
    target = set(args.get("extra_allowed") or [])
    for tf in expand(args["target_file"]):
        for nd in sel(load_json(tf), toks(args["target_dict"])):
            ks = _dict_keys(nd)
            if ks:
                target |= ks
    probs = []
    for f in expand(args["file"]):
        vals = sel(load_json(f), toks(args["collect"]))
        probs += _hits_guard(cid, args["collect"], vals, args)
        for v in vals:
            if not isinstance(v, str):
                continue
            refs = [v] if not split else [x.strip() for x in v.split(split) if x.strip()]
            for ref in refs:
                if ref not in target:
                    probs.append("[%s] %s `%s` 引用 %r 不存在于目标键集合"
                                 % (cid, rel(f), args["collect"], ref))
    return probs


def prim_set_covers(args):
    """
    ★ 第 9 原语（议题 N-022）：**集合覆盖** —— collect 取到的**标量**并成一个集合，断言 ⊇ `required`，
      缺哪项报哪项。

    为什么需要它：有两条硬判据是「集合包含」语义，用既有 8 原语**表达不了** ——
      ① `forms/CT.json` 的**制度第三十五条 8 组必备条款完整性**（字段上的标量值 1..8；
         `coverage` 只作用于 **dict 的键**，校验不了「1..8 全在」）；
      ② 可写台账字段与其登记白名单的**跨集合**关系。
    ★ 语义与 Go 侧 `case "set_covers"` **逐字对齐**：
      · 标量规范化：字符串原样；数字按整数值去小数点（`8.0` → `"8"`）；布尔 → `"true"/"false"`；**非标量跳过**（不报错）；
      · `min_hits`（默认 1）不足 ⇒ 报「疑似清单声明有误」，**不许静默通过**（与既有加固同口径）。
    """
    cid = _cid(args)
    probs = []
    required = [_scalar_str(x) for x in args.get("required") or []]
    required = [x for x in required if x is not None]
    for f in expand(args["file"]):
        vals = sel(load_json(f), toks(args["collect"]))
        probs += _hits_guard(cid, args["collect"], vals, args)
        got = set()
        for v in vals:
            s = _scalar_str(v)
            if s is not None:
                got.add(s)
        for need in required:
            if need not in got:
                probs.append("[%s] %s `%s` 集合缺少必需项 %r（collect 覆盖不足）"
                             % (cid, rel(f), args["collect"], need))
    return probs


def prim_range_contiguous(args):
    cid, probs = _cid(args), []
    for f in expand(args["file"]):
        arrs = sel(load_json(f), toks(args["collect"]))
        probs += _hits_guard(cid, args["collect"], arrs, args)
        for arr in arrs:
            if not isinstance(arr, list):
                # ★ 不再静默跳过：这几乎必然是 collect 声明写错（如误加了 [*]）
                probs.append("[%s] %s `%s` 选中的不是数组（而是 %s）—— "
                             "**疑似 collect 声明有误**（`[*]` 会展开元素；此处应指向数组本身）"
                             % (cid, rel(f), args["collect"], type(arr).__name__))
                continue
            prev = None
            for i, b in enumerate(arr):
                if not isinstance(b, dict):
                    continue
                lo, hi = b.get(args["lower_key"]), b.get(args["upper_key"])
                if i == 0:
                    if lo is not None:
                        probs.append("[%s] %s 首项 lower 应为 null，实为 %r" % (cid, rel(f), lo))
                elif lo != prev + 1:
                    probs.append("[%s] %s 档位不连续/有重叠：lower=%r，上一档 upper=%r（应 %r）"
                                 % (cid, rel(f), lo, prev, prev + 1))
                prev = hi if hi is not None else float("inf")
            if arr and isinstance(arr[-1], dict) and arr[-1].get(args["upper_key"]) is not None:
                probs.append("[%s] %s 末项 upper 应为 null，实为 %r" % (cid, rel(f), arr[-1].get(args["upper_key"])))
    return probs


def prim_pattern_absent(args):
    cid, probs = _cid(args), []
    pats = [re.compile(p) for p in args["forbidden_regex"]]
    for f in expand(args["file"]):
        vals = sel(load_json(f), toks(args["collect"]))
        probs += _hits_guard(cid, args["collect"], vals, args)
        for v in vals:
            for s in _all_strings(v):
                for p in pats:
                    if p.search(s):
                        probs.append("[%s] %s `%s` 出现禁止模式 /%s/：%r"
                                     % (cid, rel(f), args["collect"], p.pattern, s))
                        break
    return probs


def prim_cross_equal_by_key(args):
    cid, right = _cid(args), {}
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
            probs.append("[%s] %s 的 %s=%s 与 %s.%s[%s]=%s **不一致**"
                         % (cid, rel(f), args["left_value"], left,
                            rel(expand(args["right_file"])[0]), args["right_dict"], key, right[key]))
    if compared < args.get("min_hits", 1):
        probs.append("[%s] 仅比对到 %d 个单据（要求 ≥%d）—— 疑似左右两侧 key 对不上"
                     % (cid, compared, args.get("min_hits", 1)))
    return probs


def prim_array_each_required(args):
    """★ 第 10 原语（`N-036`）：**数组逐项条件必填** —— 对 `collect` 收集到的**每个数组元素**，
    当 `when_key` 缺省（无条件）或 `元素[when_key] ∈ when_in`（可用 `when_key2`/`when_in2` 再加一个 AND 条件）时，
    `required_keys` 中每个键**必须存在**。

    为什么需要它：既有 9 个原语**都表达不了「数组每一项都必填」** ——
      · `required_keys` 只作用于 **scope 的键**；`coverage` 只作用于 **dict 的键**；
      · `set_covers.min_hits` 是**逐文件**计数（不是全局），表达不了「一条不缺」。
    ⇒ ★ 本原语专治「**声明了却没人执行**」这类缺口：判据的 `else` 若承诺「拒绝」，
      **必须说清谁来执行它**；缺声明就报错，不许静默。

    ★ 语义与 Go 侧 `internal/specload/checklist.go#case "array_each_required"` **逐字对齐**
      （两侧共同契约，见 `spec/checks.json#consumer_obligations`）。
    """
    cid, probs = _cid(args), []
    pat = args["file"]
    collect = args["collect"]
    required = args.get("required_keys") or []
    wk, wi = args.get("when_key") or "", [str(x) for x in (args.get("when_in") or [])]
    wk2, wi2 = args.get("when_key2") or "", [str(x) for x in (args.get("when_in2") or [])]

    def _match(v):
        """值 → 参与 when_in 比较的字符串（★ 兜底口径对齐 Go 的 %v：bool/数值见 _scalar_str）。"""
        if isinstance(v, str) and v:
            return v
        s = _scalar_str(v)
        return s if s is not None else None

    seen = 0
    for f in expand(pat):
        for i, item in enumerate(sel(load_json(f), toks(collect))):
            if not isinstance(item, dict):
                probs.append("[%s] %s 的 %s 第 %d 项不是对象" % (cid, rel(f), collect, i))
                continue
            seen += 1
            if wk:
                if wk not in item:
                    # ★ 条件键缺失 ⇒ 不在适用面（由该键自身的 required 判据管）
                    continue
                if _match(item[wk]) not in wi:
                    continue
            if wk2:
                if wk2 not in item:
                    continue
                if _match(item[wk2]) not in wi2:
                    continue
            for k in required:
                if k not in item:
                    probs.append(
                        "[%s] %s 的 %s 第 %d 项缺键 %r（条件必填未声明 —— "
                        "「声明了却没人执行」必须能被机器看见）"
                        % (cid, rel(f), collect, i, k))
    if seen == 0:
        probs.append("[%s] collect=%r 在 file=%r 下收集到 0 项（清单声明写错不许静默通过）"
                     % (cid, collect, pat))
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
    "set_covers": prim_set_covers,
    "array_each_required": prim_array_each_required,
}


# ---------------------------------------------------------------- 主流程
def _self_audit_cid_literals():
    """
    ★ 源码自审（第 5 例缺陷的**回归守卫**）：本文件**代码里的字符串**不得再出现硬编码的判据 ID
      字面量（形如 "方括号 + S 编号 + 空格" 开头的那种消息前缀）。判据 ID 必须动态取自执行器
      注入的 `args["__cid__"]`，否则同一原语被多条判据复用时，又会**报出别人的名字**。

    实现要点：用 `tokenize` 剥掉注释，再用 `ast` 收集**docstring 的行区间**并排除 ——
      本文件自身的说明性 docstring 里**必然要举例**，不能因此误红（否则守卫会被迫"删例子"）。
    """
    import ast
    import tokenize

    src = open(os.path.abspath(__file__), "r", encoding="utf-8").read()

    # 收集 docstring 占用的行区间
    doc_lines = set()
    try:
        tree = ast.parse(src)
    except SyntaxError as e:
        return ["[META] check_spec.py 自身语法错误，无法自审：%s" % e]
    for node in ast.walk(tree):
        if isinstance(node, (ast.Module, ast.ClassDef, ast.FunctionDef, ast.AsyncFunctionDef)):
            body = getattr(node, "body", None)
            if body and isinstance(body[0], ast.Expr) and isinstance(body[0].value, ast.Constant) \
                    and isinstance(body[0].value.value, str):
                d = body[0].value
                for ln in range(d.lineno, getattr(d, "end_lineno", d.lineno) + 1):
                    doc_lines.add(ln)

    bad = []
    with open(os.path.abspath(__file__), "rb") as fh:
        for tok in tokenize.tokenize(fh.readline):
            if tok.type != tokenize.STRING:
                continue
            if tok.start[0] in doc_lines:
                continue                      # docstring 允许举例
            if re.search(r"\[S\d", tok.string):
                bad.append("第 %d 行字符串字面量含判据 ID：%s"
                           % (tok.start[0], tok.string.strip()[:60]))
    if bad:
        return ["[META] check_spec.py 内出现**硬编码判据 ID**（应用 `_cid(args)` 动态取）：\n    "
                + "\n    ".join(bad)]
    return []


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

    # ★ 判据 ID 自身必须可辨识（否则报错全是 `[?]`，等于没有定位信息）
    ids = [c.get("id") for c in checks]
    if any(not i for i in ids):
        problems.append("[META] 存在**空 id** 的判据（报错时将无法定位）")
    dups = sorted({i for i in ids if ids.count(i) > 1})
    if dups:
        problems.append("[META] 判据 **id 重复**：%s（报错无法区分是哪条）" % ", ".join(dups))

    # ★ 源码自审：判据 ID 不得硬编码
    problems += _self_audit_cid_literals()

    ran = 0
    for c in checks:
        cid, prim = c.get("id", "?"), c.get("primitive")
        fn = PRIMITIVES.get(prim)
        if fn is None:
            continue
        args = dict(c.get("args") or {})
        args["__cid__"] = cid           # ★ 注入真实判据 ID（原语一律用它做前缀）
        try:
            ps = fn(args)
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

    ids_s = " ".join(c.get("id", "?") for c in checks)
    nfiles = len(glob.glob(abs_path("spec/**/*.json"), recursive=True))
    print("OK [%s] spec/ 机读规格校验通过（清单 %s · %d 条判据 [%s] · %d 个 JSON · %d 个原语引擎）"
          % (NAME, rel(cl_path), ran, ids_s, nfiles, len(PRIMITIVES)))
    return 0


def main():
    cl_path = sys.argv[1] if len(sys.argv) > 1 else abs_path("spec/checks.json")
    return check(cl_path)


if __name__ == "__main__":
    sys.exit(main())
