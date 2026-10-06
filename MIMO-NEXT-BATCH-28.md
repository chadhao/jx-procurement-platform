# MIMO-NEXT-BATCH-28 —— `N-067` ①（**无待办节点上的 `approval(<node_id>)` 跨节点钩子**）＋ `N-068`（**联调查出的三处轻量不一致**）

> **交付方**：WorkBuddy（制度规范与产品设计方）
> **执行方**：mimo code
> **日期**：2026-10-06（**批 47 · 本包为「我方待产出」的下一包 · ★ 本轮不派工**）
> **前置**：`N-066` / `N-062` 均已结案 `AGREED`；★ 规格侧已由**本轮（批 47 · B 档）**落齐 —— `spec/chain.json` **V1.13**（`conventions.checks_when` 末段新增「**第三处承载口径**」）· `spec/README.md` **V1.33** · 常驻探针 `scripts/_probe_n067.py`（**23/23**）。
> **★ 动手前必读**：
> · **`spec/chain.json#conventions.checks_when` 末段「★★ 第三处承载口径」（V1.13）—— 本包 `T1` 的唯一权威契约来源**（★ **逐字**，本包只是转述；冲突时**以 `spec` 现文为准**）
> · `COLLAB.md#N-067`（议题正文：两段处置 ＋ 我方规格落地段）· `COLLAB.md#N-068`（三处写全）
> · `internal/httpapi/handlers_approval_approvalchecks.go`（**第 ① 条**的通用求值器 —— 本包 `T1` 要**复用**它，**不是**另造一套）
> · `internal/httpapi/handlers_approval_preview.go:96-109` ＋ `internal/httpapi/handlers_approval.go:599-614`（**组装 `Facts` 调 `Compute` 的两处先例** —— ★ 注意两者的 `Facts` **均取自请求体**）

---

## 0. 硬约束（沿用，勿破）

- ★ **工作范围**：只能在 `C:\Users\haoduan\workspace\jx-procurement-platform` 目录内读写。
- ★ **以台账为准**：冲突时一律以 `COLLAB.md` / `spec/` **当前内容**为准（**与记忆冲突时以台账为准**）。
- ★ **提交只用显式路径**（**禁止 `git add -A`**）；提交后 `git show --stat HEAD` 复核。
- ★ **每次提交前跑 `bash scripts/check_all.sh`，必绿基线 9/9**（会报项应**零命中**）。
- ★ **还原用 `cp` ＋ `sha256sum -c`**，**不得用 `git checkout -- <file>`**。
- ★ **一次只变异一处**；★ **变异「仍绿」不能判通过**（先确认变异处**是否真被执行**）。
- ★★ **`spec/**` 本包一律不动** —— 规格**已由批 47 落齐**（`chain.json` V1.13 / `README.md` V1.33）⇒ ★ **只消费、不改写**。★ **本包不新增任何路由** ⇒ ★ **不触发「路由集三处一致」链**（`docs/05-API.md` ↔ `spec/openapi.json` ↔ `router.go`）⇒ **`spec/openapi.json` 本包无需重生**（★ **例外**：若 `T2①` 你方认为须**在正本补写 `unresolved_roles` 的**元素键名**，则**同批**改 `docs/05-API.md` ＋ **由脚本重生** `spec/openapi.json` —— ★ **只许 `python scripts/gen_openapi.py` 重生，严禁手改**；★ 若决定**不**改正本，则**两者都不动**，在回执里写明该判断）。
- ★★ **`docs/**` 双方均可改**（`COLLAB.md §7`）：★ **本包必须改 `docs/20`**（`T2` ①② 各一处，理由见 §2）—— ★ 改完**在该表登记一行**。
- ★★ **`T1` 是一条「必须先取证、可能停手」的任务** —— ★ 见 `T1 §1.1` 的**停手条款**：**不要**为了「把包做完」而自造默认值、**不要**新增持久化、**不要**改用不可靠的间接反推。★ **停在议题里写清楚，比造一条看不见的绿更值钱**。

---

## 1. 背景（为什么有这一包）

★★ 本包治的是**两族**「**声明与事实不一致**」的轻量形态 —— 都**不报错**（静默误导）、都**不会**让门禁转红：

