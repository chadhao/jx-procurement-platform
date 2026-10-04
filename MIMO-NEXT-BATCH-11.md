# MIMO-NEXT-BATCH-11 —— 批 12：`N-047` 第一批（补 8 条提交时点求值器）

> ★ **本文件可整份粘贴给 mimo code。**
> 交付方：**WorkBuddy**（判据规格与 `severity` 声明）／ 实现方：**mimo code**（求值器 ＋ 测试）
> 依据：`COLLAB.md §4 · N-047`；`spec/acceptance.csv` **V1.0**；`REMAINING.md §5 批 12`
> 时间：2026-10-04 14:20

---

## §0 问题本体（一句话）

`spec/forms/BA.json` / `PR.json` / `SA.json` 里**共 23 条判据没有 `severity` 键**。
`internal/httpapi/handlers_approval_hardchecks.go#evaluateHardChecks` 首行是
`if c.Severity != "hard" { continue }` ⇒ **这 23 条被提交引擎逐条跳过**；
而 `spec/checks.json#S15` **只管 `severity=hard`** ⇒ 也不要求它们声明承载者。
⇒ ★★ **结果是「既不执行、也不报错」**（与 `N-036` 同族，但**连 `S15` 都看不见**）。

★ **不是我方不填** —— 是**填了会当场红**（本轮已实测）：把 `BA#amount_positive` 标 `hard` ⇒
`evaluateHardChecks` 对**未注册的提交时点 `hard` id 直接 fail-closed**（`不许静默通过`）⇒ 门禁立刻红。
⇒ ★ 故**必须先由你方落求值器，再由我方落 `severity`**（与 `N-048` 批 11 的硬次序同型）。

★★ **本批范围的划定（我方定，逐条有依据；不在表内的一律不做）**：
23 条中，**8 条属「提交时点 ＋ 承载字段已存在」** ⇒ 本批交办（下表 §1）。
其余 15 条我方**同批**另行处置（**不交办给你**）：6 条非提交时点（`pending_wiring`/`manual`/`structural`）、
3 条 `idempotency_key`（引擎已特例跳过）、2 条自然语言（`manual`）、
★ **4 条我方改判为「当前不可执行」并具名登记**（`PR#amount_positive` · `PR#safety_branch` ·
`SA#counterparty_conditional` · `SA#cross_month_allocation`，理由见 `COLLAB.md#N-047` 回执）。

---

## §1 本批规格（★ 逐条对齐；**不在下表的一律不动**）

★ 全部 8 条的 `when` 均已**以 `submit` 开头** ⇒ 落在 `evaluateHardChecks` 的提交白名单内，
**必须注册求值器**，否则我方落 `severity` 的**同一刻**门禁即红。

### 1.1 ★★ 注册表是 **id → 函数** ⇒ **同一 id 跨单据必须按 `form.DocType` 分流**

现状 `submitHardChecks` 是 `map[string]hardCheckFn`，**只按 id 索引**。
`amount_positive` 与 `completeness_l2` 在 **BA / PR / SA 三处同名**，★ 而取值字段不同
（BA/SA ＝ `amount_cents`；PR ＝ `estimated_total_cents`）⇒ ★★ **不可写成三个同名键**
（后注册者会覆盖前者），**必须在单个函数内按 `form.DocType` 分流**
（`specload.FormDoc` 已有 `DocType` 字段；`form.DocType ∈ {"BA","PR","SA"}`）。

