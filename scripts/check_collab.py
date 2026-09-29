#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
scripts/check_collab.py —— `COLLAB.md`（双 Agent 协商台账）结构门禁。

★ 存在理由：`COLLAB.md` 是两个 Agent 之间**唯一**的协商渠道；若它本身结构松散
  （议题 ID 重复、状态值乱写、必填字段缺失、`ESCALATED` 却不写双方方案），
  那"接门禁机器校验"就只是句空话 —— 与 `README` 定案 #54「判定门禁是否有效只能用探针法」、
  #25「命中多到无人细读＝假绿的另一种形态」同族。

★★ 本脚本的**自证记录**（每一条都是真跑出来的，不是推测）：
  · v1 → v2：首版有 3 类误报，已修 ——
      ① 把**附录 A 的议题模板**（在 ``` 代码块内）当成真议题 ⇒ 跳过代码围栏
      ② 议题块边界只认"下一个议题标题" ⇒ 末条吞掉后续附录 ⇒ 边界取
         min(下一个议题标题, 下一个 `## ` 章节, EOF)
      ③ 从 57 处误报收敛到 1 处真问题（`N-004` 的「决议」写成表格、无内联值）
  · v2 → v3：**探针法**逼出一个**假绿** —— 首版把「待议 = `## 1.`」的编号写死，
      而 `COLLAB.md` 里待议实际是 `## 4.` ⇒ **待议区的字段校验从未生效**；
      因待议区当时为空，一直没露馅。⇒ v3 改为**按章节标题关键词判定规则**，对重编号免疫。

校验项（对应 `COLLAB.md` 附录 B）：
  B1  议题 ID 唯一且递增（`^N-\\d{3}$`）
  B2  状态仅限枚举：OPEN / WK-DONE / MIMO-DONE / AGREED / ESCALATED
  B3  类型仅限枚举：需求澄清 / 接口契约 / 技术方案 / 冲突 / 阻塞
  B4  字段齐备（**按章节种类**区分）：
        待议   → 提出方 / 类型 / 责任域 / 背景 / 我方立场 / 建议方案 / 制度影响面 / 状态 / 最后更新
        已决议 → 决议 / 依据 / 决议日
        已上交 → 双方方案 / 各自代价
  B5  「已上交」议题必须含「双方方案」与「各自代价」
  B6  「最后更新」/「决议日」须绝对时间（`YYYY-MM-DD[ HH:MM]`），且不得含「昨天 / 刚才」等相对词
  S   结构节存在性；议题必须落在**已登记种类**的章节内

用法：python scripts/check_collab.py [COLLAB.md 路径]
退出码：0 = 通过；1 = 有违规；2 = 文件缺失或不可解析
"""

import os
import re
import sys

NAME = "check_collab.py"

ISSUE_RE = re.compile(r"^###\s+(N-\d{3})\s+·\s+(.*)$")
SECTION_RE = re.compile(r"^##\s+(\d+)\.\s+(.*)$")
FIELD_RE = re.compile(r"^-\s+\*\*(?P<key>[^*]+?)\*\*\s*[:：]?\s*(?P<val>.*)$")
FENCE_RE = re.compile(r"^\s*(```|~~~)")

# ★ 按标题关键词判定种类（对章节重编号免疫）
SECTION_KINDS = [("待议", "pending"), ("已决议", "agreed"), ("已上交", "escalated")]

KIND_REQUIRED = {
    "pending": [
        "提出方", "类型", "责任域", "背景", "我方立场",
        "建议方案", "制度影响面", "状态", "最后更新",
    ],
    "agreed": ["决议", "依据", "决议日"],
    "escalated": ["双方方案", "各自代价"],
}
KIND_LABEL = {"pending": "待议", "agreed": "已决议", "escalated": "已上交"}

VALID_STATUS = {"OPEN", "WK-DONE", "MIMO-DONE", "AGREED", "ESCALATED"}
VALID_TYPE = {"需求澄清", "接口契约", "技术方案", "冲突", "阻塞"}

ABS_TIME_RE = re.compile(r"\d{4}-\d{2}-\d{2}(\s+\d{2}:\d{2})?")
RELATIVE_WORDS = ["昨天", "今天", "明天", "上次", "刚才", "刚刚", "稍早", "本周", "上周", "前天", "后天"]

REQUIRED_SECTIONS = [
    ("## 0. 铁律", "铁律节"),
    ("## 1. 当前状态", "当前状态区"),
    ("## 2. 责任域划分", "责任域节"),
    ("## 4. 待议", "待议区"),
    ("## 5. 已决议", "已决议区"),
    ("## 6. 已上交用户", "已上交区"),
    ("## 附录 A", "议题模板"),
    ("## 附录 B", "校验规则"),
]


def default_path() -> str:
    here = os.path.dirname(os.path.abspath(__file__))
    return os.path.join(os.path.dirname(here), "COLLAB.md")


def classify(title: str):
    for word, kind in SECTION_KINDS:
        if word in title:
            return kind
    return None


def scan(lines):
    """→ (heads, sec_of, sec_title_of, sec_head_idx_of)
    heads:          [(issue_id, title, idx)]
    sec_of:         {idx: kind}      该行所属章节的**种类**（未识别 ⇒ None）
    sec_title_of:   {idx: 章节标题}
    section_heads:  [idx, ...]       所有 `## N.` 行的行号
    """
    heads, sec_of, sec_title_of, section_heads = [], {}, {}, []
    cur_kind, cur_title = None, None
    fence = None
    for i, ln in enumerate(lines):
        raw = ln.rstrip()
        fm = FENCE_RE.match(raw)
        if fm:
            tok = fm.group(1)
            if fence is None:
                fence = tok
            elif tok == fence:
                fence = None
            continue
        if fence is not None:
            continue
        sm = SECTION_RE.match(raw)
        if sm:
            cur_title = sm.group(2).strip()
            cur_kind = classify(cur_title)
            section_heads.append(i)
            continue
        sec_of[i] = cur_kind
        sec_title_of[i] = cur_title
        im = ISSUE_RE.match(raw)
        if im:
            heads.append((im.group(1), im.group(2).strip(), i))
    return heads, sec_of, sec_title_of, section_heads


def section_blocks(lines, heads, sec_of, section_heads):
    """按「下一个议题标题」与「下一个 ## 章节」双重边界切块。"""
    out = []
    for n, (iid, title, start) in enumerate(heads):
        end = len(lines)
        if n + 1 < len(heads):
            end = heads[n + 1][2]
        for j in section_heads:
            if start < j < end:
                end = j
                break
        out.append((iid, title, start, end, sec_of.get(start)))
    return out


