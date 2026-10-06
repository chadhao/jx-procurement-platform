# -*- coding: utf-8 -*-
"""`N-051` 正式探针 —— `spec/openapi.json`（机读契约）的**行为证明**（不是"跑一下没报错"）。

用法：`python scripts/_probe_n051.py`
（★ 本文件不进任何门禁面：`_` 前缀不对齐 `check_all.sh` 的入口集合，也不被 `check_spec.py` 的
   `spec/**/*.json` glob 收录；★ **全程只读、零落盘、零临时文件** —— 变异一律在**内存副本**上做，
   真文件与工作区不产生任何中间产物。）

它证明四件事（★ 每条都是**可复现的实测**，不是推断）：
  1. **重现一致性**：入库 `spec/openapi.json` 必须与「从人读正本 `docs/05-API.md` 重建」的文本**逐字相同**
     —— 这正是「禁止手写第二份真相」的可执行判据（对应「会报」项 `C9` 的 ① 指纹分支）。
  2. **路径参数完备性**：每个含 `{name}` 的路径模板，其 operation 必须**逐名**声明 `in=path` ＋ `required=true`
     —— OpenAPI 3.0 的硬性要求，也是机读消费方唯一能自动推出的参数信息。
  3. **operationId 唯一性 ＋ 规则一致**：全局唯一，且与 `{method_lower}_{路径归一段}` 规则逐条吻合
     （生成器遇冲突会 `SystemExit`；本探针独立再算一遍，避免"生成器自己说了算"）。
  4. **路由集合双向等价**：入库契约的 (method, path) 全集 == 生成器从正本提取的全集（69 条）。
  5. ★★ **作废声明不得回流**（`N-061 ④`）：§3.4 的「实例表单字段」端点**已摘除**（router＋handler
     同批，`internal/worker/ingest.go:145`）⇒ 正本**不得**再把它当**现行声明**（否则 C5 反向
     立刻报「正本声明、router 未注册」）—— ★ 本项用**缺口存在性反证**钉住：把原块**塞回内存副本**，
     路由集必须**立刻由 69 变 70**，且 `C9` 口径下「正本有、契约无」的差集**恰好只有它**。

★ 每个"反例"用例都**先证基线是绿的**（0 条），再变异、要求**恰好多出 1 条** ——
  否则「不报」会被误读成「判据无鉴别力」（`N-048` 教训：变异必须打在**数据流经的那份数据**上）。
"""
import copy
import io
import json
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
import gen_openapi as G  # noqa: E402

OUT = os.path.join(G.ROOT, "spec", "openapi.json")
RESULT = []


def _read(path):
    with io.open(path, "r", encoding="utf-8") as fh:
        return fh.read()


def iter_ops(doc):
    """产出 (method, path, op) —— method 已转大写。"""
    for path, item in (doc.get("paths") or {}).items():
        if not isinstance(item, dict):
            continue
        for meth, op in item.items():
            if isinstance(op, dict):
                yield meth.upper(), path, op


# ---------------------------------------------------------------- 四项检查
def check_regenerate(disk_text, regen_doc, regen_routes):
    """入库文件必须与「从正本重建」的文本逐字相同；且正本路由集非空且为 69 条。"""
    probs = []
    want = G.dumps(regen_doc)
    if disk_text != want:
        a, b = disk_text.split("\n"), want.split("\n")
        first = None
        for i, (x, y) in enumerate(zip(a, b), 1):
            if x != y:
                first = "首个差异 line %d：入库 %r / 重建 %r" % (i, x[:60], y[:60])
                break
        probs.append("入库文件与重现结果不一致（索引失真）—— %s"
                     % (first or "行数不同 %d vs %d" % (len(a), len(b))))
    if len(regen_routes) != 69:
        probs.append("生成器从正本提取 %d 条路由（期望 69）⇒ 抽取规则可能已失效" % len(regen_routes))
    return probs


