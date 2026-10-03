# MIMO 任务包 · 2026-10-04 第六轮（批 8 · `N-044` · 惰性必需节点修复）

> **投喂方式**：本文件可**整份粘贴**给 mimo。
> **强制先读**：`COLLAB.md §4` 的 **`N-044`（本批唯一议题）** ＋ `REMAINING.md §1` 的 **`B7`** 与 `§5` 的 **批 8** ＋ `spec/chain.json` 的 `thresholds.purchase.bands[*].approval_chain` / `routes.sole_source.nodes[2].tier_expand` / `routes.change.nodes[2].tier_expand` ＋ `internal/chain/nodes.go`（全）＋ `internal/chain/chain.go#Facts`。
> **一句话背景**：批 1–4 均已验收闭环。本批＝**批 8**：`SS`/`PC` 的两条**档位审批整级缺失**（`tier_chain` / `tier_approval` 是 `required: true` 的**惰性节点**）。

---

## 0. ★★ 问题本体（先看这个）

`N-043` 验收期的**证伪对照实测**发现：给 `PC×tier_approval` 加必填规则 ⇒ 六例测试**全绿** ⇒ 反证**该节点根本不生成审批任务**。

**根因（已定位，非猜测）**：
- `internal/chain/nodes.go:10-16` 的 `approverRoles` 只含 **5 个单一角色 key**；
- `routes.sole_source.nodes[2].actor` 与 `routes.change.nodes[2].actor` 都是**复合串** `"supervisor → project_general_manager"` ⇒ 既**非**审批角色、也**非** `isActionActor`（`nodes.go:218-225`，只认 `applicant|system|purchaser`）⇒ 落 `nodes.go:104-105` 的 `appendAction` ⇒ **只保留 `NodeName`、不生成 flow 任务**；
- 同节点的 `ref: "routes.<对应档位>"` 是**伪引用**（`nodes.go:78` 只认 `contract_two_level` 一个 ref）⇒ **永远不被展开**。

**后果**：`SS` 少掉「按对应档位审批」整级；`PC` 少掉「按该档位审批」整级 —— ★ **PC 全链连一个主管/总经理签字点都没有**，只剩综合运营主管的 `ledger_submit`。这与 `chain.json` 的 `chain_rule`（SS「按对应档位的审批链正常审批，**由项目总经理终审**」）与 `docs/02-UseCase.md#UC-09`（步骤 3）**明写冲突**。

---

## 1. ★★★ 我方已落的规格（`spec/chain.json`，2026-10-04 · 本批的实现依据）

### 1.1 新增 `thresholds.purchase.bands[*].approval_chain`（该档位的**审批层级序列**）

| 档位 id | `approval_chain` | 依据（`approval_chain_basis` 原文在 spec） |
|---|---|---|
| `purchase_tier1` | `["ops_supervisor"]` | `routes.purchase_tier1.nodes[approve_petty_cash]`（采一档＝备付金审批） |
| `purchase_tier2` | `["supervisor","project_general_manager"]` | `routes.purchase_tier2.nodes`（主管领导 → 合同两级） |
| `purchase_tier3` | `["supervisor","project_general_manager"]` | `routes.purchase_tier3.nodes`（主管领导审批/复核 → PGM 确认 → 合同两级） |

★★ **两条语义铁律**（`thresholds.purchase.approval_chain_note` 原文）：
1. **只列审批人角色序列，不含动作环节** —— 申请人动作 / 系统校验 / 拨付备付金 / 询比价 / 初审 / 提交集团**都不在此列**（★ 所以**不要**从 `routes.purchase_tierN.nodes` 里"挑审批节点"来当展开源：那样会把 `disburse`（拨付）等流程环节一起捞进来）。
2. **已按角色去重** —— 同一角色在档位链上多次出现（采三档的 supervisor 初审＋复核、PGM 拟成交确认＋合同二级）**只列一次**。依据＝**用户 2026-10-04 口径**：「流程上有重复的签批人，都是一次签批」。

### 1.2 新增节点级 `tier_expand`（**取代**复合 `actor` 与伪 `ref`）

`routes.sole_source.nodes[2]`（`tier_chain`）：

```json
"tier_expand": {
  "kind": "approval_chain",
  "tier_source": "amount_cents",
  "exclude_roles": ["project_general_manager"],
  "note": "…（spec 原文，含用户口径的依据）"
}
```

`routes.change.nodes[2]`（`tier_approval`）：

```json
"tier_expand": {
  "kind": "approval_chain",
  "tier_source": "r15_max",
  "exclude_roles": [],
  "note": "…（spec 原文）"
}
```

