# -*- coding: utf-8 -*-
"""`N-049` ① 探针 —— 制度锚点索引门禁（**计数面 ＋ 指针面**）的行为证明（不是「跑一下没报错」）。

用法：`python scripts/_probe_n049.py`

★ 本文件不进任何门禁面（`_` 前缀不对齐 `check_spec.py` / `check_all.sh` 入口）；
★ **不改动任何真实文件的最终状态**：每处变异前先把原字节读进内存，跑完立刻写回并 `sha256` 核对。

它证明六件事（★ 每条都是可复现实测，不是推断）：

  1. **正向**：当前树（索引 V1.3）⇒ `gen_institution_anchors.py --check` rc=0，
     且 `check_spec.py` **零**「锚点索引失真」违规。
  2. ★★ **等价性（重算口径可信）**：拿 **V1.1 索引**（`git show 93b3f71:…`）与
     **排除批 7 生成物 `spec/openapi.json`** 的重算逐项比较 ⇒ **28/28 条款计数全等**
     ⇒ ★ 证明「每字符串每条款计 1 处」的口径**不是猜的**。
  3. ★★★ **旧口径的「选择规则」已复现（本批的核心命题）**：拿 **V1.2 索引**（`git show e3c5bce:…`）
     与「**稳定引用按 `(kind_rank, at)` 排序取前 8**」比对 ⇒ **28/28 条款逐项吻合**
     （+ `第二条` 一条已登记例外：「有计数、无指针」）⇒ ★ 说明原型的规则**可从旧产物反推**，
     **不是不可复现的黑箱**。
  4. **鉴别力（六处单点变异，一次只动一处）**：
     · `E1` 把某条款的 `citation_count` +1 ⇒ **恰 1 处**且为 `E1`；
     · `E2` 只去掉 `citation_by_file` 的一行（总计数不变）⇒ **恰 1 处**且为 `E2`（★ 与 `E1` 隔离）；
     · `E3` 只在 spec 侧新增一处引用 ⇒ **恰 1 处**且为 `E3`（★ `S20` **抓不到**的那一类）；
     · ★ `E4` 只删索引里的一条**指针** ⇒ **恰 1 处**且为 `E4`；
     · ★ `E5` 只把 `unstable_count` +1 ⇒ **恰 1 处**且为 `E5`；
     · ★ `E6` 只往索引塞一条 **spec 已不引用的陈旧条款** ⇒ **恰 1 处**且为 `E6`。
  5. ★★★ **`E4` 关掉的正是「`[:8]` 上限」那个洞（缺口存在性正证）**：把 `第三十五条` 的
     **第 9 条**指针从索引里删掉 ⇒ **本版报 `E4`**；★★ 而该指针**在 V1.2 的口径下根本不在索引里**
     （被 `[:8]` 丢掉）⇒ 旧口径**连可比对的对象都没有** ⇒ 这正是本批关闭的窗口。
  6. ★★ **缺口存在性反证**：同一处 `E4` 失真 —— **有自审调用 ⇒ rc=1**；
     **把 `_institution_anchors_counts()` 调用摘掉 ⇒ rc=0（静默放行）**。
  7. **fail-closed**：索引**不可解析** ⇒ 报可见失败（**不许静默通过** —— 否则「检查不了」
     会被当成「没问题」）。

★ 口径正本 ＝ `scripts/gen_institution_anchors.py` 头注；本探针**不重复实现口径**，只调用它。
"""
import hashlib
import io
import json
import os
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
import gen_institution_anchors as G  # noqa: E402

ROOT = G.REPO
INDEX = os.path.join(ROOT, "spec", "institution-anchors.json")
CHECK_SPEC = os.path.join(HERE, "check_spec.py")
GEN = os.path.join(HERE, "gen_institution_anchors.py")
PRE_V11 = "93b3f71"             # 索引 V1.1 所在提交（纳版前）
PRE_V12 = "e3c5bce"             # 索引 V1.2 所在提交（本批改写前的最后一版）
KIND_RANK = G.KIND_RANK
CAP_OLD = 8                     # 旧口径的 `[:8]` 上限
RESULT = []


def sha(path):
    with open(path, "rb") as fh:
        return hashlib.sha256(fh.read()).hexdigest()


def ok(name, cond, detail=""):
    RESULT.append((bool(cond), name, detail))
    print("%s %s%s" % ("✓" if cond else "✗", name, ("  —— " + detail) if detail else ""))


