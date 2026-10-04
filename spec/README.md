# spec/ —— 机读规格（WorkBuddy → mimo code 的契约）

> **本目录是「机器可读的规格」，不是文档。** 它由 WorkBuddy（制度规范 + 产品设计方）维护，
> mimo code（开发方）**按此实现**。
>
> ★ **协作前提**：动手前先读仓库根 **`COLLAB.md`**（双 Agent 协商台账，唯一协商渠道）。

---

## 1. 为什么要有这个目录

现有 `docs/` 有 18 份人读文档、约 1.5 MB。实践已经证明：**人读文档会漂**——
mimo code 的审计在其中抓出**多处文档内部矛盾**（例如 `docs/11 §5.1` 说审批端点已闭合、
同文件 `§5.2` 说未实现；两者互斥），WorkBuddy 独立复核后确认 `§5.1` 对、`§5.2` 错。

⇒ **把"做什么"从散文改成机器可读的规格**，并**接入门禁**：规格漂了就红，不靠自觉。

---

## 2. 文件清单

| 文件 | 内容 | 状态 |
|---|---|---|
| **`chain.json`** | ★ **分档阈值 + 审批链**：9 条流程线（采一/采二/采三/销一/管一/管二/独家/紧急/变更）＋ 合同统一两级 ＋ 11 类单据链 ＋ 10 个角色 | ✅ **V1.1（2026-10-04 · `conventions.checks_when` 新增 `backfill(<section_id>)` 约定）** |
| **`RESOLUTIONS.md`** | ★ **口径冲突的裁定记录**（工具表 ↔ 制度 ↔ 代码 → 唯一口径） | ✅ **V1.5（30 条裁定）** |
| **`checks.json`** | ★★ **判据清单（唯一来源）** —— 由**两侧引擎共同执行**（`scripts/check_spec.py` 与 mimo 侧 Go 加载器）⇒ 消灭「第三份真相」（`N-011`） | ✅ **V1.15（11 原语 / 27 判据）** |
| `forms/*.json` | 11 张单据的表单字段 schema（字段名/类型/必填/枚举/校验） | ✅ **11 张全齐**（`BA`/`PR`/`SA`/`CT`/`SS`/`PC`/`QC`/`GR`/`SUB`/`BJ` ＋ **`RFQ`**）—— ★ **表单规格已出齐**；★ 2026-10-04 批 13（`N-052`）：`PR` **V1.3**（新增 `qualification_doc`；★ `A13` 段把 `safety_branch` 重判 `hard` ＋ `code`）· `SA` **V1.1**（新增 `allocation_note` ＋ `invoice_info` 修形）· `BA` **V1.1** · `SS` **V1.2**（三条 `when` 归一） |
| `ledger-mapping.json` | 单据 → 台账映射（含 **一对多**）＋ L01–L12 字段定义与写入者 | ✅ V1.0（★ `L11`/`L12` 的 `writable` 已修为 `false` ＋ 登记不变量） |
| **`enums.json`** | ★ **制度性枚举**（★ 后台**不可改**；改动须走裁定） | ✅ V1.1 |
| **`constants.json`** | ★ **运营性常量表**（后台可增删改；★ **只停用不删** ＋ 单据存值快照）：`unit` / `role_display_name` / `contract_template`；★ 内含配置归处（原三分法 → **四分法**） | ✅ **V1.1** |
| **`authority.json`** | ★★ **授权配置（第四类，新）**：**角色代理人**（`role_agent`）—— **后台可定义**、**点选通讯录镜像内已存在的用户**（禁手填 `open_id`）、每角色**至多 1 名**、★★ **不做替补**；含 5 条 `checks` ＋ 数据契约 ＋ 未启用守栏 | ✅ **V1.0（2026-09-30 新建）** |
| **`params.json`** | ★ **可配置参数**（★ 每个参数**强制声明 `consumer`**，否则即「假配置」—— `README` 定案 #24） | ✅ **V1.1**（★ `open_items` 已清零） |
| **`dashboard.json`** | ★ **4 张看板（13–16）指标定义与口径** ＋ ★★ **九条 `global_rules`**（未接通显示「数据未接入」· 数据源指向运营表 · **「应恒为 0」须带非空前置断言** · ★ **`pending→connected` 的推进判据** · ★ **第三种态「口径未定」** · ★ **每指标须声明 `render`（`r8`）** · ★★ **`source_ledgers` 须为派生量（`r9`）**）＋ 每张看板的 **`connected_requires`** / **`ops_table_required`** ＋ 每个指标的 **`render`**。★ 原名 `dashboard.yaml`，**改为 `.json` 以便纳入门禁** | ✅ **V1.3（2026-10-01）** |
| **`acceptance.csv`** | ★★ **判据级验收台账（`N-017` 落地）** —— `spec/forms/*.json#checks` 的**全部 97 条判据**逐条给出**可判定表达式**与**承载者**（列约定见 §3.1）；★ **只引用 `(doc_type, check_id)`、不复制 `assert` 原文**（防第二份真相） | ✅ **V1.2（2026-10-04 · 97 条；★ 批 13 再更新 **8 行**：`when_raw`/`when_kind` 归一（6 条）＋ `PR#safety_branch` 的 `machinable` `no → yes` ＋ 各条 `note` 据实重写；★★ 29 行随 `N-047` 批 12 更新（★ **分两落**）：23 条补 `severity`（首批 15 行〔`PR` 6 ＋ `SA` 9〕＋ ★ **补漏 8 行**〔`BA` 8〕—— ★ 首批**漏落**，详见 §3.1 末）、5 条 `soft` 承载由 `code` **订正**为 `pending_implementation`、1 条 `PR#amount_positive` 据实**更正**为 `hard`/`code`；★★ **自本批起有机械校验**：`scripts/check_spec.py#_ledger_vs_forms`（`[META]`）逐行比对本表与真源（`N-053`））** |
| **`openapi.json`** | ★★ **自建侧接口的机读契约（OpenAPI 3.0.3）** —— `REMAINING.md#B6` 落地、`N-051`：**60 个 path / 69 个 operation**，由 **`scripts/gen_openapi.py`** 从 `docs/05-API.md`（**人读正本 V2.20**）**机械生成**（★ **禁止手工编辑**）；★ **只引用不复制**（`security`/`parameters`/`x-error-codes`/`x-related-fr`/`x-doc-ref` 行号溯源），**不搬散文正文**（防第二份真相）；★ 判据 **`S21`–`S23`** ＋ 会报项 **`C9`** ＋ 常驻探针 `scripts/_probe_n051.py` 三重把关 | ✅ **V1.0（2026-10-04 · 69 条路由；★ 逐端点 schema **未做**，见 `x-known-gaps`）** |
| **`institution-anchors.json`** | ★★ **制度 ↔ 系统 双向锚点索引**（`COLLAB.md` `N-006` **第 2 重机制**）：机械抽取 `spec/**/*.json` 中**全部 324 处**「第X条」引用（涉 **28 条**条款）⇒ 逐条款给出**重排稳定**的指针（**158 条**）＋ 全量引用计数。★ 指针段语法与切分规则见 §3.2；★ **条款标题故意不设**（不臆造）；★ **接入门禁已启用**（判据 **`S20`** ＋ 第 11 原语 **`path_exists`**，两侧同批，`COLLAB.md#N-048`） | ✅ **V1.1（2026-10-04 · 28 条款 / 158 指针 / 324 处引用；★ 数据零改动、仅启用门禁 ＋ 钉死段归一顺序）** |

---

## 3. 命名与表达约定（`chain.json` 的 `conventions` 为准）

| 约定 | 规则 |
|---|---|
| **金额** | 一律用**分**（整数），字段名后缀 `_cents`。1000 元 = `100000`。**禁止浮点元** |
| **字段名** | `snake_case` 规范标识符；中文标签另置 `label`。★ **严禁把工具表表头的 `<br>` 带进字段名**（`RESOLUTIONS.md` R-18） |
| **术语** | 全案统一「项目总经理」= `project_general_manager`，**不设 `general_manager` 别名**（R-06） |
| **档位边界** | 闭区间。已核验：**999→采一 / 1000→采二 / 5000→采二 / 5001→采三** |
| **落账目标** | 只有 **`L01`–`L07` + `L09`** 可作落账目标；`L08`/`L10`/`L11`/`L12` **禁止**（`README` 定案 #20） |
| **裁定追溯** | 凡与工具表/制度原文不同的口径，必须带 `resolution: "R-xx"` 指向 `RESOLUTIONS.md` |

