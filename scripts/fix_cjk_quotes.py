#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""scripts/fix_cjk_quotes.py —— 修「中文规格里把引号写成 ASCII `"` 导致 **JSON 语法错**」的**修复工具**。

★★ 设计铁律一：**只修「当前已经不合法的 JSON」**。
  理由（★ 这是我第一版的错误，已纠正）：第一版做成"全仓扫描"，结果**报出 586 处假命中** ——
  因为 JSON 里有**转义引号**、Python 里有**单引号串与三引号跨行**，逐行状态机认不出来。
  ★ 一个报 586 处假命中的工具**比没有更糟**：它会被立刻忽略（`README` 定案 #25：
  **「命中多到无人细读」＝假绿的另一种形态**）。
  ⇒ **改判据：先证明它是坏的，再修** —— 只有 `json.loads` **当前失败**的文件才进入修复流程；
    ★ 合法 JSON **在构造上不可能**有"未转义引号"问题 ⇒ **假阳性为零**。

★★ 设计铁律二：**必须保留原行尾**。
  第一版用默认换行模式读写 ⇒ 把 **CRLF 文件整体转成 LF** ⇒ 修 2 个字符却产生**全文件 diff**。
  ⇒ 读、拆、拼、写**四处都要用原行尾**（`newline=""` ＋ 按探测到的 eol 处理）。

★ 适用场景（真实、已发生 9 次）：把中文引号写成 ASCII `"`，例如把某句中文用直引号包住，
  ⇒ 字符串被提前截断 ⇒ `check_spec.py` 的 `S1` 报 `Expecting ',' delimiter`。
  ★ 报错**只告诉你"有错"**，不告诉你改哪；本工具就是补这一步。

用法：
    python scripts/fix_cjk_quotes.py            # 扫描 spec/**/*.json，报告"当前不合法的文件 + 能否修好"（不改动）
    python scripts/fix_cjk_quotes.py --fix      # 就地修复（**修完再次 json.loads 校验，不过则不写**）
    python scripts/fix_cjk_quotes.py --fix spec/forms/SUB.json
退出码：0 = 无坏文件 / 全部修好；1 = 有坏文件且未能修好
"""
import json
import os
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def target_files(argv):
    explicit = [a for a in argv if not a.startswith("--")]
    if explicit:
        return [a if os.path.isabs(a) else os.path.join(ROOT, a) for a in explicit]
    out = []
    for dirpath, dirnames, filenames in os.walk(os.path.join(ROOT, "spec")):
        dirnames[:] = [d for d in dirnames if not d.startswith(".")]
        out += [os.path.join(dirpath, f) for f in filenames if f.endswith(".json")]
    return sorted(out)


def repair(text, eol):
    """修「JSON 字符串内部误用 ASCII 引号」→ (新文本, 修正数)。

    ★★ 判据（v2，已修正 v1 的错位缺陷）：一个引号是**合法定界符**，当且仅当
      **前邻**是结构/空白（`{ [ , :` 或行首/空白）**或** **后邻**是结构/空白（`, : } ]` 或行尾/空白）。
      ★ 其余一律判为**误用**，并按**独立的计数器**成对替换为 `「` / `」`。
    ★★ v1 的错：把「误用计数」与 `in_string` 状态绑在一起 ⇒ 成对误用会被**错位替换**
      （把第二处误用当收尾、第三处当开头）⇒ 修后仍不合法。★ 因为本工具**只处理不合法文件**，
      改进判据对合法文件**零影响**（它们根本不进修复流程）。
    ★ 处理**反斜杠转义**：`\"` 是内容，不是定界符。
    """
    out_lines, n = [], 0
    struct_before = set("{[,:")
    struct_after = set(",:]}")
    for line in text.split(eol):
        res, esc, mis = [], False, 0
        for i, c in enumerate(line):
            if esc:
                res.append(c); esc = False; continue
            if c == chr(92):
                res.append(c); esc = True; continue
            if c != '"':
                res.append(c); continue
            # ★ v3：跳过空格后看**前一个非空字符**是否结构字符（v2 的 `prev.isspace()` 会把
            #   `★ **"…` 这种「空格前是星号」的误用开引号误判为合法定界符 ⇒ 错位）。
            j = i - 1
            while j >= 0 and line[j] in " 	":
                j -= 1
            before = line[j] if j >= 0 else ""
            k = i + 1
            while k < len(line) and line[k] in " 	":
                k += 1
            after = line[k] if k < len(line) else ""
            legal = (before == "" or before in struct_before) or (after == "" or after in struct_after)
            if legal:
                res.append(c)
            else:
                res.append("「" if mis % 2 == 0 else "」")
                mis += 1
                n += 1
        out_lines.append("".join(res))
    return eol.join(out_lines), n


def main() -> int:
    do_fix = "--fix" in sys.argv
    files = target_files(sys.argv[1:])
    broken, fixed, unfixable = [], [], []

    for f in files:
        rel = os.path.relpath(f, ROOT).replace("\\", "/")
        try:
            s = open(f, encoding="utf-8", newline="").read()   # ★ newline="" 保留原行尾
        except Exception as e:
            print(f"  ! 读取失败 {rel}: {e}")
            continue
        try:
            json.loads(s)
            continue                       # ★ 合法的直接跳过 —— 「零假阳性」的关键
        except json.JSONDecodeError as e:
            broken.append(rel)
            why = str(e)

        eol = "\r\n" if "\r\n" in s else "\n"
        new, n = repair(s, eol)
        try:
            json.loads(new)
        except json.JSONDecodeError as e2:
            unfixable.append(f"{rel}: 修后仍不合法 —— {e2}")
            continue
        print(f"  {rel}: 当前不合法（{why}）→ 状态机可修 {n} 处引号，**修后合法** ✓")
        if do_fix:
            open(f, "w", encoding="utf-8", newline="").write(new)   # ★ 同样保留行尾
            fixed.append(rel)

    if not broken:
        print("OK 无「当前不合法的 JSON」（已扫 %d 个文件）—— ★ 合法的不进修复流程，故不存在假阳性" % len(files))
        return 0

    print()
    if unfixable:
        print("★ 以下文件**无法自动修复**，需人工处理：")
        for u in unfixable:
            print("  " + u)
        return 1
    if do_fix:
        print("已修复 %d 个文件：%s" % (len(fixed), "、".join(fixed)))
        return 0
    print("共 %d 个文件当前不合法（**未改动**）。加 --fix 修复。" % len(broken))
    return 1


if __name__ == "__main__":
    sys.exit(main())
