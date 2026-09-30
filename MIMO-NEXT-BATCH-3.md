# MIMO 任务包 · 2026-10-01 第三轮（V5.0）

> **投喂方式**：本文件可**整份粘贴**给 mimo。
> **强制先读**：`COLLAB.md`（**§1** 当前状态 ＋ 门禁状态行 ＋ **最后更新**）· **`spec/dashboard.json` V1.1（本轮新增，务必通读 `global_rules.r6`/`r7` 与各看板 `connected_requires`、`known_gaps` 第 7 条）**。
> **一句话背景**：你 `f75ab0b` 提的待确认问题（「`source_status` 由 `pending→connected` 的**推进时机与条件**」）我方已于 `ec6ffaf` 答复并**落成机读规格**（`dashboard.json` V1.1）。★ 答复**改变了本批的做法** —— 见 §0。★ 另**顺手查出一处「第二份真相」**（指标 key），它**必须与翻转同批**处理，否则翻转那一刻会静默错位。

---

## 0. ★★★ 最重要的一条：本批**不要**先把看板改成 `connected`

我方对「推进条件」的答复是：

> **`connected` 的判据＝「本看板每个指标都具备自证能力」，不是「数据源全部接上」。**
> ★ **`connected` 的正确含义不是「数据已经有了」，而是「每个指标都能自证自己有没有数据」。**

**理由**（一句话）：`source_status` 是**看板级**的，而数据源是**指标级**的（看板 16 有 **7 个台账 / 11 个指标**）——
- ★ **等全部数据到齐才置 `connected`** ⇒ 把**已经能用**的指标一起关掉（这是 `r1` 的**反向误伤**）；
- ★ **任一数据到达就置 `connected`** ⇒ 还没接通的指标**显示 0**（正是 `r3` 要防的形态）。

⇒ **唯一自洽的判据是「守卫齐全」**：置 `connected` 后，**没数据的指标自己会显示「数据未接入」**（由该指标自己的 `availability_guard` 保证）。

★ **另**：`not_enabled`（看板 13）**不参与**本推进 —— 它是**制度性未启用**，与 `pending` 的区别就是「**会不会推进**」。

---

## 1. ★★ 本批主线：`T1` 指标 key 对齐（**必须先做，且必须与翻转同批**）

**现状（我方复核 `f75ab0b` 时查出）**：看板 16 的指标 key 有**两份真相** ——
① **灰态路径**（`Build` 短路）出的是 **`spec` key**；② **聚合路径**（`buildAnomaly`）出的是**代码里的旧 key**。

| `spec` key（唯一真相） | 现实现 key | 同？ |
|---|---|---|
| `overdue_unapproved` | `overdue_review` | ✗ |
| `emergency_not_closed_24h` | `emergency_unclosed_over_24h` | ✗ |
| `sole_source` | `single_source` | ✗ |
| `account_changed` | `account_change` | ✗ |
| `split_suspicion` | `split_suspect` | ✗ |
| `purchaser_overdue` | `handler_overdue` | ✗ |
| `self_purchaser_count` | `requester_as_handler` | ✗ |
| `purchaser_concentration` | `handler_concentration` | ✗ |
| `group_rejected_unhandled` | `group_rejected_undisposed` | ✗ |
| `over_budget` | `over_budget` | ✓ |
| `emergency_purchase` | `emergency_purchase` | ✓ |

⇒ ★★ **11 项里只有 2 项同名；翻转那一刻会有 9/11 个指标「改名」。**

★ **为什么这不只是「改个名字」（两层）**：
1. **翻转即改名** ⇒ 前端 `v-for :key` 会**整列重建**；任何按指标 key 做的**下钻 / 订阅 / 导出会静默错位**（不报错，只是对上不）。
2. ★★★ **`r3` 守卫挂在旧 key 上** —— `buildAnomaly` 里 `requester_as_handler` / `handler_concentration` 是**唯二**带守卫的指标，而 `spec` 里它们叫 `self_purchaser_count` / `purchaser_concentration` ⇒ ★ **一个按 `spec` key 写的守卫会「挂在空气上」**（找不到对应指标、**静默不生效**）。

**怎么改**：**以 `spec` 为唯一真相** —— 把聚合路径的 **9 个旧 key 改名为 `spec` key**（`label` 也照 `spec`）；`over_budget`/`emergency_purchase` 两个不动。
★ 顺带：看板 14 的 `purchaser_concentration` 与看板 16 的**同款指标必须同一实现**（`known_gaps` 第 5 条）—— 两处各算一遍＝第二份真相。

**验收要求（★ 双向，缺一不可）**：
- ★ **正向**：`spec` 的 `indicators[*].key` 集合 **⊇** 实际输出的 alerts `key` 集合（**按 key 逐个比对**，不是只比数量）；
- ★ **反向（防退化）**：把某个实现 key 改回旧名 ⇒ **该断言必须红**（★ 否则这条测试没有鉴别力，同 `N-026` 同族）。

---

## 2. `T2` · 把 `connected_requires` 落成**可检查的前置条件**

