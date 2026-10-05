# -*- coding: utf-8 -*-
'''`N-059` 探针 —— 「`known_gaps` 账实不符三笔收敛」的行为证明（不是「跑一下没报错」）。

用法：`python scripts/_probe_n059.py`

★ 本文件不进任何门禁面（`_` 前缀不对齐 `check_spec.py` / `check_all.sh` 入口）。
★★ **全程不改动仓库内任何真实文件** —— 变异一律在**内存副本**上做，再对同一批断言函数
   重跑；收尾逐字节核对仓库真源 `sha256` 与开跑前相同。

它证明六件事（★ 每条都是可复现实测，不是推断）：

  0. ★★ **依据核对（外部证据，不是自我引述）**：本批「CT 应补 `applicant_department`」的
     三条依据**都在仓库里可机械核对** —— ① `docs/07-Template-Build-Guide.md` §4.2 把
     `department` 列为**必须登记的映射**、后果写「部门维度统计与行级权限（`DEPT` /
     `CHARGE_DEPT`）失去依据」；② `internal/permission/dataset.go` 的 `DEPT` /
     `CHARGE_DEPT` 令牌**就是** `department IN (…)`（出现 ≥2 处）；③ `internal/store/models.go`
     注明该列存在的**目的**正是这两个令牌。★ 并核对**同类先例**：`handlers_submission.go`
     曾为「两个令牌在该报送上**永远命中 0 条**」专门补写身份字段 ⇒ 「单据缺部门 ⇒ 行级
     权限无依据」**是本项目已确认的缺陷模式**，不是本批新造的说法。
  1. **BJ 第 2 条「有效报价判定」已据实订正**：status 不得再写「未定」；须含 `decision`
     且指向 `RFQ` 侧；★★ 并**交叉核对** `RFQ.json` 侧确已宣告「`BJ` 该条由此关闭」
     ⇒ **两处真源不矛盾**（★ 只改一处、或只信一处自述，都不算成立）。
  2. **CT 第 3 条（`R-21` 加注建议）已销项**：status 不得再写「以制度为准，本表单已落全 8 组」
     原状态，须含 `N-059`；★ 且**外部证据在位** —— `RESOLUTIONS.md#R-21` 段内确有
     「对 #6 的澄清（2026-09-29 补注…）」整段、其 `change_log` 有 **V1.3** 行。
  3. **CT 缺「申请人 / 所属部门」字段对 ⇒ 已补齐，且 11/11 一致**：CT header 须含
     `applicant` ＋ `applicant_department`，且核心声明键与其余表单**逐值相同**
     （`type` / `required` / `source` / `immutable` / `carried_by_kind`）；★ 断言
     `spec/forms/*.json` **每张**都含这两个字段名（数量 == 文件数 == 11）。
  4. ★★ **缺口存在性反证（鉴别力，三处）**：把**修复前的旧文**塞回内存副本 ⇒ 第 1 / 2 条断言
     **必须报红**；把 CT 的 `applicant_department` 从内存副本删掉 ⇒ 第 3 条断言**必须报红**
     ⇒ 证明这三条断言**能数到非 0**，不是恒真空断言
     （★ 教训：断言「0 条」前必须先证明「能数到非 0」）。
  5. **本批的划界实测**：CT / BJ 的 `checks` id 列表**逐字未变**、`checks.json` 仍
     **30 判据 / 12 原语**、`acceptance.csv` 仍 **98 行** ⇒ ★ 本批**零新增判据、零新增原语、
     零引擎改动**（只动 `known_gaps` 文本与 CT 的两个**声明型字段**）。
  6. **收尾**：仓库真源逐字节未变（探针只在内存里变异）。

★ 口径正本：判据面以 `spec/checks.json` 为准；行级权限口径以 `docs/01-PRD.md §4.2` 为准
  （本探针**不重复实现口径**，只按正本断言）。
'''
import hashlib
import io
import glob
import json
import os

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)