def read(path):
    with io.open(path, "rb") as fh:
        return fh.read()


def write(path, raw):
    with io.open(path, "wb") as fh:
        fh.write(raw)


def run_gen_check():
    p = subprocess.run([sys.executable, GEN, "--check"], cwd=ROOT, capture_output=True, text=True)
    return p.returncode, (p.stdout + p.stderr)


def run_check_spec():
    p = subprocess.run([sys.executable, CHECK_SPEC], cwd=ROOT, capture_output=True, text=True)
    out = p.stdout + p.stderr
    anchor = [ln.strip() for ln in out.splitlines()
              if "[META]" in ln and ("锚点索引" in ln or "计数失真" in ln)]
    return p.returncode, out, anchor


def drift_codes(anchor_lines):
    """从报错行里抽 `[E1]`…`[E6]` 码。"""
    codes = []
    for ln in anchor_lines:
        for c in ("E1", "E2", "E3", "E4", "E5", "E6"):
            if "[%s]" % c in ln:
                codes.append(c)
    return codes


def with_mutation(path, new_bytes, fn):
    """执行 fn，然后**无条件**还原原字节并核对 sha256。"""
    orig = read(path)
    before = sha(path)
    try:
        write(path, new_bytes)
        return fn()
    finally:
        write(path, orig)
        after = sha(path)
        ok("还原 %s（sha256 逐字节相同）" % os.path.relpath(path, ROOT).replace("\\", "/"),
           before == after, "%s…" % after[:16])


def git_show_index(rev):
    p = subprocess.run(["git", "show", "%s:spec/institution-anchors.json" % rev],
                       cwd=ROOT, capture_output=True, text=True, check=True)
    return json.loads(p.stdout)


def mutate_index_bytes(fn):
    """以**规范化序列化**（与仓库内形态逐字节一致）产出变异后的索引字节。"""
    doc = json.loads(read(INDEX).decode("utf-8"))
    fn(doc)
    return (json.dumps(doc, ensure_ascii=False, indent=2) + "\n").encode("utf-8")


def clause(doc, name):
    for c in doc["clauses"]:
        if c["clause"] == name:
            return c
    raise AssertionError("索引里没有条款 %s" % name)


# ------------------------------------------------------------------ 1. 正向
print("== 1. 正向（索引 V1.3）==")
rc, out = run_gen_check()
ok("指针面＋计数面重算与索引一致（gen --check rc=0）", rc == 0,
   out.strip().splitlines()[-1] if out.strip() else "")
rc2, out2, anchor = run_check_spec()
ok("门禁 check_spec rc=0 且零锚点索引违规", rc2 == 0 and not anchor, "anchor=%d 条" % len(anchor))
idx_cur = json.loads(read(INDEX).decode("utf-8"))
ok("索引 V1.3：spec[] 共 304 条 · unstable_count 合计 22",
   sum(len(c["spec"]) for c in idx_cur["clauses"]) == 304
   and sum(c["unstable_count"] for c in idx_cur["clauses"]) == 22,
   "spec[]=%d unstable=%d" % (sum(len(c["spec"]) for c in idx_cur["clauses"]),
                              sum(c["unstable_count"] for c in idx_cur["clauses"])))
ok("★ 不变式逐条款成立：len(spec[]) + unstable_count == citation_count",
   all(len(c["spec"]) + c["unstable_count"] == c["citation_count"] for c in idx_cur["clauses"]))

# ------------------------------------------------------------------ 2. 等价性（计数口径）
print("\n== 2. 等价性（计数口径可信：V1.1 索引 vs 排除生成物后的重算）==")
try:
    old = git_show_index(PRE_V11)
    counts, by_file = G.scan(ROOT, exclude=("spec/openapi.json",))
    bad = []
    for c in old["clauses"]:
        cl = c["clause"]
        if counts.get(cl, 0) != c["citation_count"]:
            bad.append("%s 计数 %d≠%d" % (cl, counts.get(cl, 0), c["citation_count"]))
        elif dict(sorted(by_file[cl].items())) != dict(sorted(c["citation_by_file"].items())):
            bad.append("%s 逐文件计数不等" % cl)
    ok("V1.1 的 %d 条款在「排除 spec/openapi.json」后与重算**逐项全等**" % len(old["clauses"]),
       not bad, "；".join(bad) if bad else "★ 28/28 条款 · citation_count · citation_by_file 全等")
