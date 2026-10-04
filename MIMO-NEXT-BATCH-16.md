# MIMO-NEXT-BATCH-16 —— 批 17：`N-053` 落地段 —— 新增第 12 原语 `csv_col_eq_json_by_key`（Go 侧引擎）

> ★ **本文件可整份粘贴给 mimo code。**
> 交付方：**WorkBuddy**（原语语义与规格；★ 规格已于本批入库 —— `spec/checks.json` **V1.18** 新增顶层 `_pending_primitive_note`）／ 实现方：**mimo code**（`internal/specload` Go 侧原语 ＋ 单元测试）
> 依据：`COLLAB.md §4 · N-053`；`REMAINING.md §1 B11 / §2 A15 / §5 批 17`；**`spec/checks.json` V1.18**（`_pending_primitive_note` 为该原语的**唯一规格来源**）；`spec/README.md` **V1.15**
> 时间：2026-10-05

---

## §0 问题本体（一句话 ＋ 规格已定）

★ **现象（真实事故，非假想）**：`spec/acceptance.csv`（判据级验收台账，`N-017`）与真源 `spec/forms/*.json#checks` 之间**会静默漂移** —— ★ 批 12 实测：`acceptance.csv` 首批**漏落 `BA` 8 行**（`severity` 仍 `(未声明)`、`BA#amount_tier1_only` 的 `carrier_kind` 仍 `pending_implementation`，而 `forms` 侧同期**已落** `hard`/`code`），★★ **而它通过了全部 8 道必绿门禁** —— 根因＝**该表当时不被任何脚本读取**（全仓仅 3 处注释提及）⇒ 台账与真源之间**没有机械校验、全靠人眼**。

★★ **为什么必须新增原语**：要在门禁里断言「**CSV 某行的某列 == 以 `(doc_type, check_id)` 为键的 JSON 对象的某字段**」，★ **而现有 11 个原语无一能表达** —— `cross_equal_by_key` 按 **JSON** 取数、`path_exists` 只判**指针可解析**、`coverage`/`enum_subset` 只作用于**单文件 scope**。

★ **已落的最小形态**：`scripts/check_spec.py#_ledger_vs_forms`（`[META]` 自查类，Python-only）已在跑且**必绿**；★ 本批是把它**升级为 `checks.json` 正式判据** —— ★ **为此需要两侧引擎各实现一遍同名原语**（`N-011`「消灭第三份真相」）。

★★ **规格正本 ＝ `spec/checks.json#_pending_primitive_note`**（本批已入库）。★ **本任务包 §1 是它的可执行重述，若两处冲突，以 `checks.json#_pending_primitive_note` 为准。**

★★ **本批的硬次序（两侧同批）**：① 我方规格（**已完成**，`checks.json` **V1.18**）→ ② **本批：mimo 落 Go 侧原语**（★ 此时 `checks.json` **未引用**该原语 ⇒ 门禁**保持全绿**）→ ③ **下一轮我方同批**落 `primitives.csv_col_eq_json_by_key` ＋ 判据 **`S26`** ＋ Python 侧由 `_ledger_vs_forms` 提升为同名原语 ＋ 探针 `scripts/_probe_n053.py`。

---

## §1 本批规格（★ 逐条对齐；**不在下表的一律不动**）

### T1 · Go 侧原语实现（`internal/specload/checklist.go`）

在 `runOne`（：78）的 `switch c.Primitive` 中**新增一个 `case "csv_col_eq_json_by_key"`**（★ 放在既有 `cross_equal_by_key` 之后、`default` 之前）。

**args 形态**（★ 逐字，键名不得改）：

| 键 | 类型 | 必填 | 语义 |
|---|---|---|---|
| `file` | string | ✅ | CSV 路径（★ **从内存 `files` map 取**，见下「测试可注入」） |
| `key_cols` | []string | ✅ | 按 **CSV 表头名**取键列（如 `["doc_type","check_id"]`） |
| `json_glob` | string | ✅ | 真源文件通配（如 `spec/forms/*.json`） |
| `json_collect` | string | ✅ | 在**每个**匹配文件内的点路径，收集**对象数组**（如 `checks[*]`） |
| `json_key` | []string | ✅ | 键拼装：元素是「被收集对象上的字段路径」或保留记号 **`$file_stem`**（＝文件名去扩展名），按序拼成键（如 `["$file_stem","id"]`） |
| `pairs` | []object | ✅ | `{"csv_col": <CSV 列名>, "json_field": <JSON 字段路径>, "map": {<CSV 侧字面量>: <JSON 侧字面量>}}`（`map` 可省） |
| `require_same_row_set` | bool | ✖（默认 `true`） | 键集合**双向相等** |
| `csv_cols_expected` | int | ✖（默认 `-1` ＝不校验） | CSV **数据行**列数须等于该值 |

**语义（逐条，缺一不可）**：