### 3.1 `acceptance.csv` 的列约定（`N-017` 落地 · 2026-10-04）

**用途**：把「一条判据**能不能被机器判定**、**由谁执行**」从散文变成**可数清单**。★ 本表**只引用** `(doc_type, check_id)`，**不复制** `assert` 原文 —— 判据原文、时点、`severity` 的**唯一来源仍是 `forms/*.json#checks`**。

| 列 | 取值 | 说明 |
|---|---|---|
| `doc_type` / `check_id` | — | 指向 `spec/forms/<doc_type>.json#checks[id=check_id]` |
| `when_raw` | 原文 | `forms` 里 `when` 的**原样**取值（追溯用） |
| `when_kind` | 受控 | 按 `chain.json#conventions.checks_when` 归一：`submit` / `approval` / `lifecycle:签署` / `lifecycle:算链` / `lifecycle:终态` / `lifecycle:提交后` / `lifecycle:落账后` / `backfill`（★ **批 13 新增**：对应 `backfill(<section_id>)`，`<section_id>` 取自 `forms/<DocType>.json#sections[*].id`）/ `schema`（非时点）/ `其他`（★ **不合约定**） |
| `severity` | `hard` / `soft` / `(未声明)` | 与 `forms` 一致 |
| `decision_kind` | 受控 | 可判定条件的**形态**（词表见下） |
| `decidable_expr` | — | 可机判表达式；`—` 表示**当前无法机判** |
| `machinable` | `yes` / `no` | `no` ⇒ 该条＝**人工检查项**（须在表单内以提示文案引导） |
| `carrier_kind` | 受控 | `code`（有运行时执行体）/ `structural`（结构性保证）/ `manual`（人工承载）/ `pending_wiring`（通路未接）/ `pending_implementation`（**无任何承载 ＝ 真缺口**）—— ★ 与 `forms` 的 `carried_by_kind` **同一受控词表**；★ **映射口径**：`forms` 侧的 `submit`（＝引擎自动执行）在本表记 `code`（它有真正的运行时执行体） |
| `carrier_ref` | 自由 | 落点（文件#函数 / 判据 id / 可写列），供人工核对 |
| `note` | 自由 | 该条的**实测定性**（含「部分承载」「半边不可判」这类必须说清的边界） |

★ **`decision_kind` 受控词（10 项）** —— ★★ **不是 `N-017` 原拟的 5 项**：原拟「字段存在性 / 正则 / 枚举 / 引用字段比较 / 人工」，逐条落表后**不够用**（占比最高的是**数值比较**与**跨字段引用比较**，两者都塞不进原 5 项）⇒ 如实扩为：
`存在性` / `正则` / `枚举` / `数值比较` / `引用比较` / `集合计数` / `双向条件` / `顺序` / `结构性` / `人工`。
★ **扩项本身是一条结论**：「验收条件的形式」此前**只有一份口头清单**，第一次逐条落表才发现它不完整。

★ **本轮落表时实测发现（已登记 `COLLAB.md#N-047`，逐条可复现）**：
1. ★★ **23 条判据落在两条判据的缝里** —— `BA` 8 条 / `PR` 6 条 / `SA` 9 条**缺 `severity`** ⇒ ① `internal/httpapi/handlers_approval_hardchecks.go#evaluateHardChecks` 首行 `if c.Severity != "hard" { continue }` ⇒ **提交引擎逐条跳过**；② `S15`（每条 `hard` 须声明 `carried_by_kind`）**只管 `severity=hard`** ⇒ **也不要求它们声明承载者**。⇒ 结果是**既不执行、也不报错**（「写了没人执行」同族，但连 `S15` 都看不见）。
2. **5 条无任何承载**（`pending_implementation`）：`BA#amount_tier1_only` · `PR#safety_branch` · `PR#device_tech_attachment` · `SA#counterparty_conditional` · `SA#cross_month_allocation`。
3. **3 条时点未接线**（`pending_wiring`）：`BA#receipt_per_purchase`（回交凭据）· `SA#actual_not_exceed` 与 `SA#invoice_must_link`（结算补录）。
4. **6 条 `when` 不合 `checks_when` 约定** —— ① **4 条落在约定的任何一类之外**：`BA#receipt_per_purchase`（`回交凭据`）· `BA#anti_split_before_disburse`（`业务规则`）· `SA#actual_not_exceed` 与 `SA#invoice_must_link`（`结算补录`）；② **2 条未用规范写法** `approval(<node_id>)`：`SS#tech_opinion_required_at_node2`（`node2_tech_opinion`）· `SS#pgm_final_required`（`node4_pgm_final`）。★ ✅ **已于 `N-052` 批 13（2026-10-04）全部归一** —— 4 条改入约定：`approval(return_receipt)`（`purchase_tier1.nodes[5]`）· `approval(anti_split_check)`（`purchase_tier1.nodes[2]`）· `backfill(settlement_backfill)` ×2（★ `backfill(<section_id>)` 为**本批新增的约定写法**，见 `chain.json#conventions.checks_when`）；2 条改规范写法：`approval(tech_opinion)` · `approval(pgm_final)`。⇒ **本行 0 命中**。
5. **2 处判据无字段可承载** ＋ **1 处条件必填被静默跳过** —— `PR` 缺「资质文件 / 说明」字段（`safety_branch`）· `SA` 缺「分摊说明」字段（`cross_month_allocation`）· ★ `SA#invoice_info.required_conditional = 「结算时必填」`**不含 `==`** ⇒ `evalSimpleEqual` 判定为**不可解析** ⇒ 通用校验**静默跳过**（★ 与 `N-017` 的「降级为提示文案」**不是一回事**：这条**既没提示、也没拦**）。★ ✅ **部分于 `N-052` 批 13 闭环**：① 两字段**均已补入**（`PR#qualification_doc` 带 `required_conditional` ＝ `is_safety_or_special_equipment == true`；`SA#allocation_note` **刻意不带** `required_conditional` —— 「跨月」`evalSimpleEqual` 表达不了，条件由判据唯一承载）；② `SA#invoice_info` 的不可解析条件**已修形**（改 `required: true`，「结算时」由段 `filled_at` 承载）；★ 但 `SA#cross_month_allocation` 的**触发条件仍不可判**（`occurrence_period` 的机读载荷形态未定）⇒ 该条**不翻转** `hard`。

★★ **2026-10-04 批 12 的处置（`N-047` 首批，已落地）**：上列 5 组问题按**同批**口径收口 ——
★ ① **23 条全部补齐 `severity` ＋ `carried_by_kind`**（`BA` 8 / `PR` 6 / `SA` 9）⇒ **97/97 条判据带声明**；★ ② 5 条**无承载**逐条定档：3 条改判 `pending_implementation`（`PR#safety_branch` · `SA#counterparty_conditional` · `SA#cross_month_allocation`，理由＝缺字段或触发条件不可表达；★★ **其中 `PR#safety_branch` 后于批 13 `A13` 段改判 `hard` ＋ `code`，见 §3.2；另两条仍延后，具名移入 `COLLAB.md#N-054`**）、2 条落 `code`（`BA#amount_tier1_only` · `PR#device_tech_attachment` 由 mimo 批 12 的求值器承载）；★ ③ 3 条 `pending_wiring` 如实保留（`BA#receipt_per_purchase` · `SA#actual_not_exceed` · `SA#invoice_must_link`）；★ ④ **`when` 归一与字段补齐已于批 13 落地**（见下）；★ ⑤ `SA#invoice_info.required_conditional` 的不可解析形态**已于批 13 修形**。
★★ **一处据实更正**：`PR#amount_positive` 曾被改判「提交时点金额未就绪、不可执行」—— ★ **实测证伪**（`resolvePRAmountForTier` 在判据求值**之前**写入 `estimated_total_cents`）⇒ 本批落 `hard` ＋ `code`。
★★ **一条负结果（如实登记，见 `N-052`）**：把 `SA#entertain_required` 由 `soft` 临时改 `hard` 跑 `go test ./internal/httpapi` ⇒ **全绿**（无 `未实现求值器` 报错）⇒ ★ **没有任何用例用真 spec 走 SA 提交路径** ⇒ SA/PR 侧这批 `severity` 的**提交路径激活未被端到端钉住**（对照：摘掉 `amount_positive` 注册项 ⇒ **14 个 BA 提交用例精确转红** ⇒ BA 侧已被端到端覆盖）。
★★ **该负结果已于批 13 的 `A13` 段闭环，并据实收窄了范围（★ 我方的表述有一处不准，更正）**：我方这次**分单据各做一次证伪对照**（`cp` ＋ `sha256sum -c` 还原）⇒ ① `PR#safety_branch` `soft→hard` ⇒ **精确转红 2 例**（`TestPRSubmitFrontendShapeContract` / `TestPRSubmitAmountMismatchWarnsNotRejects`，文案＝`hard 判据 "safety_branch" 未实现求值器`）⇒ ★ **PR 侧本就是被覆盖的**；② `SA#entertain_required` `soft→hard` ⇒ **全绿** ⇒ ★ **缺口只在 SA**。⇒ 本行原写「SA/PR」**据实收窄为「SA」**。

