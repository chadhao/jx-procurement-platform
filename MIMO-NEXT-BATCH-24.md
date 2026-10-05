# MIMO-NEXT-BATCH-24 —— `N-062`「声明-执行」收口（`pending_*` 19 项清零 · 一次做完）

> **交付方**：WorkBuddy（制度规范与产品设计方）
> **执行方**：mimo code
> **日期**：2026-10-06（批 34）
> **前置**：`N-060` 已 `AGREED` 结案；`N-061` 只剩 `④`（**我方域**，不在本包）。
> **★ 动手前必读**：`COLLAB.md#N-062`（本包的规格正文）· `spec/checks.json` **V1.24**（★ `S14.args.allowed` 已增 `accepted_gap`）。

---

## 0. 硬约束（沿用，勿破）

- ★ **工作范围**：只能在 `C:\Users\haoduan\workspace\jx-procurement-platform` 目录内读写。
- ★ **以台账为准**：冲突时一律以 `COLLAB.md` / `spec/` 当前内容为准。
- ★ **提交只用显式路径**（**禁止 `git add -A`**）；提交后 `git show --stat HEAD` 复核。
- ★ **每次提交前跑 `bash scripts/check_all.sh`，必绿基线 9/9**。
- ★ **还原用 `cp` ＋ `sha256sum -c`**，**不得用 `git checkout -- <file>`**。
- ★ **一次只变异一处**；★ **变异「仍绿」不能判通过**（先确认变异处**是否真被执行**）。

---

## 1. 总纲：这 19 项**只有三种正当归宿**

| 归宿 | 写法 | 含义 |
|---|---|---|
| **① 补执行体** | `carried_by_kind: "code"`（或 `submit`） | 有数据源/通路 ⇒ **实现它**，并附可机检用例 |
| **② 改声明** | 改 `source`（如 `system` → `user`）等 | 声明与事实不符 ⇒ **把声明改成事实** |
| **③ 具名豁免** | ★ **`carried_by_kind: "accepted_gap"`** | 本期确无数据源/通路 ⇒ **显式登记 + 写明理由**，从此**不再计为缺口** |

★★ **不允许的第四种 ＝ 继续挂着 `pending_implementation` / `pending_wiring`** —— 那等于「**声明了什么都不发生**」（本项目认定的最危险形态）。

★ **顺序不可颠倒**：**`J1` → `J2` → `J3`**。

---

## `J1` · 字段级 10 条（`spec/forms/*.json` 的 `sections[*].fields[*]`）

**你要做的**：**逐条先复核**（语义 ＋ **系统里有没有可用数据源**），再**三选一定性**并改 `carried_by_kind`（或 `source`）。
★ **逐条回执**：字段 ｜ 今态证据（**文件:行号**）｜ 三选一 ｜ 依据 ｜ 未做原因。

★ **我方预判（供你复核 —— ★ 与预判不符请在回执里说明并给证据，不要照抄）**：

| 字段 | 我方倾向 | 依据 |
|---|---|---|
| `BA.anti_split_check_result` | **① 补执行体** | 拆单检查**已在看板 16 落地**（`internal/dashboard` 的 `split_suspicion`）⇒ **有数据源**；★ 且属**内控红线项** |
| `CT.approval_levels` | **① 补执行体** | 审批链**已服务端算**（`chain.Compute`）⇒ 级数可派生 |
| `PC.tier_approval_record` | **① 补执行体** | 同上（档位与链可比对） |
| `SS.tier_chain_record` | **① 补执行体** | 同 `CT.approval_levels` |
| `SA.actual_vs_approved_diff_cents` | **① 补执行体** | SA 实际额与批准额均在 ⇒ 差额可算（`computed`） |
| `BA.record_date` | **① 或 ②** | 若＝单据日期/提交日 ⇒ 可派生；若＝人工填 ⇒ 改 `user`。★ **你复核后定** |
| `BA.petty_cash_receiver` | **② 改声明** | 语义是「**领取人**」⇒ 多为**表单填写** ⇒ `source` 由 `system` 改 `user` |
| `BA.is_key_sample_range` | **② 或 ③** | **先复核**（是否有判定源） |
| `PR.stock_qty` | ★ **③ 具名豁免** | ★ **系统无库存模块**（全仓无库存源）⇒ 本期**不可能有生产者** |
| `BA.is_monthly_supplier_rollover_warned` | ★ **③ 具名豁免** | ★ **无对应机制**（按月滚动提醒未建） |