1. **参数不完整**（任一必填项为空/缺省）⇒ 返回 1 条错误（★ **不得**静默返回空）。
2. **CSV 读取**：从 `files[csvPath]` 取字节（★ **不是从磁盘读** —— 见 T2 的「测试可注入」）；取不到 ⇒ 报错。`encoding/csv` 解析，**第 1 行为表头**。
   - `key_cols` 或任一 `pairs[*].csv_col` **不在表头** ⇒ 报错。
   - `csv_cols_expected >= 0` 且某数据行列数 ≠ 该值 ⇒ 报错。
   - 全空行（`len(row)==1 && row[0]==""`）跳过（**不当作数据行**）；★ 其余行一律按数据行处理（**不静默丢弃**）。
3. **真源索引**：对 `globKeys(files, jsonGlob)` 命中的**每个**文件：
   - `stem` ＝ `path.Base(name)` 去扩展名（如 `spec/forms/PR.json` → `PR`）；
   - `root, ok := decoded[name]`；不可解析 ⇒ 报错（同既有 `cross_equal_by_key`：`:440` 范式）；
   - `hits := collectPath(root, jsonCollect)`；逐项：仅 `map[string]any` 项参与（非对象项 ⇒ ★ **报错**，不许静默跳过）；
   - `key` ＝ 按 `json_key` 逐元素拼：元素 `== "$file_stem"` ⇒ `stem`；否则 `scalarStr(dig(hit, elem))`；用 `"\x1f"`（或等价不可见分隔符）联接**避免歧义**；
   - ★ **键重复 ⇒ 报错**（**不静默取后者**）。
4. **fail-closed 三条**（★ **不许把「声明写错」静默成「通过」**）：`json_glob` **命中 0 个文件** ⇒ 报错；`json_collect` 命中 **0 项** ⇒ 报错；`key_cols` 为空或 `json_key` 为空 ⇒ 报错（同 ①）。
5. **逐 CSV 数据行**：`key` ＝ 按 `key_cols`（**表头名 → 列序**）取值的联接。
   - ★ **键重复 ⇒ 报错**；
   - **键不在真源 ⇒ 报错**（文案须含该键的两个分量）。
6. `require_same_row_set == true` ⇒ **真源键未被任何 CSV 行登记 ⇒ 报错**（逐条列出）。
7. **每个 `pairs` 逐条比**：`csvVal := row[csvColIdx]`；若 `map` 非空且含 `csvVal` ⇒ `csvVal = map[csvVal]`；`jsonVal, ok := scalarString(dig(obj, jsonField))` ⇒ ★ 不等（或 `ok==false` ⇒ 视为不可比、报错）⇒ 报错。★★ **标量归一必须复用既有 `scalarString`（：811）** —— 字符串原样、布尔 → `"true"/"false"`、整数 → 十进制无小数点；★ **不得**自造归一（那会造成「同一份清单、两侧相反结论」）。

★ **报错前缀**：一律 `fmt.Sprintf("[%s] …", c.ID)`（既有约定，`:472` 同款）；★ **文案须可定位**（含文件名/行列/键/两值）。

★ **测试可注入（关键）**：CSV 必须从传入的 `files map[string][]byte` 读取，**不得** `os.ReadFile`（否则 T2 无法用内存夹具，且与 `parseJSON` 走 `files` 的既有范式不一致）。

★ **不得改动**：既有 11 个 `case`、`runChecklist` 的自检段（：49–61）、`default` 分支、任何既有 helper 的签名。

### T2 · 单元测试（★ 本批**验收的核心证据**；新建 `internal/specload/csv_col_eq_json_by_key_test.go`）

★ 装配范式照抄 `internal/specload/min_hits_per_file_test.go` 的 `mhSpec`（合成 `spec/checks.json`：`primitives` 声明该原语 ＋ 一条判据 `id="T-CSV"`）＋ `files` 内存 map（含 **CSV 字节** ＋ **JSON 字节**）；断言用 `runChecklist(files)`（同包，可直接调）。

★ **夹具形态（★ 自造，别依赖真 `spec/`）**：合成 `spec/forms/AA.json`（`{"checks":[{"id":"c1","severity":"hard","carried_by_kind":"code"},{"id":"c2","severity":"soft","carried_by_kind":"manual"}]}`）＋ 合成 `spec/forms/BB.json`（1–2 条）＋ 合成 `spec/acceptance.csv`（表头 `doc_type,check_id,x,x,severity,x,x,x,carrier_kind,x,x` ⇒ **11 列**；行 `AA,c1,…,hard,…,code,…,` 等）＋ 一条判据 args：
`{"file":"spec/acceptance.csv","key_cols":["doc_type","check_id"],"json_glob":"spec/forms/*.json","json_collect":"checks[*]","json_key":["$file_stem","id"],"pairs":[{"csv_col":"severity","json_field":"severity"},{"csv_col":"carrier_kind","json_field":"carried_by_kind","map":{"submit":"code"}}],"require_same_row_set":true,"csv_cols_expected":11}`