★★ **2026-10-04 批 13 的处置（`N-052` 首批，已落地 · 本表随之更新 8 行）**—— ★ 本批只做**我方规格域**，**不派工、不翻转任何判据**：
★ ① **`when` 归一 6/6 完成**（见上「发现项 4」）；★ 并**新增一条约定写法** `backfill(<section_id>)`（`chain.json#conventions.checks_when`）—— ★ 理由：「结算补录」这类**单据后置补录段时点**此前**在约定里无处可放**，而它**在 spec 里有现成标识**（`sections[*].id`，且该段自带 `filled_at`）⇒ ★★ **归一成「退回既有类目」会让信息丢失**（`提交后` 太粗、不指明承载者），**新增精确写法**才使「谁承载」可被指明。
★ ② **`PR` 补 `qualification_doc`（「资质文件 / 说明」）**，`type=text_and_attachment`、`required_conditional=is_safety_or_special_equipment == true` —— ★★ **裁定＝新增独立字段、不复用 `tech_attachment`**（后者已被 `PR#device_tech_attachment` 的条件 `P04` 占用；同一字段被两条判据争用会让「资质」与「技术参数」两类材料语义纠缠），依工具表 R13 的规则原文补齐。
★ ③ **`SA` 补 `allocation_note`（「分摊说明」）** —— ★ **刻意不给它 `required_conditional`**：「跨月」是**日期区间比较**、`evalSimpleEqual` 表达不了；★ **写一个不可解析的条件＝重造本批要消灭的缺陷** ⇒ 条件由判据 `cross_month_allocation` **唯一承载**（并在字段上以 `conditional_carrier` 写明这一分工）。
★ ④ **`SA#invoice_info.required_conditional` 修形**：`结算时必填`（不含 `==`、被静默跳过）⇒ **`required: true`** —— ★ 依据＝「结算时」是**段级时点**、已由 `sections[id=settlement_backfill].filled_at` 承载（**提交期该段整体跳过 ⇒ 提交行为零变化**）。
★★ ⑤ **本批实测逼出一条新缺口（如实登记、未解决）**：`SA#cross_month_allocation` 的**触发条件不可判** —— 「跨月」取决于 `occurrence_period`（工具表控件类型＝**日期区间**）的**机读载荷形态**，而 ★ **全仓 `date_range` 零消费端**、自建表单页把它渲染成**纯文本框**（`web/src/views/Submit.vue` 无 `date_range` 分支 ⇒ 落 `v-else`）⇒ ★ **不臆造载荷形态**（属新增契约、飞书侧控件载荷未实采）⇒ 该判据维持 `soft` ＋ `pending_implementation`、**不翻 `hard`**（登记 `forms/SA.json#known_gaps`）。
★ ⑥ **`SA#counterparty_conditional` 具名延后**（不静默消失）：「对外支付」**可表达**（`payment_method_input == '对公直付'`），但「需要开票」**无任何字段承载**（工具表 R28 只给了条件、未给字段）⇒ **不凭空补字段**（那会把「条件缺失」变成「我方口径」）。
★ ⑦ **门禁**：本批**零新增判据**（`checks.json` 仍 **V1.15 / 11 原语 / 27 判据**）、**零代码改动** ⇒ 必绿基线仍 **8 条**；★ 受本批影响的 8 行已随 `_ledger_vs_forms()` 机械校验（`N-053`）逐行对齐。
★★ **待接线（下一批，属 mimo 域）**：`PR#safety_branch` 的**提交时点求值器** ＋ ★★ **SA/PR 提交端到端用例**（批 12 的负结果：现**无任何用例用真 spec 走 SA 提交** ⇒ 激活未被钉住）＋ 新字段的前端可用性核对；★ 求值器落地后由我方**重判** `PR#safety_branch` 为 `hard` ＋ `code`（★ 次序不可颠倒：`severity=hard` 而求值器未注册 ⇒ 当场 fail-closed）。
★ ✅ **以上「待接线」已于同日 `A13` 段全部落地（2026-10-04，`92a403e` ＋ 我方收尾）**，逐条见下。

### 3.2 批 13 的 `A13` 段（`N-052` 第二批）—— 求值器 ＋ ★ SA 提交端到端 ＋ 注册表覆盖钉子

★ ① **`PR#safety_branch` 求值器已注册**（`internal/httpapi/handlers_approval_hardchecks.go#checkPRSafetyBranch`）：`is_safety_or_special_equipment` 为布尔 `true` ⇒ `qualification_doc` 非空；**条件在求值器内判**（`when` 只是 `submit`）、**未命中放行（不反向）**，与 `BA#safety_certificate` / `PR#device_tech_attachment` 同范式；★ 取证：`safety_branch` 在 `forms/*.json` **全仓仅 `PR` 一处** ⇒ 单键注册、无需 `DocType` 分流。
★ ② ★★ **SA 提交通路首次获得 handler 级端到端覆盖**（`internal/httpapi/hardchecks_n052_test.go`，**走真 spec**）：`S1` 合规 SA ⇒ `200` ＋ `biz_no` 前缀 `SA-` ＋ 链任务已建；`S2` 金额**负数** ⇒ `400` 点名「金额必须大于 0」（★ **由本判据的 hard 求值器承载 ⇒ 这一例即「SA 提交路径真的执行了真 spec 的 hard 判据」的活证据**）；`S3` `usage_category_l2` 空 ⇒ `400` 点名 L2；`S4` `PR#qualification_doc` 条件必填三态（**走 `validateSubmitForm`**，与 S1–S3 两条路径**分开断言**）。
★ ③ ★★ **注册表覆盖钉子（本批新机制）**：`TestSubmitHardChecksCoverRealSpec` 遍历**真 spec 全部** `forms[*].checks`，对「`severity=hard` ∧ 提交时点 `when` ∧ ∉{`idempotency_key`,`contract_no_format`}」逐条断言 `submitHardChecks` 已注册；★ **鉴别力自证**：把 `SA#entertain_required` 临时翻 `hard` ⇒ 该测试**精确转红**并点名。⇒ ★★ 把「缝」从**运行期**（某单据没有提交用例就永远不触发 fail-closed）挪到**测试期**，且**与单据类型无关**。
★ ④ **`PR#safety_branch` 已重判为 `hard` ＋ `code`**（本文件与 `acceptance.csv` **同批**回填；`forms/PR.json` → **V1.3**）。
★★ ⑤ **三处「与批次预期不同」由 mimo 如实上报，我方独立复核后确认全部成立（★ 这是本批最值钱的取证）**：
- **(a) 结构化校验先于 hard 判据** —— `handlers_approval.go` 的 `validateSubmitForm`（L634）**早于** `evaluateHardChecks`（L648）⇒ 对**同一字段**（`amount_cents` / `usage_category_l2`），`=0`／缺失／空串形态**被结构化 required 先拦**，其 hard 版文案在真实路径上**恒不可达**（等价冗余双保险，非缺陷）。★ 故「hard 真执行」的证据改用**负数**（结构化放行、hard 精确拦）。
- **(b) 我方交办包的两条变异预期结构性不成立** —— 本批 `safety_branch` 仍为 `soft`，而 `S4` 走结构化路径、覆盖钉子**只查 `hard`** ⇒ 摘注册项**不会**让它们转红。mimo **自行补了合成 hard 用例** `TestN052SafetyBranchRegisteredInEvaluate` 承担鉴别力并**主动上报** ⇒ ★ **不返工**（★★ 可复用教训：**变异的作用面必须与被断言面同层** —— 「soft 的求值器」与「hard 的注册表」不同层，跨层预期必然落空）。
- **(c) `PR` 侧本就已被端到端覆盖**（见本节前文收窄说明）。
★ ⑥ **我方独立验收（★ 不采信自报，全部我方自做）**：门禁独立复跑 **8/8 ＋ 会报零命中**；读实现（求值器条件型四例 ＋ `isSafetyTrue` 布尔/字符串双形态）；★★ **三条单点变异我方自做**（**M-A** 去条件判断 ⇒ 恰红 3 断言＝「未命中 ⇒ 放」族；**M-B** 恒放行 ⇒ 恰红 3 断言＝「命中 ⇒ 拒」族；**M-C** 摘注册项 ⇒ 合成 hard 用例恰红 2 断言、覆盖钉子保持绿＝**(b) 的实证**）⇒ **隔离性成立**；`cp` ＋ `sha256sum -c` **3/3 还原 OK**（★ 未用 `git checkout --`）。
★ ⑦ **仍延后（不静默消失 ⇒ 移入 `COLLAB.md#N-054`）**：`SA#cross_month_allocation`（触发条件依赖 `occurrence_period` 的**机读载荷形态**，未定）· `SA#counterparty_conditional`（「需要开票」**无字段**）。

