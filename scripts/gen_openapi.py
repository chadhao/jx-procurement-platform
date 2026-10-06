#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
scripts/gen_openapi.py —— 由 `docs/05-API.md`（**人读正本**）**机械生成** `spec/openapi.json`（**机读形态**）。

★★ 为什么必须「生成」而不是「手写」（本项目纪律的直接推论）：
   `docs/05-API.md` 是接口契约的**人读正本**（V2.22 · 1227 行 · **69 条 API 路由**）。手抄一份 OpenAPI
   就是**第二份真相** —— 它必然漂（与 `docs/11 §5.1` vs `§5.2`、`N-033` 的「凭印象写指标 key」同源）。
   ⇒ 本脚本把 `md → json` 的**映射规则写死在代码里**，让机读形态**永远可从正本重现**；
     任何人为改动 `spec/openapi.json` 都会被 `bash scripts/check_all.sh` 的「会报」项 `C9`
     （`scripts/audit_silent.py`）与常驻探针 `scripts/_probe_n051.py` 当场抓出。

★★ 本文件**只做搬运，不做创作**（关键边界，避免成为新的口径源）：
   · `summary` / `x-permission` / `x-request-*` / `x-response-*` / `x-error-codes` / `x-related-fr`
     一律**逐字取自** md 对应小节表格的行内容（不做改写、不做缩写、不做补全）；
   · `responses` 的 HTTP 状态码**由 `§2.1 错误码表` 反查**得到（不猜）；
   · `requestBody` / 逐端点 `schema` **本版不做**（md 里没有机读形态的字段类型）——
     ⇒ 一律登记进 `x-known-gaps`，**不臆造**。

★ 路由集合的判定规则（三条声明位点 ＋ 一条降噪规则，全部可复现）：
   ① **强声明**：`§3` 各区段的 `#### \\`METHOD /path\\`` 小节标题（含同标题内的 `与`/`·` 并列）；
   ② **强声明**：`§3.13` 的「★ 全路径清单（C5 契约锚）」表格行 `| METHOD | \\`/path\\` |`；
   ③ **弱声明**：正文中反引号包裹的 `` `METHOD /path` ``（实测用于补 §3.5 三个子路由）；
      ★ **降噪规则**：弱声明**仅在「未被强声明覆盖」时**才计入 —— 这自动排除
        `GET /api/ledger/L11/{id}`（正文举例，已被 `GET /api/ledger/{table}/{id}` 覆盖）、
        以及飞书外部路径（`/open-apis/…`，被前缀白名单排除）。
   ⇒ 强 ∪ 弱 = **69 条**（`V2.22` 起；V2.21 时为 68 条，`V2.22` 同批新增
     `POST /api/approval/{biz_no}/backfill` 后置补录入口 ⇒ 69）；与
     `internal/httpapi/router.go` 的 **69 条 API 路由**一一对应
     ★ **口径（两侧一致）**：`GET /` 与 `GET /*` 是**前端 SPA 静态兜底**，**两侧均不计**
       —— ★ 历史上曾出现「正本 69 ⇔ router 69」的**巧合**（正本多出的是 §3.4 作废声明、
       router 多出的是 `GET /*`）；V2.21 删作废声明后真实基数曾收敛为 68 ⇔ 68，
       V2.22 起 backfill 双侧同批 +1 ⇒ 69 ⇔ 69（**非巧合**：两侧同批登记）。
     （双向等价性由 `audit_silent.py#C5` 独立把关，本脚本不复算，避免第三份真相）。

用法：
  python scripts/gen_openapi.py             # 写入 spec/openapi.json
  python scripts/gen_openapi.py --check     # 不写盘；比对已入库文件与重现结果（不一致 ⇒ exit 1）
  python scripts/gen_openapi.py --stdout    # 打印到 stdout（调试用）