| 族 | 一句话 | 归属 |
|---|---|---|
| **`N-067` ①** | ★★ **机器空洞**：无待办节点上的 `hard` 判据**既不会被执行、也不会被判「未注册求值器」** —— fail-closed 的保证在**这一格**上被**静默豁免** | ★ 我方**规格先行已完成**（批 47）⇒ 本包＝**实现** |
| **`N-068`** | ★ 三处**工程表述/可观测性**：① API 契约声明的键名 ≠ 实际输出；② 自检提示的缺失数量 ≠ 真实缺失数量；③ 错误文案所述能力边界 ≠ 当前能力 | ★ 实现（我方已给全修法） |

★★★ **`N-067` ① 的形态（照抄 `spec` 契约，★ 以现文为准）**：`conventions.node_task_generation` 允许一个 `required: true` 的节点**不生成待办**（显式 `false`，或键缺失时按缺省推导为否）⇒ ★ 于是挂在它上面的 `approval(<node_id>)` 判据 **既不会被执行、也不会被判「未注册求值器」** —— 因为第 ① 条那台通用求值器**只在「任务被 approve」时触发**（按 `task.NodeID` 取节点）⇒ ★★★ **注意形态：不是「判为通过」，而是「根本不进判据面」** —— 比误放行更难发现。

★ **当前实例（实测，本包落地后在真实 spec 上「零行为变化」）**：被 `approval(<node_id>)` 引用的无任务节点**恰 1 个** —— `routes.purchase_tier1.nodes[2]` ＝ `anti_split_check`（`actor=system`）；★ 因其判据 `BA#anti_split_before_disburse` 的 `carried_by_kind = manual` ⇒ ★ **本处按契约跳过**（**人工承载 ≠ 声明了没实现**）⇒ ★★ **它在真实 spec 上不产生任何行为差异** ⇒ **鉴别力必须由合成用例承担**（见 `T1 §1.4`）。★ **不得**据此认为「拆单检查已变为系统强制拦截」。

---

## `T1` · `N-067` ① —— 无待办节点上的 `approval(<node_id>)` 判据的**跨节点钩子**

### 1.1 ★★ 先取证（**落笔前必须做**，回执里给 `文件:行号`）

1. ★★★ **【本包核心前置】`approveReject`（`internal/httpapi/handlers_approval.go:317`）期，能否【确定性】组装 `Facts`？**
   - ★ 已知事实（我方已核）：`Facts` 的**两处先例**—— `handlers_approval_preview.go:96-109`（预览）与 `handlers_approval.go:599-614`（提交）—— **均取自请求体 ＋ 会话身份**；★ 而 `approveReject` **没有请求体**（只有 `task_id` ＋ `fields` ＋ 会话身份）。
   - ★ **可用来源（我方已核到的，供你接着走）**：`t_instance` 列（`migrations/0001_init.sql:11-32`：`doc_type` · `amount_cents` · `purpose_class_l1` · `department` · `applicant_open_id` · `applicant_name`）＋ `t_instance_field` 键值表（`migrations/0001_init.sql:40-50`）＋ 角色（`ApplicantIsOpsSupervisor` ⇐ `综合运营主管`）。
   - ★★ **你要交的**：`Facts` 的**每一个字段**（`internal/chain/` 的 `type Facts struct`）**逐条**给出「能否确定性取到 / 取不到的具体原因」＋ **取值的 `文件:行号`**；★ 并**明确区分**：哪些字段影响 **route 判定**（`ResolveRoute`）、哪些影响 **节点序列**（`BuildNodes`）—— ★ 只有**两者都不影响**的字段，缺了才可以退让。
   - ★★★ **停手条款（硬）**：若**任一**「影响 route 或节点序列」的字段**无法确定性组装** ⇒ ★ **停手**，在 `COLLAB.md#N-067` 写清「**哪个字段取不到 · 为何 · 可选方案**」，**不要**：① 自造默认值；② 新增持久化（新表/新列/节点快照）；③ 改走「相邻 `seq` 空档反推」（契约**已明文**排除：`seq` **后置分配**、受 `tier3_plus` 插入 / `tier_expand` 展开 / `ref: contract_two_level` 展开 / `R-26` 有合同补插**四类形变**影响）。
