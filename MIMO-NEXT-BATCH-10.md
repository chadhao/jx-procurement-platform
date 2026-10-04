# MIMO-NEXT-BATCH-10 —— 批 6：完整明细 UI 增强（行级错误定位 ＋ 拖拽排序 ＋ 复制行 ＋ 批量粘贴）

> ★ **本文件可整份粘贴给 mimo code。**
> 交付方：**WorkBuddy**（契约与口径）／ 实现方：**mimo code**（服务端产出 ＋ 前端消费）
> 依据：`COLLAB.md §4 · N-050`；**契约正本已入库**：`docs/05-API.md` **V2.20 · §4.6**
> 时间：2026-10-04 11:14

---

## §0 问题本体（一句话）

`N-040` 已把 PR 明细的**重复段最小可用**做完（增删行 ＋ 按 schema 渲染 ＋ `buildRepeatingPayload` 组装 `fields.detail` 数组），
并**明确登记**未做的边界：**行内错误定位提示 / 拖拽排序 / 复制行 / 批量粘贴**。本批做这四项。

★ **但「行级错误定位」有一个前提**：服务端必须先给出**结构化定位**。现状（**读代码取证**）：

| 位置 | 现状 |
|---|---|
| `internal/httpapi/handlers_approval_formcheck.go` L115 / L131 | 行级错误**只有人读中文文案**：`明细「%s」第 %d 行不是对象` / `明细「%s」第 %d 行字段「%s」（%s）必填` |
| `internal/httpapi/handlers_approval_pr_amount.go` L60 / L65 / L68 | 同上：`PR 明细第 %d 行…` |
| `internal/httpapi/handlers_approval.go` L629-631 / L563-566 | 一律 `fail(c, 400, codeBadRequest, err.Error())` ⇒ `data` 恒为 `null` |
| `web/src/views/Submit.vue` L269 / L304 | `err.value = ex.message` ⇒ **整段文案显示，无行/字段定位** |

⇒ 若前端硬做定位，就只能**正则解析中文文案**（脆弱、必然随文案漂移）。
★ 故本批**先由我方定契约**（`docs/05-API.md §4.6`，已入库），**你再按契约产出与消费**。

---

## §1 我方契约（★ 逐字对齐；**正本在 `docs/05-API.md §4.6`**，本节为交办版）

`POST /api/approval/submit` 因**表单结构化校验**或 **PR 明细金额**不通过而返回 `400`（`code=40000`）时：

```json
{
  "code": 40000,
  "data": {
    "form_errors": [
      { "scope": "row", "section_id": "detail", "row_index": 1,
        "field_name": "material_name_spec", "kind": "required",
        "label": "明细 第 1 行 物料名称 / 规格型号" }
    ]
  },
  "message": "表单校验失败: 明细「明细」第 1 行字段「物料名称 / 规格型号」（material_name_spec）必填",
  "trace_id": "…"
}
```

### 1.1 硬约束（**逐条都不可放宽**）

1. ★★ **`message` 文案逐字不改** —— 既有客户端、既有测试（`strings.Contains(msg,"第 1 行")`）、用户可见提示**都不变**。
2. ★ **HTTP 状态码仍 `400`、错误码仍 `codeBadRequest`（40000）**，不改。
3. ★ **`data` 由 `null` 变为对象属「新增」** ⇒ 老客户端忽略即可 ⇒ **向后兼容**。
4. ★ `form_errors` **定义为数组**（当前实现**遇首个错误即返回** ⇒ 恒 1 个元素）；★ **前端必须按数组遍历**，不得只读第 0 项。
5. ★ **凡产生「第 N 行 / 行内字段」类错误的路径，都必须产出 `form_errors`** —— ★ 否则前端定位会**时灵时不灵**（比没有更糟：用户会学会不信红框）。
6. ★ **与另一类明细严格分流**：审批链算不到人 ⇒ `400` ＋ **`code=40010`** ＋ `data.unresolved_roles`（见 `docs/05-API.md §3.13`）—— ★ **字段名不同、不得混用**；前端须**按 `code` 分流**。

### 1.2 字段语义

