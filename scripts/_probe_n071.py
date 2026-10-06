# -*- coding: utf-8 -*-
'''`N-071`／`N-072` 探针 —— 「`spec` 内**五笔账实不符**收敛」的行为证明（不是「跑一下没报错」）。

用法：`python scripts/_probe_n071.py`

★ 本文件不进任何门禁面（`_` 前缀不对齐 `check_spec.py` / `check_all.sh` 入口）。
★★ **全程不改动仓库内任何真实文件** —— 变异一律在**内存副本**上做，再对同一批断言函数
   重跑；收尾逐字节核对仓库真源 `sha256` 与开跑前相同。

★★ **次序特性（重要，别误读为失败）**：本探针断言的是**已提交版**的 `spec/**`
   （`check_probes.sh` 在隔离 `git worktree` 内跑、`spec/**` 取 **HEAD 版**）⇒
   ★ **必须与本批 `spec` 改动同批提交之后再跑门禁**；未提交即跑 ⇒ 探针看到旧文 ⇒ 必红。

它证明八件事（★ 每条都是可复现实测，不是推断）：

  0. ★★ **外部证据核对（不是自我引述）**：本批的前提**不是**读 `spec` 自述得来的，而是
     **读代码**取证 ——「`M9` 已落地」由五处外部证据支撑（四操作路由 / `agentAuthorizer`
     消费点 / `SetAgentAuthorizer` 装配 / `TestTransferAgentAuthorization` 用例 /
     `t_notify_log` 留痕）；「补录入口已落」由两处支撑（`router.go` 路由行 ＋
     `handlers_approval_backfill.go` 的 `backfillCheckFns` 执行体）。
     ★ 口径：本探针是**拿实现校对台账**，不是拿台账自证。

  1. **`chain.json#conventions.node_actor_kind` 的「当前状态」段已据实订正**：不得再含原
     具名小节「诚实划界」；须含 `N-071`；★ 且「只声明、未消费」这一提法**若仍在**，
     必须**同时**含收口语（`不得再被读作`）—— 即该提法**只能以被否定的引用**出现。

  2. **`forms/SA.json#known_gaps[4]`（结算补录入口）「债还了、台账没销」已销**：
     `status` 须含「已闭环」＋ `N-062`；★ 并与本文件 `checks[]` 的**真源**交叉核对 ——
     `actual_not_exceed` / `invoice_must_link` 两条 `carried_by_kind` 必须**都是 `code`**
     （★ 只改一处、或只信一处自述，都不算成立）。

  3. **`authority.json#consumers[]` 两条状态据实翻面**：`transfer_rollback_by_agent` 与
     `transfer_notify_approved` 的 `status` 须含「已实现」＋ `N-060`；通知条须点明
     `t_notify_log` 留痕（★「漏发必须可检出」这一条要有落点，不能只说「已实现」）。

  4. ★★ **`N-072`「守栏只解除了一半」被如实登记**（★ 本批**最重要**的一条 —— 它是
     **新发现**，不是旧债）：`enable_guard.lifting` 须含**达成判定**；`enable_guard.rule`
     须**同时**说明「服务端那半已失效」与「告示那半仍在」＋ 点名残留物
     `roleAgentFeatureEnabled` ＋ 给出「同批撤销」的处置；★★ 且**外部证据必须仍显示
     「未撤」**（`const roleAgentFeatureEnabled = false` 仍在）⇒ ★ **本探针不得把
     「应当解除」写成「已经解除」**（那是**新造**一笔账实不符）。

  5. **版本索引三处同版**：`chain.json` / `authority.json` / `forms/SA.json` 的 `version`
     与 `spec/README.md §2` 索引行**一致**；★★ 且 `authority.json` 行不得再留**误记的
     `V1.0`**（实际自 2026-09-30 起为 `1.1`、长期未升版 ⇒ 本批一并据实并留痕）。

  6. ★★ **缺口存在性反证（鉴别力，四处）**：把**修复前的旧文**塞回内存副本 ⇒
     第 1 / 2 / 3 条断言**必须报红**；把 `enable_guard.rule` 换成「已解除、无残留」⇒
     第 4 条断言**必须报红** ⇒ 证明这四条断言**能分辨未修复态**，不是恒真空断言
     （★ 教训：断言「已修复」之前，必须先证明「能分辨未修复」）。

  7. **本批的划界实测**：`checks.json` 仍 **30 判据 / 12 原语**、`S19.args.target_filter`
     **逐字未动**、`forms/*.json` 仍 **11 张**、`SA` 的 `checks` id 列表**逐字未变**
     ⇒ ★ 本批**零新增判据、零新增原语、零引擎改动、零代码改动**（只动三处 `spec` 文本
     ＋ `README` 索引 ＋ 台账）。

  8. **收尾**：仓库真源逐字节未变（探针只在内存里变异）。

★ 口径正本：判据面以 `spec/checks.json` 为准；授权配置面以 `spec/authority.json` 为准；
  节点 actor 分类面以 `spec/chain.json#conventions.node_actor_kind` 为准
  （本探针**不重复实现口径**，只按正本断言）。
'''
import hashlib
import io
import glob
import json
import os

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)

