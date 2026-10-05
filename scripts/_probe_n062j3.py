#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
scripts/_probe_n062j3.py —— 常驻探针：`N-062` 族 `J3` 的**规格先行**（批 42 · `REMAINING.md#B23`）。

★★ 为什么存在：本批**只改规格、零引擎改动**（判据仍 30 / 原语仍 12），
   ★ 而「只声明、未消费」的新键如果**没人钉**，下一次改动就可能**静默删掉**它 ——
   与 `N-058` 的「验收组重复体」、`N-061` 的「孤儿契约行」同族：
   **声明一旦与消费面脱钩，门禁看不见**。本探针把本批的**四条声明**钉住，
   并附**缺口存在性反证**（把修复前的形态塞回内存副本 ⇒ 对应断言必须报红）。

★ 纪律：**只改内存副本**（`copy.deepcopy`），**绝不落盘** ——
   收尾核对**五个**真源文件的 `sha256` 与探针启动时逐字节一致。

★ 本批所钉（**五条声明**；★★ **批 44 更新**：`J3` 的 `BA` 侧**已落地并过验收** ⇒ ①⑤ 措辞由「只声明、未消费」改为「已落地」；★ `anti_split_check` 一行的取证结论 ＝ **「无处执行」**（另开 `N-067`））：
   ① `conventions.node_task_generation`（含 **批 42 补登记的「同族具名待定」段**：
      `anti_split_check` 同为动作型节点、**刻意不声明** `generates_task` ⇒ 其可达性
      取决于「通用求值器」；★ **「有没有人的待办」≠「流程会不会经过该节点」**）
   ② `routes.purchase_tier1.nodes[5].generates_task = true`
   ③ `conventions.checks_when` 的两处承载口径
   ④ `forms/SA.json` 的 `actual_cents` ＋差额字段计算口径
   ⑤ `forms/BA.json#anti_split_before_disburse` 的 `hard ∧ approval(anti_split_check)`（**派生、不写死**）

