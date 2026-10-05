#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
scripts/check_spec.py —— `spec/` 机读规格门禁（**清单执行器**）。

★★ 本脚本**不自持判据** —— 判据全部来自 `spec/checks.json`（唯一来源）。
   它只实现 `checks.json#primitives` 里声明的原语引擎（★ **数量不写死**，以声明为准），然后逐条执行 `checks`。

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
 · ★ **2026-10-04 第 6 例（台账静默漂移）**：`spec/acceptance.csv` 随批 12 更新时**漏落 `BA` 8 行**
   （`severity` 列仍 `(未声明)`），★ **却通过了全部 8 道必绿门禁** —— 根因：**全仓没有任何脚本读这张表**
   （只有 3 处注释提及）⇒ 台账与真源之间**没有机械校验、全靠人眼**。⇒ 新增 `_ledger_vs_forms()`：
   `[META]` 自查类、**不引入新原语**、不触碰 Go 侧加载器（无「两侧同批」问题）。
   ★ 探针自证：拿**修复前**的台账跑 ⇒ **8 处逐条报出**；复位后 ⇒ 0 处（见议题 `N-053`）。
 · ★★ **2026-10-05 第 7 例（把兜底升级为正式判据 ⇒ 同一判定只剩一处来源）**：议题 `N-053` 批 17 第 ③ 步 ——
   上面的 `_ledger_vs_forms()` **连同 `LEDGER`/`LEDGER_COLS` 常量一并删除**，其四条语义**原样搬进
   `checks.json#S26`**（第 12 原语 `csv_col_eq_json_by_key`）⇒ ★ 该判定不再有「两条来源」，
   与本文件「不自持判据」的定位一致（`N-011`「消灭第三份真相」）。
   ★★ **等价性不是推断**：`scripts/_probe_n053.py` 拿**修复前**的台账（`ef532dc` 版本）跑 `S26`
   ⇒ **逐条报出 32 处**，与迁移前 `_ledger_vs_forms()` 在**同一夹具**上的 **32 处逐项对应**；
   ★ 并做**缺口存在性反证**（把 `S26` 从清单摘掉 ⇒ 同一篡改**静默放行**）。
   ★★ **落地时实测逼出的一处硬约束（值得记住）**：Go 侧 `Load()` 原先**只装载 `*.json`**
   ⇒ `spec/acceptance.csv` **不在** Go 的 `files` 面内 ⇒ ★ 本原语一被清单引用，Go 侧
   **14 个测试当场全红**（`[S26] CSV 文件不在装载面`）⇒ ★★ **「先扩装载面、再引原语」的次序不可颠倒**
   —— 这是 `N-011`/`N-048` 同型的「两侧同批」约束在**装载层**的表现（该装载面修复已先由 mimo 落地）。
 · ★★ **2026-10-05 第 8 例（台账的另一种漂移：**索引失真**）**：`spec/institution-anchors.json`
   （`N-006` 第二重机制：制度条款 ↔ 机读规格双向索引）自 V1.0 起**只由仓库外的一次性原型脚本生成**
   ⇒ 留下窗口：**改了 spec、索引未重生 ⇒ 索引失真**。★ 既有 `S20`（指针可解析）**抓不住「有新的
   引用没被索引」** —— 两者是**不同的洞**。⇒ 新增 `_institution_anchors_counts()`：
   `[META]` 自查类（**不新增原语、不改 `checks.json`、不触碰 Go 侧加载器** ⇒ 无「两侧同批」约束），
   口径的唯一来源＝`scripts/gen_institution_anchors.py`（本文件**只比对**）。
   ★★ **它当场就抓到了真漂移（非构造）**：索引 V1.1 相对当前 spec **失真 3 处**，全部源于批 7（`B6`）
   **机械生成**的 `spec/openapi.json`（晚于索引生成时点）—— 新增条款「第二条」1 处 ＋ 「第五十二条」
   1 处。★ 等价性亦已实测：**排除该生成物后，重算与索引 28/28 条款 · `citation_count` ·
   `citation_by_file` 全等**（故重算口径可被信任）。

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


