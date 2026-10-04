# MIMO-NEXT-BATCH-9 —— 批 11：`path_exists` 原语（Go 侧实现，与 `S20` 同批）

> ★ **本文件可整份粘贴给 mimo code。**
> 交付方：**WorkBuddy**（判据与规格）／ 实现方：**mimo code**（Go 侧引擎）
> 依据：`COLLAB.md §4 · N-048`；数据文件：`spec/institution-anchors.json`（已入库，V1.0）
> 时间：2026-10-04 09:56

---

## §0 问题本体（一句话）

`spec/institution-anchors.json` 是 `N-006` 第 2 重机制（**制度条款 ↔ 机读规格 双向索引**），
`N-006` 明写要**接入门禁**：**锚点指向的文件/字段不存在 ⇒ 红**。
★ 但**现有 10 个原语都表达不了**这件事 —— `ref_exists` 只能比对**单个目标 dict 的键集合**，
而锚点指向的落点**分散在任意 spec 文件、任意深度**（如 `spec/forms/CT.json#checks[id=mandatory_clauses_complete].else`）。
⇒ 需**新增第 11 原语 `path_exists`**（两侧同批）。★ **Python 侧我已落地并自测**（见 §3），本批只差 Go 侧。

★ **为什么不能只落一侧**：`checks.json#primitives` 一旦声明 `path_exists`，
`scripts/check_spec.py` 的 META 会比对「声明 vs 实现」；而你侧 `internal/specload/checklist.go`
对**未知原语**是 **fail-closed**（文件头注释：`探针 E：未知原语 ⇒ [META]`）⇒ **单侧落 ⇒ 净检出可构建当场红**。

---

## §1 我方规格（★ 逐字对齐，语义以本节为准）

### 1.1 参数

| 参数 | 必填 | 说明 |
|---|---|---|
| `file` | ✔ | glob，指向**存放指针的文件**（本批即 `spec/institution-anchors.json`） |
| `collect` | ✔ | 点路径，收集出一批**字符串指针**（本批即 `clauses[*].spec[*].at`） |
| `min_hits` | ✖ | 默认 **1**，与既有加固同口径：**collect 命中不足即报「疑似清单声明有误」** |

### 1.2 每个字符串指针的语义

形如 `<spec 相对路径>#<点路径>`：

1. **省略 `#`** ⇒ 只校验**文件存在且可解析**（不校验点路径）。
2. **有 `#`** ⇒ ① 文件必须存在且可 `json` 解析；② 点路径在该文件内**必须命中 ≥1 个节点**。
3. **命中 0 ⇒ 报错**（★ 不许把「声明写错」静默成「通过」）。
4. 非字符串项 ⇒ 报错（锚点必须是字符串指针）。

### 1.3 点路径段语法（★ 四条，全部有实证）

| 段 | 语义 |
|---|---|
| 裸键 | 普通对象键（**不得含 `.`**） |
| `[字面键]` | ★ 键名**含 `.`** 时必须用此形式 |
| `[k=v]` | 选择器：当前层子节点中，`键 k` 的**标量值 == v** 者（用于 `checks[id=…]` / `sections[id=…]`） |
| `*` / `[*]` | 全部子节点 |
| `**` | 递归下降 |

★★ **切分规则（关键，实测逼出）**：点路径**按 `.` 切段，但方括号 `[...]` 内部不切分**。两条独立实证：

- ① **字面键可含点**：`spec/params.json` 有 **5 个含点扁平键**（`params` 下的 `reporting.monthly_cutoff_day` 等）
  ⇒ 正确写法 `spec/params.json#params.[reporting.monthly_cutoff_day].critical_note`；
- ② **选择器值可含点**：`spec/checks.json#change_log[version=1.5].change`。

★ 不做此规定 ⇒ 两种写法都被切错、**命中 0 而红** —— ★ 这是**安全失败**（不会静默取到错节点），
但会造成「同一份索引、两侧一绿一红」的假分歧 ⇒ 必须两侧同规则。

### 1.4 标量比较口径

`[k=v]` 的 `v` 与节点值比较时，**归一口径与既有 `_scalar_str` / `scalarString` 完全一致**
（字符串原样；布尔 → `"true"`/`"false"`；整数 → 十进制无小数点；非标量 ⇒ 不参与匹配）。

---

## §2 交办（T1–T5）

### T1 · 实现原语（Go 侧）
- 位置：`internal/specload/checklist.go`，新增 `case "path_exists"`（与既有 `case "set_covers"` 同层）。
- 语义：**严格按 §1**；建议**复用你既有的点路径求值骨架**（`toks`/`sel` 同款），只加：
  ① **括号感知切分**；② `[k=v]` 选择器分支；③ `[字面键]` 分支（**dict 自身**与**数组元素**两种宿主都要支持）。
