# MIMO-NEXT-BATCH-29 —— `N-067` ②③（**`return_receipt` 凭据录入 · UI 段**）＋ `N-069`（**节点录入字段对 BA 未落库** ＋ **审批时点无附件绑定通道**）

> **交付方**：WorkBuddy（制度规范与产品设计方）
> **执行方**：mimo code
> **日期**：2026-10-06（**批 49 · 本包为「我方待产出」的下一包 · ★ 本轮不派工**）
> **前置**：`N-067` ①（跨节点钩子 · `MIMO-NEXT-BATCH-28` `T1`）已于批 48 由 mimo `fb11685` 落地并经我方独立验收通过；`N-068` 已结案 `AGREED`；★ 规格侧已由**本轮（批 49 · B 档）**落齐 —— `spec/chain.json` **V1.14**（新增 **`conventions.node_record_fields`** ＋ `record_fields` 两处**据实订正**）· `spec/forms/BA.json` **V1.3** · `spec/RESOLUTIONS.md` **V1.9（36 条裁定 · 新增 `R-36`）** · `spec/README.md` **V1.34** · 常驻探针 `scripts/_probe_n067.py`（**23 → 48**）。
> **★ 动手前必读**：
> · **`spec/chain.json#conventions.node_record_fields` —— 本包 `T1`／`T2` 的**唯一权威契约来源**（★ **逐字**，本包只是转述；冲突时**以 `spec` 现文为准**）
> · `spec/RESOLUTIONS.md#R-36`（**界面归属裁定** ＋ 三处缺口裁定 —— ★ 本包的存在理由）
> · `COLLAB.md#N-067`（议题正文 ＋ ① 规格段 ＋ ① 实现段验收）· `COLLAB.md#N-069`（两处缺口写全）
> · `internal/flow/service.go:470-548`（**`act` 全文** —— ★★ ⑥⑦ 两个 `if opType == OpApprove` 分支**是 approve 时点 `fields` 的唯二消费方**，**本包 `T1` 要在这里补第三个**）
> · `internal/flow/designation.go:125-205`（`nodeFieldSpecFor` 表＋`applyNodeFieldMapTx` —— ★ **现有规则形状只支持单一 `RequiredKey`**，BA 需要**两个** ⇒ 见 `T1 §1.3` 的**形状抉择**）
> · `internal/httpapi/handlers_approval_approvalchecks.go:36-67`（**判据注册表 ＋ 内层引擎** —— ★ `checkReceiptPerPurchase` **已在**校验这两个字段，本包**不改判据**，只让「校验过的字段真的落库」）
> · `web/src/views/ApprovalConsole.vue`（**464 行全文** —— ★ 现 `doApprove` 只送 `{task_id, opinion}`）· `web/src/views/MyTasks.vue`（**界面归属裁定的核心证据**）

---

## 0. 硬约束（沿用，勿破）

