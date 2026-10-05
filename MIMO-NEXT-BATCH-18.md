# MIMO-NEXT-BATCH-18 —— 批 21：`N-057` ② —— 节点条件分支数据驱动（`NodeBranch.id` 绑定 ＋ `actor` 唯一来源 ＋ fail-visible）

> ★ **本文件可整份粘贴给 mimo code。**
> 交付方：**WorkBuddy**（规格；★ 已于本批入库 —— `spec/chain.json` **V1.6** 新增 `conventions.node_branch`）／ 实现方：**mimo code**（`internal/specload` ＋ `internal/chain` Go 侧 ＋ 单元测试）
> 依据：`COLLAB.md §4 · N-057`（② 具名余项 ①）；`REMAINING.md §1 B16 / §5 批 21`；**`spec/chain.json#conventions.node_branch`**（**规格正本**）；`spec/chain.json#conventions.non_machine_read_keys`
> 时间：2026-10-05

---

## §0 问题本体（一句话 ＋ 规格已定）

★ **现象（读代码取证，非推断）**：`spec/chain.json#routes.purchase_tier1.nodes[1]`（`approve_petty_cash`，R-03 备付金上抬）**声明了**一条节点级条件分支：

```json
"branches": [
  { "id": "applicant_is_ops_supervisor",
    "when": "applicant.is_ops_supervisor == true",
    "actor": "supervisor",
    "effect": "该节点**上抬至主管领导**" }
]
```

★★ **而实现侧一个字都没读** —— `internal/chain/nodes.go:92` 写的是：

```go
if f.ApplicantIsOpsSupervisor && hasBranchWhen(n, "applicant.is_ops_supervisor == true") {
    role = "supervisor"
    ...
}
```

⇒ ★★ **两处业务值（替换角色 `supervisor` ＋ 条件串 `applicant.is_ops_supervisor == true`）硬编码在 Go 里**，spec 的声明**只做样子**：改 `branches[0].actor` 或改 `when` 文本，**行为一动不动**（★ 与 `N-031`／`N-049` ② 同族：**「判据＝数据，不得在代码里写字面量」**）。★ 其中 `when` 文本更危险：**文本一改 ⇒ `hasBranchWhen` 匹配不上 ⇒ R-03 上抬静默失效**（`required: true` 的节点被静默降级为常规审批人）。

★★ **规格已定（本批已入库）＝ `spec/chain.json#conventions.node_branch`**，要点五条：① **绑定键 ＝ `branches[*].id`**（★ 与本仓库既有范式一致 —— 实现本就按 `nodes[*].id` 分派，如 `n.ID == "approve_petty_cash"`）；② **`when` ＝ 人读条件描述**，其真值由代码计算的事实（`chain.Facts`）提供 ⇒ **代码不得比对 `when` 文本**；③ **`actor` ＝ 命中分支后本节点的替换角色 —— 唯一来源，代码不得写字面量**；④ **fail-visible**：命中分支**缺 `actor` / 为空 / 不是审批角色** ⇒ **可见失败**（不得静默保留默认角色）；⑤ 当前实例**恰 1 处**。★ 并有 ⑥ **边界（如实）**：代码只对**自己能计算的事实**匹配分支 ⇒ spec 里出现代码不认识的 `id` ⇒ 该分支惰性（无判据可查）⇒ **新增/改名分支 `id` 必须同批给出事实生产者与鉴别性用例**。

★★ **本批的硬次序 ＝ 一条**：我方规格**已入库**（`chain.json` **V1.6**）⇒ 本批 **mimo 直接落 Go 侧**；★ **本批零新增判据、零原语改动**（`checks.json` 一字不改）⇒ **无「你先我后」的水窗、无红窗**。

---

## §1 本批规格（★ 逐条对齐；**不在下表的一律不动**）

### T1 · `internal/specload/specload.go` —— `NodeBranch` 增绑定键

