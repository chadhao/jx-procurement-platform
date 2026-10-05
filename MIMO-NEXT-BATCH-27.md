# MIMO-NEXT-BATCH-27 —— `REMAINING.md#B23` / `N-062` 族 `J3` 的 **`SA` 侧落地段**（`backfill` 后置补录入口 ＋ **契约行同批落**）

> **交付方**：WorkBuddy（制度规范与产品设计方）
> **执行方**：mimo code
> **日期**：2026-10-06（批 45 · ★ 本包**本轮交办**）
> **前置**：`MIMO-NEXT-BATCH-26.md`（`BA` 侧两条）已交付并经我方独立验收（mimo `edbef2d`）；★ `SA` 侧的**规格已由批 42 落齐**（`spec/chain.json` **V1.11** · `spec/forms/SA.json` **V1.5**）⇒ **本包交办 `SA` 侧的实现 ＋ 契约行**。
> **★ 动手前必读**：`COLLAB.md#N-062`（议题正文 ＋ 你方 `J3` 三缺口取证 ＋ 我方批 42 的规格落地段）· **`spec/chain.json#conventions.checks_when` 末段「两处时点的承载口径」第 ② 条**（**本包的唯一权威契约来源**）· `spec/forms/SA.json#sections[id=settlement_backfill]` · **`spec/chain.json#conventions.node_task_generation`**（同包 `T3` 的可达性口径）。

---

## 0. 硬约束（沿用，勿破）

- ★ **工作范围**：只能在 `C:\Users\haoduan\workspace\jx-procurement-platform` 目录内读写。
- ★ **以台账为准**：冲突时一律以 `COLLAB.md` / `spec/` 当前内容为准（**与记忆冲突时以台账为准**）。
- ★ **提交只用显式路径**（**禁止 `git add -A`**）；提交后 `git show --stat HEAD` 复核。
- ★ **每次提交前跑 `bash scripts/check_all.sh`，必绿基线 9/9**（会报项应**零命中**）。
- ★ **还原用 `cp` ＋ `sha256sum -c`**，**不得用 `git checkout -- <file>`**。
- ★ **一次只变异一处**；★ **变异「仍绿」不能判通过**（先确认变异处**是否真被执行**）。
- ★★ **`spec/**` 仍是我方（WorkBuddy）先生域 —— ★ 但本包有一条【必须由你方执行】的 `spec/` 改动**：`spec/openapi.json` 是**机械生成物**（`scripts/gen_openapi.py` 由 `docs/05-API.md` 生成）⇒ ★ **只许「用脚本重生」**，**严禁手改**；★ **除该生成物外，本包不得改动 `spec/` 下任何其他文件**（`chain.json`／`forms/*.json`／`checks.json`／`README.md` 等**一律不动**）。
- ★★ **`docs/**` 双方均可改**（`COLLAB.md §7`：「任一方改动须在该表登记一行」）—— ★ **本包必须改 `docs/05-API.md`**（新增契约行 ＋ 版本行），理由见 §1。
- ★★ **本包是一条「必须同批」的硬链**：**路由 ＋ handler ＋ 契约行 ＋ `spec/openapi.json` 重生 ＋ 生成器计数 ＋ 常驻探针期望值 ＋ 路由测试清单** —— ★ **缺任何一环，门禁必红**（见 §4 同批清单）。★ **不要「先写文档、后补路由」，也不要「先写路由、后补文档」**；**一次改完再跑门禁**。

---

## 1. 背景（为什么有这一包，为什么必须同批）

★★ **我方批 42 已把 `SA` 侧的规格写进 `spec/chain.json#conventions.checks_when` 的「两处时点的承载口径」第 ② 条**，其**入口最小契约**原文摘录（★ **以 `spec` 现文为准，本包只是转述**）：

> **`backfill(<section_id>)` 的入口口径**：该时点由**后置补录入口**承载，其**最小契约** ＝ `POST /api/approval/{biz_no}/backfill`，四道约束：
> ☆ 实例必须已 `APPROVED` 且 `doc_type` 与该段所属单据一致；
> ☆ 只接受**该段 `source=user` 的字段**（**白名单**，不得越段写 `header`、不得写 `source ∈ {system, computed}` 字段）；
> ☆ 写入落 `ext_json`（**不新增表／列**）；
> ☆ 写后**执行 `when=backfill(<section_id>)` 的判据**、失败按该判据的 `else` 处置。

★★★ **同一段里我方已明文写下「为什么契约行必须与路由实现同批落」**：

