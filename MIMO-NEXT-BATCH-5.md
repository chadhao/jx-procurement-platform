# MIMO 任务包 · 2026-10-04 第五轮（批 4 · `A4`）

> **投喂方式**：本文件可**整份粘贴**给 mimo。
> **强制先读**：`COLLAB.md §4` 的 **`N-043`（本批唯一议题）** ＋ `REMAINING.md §5`（批次顺序）＋ `spec/forms/SS.json` / `spec/forms/PC.json` ＋ `spec/chain.json#doc_chains.SS|PC` ＋ `internal/flow/designation.go#nodeFieldSpecFor`。
> **一句话背景**：批 1（`B1+B2`）· 批 2（`N-041`）· 批 3（`N-042`）**均已验收闭环**。本批＝`REMAINING.md` 的 **批 4 · `A4`**：`SS` / `PC` 的**提交通道**端到端打通与验证。

---

## 0. ★★ 我方只读核查结论 —— 先看这个（决定本批做什么、不做什么）

`N-027` 登记的未做项是「`SS` / `PC` 的**提交通道**与**审批时点判据**」。逐条核查（2026-10-04，只读）后：

| 分项 | 现状 | 证据 |
|---|---|---|
| **审批时点判据** | ✅ **已在位** | `N-038` ③④ 闭环：`internal/flow/designation.go#nodeFieldSpecFor` **表驱动** —— `SS×tech_opinion`（`tech_opinion` 必填 ＋ `*_by/at` 系统带入）· `SS×pgm_final`（`pgm_final_opinion` 必填）· `PC×ledger_submit`（`resubmitted_to_group_at` 必填） |
| **链算层** | ✅ **已在位** | `internal/chain/route.go:73` `case DocSS, DocPC` —— 线取自 `doc_chains.<X>.route`（`SS→sole_source` / `PC→change`） |
| **提交处理器** | ✅ **是通用的** | `internal/httpapi/handlers_approval.go#handleApprovalSubmit` 由 `d.Spec.Forms[body.DocType]` 驱动，**不按单据硬分支** |
| ★ **端到端证据** | ❌ **从来没有过** | `internal/httpapi` 里**唯一**打 `POST /api/approval/submit` 的测试是 `handlers_approval_submit_m4_test.go`，而它**只覆盖 `BA`**（`baSubmitBody`）；`SS`/`PC` 现有测试都是**函数级**（`pcss_inject_test.go` / `handlers_hardchecks_n027_test.go`） |

★★ **为什么这一条不能算「已经好了」**：
- 本项目 `§1` 门禁状态栏的既有判据是「**门禁绿 ≠ 规格被覆盖**」；
- `N-040` 的教训正是「探针在 Go 里**手搓 `body`** ⇒ 只覆盖服务端逻辑、**不覆盖客户端实际发什么**」；
- ⇒ ★ **「`SS`/`PC` 能不能经 HTTP 一路提交成功」从未被证明过**，而这正是「**提交通道**」这个待办的全部内容。

★ **同族但不在本批**：`N-042` 回执如实登记的边界①「`GR` HTTP 端到端随通路批」＝ `REMAINING.md#A8`（`GR`/`RFQ`/`QC` 发起通路接线）。⇒ ★ **本批不要顺手做 `GR`/`RFQ`/`QC`/`BJ`**，它们属 `A8`，我方尚未出具它们的通路口径（`doc_chains.GR` 无 `route`；`RFQ`/`BJ`/`QC` 是 `env_count: 0` 的 `no_chain`）。

---

## 1. 交办内容

### `T1` · `SS` 提交通道（handler 级端到端）

★ 复用 `handlers_approval_submit_m4_test.go` 的装配方式（`httptest` + 真实 `Deps`），**不要新造夹具范式**。

1. **正例**：按 `spec/forms/SS.json` 构造合法载荷 → `POST /api/approval/submit` ⇒ 出 `biz_no`（前缀 `SS`，格式按 `number_format`）→ `t_instance` 落库 → **`L09` 落行**（`exception_type = 独家采购`）。
2. ★★ **节点时点双向**（`SS×tech_opinion`，走 `POST /api/approval/:biz_no/approve`）：
   - **应拦**：缺 `tech_opinion` ⇒ **400**（`ErrInvalidNodeField` 的文案）；
   - **应放行**：填了 ⇒ 通过，且 `tech_opinion_by` / `tech_opinion_at` 由**服务端带入**（★ 断言是**服务端权威值**，不是客户端传进去的值）。
