# MIMO-NEXT-BATCH-17 —— 批 18：`N-053` 落地段③ 的**前置** —— Go 侧装载面扩到 `.csv`（否则 `S26` 一落即红）

> ★ **本文件可整份粘贴给 mimo code。**
> 交付方：**WorkBuddy**（诊断 ＋ 规格）／ 实现方：**mimo code**（`internal/specload` Go 侧装载器 ＋ 回归钉）
> 依据：`COLLAB.md §4 · N-053`；`REMAINING.md §1 B11 / §2 A15 / §5 批 17`；`spec/checks.json` **V1.19**（`_pending_primitive_note` ＝ 该原语的唯一规格来源）
> 时间：2026-10-05

---

## §0 问题本体（★ 我方已实测坐实，**不是推断**）

★★ **本批是我方落地判据 `S26` 的「唯一前置」。**

- `S26`（即将由我方入库）用第 12 原语 `csv_col_eq_json_by_key` 断言：`spec/acceptance.csv` 的 `severity`/`carrier_kind` 与真源 `spec/forms/*.json#checks` 的 `severity`/`carried_by_kind` **逐条一致**。
- Go 侧 `runChecklist(files)`（`internal/specload/checklist.go:39`）由 `validate.go:19` 调用 ⇒ ★ **`Load()` 每次加载真 spec 都会执行 `checks.json` 里的全部判据**。
- ★ 而 `Load()`（`internal/specload/specload.go:312–333`）的装载过滤是 `!strings.HasSuffix(path, ".json")` ⇒ ★★ **`spec/acceptance.csv` 不在 `files` map 内**，第 12 原语用 `files[csvPath]` 取不到 CSV ⇒ 真 spec 上必然报「CSV 文件不在装载面」。

★★★ **实测记录（我方做，可复现）**：临时把 `primitives.csv_col_eq_json_by_key` ＋ 判据 `S26` 注入 `spec/checks.json`，然后跑
`go test ./internal/specload -count=1` ⇒ **14 个测试全红**，报错逐字为：

```
[S26] CSV 文件不在装载面：spec/acceptance.csv
```

受影响的用例（节选）：`TestLoadRealSpec` · `TestValidateProbes` · `TestPathExistsRealAnchors` · `TestPathExistsAcceptsSelectorAndHashless` · `TestS14CarriedByKindDomain` · `TestSetCoversEngine` · `TestRefExistsSplitEngine` · `TestArrayEachRequiredProbe` · `TestDashboardD7Probes` …（共 14 个）。

★ 该实验**已复原**（`cp` 备份 ＋ `sha256sum -c`，**未用 `git checkout --`**）⇒ 工作区与 `HEAD` 一致、`spec/checks.json` **sha256 `9904beee…`**。

⇒ ★★ **结论：次序不可颠倒** —— **先**扩装载面（本批 mimo）→ **再**由我方同批落 `primitives` ＋ `S26` ＋ Python 侧 ＋ 探针。

★★ **本批不引用任何新原语、不新增判据** ⇒ 门禁**保持全绿**（无红窗）—— 这正是「硬次序」要保住的性质。

---

## §1 本批规格（★ 逐条对齐；**不在下表的一律不动**）

### T1 · 装载面扩到 `.csv`（`internal/specload/specload.go` · `Load()`）

★ 现状（`：314–328`）：
```go
err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
    if err != nil { return err }
    if d.IsDir() || !strings.HasSuffix(path, ".json") { return nil }
    ...
    files[path] = b
    return nil
})
```

★ 改法（**最小改动**）：
- 目录仍跳过；
- **扩展名为 `.json` 或 `.csv` 的文件装载**；其余（如 `spec/README.md`、`spec/RESOLUTIONS.md`）**仍不装载**（保持现状，别放宽成「装载一切」）。

★ **键名口径不变**：`spec/acceptance.csv`（相对 embed 根、正斜杠）—— 与 `files["spec/checks.json"]` 同范式。

★ **同步更新两处注释**（否则注释与行为不一致）：
1. `Load` 的函数注释（`：310–311`）「读取 `spec/**/*.json`」⇒ 改为「`*.json` ＋ `*.csv`」；
2. `Bundle.ProblemsRaw` 的字段注释（`specload.go:40`）「全部 `spec/**/*.json` 原始字节」⇒ 改为「`*.json` ＋ `*.csv`」。

★ **`specfs.go` 不用改** —— 它已是 `//go:embed all:spec`（`all:` 前缀会带上 `.csv`）。

### T2 · 回归钉（新建 `internal/specload/loader_csv_scope_test.go`，同包，可直接调 `Load`/`loadFiles`）

★ 目的：**证明装载面真的含 CSV，且没有误伤**。★ 断言要**具体**（不许只断言 `err == nil`）。

| 用例 | 断言 |
|---|---|
| **L1 · CSV 已进入装载面** | `Load(specfs.FS)` 成功 ⇒ `b.ProblemsRaw["spec/acceptance.csv"]` **存在** ∧ 字节长度 > 0 ∧ 用 `encoding/csv` 解析出**≥ 2 行**（表头 ＋ ≥1 数据行）∧ 表头**含** `doc_type` 与 `check_id` 两列 |
| **L2 · 不误伤：非 json/csv 仍不装载** | `b.ProblemsRaw` 的**全部键**都以 `.json` 或 `.csv` 结尾 ⇒ ★ 即 `spec/README.md`、`spec/RESOLUTIONS.md` **不在其中**（★ 请按实际的 `.json` 键数一起断言，避免「只断言后半句」） |
| **L3 · 既有结论不变** | `Load(specfs.FS)` 返回 `err == nil`（★ 真 spec 全绿），且 `b.ProblemsRaw` 中 `.json` 键数与 `spec/` 下 `*.json` 实际文件数**一致**（★ 请先用 `fs.WalkDir` 数出来再断言，**不要写死一个猜的数字**） |
| **L4 · 负向（缺 CSV 时当前无判据引用它）** | 用 `loadFiles` 手造与真 spec 同源但**去掉 `spec/acceptance.csv`** 的 `files` ⇒ ★ 因 `checks.json` 此时**尚未引用** `S26`，结论应为 **`err == nil`** |