`NodeBranch`（：218）新增字段（★ 放在 `When` **之前**，键名逐字）：

```go
// ID 分支的稳定绑定键（N-057 ②）：代码按该 id 匹配分支；
// When 是人读条件描述（其真值由 chain.Facts 提供）—— 代码不得比对 When 文本；
// Actor 是命中后该节点的替换角色（唯一来源，不得在代码里写字面量）。
// 规格正本 ＝ spec/chain.json#conventions.node_branch。
ID     string `json:"id"`
```

★ **不得**给 `NodeBranch` 加其它键；★ **不得**引入 `DisallowUnknownFields`（本仓库 `specload` 刻意宽松，见 `DocChainDoc.UnmarshalJSON` 的注释性字符串条目兼容）。

### T2 · `internal/chain/nodes.go` —— R-03 段改为数据驱动 ＋ fail-visible

**改前**（：89–98 现状）：

```go
if n.ID == "approve_petty_cash" {
    role := "ops_supervisor"
    note := ""
    if f.ApplicantIsOpsSupervisor && hasBranchWhen(n, "applicant.is_ops_supervisor == true") {
        role = "supervisor"
        note = "R-03：申请人＝综合运营主管，本节点上抬至主管领导"
    }
    appendApproval(n.ID, n.Label, role, note)
    continue
}
```

**改后要求（语义逐条）**：

1. **匹配按 `id`**：分支匹配条件**只能是** `branches[*].id == "applicant_is_ops_supervisor"`（★ `id` 是**稳定标识**，允许出现在代码里；★ **`when` 文本不得再出现在代码里**，也不得再用 `hasBranchWhen` 比对它）。
2. **适用性判定**：仅当 `f.ApplicantIsOpsSupervisor == true` 时才尝试上抬（★ 该事实由 `internal/httpapi` 生产：`idn.Role == "综合运营主管"`，**不在本批范围内**，生产者缺口已在 `chain.go:71` 注释登记）。
3. **角色取 `br.Actor`**：★ **不得**再写字面量 `"supervisor"`。
4. **fail-visible（★ 不许静默降级）**，两条各返回**可见错误**（`BuildNodes` 的第二个返回值，不得 `panic`、不得静默继续）：
   - **分支不存在**（事实为真、但该节点没有 `id == "applicant_is_ops_supervisor"` 的分支）⇒ 报错，文案须含**节点 id** 与**分支 id**；
   - **分支存在但 `actor` 不可用**（空字符串 或 **不是审批角色** —— 即 `!approverRoles[br.Actor]`）⇒ 报错，文案须含**分支 id** 与**实际 `actor` 值**。
5. **普通路径零行为变化**：`f.ApplicantIsOpsSupervisor == false` ⇒ 输出与改前**逐字段相同**（`role = "ops_supervisor"`、`note = ""`）；`true` ⇒ `role = spec 的 "supervisor"`、`note` 文案**保持原样**（既有用例 `internal/chain/chain_test.go:149` 断言该 note 非空）。
6. **新增 helper**：`func branchByID(n specload.NodeDoc, id string) (specload.NodeBranch, bool)`（顺序查找 `n.Branches`）。
7. **`hasBranchWhen`（：336）**：★ 先 `grep -rn "hasBranchWhen" .`（**含测试**）确认除本处外**无引用** ⇒ **删除**（不留死代码）；★ 若确有引用 ⇒ **保留并在回执说明**（不许两头都不清）。

★ **不得改动**：`routes.purchase_tier3.branches.tier3_plus` 段（：62–77）· `n.Ref == "contract_two_level"` 段 · `TierExpand` 段（：103）· 一般节点段（：111–120）· `expandContract` / `buildContractRouteNodes`（★ 后者的 `step.Actor` **没被读**是**已登记的事实**，见 `conventions.non_machine_read_keys` —— ★ **本批不修它、也不得顺手「优化」**）。

