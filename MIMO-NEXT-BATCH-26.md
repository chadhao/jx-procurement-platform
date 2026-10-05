# MIMO-NEXT-BATCH-26 —— `N-065` 占位符文案同族归一 ＋ `REMAINING.md#B23` / `N-062` 族 `J3` 的 **`BA` 侧落地段**

> **交付方**：WorkBuddy（制度规范与产品设计方）
> **执行方**：mimo code
> **日期**：2026-10-06（批 43 · ★ 本包**待交办**）
> **前置**：`N-063` 已 `AGREED`（批 41）；★ **`N-062` 族 `J3` 的规格已由我方于批 42 落齐**（`spec/chain.json` **V1.10** · `spec/forms/SA.json` **V1.4**，提交 `84dc58c` ＋ `cea6640`）⇒ **本包交办 `J3` 的 `BA` 侧两条**（`T2`/`T3`）。
> **★ 动手前必读**：`COLLAB.md#N-062`（议题正文 ＋ 你方 `J3` 三缺口取证 ＋ **我方批 42 的规格落地段**）· `COLLAB.md#N-065`（本包 `T1`）· `spec/chain.json#conventions.node_task_generation` · `spec/chain.json#conventions.checks_when` · `internal/chain/nodes.go` · `internal/specload/specload.go`。

---

## 0. 硬约束（沿用，勿破）

- ★ **工作范围**：只能在 `C:\Users\haoduan\workspace\jx-procurement-platform` 目录内读写。
- ★ **以台账为准**：冲突时一律以 `COLLAB.md` / `spec/` 当前内容为准（**与记忆冲突时以台账为准**）。
- ★ **提交只用显式路径**（**禁止 `git add -A`**）；提交后 `git show --stat HEAD` 复核。
- ★ **每次提交前跑 `bash scripts/check_all.sh`，必绿基线 9/9**（会报项应**零命中**）。
- ★ **还原用 `cp` ＋ `sha256sum -c`**，**不得用 `git checkout -- <file>`**。
- ★ **一次只变异一处**；★ **变异「仍绿」不能判通过**（先确认变异处**是否真被执行**）。
- ★★ **`spec/**` 仍是我方（WorkBuddy）先行域 —— 本批【不需要】改 `spec/**`**（规格已落齐）。★ 若复核认为**规格必须改**（例如 `anti_split_check` 的可达性结论要求新增键），**停在议题里写清方案 ＋ 影响面**，**不要动手改 `spec/**`**。
- ★★ **`docs/**` 双方均可改**（`COLLAB.md §7`：「任一方改动须在该表登记一行」；★ 先例＝你方 2026-10-03 建的 `prefill` 契约小节）。★ **本包不需要动 `docs/**`**（无新路由）⇒ 如无必要**不要动**（共享仓库分寸）。

---

## 1. 背景（为什么有这一包）

★ 你方在 `N-062` 的 `J3` 上**如实停手**并给出三缺口取证 ＋ 约 8 个工作日估计 —— 我方**判为正确处置**（三条都缺「入口／节点」，硬做只能留半成品）。

★★ **我方批 42（`84dc58c` ＋ `cea6640`）已把三条的规格全部补齐**：

| 族 | 规格落点 | 版本 |
|---|---|---|
| `BA#receipt_per_purchase` | `chain.json#conventions.node_task_generation`（新）＋ `routes.purchase_tier1.nodes[5]`（`return_receipt`）**显式 `generates_task: true`** ＋ `task_note` | **V1.9 → V1.10** |
| 两条 `SA#…`（`backfill`） | `chain.json#conventions.checks_when` 的 **`backfill(<section_id>)` 入口最小契约**（`POST /api/approval/{biz_no}/backfill` · 四道约束）＋ `forms/SA.json#sections[id=settlement_backfill]` 补 **`actual_cents`** ＋ 差额字段**计算口径** | `chain.json` **V1.10** · `forms/SA.json` **V1.4** |

★ **本包只交办其中的 `BA` 侧两条**（`T2`/`T3`：节点待办 ＋ `approval` 时点求值）⇒ 二者合起来才能让 **`BA#receipt_per_purchase` 真正生效**。
★★ **`SA` 侧（`backfill` 入口 ＋ 契约行）不在本包**，理由：它**涉及一条新路由**，与 `docs/05-API.md` 契约行**必须同批**（见下「§3 明确不做」），单独立下一包（`MIMO-NEXT-BATCH-27.md`，我方待产出）。

★★★ **本包最重要的一条口径（务必先读懂，否则会做偏）** —— **两个不同的可达性**：