| # | 单据 | 判据 id | `when` | `assert`（原文） | 求值语义（★ 我方裁定，逐条执行） | 拒绝文案要点 |
|---|---|---|---|---|---|---|
| 1 | BA | `amount_positive` | `submit` | `amount_cents > 0` | 取**金额**：`body.AmountCents`（顶层，BA 权威来源）优先；为 `nil` ⇒ 回落 `fields.amount_cents`（`hNum`）。★ **两者皆缺 ⇒ 可见失败**（不是静默放行）。≤ 0 ⇒ 拒绝 | 金额必须大于 0 |
| 2 | BA | `amount_tier1_only` | `submit` | `amount_cents <= 99999` | 取值同 #1；`> 99999`（＝ ≥ 1,000 元）⇒ 拒绝 | 采一档备案单**仅适用 < 1,000 元**；超出须走 PR（采二/采三档） |
| 3 | BA | `completeness_l2` | `submit` | `usage_category_l1 与 usage_category_l2 均已填` | 读 `hbProvided` 的 `usage_category_l1` / `usage_category_l2`，**任一为空 ⇒ 拒绝** | 用途分类一/二级均须填写 |
| 4 | BA | `safety_certificate` | `submit && usage_category_l1 == 'P03'` | `qualified_certificate 非空` | **条件在求值器内判**：`usage_category_l1 == "P03"` 时才检；`qualified_certificate` 为空 ⇒ 拒绝。★ 非 P03 ⇒ 放行（**不得**反向要求「非 P03 时该字段必须为空」—— `assert` 未声明双向） | 涉安类别（P03）须附合格证明 |
| 5 | PR | `completeness_l2` | `submit` | 同 #3 | 同 #3（PR 版） | 同 #3 |
| 6 | PR | `device_tech_attachment` | `submit && usage_category_l1 == 'P04'` | `usage_category_l1 == 'P04' ⇒ tech_attachment 非空` | **条件在求值器内判**：`usage_category_l1 == "P04"` 时才检；`tech_attachment` 为空 ⇒ 拒绝。★ 非 P04 ⇒ 放行（同上，不得反向） | 设备类（P04）须附技术附件 |
| 7 | SA | `amount_positive` | `submit` | `amount_cents > 0` | 同 #1（SA 版） | 同 #1 |
| 8 | SA | `completeness_l2` | `submit` | 同 #3 | 同 #3（SA 版） | 同 #3 |

### 1.2 硬约束（逐条都不可放宽）

1. ★ **只加求值器与注册项，不改任何既有行为的语义**；`form.Checks` 里 `severity` 仍为**未声明**
   ⇒ ★ **本批提交后，这 8 个函数在真实提交路径上尚不会被调用**（由我方随后的 `severity` 提交启用）。
   ⇒ ★★ **故测试必须构造「合成 `FormDoc`」**（把目标 `CheckDoc.Severity` 置 `"hard"`，照
   `internal/httpapi/bj_checks_test.go` 的先例直调 `d.evaluateHardChecks`），**不得**依赖真实 spec。
2. ★ **不得改 `spec/**`**（判据、`severity`、`carried_by_kind` 全归我方）。
3. ★ **文案必须能被用户读懂且点名字段**（中文）；★ **不得**把「读不到值」与「值不合法」写成同一句。
4. ★ **fail-closed 的口径要与既有函数一致**：读不到必需输入 ⇒ **可见失败**，不是放行。
5. ★ 家数/金额类取值**一律走服务端可得的输入**；**不得**在求值器里再写一遍 `spec` 里已有的阈值字面量之外的规则
   （`99999` 是本条 `assert` 的原文，属**判据＝数据**的例外，允许出现，但须在注释里注明出处 `spec/forms/BA.json#checks[id=amount_tier1_only]`）。

---

## §2 交办（T1–T4）

### T1 · 求值器（Go）
在 `internal/httpapi/handlers_approval_hardchecks.go`（或同包新文件）实现 §1 的 8 条语义，
并注册进 `submitHardChecks`（★ 按 §1.1 的分流口径，`amount_positive` / `completeness_l2` **各一个键**）。
★ 保持既有分节注释风格（`// ---- BA ----` 等）；★ `gofmt` 必须干净。

### T2 · 测试（Go）
- 每条**拦 / 放成对**：至少「应拒 ⇒ 非 nil 且文案点名」＋「应放 ⇒ nil」。
- ★ **条件型两条（#4 #6）必须四例**：条件命中且缺字段 ⇒ 拒；条件命中且字段有值 ⇒ 放；
  **条件未命中且字段为空 ⇒ 放**（★ 这是「不得反向要求」的钉子）；条件未命中且字段有值 ⇒ 放。
