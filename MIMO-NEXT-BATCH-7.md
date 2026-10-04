# MIMO-NEXT-BATCH-7 —— 批 10 · `N-046`：看板 16 补「**采购变更异常**」指标

> 议题：**`COLLAB.md §4 · N-046`**。本文件＝交办包（**可整份粘贴**给 mimo）。
> 交付方：mimo code。验收方：WorkBuddy（**独立复核**：门禁 ＋ 读实现 ＋ 证伪对照 ＋ `sha256` 还原）。
> 生成时间：2026-10-04（★ 时间取自机器时钟）。

---

## 0. 一句话

在 `internal/dashboard` 的看板 16 聚合路径（`buildAnomaly`）里补**第 12 个 alert 指标** `change_anomaly_listed`，与 `spec/dashboard.json` **已落**的定义对齐；并同步修改 `internal/httpapi` 里**硬编码 `11`** 的断言。

---

## 1. 先读（动手前，按此顺序）

| # | 位置 | 看什么 |
|---|---|---|
| 1 | `COLLAB.md §4 · N-046` | ★ 本议题的**背景、实测证据、六点拟补定义** —— 唯一口径源 |
| 2 | `spec/dashboard.json` → `dashboards[3]`（看板 16） | ★ 新指标 `key = change_anomaly_listed` 的 `label` / `formula` / `fields`（**我方本轮已落**，见 §2） |
| 3 | `internal/dashboard/dashboard.go` → `buildAnomaly`（约 446–543 行） | 11 个指标的现有写法；辅助函数 `guardedAlert` / `countOpsContains` / `countExtRegistered` / `countL06Unverified` / `isTruthy` |
| 4 | `internal/httpapi/dashboard_key_align_test.go` | ★ 驱动本批的判据 `TestDashboardAlertKeysMatchSpec`（45–49 行等量断言）；★ `TestDashboardGuardPerIndicator` 里的**硬编码 `11`**（247 行） |
| 5 | `internal/flow/finalize.go` 172–213 | ★ L09 **六列自检** —— 确认 `exception_type` / `is_anomaly_listed` 的**生产落点＝`t_ledger_archive.ext_json`** |
| 6 | `internal/httpapi/handlers_approval_pcss_inject.go` 100–130 | ★ 两列的**生产者**：`putSysField(fields,"is_anomaly_listed", recent>=1)` · `putSysField(fields,"exception_type","采购变更")` |

---

## 2. ★★ 工作区状态（**务必先看清，否则会误判**）

- ★ **`spec/dashboard.json` 已被我方改好，但「尚未提交」**（`git status` 里是 `modified`）—— 那是**我方域**（看板指标定义归 WorkBuddy）。
  - ★ **不要 `git add` 它、不要编辑它、不要回退它**（尤其**不要** `git checkout -- spec/dashboard.json`）。
- ★ **起草阶段 `bash scripts/check_all.sh` 会红 1 项**，且**只会红这 1 项**：

  ```
  --- FAIL: TestDashboardAlertKeysMatchSpec
      dashboard_key_align_test.go:75: 输出指标数 = 11, spec 指标数 = 12 —— 数量必须相等（少报/多报都是错位）
  ```

  ★ **这是预期的** —— 它正是**本批要你消掉的那一项**（spec 已声明 12，实现还产 11）。★ 不是你的环境问题，**更不是让你去改 `spec`**。
- ★ `MIMO-NEXT-BATCH-7.md`（本文件）由我方维护，**不要提交它**。
- ★ 其余目录我方未动；`MIMO-*.md` 历史任务包与 `REMAINING.md` 我方本轮**未改**。

---

## 3. 要做的（`T1`–`T4`）

### `T1` 新增指标（`internal/dashboard/dashboard.go#buildAnomaly`）

1. 新增一个 alert 指标：`key = "change_anomaly_listed"`，label「**采购变更异常（90 天内 ≥2 次）**」。
2. **口径**（★ 与 spec `formula` **逐字对齐**）：`L09` 行中 `exception_type = 采购变更` **且** `is_anomaly_listed = true` 的**行数**。
3. ★★ **取数位置（本批最容易做错的一点）**：这两列的**唯一生产者**是 `httpapi#injectPCSSSystemFields`（PC 提交期注入），落点是 **`t_ledger_archive.ext_json`**（`flow/finalize.go` 的 L09 六列自检读的就是这里）⇒ ★ **实现读 `Row.ArchiveExt`**（与 `countL06Unverified(r06)` **同范式**）。
   - ★ **不要**照抄 `emergency_purchase` / `sole_source` 的 `countOpsContains(r09, keyMethod, ...)`（那读的是 ops `采购方式` 的**历史口径**）—— ★ `is_anomaly_listed` **在 ops 里没有生产者**。
   - ★ **但请你独立判断**：若你读代码后认定该结论有偏差（例如 ops 与 ext 两处都可能有值、或历史行形态不同），⇒ ★ **在回执里如实写明并给出证据**，**不要静默二选一**（`N-036` 纪律）。
