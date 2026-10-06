# MIMO-NEXT-BATCH-30 —— `N-072`：代理人正向消费门「**守栏未跟随**」⇒ 同批撤销

> **交付方**：WorkBuddy（制度规范与产品设计方）
> **执行方**：mimo code
> **日期**：2026-10-06（**批 52 · 本包为「我方待产出」的下一包 · ★ 本轮不派工**）
> **前置**：`M9`（转交 / 加签 / 回退 / 撤回）**已全部落码**（`N-060` `F4` · 批 28 交付并经我方独立验收）；★ 契约侧已由**本轮（批 52 · B 档）**据实订正 —— `spec/authority.json` **V1.2**（`enable_guard` 三键 ＋ `consumers[]` 两条 status 翻面）；★ 本轮**未动任何代码** ⇒ 本包是**实现侧**的跟随动作。
> **★ 动手前必读**：
> · **`spec/authority.json#enable_guard` —— 本包的**唯一权威契约来源**（★ **逐字**；`rule` / `why` / `lifting` 三键已据实重写，**冲突时以该文件现文为准**）
> · `spec/authority.json#consumers[transfer_rollback_by_agent]` / `#consumers[transfer_notify_approved]`（两条正向消费端 —— ★ 本包**只在告示面跟随**，**不动它们的实现**）
> · `COLLAB.md#N-072`（议题正文：本缺口怎么发现的、为什么不能只改一处）
> · `internal/httpapi/handlers_admin_role_agents.go`（**全文** —— ★ ① `:15` 头注 · ② `:28-31` 常量与其注释 · ③ `:120` `feature_enabled` 回传 · ④ `:121` `feature_note` 文案 · ⑤ `:175` POST 响应同键）
> · `internal/httpapi/router.go:216`（该组的路由注释「…只停用不删 + `feature_enabled=false`」）
> · `internal/specload/authority.go:10`（解析层头注「`feature_enabled` 标注（`enable_guard`：`M9` 前正向消费端未实现）」）
> · `web/src/views/Admin.vue:232`（脚本区注释）· `:244`（`agentFeature` 赋值）· `:493-499`（页签告示块 —— ★ `v-if` 判 `!agentFeature`）
> · `internal/httpapi/role_agents_n028_test.go:183-191`（★ **断言 `feature_enabled == false` 的那两行** —— 本包必须同批改）
> · `docs/05-API.md §3.9` 的「★ 启用状态」行（`:546`）

---

## 0. 硬约束（沿用，勿破）

