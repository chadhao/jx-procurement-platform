# 剩余需求拆分与批次计划（Remaining Work Plan）

> **依据**：`COLLAB.md §4` 议题状态（仅 **`N-045`/`N-046`** 为 OPEN）· `docs/18 §3.2` 未闭合项 · `spec/` 缺口普查 · 用户 2026-10-03 授权
> **授权口径（用户原话）**：「所有剩余需求拆分、梳理，并开发、测试推送完毕。中间不需要我授权……只有在遇到**重大需求调整**的情况下，再让我介入」
> **更新**：2026-10-04 08:36（★ 时间取自机器时钟，**不凭估计顺推**）

---

## 0. 分档图例

| 档 | 含义 | 谁能闭环 |
|---|---|---|
| **B** | 需**我方先出规格/口径**，然后才可交办 | WorkBuddy |
| **A** | 规格已定 ⇒ **可自动闭环**（我交办 → mimo 实现 → 我证伪对照验收 → 推送） | WorkBuddy ＋ mimo |
| **C** | ★ **需用户/集团决策 ＝ 重大需求调整** ⇒ **停手上报** | 用户 |
| **D** | 环境/运维/上线门槛 ⇒ **需用户操作**（非需求调整，但只有你能做） | 用户 |

---

## 1. B 档 · 我方先行（不交办 mimo，做完才派工）

