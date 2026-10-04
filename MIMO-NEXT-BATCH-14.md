# MIMO-NEXT-BATCH-14 —— 批 15：`N-055` 两侧 `min_hits` 语义对齐（Go 侧改为**逐文件**）＋ 回归钉

> ★ **本文件可整份粘贴给 mimo code。**
> 交付方：**WorkBuddy**（裁定与规格；★ 裁定已于本批入库 —— `spec/checks.json` **V1.17**）／ 实现方：**mimo code**（Go 侧引擎语义修正 ＋ 测试）
> 依据：`COLLAB.md §4 · N-055`；`REMAINING.md §1 B13 / §5 批 15`；`spec/checks.json` **V1.17**（新增 `_min_hits_note` ＋ `change_log` **v1.17**）；`spec/README.md` **V1.13**（§4 新增参数语义段）
> 时间：2026-10-04 21:45

---

## §0 问题本体（一句话 ＋ 裁定已定）

★ **背景**：`min_hits`（默认 1）的用途是 **杜绝「`collect` 声明写错 ＝ 静默通过」**（`checks.json#change_log` v1.2）。★★ **两侧引擎对它的语义不一致** —— 且长期未被发现（只在「`collect` 只在**部分**文件里有值」这个**此前未出现过的形态**上才分叉）。

| 侧 | 位置 | 现语义 |
|---|---|---|
| **Python**（我方；＝**正本行为**） | `scripts/check_spec.py#_hits_guard`（**在 `for f in expand(file)` 循环体内**调用） | ★ **逐文件** |
| **Go**（你方；**待改**） | `internal/specload/checklist.go`：`collectAcross`（`:482`）＋ `minHitsProblems`（`:866`） | **跨文件汇总** |

★★ **复现（实测，非推断）**：批 14 落 `S25` 时把 `min_hits` 写 `1` ⇒ **Python 报 8 处红 / Go 全绿** ⇒ ★ 同一份判据、**两侧相反结论**。

★★ **我方裁定（2026-10-04，已入库 `checks.json#_min_hits_note` ＋ `change_log` v1.17）：应然语义 ＝「逐文件」**。依据三条（相互独立）：

1. `checks.json#change_log` **v1.6** 明文：「该 `min_hits` 的**每文件语义**本身是既有设计（`S5c` 等同款）」；
2. Python 源码 `prim_array_each_required` 头注：「`set_covers.min_hits` 是**逐文件**计数（不是全局），表达不了『一条不缺』」；
3. `checks.json#consumer_obligations` 既有约定：「**原语语义以本文件 `desc` 裁决**；不一致即两侧漂移」。

⇒ ★★ **Go 侧与设计不符 ⇒ 本批修正 Go 侧**（Python 侧**零改动** —— 它本就是逐文件）。

---

## §1 本批规格（★ 逐条对齐；**不在下表的一律不动**）

### 1.1 语义（唯一一条）

★ 对 **`file` glob 匹配到的每一个文件**，其 `collect` 命中数须 ≥ `min_hits`；★ **不再**对「所有文件的命中数**之和**」判。

### 1.2 适用范围（**仅**这七个 collect 型原语）

`coverage`（作用于 `dict_path`）· `enum_subset` · `ref_exists` · `range_contiguous` · `pattern_absent` · `set_covers` · `path_exists`

★ **不在表内的一律不动**：

- `json_parse` / `required_keys` —— 本就没有 `min_hits`；
- `array_each_required` —— 它的「0 项必报」是**另一套独立守卫**（`seen == 0` / 条件面为空），**语义不变**；
- ★★ **`cross_equal_by_key` 的 `min_hits` 是「配对计数」**（`compared < min_hits`）⇒ **不属本条、不得改**。

★ `ref_exists` / `path_exists` 注意：其 `min_hits` 只作用于 **`args["file"]` 的 glob**；★ `target_file` / `target_dict` **不参与** `min_hits`。

### 1.3 报错口径

