# MIMO-NEXT-BATCH-15 —— 批 16：`A8`/`N-056` 落地段 —— 「无审批链（登记型）单据」发起通路接线（`GR`/`QC`/`RFQ`/`BJ`）

> ★ **本文件可整份粘贴给 mimo code。**
> 交付方：**WorkBuddy**（通路口径与规格；★ 口径已于本批入库 —— `spec/chain.json` **V1.3**）／ 实现方：**mimo code**（链算 · 流程 · handler 级端到端用例）
> 依据：`COLLAB.md §4 · N-056`；`REMAINING.md §2 A8 / §5 批 16`；**`spec/chain.json` V1.3**（新增 `conventions.no_approval_chain` ＋ `doc_chains.{GR,QC,RFQ,BJ}.no_approval_chain: true`）；`spec/README.md` **V1.14**
> 时间：2026-10-05

---

## §0 问题本体（一句话 ＋ 口径已定）

★ **现象（可复现）**：`GR` / `QC` / `RFQ` / `BJ` 四张单据**提交必然 400** —— `internal/chain/route.go#ResolveRoute` 的 `case` 只覆盖 `BA`/`PR`/`SA`/`CT`/`SS`/`PC`，其余落 `default` ⇒ `ErrUnsupportedDoc`（全仓 `grep "DocGR\|DocQC\|DocRFQ\|DocBJ"` 在 `internal/` + `cmd/` **零命中**）。

★★ **口径缺口（本批的根因）**：这四张单据「**无审批链**」这一事实，此前**只存在于散文** —— `forms/*.json#routes` 的说明键 · `docs/07 §9` · `docs/02` 用例主流程「验收登记类，通常无需推三方实例」；而 `enums` / `forms` 里**没有布尔键**。★ 更糟：`doc_chains.GR` **有 `env_count: 1` 却无 `route`**，`RFQ`/`BJ`/`QC` 是 `env_count: 0` 的 `no_chain` ⇒ ★ **实现侧无从判定，只能硬编码**。

★★ **口径（已入库，唯一机读来源 ＝ `spec/chain.json#conventions.no_approval_chain`）**：

- **判定**：`doc_chains.<doc>.no_approval_chain == true` ⇒ 该单据为**登记型**。当前 **4 张**：`GR` / `QC` / `RFQ` / `BJ`。
- ★★ **明示排除 `SUB`** —— `SUB` 同样**无 `route` 键**，但走**独立通道** `POST /api/submission` ⇒ ★ **「无 `route` 键」不等于「登记型」**。★ 因此**不得**用「`doc_chains.<doc>.route == ""`」当判定条件（那会把 `SUB` 卷进来）。
- **提交行为（三条 ＋ 一条边界）**：
  1. **链为空**：`ResolveRoute` 返回 `RouteID == ""` 且 **不报错**；`BuildNodes` 零节点；★ **不得报 `ErrRouteMissing`**。
  2. **提交即终态**：零审批任务 ⇒ **同事务**直接置终态 `APPROVED`，★ **不新增状态值**（依据 ＝ `forms/GR.json#checks[id=ledger_l07_written].when` 明文「提交即终态」）。
  3. **落账**：按 `doc_chains.<doc>.ledger` —— `GR` → **`L07` 一行**；`QC`/`RFQ`/`BJ` **不落账**（＝ **合法事实**，不是缺陷）。
  4. **边界（不新造行为）**：三方实例推送**不在本口径内**（`docs/02` 原文「**通常**无需推三方实例」是**事实描述**、非硬约束）⇒ ★ **不新增推送分支**；若实测出错，**如实回执并按可见失败处置**，**不得静默吞掉**。

★★ **本批不做（具名，别顺手做）**：`routes.emergency`（`when: is_emergency == true`）**亦无可达通路**，但★ **不属登记型**（它有 6 个节点）⇒ 其接线缺的是「**紧急采购由哪张单据承载**」的**需求澄清**，已登记 `COLLAB.md#N-056` 的「本批不做」。

---

## §1 本批规格（★ 逐条对齐；**不在下表的一律不动**）

### T1 · 链算层（`internal/specload` ＋ `internal/chain`）