**键语义**：
| 键 | 含义 |
|---|---|
| `kind` | 固定 `approval_chain`（本批唯一取值） |
| `tier_source` | 档位取值源：`amount_cents` ＝ 同 `chain.TierOf(f.AmountCents)`；`r15_max` ＝ **R-15 就高** `tier_of(max(change_amount_cents, original_contract_amount_cents))`，与 `chain.ChangeTierOf` 同源 |
| `exclude_roles` | 展开后**剔除**的角色（★ SS ＝ `[project_general_manager]`：SS 链 `nodes[3]=pgm_final` 同为 PGM ⇒ **全链 PGM 签字点唯一＝`pgm_final`**，落实用户「重复签批人只签一次」的口径） |

### 1.3 ★ 两旧键的处置（**本批不要动**，由我方收尾）

`actor`（复合串）与 `ref`（伪引用）**暂时保留**在 spec 里 —— ★ 这是**我方刻意**的选择：删它们必须与"新增判据（禁惰性必需节点）"**同批**，而判据要等你的实现到位后才不会当场红。

⇒ ★ **你按 `tier_expand` 实现即可**；`actor`/`ref` 两键**当作不存在**。我方在你实现验收通过后删除两键 + 落判据（判据编号拟 `S19`）。

---

## 2. 交办内容

### `T1` · `SS` 的 `tier_chain` 展开（`sole_source`）

按 §1.2 的 `tier_expand` 展开为**审批任务**：

1. **档位**：`tier_of(SS.amount_cents)`（`tier_source=amount_cents`）。
2. **取链**：`thresholds.purchase.bands[id=<该档位>].approval_chain`。
3. **剔除** `exclude_roles`。
4. **逐角色** `appendApproval`（★ 复用既有 `appendApproval`，**不新造范式**）。

★ **展开结果（供你写测试对照，不是让你写死）**：

| SS 金额 | 档位 | 展开前 `exclude_roles` | 实际生成的任务角色 |
|---|---|---|---|
| 999 元（99900 分） | `purchase_tier1` | — | `ops_supervisor` |
| 1,000 元（100000 分） | `purchase_tier2` | 剔 PGM | `supervisor` |
| 6,000 元（600000 分） | `purchase_tier3` | 剔 PGM | `supervisor` |

⇒ ★ **SS 全链审批任务**（展开后，按 `seq`）＝ `tech_opinion`(inspector_group) → **`tier_chain` 展开项** → `pgm_final`(PGM) → `ledger_and_report`(ops_supervisor)。

### `T2` · `PC` 的 `tier_approval` 展开（`change`）

1. **档位**：`tier_source=r15_max` ⇒ `tier_of(max(change_amount_cents, original_contract_amount_cents))`。
2. 其余同 `T1`。`exclude_roles` 为空（PC 链无 `pgm_final`，无重复签批面）。

★ **展开结果**：

| 变更后全额（就高） | 档位 | 生成的任务角色 |
|---|---|---|
| ≤999 元 | `purchase_tier1` | `ops_supervisor` |
| 1,000–5,000 元 | `purchase_tier2` | `supervisor` ＋ `project_general_manager` |
| >5,000 元 | `purchase_tier3` | `supervisor` ＋ `project_general_manager` |

★★ **一处前置依赖，请**如实处置**（不要猜）**：`original_contract_amount_cents` 由 `injectPCSSSystemFields` 在**提交期**从 L04 等值反查后注入；而 `chain.BuildNodes` 在 **preview 时点**也会被调用 ⇒ ★ **preview 时该值可能尚未就绪**。⇒ 请：
- 若能在 preview 时点从 L04 取到 ⇒ 正常按就高定档；
- 取不到 ⇒ **如实回执**你选定的行为（例如 fallback 到 `change_amount_cents` 单值 / 或可见失败），★ **不要静默假定**，也不要自行造一个业务口径。

### `T3` · `SourceNodeID` 命名（★ 你必须自定，但要**可派生、不硬编码**）

展开后**一个链节点可能产出多个审批任务**（PC 采二/三档出 2 个）。请给出 `RoleNode.SourceNodeID` 的命名规则，要求：
- **可由 spec 派生**（如 `<节点id>:<role>`），**不得在代码里写死角色字面量**；
- 与既有 `expandContract` 的命名风格（`contract_supervisor` / `contract_pgm`）**同族**；
- ★ 说明它与 `flow` 任务、`nodeFieldSpecFor`（`internal/flow/designation.go`）的**匹配关系** —— 本批**不要求**给展开出的节点加必填字段（既有规则只有 `SS×tech_opinion` / `SS×pgm_final` / `PC×ledger_submit`），但**要保证不误伤**（展开节点 id 不应意外命中既有规则）。

