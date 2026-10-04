# MIMO-NEXT-BATCH-13 —— 批 14：`N-054` ① 落地（`date_range` 前端分支 ＋ 跨月判定求值器 ＋ 端到端用例）

> ★ **本文件可整份粘贴给 mimo code。**
> 交付方：**WorkBuddy**（规格与判据；★ 本批规格已于 `1edce8c` 入库）／ 实现方：**mimo code**（前端分支 ＋ 求值器 ＋ 测试）
> 依据：`COLLAB.md §4 · N-054`；`REMAINING.md §1 B12 / §5 批 14`；`spec/chain.json` **V1.2** · `spec/forms/SA.json` **V1.2** · `spec/checks.json` **V1.16**
> 时间：2026-10-04 18:50

---

## §0 问题本体（一句话 ＋ 契约已定）

★ **背景**：`SA#cross_month_allocation` 长期 `soft` ＋ `pending_implementation`，原因＝「跨月」**不可判** —— 它取决于 `occurrence_period`（工具表控件类型＝**日期区间**）的**提交载荷形态**，而本仓此前**对该形态无任何约定**（与 `checks_when` 当年同病：`type` 只声明**控件类型**、不声明**序列化形态**）。

★★ **我方已于本批定契约并锚定门禁**（`1edce8c`）：

| 项 | 内容 |
|---|---|
| 唯一规格来源 | `spec/chain.json#conventions.field_payload_forms` |
| 受控取值（当前唯一） | **`iso_interval`** |
| 载荷形态 | ISO 8601 区间串 **`YYYY-MM-DD/YYYY-MM-DD`**（斜杠为约定分隔符） |
| 端点位 | ★ **首尾均为闭区间端点**，`start ≤ end` |
| 「跨月」定义 | `start` 与 `end` 落在**两个不同的自然月**（按 `YYYY-MM` 比较） |
| ★★ 不可解析 | **⇒ 可见失败**（★ 不得静默通过、**不得静默当作「不跨月」**） |
| 数据落点 | `spec/forms/SA.json#sections[*].fields[name=occurrence_period].payload_form = "iso_interval"` |
| 门禁锚定 | `checks.json` **V1.16** 新增 **`S24`**（凡 `type == date_range` 须声明 `payload_form`）／**`S25`**（取值须 ∈ 受控词表）—— ★ **零引擎改动、两侧自动一致** |

★★ **本批要做三件事**：① 前端把 `date_range` 渲染成**真区间输入**并组装成该形态；② 落 `cross_month_allocation` 的**跨月判定求值器**；③ 补**端到端用例**（走真 spec、拦放双向）。
★★ **本批不做**：`N-054` ②（`SA#counterparty_conditional` 的「需要开票」字段 —— ★ 待工具表/制度侧口径，属**外部输入**）。
★ **`severity` 的翻转由我方在验收后做**（★ **你方不要改 `spec/**`**）。★ 次序不可颠倒：`hard` 而求值器未注册 ⇒ 提交期 fail-closed（**已实测**）。

---

## §1 本批规格（★ 逐条对齐；**不在下表的一律不动**）

### 1.1 前端：`date_range` 分支（`web/src/views/Submit.vue`）

★ 现状（我方已读代码）：字段渲染只有 `select` / `textarea` / `v-else`（纯文本框）等分支
⇒ ★ `occurrence_period`（`type = "date_range"`）当前落 **`v-else` ＝ 纯文本框**，用户可随手填任意字符串。

| 项 | 值 |
|---|---|
| 渲染 | ★ 两个日期输入（起 / 止）—— ★ **具体技术形态归你方**（HTML5 `input type="date"` 是自然选择；★ **不得引入新依赖**） |
| 组装 | 提交载荷里该字段的值＝ **`YYYY-MM-DD/YYYY-MM-DD`**（★ 单字符串，与契约一致） |
| 空值 | 未填 ⇒ ★ **载荷里不带该字段**（与既有字段同款；该字段是提交段必填 ⇒ 由结构化校验拦） |
| 不合法（起 > 止） | ★ 前端**可见报错**（提示文案），★ **不得提交** |
| 解析入参（回填/编辑） | 既有值若已是 `YYYY-MM-DD/YYYY-MM-DD` ⇒ 回填两个输入；★ 若形态非法 ⇒ **可见提示**（★ **不得静默清空**） |