P_BJ = os.path.join(ROOT, 'spec', 'forms', 'BJ.json')
P_CT = os.path.join(ROOT, 'spec', 'forms', 'CT.json')
P_RFQ = os.path.join(ROOT, 'spec', 'forms', 'RFQ.json')
P_PR = os.path.join(ROOT, 'spec', 'forms', 'PR.json')
P_CHECKS = os.path.join(ROOT, 'spec', 'checks.json')
P_CSV = os.path.join(ROOT, 'spec', 'acceptance.csv')
P_ANCHORS = os.path.join(ROOT, 'spec', 'institution-anchors.json')
P_README = os.path.join(ROOT, 'spec', 'README.md')
P_RES = os.path.join(ROOT, 'spec', 'RESOLUTIONS.md')
P_D07 = os.path.join(ROOT, 'docs', '07-Template-Build-Guide.md')
P_PRD = os.path.join(ROOT, 'docs', '01-PRD.md')
P_DATASET = os.path.join(ROOT, 'internal', 'permission', 'dataset.go')
P_MODELS = os.path.join(ROOT, 'internal', 'store', 'models.go')
P_HAPPR = os.path.join(ROOT, 'internal', 'httpapi', 'handlers_approval.go')
P_HSUB = os.path.join(ROOT, 'internal', 'httpapi', 'handlers_submission.go')

# ★ 修复前的旧文（逐字取自历史版本，仅作**反证素材**，不写回仓库）
OLD_BJ_STATUS = u'未定（不阻塞本单）'
OLD_CT2_STATUS = u'以制度为准，本表单已落全 8 组'

# ★ 核心声明键（比范式时逐值比对；`carried_by` / `origin_ref` / `note` 允许各自表述）
CORE_KEYS = [u'name', u'type', u'required', u'source', u'immutable', u'carried_by_kind']

EXPECT_CT_CHECKS = [
    u'mandatory_clauses_complete', u'payee_name_matches_supplier', u'prepay_pair',
    u'sole_source_link', u'tier3_quote_link', u'non_template_reason',
    u'equipment_after_sales', u'tech_opinion_needed', u'contract_no_format',
    u'sign_after_approval', u'no_amount_tiering', u'amount_vs_pr',
    u'no_self_purchaser', u'idempotency_key',
]
EXPECT_BJ_CHECKS = [
    u'min_three_valid_quotes', u'quotes_must_be_independent',
    u'technical_compliance_filled', u'selected_must_be_valid_and_compliant',
    u'selection_reason_immutable', u'not_single_source',
]

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


# ---------------------------------------------------------------- 断言函数（可对副本重跑）
def a_bj_valid_quote(bj):
    '''第 1 条：BJ 的「有效报价判定」条目 —— 已订正 ＋ 指向 RFQ ＋ 不再自称「未定」。'''
    hits = [g for g in bj[u'known_gaps'] if u'有效' in g.get(u'gap', u'') and u'报价' in g.get(u'gap', u'')]
    if len(hits) != 1:
        return False, u'含「有效报价」的 known_gaps 条数 = %d（要求恰 1）' % len(hits)
    g = hits[0]
    st = g.get(u'status', u'')
    if OLD_BJ_STATUS in st or u'已过期' not in st:
        return False, u'status 仍沿用旧状态（或缺「已过期」声明）：%s' % st[:60]
    if u'已明确' not in st:
        return False, u'status 未写「已明确」：%s' % st[:60]
    dec = g.get(u'decision', u'')
    if u'RFQ' not in dec or u'technical_compliance' not in dec:
        return False, u'decision 未同时指向 RFQ 与 technical_compliance：%s' % dec[:60]
    return True, u'恰 1 条 · status=已明确 · decision 指向 RFQ × technical_compliance'


def a_ct_r21_closed(ct):
    '''第 2 条：CT 的 `R-21` 加注建议 —— 已销项（不再沿用旧状态）。'''
    hits = [g for g in ct[u'known_gaps'] if u'R-21#6' in g.get(u'gap', u'')]
    if len(hits) != 1:
        return False, u'含 `R-21#6` 的 known_gaps 条数 = %d（要求恰 1）' % len(hits)
    st = hits[0].get(u'status', u'')
    if OLD_CT2_STATUS in st:
        return False, u'status 仍是旧状态：%s' % st[:60]
    if u'已闭环' not in st or u'N-059' not in st:
        return False, u'status 未写「已闭环」/ 未引 N-059：%s' % st[:60]
    return True, u'旧状态已销项 · status 含 N-059'


