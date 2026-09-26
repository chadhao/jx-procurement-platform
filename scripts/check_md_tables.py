#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""Markdown 表格列数一致性门禁。

起因：03-TestCase.md 的变更记录表**表头 3 列、末三行 4 列** —— Markdown 渲染时
多出的单元格会被丢弃或错位，属"看起来没问题"的静默缺陷。本脚本把全 docs/ 的
表格按块检查「表头列数 == 分隔行列数 == 每行数据列数」。

★ 只检查"连续以 | 开头的行"构成的块，遇空行/非表格行即结算。
★ 单元格里的 `\\|`（转义竖线）必须还原后再计数，否则 `flock(LOCK_EX\\|LOCK_NB)` 会被误判成多一列。
"""
import os
import re
import sys

ESC_PIPE = "\x00ESC_PIPE\x00"


def cells(line: str) -> int:
    s = line.strip()
    s = s.replace("\\|", ESC_PIPE)          # 转义竖线先藏起来
    if s.startswith("|"):
        s = s[1:]
    if s.endswith("|"):
        s = s[:-1]
    return len(s.split("|"))


def is_table_row(line: str) -> bool:
    return line.strip().startswith("|")


def is_sep_row(line: str) -> bool:
    s = line.strip()
    if not s.startswith("|"):
        return False
    s = s.replace("\\|", ESC_PIPE)
    body = s.strip("|")
    if not body.strip():
        return False
    # 允许 :---: / --- / :-- 等
    return all(re.fullmatch(r"\s*:?-{2,}:?\s*", c) for c in body.split("|"))


def check_file(path: str) -> list:
    problems = []
    with open(path, "r", encoding="utf-8", newline="") as f:
        lines = f.read().splitlines()

    i = 0
    while i < len(lines):
        if not is_table_row(lines[i]):
            i += 1
            continue
        start = i
        block = []
        while i < len(lines) and is_table_row(lines[i]):
            block.append((i + 1, lines[i]))
            i += 1

        if len(block) < 2:
            continue

        head_n = cells(block[0][1])
        # 块内第二个非分隔行若为分隔行，则以它为准（表头可能被误写）
        sep_n = None
        for ln, txt in block[1:3]:
            if is_sep_row(txt):
                sep_n = cells(txt)
                break

        if sep_n is None:
            problems.append((start + 1, "表格块缺少分隔行（|---|）", head_n, None))
            continue
        if sep_n != head_n:
            problems.append((start + 1, "表头列数 != 分隔行列数", head_n, sep_n))

        for ln, txt in block:
            if is_sep_row(txt):
                continue
            n = cells(txt)
            if n != head_n:
                problems.append((ln, "数据行列数与表头不一致", head_n, n))
    return problems


def main() -> int:
    root = os.path.join(os.path.dirname(os.path.abspath(__file__)), "docs")
    if not os.path.isdir(root):
        root = "docs"
    total = 0
    for dirpath, _dirnames, filenames in os.walk(root):
        for fn in sorted(filenames):
            if not fn.endswith(".md"):
                continue
            path = os.path.join(dirpath, fn)
            rel = os.path.relpath(path).replace("\\", "/")
            for ln, msg, want, got in check_file(path):
                total += 1
                print(f"FAIL {rel}:{ln} {msg}（期望 {want}，实际 {got}）")
    if total:
        print(f"\n共 {total} 处表格列数不一致")
        return 1
    print("OK 全部 Markdown 表格列数一致")
    return 0


if __name__ == "__main__":
    sys.exit(main())