★ **最小改动原则**：只加 `date_range` 分支，★ **不要**顺手重构既有的渲染分支（34 个 `textarea` / 17 个 `attachment` 的既有行为**零变化**）。

### 1.2 ★★ `SA#cross_month_allocation` 跨月判定求值器（1 条）

判据原文（`spec/forms/SA.json#checks[id=cross_month_allocation]`）：`when: "submit && occurrence_period 跨月"` · `assert: "跨月 ⇒ allocation_note 非空"` · `severity: soft`（★ **本批由你方落求值器后，由我方重判 `hard` ＋ `code`**）。

| 项 | 值 |
|---|---|
| 注册 id | `cross_month_allocation`（★ **请你方独立核实全仓唯一**；若同名 ⇒ 按 `form.DocType` 分流，同 `amount_positive` 先例） |
| 触发条件 | ★ **在求值器内部判**（`when` 里的中文描述**不要**写进引擎的条件面）：解析 `occurrence_period` ⇒ 判定是否跨月 |
| ★★ 不可解析 | **⇒ 拒绝**（fail-closed，文案点名 `occurrence_period` 的**格式要求**）—— ★ 理由：静默放行＝**用畸形载荷绕过分摊要求**（内控绕过） |
| 可解析且**跨月** | `allocation_note` **非空**（`hHas`）⇒ 空则**拒绝**（文案点名 `allocation_note`） |
| 可解析且**不跨月** | ★ **放行**（`assert` **未声明双向** ⇒ 不得反向要求「不跨月时该字段必须为空」—— ★ 与 `checkBASafetyCertificate` 同款纪律） |
| 范式 | ★ **照抄** `internal/httpapi/handlers_approval_hardchecks.go#checkPRSafetyBranch`（条件型四例齐） |

★ **解析口径（逐字）**：值须匹配 `^YYYY-MM-DD/YYYY-MM-DD$`（**恰好一个斜杠**、两段均为合法 ISO 日期）⇒ 取 `start` / `end`；
「跨月」＝ `start[:7] != end[:7]`（按 `YYYY-MM` 比较）。
★ **边界**：`start == end` ⇒ **不跨月**；`start > end` ⇒ ★ **视为不可解析 ⇒ 拒绝**（契约明写「首尾闭区间、`start ≤ end`」）。

### 1.3 端到端用例（handler 级、**走真 spec**）

★ 现状：批 13 已为 SA 补上 handler 级端到端（`internal/httpapi/hardchecks_n052_test.go` 的 `S1`–`S4`）⇒ ★ **本批在其上追加**（★ **不要**另起一套装配）。

| # | 用例 | 期望（★ 必须精确断言） |
|---|---|---|
| **X1** | `occurrence_period = "2026-10-05/2026-10-20"`（**同月**） ∧ `allocation_note` 空 | **200**（★ 「不跨月 ⇒ 放」的钉子） |
| **X2** | `occurrence_period = "2026-09-28/2026-10-03"`（**跨月**） ∧ `allocation_note` 空 | **400**，文案点名 `allocation_note`（★ **这一例即证明「真 spec 的该判据真的被执行」**） |
| **X3** | `occurrence_period = "2026-09-28/2026-10-03"` ∧ `allocation_note` 有值 | **200** |
| **X4** | `occurrence_period = "2026-10-01"`（★ **单日期、缺斜杠**） | **400**，文案点名 `occurrence_period` 的**格式要求**（★ **不可解析 ⇒ 可见失败**的钉子） |

---

## §2 交办（T1–T5）

### T1 · 前端（`web/src/views/Submit.vue`）
实现 §1.1。★ 保持既有代码风格；★ **不引入新依赖**；★ 若要抽纯函数便于测试 ⇒ **照 `repeatRows.js` 范式**（零 Vue/DOM、Node 直跑）。

### T2 · 求值器（Go）
实现并注册 §1.2 的 `cross_month_allocation`。
★ 注释里注明条件/契约出处（`spec/forms/SA.json#checks[id=cross_month_allocation]` ＋ `spec/chain.json#conventions.field_payload_forms`）；★ `gofmt` 干净。

### T3 · 测试（Go）
- §1.3 的 **X1–X4**（handler 级、**真 spec**、拦放双向、文案点名）。
- §1.2 求值器的**直接单测**（四例：跨月且缺 ⇒ 拒／跨月且有值 ⇒ 放／不跨月且空 ⇒ 放／**不可解析 ⇒ 拒**）。
- ★ 文案纪律照旧：**不得**把「不可解析」与「该字段没填」写成同一句。