★★ **本表不翻转任何 `connected` / 判据状态** —— 它只**记录现状**。改 `severity`、补字段、补求值器都属**同批变更**（我方补声明 → 实现侧同批接线，否则 `evaluateHardChecks` 对未注册 id **fail-closed** 当场拒启）⇒ 故**登记不抢跑**。

★★ **同日补漏（诚实登记 · 本批唯一一次「落盘后自查才发现」）**：`acceptance.csv` 首批只落了 **15 行**（`PR` 6 ＋ `SA` 9），★ **`BA` 8 行漏落** —— 即 `severity` 列仍写 `(未声明)`、`BA#amount_tier1_only` 的 `carrier_kind` 仍写 `pending_implementation`，而 `spec/forms/BA.json` 同期**已落** `hard` ＋ `code`。★★ **此漂移通过了全部 8 道必绿门禁** —— 因为 ★★ **`acceptance.csv` 当前不被任何脚本读取**（全仓仅 3 处**注释**提及：`handlers_approval_formcheck.go:9` · `handlers_approval_hardchecks.go:12,94`）⇒ **台账与真源（`forms/*.json`）之间没有任何机械校验，只靠人眼比对**。⇒ ① **数据侧已修**：按 `forms/*.json` 逐行补齐 ⇒ **97/97 行** 两列全等（`carrier_kind` 按本节的 `submit → code` 映射比对）；② **门禁侧已补**：`scripts/check_spec.py` 内置 `[META]` 自审 `_ledger_vs_forms()`（★ Python-only、**不引入新原语、不触碰 Go 侧加载器** ⇒ 无「两侧同批」问题）；★ **探针自证**：拿**修复前**的台账跑 ⇒ **逐条报出 9 处**（8 行 `severity` ＋ 1 处 `carrier_kind`），复位后 0 处；③ 「是否升级为 `checks.json` **正式判据**」**需新原语 ⇒ 两侧同批** ⇒ 登记 **`N-053`** 待定。

★ **未覆盖（如实登记）**：`README §2` 原描述里的「对齐 mimo 审计 **6❌ / 21⚠️**」＝ **FR 级**验收矩阵，与本表**不同层**；本表只覆盖**判据级**（97 条）。FR 级矩阵待产。

---

### 3.2 `institution-anchors.json` 的指针语法（`N-048` 取证立据）

**用途**：让「制度第 X 条 ↔ 机读规格的哪个落点」**双向可查且可机检**（★ `N-006` 明写「锚点指向的文件/字段不存在 ⇒ 红」）。
**指针形态**：`<spec 相对路径>#<点路径>`；★ **省略 `#`** ⇒ 只校验文件存在。

| 段 | 语义 | 为什么必须这样 |
|---|---|---|
| 裸键 | 普通对象键（**不得含 `.`**） | — |
| `[字面键]` | ★ 键名**含 `.`** 时必须用此形式 | ★ 实证：`spec/params.json` 有 **5 个含点扁平键**（`params.[reporting.monthly_cutoff_day]` 等）⇒ 裸写与嵌套路径**天然歧义** |
| `[k=v]` | 选择器：子节点中键 `k` 的标量值 == `v` 者（≥1 命中） | ★ 原始取证拿到的是**数组索引**（`checks[14]`）⇒ **重排即静默错位**、存在性检查抓不住 ⇒ 换选择器使指针**重排稳定** |
| `*` / `[*]` | 全部子节点 | 与既有 `toks`/`sel` 同口径 |
| `**` | 递归下降 | 同上 |

★★ **切分规则（关键）**：点路径**按 `.` 切段，但方括号 `[...]` 内部不切分** —— 两条独立实证：① **字面键可含点**（`params.[reporting.monthly_cutoff_day]`）；② **选择器值可含点**（`spec/checks.json#change_log[version=1.5]`）。★ 不规定此条 ⇒ 两种写法都被切错 ⇒ **命中 0 而红**（★ **安全失败**：不会静默取到错节点）。
★★ **段归一的顺序（关键，`N-048` 验收期实测逼出的两侧分歧）**：尾缀形 `name[k]`（如 `checks[id=x]`）必须**先按最后一个 `[` 拆开**成「裸键 `name` ＋ 段 `[k]`」，**之后**才把独立段 `[*]` 归一为 `*`。★ **反序**（先换 `[*]`→`*`、再拆尾缀）会把 `checks[*]` 折成**裸键 `checks*`** ⇒ **命中 0**。★★ **教训**：真锚点（158 条）**零 `[*]` 形态** ⇒ 该分歧**只潜伏在未出现的形态上**，「两侧各跑一遍都对」**抓不住** ⇒ 两侧各留**回归钉**（Go `TestPathExistsStarBracketSegment` · Python `scripts/_probe_n048.py`「正向·`[*]` 段」，★ 后者在修正前**必红**）。
★ **标量比较口径**与既有 `_scalar_str` / `scalarString` 一致（布尔 → `"true"`/`"false"`；整数 → 十进制无小数点；非标量不参与匹配）。
★★ **生成器必须排除该文件自身**：索引文本里含「第X条」字样 ⇒ 不排除则每次重生都把自己的描述当成 spec 引用（**实测已复现**：324 → 353 自增污染）。

---

## 4. 门禁

```bash
python scripts/check_spec.py      # 只查 spec/
bash   scripts/check_all.sh       # 全量（含本项）
```

**校验项**：★★ **判据的唯一来源是 `spec/checks.json`** —— 本表**只是索引，不是正本**（★ 免得两处各列一份、必然漂移；本表此前就落后过，见 §6 `V1.1`／`V1.3`）。当前 **27 条判据 / 11 个原语引擎**：