| 字段 | 类型 | 语义 |
|---|---|---|
| `scope` | string | 受控词：`field`（顶层字段）· `row`（表单重复段的行内某字段/行本身）· `rows`（重复段整体：缺数组/非数组/空数组）· `amount`（PR 明细金额＝定档依据相关，段级或行级） |
| `section_id` | string | 段 id（`spec/forms/*.json#sections[*].id`）；**顶层字段**与 **PR 明细金额**分别为该段 id（PR 即 `detail`） |
| `row_index` | int | **1 起算**行号；**非行级为 `0`** |
| `field_name` | string | 字段名（`sections[*].fields[*].name`）；**非字段级为空串** |
| `kind` | string | 受控词：`required` · `struct`（形态非法：缺/非数组、行非对象）· `value`（取值非法：单价或数量非正、汇总非正） |
| `label` | string | 人读定位串（与 `message` 同源，可直接展示） |

### 1.3 逐错误点映射表（★ **本表就是实现清单**，11 处，一处不漏）

| # | 现有错误（**文案逐字保留**） | 位置 | `scope` | `section_id` | `row_index` | `field_name` | `kind` |
|---|---|---|---|---|---|---|---|
| 1 | `字段「%s」（%s）必填` | `handlers_approval_formcheck.go` L59 | `field` | `sec.ID` | `0` | `f.Name` | `required` |
| 2 | `明细「%s」（%s）缺失 —— 行内必填字段无处承载（契约：fields.%s ＝ 行对象数组）` | 同文件 L99 | `rows` | `sec.ID` | `0` | `""` | `struct` |
| 3 | `明细「%s」（%s）必须是行对象数组，实际是 %T（N-040 契约）` | 同文件 L105 | `rows` | `sec.ID` | `0` | `""` | `struct` |
| 4 | `明细「%s」至少需要 1 行 —— 行内必填字段不能为空数组` | 同文件 L109 | `rows` | `sec.ID` | `0` | `""` | `struct` |
| 5 | `明细「%s」第 %d 行不是对象` | 同文件 L115 | `row` | `sec.ID` | `i+1` | `""` | `struct` |
| 6 | `明细「%s」第 %d 行字段「%s」（%s）必填` | 同文件 L131 | `row` | `sec.ID` | `i+1` | `f.Name` | `required` |
| 7 | `PR 明细（fields.detail）缺失 —— 定档依据必须来自服务端按明细汇总（rule：公式汇总明细小计）` | `handlers_approval_pr_amount.go` L52 | `amount` | `detail` | `0` | `""` | `struct` |
| 8 | `PR 明细（fields.detail）必须是非空数组 —— 无明细即无定档依据` | 同文件 L57 | `amount` | `detail` | `0` | `""` | `struct` |
| 9 | `PR 明细第 %d 行不是对象` | 同文件 L60 | `amount` | `detail` | `i+1` | `""` | `struct` |
| 10 | `PR 明细第 %d 行缺单价（estimated_unit_price_cents）或数量（quantity）—— 行小计无法服务端计算` | 同文件 L65 | `amount` | `detail` | `i+1` | `""` | `value` |
| 11 | `PR 明细第 %d 行单价/数量必须为正（单价=%v 数量=%v）` | 同文件 L68 | `amount` | `detail` | `i+1` | `""` | `value` |
| 12 | `PR 明细汇总为 %d —— 定档依据必须为正` | 同文件 L75 | `amount` | `detail` | `0` | `""` | `value` |

★ **`label` 取值**：与会话文案同源，例如 #6 ⇒ `明细 第 1 行 物料名称 / 规格型号`（`sec.Label` ＋ `f.Label`）；#2/#3/#4 ⇒ `sec.Label` 本身；#5 ⇒ `明细 第 1 行`；#7/#8/#9/#10/#11/#12 ⇒ `PR 明细`（行级则附第 N 行）。★ 措辞不必与示例逐字相同，**但必须人读可辨**。

---

## §2 交办（T1–T5）

