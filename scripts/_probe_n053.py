# -*- coding: utf-8 -*-
"""`N-053` 正式探针 —— 第 12 原语 `csv_col_eq_json_by_key` ＋ 判据 `S26` 的**行为证明**
（不是「跑一下没报错」）。

用法：`python scripts/_probe_n053.py`
★ 本文件不进任何门禁面：`_` 前缀不对齐 `check_spec.py` / `check_all.sh` 的入口；
★ 夹具全部落在仓库根 `_probe_n053_tmp/`（`spec/**` 之外）⇒ **不污染 `spec` 机读规格门禁**，跑完即删。

它证明五件事（★ 每条都是**可复现的实测**，不是推断）：

  1. **正向**：用**真 `spec/checks.json#S26` 的 args**（从清单里读出来，不手抄）跑真台账 ⇒ **0 报错**。
  2. ★★ **等价性**：拿**修复前**的台账（`ef532dc` 版本，`git show` 取字节）跑 `S26` ⇒
     **逐条报出 28 处**，且 (判据键, 列) 集合与**迁移前 `_ledger_vs_forms()` 在同一夹具上的实测基线逐项相同**
     （★ N-062 J2 连带：forms 六条 soft `carried_by_kind` pending→submit ⇒ 旧 csv 夹具漂移集 −5＋1＝28；N-026 双边同步、非静默调数） ⇒ ★ 「把兜底升级为正式判据」**没有丢语义**（这是本条最重要的证据）。
  3. **鉴别力（真数据上）**：在**内存里**改真 `forms/BA.json` 一条 `severity` ⇒ **恰 1 条**、且点名该判据
     （★ 不落盘、零副作用）。
  4. ★★ **缺口存在性反证**：同一份合成夹具 —— **有 `S26` ⇒ 红**、**把 `S26` 从清单摘掉 ⇒ 静默放行**
     （★ 只有「有它即拦」＋「无它即漏」两者齐备，才证明缺口真实存在且是该判据关掉的）。
  5. **fail-closed 与五条语义**：CSV 缺失 / `json_glob` 命中 0 文件 / `json_collect` 命中 0 项 /
     键重复 / 键不在真源 / 真源未登记 / 列数不符 / `map` 生效 / 非标量不可比 ⇒ 逐条行为对齐。

★ 语义正本 ＝ `spec/checks.json#primitives.csv_col_eq_json_by_key.desc`；两侧同口径。
"""
import contextlib
import io as _io
import json
import os
import tempfile
import shutil
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
import check_spec as C  # noqa: E402

TMPDIR = os.path.join(C.ROOT, "_probe_n053_tmp")  # N-060: 仓内相对（用例 file 字面依赖）；隔离跑=WT内、手工跑=.gitignore 兜底；★ 不 rmtree（bulk 删被环境拦）
RESULT = []


# --------------------------------------------------------------------- 基础设施
def w(path, text):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w", encoding="utf-8", newline="\n") as fh:
        fh.write(text)
    return path


def s26_args():
    """★ 从真清单里读出 `S26` 的 args（★ 不手抄 —— 手抄会与清单一同漂移）。"""
    doc = C.load_json(C.abs_path("spec/checks.json"))
    hit = [c for c in doc["checks"] if c.get("id") == "S26"]
    assert len(hit) == 1, "清单里 S26 命中 %d 条" % len(hit)
    a = dict(hit[0]["args"])
    assert hit[0]["primitive"] == "csv_col_eq_json_by_key"
    return a


def call(args, cid="PROBE-N053"):
    a = dict(args)
    a["__cid__"] = cid
    C._cache.clear()
    return C.prim_csv_col_eq_json_by_key(a)


def case(name, ok, detail=""):
    RESULT.append((ok, name, detail))
    print("%s %-34s %s" % ("✓" if ok else "✗", name, detail))