| 问题 | 由谁回答 | 本包落点 |
|---|---|---|
| 该节点**有没有「人的待办」** | `routes.*.nodes[*].generates_task`（显式）／缺省＝`isActionActor` 推导 | `T2` |
| **流程会不会经过该节点**（⇒ 该节点的 `approval(...)` 判据在何处求值） | **通用求值器**（`conventions.checks_when` 第 ① 条） | `T3` |

★ 把两者混为一谈，会**过宽**（把所有「动作型 actor」节点一律判成「判据永不执行」）或**过窄**（把「没人点、流程也不经过」判成正常）。★ 本包的 `T3` 要求你**先交一张「可达时刻表」**把这个区分落到每个节点上。

---

## `T1` · 占位符报错文案的**同族归一**（`N-065`）

★ **来源**：你方在批 41 `N-063` 的 `T2` 回执里**主动上报的同族残留**（我方**受理并成题**）—— ★ 你**没有越界自己改**，做法正确。

**实测的三个位点**（你方已给，我方复核过）：

| # | 位点 | 现状问题 |
|---|---|---|
| ① | `internal/httpapi/handlers_approval.go:1102` | ＝**`defs/sync` 门②**，★ **联调建定义的唯一入口** ⇒ **误导面比导入层更大**：仍指向**不存在的查询路径** |
| ② | `internal/config/importmap.go:196` | `field_id` 段文案（「模板中控件」）—— ★ **`N-063` 的 `T2` 已结论：该段系具名豁免、系统当前不产出** ⇒ 文案不可得 |
| ③ | `internal/config/importmap.go:18` · `cmd/jxapproval/seed.go:46` | **陈旧注释**（随口径变更而过时） |

**你要做的**：

1. ★★ **口径一律沿用 `N-063` `T1` 已立的那句**（你方已落地、我方已验收）—— 同族文案**只有一种正确写法**：
   - 术语上**不得**指向「飞书审批后台」这类**不存在的查询位置**；应说明「**我方自定义**」并给出转向理由（**定义由 API 建、code 由本侧指定**）。
2. ★ ① 与 ② 的**语义不同、不得照抄同一句**：
   - ① 是**建定义入口**的占位符校验 ⇒ 指「我方自定义的 `approval_code`」；
   - ② 是**字段映射段**（`field_id`）⇒ 应说明**本系统当前不产出该映射**（★ 依据＝`N-063` `T2` 的三态结论），★ **不要**再教人去「模板里找控件」。
3. ★ ③ 两条**陈旧注释**一并订正（★ 只改注释，不改行为）。
4. ★ **全仓 `grep` 旧措辞应为 0 命中**；★ 若他处仍有（非 `.go`／生成物／历史文档），**一并列出但不擅自改**（属我方域）。

**可机检用例（至少一条）**：构造占位符载荷打 `defs/sync` ⇒ 断言 **① 响应含新口径关键词** ＋ **② 响应不含旧措辞**（★ 两条断言都要，★ 缺一条就等于只证了一半 —— 这是 `N-063` `M2` 的教训）。

---

## `T2` · 消费 `generates_task` ＋ 打通 `return_receipt` 的 **applicant 待办通路**

**目标（一句话）**：让 `BA#receipt_per_purchase`（`when=approval(return_receipt)`）的**时点真正到来**。

### 2.1 规格（已落，勿改）

- `spec/chain.json#routes.purchase_tier1.nodes[5]`：`{"id":"return_receipt","actor":"applicant","required":true, "generates_task": true, …}`（＋ `task_note`）。
- `spec/chain.json#conventions.node_task_generation`：**显式声明优先于缺省推导**；缺省＝沿用 `internal/chain/nodes.go#isActionActor`（`applicant`／`system`／`purchaser` ⇒ 不生成；其余角色 ⇒ 生成）。

### 2.2 你要做的

1. ★ **`internal/specload/specload.go#NodeDoc` 增字段 `GeneratesTask *bool`**（★ **必须是指针**：`nil` ＝ 未声明、`false` ＝ 显式不生成 —— 用 `bool` 二者不可区分，这正是本键的设计要点）。
2. ★ **`internal/chain/nodes.go:129` 附近的推导改为「显式优先」**：
   - 显式 `true` ⇒ 该节点**必须生成一条待办**（`return_receipt` 的 actor 是 `applicant` ⇒ 生成的任务归 `applicant`）；
   - 显式 `false` ⇒ **不生成**；
   - 键缺失 ⇒ **保持现行推导**（⇒ ★ **既有 9 条流程线行为零变化**）。
