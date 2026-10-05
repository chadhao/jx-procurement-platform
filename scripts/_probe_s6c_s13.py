#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
探针自证：`S6c`（N-021 split）· `S13`（N-022 set_covers）的能力与鉴别力。

铁律（本项目血的教训）：
  - **全量备份 spec/**，不写"备份清单"（会漏）⇒ 曾导致篡改未还原、污染后续探针
  - 每条探针：篡改 → 跑门禁 → **断言必须报错，且报错里含该判据的真实 ID** → 还原
  - ★ **两类证据缺一不可**：
      「有它即拦」＝ 制造违规 ⇒ 必须报出该判据 ID；
      「无它即漏」＝ 把该判据**从清单摘掉**后再制造同一违规 ⇒ 必须**静默放行（rc=0）**
                      —— 只有这样才能证明**缺口真实存在、且是被这条判据关掉的**。
  - ★ **两侧引擎一致性**（新增）：同一篡改，**Python 门禁与 Go 加载器必须都拦下** ——
    否则就是「同一份清单，一边绿一边红」，即 `N-011` 要防的漂移。Go 侧用 `go test ./internal/specload/`
    （`spec/` 是 `//go:embed` 内嵌，改动会被测试重新读取）。
  - `try/finally` 保证任何情况下还原 `spec/`。
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
CT = SPEC / "forms" / "CT.json"

results = []


def run_gate(cl=None):
    cmd = [PY, str(CHECK)] + ([str(cl)] if cl else [])
    p = subprocess.run(cmd, cwd=str(REPO), capture_output=True, text=True,
                       encoding="utf-8", errors="replace")
    return p.returncode, (p.stdout or "") + (p.stderr or "")


def run_go_specload():
    """跑 Go 侧 spec 加载器测试（会重新读 spec/，因为 embed 在构建时生效）。"""
    p = subprocess.run(["go", "test", "./internal/specload/", "-count=1", "-run", "TestLoadRealSpec"],
                       cwd=str(REPO), capture_output=True, text=True,
                       encoding="utf-8", errors="replace", timeout=300)
    return p.returncode, (p.stdout or "") + (p.stderr or "")


def _snapshot(root: Path):
    """目录内容指纹：相对路径 → sha256。用于**证明**还原干净，而不是"我以为还原了"。"""
    import hashlib
    out = {}
    for f in sorted(root.rglob("*")):
        if f.is_file():
            out[str(f.relative_to(root)).replace("\\", "/")] = \
                hashlib.sha256(f.read_bytes()).hexdigest()
    return out


BACKUP_SNAP = None


def restore():
    """
    ★ 还原且**可证明**（本轮踩坑后加固）：
      Windows 上 `rmtree` + `copytree` 有竞态（删了目录但句柄未释放 ⇒ `makedirs` 报 WinError 183），
      首版探针因此**中途崩在 restore 上、把 `spec/chain.json` 留成了篡改态**。
      ⇒ 改为「**先删备份里没有的多余项 → 再按备份覆盖写回（`dirs_exist_ok`）→ 哈希校验 → 失败重试**」。
      ★ 关键差别：不依赖"删干净"这个前提，而是**声明式地让最终状态等于备份**，并**逐文件校验**。
    """
    import time
    assert BACKUP_SNAP is not None, "备份指纹未初始化"
    last = None
    for attempt in range(5):
        try:
            # ① prune：备份里没有的（多为探针新增）一律删掉
            if SPEC.exists():
                for p in sorted(SPEC.rglob("*"), reverse=True):
                    rel = p.relative_to(SPEC)
                    if not (BACKUP / rel).exists():
                        if p.is_dir():
                            shutil.rmtree(p, ignore_errors=True)
                        else:
                            p.unlink(missing_ok=True)
            # ② 覆盖写回（同名文件被备份版本覆盖 ⇒ 篡改必被撤销）
            shutil.copytree(BACKUP, SPEC, dirs_exist_ok=True)
            # ③ ★ 哈希校验：必须逐文件等于备份，否则**重试**而不是假装成功
            if _snapshot(SPEC) == BACKUP_SNAP:
                return
            last = "内容与备份不一致"
        except OSError as e:
            last = str(e)
        time.sleep(0.25 * (attempt + 1))
    raise RuntimeError("★ 探针还原失败（污染工作区风险）：%s —— 请立即 `git checkout -- spec/`" % last)


def load(p):
    return json.loads(p.read_text(encoding="utf-8"))


def save(p, o):
    p.write_text(json.dumps(o, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def drop_ids(ids):
    cl = load(SPEC / "checks.json")
    cl["checks"] = [c for c in cl["checks"] if c.get("id") not in ids]
    with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False, encoding="utf-8") as fh:
        json.dump(cl, fh, ensure_ascii=False, indent=2)
        return Path(fh.name)


def probe(name, mutate, expect_id, also_go=False, expect_pass=False):
    """
    mutate(): 篡改 spec/（原地）
    expect_pass=True ⇒ 断言**放行**（用于「无它即漏」）
    否则断言：Python 非零且含 expect_id；also_go ⇒ Go 也必须非零。
    """
    mutate()
    try:
        rc, out = run_gate()
        hit = expect_id in out
        py_ok = (rc == 0) if expect_pass else ((rc != 0) and hit)
        detail = "py rc=%d 命中%s=%s" % (rc, expect_id, "是" if hit else "否")
        if also_go and not expect_pass:
            grc, gout = run_go_specload()
            go_ok = (grc != 0)
            detail += " · go rc=%d %s" % (grc, "拦下" if go_ok else "★未拦下")
            ok = py_ok and go_ok
        else:
            ok = py_ok
    finally:
        restore()
    results.append((name, ok, detail))
    return ok


# ---------- 准备 ----------
shutil.copytree(SPEC, BACKUP, dirs_exist_ok=True)  # mkdtemp 空目录（无需先清；避免批量 rmtree）
BACKUP_SNAP = _snapshot(BACKUP)
print("[备份] 全量 spec/ -> %s（%d 个文件，已取哈希指纹）\n" % (BACKUP, len(BACKUP_SNAP)))

msgs = []

# ---------- P0 控制组 ----------
rc, out = run_gate()
assert rc == 0, "★ 前置失败：原始状态门禁不为绿\n" + out
print("[P0] 控制组（原始）: 绿 ✓\n")

try:
    # ================= S6c（N-021 split） =================

    # P1 「有它即拦」：管道串含坏段
    def m1():
        o = load(CHAIN)
        o["doc_chains"]["SA"]["route_by_condition"] = "expense_sales | nonexistent_route"
        save(CHAIN, o)

    probe("P1  S6c 有它即拦：管道串含坏段", m1, "S6c", also_go=True)

    # P2 「无它即漏」：摘掉 S6c 后同一篡改必须放行
    def m2():
        m1()

    tmp = drop_ids(["S6c"])
    try:
        m2()
        rc, out = run_gate(tmp)
        passed, hit = (rc == 0), ("nonexistent_route" in out)
        results.append(("P2  S6c 无它即漏：摘掉后同一篡改应放行", passed and not hit,
                        "rc=%d 放行=%s 被别的判据报出=%s" % (rc, "是" if passed else "否", "是" if hit else "否")))
    finally:
        tmp.unlink(missing_ok=True)
        restore()

    # P3 反向：**合法**管道串必须通过（证明不是"一律报错"）
    def m3():
        o = load(CHAIN)
        # 原文三段全部存在 ⇒ 不应报 S6c
        o["doc_chains"]["SA"]["route_by_condition"] = "expense_sales | expense_mgmt_advance | expense_mgmt_direct"
        save(CHAIN, o)

    m3()
    try:
        rc, out = run_gate()
        ok = (rc == 0)
        results.append(("P3  S6c 反向：合法管道串应通过", ok, "rc=%d" % rc))
    finally:
        restore()

    # ================= S13（N-022 set_covers） =================

    # P4 「有它即拦」：把第 5 组的组号改成 9 ⇒ 集合缺 5
    def m4():
        o = load(CT)
        n = 0
        for s in o["sections"]:
            for f in s["fields"]:
                if f.get("clause_group") == 5:
                    f["clause_group"] = 9
                    n += 1
        assert n > 0, "找不到 clause_group==5 的字段"
        save(CT, o)

    probe("P4  S13 有它即拦：缺第 5 组", m4, "S13", also_go=True)

    # P5 「无它即漏」
    def m5():
        m4()

    tmp = drop_ids(["S13"])
    try:
        m5()
        rc, out = run_gate(tmp)
        passed = (rc == 0)
        results.append(("P5  S13 无它即漏：摘掉后同一篡改应放行", passed, "rc=%d 放行=%s" % (rc, "是" if passed else "否")))
    finally:
        tmp.unlink(missing_ok=True)
        restore()

    # P6 min_hits 护栏：把 clause_group 改名 ⇒ collect 命中 0 ⇒ 必须报（不许静默通过）
    def m6():
        o = load(CT)
        n = 0
        for s in o["sections"]:
            for f in s["fields"]:
                if "clause_group" in f:
                    f["clauseGroup"] = f.pop("clause_group")
                    n += 1
        assert n > 0
        save(CT, o)

    probe("P6  S13 min_hits：collect 命中 0 必须报", m6, "S13")

    # P7 非标量跳过：clause_group 变成 dict ⇒ 跳过不崩，但集合缺 1..8 ⇒ 必须报
    def m7():
        o = load(CT)
        for s in o["sections"]:
            for f in s["fields"]:
                if "clause_group" in f:
                    f["clause_group"] = {"x": 1}
        save(CT, o)

    probe("P7  S13 非标量应跳过（不崩），集合缺项必须报", m7, "S13")

    # P8 反向：原样必须通过（证明不是"一律报错"）
    rc, out = run_gate()
    results.append(("P8  S13 反向：原始 CT 八组齐应通过", rc == 0, "rc=%d" % rc))

finally:
    restore()

# ---------- 还原终检 ----------
rc, out = run_gate()
assert rc == 0, "★ 还原后门禁不为绿 —— 探针污染了工作区！\n" + out

# ---------- 汇总 ----------
print("=" * 92)
print("探针结果")
print("=" * 92)
allok = True
for name, ok, detail in results:
    allok = allok and ok
    print("  %-44s %-9s %s" % (name, "拦下 ✓" if ok else "★未拦下✗", detail))
print("-" * 92)
print("还原后终检: 绿 ✓")
print("总计: %d/%d 条探针具备鉴别力" % (sum(1 for r in results if r[1]), len(results)))
if not allok:
    print("★ 存在无鉴别力的判据 —— 判据形同虚设，必须修")
    sys.exit(1)
print("★ 全部通过：S6c / S13 均能真实拦下违规；且（P1/P4）**Python 与 Go 两侧引擎一致拦下**。")