def _dig(node, key_path):
    """
    按**点路径**逐段下钻取单值（★ 与 Go 侧 `dig`（`internal/specload/checklist.go:1104`）**逐字同口径**）：
    每段必须是 dict 的键；**取不到**（中途不是 dict / 键不存在）⇒ 返回 `None`。

    ★ 为什么不复用 `sel(node, toks(path))`：`sel` 是实现**点路径选择器**（支持 `*`/`**`/`[k=v]`）的通用取值器，
      返回**值列表**；而本文件里 `json_key[*]`/`pairs[*].json_field` 的语义是 Go 的 `dig` —— **朴素 `split(".")`
      + 逐段 key 查找**。★ 两者在**含 `*` 或 `[` 的键名**上会分叉 ⇒ ★ 必须与 Go 同口径，否则
      「同一份清单，两侧相反结论」。★ 缺键返回 `None`（＝不可比），由调用方报错，**不静默跳过**。
    """
    cur = node
    for seg in key_path.split("."):
        if not isinstance(cur, dict) or seg not in cur:
            return None
        cur = cur[seg]
    return cur


# ------------------------------------------------- 原语引擎（★ 数量不写死，以声明为准）
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


def _parse_ref_target_filter(args, cid):
    """
    `N-049` ② 第二半：解析可选 `target_filter`（**派生白名单**）。★ 与 Go 侧
    `parseRefTargetFilter`（`internal/specload/checklist.go`）**逐字同口径**；
    规格正本 ＝ `spec/checks.json#_pending_arg_note`。

    返回 `(filter, problems)`：
      · 未设该键 ⇒ `(None, [])` —— **完全旁路**（行为与原先逐字节一致）；
      · 形态非法（非对象 / 缺非空字符串 `field` / 缺数组 `in` / `in` 非数组）
        ⇒ `(None, [一条可见报错])` —— **fail-closed ①：不静默忽略**
        （否则「参数写错」＝「白名单照旧全收」，判据回到失效态）。
    ★ `in: []` **不算**形态非法（其后果由「过滤后白名单为空」兜住）。
    """
    if "target_filter" not in args:
        return None, []
    tf = args["target_filter"]
    if not isinstance(tf, dict):
        return None, ["[%s] target_filter 形态非法：必须是对象 {\"field\":…,\"in\":[…]}（实为 %s）"
                      % (cid, type(tf).__name__)]
    field = tf.get("field")
    if not isinstance(field, str) or not field.strip():
        return None, ["[%s] target_filter 形态非法：缺非空字符串字段 field" % cid]
    if "in" not in tf:
        return None, ["[%s] target_filter 形态非法：缺数组字段 in" % cid]
    inraw = tf["in"]
    if not isinstance(inraw, list):
        return None, ["[%s] target_filter 形态非法：in 必须是数组（实为 %s）"
                      % (cid, type(inraw).__name__)]
    inset = set()
    for x in inraw:
        s = _scalar_str(x)          # ★ 复用既有标量归一（与 Go `scalarString` 同口径）
        if s is not None:
            inset.add(s)
    return {"field": field, "in": inset}, []