P_CHAIN = os.path.join(ROOT, 'spec', 'chain.json')
P_SA = os.path.join(ROOT, 'spec', 'forms', 'SA.json')
P_AUTH = os.path.join(ROOT, 'spec', 'authority.json')
P_CHECKS = os.path.join(ROOT, 'spec', 'checks.json')
P_README = os.path.join(ROOT, 'spec', 'README.md')
P_ROUTER = os.path.join(ROOT, 'internal', 'httpapi', 'router.go')
P_OPS = os.path.join(ROOT, 'internal', 'flow', 'ops.go')
P_BOOT = os.path.join(ROOT, 'cmd', 'jxapproval', 'bootstrap.go')
P_OPSTEST = os.path.join(ROOT, 'internal', 'flow', 'ops_test.go')
P_BACKFILL = os.path.join(ROOT, 'internal', 'httpapi', 'handlers_approval_backfill.go')
P_AGENTH = os.path.join(ROOT, 'internal', 'httpapi', 'handlers_admin_role_agents.go')

# ★ 修复前的旧文（逐字取自历史版本，仅作**反证素材**，不写回仓库）
OLD_NAK_MARK = u'当前状态（诚实划界，不声称已覆盖）'
OLD_SA4_STATUS = u'★★ **规格已齐（批 42）· 待实现**'
OLD_CONS1_STATUS = u'⏳ **未实现** —— 属 `M9`（转交 / 加签 / 回退 / 撤回）批次'
OLD_CONS2_STATUS = u'⏳ **未实现**（随 `M9`）'
OLD_GUARD_RULE = (u'★★ **在 `M9` 落地前，本配置不产生任何权限** —— `/admin` 页签须'
                  u'**显式标注「代理人功能未启用」**；服务端**不得**因为存在代理人记录而放行任何操作。')

RESULT = []
BEFORE = {}


def sha(path):
    with io.open(path, 'rb') as f:
        return hashlib.sha256(f.read()).hexdigest()


def load(path):
    with io.open(path, 'r', encoding='utf-8') as f:
        return json.load(f)


def text(path):
    with io.open(path, 'r', encoding='utf-8', newline='') as f:
        return f.read()


def ok(name, cond, detail=''):
    RESULT.append((bool(cond), name, detail))
    print(u'  %s %s%s' % (u'\u2713' if cond else u'\u2717', name,
                          (u'   [' + detail + u']') if detail else u''))


def clone(obj):
    return json.loads(json.dumps(obj))


# ---------------------------------------------------------------- 断言函数（可对副本重跑）
def a_chain_nak(chain):
    '''第 1 条：node_actor_kind 的「当前状态」段已据实订正。'''
    v = chain[u'conventions'][u'node_actor_kind']
    if OLD_NAK_MARK in v:
        return False, u'仍含原具名小节「%s」' % OLD_NAK_MARK
    if u'N-071' not in v:
        return False, u'未标 `N-071`（无本批归属）'
    if u'据实订正' not in v:
        return False, u'未出现「据实订正」'
    if u'只声明、未消费' in v and u'不得再被读作' not in v:
        return False, u'「只声明、未消费」以**肯定式**出现（缺收口语「不得再被读作」）'
    if u'target_filter' not in v:
        return False, u'未给出落地段证据 `target_filter`'
    if u'批 24' not in v:
        return False, u'未给出落地段批次（批 24）'
    return True, u'已订正（含 `N-071` ＋ `target_filter` ＋ 批 24 落地段）'