★★ **L4 的写法（重要）**：断言 **`err == nil`**，并在注释里写明「★ 我方落地 `S26` 后此例语义**会自然翻转**为『缺 CSV ⇒ 报错』，届时由我方**同批**更新本用例」。
★ **不要**写一个**本批就红**的用例（那会让驱动判据③结构性不可满足）。

### T3 · 三条单点变异（★ 一次只改一处；`cp` 备份还原，**禁用 `git checkout --`**）

| # | 变异 | 预期 |
|---|---|---|
| **M1** | 过滤改回 `!strings.HasSuffix(path, ".json")`（即本批修复回退） | ★ **恰红 L1**（其余 L2/L3/L4 保持绿） |
| **M2** | 放宽为「装载一切文件」（去掉后缀过滤） | ★ **恰红 L2**（其余保持绿） |
| **M3** | 只把 `.csv` 的装载键写成别的形态（如去掉 `spec/` 前缀，或写反斜杠）—— ★ 任选一种**能改变键名**的写法 | ★ **恰红 L1**（取不到该键） |

★ **变异前先自证「变异真的改变了行为」**；★ **编译失败 ≠ 判据无鉴别力 —— 先怀疑变异本身**；★ 还原后须给出 `sha256sum -c` 证据。

### T4 · 硬边界（★ 越界即返工）

- ★ **不动 `spec/**` 一字**（`checks.json` 的 `primitives`/`checks`/版本号、`acceptance.csv` 全不动）。
- ★ **不动 `internal/specload/checklist.go`**（第 12 原语已由 `f1db150` 落地，本批**不碰**）。
- ★ **不动 Python 侧**（`scripts/check_spec.py` 归我方）。
- ★ 不动既有 11 个原语与其测试、不动 `check_all.sh`。
- ★ 提交只用**显式路径**（**禁止 `git add -A`**）；★ 完成后回写 `COLLAB.md`（本议题追加回执块）并推送 `origin/main`。

### T5 · 回执与自测

★ 回执须含：① 改动文件（显式路径）② `go test ./... -count=1` 全绿 ③ `bash scripts/check_all.sh` **必绿 8/8 ＋ 会报零命中** ④ `T2` 四条用例的**逐条结果**（含 L1 解析出的行数与表头列）⑤ `T3` 三条变异「红在哪、绿在哪」＋ `sha256sum -c` 还原证据 ⑥ `MIMO-DONE` 标记（★ **状态留 `OPEN`** —— 结案由我方在 `S26` 落地后判定）。

---

## §2 为什么不与本批同时落 `S26`（★ 请照此理解，别擅自加）

★ `S26` 属**我方域**（判据 ＋ Python 侧）；★ 且它**一旦入库就必须两侧齐**：Go 侧 `runOne` 对未知原语 fail-closed、Python 侧 `[META]` 比对「`primitives` 声明 vs 已实现」⇒ **单侧落必红**。
★★ 因此本批只把 Go 侧**装载面**准备好，使 `S26` 一旦入库即**两侧同时可跑**；★ 本批**不引用任何新原语** ⇒ 门禁**保持全绿**。

---

## §3 验收方式（我方，交办后执行；列此仅供你自检对标）

1. 独立复跑 `bash scripts/check_all.sh` ⇒ **必绿 8/8 ＋ 会报零命中**（★ 不采信自报）；
2. 逐行走查实现（过滤是否**只**放宽到 `.csv`、键名是否为 `spec/acceptance.csv`、两处注释是否同步）；
3. ★★ **三条单点变异我方自做**（M1/M2/M3，一次只变异一处）＋ `cp` 备份 ＋ `sha256sum -c` 还原；
4. 独立复跑 `go test ./internal/specload -count=1`；
5. ★★ **交叉验证**：我方随后注入 `primitives` ＋ `S26` 再跑一次 `go test ./internal/specload -count=1` ⇒ ★ 应**从「14 个红」变为「全绿」**（这正是本批的目的）。

---

## §4 交办纪律（★ 硬性，与 §1–§3 同等效力）

1. ★ **工作范围仅限本仓库目录** —— 即 `C:\Users\haoduan\workspace\jx-procurement-platform`（当前仓库树内）；★ **不得读写仓库外任何路径**（含 RaiDrive 网络盘 `Z:` 及一切挂载盘）。
2. ★ **以 `COLLAB.md` 为准** —— 若与本机记忆、对话上下文、或其它文档冲突时，**一律以 `COLLAB.md` 台账为最高依据**；动手前先读 `COLLAB.md §1`（当前状态）与 `§4 · N-053` 段。
3. ★ **提交前必跑 `bash scripts/check_all.sh` ⇒ 必绿 8/8 ＋ 会报零命中**；★ **红了不入库**（先修到绿再提交）。
4. ★ **只用显式路径提交** —— `git add <显式文件路径>…`（本批应为 `internal/specload/specload.go` ＋ 新建 `internal/specload/loader_csv_scope_test.go` ＋ `COLLAB.md`）；★★ **禁止 `git add -A` / `git add .`**。
5. ★ **完成后回写 `COLLAB.md` 台账**（本议题追加回执块 ＋ 打 `MIMO-DONE` 标记，★ **状态字段留 `OPEN`** —— 结案由我方在 `S26` 落地后判定）**并推送 `origin/main`**。
