#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
探针自证：S5c / S6b 两条新判据 ＋ 4 条 META 护栏的**鉴别力**验证。

铁律（本项目血的教训）：
  - **全量备份 spec/**，不写"备份清单"（曾漏 backup 一个文件 ⇒ 篡改未还原 ⇒ 污染后续探针）
  - 每条探针：篡改 → 跑门禁 → **断言必须报错，且报错里含该判据的真实 ID** → 还原
  - 还原后必须再跑一次 ⇒ **断言必须通过**（证明还原干净）
  - 任一断言失败 ⇒ 立即中止

★ 本轮探针还额外逼出并固化了一条规则：
  **判据 ID 必须由执行器注入（`args["__cid__"]`），原语不得硬编码** ——
  否则同一原语被多条判据复用时，会**报出别人的名字**（首轮探针就撞上了：
  S5c 拦下了违规，报错却写 `[S8/S11]`，断言找 `S5c` 找不到 ⇒ 误判为"未拦下"）。
"""
import json
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
SPEC = REPO / "spec"
BACKUP = Path(tempfile.mkdtemp(prefix="probe_spec_"))  # N-060 T3: 系统临时目录（不落仓）
PY = sys.executable
CHECK = REPO / "scripts" / "check_spec.py"

CHAIN = SPEC / "chain.json"

results = []


def run_gate(cl=None):
    cmd = [PY, str(CHECK)] + ([str(cl)] if cl else [])
    p = subprocess.run(cmd, cwd=str(REPO), capture_output=True, text=True,
                       encoding="utf-8", errors="replace")
    return p.returncode, (p.stdout or "") + (p.stderr or "")


def restore():
    # N-060 T3: 覆盖式还原（篡改只是改文件内容 ⇒ copytree 覆盖即可）；
    # ★ 不 rmtree(SPEC) —— 单次删 >50 文件会被环境 safe-delete 拦截（63 个 spec 文件）。
    shutil.copytree(BACKUP, SPEC, dirs_exist_ok=True)


def probe_spec(name, mutate, expect_id):
    """篡改 spec/chain.json → 断言非零退出且输出含 expect_id"""
    o = json.loads(CHAIN.read_text(encoding="utf-8"))
    mutate(o)
    CHAIN.write_text(json.dumps(o, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    rc, out = run_gate()
    hit = expect_id in out
    ok = (rc != 0) and hit
    results.append((name, ok, "rc=%d 命中%s" % (rc, expect_id if hit else "—")))
    restore()
    return ok


def probe_cl(name, mutate_cl, expect_sub):
    """篡改一份**临时** checks.json（不动 spec/）→ 断言非零退出且输出含 expect_sub"""
    cl = json.loads((SPEC / "checks.json").read_text(encoding="utf-8"))
    mutate_cl(cl)
    with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False,
                                     encoding="utf-8") as fh:
        json.dump(cl, fh, ensure_ascii=False, indent=2)
        tmp = Path(fh.name)
    try:
        rc, out = run_gate(tmp)
    finally:
        tmp.unlink(missing_ok=True)
    hit = expect_sub in out
    ok = (rc != 0) and hit
    results.append((name, ok, "rc=%d 命中「%s」%s" % (rc, expect_sub, "是" if hit else "否")))
    return ok


def probe_gap(name, mutate_spec, drop_ids, forbidden_in_out):
    """
    ★ 缺口存在性反证：篡改 spec ＋ **从临时清单里摘掉指定判据** ⇒ 必须 rc==0（静默放行）。
    断言三件事：
      1. rc == 0（确实"漏"了）
      2. 输出里**没有** forbidden_in_out 里的值（确认不是别的判据报出来的）
      3. 输出是 OK 行（不是崩溃/路径错 —— 否则 rc 非 0 或输出异常）
    """
    o = json.loads(CHAIN.read_text(encoding="utf-8"))
    mutate_spec(o)
    CHAIN.write_text(json.dumps(o, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    try:
        cl = json.loads((SPEC / "checks.json").read_text(encoding="utf-8"))
        cl["checks"] = [c for c in cl["checks"] if c.get("id") not in drop_ids]
        with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False,
                                         encoding="utf-8") as fh:
            json.dump(cl, fh, ensure_ascii=False, indent=2)
            tmp = Path(fh.name)
        try:
            rc, out = run_gate(tmp)
        finally:
            tmp.unlink(missing_ok=True)
    finally:
        restore()
    passed_through = (rc == 0)
    reported = any(x in out for x in forbidden_in_out)
    ok = passed_through and not reported
    results.append((name, ok, "rc=%d 放行=%s 被别的判据报出=%s %s"
                    % (rc, "是" if passed_through else "否",
                       "是" if reported else "否",
                       "" if ok else "← 缺口反证不成立")))
    return ok


# ---------- 准备：全量备份 ----------
shutil.copytree(SPEC, BACKUP, dirs_exist_ok=True)  # BACKUP=mkdtemp 空目录（无需先清）
print("[备份] 全量 spec/ -> %s\n" % BACKUP)

# ---------- P0 控制组：原始必须绿 ----------
rc, out = run_gate()
assert rc == 0, "★ 前置失败：原始状态门禁不为绿\n" + out
print("[P0] 控制组（原始）: 绿 ✓\n")

# ---------- 业务判据探针 ----------
# ★ 整段用 try/finally 包住：**探针中途崩溃也必须还原 spec/**，
#   否则会给仓库留下被篡改的规格（本项目已因此吃过一次亏）。
try:

    def m1(o):
        o["doc_chains"]["BA"]["ledger"] = ["L10"]

    probe_spec("P1  S5c  chain 侧 ledger 指向禁落账目标", m1, "S5c")

    def m2(o):
        o["doc_chains"]["PR"]["route_by_tier"]["purchase_tier2"] = "nonexistent_route"

    probe_spec("P2  S6b  route_by_tier 指向不存在流程线", m2, "S6b")

    # ---------- META 护栏探针（用临时清单，不动 spec/） ----------

    def m5(cl):
        cl["checks"].append(dict(cl["checks"][0]))          # 复制一条 ⇒ id 重复

    probe_cl("P3  META 判据 id 重复必须报错", m5, "id 重复")

    def m6(cl):
        cl["checks"][0]["id"] = ""

    probe_cl("P4  META 判据 id 为空必须报错", m6, "空 id")

    def m7(cl):
        cl["checks"][0]["primitive"] = "no_such_primitive"

    probe_cl("P5  META 引用未实现原语必须报错", m7, "未实现的原语")

    # ---------- P6–P7：★ 缺口存在性反证（最有价值的一组） ----------
    # 把 S5c / S6b **从清单里摘掉**，再打同样的篡改 ⇒ 必须**静默放行（rc=0）**。
    # 只有这样才能证明：① 缺口是**真实存在**的（不是被别的判据顺手挡住）；② 是**该判据**关掉的。
    # ★ 这组探针是本项目"不许声称有效"纪律的直接落实 —— 断言的是「无此判据即漏」，
    #   而前面 P1–P2 断言的是「有此判据即拦」，两者合起来才构成完整证据链。

    def m8(o):
        o["doc_chains"]["BA"]["ledger"] = ["L10"]

    probe_gap("P6  反证：摘掉 S5c 后 L10 应被静默放行", m8, ["S5c"], ["L10"])

    def m9(o):
        o["doc_chains"]["PR"]["route_by_tier"]["purchase_tier2"] = "nonexistent_route"

    probe_gap("P7  反证：摘掉 S6b 后悬空档位路由应被放行", m9, ["S6b"], ["nonexistent_route"])

    # ---------- P8：硬编码 ID 回归守卫（源码自审） ----------
    # 做法：把 check_spec.py 复制到 **scripts/ 下**（★ 必须仍在仓库内 ——
    #       脚本用 `__file__` 推 `ROOT`，放系统 temp 会让它找不到 spec/checks.json 而 rc=2，
    #       那样测到的是"路径错"而不是"守卫有效"），注入一处硬编码 ID 字面量；
    #       自审读 `__file__` ⇒ 审计这份副本 ⇒ 必须报错。跑完必删。
    src = CHECK.read_text(encoding="utf-8")
    needle = 'def prim_json_parse(args):\n    cid, probs, files = _cid(args), [], expand(args["file"])'
    assert needle in src, "★ 探针 P8 失效：找不到注入锚点（源码已变，请更新探针）"
    poisoned = src.replace(needle, needle + '\n    _bogus = "[S99] 硬编码回归样本"', 1)
    pcopy = REPO / "scripts" / "_probe_poisoned_check_spec.py"
    try:
        pcopy.write_text(poisoned, encoding="utf-8")
        p = subprocess.run([PY, str(pcopy)], cwd=str(REPO), capture_output=True,
                           text=True, encoding="utf-8", errors="replace")
        out = (p.stdout or "") + (p.stderr or "")
        hit = "硬编码判据 ID" in out
        ok = (p.returncode != 0) and hit
        results.append(("P8  META 源码硬编码判据 ID 回归守卫", ok,
                        "rc=%d 命中「硬编码判据 ID」%s" % (p.returncode, "是" if hit else "否")))
    finally:
        pcopy.unlink(missing_ok=True)

finally:
    restore()

# ---------- 还原终检 ----------
restore()
rc, out = run_gate()
assert rc == 0, "★ 还原后门禁不为绿 —— 探针污染了工作区！\n" + out

# ---------- 汇总 ----------
print("=" * 82)
print("探针结果")
print("=" * 82)
allok = True
for name, ok, detail in results:
    allok = allok and ok
    print("  %-48s %-8s %s" % (name, "拦下 ✓" if ok else "★未拦下✗", detail))
print("-" * 82)
print("还原后终检: 绿 ✓")
print("总计: %d/%d 条探针具备鉴别力" % (sum(1 for r in results if r[1]), len(results)))
if not allok:
    print("★ 存在无鉴别力的判据 —— 判据形同虚设，必须修")
    sys.exit(1)
print("★ 全部通过：S5c / S6b 与 4 条 META 护栏均能真实拦下违规。")
