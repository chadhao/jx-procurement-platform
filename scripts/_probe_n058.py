# -*- coding: utf-8 -*-
'''`N-058` 探针 —— 「验收组的唯一正本」与「声明却无人读的键」两项收敛的行为证明
（不是「跑一下没报错」）。

用法：`python scripts/_probe_n058.py`

★ 本文件不进任何门禁面（`_` 前缀不对齐 `check_spec.py` / `check_all.sh` 入口）。
★★ **全程不改动仓库内任何真实文件** —— 变异一律在**内存副本**上做，再对同一批断言函数
   重跑；收尾逐字节核对仓库真源 `sha256` 与开跑前相同。

它证明九件事（★ 每条都是可复现实测，不是推断）：

  0. **依据核对**：`spec/constants.json` 已把 `acceptance_groups` 登记为**制度性枚举**、
     其 `store` ＝ `spec/enums.json` ⇒ ★ 「唯一正本在 enums」**不是我方新发明的口径**，
     而是既有登记的直接推论（本议题只是**把数据改成与登记一致**）。
  1. **唯一正本成立**：`enums.json#acceptance_groups.by_category_prefix` 恰 **3 组**，
     组前缀逐字为 `P01` / `P02–P04` / `P05–P08`（★ 分隔符是 **EN DASH `U+2013`**，
     由码点断言，不靠肉眼）。
  2. **重复体已消除**：`chain.json#doc_chains.GR` **不得**再含 `acceptance_groups` 键；
     须含指针键 `acceptance_groups_ref` 指向 enums；`chain.json#conventions.acceptance_groups`
     须明写「唯一正本 ＋ 不得复制」；`forms/GR.json` 全文不得再引用**已删除**的路径。
  3. ★★ **缺口存在性反证（鉴别力）**：把**修复前的重复体**（ASCII 连字符版）塞回
     `chain.json` 的内存副本 ⇒ 第 1 条断言**必须报红** ⇒ 证明该断言**能数到非 0**，
     不是恒真空断言（★ 教训：断言「0 条」前必须先证明「能数到非 0」）。
  4. ★★ **漂移实证**：修复前的两份定义，**键集逐字节不等** —— 差异恰在**分隔符**：
     `chain.json` 侧为 **ASCII HYPHEN `U+002D`**、`enums.json` 侧为 **EN DASH `U+2013`**
     ⇒ ★ 「两份真相必然漂移」这句判断在本例上**已有实物**（不是预期、是已经发生的漂移）。
  5. **语义不明键已具名化**：`doc_chains.GR` 不含 `finalized`；含 `obsolete_rules`，
     且其文本含 `R-16` 与「经办人」原文；★ 反证：内存副本去掉该键 ⇒ 对应断言报红。
  6. ★★ **「声明却无人读」的判定面扩展（机械核对，非引述）**：`DocChainDoc` 结构体
     **确已声明** `prefix`（⇒ 看起来机读），而 `doc_chains` 被**实际消费**的字段集合
     （`internal/chain/route.go` 的 `dc.<Field>` 选择器）**不含** `Prefix` ⇒ ★ 该键
     「结构体已声明、全仓零消费方」这一声明**由代码实测坐实**；★ 同时断言该消费集合
     **非空**（能数到非 0），否则「不含 Prefix」是句废话。
  7. **过期台账已订正**：`forms/SS.json#known_gaps[0]` 不得再写「`conventions` 里没有
     这一项」（★ 实测 `conventions.env_count` **2026-10-01 就已立**，原文属**过期**）。
  8. **收尾**：仓库真源逐字节未变（探针只在内存里变异）。

★ 口径正本 ＝ `spec/chain.json#conventions.acceptance_groups` 与
  `#conventions.non_machine_read_keys`（本探针**不重复实现口径**，只按正本断言）。
'''
import hashlib
import io
import json
import os
import re
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)

P_CHAIN = os.path.join(ROOT, 'spec', 'chain.json')
P_ENUMS = os.path.join(ROOT, 'spec', 'enums.json')
P_CONST = os.path.join(ROOT, 'spec', 'constants.json')
P_GR = os.path.join(ROOT, 'spec', 'forms', 'GR.json')
P_QC = os.path.join(ROOT, 'spec', 'forms', 'QC.json')
P_SS = os.path.join(ROOT, 'spec', 'forms', 'SS.json')
P_SPELOAD = os.path.join(ROOT, 'internal', 'specload', 'specload.go')
P_ROUTE = os.path.join(ROOT, 'internal', 'chain', 'route.go')

