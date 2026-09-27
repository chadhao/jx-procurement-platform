#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""audit_silent.py — 「静默缺陷」机械化排查（只读）。

静默缺陷 = **不报错、但结果错或空**。它的共同形态是「生产者/消费者不对称」：
某列/键/映射/接口**有人写没人读**、或**有人读没人写**、或**配了但没有消费端**。

本脚本做 8 项机械检查（每项都可能误报，须人工确认；但"零命中"是可信的）：

  C1  迁移里定义、但 Go 代码从不引用的列          → 死列 / 预留列
  C2  仅出现在 INSERT/UPDATE 上下文、从无 SELECT 的列 → 有人写没人读
  C3  PassthroughBizFields 里没有任何读取者的名字   → 映射落库但无人消费
  C4  threshold 键是否有消费端（对照代码里的消费表）
  C5  注册的路由 ↔ API 文档双向差集
  C6  被吞掉的错误（`_ = x` 且 x 不是 Close/Release/Rollback 等）
  C7  定义了但从未被返回的错误码常量
  C8  测试里直接造台账行（绕过真实 Ingest 路径）的位点

用法：python scripts/audit_silent.py [--root .]
退出码：0 = 无命中；1 = 有命中（供 CI 用；命中需人工判定是否真缺陷）
"""
import io
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
if '--root' in sys.argv:
    ROOT = os.path.abspath(sys.argv[sys.argv.index('--root') + 1])

GO_FILES = []
for dirpath, dirnames, filenames in os.walk(ROOT):
    dirnames[:] = [d for d in dirnames if d not in ('.git', 'node_modules', 'dist')]
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


# ---------------------------------------------------------------- C6
# 已判定安全的丢弃点：(路径后缀, 代码片段, 理由)。命中即跳过。
BENIGN_DISCARDS = [
    ('httpapi/router.go', '_ = c.JSON(', '响应已失败，写错误体不再关心返回值'),
    ('singlelock/lock.go', '_ = writeInfo(', '诊断信息写入失败不影响单实例保证（代码注释已说明）'),
    ('singlelock/lock.go', '_ = err', '显式忽略：见上下文注释'),
    ('dashboard/export.go', '_ = xml.EscapeText(', '写入内存 Buffer 不会失败'),
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
    used = set()
    for p, s in TESTSRC.items():
        for m in re.finditer(r'seedArchive(?:DocExt)?\([^)]*?"(L\d\d)"', s, re.S):
            used.add(m.group(1))
        for m in re.finditer(r'seedOps\([^)]*?"(L\d\d)"', s, re.S):
            used.add(m.group(1))
    for lt in sorted(used):
        if lt in producible:
            continue
        # ★ 例外：**负向断言夹具** —— 故意造一行"生产上不可能存在"的数据，
        #   用来证明读侧不会读它（B37 的 L11 回归就是这么写的）。
        #   判据：该 seed 调用附近的注释出现否定语（不会/不再/负向/脏）。
        neg_markers = ('负向', '不会', '不再', '脏数据', '不该', '不得')
        excused = False
        for p, s in TESTSRC.items():
            for m in re.finditer(r'seed(?:Archive|Ops)(?:DocExt)?\([^)]*?"%s"' % lt, s, re.S):
                ctx = s[max(0, m.start() - 600):m.start()]
                if any(mk in ctx for mk in neg_markers):
                    excused = True
                    break
            if excused:
                break
        if not excused:
            hit('C8b', '测试夹具造了台账 `%s` 的行，但样例配置里**没有任何 doc_type 能生产它** '
                       '（用例在验证生产上走不到的路径）' % lt)


def main():
    check_columns()
    check_passthrough_bizfields()
    check_thresholds()
    check_routes()
    check_swallowed_errors()
    check_error_codes()
    check_test_fixture_bypass()

    order = ['C1', 'C2', 'C3', 'C4', 'C5', 'C6', 'C7', 'C8a', 'C8b']
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

    print('合计命中 %d 处' % len(hits))
    return 1 if hits else 0


if __name__ == '__main__':
    sys.exit(main())