| 编号 | 项 | 现状（证据） | 完成判据 |
|---|---|---|---|
| **B1** | ★ **`S16` 扩围** | ★ 只覆盖 `immutable=true` 的 **54/96** 个 `source∈{system,computed}` 字段实例 ⇒ **漏 42**（`N-039` 我方责任段） | 判据改为覆盖全 96；**门禁 8/8 仍绿**（须与 B2 同批落地，否则当场红） |
| **B2** | ★ **42 个盲区字段逐条定性** | 只读普查：**32 个** Go 侧有出现（需人读判定是否真生产点）· **10 个零命中**（`BA.anti_split_check_result`／`BA.petty_cash_receiver`／`BA.record_date`／`BA.is_key_sample_range`／`BA.is_monthly_supplier_rollover_warned`／`CT.approval_levels`／`PC.tier_approval_record`／`PR.stock_qty`／`SA.actual_vs_approved_diff_cents`／`SS.tier_chain_record`） | 每条给出 `carried_by_kind` ＋ 落点或"真缺口"结论 |
| **B3** | ★ **`anomaly_monthly_report` 落点口径** | 判据 `pending_implementation`；`t_submission` 无异常/专项说明列，看板无异常清单指标（`N-039` 拆分说明） | ✅ **已定稿并落地（2026-10-04 04:57，批 5）**：**落点＝表列** —— `spec/ledger-mapping.json` **V1.1** 给 `L09` 新增**可写列 `anomaly_note`**（「异常变更专项说明」，`writable: true`、`writer: 综合运营主管`、`when: 月度报送时`；★ 与既有 `is_emergency_closed` **同范式**、**零新增单据类型**）；字段定义已登记 `docs/reference/config-mapping.sample.json#ledger_field`。判据 `anomaly_monthly_report` ⇒ `carried_by_kind` **`pending_implementation` → `manual`** ＋ 落点写进 `carried_by`。★ **机检成立（实测双向）**：`writable: true` 而不登记字段定义 ⇒ `cmd/jxapproval#TestUnregisteredWritableLedgerFields` **红**；补登记 ⇒ **复绿 8/8**。★ 顺带**补登记 `is_anomaly_listed`**（`flow/finalize.go` 落账自检 6 列之一却**从未登记**）。★ **仍缺的「列示面」另立 `N-046`**（看板 16 无「采购变更异常」指标；★ 须与实现同批） |
| **B4** | `spec/acceptance.csv`（`N-017`） | 状态 `WK-DONE`，**须核实是否已交付** | 自然语言类 `checks` 有可机判载体 |
| **B5** | `spec/institution-anchors.json`（`N-006`） | 待产（`COLLAB §1`） | 制度条款 ↔ 判据双向可追溯 |
| **B6** | `spec/openapi.yaml` | 待产（`COLLAB §1`） | 与 `docs/05-API` 一致且可机检 |
| **B7** | ★★ **`N-044` 惰性必需节点**：`SS.tier_chain` / `PC.tier_approval` 声明 `required: true` 却**不生成审批任务** | ✅ **已定性**（2026-10-04，`N-043` 验收期**证伪对照实测**）：复合 `actor`（`"supervisor → project_general_manager"`）既非审批角色、也非动作 actor ⇒ `internal/chain/nodes.go:104` 落 `appendAction`（只记 `NodeName`）⇒ **`SS` 少「按档位审批」整级、`PC` 少「按该档位审批」整级** | ✅ **我方规格已出**（2026-10-04 02:31）：`thresholds.purchase.bands[*].approval_chain` ＋ `sole_source.nodes[2]`/`change.nodes[2]` 的 `tier_expand`（★ **只新增键** ⇒ 门禁 **8/8 全绿**）；★ 用户口径「重复的签批人只签一次」⇒ `exclude_roles=[project_general_manager]`。✅ **全闭环**（2026-10-04 03:58）：① mimo 实现 **`db18374`**（`expandTierApproval` 逐角色展开；handler 级可证 ＋ **三处单点变异精确转红**）⇒ **我方验收通过**（`AGREED`）；② **我方收尾**＝删 **5 个失效键** ＋ 落 **`S19`**（`checks.json` V1.11；★ **窄版：禁复合 `actor`**）—— ★ **红→绿同批实证**（加判据 ⇒ Python 4 处 ＋ Go 同时红；删键 ⇒ 复绿 8/8）。★★ **遗留**：完整版「禁惰性必需节点」（`actor` ⊆ 白名单）**阻塞于 `N-045`** ⇒ 见 `B8` |
| **B8** | ★★ **`N-045`：`emergency.backfill_approval` 第三处惰性必需节点 ＋ 完整版「禁惰性必需节点」判据** | ✅ **已定性**（2026-10-04，`N-044` **收尾期实测**）：`spec/chain.json#routes.emergency.nodes[4]`（`required: true`、`actor="按档位审批人"`、**无 `ref`/`tier_expand`**）⇒ `nodes.go:114` 落 `appendAction` ⇒ **不生成审批任务**；★ 与 `N-044` 同类但**不含 `→`** ⇒ `S19` **静默**；★ `emergency` 路由**未被任何 `doc_chains` 引用**（当前不可达） | ⏳ **卡口径（`C9`）**：「按对应档位补审批（**不得降档**）」的**档位取值源**需用户/业务定（我方倾向「关联 PR 档位 ∪ 就高」）；★ **口径到位前不派工**；到位后 ⇒ 落 `tier_expand`（同 `N-044` 范式）＋ 把 `S19` 升级为**完整版**（★ **必须与修复同批**，否则当场红） |

---

## 2. A 档 · 可自动闭环（我交办 → mimo 实现 → 我验收 → 推送）