### T1 · 服务端产出 `form_errors`（Go）
- ★ **做法不限，但要同时满足 §1.1 的 6 条硬约束**。推荐：新增一个**结构化错误类型**（如 `formError{Scope, SectionID, RowIndex, FieldName, Kind, Label string/int; msg string}`）：
  - `Error()` **原样返回既有文案**（⇒ 上层 `"表单校验失败: "+verr.Error()` 与既有测试**零改动**）；
  - 由 §1.3 的 **12 处**改为构造该类型（**一处不漏**）；
  - 在两处 handler 出口（`handlers_approval.go` L629-631 表单校验、L563-566 PR 金额）改为：
    **是结构化错误 ⇒ `failWithDetail(c, 400, codeBadRequest, 同文案, {"form_errors":[...]})`；否则维持 `fail(...)`**。
    ★ `failWithDetail` 已存在（`internal/httpapi/response.go` L73），无需新写响应助手。
- ★ **不得**改 `spec/**`、`chain.json`、`forms/*.json`；★ **不得**改任何判据与错误码；★ **不得**改 `message` 文案（**改文案＝破向后兼容＝本批失败**）。

### T2 · 前端：抽纯函数模块 ＋ 行级错误定位
- ★ **新建 `web/src/repeatRows.js`（纯 ESM，★ 不得 `import` Vue）**，导出三个纯函数（★ 我方验收要**用 Node 直跑它们**，所以必须无框架依赖、无 DOM 依赖）：
  - `rowEditableFieldsOf(section)` —— 等价现 `Submit.vue#rowEditableFields`（`source === 'user'`）。
  - `parseBulkRows(fields, text)` —— 批量粘贴解析；**返回 `{ rows, error }`**（`rows`：行对象数组；`error`：人读错误串或 `''`）。语义见 T3②。
  - `locateFormErrors(formErrors)` —— 入参＝`data.form_errors` 数组；**返回 `{ first, keys }`**：`first`＝首个可定位项 `{ section_id, row_index, field_name }` 或 `null`；`keys`＝**全数组**的定位键字符串数组（`${section_id}|${row_index}|${field_name}`）。★ **必须遍历全数组、不得只读第 0 项**；入参非数组 ⇒ `{first:null, keys:[]}`（不抛异常）。
- `Submit.vue` 消费（**唯一注入点**＝`doSubmit` 的 `catch`）：
  - `code === 40000 && data.form_errors` 非空 ⇒ 逐条高亮（行级 ⇒ 该行容器、字段级 ⇒ 该 input）＋ 对 `first` 做 `scrollIntoView({ block: 'center' })`；
  - ★ **取不到 `form_errors`（`40010` / 网络失败 / 老响应）⇒ 回落**：照旧显示 `message`（**不得因取不到定位就不显示错误**）；
  - ★ **下一次提交、或用户编辑该行 ⇒ 清除该行高亮**（避免陈旧红框）；
  - ★ 高亮样式**复用既有样式体系**（`styles.css` / 页内 scoped），不引入 UI 库。

### T3 · 复制行 ＋ 批量粘贴
① **复制行**：`copyRepeatingRow(secId, idx)` —— 复制该行**全部可编辑字段值**（浅拷贝值即可），**插到该行之后**；★ 新行**不继承**错误高亮。
② **批量粘贴**：**页内 `textarea`**（每段一个，`v-if` 展开，不引入组件库）：
   - 解析规则：**按行切分**（丢弃全空行）→ **按 `\t` 切列**；**该行无 `\t` 时按 `,` 切列**；
   - **列顺序 ＝ `rowEditableFields(sec)` 的声明顺序**；某行**列数多于字段数 ⇒ `error` 可见报错**（★ **不静默丢弃**）；列数少 ⇒ 其余留空；
   - ★★ **值按「手工输入的同等口径」原样写入行对象**：money 字段写**元**（由既有 `buildRepeatingPayload` **在提交时**统一 `yuanToCents`）—— ★ **绝不在粘贴路径再换算一次**（两处换算＝口径分叉）；数字/文本**一律**以字符串存入（与手工输入一致）；
   - 粘贴结果 **追加**在现有行之后（**不覆盖**）；★ **不做列名识别 / 表头猜测**（猜错＝静默错位）。