def a_ct_dept_pair(ct, others):
    '''第 3 条：CT 补齐 `applicant` / `applicant_department`，且与其余表单范式一致。'''
    hdr = None
    for s in ct[u'sections']:
        if s.get(u'id') == u'header':
            hdr = s
    if hdr is None:
        return False, u'找不到 header 段'
    by = {f.get(u'name'): f for f in hdr.get(u'fields', [])}
    miss = [n for n in (u'applicant', u'applicant_department') if n not in by]
    if miss:
        return False, u'CT header 缺字段：%s' % (u'/' .join(miss))
    for n in (u'applicant', u'applicant_department'):
        f = by[n]
        if f.get(u'source') != u'system' or f.get(u'required') is not True \
                or f.get(u'immutable') is not True or f.get(u'carried_by_kind') != u'code':
            return False, u'%s 声明不符（source/required/immutable/carried_by_kind）' % n
    # 11/11 一致性：每张表都含这两个字段名
    lack = []
    for path, d in others:
        names = []
        for s in d.get(u'sections', []):
            for f in s.get(u'fields', []):
                names.append(f.get(u'name'))
        if u'applicant' not in names or u'applicant_department' not in names:
            lack.append(os.path.basename(path))
    if lack:
        return False, u'仍有表单缺该字段对：%s' % (u','.join(lack))
    return True, u'CT 已补 ＋ %d 张表单全部含该字段对' % len(others)


def a_core_keys_match(ct, pr):
    '''第 3 条（续）：CT 与 PR 的 `applicant_department` 核心声明键**逐值相同**。'''

    def pick(d, name):
        for s in d[u'sections']:
            for f in s.get(u'fields', []):
                if f.get(u'name') == name:
                    return f
        return None

    a, b = pick(ct, u'applicant_department'), pick(pr, u'applicant_department')
    if a is None or b is None:
        return False, u'取字段失败（CT=%s / PR=%s）' % (a is not None, b is not None)
    diff = [k for k in CORE_KEYS if a.get(k) != b.get(k)]
    if diff:
        return False, u'核心键取值不同：%s' % (u','.join(diff))
    return True, u'六键（%s）逐值相同' % u'/'.join(CORE_KEYS)


def a_anchor_invariant(anchors):
    '''第 5 条（续）：制度锚点索引的逐条款不变式 —— `len(spec[]) + unstable_count == citation_count`。'''
    cls = anchors.get(u'clauses') or []
    if len(cls) != 29:
        return False, u'条款数 = %d（要求 29）' % len(cls)
    bad = []
    for c in cls:
        if len(c.get(u'spec', [])) + c.get(u'unstable_count', 0) != c.get(u'citation_count', 0):
            bad.append(c.get(u'clause'))
    if bad:
        return False, u'%d 条不满足不变式：%s' % (len(bad), u','.join(bad[:3]))
    return True, u'29 条款全部满足（spec[] 共 %d 条）' % sum(len(c.get(u'spec', [])) for c in cls)


def a_anchor_in_sync():
    '''第 5 条（续）：索引与真源**逐条款一致** —— 唯一口径来源 = scripts/gen_institution_anchors.py。'''
    import sys
    sys.path.insert(0, HERE)
    import gen_institution_anchors as gen
    problems = gen.audit(ROOT)
    if problems:
        return False, u'%d 处失真：%s' % (len(problems), problems[0][:90])
    return True, u'audit() 零失真'