| 编号 | 项 | 来源 | 量级 | 备注 |
|---|---|---|---|---|
| **A1** | 金额不一致审计 `TargetID` 由 `PR(unsaved)` 改为**真实 `biz_no`** | `N-041`（OPEN，我方已裁定做法） | 小 | 裁定：mismatch 信息保留到 `flow.Submit` 成功后写 |
| **A2** | ★ **批次三①：`T04` 权限位点** | `docs/18 §3.2 #2①`（用户明确要求） | 中 | `t_instance` / `t_ledger_archive` / `t_user_role` 新列 |
| **A3** | ★ **批次三②：人员管理页改造** | `docs/18 §3.2 #2②`（用户明确要求） | 中 | `/admin` 手填 `open_id` → **从镜像选人 ＋ 赋角色** |
| **A4** | **`SS`/`PC` 提交通道与审批时点判据** | `N-027` 未做项（随发起批 / `M9`） | 中 | 当时按"契约先做、表现层后做"切分 |
| **A5** | **完整明细 UI 增强**（行级错误定位 / 拖拽排序 / 复制行 / 批量粘贴） | `N-040` 边界（mimo 已如实登记） | 中 | 当前为"最小可用"，行级错误靠服务端 400 文案 |
| **A6** | `N-035` 「自批自派」拦截 —— **先核实是否已落** | `N-035`（AGREED） | 小 | 若已落则只需关闭议题 |
| **A7** | `N-038` 项④剩余：`PC#anomaly_monthly_report` 服务端侧 | `N-038`（OPEN，须先有 B3 口径） | ~~小~~ **零代码** | ★★ **改判（2026-10-04）：无需代码实现** —— `B3` 落点选**表列**（`L09.anomaly_note` 可写列）⇒ 写入走**台账页既有通用通道**（`t_ledger_ops.ops_json`），缺的只是一条**字段定义**（已登记进样例配置基线）。★ 唯一遗留＝把该字段定义**导入运行库**（配置导入通道；属运维步骤，非开发）⇒ **本项闭环** |
| **A9** | ★★ **看板 16 补「采购变更异常」指标**（`N-046`） | `B3` 定稿期发现（`COLLAB §4 · N-046`） | 小 | ✅ **已完成（2026-10-04 08:36）—— `N-046` 结案（`AGREED`）**：`spec/dashboard.json` 看板 16 新增**第 12 个指标 `change_anomaly_listed`**（我方）＋ 实现 `internal/dashboard#buildAnomaly`（mimo `e05420d`：读 `ArchiveExt` ＋ **列级守卫** `countExtRegistered(r09,"is_anomaly_listed")`）**同批**落地；我方独立验收（**两次单点变异精确转红、隔离成立** ＋ `cp`/`sha256` 还原）⇒ 门禁 **8/8**。★ 原先的定性依据（单独落 spec ⇒ `TestDashboardAlertKeysMatchSpec` 精确转红『11 ≠ 12』）**已被本轮实测复现并作为「同批」证据**。★ 遗留（非阻塞）：实现注释「**7** 个台账」应为 **6**（待 mimo 下轮订正）；★★ 教训：**「同批」批次我方 spec 必须先入库**，否则 `drive_mimo.sh` 判据③（含「净检出可构建」→ 测 HEAD）结构性不可满足
| **A8** | **GR / RFQ / QC 发起通路接线**（★ 含 `acceptors` 的 handler 端到端补测 —— `N-042` 回执如实登记的边界①） | `N-042` 验收 | 中 | `ResolveRoute` 暂无 `DocGR` case；`t_instance.acceptors` 的 HTTP 链路未端到端 |

---

## 3. C 档 · ★ 需用户/集团决策 —— **用户 2026-10-03 已回复：「C 档按你推荐的来」**

★★ **必须先分清两类，不能一律"按推荐"** —— 我**只对一部分给过推荐值**：

### 3.1 ★ 我方**给过推荐值**且已落实（用户已批「按推荐」）

