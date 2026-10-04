# MIMO 任务包 · 2026-10-04 第七轮（批 9 · `N-045` · 紧急采购补审批档位源「就高」）

> **投喂方式**：本文件可**整份粘贴**给 mimo。
> **强制先读**：`COLLAB.md §4` 的 **`N-045`（本批唯一议题）** ＋ `§8` 的 **2026-10-04 09:21 批复** ＋ `REMAINING.md §1` 的 **`B7`/`B8`** 与 `§5` 的 **批 9** ＋ `spec/chain.json` 的 **`routes.emergency`（全）** / **`routes.sole_source.nodes[2].tier_expand`** / **`routes.change.nodes[2].tier_expand`** / **`conventions.tier_source`** ＋ `spec/RESOLUTIONS.md#R-31` ＋ `internal/chain/nodes.go`（全）＋ `internal/chain/chain.go#Facts` ＋ `internal/chain/tier_expand_test.go`（全）。
> **一句话背景**：批 1–8 均已验收闭环（`N-044` 的 `tier_expand` 范式已在 `SS`/`PC` 上落地并通过双向验收）。本批＝**批 9**：`routes.emergency.nodes[4]`（`backfill_approval`）是**第三处惰性必需节点**，且其**档位取值源**已于 2026-10-04 由用户定案为「**就高**」。

---

## 0. ★★ 问题本体（先看这个）

`N-044` **收尾期实测**发现：`spec/chain.json#routes.emergency.nodes[4]` ＝ `backfill_approval`（`required: true`、label「按对应档位补审批（★ **不得降档**）」）的 `actor` 写作**描述性串** `"按档位审批人"` —— 它**既非** `approverRoles`（5 个单一角色 key）、**也非** `isActionActor`（`applicant|system|purchaser`）⇒ 落 `internal/chain/nodes.go:114-115` 的 `appendAction` ⇒ **只记 `NodeName`、不生成 flow 任务**。

★ 与 `N-044` **同源同因**，但 `N-044` 的窄版判据 `S19`（禁复合串 `→`）**对它静默**（该串不含 `→`）⇒ 需要**完整版**判据才能机检。

**另一处更根本的缺口**：**档位从哪来，规格里从未定义**。`N-044` 的两处（`SS`/`PC`）都是**本单金额**定档，而本条语义是「按**对应**档位」—— 照抄 `amount_cents` 会引入一个**未确认的业务口径**，故当时**停手上报**（`N-036`「不抢跑」纪律）。

**用户口径（2026-10-04 原话）**：「**C9 就高**」⇒ 已定案为 **`tier = tier_of(max(补录金额, 关联 PR 金额))`**（裁定 `R-31`）。

---

## 1. ★★★ 我方已落的规格（2026-10-04 · 本批的实现依据）

### 1.1 `routes.emergency.nodes[4]` —— 删描述性 `actor`，改落 `tier_expand`

```json
{
  "seq": 5,
  "id": "backfill_approval",
  "label": "按对应档位补审批（★ **不得降档**）",
  "required": true,
  "tier_expand": {
    "kind": "approval_chain",
    "tier_source": "emergency_max",
    "exclude_roles": ["supervisor", "ops_supervisor"],
    "note": "…（spec 原文，含用户口径与两处刻意差异）"
  }
}
```

★ **注意与我方交付给你的 `SS`/`PC` 两处的差异**（都是**刻意**的）：

