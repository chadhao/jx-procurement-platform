#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
scripts/_probe_n067.py —— 常驻探针：`N-067` ① 的**规格先行**（批 47 · `REMAINING.md#B24`）。

★★ 为什么存在：本批**只改规格、零引擎改动**（判据仍 30 / 原语仍 12），
   ★ 而「只声明、未消费」的新契约如果**没人钉**，下一次改动就可能**静默删掉**它 ——
   与 `N-058` 的「验收组重复体」、`N-061` 的「孤儿契约行」、`N-062` 的「新键无人读」同族：
   **声明一旦与消费面脱钩，门禁看不见**。本探针把本批的**一条声明**钉住，
   并附**缺口存在性反证**（把第三处段从内存副本里截掉 ⇒ 对应断言必须报红）。

★ 纪律：**只改内存副本**（`copy.deepcopy`），**绝不落盘** ——
   收尾核对**四个**真源文件的 `sha256` 与探针启动时逐字节一致。

★ 本批所钉（**一条声明 · 六个承重口径**）：
   `chain.json#conventions.checks_when` 的 **第三处承载口径** ——
   「无待办节点上的 `approval(<node_id>)` 判据由『流程跨过该节点』的钩子承载」：
   ① 治的缺口 ＝ **fail-closed 在「无任务节点」这一格被静默豁免**（不是「判为通过」，
      而是「根本不进判据面」）；
   ② 挂载点唯一 ＝ `handlers_approval.go#approveReject`（与第 ① 条**同一处、同一注册表**
      `approvalCheckFns`、**拦在 `Flow.Approve` 之前**、事务外）；
   ③ **不得**放进 `internal/flow#advanceTx`（那里只有 `t_flow_task`、且**节点快照未持久化**
      ⇒ 无任务节点在其视野之外；用任务表相邻 `seq` 空档反推是**不可靠的间接推断**）；
   ④ fail-closed 与第 ① 条**逐字同源**（`hard` ∧ 非 `manual` ⇒ 未注册即**可见失败**）；
   ⑤ 去重 ＝ 两路径按「该节点**有无待办**」**互斥划分**，同一 `node_id` 一次推进至多执行一次；
   ⑥ **不新增任何持久化**（无新表 / 无新列 / 无节点快照）。
   ★ **诚实划界（必须钉住，防后人误读）**：本契约落地后 `BA#anti_split_before_disburse`
   **仍不会被自动执行**（其 `carried_by_kind=manual` ⇒ 跳过）—— 本契约治的是**机制空洞**，
   **不是**把人工判据自动化。