| 编号 | 项 | 我方推荐 | 落实核验（2026-10-03） |
|---|---|---|---|
| **C1** | `B47 / S-1`：`L03` 无生产者 | **一对一放宽为一对多**（PR 同落 L02 ＋ L03） | ✅ **spec 已在**：`ledger-mapping.json#L03.resolution_note`（R-02）＋ `chain.json:832-834`。✅ **实现已在**：`internal/config/maps.go:17`、`store/repo_config.go:45`、`config/importmap.go:231` 三处均写明「PR 同时落 L02 与 L03」⇒ **已闭环** |
| **C8-B1** | 采三档（加强）节点顺序（`R-09`） | 按 `R-09` 裁定 | ✅ 已落（`chain.json` routes） |
| **C8-B2** | 对公直付范围 | 费用线 M 类可逆向组 | ✅ `chain.json:578`（`corporate_direct` 规则在位） |
| **C8-B3** | 销售部「项目总经理协管」含义 | 即"由项目总经理协管" | ✅ `chain.json:42` 原文在位 |
| **C8-B4** | 合同标准范本 | 按制度第三十六条**视为已定** | ✅ 制度侧已定，无需改代码 |
| **C8-B5** | 合同额超 PR 的浮动容差 | **≤10% 放行 / >10% 走 PC** | ✅ `spec/params.json:66` `contract.amount_over_pr_tolerance_percent` 在位 |
| **C8-B6** | 合同单补「用途分类」 | 从关联 PR 带入 | ✅ `spec/forms/CT.json` `usage_category_l1/l2` `source=system` ＋ rule「从关联 PR 带入」在位 |
| **C8-B7** | 「经办人 ≠ 需求提出人」升级为**硬拦截** | **升级为 hard** | ✅ `PR.json#no_self_purchaser`（`hard`/`submit`）＋ `#no_self_purchaser_at_designation`（`hard`/`code`）**均已在位** |

### 3.2 ★★ 我方**从未给过推荐值** ⇒ 「按推荐」**对该组不成立**，不得编造

| 编号 | 项 | 为什么我方没有推荐值 | 是否阻塞开发 |
|---|---|---|---|
| **C2** | `Q17` 实采 `approval_instance` 报文定幂等字段 | 需**真实报文**（现规格已按 `header.event_id`/`uuid` 定，Q17 是"用实采报文再确认一次"） | ❌ 不阻塞 |
| **C3** | `Q1` `approval_code` / 字段 id 映射表 | 值来自**飞书侧实际配置**（导入机制已就绪，缺的是填值） | ❌ 不阻塞（挡**上线**） |
| **C4** | `Q2` 飞书档位与 API 配额现值 | 需**账号侧数据** | ❌ 不阻塞 |
| **C5** | `Q4` 数据出境合规**书面结论** | 属**法务/合规**结论，非技术判断 | ❌ 不阻塞（挡上线） |
| **C6** | `Q19` 三单匹配容差 | 需**集团财务给数** | ⚠️ 挡**该条判据**的阈值定稿，不挡其余 |
| **C7** | 待决策清单 **A 组 5 项**（`A1` 代理人名单 / `A2` 报销时限 / `A3` 集团流程启动条件 / `A4` `unit` 候选集 / `A5` 集团归口） | ★ 全部是**人名 / 集团书面 / 制度未给的候选集** —— ★ **我方已明确表态"不自行编造"**（`A2` 是**唯一卡住制度定稿**的一项） | ❌ 不阻塞代码；★ `A2` 卡**制度定稿** |
| **C9** | `N-045` 的**档位取值源**：「紧急采购事后按对应档位补审批（**不得降档**）」 —— 取关联 PR 的档位 / 取就高 / 其他 | ★ 属**业务口径**（不是能从 `spec` 推出的）：★ 我方有**倾向值**（① 关联 PR 档位 ＋ ② `max(补录金额, 关联 PR 金额)` **就高**）但**未获批准** ⇒ ★ **不得自称「按推荐」**（同 §3.2 立场的纪律） | ❌ **不阻塞代码**（`emergency` 链**当前不可达**）；★ 卡 `N-045` 与**完整版判据** |

★ **处理方式**：`C2–C7` 一律标为 **「待外部输入 · 不阻塞开发」**，**不阻塞批 2–7 的推进**；等外部输入到位时回填。
★ **绝不用"按推荐"去覆盖我方没有推荐值的事项** —— 那等于替你编造集团口径。

## 4. D 档 · ★ **用户 2026-10-03 明确：「D 档不需要考虑，不是开发问题」⇒ 已移出本轮范围**