> ★★ **本口径是「我方规格先行」，尚未写入 `docs/05-API.md` 的契约表** —— ★ **原因（本仓库已立的纪律）**：人读正本契约表与其机读形态 `spec/openapi.json` ＋ `internal/httpapi/router.go` 三者**路由集必须逐字一致**（会报项 `C9` ＋ 判据 `S21`–`S23`）⇒ ★ **先写文档 ＝ 当场造出一条「文档声明了、路由不存在」的红线**（`N-061 ④` 刚拆掉一个同类案例）⇒ ★ **契约行必须与路由实现同批落**。

⇒ ★ **本包就是把这三者（正本 / 机读 / 路由）在同一批里一起落下来。**

★★ **治的缺口（如实定性）**：`SA#actual_not_exceed` 与 `SA#invoice_must_link` 两条 `severity=hard` 判据的 `when=backfill(settlement_backfill)` —— ★ 该段的**补录入口从未建过** ⇒ 引擎对非提交时点一律 `continue` **跳过** ⇒ ★★ **两条 `hard` 判据的 `else`（「须走超支确认」「不予受理」）承诺至今从未发生过**。★ 两判据今为 `carried_by_kind: pending_wiring`（★ **本包落地后须翻 `code`，改判由我方同批做 —— 你不要改 `spec/forms/SA.json`**）。

---

## `T1` · 新增 `POST /api/approval/{biz_no}/backfill`（后置补录入口）

### 1.1 先取证（★ 落笔前必须做，回执里给 `文件:行号`）

1. ★ `t_instance` **是否已有 `ext_json` 列**？列名与类型？（`migrations/` 与 `store/` 两处都要看到）★ 若**没有** ⇒ ★ **停手**、在议题里写清方案（★ 「不新增表／列」是本契约的硬约束，不得自行加列）。
2. ★ **现有实例读写路径**：谁写过 `t_instance.ext_json`？谁读过？★ 已知线索：`applicant_department` 的客户端同名值会**残留 `ext_json`**（`spec/forms/SA.json` 的 `carried_by` 有 2026-10-03 实测记述）⇒ ★ 找出**那条写入路径**（`文件:行号`）。
3. ★ **判据求值入口**：`internal/httpapi/handlers_approval_hardchecks.go#evaluateHardChecks` 的**入参形态**（能否按 `section_id` 只跑该段的 `when=backfill(<section_id>)` 判据？★ 若不能，**最小改造**是什么？）。
4. ★ **`{biz_no}` 解析与鉴权**：既有 `POST /api/approval/{biz_no}/approve` 的 handler（`文件:行号`）如何取实例、如何做**行级/角色**校验 ⇒ ★ 本端点**须沿用同一套**（不得自造）。

### 1.2 契约（我方已定，照此实现）

★ **路径与方法**：`POST /api/approval/{biz_no}/backfill`
★ **挂载位置**：`internal/httpapi/router.go` 的 `api` 组（**须 `requireSession`**），★ 与 `:biz_no` 系列同段（`approve`/`reject`/`transfer`/… 旁）。
★ **鉴权**：免登会话 ＋ **行级**（★ 沿用 `approve` 同款取实例与校验；★ **谁能补录**：本期取「**申请单的申请人本人**」—— 若你方复核认为应放宽/收紧，**在议题里写明**、不自行放宽）。

**请求体**（★ 我方定的最小形态）：

```json
{ "fields": { "invoice_info": "…", "actual_cents": 123400 } }
```

★ **白名单（硬）**：只接受 `spec/forms/SA.json#sections[id=settlement_backfill].fields[*] where source == "user"` 的 **`name`** ⇒ 当前恰 **4** 个：`handler` · `invoice_info` · `actual_cents` · `invoice_count_and_overrun_note`。
★ **必须拒绝**：不在白名单的键（含 `header` 段字段、`biz_no`/`applicant` 等 `source=system` 字段、`actual_vs_approved_diff_cents` 这个 `source=computed` 字段）⇒ ★ **可见拒绝**（非 200），**不得静默丢弃**（★ 静默丢弃＝「写了却没人管」，本仓头号反模式）。

**响应**（★ 我方定的最小形态；★ 包在 §2 统一包裹里）：

```json
{ "biz_no": "SA-2610-0001", "section_id": "settlement_backfill",
  "written": ["invoice_info", "actual_cents"],
  "checks": [ { "id": "actual_not_exceed", "severity": "hard", "passed": true } ] }
```

★ **错误码**：★★ **不得新增错误码** —— 复用既有（与本仓提交期同类拦截所用者一致）；★ 用了哪个**在回执里写明**。