| 差异点 | `SS`/`PC`（`N-044`） | 本节点（`N-045`） | 为什么 |
|---|---|---|---|
| 取值源 | `amount_cents` / `r15_max`（**本单**金额） | **`emergency_max`**（**跨单**就高：补录金额 × 关联 PR 金额） | label 是「按**对应**档位」，不是「按本单金额」 |
| 缺值行为 | `r15_max` **允许单边可得取单边**（`N-044` 任务包 T2 授权的 preview 展示退化） | ★★ **两值必须齐全，缺任一 ⇒ 可见失败** | 「**不得降档**」是**单调上界**：单边取值**可能低于应属档位**（例：紧急买 3,000 元，但它属于一张 50,000 元的 PR ⇒ 应属采三档，取单边只会得采二档＝**降档**） |
| `exclude_roles` | `SS`: `[project_general_manager]`；`PC`: `[]` | `[supervisor, ops_supervisor]` | 沿用用户 2026-10-04 去重口径：`supervisor` 已在 `seq2`（主管领导紧急认定）、`ops_supervisor` 已在 `seq6`（核销闭合）各有一个签字点 |

### 1.2 `routes.emergency.rules` —— 档位公式（机读与理由同处）

```json
"rules": {
  "tier_formula": "tier = tier_of(max(补录金额, 关联 PR 金额))",
  "tier_formula_label": "★ 档位就高（不得降档）",
  "tier_formula_reason": "…（spec 原文）",
  "dedupe_note": "…（spec 原文）"
}
```

### 1.3 ★★ `conventions.tier_source` —— **新增的取值约定（本批的逐字依据）**

`spec/chain.json#conventions.tier_source` 原文规定三个受控取值（★ 此前**无约定**，两个取值散落在实现注释里、靠注释解释）：

| 取值 | 语义 | 缺值行为 |
|---|---|---|
| `amount_cents` | **本单金额直接定档** `tier_of(Facts.AmountCents)` | 缺金额 ⇒ **可见失败** |
| `r15_max` | **本单内部就高** `tier_of(max(change_amount_cents, original_contract_amount_cents))`（`R-15`） | ★ **单边可得 ⇒ 取可得侧**；两值皆缺 ⇒ 退 `Facts.AmountCents`；仍缺 ⇒ 可见失败 |
| **`emergency_max`** | **跨单就高** `tier_of(max(补录金额, 关联 PR 金额))`（`R-31`） | ★★ **两值必须齐全，缺任一 ⇒ 可见失败** |

★★ **实现时必须与 `conventions.tier_source` 的 `desc` 逐字对齐** —— 本项目两侧（`spec` 文本 / 代码）语义一致是**硬要求**（`N-021`/`N-022` 立下的规矩）。

---

## 2. 交办内容

### `T1` · `emergency_max` 档位取值源（`internal/chain`）

1. `chain.Facts` 新增字段 **`RelatedPRAmountCents *int64`**（关联 PR 金额，分；语义见 `R-31`）—— ★ 注释请写明它与 `AmountCents`（**补录金额**＝本单实际发生金额）的分工与来源。
2. `resolveTierForExpand` 新增分支 **`case "emergency_max"`**：
   - 取值集 ＝ `{f.AmountCents, f.RelatedPRAmountCents}` 中**非 nil** 的项；
   - ★★ **两者必须都非 nil** —— 缺任一 ⇒ 返回**可见失败**（`ErrAmountMissing`，错误文案须**指名缺的是哪一个**：补录金额 or 关联 PR 金额）；
   - 取 `max` 后 → `TierOf(b, maxV)`；
   - ★ **严禁**照 `r15_max` 写「单边可得取单边」或「退 `AmountCents`」的 fallback（见 §1.1 表第 2 行；`R-31` 已明确这是**刻意不同**）。
3. ★ **不得**在代码里出现档位 id / 阈值字面量（与 `N-044` 同：`TierOf` 读 `thresholds.purchase.bands`，展开源读 `bands[].approval_chain`）。

### `T2` · 展开结果（★ 供你写测试对照，**不是让你写死**）

`routes.emergency.nodes[4]` 的展开 ＝ `bands[tier].approval_chain` **减去** `{supervisor, ops_supervisor}`：