- ★ **工作范围**：只能在 `C:\Users\haoduan\workspace\jx-procurement-platform` 目录内读写。
- ★ **以台账为准**：冲突时一律以 `COLLAB.md` / `spec/` **当前内容**为准（**与记忆冲突时以台账为准**）。
- ★ **提交只用显式路径**（**禁止 `git add -A`**）；提交后 `git show --stat HEAD` 复核。
- ★ **每次提交前跑 `bash scripts/check_all.sh`，必绿基线 9/9**（会报项应**零命中**）。
- ★ **还原用 `cp` ＋ `sha256sum -c`**，**不得用 `git checkout -- <file>`**。
- ★ **一次只变异一处**；★ **变异「仍绿」不能判通过**（先确认变异处**是否真被执行**）。
- ★★ **`spec/**` 本包一律不动** —— 规格**已由批 52 落齐**（`authority.json` **V1.2**）⇒ ★ **只消费、不改写**。
- ★★ **本包不新增、不删除任何路由** ⇒ 路由计数口径（`docs/05-API.md` 正本 ⇔ `spec/openapi.json` ⇔ `router.go`，**69 条**）**不得变动**；★ 但 `docs/05-API.md` **正文会变** ⇒ **必须由 `scripts/gen_openapi.py` 重新生成** `spec/openapi.json`（脚本重生、**不得手改**）＋ 跑 `--check` 必须 **OK（69）**。
- ★★ **`docs/**` 双方均可改**（`COLLAB.md §7`）：本包**必须**改 `docs/05-API.md` 那一行，改完**在 `§7` 登记一行**。
- ★★★ **本包的核心纪律 ＝「要么全改、要么不改」** —— ★ 见 §1 的理由：**只改一处**会当场把「告示说未启用」换成**另一种**账实不符（告示说已启用、而响应仍 `false`，或反之）⇒ ★ **宁可四项全做，不可只做两项**。
- ★★★ **前端产物是【入库】的，必须同批重建**：`internal/webui/webui.go` 用 `//go:embed all:dist` 内嵌 `internal/webui/dist/**`，而该目录**已被 git 跟踪**（`git ls-files internal/webui/dist/assets/Admin-*.js` 有命中）⇒ ★★ **只改 `web/src/views/Admin.vue` 而不重建 ⇒ 线上页面照旧显示「代理人功能未启用」**（源码改了、**服务出去的那份没改**）。⇒ ★ 本包**必须**用既有脚本 `bash scripts/build.sh` 重建并**显式提交变更后的 `internal/webui/dist/**`**（★ **哈希文件名会变** ⇒ 会同时出现「新增 `Admin-<新哈希>.js`」与「删除 `Admin-<旧哈希>.js`」—— ★ **两者都要提交**，`git status` 里**不得**留未提交的 dist 改动）。★ 注意：`web/dist/` 与 `bin/` **已 gitignore** ⇒ 不进库（**不用提交**）。

---

## 1. 背景（为什么有这一包）

★★ 本包治的是**一处「守栏未跟随」** —— **功能早就开了，告示还挂着「未启用」**：

| # | 事实（★ 已由我方独立取证） | 后果 |
|---|---|---|
| ① | `cmd/jxapproval/bootstrap.go` 调 `flowSvc.SetAgentAuthorizer(httpapi.NewTaskAgentAuthorizer(specBundle, db))` ⇒ ★★ **代理人正向消费门已装配** | **服务端确实会因存在代理人记录而放行转交 / 回退**（仅限本节点 · `fail-closed`） |
| ② | `internal/flow/ops.go` 的 `Transfer`（行 96）与 `Rollback`（行 342）**已在判权条件里消费 `agentAuthorizer`** | 同上 —— ★ 这是**运行期事实**，不是「线路已画好但没通电」 |
| ③ | `internal/flow/ops_test.go#TestTransferAgentAuthorization` 恰为**代理放行 / 代理拒绝 / `nil` 门＝仅本人 / `AddSign` 不走代理门** | ★ **`spec/authority.json#enable_guard.lifting` 的解除条件（「落地 ＋ 应拦 / 应放行两类用例」）**已满足** |
| ④ | ★★★ 而 `internal/httpapi/handlers_admin_role_agents.go:31` 仍是 `const roleAgentFeatureEnabled = false`，`:121` 的 `feature_note` 仍写「代理人功能未启用：本页仅登记配置；转交/回退按代理人待 **M9 落地后生效**」 | ★★ **`/admin` 页签对运维说了假话** —— 一句「未启用」，而功能**已生效** |
| ⑤ | `web/src/views/Admin.vue:493-499` 的告示块（`v-if` 判 `!agentFeature`）＋ `docs/05-API.md §3.9`「★ 启用状态」行 ＋ `internal/specload/authority.go:10` 头注 | ★ 三处**同口径陈旧**，合并产生「**系统自称未启用**」的错觉 |

★★ **现状定性（务必按此理解，别当成「守栏仍在生效」）**：**守栏的「服务端不得放行」那半已失效**，**「告示未撤」那半仍在** ⇒ ★ 这是**一处账实不符**，**不是**一处有意保留的安全边界。★★ 本仓的立场一贯是：**账实不符是红线** —— 它比「少做一个功能」严重，因为它让**后续所有人的判断基准**都是错的。

