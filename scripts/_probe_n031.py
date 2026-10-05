#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""scripts/_probe_n031.py —— 独立验证 mimo 的 `N-031` 探针**具有鉴别力**（「修复前后行为相反」）。

★ 为什么不能只看它绿：`TestAgentDeniedDataDrivenProbe` 自称「字面量实现会放行 future_node ⇒ 本用例必须红」，
  但**这句话本身需要被验证** —— 否则它可能只是"在当前实现下恰好通过"（`N-026` 同族：**期望值与被测对象同源**）。
★ 做法（改文件、不改内存；★ 只临时改**选择逻辑**，改完**逐字节校验还原**）：
  ① 备份 `internal/chain/agent.go`（内存 + 落盘 ＋ `sha256`）；
  ② **把数据驱动的判定临时换回字面量**（`n.ID == "approve_petty_cash" || n.ID == "disburse"`）；
  ③ 断言 `TestAgentDeniedDataDrivenProbe` **必须红**（这就是鉴别力）；
  ④ 同时断言 `TestNodeAllowsAgent` ① 仍然**绿** —— ★ 证明红的是「探针」，不是「spec 标记丢了」；
  ⑤ 还原 ＋ `sha256` 逐字节校验；⑥ 反向控制：还原后两条都必须复绿。

用法：python scripts/_probe_n031.py     退出码 0 = 全部如期；1 = 有项不符
"""
import hashlib
import pathlib
import subprocess
import tempfile
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
TARGET = ROOT / "internal" / "chain" / "agent.go"
BAK = pathlib.Path(tempfile.gettempdir()) / "_probe_n031_agent.go"  # N-060 T3: 不落仓

DRIVEN = "if n.AgentAllowed != nil && !*n.AgentAllowed {"
LITERAL = 'if n.ID == "approve_petty_cash" || n.ID == "disburse" {'

results = []


def sh(cmd):
    p = subprocess.run(cmd, cwd=str(ROOT), capture_output=True, text=True)
    return p.returncode, (p.stdout or "") + (p.stderr or "")


def record(name, ok, detail=""):
    results.append((name, ok))
    print(("  ✓ " if ok else "  ✗ ") + name + (f"   {detail}" if detail else ""))


def main() -> int:
    orig = TARGET.read_bytes()
    digest = hashlib.sha256(orig).hexdigest()
    BAK.parent.mkdir(parents=True, exist_ok=True)
    BAK.write_bytes(orig)

    text = orig.decode("utf-8")
    if text.count(DRIVEN) != 1:
        print(f"★ 探针自身失败：数据驱动判定锚点命中 {text.count(DRIVEN)} 次（期望 1）")
        return 1

    try:
        TARGET.write_bytes(text.replace(DRIVEN, LITERAL, 1).encode("utf-8"))

        rc, out = sh(["go", "test", "./internal/chain/", "-run", "TestAgentDeniedDataDrivenProbe", "-count=1"])
        record("★ 回退成字面量后，鉴别性探针必须红（＝它真有鉴别力）", rc != 0,
               f"rc={rc}")
        # ★ 失败点的措辞按**实际**写：预期先触发「计数断言」`应收集 3 个…实为 2`（不是命名断言）。
        #   ★ 更本质的判据在下一项：**同一变异状态下对照组 `TestNodeAllowsAgent` 仍绿**
        #   ⇒ 证明这是**语义断言失败**，不是编译错/环境错。
        record("★ 红的原因是断言失败（计数断言 / 命名断言），而非编译错",
               ("应收集 3 个" in out) or ("future_added_node" in out) or ("未被拒绝" in out), "")

        rc2, _ = sh(["go", "test", "./internal/chain/", "-run", "TestNodeAllowsAgent", "-count=1"])
        record("对照组：此时 TestNodeAllowsAgent 仍绿（证明 spec 标记没丢，红的只是探针）",
               rc2 == 0, f"rc={rc2}")
    finally:
        TARGET.write_bytes(orig)
        ok = hashlib.sha256(TARGET.read_bytes()).hexdigest() == digest
        record("还原可证明（sha256 逐字节一致）", ok, digest[:12])
        if not ok:
            print("★★ 还原失败 —— 请手工 `git checkout -- internal/chain/agent.go`！")
            return 1

    rc3, _ = sh(["go", "test", "./internal/chain/", "-count=1"])
    record("反向控制：还原后 internal/chain 全绿", rc3 == 0, f"rc={rc3}")

    bad = [r for r in results if not r[1]]
    print(f"\n{'★ 全部如期' if not bad else '★ 有 %d 项不符' % len(bad)}（共 {len(results)} 项）")
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