- ★ 仍须带**当前判据的真实 `id`**（既有 `on_emitting_a_violation` 义务，**不得**硬编码 id 字面量）。
- ★ 文案措辞**不必**与 Python 逐字相同（两侧既有措辞本就不同）；但**必须**：① 是**逐文件**判定的产物；② 保留 `min_hits` 字样（★ 既有 Go 测试 `internal/specload/path_exists_test.go:148` 就断言 `strings.Contains(probs[0], "min_hits")` ⇒ 别把该词改掉）。
- ★ 一处**如实登记（本批不做）**：逐文件化后，多文件 glob 会产出**多条同文案**报错，而文案**未带文件名** ⇒ 不可分辨是哪张表单（Python 侧同病）。★ **本批不改文案**（避免扩大面），由我方另记后续轮次。

### 1.4 与「真 spec 现状」的关系（★ 重要，别误以为要动 spec）

★ 真 spec 当前**不受影响**，两侧在既有数据上**本就同结论**：

| 判据 | 形态 | 逐文件下为何仍绿 |
|---|---|---|
| `S8`（`forms/*.json` · `sections[*].fields[*].name` · `min_hits: 1`） | 多文件 glob | 11 张表单**每张都有**字段 ⇒ 每文件命中 ≥1 |
| `S14`（`forms/*.json` · `checks[*].carried_by_kind` · `min_hits: 1`） | 多文件 glob | 11 张表单**每张都有** `checks` ⇒ 每文件命中 ≥1 |
| `S5b`（`forms/*.json` · `ledger[*]` · **`min_hits: 0`**） | 多文件 glob | `QC`/`RFQ`/`BJ` 的 `ledger` 就是 `[]`（**合法事实**）⇒ 必须写 0 |
| `S25`（`forms/*.json` · `sections[*].fields[*].payload_form` · **`min_hits: 0`**） | 多文件 glob | `payload_form` 当前仅 `SA.json` 一处 ⇒ 必须写 0 |

⇒ ★★ **本批不要求、也不允许改任何 `checks[*].args`**（含把 `S25` 改回 `1` —— 逐文件下 `S25` 写 `1` 会**两侧同红**）。

---

## §2 交办（T1–T4）

### T1 · Go 侧引擎（`internal/specload/checklist.go`）

把 §1.2 七个 case 的 `min_hits` 判定改为**逐文件**：即「对**每个**被 `file` glob 匹配到的文件，**分别**收集该文件内的命中，**分别**与该 `min_hits` 比较」，而非「先 `collectAcross` 汇总、再比较」。

★ 实现形态归你方（例如给 `collectAcross` 增加「按文件返回」的形态，或新增 `minHitsProblemsPerFile`）；★ 但**不得**：

- 改 `min_hits` 的默认值（仍 **1**）；
- 改 §1.2 范围外原语的语义；
- 改 `spec/**`（归我方）。

### T2 · 回归钉（★ **两侧同结论**是本批的核心证据）

★ 构造「**`collect` 只在部分文件里有值**」的夹具 ⇒ ★ **必须报**（★ **改前 Go 不报** ⇒ 这就是鉴别力所在）。

★ 建议做成**表驱动**测试（放 `internal/specload/`，用既有 `loadFiles` 内存夹具范式），对 §1.2 的**七个**原语各注入一条**合成判据**（`min_hits: 1`）＋各自的合成夹具：

| 组 | 夹具 | 期望 |
|---|---|---|
| **(a)** 两份文件：一份「命中 1」、一份「命中 0」；`min_hits: 1` | 每原语各一例 | **必须报**（★ 改前 Go 汇总 = 1 ⇒ **不报**） |
| **(b)** 两份文件：**各**命中 1；**`min_hits: 2`** | 至少覆盖 1 个原语 | **必须报**（★ 每文件 1 < 2；改前 Go 汇总 = 2 ⇒ **不报**） |
| **(c)** 两份文件：各命中 1；`min_hits: 1` | 同 (a) 的原语集 | **必须过**（★ 防「改完恒红」） |

★ 各原语的夹具形态不同（`enum_subset` 要 `allowed`、`set_covers` 要 `required`、`range_contiguous` 要 `lower_key`/`upper_key`、`pattern_absent` 要 `forbidden_regex`、`ref_exists` 要 `target_dict`、`path_exists` 要指针、`coverage` 要 `dict_path`）—— ★ **具体夹具归你方设计**，只要满足上表三组语义。