- ★ **工作范围**：只能在 `C:\Users\haoduan\workspace\jx-procurement-platform` 目录内读写。
- ★ **以台账为准**：冲突时一律以 `COLLAB.md` / `spec/` **当前内容**为准（**与记忆冲突时以台账为准**）。
- ★ **提交只用显式路径**（**禁止 `git add -A`**）；提交后 `git show --stat HEAD` 复核。
- ★ **每次提交前跑 `bash scripts/check_all.sh`，必绿基线 9/9**（会报项应**零命中**）。
- ★ **还原用 `cp` ＋ `sha256sum -c`**，**不得用 `git checkout -- <file>`**。
- ★ **一次只变异一处**；★ **变异「仍绿」不能判通过**（先确认变异处**是否真被执行**）。
- ★★ **`spec/**` 本包一律不动** —— 规格**已由批 49 落齐**（`chain.json` V1.14 / `BA.json` V1.3 / `RESOLUTIONS.md` V1.9 / `README.md` V1.34）⇒ ★ **只消费、不改写**。
- ★★ **本包【新增】一个路由**（见 `T1 §1.4`：`attachment_ids` **不新增端点**，但若你方判断须新增审批时点附件端点 ⇒ 则**必须**同批改 `docs/05-API.md` ＋ **由脚本重生** `spec/openapi.json` ＋ 生成器计数 ＋ 探针期望 ＋ `router_test.go` —— ★ **缺一即红**，理由 ＝ 路由集三方（正本 ↔ `openapi.json` ↔ `router.go`）**逐字一致**（`C9` ＋ `S21`–`S23`））。★★ **我方倾向：不新增端点**（详见 `T1 §1.4`）⇒ 若采纳倾向案，则**上述五处一律不动**，★ **在回执里写明该判断**。
- ★★ **`docs/**` 双方均可改**（`COLLAB.md §7`）：★ 本包**必须**改 `docs/05-API.md`（在 `approve`/`reject` 的请求体小节**补写可选 `attachment_ids`**）—— ★ 改完**在该表登记一行** ＋ ★ **若因此触发路由/契约面变化**，按上一条处置。
- ★★ **`T1` 是一条「含形状抉择、可据理停手」的任务** —— ★ 见 `T1 §1.3` 的**形状抉择**与 `T1 §1.5` 的**停手条款**：**不要**为了「把包做完」而**写第二份真相**（在 `internal/flow` 里另抄一份字段名表而**不与 spec 互锁**）—— ★ ★ **宁可停在议题里写清楚，也不要造一条看不见的绿**。

---

## 1. 背景（为什么有这一包）

★★ 本包治的是**一条断链** —— 「**判据会校验、但校验完就丢**」：

| # | 事实 | 后果 |
|---|---|---|
| ① | `BA#receipt_per_purchase`（`severity=hard`）**已在** `approvalCheckFns` 注册（`handlers_approval_approvalchecks.go:37/93`），**同意 `return_receipt` 时**校验 `payment_receipt_no` ＋ `payment_receipt_file` 非空 | ★ **校验真的跑了** |
| ② | ★★★ 而 `internal/flow/service.go#act` 里，approve 时点的 `fields` **只被两个分支消费**：⑥ `applyDesignationTx`（**仅 PR**）· ⑦ `applySSNodeFieldsTx`（**仅 SS/PC**）—— `nodeFieldSpecFor("BA","return_receipt")` **返回 `nil`** ⇒ **no-op** | ★★ **校验通过 ⇒ 字段被丢弃**（验后即弃） |
| ③ | ★★★ 审批时点的请求体（`approvalActionBody`，`handlers_approval.go:300-309`）**只有 `fields`、没有 `attachment_ids`** | ★★ **`payment_receipt_file` 无绑定通道**（`BindStagingTx` 只在**提交**路径被调用，`service.go:313`） |
| ④ | ★★ `NodeDoc.RecordFields` **已被 `specload` 解析**（`internal/specload/specload.go:195`），而**全仓零消费方**（机械实测：`grep -rn RecordFields` **仅此 1 处**） | ★ 前端**无从知道**该录哪些字段 |

★★★ **为什么这四处必须【同批】落地**：任缺其一，`BA` 全链在 UI 上都**走不到终态** —— 缺 ① 无人拦（现状已补）；缺 ② **录了等于没录**（数据不落，审计面空白）；缺 ③ **附件 id 悬空**；缺 ④ **前端不知道要录什么** ⇒ ★ **不得**把 ②③④ 拆成三批分别声称「已生效」。

★★ **当前「绿」为何掩盖了它**：既有 e2e（`BA#receipt_per_purchase` 一族）**只断言「任务被释放 ＋ 实例到终态」**，★ **从不断言落库**，且附件夹用的是**假串** `att://receipt-1` ⇒ ★★ **门禁全绿而证据链是空的**。★★ 这正是本仓三令五申的形态：**绿是底线、不是结论**。

