# MIMO-NEXT-BATCH-19 —— 批 23：`N-049` ② 第二半 —— `ref_exists` 增派生参数 `target_filter`（派生白名单）

> ★ **本文件可整份粘贴给 mimo code。**
> 交付方：**WorkBuddy**（规格；★ 已于本批入库 —— `spec/checks.json` **V1.22** 顶层新增 **`_pending_arg_note`**）／ 实现方：**mimo code**（`internal/specload` Go 侧 ＋ 单元测试）
> 依据：`COLLAB.md §4 · N-049`（② 第二半）；`REMAINING.md §1 B15 / §5 批 23`；**`spec/checks.json#_pending_arg_note`**（**规格正本**）；`spec/chain.json#conventions.node_actor_kind`
> 时间：2026-10-05

---

## §0 问题本体（一句话 ＋ 规格已定）

★ **现象（读代码取证，非推断）**：`S19`（`spec/checks.json`）的 `args` 现为

```json
{ "file": "spec/chain.json", "collect": "routes.**.actor",
  "target_file": "spec/chain.json", "target_dict": "roles",
  "extra_allowed": ["system"], "min_hits": 1 }
```

⇒ 白名单 ＝ **`chain.json#roles` 的全部键** ∪ `{"system"}`。★★ 而 `roles` 表里有 **3 个 `node_actor_kind == "none"` 的角色**（`group_finance` / `group_approval` / `sys_admin`）—— 它们**不是节点 actor**（Go 侧 `internal/chain/nodes.go` 的 `approverRoles` 只有 **5** 个键、`isActionActor` 只认 **3** 个字符串）⇒ ★★ **若将来把三者之一写成某个节点的 `actor`，`S19` 不报错，而该节点仍落 `appendAction`（`required: true` 却无人审）** —— 即 **判据在那一面失效**（与 `N-044`/`N-045` 的缺陷**同类**）。

★★ **规格已定（本批已入库）＝ `spec/checks.json#_pending_arg_note`**（唯一规格来源）。要点：
1. **参数形态**：`ref_exists` 新增**可选**参数 `target_filter` ＝ `{"field": <字符串，目标条目上的点路径>, "in": [<标量数组>]}`；★ **缺省 ⇒ 行为与原先逐字节一致**（向后兼容，`S6c` 等既有用法零影响）。
2. **语义三条**：① 白名单 ＝ `target_dict` 中「`field` **取得到标量值** **且** 该值 ∈ `in`」的**键**；取不到值（键不存在／路径中断／值为 `null`／值非标量）或值不在 `in` ⇒ **不进白名单**；② **与 `split` 互不影响、顺序无关**（`split` 作用于**收集到的引用值**，本参数作用于**目标白名单**）；③ **标量归一**与 `set_covers`/`path_exists`/`csv_col_eq_json_by_key` **完全一致**（字符串原样 · 布尔 → `"true"`/`"false"` · 整数 → 十进制无小数点）。
3. **fail-closed 三条**：① 参数**形态非法**（缺 `field` 或 `in`、`field` 非字符串、`in` 非数组）⇒ **报错**（★ 不得静默忽略）；② **凡 `target_dict` 的条目缺 `field`（取不到标量值）⇒ 报错并点名该键**；③ **过滤后白名单为空 ⇒ 报错**。
4. **落点预期（第 ③ 段我方执行，本批你**不要**动）**：`S19.args` 将增 `"target_filter": {"field": "node_actor_kind", "in": ["approver", "action"]}` ⇒ 白名单 ＝ **7 个角色键**（`approver` 5 ＋ `action` 2）∪ `system` ⇒ ★ 与 Go 侧 `approverRoles`(5) ∪ `isActionActor`(3) **集合相等**。

★★ **本批的硬次序 ＝ 你只做第 ② 步**：① 我方规格（**已入库**：`checks.json` **V1.22**）→ **② 你落 Go 侧参数**（★ 此时 `checks.json` **未引用** `target_filter` ⇒ 门禁**保持全绿**、**无红窗**）→ ③ 我方同批（Python 侧 ＋ `S19.args` ＋ 常驻探针）。★ **本批零新增判据、零新增原语**（`checks.json` 只多了一个**声明性**顶层字段）。