★★ **为什么必须「同批四件」**：这四项是**同一个事实的四个出口**（常量 / 常量注释 / 前端告示 / 契约行），★ **任缺其一就换一种错法**：

- 只改常量 ⇒ 前端**照旧**显示「未启用」（`agentFeature` 变 `true` 而已，告示块消失、**没有任何一处告诉运维「现在能用了」**）；
- 只改前端 ⇒ 响应仍 `feature_enabled:false` ⇒ **前端逻辑与后端字段打架**；
- 改了常量**没改测试** ⇒ `role_agents_n028_test.go` **当场转红** ⇒ ★ **门禁替你抓住**（这是好事，别绕过）；
- 改了 `docs/05-API.md` **没重生 `spec/openapi.json`** ⇒ `doc_sha256` 漂移 ⇒ **`--check` 当场转红**。

★ **一处历史说明（避免误读为「有人违反过指令」）**：`MIMO-NEXT-BATCH-2.md:47` 曾写「**也不要为了『让页签看起来有用』而放开 `feature_enabled`**」—— ★ 那句的**前提是「`M9` 尚未落地」**。★★ **现在的前提已经变了**（见上表 ①②③）：放开**不是为了「让页签看起来有用」**，而是**撤掉一句已经不成立的话**。★ 两者的区别在于**有没有运行期事实支撑** —— 现在有。

---

## `T1` · 后端 —— 常量与其全部注释面

### 1.1 改动点（逐处，★ 别多改）

| # | 位置 | 现值 | 要求 |
|---|---|---|---|
| ① | `internal/httpapi/handlers_admin_role_agents.go:31` | `const roleAgentFeatureEnabled = false` | → **`true`**（★ 这是本包的**唯一行为开关**） |
| ② | 同文件 `:28-30`（常量注释） | 写「正向消费端（代理人可转交/回退）**随 `M9` 落地才置 `true`**。解除条件见 `spec/authority.json#enable_guard.lifting` —— 未解除前，任何操作**不得**因存在代理人记录而放行（服务端本就不读本表参与解析，见链侧负向守卫）。」 | ★ **据实重写**：写明**解除条件已达成**（引 `spec/authority.json#enable_guard.lifting` 的「达成判定」）＋ 装配点（`bootstrap.go#SetAgentAuthorizer`）＋ 消费点（`ops.go` 的 `Transfer` / `Rollback`）＋ ★ **边界仍在**（仅限本节点 · 不可加签 · 不可撤回 · 备付金节点按节点排除 · `fail-closed`） |
| ③ | 同文件 `:15`（包级头注） | 写「★ `feature_enabled=false`：正向消费端属 `M9`；页签须显式标注「代理人功能未启用」（`README #24`）。」 | ★ **据实重写**：`feature_enabled=true`；★ 并保留「为什么当初要标」的历史（`README` 定案 **#24**：无消费端＝假配置）⇒ ★ **写清「当时标得对、现在该撤」**，别把历史一笔抹掉 |
| ④ | 同文件 `:120`（`feature_enabled` 回传行末注） | `// false：M9 前正向消费端未实现（README #24）` | ★ 据实 |
| ⑤ | 同文件 `:121`（`feature_note`） | `"代理人功能未启用：本页仅登记配置；转交/回退按代理人待 M9 落地后生效"` | ★ **改文案**，要点：**已启用**；**已登记的代理人在其被代理角色的节点上可执行「转交 / 回退」**；★ **仅限本节点**；★ **不可加签、不可撤回**；★ **备付金两节点不接受代理人**。★ **不承诺制度外的东西**（别写「代理人可代为审批一切操作」） |
| ⑥ | 同文件 `:175`（POST 响应同键） | 回传 `roleAgentFeatureEnabled` | ★ 走常量即可，**不必**改这行；★ **但请核一遍**它确实读的是同一个常量（**不得**另写一个字面量 `true`） |
| ⑦ | `internal/httpapi/router.go:216`（该组路由注释） | 「…只停用不删 + `feature_enabled=false`」 | ★ 据实 |
| ⑧ | `internal/specload/authority.go:10`（解析层头注） | 「`feature_enabled` 标注（`enable_guard`：`M9` 前正向消费端未实现）」 | ★ 据实（★ 该文件**只解析 `enable_guard` 的规则文本**，**不改解析行为**） |