★ **`N-067` ②③ 与 `N-069` 的关系（★ 不合并、互为前置）**：
- `N-067` ②③ ＝ **UI 能力段**（谁在哪个界面录、怎么录）；
- `N-069` ＝ **机制段**（录进去的东西往哪落、附件怎么绑）；
- ⇒ ★★ **两者是同一件事的两半**：只做 UI ⇒ 录了不落库（**假生效**）；只做机制 ⇒ 没有录入入口（**死代码**）⇒ ★ **必须同包**。

---

## `T1` · 后端 —— `N-069` 两处机制缺口 ＋ `record_fields` 暴露

### 1.1 契约（我方已定 · 逐字以 `spec/chain.json#conventions.node_record_fields` 为准）

★ **语义**（照契约）：`record_fields` ＝ **该节点由【人】在办理时录入的字段名清单** —— ★ 取值**必须**是 `forms/<doc>.json#sections[*].fields[*].name`（**机读名**），**绝不允许**写中文标签；★ 且所列字段在该表单里**必须 `source == "user"`**。

★ **承重不变量**（照契约）：对任意 `when == "approval(<node_id>)"` ∧ `severity == "hard"` ∧ `carried_by_kind != "manual"` 的判据，其 `assert` 里**点名的字段名 ⊆ 该节点的 `record_fields`**。★ 理由：**判据要的字段，必须有人能录**。

★ **持久化**（照契约）：节点动作**写 `ext_json`**，且**只写已声明的键**（`record_fields` 之内的键；**不得**把客户端多送的键一并落库 —— ★ 那是**越权写**）。

★ **本包 `T1` 的落点（三件，缺一即「悬空」）**：

| # | 落点 | 现状 → 目标 |
|---|---|---|
| `T1-a` | **approve 时点字段落库**（BA × `return_receipt`） | `nodeFieldSpecFor("BA","return_receipt")` = `nil`（no-op）⇒ ★ 在 `act` 的 `if opType == OpApprove` 块内**落 `record_fields` 声明的键**到 `inst.ext_json`（**同一事务**） |
| `T1-b` | **审批时点附件绑定通道** | `approvalActionBody` **无** `attachment_ids` ⇒ ★ 新增**可选** `attachment_ids`，在 **approve 事务内**逐个 `BindStagingTx`（★ **任一失败 ⇒ 整体回滚**，与提交路径同款） |
| `T1-c` | **`record_fields` 暴露** | 全仓**零消费方** ⇒ ★ 在**任务列表/实例详情**（即前端实际读的那个响应）里带上当前节点的 `record_fields`，供 `T2` 渲染 |

### 1.2 ★★ 先取证（**落笔前必须做**，回执里给 `文件:行号`）

1. ★★★ **`T1-c` 的响应面**：前端 `ApprovalConsole.vue` 取详情的**那一个** API 是哪个（`fetchApprovalDetail`？`GET /api/approval/{biz_no}`？）⇒ ★ **给出 `web/src/api` 的调用点 ＋ 后端 handler ＋ 响应结构体 `文件:行号`**；★ 并明确：**新增 `record_fields` 是加在【任务行】还是【实例详情】** —— ★ 判据＝**前端渲染控件时手上有什么**（★ 若详情接口已返回当前 `task`，加在 `task` 上最省；★ 若只有一个 `node_id`，则须一并给出 `record_fields` 与**字段定义元数据**（中文名/控件类型/是否必填）—— ★ **元数据从哪来**要说清楚，**不得**让前端硬编码中文名）。
2. ★★ **`BindStagingTx` 的**准入语义****（`internal/store/repo_staging.go:73`）：它按 `ownerOpenID` ＋ `instanceCode` ＋ `bizNo` 校验什么？★ **`return_receipt` 的办理人是【申请人本人】** ⇒ ★ **用申请人 open_id 作为 `ownerOpenID` 是否与提交期一致**？（提交期该参数是 `in.ApplicantOpenID`，见 `service.go:313`）⇒ ★ **给出结论 ＋ `文件:行号`**。
3. ★★ **`ext_json` 的写入口径**：`act` 里现成的 **`inst.ExtJSON` 读改写**先例＝`applyNodeFieldMapTx`（`designation.go:187-205`）⇒ ★ **复核它是否为「键级合并」（保留未提及的既有键）** —— ★ **必须复用同一口径**，**不得**整份覆盖（★ 那会抹掉提交期落的 `payment_method_input` 等键 ⇒ **回归**）。
4. ★ **`approvalActionBody` 的兼容性**：★ 新增可选字段**是否**会破坏既有调用方（飞书回调走 `flow` 层**不经过**此结构体 —— ★ **复核**：`internal/flow/callback.go` 的入口是否**构造 `fields`**、**Yes/No 都给出 `文件:行号`**）；★ 并**复核**：`Fields` 为 `nil` 时**新分支必须豁免**（与 ⑥⑦「非结构化通道豁免」**同款** —— ★ **飞书两键只有意见、没有结构化字段**，若在 `nil` 时强制必填 ⇒ **把回调打成 400**，属**严重回归**）。