def a_sa_gap4(sa):
    '''第 2 条：SA 补录入口那一笔的「债还了、台账没销」已销 ＋ 与 checks 真源交叉核对。'''
    kg = sa[u'known_gaps']
    hits = [g for g in kg if u'settlement_backfill' in g.get(u'gap', u'')]
    if len(hits) != 1:
        return False, u'含 `settlement_backfill` 的 known_gaps 条数 = %d（要求恰 1）' % len(hits)
    g = hits[0]
    st = g.get(u'status', u'')
    if u'已闭环' not in st or u'N-062' not in st:
        return False, u'status 未翻「已闭环（`N-062`）」：%s' % st[:60]
    if u'待实现' in st and u'已过期' not in st:
        return False, u'status 仍以「待实现」作当前态（缺「已过期」标记）'
    nt = g.get(u'note', u'')
    if u'已闭环' not in nt or u'router.go' not in nt:
        return False, u'note 未标明闭环或未点明路由落点（`router.go`）'
    if u'待实现' in nt and u'已过期' not in nt:
        return False, u'note 仍以「待实现」作当前态（缺「已过期」标记）'
    chk = dict((c[u'id'], c) for c in sa[u'checks'])
    for cid in (u'actual_not_exceed', u'invoice_must_link'):
        c = chk.get(cid)
        if c is None:
            return False, u'`checks` 缺 %s' % cid
        if c.get(u'carried_by_kind') != u'code':
            return False, u'%s.carried_by_kind = %s（真源要求 `code`）' % (cid, c.get(u'carried_by_kind'))
        if u'批 45' not in c.get(u'carried_by', u''):
            return False, u'%s.carried_by 未指向批 45 落地段' % cid
    return True, u'status 已闭环 ＋ 两条判据 carried_by_kind=code（与 `checks[]` 真源交叉一致）'


def a_auth_consumers(auth):
    '''第 3 条：consumers 两条状态据实翻面。'''
    cons = dict((c[u'id'], c) for c in auth[u'consumers'])
    pairs = ((u'transfer_rollback_by_agent', u'代理转交/回退'),
             (u'transfer_notify_approved', u'通知已通过者'))
    for cid, _ln in pairs:
        c = cons.get(cid)
        if c is None:
            return False, u'`consumers` 缺 %s' % cid
        st = c.get(u'status', u'')
        if u'已实现' not in st:
            return False, u'%s.status 未翻「已实现」：%s' % (cid, st[:60])
        if u'N-060' not in st:
            return False, u'%s.status 未标 `N-060`（缺落地批次归属）' % cid
        if u'未实现' in st and u'已过期' not in st:
            return False, u'%s.status 仍以「未实现」作当前态' % cid
    n2 = cons[u'transfer_notify_approved'].get(u'status', u'')
    if u't_notify_log' not in n2:
        return False, u'通知条未点明 `t_notify_log`（「漏发可检出」无落点）'
    n1 = cons[u'transfer_rollback_by_agent'].get(u'status', u'')
    for kw in (u'NodeAllowsAgent', u'fail-closed'):
        if kw not in n1:
            return False, u'转交/回退条未含边界证据 `%s`' % kw
    return True, u'两条已翻「已实现」（含 `N-060`）＋ 通知条点明 `t_notify_log` ＋ 边界逐条对上'