★ **另加一条真实 spec 回归**：真 spec 现状下 `go test ./internal/specload/ -count=1` 与 `bash scripts/check_all.sh` **全绿**（依据见 §1.4）。

### T3 · 三条单点变异（★ 一次只改一处；`cp` 备份还原，**禁用 `git checkout --`**）

| 变异 | 预期 |
|---|---|
| **M1** 把 **`enum_subset`** 改回**跨文件汇总** | T2 中 `enum_subset` 的 (a) 行 ⇒ **恰红 1 条**；★ **其余 6 个原语的 (a) 行与 (b)(c) 全绿** ⇒ 隔离性成立 |
| **M2** 把 **`path_exists`** 改回**跨文件汇总** | 恰红 `path_exists` 的 (a) 行；其余全绿 |
| **M3** 让 `min_hits` 的**数值被忽略**（只在「命中数 == 0」时报） | 恰红 (b) 行（`min_hits: 2`、每文件 1 命中）；(a)(c) 全绿 |

★ 三条都要给出「**红在哪、绿在哪**」的**逐条对照**；★ 还原后 `go test ./... -count=1` 全绿、临时备份文件**已删**（★ `internal/httpapi/` 下**已有两个历史 `.bak`**，**勿新增、勿动它们**）。

### T4 · 回执 ＋ 自测

- `bash scripts/check_all.sh` ⇒ **必绿 8/8 ＋ 会报零命中**（★ 本批 `spec/**` 零改动 ⇒ 判据 / Go 包 / 净检出**零波动**）。
- `gofmt -l` 空、`go vet ./...` 干净。
- ★ **回执里必须逐条回答**：① 七个 case 各自的改动点（`file:line`）；② T2 的夹具与断言（★ **改前 Go 是「不报」吗？请实测取证**，这是本批鉴别力的关键）；③ 真实 spec 下 `go test ./internal/specload/ -count=1` 与 `bash scripts/check_all.sh` 的结论；④ 有没有哪条**按 §1 无法实现**（★ **有就如实写出来 ＋ 指出冲突点**，**不要**自行改规格、**不要**静默降级）。

---

## §3 划界（★ 严格；不在表内的一律不做）

- ★ **不动 `spec/**`**（含 `checks.json` 的 `_min_hits_note` / `change_log` / 任何 `checks[*].args`）—— ★ 我方已入库、**你方只读**。
- ★ **不动** §1.2 范围外原语的语义（`json_parse` / `required_keys` / `array_each_required` / **`cross_equal_by_key` 的配对计数**）。
- ★ **不动** `scripts/**`（Python 侧归我方；★ 本批 Python 侧**零改动** —— 它本就是逐文件）。
- ★ **不动** 批 12/13/14 已落地的求值器（`amountPositiveOf` / `checkPRSafetyBranch` / `checkSACrossMonthAllocation`）与 `evaluateHardChecks` 的白名单与 fail-closed。
- ★ **不动** `router.go` / `conventions` 其它键 / `N-048` 的 `path_exists` **业务面**（本批只碰它的 `min_hits` 判定位置）。
- ★ **不改报错文案措辞**（★ 但要保留 `min_hits` 字样；「报错带文件名」属后续轮次，见 §1.3）。
- ★ **不做** `A8`（GR/RFQ/QC 发起通路接线 —— 我方尚未出具通路口径）· **不做** `N-053`（升级为正式判据）· **不做** `N-054` ②。

---

## §4 完成判据（三条，缺一不可）

1. ★★ `COLLAB.md` 的 **`### N-055`** 段内写入你的回执（改动文件**显式路径** ＋ T3 对照表 ＋ 如实项），★ **并在该段内出现字面量 `MIMO-DONE`**（驱动脚本的完成判据①）。★ **议题状态字段仍填 `OPEN`**（结案由我方判定）。
2. **提交并推送** `origin/main`：**显式路径**提交（★ **禁止 `git add -A`**），提交信息带你的署名前缀。
3. 提交前跑 `bash scripts/check_all.sh` ⇒ **必绿 8/8**。

★ 若中途被打断，直接从断点继续；★ 若判定某条**无法按规格实现**，**照样提交已完成部分 ＋ 在回执里如实说明**，**不要**为了「看起来完成」而改动规格或降级语义。
