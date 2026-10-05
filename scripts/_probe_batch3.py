#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
独立复核（WorkBuddy 侧）：**篡改 spec 文件 ⇒ 相关测试必须随之失败**。

★ 为什么必须做这一条（mimo 的探针没覆盖）：
  mimo 的探针改的是**内存里的 bundle 副本**，只证明了「访问器没写死常量」，
  **没证明「值是从 spec/*.json 文件读来的」** —— 若加载层从别处取值（或曾在别处硬编码），
  它的探针**照样全绿**。⇒ 补这一环：**改文件 ⇒ 行为必须变**。

★ 纪律（沿用既有做法）：
  · **全量备份 spec/**（不写"备份清单"，会漏）；
  · **哈希校验还原**（prune 多余 → 覆盖写回 → sha256 逐文件比对 → 重试）；
  · 每条探针断言**必须失败**；**控制组必须通过**；还原后必须再通过。
"""
import hashlib
import json
import pathlib
import re
import shutil
import tempfile
from pathlib import Path
import subprocess
import sys
import time

REPO = pathlib.Path(__file__).resolve().parent.parent
SPEC = REPO / "spec"
BACKUP = Path(tempfile.mkdtemp(prefix="probe_spec_"))  # N-060 T3: 系统临时目录（不落仓）

TARGETS = [
    "./internal/httpapi/",
    "./internal/chain/",
    "./cmd/jxapproval/",
]
RUN = ("TestBatchPeriodCutoff|TestParamsProbeChangeValueChangesBehavior|"
       "TestAmountVsPrUsesParamThreshold|TestCTRouteTwoLevel|"
       "TestPaymentRoutePriorities|TestPaymentRouteRuleAnchor|"
       "TestUnregisteredWritableLedgerFields")

results = []


def snap(root: pathlib.Path):
    out = {}
    for f in sorted(root.rglob("*")):
        if f.is_file():
            out[str(f.relative_to(root)).replace("\\", "/")] = hashlib.sha256(f.read_bytes()).hexdigest()
    return out


BACKUP_SNAP = None


def restore():
    assert BACKUP_SNAP is not None
    last = None
    for attempt in range(5):
        try:
            if SPEC.exists():
                for p in sorted(SPEC.rglob("*"), reverse=True):
                    rel = p.relative_to(SPEC)
                    if not (BACKUP / rel).exists():
                        if p.is_dir():
                            shutil.rmtree(p, ignore_errors=True)
                        else:
                            p.unlink(missing_ok=True)
            shutil.copytree(BACKUP, SPEC, dirs_exist_ok=True)
            if snap(SPEC) == BACKUP_SNAP:
                return
            last = "内容与备份不一致"
        except OSError as e:
            last = str(e)
        time.sleep(0.25 * (attempt + 1))
    raise RuntimeError("★ 还原失败：%s —— 请立即 git checkout -- spec/" % last)


def run_tests():
    p = subprocess.run(["go", "test"] + TARGETS + ["-count=1", "-run", RUN],
                       cwd=str(REPO), capture_output=True, text=True,
                       encoding="utf-8", errors="replace", timeout=600)
    return p.returncode, (p.stdout or "") + (p.stderr or "")


def load(p):
    return json.loads(p.read_text(encoding="utf-8"))


def save(p, o):
    p.write_text(json.dumps(o, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def probe(name, mutate, expect_fail=True):
    try:
        mutate()
        rc, out = run_tests()
        ok = (rc != 0) if expect_fail else (rc == 0)
        tail = [l for l in out.splitlines() if l.startswith(("--- FAIL", "ok ", "FAIL", "---"))]
        results.append((name, ok, "rc=%d %s" % (rc, ("；".join(tail[:3]))[:150])))
    finally:
        restore()


# ---------- 准备 ----------
shutil.copytree(SPEC, BACKUP, dirs_exist_ok=True)  # mkdtemp 空目录（无需先清；避免批量 rmtree）
BACKUP_SNAP = snap(BACKUP)
print("[备份] 全量 spec/（%d 文件，已取 sha256 指纹）\n" % len(BACKUP_SNAP))

# ---------- P0 控制组：未篡改 ⇒ 必须全过 ----------
rc, out = run_tests()
assert rc == 0, "★ 控制组失败（原状就红）：\n" + out[-2000:]
print("[P0] 控制组（未篡改）: 全过 ✓\n")

try:
    # P1 · 改 params.json 的报销截止日 25 → 31
    def m1():
        f = SPEC / "params.json"
        o = load(f)
        o["params"]["reporting.monthly_cutoff_day"]["value"] = 31
        save(f, o)

    probe("P1 改 params: 报销截止日 25→31", m1)

    # P2 · 改 params.json 的合同容差 10 → 30
    def m2():
        f = SPEC / "params.json"
        o = load(f)
        o["params"]["contract.amount_over_pr_tolerance_percent"]["value"] = 30
        save(f, o)

    probe("P2 改 params: 合同容差 10→30", m2)

    # P3 · 改 chain.json 付款路径规则（把第 1 条优先级挪到第 4 条之后）
    def m3():
        f = SPEC / "chain.json"
        o = load(f)
        d = o["payment_route_rule"]["decisions"]
        d.insert(0, d.pop())          # 原末条提到最前 ⇒ 优先级顺序变
        save(f, o)

    # ★ N-026 修复前：期望值从 spec 派生（routeValue(b,n)）⇒ 改顺序后仍通过（无鉴别力）。
    #   N-026 修复后：mimo 加了**顺序敏感断言** ⇒ 改顺序必须失败。
    #   ★ 同一探针、修复前后行为相反 —— 这正是"修复真实有效"的证据。
    probe("P3 改 chain 优先级顺序（N-026 后应失败）", m3)

    # P4 · ledger-mapping 加一个「已声明可写但未登记」的字段
    def m4():
        f = SPEC / "ledger-mapping.json"
        o = load(f)
        o["ledgers"]["L04"]["fields"].append(
            {"name": "probe_field", "label": "探针未登记列", "writable": True, "writer": "探针"})
        save(f, o)

    # ★ N-026 修复前：真 spec 子测试「不设期望条数」⇒ 改了也不失败。
    #   N-026 修复后：改为断言「应报 7 条 + 无 L10/L11/L12」（N-060 H3.3③：样例补登 L06 三键 ⇒ 10→7，与 seed_t5_test 同批）⇒ 多一条必须失败。
    probe("P4 改 ledger-mapping 加可写未登记列（N-026 后应失败）", m4)

    # P5 · 反向控制：改一个**不该影响**这些测试的东西 ⇒ 必须仍全过
    def m5():
        f = SPEC / "params.json"
        o = load(f)
        o["params"]["reporting.monthly_cutoff_day"]["desc"] += "（探针只改说明文字）"
        save(f, o)

    probe("P5 反向控制：只改说明文字 ⇒ 应仍全过", m5, expect_fail=False)


    # ---------- P6 · ★ 真鉴别力：真 spec 下 T5 的应有输出（Python 独立复现） ----------
    # 为什么要有这条：T5 的 Go 测试在"真实 spec"上**没有断言** ⇒ 没人知道真 spec 下该报几条。
    # 这里用 Python 按同一口径（writable:true 取 label，比对 ledger_field.field_key）独立算一遍，
    # 结果作为**给 mimo 的期望值**，并断言一条关键不变量：**只读台账不得出现**。
    try:
        lm = json.loads((SPEC / "ledger-mapping.json").read_text(encoding="utf-8"))
        cm = json.loads((REPO / "docs/reference/config-mapping.sample.json").read_text(encoding="utf-8"))
        want = {}
        for lt, l in lm["ledgers"].items():
            for f in l["fields"]:
                if f.get("writable"):
                    want.setdefault(lt, []).append(f.get("label") or f["name"])
        have = {(e["ledger_type"], e["field_key"]) for e in cm["ledger_field"]}
        miss = [(lt, lbl) for lt in sorted(want) for lbl in want[lt] if (lt, lbl) not in have]
        readonly_leak = [x for x in miss if x[0] in ("L10", "L11", "L12")]
        ok = (len(miss) == 7) and not readonly_leak
        results.append(("P6 真 spec 下 T5 期望＝7 条且无只读台账", ok,
                        "实报 %d 条 · 只读台账泄漏 %d 条 → %s" % (
                            len(miss), len(readonly_leak),
                            "；".join("%s/%s" % x for x in miss))))
    except Exception as e:  # noqa: BLE001
        results.append(("P6 真 spec 下 T5 期望", False, "计算失败：%s" % e))

finally:
    restore()

rc, out = run_tests()
assert rc == 0, "★ 还原后测试不为绿 —— 探针污染了工作区！\n" + out[-1500:]

print("=" * 96)
print("独立复核结果")
print("=" * 96)
allok = True
for name, ok, detail in results:
    allok = allok and ok
    print("  %-42s %-8s %s" % (name, "如期 ✓" if ok else "★不符✗", detail))
print("-" * 96)
print("还原后复跑: 全过 ✓")
print("总计: %d/%d 条如期" % (sum(1 for r in results if r[1]), len(results)))
if not allok:
    print("★ 有探针不符预期 —— 需人工判断是「实现没读文件」还是「测试自算期望」")
    sys.exit(1)
print("★ 结论：**改 params 文件 ⇒ 行为确实随之变化** ⇒ 参数消费端真的从 spec 读值。")
print("★ N-026 已修复并**经本探针独立验证**：同一探针在修复前「仍通过」、修复后「如期失败」")
print("  ⇒ 两处测试现已具备鉴别力（付款路径顺序敏感 + T5 真 spec 断言 7 条且无只读台账）。")