### 1.3 ★★ 形状抉择（**本包唯一一处需要你方判断的地方 —— 请据理择一并写进回执**）

★★ **背景（实测事实）**：`internal/flow` **不 import `internal/specload`**（生产代码内零引用；仅 `designation_anchor_test.go:17-29` 在**测试**里 `specload.Load`）⇒ ★★ **`act` 里拿不到 `spec`**。而 `record_fields` 的**唯一真源是 `spec`**。

⇒ ★★ **两条路线，请择一**（★ 两者**都必须**满足「**不与 spec 形成第二份真相**」这条底线）：

| 路线 | 做法 | 代价 / 风险 |
|---|---|---|
| **ⓐ 表驱动 ＋ 交叉钉**（★ **我方倾向**） | 沿用 `designation.go#nodeFieldSpecFor` 的**表驱动先例**：为该表**加一行** `BA × return_receipt → [payment_receipt_no, payment_receipt_file]`，★ **同时**按**既有先例**（`internal/flow/designation_anchor_test.go` 读 `specload.Load`）**加一条锚定测试**：断言**该表与 `spec#record_fields` 逐字一致**（★ spec 改而表未改 ⇒ **当场红**） | ★ 表形状需**扩**（现 `nodeFieldRule` **只支持单一 `RequiredKey`**，BA 要**两个** ⇒ ★ 需扩为**键列表**，★ **不得**为 BA 单开一个旁路函数 —— 那是第二套形状） |
| **ⓑ 由 HTTP 层注入** | 在 `approveReject`（**已持有 `d.Spec`**）把该节点的 `record_fields` **作为参数**传给 `Flow.Approve`（★ `act` 已有 `fields` 参数位） | ★ **须改 `flow.Service` 的方法签名**（`Approve`/`act`/`…`）⇒ ★ **波及面更大**、且**回调路径**（飞书）也走 `act` ⇒ ★ 须确认**注入面覆盖全部调用方**，**不得**漏一条（漏了 ＝ 该路径静默不落库） |

★ **共同底线（★ 两条路线都不许破）**：
- ★★ **不得**在 `internal/flow` 里**硬编码中文标签**、**不得**写一份**独立于 spec 的字段清单**而不加锚定测试；
- ★★ **不得**顺手把 `spec` 的读取引入 `internal/flow` 的生产代码（★ 依赖倒置：**规格装载属上层**）—— 若走 ⓑ，注入的**必须是值**（`[]string`），**不是** `specload` 类型。

### 1.4 契约细节（`T1-b` 附件通道）