3. ★ **`SS×pgm_final` 同理**：缺 `pgm_final_opinion` ⇒ 400；填了 ⇒ 放行。

### `T2` · `PC` 提交通道（handler 级端到端）

1. **正例**：先备一张**已存在**的 `CT`（`L04` 行）→ 提交 `PC`（字段按 `spec/forms/PC.json`，含 `contract_no`）⇒ 出 `biz_no` → **`L09` 落行**（`exception_type = 采购变更`）→ ★ `finalizeLedgersTx` 的 **`L09` 6 列自检无告警**（无 `ledger_l09_column_missing` 审计）。
2. ★ **`L04` 查不到 ⇒ fail-closed**（`injectPCSSSystemFields` 既有语义）—— 必须被**端到端断言**覆盖（不是函数级）。
3. ★ **`PC×ledger_submit` 节点时点双向**：缺 `resubmitted_to_group_at` 走该节点同意 ⇒ **400**；填了 ⇒ 放行。
   ☆ 依据：`PC#approval_and_filing.filled_at = "node3_tier_approval_and_node4_ledger_submit"` ＋ `filled_at_note`（`N-038-附三`）—— ★ **只在 `node4 ledger_submit` 拦，`node3 tier_approval` 不拦**。

### `T3` · ★ 路径上任何一处打不通 ⇒ **修到通**

典型嫌疑（**逐一实测，不要只看代码**）：`GET /api/approval/meta` 的 `doc_types_available` 是否含 `SS`/`PC` · `approval_code ↔ doc_type` 映射是否覆盖 · `EnsureResolvable` 对 `SS`/`PC` 的候选角色能否解析 · `chain.BuildNodes` 对 `sole_source` / `change` 两条 route 是否齐节点。

★ 修法一律循**既定范式**：`spec` 驱动 · fail-closed · **不硬编码** · **不新造业务口径**。

### `T4` · ★ 若确有规格缺口 ⇒ **如实回执，不猜着实现**

例：`SS`/`PC` 某字段在 `spec` 里没有取值来源 · 某 route 的节点清单在 `chain.json#routes` 缺项 · 某判据的 `when` 无承载者。

★ 处置＝**如实登记**到 `N-043` 回执（**附证据：文件#行号 + 命令输出**），**停手不抢跑**，由我方补规格后再同批 —— ★ 这与 `N-036`「不抢跑 `node3`」是同一处置，我方认可那种处置。

---

## 2. 纪律（照旧，逐条生效）

1. **工作范围仅限本仓库目录**（`C:\Users\haoduan\workspace\jx-procurement-platform`），不得越界。
2. ★★ **以 `COLLAB.md` 为准**；与你的会话记忆冲突时，**以台账为准**（会话建于 2026-09-28，此后 spec 已多轮演进：`N-036` 结案 · `N-038` 开且部分闭环 · `checks.json` 已至 V1.10 · `N-042` 结案）。
3. 提交前跑 `bash scripts/check_all.sh` —— **必绿 8/8**（会报项**零命中**为佳）。
4. ★ **只用显式路径提交**（**禁止 `git add -A` / `git add .`**）；提交后 `git show --stat HEAD` **核对清单**。
5. 完成后**回写 `COLLAB.md` 的 `N-043` 段**（状态改 `MIMO-DONE` ＋ 追加回执）并**推送 `origin/main`**。
6. ★ 测试要求：**每条硬约束都要「应拦 ＋ 应放行」两个方向**（只测应拦会漏**误拦**，误拦与漏拦同样致命）。
7. ★ **不要动 `spec/` 与 `docs/`**（我方域）；发现的规格问题**写进回执**，不要自行改规格。
8. ★ 台账时间戳：**取 `git log --format=%ci` 的提交时间**（或 `date`），**禁止凭估计顺推**。

---

## 3. 完成判据（三条全满足才算完成）

1. `COLLAB.md` 的 `N-043` 段内出现 `MIMO-DONE` ＋ 回执；
2. 有**新提交**（显式路径，非 `[WorkBuddy]` 前缀）；
3. `bash scripts/check_all.sh` **退出码 0（必绿 8/8）**，且已推送 `origin/main`。

★ 中途被打断 ⇒ **从断点继续**，已完成的项**不要重做**。