### 1.3 四道约束的落点（逐条对应 §1 的原文）

| # | 约束（原文） | 你要做的 |
|---|---|---|
| ☆1 | 实例必须已 `APPROVED` 且 `doc_type` 与该段所属单据一致 | 取实例 ⇒ 断言 `status == APPROVED`、`doc_type == "SA"`；★ 不满足 ⇒ **可见拒绝** |
| ☆2 | 只接受该段 `source=user` 的字段（白名单） | ★ **白名单从 `spec/forms/SA.json` 读**（**不得把 4 个字段名硬编码** —— ★ 那是「第二份真相」，`S14`/`N-058` 一族的老问题） |
| ☆3 | 写入落 `ext_json`（不新增表／列） | ★ 落 `t_instance.ext_json`；★ **与既有键合并**（不得整列覆盖 —— ★ 会把 `applicant_department` 之类既有残留**冲掉**） |
| ☆4 | 写后执行 `when=backfill(<section_id>)` 的判据、失败按 `else` 处置 | ★ 白名单写毕 ⇒ **在同一请求内**跑该段判据；★ `hard` 不过 ⇒ **可见失败（非 200，并点名判据）** |

### 1.4 验收判据（我方将独立重做）

- ★★ **端到端（走真 `Deps` ＋ 真路由 ＋ 真 spec）**：造一张 **APPROVED 的 `SA`** 实例 ⇒
  - ① 补录 `actual_cents > 批准额` ⇒ **拦**（非 200 且点名 `actual_not_exceed`）；
  - ② 补录 `actual_cents ≤ 批准额` ＋ `invoice_info` 非空 ⇒ **放**（200，且 `written` 含两键）；
  - ③ 不在白名单的键（★ 至少试 `amount_cents` 与 `actual_vs_approved_diff_cents` 两个）⇒ **可见拒绝**；
  - ④ 非 `APPROVED` 实例（`PENDING`）⇒ **可见拒绝**；
  - ⑤ **写后可读**：`GET /api/approval/{biz_no}`（或既有详情出口）能读回补录值。
- ★★ **零回归**：既有 `SA` 提交/审批用例全绿；★ **`ext_json` 既有键不被冲掉**（★ 造一个带残留 `applicant_department` 的实例再补录，断言残留仍在）。
- ★★ **单点变异（至少一处，红/绿实测原文写进回执）**：
  - 建议：把**白名单过滤**改成**恒放行**（或去掉）⇒ ③ 的用例**必须恰红**；★ **与该变异无关的用例（①②④）必须保持绿**（**隔离性**）。
  - ★ **第二处（加分，鼓励）**：把 ☆4 的**判据执行**摘掉 ⇒ ① 的用例**必须恰红**。
  - ★ 还原一律 `cp` ＋ `sha256sum -c`（**不得** `git checkout --`）。

---

## `T2` · ★★ **同批落「路由集三处一致」**（★ 本包的心脏，缺一环即红）

★ 本仓的硬约束：**`docs/05-API.md`（人读正本）↔ `spec/openapi.json`（机读形态）↔ `internal/httpapi/router.go`（路由实现）三处路由集必须逐字一致**。理由是**双向都会报**：

- ★ **正本有、路由无** ⇒ `C5`（`scripts/audit_silent.py`，**双向集合差**）＋ `C9`。
- ★ **路由有、正本无** ⇒ `C5` 反向（`audit_silent` 报「已注册未文档」）。
- ★ 且 `check_all.sh` 会跑 `python scripts/gen_openapi.py --check`（**重生一致性**）。

★ **因此以下 6 件事必须一次改完**（★ 顺序：先改实现与正本 ⇒ 再重生 ⇒ 再改计数与探针 ⇒ 最后跑门禁）：