- ★ **#1 #2 #7 必须含边界例**：`0` ⇒ 拒；负数 ⇒ 拒；`99999` ⇒ 放；`100000` ⇒ 拒（#2）。
- ★ **#1 #2 #7 必须含「金额两处都缺」例** ⇒ 拒（fail-closed）。
- ★ **跨单据分流的钉子**：同一 id 用一个 `DocType=BA` 的表单跑、再用 `DocType=PR` 的表单跑，
  ★ **断言 PR 那条读的是 `estimated_total_cents` 而不是 `amount_cents`**（防「同键互相覆盖」重现）。

### T3 · 三条单点变异（★ 一次只改一处；`cp` 备份还原，**禁用 `git checkout --`**）
| 变异 | 预期 |
|---|---|
| **M1** 把 `BA#amount_tier1_only` 的阈值由 `99999` 改成 `999999` ⇒ | 恰好该边界用例转红（`100000` 由拒变放），**其余条全部保持绿** |
| **M2** 把 `completeness_l2` 的判据由「任一为空即拒」改成「只判 L1」⇒ | 恰好 `L1 有 / L2 空` 一例转红，其余绿 |
| **M3** 删掉 `amount_positive` 的 `form.DocType` 分流（改成一律读 `amount_cents`）⇒ | 恰好 **PR** 的那条转红（PR 的 `amount_cents` 不存在 ⇒ 拒），BA/SA 保持绿 |
★ 三条都要给出「红在哪、绿在哪」的**逐条对照**；★ 还原后复跑 `go test ./... -count=1` 全绿、`.bak` 已删。

### T4 · 回执 ＋ 自测
- `bash scripts/check_all.sh` ⇒ **必绿 8/8 ＋ 会报零命中**（★ 本批 `spec/**` 零改动 ⇒ 判据 / Go 包 / 净检出**零波动**）。
- `gofmt -l` 空、`go vet ./...` 干净。
- ★ **回执里必须逐条回答**：① 8 条是否都按 §1 落地；② 各条的**拒绝文案**；③ 有没有哪条**按 §1 无法实现**
  （★ 有就**如实写出来 ＋ 指出冲突点**，**不要**自行改规格、**不要**静默降级 —— 这正是本项目的纪律）。

---

## §3 划界（★ 严格）

- ★ **不动 `spec/**`**（含 `forms/*.json` 的 `severity` / `carried_by_kind` / `checks.json` / `acceptance.csv`）。
- ★ **不动** §1 表外的任何判据（尤其 `idempotency_key` 三处、`PR#safety_branch`、`SA#counterparty_conditional`、
  `SA#cross_month_allocation`、两条自然语言判据）。
- ★ **不动** `evaluateHardChecks` 的**白名单逻辑与 fail-closed 语义**（那是既有契约）。
- ★ **不动** `PR#amount_positive`（我方已改判「提交时点金额未就绪」，见 §0）。
- ★ **不做**前端；**不做**新字段；**不动** `chain.json` / `router.go` / `N-048` 的 `path_exists` 面。

---

## §4 完成判据（三条，缺一不可）

1. ★★ `COLLAB.md` 的 **`### N-047`** 段内写入你的回执（改动文件显式路径 ＋ T2/T3 对照表 ＋ 如实项），
   ★ **并在该段内出现字面量 `MIMO-DONE`**（这是驱动脚本的完成判据①）；★ 议题**状态字段仍填 `OPEN`**
   （本批只是 `N-047` 的**第一批**，结案由我方在落 `severity` 后判定）—— ★ **`MIMO-DONE` 与 `OPEN` 不冲突**：
   前者是「本轮已交差」，后者是「议题尚未整体闭环」。
2. **提交并推送** `origin/main`：**显式路径**提交（★ **禁止 `git add -A`**），提交信息带你的署名前缀。
3. 提交前跑 `bash scripts/check_all.sh` ⇒ **必绿 8/8**。

★ 若中途被打断，直接从断点继续；★ 若判定某条**无法按规格实现**，**照样提交已完成部分 ＋ 在回执里如实写明**，
**不要**为了「看起来完成」而改动规格或降级语义。