### T4 · 三条单点变异（★ 一次只改一处；`cp` 备份还原，**禁用 `git checkout --`**）

| 变异 | 预期 |
|---|---|
| **M1** 求值器**去掉「跨月」判定**（一律要求 `allocation_note` 非空） | 恰红 X1（同月 ∧ 空 ⇒ 应放），**其余全绿** |
| **M2** 求值器**改成恒放行** | 恰红 X2（跨月 ∧ 空 ⇒ 应拒），**其余全绿** |
| **M3** **不可解析改静默放行**（当作「不跨月」） | 恰红 X4（单日期 ⇒ 应拒），**其余全绿** |

★ 三条都要给出「红在哪、绿在哪」的**逐条对照**；★ 还原后复跑 `go test ./... -count=1` 全绿、`.bak` 已删（★ 注意：`internal/httpapi/` 下**已有两个历史 `.bak`**，勿新增、勿动它们）。

### T5 · 回执 ＋ 自测
- `bash scripts/check_all.sh` ⇒ **必绿 8/8 ＋ 会报零命中**（★ 本批 `spec/**` 零改动 ⇒ 判据 / Go 包 / 净检出**零波动**）。
- `gofmt -l` 空、`go vet ./...` 干净；★ 动了前端 ⇒ `scripts/build.sh` 通过 ＋ **零新增 eslint error**（★ 本仓有 1 个既有 baseline error 于 `web/src/App.vue`，**不算新增**）。
- ★ **回执里必须逐条回答**：① X1–X4 是否都落地（逐条现在是什么断言）；② 各条的**拒绝文案**；③ `cross_month_allocation` 是否**全仓唯一**（取证方式）；④ 前端改动的**最小性**（是否动过既有分支）；⑤ ★ 有没有哪条**按 §1 无法实现**（★ **有就如实写出来 ＋ 指出冲突点**，**不要**自行改规格、**不要**静默降级）。

---

## §3 划界（★ 严格；不在表内的一律不做）

- ★ **不动 `spec/**`**（含 `forms/SA.json` 的 `severity` / `carried_by_kind` / `when` / `acceptance.csv` / `checks.json`）—— ★ **`severity` 的翻转由我方在验收后做**（`soft` → `hard` ＋ `pending_implementation` → `code`）。
- ★ **不做** `N-054` ②（`SA#counterparty_conditional` 的「需要开票」字段 —— 待**外部输入**）。
- ★ **不动** `evaluateHardChecks` 的**白名单逻辑与 fail-closed 语义**。
- ★ **不动** 批 12／批 13 已落地的求值器语义（含 `amountPositiveOf` 的 `DocType` 分流、`checkPRSafetyBranch`）。
- ★ **不动** `chain.json` 的既有 `conventions` 其它键 / `router.go` / `N-048` 的 `path_exists` 面 / 既有 `.bak`。
- ★ **不做** `A8`（GR/RFQ/QC 发起通路接线 —— 我方尚未出具通路口径）。
- ★ **不动** `S24`/`S25` 的 args 与语义（★ 门禁已绿，别「顺手优化」`min_hits` —— 那里有一条**两侧语义分歧**未定，见 `COLLAB.md#N-055`）。

---

## §4 完成判据（三条，缺一不可）

1. ★★ `COLLAB.md` 的 **`### N-054`** 段内写入你的回执（改动文件**显式路径** ＋ T3/T4 对照表 ＋ 如实项），★ **并在该段内出现字面量 `MIMO-DONE`**（驱动脚本的完成判据①）。★ **议题状态字段仍填 `OPEN`**（本批只是 `N-054` ① 的落地段；★ ② 待外部输入 ⇒ 结案由我方判定）。
2. **提交并推送** `origin/main`：**显式路径**提交（★ **禁止 `git add -A`**），提交信息带你的署名前缀。
3. 提交前跑 `bash scripts/check_all.sh` ⇒ **必绿 8/8**。

★ 若中途被打断，直接从断点继续；★ 若判定某条**无法按规格实现**，**照样提交已完成部分 ＋ 在回执里如实说明**，**不要**为了「看起来完成」而改动规格或降级语义。