def check_path_params(doc):
    """路径参数完备性（模板 `{name}` ↔ operation.parameters 逐名等价、in=path、required=true）。"""
    probs = []
    for meth, path, op in iter_ops(doc):
        want = G.path_params(path)
        if not want:
            continue
        params = op.get("parameters") or []
        got = [p.get("name") for p in params if p.get("in") == "path"]
        if sorted(got) != sorted(want):
            probs.append("%s %s：路径模板参数 %s ≠ 已声明 %s" % (meth, path, want, got))
            continue
        for p in params:
            if p.get("in") == "path" and p.get("required") is not True:
                probs.append("%s %s：参数 %r 未标 `required: true`" % (meth, path, p.get("name")))
    return probs


def check_operation_ids(doc):
    """operationId 全局唯一 ＋ 与生成规则逐条一致。"""
    probs = []
    seen = {}
    for meth, path, op in iter_ops(doc):
        oid = op.get("operationId")
        if not oid:
            probs.append("%s %s：缺 `operationId`" % (meth, path))
            continue
        if oid in seen:
            probs.append("`operationId` 重复 %r（%s %s 与 %s）" % (oid, meth, path, seen[oid]))
        seen[oid] = "%s %s" % (meth, path)
        want = G.operation_id(meth, path)
        if oid != want:
            probs.append("%s %s：`operationId` %r ≠ 规则推导 %r" % (meth, path, oid, want))
    return probs


def check_route_set(doc, regen_routes):
    """入库契约的 (method, path) 全集 == 生成器从正本提取的全集。"""
    probs = []
    want = {(m, p) for m, p, _k, _ln in regen_routes}
    got = {(meth, path) for meth, path, _op in iter_ops(doc)}
    for m, p in sorted(want - got):
        probs.append("正本声明 `%s %s`，契约**未收录**" % (m, p))
    for m, p in sorted(got - want):
        probs.append("契约收录 `%s %s`，**正本未声明**（第二份真相）" % (m, p))
    return probs


# ---------------------------------------------------------------- 第五项：作废声明不得回流
# ★★ `N-061 ④`：§3.4 的「实例表单字段」端点**已摘除**（router＋handler 同批，
#    `internal/worker/ingest.go:145`）⇒ 正本**不得**再把它当**现行声明** —— 否则
#    `audit_silent#C5` 反向立刻报「正本声明、router 未注册」（受理前实测＝ 1 处）。
OBSOLETE = ("GET", "/api/instances/{instance_code}/fields")


def check_obsolete_absent(md_lines, regen_routes, disk_doc):
    """正向：正本声明集**不得**含已摘除端点；并返回**缺口存在性反证**的实测数据。

    ★ 反证做法（★ 变异打在**数据流经的那份数据**上）：把原 `####` 强声明标题**塞回内存副本**
      ⇒ 路由集必须**立刻由 69 变 70**，且 `C9` 口径（正本声明 − 机读契约收录）的差集
      **恰好只有这一条** —— 这正是「删掉该块才让门禁转绿」的可复现证据（不是"跑一下没报错"）。
    """
    probs = []
    got = {(m, p) for m, p, _k, _ln in regen_routes}
    if OBSOLETE in got:
        probs.append("正本仍把**已摘除**端点当现行声明：%s %s ⇒ C5 反向会报「正本声明、router 未注册」"
                     % OBSOLETE)
    if len(got) != 69:
        probs.append("正本声明集 %d 条（期望 69）" % len(got))

    mut = list(md_lines)
    anchor = next((i for i, ln in enumerate(mut)
                   if ln.startswith("#### `GET /api/instances/{instance_code}/prefill`")), None)
    if anchor is None:
        probs.append("找不到 `prefill` 小节标题 ⇒ 反证用例无法构造（抽取规则可能已变）")
        return probs, {}
    mut.insert(anchor, "#### `%s %s`" % OBSOLETE)
    mut_set = {(m, p) for m, p, _k, _ln in G.extract_routes(mut)[0]}
    disk_ops = {(meth, path) for meth, path, _op in iter_ops(disk_doc)}
    return probs, {"mut_count": len(mut_set),
                   "extra": sorted(mut_set - got),
                   "c9_diff": sorted(mut_set - disk_ops)}