| 补录金额 | 关联 PR 金额 | 就高 | 档位 | `approval_chain` | 实际生成的任务角色 |
|---|---|---|---|---|---|
| 800 元（80000 分） | 600 元（60000 分） | 800 元 | `purchase_tier1` | `[ops_supervisor]` | ★ **空**（`ops_supervisor` 已被剔除 ⇒ 与 `seq6` 核销闭合同为一人、只签一次） |
| 3,000 元（300000 分） | 5,000 元（500000 分） | 5,000 元 | `purchase_tier2` | `[supervisor, project_general_manager]` | `project_general_manager`（`supervisor` 已在 `seq2` 签字、剔除） |
| 30,000 元（3000000 分） | 3,000 元（300000 分） | 30,000 元 | `purchase_tier3` | `[supervisor, project_general_manager]` | `project_general_manager` |
| 3,000 元 | **缺** | — | — | — | ★ **可见失败**（`ErrAmountMissing`） |

★ **"空"是预期结果、不是缺陷** —— 采一档的补审批人与「核销闭合」是同一角色，按用户 2026-10-04 口径只保留一个签字点（`seq6`）。★ 请为这一格写一条**显式用例**（断言：不报错、且该节点不产出审批任务），并在回执里说明你的写法 —— ★ 这样一个"零产出"不会被后人误读成"又惰性了"。

★ **`SourceNodeID` 命名**：沿用 `N-044` 的 `<节点id>_<role>`（如 `backfill_approval_project_general_manager`）—— ★ 与其保持一致，除非你有更强的理由（有 ⇒ 在回执里说明）。

### `T3` · 关联 PR 金额的**生产者** —— ★ **本批只要求 chain 层可测，不要自行造通路**

★★ 事实（请**先核对再动手**）：`routes.emergency` **未被任何 `doc_chains` 引用**，`chain.ResolveRoute` 也**没有** `emergency` 的 case ⇒ 该链**当前不可达**（与 `GR`「定义未接线」同类，属 `REMAINING.md#A8` 面）。

⇒ 本批的要求：

1. **必须**：`internal/chain` 层可测 —— `BuildNodes(b, "emergency", Facts{DocType: …, AmountCents: …, RelatedPRAmountCents: …})` 四条用例（`T2` 的表）逐条成立；
2. **不要**：为了让 handler 能注入 `RelatedPRAmountCents` 而**新造** emergency 的提交/预览通路、或改 `ResolveRoute`/`doc_chains` —— ★ 那属 `A8` 接线批，**做了会返工**；
3. ★ 若你在实现过程中发现「不接线就没法写出可证伪的用例」⇒ **如实回执**你的困难与建议，★ 不要静默降低要求。

### `T4` · ★ **证伪对照（必须做）** —— 本批的核心交付

按 `N-043` 立下的规矩：**"能跑"不算数，"改回缺陷版会红"才算数**。请为自己新增的测试**各自**做一次单点变异并记录：

1. **变异 A**：把 `emergency_max` 的「**两值必须齐全**」改成「**单边可得取单边**」（即 `r15_max` 写法）⇒ ★ **「缺关联 PR 金额」那条用例必须转红**（会静默降档）；
2. **变异 B**：把 `emergency_max` 改成**只取 `AmountCents`**（＝照抄 `amount_cents`）⇒ ★ **`T2` 第 2 行（紧急 3,000 / PR 5,000 ⇒ 应采二档）必须转红**；
3. **变异 C**：把 `exclude_roles` **改为空**（不去重）⇒ ★ **`T2` 第 1/2 行必须转红**（会分别多出 `ops_supervisor` / `supervisor` 任务）。

★ 每次变异**只改一处**，并报告「**红的是哪些、绿的是哪些**」（隔离性）。★ 变异后**一律用 `cp` 备份还原**，**禁止 `git checkout --`**（`N-041` 已有事故先例）。

### `T5` · 顺手订正（`N-046` 遗留⑦，非阻塞、一并做掉）

