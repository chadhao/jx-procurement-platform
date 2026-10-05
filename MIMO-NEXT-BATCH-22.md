# MIMO-NEXT-BATCH-22 —— `N-060` 第四部分（残余收尾 · 一次做完）

> **交付方**：WorkBuddy（制度规范与产品设计方）
> **执行方**：mimo code
> **日期**：2026-10-05（批 30）
> **前置**：`N-060` 第一部分（`T1`–`T4`）· 第二部分（`F1`–`F13`）· 第三部分（`G1`/`G2`）**均已验收通过**；★ 本包只做**残余收尾**。
> **★ 动手前必读**：`COLLAB.md#N-060`（含本包对应进度块）· `spec/RESOLUTIONS.md#R-35`（**本轮新增裁定，本包的规格依据**）· `docs/19-Permission-Points-Spec.md`（权限位点规格）。

---

## 0. 硬约束（沿用，勿破）

- ★ **工作范围**：只能在 `C:\Users\haoduan\workspace\jx-procurement-platform` 目录内读写。
- ★ **以台账为准**：若台账与你的会话记忆冲突，**一律以 `COLLAB.md` / `spec/` 当前内容为准**。
- ★ **提交只用显式路径**（**禁止 `git add -A` / `git add .`** —— 双方共用同一工作区）。
- ★ **每次提交前跑 `bash scripts/check_all.sh`，必绿基线 9/9**（含「常驻探针回归 12/12」）。
- ★ **跑任何探针后必查** `git status --short` 与 `git diff --stat spec/`，确认**零残留**。
- ★ **还原用 `cp` 备份 ＋ `sha256sum -c`**，**不得用 `git checkout -- <file>`**。
- ★ **一条判据只声称一个时点**；新增判据前先确认调度器对它是**跳过**还是 **fail-closed**。

---

## H1 · `G3` 原件登记**消费侧接线**（`FR-M6-05` · ★ 依 `R-35`）

**背景**：批 29 你方**停手点名**（判为**正确**）—— `R-34` 把两字段落在 `SUB#handover_register`（**表单段**），与「不得新造端点/表/列」×「提交即落库回读」**三者互斥**。★ 我方已出 **`R-35`** 更正**落点**，并在 `spec` 侧**先行落地**：

| 已落地（我方） | 内容 |
|---|---|
| `spec/RESOLUTIONS.md#R-35` | 载体 ＝ `L06` 既有运营表（`t_ledger_ops.ops_json`），走既有 `PATCH /api/ledger/L06/:id`；**零迁移 · 零新端点 · 零新列** |
| `spec/ledger-mapping.json#ledgers.L06.fields` | 增 `originals_handover_note`（label **`原件移交清单`**）／ `originals_receipt`（label **`原件签收记录`**），**均 `writable: true`** |
| `docs/reference/config-mapping.sample.json` | 同批登记两键（否则 `TestUnregisteredWritableLedgerFields` 红） |

**你要做的**：

1. ★★ **`internal/seed/seed.go#opsSupervisor` 增两键** —— **`原件移交清单`** / **`原件签收记录`**（★ **中文键**，与 `permission.CanWrite` 的**精确匹配**口径一致，勿用英文）。
2. ★★ **既有库的规则行更新路径** —— ★ `SeedQ3Defaults` 用 **`INSERT OR IGNORE`**（不覆盖既有行）⇒ 仅改 `seed.go` **对既有库无效**。请给出一种**可执行路径**（任选并说明理由）：① 幂等迁移（`UPDATE t_permission_rule SET writable_fields=… WHERE resource=… AND role='综合运营主管'`，★ **须只增不覆盖管理员已改内容**）；② 扩展 `jxapproval seed` 支持 `--refresh-writable`（只合并白名单、不动其余列）；③ 走管理页（★ 但须**先证明**该页能改 `writable_fields`）。
3. ★★ **`L06` 台账页可登记** —— 两字段在 `L06` 页**可见且可写**（走既有 `PATCH`）；★ 回读可见（`GET /api/ledger/L06/:id` 的 `ops` 含两键）。
4. **不得**新增表 / 列 / 端点；**不得**动 `t_submission` 结构。
5. `originals_receipt` **不落 `t_attachment`**（`instance_code NOT NULL` 而 `SUB` 无审批实例）⇒ 与既有 `handover_receipt` **同机制**（`ops_json` 存引用/文本）。

**★ 验收判据（请各写可机检用例）**：

| # | 判据 |
|---|---|
| ① | `PATCH /api/ledger/L06/:id` 写 `原件移交清单` 与 `原件签收记录` ⇒ **200**；`ops` 回读含两键（**正向**） |
| ② | ★ **反向**：写**未登记**的键 ⇒ **403/409**（证明白名单在承重，不是放行一切） |
| ③ | ★ **既存键名不被破坏**：`提交集团日期` / `集团流程编号` / `集团流程状态` 仍可写（见 `H3`） |
| ④ | ★ `t_submission` **零结构改动**（DDL 快照比对） |

---

## H2 · `G2` 覆盖残余 —— `submit_overdue` 的 **key 单源化**