### T3 · 单元测试（★ 本批**验收的核心证据**；新建 `internal/chain/node_branch_actor_test.go`）

★ 装配范式照抄 `internal/chain/chain_test.go`：`loadBundle(t)`（＝与生产同一份 embed 字节，`specfs.FS`）；★ **每次用例各自 `loadBundle(t)`**（不同 bundle 实例 ⇒ 变异互不污染）。

★ **变异注入点（★ 本项目教训：变异必须打在「数据流经的那份数据」上、且在被读取点之前）**：直接改**内存 bundle** 里的 `b.Chain.Routes["purchase_tier1"].Nodes[1].Branches[0]`（`Routes` 是 `map[string]RouteDoc`、`Nodes` 是切片 ⇒ 直接索引可改到底层数组），**再**调 `BuildNodes(b, …)`。

| 用例 | 构造 | 期望 |
|---|---|---|
| **D1 · 数据驱动（★ 本批主证）** | 把 `Branches[0].Actor` 改成 `"deputy_general_manager"` | `approve_petty_cash` 的 `ActorRole == "deputy_general_manager"`（★ **写字面量的实现必红**） |
| **D2 · 绑定键是 `id`（不是「第一条分支」）** | 把 `Branches[0].ID` 改成 `"other_fact"` | 上抬**不生效**：`ActorRole == "ops_supervisor"` |
| **D3 · fail-visible：缺 `actor`** | `Branches[0].Actor = ""` ＋ 事实为真 | `BuildNodes` **返回非 nil 错误**，且错误文案含 `applicant_is_ops_supervisor` |
| **D4 · fail-visible：非审批角色** | `Branches[0].Actor = "purchaser"`（action actor）＋ 事实为真 | `BuildNodes` **返回非 nil 错误**，且文案含 `purchaser` |
| **D5 · 回归：事实为假不上抬** | 真 spec ＋ `ApplicantIsOpsSupervisor: false` | `ActorRole == "ops_supervisor"` 且 `BranchNote == ""` |
| **D6 · 回归：真 spec 上抬仍生效** | 真 spec ＋ `true` | `ActorRole == "supervisor"` 且 `BranchNote != ""`（★ 与既有 `TestBuildNodes/BA_R03上抬` 同判据，**两条都在**属正常冗余，不得删既有那条） |

★ 断言方式：**逐条断言「红在哪、哪条不红」**（筛 `SourceNodeID == "approve_petty_cash"` 的节点后逐字段比），不允许只断言 `err != nil` 或 `len(nodes) > 0`。

### T4 · 三条单点变异（★ 一次只改一处；`cp` 备份还原，**禁用 `git checkout --`**）

| # | 变异 | 预期 |
|---|---|---|
| **M1** | 把 `role = br.Actor` 改回字面量 `role = "supervisor"` | ★ **恰红 D1**（其余全绿）⇒ 证明 D1 真的钉住了「数据驱动」 |
| **M2** | 去掉两条 fail-visible 检查（缺 actor 时静默保留默认角色） | ★ **恰红 D3 ＋ D4**（其余全绿） |
| **M3** | 匹配条件由 `id` 改为「取 `n.Branches[0]`（不看 id）」 | ★ **恰红 D2**（其余全绿） |

★ **变异前先自证「变异真的改变了行为」**（打在非数据流经处＝空操作）；★ **编译失败 ≠ 判据无鉴别力 —— 先怀疑变异本身**；★ 每条变异后 `cp` 还原 ＋ `sha256sum -c` 核对。

### T5 · 硬边界（★ 越界即返工）