def prim_ref_exists(args):
    # ★ `split`（议题 N-021）：某些字段把多个引用**串在一个字符串里**（管道串），
    #   如 `route_by_condition: "expense_sales | expense_mgmt_advance | expense_mgmt_direct"`
    #   ⇒ 须先按分隔符拆段、逐段 TrimSpace、**空段跳过**（容忍多余空格/尾随分隔符）再逐段校验。
    #   ★ **未设 split ⇒ 行为与原先逐字节一致**（向后兼容，不影响任何既有判据）。
    #   ★ 语义与 Go 侧 `argStringOpt` 分支**逐字对齐**（两侧共同契约，见 spec/checks.json#consumer_obligations）。
    #
    # ★ `target_filter`（议题 N-049 ② 第二半）：**派生白名单** —— 只收窄 `target_dict` 的键集合，
    #   不改收集面（收集面归 N-057）、不新增判据。语义（规格正本＝#_pending_arg_note）：
    #     ① 白名单 ＝「`field` 取得到标量值 **且** 该值 ∈ `in`」的键；取不到值（键不存在/路径中断/
    #        null/非标量）或值不在 `in` ⇒ 该键**不进白名单**（＝与 `none` 同等：非节点 actor）；
    #     ② 与 `split` **互不影响、顺序无关**（`split` 作用于**收集到的引用值**，本参数作用于**目标白名单**）；
    #     ③ 标量归一复用 `_scalar_str`（与 `set_covers`/`path_exists` 完全一致）。
    #   ★ **未设 ⇒ 行为与原先逐字节一致**（向后兼容）；★★ 与 Go 侧同批落地（`N-049` ② 硬次序第 ③ 步）——
    #     两侧任一单侧先行 ⇒ **白名单宽度不同、同一判据两个结论**，且该分歧**在当前数据上两侧都绿**（更难发现）。
    cid, split = _cid(args), args.get("split")
    flt, probs = _parse_ref_target_filter(args, cid)      # fail-closed ①（形态非法）
    target = set(args.get("extra_allowed") or [])
    missing = []
    for tf in expand(args["target_file"]):
        for nd in sel(load_json(tf), toks(args["target_dict"])):
            ks = _dict_keys(nd)
            if ks:
                for k in sorted(ks):
                    if flt is not None:
                        s = _scalar_str(_dig(nd[k], flt["field"]))   # ★ 复用 `dig` 点路径语义
                        if s is None:
                            # fail-closed ②：键不存在/路径中断/null/非标量 ⇒ **点名该键**
                            # （★「未分类」≠「分类为 none」；同 `S15` 之精神）
                            missing.append(k)
                            continue
                        if s not in flt["in"]:
                            continue          # 值不在 in ⇒ 不进白名单（＝与 none 同等）
                    target.add(k)
    for k in missing:
        probs.append("[%s] target_dict 键 %r 缺字段 %r（取不到标量值 —— 未分类不许静默当成可入白名单）"
                     % (cid, k, flt["field"]))
    # fail-closed ③：过滤后白名单为空（★ extra 并入后仍空 ⇒ 过滤写错不许静默掏空判据面）
    if flt is not None and not target:
        probs.append("[%s] target_filter 过滤后白名单为空（过滤写错不许静默掏空判据面）" % cid)
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
    # ★ `allow_empty_match`（2026-10-03）：条件面为空时**显式豁免**，须在判据 args 里写 true
    #   并由 desc 说明依据 —— ★ 目的是「豁免必须留痕」，而不是「豁免可以悄悄做」。
    aem = args.get("allow_empty_match") is True

    def _match(v):
        """值 → 参与 when_in 比较的字符串（★ 兜底口径对齐 Go 的 %v：bool/数值见 _scalar_str）。"""
        if isinstance(v, str) and v:
            return v
        s = _scalar_str(v)
        return s if s is not None else None

    seen = 0
    # ★★ `matched` ＝ **真正进入条件必填面**的项数（两个 when 都通过）。
    #   为什么要它：★ `seen` 只证明「collect 收到了东西」，**不证明 when_in 写得对** ——
    #   ★ 实测（2026-10-03）：`when_key=immutable` 配 `when_in=["True"]`（大写 T）时，
    #   字段实际值是 JSON 布尔 `true`、两侧引擎都归一为小写 `"true"` ⇒ **一项都不匹配**，
    #   ★ 而 `seen=259 > 0` ⇒ **`seen==0` 守卫看不见、门禁显示全绿、判据完全空转**。
    #   ⇒ 这是「**条件写错冒充条件不适用**」，与 `seen==0`（collect 写错）是**不同**的形态。
    matched = 0
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
            matched += 1
            for k in required:
                if k not in item:
                    # ★ 命中必须**可定位**：给出「哪一项」的**业务标识**（字段名/判据 id），
                    #   否则「第 N 项」要重跑脚本逐个数数组下标 —— ★ 54 条红等于没报
                    #   （2026-10-03 `S16` 首跑即如此：54 条命中全是「第 N 项」，无一能直接定位）。
                    label = ""
                    for lk in ("name", "id", "key", "code", "label"):
                        v = item.get(lk)
                        if isinstance(v, str) and v.strip():
                            label = "（%s=%s）" % (lk, v)
                            break
                    probs.append(
                        "[%s] %s 的 %s 第 %d 项%s缺键 %r（条件必填未声明 —— 「声明了却没人执行」必须能被机器看见）"
                        % (cid, rel(f), collect, i, label, k))
    if seen == 0:
        probs.append("[%s] collect=%r 在 file=%r 下收集到 0 项（清单声明写错不许静默通过）"
                     % (cid, collect, pat))
    # ★★ 条件面为空守卫（2026-10-03 新增，与 Go 侧同批）：collect 收到了项、但**没有一项命中 when**。
    #   ★ 这正是「when_in 字面量写错」（大小写 / 枚举值拼错）时的形态 —— ★ 此前**任何守卫都看不见**，
    #   ★ 判据静默空转而门禁全绿（★ 已实测：`when_in=["True"]` vs 实际 `true` ⇒ 0 命中 0 报错）。
    #   ★ 与 `seen==0` 分开报，两者病因不同（collect 写错 vs 条件写错）。
    if seen > 0 and matched == 0 and not aem:
        probs.append(
            "[%s] collect=%r 收到 %d 项，但**无一项命中 when 条件**（when_key=%r when_in=%r"
            % (cid, collect, seen, wk, wi)
            + (" / when_key2=%r when_in2=%r" % (wk2, wi2) if wk2 else "")
            + "）—— ★ **条件字面量极可能写错**（大小写 / 枚举值），判据正静默空转；"
              "若确应为空，请在 desc 里显式声明 `allow_empty_match` 并说明依据")
    return probs


