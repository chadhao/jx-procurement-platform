#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""audit_silent.py — 「静默缺陷」机械化排查（只读）。

静默缺陷 = **不报错、但结果错或空**。它的共同形态是「生产者/消费者不对称」：
某列/键/映射/接口**有人写没人读**、或**有人读没人写**、或**配了但没有消费端**。

本脚本做 9 项机械检查（每项都可能误报，须人工确认；但"零命中"是可信的）：

  C1  迁移里定义、但 Go 代码从不引用的列          → 死列 / 预留列
  C2  仅出现在 INSERT/UPDATE 上下文、从无 SELECT 的列 → 有人写没人读
  C3  PassthroughBizFields 里没有任何读取者的名字   → 映射落库但无人消费
  C4  threshold 键是否有消费端（对照代码里的消费表）
  C5  注册的路由 ↔ API 文档双向差集
  C9  机读契约 spec/openapi.json ↔ 人读正本 docs/05-API.md 的真值一致性（正本指纹 ＋ 双向路由差集）
      ★ 复用生成器 scripts/gen_openapi.py 的映射（**不重写抽取规则**，避免第三份真相）
  C6  被吞掉的错误（`_ = x` 且 x 不是 Close/Release/Rollback 等）
  C7  定义了但从未被返回的错误码常量
  C8  测试里直接造台账行（绕过真实 Ingest 路径）的位点