2. ★ **「该节点已通过」的判定口径**：`t_flow_task`（`internal/store/` 与 `migrations/`）的**状态字段名与取值**是什么？「**该节点全部通过**」＝ 该节点**全部** task 状态为通过（★ 注意**顺序会签**：同节点可能**多条** task，`internal/flow` 的 `releaseNextTx` 逐级释放）⇒ 给出 `文件:行号`。
3. ★ **「无任务节点」的取法**：`chain.ResolvedChain`（`internal/chain/service.go:23-31`）的 `Nodes`（`[]RoleNode`，**含非审批环节**）与 `Spec`（`[]flow.NodeSpec`，**仅审批任务**）之差 —— ★ **复核该差集是否恰好等于**「`generates_task` 解析为否」的节点集合；★ 若**不等**（例如 `IsApproval == false` 的环节也落在 `Nodes` 里），给出**实际可用的判别字段**（`RoleNode.IsApproval`？`SourceNodeID`？）＋ `文件:行号`。
4. ★ `Deps.Chain` / `Deps.Spec` 在 `approveReject` 是否可用（★ 我方已核 `internal/httpapi/router.go:71-77` 有这两个字段 ⇒ **复核即可**）；★ 若 `d.Chain == nil` 或 `d.Spec == nil`，**参照** `handlers_approval_preview.go:77-79` 的**同款** `503 可见失败`（**不得**静默跳过）。

### 1.2 契约（我方已定，照此实现）

★ **触发时机与挂载点（唯一）**：`internal/httpapi/handlers_approval.go#approveReject` —— ★ **与第 ① 条同一处**（`handlers_approval_approvalchecks.go` 的注册表 `approvalCheckFns`），**拦在 `Flow.Approve` 之前**（**事务外**：判据不过 ⇒ `400`，**不落库、无半程副作用**）。

★ **求值对象**：**本次 approve 跨过的**无待办节点（可能 **0..n** 个）。
★ **「跨过」的语义（照契约）**：该节点（按 `seq` 定位）**之前**的、**已物化为 `t_flow_task`** 的节点**全部通过**之时刻。
★ **求值方式（★ 复用，不得另造）**：对每个跨过的节点，按其 `node_id` 跑该单据表单里 `severity == "hard"` ∧ `when == "approval(<node_id>)"` 的判据 ⇒ ★★ **复用 `evaluateApprovalChecksFor`**（`handlers_approval_approvalchecks.go:44-67`，或抽出一份**同构**内层供两处共用）—— ★ **禁止**写第二套分派逻辑（那是「**第二份真相**」，本仓头号反模式；`S14` / `N-058` 一族）。

★ **fail-closed 口径（与 `submit` 及第 ① 条逐字同源）**：`severity=hard` ∧ `carried_by_kind ≠ "manual"` ⇒ **必须已注册求值器；未注册 ⇒ 可见失败**（`400` 并**点名判据**，文案与第 ① 条同款）；`carried_by_kind == "manual"` ⇒ **跳过不执行**（★ 引擎**不越权**执行人工判据、**亦不对其 fail-closed**）。

★ **不重复执行（去重判据，两条须同时满足）**：
- ① **两条路径按「该节点有无待办」互斥划分、不存在交集** —— 有任务的节点**一律**走第 ① 条（按 `task.NodeID` 求值）；无任务的节点**一律**走本处 ⇒ **同一个 `node_id` 在一次推进中至多执行一次**；
- ② 同一节点内**顺序会签**时，本处只在「**使该节点达到全部通过**」的**那一次** approve 上触发（此前各次**不**触发）。

★ **不新增任何持久化**（无新表、无新列、无节点快照）。

### 1.3 边界与**禁止**（★ 回执里逐条自查）

| # | 边界 | 说明 |
|---|---|---|
| ☆1 | 该节点**之前无任何已物化节点** | 「跨过」时刻落在**提交期** ⇒ ★ **本期不定义**该分支（实测**无实例**）⇒ ★ **不得**就近塞进提交白名单（那会绕过「一条判据只声称一个时点」）；★ 若你方实现时**真遇到**该情形，**停手登记**，不自行扩契约 |
| ☆2 | **不得**改 `generates_task` 语义 | 亦不得改 `required` 的推进语义、不得改终态逻辑（零任务链的「提交即终态」仍由 `conventions.no_approval_chain` 覆盖） |
| ☆3 | **不得**把 `anti_split_check` 改成 `generates_task: true` | ★ 给**系统环节**造人工待办入口 ＝ **错的修法**（契约明文） |
| ☆4 | **不得**改任何判据的 `carried_by_kind` | `anti_split` 的 `manual` 是**正确处置**（其 `known_cost` 已具名）⇒ ★ **本包治的是「机制空洞」，不是「把人工判据自动化」** |
| ☆5 | **不得**新增第二套判据分派/求值逻辑 | 复用第 ① 条的内层引擎 |
| ☆6 | **不得**新增持久化 | 见上契约末条 |