- 报错文案需**可定位**：至少含「哪条指针」＋「文件不存在」/「命中 0 个节点」两类区分
  （★ 我侧文案见 §3，措辞不必逐字相同，但**分类必须一致**）。

### T2 · 注册与探针
- 在 `checks.json#primitives` 里**不要**动 —— ★ **声明由我方落**（与 `S20` 同批，见 T3）。
- 新增 Go 侧单测（建议 `internal/specload/path_exists_test.go`），**三类反例必须各一**：
  1. **选择器值写错**（`sections[id=nope]`）⇒ 报「命中 0」；
  2. **文件不存在**（`spec/forms/NO_SUCH_FILE.json#…`）⇒ 报「文件不存在」；
  3. **含点键写成裸键**（`params.reporting.monthly_cutoff_day…`）⇒ 报「命中 0」
     （★ 这条同时证明「必须用 `[键]`」不是空话）。
  ★ 另需**正例**：直接读真 `spec/institution-anchors.json` 的 `clauses[*].spec[*].at` 全量断言 **0 报错**
  （该文件当前 **158 条指针**，已全部可解析）。

### T3 · 划界（★ 严格）
- **不得改 `spec/**`**：`checks.json`（`S20` 与 `primitives` 声明）、`institution-anchors.json` **均由我方落**。
- **不得改 `scripts/check_spec.py`**（我方域）。
- ★ **同批含义**：你落 Go 侧（此时 `checks.json` 未引用 `path_exists` ⇒ 门禁保持全绿）；
  你提交后**由我方**同批落 `primitives` 声明 ＋ `S20` ＋ Python 侧（已就绪）⇒ 两侧齐 ⇒ 绿。
  ★ **不要**为了让 `S20` 早点生效而自行往 `checks.json` 加判据 —— 那会让我侧 META 报「声明了未实现的引擎」。

### T4 · 三条单点变异（★ 每次只改一处，`cp` 备份还原，禁用 `git checkout --`）
1. 把选择器匹配改成「只比对键存在、不比对值」⇒ 反例 1 **必须转红**；
2. 把括号感知切分改回朴素 `strings.Split(ptr, ".")` ⇒ 反例 3 **必须转红**；
3. 把「命中 0 ⇒ 报错」改成静默 continue ⇒ ★ **三条反例全绿** —— ★ 这一条要**如实报告**
   （正常会全红，若你发现只有部分变红，说明我的反例设计有漏，请指出）。

### T5 · 不做清单（本批明确不做）
- 不新增/修改任何 `S` 判据；不改 `forms/`；不碰 `chain.json`；
- 不做「按 id 反查」类扩展（`[k=v]` 已够用）；
- 不去改 `spec/institution-anchors.json` 的内容（含 `citation_stats`）；
- 不接 `emergency` 通路（属 `A8`）。

---

## §3 我方已就绪的部分（供你对照）

- **Python 侧已落地**：`scripts/check_spec.py` 新增 `split_ptr_segs()` ＋ `_ptr_walk()` ＋ `prim_path_exists()`，
  并注册进 `PRIMITIVES`（★ **尚未**写进 `checks.json#primitives`，故当前对门禁不可见）。
- **自测结果**（我方一次性脚本，仓库外）：
  - 正向：对真 `spec/institution-anchors.json` 全量 158 条指针 ⇒ **0 报错**；
  - 反向 A（选择器值改坏）⇒ **精确 1 条**；反向 B（文件不存在）⇒ **精确 1 条**；
    反向 C（含点键写成裸键）⇒ **精确 1 条**；还原后复检 ⇒ **0 条**。
- ★ 你的实现完成后，**由我方**同一批落：`checks.json#primitives.path_exists`（含 `desc`）＋ **`S20`**
  （`args`: `{"file":"spec/institution-anchors.json","collect":"clauses[*].spec[*].at","min_hits":1}`）
  ＋ 正式探针 `scripts/_probe_n048.py`，然后复跑门禁 **8/8** 与会报零命中。

---

## §4 完成判据（三条，缺一不可）

1. `COLLAB.md#N-048` 段内出现你方 **`MIMO-DONE`** 字样（回执含：改了哪些文件、测试名、变异结果）；
2. **`HEAD` 越过基线**（基线＝你开工时的 `HEAD`；提交信息请勿以 `[WorkBuddy]` 开头）；
3. `bash scripts/check_all.sh` **退出码 0**（必绿 **8/8**、会报零命中）。

★ 提交纪律：**只用显式路径**（禁 `git add -A`）；完成**回写 `COLLAB.md` 并推送 `origin/main`**。
★ 若发现本任务包**我方有错**（例如反例构造不可满足、规格自相矛盾）⇒ **照实报告**，不要自行绕过 —— 本项目按「如实登记」计分，不按「表面全绿」计分。