★ **形态**：`approvalActionBody` **新增可选** `attachment_ids []string`（`json:"attachment_ids"`）。
★★ **端点结论（我方倾向：不新增端点）**：`POST /api/approval/attachments`（**暂存上传**，`router.go:157`）＋ `GET /api/approval/attachments/:file_id`（`router.go:158`）**已存在** ⇒ ★ **复用即可**：前端**先上传拿 `file_id`**，再把 `file_id` 数组放进 `attachment_ids` ⇒ ★ **无需新端点** ⇒ **不触发路由集三处一致链**。
★★ **若你方判断须新增端点**（例如「审批时点专用上传」）⇒ ★ **必须同批**改 `docs/05-API.md` ＋ **脚本重生** `spec/openapi.json` ＋ 生成器计数 ＋ 探针期望 ＋ `router_test.go`（见 §0 倒数第三条）。
★ **事务语义**：与提交路径**同款** —— ★ **任一 `file_id` 不可绑定 ⇒ 整体回滚**（任务不推进、`ext` 不落、附件不绑）。
★ **失败要可见**：★ 不可绑定 ⇒ **`400` ＋ 点明 `file_id`**（**不得**静默跳过该附件）。

### 1.5 ★★ 停手条款（硬）

★ 若 `§1.2` 第 1 项**取不到**「前端渲染控件所需的最小元数据」（例如：接口只给 `node_id`、而**字段的中文名/控件类型无任何机读来源**）⇒ ★ **停手**，在 `COLLAB.md#N-069` 写清「**缺什么 · 为何 · 可选方案**」，**不要**：① 让前端硬编码中文名；② 为此新增持久化；③ 自行发明字段元数据格式。
★ 若 `§1.3` 两条路线**你方判断都有硬阻碍** ⇒ ★ **停手登记**，**不要**为了交付而降级为「不落库、只回显」。

---

## `T2` · 前端 —— `N-067` ②③ 的 **UI 段**（★ 归属已裁定：**复用审批控制台**）

### 2.1 ★★ 界面归属裁定（我方已定 · `R-36` · **不再讨论**）

★★★ **裁定 ＝ ☆ 复用审批控制台（`web/src/views/ApprovalConsole.vue`）**，**不**新建「申请人专用录入页」。**三条独立证据**（批 49 实测）：

| # | 证据 | 文件:行号 |
|---|---|---|
| ① | ★ `MyTasks.vue` 的数据源是 `GET /api/approval/tasks` ＝ `t_flow_task` 中 **`RELEASED ∧ PENDING ∧ assignee=me`** —— ★★ **按「指派给我」而非「我是审批人角色」筛选** ⇒ ★ `return_receipt`（`actor=applicant`）的任务**本就出现在申请人本人的「我的待办」里**；点「办理」即进控制台 | `web/src/views/MyTasks.vue:4,67,79` |
| ② | ★★ 控制台的可办理判据是 **`actionable = PENDING ∧ isMine`**（`isMine` 比对当前会话身份）⇒ ★★ **「审批人替申请人填凭据」的角色错位【结构上不可能发生】**（非本人拿不到可办理态，且后端 `act` 另有**本人硬校验** `task.AssigneeOpenID != actor` ⇒ `ErrNotAssignee`） | `web/src/views/ApprovalConsole.vue:71,76-78,324` · `internal/flow/service.go:497-500` |
| ③ | ★ 若另建申请人专用页 ⇒ ★ **同一份「任务动作契约」被实现两次**（`approveTask` 载荷、`actionable` 判据、鉴权口径、错误文案）⇒ ★★ 即本仓头号反模式「**第二份真相**」 | —— |

⇒ ★★ **结论**：`T2` 在**既有控制台**上做**增量**：按 `T1-c` 暴露的 `record_fields` **渲染录入控件**，**仅在** `record_fields` 非空时出现。★ **控制台对「无 `record_fields` 的节点」行为【零变化】**（★ 这是**必须守住**的回归边界）。

### 2.2 要做的三件