### 1.2 明确不做（★ 越界即错）

- ★★ **不动** `enable_guard` 的**语义实现**（若有）—— ★ 本包**只撤告示**：`enable_guard` 在 `spec` 里**已据实重写**，**不得**在代码里另抄一份「守栏是否生效」的判断（那就是**第二份真相**）。
- ★★ **不动** `agent_authorizer.go` 的三重判定、**不动** `ops.go` 的判权条件、**不动** `AddSign` 的「仅本人」、**不动** `Cancel` 的「仅发起人」、**不动** `NodeAllowsAgent` 的备付金排除 —— ★ 这些**都已正确**，本包**不是**改判权。
- ★★ **不得**"顺手"把 `/admin` 页签**能力**扩了（例如新增代理人的字段、新增停用入口的形态）—— ★ 本包**只改文案与常量**，产品能力面**零变化**。
- ★ **不得**为了「让测试好过」而**删掉** `role_agents_n028_test.go` 的那两条断言 —— ★ **反向改**：断言一律由 `false` 翻 `true`（见 `T4`）。

---

## `T2` · 前端 —— `/admin` 角色代理人页签告示

| # | 位置 | 要求 |
|---|---|---|
| ① | `web/src/views/Admin.vue:493-499` | ★ `v-if` 判 `!agentFeature` 的那个告示块：★ **现文案已不成立**（写「正向消费端（代理人转交 / 回退）随 `M9` 落地后开启」）。★ **处置二选一，取其一并说明理由**：**(a)** 把该块改为**「已启用」的正面说明块**（`v-else` 或改为判 `agentFeature`）—— ★ **我方倾向 (a)**，理由：`feature_note` 已经在说「已启用 ＋ 四条边界」，页签**应当**把边界显示给运维（**告知边界 ≠ 堆说明**）；**(b)** 直接**移除**该块 —— 仅在你能说明「页面别处已足够表达」时才可。★★ **不得**留下任何「未启用」字样。 |
| ② | 同文件 `:232`（脚本区注释）· `:493`（模板注释） | ★ 据实（删掉「`feature_enabled=false` ⇒ 页签显式标注未启用」的旧述） |
| ③ | `web/src/views/Admin.vue:244` | `agentFeature.value = !!data.feature_enabled` ★ **不改**（它本来就是对的 —— 改的是它读到的值） |
| ④ | ★★★ **重建并提交内嵌产物** | 跑 **`bash scripts/build.sh`**（★ 该脚本第 `[2/3]` 步把 `web/dist/` 复制到 `internal/webui/dist/`）⇒ ★ 然后**显式提交** `internal/webui/dist/**` 的变更（★ 见 §0 的「前端产物是入库的」一条：**新旧哈希文件都要提交**，`git status` 里不得残留 dist 改动）。★★ **判据**：`grep -rn "代理人功能未启用" internal/webui/dist/` ⇒ **零命中** |

★ **前端构建环境**：`web/` 下已有 `node_modules/`（★ 若你的环境缺少依赖，**先在回执里说明**，★ **不要**未经确认就联网下载 —— 本项目的纪律是「下载前先确认」；★ 若确实无法本地构建 ⇒ **停手并报告**，由我方决定是补依赖还是改用别的处置）。

★ **前端请自行跑一遍构建**（若本仓有前端构建门禁，`:必绿` 项会覆盖；★ 若没有，请在回执里说明你**如何验证** `Admin.vue` 仍可编译）。

---

## `T3` · 契约行 —— `docs/05-API.md §3.9`