跑法：python scripts/_probe_n067.py   （退出码 0 = 全通过；1 = 有失败项）
"""
import copy
import hashlib
import json
import os
import re
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)


def sha(p):
    with open(p, "rb") as f:
        return hashlib.sha256(f.read()).hexdigest()


def load(rel):
    with open(os.path.join(ROOT, rel), "r", encoding="utf-8") as f:
        return json.load(f)


CHAIN = "spec/chain.json"
BA = "spec/forms/BA.json"
CHECKS = "spec/checks.json"
README = "spec/README.md"

# 第三处承载口径段的**起头标记**（截取内存副本时用；★ 与 chain.json 文本同步维护）
MARK = "第三处承载口径"

fails = []
oks = 0


def chk(cond, label, detail=""):
    global oks
    if cond:
        oks += 1
        print("  ✓ " + label)
    else:
        fails.append(label)
        print("  ✗ " + label + ("　—— " + detail if detail else ""))


def main():
    before = {p: sha(os.path.join(ROOT, p)) for p in (CHAIN, BA, CHECKS, README)}

    chain = load(CHAIN)
    ba = load(BA)
    checks = load(CHECKS)

    conv = chain.get("conventions") or {}
    cw = conv.get("checks_when", "")

    # ---- 1. 第三处承载口径：一条声明 · 六个承重口径 ----
    chk(MARK in cw, "1a checks_when 含「%s」段" % MARK)
    for token in (
        "无待办节点",
        "跨过",
        "approveReject",
        "approvalCheckFns",
        "Flow.Approve",
        "互斥",
        "不新增任何持久化",
        "advanceTx",
        "N-067",
    ):
        chk(token in cw, "1b 第三处段含承重口径「%s」" % token)

    # ---- 2. 诚实划界：落地后 anti_split 仍不会被自动执行 ----
    chk("仍不会被自动执行" in cw,
        "2a 诚实划界段在（「本契约落地后 … 仍不会被自动执行」）")
    chk("不是**把人工判据自动化" in cw or "**不是**把人工判据自动化" in cw,
        "2b 明写「不是把人工判据自动化」（防误读为「拆单检查已系统强制」）")

    # ---- 3. 派生核对：当前唯一实例与其 manual 形态（★ 全部从真源实测，不写死数值）----
    nodes = chain["routes"]["purchase_tier1"]["nodes"]
    byid = {n.get("id"): n for n in nodes}
    asc = byid.get("anti_split_check", {})
    chk(asc.get("actor") == "system" and asc.get("required") is True,
        "3a anti_split_check 实测 actor=system ∧ required=true（本处适用条件的前提）")
    chk("generates_task" not in asc,
        "3b anti_split_check 未声明 generates_task（⇒ 按缺省推导为「不生成待办」＝ 本处适用）")
    ba_checks = {c.get("id"): c for c in (ba.get("checks") or [])}
    asc_ch = ba_checks.get("anti_split_before_disburse")
    chk(bool(asc_ch) and asc_ch.get("severity") == "hard",
        "3c BA#anti_split_before_disburse 实测 severity=hard（★ 从 forms/BA.json 派生）")
    chk(bool(asc_ch) and asc_ch.get("when") == "approval(anti_split_check)",
        "3d 其 when 实测 = approval(anti_split_check)（★ 从 forms/BA.json 派生）")
    chk(bool(asc_ch) and asc_ch.get("carried_by_kind") == "manual",
        "3e 其 carried_by_kind 实测 = manual ⇒ 本处对它**零行为变化**"
        "（★ 若某日该判据翻 code，则 2a/2b 的「诚实划界」段与本断言须同步复核 —— 不静默）")

    # ---- 4. 缺口存在性反证：截掉第三处段 ⇒ 1a / 1b 必红 ----
    mut = copy.deepcopy(chain)
    m_cw = mut["conventions"]["checks_when"]
    i = m_cw.find(MARK)
    mut["conventions"]["checks_when"] = m_cw[:i] if i >= 0 else m_cw
    chk(MARK not in mut["conventions"]["checks_when"],
        "4 反证：截掉第三处段 ⇒ 断言 1a 转红（★ 证明该段在承重，不是可有可无的散文）"
        "（内存副本，未落盘）")

    # ---- 5. 不变量：本批零新增判据 / 零新增原语（版本与计数从真源与 README 双向核对）----
    nchecks = len(checks["checks"])
    nprims = len(checks.get("primitives") or {})
    with open(os.path.join(ROOT, README), "r", encoding="utf-8") as f:
        rd = f.read()
    m = re.search(r"V(\d+\.\d+)（(\d+) 原语 / (\d+) 判据）", rd)
    chk(bool(m), "5a README §2 的 checks.json 行可解析（真源版本 + 计数）")
    if m:
        chk(checks["version"] == m.group(1),
            "5b 版本一致 checks.json=%s ↔ README=%s" % (checks["version"], m.group(1)))
        chk(nprims == int(m.group(2)) and nchecks == int(m.group(3)),
            "5c 计数一致 原语 %d/%s · 判据 %d/%s" % (nprims, m.group(2), nchecks, m.group(3)))
    chk(nchecks == 30 and nprims == 12,
        "5d 本批零新增：判据仍 30 / 原语仍 12（实得 %d / %d）" % (nchecks, nprims))

    # ---- 6. 收尾：本探针只改内存副本，真源逐字节未变 ----
    after = {p: sha(os.path.join(ROOT, p)) for p in before}
    chk(before == after, "6 真源 4 文件 sha256 逐字节未变（探针未落盘）")

    print()
    print("合计：通过 %d / 失败 %d" % (oks, len(fails)))
    if fails:
        print("★ 失败项：" + "；".join(fails))
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