def split_ptr_segs(ptr):
    """★★ 点路径切分（`path_exists` 专用）：按 `.` 切段，**方括号 `[...]` 内部不切分**。

    ★ 两条**实测**必要性（`N-048` 取证，`spec/institution-anchors.json` 立据）：
      ① **字面键可含点**：`spec/params.json` 有 5 个含点扁平键
         （如 `params.reporting.monthly_cutoff_day`）⇒ 须写作 `params.[reporting.monthly_cutoff_day]`；
      ② **选择器值可含点**：`spec/checks.json#change_log[version=1.5]`。
    ★ 不做此规定 ⇒ 两种写法都会被切错 ⇒ **命中 0 而红**（安全失败：不会静默取到错节点）。
    """
    segs, buf, depth = [], "", 0
    for ch in ptr:
        if ch == "[":
            depth += 1
            buf += ch
        elif ch == "]":
            depth -= 1
            buf += ch
        elif ch == "." and depth == 0:
            if buf:
                segs.append(buf)
                buf = ""
        else:
            buf += ch
    if buf:
        segs.append(buf)
    return segs


def _ptr_walk(node, segs):
    """`path_exists` 的指针求值：返回命中节点列表。段语义：
      · `**`        递归下降
      · `*` / `[*]` 全部子节点
      · `[k=v]`     选择器：子节点中 `k` 的标量值 == `v` 者（≥1 命中）
      · `[字面键]`  字面键（允许含 `.`）
      · 裸键        普通对象键（不得含 `.`）
    """
    if not segs:
        return [node]
    head, rest = segs[0], segs[1:]
    out = []
    if head == "**":
        out += _ptr_walk(node, rest)
        for c in _children(node):
            out += _ptr_walk(c, segs)
        return out
    if head == "*":
        for c in _children(node):
            out += _ptr_walk(c, rest)
        return out
    m = re.match(r"^\[(.*)\]$", head)
    if m:
        body = m.group(1)
        if "=" in body:
            k, v = body.split("=", 1)
            for c in _children(node):
                if isinstance(c, dict) and _scalar_str(c.get(k)) == v:
                    out += _ptr_walk(c, rest)
            return out
        if isinstance(node, dict) and body in node:
            out += _ptr_walk(node[body], rest)
            return out
        for c in _children(node):
            if isinstance(c, dict) and body in c:
                out += _ptr_walk(c[body], rest)
        return out
    if isinstance(node, dict) and head in node:
        out += _ptr_walk(node[head], rest)
    return out