| # | 文件 | 改什么 |
|---|---|---|
| 1 | `internal/httpapi/router.go` | 注册 `api.POST("/approval/:biz_no/backfill", d.handleApprovalBackfill)` |
| 2 | `internal/httpapi/handlers_approval*.go` | 新增 `handleApprovalBackfill`（含 `T1` 的四道约束） |
| 3 | **`docs/05-API.md`** | ① **§3.13 全路径清单表**（`:734` 表头 · `:735`–`:749` 数据行）**新增一行**（格式照抄邻近行：`\| POST \| `/api/approval/{biz_no}/backfill` \| 请求 \| 响应 \|`；★ **建议插在 `:741` 的 `approve` 行之前**，与其余 `:biz_no` 系列同段）；② **§3.13 新增一个 `#### POST /api/approval/{biz_no}/backfill（后置补录 · N-062 族 J3）` 小节**（`\| 项 \| 内容 \|` 表：用途 / 鉴权 / 请求体 / 白名单 / 响应 / 错误码 / 关联）；③ **头部「版本」字段**（`:10`）更新为 **V2.22**（★ 照现有写法把本批要点写进那一长串）＋ **§12 变更记录表**（`:1178` 起，**新→旧**）**表首**（现 `V2.21` 行在 `:1182`）**之上插一行 `V2.22`**（★ 日期写 **2026-10-06**；★ **不得**使用相对时间词，见 §6） |
| 4 | **`spec/openapi.json`** | ★★ **只许 `python scripts/gen_openapi.py` 重生**，**严禁手改任何一个字节**（★ 它带 `doc_sha256`／`doc_lines`／`route_count`，手改必被 `gen_openapi.py --check` 抓出） |
| 5 | `scripts/gen_openapi.py` | ★ `x-source.router_go_route_count` **68 → 69**（`:563`）＋ 同处 `router_go_route_count_note`（`:564`–`:567`）的「**正本 68 ⇔ router 68**」改 **69 ⇔ 69**；★ **文件头注释里也有「68 条」字样**（`:7`、`:27`–`:28`），**一并改成 69** —— ★ **口径变了就要全改**（★ 该字段是**人工维护的溯源元数据**、`C5` 不复算它 ⇒ **改漏了门禁不会红，会静默错**） |
| 6 | `scripts/_probe_n051.py` | ★ 多处**写死 68**（`:16` `:20` `:56` `:68` `:69` `:132` `:140` `:141` `:223`）⇒ **68 → 69**；★★ **并同步「缺口存在性反证」里的期望**（`:229` `:232` `:235` 的 `mut_count == 69` ⇒ **70**；标签「68→69」⇒ **69→70**）—— ★ **这条最易漏**（漏了会让第 5 项「反证」恒红） |

★ **另须同步**：`internal/httpapi/router_test.go` 的 `expectRoutes` 清单（★ **凡在测试里列举路由清单者一并补齐**，别只补一处）。

★★ **回执必须给出**：`python scripts/gen_openapi.py --check` 的输出原文 ＋ `bash scripts/audit_silent.py`（或门禁里的 `C5`/`C9` 段）**零命中**原文 ＋ 路由计数**两侧一致**的证据（★ **`69 ⇔ 69`**）。

---

## `T3` · 与 `MIMO-NEXT-BATCH-26.md` 的**接口一致性**（★ 只核对，不重做）

★ 本包的 `backfill` 判据 `SA#actual_not_exceed` / `SA#invoice_must_link` 属 **`backfill(<section_id>)`** 时点 —— ★ **与 `approval(<node_id>)` 是两类时点**（见 `conventions.checks_when`），**不要**把它并进 `T3` 的通用求值器（★ 求值器只负责 `approval(<node_id>)`；`backfill` 由**本包的入口**承载）。

★ **须在回执里明写一句**：两个时点**各自的承载者**分别是谁（`approval(<node_id>)` ⇒ 通用求值器；`backfill(<section_id>)` ⇒ 本包的 `POST …/backfill` 入口），★ **不得含糊**（这是我方批 42 立「两个不同的可达性」那条口径的直接应用）。

---

## 2. 明确**不做**（★ 重要）

| 项 | 原因 |
|---|---|
| 改 `spec/chain.json` · `spec/forms/*.json` · `spec/checks.json` · `spec/README.md` | ★ **我方先行域**；★ 规格已由批 42 落齐 ⇒ **只消费、不改写** |
| 把 `SA#actual_not_exceed` / `SA#invoice_must_link` 的 `carried_by_kind` 从 `pending_wiring` 翻 `code` | ★ **`spec` 声明，属我方域** ⇒ ★ **你方只实现；落地后由我方同批翻**（★ 翻的**前提是入口真的通了**） |
| 计算 `actual_vs_approved_diff_cents`（差额字段） | ★ 该字段 `source=computed`、`carried_by_kind: accepted_gap`（★ **具名豁免**，仍在我方裁定中）⇒ ★ **本包只允许它作为「非白名单键被拒」的反例，不要求你实现计算** |
| 补录**前端页面**（`SA` 详情页补录区 UI） | ★ 本批只做**通路**；★ UI 精细化另批（★ 如你方认为**没有 UI 就无法端到端验证**，请**用 HTTP 级用例**证明通路，UI 另计） |
| `docs/` 下**其他**文件 | ★ 本包**只需** `docs/05-API.md`；★ 若你方认为还有其他文档必须同改（如 `docs/11 §4.2` 的「须新增路由」表），**先改、并在 `COLLAB.md §7` 登记一行**、回执里说明 |

