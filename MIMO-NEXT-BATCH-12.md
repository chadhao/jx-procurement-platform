# MIMO-NEXT-BATCH-12 —— 批 13：`N-052` 第二批（`PR` 新字段求值器 ＋ ★ SA 提交端到端用例）

> ★ **本文件可整份粘贴给 mimo code。**
> 交付方：**WorkBuddy**（规格与判据；★ 本批规格已于 `e69e77a` 入库）／ 实现方：**mimo code**（求值器 ＋ 测试）
> 依据：`COLLAB.md §4 · N-052`；`REMAINING.md §1 B10 / §2 A13 / §5 批 13`；`spec/forms/PR.json` **V1.2** · `SA.json` **V1.1** · `chain.json` **V1.1**
> 时间：2026-10-04 17:20

---

## §0 问题本体（一句话 ＋ 我方已实测的边界）

★ **规格段已完成（`e69e77a`，我方域）**：`when` 归一 **6/6**（含 `chain.json#conventions.checks_when` **新增一类 `backfill(<section_id>)`**）·
`PR` 新增字段 `qualification_doc`（`text_and_attachment`，`required_conditional="is_safety_or_special_equipment == true"`）·
`SA` 新增字段 `allocation_note`（`textarea`，★ **刻意不带 `required_conditional`**）· `SA#invoice_info` 修形为 `required: true`（段级 `filled_at` 承载时点 ⇒ 提交期整体跳过）。

★★ **本批要做的是「激活 ＋ 覆盖」两件事**：

1. **激活 `PR#safety_branch`** —— 该判据现为 `soft` ＋ `pending_implementation`，**求值器未注册** ⇒ ★ **不得**直接标 `hard`
   （`when` 以 `submit` 开头而无求值器 ⇒ `evaluateHardChecks` 当场 fail-closed，**已实测**）。⇒ 本批由你方落求值器，
   **随后由我方**重判为 `hard` ＋ `code`（★ **规格归我方，你方不要改 `severity`**）。
2. ★★ **补上「SA 提交路径」的端到端覆盖** —— 见下。

★★★ **我方本轮实测（两条证伪对照，`cp` ＋ `sha256sum -c` 已还原；★ 结论与你方批 12 回执的表述有一处出入，据实更正）**：

| # | 变异（一次只改一处，改完即还原） | 实测 | 结论 |
|---|---|---|---|
| **A** | `spec/forms/PR.json`：`PR#safety_branch` 的 `severity` `soft → hard` | **精确转红 2 例**：`TestPRSubmitFrontendShapeContract` / `TestPRSubmitAmountMismatchWarnsNotRejects`，文案一致＝`hard 判据 "safety_branch" 未实现求值器（不许静默通过）` | ★ **PR 侧已被真 spec 端到端覆盖** |
| **B** | `spec/forms/SA.json`：`SA#entertain_required` 的 `severity` `soft → hard` | `go test ./internal/httpapi -count=1` ⇒ **全绿、退出码 0、零「未实现求值器」** | ★ **SA 侧确实无任何用例走真 spec 的提交路径** |

⇒ ★★ **更正**：批 12 回执写的是「**SA/PR** 侧未被端到端钉住」，★ **实测只对 SA 成立**（PR 有 `pr_contract_test.go` / `hard_checks_t4_test.go` 等 handler 级用例，天然覆盖）。
⇒ ⇒ 本批的覆盖缺口**只在 SA**；PR 侧只需补**新字段的那条**（见 §1.2）。

---

## §1 本批规格（★ 逐条对齐；**不在下表的一律不动**）

### 1.1 `PR#safety_branch` 求值器（1 条）

判据原文（`spec/forms/PR.json#checks[id=safety_branch]`）：`when:"submit"` · `assert:"is_safety_or_special_equipment == true ⇒ 须附资质文件或说明"` · `else:"拒绝提交"`。