- ★ **不动 `spec/**` 一字**（`chain.json` 的 `branches[0].id` ＋ `conventions.node_branch` / `non_machine_read_keys` **已由我方落库**；★ 本批**不新增判据、不加原语、不改 `checks.json` 版本号**）。
- ★ **不动 Python 侧**（`scripts/check_spec.py` 归我方）· **不动 `scripts/check_all.sh`**。
- ★ **不动** `contract_approval` 相关代码（`expandContract` / `buildContractRouteNodes`）与 `doc_chains`（★ 那两处是**已登记的非机读键**，见 `conventions.non_machine_read_keys`）。
- ★ 提交只用**显式路径**（**禁止 `git add -A` / `git add .`**）；★ 完成后回写 `COLLAB.md`（本议题追加回执块 ＋ 标 `MIMO-DONE`，★ **状态字段留 `OPEN`** —— 结案由我方验收后判定）**并推送 `origin/main`**。

### T6 · 回执与自测

★ 回执须含：① 改动文件（显式路径）② `go test ./... -count=1` 全绿 ③ `bash scripts/check_all.sh` **必绿 8/8 ＋ 会报零命中** ④ T3 六条用例的**逐条结果** ⑤ T4 三条变异「红在哪、绿在哪」＋ `sha256sum -c` 还原证据 ⑥ `hasBranchWhen` 的处置（删除 / 保留 ＋ 依据）⑦ `MIMO-DONE` 标记。

---

## §2 为什么本批「不加判据、不改 `checks.json`」（★ 请照此理解，别擅自加）

★ 本批修的是**消费方**（Go 读 spec），规格（`branches[*].id` 的绑定契约）**已在 `chain.json` 的 `conventions` 里**（`conventions` 是新键的既有落点，**不被任何判据引用**）⇒ ★ 门禁判定值**零波动**（判据仍 30 / 原语仍 12 / 必绿基线仍 8）。★★ 若本批擅自加判据或改 `checks.json`，会与**我方正在跑的收尾**冲突（同一文件双方同批改＝本项目反复踩过的坑）。

---

## §3 验收方式（我方，交办后执行；列此仅供你自检对标）

1. 独立复跑 `bash scripts/check_all.sh` ⇒ **必绿 8/8 ＋ 会报零命中**（★ 不采信自报）；
2. 逐行走查实现（是否仍残留 `"supervisor"` / `when` 字面量 · 两条 fail-visible 是否真的返回错误 · `branchByID` 是否顺序查找）；
3. ★★ **三条单点变异我方自做**（M1/M2/M3，一次只变异一处）＋ `cp` 备份 ＋ `sha256sum -c` 还原；
4. 独立复跑 `go test ./internal/chain -count=1` ＋ 断言 T3 六条用例的**鉴别力**（D1/D2/D3 在对应变异下必须转红）。

---

## §4 交办纪律（★ 硬性，与 §1–§3 同等效力）

1. ★ **工作范围仅限本仓库目录** —— 即 `C:\Users\haoduan\workspace\jx-procurement-platform`（当前仓库树内）；★ **不得读写仓库外任何路径**（含 RaiDrive 网络盘 `Z:` 及一切挂载盘）。
2. ★ **以 `COLLAB.md` 为准** —— 若与本机记忆、对话上下文、或其它文档冲突时，**一律以 `COLLAB.md` 台账为最高依据**；动手前先读 `COLLAB.md §1`（当前状态）与 `§4 · N-057` 段。
3. ★ **提交前必跑 `bash scripts/check_all.sh` ⇒ 必绿 8/8 ＋ 会报零命中**；★ **红了不入库**（先修到绿再提交）。
4. ★ **只用显式路径提交** —— `git add <显式文件路径>…`（本批应为 `internal/specload/specload.go` ＋ `internal/chain/nodes.go` ＋ 新建 `internal/chain/node_branch_actor_test.go` ＋ `COLLAB.md`）；★★ **禁止 `git add -A` / `git add .`**。
5. ★ **完成后回写 `COLLAB.md` 台账**（本议题追加回执块 ＋ 打 `MIMO-DONE` 标记，★ **状态字段留 `OPEN`**）**并推送 `origin/main`**。