★ **切换无红窗已由我方实测（数据面）**：`spec/chain.json` 按 `routes.**.actor` 机械收集 ⇒ **45 处**、**去重 7 个取值**（`applicant`/`inspector_group`/`ops_supervisor`/`project_general_manager`/`purchaser`/`supervisor`/`system`）⇒ **新旧白名单下均 0 处报错**（★ 三个 `none` 角色**零出现**）；★ 反向变异（任一 `actor` → `group_finance`）⇒ 旧白名单 **0 处放行**、新白名单**恰 1 处报错**。★ 因此本批**不改任何 spec 数据**（`chain.json` **一字不动**）。

---

## §1 本批规格（★ 逐条对齐；**不在下表的一律不动**）

### T1 · `internal/specload/checklist.go` —— `case "ref_exists"` 支持 `target_filter`

★ 现状（`:160`–`:201`）：先由 `target_file` glob ＋ `target_dict` 收敛出 `targetKeys`，再把 `extra_allowed` 并入，然后逐条比对收集到的引用值。

★ **改法（保持既有代码结构，只在「建 `targetKeys`」处插入过滤）**：

1. 在 `extra := argStrings(c.Args, "extra_allowed")` 附近解析新参数（★ **建议**新增一个包内小 helper，避免把 `c.Args` 的解析散在主流程里；★ **命名自定**）：

```go
// target_filter（N-049 ② 第二半）：按 target_dict 每条目的某字段取值收窄白名单。
// 规格正本 ＝ spec/checks.json#_pending_arg_note。
// 形态：{"field": "<点路径>", "in": [<标量>…]}；★ 缺省 ⇒ 行为与原先逐字节一致。
type refTargetFilter struct {
    Raw   json.RawMessage
    Has   bool
    Field string
    In    map[string]bool   // 已按 scalarString 归一
}
```

2. **形态校验（fail-closed ①）**：`target_filter` 存在时，其 JSON 必须能解析为**对象**且**同时含** `field`（字符串、**非空**）与 `in`（**数组**，★ **允许为空数组**，其后果由 fail-closed ③ 兜住）⇒ 否则 `problems` 追加一条**点名**该判据 id 与参数名的错误（★ 文案自定，但须**可说清是哪一处声明写错**）。
3. **建白名单（含 fail-closed ②）**：

```go
targetKeys := map[string]bool{}
for _, tn := range globKeys(files, targetFile) {
    if root, ok := decoded[tn]; ok {
        for k, v := range asMap(scopeNode(root, targetDict)) {
            if filter.Has {
                fv := dig(v, filter.Field)          // ★ 复用既有 dig（单键/点路径）
                s, ok := scalarString(fv)           // ★ 复用既有标量归一
                if !ok {                            // 键不存在／路径中断／null／非标量
                    missing = append(missing, k)    // ★ fail-closed ②：点名
                    continue
                }
                if !filter.In[s] {
                    continue                        // 不在 in ⇒ 不进白名单（＝ none 同等）
                }
            }
            targetKeys[k] = true
        }
    }
}
for _, e := range extra { targetKeys[e] = true }    // ★ extra_allowed 仍**无条件**并入
```

4. **fail-closed ② / ③ 的报告**：`missing` 非空 ⇒ **每条**追加一条错误（**点名该键**，★ 不合并成一条）；`filter.Has && len(targetKeys) == 0` ⇒ 追加一条错误（★ **不得**静默当作「无违规」）。
5. ★ **不得改动**：`min_hits` / `split` / `extra_allowed` 的既有语义与调用顺序；`collectAcross` / `minHitsProblems` / `stringHits` 的行为；其它 11 个原语。

### T2 · 单元测试（★ 本批**验收的核心证据**；新建 `internal/specload/ref_exists_target_filter_test.go`）