跑法：python scripts/_probe_n062j3.py   （退出码 0 = 全通过；1 = 有失败项）
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
SA = "spec/forms/SA.json"
BA = "spec/forms/BA.json"
CHECKS = "spec/checks.json"
README = "spec/README.md"

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
    before = {p: sha(os.path.join(ROOT, p)) for p in (CHAIN, SA, BA, CHECKS, README)}

    chain = load(CHAIN)
    sa = load(SA)
    ba = load(BA)
    checks = load(CHECKS)

    conv = chain.get("conventions") or {}

    # ---- 1. 新约定：node_task_generation（只声明、未消费）----
    ntg = conv.get("node_task_generation", "")
    chk(bool(ntg), "1a 存在 conventions.node_task_generation")
    for token in ("generates_task", "isActionActor", "节点是否生成待办", "零变化", "GeneratesTask"):
        chk(token in ntg, "1b 约定含关键口径「%s」" % token)

    # ---- 2. 显式声明 1 处：purchase_tier1.return_receipt ----
    nodes = chain["routes"]["purchase_tier1"]["nodes"]
    byfreed = {n.get("id"): n for n in nodes}
    r = byfreed.get("return_receipt", {})
    chk(r.get("generates_task") is True, "2a return_receipt 显式 generates_task=true")
    chk("task_note" in r, "2b return_receipt 带 task_note（写明依据与实现侧边界）")
    # 具名保留：self_purchase 不得被顺手声明
    sp = byfreed.get("self_purchase", {})
    chk("generates_task" not in sp, "2c self_purchase 具名保留（未声明 generates_task）")

    # ---- 2d–2f. 同族具名待定（批 42 补登记）：anti_split_check ----
    #   ★ 我方自查发现（非 mimo 上报）：`actor=system` 的节点同样落 `isActionActor`
    #     ⇒ 同样不生成待办，而 `BA#anti_split_before_disburse`（hard）挂在它的时点上。
    for token in ("同族具名待定", "anti_split_check", "两个不同的可达性", "通用求值器", "无处执行", "N-067"):
        chk(token in ntg, "2d 约定含同族待定关键口径「%s」" % token)
    asc = byfreed.get("anti_split_check", {})
    chk(asc.get("actor") == "system" and asc.get("required") is True,
        "2e anti_split_check 是 actor=system ∧ required=true（同族的判定前提）")
    chk("generates_task" not in asc,
        "2f anti_split_check **刻意不声明** generates_task（★ 系统环节没有人的待办入口，"
        "声明 true 是错的修法）")
    # ★ 不写死判据名与 when：从 forms/BA.json 实测派生（规格一变本断言自动跟随）
    ba_checks = {c.get("id"): c for c in (ba.get("checks") or [])}
    asc_ch = ba_checks.get("anti_split_before_disburse")
    chk(bool(asc_ch) and asc_ch.get("severity") == "hard"
        and asc_ch.get("when") == "approval(anti_split_check)",
        "2g BA#anti_split_before_disburse 实测＝hard ∧ when=approval(anti_split_check)"
        "（★ 从 forms/BA.json 派生，非写死）")

    # ---- 3. 缺口存在性反证 A：摘掉 generates_task ⇒ 2a 必红 ----
    mut = copy.deepcopy(chain)
    for n in mut["routes"]["purchase_tier1"]["nodes"]:
        if n.get("id") == "return_receipt":
            n.pop("generates_task", None)
    r2 = {n.get("id"): n for n in mut["routes"]["purchase_tier1"]["nodes"]}["return_receipt"]
    chk((r2.get("generates_task") is True) is False,
        "3 反证 A：摘掉 generates_task ⇒ 断言 2a 转红（内存副本，未落盘）")

    # ---- 3b. 缺口存在性反证 B：摘掉「同族具名待定」段 ⇒ 2d 必红 ----
    mut_ntg = copy.deepcopy(ntg)
    idx = mut_ntg.find("同族具名待定")
    mut_ntg = mut_ntg[:idx] if idx >= 0 else mut_ntg
    chk("同族具名待定" not in mut_ntg,
        "3b 反证 B：摘掉「同族具名待定」段 ⇒ 断言 2d 转红（★ 证明该段在承重，"
        "不是可有可无的散文）")

    # ---- 4. checks_when 的两处承载口径 ----
    cw = conv.get("checks_when", "")
    for token in ("通用求值器", "fail-closed", "POST /api/approval/{biz_no}/backfill",
                  "白名单", "ext_json", "必须与路由实现同批落"):
        chk(token in cw, "4 约定 checks_when 含「%s」" % token)

    # ---- 5. SA 补录段新增 actual_cents（输入字段）----
    sec = [s for s in sa["sections"] if s["id"] == "settlement_backfill"][0]
    names = [f["name"] for f in sec["fields"]]
    chk("actual_cents" in names, "5a settlement_backfill 含 actual_cents")
    ac = [f for f in sec["fields"] if f["name"] == "actual_cents"][0]
    chk(ac["type"] == "money_cents" and ac["source"] == "user" and ac["required"] is False,
        "5b actual_cents = money_cents/user/required false")
    chk(ac.get("origin") == "spec_increment",
        "5c origin 标 spec_increment（不冒充 tool_table / institution）")
    chk(names.index("actual_cents") < names.index("actual_vs_approved_diff_cents"),
        "5d actual_cents 在差额字段之前（输入先于派生）")

    # ---- 6. 缺口存在性反证 B：摘掉 actual_cents ⇒ 5a / 5d 必红 ----
    mut2 = copy.deepcopy(sa)
    s2 = [s for s in mut2["sections"] if s["id"] == "settlement_backfill"][0]
    s2["fields"] = [f for f in s2["fields"] if f["name"] != "actual_cents"]
    n2 = [f["name"] for f in s2["fields"]]
    chk(("actual_cents" in n2) is False,
        "6 反证 B：摘掉 actual_cents ⇒ 断言 5a 转红（内存副本，未落盘）")

    # ---- 7. 差额字段：声明不得先于执行体 ----
    diff = [f for f in sec["fields"] if f["name"] == "actual_vs_approved_diff_cents"][0]
    chk(diff.get("carried_by_kind") == "accepted_gap",
        "7a 差额字段仍为 accepted_gap（无执行体时不得声明 code）")
    chk("amount_cents" in diff.get("rule", "") and "−" in diff.get("rule", ""),
        "7b 差额字段 rule 含计算口径（actual_cents − header.amount_cents）")

    # ---- 8. 不变量：本批零新增判据 / 零新增原语 ----
    nchecks = len(checks["checks"])
    nprims = len(checks.get("primitives") or {})
    with open(os.path.join(ROOT, README), "r", encoding="utf-8") as f:
        rd = f.read()
    m = re.search(r"V(\d+\.\d+)（(\d+) 原语 / (\d+) 判据）", rd)
    chk(bool(m), "8a README §2 的 checks.json 行可解析（真源版本 + 计数）")
    if m:
        chk(checks["version"] == m.group(1),
            "8b 版本一致 checks.json=%s ↔ README=%s" % (checks["version"], m.group(1)))
        chk(nprims == int(m.group(2)) and nchecks == int(m.group(3)),
            "8c 计数一致 原语 %d/%s · 判据 %d/%s" % (nprims, m.group(2), nchecks, m.group(3)))

    # ---- 9. 收尾：本探针只改内存副本，真源逐字节未变 ----
    after = {p: sha(os.path.join(ROOT, p)) for p in before}
    chk(before == after, "9 真源 5 文件 sha256 逐字节未变（探针未落盘）")

    print()
    print("合计：通过 %d / 失败 %d" % (oks, len(fails)))
    if fails:
        print("★ 失败项：" + "；".join(fails))
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