EN_DASH = u'\u2013'
HYPHEN = u'-'

# 修复前的重复体（★ 逐字节取自 `chain.json` 历史版本，仅作**反证素材**，不写回仓库）
PRE_FIX_DUP = {
    u'P01': {u'label': u'原辅料类', u'members': [u'采购经办人', u'质检技术部', u'库管'], u'size': 3},
    u'P02' + HYPHEN + u'P04': {u'label': u'设备耗材类', u'members': [u'采购经办人', u'质检技术部'], u'size': 2},
    u'P05' + HYPHEN + u'P08': {u'label': u'其他类', u'members': [u'采购经办人', u'综合运营部人员'], u'size': 2},
}

EXPECTED_GROUPS = [
    (u'P01', u'原辅料类', [u'采购经办人', u'质检技术部', u'库管'], 3),
    (u'P02' + EN_DASH + u'P04', u'设备耗材类', [u'采购经办人', u'质检技术部'], 2),
    (u'P05' + EN_DASH + u'P08', u'其他类', [u'采购经办人', u'综合运营部人员'], 2),
]

RESULT = []
BEFORE = {}


def sha(path):
    with io.open(path, 'rb') as f:
        return hashlib.sha256(f.read()).hexdigest()


def load(path):
    with io.open(path, 'r', encoding='utf-8') as f:
        return json.load(f)


def raw(path):
    with io.open(path, 'r', encoding='utf-8', newline='') as f:
        return f.read()


def ok(name, cond, detail=''):
    RESULT.append((bool(cond), name, detail))
    print(u'  %s %s%s' % (u'\u2713' if cond else u'\u2717', name,
                          (u'   [' + detail + u']') if detail else u''))


# ---------------------------------------------------------------- 断言函数（可对副本重跑）
def a_unique_source(chain):
    '''第 1/2 条：重复体已消除 ＋ 指针键在位。★ 反证时对内存副本调用同一函数。'''
    gr = chain[u'doc_chains'][u'GR']
    return (u'acceptance_groups' not in gr,
            u'acceptance_groups_ref' in gr and u'spec/enums.json#acceptance_groups' in gr[u'acceptance_groups_ref'])


def a_obsolete_rules(chain):
    '''第 5 条：`finalized` 已具名化。'''
    gr = chain[u'doc_chains'][u'GR']
    txt = u' '.join(gr.get(u'obsolete_rules', []))
    return (u'finalized' not in gr,
            bool(gr.get(u'obsolete_rules')) and u'R-16' in txt and u'经办人' in txt)


def consumed_doc_chain_fields():
    '''第 6 条：`doc_chains` 被**实际消费**的字段集合（`dc.<Field>` 选择器，机械抽取）。'''
    src = raw(P_ROUTE)
    return set(re.findall(r'\bdc\.([A-Za-z]\w*)', src))


def declared_doc_chain_fields():
    '''第 6 条：`DocChainDoc` 结构体**声明**的 json 字段（机械抽取）。'''
    src = raw(P_SPELOAD)
    i = src.find('type DocChainDoc struct')
    j = src.find('\n}', i)
    return re.findall(r'json:"([^"]+)"', src[i:j])


print(u'===== `N-058` 探针（验收组唯一正本 ＋ 声明却无人读的键）=====')
for p in (P_CHAIN, P_ENUMS, P_CONST, P_GR, P_QC, P_SS, P_SPELOAD, P_ROUTE):
    BEFORE[p] = sha(p)

chain = load(P_CHAIN)
enums = load(P_ENUMS)
consts = load(P_CONST)

print(u'\n== 0. 依据核对：唯一正本方向由既有登记推出 ==')
ins = [c for c in consts[u'three_way_split'][u'categories'] if c[u'id'] == u'institution_enum'][0]
ok(u'0a `constants.json` 把 `acceptance_groups` 登记为**制度性枚举**',
   u'acceptance_groups' in ins[u'members'], u'members 含 acceptance_groups=%s' % (u'acceptance_groups' in ins[u'members']))
ok(u'0b 该类的 `store` 指向 `spec/enums.json`（⇒ 唯一正本在 enums，非新口径）',
   u'spec/enums.json' in ins[u'store'], ins[u'store'][:60])

print(u'\n== 1. 唯一正本：`enums.json#acceptance_groups` 逐字断言（含码点）==')
groups = enums[u'acceptance_groups'][u'by_category_prefix']
got = [(g[u'prefix'], g[u'label'], list(g[u'members']), g[u'size']) for g in groups]
ok(u'1a 恰 **3 组**且 `prefix`/`label`/`members`/`size` 与期望**逐字相等**',
   got == EXPECTED_GROUPS, u'实际=%s' % (json.dumps(got, ensure_ascii=False)[:160],))