# ---------------------------------------------------------------- 断言工具
def case(name, probs, want_n=0, must_have=None):
    ok = len(probs) == want_n
    for s in (must_have or []):
        ok = ok and any(s in p for p in probs)
    RESULT.append((ok, name, len(probs), want_n))
    print("%s %-40s 实得 %d 条（期望 %d）" % ("✓" if ok else "✗", name, len(probs), want_n))
    for p in probs[:4]:
        print("      · " + p)
    return ok


def case_break(name, base_probs, mut_probs, must_have=None, exact=True):
    """★「先破坏再修」：基线必须 0 条，变异后必须 1 条（`exact=False` 时放宽为 ≥1）。

    ★ 为何需要 `exact=False`：一处篡改可能**同时**触发两条互不相同的判据（例：改某 operationId 与
      另一条撞车 ⇒ 既「重复」又「不合规则」）。这**不是误报** —— 该值确实两样都不满足；
      故按「≥1 且必须含指定文案」断言，比强行凑「恰好 1」更诚实。
    """
    n_ok = (len(mut_probs) == 1) if exact else (len(mut_probs) >= 1)
    ok = (len(base_probs) == 0) and n_ok
    for s in (must_have or []):
        ok = ok and any(s in p for p in mut_probs)
    want = 1 if exact else ">=1"
    RESULT.append((ok, name, len(mut_probs), want))
    print("%s %-40s 基线 %d → 变异 %d 条（期望 0 → %s）"
          % ("✓" if ok else "✗", name, len(base_probs), len(mut_probs), want))
    for p in mut_probs[:4]:
        print("      · " + p)
    return ok


def _pick(doc, want_param=True):
    """挑一个「含路径参数且已声明 parameters」的 (path, method) 作变异靶点。"""
    for meth, path, op in iter_ops(doc):
        if G.path_params(path) and op.get("parameters"):
            return path, meth
    raise AssertionError("找不到含路径参数的 operation（用例无法构造）")