| # | 判据（要点） |
|---|---|
| `S1` | `spec/**/*.json` 全部可被 `json.load` 解析（★ 抓语法错，含**把中文引号误写成 ASCII 双引号** —— 本条已**当场抓出过两次**，最近一次是 `authority.json`） |
| `S2` | `chain.json` 顶层必含六键（`version`/`roles`/`thresholds`/`contract_approval`/`routes`/`doc_chains`） |
| `S3` | `routes` **恰好**覆盖 9 条流程线（不多不少） |
| `S4` | `doc_chains` 必须含 11 类单据（`BA/PR/SA/RFQ/BJ/SS/CT/PC/GR/QC/SUB`；允许另有说明键） |
| `S5d` | ★★ **每张 `forms/*.json` 必须显式声明关键顶层键（含 `ledger`）** —— ★ 2026-09-30 新增，**承接 `S5b` 原先的「命中数守栏」并升级**：把「**没声明 `ledger`**」（＝漏写，该报错）与「**声明 `ledger` 为空数组**」（＝不落账，是事实）区分开（同族：**「没有数据」与「没有字段」不可区分**） |
| `S5a` | `chain.json` 内全部 `ledger` 取值 ⊆ `L01`–`L12` |
| `S5b` | `forms/*.json` 内全部 `ledger` 取值 ⊆ `L01`–`L12` |
| `S5c` | ★ `chain.json` 侧的 `ledger`（`doc_chains.*` 与 `routes.*.nodes[*]`）**也不得指向** `L08`/`L10`/`L11`/`L12`（`N-020` 补：原 `S5a` 只做「⊆」、`S11` 只扫 `ledger-mapping` ⇒ **两侧都放行**，而本节原文要求「不得指向」） |
| `S6` | `route` 的取值必须存在于 `routes`（合同统一两级另列，允许） |
| `S6b` | ★ `doc_chains.*.route_by_tier.*` 的取值必须存在于 `routes`（`N-020` 补：原 `S6` 只管 `**.route`，此项**悬空会静默通过**） |
| `S6c` | ★ `doc_chains.*.route_by_condition` 的**管道串须逐段**存在于 `routes`（`N-021` 补；依赖 `ref_exists.split`） |
| `S7` | 采档阈值**无缝且无重叠**（首项 `lower=null`、末项 `upper=null`） |
| `S8` | `forms/*.json` 字段名不得含 `<br>` 或空白（`R-18` 命名规范） |
| `S9` | `ledger-mapping.json` **恰好**覆盖 `L01`–`L12` 全部 12 张 |
| `S10` | `doc_to_ledger` 的 `ledger` 取值必须**真实存在于** `ledgers` |
| `S11` | `doc_to_ledger` **不得把** `L08`/`L10`/`L11`/`L12` 当落账目标（`README` 定案 #20） |
| `S12` | ★★ `forms/*.json` 的 `ledger` 必须与 `ledger-mapping.doc_to_ledger` **完全一致** —— 这是 `R-02`（`L03` 恒空**静默缺陷**）的执行守卫 |
| `S13` | ★★ **制度第三十五条「合同必备条款」8 组完整性** —— `forms/CT.json` 的 `clause_group` 必须覆盖 **1..8**；缺一组＝**少一道硬拦截**（与「`L03` 恒空」同族：**少了东西与没违规长得一样**） |
| `S14` | ★★ **`checks[*].carried_by_kind` 的取值必须来自受控词表**（`N-036`）：`code` / `structural` / `manual` / `pending_wiring` / `pending_implementation` —— ★ **与 `acceptance.csv#carrier_kind` 是同一张表**；★★ **2026-10-04 批 12 起 `min_hits` 由 `0` 回到 `1`**（`N-047`）：扩围后**每张表单的每条判据都带 `carried_by_kind`** ⇒ 原先「有 8 张表单本就没有这个字段」的依据已消失，`min_hits: 0` 的代价（collect 写错不被发现）不再需要承担。★ 鉴别力实测：把 `collect` 改成不存在路径 ⇒ **Python 报「仅命中 0 处（要求 ≥1）」**（还原复绿） |
| `S15` | ★★★ **每一条判据（★ 不限 `severity`）都必须声明 `carried_by_kind`**（`N-036` 立、`N-047` 批 12 **扩围**；`checks.json` **V1.14 → V1.15**）—— ★★ **旧形态只覆盖 `severity=hard`**，而**缺 `severity` 的判据既不被它要求、也不被 `evaluateHardChecks` 执行**（该函数首行只对 `severity == hard` 放行、其余一律 `continue`）⇒ 落在两条机制之间的**缝**里（`BA` 8 / `PR` 6 / `SA` 9 共 **23 条**）；★★ **更糟的是 `when_key=severity` 对没有该键的项直接 `continue` ⇒ 恰好保护了不该保护的那批**。★ 故 `args` **删 `when_key`/`when_in`**（无条件面 —— 两侧 `array_each_required` 均已支持「缺省即无条件」⇒ **零引擎改动、两侧自动一致**）。★ 同批 **97/97 条判据全部带 `carried_by_kind`**（含 5 条 `soft`，其值 `pending_implementation` —— ★ 实测它们**无任何执行通道**）。★ 鉴别力实测：删掉 `GR#inspection_vs_conclusion_hint` 的 `carried_by_kind` ⇒ **Python 报 1 处、Go 同样报红**（还原复绿） |
| `S16` | ★★ **每个 `source ∈ {system, computed}` 的字段都必须声明「谁保证它不可篡改 / 谁生产它」**（`N-038` 立、`N-039` 扩围 —— ★ 原带 `immutable: true` 条件时只覆盖 **54/96** ⇒ **收掉该条件**） |
| `S18` | ★★ **每条原语声明必须是扁平对象 `{desc, args}`**（`N-038`）—— ★ 治「**声明形状不一 ⇒ 引擎解析不到却零报错**」；★ 初版要求 `desc`＋`args` 两者（与 `s14_probe_test.go` 的刻意空 `collect` 冲突）⇒ **收窄为只要求 `desc`**（判据**宁准勿宽**） |
| `S19` | ★★★ **节点 `actor` 必须落在合法白名单内 —— 「禁惰性必需节点」的完整版**（`N-044` 收尾立项 ⇒ **`N-045` 实现落地后升级**，`checks.json` **V1.11 → V1.12**）—— `pattern_absent`（只禁复合串 `→`）⇒ **`ref_exists`**：`routes.*.nodes[*].actor` **⊆ `chain.json#roles` 的键 ∪ `extra_allowed:["system"]`**（★ **数据驱动**：`roles` 是角色 key 的唯一真源，不在判据里写死字面量 ⇒ 不会有「spec 一份、判据一份」的漂移）。★ 窄版对**描述性串**（`N-045` 的 `"按档位审批人"`）**静默** ⇒ 完整版才捕获。★ **残余盲区（如实登记）**：`roles` 表内 `group_finance`/`group_approval`/`sys_admin` **不是审批 actor** ⇒ 用作 `actor` 时不报且仍惰性；收口＝让 Go 侧 `approverRoles` 由 spec 派生（下一批小项，同 `N-031` 族） |
| `S20` | ★★★ **制度锚点必须真实可达 —— `N-006` 第 2 重机制收官**（`N-048`，`checks.json` **V1.12 → V1.13**）—— 第 11 原语 **`path_exists`**：`clauses[*].spec[*].at` 的**每一条指针必须可解析**（目标文件存在 ∧ 可 JSON 解析 ∧ 点路径命中 ≥1 节点），★ 报错**三类分类独立**（文件不存在 ≠ 不可解析 ≠ 命中 0）。★ **它治什么**：**规格改字段名／挪位置 ⇒ 索引静默变假线索** —— 读者按指针去查，**查到的不是条款、是空气**（★ 比「没有索引」更坏：**它替错误背书**）。★ **为什么必须新原语**：`ref_exists` 只比**单个目标 dict 的键集合**，而锚点落点**分散在任意文件、任意深度** ⇒ 既有 10 个原语**全都表达不了**；★ 且 Go 侧未知原语 fail-closed ＋ Python 侧 META 比对「声明 vs 实现」⇒ **单侧落即净检出红**。★★ **验收期实测逼出的两侧分歧**：段归一**顺序** —— 必须**先拆 `name[k]` 尾缀、再换 `[*]`→`*`**；反序会把 `checks[*]` 折成裸键 `checks*`（命中 0）⇒ ★ 真锚点零 `[*]` 形态 ⇒ 分歧**只潜伏在未出现的形态上** ⇒ 两侧各留**回归钉**（Go `TestPathExistsStarBracketSegment` · Python `scripts/_probe_n048.py`）。★ **残余盲区（如实登记）**：① 只保证「已声明为指针的落点存在」，**不**保证「制度条款已完整索引」（22 处无稳定键 ⇒ 只计数不建指针）；② 只做**存在性**，**不**校验被指内容与条款**语义仍相符**；③ **生成器仍在仓库外** ⇒ 仍有「改了 spec、索引未重生」的窗口 |
| `S21` | ★★ **机读契约不得被掏空**（`N-051`）：`spec/openapi.json` 顶层**必含七键** `openapi`/`info`/`servers`/`security`/`paths`/`components`/`x-source`（`required_keys`；**零引擎改动**）—— ★ 治「有人把契约改成空壳/半成品 ⇒ 下游以为接口不存在」。★ `x-source` 也在必含之列：机读契约**必须自带「我从哪份正本、哪个版本、哪个指纹生成」**（这正是 `C9` 指纹分支的可检查点） |
| `S22` | ★★ **每个 operation 必须自描述**（`N-051`）：`paths.*.*` 必含五键 `operationId`/`summary`/`tags`/`security`/`responses`（`required_keys`）—— ★ 治「**逐端点信息缺项**却仍被解析通过」（同 `S5d` 之理：**漏写与「本来就是空」不可区分** ⇒ 一律要求显式声明）。★ 边界：只查**键在不在**，**不**查取值对不对（取值面由 `C9` ＋ 探针把关） |
| `S23` | ★★ **每个 operation 必须有兜底响应分支**（`N-051`）：`paths.*.*.responses` 必含 `default`（`required_keys`）—— ★ 对应正本 §2「**错误包裹总则**」：任何端点都可能返回**未逐条列举**的错误码 ⇒ 契约须显式给出兜底分支，**不得**只列成功码（★ 否则消费方按 schema 生成会**把错误分支判成非法响应**） |