### 1.4 ★★★ 鉴别性用例（**本包最容易做错的一步，务必读**）

★★ **先说结论：真实 `spec` 上，本处「零行为变化」** —— 唯一实例 `anti_split_check` 的判据是 `manual` ⇒ **跳过** ⇒ ★ **你**无法**用真实 spec 证明钩子生效**（跑出来「全绿」**证明不了任何事**）。

⇒ ★★ **鉴别力必须由【合成 spec】承担**（在测试内构造 `specload.Bundle` 的**内存副本**，**不落盘**）：

| # | 合成场景 | 期望 |
|---|---|---|
| ① | **无待办节点**（`generates_task` 解析为否）＋ `severity=hard` ＋ `when=approval(<该节点>)` ＋ `carried_by_kind ≠ manual` ＋ **未注册求值器** | ★ **approve ⇒ 可见失败**（`400` 并**点名判据**，文案与第 ① 条同款） |
| ② | **反证**：在 ① 的基础上**摘掉跨节点钩子** | ★ 该用例**必须恰红**（approve 变 `200`）—— ★ **这是「钩子在承重」的唯一证明** |
| ③ | **无待办节点** ＋ `hard` ＋ `carried_by_kind = manual` | ★ approve **照常 `200`**（证「**跳过**」语义**未被误伤**） |
| ④ | **回归**：真实 `spec` 下的既有用例（`BA#receipt_per_purchase` / `PR#no_self_purchaser_at_designation` / `SS` 两条） | ★ **全绿**（第 ① 条行为不变） |

★ **另**：★ **必须有**一条用例证明「**本次 approve 未跨过任何无任务节点时，本处不触发**」（★ 即**边界**不入判据面 —— 否则会变成「每次都跑全链的无任务节点」）。

### 1.5 验收判据（我方将**独立重做**，不采信自报）

- ★★ **门禁**：`bash scripts/check_all.sh` **必绿 9/9 ＋ 会报零命中**。
- ★★ **单点变异（至少一处，红/绿实测原文写进回执）**：**摘掉跨节点钩子** ⇒ §1.4 的用例 ① **必须恰红**（`200`）；★ **与该变异无关的用例（③④）必须保持绿**（**隔离性**）。★ 还原一律 `cp` ＋ `sha256sum -c`（**不得** `git checkout --`）。
- ★★ **合成用例的「先红后绿」**：先写用例 ①（在**实现之前** ⇒ **必红**，`200`），再实现 ⇒ **转绿**。★ 若你先实现、后写用例，**必须在回执里说明**（同 `N-064`/`N-066` 我方肯定过的做法）。
- ★ **回执必须给**：`T1 §1.1` 四项取证的 `文件:行号` 结论（**放在回执最前面**）；★ 若任一项触发**停手条款** ⇒ 停手、议题里写清。

---

## `T2` · `N-068` —— 联调查出的**三处轻量不一致**

★ 三处**互不依赖**、**均不阻塞联调**，但**都会误导下一个人** ⇒ 按「**可见的红 > 伪装的绿**」登记。★ 建议**先做 `T2`（快、独立）再做 `T1`（重）**。

### 2.1-① `unresolved_roles` 的**键名契约不一致**（**优先级最高** —— 会真的让前端取值落空）