except Exception as e:                                        # 取不到旧版本不算通过
    ok("等价性（需 `git show %s:…`）" % PRE_V11, False, str(e))

# ------------------------------------------------------------------ 3. 旧口径「选择规则」复现
print("\n== 3. ★★ 旧口径的选择规则已复现（V1.2 索引 ≡「稳定引用按 (kind_rank, at) 排序取前 8」）==")
try:
    v12 = git_show_index(PRE_V12)
    exp = G.expected(ROOT)
    match, exc, mism = 0, [], []
    for c in v12["clauses"]:
        cl = c["clause"]
        e = exp.get(cl)
        if e is None:
            mism.append("%s（spec 已无引用）" % cl)
            continue
        want = e["spec"][:CAP_OLD]
        got = [{"at": s["at"], "kind": s.get("kind")} for s in c["spec"]]
        if got == want:
            match += 1
        elif not got and want:                 # ★ 已登记的例外：有计数、无指针
            exc.append("%s（索引 0 条 / 规则期望 %d 条）" % (cl, len(want)))
        else:
            mism.append("%s（索引 %d 条 / 规则 %d 条）" % (cl, len(got), len(want)))
    ok("V1.2 的 spec[] 与「排序后取前 8」**逐项吻合**（%d/%d）" % (match, len(v12["clauses"])),
       not mism, ("；".join(mism) if mism else "★ 例外仅：%s" % "；".join(exc)))
except Exception as e:
    ok("规则复现（需 `git show %s:…`）" % PRE_V12, False, str(e))

# ------------------------------------------------------------------ 4. 单点变异
print("\n== 4. 鉴别力（六处单点变异 · 一次只动一处）==")

# E1：只动 citation_count
m = mutate_index_bytes(lambda d: clause(d, "第五十二条").__setitem__(
    "citation_count", clause(d, "第五十二条")["citation_count"] + 1))
rc, _, anchor = with_mutation(INDEX, m, run_check_spec)
codes = drift_codes(anchor)
ok("E1：`citation_count` +1 ⇒ **恰 1 处**违规且为 E1", rc == 1 and codes == ["E1"],
   "rc=%d codes=%s" % (rc, codes))

# E2：只动 citation_by_file（去掉一个文件；总计数不变 ⇒ E1 不报）
def _e2(d):
    cbf = clause(d, "第五十二条")["citation_by_file"]
    keys = sorted(cbf)
    assert len(keys) > 1, "该条款的 citation_by_file 至少两个文件才可做本变异"
    del cbf[keys[-1]]
m = mutate_index_bytes(_e2)
rc, _, anchor = with_mutation(INDEX, m, run_check_spec)
codes = drift_codes(anchor)
ok("E2：只改逐文件计数 ⇒ **恰 1 处**违规且为 E2（E1 不报 ⇒ 两类报错隔离）",
   rc == 1 and codes == ["E2"], "rc=%d codes=%s" % (rc, codes))

# E3：只在 spec 侧新增引用（`第七十二条`，索引无该条款）
GR = os.path.join(ROOT, "spec", "forms", "GR.json")
gr_raw = read(GR)
needle = b'"institution": "'
assert gr_raw.count(needle) == 1
m_e3 = gr_raw.replace(needle, needle + "（另见第七十二条）".encode("utf-8"), 1)
assert m_e3 != gr_raw
rc, _, anchor = with_mutation(GR, m_e3, run_check_spec)
codes = drift_codes(anchor)
ok("E3：spec 侧新增「第七十二条」引用 ⇒ **恰 1 处**违规且为 E3（`S20` 抓不到的那一类）",
   rc == 1 and codes == ["E3"], "rc=%d codes=%s" % (rc, codes))

# E4：只删索引里的一条指针（取 `第三十五条` 的第 9 条 —— ★ 正是旧 `[:8]` 丢掉的第一条）
CAP_HOLE = "spec/forms/CT.json#sections[id=clauses].fields[name=breach_liability].origin_ref"
c35 = [c for c in idx_cur["clauses"] if c["clause"] == "第三十五条"][0]
ok("★ `第三十五条` 的第 9 条指针恰为「旧口径被 `[:8]` 丢掉的第一条」",
   len(c35["spec"]) > CAP_OLD and c35["spec"][CAP_OLD]["at"] == CAP_HOLE,
   "spec[%d].at=%s" % (CAP_OLD, c35["spec"][CAP_OLD]["at"] if len(c35["spec"]) > CAP_OLD else "—"))