1. ★★ **按 `record_fields` 渲染控件**：文本/数值类 ⇒ 输入框；附件类 ⇒ 走**既有暂存上传通道**（`T1 §1.4`）拿 `file_id`。
   - ★ **控件形态从哪来**：以 `T1 §1.2` 第 1 项**取证结论**为准 —— ★ **不得**在 `.vue` 里硬编码字段名或中文标签（★ 那会让「spec 改字段名 ⇒ 前端静默错位」）。
2. ★★ **提交载荷**：`doApprove`（`ApprovalConsole.vue:207-210`）在 `record_fields` 非空时**一并送 `fields`**（＋ 附件时 `attachment_ids`）；★ **为空时载荷逐字不变**（★ 既有调用方零影响）。
3. ★★ **必填提示的前端定位**：★ 前端**只做体验**（缺项时**提示**），**权威拦截在后端**（`checkReceiptPerPurchase` ＋ `T1-a` 落库失败）⇒ ★★ **不得**把前端提示当作判据的承载者（★ 前端可绕，判据不可绕）。

### 2.3 ★ 边界与**禁止**

| # | 边界 | 说明 |
|---|---|---|
| ☆1 | ★ **不新建页面、不新建路由** | 归属已裁定（`§2.1`） |
| ☆2 | ★ **不改 `actionable` 判据** | `PENDING ∧ isMine` 是**正确处置**（证据②）；★ 放宽它才是错的 |
| ☆3 | ★ **不硬编码字段名 / 中文标签** | 见 `§2.2` 第 1 条 |
| ☆4 | ★ **无 `record_fields` 的节点：控件区不渲染、载荷逐字不变** | ★ 回归边界 |
| ☆5 | ★ **不动台账写入口**（`Ledger.vue#editOps`） | ★ 见 `§4` 的边界裁定 |

---

## `T3` · 测试与单点变异（★ 我方将**独立重做**）

### 3.1 用例（★ 必须能**先红后绿**，或说明为何不能）

| # | 场景 | 期望 |
|---|---|---|
| ① | ★★ **端到端（走真 spec）**：BA 提交 ⇒ 推进到 `return_receipt` ⇒ ★ **带 `fields` ＋ `attachment_ids`** 同意 ⇒ ★ **终态** ＋ ★★ **落库断言**（`inst.ext_json` 里**确有** `payment_receipt_no` ＋ `payment_receipt_file`）＋ **附件已绑**（`t_attachment` 行存在且 `instance_code` 正确） | ★ 绿 |
| ② | ★ **反证 / 回归**：**不带** `fields` 同意 ⇒ ★ 判据**必须拦**（`400`，`BA#receipt_per_purchase`）—— ★ 证「判据仍在承重」 | ★ 绿（拦下） |
| ③ | ★★ **只写声明键**：客户端**多送**一个未声明键 ⇒ ★ **不得**落进 `ext_json`（★ 越权写） | ★ 绿 |
| ④ | ★★ **附件不可绑定** ⇒ **整体回滚**：任务**仍 `PENDING`**、`ext_json` **未变**、无半程副作用 | ★ 绿 |
| ⑤ | ★ **`nil` 豁免（回归）**：`fields == nil`（飞书回调形态）⇒ ★ **不得**因新分支而 400 | ★ 绿 |
| ⑥ | ★ **前端回归**：`record_fields` 为空 / 未知节点 ⇒ 控制台**控件区不渲染**、`doApprove` 载荷**逐字不变** | ★ 绿 |

★ **另（★ 本包最容易漏的一条）**：★ **必须**有一条用例证明「**`record_fields` 暴露面确实被消费**」—— 即接口响应里**确有**该字段（★ 否则 `T1-c` 会变成**又一个零消费方的死键**，与它要修的 ④ 号事实**同病**）。

### 3.2 验收判据