`spec/dashboard.json` 每张看板新增了 **`connected_requires`**（逐板列出硬前提）。★ **请按它逐条落地，不要自行简化**。要点：

| 看板 | 硬前提（摘要） |
|---|---|
| **13** | ★ **不适用** —— `not_enabled` **不参与推进**（要推它得先改 `ledger-mapping#L12.storage` ＋ 用户决策，**不由实现单方推进**） |
| **14** | ★★ **6 个指标各自声明守卫**，且**分两类**：**须守卫**（其 0 会被误读为「很正常」）＝ `avg_cycle_days` · `delay_top5` · `supplier_monthly_accum_top` · `purchaser_concentration`；**可不守卫但须登记理由**（0 是真实语义）＝ `in_flight_orders` · `monthly_amount_trend`。★ **默认必须守卫；「不守卫」是一个需要写理由的决定** |
| **15** | ★★ **`abnormal_subject_hint` 的口径未定 ⇒ 必须有第三种态**（见 §3）—— **这是本看板能否翻转的唯一硬前提** |
| **16** | ★★ **11 个指标「每个」各自声明守卫**（**不是看板级一个**）＋ 与看板 14 共用指标同一实现 ＋ **`T1` 的 key 对齐** |

---

## 3. `T3` · 新增第三种态：**口径未定**（`global_rules.r7`）

★★ **`r7` 是新增纪律**：除 `connected` / `not_enabled` / `pending` 之外，**指标层还需要 `undefined_criteria`（口径未定）**。

**为什么**（看板 15 的 `abnormal_subject_hint`）：**数据源接得通、但「什么算异常」的标准没给**（工具表只有五个字）⇒ ★ 若翻转后它显示 `0`，会被读成「**没有异常科目**」；而真相是「**还没有判定标准**」。

⇒ ★ **三者渲染文案必须互不相同**：`数据未接入` / `数据未接入` / **`口径未定`**（★ 「没接上」「源为空」「标准没给」是三种不同成因，不得长得一样）。

**怎么落**：该指标 `formula` 为「未定义」⇒ 其守卫须声明 `undefined_criteria`，渲染为「**口径未定 —— 待财务/集团给判定标准**」，**不得显示任何数字**。

---

## 4. `T4` · 补 `[D5]`/`[D6]` 机检（我方 `[D2]` 复核的记项）

★ 现 `[D2]` **只查 `r1`/`r2`/`r3` 字符串非空** ⇒ **`r2` 的语义没有被机检**。建议补两条（**名称可自定，语义须一致**）：

- **`[D5]`**：`source_ledgers` **∩** {`L03`,`L04`,`L05`,`L06`,`L07`,`L11`} **≠ ∅** ⇒ 该看板**必须显式声明**「须读运营表」的标记（如 `ops_table_required: true`），否则报错。★ 这三张台账名单**取自 `r2` 自己的正文**（不得另起一份）。★ 现 `14`/`15`/`16` 命中、`13` 不命中 ⇒ 配**双向**探针。
- **`[D6]`**：`source_status == connected` 的看板 ⇒ **其每个指标必须声明 `availability_guard`**（`T2`）。★ 这是把 §0 的答复**变成能拦住人的东西** —— 否则「守卫齐全」只是口头约定。

---

## 5. `T5` · 两侧同批的小项（★ **约定顺序：我先改 `spec`，同批你改实现**）

| # | 事项 | ★ 为什么必须同批 |
|---|---|---|
| ① | ★ **`related_pr_must_exist` 同名异义**：`SS` 语义＝「存在**且已批准**」／`RFQ` 语义＝「**只要存在**」 | ★ 判据 id 是**注册键** ⇒ ★ **只改我这一侧会让 `RFQ` 的新 id 未注册 ⇒ fail-closed ⇒ 拦掉全部合法 RFQ**。⇒ **不要单侧改**；**等我先说「spec 已改」，你再动实现与测试**（两个语义都要有用例） |
| ② | ★ **`BJ` 是否引用 `RFQ` 单号**（新增 `related_rfq_no`）：`BJ` 与 `RFQ` 在**同一节点**（采三档 `rfq` 询比价）⇒ 比价表引用它所依据的询价单 | ★ 同样是**跨两侧契约**：我方加字段 ＋ 你加校验，**两侧同批** |

★ **另**（不催，你上轮自己登记的债）：`/admin` 常量页签 · `CT` 的 `usage_category` 带入 · `PR` 审批时点的 `designated` 填报 · `SS`/`PC` 提交通路与审批时点判据（随发起批 / `M9`）。

---

## 6. 本批**不要**做

- ★ **不要**为了让看板「看起来有数据」而把 `source_status` 改成 `connected`（`T1`/`T2`/`T4` 未完成前，翻转＝把「没接上」显示成「0」）。
- ★ **不要**改 `spec/`（`spec/` 是**唯一真相**、由我方交付）；发现问题请**开议题**（`COLLAB.md §4`）。
- ★ **不要**只改一侧的判据 id（见 `T5`）。