def _e4(d):
    c = clause(d, "第三十五条")
    c["spec"] = [s for s in c["spec"] if s["at"] != CAP_HOLE]
m_e4 = mutate_index_bytes(_e4)
rc, _, anchor = with_mutation(INDEX, m_e4, run_check_spec)
codes = drift_codes(anchor)
ok("E4：删一条**指针** ⇒ **恰 1 处**违规且为 E4", rc == 1 and codes == ["E4"],
   "rc=%d codes=%s" % (rc, codes))

# E5：只动 unstable_count
m = mutate_index_bytes(lambda d: clause(d, "第三十五条").__setitem__(
    "unstable_count", clause(d, "第三十五条")["unstable_count"] + 1))
rc, _, anchor = with_mutation(INDEX, m, run_check_spec)
codes = drift_codes(anchor)
ok("E5：`unstable_count` +1 ⇒ **恰 1 处**违规且为 E5", rc == 1 and codes == ["E5"],
   "rc=%d codes=%s" % (rc, codes))

# E6：塞一条 spec 已不引用的陈旧条款
m = mutate_index_bytes(lambda d: d["clauses"].append(
    {"clause": "第九十九条", "quoted_phrases": [], "spec": [],
     "citation_count": 0, "citation_by_file": {}, "unstable_count": 0}))
rc, _, anchor = with_mutation(INDEX, m, run_check_spec)
codes = drift_codes(anchor)
ok("E6：索引里塞一条陈旧条款 ⇒ **恰 1 处**违规且为 E6", rc == 1 and codes == ["E6"],
   "rc=%d codes=%s" % (rc, codes))

# ------------------------------------------------------------------ 5. 缺口存在性反证（E4 那一类）
print("\n== 5. 缺口存在性反证（摘掉自审 ⇒ 同一 E4 失真静默放行）==")
call_line = "    problems += _institution_anchors_counts()"
cs_raw = read(CHECK_SPEC)
assert cs_raw.count(call_line.encode("utf-8")) == 1
m_cs = cs_raw.replace(call_line.encode("utf-8"), "    pass  # PROBE: 自审已摘".encode("utf-8"), 1)


def _gap():
    """在 INDEX 已带 `E4` 失真的前提下，观察「有自审 / 无自审」两种结果。"""
    write(CHECK_SPEC, m_cs)
    try:
        rc_off, _, anchor_off = run_check_spec()
    finally:
        write(CHECK_SPEC, cs_raw)
    rc_on, _, anchor_on = run_check_spec()
    return rc_off, anchor_off, rc_on, anchor_on


cs_before = sha(CHECK_SPEC)
rc_off, anchor_off, rc_on, anchor_on = with_mutation(INDEX, m_e4, _gap)
ok("还原 scripts/check_spec.py（sha256 逐字节相同）", sha(CHECK_SPEC) == cs_before)
ok("同一 E4 失真：摘掉自审 ⇒ **rc=0 静默放行**", rc_off == 0 and not anchor_off,
   "rc=%d anchor=%d 条" % (rc_off, len(anchor_off)))
ok("装回自审 ⇒ **rc=1 拦下**（缺口真实存在且由该检查关掉）",
   rc_on == 1 and drift_codes(anchor_on) == ["E4"],
   "rc=%d codes=%s" % (rc_on, drift_codes(anchor_on)))

# ------------------------------------------------------------------ 6. fail-closed
print("\n== 6. fail-closed（索引不可解析 ⇒ 可见失败）==")
rc, _, anchor = with_mutation(INDEX, b"{ this is not json", run_check_spec)
ok("索引不可解析 ⇒ 报可见失败（不许静默通过）",
   rc == 1 and any("锚点索引重算失败" in x for x in anchor),
   "rc=%d anchor=%d 条" % (rc, len(anchor)))

# ------------------------------------------------------------------ 收尾
bad = [r for r in RESULT if not r[0]]
print("\n===== 探针结果：%d/%d 通过 =====" % (len(RESULT) - len(bad), len(RESULT)))
for _, name, detail in bad:
    print("  ✗ %s  %s" % (name, detail))
raise SystemExit(1 if bad else 0)
