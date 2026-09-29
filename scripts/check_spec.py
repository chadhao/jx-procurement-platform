#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
scripts/check_spec.py —— `spec/` 机读规格门禁。

★ 存在理由：`spec/` 是 WorkBuddy（制度+产品设计）交给 mimo code 的**机读契约**。
  人读文档会漂（本项目已实证多起），机读规格若**语法错、引用悬空、编号对不上**，
  同样会让开发拿到错的前提 —— 而且**错得静默**（写进去就编译不过才被发现，或者更糟：编译过但语义错）。

★★ 本门禁的第一条真问题就是**我自己犯的**（2026-09-29 首跑）：
  `spec/chain.json` 初稿里有 **3 处把中文引号写成了 ASCII 双引号**（`"应升级为…"`），
  直接把 JSON 打断 ⇒ 由本脚本的 XML/JSON 解析检查当场抓出。
  ⇒ 这正是"规格必须过机器校验"的实证，不是假想需求。

校验项：
  S1  `spec/` 下所有 `*.json` 必须能被 `json.load` 解析（★ 抓语法错，含引号/尾逗号）
  S2  `chain.json` 顶层必含键：version / roles / thresholds / contract_approval / routes / doc_chains
  S3  `routes` 必须恰好 9 条（采一/采二/采三/销一/管一/管二/独家/紧急/变更）
  S4  `doc_chains` 必须含 11 类单据（BA/PR/SA/RFQ/BJ/SS/CT/PC/GR/QC/SUB）
  S5  所有 `ledger` 取值必须是 `L01`–`L12`（且非 L08/L10/L12 —— 那三张不可作落账目标）
  S6  `route` / `ref` / `route_by_tier` 的取值必须指向存在的 route 或 `contract_two_level`
  S7  阈值区间的连续性：采一/采二/采三必须无缝且无重叠（99999 | 100000-500000 | 500001-）
  S8  `forms/*.json`（若存在）必须合法，且字段名不得含 `<br>` 或空格（R-18 命名规范）
  S9  `ledger-mapping.json`（若存在）必须覆盖 L01–L12 全部 12 个编号

