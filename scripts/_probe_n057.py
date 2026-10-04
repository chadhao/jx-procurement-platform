# -*- coding: utf-8 -*-
'''`N-057` 探针 —— `S19` 收集面放宽 与 复合 `actor` 归一 的行为证明（不是「跑一下没报错」）。

用法：`python scripts/_probe_n057.py`

★ 本文件不进任何门禁面（`_` 前缀不对齐 `check_spec.py` / `check_all.sh` 入口）。
★★ 更强的隔离：**全程不改动仓库内任何真实文件** —— 变异一律写进**临时目录**
   （`chain.json` 的变异副本 ＋ `checks.json` 的副本），再以
   `python scripts/check_spec.py <临时清单>` 跑 **同一套真实判据**
   （`check_spec.py#abs_path` 以仓库根为基准解析相对路径 ⇒ 只有被改写的那几条路径落到临时副本，
   其余判据仍读仓库真源）。收尾核对仓库内 5 个文件的 `sha256` 与开跑前逐字节相同。

它证明七件事（★ 每条都是可复现实测，不是推断）：

  1. **正向**：真 spec ⇒ `S19`（`collect = routes.**.actor`）**0 报错**，收集面命中 **45** 处。
  2. **新纳入的位点确在面内**：命中集合**含** `inserted_nodes[*].actor` 归一后的 `purchaser`
     与 `nodes[*].branches[*].actor` 的 `supervisor`。
  3. ★★ **「未纳入面」的声明核对**：命中集合**不含** `supervisor | project_general_manager`
     与 `purchaser | project_general_manager` ⇒ 证实 `contract_approval.order[*].actor` /
     `doc_chains.*.nodes[*].actor` **确实在收集面之外**（「不为死数据背书」这句声明属实，非空话）。
  4. **行为不变性**：`inserted_nodes[2]` 归一后 `actor = purchaser`、`required = true`，
     而 `roles.purchaser.node_actor_kind == action` ⇒ ★ 归一前后**都**落动作环节（不生成审批任务）。
  5. ★★ **鉴别力（单点变异）**：只把 `inserted_nodes[2].actor` 改回复合串 `purchaser + 评审组`
     ⇒ 全清单**恰 1 处**违规，且文案点名该值。
  6. ★★ **缺口存在性反证**：同一变异状态下，把 `S19.args.collect` 换回**旧值**
     `routes.*.nodes[*].actor` ⇒ **违规 0 处（静默放行）**；换回**新值** ⇒ **1 处拦下**。
     ★ 只有「放宽即拦」＋「退回即漏」两者齐备，才证明放宽在承重、且旧收集面确实漏了它。
  7. **fail-closed**：`collect` 写到不存在的路径 ⇒ 命中 0 ⇒ `min_hits: 1` ⇒ **报可见失败**
     （不许把「声明写错」静默成「通过」）。

★ 口径正本 ＝ `spec/checks.json#S19`（`desc` 裁决语义）；本探针**不重复实现口径**，只调用真原语。
★ Go 侧同结论由 `go test ./...` 覆盖（`internal/specload.Load` 在装载期即跑本套判据）。
'''
import hashlib
import io
import json
import os
import shutil
import subprocess
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)
sys.path.insert(0, HERE)
import check_spec as CS  # noqa: E402

SPEC_CHAIN = os.path.join(ROOT, 'spec', 'chain.json')
SPEC_CHECKS = os.path.join(ROOT, 'spec', 'checks.json')
CHECK_SPEC = os.path.join(HERE, 'check_spec.py')
WATCH = [SPEC_CHAIN, SPEC_CHECKS,
         os.path.join(ROOT, 'spec', 'README.md'),
         os.path.join(ROOT, 'COLLAB.md'),
         os.path.join(ROOT, 'REMAINING.md')]
OLD_COLLECT = 'routes.*.nodes[*].actor'
NEW_COLLECT = 'routes.**.actor'
RESULT = []


def sha(path):
    with open(path, 'rb') as fh:
        return hashlib.sha256(fh.read()).hexdigest()


def ok(name, cond, detail=''):
    RESULT.append((bool(cond), name, detail))
    print('%s %s%s' % ('✓' if cond else '✗', name, ('  —— ' + detail) if detail else ''))


def load(path):
    with io.open(path, 'r', encoding='utf-8') as fh:
        return json.load(fh)


def dump(path, obj):
    with io.open(path, 'w', encoding='utf-8', newline=chr(10)) as fh:
        json.dump(obj, fh, ensure_ascii=False, indent=2)


def s19(checks):
    hit = [c for c in checks['checks'] if c.get('id') == 'S19']
    assert len(hit) == 1
    return hit[0]


def run_check(checks_path):
    p = subprocess.run([sys.executable, CHECK_SPEC, checks_path],
                       cwd=ROOT, capture_output=True, text=True)
    return p.returncode, (p.stdout + p.stderr)


def s19_lines(out):
    return [ln.strip() for ln in out.splitlines()
            if ln.strip().startswith('[S19]')]


# ------------------------------------------------------------------ 0. 基线 sha256
print('== 0. 基线（收尾将逐字节核对）==')
BEFORE = dict((p, sha(p)) for p in WATCH)
ok('仓库待核文件 5 个已取 sha256', len(BEFORE) == 5)

CHECKS_RAW = load(SPEC_CHECKS)
CHAIN_RAW = load(SPEC_CHAIN)
S19_RAW = s19(CHECKS_RAW)
ok('真清单 S19.args.collect == routes.**.actor', S19_RAW['args'].get('collect') == NEW_COLLECT,
   'actual=%r' % S19_RAW['args'].get('collect'))