| # | 位置 | 现值 | 要求 |
|---|---|---|---|
| ① | `docs/05-API.md §3.9` 的「★ 启用状态」行（`:546`） | 「★ 正向消费端（**转交 / 回退由代理人操作**）属 `M9`、**未实现** ⇒ **`feature_enabled=false`**，页签须**显式标注「代理人功能未启用」**；★ 解除条件见 `spec/authority.json#enable_guard`（依据 `README` 定案 #24：无消费端＝假配置）」 | ★ **据实重写**：**`feature_enabled=true`**；★ 写明「**解除条件已达成** ⇒ 守栏在**服务端判权面已失效**（`bootstrap.go#SetAgentAuthorizer` 装配 ＋ `ops.go` 的 `Transfer`/`Rollback` 消费）；★ **告示面随本包一并更新**」；★★ **必须保留**「解除条件见 `spec/authority.json#enable_guard`」这条**指针**（★ 别把指针删了 —— 那会让口径失去正本） |

| # | 连带（★ 缺一即红） | 要求 |
|---|---|---|
| ② | `spec/openapi.json` | ★ 由 `python scripts/gen_openapi.py` **重新生成**（**不得手改一字节**）；★ 跑 `python scripts/gen_openapi.py --check` ⇒ **必须 `OK`（69 条路由）**；★ 预期 `diff` **只有 `doc_sha256` 一行**（★ 若出现别的行 ⇒ **停下来报告** —— 说明你改到了不该改的面） |
| ③ | `COLLAB.md §7`（`docs/` 改动登记） | ★ **登记一行**（时间 ＋ 文件 ＋ 一句话） |

---

## `T4` · 测试 —— 断言随事实翻面（★ 本包最容易做错的一处）

| # | 位置 | 现值 | 要求 |
|---|---|---|---|
| ① | `internal/httpapi/role_agents_n028_test.go:183`（注释） | 「GET 契约：`eligible_roles` 非空 / `feature_enabled=false` / `denied` 含两个备付金节点」 | ★ 据实（`=true`） |
| ② | 同文件 `:189-190`（断言） | `if data["feature_enabled"] != false { t.Errorf("feature_enabled 应为 false（M9 前），实为 %v", ...) }` | ★ **翻为 `true`**，★★ 且**错误文案要据实**（★ **不得**留「（`M9` 前）」这种已不成立的话 —— 错误文案会在别人排障时误导人） |
| ③ | ★ **新增断言（我方要求）** | — | ★★ **加一条「告示与事实同向」的断言**：`feature_note` **不得**再含「未启用」字样，且应点明**至少一条边界**（★ 建议断言含「加签」或「备付金」—— ★ 由你判断哪条最稳，**但必须是可鉴别的**：把 `feature_note` 改回旧文案 ⇒ 该断言**必须转红**）。★ 理由：本包的**全部风险**就是「改了常量忘了文案」；★ 一条断言就能把它钉住。 |

★ **验收会做单点变异**：把 `roleAgentFeatureEnabled` **改回 `false`** ⇒ ★★ **`T4②` 与 `T4③` 必须同时转红**，而**与本变异无关的用例必须保持绿**（★ 尤其 `internal/flow` 的代理/操作组 —— 它们**不读**这个常量，**必须**仍是 `ok`）。

---

## 2. 验收判据（我方将逐条独立复核）