3. ★★ **入口必须一起打通**（这是 `conventions.node_task_generation` 已立的**第 ☆ 条边界**：「`generates_task: true` 的节点，其 `actor` 必须有对应的待办入口（前端控制台 ＋ 后端 `approve` 端点对该 actor 放行）—— **否则是「声明了没有入口」**」）：
   - **后端**：`POST /api/approval/{biz_no}/approve`（或等价端点）对该节点 actor＝`applicant` **必须放行** ⇒ ★ **先取证现状**（现是否被角色门拦住？拦在哪一行？）⇒ **修到通**。
   - **前端**：`applicant` 的**待办列表必须能渲染该任务**（你方 `J3` 取证写「**前端从未渲染**」）⇒ ★ **本批要求「最小可用」**（既有待办列表按 actor 过滤即应能显示；若结构上显示不了，做**最小改动**）。★ **完整 UI 精细化不在本批**。
   - ★ 若前端「最小可用」也**工作量不可控** ⇒ **停在议题里给方案 ＋ 估计**，★ **但后端通路必须先打通并验通**（否则仍留半成品）。

### 2.3 验收判据（我方将独立重做）

- ★ **handler 级端到端**（★ 不手搓 `body` 之外的东西、走真 `Deps` ＋ 真路由）：造一张**推进到 `return_receipt`** 的 `BA` 实例 ⇒ 断言 ① `t_flow_task` **出现该节点任务且 actor＝applicant**；② `applicant` 会话**可提交该节点**；③ 提交后流程**推进到下一节点**。
- ★★ **零回归证据**：把 `return_receipt` 的 `generates_task` 声明**从内存副本摘掉**（或用例内构造）⇒ **任务集合回到现状**（★ 证明本键是**唯一**的行为来源、不是顺带改出来的）。
- ★ **单点变异建议**：把「显式优先」改回「一律走 `isActionActor`」⇒ 新用例**必须恰红**；与该变异**无关**的用例（其余 9 条流程线的任务数）**必须保持绿**。

---

## `T3` · `when=approval(<node_id>)` 的**通用求值器** ＋ ★ 先交「可达时刻表」

**目标（一句话）**：让 `approval(<node_id>)` 这个时点**由数据决定谁来求值**，而不是靠逐单据的代码特例；且**声明了却没有求值器**必须是**可见失败**。

### 3.1 实测事实（我方已核，你可复核）

★ 全仓 `when=approval(...)` 的判据**共 7 条**（含重复节点）：

| 判据 | `when` | `severity` | `carried_by_kind`（今） | 该节点的 `actor` | 该节点生成待办？ |
|---|---|---|---|---|---|
| `BA#receipt_per_purchase` | `approval(return_receipt)` | `hard` | `pending_wiring` | `applicant` | ★ **`T2` 使其为是** |
| `BA#anti_split_before_disburse` | `approval(anti_split_check)` | `hard` | `manual` | ★ **`system`** | ★ **否**（同族具名待定） |
| `BA#no_self_purchaser_at_designation` | `approval(supervisor_approval)` | `hard` | `code` | `supervisor` | 是 |
| `BA#cross_dept_designation` | `approval(supervisor_approval)` | `hard` | `manual` | `supervisor` | 是 |
| `SS#tech_opinion_required_at_node2` | `approval(tech_opinion)` | `hard` | `code` | `inspector_group` | 是 |
| `SS#pgm_final_required` | `approval(pgm_final)` | `hard` | `code` | `project_general_manager` | 是 |

★ **你方 `J3` 已实测**：`when=approval(<node>)` 的**通用求值器全仓非测试代码零命中**；唯一先例是 `SS` 的**代码特例**（`internal/flow#applySSNodeFieldsTx`）。
★★ **我方批 42 补登记**：`anti_split_check`（`actor=system`、`required=true`）**同样不生成待办**，而 `BA#anti_split_before_disburse`（`hard`）挂在它的时点上 ⇒ **与 `return_receipt` 是同一族的「时点不可达」**。★ 我方**刻意没有**为它声明 `generates_task: true`（**系统环节不该有人的待办入口**，声明 `true` 会要求给 `system` 造控制台入口 ⇒ **错的修法**）。

### 3.2 交付物 1（★ **必须先交**）：**可达时刻表**

逐节点（上表 6 行 ＋ 你复核后认为应补的）给出结论，**每行必须有可实现结论**：

| 节点 | 生成待办？ | `approval(<node>)` 判据在**何处**求值 | 依据（`文件:行号`） |
|---|---|---|---|

★ 最后一列**必须是「实测」而不是「应该」**。★★ 若某行的结论是「**无处执行**」⇒ ★ **停在议题里点名**，**不要**自行把它改成 `manual` / `accepted_gap`（那是 `spec` 声明，属我方域）。

### 3.3 交付物 2：通用求值器