| 用例 | 构造 | 期望 |
|---|---|---|
| **C1 · 全等 ⇒ 绿** | 夹具一致 | `runChecklist` **零** `T-CSV` 报错 |
| **C2 · 值不等 ⇒ 红** | 把 CSV 的 `severity` 改成 `soft`（真源 `hard`） | **恰 1 条**报错，文案点名 `AA`/`c1`/`severity`/两值 |
| **C3 · `map` 生效 ⇒ 绿** | CSV `carrier_kind` 写 `code`，真源 `carried_by_kind` 写 `submit`（★ 依赖 `map{"submit":"code"}`） | **零**报错（★ 反证 `map` 没生效则 C3 必红） |
| **C4 · 漏登记 ⇒ 红** | CSV 删掉 `AA,c2` 那行（`require_same_row_set:true`） | **恰 1 条**「真源判据 `AA#c2` 未登记」 |
| **C5 · CSV 多一行 ⇒ 红** | CSV 加 `CC,c9` 一行（真源无此键） | **恰 1 条**「键不在真源」 |
| **C6 · 键重复 ⇒ 红** | 真源两文件同名 `id`（或 CSV 重复键） | 报错（★ 反证「键重复不静默」） |
| **C7 · fail-closed：`json_glob` 命中 0 文件** | `json_glob` 改成 `spec/nope/*.json` | 报错（★ 不许静默通过） |
| **C8 · fail-closed：收集面 0 项** | 真源 `checks` 为空数组 | 报错 |
| **C9 · 列数不符 ⇒ 红** | CSV 某行列数 ≠ 11（`csv_cols_expected:11`） | 报错 |

★ 断言方式照 `mhProblems`（只筛含 `T-CSV` 的问题、逐条断言条数与关键词）；★ **每条用例须断言「红在哪、哪条不红」**，不允许只断言 `len>0`。

### T3 · 三条单点变异（★ 一次只改一处；`cp` 备份还原，**禁用 `git checkout --`**）

| # | 变异 | 预期 |
|---|---|---|
| **M1** | 去掉 `pairs` 的比较、只建索引 | **C2 必红转绿**（＝缺陷重现），余全绿 ⇒ 隔离成立 |
| **M2** | `map` 查表改为恒用 CSV 原值 | ★ **恰红 C3**（其余全绿） |
| **M3** | 删掉 `require_same_row_set` 分支 | ★ **恰红 C4**（其余全绿） |

★ **变异前先自证「变异真的改变了行为」**（★ 本项目教训：变异打在非数据流经处＝空操作）；★ **编译失败 ≠ 判据无鉴别力 —— 先怀疑变异本身**。

### T4 · 硬边界（★ 越界即返工）

- ★ **不动 `spec/**`**（`checks.json` 的 `primitives` 与 `checks` **一字不改**）；★ **不新增 `S26`**；★ **不改 `checks.json` 的版本号**。
- ★ **不动 Python 侧**（`scripts/check_spec.py` 归我方；`_ledger_vs_forms` 保持原样）。
- ★ **不动**既有 11 个原语、不动 `runChecklist` 自检段、不动 `check_all.sh`。
- ★ `spec/acceptance.csv` **不改**（数据侧已一致，勿「顺手修」）。
- ★ 提交只用**显式路径**（**禁止 `git add -A`**）；★ 完成后回写 `COLLAB.md`（本议题追加回执块 ＋ `§1` 相应单元格）并推送 `origin/main`。

### T5 · 回执与自测

★ 回执须含：① 改动文件（显式路径）② `go test ./... -count=1` 全绿 ③ `bash scripts/check_all.sh` **必绿 8/8 ＋ 会报零命中** ④ T2 九条用例的**逐条结果** ⑤ T3 三条变异「红在哪、绿在哪」＋ `sha256sum -c` 还原证据 ⑥ `MIMO-DONE` 标记（★ 状态留 `OPEN`，结案由我方判定）。

---

## §2 为什么不与本批同时加 `S26`（★ 请照此理解，别擅自加）

★ `S26` 是 `checks.json` 里**引用**该原语的判据 ⇒ ★ **一旦在 Go 侧未实现时加它，`runOne` 的 `default`（：471）会 fail-closed ⇒ 门禁当场红**；★★ 且 Python 侧 `check_spec.py`（：744）会比对「`primitives` 声明 vs 已实现」⇒ **单侧落必红**。
★★ 因此本批**只落 Go 侧实现 ＋ 测试**（此时 `checks.json` **未引用**该原语 ⇒ 门禁**保持全绿**）；★ **`primitives` 声明 ＋ `S26` ＋ Python 侧 ＋ 探针，由我方在下一轮同批完成**。

---

## §3 验收方式（我方，交办后执行；列此仅供你自检对标）

1. 独立复跑 `bash scripts/check_all.sh` ⇒ **必绿 8/8 ＋ 会报零命中**（★ 不采信自报）；
2. 逐行走查实现（args 解析 · 归一是否复用 `scalarString` · 三条 fail-closed · 键重复是否报错）；
3. ★★ **三条单点变异我方自做**（M1/M2/M3，一次只变异一处）＋ `cp` 备份 ＋ `sha256sum -c` 还原；
4. 独立复跑 `go test ./internal/specload -count=1` ＋ 断言 T2 九条用例的**鉴别力**（C2/C4 等在对应变异下必须转红）。