def keycol(prob):
    """从报错文案里抽出 (判据键, 列名) —— 用于与基线逐项比对。

    ★ 注意：键联接符是 **`\\x1f`**，而文案用 `%r` 渲染 ⇒ 输出里出现的是**转义写法**
      （反斜杠 ＋ `x1f`，共 6 个字符），不是真控制符 ⇒ **两种形态都要归一**。
      （★ 这正是「先读实现再写断言」的价值：按直觉只 replace 真控制符 ⇒ 32 项全部对不上。）
    """
    seg = prob.split("键 ")[-1]
    k = seg.split(" ")[0].strip("'\"")
    k = k.replace("\\x1f", "#").replace("\x1f", "#")
    col = ""
    if " 列 " in prob:
        col = prob.split(" 列 ")[1].split(" ")[0].strip("'\"")
    return k, col


def main():
    print("═══ N-053 探针 · csv_col_eq_json_by_key（第 12 原语）＋ S26 ═══\n")
    os.makedirs(TMPDIR, exist_ok=True)  # mkdtemp 已建（N-060: 不 rmtree —— 避免 bulk 删除拦截）

    args = s26_args()
    print("从清单读出的 S26.args.file = %s / primitives = %d 个\n"
          % (args["file"], len(C.load_json(C.abs_path("spec/checks.json"))["primitives"])))

    # ── 1 正向：真 spec ────────────────────────────────────────────────
    probs = call(args)
    case("正向·真 spec 全量", len(probs) == 0, "0 报错（实得 %d）" % len(probs))
    for p in probs[:5]:
        print("      · " + p)

    # ── 2 等价性：修复前台账（ef532dc）⇒ 28 处，且与迁移前基线逐项相同 ──
    pre = os.path.join(TMPDIR, "prefix_acceptance.csv")
    with open(pre, "wb") as fh:
        fh.write(subprocess.run(["git", "show", "ef532dc:spec/acceptance.csv"],
                                cwd=C.ROOT, stdout=subprocess.PIPE, check=True).stdout)
    a_pre = dict(args)
    a_pre["file"] = "_probe_n053_tmp/prefix_acceptance.csv"
    got = call(a_pre)
    cols = [keycol(p) for p in got if " 列 " in p]
    got_keys = set([keycol(p) for p in got if " 列 " in p])
    BASE = {
        ("BA#amount_positive", "severity"), ("BA#amount_tier1_only", "severity"),
        ("BA#amount_tier1_only", "carrier_kind"), ("BA#completeness_l2", "severity"),
        ("BA#safety_certificate", "severity"), ("BA#receipt_per_purchase", "severity"),
        ("BA#anti_split_before_disburse", "severity"), ("BA#no_purchaser_field", "severity"),
        ("BA#idempotency_key", "severity"),
        ("PR#amount_positive", "severity"), ("PR#completeness_l2", "severity"),
        ("PR#cross_dept_designation", "severity"), ("PR#safety_branch", "severity"),
        ("PR#safety_branch", "carrier_kind"), ("PR#device_tech_attachment", "severity"),
        ("PR#device_tech_attachment", "carrier_kind"), ("PR#idempotency_key", "severity"),
        ("SA#amount_positive", "severity"), ("SA#completeness_l2", "severity"),
        ("SA#entertain_required", "severity"), ("SA#inspection_basis", "severity"),
        ("SA#counterparty_conditional", "severity"), ("SA#counterparty_conditional", "carrier_kind"), ("SA#cross_month_allocation", "severity"),
        ("SA#cross_month_allocation", "carrier_kind"), ("SA#actual_not_exceed", "severity"),
        ("SA#invoice_must_link", "severity"), ("SA#idempotency_key", "severity"),
    }
    case("等价性·修复前台账 ⇒ 28 处", len(cols) == 28, "逐列报出 %d 条（期望 28）" % len(cols))
    case("等价性·与迁移前基线逐项相同", got_keys == BASE,
         "差集 = %s" % (sorted(got_keys ^ BASE) or "（空）"))
    case("等价性·无越界报错（无未登记/列数）",
         not [p for p in got if "未登记" in p or "列数" in p],
         "%d 条中" % len(got))

    # ── 3 鉴别力（真数据 · 内存变异，零落盘）──────────────────────────
    # ★ 关键：`call()` 会先 `_cache.clear()`（防读到陈旧缓存）⇒ 内存变异必须**不走 call()**，
    #   否则变异当场被清掉、断言恒「实得 0」（★ 本探针第一版即栽在这里 —— 变异打在会被清掉的缓存上）。
    call(args)                                     # 预热 _cache
    k_ba = [k for k in C._cache if os.path.basename(k) == "BA.json"]
    assert len(k_ba) == 1, "缓存里 BA.json 命中 %d" % len(k_ba)
    ba = C._cache[k_ba[0]]
    tgt = [c for c in ba["checks"] if c.get("id") == "amount_positive"][0]
    old_sev = tgt["severity"]
    tgt["severity"] = "soft"                       # ★ 只改这一处（内存，不落盘）
    a2 = dict(args)
    a2["__cid__"] = "PROBE-N053"
    probs = C.prim_csv_col_eq_json_by_key(a2)      # ★ 不清缓存 ⇒ 变异生效
    hit = [p for p in probs if "amount_positive" in p]
    case("鉴别力·真数据单点变异 ⇒ 恰 1 条",
         len(probs) == 1 and len(hit) == 1 and "severity" in hit[0],
         "恰 1 条（实得 %d）：%s" % (len(probs), hit[0][:78] if hit else "（无）"))
    tgt["severity"] = old_sev
    C._cache.clear()
    probs2 = call(args)
    case("鉴别力·还原后复绿", len(probs2) == 0, "0 报错（实得 %d）" % len(probs2))

    # ── 4 缺口存在性反证（合成夹具：有 S26 ⇒ 红；摘掉 S26 ⇒ 静默放行）──
    hdr = "doc_type,check_id,c3,c4,severity,c6,c7,c8,carrier_kind,c10,c11"
    w(os.path.join(TMPDIR, "forms", "AA.json"), json.dumps({
        "checks": [{"id": "c1", "severity": "hard", "carried_by_kind": "code"},
                   {"id": "c2", "severity": "soft", "carried_by_kind": "submit"}]}))
    w(os.path.join(TMPDIR, "forms", "BB.json"), json.dumps({
        "checks": [{"id": "b1", "severity": "soft", "carried_by_kind": "manual"}]}))
    w(os.path.join(TMPDIR, "t_bad.csv"), "\n".join([
        hdr,
        "AA,c1,x,x,soft,y,y,y,code,z,z",       # ★ 篡改：真源 hard、台账写 soft
        "AA,c2,x,x,soft,y,y,y,code,z,z",
        "BB,b1,x,x,soft,y,y,y,manual,z,z"]) + "\n")
    syn = {"file": "_probe_n053_tmp/t_bad.csv", "key_cols": ["doc_type", "check_id"],
           "json_glob": "_probe_n053_tmp/forms/*.json", "json_collect": "checks[*]",
           "json_key": ["$file_stem", "id"],
           "pairs": [{"csv_col": "severity", "json_field": "severity"},
                     {"csv_col": "carrier_kind", "json_field": "carried_by_kind",
                      "map": {"submit": "code"}}],
           "require_same_row_set": True, "csv_cols_expected": 11}
    spec_with = {"version": "1", "primitives": {"csv_col_eq_json_by_key": {"desc": "probe"}},
                 "checks": [{"id": "S26", "severity": "must-green", "desc": "probe",
                             "primitive": "csv_col_eq_json_by_key", "args": syn}]}
    spec_wo = json.loads(json.dumps(spec_with))
    # ★ 不能把 `checks` 清空 —— 那会触发**另一条** fail-closed（`[META] 清单里没有任何 checks`），
    #   于是 rc=1 便不再是「同一篡改被放行」的证据。⇒ 换成一条与本篡改**无关**的惰性判据（绿）。
    spec_wo["checks"] = [{"id": "S1", "severity": "must-green", "desc": "probe",
                          "primitive": "json_parse",
                          "args": {"file": "_probe_n053_tmp/forms/*.json"}}]
    sw = w(os.path.join(TMPDIR, "checks_with.json"), json.dumps(spec_with, ensure_ascii=False))
    sn = w(os.path.join(TMPDIR, "checks_without.json"), json.dumps(spec_wo, ensure_ascii=False))

    def run_check(p):
        buf = _io.StringIO()
        with contextlib.redirect_stdout(buf):
            rc = C.check(p)
        return rc, buf.getvalue()

    rc_w, out_w = run_check(sw)
    rc_n, out_n = run_check(sn)
    case("缺口反证·有 S26 ⇒ 红", rc_w == 1 and "S26" in out_w,
         "rc=%d，报出 S26" % rc_w)
    case("缺口反证·摘掉 S26 ⇒ 静默放行", rc_n == 0,
         "rc=%d（同篡改不再被拦）" % rc_n)

    # ── 5 fail-closed 与五条语义（真 args 派生 + 合成夹具）─────────────
    def n1(**kw):
        a = dict(args)
        a.update(kw)
        return call(a)

    p = n1(file="spec/nope.csv")
    case("fail-closed·CSV 缺失", len(p) == 1 and "不存在" in p[0], p[0][:74])
    p = n1(json_glob="spec/nope/*.json")
    case("fail-closed·glob 命中 0 文件", len(p) == 1 and "命中 0 个文件" in p[0], p[0][:74])
    p = n1(json_collect="nope[*]")
    # ★ 收集面在**每个**匹配文件上各判一次 ⇒ 每张表单 1 条（★ 与 `min_hits` 同族的「逐文件」口径；
    #   ★ 这里**带了文件名**，不像 `min_hits` 的多条同文案）；★★ 且真源索引为空后，逐 CSV 行会
    #   **级联**报「键不在真源」（97 行）—— ★ 这与 Go 侧**同一代码路径**（`srcTotal==0 && len(problems)>0`
    #   ⇒ 不走兜底、继续走 CSV 循环）⇒ **两侧一致，不是分歧**；★ 断言按「逐文件条数 == 表单数」+ 「其余全是级联」来写。
    n_files = len(C.expand("spec/forms/*.json"))
    n0 = [x for x in p if "命中 0 项" in x]
    rest = [x for x in p if "命中 0 项" not in x]
    case("fail-closed·收集面命中 0 项（逐文件＋级联）",
         len(n0) == n_files and bool(rest) and all("键不在真源" in x for x in rest),
         "共 %d 条 ＝ 逐文件 %d（表单数 %d）＋ 级联「键不在真源」%d"
         % (len(p), len(n0), n_files, len(rest)))
    p = n1(key_cols=["doc_type", "nope_col"])
    case("fail-closed·表头缺键列", len(p) == 1 and "缺键列" in p[0], p[0][:74])
    p = n1(pairs=[{"csv_col": "nope_col", "json_field": "severity"}])
    case("fail-closed·表头缺比对列", len(p) == 1 and "缺比对列" in p[0], p[0][:74])
    p = call({})
    case("fail-closed·参数不完整", len(p) == 1 and "参数不完整" in p[0], p[0][:74])

    # 合成夹具上的语义：map 生效 / 键不在真源 / 未登记 / 键重复 / 列数 / 非标量
    def syn_call(**kw):
        s = dict(syn)
        s.update(kw)
        return call(s)

    w(os.path.join(TMPDIR, "t_ok.csv"), "\n".join([
        hdr, "AA,c1,x,x,hard,y,y,y,code,z,z",
        "AA,c2,x,x,soft,y,y,y,code,z,z",
        "BB,b1,x,x,soft,y,y,y,manual,z,z"]) + "\n")
    p = syn_call(file="_probe_n053_tmp/t_ok.csv")
    case("语义①·全等（含 map submit→code）⇒ 绿", len(p) == 0, "0 报错（实得 %d）" % len(p))

    w(os.path.join(TMPDIR, "t_extra.csv"), "\n".join([
        hdr, "AA,c1,x,x,hard,y,y,y,code,z,z",
        "AA,c2,x,x,soft,y,y,y,code,z,z",
        "BB,b1,x,x,soft,y,y,y,manual,z,z",
        "CC,c9,x,x,hard,y,y,y,code,z,z"]) + "\n")
    p = syn_call(file="_probe_n053_tmp/t_extra.csv")
    case("语义②·键不在真源 ⇒ 恰 1 条", len(p) == 1 and "键不在真源" in p[0], p[0][:74])

    w(os.path.join(TMPDIR, "t_less.csv"), "\n".join([
        hdr, "AA,c1,x,x,hard,y,y,y,code,z,z",
        "BB,b1,x,x,soft,y,y,y,manual,z,z"]) + "\n")
    p = syn_call(file="_probe_n053_tmp/t_less.csv")
    case("语义③·真源未登记（same_row_set）⇒ 恰 1 条",
         len(p) == 1 and "未登记" in p[0] and "c2" in p[0], p[0][:74])

    w(os.path.join(TMPDIR, "checks_dup.json"), json.dumps(
        {"version": "1", "primitives": {"csv_col_eq_json_by_key": {"desc": "probe"}},
         "checks": [{"id": "S26", "severity": "must-green", "desc": "probe",
                     "primitive": "csv_col_eq_json_by_key",
                     "args": dict(syn, json_glob="_probe_n053_tmp/dup/*.json")}]},
        ensure_ascii=False))
    w(os.path.join(TMPDIR, "dup", "AA.json"), json.dumps({
        "checks": [{"id": "c1", "severity": "hard", "carried_by_kind": "code"},
                   {"id": "c1", "severity": "hard", "carried_by_kind": "code"}]}))
    rc_d, out_d = run_check(os.path.join(TMPDIR, "checks_dup.json"))
    case("语义·真源键重复 ⇒ 报错（不静默取后者）",
         rc_d == 1 and "键重复" in out_d and "不静默取后者" in out_d,
         "rc=%d" % rc_d)

    w(os.path.join(TMPDIR, "t_cols.csv"), "\n".join([
        hdr, "AA,c1,x,x,hard,y,y,y,code,z",          # ★ 少一列（10 ≠ 11）
        "AA,c2,x,x,soft,y,y,y,code,z,z",
        "BB,b1,x,x,soft,y,y,y,manual,z,z"]) + "\n")
    p = syn_call(file="_probe_n053_tmp/t_cols.csv")
    cols_p = [x for x in p if "列数" in x]
    case("语义⑤·列数不符 ⇒ 恰 1 条（且不连带「未登记」）",
         len(cols_p) == 1 and len([x for x in p if "未登记" in x]) == 0,
         "列数 %d 条 / 未登记 %d 条" % (len(cols_p), len([x for x in p if "未登记" in x])))

    # ★ 真源字段非标量 ⇒ 报「不可比」。★ 夹具的**文件名必须与 CSV 首列同名**（`$file_stem` 参与拼键），
    #   否则键对不上、报的是「键不在真源」（★ 本探针第一版即栽在这里：夹具叫 `forms_non.json` ⇒ 键 `forms_non<c1>`）。
    w(os.path.join(TMPDIR, "nonstd", "AA.json"), json.dumps({
        "checks": [{"id": "c1", "severity": "hard", "carried_by_kind": {"deep": 1}}]}))
    w(os.path.join(TMPDIR, "t_nonstd.csv"), "\n".join([hdr, "AA,c1,x,x,hard,y,y,y,code,z,z"]) + "\n")
    p = syn_call(json_glob="_probe_n053_tmp/nonstd/*.json", file="_probe_n053_tmp/t_nonstd.csv")
    case("语义④·真源字段非标量 ⇒ 恰 1 条「不可比」",
         len(p) == 1 and "不可比" in p[0], p[0][:74] if p else "（无）")

    # ── 收尾 ─────────────────────────────────────────────────────────
    bad = [r for r in RESULT if not r[0]]
    print("\n═══ 汇总：%d/%d 用例通过 ═══" % (len(RESULT) - len(bad), len(RESULT)))
    for _, name, detail in bad:
        print("  ✗ %s —— %s" % (name, detail))
    return 1 if bad else 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    finally:
        # N-060: 不 rmtree(TMPDIR) —— bulk 删除会被环境 safe-delete 拦（temp 留置系统自清）
        C._cache.clear()