### T4 · 拖拽排序（★ 含降级口径）
- 用**原生 HTML5 `draggable`**（`dragstart` / `dragover` / `drop`）＋ 现有 DOM，**不引入任何依赖**；
- ★ **仅影响展示与提交数组顺序** —— **不引入任何新语义**（服务端行序无业务含义，`spec/**` 一字不改）；
- ★ 若因现有结构限制**无法稳定实现** ⇒ 允许降级为「**上移 / 下移**」按钮，**但必须在 `COLLAB.md#N-050` 回执里如实登记降级理由** —— ★ **禁止静默降级**（回执里不写＝没做）。

### T5 · 自测与回执
- 新增 Go 用例（建议 `internal/httpapi/form_errors_test.go`）**至少 4 例**：
  1. 重复段**行内必填缺失** ⇒ `400` ＋ **`data.form_errors[0]` 四要素**（`scope=row` / `section_id=detail` / `row_index=1` / `field_name=material_name_spec`）＋ **`message` 仍含「第 1 行」**；
  2. 重复段**非数组 / 缺失** ⇒ `scope=rows`；
  3. PR 明细**缺单价或数量** ⇒ `scope=amount` ＋ `row_index` 正确；
  4. ★ **对照**：审批链算不到人 ⇒ `code=40010` ＋ `data.unresolved_roles`，且 **`data.form_errors` 不存在**（★ 两类明细**不混用**的机检）。
- 前端自测：`cd web && npx eslint src` **0 error**；`bash scripts/build.sh` 通过（★ 会重建 `internal/webui/dist/**`，**该目录入库**，须一并提交）。
- 回执写进 `COLLAB.md#N-050`，含：改动文件清单、测试名、**降级情况（若有）**、以及任何**你认为我方写错**的地方。

### T6 · 三条单点变异（★ 每次只改一处，`cp` 备份还原，**禁用 `git checkout --`**）

| # | 变异 | 预期 |
|---|---|---|
| 1 | 把 §1.3 的 **#6** 改回裸 `fmt.Errorf(...)`（不构造结构化错误） | 用例 1 **转红**（`form_errors` 缺失）；其余用例**保持绿** |
| 2 | `parseBulkRows`：**列多于字段时改为静默截断** | 若你自测含该用例 ⇒ 必红；★ 若无 ⇒ **如实说明「无鉴别力」** |
| 3 | `locateFormErrors`：**只读数组第 0 项** | 含 **2 条以上 `form_errors`** 的用例必红（★ 若你只造了单元素用例 ⇒ 请补一条双元素用例，或**如实说明无法鉴别**） |

★ 变异结果**必须逐条如实报告**（含「未转红」的情形）—— ★ 本项目按「**如实登记**」计分，**不按「表面全绿」计分**。

---

## §3 划界（★ 严格）

- ★ **不得改 `spec/**`**（`checks.json` / `forms/*.json` / `chain.json`）—— 本批**无 spec 改动**。
- ★ **不得改 `docs/05-API.md`**（契约正本归我方；你若认为契约有错 ⇒ **写在回执里**，不要改文件）。
- ★ **不得改 `scripts/check_*.py`**、不改任何门禁判据。
- ★ **不得引入任何前端运行期依赖**（不加 `package.json` 的 `dependencies`）；`devDependencies` 亦不加。
- ★ **不得触碰提交契约**：`fields.<section_id>` ＝ **行对象数组**（`N-040` 已定），`rowEditableFields` 只收 `source=user`。
- ★ **不做**：列名识别 / 跨段粘贴 / 明细行序的业务语义 / 明细行的服务端重排序。

---

## §4 完成判据（三条，缺一不可）

1. `COLLAB.md` 的 **`N-050`** 段内出现你方 **`MIMO-DONE`** 字样（回执含：改动文件、测试名、**变异逐条结果**、降级情况）；
2. **`HEAD` 越过基线**（基线＝你开工时的 `HEAD`；提交信息**请勿**以 `[WorkBuddy]` 开头）；
3. `bash scripts/check_all.sh` **退出码 0**（必绿 **8/8**、会报零命中）。

★ 提交纪律：**只用显式路径**（禁 `git add -A`；`internal/webui/dist` 可用**目录显式路径** `git add internal/webui/dist`）；
★ 完成**回写 `COLLAB.md` 并推送 `origin/main`**；★ 中途被打断 ⇒ 从断点继续，**已完成的项不要重做**。
