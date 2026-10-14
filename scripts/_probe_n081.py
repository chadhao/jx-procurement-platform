# -*- coding: utf-8 -*-
'''`N-081` 探针 —— 「`internal/httpapi/router.go` ↔ 接口正本 `docs/05-API.md`
路由集**严格双向等价**」的行为证明（不是「跑一下没报错」）。

用法：`python scripts/_probe_n081.py`

★★ 为什么存在（`N-081` 的根因，实测得出）：
  路由与本契约的**单向**把关此前只由 `audit_silent.py#C5`（**会报级**）承担，且其
  **前向**判据用 `endswith()`（**后缀**）匹配 —— 于是 `router.go` 里**组相对**的
  `POST /org/sync`（注册在 `/api/admin` 组下、归一为 `org/sync`）会被正本里**另一条**
  `POST /internal/org/sync`（归一为 `internal/org/sync`）**后缀命中** ⇒ 被判「已提及」
  ⇒ ★ **「改了 router、忘了改正本」这类漂移会全门禁绿灯溜过**。
  ★ 实证（`N-081` 受理前）：新增 `POST /api/admin/org/sync` 却**未**登记正本时，
  `bash scripts/check_all.sh` 的 `audit_silent#C5` 报 **「无命中」**、总判定**通过**。
  ⇒ 本探针**解析 `router.go` 的分组前缀**得到全路径，再与正本路由集**逐条严格比对**，
     把这一类漂移纳入**必绿可检**（本文件由 `check_probes.sh` 通配收录 ⇒ 属必绿「常驻探针回归」项）。

★ 判别口径（与生成器/审计**同语义**，但不复用其「松」的后缀匹配）：
  · `router.go`：`X := echo.New()` 与 `X := Y.Group("...")` 建前缀表 ⇒ 全路径 = 前缀 + 路径；
    `:id` 与 `{id}` 均归一为 `*`；★ 口径与 `gen_openapi.py` 一致：`GET /` 与 `GET /*`
    （前端 SPA 静态兜底）**不计**。
  · 正本：**复用** `scripts/gen_openapi.py#build()` 的抽取结果（**不重写抽取规则**，避免第三份真相）。
  · 等价：段数相同，且逐段相等或任一侧为 `*`。

★★ **次序特性（重要，别误读为失败）**：本探针比对的是**已提交版**的 `router.go` 与
   `docs/05-API.md`（`check_probes.sh` 在隔离 `git worktree` 内跑、真源取 **HEAD 版**）
   ⇒ ★ **必须与「router 注册 ＋ 正本登记」同批提交之后再跑门禁**；未提交即跑 ⇒ 探针看到旧文 ⇒ 必红。

★ 全程只读、零落盘：所有变异一律在**内存副本**上做（与 `_probe_n071.py` 同纪律），
  收尾核对 `router.go` 与 `docs/05-API.md` 的 `sha256` 与开跑前相同。

它证明六件事（★ 每条都是可复现实测，不是推断）：
  1. **双向等价（当前态）**：`router.go` 解析后路由集 == 正本路由集（两侧各 **71** 条），
     双向差集**皆空**。
  2. **鉴别力 A（正本有、router 无）**：把新增路由从 `router.go` **内存副本**删掉
     ⇒ 必须报「正本有、router 无」**恰 1 条**。
  3. **鉴别力 B（router 有、正本无）**：给 `router.go` **内存副本**凭空加一条未登记路由
     ⇒ 必须报「router 有、正本无」**恰 1 条**。
  4. ★★ **鉴别力 C（N-081 漏网现场重现）**：把正本里的 `#### POST /api/admin/org/sync`
     小节标题从**内存副本**删掉 ⇒ 必须报「router 有、正本无」**恰该 1 条** ——
     ★ 这一条正是 `audit_silent#C5`（后缀匹配）**漏掉**的那一笔：本探针抓得到、C5 抓不到。
     ⇒ 证明本判据**能分辨「未登记」态**，不是恒真空断言。
  5. ★★ **契约元数据 ↔ 实现/正本 一致**：`spec/openapi.json#x-source` 的
     `router_go_route_count`（== `router.go` 实解析数）· `route_count`（== 正本路由数）·
     `doc_version`（== 正本版本标记）· `doc_sha256`（== 正本 sha256）**四项逐一对上** ——
     ★ 这四个字段此前**全无判据绑定**（`N-081` 的「失真长期无人发现」即由此而来）。
  6. ★★ **鉴别力 D（`router_go_route_count` 失真）**：把该字段在**内存副本**改回 `70`
     ⇒ 必须报「≠ `router.go` 实解析 71」**恰 1 条** ⇒ 证明元数据判据能分辨失真态。
'''
import hashlib
import io
import json
import os
import re
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)
sys.path.insert(0, HERE)
import gen_openapi as G  # noqa: E402