ok(u'1b 分隔符是 **EN DASH `U+2013`**（码点断言，非肉眼）',
   all(EN_DASH in g[0] for g in got if len(g[0]) > 4) and
   all(HYPHEN not in g[0] for g in got),
   u'码点=%s' % ([hex(ord(c)) for c in got[1][0]],))

print(u'\n== 2. 重复体已消除 ＋ 指针键 ＋ 约定 ＋ `origin_ref` ==')
u1, u2 = a_unique_source(chain)
gr = chain[u'doc_chains'][u'GR']
ok(u'2a `doc_chains.GR` **不含** `acceptance_groups`（重复体已删）', u1,
   u'GR keys=%s' % (list(gr.keys()),))
ok(u'2b 含指针键 `acceptance_groups_ref` 且指向 `enums.json#acceptance_groups`', u2,
   u'ref=%s' % (gr.get(u'acceptance_groups_ref', u'')[:90],))
conv = chain[u'conventions'].get(u'acceptance_groups', u'')
ok(u'2c `conventions.acceptance_groups` 明写「唯一正本」＋「不得再复制」',
   u'唯一正本' in conv and u'再复制该定义' in conv,
   u'len=%d' % len(conv))
# ★ 断言口径（防过宽 / 防过窄）：**只要求元数据引用位点**（`origin_ref` / `chain` / `enum` 等
#   指向别处的引用键）不再指向**已删除**的路径；★ `known_gaps` 里为记录「删除这件事」而
#   **提及**该路径属正当（历史留痕），不得据此判红。
def _collect_refs(node, out):
    if isinstance(node, dict):
        for k, v in node.items():
            if isinstance(v, str) and (k == u'origin_ref' or k.endswith(u'_ref') or k in (u'chain', u'enum')):
                out.append((k, v))
            else:
                _collect_refs(v, out)
    elif isinstance(node, list):
        for it in node:
            _collect_refs(it, out)


refs = []
_collect_refs(load(P_GR), refs)
bad_refs = [(k, v) for k, v in refs if u'doc_chains.GR.acceptance_groups' in v]
ok(u'2d `forms/GR.json` 的**引用键**不再指向已删除的 `doc_chains.GR.acceptance_groups`',
   not bad_refs and len(refs) > 0,
   u'引用位点=%d，悬挂=%d' % (len(refs), len(bad_refs)))

print(u'\n== 3. ★★ 缺口存在性反证：同一断言在「修复前形态」上必须报红 ==')
mut = json.loads(json.dumps(chain))
mut[u'doc_chains'][u'GR'][u'acceptance_groups'] = json.loads(json.dumps(PRE_FIX_DUP))
m1, m2 = a_unique_source(mut)
ok(u'3a 把修复前重复体塞回 ⇒ 第 2a 条断言**报红**（断言能数到非 0，非恒真空）', not m1,
   u'mut 后 2a=%s' % m1)
ok(u'3b 真源上同一断言为**绿**（正反两面齐备）', u1, u'')

print(u'\n== 4. ★★ 漂移实证：修复前两份定义**键集逐字节不等** ==')
pre_keys = set(PRE_FIX_DUP.keys())
now_keys = set(g[u'prefix'] for g in groups)
ok(u'4a 修复前 `chain` 键集 ≠ `enums` 前缀集（**已发生**的漂移，非预期）',
   pre_keys != now_keys,
   u'chain=%s / enums=%s' % (sorted(pre_keys), sorted(now_keys)))
# ★ 配对口径：**只把分隔符归一**（EN DASH → HYPHEN）后应能一一配上；
#   配上的对里仍有差异者，即为「同义异名」的实物。
norm = lambda s: s.replace(EN_DASH, HYPHEN)  # noqa: E731
pairs = [(a, b) for a in sorted(pre_keys) for b in sorted(now_keys) if norm(a) == norm(b)]
diff = [(a, b) for a, b in pairs if a != b]
ok(u'4b 三组一一配得上（归一分隔符后），且**恰 2 组**存在差异 ⇒ 差异即分隔符',
   len(pairs) == 3 and len(diff) == 2 and all(HYPHEN in a and EN_DASH in b for a, b in diff),
   u'diff=%s' % (diff,))