def _norm_ptr_segs(raw):
    """段归一（`path_exists` 专用）—— ★★ **须与 Go `normalizePtrSegs` 逐字同序**。

      · 独立段 `[*]` → `*`（全部子节点）；
      · 尾缀形 `name[k]`（如 `checks[id=x]`）**先按最后一个 `[` 拆开** ⇒ 裸键 `name` ＋ 段 `[k]`；
        其中 `name[*]` → `name` ＋ `*`（**数组元素**，不是裸键）。

    ★★ **顺序为什么必须钉死（`N-048` 验收期实测逼出的两侧分歧）**：
      旧实现**先把 `[*]` 换成 `*`、再拆尾缀** ⇒ `checks[*]` 被折成**裸键 `checks*`** ⇒ **命中 0**；
      Go 侧**先拆尾缀** ⇒ `checks` ＋ `*` ⇒ **命中**。
      ★ 真锚点（`institution-anchors.json` 158 条）**零 `[*]` 形态** ⇒ 当时两侧在实测上"看似一致"，
      **分歧只潜伏在未出现的形态上** —— 这正是"两侧共同契约"必须机检、不能靠"跑一遍都对"的原因。
      ★ 回归钉：`scripts/_probe_n048.py` 的「正向·`[*]` 段」用例在修正前**必红**、修正后必绿。
    ★ 与 `split_ptr_segs()` 分工：**切分**（方括号内不按 `.` 切）在那边，本函数只做**段归一**。
    """
    out = []
    for s in raw:
        if s == "[*]":
            out.append("*")
            continue
        i = s.rfind("[")
        if i > 0 and s.endswith("]"):
            out.append(s[:i])
            out.append("*" if s[i:] == "[*]" else s[i:])
            continue
        out.append(s)
    return out


def prim_path_exists(args):
    """★ 第 11 原语（`N-048`）：**锚点指针必须可解析** —— 收集到的每个字符串写成
    `<spec 相对路径>#<点路径>`（省略 `#` ＝ 只校验文件存在），断言：**文件存在** ∧ **点路径命中 ≥1 个节点**。

    为什么需要它：`spec/institution-anchors.json`（`N-006` 第 2 重机制）是**跨文件**的引用索引，
      ★ 既有 10 个原语**都表达不了**「这条指针指向的文件/字段真实存在」——
      `ref_exists` 只能比对**单个目标 dict 的键集合**，而锚点指向的落点**分散在任意文件、任意深度**。
      ⇒ ★ 这正是 `N-006` 明写的「**接入门禁**（锚点指向的文件/字段不存在 ⇒ 红）」。

    ★ 段语法（与 `spec/institution-anchors.json#conventions.segment_form` **逐字一致**）：
      裸键（不得含点）· `[字面键]`（可含点）· `[k=v]`（选择器）· `*`/`[*]`（全部子节点）· `**`（递归下降）。
    ★ 命中 0 ⇒ **报错**（与既有加固同口径：**不许把「声明写错」静默成「通过」**）。
    ★ 语义须与 Go 侧 `internal/specload/checklist.go#case "path_exists"` **逐字对齐**（两侧共同契约）。
    """
    cid, probs = _cid(args), []
    for f in expand(args["file"]):
        refs = sel(load_json(f), toks(args["collect"]))
        probs += _hits_guard(cid, args["collect"], refs, args)
        for ref in refs:
            if not isinstance(ref, str):
                probs.append("[%s] %s `%s` 出现非字符串项（%s）—— 锚点必须是字符串指针"
                             % (cid, rel(f), args["collect"], type(ref).__name__))
                continue
            if "#" in ref:
                path, ptr = ref.split("#", 1)
            else:
                path, ptr = ref, ""
            try:
                doc = load_json(abs_path(path))
            except Exception as e:
                probs.append("[%s] 锚点 %r 指向的文件不存在或不可解析：%s" % (cid, ref, e))
                continue
            if not ptr.strip():
                continue
            if not _ptr_walk(doc, _norm_ptr_segs(split_ptr_segs(ptr))):
                probs.append("[%s] 锚点 %r 的点路径在 %s 内**命中 0 个节点**（字段不存在 / 改名 / 语法写错）"
                             % (cid, ref, path))
    return probs