> ★ 以下仅作**事实留痕**，**不再作为我方待办、不再上报**。★ 唯一的例外性事实：`D1` 会影响**「收没收到」类验收结论的可信度**（属技术事实，非开发问题）。


| 编号 | 项 | 现状 |
|---|---|---|
| **D1** | ★★ **`R33` 服务器 `/etc/resolv.conf` 被 `dhcpcd` 周期性写空** | **最高阻塞**；不修则飞书链路时通时断、"收没收到"类验收结论**一律不可信** |
| **D2** | **飞书事件订阅终验** | `t_event_inbox` 仍 0 行；★ **须先修 `D1`**，再实际改一次部门名/加减成员作触发 |
| **D3** | 上线门槛三项 | ① `JX_ACTION_CALLBACK_TOKEN` 仍为测试值；② `DEV_MODE=true` 仍开；③ **服务器密码与 App Secret 建议轮换** |
| **D4** | `-race` 数据竞争补测 | 本机无 gcc（`-race` 依赖 cgo）⇒ 须在有 gcc 的机器上跑 |
| **D5** | 联调夹具残留清理 | 本地 7 表 ＋ 飞书侧脏数据（定义 `F5A235B2-…`、旧实例 `PR-TEST-0001`、测试卡片） |
| **D6** | 可选：`authen/v2/oauth/token` 迁 v3 · 补订阅 `contact.scope.updated_v3` 与 1.0 `user_status_change` | 官方 v2 已标历史版本 |

---

## 5. 批次执行顺序（我方推进节奏）

| 批次 | 内容 | 前置 |
|---|---|---|
| **批 1** | **B1 ＋ B2**（`S16` 扩围 ＋ 42 字段定性，必须同批落） | 无（无运行中轮次，此刻安全） |
| **批 2** | **A1**（`N-041`，小项，先跑通一轮） | 无 |
| **批 3** | **A2 ＋ A3**（批次三：权限位点 ＋ 人员管理页 —— 用户明确要求） | 无 |
| **批 4** | ✅ **A4**（`SS`/`PC` 提交通道与审批时点判据）—— **已完成**：`N-043`（mimo `655fdf1`）2026-10-04 02:08 我方验收通过 ⇒ `AGREED` | 无 |
| **批 5** | **B3 → A7**（月度报送口径 → 服务端落地） | ✅ **已完成（2026-10-04 04:57，**全在我方**）**：`B3` 落点＝**表列**（`L09.anomaly_note`）⇒ **`A7` 无需代码**（写入走既有通用通道）；判据 `carried_by_kind` → `manual`；门禁 8/8。★ **列示面另立 `A9`/`N-046`**（须与实现同批） |
| **批 6** | **A5**（完整明细 UI 增强）· **A6**（核实 `N-035`） | 无 |
| **批 7** | **B4 / B5 / B6**（`acceptance.csv` · `institution-anchors.json` · `openapi.yaml`） | 无 |
| **批 8** | ★★ **`N-044` 惰性必需节点修复**（`SS`/`PC` 档位审批整级缺失）—— 见 §1 的 **`B7`** | ★ **用户口径 2026-10-04 已到**（「流程上有重复的签批人，都是一次签批呀」⇒ 采纳**去重**案）；★ 我方规格**已出**（`bands[*].approval_chain` ＋ 两节点 `tier_expand`，**只新增键** ⇒ 门禁绿）⇒ **本批可派工**，任务包 [`MIMO-NEXT-BATCH-6.md`](./MIMO-NEXT-BATCH-6.md)；★★ **本批提前于批 5** —— 它是**已证实的实现缺陷**（`required: true` 却无人审），优先级高于批 5 的规格工作 |
| **批 9** | ★★ **`N-045` 第三处惰性必需节点**（`emergency.backfill_approval`）＋ `S19` 升级为**完整版** | ★ **待用户口径（`C9`）** —— 口径未定前**停手不派工** |
| **批 10** | ★★ **`A9` / `N-046`：看板 16 补「采购变更异常」指标**（三种例外类型在事后监管面的对称性） | ✅ **已完成（2026-10-04 08:36）**：我方先落 `spec`（第 12 指标 `change_anomaly_listed`）＋ 交付 [`MIMO-NEXT-BATCH-7.md`](./MIMO-NEXT-BATCH-7.md) → mimo 实现 `e05420d` → **我方独立验收通过**（**两次单点变异精确转红、隔离成立** ＋ `cp`/`sha256` 还原）⇒ **`N-046` 结案（`AGREED`）**；门禁 **8/8**。★★ **教训**：`drive_mimo.sh` 判据③含「净检出可构建」→ **测 HEAD** ⇒ **「同批」批次我方 spec 改动必须先入库**（本轮 spec 后提交，曾令判据③结构性不可满足 ⇒ 已停手并由我方提交 `662ef25` 收口） |