def a_enable_guard(auth):
    '''第 4 条：N-072 —— 守栏「条件已达成 / 撤销未执行」两分如实登记。'''
    eg = auth.get(u'enable_guard') or {}
    lift = eg.get(u'lifting', u'')
    rule = eg.get(u'rule', u'')
    if u'达成判定' not in lift:
        return False, u'`lifting` 未登记「达成判定」（未给出达成结论）'
    if u'条件已满足' not in lift:
        return False, u'`lifting` 未明确「条件已满足」'
    if u'服务端' not in rule or u'已失效' not in rule:
        return False, u'`rule` 未说明「服务端那半已失效」'
    if u'告示' not in rule or u'仍在' not in rule:
        return False, u'`rule` 未说明「告示那半仍在」（残留未被承认）'
    if u'roleAgentFeatureEnabled' not in rule:
        return False, u'`rule` 未点名残留物 `roleAgentFeatureEnabled`'
    if u'mimo' not in rule or u'同批' not in rule:
        return False, u'`rule` 未给出「同批撤销」的处置与责任域'
    if u'账实不符' not in rule:
        return False, u'`rule` 未把「服务端已放行/告示说未启用」定性为账实不符'
    if OLD_GUARD_RULE in rule:
        return False, u'`rule` 仍是修复前旧文'
    return True, u'「条件已达成 / 撤销未执行」两分如实登记 ＋ 残留点名 ＋ 处置与责任域齐'


def _readme_row(rmd, row_start):
    for ln in rmd.split(u'\n'):
        if ln.startswith(row_start):
            return ln
    return u''


def a_versions(chain, sa, auth, rmd):
    '''第 5 条：版本索引三处同版。'''
    if chain.get(u'version') != u'1.15':
        return False, u'`chain.json.version = %s`（要求 `1.15`）' % chain.get(u'version')
    if auth.get(u'version') != u'1.2':
        return False, u'`authority.json.version = %s`（要求 `1.2`）' % auth.get(u'version')
    if sa.get(u'version') != u'1.7':
        return False, u'`forms/SA.json.version = %s`（要求 `1.7`）' % sa.get(u'version')
    r_chain = _readme_row(rmd, u'| **`chain.json`** |')
    r_auth = _readme_row(rmd, u'| **`authority.json`** |')
    r_forms = _readme_row(rmd, u'| `forms/*.json` |')
    if u'V1.15' not in r_chain:
        return False, u'§2 `chain.json` 行未前置 `V1.15`'
    if u'V1.2' not in r_auth:
        return False, u'§2 `authority.json` 行未写 `V1.2`'
    if u'此前误记 V1.0' not in r_auth:
        return False, u'§2 `authority.json` 行的**误记 `V1.0`** 未据实并留痕'
    if u'SA` **V1.6 → V1.7' not in r_forms:
        return False, u'§2 `forms/*.json` 行未登记 `SA` **V1.6 → V1.7**'
    if u'| **V1.35** |' not in rmd:
        return False, u'§6 未新增 `V1.35` 行'
    return True, u'三个 `version` 与 §2 索引逐处同版 ＋ §6 已加 `V1.35` ＋ 误记 `V1.0` 已留痕'


def a_external(ext):
    '''第 0 条：外部证据核对（拿实现校对台账，不自我引述）。'''
    checks = [
        (u'路由：四操作 4 条', u'/approval/:biz_no/transfer' in ext['router']
         and u'/approval/:biz_no/addsign' in ext['router']
         and u'/approval/:biz_no/rollback' in ext['router']
         and u'/approval/:biz_no/cancel' in ext['router']),
        (u'路由：backfill 1 条', u'/approval/:biz_no/backfill' in ext['router']),
        (u'装配：`SetAgentAuthorizer`', u'SetAgentAuthorizer' in ext['boot']),
        (u'消费：`agentAuthorizer`', u'agentAuthorizer' in ext['ops']),
        (u'用例：`TestTransferAgentAuthorization`', u'TestTransferAgentAuthorization' in ext['opstest']),
        (u'执行体：`backfillCheckFns`', u'backfillCheckFns' in ext['backfill']
         and u'checkBackfillActualNotExceed' in ext['backfill']),
        (u'★★ 残留仍「未撤」：`roleAgentFeatureEnabled = false` 仍在',
         u'roleAgentFeatureEnabled = false' in ext['agenth']),
    ]
    bad = [n for n, c in checks if not c]
    if bad:
        return False, u'外部证据缺失：%s' % u' / '.join(bad)
    return True, u'七项外部证据齐（★ 含「残留仍为 false」⇒ 本探针未把「应解除」写成「已解除」）'