def prim_csv_col_eq_json_by_key(args):
    """★ 第 12 原语（`N-053`）：**CSV 台账某行的某列 == 以 `json_key` 拼键的 JSON 真源对象的某字段**。

    用途：断言 `spec/acceptance.csv`（判据级验收台账，`N-017`）的 `severity` / `carrier_kind`
      与真源 `spec/forms/*.json#checks` 的 `severity` / `carried_by_kind` **逐条一致**（行集双向相等）。

    ★ **为什么必须新增原语**：要断言的形态是「**CSV 行**的列 == 以 `(doc_type, check_id)` 为键的
      **JSON 对象**的字段」，★★ **而既有 11 个原语无一能表达** —— `cross_equal_by_key` 按 **JSON** 取数；
      `path_exists` 只判**指针可解析**；`coverage` / `enum_subset` 只作用于**单文件 scope**；
      `set_covers` 只做「集合包含」、不做跨格式联接。

    ★★ **立项依据 ＝ 真实事故（批 12 实测，非假想）**：该表随批 12 更新时**漏落 `BA` 8 行**
      （`severity` 仍 `(未声明)`、`BA#amount_tier1_only` 的 `carrier_kind` 仍 `pending_implementation`），
      ★ **却通过了当时全部 8 道必绿门禁** —— 根因：**全仓没有任何脚本读这张表**。
      ★ 台账自称「可机械核对」，其价值**全在**这一点；★ 漂移的恰是 `severity`（**决定判据跑不跑**）。

    ★★ **args（与 Go 侧 `case "csv_col_eq_json_by_key"` 逐字同口径）**：
      `file`（CSV 路径）· `key_cols`（按 **CSV 表头名**取键列）· `json_glob`（真源文件通配）·
      `json_collect`（每个匹配文件内的点路径，收集**对象数组**）· `json_key`（元素为「被收集对象上的
      字段路径」或保留记号 **`$file_stem`** ＝文件名去扩展名；按序拼成键）·
      `pairs`（`{csv_col, json_field, map?}`；`map` ＝ **JSON 侧字面量 → CSV 侧字面量**）·
      `require_same_row_set`（默认 `True`：键集合**双向相等**）· `csv_cols_expected`（可省，数据行列数）。

    ★★ **语义（逐条，与 `checks.json#_pending_primitive_note` 一致）**：
      ① 建真源索引：逐文件收集，**键重复 ⇒ 报错**（★ 不静默取后者）；② 逐 CSV 数据行按 `key_cols` 建键，
      **键不在真源 ⇒ 报错**；③ `require_same_row_set` 为真时**真源键未登记 ⇒ 报错**；
      ④ 每个 `pairs`：**JSON 字段值的标量归一先过 `map` 归一**，**再**与 CSV 值比较；
      ⑤ `csv_cols_expected` 不符 ⇒ 报错。
    ★ **标量归一复用 `_scalar_str`**（与 Go 的 `scalarString` 同口径）—— ★ 不得自造，否则两侧相反结论。
    ★ **fail-closed**：CSV 缺失 / `json_glob` **命中 0 个文件** / `json_collect` **命中 0 项** / **键重复**
      ⇒ **一律报错**（★ 不许把「声明写错」静默成「通过」）。
    ★ 语义正本 ＝ `spec/checks.json#primitives.csv_col_eq_json_by_key.desc`（本函数的 docstring 是其摘要，
      冲突时以清单为准 —— 本项目「清单是唯一真相」）。
    """
    import csv as _csv

    cid, probs = _cid(args), []
    csv_path = args.get("file") or ""
    key_cols = args.get("key_cols") or []
    json_glob = args.get("json_glob") or ""
    json_collect = args.get("json_collect") or ""
    json_key = args.get("json_key") or []
    pairs = args.get("pairs") or []
    same_row_set = args.get("require_same_row_set", True)
    cols_expected = args.get("csv_cols_expected", -1)

    # ① 参数完整性（缺一即报 —— 不静默返回空）
    if not str(csv_path).strip() or not key_cols or not str(json_glob).strip() \
            or not str(json_collect).strip() or not json_key or not pairs:
        return ["[%s] 参数不完整（必填：file/key_cols/json_glob/json_collect/json_key/pairs）" % cid]

    # ② CSV 读取（首行表头；★ 列数不固定 —— 由 csv_cols_expected 逐行自检）
    path = abs_path(csv_path)
    if not os.path.isfile(path):
        return ["[%s] CSV 文件不存在或不可读：%s" % (cid, csv_path)]
    with open(path, "r", encoding="utf-8", newline="") as fh:
        recs = list(_csv.reader(fh))
    if not recs:
        return ["[%s] %s CSV 为空（无表头）" % (cid, csv_path)]
    col_idx = {}
    for i, h in enumerate(recs[0]):
        col_idx[h] = i
    for kc in key_cols:
        if kc not in col_idx:
            return ["[%s] %s 表头缺键列 %r" % (cid, csv_path, kc)]
    pair_idx = []
    for pr in pairs:
        col = pr.get("csv_col")
        if col not in col_idx:
            return ["[%s] %s 表头缺比对列 %r" % (cid, csv_path, col)]
        pair_idx.append(col_idx[col])

    # ③ 真源索引
    matched = expand(json_glob)
    if not matched:
        return ["[%s] json_glob %r 命中 0 个文件（声明写错不许静默通过）" % (cid, json_glob)]
    key_sep = "\x1f"
    index = {}
    src_total = 0
    for name in matched:
        stem = os.path.splitext(os.path.basename(name))[0]
        try:
            root = load_json(name)
        except Exception as e:
            probs.append("[%s] %s 不可解析：%s" % (cid, rel(name), e))
            continue
        hits = sel(root, toks(json_collect))
        if not hits:
            probs.append("[%s] %s 内 json_collect %r 命中 0 项（收集面写错不许静默通过）"
                         % (cid, rel(name), json_collect))
            continue
        for h in hits:
            if not isinstance(h, dict):
                probs.append("[%s] %s 的 %s 命中非对象项（%s）—— 键只能建在对象上"
                             % (cid, rel(name), json_collect, type(h).__name__))
                continue
            parts = []
            for ek in json_key:
                if ek == "$file_stem":
                    parts.append(stem)
                else:
                    s = _scalar_str(_dig(h, ek))
                    parts.append(s if s is not None else "")
            key = key_sep.join(parts)
            if key in index:
                probs.append("[%s] 真源键重复（不静默取后者）：%r（%s）" % (cid, key, rel(name)))
                continue
            index[key] = (h, rel(name))
            src_total += 1
    if src_total == 0 and not probs:
        probs.append("[%s] json_collect 合计命中 0 项" % cid)

    # ④ 逐 CSV 数据行
    csv_keys = set()
    for ri, rec in enumerate(recs[1:]):
        row_no = ri + 2                      # 1-based 行号（表头 = 1）
        if len(rec) == 1 and rec[0] == "":
            continue                         # 全空行跳过（不当数据行）
        if cols_expected >= 0 and len(rec) != cols_expected:
            probs.append("[%s] %s 第 %d 行列数 = %d，期望 %d"
                         % (cid, csv_path, row_no, len(rec), cols_expected))
            # ★ 不 continue：列数错不阻断该行的键登记/比对（同 Go 侧）—— 否则 require_same_row_set
            #   会连带报出「未登记」第 2 条、掩盖根因。
        key_parts = []
        for kc in key_cols:
            idx = col_idx[kc]
            key_parts.append(rec[idx] if idx < len(rec) else "")
        key = key_sep.join(key_parts)
        if key in csv_keys:
            probs.append("[%s] CSV 键重复（第 %d 行）：%r" % (cid, row_no, key))
            continue
        csv_keys.add(key)
        if key not in index:
            probs.append("[%s] 键不在真源（%s 第 %d 行）：doc_type=%r check_id=%r（按 key_cols 联接）"
                         % (cid, csv_path, row_no, key_parts[0], key_sep.join(key_parts[1:])))
            continue
        obj, from_file = index[key]
        for i, pr in enumerate(pairs):
            idx = pair_idx[i]
            csv_val = rec[idx] if idx < len(rec) else ""
            json_val = _scalar_str(_dig(obj, pr.get("json_field")))
            if json_val is None:
                probs.append("[%s] %s 第 %d 行 键 %r 列 %r：真源字段 %r 非标量/缺失（不可比）—— CSV=%r"
                             % (cid, csv_path, row_no, key, pr.get("csv_col"),
                                pr.get("json_field"), csv_val))
                continue
            mp = pr.get("map") or {}
            if mp and json_val in mp:
                json_val = mp[json_val]      # ★ 键＝JSON 侧字面量、值＝CSV 侧字面量
            if csv_val != json_val:
                probs.append("[%s] %s 第 %d 行 键 %r 列 %r 不一致：CSV=%r vs 真源 %s.%s=%r"
                             % (cid, csv_path, row_no, key, pr.get("csv_col"), csv_val,
                                from_file, pr.get("json_field"), json_val))

    # ⑤ require_same_row_set：真源键未登记 ⇒ 逐条报
    if same_row_set:
        for key in sorted(index):
            if key not in csv_keys:
                probs.append("[%s] 真源判据未登记（require_same_row_set）：%r（来源 %s）"
                             % (cid, key, index[key][1]))
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
    "path_exists": prim_path_exists,
    "csv_col_eq_json_by_key": prim_csv_col_eq_json_by_key,
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