★ 一律用**内存 `files`** 造夹具（该包既有测试的范式），**不读真 spec**；★ 每条用例一个 `t.Run`。

| id | 用例 | 断言 |
|---|---|---|
| `R1` | **缺省**（不写 `target_filter`） | 与原先**行为一致**：目标 dict 的**全部键**都在白名单（★ 造一个「会被过滤掉」的键也要能通过 ⇒ 证明缺省＝不过滤） |
| `R2` | `field` 命中 + 值 ∈ `in` | **无报错**；★ 同时确认 `in` 里**未出现**的目标键被**排除**（造一个值不在 `in` 的键，用引用去命中它 ⇒ **必须报错**） |
| `R3` | 引用值命中「值不在 `in`」的键 | **报错**（★ 这是本参数的**存在理由**：旧白名单会放行） |
| `R4` | 条目**缺 `field`** | **报错并点名该键**（★ 断言错误文案里**含该键名**） |
| `R5` | `field` 的**点路径**（`a.b`） | 取值正确（★ 证明用的是 `dig` 的点路径语义，不是单层取键） |
| `R6` | **形态非法**（缺 `in` / `field` 为空串 / `in` 非数组） | **各报错**（★ `in: []` **不算**形态非法 —— 它走 fail-closed ③） |
| `R7` | **过滤后白名单为空** | **报错**（★ 与 `R6` 分开断言，避免两条机制互相掩盖） |
| `R8` | **`extra_allowed` 与过滤并存** | `extra_allowed` 里的值**仍被接受**（★ 证明并入顺序未变） |

★ **另需一条回归钉**：既有 `ref_exists` 用法（含 `S6c` 的 `split` 管道串）在本批改动后**语义不变** —— ★ 用**既有测试**即可（`go test ./internal/specload -count=1` 必须全绿）；★ 若既有测试覆盖不到 `split`，请**补一条**（`R9`：`split` ＋ `target_filter` **同时设置**，两者**互不影响**）。

### T3 · 三条单点变异（★ 一次只改一处；`cp` 备份还原，**禁用 `git checkout --`**）

| 变异 | 预期 |
|---|---|
| **M1** 把「值不在 `in` ⇒ 跳过」改成「无条件加入白名单」（＝过滤恒不生效） | `R3` **精确转红**；★ `R1`/`R2`/`R8` 保持绿（隔离性） |
| **M2** 去掉「条目缺 `field` ⇒ 报错」分支（改成静默跳过） | `R4` **精确转红**；其余保持绿 |
| **M3** 去掉「过滤后白名单为空 ⇒ 报错」分支 | `R7` **精确转红**；★ 注意 `R4` 不得被连带（若连带，说明两条机制**互相依赖** ⇒ 请如实上报，**不要**把断言改弱） |

★ 变异做完**必须** `cp` 还原 ＋ `sha256sum -c` 核对 ＋ `go test ./internal/specload -count=1` 复绿。

### T4 · 硬边界（★ 越界即返工）

1. ★★ **不动 `spec/**` 一字** —— `chain.json` / `checks.json` / `forms/*` / `README.md` **全部只读**；★ **本批不改 `checks.json` 版本号**、**不加判据**、**不动 `S19.args`**（那是第 ③ 段我方的动作）。
2. ★ **不动 Python 侧**（`scripts/check_spec.py` 属我方域；★ 你改了会导致两侧不同步）。
3. ★ **不动其它 11 个原语**；★ **不动** `collectAcross` / `minHitsProblems` / `scalarString` / `dig` / `scopeNode` 的既有语义（★ `dig`/`scalarString` 只**复用**）。
4. ★ **不新增第三方依赖**；★ **不引入 `DisallowUnknownFields`**（本仓库 `specload` 刻意宽松）。
5. ★ **提交前必跑 `bash scripts/check_all.sh` ⇒ 必绿 8/8 ＋ 会报零命中**；★ **红了不入库**。
6. ★★ **只用显式路径提交** —— 本批应为 `internal/specload/checklist.go` ＋ 新建 `internal/specload/ref_exists_target_filter_test.go` ＋ `COLLAB.md`；★★ **禁止 `git add -A` / `git add .`**。