| 项 | 值 |
|---|---|
| 注册 id | `safety_branch`（★ **全仓仅 `PR` 一处** —— 请你方独立核实；若确有同名，则按 `form.DocType` 分流，同 `amount_positive` 之先例） |
| 触发条件 | ★ **在求值器内部判**（`when` 只是 `submit`，条件不写在 `when` 里）：`is_safety_or_special_equipment` 为**布尔 `true`** 时才检 |
| 命中后校验 | `qualification_doc` **非空**（`hHas`）⇒ 空则拒绝 |
| 条件未命中 | ★ **放行**（`assert` **未声明双向** ⇒ **不得**反向要求「非涉安时该字段必须为空」—— ★ 与 `checkBASafetyCertificate` / `checkPRDeviceTechAttachment` **同款纪律**） |
| 拒绝文案 | 中文、**点名字段**；★ 与「读不到值」区分开（本题无「读不到」分支，条件是布尔判定） |
| 范式 | ★ **照抄** `internal/httpapi/handlers_approval_hardchecks.go#checkBASafetyCertificate`（BA 的条件型，四例齐） |

★ **`qualification_doc` 是本批 `e69e77a` 新增的字段**，与 `tech_attachment` **互不替代**（`tech_attachment` 已被 `PR#device_tech_attachment` 占用，条件 `P04`）。

### 1.2 ★★ SA 提交端到端（handler 级、**走真 spec**）—— 本批最要紧的一条

★ 现状：`internal/httpapi` 里**没有任何用例**以 `POST /api/approval/submit` 提交一张 **`doc_type=SA`** 的单
（全仓 SA 出现处全是台账种子数据或合成 `FormDoc` 直调；★ 已用 grep 核实）。

**装配**：沿用既有范式（`hard_checks_t4_test.go#newPRSubmitApp` ／ `ss_pc_submit_test.go#newSSPCApp`）——
`specload.Load(specfs.FS)`（**真 spec**，不得用合成 `FormDoc`）＋ `approval_code ↔ doc_type` 映射（`code-sa → SA`）＋
`Maps.Ledger = {"SA": {"L05"}}` ＋ 角色（`申请人` / `主管领导` / `综合运营主管`）。

**SA 提交时点必填（`source=user ∧ required ∧ 提交段`，逐条来自 `spec/forms/SA.json`）**：
`expense_subject` · `usage_category_l1` · `usage_category_l2` · `amount_cents` · `purpose` · `occurrence_period` · `payment_method_input`。
★ 链归线（`internal/chain/route.go#resolveExpenseRoute`，★ **建议取最简的一条**）：`usage_category_l1 = "S01"` ⇒ `expense_sales`（不依赖支付方式二分）。
★ `occurrence_period` 是 `date_range` 类型：提交期按**非空字符串**即可（★ **不要**为它引入新的机读形态 —— 那是 `SA#cross_month_allocation` 的**延后项**，见 §3）。

| # | 用例 | 期望（★ 必须精确断言） |
|---|---|---|
| **S1** | 合规 SA 载荷 | **200** ＋ 响应 `biz_no` 前缀 **`SA-`** ＋ 链算成功（`flow` 任务已建 —— 取实例/任务可读即算，不必推到终态） |
| **S2** | `amount_cents` 缺失 **或** `= 0` | **400**，文案点名「金额必须大于 0」（★ 由已注册的 `SA#amount_positive` 承载 ⇒ **这一例即证明「SA 提交路径真的执行了真 spec 的 hard 判据」**） |
| **S3** | 只填 `usage_category_l1`、`usage_category_l2` 留空 | **400**，文案点名「用途分类一/二级均须填写」（`SA#completeness_l2`） |
| **S4** | `PR#qualification_doc` 条件必填（★ **新字段的提交期校验**，走 `validateSubmitForm` 的 `evalSimpleEqual` 路径） | ① `is_safety_or_special_equipment=true` ∧ `qualification_doc` 空 ⇒ **400**（点名 `qualification_doc`）；② 同条件但 `qualification_doc` 有值 ⇒ **200**；③ `is_safety_or_special_equipment=false` ∧ `qualification_doc` 空 ⇒ **200**（★ 这是「条件未命中 ⇒ 放」的钉子） |