- 按 `<node_id>` 查表：从 `spec/forms/*.json#checks` 取 `when == "approval(<本节点 id>)"` 的判据并求值。
- ★ **不得再为某个单据写新的代码特例**（现状 `SS` 的特例**可以先共存**，但须如实说明「本批未迁移」；★ 若迁移成本可控，**鼓励迁移**并说明）。
- ★ **`severity=hard` 的这类判据在其节点被执行时必须被求值**；★★ **「声明了却没有求值器」（规则/原语未注册、节点 id 认不出）⇒ 可见失败**，★ **不得**沿用现状「非提交时点一律 `continue` 跳过」（★ 那等于判据 `else` 的承诺**永不发生**）。

### 3.4 验收判据（我方将独立重做）

- ★ **端到端拦/放双向**：`BA#receipt_per_purchase` —— 未回交凭据 ⇒ **拦**（非 200 且点名判据）；已回交 ⇒ **放**。
- ★ **零回归**：`SS#tech_opinion_required_at_node2` / `SS#pgm_final_required` / `PC×ledger_submit` 的既有节点字段行为**不得回归**（既有用例应保持绿）。
- ★ **单点变异建议**：停用通用求值器（改为恒不匹配）⇒ 新用例**必须恰红**；★ 与该变异**无关**的用例**必须保持绿**。
- ★ 若 `anti_split_check` 一行结论为「当前无处执行」⇒ ★ **在回执里明说**，并在**判据侧不动任何 `carried_by_kind`**，由我方裁定（★ 可能另开议题）。

---

## 2. 明确**不做**（★ 重要）

| 项 | 原因 |
|---|---|
| `SA` 侧 `backfill` 入口（`POST /api/approval/{biz_no}/backfill`） | ★ **不在本包** ⇒ 单独立 [`MIMO-NEXT-BATCH-27.md`](./MIMO-NEXT-BATCH-27.md)（我方待产出）。★ 理由：**涉及新路由**，而 `docs/05-API.md` 契约行 ↔ `spec/openapi.json` ↔ `router.go` 的**路由集必须逐字一致**（会报项 `C9` ＋ 判据 `S21`–`S23`）⇒ ★ **先写文档 ＝ 当场造出「文档声明了、路由不存在」的红线**（本仓头号红线）⇒ **必须同批落**，该包会一并写清四道约束与同批清单 |
| 改 `spec/**` | ★ **我方先行域**；本批规格已落齐 ⇒ 只消费、不改写 |
| 改 `docs/**` | ★ 本包**无新路由** ⇒ 无必要（★ `docs/` 虽双方可改，但须在 `§7` 登记） |
| `N-054 ②`（`SA#「需要开票」`字段） | ★ 待外部输入 |
| UI 精细化（`return_receipt` 的展示美化、`SA` 详情页补录区） | ★ 本批只做「**最小可用 ＋ 通路打通**」 |

---

## 4. 交付要求

1. 每完成一族跑 `bash scripts/check_all.sh`，**必绿 9/9 ＋ 会报零命中**。
2. ★ `T1`：**全仓 `grep` 旧措辞 ＝ 0 命中** ＋ 至少一条**双断言**用例（含新口径 ∧ 不含旧措辞）。
3. ★ `T2`：`t_flow_task` 断言 ＋ `applicant` 放行证据（**`文件:行号` ＋ 端点 ＋ 实测响应**）＋ **零回归证据**。
4. ★ `T3`：★★ **先交「可达时刻表」**（每行带 `文件:行号`）＋ 求值器落点 ＋ **fail-closed 的可见失败证据**（★ 造一个「未注册」场景，证明它是**可见失败**而不是静默跳过）。
5. 提交**只用显式路径**；完成后在 `COLLAB.md#N-065` 与 `COLLAB.md#N-062` **两段各追加一条逐条回执**（含证据 ＋ 单点变异结果 ＋ 未做原因），★ **两段都要写 `MIMO-DONE`**（★ 驱动脚本按议题段查找该标记 ⇒ **缺一段会被判成没跑完而续跑**）。
6. 推送 `origin/main`。
7. ★ 需要我方裁定才能定的口径（**尤其 `T3` 的「无处执行」结论**），**停在议题里写清楚，不要猜**。

---

## 5. 单点变异要求（我方验收时会**独立重做**，★ 不采信自报）

★ 至少两处**单点变异**（`T2` 一处、`T3` 一处）并把**红/绿实测原文**写进回执：

- `T2` 建议：把「**显式 `generates_task` 优先**」改回「一律走 `isActionActor`」⇒ 你的新用例**必须恰红**。
- `T3` 建议：把通用求值器改为**恒不匹配**（等价于现状）⇒ 你的新用例**必须恰红**。
- ★ 两处都要**同时报告**「与该变异无关的用例保持绿」（**隔离性**）。
- ★ 还原一律 `cp` ＋ `sha256sum -c`（**不得** `git checkout --`）。