用法：python scripts/check_spec.py
退出码：0 = 通过；1 = 有违规；2 = 无法定位 spec/ 目录
"""

import glob
import json
import os
import re
import sys

NAME = "check_spec.py"

REQUIRED_TOP_KEYS = ["version", "roles", "thresholds", "contract_approval", "routes", "doc_chains"]
REQUIRED_DOC_CHAINS = ["BA", "PR", "SA", "RFQ", "BJ", "SS", "CT", "PC", "GR", "QC", "SUB"]
REQUIRED_ROUTES = [
    "purchase_tier1", "purchase_tier2", "purchase_tier3",
    "expense_sales", "expense_mgmt_advance", "expense_mgmt_direct",
    "sole_source", "emergency", "change",
]
ALL_LEDGERS = [f"L{i:02d}" for i in range(1, 13)]
# ★ 不可作落账目标（README 定案 #20：只有 L01–L07 + L09 可落账）
FORBIDDEN_AS_TARGET = ["L08", "L10", "L11", "L12"]

LEDGER_RE = re.compile(r"^L\d{2}$")
BAD_FIELDNAME_RE = re.compile(r"<br\s*/?>|\s")


def spec_dir():
    here = os.path.dirname(os.path.abspath(__file__))
    return os.path.join(os.path.dirname(here), "spec")


def check() -> int:
    d = spec_dir()
    if not os.path.isdir(d):
        print(f"FAIL [{NAME}] 找不到 spec/ 目录：{d}")
        return 2

    problems = []
    loaded = {}

    # ---- S1：所有 JSON 可解析 ----
    json_files = sorted(glob.glob(os.path.join(d, "**", "*.json"), recursive=True))
    if not json_files:
        problems.append("[S1] spec/ 下没有任何 .json 文件（机读规格为空？）")
    for f in json_files:
        rel = os.path.relpath(f, os.path.dirname(d)).replace("\\", "/")
        try:
            with open(f, "r", encoding="utf-8") as fh:
                loaded[rel] = json.load(fh)
        except json.JSONDecodeError as e:
            problems.append(f"[S1] {rel} JSON 语法错误：{e}")
        except Exception as e:
            problems.append(f"[S1] {rel} 读取失败：{e}")

    chain = loaded.get("spec/chain.json")

    # ---- S2：chain.json 顶层必含键 ----
    if chain is None:
        if "spec/chain.json" not in [os.path.relpath(f, os.path.dirname(d)).replace("\\", "/") for f in json_files]:
            problems.append("[S2] 缺少 spec/chain.json（审批链规格是地基）")
    else:
        for k in REQUIRED_TOP_KEYS:
            if k not in chain:
                problems.append(f"[S2] chain.json 缺顶层键「{k}」")

    if isinstance(chain, dict):
        routes = chain.get("routes", {}) or {}
        doc_chains = {k: v for k, v in (chain.get("doc_chains", {}) or {}).items()
                      if not k.startswith("_")}
        valid_route_ids = set(routes.keys()) | {"contract_two_level"}

        # ---- S3：routes 恰好 9 条 ----
        missing_routes = [r for r in REQUIRED_ROUTES if r not in routes]
        if missing_routes:
            problems.append(f"[S3] routes 缺流程线：{', '.join(missing_routes)}")
        extra = [r for r in routes if r not in REQUIRED_ROUTES]
        if extra:
            problems.append(f"[S3] routes 出现未登记的流程线（请同步更新 REQUIRED_ROUTES）：{', '.join(extra)}")

        # ---- S4：doc_chains 11 类 ----
        missing_dc = [k for k in REQUIRED_DOC_CHAINS if k not in doc_chains]
        if missing_dc:
            problems.append(f"[S4] doc_chains 缺单据类型：{', '.join(missing_dc)}")

        # ---- S5：ledger 取值合法 + 不作落账目标 ----
        def walk_ledgers(obj, path):
            if isinstance(obj, dict):
                for k, v in obj.items():
                    if k == "ledger" and isinstance(v, list):
                        for code in v:
                            if not isinstance(code, str) or not LEDGER_RE.match(code):
                                problems.append(f"[S5] {path} ledger 取值非法：{code!r}")
                            elif code in FORBIDDEN_AS_TARGET:
                                problems.append(
                                    f"[S5] {path} ledger 指向 {code} —— "
                                    f"{'/'.join(FORBIDDEN_AS_TARGET)} 不可作落账目标（README #20）"
                                )
                            elif code not in ALL_LEDGERS:
                                problems.append(f"[S5] {path} ledger 编号不存在：{code}")
                    walk_ledgers(v, f"{path}.{k}")
            elif isinstance(obj, list):
                for i, v in enumerate(obj):
                    walk_ledgers(v, f"{path}[{i}]")

        walk_ledgers(chain, "chain")

        # ---- S6：route / ref 引用必须存在 ----
        def walk_refs(obj, path):
            if isinstance(obj, dict):
                for k, v in obj.items():
                    if k in ("route", "ref") and isinstance(v, str):
                        # ref 可能是 "routes.<档位>" 这类占位，排除
                        if v.startswith("routes.") or v.startswith("<"):
                            pass
                        elif v not in valid_route_ids:
                            problems.append(f"[S6] {path}.{k} 指向不存在的流程线：{v}")
                    if k == "route_by_tier" and isinstance(v, dict):
                        for kk, vv in v.items():
                            if vv not in valid_route_ids:
                                problems.append(f"[S6] {path}.route_by_tier[{kk}] 指向不存在的流程线：{vv}")
                    if k == "route_by_condition" and isinstance(v, str):
                        for part in [p.strip() for p in v.split("|")]:
                            if part and part not in valid_route_ids:
                                problems.append(f"[S6] {path}.route_by_condition 指向不存在的流程线：{part}")
                    walk_refs(v, f"{path}.{k}")
            elif isinstance(obj, list):
                for i, v in enumerate(obj):
                    walk_refs(v, f"{path}[{i}]")

        walk_refs(chain, "chain")

        # ---- S7：采档阈值必须无缝无重叠 ----
        bands = (((chain.get("thresholds") or {}).get("purchase") or {}).get("bands") or [])
        if len(bands) != 3:
            problems.append(f"[S7] thresholds.purchase.bands 应为 3 档，实为 {len(bands)}")
        else:
            prev_upper = None
            for b in bands:
                lo, hi = b.get("lower_inclusive"), b.get("upper_inclusive")
                if prev_upper is None:
                    if lo is not None:
                        problems.append(f"[S7] 首档 {b.get('id')} 的 lower_inclusive 应为 null（负金额不存在），实为 {lo}")
                else:
                    if lo != prev_upper + 1:
                        problems.append(
                            f"[S7] 档位不连续/有重叠：{b.get('id')} lower={lo}，"
                            f"上一档 upper={prev_upper}（应为 {prev_upper + 1}）"
                        )
                prev_upper = hi if hi is not None else float("inf")
            if bands[-1].get("upper_inclusive") is not None:
                problems.append(f"[S7] 末档 upper_inclusive 应为 null（无上限），实为 {bands[-1].get('upper_inclusive')}")

    # ---- S8：forms/*.json ----
    form_files = sorted(glob.glob(os.path.join(d, "forms", "*.json")))
    for f in form_files:
        rel = os.path.relpath(f, os.path.dirname(d)).replace("\\", "/")
        data = loaded.get(rel)
        if data is None:
            continue

        def walk_names(obj, path):
            if isinstance(obj, dict):
                nm = obj.get("name")
                if isinstance(nm, str) and BAD_FIELDNAME_RE.search(nm):
                    problems.append(f"[S8] {path} 字段名不规范（含 <br> 或空白）：{nm!r}（R-18）")
                for k, v in obj.items():
                    walk_names(v, f"{path}.{k}")
            elif isinstance(obj, list):
                for i, v in enumerate(obj):
                    walk_names(v, f"{path}[{i}]")

        walk_names(data, rel)

    # ---- S9：ledger-mapping.json 覆盖 L01–L12 ----
    lm = loaded.get("spec/ledger-mapping.json")
    if isinstance(lm, dict):
        codes = set()
        for k, v in lm.items():
            if k.startswith("_"):
                continue
            if isinstance(v, dict) and isinstance(v.get("code"), str):
                codes.add(v["code"])
            elif LEDGER_RE.match(k):
                codes.add(k)
        missing = [c for c in ALL_LEDGERS if c not in codes]
        if missing:
            problems.append(f"[S9] ledger-mapping.json 未覆盖台账：{', '.join(missing)}")

    if problems:
        print(f"FAIL [{NAME}] 共 {len(problems)} 处违规（已扫 {len(json_files)} 个 JSON）：")
        for p in problems:
            print("  " + p)
        return 1

    print(f"OK [{NAME}] spec/ 机读规格校验通过（{len(json_files)} 个 JSON，"
          f"{len(form_files)} 个表单 schema）")
    return 0


def main() -> int:
    return check()


if __name__ == "__main__":
    sys.exit(main())