- ★★ **门禁**：`bash scripts/check_all.sh` **必绿 9/9 ＋ 会报零命中**。
- ★★ **单点变异（至少三处，红/绿实测原文写进回执）**：
  - **M1** ★ **摘掉 `T1-a` 落库**（`act` 里的新分支置空）⇒ ★ 用例 ① 的**落库断言恰红**；★ **用例 ②③④⑤ 保持绿**（**隔离性**）。
  - **M2** ★ **摘掉 `T1-b` 附件绑定** ⇒ ★ 用例 ①（附件部分）／④ **恰红**；★ 其余保持绿。
  - **M3** ★ **摘掉锚定测试的表↔spec 互锁**（若走 `§1.3` 路线 ⓐ：改表里一个字段名，锚定测试须**恰红**）⇒ ★ **证明互锁在承重、不是装饰**。
  - ★ **还原一律 `cp` ＋ `sha256sum -c`**（**不得** `git checkout --`）。
- ★★ **合成/真实 spec 的鉴别力**：★ 本包**在真实 spec 上即有声**（`BA#receipt_per_purchase` 是**真实运行**的判据，与 `N-067` ① 的「零行为变化」**不同**）⇒ ★ **不需要**合成 spec，★ **但**用例 ① 的**落库断言**必须是**真断言**（★ **不得**只断言 `200`／终态 —— ★ 那正是**本包要治的那个盲区**）。

---

## 2. 明确**不做**（★ 重要）

| 项 | 原因 |
|---|---|
| 改 `spec/**`（`chain.json` / `forms/*.json` / `checks.json` / `README.md` / `openapi.json`） | ★ **我方先行域，且规格已由批 49 落齐** ⇒ **只消费、不改写**（★ `openapi.json` 的唯一例外见 §0） |
| 改 `checkReceiptPerPurchase` 判据逻辑 | ★ **判据已正确**（`handlers_approval_approvalchecks.go:93-106`）—— ★ 本包治的是「**校验完就丢**」，**不是**判据本身 |
| 改任何判据的 `carried_by_kind` / `severity` | ★ **我方域**（`spec` 声明） |
| ★★ **改台账写入口或 `L01.付款凭据号` 的权属** | ★★ **见 §4 边界裁定** —— 台账登记（`L01` ops）与节点留存是**两个不同的动作** |
| 新建「申请人专用录入页」 | ★ **归属已裁定**（`§2.1`） |
| 把前端必填提示当作判据承载 | ★ 前端可绕（`§2.2` 第 3 条） |
| 计算 `actual_vs_approved_diff_cents` 等既有缺口 | ★ 不属本包 |
| 新增持久化（新表/新列/节点快照） | ★ 复用 `ext_json` ＋ 既有 `t_attachment` 即可 |

---

## 3. 交付要求

1. 每完成一族跑 `bash scripts/check_all.sh`，**必绿 9/9 ＋ 会报零命中**。
2. ★ `T1`：`§1.2` **四项取证的 `文件:行号` 结论放在回执最前面**；★ `§1.3` **形状抉择逐条说明理由**；★ 若触发 `§1.5` **停手条款** ⇒ **停手**、在 `COLLAB.md#N-069` 写清（**不要**硬做）。
3. ★ `T1`：`§1.4` 的**端点结论**（新增 / 不新增）**明确写一句**；★ 若新增 ⇒ 五处同批全带。
4. ★ `T2`：`§2.2` 三件逐件给改动证据（`文件:行号` ＋ 用例红/绿原文）。
5. ★ `T3`：**M1／M2／M3** 各给红/绿实测原文 ＋ **隔离性**（无关用例仍绿）＋ `cp`/`sha256sum -c` 还原输出。
6. 提交**只用显式路径**；完成后在 `COLLAB.md#N-067` 与 `#N-069` **各追加一条 `MIMO-DONE` 逐条回执**。
7. 推送 `origin/main`。
8. ★ 需要我方裁定才能定的口径，**停在议题里写清楚，不要猜**。

---

## 4. ★★ 台账边界裁定（`R-36` 已定 · **不得越界**）