★ **`S21`–`S23` 的诚实划界（重要）**：这三条**只**保证 `spec/openapi.json` 的**结构完整**；★ 「**路由集与 `docs/05-API.md` 是否一致**」**不在本清单内** —— 因为判据原语只读 **JSON**，比对「正本 Markdown 声明的路由」需要**读 Markdown** 的新原语（受 `checks.json#_format_note`「本仓库 spec 一律 JSON」约束）⇒ 该一致性**由两处独立把关**：门禁「会报」项 **`C9`**（`scripts/audit_silent.py`，**复用生成器**避免第三份真相）＋ 常驻探针 **`scripts/_probe_n051.py`**（重现一致性 ＋ 路径参数完备性 ＋ `operationId` 唯一性，**10/10**）。★ 「**升级为门禁必绿项**」需先落新原语 ⇒ 属**两侧同批**工作，已登记 `COLLAB.md#N-051`（**不抢跑**）。

★ **`S17` 已撤销**（`N-039`：条件面为空守卫改由 `array_each_required` 的 `matched` 参数表达），全 spec 不得再引用该编号。

★★ **`S20` 已落地**（2026-10-04 · `N-048` 收尾）：原先在此的「**拟新增 `S20`（`path_exists`）尚未落地**」提示**已销**（★ 本节一度**刻意不预登记未生效判据**，免得索引又变成「第二份真相」—— ★ **该纪律继续有效**，只是现在 `S20` 已真实生效，故正式入表，见上）。
★ 生效时点与举证：数据 = `institution-anchors.json` **V1.1**；引擎 = Python `scripts/check_spec.py#prim_path_exists` ＋ Go `internal/specload/checklist.go#case "path_exists"`（**两侧同批**）；★ **本判据的鉴别力已用单点变异实测**（改写一条指针的选择器值／文件名／含点键写法 ⇒ `[S20]` **各精确报红**，还原复绿）；★ 常驻回归探针 `scripts/_probe_n048.py`（**10/10**）。
★ `S20` 的 `args`：`{"file":"spec/institution-anchors.json","collect":"clauses[*].spec[*].at","min_hits":1}`（★ `min_hits` **刻意取 1 而非 158**：1 已足够抓「collect 路径写错／索引被清空」的空转假绿；★ 取 158 会多一个**每加一条条款就要同步改**的硬编码数字 —— ★ 宁可让「索引骤然变空」报错，**不让「新增条款」报错**）。

★★ **两侧共同契约**：同一条判据由 `scripts/check_spec.py`（Python）**与** mimo 侧 Go 加载器**各实现一遍**，且必须**逐字对齐** —— 含**语言怪癖**（例：Python 里 `bool` 是 `int` 的**子类**，`_scalar_str` 的 `bool` 分支必须排在 `int` 之前，否则 `True` 会变 `"True"`）。★ 已由**两侧引擎一致性探针**验证：同一篡改，两侧**都拦下**（`N-021`）。

★ **本门禁的自证记录**（`README` 定案 #54：有效性只能用探针证明）：
- **首跑即抓到 WorkBuddy 自己的错**：`chain.json` 初稿有 **3 处把中文引号写成 ASCII 双引号**，直接打断 JSON ⇒ 被 S1 当场抓出
- **探针对照**：故意造 4 类违规（删一条流程线 / `ledger` 指向 `L10` / `route` 指向不存在流程线 / 档位重叠）⇒ **逐条报出**，非假绿

---

## 5. 与其他文件的关系

```
COLLAB.md                  ← 双方唯一协商渠道（谁必须先给方案、议题怎么走）
spec/*.json  (本目录)       ← ★ 机器可读契约：做什么（WorkBuddy 维护）
spec/RESOLUTIONS.md        ← 口径裁定的唯一追溯点
docs/                      ← 人读产品规格：为什么这样设计
deliverables/…/采购及费用审批管理办法V4.0.docx   ← 公司级制度（Word，不进代码库）
```

★ **`spec/` 归 WorkBuddy，`.mimocode/` 归 mimo code** —— 双方**不动对方的文件**（`COLLAB.md §3`）。

---

## 6. 变更记录