### `T4` · ★ **证伪对照（必须做）** —— 这是本批的核心交付

按 `N-043` 立下的规矩：**"能跑"不算数，"改回缺陷版会红"才算数**。请为自己新增的测试**各自**做一次单点变异并记录：

1. **变异 A**：把 `tier_expand` 消费逻辑**停用**（退回 `actor` 复合串 ⇒ `appendAction`）⇒ 相关测试**必须转红**；
2. **变异 B**：把 `exclude_roles` **改为空**（不去重）⇒ SS 采二/三档的用例**必须转红**（会多出一个 PGM 任务）；
3. **变异 C**：把 `tier_source` 的档位判定**固定成 `purchase_tier1`** ⇒ 档位相关用例**必须转红**。

★ 每次变异**只改一处**，并报告"**红的是哪些、绿的是哪些**"（隔离性）。★ 变异后**一律用 `cp` 备份还原**，**禁止 `git checkout --`**（`N-041` 已有事故先例）。

### `T5` · 既有测试同步（★ 会红，属预期）

`internal/httpapi/ss_pc_submit_test.go` 的 `TestSSSubmitEndToEnd` / `TestPCEndToEnd` / `TestPCLedgerSubmitNodeBidirectional` **按"现实任务集"写死了推进步数**（`N-043` 回执 T4 ①）⇒ ★ 展开生效后它们**必然红**。请**同步更新**（改成"按实际生成的任务推进"或按展开后的任务集断言），并在回执里**逐条说明改了哪几处、原断言是什么**。

★ 同时**更新注释**里的"M2 规则：`tier_chain` 不生成任务"等**已过时表述** —— 本项目最忌"注释断言了缺陷"。

### `T6` · ★ 本批**不做**（明确划界，做了会返工）

1. **不要**动 `spec/` 与 `docs/`（我方域）；
2. **不要**处理 `SS.tier_chain_record` / `PC.tier_approval_record` 两个字段 —— ★ 我方已发现它们的 `carried_by_kind=pending_implementation` 与 spec 自述存在**语义矛盾**（`SS.json` 段级 `_note` 写「节点③是**继承档位链**、**不产生本单字段**」，而字段却登记为 `source=system` ＋ `required=true`）⇒ ★ **由我方另行定稿**（撤销 or 补语义），**本批不派**；
3. **不要**顺手做 `GR`/`RFQ`/`QC`/`BJ` 的通路（属 `REMAINING.md#A8`）；
4. **不要**改 `env_count`（另有 `conventions.env_count` 明写"语义待定、不得依赖"）。

---

## 3. 纪律（照旧，逐条生效）

1. **工作范围仅限本仓库目录**（`C:\Users\haoduan\workspace\jx-procurement-platform`），不得越界。
2. ★★ **以 `COLLAB.md` 为准**；与你的会话记忆冲突时，**以台账为准**。
3. 提交前跑 `bash scripts/check_all.sh` —— **必绿 8/8**（会报项**零命中**为佳）。
4. ★ **只用显式路径提交**（**禁止 `git add -A` / `git add .`**）；提交后 `git show --stat HEAD` **核对清单**。
5. 完成后**回写 `COLLAB.md` 的 `N-044` 段**（状态改 `MIMO-DONE` ＋ 追加回执）并**推送 `origin/main`**。
6. ★ 测试要求：**每条硬约束都要「应拦 ＋ 应放行」两个方向**；★ 本批的"应拦"＝**缺陷版必须转红**（见 `T4`）。
7. ★ **不要动 `spec/` 与 `docs/`**（我方域）；发现的规格问题**写进回执**，不要自行改规格。
8. ★ 台账时间戳：**取 `git log --format=%ci` 的提交时间**（或 `date`），**禁止凭估计顺推**。

---

## 4. 完成判据（四条全满足才算完成）

1. `SS` / `PC` 的档位审批节点**真的生成审批任务**（handler 级端到端可证）；
2. `T4` 的**三条单点变异各自精确转红**，且**隔离性**已报告；
3. `bash scripts/check_all.sh` **退出码 0（必绿 8/8）**，已推送 `origin/main`；
4. `COLLAB.md` 的 `N-044` 段出现 `MIMO-DONE` ＋ 回执（含 `T2` 前置依赖的处置、`T3` 命名规则、`T5` 改动清单）。

★ 中途被打断 ⇒ **从断点继续**，已完成的项**不要重做**。