★★ 本包**只负责**「**节点留存**」（节点办理时把凭据原始信息落 `ext_json` ＋ 附件绑定）。
★★ **不负责、也不得改动**「**台账登记**」：`L01.付款凭据号` 的 `ops` 写入方**仍是综合运营主管**（`spec/ledger-mapping.json` ＋ `seed.go#opsSupervisor`），这是 `Q14` 已定口径。

★ **两个动作的具名区别（写进回执，避免后人误合并）**：

| 动作 | 承载 | 写入方 | 语义 |
|---|---|---|---|
| **节点留存**（本包） | `t_instance.ext_json` ＋ `t_attachment` | ★ **办理该节点的本人**（`return_receipt` ＝ 申请人） | 凭据**原件留档**（审批时点的第一手证据） |
| **台账登记**（既有 · 不动） | `t_ledger_ops.ops_json`（`L01`） | ★ **综合运营主管** | 台账面**登记/更正** |

⇒ ★★ **不得**因本包而**放宽或收窄**任何一方的权属；★★ **不得**把节点留存当作台账登记的**替代**。

---

## 5. 同批清单（★ 自查用，缺一即红/缺一即「悬空」）

- [ ] `T1` `§1.2` 取证四项（★ 含 `T1-c` 响应面 ＋ 元数据来源）
- [ ] `T1` `§1.3` 形状抉择（ⓐ / ⓑ 择一并说明理由；★ **不与 spec 形成第二份真相**）
- [ ] `T1-a` approve 时点落库（★ **只写声明键** · 键级合并 · 同一事务）
- [ ] `T1-b` `attachment_ids` 可选字段 ＋ 事务内 `BindStagingTx` ＋ 失败可见（`400` 点名）
- [ ] `T1-c` `record_fields` 暴露（★ **须有消费方**，否则又是死键）
- [ ] `T1` `nil` 豁免（飞书回调形态**不得**被打成 400）
- [ ] `T1` 端点结论写明（不新增 ⇒ 五处不动；新增 ⇒ `docs/05-API` ＋ `openapi` 脚本重生 ＋ 计数 ＋ 探针 ＋ `router_test.go`）
- [ ] `T2` 控制台按 `record_fields` 渲染（★ **零硬编码**）
- [ ] `T2` 载荷：非空送 `fields`/`attachment_ids`；**空则逐字不变**
- [ ] `T3` 用例 ①–⑥（★ ① 的**落库/附件断言必须真**）＋ `record_fields` 被消费的用例
- [ ] **单点变异 M1／M2／M3** ＋ 隔离性 ＋ `cp`/`sha256sum -c` 还原
- [ ] `docs/05-API.md` 同批（★ 在 `COLLAB.md §7` **登记一行**）
- [ ] `bash scripts/check_all.sh` **必绿 9/9 ＋ 会报零命中**
- [ ] `COLLAB.md#N-067` / `#N-069` 各追加 `MIMO-DONE` 回执
- [ ] 显式路径提交 ＋ 推送 `origin/main`

---

## 6. 文书与时间口径（★ 沿用本仓既有纪律，勿破）

- ★ **时间一律写绝对日期**（`YYYY-MM-DD`；需要时到分钟）—— ★ **不得**使用「今天」「昨天」「上周」「本批刚」这类**相对时间词**（★ 台账与文档要能被**日后独立复核**）。
- ★ **所有文字用简体中文**（含 `zh-rHK` / `zh-rTW` 语境一律简体）。
- ★ **台账时间戳取真值**：需要提交时间时一律 `git log --format=%ci` 读，**不得**凭估计顺推。
- ★ **`COLLAB.md` 的字段名逐字固定**：`- **背景**：` / `- **建议方案**：` 等须**逐字**且**内容同在一行**（换行会被判「字段为空」）；`- **类型**：` 只允许既有 5 个枚举值（**加括注即非法**）。
- ★ **Markdown 表格里的「竖线」一律转义为 `\|`** —— ★★ **反引号不保护竖线**（本仓头号陷阱；`COLLAB.md` / `MIMO-*.md` 已在表格门禁扫描面内）。