| 版本 | 日期 | 变更 |
|---|---|---|
| **V1.10** | 2026-10-04 | ★★★ **批 13 的 `A13` 段（`N-052` 第二批）：`PR#safety_branch` 求值器 ＋ ★★ **SA 提交通路首次端到端覆盖** ＋ 注册表覆盖钉子 ⇒ `N-052` 的**可执行范围闭环**（两处具名延后移入 `N-054`）** —— ★ mimo `92a403e`（`checkPRSafetyBranch` ＋ `hardchecks_n052_test.go` 6 测试）经我方**独立验收通过**：门禁 **8/8 ＋ 会报零命中** ＋ **三条单点变异我方自做（隔离成立）** ＋ `cp`/`sha256sum -c` **3/3 还原 OK**。★★ **我方收尾**：`forms/PR.json` **V1.2 → V1.3**（`safety_branch` 由 `soft`/`pending_implementation` 重判为 **`hard`/`code`**）＋ `spec/acceptance.csv` **同批回填该行**（`carried_by` 落点名 `checkPRSafetyBranch`）。★★ **本批最重要的一条更正（我方原表述不准，已据实收窄）**：批 12 的负结果原写「**SA/PR** 激活未被端到端钉住」—— ★ 我方**分单据各做一次证伪对照**：`PR#safety_branch` `soft→hard` ⇒ **精确转红 2 例** ⇒ **PR 本就已覆盖**；`SA#entertain_required` `soft→hard` ⇒ **全绿** ⇒ ★ **缺口只在 SA**。★★ **新增机制**：`TestSubmitHardChecksCoverRealSpec`（遍历真 spec 全 forms，提交时点 `hard` 判据**必须已注册求值器**，跳过 `idempotency_key`/`contract_no_format`）⇒ ★★ 把「某单据没有提交用例 ⇒ 判据标了 `hard` 却永不执行」这条**运行期**的缝，挪到**测试期**且**与单据类型无关**；★ 鉴别力自证＝把 `SA#entertain_required` 临时翻 `hard` ⇒ 该测试**精确转红**并点名。★★ **三条实证级发现（mimo 如实上报，我方复核全部成立）**：(a) `validateSubmitForm`（`handlers_approval.go:634`）**早于** `evaluateHardChecks`（:648）⇒ 同字段的 `=0`/缺失/空串**被结构化先拦**、其 hard 版文案**恒不可达**（冗余双保险，非缺陷），故「hard 真执行」的证据改用**负数**；(b) 我方交办包的两条变异预期**结构性不成立**（本批 `safety_branch` 仍 `soft`，而 `S4` 走结构化、覆盖钉子只查 `hard` ⇒ 摘注册项不会让它们红）⇒ mimo **自补合成 hard 用例**承担鉴别力并主动上报，**不返工**（★★ 教训：**变异的作用面必须与被断言面同层**）；(c) `PR` 侧本就已覆盖。★ 归属：`COLLAB.md#N-052` · `MIMO-NEXT-BATCH-12.md` · `REMAINING.md §5 批 13`。 |
| **V1.9** | 2026-10-04 | ★★★ **批 13 的 `B10` 段（`N-052` 首批）：`when` 归一 6/6 ＋ 补两字段 ＋ `invoice_info` 修形 ＋ 新增 `backfill(<section_id>)` 约定**（★ **零新增判据** ⇒ `checks.json` 仍 **V1.15 / 11 原语 / 27 判据**；★ **零代码改动**）：★ §2 四处版本据实更新（`chain.json` → **V1.1** · `acceptance.csv` → **V1.2** · `forms/*` 逐张版本）；★ §3.1 的 `when_kind` 受控词表**新增 `backfill`**，并把「发现项 4/5」标为**已归一/部分闭环**；★ §3.1 新增**批 13 处置块**（含一条**新缺口**）。★★ **一处口径裁定（我方域内，可反悔）**：`PR#safety_branch` 的「资质文件**或说明**」**新增独立字段 `qualification_doc`**、**不复用** `tech_attachment`（后者已被 `PR#device_tech_attachment` 的条件 `P04` 占用；且「资质」≠「技术参数」）⇒ 若口径要求复用，**反向操作＝删该字段 ＋ 把判据指回 `tech_attachment`**（单点、可逆）。★★ **新增约定写法 `backfill(<section_id>)` 的理由**：`结算补录` 这类**单据后置补录段时点**在 `checks_when` 里**无处可放**，而 ★ **「退到既有类目 `提交后`」会丢信息**（不指明承载者）；★ 新写法的 `<section_id>` **取自 spec 已有的 `sections[*].id`** ⇒ **可机检、非臆造**，且该段自带 `filled_at`（提交期整体跳过）⇒ 语义自洽。★★ **本批实测逼出的新缺口（如实登记、不解决）**：`SA#cross_month_allocation` 的**触发条件不可判** —— 「跨月」依赖 `occurrence_period`（工具表控件＝**日期区间**）的**机读载荷形态**，而 ★ **全仓 `date_range` 零消费端**、自建表单页渲染为**纯文本框** ⇒ ★ **不臆造载荷形态** ⇒ 判据维持 `soft` ＋ `pending_implementation`（登记 `forms/SA.json#known_gaps` 第 4 条）。★★ **同族另一处具名延后**：`SA#counterparty_conditional` —— 「对外支付」可表达（`payment_method_input == '对公直付'`），「需要开票」**无字段** ⇒ **不凭空补字段**。★ 依据：`COLLAB.md#N-052` · `REMAINING.md §5 批 13`。 |
| V1.8 | 2026-10-04 | ★★★ **`N-047` 批 12：23 条判据补齐 `severity` ＋ `S15` 扩为「每条判据都须声明 `carried_by_kind`」＋ `S14.min_hits` 0→1**（`checks.json` **V1.14 → V1.15**；判据仍 **27** 条 · 原语 **11** 个；★ **零引擎改动**，`S15` 只删两个 `args` 键、`S14` 只改一个数）：★ §2 两处版本据实更新（`checks.json` → **V1.15** · `acceptance.csv` → **V1.1**）；§3.1 补两段（`carrier_kind` 的 `submit → code` 映射口径 ＋ **本批处置**）；§4 据实重写 `S14`/`S15` 两行。★★ **为什么 `S15` 必须改范围（本批唯一一次范围变更）**：旧形态只覆盖 `severity=hard`，而**缺 `severity` 的判据既不被它要求、也不被 `evaluateHardChecks` 执行** ⇒ 落在两条机制之间的**缝**里；★★ **且旧形态的条件面恰好保护了不该保护的那批** —— `when_key=severity` 对没有该键的项**直接 `continue`**。⇒ `args` **删 `when_key`/`when_in`**；★ 依据＝两侧 `array_each_required` 均已支持「`when_key` 缺省 ＝ 无条件」（Go `internal/specload/checklist.go` / Python `scripts/check_spec.py`）⇒ **零引擎改动**。★★ **本批的硬次序（实测逼出，且更正了我方先前的一句错话）**：本节此前写「我方先行、给 23 条补 `severity`（不依赖实现）」—— ★ **实测证伪**：`severity=hard` 而求值器未注册 ⇒ `evaluateHardChecks` **当场 fail-closed** ⇒ **`severity` 与求值器必须同批**（与 `N-044`/`N-045`/`N-048` 同型）。⇒ 次序＝mimo 先落 8 条求值器（此时 `severity` 未声明、求值器不被调用、门禁保持全绿）→ 我方落 `severity`/`when`/`S15`/`S14`（两侧齐 ⇒ 绿）。★★ **本批的证伪对照（我方自做，一次只变异一处，`cp` ＋ `sha256sum -c` 逐一还原）**：① 删一条 `carried_by_kind` ⇒ **`S15` 两侧同时报红**；② `S14.args.collect` 改成不存在路径 ⇒ **报「仅命中 0 处（要求 ≥1）」**；③ 摘掉 `amount_positive` 注册项 ⇒ **14 个 BA 提交用例精确转红**（证明**真实提交路径**消费真 spec 的 `severity`）；④ ★ **负结果**：`SA#entertain_required` 由 `soft` 改 `hard` ⇒ **全绿** ⇒ **SA/PR 的激活未被端到端钉住**（登记 `N-052`）。★★ **一处据实更正**：`PR#amount_positive` 曾被改判「提交时点不可得、不可执行」—— ★ **实测证伪**（`handlers_approval_pr_amount.go#resolvePRAmountForTier` 在 `handlers_approval.go` 判据求值**之前**写入 `estimated_total_cents`，`mergeProvidedFields` 全量复制）⇒ 本批落 `hard` ＋ `code`。★★ **另订正 `acceptance.csv` V1.0 的一处误记**：5 条 `soft` 判据的承载原记为 `code`，**实测它们无任何执行通道**（引擎跳过一切非 `hard`）⇒ 改为 `pending_implementation`。★ 落点：`spec/forms/BA.json`（8）· `PR.json`（6）· `SA.json`（9）· `GR.json`/`QC.json`/`RFQ.json`/`SS.json`/`SUB.json`（各 1，仅补 `carried_by_kind`）· `spec/checks.json` · `spec/acceptance.csv` · 本文件。★ 依据：`COLLAB.md#N-047` · `MIMO-NEXT-BATCH-11.md` · `REMAINING.md §5 批 12`。★★ **同日补漏（`N-053`）**：`acceptance.csv` 首批**漏落 `BA` 8 行**（`severity` 仍 `(未声明)`）—— ★ 此漏**通过了全部 8 道必绿门禁**，因 ★★ 该表**不被任何脚本读取**（无台账↔真源校验）。⇒ 已补齐（**97/97 行两列全等**）＋ 在 `scripts/check_spec.py` 内置 `[META]` 自审 `_ledger_vs_forms()`（★ Python-only、不引入新原语）＋ **探针自证**（修复前台账 ⇒ 报 9 处；复位后 0）；★ 「升级为正式判据需新原语 ⇒ 两侧同批」登记 `N-053`。 |
| V1.7 | 2026-10-04 | ★★★ **接口契约的机读形态入库（`REMAINING.md#B6` 闭环 · `N-051`）⇒ 新增判据 `S21`/`S22`/`S23`**（`checks.json` **V1.13 → V1.14**；判据 **24 → 27**；★ **零引擎改动**，三条全用既有 `required_keys` 原语 ⇒ **两侧自动一致**）：★ 交付 **`spec/openapi.json` V1.0**（OpenAPI 3.0.3 · **60 path / 69 operation**），由 **`scripts/gen_openapi.py`** 从 `docs/05-API.md`（人读正本 **V2.20**）**机械生成**。★★ **为什么是 `.json` 而不是原计划的 `.yaml`（据实改判）**：★ 本仓库 `checks.json#_format_note` 早已定案 spec **一律 JSON**；★★ **实测取证**（本轮）：① **PyYAML 在本机不可用**（`import yaml` 报错）⇒ `.yaml` 走不了 Python 侧；② `check_spec.py#S1` **只 glob `spec/**/*.json`**、`specload` 只按名取已知文件 ⇒ **`.yaml` 落在整个门禁面之外**（**完全不可见** ＝ 交了个没人校验的契约）。⇒ **据实改判为 `.json`**（★ `REMAINING.md#B6` 原写 `openapi.yaml`，本次一并更正）。★★ **纪律「只引用不复制」**（与 `acceptance.csv` 同）：契约只搬 `security` / `parameters`（路径参数） / `x-error-codes`（整数列表） / `x-related-fr`（id 列表） / `x-doc-ref`（**行号区间溯源**）；★ **不搬散文正文、不臆造逐端点 schema**（体量 164 KB → 154 KB；未做项全部登记 `x-known-gaps`）。★★ **理由「为什么要生成而不是手写」**：手抄一份 OpenAPI **就是第二份真相**，必然漂（同 `docs/11 §5.1` vs `§5.2`、`N-033` 的「凭印象写指标 key」）⇒ 映射规则写死在生成器代码里、机读形态**永远可从正本重现**。★ **路由集三方一致实测**：正本声明（69）↔ `router.go`（69，去 `GET /`＋`GET /*` 静态兜底）↔ 生成器输出（69），`C5` 口径**双向差集为空**。★★ **三重把关 ＋ 各自边界**：`S21`–`S23`（**结构完整** · 在必绿清单内）· `C9`（**契约 ↔ 正本一致性**：① `x-source.doc_sha256` 指纹 ② 路由集**逐字**双向差集 ③ 条数为 0 报错防「都空」假绿；**会报**项）· `scripts/_probe_n051.py`（**10/10**：重现一致性 ＋ 路径参数完备性 ＋ `operationId` 唯一性＋规则一致 ＋ 路由集等价 ＋ **六条反例**）。★ **本批的鉴别力已用三处单点变异实测**（① 删某 operation 的 `security` ⇒ **`S22` 精确红、其余 26 条保持绿**；② 篡改 `x-source.doc_sha256` ⇒ **`C9` 指纹分支精确报 1 处**；③ 删一条 path ⇒ **`C9` 路由集分支精确报 1 处**）⇒ **隔离性成立**，`cp`＋`sha256sum -c` **3/3 还原 OK**。★ 如实登记**两处正本自身待收敛项**（契约**镜像不修正**）：① 路径参数命名不一致（§3.4 `{instance_code}` vs §3.12 `{code}`，同一路由）；② §3.13 正文 `error_detail.unresolved_roles[]` vs §4.6 定稿 `data.unresolved_roles`（**同一概念两种键路径**）。★ **残留窗口（如实登记）**：生成器**尚未接入 `check_all.sh` 必绿项**（当前靠 `C9` 每次跑生成器重建 + 探针；★ 「升级为必绿」需先落**读 Markdown 的新原语** ⇒ 属两侧同批，登记 `N-051`）。 |
| V1.6 | 2026-10-04 | ★★★ **`N-006` 第 2 重机制收官 ⇒ 新增第 11 原语 `path_exists` ＋ 判据 `S20`**（`N-048`，**两侧同批**；`checks.json` **V1.12 → V1.13**；原语 **10 → 11**、判据 **23 → 24**）：★ §2 两行据实更新（`checks.json` → **V1.13** · `institution-anchors.json` → **V1.1**）；★ §4 **`S20` 正式入表**，并**销掉**原先「拟新增 `S20` 尚未落地」的提示（★「**不预登记未生效判据**」的纪律**保留**，只是它现在已生效）；★★ **§3.2 新增「段归一顺序」规格**。★★★ **本轮最值钱的一条（验收期实跑逼出，非推断）**：**两侧同契约的分歧可以只潜伏在「未出现的形态」上** —— Python 旧实现**先换 `[*]`→`*`、再拆 `name[k]` 尾缀** ⇒ `checks[*]` 折成**裸键 `checks*`**（命中 0），而 Go **先拆尾缀**（命中）⇒ **同一份索引、两侧一绿一红**；★ 而真锚点（158 条）**零 `[*]` 形态** ⇒ **「两侧各跑一遍都对」根本抓不住** ⇒ ★ 处置＝**两侧各留一道回归钉**（Go `TestPathExistsStarBracketSegment` ＋ Python `scripts/_probe_n048.py`），★ 且 Go 侧钉经**单点变异实测**（改回反序 ⇒ **恰该 1 条转红**）。★ 另如实登记：`known_gaps.not_full_pointer_index`（22 处无稳定键 ⇒ 只计数不建指针）与 `generator_outside_repo` **仍未闭环** —— ★ 本版**不声称**已覆盖。 |
| V1.5 | 2026-10-04 | ★★ **交付 `institution-anchors.json` V1.0（`N-006` 第 2 重机制）＋ 新增 §3.2 指针语法**：★ §2 文件清单该行据实更新（**28 条款 / 158 稳定指针 / 324 处引用**）；★★ **新增 §3.2** —— 把三件事写成规格：① **段语法**（裸键 / `[字面键]` / `[k=v]` 选择器 / `*` / `**`）；② ★★ **切分规则＝按 `.` 切段但括号内不切分**（两条实证：`spec/params.json` 的 **5 个含点扁平键** ＋ `change_log[version=1.5]` 的**选择器值含点**）；③ ★ **生成器必须排除自身**（实测自引用污染 324 → 353）；★ 并说明**为什么锚点不用数组索引**（重排即静默错位）；§4 补「**拟新增 `S20`（`path_exists`）尚未落地**」的说明（★ 不预登记未生效判据）；★ 条款标题**故意不设**（制度正本非本仓库，臆造＝假信息）。 |
| V1.4 | 2026-10-04 | ★★★ **`S19` 由窄版升级为完整版「禁惰性必需节点」**（`N-044` 收尾立项 ⇒ `N-045` 落地后升级；`checks.json` **V1.11 → V1.12**）：`pattern_absent`（只禁复合串 `→`）⇒ **`ref_exists`**（`routes.*.nodes[*].actor` ⊆ `chain.json#roles` 键集合 ∪ `extra_allowed:["system"]`）。★ **为什么用引用式白名单而非写死 8 个字面量**：本项目立场「**判据＝数据，不得在代码里写字面量**」（`conventions.agent_allowed` 同款）。★ **为什么当初只落窄版**：完整版**先落地而未修节点即当场红** ⇒ 判据与修复**必须同批**；本次次序＝我方落规格（删描述性 `actor` ＋ `tier_expand`）→ mimo 实现 → 判据升级。★★ **红→绿同批实证**（先破坏再修）：把 `"actor": "按档位审批人"` 加回 ⇒ **Python 报 `[S19] … 引用 '按档位审批人' 不存在于目标键集合` ＋ Go `go test ./internal/specload` 同时红（同一文案）**；移除 ⇒ 两侧复绿。★ 如实登记**残余盲区**（`roles` 表内 3 个非审批角色）；§2/§4 相应行已据实更新。 |
| V1.3 | 2026-10-04 | ★★ **交付 `acceptance.csv`（`N-017` 闭环）＋ 修两处本表陈旧**：① 新增 **§3.1**（`acceptance.csv` 列约定与两张受控词表），§2 文件清单该行据实更新为 **V1.0 · 97 条**；② ★ **`decision_kind` 词表由原拟 5 项扩为 10 项**（原 5 项塞不下占比最高的**数值比较**与**引用比较**）；③ ★ **修本表自身的两处陈旧**（§2 的 `checks.json` 仍写 `V1.6 / 9 原语 / 18 判据`，实为 **V1.11 / 10 原语 / 23 判据**；§4 索引只列到 `S13`，**漏 `S14`/`S15`/`S16`/`S18`/`S19` 五条**，且未写明 `S17` 已撤销）—— ★ **这正是本 README 自己警告过的「索引漂移」**（§6 `V1.1`）；★ **`S15` 那行补写它的边界**（只管 `severity=hard`）；④ ★★ **本轮落表实测发现 5 组问题**（已登记 `COLLAB.md#N-047`）：**23 条判据缺 `severity` ⇒ 落在 `S15` 与 `evaluateHardChecks` 的缝里**（既不执行、也不被要求声明承载者）· **5 条无任何承载** · **3 条时点未接线** · **6 条 `when` 不合约定** · **2 处无字段可承载 ＋ 1 处条件必填被静默跳过**。★ **不改任何判据状态、不改 `forms`** —— 改 `severity` 与补字段属**同批变更**，登记不抢跑。 |
| V1.2 | 2026-09-30 | ★ **随交付 `forms/QC.json` 修掉两条判据互相矛盾（零引擎改动）**：`checks.json` **V1.5 → V1.6**；① `S5b.min_hits` **1 → 0**（★ 显式写 0：两侧缺省都是 1）—— 因 `min_hits` 是**每文件**语义，而「有的单据确实不落账」（`QC`/`RFQ`/`BJ` 的 `ledger` 就是 `[]`，`S12` 又要求它与映射**逐字相同**）⇒ **原判据与 `S12` 互相矛盾**；② **新增 `S5d`** 承接原守栏并升级（每张表单必须显式声明 `ledger` 等关键键）；③ §2 状态更新（`checks.json` V1.6 / `forms` **7 张**）；④ 本表判据数 **17 → 18**。★ **零引擎改动**（`S5d` 只用已有原语 `required_keys`）⇒ 两侧自动一致；★ 已由**两侧引擎一致性探针** `scripts/_probe_s5d.py`（**6/6**）验证：篡改 spec 后 **Python 与 Go 都报 `S5d`**。 |
| V1.1 | 2026-09-30 | ★ **消漂移 ＋ 登记新文件（纯文档）**：① **§2 文件清单**补登 **`checks.json` / `enums.json` / `constants.json` / `authority.json`（新）**，并据实更新状态（`forms/` 已交付 6 张 · `ledger-mapping` 已交付 · `RESOLUTIONS` V1.5 30 条 · `params` V1.1 `open_items` 清零）；② ★★ **§4 校验项改为「只是索引、不是正本」** —— 原表只列 `S1`–`S9`，而 `checks.json` 已达 **17 条**（`S5a/b/c`、`S6b/c`、`S10`–`S13` 从未登记）⇒ 逐条据实重列 ＋ 补「两侧共同契约」与**语言怪癖**（`bool` 是 `int` 子类）说明；③ **§5 修正制度文件名**（原写 `采购及费用审批制度V4.0.docx`，实为 **`采购及费用审批管理办法V4.0.docx`**）；④ `S1` 自证记录补「已当场抓出过两次中文引号误写」。★ **不改任何编号**。 |
| V1.0 | 2026-09-29 | 首版：目录定位 / 文件清单 / 命名约定 / 门禁九项与自证记录 |