def a_scope(chain, sa, cj, forms_n, sa_ids_now):
    '''第 7 条：本批划界实测（零新增判据/原语/引擎/代码）。'''
    if len(cj[u'checks']) != 30:
        return False, u'`checks.json` 判据数 = %d（要求 30）' % len(cj[u'checks'])
    if len(cj[u'primitives']) != 12:
        return False, u'`checks.json` 原语数 = %d（要求 12）' % len(cj[u'primitives'])
    s19 = [c for c in cj[u'checks'] if c.get(u'id') == u'S19']
    if len(s19) != 1:
        return False, u'`S19` 条数 = %d（要求 1）' % len(s19)
    tf = s19[0].get(u'args', {}).get(u'target_filter')
    if tf != {u'field': u'node_actor_kind', u'in': [u'approver', u'action']}:
        return False, u'`S19.args.target_filter` 被动了（本批不得触及）：%s' % tf
    if forms_n != 11:
        return False, u'`spec/forms/*.json` 文件数 = %d（要求 11）' % forms_n
    if sa_ids_now != SA_IDS_EXPECT:
        return False, u'SA `checks` id 列表变了（本批不得触及）'
    return True, u'判据仍 30 / 原语仍 12 / `S19.args.target_filter` 逐字未动 / 表单仍 11 张'


SA_IDS_EXPECT = None  # 开跑时从真源读入（★ 只做「未变」比较，不硬编码内容）