def a_anchors_prose(anchors):
    '''第 4 笔：索引的**版本 / `scope` 散文 / `change_log`** 必须与 `clauses[]` 数据同版。

    ★ 动因（实测）：`gen_institution_anchors.py --rewrite` **只回填 `clauses[]` 的四项**，
      版本 / 散文 / `change_log` **一律原样保留** ⇒ ★ 回填之后散文会**静默失真**，
      且 `check_spec.py` 的 `[META]` 自审**只比 `clauses[]`、看不到散文** ⇒ 机械审计抓不住。
    '''
    cls = anchors.get(u'clauses') or []
    n_spec = sum(len(c.get(u'spec', [])) for c in cls)
    n_un = sum(c.get(u'unstable_count', 0) for c in cls)
    n_cit = sum(c.get(u'citation_count', 0) for c in cls)
    sc = anchors.get(u'scope') or u''
    bad = []
    if anchors.get(u'version') != u'1.4':
        bad.append(u'version=%s（要求 1.4）' % anchors.get(u'version'))
    if (u'共 **%d 条**指针' % n_spec) not in sc:
        bad.append(u'`scope` 散文未见「共 **%d 条**指针」' % n_spec)
    if (u'现行 **%d 处**' % n_un) not in sc:
        bad.append(u'`scope` 散文未见「现行 **%d 处**」' % n_un)
    if u'1.4' not in [e.get(u'version') for e in (anchors.get(u'change_log') or [])]:
        bad.append(u'`change_log` 无 1.4 条目')
    if bad:
        return False, u'; '.join(bad[:3])
    return True, (u'版本 1.4 · `scope` 散文计数与数据一致（%d 条 / %d 处 / 引用 %d）· `change_log` 含 1.4'
                  % (n_spec, n_un, n_cit))


def a_readme_version_sync(readme_text, checks_version):
    '''第 4 笔：`spec/README.md` §2 的索引行必须与文件内**实际版本**同版（本批抓到差一版）。'''
    hits = [ln for ln in readme_text.split(u'\n') if ln.startswith(u'| **`checks.json`** |')]
    if len(hits) != 1:
        return False, u'`checks.json` 行命中 %d 次' % len(hits)
    # ★ 2026-10-06 修（对规格演进免疫）：原实现把「12 原语 / 30 判据」与「§6 须含 V1.25 行」
    #   两处**常数**写死在断言里 ⇒ 每次 bump 都误红（本次 `checks.json` 1.23→1.24 即触发，
    #   且 `5k` 的内存变异因替换目标变成**空操作**而连带失败）。
    #   ⇒ 只守**不变量**：① §2 行版本 == 真源版本；② §6 变更日志表体非空。
    #   ★ 与 `N-049`/`N-058` 同族：**守不变量，不守常数** ——「临时批次的计数 ≠ 系统不变量」。
    want = u'✅ **V%s' % checks_version
    if want not in hits[0]:
        return False, u'§2 行未见「✅ **V%s」⇒ 索引与真源不同版' % checks_version
    if not [ln for ln in readme_text.split(u'\n') if ln.startswith(u'| **V1.')]:
        return False, u'§6 变更日志无数据行'
    return True, (u'§2 的 `checks.json` 行版本 == 文件内实际 V%s · §6 变更日志非空'
                  % checks_version)


