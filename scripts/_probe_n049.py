# -*- coding: utf-8 -*-
"""`N-049` ① 探针 —— 制度锚点索引**计数面门禁**的行为证明（不是「跑一下没报错」）。

用法：`python scripts/_probe_n049.py`

★ 本文件不进任何门禁面（`_` 前缀不对齐 `check_spec.py` / `check_all.sh` 入口）；
★ **不改动任何真实文件的最终状态**：每处变异前先把原字节读进内存，跑完立刻写回并 `sha256` 核对。

它证明五件事（★ 每条都是可复现实测，不是推断）：

  1. **正向**：当前树（索引 V1.2）⇒ `gen_institution_anchors.py --check` rc=0，
     且 `check_spec.py` **零**「计数失真」违规。
  2. ★★ **等价性（重算口径可信）**：拿 **V1.1 索引**（`git show <纳版前提交>:…`）
     与**排除批 7 生成物 `spec/openapi.json`** 的重算逐项比较 ⇒ **全等**
     ⇒ ★ 证明「每字符串每条款计 1 处」的口径**不是猜的**，是能从旧产物反推验证的。
  3. **鉴别力（三处单点变异，一次只动一处）**：
     · `E1` 只把 `第五十二条` 的 `citation_count` 34→33 ⇒ **恰 1 处**且为 `E1`；
     · `E2` 只把 `第五十二条` 的 `citation_by_file` 去掉 `spec/openapi.json` ⇒ **恰 1 处**且为 `E2`
       （★ 总计数仍 34 ＝ 重算值 ⇒ **`E1` 不报** ⇒ 两类报错相互隔离）；
     · `E3` 只在 spec 侧新增一处引用（`第七十二条`）⇒ **恰 1 处**且为 `E3`
       （★★ 这正是 `S20` **抓不到**的那一类：**有新引用没被索引**）。
  4. ★★ **缺口存在性反证**：同一处 `E1` 失真 —— **有自审调用 ⇒ rc=1**；
     **把 `_institution_anchors_counts()` 调用摘掉 ⇒ rc=0（静默放行）**
     （★ 只有「有它即拦」＋「无它即漏」两者齐备，才证明缺口真实存在且是该检查关掉的）。
  5. **fail-closed**：索引**不可解析** ⇒ 报可见失败（**不许静默通过** —— 否则「检查不了」
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
PRE_COMMIT = "93b3f71"          # 纳版前的提交（索引 V1.1 所在）
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
    """从报错行里抽 [E1]/[E2]/[E3] 码。"""
    codes = []
    for ln in anchor_lines:
        for c in ("E1", "E2", "E3"):
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


# ------------------------------------------------------------------ 1. 正向
print("== 1. 正向（索引 V1.2）==")
rc, out = run_gen_check()
ok("计数重算与索引一致（gen --check rc=0）", rc == 0, out.strip().splitlines()[-1] if out.strip() else "")
rc2, out2, anchor = run_check_spec()
ok("门禁 check_spec rc=0 且零计数失真违规", rc2 == 0 and not anchor, "anchor=%d 条" % len(anchor))

# ------------------------------------------------------------------ 2. 等价性
print("\n== 2. 等价性（口径可信：V1.1 索引 vs 排除生成物后的重算）==")
try:
    raw_old = subprocess.run(
        ["git", "show", "%s:spec/institution-anchors.json" % PRE_COMMIT],
        cwd=ROOT, capture_output=True, text=True, check=True).stdout
    old = json.loads(raw_old)
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
    ok("等价性（需 `git show %s:…`）" % PRE_COMMIT, False, str(e))

# ------------------------------------------------------------------ 3. 单点变异
print("\n== 3. 鉴别力（单点变异 · 一次只动一处）==")
idx_raw = read(INDEX)
idx_txt = idx_raw.decode("utf-8")

# E1：只动 citation_count（第五十二条 34 → 33）
m_e1 = idx_txt.replace('"citation_count": 34,', '"citation_count": 33,', 1).encode("utf-8")
assert m_e1 != idx_raw
rc, _, anchor = with_mutation(INDEX, m_e1, run_check_spec)
codes = drift_codes(anchor)
ok("E1：改动 citation_count ⇒ **恰 1 处**违规且为 E1", rc == 1 and codes == ["E1"], "rc=%d codes=%s" % (rc, codes))

# E2：只动 citation_by_file（去掉 spec/openapi.json 一行；总计数不变 ⇒ E1 不报）
m_e2 = idx_txt.replace(
    '        "spec/ledger-mapping.json": 2,\n        "spec/openapi.json": 1\n',
    '        "spec/ledger-mapping.json": 2\n', 1).encode("utf-8")
assert m_e2 != idx_raw
rc, _, anchor = with_mutation(INDEX, m_e2, run_check_spec)
codes = drift_codes(anchor)
ok("E2：只改逐文件计数 ⇒ **恰 1 处**违规且为 E2（E1 不报 ⇒ 两类报错隔离）",
   rc == 1 and codes == ["E2"], "rc=%d codes=%s" % (rc, codes))

# E3：只在 spec 侧新增引用（`第七十二条`，第七十一条已在索引内）
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

# ------------------------------------------------------------------ 4. 缺口存在性反证
print("\n== 4. 缺口存在性反证（摘掉自审 ⇒ 同一失真静默放行）==")
call_line = "    problems += _institution_anchors_counts()"
cs_raw = read(CHECK_SPEC)
assert cs_raw.count(call_line.encode("utf-8")) == 1
m_cs = cs_raw.replace(call_line.encode("utf-8"), "    pass  # PROBE: 自审已摘".encode("utf-8"), 1)


def _gap():
    """在 INDEX 已带 `E1` 失真的前提下，观察「有自审 / 无自审」两种结果。

    ★ 顺序：先摘掉自审跑一次（应静默放行）⇒ 再装回自审跑一次（应拦下）。
    ★ `CHECK_SPEC` 由本函数自行还原，并由外层核对 `sha256`。
    """
    write(CHECK_SPEC, m_cs)
    try:
        rc_off, _, anchor_off = run_check_spec()
    finally:
        write(CHECK_SPEC, cs_raw)
    rc_on, _, anchor_on = run_check_spec()
    return rc_off, anchor_off, rc_on, anchor_on


cs_before = sha(CHECK_SPEC)
rc_off, anchor_off, rc_on, anchor_on = with_mutation(INDEX, m_e1, _gap)
ok("还原 scripts/check_spec.py（sha256 逐字节相同）", sha(CHECK_SPEC) == cs_before)
ok("同一 E1 失真：摘掉自审 ⇒ **rc=0 静默放行**", rc_off == 0 and not anchor_off,
   "rc=%d anchor=%d 条" % (rc_off, len(anchor_off)))
ok("装回自审 ⇒ **rc=1 拦下**（缺口真实存在且由该检查关掉）",
   rc_on == 1 and drift_codes(anchor_on) == ["E1"],
   "rc=%d codes=%s" % (rc_on, drift_codes(anchor_on)))

# ------------------------------------------------------------------ 5. fail-closed
print("\n== 5. fail-closed（索引不可解析 ⇒ 可见失败）==")
rc, _, anchor = with_mutation(INDEX, b"{ this is not json", run_check_spec)
ok("索引不可解析 ⇒ 报可见失败（不许静默通过）", rc == 1 and any("锚点索引计数重算失败" in x for x in anchor),
   "rc=%d anchor=%d 条" % (rc, len(anchor)))

# ------------------------------------------------------------------ 收尾
bad = [r for r in RESULT if not r[0]]
print("\n===== 探针结果：%d/%d 通过 =====" % (len(RESULT) - len(bad), len(RESULT)))
for _, name, detail in bad:
    print("  ✗ %s  %s" % (name, detail))
raise SystemExit(1 if bad else 0)