- ★ **位点**：`internal/chain/assign.go:31-37` 的 `type UnresolvedRole struct` —— **四个字段均无 `json tag`** ⇒ 实际输出 `{"NodeID":…,"NodeName":…,"Role":…,"Reason":…}`；★ 而契约（`internal/httpapi/handlers_approval_preview.go:12` 契约注释 ＋ `docs/05-API.md:693`）声明的是 **`{node_id,node_name,role,reason}`**。
- ★ **取证（我方已核，供你复核）**：★ **同类结构全都已有 snake_case tag** —— `internal/httpapi/handlers_approval.go:517-518`（`node_id`/`node_name`）· `handlers_approval_preview.go:41-42`（`source_node_id`/`node_name`）· `internal/platform/feishu/push.go:122,131`（`node_id`/`node_name`）⇒ ★★ **本处是全仓唯一漏网** ⇒ **修它 ＝ 向既有约定靠拢**（风险极低）。
- ★ **修**：四字段补 `json:"node_id"` / `json:"node_name"` / `json:"role"` / `json:"reason"`。
- ★ **用例**：断言**响应体的键名**（**不只看 `code`**）—— ★ **先红后绿**（加用例时必红 ⇒ 补 tag 后转绿）。
- ★★ **同批文档（必须）**：`docs/20-Integration-Execution-Sheet.md:237-239` **明文声称**「`unresolved_roles` 目前的**响应键名是 `NodeID/NodeName/Role/Reason`**（大写驼峰）…… 已登记 `N-068` 修复」⇒ ★ 改为**已修复**形态（★ **保留历史对照、不删原文**）＋ ★ 在 `COLLAB.md §7` **登记一行**。
- ★ **正本与 `openapi`**：★ `docs/05-API.md` 的 `unresolved_roles[]` **未声明元素键名**（`:693`／`:751`／`:1049`）⇒ ★ **修 tag 属「实现向契约靠拢」，正本无需改 ⇒ `spec/openapi.json` 无需重生**；★ **但若你方认为须在正本补写元素键名** ⇒ ★ 则**同批**改 `docs/05-API.md` ＋ **由脚本重生** `spec/openapi.json`（★ **严禁手改**、`gen_openapi.py --check` 必 OK）。

### 2.1-② 启动自检「**算不到人的角色**」**恒多报 1**

- ★ **位点**：`cmd/jxapproval/bootstrap.go:188-195` —— 烟测只传 `DocType` / `AmountCents` / `UsageCategoryL1`（`:189-191`），★ **未传 `ApplicantOpenID`** ⇒ 而 `internal/chain/assign.go:90-97` 对 `actor == "applicant"` 节点在**缺申请人身份**时**记一条 `unresolved`** ⇒ ★ 该条**恒存在**，**与配置无关**。
- ★ **现象（实测）**：配齐 4 个业务角色前日志报 `unresolved=3`，配齐后**仍报 `unresolved=1`**（正是 `回交凭据` 那个 `applicant` 节点），而 `POST /api/approval/preview`（携带会话身份）**同一链 `0`** ⇒ ★ 两者**互相矛盾**，运维照日志去权限管理页**找不到要补的角色**（`docs/20:234` 已明文警告过这一点）。
- ★★ **修（二选一，建议 a）**：
  - **a.** 烟测传一个**具名探针身份**（如会话/系统管理员的 `open_id`），使 `applicant` 节点可解析；
  - **b.** 或把 `applicant` 类从**烟测的计数**中排除、并在日志里**区分**「**配置缺失**」与「**探针未传身份**」。
- ★★ **无论哪条**：★ **日志必须能点到具体角色/节点名**（现只给 `"unresolved", len(rc.Unresolved)` 一个**数字** ⇒ `docs/20` 的运维指引**无从落地**）。
- ★★ **若取 a**：★ **不得**用它**掩盖真实配置缺失** —— 日志须**同时**给出「**本次探针身份**」与「**真实 `unresolved` 明细**」（节点名 ＋ 角色 ＋ 原因）。
- ★ **同批文档（必须）**：`docs/20-Integration-Execution-Sheet.md:229-235`（**实测对照表** ＋ 「已登记 `N-068` 要求把该计数与 `preview` 口径对齐」）⇒ 更新为**已对齐**形态（★ 保留历史对照）；★ 在 `COLLAB.md §7` 登记一行。

### 2.1-③ `chain` 对 `SUB` 的**拒绝文案陈旧**