ok('真清单 S19.args 其余键仍在（file/target_file/target_dict/extra_allowed/min_hits）',
   set(S19_RAW['args']) == {'file', 'collect', 'target_file', 'target_dict', 'extra_allowed', 'min_hits'},
   'keys=%s' % sorted(S19_RAW['args']))

# ------------------------------------------------------------------ 1. 正向
print('\n== 1. 正向（真 spec）==')
probs = CS.prim_ref_exists(dict(S19_RAW['args'], __cid__='S19'))
hits = CS.sel(load(SPEC_CHAIN), CS.toks(NEW_COLLECT))
ok('S19（真 spec）零报错', probs == [], '报错 %d 处：%s' % (len(probs), probs[:2]))
ok('收集面命中 45 处（旧面为 41 处 ⇒ 新纳入 4 处）', len(hits) == 45, 'hits=%d' % len(hits))

# ------------------------------------------------------------------ 2/3. 面的边界
print('\n== 2/3. 收集面的边界（声明核对）==')
ins = CHAIN_RAW['routes']['purchase_tier3']['branches']['tier3_plus']['inserted_nodes']
ok('命中集合含 inserted_nodes 归一值 purchaser（该位点确在新面内）', 'purchaser' in hits)
ok('命中集合含 nodes[*].branches[*].actor 的 supervisor（该位点确在新面内）', 'supervisor' in hits)
DECOR = ['supervisor | project_general_manager', 'purchaser | project_general_manager']
ok('命中集合不含两类装饰性 actor（contract_approval / doc_chains 确在面外）',
   all(d not in hits for d in DECOR), '面外取值=%s' % [d for d in DECOR if d in hits])

# ------------------------------------------------------------------ 4. 行为不变性
print('\n== 4. 行为不变性（归一不改流程语义）==')
ok('inserted_nodes[2] 归一后 actor == purchaser', ins[2]['actor'] == 'purchaser',
   'actual=%r' % ins[2]['actor'])
ok('inserted_nodes[2] 仍 required == true', ins[2]['required'] is True)
ok('roles.purchaser.node_actor_kind == action ⇒ 落动作环节（不生成审批任务）',
   CHAIN_RAW['roles']['purchaser'].get('node_actor_kind') == 'action')

# ------------------------------------------------------------------ 5/6/7. 临时目录内的变异
print('\n== 5/6/7. 变异（临时目录；仓库真源零改动）==')
TMP = tempfile.mkdtemp(prefix='jx_n057_')
try:
    MUT = json.loads(json.dumps(CHAIN_RAW))
    MUT['routes']['purchase_tier3']['branches']['tier3_plus']['inserted_nodes'][2]['actor'] = 'purchaser + 评审组'
    MUT_CHAIN = os.path.join(TMP, 'chain.mut.json')
    dump(MUT_CHAIN, MUT)

    def mk_checks(collect, chain_path, tag):
        c = json.loads(json.dumps(CHECKS_RAW))
        s19(c)['args']['collect'] = collect
        s19(c)['args']['file'] = chain_path
        p = os.path.join(TMP, 'checks.%s.json' % tag)
        dump(p, c)
        return p

    ck_mut_new = mk_checks(NEW_COLLECT, MUT_CHAIN, 'mut_new')
    ck_mut_old = mk_checks(OLD_COLLECT, MUT_CHAIN, 'mut_old')
    ck_real_new = mk_checks(NEW_COLLECT, SPEC_CHAIN, 'real_new')
    ck_bogus = mk_checks('routes.不存在的分支[*].actor', SPEC_CHAIN, 'bogus')

    rc, out = run_check(ck_real_new)
    ok('真数据 ＋ 新 collect ⇒ 全清单通过（rc=0）', rc == 0 and not s19_lines(out),
       'rc=%d S19=%d 条' % (rc, len(s19_lines(out))))

    rc, out = run_check(ck_mut_new)
    lines = s19_lines(out)
    ok('★ 变异（复合串回灌）＋ 新 collect ⇒ **恰 1 处**违规且点名该值',
       rc == 1 and len(lines) == 1 and 'purchaser + 评审组' in lines[0],
       'rc=%d n=%d %s' % (rc, len(lines), lines[:1]))

    rc, out = run_check(ck_mut_old)
    lines = s19_lines(out)
    ok('★★ 缺口反证：同一变异 ＋ **旧** collect ⇒ **0 处（静默放行）**',
       rc == 0 and not lines, 'rc=%d S19=%d 条' % (rc, len(lines)))

    rc, out = run_check(ck_bogus)
    lines = s19_lines(out)
    ok('fail-closed：collect 指向不存在路径 ⇒ 命中 0 ⇒ 报可见失败',
       rc == 1 and len(lines) == 1 and u'仅命中 0 处' in lines[0],
       'rc=%d %s' % (rc, lines[:1]))
finally:
    shutil.rmtree(TMP, ignore_errors=True)

# ------------------------------------------------------------------ 收尾
print('\n== 收尾：仓库真源逐字节核对 ==')
for p in WATCH:
    ok('未改动 %s' % os.path.relpath(p, ROOT).replace(os.sep, '/'), sha(p) == BEFORE[p])

bad = [r for r in RESULT if not r[0]]
print('\n===== 探针结果：%d/%d 通过 =====' % (len(RESULT) - len(bad), len(RESULT)))
for _, name, detail in bad:
    print('  ✗ %s  %s' % (name, detail))
raise SystemExit(1 if bad else 0)