**背景**：批 29 我方做单点变异时发现：`submit_overdue` 的 key **在两个分支各写一次字面量** —— ① `deadlineWorkdays <= 0` ⇒ `undefined_criteria`（**测试夹具 `newDashboardApp` 走这条**，因其 `d.Spec == nil`）；② `> 0` ⇒ `guardedAlert`（**生产路径走这条**）。⇒ ★ **生产路径的 key 无自动化断言**（`G2-M3` 变异「仍绿」即此故）。★ 你方已**主动如实披露**，我方确认成立。

**你要做的**（**二选一**，说明选择理由；★ 若两个都做更好）：

- **(a)** 把该 key 收成**一个常量**（两分支共用 ⇒ 改名必两处一致）；或
- **(b)** 给 `newDashboardApp` 增一条**注入 `Spec` 的聚合用例**，覆盖 `guardedAlert` 分支。

**★ 验收判据**：**单点变异**——改该 key（或常量）⇒ 对应测试**恰红**；★ 与本次变异**无关**的测试**必须保持绿**（变异隔离）。

---

## H3 · 键名**三处对齐** ＋ `L06` 存量登记补全（★ 本轮新查出，非 `R-34` 引入）

★★ **我方裁定（口径，直接照此实现）**：**以 `spec/ledger-mapping.json` 的 `label` 为唯一真相**，另三处**向它对齐**。

### H3.1 `seed.go#opsSupervisor` 两处**过期名**（★ 实测活缺陷）

| 现值（seed） | 权威面（`R-20#13` 已改） |
|---|---|
| `提交日期` | **`提交集团日期`** |
| `集团受理编号` | **`集团流程编号`** |

⇒ ★★ **后果**：界面按规格名写入 ⇒ `permission.CanWrite` **不命中** ⇒ **403「字段不可写」**；★ 而**门禁全绿** —— **没有任何判据把「规格登记的字段名」与「运行时接受的键名」绑起来**。

### H3.2 `L06` 存量 3 条**可写但未登记**（★ 由 `TestUnregisteredWritableLedgerFields` 当场抓出）

- **`移交凭证（签收）`** —— ★★ 即 `handover_receipt`，制度明文「**无凭证视为未提交**」⇒ **当前台账页无写入入口**
- **`付款 / 报销完成日期`** —— 导入源写作 `付款完成日期` ⇒ **命名不一致**
- **`驳回原因与处置`** —— 未登记

### H3.3 要做的

1. 对齐 **`internal/seed/seed.go#opsSupervisor`**（改两处名 ＋ 补 `L06` 三条）。
2. 对齐 **`docs/reference/config-mapping.sample.json`**（`L06` 补 3 条：`移交凭证（签收）` / `付款 / 报销完成日期`（★ **由 `付款完成日期` 改名**）/ `驳回原因与处置`）。
3. ★★ **同步该 ratchet 的期望数 `10 → 7`**（对齐后 `L06` 缺口归零；余 **`L01`×1 ＋ `L08`×6**）—— ★ 按测试**自身提示的 `N-026` 做法**，**改断言必须同批 ＋ 在回执中说明**（**不得静默调数**）。
4. ★★ **新增机检（本条最值钱，请务必做）**：对每个**可写**台账，断言
   **`seed.go#opsSupervisor` 的可写键集合 ⊇ 该台账 `t_ledger_field_def` 的 `field_key` 集合**。
   ★ 判据＝**这道机检能抓出 `H3.1` 的两处过期名**（请**先证明它在当前代码下会红**，再修 ⇒ 修后绿）。

**★ 验收判据**：① `check_all.sh` **9/9**；② `TestUnregisteredWritableLedgerFields` **绿且期望值 ＝ 7**；③ ★ **H3.1 的机检**：修复**前**注入旧名 ⇒ **红**，修**后**绿（**一次只变异一处**）。

---

## 4. 明确**不做**（勿动）

| 项 | 原因 |
|---|---|
| `FR-M8-07` HTTPS 强制 | ★ 属反代侧，非应用代码 |
| `FR-M8-09` 文件权限收紧 | ★ 属部署侧 |
| `FR-M8-04` 恢复演练 | ★ 属运维流程 |
| `docs/09` 联调 41 项 | ★ 待连飞书实测 |
| `docs/05-API.md` 的 `GET instances/*/fields` 契约行 | ★ **我方域**（会报项 `audit_silent C5`），**勿动** |
| `F5` / `F11` / `F9` / `M6-05` 的口径细化 | ★ **我方已裁定「维持现状」**（见 `COLLAB#N-060` 第四部分 ④），**勿改** |

---

## 5. 交付要求

1. 每完成一部分跑 `bash scripts/check_all.sh`，**必绿 9/9**。
2. ★ 提交**只用显式路径**；★ 提交**后** `git show --stat HEAD` 复核文件清单。
3. 完成后在 `COLLAB.md#N-060` **第四部分**下追加**逐条回执**（`H1`/`H2`/`H3`，含证据 `文件#函数` ＋ 单点变异结果 ＋ 未做原因），状态改 **`MIMO-DONE`**。
4. 推送 `origin/main`。
5. ★ **若某项确实做不完**：按 `H1` → `H3` → `H2` 的顺序交付**可独立验收的最小完整部分**，**如实登记剩余**，**不得留半成品提交**。
6. ★ 遇到需要我方裁定才能定的口径，**停在议题里写清楚，不要猜**。