★ **S4 与 S1–S3 分开**（一个走 `validateSubmitForm`，一个走 `evaluateHardChecks`）—— ★ **不要**把两者混成一条断言。

### 1.3 ★ 注册表覆盖钉子（测试-only，1 条）

新增**一条**测试（如 `internal/httpapi/hardchecks_coverage_test.go#TestSubmitHardChecksCoverRealSpec`）：
遍历 **真 spec**（`metaTestBundle(t)`）的**全部** `forms[*].checks`，对每条满足
「`severity == "hard"` ∧ 提交时点 `when`（`submit`／`submit ` 前缀／含「提交前」）∧ id ∉ {`idempotency_key`, `contract_no_format`}」
的判据，断言 `submitHardChecks[id]` **存在**；不存在 ⇒ `t.Errorf` 逐条列名。

★ **为什么加这一条**：`evaluateHardChecks` 的 fail-closed 只在**真有人提交该单据**时才触发
⇒ ★★ **「某单据没有任何提交用例」＝ 该单据的提交时点判据可以长期「标了 hard 却没人执行」而全绿**（本轮实测的 SA 缺口就是这一形态）。
这条把「缝」从**运行期**挪到**编译/测试期**，且**与单据类型无关**（BA/PR/SA/CT/SS/PC/GR/QC/BJ/RFQ/SUB 一次性全盖上）。
★ 本批新增该测试后，把 `SA#entertain_required` 临时翻 `hard`（★ **你方自行做、做完 `cp` 还原**）⇒ ★ **该测试必红**（这是它的鉴别力自证）。

### 1.4 前端核实（★ 我方已先核实，请你方独立复核）

★ 我方已读 `web/src/views/Submit.vue`：`visibleFields` ＝「`filled_at` 为空的段」×「`source=user` 的字段」
⇒ 新字段 `PR#qualification_doc`（`header` 段）与 `SA#allocation_note`（`header` 段）**均在渲染面内**；
`textarea` / `text_and_attachment` 落 `v-else`（纯文本框）—— ★ **与既有 34 个 `textarea` / 17 个 `attachment` 字段同款**。
⇒ ★ **预计无需前端改动**（附件的上传走页级 `attachment_ids`，非字段级 —— ★ 这是**既有设计**，不是本批引入的缺口）。

★ 请你方**独立核实**（跑一次或读代码）：① 两个新字段确实出现在 `SA`/`PR` 表单的渲染结果里；② 可填可提交。
★ **若确需改动，只做最小改动**并在回执说明「为什么我方的核实不成立」；★ **不要**顺手引入 `date_range` 分支（延后项，见 §3）。

---

## §2 交办（T1–T4）

### T1 · 求值器（Go）
实现并注册 §1.1 的 `safety_branch`（`handlers_approval_hardchecks.go` 或同包新文件）。
★ 保持既有分节注释风格；★ 注释里注明阈值/条件出处（`spec/forms/PR.json#checks[id=safety_branch]`）；★ `gofmt` 必须干净。

### T2 · 测试（Go）
- §1.2 的 **S1–S4**（★ handler 级、**真 spec**、拦放双向、文案点名）。
- §1.3 的**覆盖钉子**（1 条）。
- §1.1 求值器的**直接单测**（条件型四例：命中且缺 ⇒ 拒／命中且有值 ⇒ 放／未命中且空 ⇒ 放／未命中且有值 ⇒ 放）。
- ★ 家数/文案类纪律照旧：**不得**把「读不到」与「值不合法」写成同一句。

### T3 · 三条单点变异（★ 一次只改一处；`cp` 备份还原，**禁用 `git checkout --`**）

| 变异 | 预期 |
|---|---|
| **M1** `checkPRSafetyBranch` **去掉条件判断**（一律要求 `qualification_doc` 非空） | 恰红「`is_safety=false` ∧ 空 ⇒ 应放」一例（§1.1 单测 ＋ §1.2 S4③），**其余全绿** |
| **M2** `checkPRSafetyBranch` **改成恒放行**（不再检查 `qualification_doc`） | 恰红「`true` ∧ 空 ⇒ 应拒」一例（§1.1 单测 ＋ §1.2 S4①），**其余全绿** |
| **M3** **摘掉** `submitHardChecks["safety_branch"]` 注册项 | 恰红 §1.2 S4②（＝`true` ∧ 有值 应 200，实得「未实现求值器」）＋ **§1.3 覆盖钉子红**（★ 证它有鉴别力），**其余全绿** |