- ★ **位点**：`internal/chain/chain.go:47-48` —— `ErrUnsupportedDoc = errors.New("chain: 暂不支持该单据类型（批 1 仅 BA/PR/SA）")`；★ 实测 `POST /api/approval/preview {doc_type:"SUB"}` ⇒ `40000 chain: 暂不支持该单据类型（批 1 仅 BA/PR/SA）: SUB`。
- ★ **事实**：当前**实际已支持 6 条链**（`BA`/`CT`/`PC`/`PR`/`SA`/`SS` 实测均有 route：`CT`→`contract_two_level`、`PC`→`change`、`SS`→`sole_source`）⇒ ★ 文案里「批 1 仅 BA/PR/SA」**已过时**，会把排查**引向错误结论**。
- ★ **修**：文案改为**据实描述**（如「`SUB` 走独立提交通道（`POST /api/submission`）、**不参与审批链计算**」，或**列出实际支持的 `doc_type`**）；★ ★ **不要写死批次号**（会再次过期）；★ 注释（`:47`「批 1 之外的单据类型」）**同改**。
- ★★ **取证（我方已核，★ 这条让你放心改）**：全仓对 `ErrUnsupportedDoc` **均以【变量】引用**，**无任何字面断言** —— `internal/chain/chain_test.go:84`（`{"批3之外单据(SUB独立通道未接入)", Facts{DocType:"SUB"}, "", ErrUnsupportedDoc}`）· `internal/httpapi/handlers_approval.go:850`（`errors.Is(err, chain.ErrUnsupportedDoc)`）· `internal/chain/route.go:104`（`fmt.Errorf("%w: %s", ErrUnsupportedDoc, …)`）⇒ ★ **改文案不会打红任何测试**（★ 回执里**复核这一点**：`grep` 一遍确认无字面串断言）。
- ★ **注意（不改）**：`COLLAB.md:3566` 与 `MIMO-NEXT-BATCH-15.md:12` 里出现的旧文案属**历史实测记录/历史转述** ⇒ ★ **保持原样**（台账是追加式的、历史不失真）。

### 2.2 验收判据（我方将独立重做）

- ① `unresolved_roles` **响应键名与契约逐字一致**（★ **新增键名断言用例，先红后绿**）；★ `docs/20:237-239` 同批更新。
- ② 启动自检的 `unresolved` 计数**与 `preview` 口径一致**（**或**日志能**点名**具体角色/节点）；★ `docs/20:229-235` 同批更新。
- ③ `SUB` 文案**不再含陈旧批次号**；★ 全仓**无字面串断言**（复核）。
- ④ `bash scripts/check_all.sh` **必绿 9/9**。
- ⑤ ★ **每处附单点变异**（一次只变异一处）：建议 ① 把 `json tag` 摘掉 ⇒ 键名用例**恰红**；② 把烟测的 `ApplicantOpenID` 还原为不传 ⇒ 计数用例/日志断言**恰红**；③ 把旧文案改回 ⇒ 文案断言**恰红**；★ 各自**无关用例保持绿**。

---

## 2. 明确**不做**（★ 重要）

| 项 | 原因 |
|---|---|
| 改 `spec/**`（`chain.json` / `forms/*.json` / `checks.json` / `README.md` / `openapi.json`） | ★ **我方先行域，且规格已由批 47 落齐** ⇒ **只消费、不改写**（★ `openapi.json` 的唯一例外见 §0） |
| 新增/修改任何**路由** | ★ `T1`/`T2` **均不新增端点** ⇒ **不触发「路由集三处一致」链** |
| 把 `anti_split_check` 改成 `generates_task: true` | ★ **错的修法**（契约明文）；★ 且会**改变现有行为** |
| 改任何判据的 `carried_by_kind` | ★ **我方域**（`spec` 声明）；★ `manual` 是正确处置 |
| 实现「拆单检查的**自动**执行」 | ★ **不在本契约内** —— 契约治的是**机制空洞**，**不是**把人工判据自动化（★ 契约「诚实划界」段） |
| `N-067` ② 的 **UI 录入入口**（`return_receipt` 凭据录入） | ★ **另批** —— ★ **前置 ＝ 先确认界面归属**（☆ 复用审批控制台 vs ☆ 申请人专用录入页），**待我方确认后**再出包 |
| 计算 `actual_vs_approved_diff_cents` 等既有缺口 | ★ 不属本包 |

---

## 3. 交付要求

1. 每完成一族跑 `bash scripts/check_all.sh`，**必绿 9/9 ＋ 会报零命中**。
2. ★ `T1`：§1.1 **四项取证的 `文件:行号` 结论放在回执最前面**；★ 若触发**停手条款** ⇒ **停手**、在 `COLLAB.md#N-067` 写清（**不要**硬做）。
3. ★ `T1`：§1.4 的**合成用例**（① ② ③ ④ ＋ 边界用例）逐条给**红/绿实测原文**。
4. ★ `T2`：三处**逐处**给改动证据（`文件:行号` ＋ 用例红/绿原文 ＋ `docs/20` 的同批更新）。
5. ★ **单点变异**：`T1` 至少一处、`T2` 每处一处；给出**红/绿实测原文** ＋ **隔离性**（无关用例仍绿）＋ `cp`/`sha256sum -c` 还原输出。
6. 提交**只用显式路径**；完成后在 `COLLAB.md#N-067` 与 `#N-068` **各追加一条 `MIMO-DONE` 逐条回执**。
7. 推送 `origin/main`。
8. ★ 需要我方裁定才能定的口径，**停在议题里写清楚，不要猜**。