★ **③ 的写法要求**：`carried_by` 必须写明 **「为什么本期不做」**（不是「以后再说」，而是**缺什么**：无数据源 / 无通路 / 制度未定）。

---

## `J2` · 判据级 **6 条 `soft`** —— ★ **一个机制一次性解决**

★★ **共同根因（已实测）**：`evaluateHardChecks` **只对 `severity == hard` 放行、其余 `continue`** ⇒ ★★ **所有 `soft` 判据都没有执行体**（「**提示通道**」从未建）。

涉及：`GR#inspection_vs_conclusion_hint` · `QC#no_duplicate_qc_for_same_batch` · `RFQ#response_shortfall_warning` · `SA#counterparty_conditional` · `SS#fixed_asset_conflict` · `SUB#submit_deadline_warning`

**你要做的 —— 建「提示通道」**（`soft` 判据的统一执行体 ＋ 落点与呈现）：

| # | 要求 |
|---|---|
| ① | 提交期 `soft` 判据**被执行**（不再 `continue` 跳过） |
| ② | 结果**可见**（如 `warnings[]` 随提交响应返回 ＋ 前端呈现） |
| ③ | **不阻断**（语义 ＝ 提示） |
| ④ | ★★ **与 `hard` 严格区分** —— 本项目铁律：**「超期不予受理」≠「超期计入预警」**（`SUB#submit_deadline_warning` 的 `critical_note` 明文） |

★ `SA#counterparty_conditional` 的**触发条件**：「对外支付」**可表达**；★ 「**需要开票**」字段**待你复核**（与 `N-054 ②` 同源）⇒ 若确认不可表达，**停在议题里点名**。

---

## `J3` · 判据级 **3 条 `pending_wiring`**（通路未接）

| 判据 | `when` | 缺什么（你复核） |
|---|---|---|
| `BA#receipt_per_purchase` | `approval(return_receipt)` | `chain.json#routes.purchase_tier1.nodes[5]` 的 `return_receipt` 节点**无入口** |
| `SA#actual_not_exceed` | `backfill(settlement_backfill)` | 后置补录段入口未建 |
| `SA#invoice_must_link` | `backfill(settlement_backfill)` | 同上 |

**你要做的**：**先复核「到底缺什么」**（节点 / 补录入口 / 调度），★ **优先给出「最小可验收」的通路**；★★ 若确认**必须新建 UI 或节点**（工作量不可控）⇒ **停在议题里给方案 ＋ 工作量估计**，**不许半成品提交**。

---

## 2. 明确**不做**

| 项 | 原因 |
|---|---|
| `N-061 ④`（`docs/05-API` 孤儿契约行 ＋ `openapi` 重生成 ＋ 计数 `69`） | ★ **我方域**，单列一轮 |
| `FR-M8-07` / `M8-09` / `M8-04` | ★ 非应用代码（反代 / 部署 / 运维流程） |
| C 档（`Q17`/`Q1`/`Q2`/`Q4`/`Q19` ＋ 待决策 A 组） | ★ 待外部输入 |

---

## 3. 交付要求

1. 每完成一族跑 `bash scripts/check_all.sh`，**必绿 9/9**。
2. ★★ **`spec/forms` 里 `pending_implementation` 必须归零**；`pending_wiring` 若 `J3` 未做完，须**具名保留并说明**。
3. ★ 每条「① 补执行体」**附可机检用例**；★ 每条「③ 具名豁免」在 `carried_by` 写明**理由**。
4. ★ 新增判据 / 原语须**两侧同批**（**引擎侧先、清单侧后**，无红窗）。
5. 提交**只用显式路径**；完成后在 `COLLAB.md#N-062` 追加**逐条回执**（`J1` 10 行 ＋ `J2` 6 行 ＋ `J3` 3 行，含证据 `文件#函数` ＋ 单点变异结果 ＋ 未做原因），状态改 **`MIMO-DONE`**。
6. 推送 `origin/main`。
7. ★ **做不完**：按 `J1` → `J2` → `J3` 交付**可独立验收的最小完整部分**，**如实登记剩余**，**不得留半成品提交**。
8. ★ 需要我方裁定才能定的口径，**停在议题里写清楚，不要猜**。