★ 三条都要给出「红在哪、绿在哪」的**逐条对照**；★ 还原后复跑 `go test ./... -count=1` 全绿、`.bak` 已删
（★ 注意：本仓 `internal/httpapi/` 下**已有两个历史 `.bak`**，勿新增、勿动它们）。

### T4 · 回执 ＋ 自测
- `bash scripts/check_all.sh` ⇒ **必绿 8/8 ＋ 会报零命中**（★ 本批 `spec/**` 零改动 ⇒ 判据 / Go 包 / 净检出**零波动**）。
- `gofmt -l` 空、`go vet ./...` 干净；若动了前端 ⇒ `scripts/build.sh` 通过 ＋ 零新增 eslint error。
- ★ **回执里必须逐条回答**：① S1–S4 与覆盖钉子是否都落地（逐条现在是什么断言）；② 各条的**拒绝文案**；
  ③ `safety_branch` 是否**全仓仅 PR 一处**（取证方式）；④ §1.4 的独立核实结论（是否需要前端改动）；
  ⑤ ★ 有没有哪条**按 §1 无法实现**（★ **有就如实写出来 ＋ 指出冲突点**，**不要**自行改规格、**不要**静默降级）。

---

## §3 划界（★ 严格；不在表内的一律不做）

- ★ **不动 `spec/**`**（含 `forms/*.json` 的 `severity` / `carried_by_kind` / `when` / `acceptance.csv` / `checks.json`）—— ★ **`severity` 的翻转由我方在验收后做**。
- ★ ★★ **本批两处明确不做（★ 已具名延后，不要顺手做）**：
  1. `SA#cross_month_allocation` —— 承载字段 `allocation_note` **已补入**，但**触发条件不可判**（「跨月」依赖 `occurrence_period` 的**机读载荷形态**；★ 实测全仓 `date_range` **零消费端**）⇒ ★ **本批不要求它翻 `hard`、不注册求值器、不引入 `date_range` 前端分支**。
  2. `SA#counterparty_conditional` —— 「对外支付」可表达（`payment_method_input == '对公直付'`），但「**需要开票**」**无任何字段承载** ⇒ ★ **本批不补字段、不归一 `when`、不翻转**。
- ★ **不动** `evaluateHardChecks` 的**白名单逻辑与 fail-closed 语义**（既有契约）。
- ★ **不动** 批 12 已落地的 8 条 BA/PR/SA 求值器语义（含 `amountPositiveOf` 的 `DocType` 分流）。
- ★ **不动** `chain.json` / `router.go` / `N-048` 的 `path_exists` 面 / 既有 `.bak`。
- ★ **不做** `A8`（GR/RFQ/QC 发起通路接线 —— 我方尚未出具通路口径）。

---

## §4 完成判据（三条，缺一不可）

1. ★★ `COLLAB.md` 的 **`### N-052`** 段内写入你的回执（改动文件显式路径 ＋ T2/T3 对照表 ＋ 如实项），
   ★ **并在该段内出现字面量 `MIMO-DONE`**（驱动脚本的完成判据①）。★ **议题状态字段仍填 `OPEN`**
   （本批只是 `N-052` 的 `A13` 段；★ `cross_month` / `counterparty` 两项延后未闭 ⇒ 结案由我方判定）。
2. **提交并推送** `origin/main`：**显式路径**提交（★ **禁止 `git add -A`**），提交信息带你的署名前缀。
3. 提交前跑 `bash scripts/check_all.sh` ⇒ **必绿 8/8**。

★ 若中途被打断，直接从断点继续；★ 若判定某条**无法按规格实现**，**照样提交已完成部分 ＋ 在回执里如实写明**，
**不要**为了「看起来完成」而改动规格或降级语义。