---

## 4. 同批清单（★ 自查用，缺一即红/缺一即「悬空」）

- [ ] `T1` §1.1 取证四项（★ **核心前置**，含**停手判定**）
- [ ] `T1` 跨节点钩子（**复用** `evaluateApprovalChecksFor`；`Flow.Approve` 之前、事务外）
- [ ] `T1` 合成用例 ①（未注册 ⇒ `400`）＋ ②（摘钩子 ⇒ 恰红）＋ ③（manual ⇒ 跳过）＋ ④（真实 spec 回归）＋ 边界用例
- [ ] `T1` 不新增持久化、未改 `generates_task`/`required`/终态、未改 `carried_by_kind`
- [ ] `T2①` 四字段补 `json tag` ＋ **键名断言用例**（先红后绿）＋ `docs/20:237-239` 同批
- [ ] `T2②` 烟测修法（a 或 b）＋ **日志点名牌角色** ＋ `docs/20:229-235` 同批
- [ ] `T2③` 文案据实（★ 无批次号）＋ 全仓**无字面串断言**复核
- [ ] `docs/20` 两处（★ 改完在 `COLLAB.md §7` **登记一行**）
- [ ] 若动 `docs/05-API.md` ⇒ `spec/openapi.json` **由脚本重生**（`--check` OK）；★ 不动则回执写明判断
- [ ] `bash scripts/check_all.sh` **必绿 9/9 ＋ 会报零命中**
- [ ] 单点变异（`T1` ≥1、`T2` 每处 1）＋ 隔离性 ＋ `cp`/`sha256sum -c` 还原
- [ ] `COLLAB.md#N-067` / `#N-068` 各追加 `MIMO-DONE` 回执
- [ ] 显式路径提交 ＋ 推送 `origin/main`

---

## 5. 单点变异要求（我方验收时会**独立重做**，★ 不采信自报）

★ `T1` 至少 **1** 处、`T2` **每处 1** 处，并把**红/绿实测原文**写进回执：

- `T1`：**摘掉跨节点钩子** ⇒ §1.4 用例 ① **必须恰红**（`200`）；★ 用例 ③④ **保持绿**。
- `T2①`：**摘掉 `json tag`** ⇒ 键名用例**必须恰红**；★ 其余 `T2①` 用例保持绿。
- `T2②`：**还原「不传 `ApplicantOpenID`」** ⇒ 计数/日志断言**必须恰红**；★ 其余保持绿。
- `T2③`：**把旧文案改回** ⇒ 文案断言**必须恰红**；★ 其余保持绿。
- ★ 各处都要**同时报告**「与该变异无关的用例保持绿」（**隔离性**）。
- ★ 还原一律 `cp` ＋ `sha256sum -c`（**不得** `git checkout --`）。

---

## 6. 文书与时间口径（★ 沿用本仓既有纪律，勿破）

- ★ **时间一律写绝对日期**（`YYYY-MM-DD`；需要时到分钟）—— ★ **不得**使用「今天」「昨天」「上周」「本批刚」这类**相对时间词**（★ 台账与文档要能被**日后独立复核**）。
- ★ **所有文字用简体中文**（含 `zh-rHK` / `zh-rTW` 语境一律简体）。
- ★ **台账时间戳取真值**：需要提交时间时一律 `git log --format=%ci` 读，**不得**凭估计顺推。
- ★ **`COLLAB.md` 的字段名逐字固定**：`- **背景**：` / `- **建议方案**：` 等须**逐字**且**内容同在一行**（换行会被判「字段为空」）；`- **类型**：` 只允许既有 5 个枚举值（**加括注即非法**）。
- ★ **Markdown 表格里的「竖线」一律转义为 `\|`** —— ★★ **反引号不保护竖线**（本仓头号陷阱；`COLLAB.md` / `MIMO-*.md` 已在表格门禁扫描面内）。