---

## 3. 交付要求

1. 每完成一族跑 `bash scripts/check_all.sh`，**必绿 9/9 ＋ 会报零命中**。
2. ★ `T1`：四道约束**逐条**给落点（`文件:行号`）＋ **白名单从 `spec` 读**（★ 给出读取处的 `文件:行号`，证明**不是硬编码**）。
3. ★ `T2`：§4 六项**逐项**给出改动证据（★ 尤其 `--check` 与计数一致）。
4. ★ **单点变异**：至少一处（★ 建议＝白名单恒放行），给出**红/绿实测原文** ＋ **隔离性**（无关用例仍绿）＋ `cp`/`sha256sum -c` 还原输出。
5. ★ **先取证后落笔**：§1.1 四项取证的结论（`文件:行号`）**放在回执最前面**（★ 若某项取证导致方案必须变，**先停、在议题里写清**）。
6. 提交**只用显式路径**；完成后在 `COLLAB.md#N-062` **追加一条 `MIMO-DONE` 逐条回执**（★ **本包只对应 `#N-062` 一个议题段** —— 若你方认为还须写别的议题段，**也一并写**）。
7. 推送 `origin/main`。
8. ★ 需要我方裁定才能定的口径，**停在议题里写清楚，不要猜**。

---

## 4. 同批清单（★ 自查用，缺一即红）

- [ ] `router.go` 注册了新路由
- [ ] handler 实现（含四道约束）
- [ ] `docs/05-API.md`：全路径表 **1** 行 ＋ `####` 小节 **1** 个 ＋ 版本字段/变更记录 **V2.22** 行
- [ ] `spec/openapi.json` **由脚本重生**（`gen_openapi.py --check` **OK**）
- [ ] `scripts/gen_openapi.py` 计数 **69**（含文件头注释与 `_note`）
- [ ] `scripts/_probe_n051.py` 期望值 **69** ＋ **反证期望 70**
- [ ] `router_test.go#expectRoutes`（及其它列举路由的测试）补齐
- [ ] `bash scripts/check_all.sh` **必绿 9/9 ＋ 会报零命中**
- [ ] 单点变异 ＋ 隔离性 ＋ `cp`/`sha256sum -c` 还原
- [ ] `COLLAB.md#N-062` 追加 `MIMO-DONE` 回执
- [ ] 显式路径提交 ＋ 推送 `origin/main`

---

## 5. 单点变异要求（我方验收时会**独立重做**，★ 不采信自报）

★ 至少 **1** 处（★ 建议 2 处），并把**红/绿实测原文**写进回执：

- `T1` 建议 ①：把**白名单**改为**恒放行** ⇒ 「非白名单键被拒」的用例**必须恰红**。
- `T1` 建议 ②：摘掉**判据执行**（☆4）⇒ 「超支被拦」的用例**必须恰红**。
- ★ 两处都要**同时报告**「与该变异无关的用例保持绿」（**隔离性**）。
- ★ 还原一律 `cp` ＋ `sha256sum -c`（**不得** `git checkout --`）。

---

## 6. 文书与时间口径（★ 沿用本仓既有纪律，勿破）

- ★ **时间一律写绝对日期**（`YYYY-MM-DD`；需要时到分钟）—— ★ **不得**使用「今天」「昨天」「上周」「本批刚」这类**相对时间词**（★ 台账与文档要能被**日后独立复核**，相对时间一过即失真）。★ 本包已写明：`docs/05-API.md` 的 `V2.22` 变更记录行日期 ＝ **2026-10-06**。
- ★ **所有文字用简体中文**（含 `zh-rHK` / `zh-rTW` 语境一律简体）。
- ★ **台账时间戳取真值**：需要提交时间时一律 `git log --format=%ci` 读，**不得**凭估计顺推。
- ★ **`COLLAB.md` 的字段名逐字固定**：`- **背景**：` / `- **建议方案**：` 等须**逐字**且**内容同在一行**（换行会被判「字段为空」）；`- **类型**：` 只允许既有 5 个枚举值（**加括注即非法**）。
- ★ **Markdown 表格里的「竖线」一律转义为 `\|`** —— ★★ **反引号不保护竖线**（本仓头号陷阱；`COLLAB.md` / `MIMO-*.md` 已在表格门禁扫描面内）。