1. ★ `bash scripts/check_all.sh` ⇒ **必绿 9/9 全绿 ＋ 会报项零命中**；★ 常驻探针（**动态收录**，本批新增 `_probe_n071.py`）全绿。
2. ★ `python scripts/gen_openapi.py --check` ⇒ **`OK`（69 条路由）**；★ `spec/openapi.json` 的 `diff` **仅 `doc_sha256`**。
3. ★ 全仓 `grep -rn "代理人功能未启用"` ⇒ ★★ **须逐条对上下列「允许保留」清单，除此以外零命中**（★ 这是本包**最直接的判据**）：
   - ★ **应当归零的四类（★ 本包的实做面）**：`web/src/views/Admin.vue` · `internal/webui/dist/**` · `internal/httpapi/handlers_admin_role_agents.go` · `docs/05-API.md`；
   - ★ **允许保留（历史留痕，不得删）**：`COLLAB.md`（附录 C 与议题历史）· `MIMO-NEXT-BATCH-2.md`／`MIMO-NEXT-BATCH-30.md`（**任务包历史／本包自身**）· `spec/authority.json#enable_guard.rule`（**引用式提及**：它引述「原文写什么」以说明订正理由，**不是**在告示未启用）· `spec/README.md`（同属引用式）· `scripts/_probe_n071.py`（**反证素材**常量 `OLD_GUARD_RULE`）；
   - ★ 其余（含 `bin/**` 之类**未入库**产物）**不计**。
4. ★ 全仓 `grep -rn "roleAgentFeatureEnabled"` ⇒ ★ 定义处为 **`= true`**，其余引用**全部读该常量**（**不得**出现第二个字面量）。
5. ★ `docs/05-API.md §3.9` 的「★ 启用状态」行**已据实**，且★ **仍保留**指向 `spec/authority.json#enable_guard` 的指针。
6. ★ **`internal/webui/dist` 已重建**：`grep -rn "代理人功能未启用" internal/webui/dist/` ⇒ **零命中**；★ 且 `git status --porcelain` 里**没有**未提交的 `internal/webui/dist/**` 改动。
7. ★ **两处单点变异**（请自行做完并写进回执，★ 我方会**独立重做**）：
   - **M1**：`roleAgentFeatureEnabled` **改回 `false`** ⇒ ★ `T4②` ＋ `T4③` **恰红**，`internal/flow` 包**保持 `ok`**；
   - **M2**：`feature_note` **改回旧文案**（「代理人功能未启用：…」）⇒ ★ **仅** `T4③` 恰红（★ 若 `T4②` 也红 ⇒ 说明你的新断言与旧断言**未隔离**，**请改新断言的口径**）。
   - ★ 还原一律 `cp` ＋ `sha256sum -c`。
8. ★ **回执要求**：★ 提交号 · 改动文件清单（**逐文件逐行说明**，★ 含 `internal/webui/dist/**` 的新旧文件）· 门禁原文 · 变异两处的红/绿实测原文 · `--check` 原文 · ★ **判据 3 / 4 / 6 的 `grep` 原文**（★ **贴输出，不要只说「已验证」**）。

---

## 3. ★ 不要做

- ★ **不要**顺手改 `spec/**`（本包规格已定；★ 若你认为 `spec/authority.json#enable_guard` 的**表述**有问题 ⇒ **写在回执里提出来**，★ **不要**自己改）。
- ★ **不要**顺手扩任何产品能力（代理人字段、停用形态、审批台入口……一律**不碰**）。
- ★ **不要**用「删测试」的方式让门禁变绿。
- ★ **不要**为了「看起来完整」而**新增路由** —— ★ 本包**零路由变化**；★ 一旦新增 ⇒ `docs/05-API.md` 正本 ⇔ `spec/openapi.json` ⇔ `router.go` 三处计数链**必红**。
- ★ **不要**只做 `T1` 就交差 —— ★ 见 `§1` 的理由：**只改一处会换一种错法**。

---

★ **包内四处「必须停下来问」的情形（★ 停手正确，别硬做）**：① `gen_openapi.py --check` 的 `diff` **不止 `doc_sha256`**；② 你发现 `feature_enabled` 另有**除本包四处之外**的消费端（★ 那就说明本包的「全改」清单不全 ⇒ **先报告**）；③ `T4③` 的新断言**无法做到与 `T4②` 隔离**（★ 那说明 `feature_note` 与 `feature_enabled` 在该测试里**同源**，需要先改测试结构 —— ★ **先报告**）；④ ★ **前端构建环境不可用**（缺依赖且不便联网下载 ⇒ **先报告**，★ 本项目纪律：**下载前必须先确认**）。