# ★ 2026-10-05（`N-053` 第 ③ 步）：原 `_ledger_vs_forms()` 兜底（上文「第 6 例」）**连同
#   `LEDGER`/`LEDGER_COLS` 常量一并删除** —— 其四条语义已**原样搬进 `checks.json#S26`（第 12 原语
#   `csv_col_eq_json_by_key`）** ⇒ ★ 该判定不再有「两条来源」，与本文件「不自持判据」的定位一致。
#   ★ 等价性由 `scripts/_probe_n053.py` 实测：修复前台账（`ef532dc`）⇒ `S26` 逐条报出 **32 处**，
#   与迁移前兜底在同一夹具上的 **32 处逐项对应**；★ 并做缺口存在性反证（摘掉 `S26` ⇒ 静默放行）。


def _institution_anchors_counts():
    """[META] 制度锚点索引（`spec/institution-anchors.json`）的**计数面 ＋ 指针面**自审 —— `N-049` ①。

    ★ 该索引原本由**仓库外**的一次性原型脚本生成（原 `known_gaps.generator_outside_repo`）
      ⇒ 留下「改了 spec、索引未重生 ⇒ 索引失真」的窗口。★ `S20`（指针可解析）**抓不住**：
      ① 「有新的引用没被索引」（计数面）；② 「已收录条款的**指针列表**被漏更」（指针面）。
    ★ 口径的**唯一来源**＝`scripts/gen_institution_anchors.py` —— 本函数只调它的 `audit()`，
      **不重复实现口径**（否则又会出现「同一判定两条来源」，见本文件头注第 7 例）。
    ★ 缺口反证（本项落盘时实测）：删掉本调用 ⇒ 同一处索引失真（`spec/openapi.json` 的 2 处引用）
      **静默放行**；装回 ⇒ 报出 3 处。★ 指针面同理（见 `scripts/_probe_n049.py` 的 `E4`）。
    """
    here = os.path.dirname(os.path.abspath(__file__))
    if here not in sys.path:
        sys.path.insert(0, here)
    try:
        import gen_institution_anchors as gen
    except Exception as e:                       # fail-closed：导入不了即视为该面**未受保护**
        return ["[META] 无法导入 `scripts/gen_institution_anchors.py` ⇒ 索引失真的窗口**未受保护**：%s" % e]
    try:
        return gen.audit(ROOT)
    except Exception as e:
        return ["[META] 制度锚点索引重算失败（口径见 `scripts/gen_institution_anchors.py` 头注）：%s" % e]


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

    # ★ 台账类自审（`[META]`，非 `checks.json` 判据）：制度锚点索引的**计数面 ＋ 指针面**不得失真
    problems += _institution_anchors_counts()


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