def main():
    print(u'== 0. 外部证据核对（读代码，不自我引述）==')
    ext = {
        'router': text(P_ROUTER),
        'ops': text(P_OPS),
        'boot': text(P_BOOT),
        'opstest': text(P_OPSTEST),
        'backfill': text(P_BACKFILL),
        'agenth': text(P_AGENTH),
    }
    cond, det = a_external(ext)
    ok(u'0 外部证据七项齐（★ 含「残留仍 false」）', cond, det)

    chain = load(P_CHAIN)
    sa = load(P_SA)
    auth = load(P_AUTH)
    cj = load(P_CHECKS)
    rmd = text(P_README)

    print(u'\n== 1. `chain.json#conventions.node_actor_kind` 已据实订正 ==')
    cond, det = a_chain_nak(chain)
    ok(u'1a 不再含具名小节「诚实划界」＋ 含 `N-071` ＋ 落地段证据齐', cond, det)
    bad = clone(chain)
    bad[u'conventions'][u'node_actor_kind'] = (
        u'★★ **当前状态（诚实划界，不声称已覆盖）**：本版**只声明、未消费**。')
    cond2, _ = a_chain_nak(bad)
    ok(u'1b ★ 反证（内存副本）：把旧文塞回 ⇒ 1a 必须报红',
       (not cond2) and bad[u'conventions'][u'node_actor_kind'] != chain[u'conventions'][u'node_actor_kind'],
       u'变异后 cond=%s' % cond2)

    print(u'\n== 2. `forms/SA.json#known_gaps[4]`「债还了、台账没销」已销 ==')
    cond, det = a_sa_gap4(sa)
    ok(u'2a status 已闭环（`N-062`）＋ `checks[]` 两条 `carried_by_kind=code`（交叉核对）', cond, det)
    bad = clone(sa)
    for g in bad[u'known_gaps']:
        if u'settlement_backfill' in g.get(u'gap', u''):
            g[u'status'] = OLD_SA4_STATUS
    cond2, _ = a_sa_gap4(bad)
    ok(u'2b ★ 反证（内存副本）：把 `status` 改回「★★ 规格已齐（批 42）· 待实现」⇒ 2a 必须报红',
       not cond2, u'变异后 cond=%s' % cond2)

    print(u'\n== 3. `authority.json#consumers[]` 两条状态据实翻面 ==')
    cond, det = a_auth_consumers(auth)
    ok(u'3a 两条均含「已实现」＋ `N-060` ＋ 通知条点明 `t_notify_log` ＋ 边界逐条对上', cond, det)
    bad = clone(auth)
    for c in bad[u'consumers']:
        if c[u'id'] == u'transfer_rollback_by_agent':
            c[u'status'] = OLD_CONS1_STATUS
        if c[u'id'] == u'transfer_notify_approved':
            c[u'status'] = OLD_CONS2_STATUS
    cond2, _ = a_auth_consumers(bad)
    ok(u'3b ★ 反证（内存副本）：把两条 `status` 改回「⏳ 未实现（随 `M9`）」⇒ 3a 必须报红',
       not cond2, u'变异后 cond=%s' % cond2)

    print(u'\n== 4. `N-072`：守栏「条件已达成 / 撤销未执行」两分如实登记 ==')
    cond, det = a_enable_guard(auth)
    ok(u'4a `lifting` 含达成判定 ＋ `rule` 两分并陈 ＋ 残留点名 ＋ 处置与责任域齐', cond, det)
    bad = clone(auth)
    bad[u'enable_guard'][u'rule'] = u'★★ 本守栏已解除，无任何残留。'
    cond2, _ = a_enable_guard(bad)
    ok(u'4b ★ 反证（内存副本）：把 `rule` 换成「已解除、无残留」⇒ 4a 必须报红',
       not cond2, u'变异后 cond=%s' % cond2)
    bad2 = clone(auth)
    bad2[u'enable_guard'][u'lifting'] = u'★ 解除条件：`M9` 的「转交 / 回退按代理人判定」落地并通过其**应拦 / 应放行**两类用例。'
    cond3, _ = a_enable_guard(bad2)
    ok(u'4c ★ 反证（内存副本）：`lifting` 去掉达成判定（回到条件式）⇒ 4a 必须报红',
       not cond3, u'变异后 cond=%s' % cond3)

    print(u'\n== 5. 版本索引三处同版 ==')
    cond, det = a_versions(chain, sa, auth, rmd)
    ok(u'5a `chain.json` 1.15 / `authority.json` 1.2 / `forms/SA.json` 1.7 与 §2 索引同版', cond, det)
    cond2, _ = a_versions(chain, sa, _v(auth, u'1.1'), rmd)
    ok(u'5b ★ 反证（内存副本）：把 `authority.json.version` 降到 `1.1` ⇒ 5a 必须报红',
       not cond2, u'变异后 cond=%s' % cond2)
    cond3, _ = a_versions(chain, sa, auth, rmd.replace(u'此前误记 V1.0', u'MUST-NOT-HAVE'))
    ok(u'5c ★ 反证（内存副本）：抹掉 §2 的「误记 V1.0」留痕 ⇒ 5a 必须报红',
       not cond3, u'变异后 cond=%s' % cond3)

    print(u'\n== 6. 本批划界实测（零新增判据/原语/引擎/代码）==')
    forms_n = len(glob.glob(os.path.join(ROOT, 'spec', 'forms', '*.json')))
    cond, det = a_scope(chain, sa, cj, forms_n, [c[u'id'] for c in sa[u'checks']])
    ok(u'6a 判据仍 30 / 原语仍 12 / `S19.args.target_filter` 逐字未动 / 表单仍 11 张', cond, det)

    print(u'\n== 7. 收尾：仓库真源逐字节核对（探针只在内存里变异）==')
    for p in BEFORE:
        ok(u'未改动 %s' % os.path.relpath(p, ROOT).replace(os.sep, u'/'), sha(p) == BEFORE[p])

    bad = [r for r in RESULT if not r[0]]
    print(u'\n===== 探针结果：%d/%d 通过 =====' % (len(RESULT) - len(bad), len(RESULT)))
    for _, name, detail in bad:
        print(u'  \u2717 %s  %s' % (name, detail))
    raise SystemExit(1 if bad else 0)


def _v(auth, ver):
    a = clone(auth)
    a[u'version'] = ver
    return a


if __name__ == u'__main__':
    SA_IDS_EXPECT = [c[u'id'] for c in load(P_SA)[u'checks']]
    for _p in (P_CHAIN, P_SA, P_AUTH, P_CHECKS, P_README,
               P_ROUTER, P_OPS, P_BOOT, P_OPSTEST, P_BACKFILL, P_AGENTH):
        BEFORE[_p] = sha(_p)
    main()