def check(path: str) -> int:
    if not os.path.isfile(path):
        print(f"FAIL [{NAME}] 找不到 {path}")
        return 2
    try:
        with open(path, "r", encoding="utf-8", newline="") as f:
            text = f.read()
    except Exception as e:
        print(f"FAIL [{NAME}] 读取失败：{e}")
        return 2

    lines = text.split("\n")
    heads, sec_of, sec_title_of, section_heads = scan(lines)
    blocks = section_blocks(lines, heads, sec_of, section_heads)
    problems = []

    # ---- B1：ID 唯一且递增（跨章节） ----
    seen, prev = {}, 0
    for iid, _t, start, _e, _s in blocks:
        lineno = start + 1
        if iid in seen:
            problems.append(f"[B1] COLLAB.md:{lineno} 议题 ID 重复：{iid}（首次出现在第 {seen[iid]} 行）")
        else:
            seen[iid] = lineno
        num = int(iid[2:])
        if num <= prev:
            problems.append(f"[B1] COLLAB.md:{lineno} 议题 ID 未递增：{iid}（上一个为 N-{prev:03d}）")
        prev = max(prev, num)

    # ---- 逐议题 ----
    for iid, _title, start, end, kind in blocks:
        lineno = start + 1
        block = lines[start:end]
        fields = {}
        for ln in block[1:]:
            fm = FIELD_RE.match(ln.rstrip())
            if fm:
                fields.setdefault(fm.group("key").strip(), fm.group("val").strip())

        if kind is None:
            st = sec_title_of.get(start) or "(不在任何章节内)"
            problems.append(
                f"[S] COLLAB.md:{lineno} {iid} 位于未登记章节「{st}」—— "
                f"该章节的议题规则未定义（可登记的关键词：{' / '.join(w for w, _ in SECTION_KINDS)}）"
            )
            continue

        label = KIND_LABEL[kind]

        # B4 字段齐备
        for k in KIND_REQUIRED[kind]:
            if k not in fields:
                problems.append(f"[B4] COLLAB.md:{lineno} {iid}（{label}）缺必填字段「{k}」")
            elif not fields[k]:
                problems.append(f"[B4] COLLAB.md:{lineno} {iid}（{label}）字段「{k}」为空")

        # B2 / B3：仅「待议」区校验枚举
        if kind == "pending":
            st = fields.get("状态", "")
            if st and st not in VALID_STATUS:
                problems.append(
                    f"[B2] COLLAB.md:{lineno} {iid} 状态值非法：「{st}」"
                    f"（允许：{' / '.join(sorted(VALID_STATUS))}）"
                )
            ty = fields.get("类型", "")
            if ty and ty not in VALID_TYPE:
                problems.append(
                    f"[B3] COLLAB.md:{lineno} {iid} 类型值非法：「{ty}」"
                    f"（允许：{' / '.join(sorted(VALID_TYPE))}）"
                )

        # B5：「已上交」区必须含双方方案与各自代价
        if kind == "escalated":
            for must in ("双方方案", "各自代价"):
                if not fields.get(must):
                    problems.append(
                        f"[B5] COLLAB.md:{lineno} {iid}（已上交）缺「{must}」—— "
                        f"上交用户时必须写清双方方案与各自代价"
                    )

        # B6 绝对时间
        for key in ("最后更新", "决议日"):
            v = fields.get(key, "")
            if not v:
                continue
            if not ABS_TIME_RE.search(v):
                problems.append(f"[B6] COLLAB.md:{lineno} {iid} 「{key}」不是绝对时间：{v}")
            for w in RELATIVE_WORDS:
                if w in v:
                    problems.append(f"[B6] COLLAB.md:{lineno} {iid} 「{key}」含相对时间词「{w}」：{v}")

    # ---- S：结构节存在性 ----
    for needed, label in REQUIRED_SECTIONS:
        if needed not in text:
            problems.append(f"[S] COLLAB.md 缺少结构节：{label}（应含「{needed}」）")

    if problems:
        print(f"FAIL [{NAME}] 共 {len(problems)} 处违规（已扫 {len(blocks)} 条议题）：")
        for p in problems:
            print("  " + p)
        return 1

    dist = {}
    for _i, _t, _s, _e, k in blocks:
        dist[KIND_LABEL.get(k, "未登记")] = dist.get(KIND_LABEL.get(k, "未登记"), 0) + 1
    sd = " · ".join(f"{k} {v}" for k, v in sorted(dist.items()))
    print(f"OK [{NAME}] COLLAB.md 结构校验通过（议题 {len(blocks)} 条：{sd or '无'}）")
    return 0


def main() -> int:
    path = sys.argv[1] if len(sys.argv) > 1 else default_path()
    return check(path)


if __name__ == "__main__":
    sys.exit(main())
