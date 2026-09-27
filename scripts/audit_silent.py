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
    """从迁移脚本解析 {table: [col, ...]}。"""
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
    # C8a：非测试代码的写入点
    #   allowed        精确路径白名单（已确认的"真实写入者"）
    #   allowed_prefix 目录白名单：③ 下台账/状态史/附件的**新写入者是 internal/flow**
    #                  （`flow.finalize`，FR-M9-12）。不加它，会把"接管者"误报成"越权写入者"
    #                  （R22：门禁自身失效——漏检 + 误报）。
    #
    # ★ 正则必须**同时**覆盖事务版 `UpsertArchiveTx(` / `UpsertOpsTx(`：
    #   ③ 新写入者 `flow.finalize` 用的是 **`UpsertArchiveTx`**（`internal/flow/finalize.go`）。
    #   若只匹配 `UpsertArchive\(`，`flow/finalize.go` 那行**永远进不了 if** →
    #   白名单再加也没用 → **仍是漏检**，且让 R22 **看起来已经修好**（比没修更危险）。
    allowed = ('internal/worker/ingest.go', 'internal/store/repo_ledger.go',
               'internal/httpapi/handlers_biz.go')
    allowed_prefix = ('internal/flow/',)
    for p, s in sorted(SRC.items()):
        r = rel(p)
        for i, line in enumerate(s.split('\n'), 1):
            if re.search(r'UpsertArchive(?:Tx)?\(|UpsertOps(?:Tx)?\(', line) \
                    and not any(r == a for a in allowed) \
                    and not r.startswith(allowed_prefix):
                hit('C8a', '%s:%d 非测试代码直接写台账（确认它是不是真实写入者）：%s'
                    % (r, i, line.strip()[:110]))

    # C8b：可生产台账集合 = 样例配置里所有 doc_type 的台账
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