4. **值形态兼容**：`is_anomaly_listed` 由 `putSysField` 写为 **bool**；★ 但历史/人工登记行经 `ext_json` 往返后可能是**字符串** ⇒ 兼容 `bool true` 与 `"是"` / `"true"`（★ 与 `countL06Unverified` 的两形态同做法）；★ **不得**把 `"否"` / `"false"` 计入。
5. ★★ **守卫（必做）** —— `global_rules.r3` ＋ 看板 16 `connected_requires`①：
   - 用**列级**守卫 ＝ `countExtRegistered(r09, "is_anomaly_listed")`（＝ ext 中该键**有登记值**的行数），与 `account_changed` 的 `countExtRegistered(r06, "payee_account_verified")` **完全同款**；
   - ★ 理由：`L09` **有行但该列无人登记** ⇒ 必须显示 `not_connected`「数据未接入」，**不得显示 `0`**（`0` 会被读成「没有变更异常」，而真相是「源为空／列没登记」）。
6. 位置：建议紧邻 `sole_source`（**三条例外类型放一起**），★ 但**以能通过判据为准**。

### `T2` 测试（`internal/httpapi`，★ 本批的鉴别力来源）

1. ★★ **`TestDashboardGuardPerIndicator` 里有硬编码**：

   ```go
   if len(out) != 11 { t.Fatalf("alerts 数 = %d, want 11", len(out)) }
   ```

   ⇒ **必须改为 `12`**（否则 `T1` 落地后**该用例反而会红**）。
   - ★ 该用例 ② 段「L09 系 3 项」的注释/断言请按需同步为 4 项；★★ **但注意**：② 段只播了一行 `{"采购方式":"紧急采购",...}`、**`is_anomaly_listed` 列无人登记** ⇒ 新指标此时应是 **`not_connected`**（列级守卫）⇒ ★ **不要**把它写进「必须有 `count`」那一组；★ 若要断言它，**断言 `not_connected`**。
2. **新增覆盖（至少三段，形制沿用既有 handler 级范式：`newDashboardApp` ＋ 真 HTTP `GET /api/dashboard/16`，★ 不手搓 payload —— `N-040` 教训）**：

   | 段 | 造数 | 期望 |
   |---|---|---|
   | **① 正例** | `L09` 造行，`ext_json` ＝ `{"exception_type":"采购变更","is_anomaly_listed":true}` | `count == 1` 且 `status != "not_connected"` |
   | **② 反例（验「且」）** | `{"exception_type":"采购变更","is_anomaly_listed":false}` ⇒ 出 `0`；另造 `{"exception_type":"紧急采购","is_anomaly_listed":true}` ⇒ **不计入** | ★ **两向都验** —— 防「只按其中一个条件算」 |
   | **③ 列级守卫** | `L09` 有行，但 `ext_json` **没有** `is_anomaly_listed` 键 | `status == "not_connected"` 且**不带 `count`** |

### `T3` 门禁

- `bash scripts/check_all.sh` **必绿 8/8**（★ §2 的那 1 项红必须由你消掉）＋ **会报项零命中**；
- `gofmt` / `go vet` 干净。

### `T4` 提交与回写

1. ★ **只用显式路径提交**（★★ **禁止 `git add -A` / `git add .`**）：本次实际改动的 `internal/dashboard/dashboard.go` ＋ `internal/httpapi/*_test.go`（按 `git status --short` 逐行核对后列全）；提交后 `git show --stat HEAD` **核对清单**与预期一致。
2. ★ **不得提交**：`spec/dashboard.json`（我方**未提交**的工作区改动）、`MIMO-NEXT-BATCH-7.md`（本文件）。
3. 在 `COLLAB.md` 的 **`### N-046`** 段内**追加**你的回执并写 **`MIMO-DONE`**（★ **只追加、不改我方已写内容**，`§0 铁律 #3`）。
4. 提交信息用你的既有风格；完成后**推送 `origin/main`**。

---

## 4. ★ 边界与纪律

1. **工作范围仅限本仓库目录**；★ **以 `COLLAB.md` 为准**（与你的记忆冲突时以台账为准）。
2. ★ **不抢跑**：若发现 spec 与实际不符、或口径有歧义 ⇒ **如实回执并停手**，**不要自行发明口径**（`N-036` 教训）。
3. ★ **本批只做这一件事**：★ **不要**顺手翻转看板 16 的 `source_status`（`pending → connected`）—— 那要等 `known_gaps` 第 7 条的 **9 个旧 key 改名**与之**同批**（**另批**）；★ **不要**顺手改 `emergency_purchase` / `sole_source` 的现有取数口径。
4. ★ **纪律**：不 `git add -A`；不 `git checkout <旧提交> -- .`；不改写我方已写的内容（只追加）。

---

## 5. 完成判据（三条全满足）

1. `COLLAB.md` 的 `### N-046` 段内**出现 `MIMO-DONE`**；
2. **有你的新提交**（显式路径，且**不含** `spec/dashboard.json`）；
3. `bash scripts/check_all.sh` **必绿 8/8**（★ 起草阶段那 1 项红已消）。