1. `internal/specload/specload.go#DocChainDoc`（：224）新增字段 `NoApprovalChain bool \`json:"no_approval_chain"\``（★ 该结构的 `UnmarshalJSON` 是**非严格** `json.Unmarshal` ⇒ 加字段即可，别改成 `DisallowUnknownFields`）。
2. `internal/chain/chain.go`（：30）新增单据常量 **`DocGR = "GR"` / `DocQC = "QC"` / `DocRFQ = "RFQ"` / `DocBJ = "BJ"`**（与既有 `DocBA`…`DocPC` 同处）。
3. `internal/chain/route.go#ResolveRoute`（：19）新增分支：

```go
case DocGR, DocQC, DocRFQ, DocBJ:
    dc := b.Chain.DocChains[f.DocType]
    if dc.NoApprovalChain {
        return RouteResult{RouteID: ""}, nil // 登记型：链为空（★ 不报错）
    }
    if dc.Route == "" {
        return RouteResult{}, fmt.Errorf(
            "%w: doc_chains.%s 既未声明 no_approval_chain、也无 route（机读规格不完整）",
            ErrRouteMissing, f.DocType)
    }
    return RouteResult{RouteID: dc.Route}, nil
```

★ **fail-closed 要求**：flag 为 `false` 且 `route` 为空 ⇒ **可见失败**（`ErrRouteMissing`）；★ **不得**把「查不到 `doc_chains` 条目」静默当作登记型。★ **判定只读 `no_approval_chain` 键**，★ **不读 `env_count`**（该键 `conventions` 明文「语义待定，当前不得依赖它做任何判定」，且实测 `GR.env_count=1` 而 `RFQ`/`BJ`/`QC`=0 ⇒ 用它判会自相矛盾）。

4. `internal/chain/nodes.go#BuildNodes`（：30）**首行**加：

```go
if routeID == "" {
    return []RoleNode{}, nil // 登记型：零节点（★ 必须在 Routes 查表之前，且不得报 ErrRouteMissing）
}
```

★ 其余不动：`Compute`（`internal/chain/service.go`）无需改 —— 空 `nodes` 经 `Resolve` 得空 `Spec`、`Unresolved` 为空 ⇒ `EnsureResolvable` 天然通过。

### T2 · 流程层（`internal/flow/service.go#Submit`，：192）

★ 在 `createTasksTx`（：278）**之后、同一个事务内**（`WithTx` 回调内），加「零审批任务 ⇒ 提交即终态」：

- **条件**：`len(in.Nodes) == 0`（★ 以**传入的审批节点**为准；零节点 ⇒ `createTasksTx` 的循环不执行 ⇒ `t_flow_task` **零行**）。
- **动作**：调 `s.terminalizeTx(ctx, tx, inst, InstanceApproved, at, "")`。
  ★ **顺序不可颠倒**：`Submit` →（**同事务**）`terminalizeTx` →（`terminalizeTx` 内部自动）落账 ＋ 落账自检。★ **不得**写成「先提交、出事务后再置终态」（会留下 `PENDING` 悬挂窗口）。
- ★ **状态史**：`terminalizeTx` 自身**不写**状态史（既有约定：状态史在 `Submit`/`act`/`Cancel` 各迁移点写）⇒ ★ 本批须在 `Submit` 内**补一行** `AppendStatusHistory`，取 `act`（：534）同范式：`Status = inst.Status`（终态后值）· `OperatorOpenID = in.ApplicantOpenID` · `Opinion = "提交即终态（无审批链）"`。
  ★★ **不得**因「提交即终态」而**删掉**既有的 `PENDING`/`"提交"` 留痕行（：308）—— 那是 `Submit` 的既有行为，**本批不动**。
- ★ **事件**：同批 `events` 追加 `FlowEvent{Type: EventInstanceApproved, ...}`（与 `act`（：556）的终态事件同范式）；`emit` 仍在事务提交后（：353）。
- ★ **不得新增状态值**（`PENDING`/`APPROVED`/`REJECTED`/`CANCELED` 之外一律不加）；★ **不得**给 `TaskXXX` 加新值。

### T3 · handler 级端到端用例（★ 本批**验收的核心证据**）

★ 目录：`internal/httpapi/`，**新增**一个 `*_test.go`（★ **不是**改既有 `gr_checks_test.go`/`qc_checks_test.go`/`rfq_checks_test.go` —— 那三个是**直调求值器**的函数级用例，本批要的是**过 HTTP**）。