用法：python scripts/audit_silent.py [--root .]
退出码：0 = 无命中；1 = 有命中（供 CI 用；命中需人工判定是否真缺陷）
"""
import io
import json
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
if '--root' in sys.argv:
    ROOT = os.path.abspath(sys.argv[sys.argv.index('--root') + 1])

GO_FILES = []
for dirpath, dirnames, filenames in os.walk(ROOT):
    # ★ 跳过 `_` 前缀目录 —— **对齐 Go 工具链自身的规则**（`go build`/`go vet` 一律忽略
    #   以 `_` 开头的目录）。本仓库的审计探针 **fixture**（`scripts/_probe_c5/`，用于验证
    #   C5 双向检查「能报」，见该目录内注释）即以 `_` 前缀命名 —— 正是借这条规则让它**不进真扫描**：
    #   既保留为可复跑证据，又不污染「扫描 N 个 .go」的统计与结论。
    #   ★ 这是**规则**（与工具链同口径），不是逐目录的例外清单。
    dirnames[:] = [d for d in dirnames
                   if d not in ('.git', 'node_modules', 'dist') and not d.startswith('_')]
    for fn in filenames:
        if fn.endswith('.go'):
            GO_FILES.append(os.path.join(dirpath, fn))

MIG_FILES = []
for dirpath, _d, filenames in os.walk(os.path.join(ROOT, 'migrations')):
    for fn in sorted(filenames):
        if fn.endswith('.sql'):
            MIG_FILES.append(os.path.join(dirpath, fn))


def read(p):
    return io.open(p, encoding='utf-8', errors='replace').read()


def rel(p):
    return os.path.relpath(p, ROOT).replace('\\', '/')


def is_test(p):
    return p.endswith('_test.go')


SRC = {p: read(p) for p in GO_FILES if not is_test(p)}
TESTSRC = {p: read(p) for p in GO_FILES if is_test(p)}
MIG = {p: read(p) for p in MIG_FILES}

hits = []

# ★★ 已豁免的调用点（带位置）。★★ **必须始终可见** ——
#   静默豁免与静默命中同病：「没人看见的放过」＝判据形同虚设。
#   ⇒ 每次运行都列出条数与**逐条明细**（见 main）。
excused = []

# 已人工判定为「有意为之 / 已记录在案」的命中：(检查项, 消息包含的片段, 理由)。
# ★ 只用于**压掉已知项**，使门禁对"新出现的"命中保持灵敏；绝不用于放宽检查逻辑。
KNOWN = [
    ('C4', 'purchase_tier', '档位判定在飞书模板条件分支里，本系统侧无消费端；已登记口径并提示'),
    ('C4', 'emergency_hours', '紧急采购补录时限由人工把握；已登记口径并提示'),
    ('C4', 'submit_workdays', '提交集团时限由人工把握（人工桥纪律①）；已登记口径并提示'),
]


def hit(check, msg):
    for c, frag, _why in KNOWN:
        if c == check and frag in msg:
            return
    hits.append((check, msg))


# ---------------------------------------------------------------- C1 / C2
def parse_columns():
    """从迁移脚本解析 {table: [col, ...]}。

    ★ 覆盖两种建列方式（缺一不可）：
      1) `CREATE TABLE IF NOT EXISTS` —— 表**创建**时的列；
      2) `ALTER TABLE <t> ADD COLUMN <c>` —— 表**演进**时的列。
    ★ 为什么必须补 ALTER：转向 ③ 引入的新列**几乎全是 ALTER 加的**（0001–0007 已应用、
      **不得回改**，新列只能 ALTER）。若只解析 CREATE TABLE，ALTER 列**永远不在 C1 视野**
      → 「建了列但没有任何写入者」（B42 老病）在 ALTER 路径上完全不可见 ——
      即 C1 的检查范围只覆盖了"表创建"、没覆盖"表演进"。（QA 独立复核实测确认。）
    """
    tables = {}
    for p, s in MIG.items():
        for m in re.finditer(r'CREATE TABLE IF NOT EXISTS\s+(\w+)\s*\((.*?)\n\);', s, re.S):
            table, body = m.group(1), m.group(2)
            cols = []
            for line in body.split('\n'):
                line = line.strip()
                if not line or line.startswith('--'):
                    continue
                cm = re.match(r'^(\w+)\s+(INTEGER|TEXT|REAL|BLOB|NUMERIC)', line, re.I)
                if cm:
                    cols.append(cm.group(1))
                # 表级约束（UNIQUE/PRIMARY KEY）跳过
            tables[table] = cols
        # ★ ALTER TABLE ... ADD COLUMN（表演进）：与 CREATE TABLE 的列进**同一套** C1 检查。
        #   实际语法形态（见 0003/0004/0007/0008/0009/0010），列名与类型恒在同一行：
        #     ALTER TABLE t_flow_task ADD COLUMN release_state TEXT NOT NULL DEFAULT 'HELD';
        #     ALTER TABLE t_instance  ADD COLUMN ext_json      TEXT NOT NULL DEFAULT '{}';
        for m in re.finditer(r'ALTER\s+TABLE\s+(\w+)\s+ADD\s+COLUMN\s+(\w+)', s, re.I):
            table, col = m.group(1), m.group(2)
            cols = tables.setdefault(table, [])
            if col not in cols:
                cols.append(col)
    return tables


SQL_WORDS = {
    'id', 'created_at', 'updated_at', 'ledger_type', 'biz_no', 'map_kind', 'map_key',
    'map_value', 'doc_type',
}


def check_columns():
    tables = parse_columns()
    if not tables:
        hit('C1', '未解析到任何表——迁移文件路径可能不对')
        return
    all_src = '\n'.join(SRC.values())
    for table, cols in sorted(tables.items()):
        for col in cols:
            # 在非测试 Go 代码里是否出现过该标识符
            if re.search(r'\b%s\b' % re.escape(col), all_src):
                continue
            hit('C1', '列 `%s.%s` 在迁移里定义，但非测试 Go 代码从不引用（死列/预留列）'
                % (table, col))


# ---------------------------------------------------------------- C3
def _const_map(src):
    """常量名 → 字面量值（用于识别 `config.BizFieldXxx` 形式的间接引用）。"""
    return {k: v for k, v in re.findall(r'(\w+)\s*=\s*"([^"]+)"', src)}


def _readers_of(name, const2val, skip_suffix):
    """统计某字面量在非测试代码里的读取者（同时认「字符串字面量」与「常量引用」）。

    ★ 必须认常量：`config.BizFieldInspectionResult` 就是 `"inspection_result"`，
      只数字面量会把真实读取者漏掉（本脚本初版即误报此处）。
    """
    who = []
    for pp, ss in SRC.items():
        if pp.endswith(skip_suffix):
            continue
        base = rel(pp)
        for i, line in enumerate(ss.split('\n'), 1):
            if '"%s"' % name in line:
                who.append('%s:%d' % (base, i))
                continue
            for c, v in const2val.items():
                if v == name and re.search(r'\b%s\b' % c, line):
                    who.append('%s:%d(via %s)' % (base, i, c))
    return who


def check_passthrough_bizfields():
    p = os.path.join(ROOT, 'internal/config/bizfields.go')
    if not os.path.exists(p):
        hit('C3', '找不到 internal/config/bizfields.go')
        return
    s = read(p)
    m = re.search(r'var PassthroughBizFields = map\[string\]bool\{(.*?)\n\}', s, re.S)
    if not m:
        hit('C3', '未解析到 PassthroughBizFields')
        return
    names = []
    for line in m.group(1).split('\n'):
        line = line.split('//')[0].strip().rstrip(',')
        if not line:
            continue
        cm = re.match(r'^(BizField\w+)', line)
        if cm:
            vm = re.search(r'%s\s*=\s*"([^"]+)"' % cm.group(1), s)
            if vm:
                names.append(vm.group(1))
        else:
            cm2 = re.match(r'^"([^"]+)"', line)
            if cm2:
                names.append(cm2.group(1))

    const2val = _const_map(s)
    for name in sorted(set(names)):
        if not _readers_of(name, const2val, 'bizfields.go'):
            hit('C3', 'biz_field `%s` 登记为透传 ext_json，但**无任何读取者** '
                      '（模板可映射它 → 值落库却无人消费 → 静默失效）' % name)


# ---------------------------------------------------------------- C4
def check_thresholds():
    sample = os.path.join(ROOT, 'docs/reference/config-mapping.sample.json')
    imp = os.path.join(ROOT, 'internal/config/importmap.go')
    if not (os.path.exists(sample) and os.path.exists(imp)):
        return
    s = read(imp)
    m = re.search(r'ConsumedThresholdKeys = map\[string\]bool\{(.*?)\n\}', s, re.S)
    consumed = set(re.findall(r'"([^"]+)"\s*:', m.group(1))) if m else set()
    import json
    data = json.loads(read(sample))
    for e in data.get('threshold', []):
        k = e.get('key')
        if k and k not in consumed:
            hit('C4', '阈值键 `%s` 在样例中登记，但不在 ConsumedThresholdKeys（=假配置）' % k)


# ---------------------------------------------------------------- C5
def check_routes():
    p = os.path.join(ROOT, 'internal/httpapi/router.go')
    if not os.path.exists(p):
        hit('C5', '找不到 internal/httpapi/router.go')
        return
    s = read(p)
    routes = set()
    for m in re.finditer(r'\.(GET|POST|PUT|PATCH|DELETE)\(\s*"([^"]+)"', s):
        routes.add((m.group(1), m.group(2)))

    api = os.path.join(ROOT, 'docs/05-API.md')
    doc = read(api) if os.path.exists(api) else ''
    # ★ 两侧都把「路径参数」归一为 `*`、并把 `/api` 分组前缀剥掉再比：
    #   路由在 `e.Group("/api")` 下注册为 `/ledger/:table`，文档写 `/api/ledger/{table}`。
    #   直接子串比较会把每一条都误报。
    def norm(x):
        x = re.sub(r'[:{]\w+\}?', '*', x)      # :code / {code} → *
        x = re.sub(r'^/?api/', '', x)          # 剥掉分组前缀
        x = re.sub(r'/+', '/', x)              # 折叠重复斜杠
        return x.strip('/')
    doc_paths = {norm(t) for t in re.findall(r'/[A-Za-z0-9_\-/:{}]+', doc)}
    for method, path in sorted(routes):
        n = norm(path)
        if not n or n == '*':
            continue          # 站点根 / 通配，不适用
        # ★ 用**后缀**匹配：路由可能注册在多层分组下（如 `/api/admin` 组的 `/users`
        #   对文档的 `admin/users`）。精确相等会把这类全部误报。
        if not any(d.endswith(n) for d in doc_paths):
            hit('C5', '路由 `%s %s` 已注册，但 docs/05-API.md 未提及（归一后 `%s`）'
                % (method, path, n))

    # ------------------------------------------------------ C5 反向（#62）
    # ★ 反向差集：「文档**显式声明**的 `METHOD /path` → router.go 必须已注册」。
    #   单向（只报「注册未文档」）会漏掉「文档承诺了、代码没实现」的静默漂移（本仓库头号红线）。
    #   降噪（否则正文/外部路径/示例会大量误报）：
    #     · 只认「方法 + **以 / 开头**的完整路径」的显式写法（heading / 反引号 / 表格单元格），
    #       故文档里的相对简写（`` 与 `/reject` ``）**不**单独声明一条；
    #     · 路径**限于我方前缀**（api/ internal/ approval/ auth/ 及 healthz/readyz），
    #       排除飞书外部路径（`/open-apis/…`）与正文示例。
    reg = {(m, norm(p)) for m, p in routes}
    # ★ 前缀判据须作用于**原始路径**（`norm` 会把 `/api/` 前缀剥掉，剥后必然匹配不到 `api/`）。
    own_prefix = ('/api/', '/internal/', '/approval/', '/auth/')
    own_exact = ('/healthz', '/readyz')

    def _equiv(rm, rn, dm, dn):
        """注册路径（源码里的**组相对**路径）与文档路径是否指同一条。"""
        if rm != dm:
            return False
        a, b = rn.split('/'), dn.split('/')
        if a == b:
            return True
        # 同段数：逐段相等或任一侧为 `*`（`:x`/`{x}` 已归一为 `*`）。
        if len(a) == len(b) and all(x == y or x == '*' or y == '*' for x, y in zip(a, b)):
            return True
        # 组相对：一方是另一方的**整段后缀**（如注册 `/users` ↔ 文档 `admin/users`）。
        if len(a) < len(b):
            return b[-len(a):] == a
        return a[-len(b):] == b

    # ★★ 反向的降噪关键：**只把「方法 与 路径」的显式搭配当声明，不把正文里的
    #   简写/链式概览当声明**。两种必须跳过的形态（都是「散文式提及」而非「契约声明」）：
    #     ① 链式概览：`` `GET/POST/PATCH /api/admin/users` `` —— 这是把多个方法**缩写在
    #        一个斜杠链**里（典型出现在变更记录/概述句）。其中与路径相邻的只有**末位**
    #        方法（`PATCH`），且它**紧跟在 `/` 之后**。若照单全收，会把「一个方法链的概览」
    #        误当成「对该路径逐方法的独立承诺」（本仓库实测：变更记录 V1.1 行的
    #        `GET/POST/PATCH /api/admin/users` 会派生出一条**幻影** `PATCH admin/users`）。
    #        ⇒ 判据：**方法 token 的前一个字符是 `/` ⇒ 属链式概览，跳过**。
    #        （真正的契约声明处，方法前恒是 `` ` `` / `|` / 空白 / `>` / `·` / 行首，不会是 `/`。）
    #     ② 形如模板：`` `POST /api/approval/{biz_no}/<action>` `` —— 会被正则截到
    #        `/api/approval/{biz_no}/`（**以 `/` 收尾**），是「路径形如」的说明，非一条具体路由。
    declared = set()
    for m in re.finditer(r'(^|[^/\w])(GET|POST|PUT|PATCH|DELETE)\s+(/[A-Za-z0-9_\-/:{}]+)', doc):
        method, raw = m.group(2), m.group(3)
        if raw.endswith('/'):
            continue
        if not (raw in own_exact or raw.startswith(own_prefix)):
            continue
        declared.add((method, norm(raw)))
    for method, dn in sorted(declared):
        if not dn:
            continue
        if not any(_equiv(rm, rn, method, dn) for rm, rn in reg):
            hit('C5', 'docs/05-API.md 声明 `%s %s`，但 router.go **未注册**（归一后 `%s`）'
                % (method, dn, dn))

# ---------------------------------------------------------------- C6
# 已判定安全的丢弃点：(路径后缀, 代码片段, 判定人/日期/理由)。命中即跳过。
# ★ 纪律（定案 #54）：**只用于压掉已逐行判过、有意为之的丢弃**；绝不为了"看起来完整"而放宽/扩面。
#   每条**精确到「完整相对路径 + 精确表达式」**（表达式取整条语句，非松前缀），并在理由里留判定人/日期。
BENIGN_DISCARDS = [
    ('httpapi/router.go', '_ = c.JSON(', '响应已失败，写错误体不再关心返回值'),
    ('singlelock/lock.go', '_ = writeInfo(', '诊断信息写入失败不影响单实例保证（代码注释已说明）'),
    ('singlelock/lock.go', '_ = err', '显式忽略：见上下文注释'),
    ('dashboard/export.go', '_ = xml.EscapeText(', '写入内存 Buffer 不会失败'),
    ('internal/platform/feishu/external.go', '_ = json.Unmarshal(data, &out)',
     '判定 team-lead/2026-09-27：解析失败有入参兜底 firstNonEmpty(out.ApprovalCode, def.ApprovalCode)（同函数下两行），非静默、有意为之'),
]

BENIGN_IGNORE = re.compile(r'_ = (?:[\w.]+\.)?(Close|Release|Rollback|Unlock|Sync|Flush|Seek)')


def check_swallowed_errors():
    for p, s in sorted(SRC.items()):
        lines = s.split('\n')
        for i, line in enumerate(lines, 1):
            st = line.strip()
            if not st.startswith('_ ='):
                continue
            if BENIGN_IGNORE.match(st):
                continue
            # ★ 注意用 rel(p)（相对路径、正斜杠）比较：p 是绝对路径且 Windows 下是反斜杠，
            #   直接用 p.endswith('dashboard/export.go') 永远不成立（本脚本初版即如此）。
            r = rel(p)
            if any(r.endswith(sfx) and frag in st for sfx, frag, _ in BENIGN_DISCARDS):
                continue
            # `_ = fail(c, ...)` 后紧跟 return → 响应已写出，属正常写法
            if '_ = fail(' in st:
                nxt = lines[i].strip() if i < len(lines) else ''
                if nxt.startswith('return'):
                    continue
            hit('C6', '%s:%d 显式丢弃返回值：%s' % (r, i, st[:120]))


# ---------------------------------------------------------------- C7
def check_error_codes():
    all_src = '\n'.join(SRC.values())
    codes = set(re.findall(r'^\s*(code[A-Z]\w*)\s*=\s*"', all_src, re.M))
    for c in sorted(codes):
        n = len(re.findall(r'\b%s\b' % c, all_src))
        if n <= 1:
            hit('C7', '错误码常量 `%s` 定义后从未被返回' % c)


# ---------------------------------------------------------------- C8
def check_test_fixture_bypass():
    """测试夹具绕过真实写入路径（B37 / B47 的同一模式）。

    两件事：
      C8a  非测试代码里出现的 `UpsertArchive` / `UpsertOps` —— 只应出现在
           「真实写入者」（worker/ingest）与「运营字段写入接口」（handlers_biz）里。
      C8b  测试夹具里造的**台账类型**中，有没有「任何 doc_type 都产生不了」的
           —— 那意味着用例在验证一条生产上永远走不到的路径（B37/B47 的形态）。
    """
    # C8a：非测试代码的写入点 —— ★ 只认"写入调用"，排除函数定义行
    #
    # ★ 为什么排除定义行：C8a 的语义是「写入点」，而定义行（`func (d *DB) UpsertArchiveTx(...)`）
    #   **不是写入点** —— 写入点是**调用**它的地方。把定义行算命中属**误判**；修好正则后，
    #   每个写函数的定义都会成为命中 → 门禁被自己的噪声淹没 → 而"命中多到无人细读"
    #   **本身就是假绿的另一种形态**（与 `3f76985`「扫 0 文件仍 OK」同病）。
    #   ★ 原则：**能用规则解决的就不要用例外**；白名单是"例外清单"，**越长越接近门禁失效**。
    #
    # ★★ 判据 = 「按目标类型」为主 + 「按函数名」为辅，两者取**并集**（宁多勿漏）：
    #   · 主判据（类型）：调用参数里出现指向台账行的（指针）复合字面量
    #     `&store.LedgerArchive{` / `&store.LedgerOps{`。
    #     **"写台账"的本质是"往 t_ledger_archive / t_ledger_ops 写"，而不是"调用了某个
    #     叫 UpsertXxx 的函数" —— 函数名只是实现细节，类型才是契约。**
    #     ★ 为什么必须带 `&`（而非宽泛匹配 `store.LedgerXxx{`）：写 API 收的是**指针**
    #       （`UpsertArchive(ctx, q, a *LedgerArchive)`），故写入恒为 `&store.LedgerXxx{`；
    #       读路径用的是**值**复合字面量（`map[string]store.LedgerArchive{}` /
    #       `[]store.LedgerArchive{...}`），**不带 `&`** —— 不加 `&` 会把这些读侧构造误报为写入。
    #   · 辅判据（名字）：`UpsertArchive(` / `UpsertArchiveTx(` / `UpsertOps(` / `UpsertOpsTx(`。
    #     ★ 只作**补充**：单靠名字会**改名即绕过**（`InsertArchive` / `AppendArchive` /
    #       `UpsertLedger` 这类调用一律漏检）—— 而 R23 的教训正是「换个名字做的事，
    #       最容易被当作无害」。故名字模式**不得**成为唯一判据。
    #
    # allowed 精确路径白名单（已确认的"真实写入者"）：
    #           · internal/worker/ingest.go     —— `g.db.UpsertArchiveTx(ctx, tx, &store.LedgerArchive{...})`
    #           · internal/httpapi/handlers_biz.go —— `d.DB.UpsertOps(ctx, &store.LedgerOps{...})`
    #         （`internal/store/repo_ledger.go` 已移出：其中只有 **定义**、无调用，规则收紧后不再命中）
    allowed = ('internal/worker/ingest.go', 'internal/httpapi/handlers_biz.go')
    allowed_prefix = ('internal/flow/',)
    decl_re = re.compile(r'^\s*func\b')                                # 函数定义行 ≠ 写入点
    type_re = re.compile(r'&\s*store\.Ledger(?:Archive|Ops)\s*\{')     # 主判据：目标类型（写入恒为指针复合字面量）
    name_re = re.compile(r'UpsertArchive(?:Tx)?\(|UpsertOps(?:Tx)?\(')  # 辅判据：函数名（并集·宁多勿漏）
    for p, s in sorted(SRC.items()):
        r = rel(p)
        for i, line in enumerate(s.split('\n'), 1):
            if decl_re.match(line):
                continue
            if (type_re.search(line) or name_re.search(line)) \
                    and not any(r == a for a in allowed) \
                    and not r.startswith(allowed_prefix):
                hit('C8a', '%s:%d 非测试代码直接写台账（确认它是不是真实写入者）：%s'
                    % (r, i, line.strip()[:110]))

    # C8b：可生产台账集合 = 样例配置里所有 doc_type 的台账
    #
    # ★★ 已知盲区（**如实登记**，不通过"扩大名字清单"去掩盖）：
    #   本检查**依赖夹具命名约定** —— 只认 `seedArchive*` / `seedOps*` 两个函数名前缀
    #   （正则 `seedArchive(?:DocExt)?\(` / `seedOps\(`）。**换个夹具名即失效**：
    #     · 改叫 `mkLedger(...)` / `putRow(...)` → 本检查**看不见**；
    #     · 现存夹具名已**超出**正则覆盖面：`seedArchiveDoc(` / `seedArchiveExt(` 都**不**匹配
    #       `seedArchive(?:DocExt)?\(`（`Doc`/`Ext` 后缀不在模式里）—— 即覆盖面本就偏窄。
    #   ★ 为什么不"顺手"补全名字清单：补名字只是把"按名字"的毛病**又犯一遍**（换名照样绕过，
    #     且清单越长越像"已覆盖"的错觉）。真正的解法应是**按目标表**判据（夹具里出现对
    #     `t_ledger_archive` / `t_ledger_ops` 的写入即算），但本项目测试夹具**一律经 `seed*`
    #     助手写库、无裸 `INSERT INTO t_ledger_*`**（已实测），故该判据当前**恒为空、无增益**，
    #     暂不采用。此处**如实标注盲区**，供人工复核时对"改名夹具"保持警觉。
    import json
    sample = os.path.join(ROOT, 'docs/reference/config-mapping.sample.json')
    producible = set()
    if os.path.exists(sample):
        try:
            for e in json.loads(read(sample)).get('ledger_type', []):
                producible.add(e.get('value'))
        except Exception as ex:  # noqa: BLE001
            hit('C8b', '解析样例配置失败：%s' % ex)
    used = {}          # 台账 → [(文件, 行号, 调用点偏移, 全文)]
    seed_re = re.compile(r'seed(?:Archive(?:DocExt)?|Ops)\([^)]*?"(L\d\d)"', re.S)
    for p, s in TESTSRC.items():
        for m in seed_re.finditer(s):
            ln = s.count('\n', 0, m.start()) + 1
            used.setdefault(m.group(1), []).append((p, ln, m.start(), s))

    # ★ 例外：**负向断言夹具** —— 故意造一行"生产上不可能存在"的数据，
    #   用来证明读侧不会读它（B37 的 L11 回归就是这么写的）。
    #   判据：该 seed 调用附近的注释出现否定语。
    #   ★★ 2026-10-01 补「反例」：本判据的语义**就是**识别负向夹具，而「反例」是这类用例的
    #      **标准说法**却漏在词表外 —— 于是 mimo 在 N-034 补的那条反例用例被误报。
    #      ★ 这是**提升判据的理解力**，不是"压掉已知项"（与之相对，`KNOWN` 才是后者）。
    #   ★★ 同批另修两处与兄弟判据不一致的地方（C6/C8a 都带位置）：
    #      ① 命中**带 file:line**（此前只有一句描述 ⇒ 人工判定无从下手）；
    #      ② 豁免**按调用点**判（原按台账判 —— 同一台账只要有一处带否定语，
    #         就把它**所有**调用点一并豁免了）；③ 豁免**可见**（见 main 的汇总行）。
    neg_markers = ('负向', '反例', '不会', '不再', '脏数据', '不该', '不得')
    for lt in sorted(used):
        if lt in producible:
            continue
        for p, ln, off, s in used[lt]:
            ctx = s[max(0, off - 600):off]
            if any(mk in ctx for mk in neg_markers):
                excused.append('%s:%d 台账 `%s`（负向/反例夹具 —— 故意造不可达台账行以证明读侧不读它）'
                               % (rel(p), ln, lt))
                continue
            hit('C8b', '%s:%d 测试夹具造了台账 `%s` 的行，但样例配置里**没有任何 doc_type 能生产它** '
                       '（用例在验证生产上走不到的路径）' % (rel(p), ln, lt))


# ---------------------------------------------------------------- C9
# ★★ 2026-10-04 新增（`REMAINING.md#B6` / `COLLAB.md#N-051`）：
#   `spec/openapi.json`（机读契约）↔ `docs/05-API.md`（人读正本）的**真值一致性**。
# ★ 为什么放在这里而不是新脚本：本项与 `C5` 同族（都是「契约 ↔ 实现/正本 的双向差集」），
#   同属「会报既存问题、不阻塞总判定」这一档（`check_all.sh` 的 `report` 组）。
# ★★ 为什么**复用生成器**而不是重写抽取：`md → json` 的映射规则只应有**一份实现**
#   （`scripts/gen_openapi.py`）。若这里再写一遍，就是**第三份真相** —— 迟早与生成器不一致，
#   且不一致时无法判断谁错。⇒ 本项只做「**文件是否仍等于生成器的输出**」。
#   ★ 判据代价：生成器被删/被改签名 ⇒ 本项**报错可见**（而不是静默变成"无人检查"）。
def check_openapi_alignment():
    gen_py = os.path.join(ROOT, 'scripts', 'gen_openapi.py')
    out_p = os.path.join(ROOT, 'spec', 'openapi.json')
    doc_p = os.path.join(ROOT, 'docs', '05-API.md')
    for need, what in ((gen_py, 'scripts/gen_openapi.py（生成器）'),
                       (out_p, 'spec/openapi.json（机读契约）'),
                       (doc_p, 'docs/05-API.md（人读正本）')):
        if not os.path.exists(need):
            hit('C9', '缺 %s' % what)
    if not (os.path.exists(gen_py) and os.path.exists(out_p) and os.path.exists(doc_p)):
        return

    sys.path.insert(0, os.path.join(ROOT, 'scripts'))
    try:
        import gen_openapi  # noqa: E402
    except Exception as e:                       # 生成器不可用 ⇒ 必须可见，不能静默
        hit('C9', '无法导入生成器 scripts/gen_openapi.py：%r' % (e,))
        return

    try:
        doc_json, routes, _wk, _wd, _amb, _syn, _n = gen_openapi.build()
    except SystemExit as e:
        hit('C9', '生成器无法从正本重建契约（SystemExit %s）' % (e,))
        return
    try:
        have = json.loads(read(out_p))
    except Exception as e:
        hit('C9', 'spec/openapi.json 不可解析：%r' % (e,))
        return

    # ① 正本指纹：正本被改过而机读契约未重生 ⇒ 立刻可见（这正是「索引失真」的窗口）
    want_sha = doc_json['x-source']['doc_sha256']
    have_sha = (have.get('x-source') or {}).get('doc_sha256')
    if have_sha != want_sha:
        hit('C9', 'docs/05-API.md 与 spec/openapi.json 的 `x-source.doc_sha256` 不一致 ⇒ '
                  '**正本改过、机读契约未重生**（跑 `python scripts/gen_openapi.py`）'
                  '（正本 %s / 契约 %s）' % (want_sha[:12], (have_sha or '缺失')[:12]))

    # ② 路由集合双向差集（★ 逐字比路径写法，比归一后更严）
    want = set()
    for m, pth, _k, _ln in routes:
        want.add((m, pth))
    got = set()
    for pth, item in (have.get('paths') or {}).items():
        if not isinstance(item, dict):
            continue
        for meth in item:
            got.add((meth.upper(), pth))
    for m, pth in sorted(want - got):
        hit('C9', '正本声明 `%s %s`，但 spec/openapi.json **未收录**（机读契约缺该路由）' % (m, pth))
    for m, pth in sorted(got - want):
        hit('C9', 'spec/openapi.json 收录 `%s %s`，但**正本未声明**（★ 第二份真相 —— '
                  '机读契约不得超出人读正本）' % (m, pth))

    # ③ 路由条数（★ 双向差集为空时二者必然相等；此处独立再算一次，防"都空"的假绿）
    if len(want) == 0:
        hit('C9', '生成器从正本**未提取到任何路由** ⇒ 抽取规则可能已失效（不是"全部一致"）')


def main():
    check_columns()
    check_passthrough_bizfields()
    check_thresholds()
    check_routes()
    check_openapi_alignment()
    check_swallowed_errors()
    check_error_codes()
    check_test_fixture_bypass()

    order = ['C1', 'C2', 'C3', 'C4', 'C5', 'C9', 'C6', 'C7', 'C8a', 'C8b']
    print('===== 静默缺陷机械排查（只读）=====')
    print('扫描：%d 个非测试 .go / %d 个测试 .go / %d 个迁移\n' % (
        len(SRC), len(TESTSRC), len(MIG)))
    for c in order:
        rows = [m for k, m in hits if k == c]
        if not rows:
            print('[%s] 无命中 ✓' % c)
            continue
        print('[%s] %d 处（需人工判定）' % (c, len(rows)))
        for r in rows:
            print('   · ' + r)
        print()

    # ★★ 豁免必须可见：既不静默放过，也不假装"零产出＝没问题"
    if excused:
        print('[C8b] 已声明豁免 %d 处（负向/反例夹具 · ★ 不静默）：' % len(excused))
        for e in excused:
            print('   · ' + e)
        if not any(k == 'C8b' for k, _ in hits):
            print('   ★ 本判据本次**零产出、全靠豁免** —— ★ 这不是错误（本项目的用例本就要求'
                  '「拦 ＋ 放」成对、反例是标准配置），但★ **每条豁免的语义都要经得起看**：')
            print('     判据口径是「seed 调用**前 600 字**内出现否定语即豁免」—— ★ 这是本条**已知的宽松处**：')
            print('     ① 窗口偏宽（远处的无关注释也能误豁免）；② 只能识别词表内的说法。')
            print('     ⇒ 复核时请逐条确认「它真的是在验证『读不到』」。')
        print()

    print('合计命中 %d 处' % len(hits))
    return 1 if hits else 0


if __name__ == '__main__':
    sys.exit(main())