`internal/dashboard/dashboard.go` 与 `internal/dashboard/dashboard_key_align_test.go` 的注释写「12 个指标分布在 **7** 个台账上」—— ★ **实测应为 6**（`source_ledgers = L01,L02,L03,L06,L09,L12`）。★ 只改注释、**不改逻辑**；改完在回执里列出**改了哪几行**。

### `T6` · ★ 本批**不做**（明确划界，做了会返工）

1. **不要**动 `spec/` 与 `docs/`（我方域）—— 发现的规格问题**写进回执**；
2. **不要**做 `emergency` 的 `ResolveRoute` / `doc_chains` 接线（属 `A8`）；
3. **不要**顺手做 `GR`/`RFQ`/`QC`/`BJ` 的通路（同属 `A8`）；
4. **不要**改 `env_count`（`conventions.env_count` 明写「语义待定、不得依赖」）；
5. **不要**碰 `S19` 判据（★ 判据是我方域：你实现落地并通过验收后，由我方把它从**窄版**升级为**完整版「禁惰性必需节点」**）；
6. **不要**处理 `spec/acceptance.csv` / `N-047` 里的判据承载问题（★ 那是**下一批**的「我方补 `severity` ＋ 你注册求值器」**同批**工作，改动面完全不同）。

---

## 3. 纪律（照旧，逐条生效）

1. **工作范围仅限本仓库目录**（`C:\Users\haoduan\workspace\jx-procurement-platform`），不得越界。
2. ★★ **以 `COLLAB.md` 为准**（`§4 N-045` ＋ `§8` 2026-10-04 批复 ＋ `§1` 当前状态）；与你的会话记忆冲突时，**以台账为准**。
3. 提交前跑 `bash scripts/check_all.sh` —— **必绿 8/8**（会报项**零命中**为佳）。
4. ★ **只用显式路径提交**（**禁止 `git add -A` / `git add .`**）；提交后 `git show --stat HEAD` **核对清单**。
5. 完成后**回写 `COLLAB.md` 的 `N-045` 段**（状态改 `MIMO-DONE` ＋ 追加回执）并**推送 `origin/main`**。
6. ★ 测试要求：**每条硬约束都要「应拦 ＋ 应放行」两个方向**（★ 本批的"应拦"＝**缺陷版必须转红**，见 `T4`；"应放行"＝**合法值必须通过**）。
7. ★ **不要动 `spec/` 与 `docs/`**（我方域）；发现的规格问题**写进回执**，不要自行改规格。
8. ★ 台账时间戳：**取 `git log --format=%ci` 的提交时间**（或 `date`），**禁止凭估计顺推**。
9. ★★ **你的判据③不会因 spec 缺失而红** —— 我方的 `spec` 改动（`tier_expand` / `conventions.tier_source` / `R-31`）**已先行入库并推送**（这是 `N-046` 那轮实测出的教训：`drive_mimo.sh` 的判据③含「净检出可构建」⇒ **测 HEAD**）。⇒ 若你**仍然**遇到「净检出可构建」红，请**先核对 HEAD 是否已含我方递交**，并在回执里**如实报告**，不要自行代提我方文件。

---

## 4. 完成判据（四条全满足才算完成）

1. `buildnodes("emergency", …)` 的 `backfill_approval` **真的按档位产出审批任务**（`T2` 的四行逐条成立，含「采一档 ⇒ 空」那条）；★ 且 `emergency_max` 的**缺值 ⇒ 可见失败**成立；
2. `T4` 的**三条单点变异各自精确转红**，且**隔离性**已报告；
3. `bash scripts/check_all.sh` **退出码 0（必绿 8/8）**，已推送 `origin/main`；
4. `COLLAB.md` 的 `N-045` 段出现 `MIMO-DONE` ＋ 回执（含 `T2` 四行的实测结果、`T3` 的处置、`T4` 三个变异的红/绿清单、`T5` 的改动行号）。

★ 中途被打断 ⇒ **从断点继续**，已完成的项**不要重做**。