★ **每批的门禁与验收方式（固定动作，不因批量而放宽）**：
1. 我出**交办**（写进 `COLLAB.md`，含判据与边界）；
2. **CLI 驱动** mimo（`-m xiaomi-token-plan-cn/mimo-v2.6-flash --variant high`，`drive_mimo.sh` 三条判据 ＋ 中断续跑）；
3. 我**独立复核**：门禁 8/8 ＋ 读实现 ＋ ★ **证伪对照（一次只变异一处！）** ＋ 还原 sha256；
4. **推送** GitHub（用户 2026-10-03 立规：验收通过即推送）；
5. 更新 `COLLAB.md`（议题状态 ＋ `§1`），并把结论**如实**登记（含我方错误）。

★ **停手上报的唯一触发条件**：命中 **C 档**（重大需求调整）或 **D 档需你操作**的事项。

---

## 6. 变更记录

| 日期 | 变更 |
|---|---|
| 2026-10-04 08:36 | ★★★ **批 10 闭环（`A9` / `N-046`）：看板 16 补「采购变更异常」指标（spec 与实现同批）⇒ `N-046` 结案（`AGREED`）** —— ★ **我方**：`spec/dashboard.json` 看板 16 新增**第 12 个指标 `change_anomaly_listed`**（`render=alert`；`formula`＝`L09.exception_type = 采购变更` ∧ `是否进入异常清单 = true` 的单数；`fields` 引 `L09.例外类型` / `L09.是否进入异常清单`）＋ 同步「11→12 个指标」「7→6 个台账」；交付 [`MIMO-NEXT-BATCH-7.md`](./MIMO-NEXT-BATCH-7.md)。★ **mimo**：`e05420d`（`countChangeAnomalyListed` 读 `Row.ArchiveExt`、`countExtRegistered(r09,"is_anomaly_listed")` **列级守卫**；测试硬编码 `11→12` ＋ 新增 handler 级 `TestDashboardChangeAnomalyListed`）。★★ **我方独立验收（不采信自报）**：逐行读实现 ＋ **两次单点变异**（守卫源 → `len(r09)` ⇒ `TestDashboardGuardPerIndicator` 精确转红、其余保持绿；去掉「且」⇒ `TestDashboardChangeAnomalyListed` 精确转红、守卫用例保持绿 ⇒ **隔离均成立**）＋ `cp` 还原 ＋ `sha256sum -c` ＋ 门禁 **8/8**（收口后 / 还原后各一次）。★★★ **本轮最重要的一条新教训**：`drive_mimo.sh` 判据③＝`check_all` 必绿，**而「净检出可构建」测的是 HEAD** ⇒ ★ **凡「spec 与实现须同批」的批次，我方 spec 改动必须先入库**，否则判据③**结构性不可满足**（本轮实测：驱动在 mimo 提交后空烧尝试次数 —— 已及时停手，由我方提交 `662ef25` 收口后复绿）。★ mimo **自己诊断出该根因、如实登记、且拒绝代提我方文件**（守纪律）。★★ **顺手清一笔规格债（实测发现）**：`spec/dashboard.json` 的 `connected_requires` 16③ 与 `known_gaps` 第 6 条原称「**现聚合路径仍用旧 key** ⇒ 不得翻转」—— ★ **实测证伪**（9 个旧 key 在非测试代码零命中；`TestDashboardAlertKeysMatchSpec` ⊆＋等量 ＋ `TestDashboardAlertKeysRejectOldName` 9 个旧名逐个必红，双向机检在位）⇒ ★ 该债**已于 2026-10-01 `BATCH-3 T1` 还清、只是规格文本没跟上**（同「实现了没人回填」族）⇒ 改为「✅ 已解决」并**保留历史对照**。★ **遗留（非阻塞）**：实现/测试注释「12 个指标分布在 **7** 个台账上」应为 **6**（请 mimo 下轮订正）。★ 台账：`COLLAB.md`（`N-046` 验收块 ＋ 状态 `AGREED` · `§1` 六处 · 附录 C）· `REMAINING.md`（本节 · `§2 A9` · `§5 批 10`）· `spec/forms/PC.json`（`known_gaps` 末条状态 → 已落地） |
| 2026-10-04 04:57 | ★★★ **批 5 闭环（`B3` → `A7`，**全在我方，未派工**）；`N-038` 结案；新开 `N-046`/`A9`/批 10** —— ★★ **`B3` 落点定稿＝表列**：`spec/ledger-mapping.json` **V1.1** 给 `L09` 新增**可写列 `anomaly_note`**（「异常变更专项说明」，`writer: 综合运营主管`、`when: 月度报送时`）＋ **补登记 `is_anomaly_listed`**（落账自检 6 列之一却从未登记）；字段定义登记进 `docs/reference/config-mapping.sample.json`；判据 `PC#anomaly_monthly_report` ⇒ **`pending_implementation` → `manual`** ＋ 落点/裁定/拆分写进 `carried_by`/`verify_note`。★ **选「表列」而非「看板指标」的三条理由**（①判据诉求是**文本**说明、指标承不了；②有 `is_emergency_closed` **现成同范式**；③**零新增单据类型**）。★★ **两次独立探针把「落点不是空话」验掉**：① `writable: true` 不登记字段定义 ⇒ `TestUnregisteredWritableLedgerFields` **红**，补登记 ⇒ **复绿 8/8**；② 单独给看板 16 加指标 ⇒ `TestDashboardAlertKeysMatchSpec` **精确转红**『输出 11 ≠ spec 12』⇒ ★ **据此把 `N-046` 定性为「须与实现同批」**。★★ **顺手清掉一笔规格债**：**8 条 `pending_implementation` → `code`**（`spec/forms/PC.json` V1.1 七条 ＋ `spec/forms/PR.json` V1.1 一条）—— ★ 取证＝**逐条读实现 ＋ 跑 `TestInjectPCSSSystemFields`**（逐字段断言全 PASS），**非按名 grep**。★★★ **本轮教训：「写了没人读」的反面＝「实现了没人回填」** —— `special_explanation_required` 规格里一直写「**无生产点** ＋ 阈值待集团确认」，而 rule **早已写明**、实现随 `N-039`（`c1eb311`）**已落且过测** ⇒ **`carried_by_kind` 必须与实现验收同批回填**。★ 门禁 **必绿 8/8 ＋ 会报零命中**（判据 / Go 包 / 净检出**零波动**）。★ 台账：`COLLAB.md`（`§1` 六处 ＋ `N-038` 结案说明 ＋ 新 `N-046` ＋ 附录 C）· `REMAINING.md`（`§1 B3` · `§2 A7 改判/A9` · `§5 批 5✅/批 10` · 本节） |
| 2026-10-04 02:31 | ★★ **`N-044` 规格批（`B7`）我方先行部分完成** —— ★ 用户 2026-10-04 口径「**流程上有重复的签批人，都是一次签批呀**」⇒ 采纳**去重**案。★ `spec/chain.json` **只新增键**：`thresholds.purchase.bands[*].approval_chain`（`tier1=[ops_supervisor]` · `tier2/3=[supervisor,project_general_manager]`；**只列审批人**、**已按角色去重**）＋ `sole_source.nodes[2]`/`change.nodes[2]` 的 `tier_expand`（`tier_source` ＝ `amount_cents`/`r15_max`；SS `exclude_roles=[project_general_manager]`）⇒ ★ **门禁 8/8 全绿 ＋ 会报零命中**（判据 / Go 包 / 净检出**零波动**）。★ 旧键（复合 `actor` ＋ 伪 `ref`）**刻意保留** —— 删它们须与「禁惰性必需节点」判据（拟 `S19`）**同批**。★ 交付 **`MIMO-NEXT-BATCH-6.md`**（批 8）⇒ ★ **批 8 提前于批 5 派工**（已证实的实现缺陷优先）。★ 另发现 `SS.tier_chain_record`/`PC.tier_approval_record` 的**规格内部矛盾**（`SS.json#_note` 与 `PC.json#filled_at_note` 三处不自洽）⇒ **本批不派、我方另行定稿**。 |
| 2026-10-04 03:58 | ★★★ **批 8（`N-044`）验收通过 ⇒ 结案（`AGREED`）＋ 我方收尾；★★ 收尾期新发现 ⇒ 新开 `N-045`** —— mimo `db18374`（`expandTierApproval` 按 `bands[*].approval_chain` 逐角色展开 · `SourceNodeID=<节点id>_<role>` · `amount_cents`/`r15_max` 双源 ＋ 单边 fallback、全缺**可见失败** · handler 侧 inject **前移**至 `chain.Compute` 之前）；我方**三次独立复跑门禁全 8/8** ＋ **三处单点变异 A/B/C 精确转红、隔离性成立** ＋ `sha256sum -c` 还原（★ **未用 `git checkout --`**）。★ **我方收尾**：新增 **`S19`**（`checks.json` V1.11：`routes.*.nodes[*].actor` 禁复合串；★ **红→绿同批实证** —— 加判据 ⇒ **Python 报 4 处 ＋ Go `go test` 同时转红**（零引擎改动、两侧自动一致），删 5 键 ⇒ **复绿 8/8**）＋ 删 **5 个失效键**（`sole_source.tier_chain` 复合 `actor` ＋ 伪 `ref` · `change.tier_approval` 复合 `actor` · `purchase_tier2/3.contract_two_level` 复合 `actor`）。★★ **新开 `N-045`**：`emergency.backfill_approval`（`required: true`、`actor="按档位审批人"`）＝**第三处惰性必需节点**；★ `emergency` **未被 `doc_chains` 引用** ⇒ 当前不可达；★ 卡「不得降档」档位源口径 ⇒ 新增 **`B8` / `C9` / 批 9**。 |
| 2026-10-04 02:08 | ★★ **批 4（`A4` · `N-043`）验收通过 ⇒ 结案（`AGREED`）**（mimo `655fdf1`；独立复跑门禁 ×3 全 8/8 ＋ 四轮单点变异 ＋ `sha256 -c` 还原）。★★ **验收期的证伪对照挖出新发现**：变异「给 `PC×tier_approval` 加必填」**不转红** ⇒ 实测反证该节点**根本不生成审批任务** ⇒ 新开 **`N-044`**（惰性必需节点）⇒ 新增 §1 的 **`B7`** 与 §5 的 **批 8**（★ 其中一个口径**需用户确认**：SS 档位链 PGM 与 `pgm_final` 是否重复签批 ⇒ **已上报，未得口径前不派工**）。 |
| 2026-10-03 23:10 | 首版：B/A/C/D 四档 ＋ 批 1–7 执行顺序 |