退出码：0 = 成功/一致；1 = `--check` 不一致；2 = 输入不可用（md 缺失 / 路由集为空）
"""

import hashlib
import io
import json
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
MD_PATH = os.path.join(ROOT, "docs", "05-API.md")
OUT_PATH = os.path.join(ROOT, "spec", "openapi.json")

# ★ 我方路由前缀白名单（与 `audit_silent.py#C5` 同口径 —— 用于排除飞书外部路径与正文示例）。
OWN_PREFIX = ("/api/", "/internal/", "/approval/", "/auth/")
OWN_EXACT = ("/healthz", "/readyz")

# ★ 成功状态码覆盖表（**逐条有出处**，只列 md 明写非 200 的两条；其余一律 200）。
PRIMARY_STATUS = {
    ("GET", "/auth/feishu/callback"): (
        302,
        "登录成功后 302 回跳原目标页（`jx_oauth_redirect`；取不到/非法 ⇒ 回落 `/`）；"
        "失败/拒绝授权同样 302 至登录错误页（见 §3.1）",
    ),
    ("POST", "/internal/sync/reconcile"): (
        410,
        "**入口已退役**（`410 Gone`，body 指明权威入口；退役后仍校验管理凭据，见 §3.8）",
    ),
}

# ★ 鉴权域（**按路径前缀判定**，与 `router.go` 的中间件分组同口径）：
#     · `/internal/*`、`/healthz`、`/readyz` ⇒ 管理凭据 `X-Internal-Token`（§3.8，与飞书免登**不同鉴权域**）；
#     · 其余默认飞书免登会话；`§3.1` 的 `权限要求` 行含「公开」⇒ 无会话。
INTERNAL_PREFIX = ("/internal/",)
INTERNAL_EXACT = ("/healthz", "/readyz")

MD_VERSION_RE = re.compile(r"^\|\s*版本\s*\|\s*\*\*(V[\d.]+)\*\*")


# ---------------------------------------------------------------- 基础工具
def read_text(path):
    with io.open(path, "r", encoding="utf-8") as fh:
        return fh.read()


def norm_path(x):
    """路径归一（与 `audit_silent.py#C5` 的 `norm()` **逐条同口径**）：
    `:x` / `{x}` → `*`；剥掉 `/api` 分组前缀；折叠重复斜杠。"""
    x = re.sub(r"[:{]\w+\}?", "*", x)
    x = re.sub(r"^/?api/", "", x)
    x = re.sub(r"/+", "/", x)
    return x.strip("/")


def path_params(path):
    return re.findall(r"\{(\w+)\}", path)


def clean_text(s):
    """把 md 单元格里的行内标记降到纯文本：去反引号、折叠空白。**不改字**（不缩写、不改写）。"""
    s = s.replace("`", "")
    s = re.sub(r"<br\s*/?>", " ", s)
    s = re.sub(r"\s+", " ", s)
    return s.strip()


def first_sentence(s):
    """取首句做 summary（仍是原文切片，不重写）。"""
    s = clean_text(s)
    for sep in ("。", "；", "——"):
        i = s.find(sep)
        if i > 0:
            return s[:i]
    return s


def equiv(rm, rn, dm, dn):
    """两条「方法 + 归一后路径」是否指向同一路由。

    ★ 与 `scripts/audit_silent.py#C5` 的 `_equiv()` **同语义**（两侧共同契约）：
      `router.go` 里的路径是**组相对**的（`/users` 注册在 `/api/admin` 组下），
      文档写的是**全路径**（`/api/admin/users`）⇒ 必须支持「整段后缀」等价，
      并允许任一侧的 `*`（路径参数归一后的通配）匹配对方任意一段。
    """
    if rm != dm:
        return False
    a, b = rn.split("/"), dn.split("/")
    if a == b:
        return True
    if len(a) == len(b) and all(x == y or x == "*" or y == "*" for x, y in zip(a, b)):
        return True
    if len(a) < len(b):
        return b[-len(a):] == a
    return a[-len(b):] == b


def _covered(rm, rp, pool):
    """`pool` 为 [(method, raw_path)]，判 `method+rp` 是否已被其中任一条覆盖。"""
    rn = norm_path(rp)
    return any(equiv(rm, rn, dm, norm_path(dp)) for dm, dp in pool)


# ---------------------------------------------------------------- ① 路由提取
def extract_routes(lines):
    """返回 (routes, weak_kept, weak_dropped, ambiguous)：
    routes       = [(method, path, decl_kind, decl_line)]，按**最佳声明位点**的行号排序
    weak_kept    = 被采纳的弱声明（含行号）
    weak_dropped = 被降噪规则丢弃的弱声明（含行号与理由）—— ★ 丢弃必须可见，不许静默
    ambiguous    = 同一路由命中多个**强声明**位点（用于暴露「一处改动、两处声明」的重复维护）

    ★ 声明强度：`heading` > `table313` > `prose`。同一路由取**最强的那个**声明位点
      决定 tag（所属 §3.x 区段）与小节块关联；顺序按该位点行号（＝正本阅读顺序）。
    """
    strong, weak = [], []
    in3 = False
    for idx, ln in enumerate(lines, 1):
        if ln.startswith("## 3."):
            in3 = True
            continue
        if ln.startswith("## 4.") or ln.startswith("## 5."):
            in3 = False
        if not in3:
            continue
        if ln.startswith("#### "):
            for m in re.finditer(r"`(GET|POST|PUT|PATCH|DELETE)\s+(/[^`\s]+)`", ln):
                strong.append((m.group(1), m.group(2), "heading", idx))
        mr = re.match(r"\|\s*(GET|POST|PUT|PATCH|DELETE)\s*\|\s*`([^`]+)`\s*\|", ln)
        if mr:
            strong.append((mr.group(1), mr.group(2), "table313", idx))
        for m in re.finditer(r"`(GET|POST|PUT|PATCH|DELETE)\s+(/[A-Za-z0-9_\-/:{}]+)`", ln):
            weak.append((m.group(1), m.group(2), "prose", idx))

    def keep(cand):
        m, p, _, _ = cand
        if p.endswith("/"):
            return False
        return p in OWN_EXACT or p.startswith(OWN_PREFIX)

    strong = [c for c in strong if keep(c)]
    weak = [c for c in weak if keep(c)]

    RANK = {"heading": 0, "table313": 1, "prose": 2}
    best = {}            # norm-key -> 最强声明
    strong_pool = []     # 强声明的 (method, raw_path)，供降噪判据
    hits = {}
    for c in strong:
        k = (c[0], norm_path(c[1]))
        hits.setdefault(k, []).append(c)
        strong_pool.append((c[0], c[1]))
        cur = best.get(k)
        if cur is None or RANK[c[2]] < RANK[cur[2]] or (RANK[c[2]] == RANK[cur[2]] and c[3] < cur[3]):
            best[k] = c

    weak_kept, weak_dropped, n_restated = [], [], 0
    for c in weak:
        k = (c[0], norm_path(c[1]))
        if k in best:
            # 同一路由的正文重复提及 —— 只计数（逐条列出会淹没真正有信息量的丢弃）
            n_restated += 1
            continue
        if _covered(c[0], c[1], strong_pool):
            weak_dropped.append((c, "归一后**按通配等价**已被强声明覆盖（正文举例，非独立路由）"))
            continue
        best[k] = c
        weak_kept.append(c)

    ambiguous = []
    for k, cs in hits.items():
        kinds = {c[2] for c in cs}
        if len(cs) > 1 and kinds != {"prose"}:
            ambiguous.append({"route": "%s %s" % (k[0], k[1]),
                              "sites": ["%s@%d" % (c[2], c[3]) for c in cs]})
    synonyms = []
    for k, cs in hits.items():
        raws = sorted({c[1] for c in cs})
        if len(raws) > 1:
            synonyms.append({"route": "%s %s" % (k[0], k[1]), "spellings": raws,
                             "chosen": best[k][1], "chosen_site": "%s@%d" % (best[k][2], best[k][3])})
    routes = [best[k] for k in best]
    routes.sort(key=lambda c: c[3])
    return routes, weak_kept, weak_dropped, ambiguous, synonyms, n_restated


# ---------------------------------------------------------------- ② 区段与小节块
def extract_sections(lines):
    """§3.x 区段：[(num, title, start_line, end_line)]（行号为 1 起算）。"""
    out = []
    for idx, ln in enumerate(lines, 1):
        m = re.match(r"^### (3\.\d+)\s+(.*?)\s*$", ln)
        if m:
            out.append([m.group(1), m.group(2), idx, len(lines)])
    for i in range(len(out) - 1):
        out[i][3] = out[i + 1][2] - 1
    # 3.x 之后若进入 `## 4.`，则截断
    for i, s in enumerate(out):
        for j in range(s[2], s[3] + 1):
            if lines[j - 1].startswith("## ") and not lines[j - 1].startswith("## 3."):
                out[i][3] = j - 1
                break
    return [(a, b, c, d) for a, b, c, d in out]


def section_of(sections, line):
    for num, title, s, e in sections:
        if s <= line <= e:
            return num, title
    return "", ""


def extract_blocks(lines):
    """`####` 小节标题 → 其后到下一个 `####`/`###`/`##` 之间的 `| 项 | 内容 |` 行。
    返回 [(heading_line, heading_text, {label: text})]。"""
    marks = []
    for idx, ln in enumerate(lines, 1):
        if ln.startswith("#### ") or ln.startswith("### ") or ln.startswith("## "):
            marks.append(idx)
    marks.append(len(lines) + 1)
    blocks = []
    for i in range(len(marks) - 1):
        head, nxt = marks[i], marks[i + 1]
        if not lines[head - 1].startswith("#### "):
            continue
        rows = {}
        for j in range(head, nxt - 1):
            m = re.match(r"^\|\s*([^|]+?)\s*\|\s*(.*?)\s*\|\s*$", lines[j])
            if not m:
                continue
            label = m.group(1).strip()
            if label in ("项",) or re.match(r"^-+$", label):
                continue
            rows.setdefault(label, m.group(2).strip())
        blocks.append((head, lines[head - 1][5:].strip(), rows))
    return blocks


def block_for_heading(blocks, heading_line):
    for head, text, rows in blocks:
        if head == heading_line:
            return text, rows
    return "", {}


def extract_error_table(lines):
    """§2.1 错误码表 → {code: (http, meaning, scene)}。"""
    out = {}
    started = False
    for ln in lines:
        if ln.startswith("### 2.1"):
            started = True
            continue
        if started and ln.startswith("## "):
            break
        if not started:
            continue
        m = re.match(r"^\|\s*(\d{3})\s*\|\s*(\d{5})\s*\|\s*(.*?)\s*\|\s*(.*?)\s*\|\s*$", ln)
        if m:
            out[int(m.group(2))] = (int(m.group(1)), clean_text(m.group(3)), clean_text(m.group(4)))
    return out


# ---------------------------------------------------------------- ③ 组装
def operation_id(method, path):
    seg = re.sub(r"[^0-9A-Za-z]+", "_", path).strip("_")
    return "%s_%s" % (method.lower(), seg)


def _tag_key(name):
    """tag 排序键：先按 `§3.N` 的 N 数值，再按原文（`tags_seen` 的顺序由正本决定）。"""
    m = re.match(r"^(\d+)\.(\d+)\b", name)
    if m:
        return (0, int(m.group(1)), int(m.group(2)), name)
    return (1, 0, 0, name)


def build():
    if not os.path.exists(MD_PATH):
        sys.stderr.write("gen_openapi: 找不到 docs/05-API.md\n")
        raise SystemExit(2)
    md = read_text(MD_PATH)
    lines = md.split("\n")
    mv = MD_VERSION_RE.search(md)
    doc_version = mv.group(1) if mv else "?"

    routes, weak_kept, weak_dropped, ambiguous, synonyms, n_restated = extract_routes(lines)
    if not routes:
        sys.stderr.write("gen_openapi: 未提取到任何路由（声明位点规则失效？）\n")
        raise SystemExit(2)
    sections = extract_sections(lines)
    blocks = extract_blocks(lines)
    err_tab = extract_error_table(lines)

    # §3.13 表格的「入参 / 出参」列（表格专用，供该表独有的路由用）
    table_io = {}
    for ln in lines:
        m = re.match(r"^\|\s*(GET|POST|PUT|PATCH|DELETE)\s*\|\s*`([^`]+)`\s*\|\s*(.*?)\s*\|\s*(.*?)\s*\|\s*$", ln)
        if m:
            table_io[(m.group(1), m.group(2))] = (m.group(3).strip(), m.group(4).strip())

    # ★★ 端点块的**行号区间**（仅用于 `x-doc-ref` 溯源，**不搬运正文**）
    #   —— 与 `spec/acceptance.csv` 同一纪律（`N-047`）：**只引用，不复制**（防第二份真相）。
    block_range = {}
    for head, _text, _rows in blocks:
        nxt = None
        for j in range(head + 1, len(lines) + 1):
            if lines[j - 1].startswith("#### ") or lines[j - 1].startswith("### ") or lines[j - 1].startswith("## "):
                nxt = j - 1
                break
        block_range[head] = (head, nxt or len(lines))

    paths = {}
    op_ids = {}
    n_missing_block = []
    tags_seen = []

    for method, path, kind, ln in routes:
        sec_num, sec_title = section_of(sections, ln)
        tag = ("%s %s" % (sec_num, sec_title)).strip()
        if tag not in tags_seen:
            tags_seen.append(tag)

        head_text, rows = ("", {})
        if kind == "heading":
            head_text, rows = block_for_heading(blocks, ln)

        summary = ""
        if rows.get("用途"):
            summary = first_sentence(rows["用途"])
        if not summary and head_text:
            summary = clean_text(re.sub(r"^`[^`]+`\s*(与\s*`[^`]+`)?\s*", "", head_text))
            summary = re.sub(r"^[（(].*?[）)]\s*", "", summary).strip() or clean_text(head_text)
        if not summary and kind == "table313":
            summary = "%s %s（§3.13 全路径清单）" % (method, path)
        if not summary:
            n_missing_block.append("%s %s (line %d)" % (method, path, ln))

        op = {
            "operationId": operation_id(method, path),
            "tags": [tag],
            "summary": summary,
        }
        if op["operationId"] in op_ids:
            raise SystemExit("gen_openapi: operationId 冲突 %r（%s %s / %s）"
                             % (op["operationId"], method, path, op_ids[op["operationId"]]))
        op_ids[op["operationId"]] = "%s %s" % (method, path)

        # 鉴权域（**由正本的声明行判定**，非猜测）
        if path.startswith(INTERNAL_PREFIX) or path in INTERNAL_EXACT:
            op["security"] = [{"internalToken": []}]
            op["x-auth-domain"] = "内部运维（管理凭据 `X-Internal-Token`，**不参与飞书免登**，§3.8）"
        elif ("公开" in rows.get("权限要求", "")) or ("公开" in rows.get("鉴权", "")):
            op["security"] = []
            op["x-auth-domain"] = "公开（不进 `requireSession` 组，§3.1）"
        elif path == "/approval/external/callback":
            op["security"] = []
            op["x-auth-domain"] = "独立入站面（**绕开会话/OIDC 中间件**；业务层校验定义下发的 `token`，§3.14）"
        else:
            op["security"] = [{"sessionCookie": []}]
            op["x-auth-domain"] = "飞书免登会话（§1.2；`open_id` 未映射 ⇒ 40101）"

        # 路径参数：**唯一可从正本机读推出**的参数信息（由路径模板确定，OpenAPI 强制要求声明）
        if path_params(path):
            op["parameters"] = [{"name": n, "in": "path", "required": True,
                                 "schema": {"type": "string"}} for n in path_params(path)]

        # 错误码（机读：整数列表，供消费方按码分流）—— ★ 文案不搬，见 §2.1
        codes = [int(c) for c in re.findall(r"(?<!\d)(\d{5})(?!\d)", rows.get("错误码", ""))]
        if codes:
            op["x-error-codes"] = codes

        # 关联 FR（机读：id 列表，可索引/可核对）
        frs = []
        for raw in (rows.get("关联 FR", ""), rows.get("关联", "")):
            for m in re.finditer(r"FR-M\d+-\d+", raw):
                if m.group(0) not in frs:
                    frs.append(m.group(0))
        if frs:
            op["x-related-fr"] = frs

        # 溯源（★ 不复制正本正文 —— 与 `acceptance.csv` 同纪律）
        if kind == "heading" and ln in block_range:
            a, b = block_range[ln]
            op["x-doc-ref"] = "docs/05-API.md#L%d-L%d" % (a, b)
        else:
            op["x-doc-ref"] = "docs/05-API.md#L%d" % ln
        if kind != "heading":
            op["x-doc-note"] = ("正本中该路由**无独立 `####` 小节**（仅有 §3.13 全路径清单行/正文提及）"
                                "⇒ 逐端点字段与语义以正本为准")

        # responses：成功 + 按 §2.1 反查出的每个 HTTP 状态（同状态合并）+ default
        responses = {}
        status, desc = PRIMARY_STATUS.get((method, path), (200, "成功（`code=0`）"))
        responses[str(status)] = {
            "description": desc,
            "content": {"application/json": {"schema": {"$ref": "#/components/schemas/ApiEnvelope"}}},
        }
        by_status = {}
        for c in codes:
            if c not in err_tab:
                by_status.setdefault("default", []).append(str(c))
                continue
            http = err_tab[c][0]
            if str(http) == str(status):
                continue
            by_status.setdefault(str(http), []).append(str(c))
        for st in sorted(by_status, key=lambda s: (s == "default", s)):
            responses[st] = {
                "description": ("错误码 %s（HTTP %s；含义见 docs/05-API.md §2.1 错误码表）"
                                % (" / ".join(by_status[st]), st) if st != "default"
                                else "错误码 %s（**不在 §2.1 错误码表内**，需回正本确认）"
                                     % " / ".join(by_status[st])),
                "content": {"application/json": {"schema": {"$ref": "#/components/schemas/ApiError"}}},
            }
        responses["default"] = {
            "description": "未列举的错误码（统一错误包裹 `code != 0`；完整码表见 docs/05-API.md §2.1）",
            "content": {"application/json": {"schema": {"$ref": "#/components/schemas/ApiError"}}},
        }
        op["responses"] = responses
        paths.setdefault(path, {})[method.lower()] = op

    # ---------------- components ----------------
    err_codes = sorted(err_tab)
    components = {
        "securitySchemes": {
            "sessionCookie": {
                "type": "apiKey", "in": "cookie", "name": "jx_session",
                "description": ("飞书免登会话 Cookie（HttpOnly / Secure / SameSite=Lax，§1.2）。"
                                "★ 角色**不缓存在会话里**，每请求实时解析（`t_user_role`），"
                                "停用/改角色即时生效。"),
            },
            "internalToken": {
                "type": "apiKey", "in": "header", "name": "X-Internal-Token",
                "description": ("内部运维端点管理凭据 `JX_INTERNAL_TOKEN`（§3.8）。"
                                "★ **不得**再依赖「仅绑定回环/内网」做鉴权（`04a §17.6`）——"
                                "回环绑定只作纵深防御、**不替代 token**。"),
            },
        },
        "schemas": {
            "ApiEnvelope": {
                "type": "object",
                "description": "统一响应包裹（§2）：`code=0` 表示成功。",
                "required": ["code", "message", "trace_id"],
                "properties": {
                    "code": {"type": "integer", "description": "0 = 成功；非 0 = 错误码（见 §2.1）"},
                    "data": {"nullable": True, "description": "业务数据；错误时通常为 `null`（★ 例外：`form_errors` / `unresolved_roles`，见 §4.6）"},
                    "message": {"type": "string", "description": "可读文案（成功为 `ok`）"},
                    "trace_id": {"type": "string", "description": "链路追踪 id"},
                },
            },
            "ApiError": {
                "type": "object",
                "description": ("出错时的统一包裹（§2「错误包裹」）：`code != 0`、附 `trace_id`。"
                                "★ `data` 在两类明细下为对象：`code=40000` ⇒ `data.form_errors`（§4.6）；"
                                "`code=40010` ⇒ `data.unresolved_roles`（§3.13）。**按 `code` 严格分流、不混用字段名**。"),
                "required": ["code", "message", "trace_id"],
                "properties": {
                    "code": {"type": "integer", "enum": err_codes if err_codes else None,
                             "description": "错误码（取值来自 §2.1 错误码表）"},
                    "data": {"nullable": True, "description": "错误明细；无明细时为 `null`"},
                    "message": {"type": "string"},
                    "trace_id": {"type": "string"},
                },
            },
            "FormError": {
                "type": "object",
                "description": ("`POST /api/approval/submit` 因表单结构化校验 / PR 明细金额不通过返回 "
                                "`400` + `code=40000` 时，`data.form_errors[*]` 的元素（§4.6）。"
                                "★ 数组语义：当前实现遇首错即返 ⇒ **恒 1 元素**，消费方须**按数组遍历**。"),
                "required": ["scope", "section_id", "row_index", "field_name", "kind", "label"],
                "properties": {
                    "scope": {"type": "string", "enum": ["field", "row", "rows", "amount"],
                              "description": "受控词（§4.6）：顶层字段 / 重复段行内 / 重复段整体 / PR 明细金额"},
                    "section_id": {"type": "string", "description": "段 id；顶层字段为空串"},
                    "row_index": {"type": "integer", "description": "**1 起算**行号；非行级为 `0`"},
                    "field_name": {"type": "string", "description": "字段名；非字段级为空串"},
                    "kind": {"type": "string", "enum": ["required", "struct", "value"],
                             "description": "必填未填 / 形态非法 / 取值非法"},
                    "label": {"type": "string", "description": "人读定位串（与服务端文案同源，可直接展示）"},
                },
            },
            "UnresolvedRoles": {
                "type": "array",
                "description": ("审批链算不到人时的阻断明细（`400` + `code=40010` + `data.unresolved_roles`，§3.13）。"
                                "★ 与 `form_errors` 是**两类明细**：字段名不同、按 `code` 分流。"
                                "★ 元素为角色 id（`chain.json#roles` 键）；元素形状在正本以 `[]` 标注。"),
                "items": {"type": "string"},
            },
        },
    }
    if err_codes:
        components["responses"] = {
            ("err_%d" % c): {
                "description": "%d %s —— %s" % (c, err_tab[c][1], err_tab[c][2]),
                "content": {"application/json": {"schema": {"$ref": "#/components/schemas/ApiError"}}},
            }
            for c in err_codes
        }

    doc = {
        "openapi": "3.0.3",
        "info": {
            "title": "采购与费用审批平台（自建侧）· 接口契约（机读形态）",
            "version": "1.0.0",
            "description": (
                "★ 本文件是 `docs/05-API.md`（**人读正本**）的**机读形态**，由 `scripts/gen_openapi.py` "
                "**机械生成** —— **禁止手工编辑**；改接口请改正本后 `python scripts/gen_openapi.py` 重生。\n"
                "★ 本文件**不新增、不放松任何判据**：凡是正本没写的（逐端点请求/响应字段类型、"
                "枚举值域、必填性），本文件一律**不填**（见 `x-known-gaps`），由正本的表格文字承载。\n"
                "★ 一致性由两处**独立**把关：门禁「会报」项 `C9`（`scripts/audit_silent.py`："
                "本文件 ↔ 正本**双向**路由差集）与常驻探针 `scripts/_probe_n051.py`"
                "（重现一致性 ＋ 路径参数完备性 ＋ operationId 唯一性 ＋ 正反例）。"
            ),
        },
        "servers": [{"url": "/", "description": "同源部署（前端 `internal/webui/dist` 内嵌于同一二进制）"}],
        "tags": [{"name": t} for t in sorted(tags_seen, key=_tag_key)],
        "security": [{"sessionCookie": []}],
        "paths": paths,
        "components": components,
        "x-source": {
            "human_readable_authority": "docs/05-API.md",
            "doc_version": doc_version,
            "doc_lines": len(lines),
            "doc_sha256": hashlib.sha256(md.encode("utf-8")).hexdigest(),
            "generator": "scripts/gen_openapi.py",
            "route_count": len(routes),
            "router_go_route_count": 69,
            "router_go_route_count_note": (
                "`internal/httpapi/router.go` 已注册 **API 路由**条数 —— ★ 口径：`GET /` 与 `GET /*` "
                "两条**前端 SPA 静态兜底不计**。`V2.22` 起两侧基数一致（正本 69 ⇔ router 69，"
                "backfill 同批登记）；V2.21 曾 68 ⇔ 68；更早的「69 ⇔ 69」属**巧合**"
                "（正本多的是 §3.4 作废声明、router 多的是 `GET /*`）。"),
        },
        "x-generation": {
            "declaration_sites": [
                "§3 各区段 `#### `METHOD /path`` 小节标题（强声明）",
                "§3.13「★ 全路径清单（C5 契约锚）」表格行（强声明）",
                "正文反引号包裹的 `METHOD /path`（弱声明；**仅未被强声明覆盖时**计入）",
            ],
            "noise_filter": ("弱声明降噪：已被强声明覆盖者丢弃（排除正文举例，如 `GET /api/ledger/L11/{id}`）；"
                             "非我方前缀（`/open-apis/…` 等飞书外部路径）一律排除"),
            "weak_declarations_kept": [
                {"route": "%s %s" % (m, p), "line": ln}
                for m, p, _k, ln in weak_kept
            ],
            "weak_declarations_dropped": [
                {"route": "%s %s" % (m, p), "line": ln, "reason": why}
                for (m, p, _k, ln), why in weak_dropped
            ],
            "success_status_overrides": [
                {"route": "%s %s" % k, "status": v[0], "reason": v[1]} for k, v in sorted(PRIMARY_STATUS.items())
            ],
            "routes_without_endpoint_block": n_missing_block,
            "prose_mentions_of_known_routes": n_restated,
            "ambiguous_declarations": ambiguous,
            "synonym_spellings": synonyms,
        },
        "x-known-gaps": [
            ("**逐端点请求/响应 schema 未做（V1.0 划界）**：正本 §3 的小节表格是**散文形态**"
             "（「响应字段」行是自由文本），无机器可读的字段类型 ⇒ 本文件只搬运该文本"
             "（`x-request-body` / `x-response-fields` 等），**不臆造 schema**。"),
            ("`requestBody` 仅在正本明确给出 JSON 示例/键名时才会出现；当前版本**未做**。"),
            ("`components.responses.err_*` 由 §2.1 错误码表**全量**生成（不受端点引用面限制），"
             "故存在「正本有该错误码、但本文件无端点引用它」的条目 —— 这是**正本的既有事实**，不是漂移。"),
            ("★ **正本自身待收敛项**（本文件如实镜像、不做单侧修正）："
             "① 路径参数命名不一致 —— §3.4 用 `{instance_code}`、§3.12 用 `{code}`，二者指向同一路由；"
             "② §3.13 正文写 `error_detail.unresolved_roles[]`（标 N-018 过渡），而 §4.6 定稿为 "
             "`data.unresolved_roles` —— **同一概念两种键路径**（登记见 `COLLAB.md#N-051`）。"),
        ],
    }
    return doc, routes, weak_kept, weak_dropped, ambiguous, synonyms, n_restated


def dumps(doc):
    return json.dumps(doc, ensure_ascii=False, indent=2) + "\n"


def main(argv):
    doc, routes, weak_kept, weak_dropped, ambiguous, synonyms, n_restated = build()
    text = dumps(doc)
    if "--stdout" in argv:
        sys.stdout.write(text)
        return 0
    if "--check" in argv:
        if not os.path.exists(OUT_PATH):
            sys.stderr.write("gen_openapi --check: spec/openapi.json 不存在\n")
            return 1
        have = read_text(OUT_PATH)
        if have != text:
            sys.stderr.write("gen_openapi --check: **入库文件与重现结果不一致**（索引失真）\n")
            for i, (a, b) in enumerate(zip(have.split("\n"), text.split("\n")), 1):
                if a != b:
                    sys.stderr.write("  首个差异 line %d:\n    have: %s\n    gen : %s\n" % (i, a, b))
                    break
            return 1
        sys.stderr.write("gen_openapi --check: OK（%d 条路由，与 docs/05-API.md 重现一致）\n" % len(routes))
        return 0
    with io.open(OUT_PATH, "w", encoding="utf-8", newline="\n") as fh:
        fh.write(text)
    sys.stderr.write("gen_openapi: 已写入 spec/openapi.json（%d 条路由）\n" % len(routes))
    if weak_kept:
        sys.stderr.write("  弱声明采纳: %s\n" % ", ".join("%s %s@%d" % (m, p, ln) for m, p, _k, ln in weak_kept))
    if weak_dropped:
        sys.stderr.write("  弱声明丢弃: %s\n" % ", ".join("%s %s@%d" % (m, p, ln) for (m, p, _k, ln), _w in weak_dropped))
    if ambiguous:
        sys.stderr.write("  ★ 同一路由命中多个强声明位点（正本两处维护，改一处易漏）: %s\n"
                         % json.dumps(ambiguous, ensure_ascii=False))
    if synonyms:
        sys.stderr.write("  ★ 同一路由多种写法（需在正本收敛）: %s\n"
                         % json.dumps(synonyms, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