P_ROUTER = os.path.join(ROOT, 'internal', 'httpapi', 'router.go')
P_DOC05 = os.path.join(ROOT, 'docs', '05-API.md')
P_OPENAPI = os.path.join(ROOT, 'spec', 'openapi.json')

# 本批（N-081）新增的路由（用于鉴别力用例的精准靶点）。
NEW_ROUTE = ('POST', '/api/admin/org/sync')
NEW_HEADING = '#### `POST /api/admin/org/sync`'

EXPECT_COUNT = 71  # 两侧基数（正本 71 ⇔ router 71；`GET /` 与 `GET /*` 不计）

RESULT = []
BEFORE = {}


def sha(path):
    with io.open(path, 'rb') as f:
        return hashlib.sha256(f.read()).hexdigest()


def text(path):
    with io.open(path, 'r', encoding='utf-8', newline='') as f:
        return f.read()


def ok(name, cond, detail=''):
    RESULT.append((bool(cond), name, detail))
    print(u'  %s %s%s' % (u'\u2713' if cond else u'\u2717', name,
                          (u'   [' + detail + u']') if detail else u''))


def norm(p):
    """路径归一：`:x` / `{x}` → `*`；折叠重复斜杠；去尾斜杠。
    ★ 与 `gen_openapi.norm_path` 同口径，但**不剥 `/api` 前缀** —— 因为本探针
    已把 `router.go` 的**分组前缀**解析进全路径，两侧都是**全路径**，直接比。"""
    p = re.sub(r':[A-Za-z_]\w*', '*', p)
    p = re.sub(r'\{[A-Za-z_]\w*\}', '*', p)
    p = re.sub(r'/+', '/', p)
    return p.rstrip('/')


def equiv(a, b):
    sa, sb = a.split('/'), b.split('/')
    return len(sa) == len(sb) and all(x == y or x == '*' or y == '*' for x, y in zip(sa, sb))


def parse_router(src):
    """解析 `router.go`，返回 [(method, 全路径归一)]（排除 SPA 兜底 `GET /` 与 `GET /*`）。

    ★ 分组解析：`X := echo.New()`（根前缀空）＋ `X := Y.Group("…")`（前缀拼接）。
      凡 `X.METHOD("path")` 即登记 `prefix[X] + path`。
    """
    prefix = {}
    m = re.search(r'(\w+)\s*:=\s*echo\.New\(\)', src)
    root = m.group(1) if m else 'e'
    prefix[root] = ''
    for m in re.finditer(r'(\w+)\s*:=\s*(\w+)\.Group\(\s*"([^"]*)"', src):
        v, parent, gp = m.group(1), m.group(2), m.group(3)
        prefix[v] = prefix.get(parent, '') + gp
    out = []
    for m in re.finditer(r'(\w+)\.(GET|POST|PUT|PATCH|DELETE)\(\s*"([^"]+)"', src):
        v, meth, path = m.group(1), m.group(2), m.group(3)
        full = prefix.get(v, '') + path
        if full == '/' or '*' in full:      # SPA 静态兜底：不计（与 gen_openapi 同口径）
            continue
        out.append((meth, norm(full)))
    return out


def docs_routes():
    """复用生成器抽取正本路由集（不重写规则）。"""
    _doc, routes, *_ = G.build()
    return [(m, norm(p)) for m, p, _k, _ln in routes]


def diff(router_set, docs_set):
    return (sorted(router_set - docs_set), sorted(docs_set - router_set))


def meta_problems(meta, router_n, docs_n, doc_ver, doc_sha):
    """`spec/openapi.json` 的 `x-source` 元数据 ↔ 实现（`router.go`）/ 正本（`docs/05-API.md`）一致性。

    ★ 绑定点（此前**全无判据**，`N-081` 即由此漏出）：`router_go_route_count` 是**硬编码**值
      （`gen_openapi.py`），既不与 `router.go` 实解析数比对、也不被任何门禁约束 ⇒ 可长期失真。
    """
    p = []
    src = meta.get('x-source') or {}
    if src.get('router_go_route_count') != router_n:
        p.append(u'x-source.router_go_route_count=%r ≠ router.go 实解析 %d'
                 % (src.get('router_go_route_count'), router_n))
    if src.get('route_count') != docs_n:
        p.append(u'x-source.route_count=%r ≠ 正本路由数 %d' % (src.get('route_count'), docs_n))
    if src.get('doc_version') != doc_ver:
        p.append(u'x-source.doc_version=%r ≠ 正本版本标记 %r' % (src.get('doc_version'), doc_ver))
    if src.get('doc_sha256') != doc_sha:
        p.append(u'x-source.doc_sha256 与正本 sha256 不一致（正本改过、契约未重生）')
    return p