### T5 · 回执与自测（★ 交差时请逐条回答）

1. **改动文件清单**（显式路径）＋ 每个文件**改了哪几段**；
2. `R1`–`R8`（＋ 若补了 `R9`）逐条**通过情况**；
3. `M1`/`M2`/`M3` **逐条红/绿对照表**（★ 说清**哪个用例转红、哪些保持绿**；若出现连带红，**如实写**，别改断言）；
4. `bash scripts/check_all.sh` 的**总判定行**原文（必绿 8/8 ＋ 会报零命中）；
5. ★ **与本文档预期不一致之处**（口径差／做不到／发现新事实）—— ★ **如实报告，不要抢跑**；★ 有规格缺口时**先问我方**，不要自己定；
6. ★ 确认 `git status` 中**没有** `spec/**` 的改动（本批它必须一字不动）。

---

## §2 为什么本批「不加判据、不切 `S19.args`」（★ 请照此理解，别擅自加）

★ **Go 侧对「未实现的参数」是静默忽略**（现有实现只 `argString`/`argStrings`/`argInt` 取它认识的键）⇒ ★ 若把 `S19.args` **先**改成带 `target_filter`，而 Go 侧还没实现，则**两侧白名单宽度不同、同一判据两个结论** —— ★ 且**当前数据上两侧都绿**（三个 `none` 角色零出现）⇒ **这种分歧更难发现**（同 `N-048` 的段归一顺序、`N-055` 的 `min_hits`）。
⇒ ★ 因此次序不可颠倒：**你先落 Go 侧参数 → 我方再把 `S19.args` 切过去并同批落 Python 侧 ＋ 探针**。★ 届时**切换前**我方会先证「切换后仍绿」（本批已给出数据面证据）。

---

## §3 验收方式（我方，交办后执行；列此仅供你自检对标）

1. 独立复跑 `bash scripts/check_all.sh` ⇒ **必绿 8/8 ＋ 会报零命中**（★ 不采信自报）；
2. 逐行走查实现（`target_filter` 缺省路径是否**完全旁路** · 标量归一是否**复用 `scalarString`** · 三条 fail-closed 是否**真的返回 problems** · `extra_allowed` 并入顺序是否未变）；
3. ★★ **三条单点变异我方自做**（M1/M2/M3，一次只变异一处）＋ `cp` 备份 ＋ `sha256sum -c` 还原；
4. 独立复跑 `go test ./internal/specload -count=1`；★ 断言 `R3`/`R4`/`R7` 在对应变异下**必须转红**（鉴别力）；
5. ★ **`spec/**` 零改动**（`git show --stat` 核对）。

---

## §4 交办纪律（★ 硬性，与 §1–§3 同等效力）

1. ★ **工作范围仅限本仓库目录** —— 即 `C:\Users\haoduan\workspace\jx-procurement-platform`（当前仓库树内）；★ **不得读写仓库外任何路径**（含 RaiDrive 网络盘 `Z:` 及一切挂载盘）。
2. ★ **以 `COLLAB.md` 为准** —— 若与本机记忆、对话上下文、或其它文档冲突时，**一律以 `COLLAB.md` 台账为最高依据**；动手前先读 `COLLAB.md §1`（当前状态）与 `§4 · N-049` 段。
3. ★ **提交前必跑 `bash scripts/check_all.sh` ⇒ 必绿 8/8 ＋ 会报零命中**；★ **红了不入库**。
4. ★ **只用显式路径提交**（见 `T4` 第 6 条）；★★ **禁止 `git add -A` / `git add .`**；★ 提交信息**无需**加 `[WorkBuddy]` 前缀（那是我方前缀）。
5. ★ **完成后回写 `COLLAB.md` 台账**（本议题追加**回执块** ＋ 打 `MIMO-DONE` 标记，★ **状态字段留 `OPEN`** —— 第 ③ 段收口由我方做）**并推送 `origin/main`**。