print(u'\n== 5. 语义不明键已具名化（`finalized` → `obsolete_rules`）==')
o1, o2 = a_obsolete_rules(chain)
ok(u'5a `doc_chains.GR` **不含** `finalized`（语义与键名相反的键已消除）', o1, u'')
ok(u'5b 含 `obsolete_rules` 且文本带 `R-16` 与「经办人」原文', o2,
   u'%s' % (json.dumps(gr.get(u'obsolete_rules'), ensure_ascii=False)[:110],))
mut2 = json.loads(json.dumps(chain))
mut2[u'doc_chains'][u'GR'].pop(u'obsolete_rules', None)
_m1, _m2 = a_obsolete_rules(mut2)
ok(u'5c 反证：内存副本去掉该键 ⇒ 5b 断言**报红**', not _m2, u'mut 后 5b=%s' % _m2)

print(u'\n== 6. ★★ 「声明却无人读」：`doc_chains.*.prefix` 由代码实测坐实 ==')
declared = declared_doc_chain_fields()
consumed = consumed_doc_chain_fields()
nmrk = chain[u'conventions'].get(u'non_machine_read_keys', u'')
ok(u'6a `conventions.non_machine_read_keys` 已登记 `doc_chains.*.prefix` ＋「零消费方」',
   u'doc_chains.*.prefix' in nmrk and u'零消费方' in nmrk, u'len=%d' % len(nmrk))
ok(u'6b **结构体确已声明** `prefix`（⇒ 看起来机读）', u'prefix' in declared,
   u'declared=%s' % (declared,))
ok(u'6c **实际消费集合非空**（能数到非 0，否则 6d 是句废话）',
   len(consumed) >= 3 and {u'Route', u'RouteByTier', u'NoApprovalChain'} <= consumed,
   u'consumed=%s' % (sorted(consumed),))
ok(u'6d 消费集合**不含** `Prefix` ⇒「全仓零消费方」成立（非引述，是实测）',
   not any(f.lower() == u'prefix' for f in consumed), u'consumed=%s' % (sorted(consumed),))

print(u'\n== 7. 过期台账已订正 ==')
ss = load(P_SS)
g0 = ss[u'known_gaps'][0]
g0s = g0.get(u'status', u'')
ok(u'7a `SS.json#known_gaps[0]` 不再写「`conventions` 里没有这一项」（**已过期**）',
   u'没有这一项' not in g0s and u'conventions.env_count' in g0s, u'status=%s' % g0s[:90])
gc = chain[u'conventions'].get(u'env_count', u'')
ok(u'7b 反证：`conventions.env_count` **确实在位**（原文属过期，非我方编造）',
   bool(gc) and u'语义待定' in gc, u'len=%d' % len(gc))
grj = load(P_GR)
gqc = load(P_QC)
ok(u'7c `GR.json#known_gaps[5]/[6]` 状态已订正为闭环（含 `N-058`）',
   u'N-058' in grj[u'known_gaps'][5].get(u'status', u'') and
   u'N-058' in grj[u'known_gaps'][6].get(u'status', u''),
   u'%s / %s' % (grj[u'known_gaps'][5].get(u'status', u'')[:40],
                 grj[u'known_gaps'][6].get(u'status', u'')[:40]))
ok(u'7d `QC.json#known_gaps[3]/[4]` 状态已订正为闭环（含 `N-058`）',
   u'N-058' in gqc[u'known_gaps'][3].get(u'status', u'') and
   u'N-058' in gqc[u'known_gaps'][4].get(u'status', u''),
   u'%s / %s' % (gqc[u'known_gaps'][3].get(u'status', u'')[:40],
                 gqc[u'known_gaps'][4].get(u'status', u'')[:40]))
ok(u'7e `GR.json#known_gaps[0]` 已据实订正（`prefix` 已落但**无消费端**）',
   u'零消费端' in grj[u'known_gaps'][0].get(u'status', u'') and
   u'N-058' in grj[u'known_gaps'][0].get(u'status', u''),
   u'status=%s' % grj[u'known_gaps'][0].get(u'status', u'')[:90])

print(u'\n== 8. 收尾：仓库真源逐字节核对（探针只在内存里变异）==')
for p in BEFORE:
    ok(u'未改动 %s' % os.path.relpath(p, ROOT).replace(os.sep, u'/'), sha(p) == BEFORE[p])

bad = [r for r in RESULT if not r[0]]
print(u'\n===== 探针结果：%d/%d 通过 =====' % (len(RESULT) - len(bad), len(RESULT)))
for _, name, detail in bad:
    print(u'  \u2717 %s  %s' % (name, detail))
raise SystemExit(1 if bad else 0)