# ================================================================ 0. 依据核对
def main():
    print(u'== 0. 依据核对（外部证据；★ 不采信本议题的自述）==')
    d07, res, prd = text(P_D07), text(P_RES), text(P_PRD)
    dataset, models = text(P_DATASET), text(P_MODELS)
    happr, hsub = text(P_HAPPR), text(P_HSUB)

    ok(u'0a `docs/07 §4.2` 把 `department` 列为必须登记的映射 ＋ 明写后果「失去依据」',
       u'| `department` |' in d07 and u'失去依据' in d07,
       u'含 `| department |`=%s · 含「失去依据」=%s' % (u'| `department` |' in d07, u'失去依据' in d07))
    ok(u'0b 同一表内**对照组**：`purpose_class_l1` 亦在列（能数到非 0，本断言不是空话）',
       u'purpose_class_l1' in d07, u'含 purpose_class_l1=%s' % (u'purpose_class_l1' in d07))

    r21 = res.split(u'## R-21', 1)[-1].split(u'\n## ', 1)[0]
    ok(u'0c `RESOLUTIONS.md#R-21` 段内确有「对 #6 的澄清」补注（含 2026-09-29）',
       u'对 #6 的澄清' in r21 and u'2026-09-29' in r21,
       u'段长 %d 字' % len(r21))
    ok(u'0d `RESOLUTIONS.md` change_log 含 **V1.3** 行且该行含 `R-21#6`',
       any(u'R-21#6' in ln and u'V1.3' in ln for ln in res.splitlines()),
       u'命中行数=%d' % len([ln for ln in res.splitlines() if u'R-21#6' in ln and u'V1.3' in ln]))

    n_scope = dataset.count(u'col("department") + " IN ("')
    ok(u'0e `internal/permission/dataset.go`：`DEPT`/`CHARGE_DEPT` 令牌**就是** `department IN (…)`',
       u'ScopeDept' in dataset and u'ScopeChargeDept' in dataset and n_scope >= 2,
       u'ScopeDept=%s · ScopeChargeDept=%s · department-IN 出现 %d 处'
       % (u'ScopeDept' in dataset, u'ScopeChargeDept' in dataset, n_scope))
    ok(u'0f `internal/store/models.go` 注明该列的目的就是 DEPT / CHARGE_DEPT 两个令牌',
       u'DEPT' in models and u'CHARGE_DEPT' in models and u'Department' in models,
       u'三处命中均成立')
    n_dept = happr.count(u'firstNonEmptyStr(body.Department, idn.Department)')
    ok(u'0g `handlers_approval.go` 已有**通用**部门写入（与 doc_type 无关）⇒ 零引擎改动',
       n_dept >= 2, u'出现 %d 处（提交 ＋ preview）' % n_dept)
    ok(u'0h ★ 同类先例：`handlers_submission.go` 曾为「两个令牌**永远命中 0 条**」补写身份字段',
       u'永远命中 0 条' in hsub and u'DEPT' in hsub,
       u'含该注释=%s' % (u'永远命中 0 条' in hsub))
    ok(u'0i `docs/01-PRD.md §4.2` 行级口径含「本部门」「所分管部门」（★ 行级权限的依据）',
       u'本部门' in prd and u'所分管部门' in prd, u'两处命中成立')

    # ================================================================ 1/2/3
    bj, ct, rfq, pr = load(P_BJ), load(P_CT), load(P_RFQ), load(P_PR)
    all_forms = [(p, load(p)) for p in sorted(glob.glob(os.path.join(ROOT, 'spec', 'forms', '*.json')))]

    print(u'\n== 1. BJ：第 2 条「有效报价判定」据实订正（＋ 与 RFQ 侧交叉核对）==')
    cond, det = a_bj_valid_quote(bj)
    ok(u'1a BJ 该条已订正（恰 1 条 · 不再写「未定」 · decision 指向 RFQ × 技术符合）', cond, det)
    rfq_closed = [g for g in rfq[u'known_gaps']
                  if u'BJ' in json.dumps(g, ensure_ascii=False) and u'关闭' in json.dumps(g, ensure_ascii=False)]
    ok(u'1b ★ 交叉核对：`RFQ.json` 侧确已宣告「`BJ` 该条由此关闭」⇒ 两处真源不矛盾',
       len(rfq_closed) >= 1, u'命中 %d 条' % len(rfq_closed))

    print(u'\n== 2. CT：第 3 条（`R-21` 加注建议）已销项 ==')
    cond, det = a_ct_r21_closed(ct)
    ok(u'2a CT 该条已销项（旧状态不再出现 · status 引 N-059）', cond, det)

    print(u'\n== 3. CT：补齐「申请人 / 所属部门」字段对 ＋ 11/11 一致 ==')
    n_files = len(all_forms)
    ok(u'3a 表单文件数实测 = 11（★ 后面的「11/11」不是套话）', n_files == 11, u'n=%d' % n_files)
    others = [(p, d) for p, d in all_forms if not p.endswith(u'CT.json')]
    cond, det = a_ct_dept_pair(ct, others)
    ok(u'3b CT header 含 `applicant` ＋ `applicant_department`，声明键合规，且其余 10 张均含该字段对',
       cond, det)
    cond, det = a_core_keys_match(ct, pr)
    ok(u'3c CT 该字段与 PR 的**核心声明键逐值相同**（非「看起来像」）', cond, det)
    ct_gap4 = [g for g in ct[u'known_gaps'] if u'field_id' in g.get(u'gap', '')]
    ok(u'3d CT 第 5 条状态已收窄（引 B6 ⇒ 用途侧已解决；引 applicant ⇒ 部门侧本版补齐）',
       len(ct_gap4) == 1 and u'B6' in ct_gap4[0].get(u'status', u'')
       and u'applicant' in ct_gap4[0].get(u'status', u''),
       u'条数=%d' % len(ct_gap4))

    print(u'\n== 4. 缺口存在性反证（★ 一次只变异一处，且只在内存副本上）==')
    bj_bad = json.loads(json.dumps(bj, ensure_ascii=False))
    for g in bj_bad[u'known_gaps']:
        if u'有效' in g.get(u'gap', u'') and u'报价' in g.get(u'gap', u''):
            g[u'status'] = OLD_BJ_STATUS
            g.pop(u'decision', None)
    cond, det = a_bj_valid_quote(bj_bad)
    ok(u'4a 反证①：把 BJ 的旧 status 塞回 ⇒ 第 1 条断言**报红**（能数到非 0）', not cond, det)

    ct_bad = json.loads(json.dumps(ct, ensure_ascii=False))
    for g in ct_bad[u'known_gaps']:
        if u'R-21#6' in g.get(u'gap', u''):
            g[u'status'] = OLD_CT2_STATUS
    cond, det = a_ct_r21_closed(ct_bad)
    ok(u'4b 反证②：把 CT 的旧 status 塞回 ⇒ 第 2 条断言**报红**', not cond, det)

    ct_bad2 = json.loads(json.dumps(ct, ensure_ascii=False))
    for s in ct_bad2[u'sections']:
        if s.get(u'id') == u'header':
            s[u'fields'] = [f for f in s[u'fields'] if f.get(u'name') != u'applicant_department']
    cond, det = a_ct_dept_pair(ct_bad2, others)
    ok(u'4c 反证③：从内存副本删掉 CT 的 `applicant_department` ⇒ 第 3 条断言**报红**', not cond, det)

    cond, det = a_core_keys_match(ct, pr)
    ok(u'4d 变异隔离：反证③ 的那次变异**不污染**核心键比对（未变异时仍成立）', cond, det)

    print(u'\n== 5. 划界实测（本批零新增判据 / 零新增原语 / 零引擎改动）==')
    cj = load(P_CHECKS)
    ok(u'5a CT 的 `checks` id 列表**逐字未变**（%d 条）' % len(EXPECT_CT_CHECKS),
       [c[u'id'] for c in ct[u'checks']] == EXPECT_CT_CHECKS,
       u'实际=%d 条' % len(ct[u'checks']))
    ok(u'5b BJ 的 `checks` id 列表**逐字未变**（%d 条）' % len(EXPECT_BJ_CHECKS),
       [c[u'id'] for c in bj[u'checks']] == EXPECT_BJ_CHECKS,
       u'实际=%d 条' % len(bj[u'checks']))
    ok(u'5c `checks.json` 仍 **30 判据 / 12 原语**',
       len(cj[u'checks']) == 30 and len(cj[u'primitives']) == 12,
       u'%d 判据 / %d 原语' % (len(cj[u'checks']), len(cj[u'primitives'])))
    csv_rows = [ln for ln in text(P_CSV).strip().split(u'\n') if ln.strip()]
    ok(u'5d `acceptance.csv` 仍 **98 行**（含表头 ⇒ 97 条判据）', len(csv_rows) == 98, u'%d 行' % len(csv_rows))
    ok(u'5e BJ/CT 版本号已递增（V1.1）',
       bj.get(u'version') == u'1.1' and ct.get(u'version') == u'1.1',
       u'BJ=%s / CT=%s' % (bj.get(u'version'), ct.get(u'version')))
    cond, det = a_anchor_in_sync()
    ok(u'5f ★ 连带实测：本批在 CT 里新增/删除了「制度第 X 条」引用 ⇒ 制度锚点索引**必须同批重算**', cond, det)
    cond, det = a_anchor_invariant(load(P_ANCHORS))
    ok(u'5g 制度锚点索引逐条款不变式成立（`len(spec[]) + unstable_count == citation_count`）', cond, det)

    anch = load(P_ANCHORS)
    cond, det = a_anchors_prose(anch)
    ok(u'5h ★ 第 4 笔：锚点索引的**版本 / `scope` 散文 / `change_log`** 三者与 `clauses[]` 数据同版'
       u'（★ `--rewrite` 只回填 `clauses[]` ⇒ 散文会静默失真、机械审计看不见）', cond, det)
    anch_bad = json.loads(json.dumps(anch))
    anch_bad[u'scope'] = (anch_bad[u'scope'].replace(u'共 **307 条**指针', u'共 **304 条**指针')
                                          .replace(u'现行 **21 处**', u'现行 **22 处**'))
    cond2, _ = a_anchors_prose(anch_bad)
    ok(u'5i ★ 反证（内存副本）：把 `scope` 散文改回旧计数（304 / 22）⇒ 5h 必须报红',
       (not cond2) and anch_bad[u'scope'] != anch[u'scope'], u'变异后 cond=%s' % cond2)

    rmd = text(P_README)
    cond, det = a_readme_version_sync(rmd, cj[u'version'])
    ok(u'5j ★ 第 4 笔：`spec/README.md` §2 的 `checks.json` 行与文件内实际版本**同版**'
       u'（★ 原写 `V1.22`、而 §6 已有 `V1.23` 行 ⇒ 索引差一版）', cond, det)
    # ★ 2026-10-06 修：变异目标由「写死 V1.23」改为**从该行推导** ⇒ 对版本演进免疫。
    #   ★★ 注意必须**定位到 `checks.json` 那一行**再改 —— 直接在全文 `find` 会命中更靠前的
    #   其它 `✅ **V…` ⇒ 变异变成空操作 ⇒ 反证假红（本轮首版即踩）。
    _lines = rmd.split(u'\n')
    _idx = [i for i, ln in enumerate(_lines) if ln.startswith(u'| **`checks.json`** |')][0]
    _ln = _lines[_idx]
    _i = _ln.find(u'✅ **V')
    assert _i >= 0, u'该行找不到版本串'
    _j = _i + len(u'✅ **V')
    _k = _j
    while _k < len(_ln) and (_ln[_k].isdigit() or _ln[_k] == u'.'):
        _k += 1
    _cur = _ln[_j:_k]
    _maj, _min = _cur.split(u'.')[0], _cur.split(u'.')[1]
    _low = u'%s.%d' % (_maj, int(_min) - 1)
    _lines[_idx] = _ln[:_j] + _low + _ln[_k:]
    rmd_bad = u'\n'.join(_lines)
    cond2, _ = a_readme_version_sync(rmd_bad, cj[u'version'])
    ok(u'5k ★ 反证（内存副本）：把 §2 行版本改回 `V1.22` ⇒ 5j 必须报红',
       (not cond2) and rmd_bad != rmd, u'变异后 cond=%s' % cond2)

    print(u'\n== 6. 收尾：仓库真源逐字节核对（探针只在内存里变异）==')
    for p in BEFORE:
        ok(u'未改动 %s' % os.path.relpath(p, ROOT).replace(os.sep, u'/'), sha(p) == BEFORE[p])

    bad = [r for r in RESULT if not r[0]]
    print(u'\n===== 探针结果：%d/%d 通过 =====' % (len(RESULT) - len(bad), len(RESULT)))
    for _, name, detail in bad:
        print(u'  \u2717 %s  %s' % (name, detail))
    raise SystemExit(1 if bad else 0)


if __name__ == u'__main__':
    for _p in (P_BJ, P_CT, P_RFQ, P_PR, P_CHECKS, P_CSV, P_RES, P_D07, P_PRD, P_ANCHORS,
               P_DATASET, P_MODELS, P_HAPPR, P_HSUB):
        BEFORE[_p] = sha(_p)
    main()