★ 建 app 的范式照抄 `hardchecks_n052_test.go:90` 的 `newSASubmitAppWith`（`storetest.NewDB` → `store.Migrate` → `seed.SeedQ3Defaults` → `UpsertConfigMapping(approval_code=…)` → `LoadApprovalMap` → `UpsertApprovalDef` → `specload.Load(specfs.FS)` → `NewRouter`），★ 并**同批**为 `GR`/`QC`/`RFQ`/`BJ` 各配一条 `approval_code ↔ doc_type` 映射与 `t_approval_def`（否则 handler 在：548 就先 400，测不到通路）。

| 用例 | 请求 | 期望（★ 逐条断言，缺一不可） |
|---|---|---|
| **E1 · `GR`** | `POST /api/approval/submit`（合法 GR 载荷） | ① HTTP **200**；② 响应 `status == "APPROVED"`；③ `biz_no` 以 **`GR-`** 开头；④ `t_flow_task` 该 `biz_no` **0 行**；⑤ `t_instance.status == "APPROVED"`；⑥ **`t_ledger_archive` 有且仅有 `L07` 一行**（★ 且 `biz_no` 对得上） |
| **E2 · `QC`** | 同上（合法 QC 载荷） | ① **200**；② `status == "APPROVED"`；③ **0 任务**；④ **不落账**（`t_ledger_archive` 该 `biz_no` **0 行**） |
| **E3 · `RFQ`** | 同上（合法 RFQ 载荷） | 同 E2 |
| **E4 · `BJ`** | 同上（合法 BJ 载荷） | 同 E2 |
| **E5 · 反向（负例）** | ★ 至少要有一条**证明「本批没有把通路开得过宽」** | 见下 |

★ **E5 的构造要求（两选一，**必须做至少一条**）**：**(a)** 内存里把某张**有链**单据（如 `SA`）的 `doc_chains` 条目**临时**置 `NoApprovalChain = true` 的**反向**：即把 `BA` 的 `no_approval_chain` 人为置 `true` ⇒ ★ 该单据**也不应**因此获得零节点通路（★ 因为 `BA` 的 `ResolveRoute` 分支**不读**该键）—— 断言 `BA` 提交仍走既有 `purchase_tier1` 链（≥1 任务）；**(b)** 把 `GR` 的 `no_approval_chain` 在内存里置 `false` 且 `route` 为空 ⇒ 断言提交 **400**（`ErrRouteMissing` 路径，fail-closed 成立）。★ 用内存 mutator（`mutate(bundle)`）实现，★ **spec 文件零改动**（同 `newSASubmitAppWith` 的既有范式）。

★ **载荷构造**：`GR`/`QC`/`RFQ`/`BJ` 各自的 `hard`/提交时点判据**必须在夹具里被满足**（★ 夹具不合法 ⇒ 400 打在表单/硬判据上，**测不到通路**）。★ 各单据的必填与硬判据**自己从 `spec/forms/*.json` 读**（别凭印象）；★ 若某条 `hard` 判据**在当前实现下不可满足**，★ **如实回执**（**不要**为了测试通过去改 `severity`、**不要**改 `spec/`）。

### T4 · 三条单点变异（★ 一次只改一处；`cp` 备份还原，**禁用 `git checkout --`**）

| 变异 | 预期（★ 必须**精确**） |
|---|---|
| **M1** T1-4 的 `routeID == ""` 早返回**去掉**（让它落回 `Routes[""]` 查表 ⇒ 报 `ErrRouteMissing`） | ★ E1–E4 **全红**（400）；★ **与通路无关的用例全绿** |
| **M2** T2 的「零任务 ⇒ 终态」**去掉**（保留写实例 ⇒ 停在 `PENDING`） | ★ E1–E4 的 ②⑤ 断言**恰红**（200 但 `status == "PENDING"`）＋ ⑥/E2–E4 的落账断言**恰红**（永不落账）⇒ ★ **这正是「不可接受形态」的实跑证据** |
| **M3** T2 的落账**口径改错**（例如把 GR 的 `maps.Ledger` 配成 `L05`，或把 `QC` 配成 `L07`） | ★ E1（或 E2）的**落账断言恰红**；★ 其余全绿 |