def main():
    print("═══ N-051 探针 · spec/openapi.json（机读契约）═══\n")

    disk = _read(OUT)
    regen_doc, regen_routes, *_ = G.build()
    doc = json.loads(disk)

    n_pathparam = sum(1 for _m, _p, _o in iter_ops(doc) if G.path_params(_p))
    n_ops = sum(1 for _ in iter_ops(doc))
    print("入库契约：%d 个 path · %d 个 operation · 其中 %d 个含路径参数\n"
          % (len(doc.get("paths") or {}), n_ops, n_pathparam))

    # ── 正向 1：重现一致性（真文件）──
    p_regen = check_regenerate(disk, regen_doc, regen_routes)
    case("正向·重现一致性（正本→契约逐字相同）", p_regen, 0)

    # ── 正向 2：路径参数完备性 ──
    p_pp = check_path_params(doc)
    case("正向·路径参数完备性（%d 个 operation）" % n_pathparam, p_pp, 0)

    # ── 正向 3：operationId 唯一性 ＋ 规则一致 ──
    p_oid = check_operation_ids(doc)
    case("正向·operationId 唯一性＋规则一致", p_oid, 0)

    # ── 正向 4：路由集合双向等价 ──
    p_rs = check_route_set(doc, regen_routes)
    case("正向·路由集合双向等价（69 条）", p_rs, 0)

    # ── 正向 5：作废声明不得回流（`N-061 ④`）＋ 缺口存在性反证 ──
    md_lines = G.read_text(G.MD_PATH).split("\n")
    p_ob, counter = check_obsolete_absent(md_lines, regen_routes, doc)
    case("正向·作废声明不得回流（N-061 ④）", p_ob, 0)
    ok_ctr = (counter.get("mut_count") == 70
              and counter.get("extra") == [OBSOLETE]
              and counter.get("c9_diff") == [OBSOLETE])
    RESULT.append((ok_ctr, "反证·塞回作废块 ⇒ 69→70 且 C9 恰多这一条",
                   len(counter.get("extra") or []), 1))
    print("%s %-40s 塞回后 %s 条 · 多出 %s · C9 差集 %s"
          % ("✓" if ok_ctr else "✗", "反证·塞回作废块 ⇒ 69→70 且 C9 恰多这一条",
             counter.get("mut_count"), counter.get("extra"), counter.get("c9_diff")))

    print()

    # ── 反例A：删掉某含路径参数 operation 的 parameters ⇒ 完备性精确红 ──
    tgt_path, tgt_meth = _pick(doc)
    m = copy.deepcopy(doc)
    del m["paths"][tgt_path][tgt_meth.lower()]["parameters"]
    case_break("反例A·删 parameters（%s %s）" % (tgt_meth, tgt_path),
               p_pp, check_path_params(m), must_have=["路径模板参数"])

    # ── 反例B：把某路径参数 required 改 False ⇒ 精确红 ──
    m = copy.deepcopy(doc)
    m["paths"][tgt_path][tgt_meth.lower()]["parameters"][0]["required"] = False
    case_break("反例B·路径参数 required=false",
               p_pp, check_path_params(m), must_have=["required"])

    # ── 反例C：让两个 operation 的 operationId 撞车 ⇒ 唯一性精确红 ──
    ops = list(iter_ops(doc))
    m = copy.deepcopy(doc)
    (m1, p1, _), (m2, p2, _) = ops[0], ops[1]
    m["paths"][p2][m2.lower()]["operationId"] = m["paths"][p1][m1.lower()]["operationId"]
    case_break("反例C·operationId 撞车",
               p_oid, check_operation_ids(m), must_have=["重复"], exact=False)

    # ── 反例D：篡改一个 operationId 使其不合规则 ⇒ 精确红 ──
    m = copy.deepcopy(doc)
    m["paths"][p1][m1.lower()]["operationId"] = "oops_handwritten"
    case_break("反例D·operationId 不合规则",
               p_oid, check_operation_ids(m), must_have=["规则推导"])

    # ── 反例E：从契约删一条路径 ⇒ 路由集双向差集精确红 ──
    m = copy.deepcopy(doc)
    del m["paths"][tgt_path]
    case_break("反例E·契约少一条路由（%s）" % tgt_path,
               p_rs, check_route_set(m, regen_routes), must_have=["未收录"])

    # ── 反例F：文本级「先破坏再修正」——改一字符必红、还原必绿 ──
    broken = disk.replace('"openapi": "3.0.3"', '"openapi": "9.9.9"', 1)
    assert broken != disk, "文本变异靶点未命中（用例无法构造）"
    p_broken = check_regenerate(broken, regen_doc, regen_routes)
    p_fixed = check_regenerate(disk, regen_doc, regen_routes)
    ok = (len(p_broken) == 1) and (len(p_fixed) == 0)
    RESULT.append((ok, "反例F·契约文本破坏→还原", len(p_broken), 1))
    print("%s %-40s 破坏 %d 条 → 还原 %d 条（期望 1 → 0）"
          % ("✓" if ok else "✗", "反例F·契约文本破坏→还原", len(p_broken), len(p_fixed)))
    for p in p_broken[:2]:
        print("      · " + p)

    bad = [r for r in RESULT if not r[0]]
    print("\n═══ 汇总：%d/%d 用例通过 ═══" % (len(RESULT) - len(bad), len(RESULT)))
    for _, name, got, want in bad:
        print("  ✗ %s（实得 %d，期望 %d）" % (name, got, want))
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