def main():
    src = text(P_ROUTER)
    md = G.read_text(G.MD_PATH)

    rr_now = parse_router(src)
    dr_now = docs_routes()
    rset_now, dset_now = set(rr_now), set(dr_now)

    print(u'router.go（解析分组前缀后）: %d 条 · 正本: %d 条\n' % (len(rset_now), len(dset_now)))

    # ── 用例 1：双向等价（当前态）──
    only_r, only_d = diff(rset_now, dset_now)
    ok(u'1 双向等价（%d ⇔ %d，双向差集皆空）' % (len(rset_now), len(dset_now)),
       (len(rset_now) == EXPECT_COUNT and len(dset_now) == EXPECT_COUNT
        and not only_r and not only_d),
       u'router 有正本无 %s / 正本有 router 无 %s' % (only_r, only_d))

    # ── 用例 2：鉴别力 A —— 从 router 内存副本删掉新路由 ⇒ 「正本有、router 无」恰 1 条 ──
    mut = src.replace(
        u'admin.POST("/org/sync", d.handleAdminOrgSync)',
        u'// MUTANT: (registration removed in memory only)')
    cond = mut != src
    only_r2, only_d2 = diff(set(parse_router(mut)), dset_now)
    ok(u'2 ★ 鉴别力A：router 少注册新路由 ⇒ 报「正本有、router 无」恰 1 条',
       cond and only_d2 == [(u'POST', norm(NEW_ROUTE[1]))] and not only_r2,
       u'变异命中=%s · 正本有 router 无 %s' % (cond, only_d2))

    # ── 用例 3：鉴别力 B —— 给 router 内存副本凭空加一条 ⇒ 「router 有、正本无」恰 1 条 ──
    ghost = u'\tadmin.POST("/ghost/probe-n081", d.handleAdminOrgSync)\n'
    mut = src + u'\nfunc _probeGhost() {\n' + ghost + u'}\n'
    only_r3, only_d3 = diff(set(parse_router(mut)), dset_now)
    ok(u'3 ★ 鉴别力B：router 凭空多一条未登记路由 ⇒ 报「router 有、正本无」恰 1 条',
       only_r3 == [(u'POST', norm(u'/api/admin/ghost/probe-n081'))] and not only_d3,
       u'router 有正本无 %s' % only_r3)

    # ── 用例 4：★★ 鉴别力 C —— N-081 漏网现场重现（正本内存副本删掉新端点小节）──
    md_lines = md.split(u'\n')
    mut_lines = [ln for ln in md_lines if not ln.startswith(NEW_HEADING)]
    dset_mut = {(m, norm(p)) for m, p, _k, _ln in G.extract_routes(mut_lines)[0]}
    only_r4, only_d4 = diff(rset_now, dset_mut)
    ok(u'4 ★★ 鉴别力C：正本删掉 `POST /api/admin/org/sync` 小节 ⇒ 报「router 有、正本无」恰该 1 条',
       only_r4 == [(u'POST', norm(NEW_ROUTE[1]))] and not only_d4,
       u'router 有正本无 %s（★ 这一笔 audit_silent#C5 漏掉 —— 后缀匹配假阴性）' % only_r4)

    # ── 用例 5：`spec/openapi.json` 元数据 ↔ 实现/正本 一致 ──
    meta = json.load(io.open(P_OPENAPI, encoding='utf-8'))
    mv = re.search(r'^\|\s*版本\s*\|\s*\*\*(V[\d.]+)\*\*', md, re.M)
    doc_ver = mv.group(1) if mv else None
    probs5 = meta_problems(meta, len(rset_now), len(dset_now), doc_ver, sha(P_DOC05))
    ok(u'5 契约元数据一致（router_go_route_count / route_count / doc_version / doc_sha256 ↔ 实现·正本）',
       not probs5, u' / '.join(probs5) or u'四项皆一致')

    # ── 用例 6：★★ 鉴别力 D —— 把 `router_go_route_count` 改回 70（内存副本）⇒ 必须报红 ──
    mut_meta = json.loads(json.dumps(meta))
    mut_meta['x-source']['router_go_route_count'] = 70
    probs6 = meta_problems(mut_meta, len(rset_now), len(dset_now), doc_ver, sha(P_DOC05))
    ok(u'6 ★★ 鉴别力D：把 `router_go_route_count` 改回 70 ⇒ 报「≠ router.go 实解析 71」',
       len(probs6) == 1 and u'router_go_route_count' in probs6[0], u'%s' % probs6)

    # ── 收尾：仓库真源逐字节未变（探针只在内存里变异）──
    print(u'\n== 收尾：真源逐字节核对 ==')
    for p in BEFORE:
        ok(u'未改动 %s' % os.path.relpath(p, ROOT).replace(os.sep, u'/'), sha(p) == BEFORE[p])

    bad = [r for r in RESULT if not r[0]]
    print(u'\n===== 探针结果：%d/%d 通过 =====' % (len(RESULT) - len(bad), len(RESULT)))
    for _c, name, detail in bad:
        print(u'  %s %s  %s' % (u'\u2717', name, detail))
    raise SystemExit(1 if bad else 0)


if __name__ == u'__main__':
    for _p in (P_ROUTER, P_DOC05, P_OPENAPI):
        BEFORE[_p] = sha(_p)
    main()