★ 三条都要给出「**红在哪、绿在哪**」的**逐条对照**；★ 还原后 `go test ./... -count=1` 全绿、临时备份文件**已删**（★ `internal/httpapi/` 下**已有两个历史 `.bak`**，**勿新增、勿动它们**）。

### T5 · 回执 ＋ 自测

- `bash scripts/check_all.sh` ⇒ **必绿 8/8 ＋ 会报零命中**（★ 本批 `spec/**` 零改动 ⇒ 判据 / Go 包 / 净检出**零波动**）。
- `gofmt -l` 空、`go vet ./...` 干净。
- ★ **回执里必须逐条回答**：① T1–T2 的改动点（`file:line`）；② E1–E5 的实际断言与结果（★ **E1 的 L07 行内容**请贴出来）；③ M1/M2/M3 的红/绿对照；④ **三条纪律的自查**：〔**`severity` 与求值器同批**〕本批**不翻任何 `severity`**（★ 若你发现某条判据**因本批才变得可达**、理应翻 `hard`，★ **在回执里点名，由我方裁** —— **不要自己改 `spec/`**）；〔**注册表按 `id` 索引**〕若你的实现涉及求值器注册表（本批**不应涉及**），必须按 `id` 索引、同名 `id` 不得互相覆盖；〔**判据状态只随实测回填**〕**不得**凭推断登记任何「已支持」。
- ★ **不许静默降级**：有没有哪条**按 §1 无法实现**（★ **有就如实写出来 ＋ 指出冲突点**，**不要**自行改规格、**不要**把断言放宽成「必然成立」的弱断言）。

---

## §2 划界（★ 严格；不在表内的一律不做）

- ★ **不动 `spec/**`** —— `chain.json` 的 `conventions.no_approval_chain` 与四条 `doc_chains.*.no_approval_chain` 由我方维护，**你方只读**；★ **不改 `checks.json`**（仍 **29 条判据 / 11 原语**）。
- ★ **不引入新原语**、**不新增判据** —— 本批是**实现批**，不是规格批。
- ★ **不动 `routes.emergency` 通路**（`N-056` 已具名「本批不做」）—— ★ 也不得顺手给 `emergency` 加 case。
- ★ **不动三方实例推送分支**（★ 若实测出错须**如实回执**，不得静默吞掉、不得新造推送行为）。
- ★ **不动 `SUB`** —— 它有自己的通道 `POST /api/submission`（`handleCreateSubmission`），★ 不得把 `SUB` 纳入本批判定，★ 也不得把 `SUB` 的 `doc_chains` 条目加 `no_approval_chain`。
- ★ **不动** `payment.go`（`PaymentRouteOf`）· `handlers_approval.go` 的表单/硬判据/幂等/回源各段 · `finalizeLedgersTx` 的**数据源**（★ 运行时台账来源仍是配置映射 `maps.LedgerTypesFor(docType)`，源自 `spec/ledger-mapping.json#doc_to_ledger`；★ 本批 **不要求**把它改成直读 `doc_chains.<doc>.ledger` —— 对 `GR`/`QC`/`RFQ`/`BJ` 两者结论本就一致）。
- ★ **不做** `N-053`（升级为正式判据，需新原语、两侧同批）· **不做** `N-054` ② · **不做** `N-049` 三项。
- ★ **不改报错文案**（本批不碰文案面）。

---

## §3 完成判据（三条，缺一不可）

1. ★★ `COLLAB.md` 的 **`### N-056`** 段内写入你的回执（改动文件**显式路径** ＋ E1–E5 结果 ＋ T4 对照表 ＋ 如实项），★ **并在该段内出现字面量 `MIMO-DONE`**（驱动脚本的完成判据①）。★ **议题状态字段仍填 `OPEN`**（结案由我方判定）。
2. **提交并推送** `origin/main`：**显式路径**提交（★ **禁止 `git add -A`**），提交信息带你的署名前缀。
3. 提交前跑 `bash scripts/check_all.sh` ⇒ **必绿 8/8**。

★ 若中途被打断，直接从断点继续；★ 若判定某条**无法按规格实现**，**照样提交已完成部分 ＋ 在回执里如实说明**，**不要**为了「看起来完成」而改动规格或降级语义。
