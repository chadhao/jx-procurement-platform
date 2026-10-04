# 剩余需求拆分与批次计划（Remaining Work Plan）

> **依据**：`COLLAB.md §4` 议题状态（仅 **`N-045`/`N-046`** 为 OPEN）· `docs/18 §3.2` 未闭合项 · `spec/` 缺口普查 · 用户 2026-10-03 授权
> **授权口径（用户原话）**：「所有剩余需求拆分、梳理，并开发、测试推送完毕。中间不需要我授权……只有在遇到**重大需求调整**的情况下，再让我介入」
> **更新**：2026-10-04 09:56（★ 时间取自机器时钟，**不凭估计顺推**）

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
| **B4** | `spec/acceptance.csv`（`N-017`） | ✅ **已交付（2026-10-04 08:52 · 批 7 我方先行段）** —— ★ 交付前**先核实**：`find . -iname "*acceptance*"`（排除 `node_modules`/`.git`）**零命中** ⇒ 确认**从未交付**（`spec/README §2` 记「⏳ 随第一批」）。★ 交付＝**判据级验收台账 V1.0 · 97 条 · 11 列**（`spec/forms/*.json#checks` **全量**），列约定与两张受控词表写进 `spec/README.md §3.1`；★ **只引用 `(doc_type, check_id)`、不复制 `assert` 原文**（防第二份真相） | ✅ **完成** —— 自然语言类 `checks` **已有可机判载体**：**91 条**给出可判定表达式 ＋ **6 条**判为不可机判（其中 **4 条 `decision_kind=人工`**＝人工检查项：`SA#entertain_required` / `SA#inspection_basis` / `CT#sign_after_approval` / `PC#anomaly_monthly_report`；另 **2 条**因**承载字段不存在**：`PR#safety_branch` / `SA#cross_month_allocation`）。★★ **落表实测出 5 组问题 ⇒ 新开 `N-047`**（最要紧的一条：**23 条判据缺 `severity` ⇒ 落在 `S15` 与 `evaluateHardChecks` 的缝里**，既不执行也不报错） |
| **B5** | `spec/institution-anchors.json`（`N-006`） | ✅ **数据已交付（2026-10-04 09:56 · 批 7 的 `B5` 段，我方先行）** —— ★ **先读引擎再下结论**：既有 10 个原语**都表达不了**「锚点指向的文件/字段是否存在」（`ref_exists` 只比**单个 dict 的键集合**，而落点分散在任意文件任意深度）⇒ ★ **接入门禁须新增原语 `path_exists`**（`N-048`）。★ 索引本体＝机械抽取 **324 处**引用（**28 条**条款）⇒ **158 条重排稳定指针**（★ 逐条实测可解析）＋ 全量计数；★ 取证逼出**三条硬结论**（括号内不切分 / 数组索引换 `[k=v]` 选择器 / 生成器排除自身）；★ **不设条款标题**（不臆造） | ✅ **双向可追溯已达成**（条款 → 158 条指针；落点 → 条款可反查，另有全量计数）；★ **门禁**：须与 Go 侧同批（转 **§2 `A11` / §5 批 11**） |
| **B6** | `spec/openapi.yaml` | 待产（`COLLAB §1`） | 与 `docs/05-API` 一致且可机检 |
| **B7** | ★★ **`N-044` 惰性必需节点**：`SS.tier_chain` / `PC.tier_approval` 声明 `required: true` 却**不生成审批任务** | ✅ **已定性**（2026-10-04，`N-043` 验收期**证伪对照实测**）：复合 `actor`（`"supervisor → project_general_manager"`）既非审批角色、也非动作 actor ⇒ `internal/chain/nodes.go:104` 落 `appendAction`（只记 `NodeName`）⇒ **`SS` 少「按档位审批」整级、`PC` 少「按该档位审批」整级** | ✅ **我方规格已出**（2026-10-04 02:31）：`thresholds.purchase.bands[*].approval_chain` ＋ `sole_source.nodes[2]`/`change.nodes[2]` 的 `tier_expand`（★ **只新增键** ⇒ 门禁 **8/8 全绿**）；★ 用户口径「重复的签批人只签一次」⇒ `exclude_roles=[project_general_manager]`。✅ **全闭环**（2026-10-04 03:58）：① mimo 实现 **`db18374`**（`expandTierApproval` 逐角色展开；handler 级可证 ＋ **三处单点变异精确转红**）⇒ **我方验收通过**（`AGREED`）；② **我方收尾**＝删 **5 个失效键** ＋ 落 **`S19`**（`checks.json` V1.11；★ **窄版：禁复合 `actor`**）—— ★ **红→绿同批实证**（加判据 ⇒ Python 4 处 ＋ Go 同时红；删键 ⇒ 复绿 8/8）。★★ **遗留**：完整版「禁惰性必需节点」（`actor` ⊆ 白名单）**阻塞于 `N-045`** ⇒ 见 `B8` |
| **B8** | ★★ **`N-045`：`emergency.backfill_approval` 第三处惰性必需节点 ＋ 完整版「禁惰性必需节点」判据** | ✅ **已定性**（2026-10-04，`N-044` **收尾期实测**）：`spec/chain.json#routes.emergency.nodes[4]`（`required: true`、`actor="按档位审批人"`、**无 `ref`/`tier_expand`**）⇒ `nodes.go:114` 落 `appendAction` ⇒ **不生成审批任务**；★ 与 `N-044` 同类但**不含 `→`** ⇒ `S19` **静默**；★ `emergency` 路由**未被任何 `doc_chains` 引用**（当前不可达） | ✅ **已闭环（2026-10-04 09:45 · 批 9 验收通过 ⇒ `N-045` 结案 `AGREED`）** —— ★ mimo **`6173edb`**（`Facts.RelatedPRAmountCents` ＋ `case "emergency_max"`：**两值逐一检查、取 `max` 后 `TierOf`、零单边 fallback**）＋ 我方 **`S19` 完整版**（`ref_exists`；**红→绿同批实证**）⇒ ★ **「定性 → 规格 → 实现 → 判据」四段走完**；★ 独立验收＝门禁 **8/8** ＋ **我方自做三处单点变异**（隔离性成立）＋ `cp`/`sha256` 还原。★ 此前 → ✅ **口径已到 ＋ 我方规格已落（2026-10-04 09:21）⇒ 可派工** —— ★ 用户原话「**C9 就高**」⇒ 采纳我方倾向案 ②：**档位 ＝ `tier_of(max(补录金额, 关联 PR 金额))`**（裁定 **`R-31`**）。★ **我方已落**：`spec/chain.json`（`routes.emergency.nodes[4]` **删描述性 `actor`「按档位审批人」** ＋ 落 `tier_expand{tier_source=emergency_max, exclude_roles=[supervisor,ops_supervisor]}` · `routes.emergency.rules` 补档位公式 · ★★ **`conventions` 新增 `tier_source` 取值约定**）＋ `spec/RESOLUTIONS.md#R-31` ＋ 任务包 [`MIMO-NEXT-BATCH-8.md`](./MIMO-NEXT-BATCH-8.md)（批 9）。★★ **关键设计**：`emergency_max` **两值必须齐全、缺任一即可见失败** —— ★ 与 `N-044` 的 `r15_max` 「单边可得取单边」**刻意不同**（「不得降档」是**单调上界**，单边取值**可能低于应属档位**）。★ 附带（次级、待复核）：`exclude_roles=[supervisor, ops_supervisor]`（`seq2` 紧急认定 / `seq6` 核销闭合已各有一个签字点）。★ **随后由我方**把 `S19` 升级为**完整版**（★ **必须与修复同批**，否则当场红） |

---

## 2. A 档 · 可自动闭环（我交办 → mimo 实现 → 我验收 → 推送）

| 编号 | 项 | 来源 | 量级 | 备注 |
|---|---|---|---|---|
| **A1** | 金额不一致审计 `TargetID` 由 `PR(unsaved)` 改为**真实 `biz_no`** | `N-041`（OPEN，我方已裁定做法） | 小 | 裁定：mismatch 信息保留到 `flow.Submit` 成功后写 |
| **A2** | ★ **批次三①：`T04` 权限位点** | `docs/18 §3.2 #2①`（用户明确要求） | 中 | `t_instance` / `t_ledger_archive` / `t_user_role` 新列 |
| **A3** | ★ **批次三②：人员管理页改造** | `docs/18 §3.2 #2②`（用户明确要求） | 中 | `/admin` 手填 `open_id` → **从镜像选人 ＋ 赋角色** |
| **A4** | **`SS`/`PC` 提交通道与审批时点判据** | `N-027` 未做项（随发起批 / `M9`） | 中 | 当时按"契约先做、表现层后做"切分 |
| **A5** | **完整明细 UI 增强**（行级错误定位 / 拖拽排序 / 复制行 / 批量粘贴） | `N-040` 边界（mimo 已如实登记） | 中 | 当前为"最小可用"，行级错误靠服务端 400 文案 |
| **A6** | `N-035` 「自批自派」拦截 —— **先核实是否已落** | `N-035`（**AGREED**） | 小 | ✅ **已核实（2026-10-04 08:52）：已落且议题已结** —— `COLLAB.md#N-035` 状态字段＝**`AGREED`**（2026-10-01 16:10 我方验收通过：`flow/designation.go` 的 `actor == applicant && purchaser == applicant ⇒ ErrInvalidDesignation`；**只拦两者同时成立**，两个「允许」原样保留；三向用例齐 ＋ 锚定测试）⇒ ★ **本项无需任何动作** |
| **A7** | `N-038` 项④剩余：`PC#anomaly_monthly_report` 服务端侧 | `N-038`（OPEN，须先有 B3 口径） | ~~小~~ **零代码** | ★★ **改判（2026-10-04）：无需代码实现** —— `B3` 落点选**表列**（`L09.anomaly_note` 可写列）⇒ 写入走**台账页既有通用通道**（`t_ledger_ops.ops_json`），缺的只是一条**字段定义**（已登记进样例配置基线）。★ 唯一遗留＝把该字段定义**导入运行库**（配置导入通道；属运维步骤，非开发）⇒ **本项闭环** |
| **A9** | ★★ **看板 16 补「采购变更异常」指标**（`N-046`） | `B3` 定稿期发现（`COLLAB §4 · N-046`） | 小 | ✅ **已完成（2026-10-04 08:36）—— `N-046` 结案（`AGREED`）**：`spec/dashboard.json` 看板 16 新增**第 12 个指标 `change_anomaly_listed`**（我方）＋ 实现 `internal/dashboard#buildAnomaly`（mimo `e05420d`：读 `ArchiveExt` ＋ **列级守卫** `countExtRegistered(r09,"is_anomaly_listed")`）**同批**落地；我方独立验收（**两次单点变异精确转红、隔离成立** ＋ `cp`/`sha256` 还原）⇒ 门禁 **8/8**。★ 原先的定性依据（单独落 spec ⇒ `TestDashboardAlertKeysMatchSpec` 精确转红『11 ≠ 12』）**已被本轮实测复现并作为「同批」证据**。★ 遗留（非阻塞）：实现注释「**7** 个台账」应为 **6**（待 mimo 下轮订正）；★★ 教训：**「同批」批次我方 spec 必须先入库**，否则 `drive_mimo.sh` 判据③（含「净检出可构建」→ 测 HEAD）结构性不可满足
| **A10** | ★★ **`N-045`：`emergency.backfill_approval` 的档位展开实现**（`tier_source=emergency_max`） | `N-044` 收尾期实测发现（`COLLAB §4 · N-045`） | 中 | ✅ **已完成（2026-10-04 09:45）**：mimo **`6173edb`** 实现 ⇒ 我方**独立验收通过**（**三处单点变异隔离性成立**，含 mimo **自行补的跨档判别行**；`cp`/`sha256` 还原）＋ **我方 `S19` 完整版红→绿实证** ⇒ ★ **`N-045` 结案 `AGREED`**。★ 此前 → ★★ **口径已到（2026-10-04「就高」）＋ 我方规格已落 ⇒ 可派工**：`internal/chain` 新增 `Facts.RelatedPRAmountCents` ＋ `resolveTierForExpand` 新增 `emergency_max`（**两值必须齐全、缺任一即可见失败**；★ 严禁照 `r15_max` 做单边 fallback —— 那会在「紧急买 3,000 元、但属 5 万元 PR」场景**静默降档**）；任务包 [`MIMO-NEXT-BATCH-8.md`](./MIMO-NEXT-BATCH-8.md)。★ **本批只要求 chain 层可测**（`emergency` 路由**当前不可达** ⇒ `ResolveRoute`/`doc_chains` 接线属 `A8`、**不要顺手做**）。★ 验收通过后由我方把 `S19` 升**完整版**（`actor` ⊆ 白名单）结案 |
| **A11** | ★★ **`path_exists` 原语（Go 侧）＋ `S20` 判据**（`N-006` 接入门禁） | `B5` 交付期**取证逼出**（`COLLAB §4 · N-048`） | 小 | ★ **Python 侧我方已落地并自测**（`split_ptr_segs` ＋ `_ptr_walk` ＋ `prim_path_exists`；正向 **158 条指针 0 报错** ＋ 三条反向**各精确 1 条**）⇒ **只剩 Go 侧**：任务包 [`MIMO-NEXT-BATCH-9.md`](./MIMO-NEXT-BATCH-9.md)（批 11）。★ **同批**：mimo 落 Go ⇒ ★ **先** mimo 提交（`checks.json` 未引用 ⇒ 门禁保持全绿）⇒ **再** 我方落 `primitives` 声明 ＋ `S20` ＋ 正式探针 |
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
| **C7** | 待决策清单 **A 组 5 项**（`A1` 代理人名单 / `A2` 报销时限 / `A3` 集团流程启动条件 / `A4` `unit` 候选集 / `A5` 集团归口） | ★ 全部是**人名 / 集团书面 / 制度未给的候选集** —— ★ **我方已明确表态"不自行编造"**。★★ **2026-10-04 更正**：本条此前称「**`A2` 是唯一卡住制度定稿的一项**」—— ★ **实测更正**：`A2`（报销时限／超期处置）**2026-09-30 已定案并已落规格**（`params.json#reporting.monthly_cutoff_day=25` ＋ `#reporting.overdue_handling=auto_next_month`；`enums.json#pending_enums` 亦标「已定」），且 **2026-10-04 经用户复核确认「逐条一致」**（「每月报销，过期延下月，**一直往后只提醒不限制**」）⇒ ★ **规格侧零待办**；**真正遗留的只有制度正本（非本仓库）第三十九条字样改写** ⇒ ★ **不阻塞任何开发批次**。★ `A1`/`A3`/`A4` 维持「可先用默认值顶着」 | ❌ 不阻塞代码；★ 制度正本改写属**我方文档待办** |
| **C9** | `N-045` 的**档位取值源**：「紧急采购事后按对应档位补审批（**不得降档**）」 —— 取关联 PR 的档位 / 取就高 / 其他 | ★ 属**业务口径**（不是能从 `spec` 推出的）：★ 我方原**倾向值**＝① 关联 PR 档位 ＋ ② `max(补录金额, 关联 PR 金额)` **就高** —— ★ **已获批准** | ✅ **已定（2026-10-04 · 用户原话「C9 就高」）⇒ 采纳倾向案 ②**：档位 ＝ `tier_of(max(补录金额, 关联 PR 金额))`（裁定 **`R-31`**）⇒ ★ 已转 **§2 的 `A10` / §5 的批 9** 并**已于 2026-10-04 09:45 闭环**（`N-045` `AGREED`）；★ **不再卡任何东西** |

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
| **批 7** | **B4 / B5 / B6**（`acceptance.csv` · `institution-anchors.json` · `openapi.yaml`） | ★ **无**（三项均属**我方先行**，不依赖 mimo）—— ★ **`B5` ✅ 数据已交付（2026-10-04 09:56）**：`spec/institution-anchors.json` **V1.0**（28 条款 / 158 条稳定指针 / 全量 324 处引用）—— ★ **其「接入门禁」不在本批**，转 **§2 `A11` / 批 11**（`N-048`，须与 Go 侧同批）。★ **`B4` ✅ 已完成（2026-10-04 08:52）**：`spec/acceptance.csv` **V1.0**（97 条判据 · 11 列：可判定表达式 ＋ 承载者）＋ `spec/README.md` **V1.3**（新增 §3.1 列约定 ＋ 修两处自身陈旧）；★★ **落表实测出 5 组问题 ⇒ 新开 `N-047`**。★ **`B5`（`institution-anchors.json`）· `B6`（`openapi.yaml`）待产** —— ★ 二者**无外部依赖，可继续由我方单独推进**；★ `B6` 的**人读正本**在 `docs/05-API`（体量大 ⇒ 建议先做 `B5`） |
| **批 8** | ★★ **`N-044` 惰性必需节点修复**（`SS`/`PC` 档位审批整级缺失）—— 见 §1 的 **`B7`** | ★ **用户口径 2026-10-04 已到**（「流程上有重复的签批人，都是一次签批呀」⇒ 采纳**去重**案）；★ 我方规格**已出**（`bands[*].approval_chain` ＋ 两节点 `tier_expand`，**只新增键** ⇒ 门禁绿）⇒ **本批可派工**，任务包 [`MIMO-NEXT-BATCH-6.md`](./MIMO-NEXT-BATCH-6.md)；★★ **本批提前于批 5** —— 它是**已证实的实现缺陷**（`required: true` 却无人审），优先级高于批 5 的规格工作 |
| **批 9** | ★★ **`N-045` 第三处惰性必需节点**（`emergency.backfill_approval`）＋ `S19` 升级为**完整版** | ✅ **已完成（2026-10-04 09:45）** —— mimo **`6173edb`**（`emergency_max` **两值齐全**）＋ 我方 **`S19` 完整版**（红→绿实证）⇒ **`N-045` 结案 `AGREED`**；★ **次序（硬）四段全走完**：① 我方规格（入库 `1783096`）→ ② mimo 实现（`6173edb`）→ ③ 我方升 `S19` 完整版（此时必绿）。★ 此前 → ★★ **口径已到（2026-10-04「C9 就高」）＋ 我方规格已落（09:21）⇒ 可派工** —— 规格：`chain.json`（`routes.emergency.nodes[4]` 删描述性 `actor` ＋ `tier_expand{tier_source=emergency_max, exclude_roles=[supervisor,ops_supervisor]}` · `routes.emergency.rules` · `conventions.tier_source`）＋ `RESOLUTIONS.md#R-31`；任务包 [`MIMO-NEXT-BATCH-8.md`](./MIMO-NEXT-BATCH-8.md)。★ **次序（硬）**：① 我方规格（已完成、已入库）→ ② mimo 实现 → ③ 我方升 `S19` 完整版（此时必绿）—— ★ 判据先落而节点未修 ⇒ **当场红**（`N-044` 实测） |
| **批 11** | ★★ **`A11` / `N-048`：`path_exists` 原语（Go 侧）⇒ 制度锚点接入门禁** | ★ **我方数据已交付（`institution-anchors.json` V1.0）＋ Python 侧引擎已落地并自测** ⇒ 可派工；任务包 [`MIMO-NEXT-BATCH-9.md`](./MIMO-NEXT-BATCH-9.md)。★ **次序（硬）**：① mimo 落 Go 侧原语（★ 此时 `checks.json` **未引用**该原语 ⇒ 门禁**保持全绿**，不会单侧落红）→ ② 我方同批落 `checks.json#primitives.path_exists` ＋ **`S20`** ＋ 正式探针 `scripts/_probe_n048.py` ⇒ 两侧齐 ⇒ 绿 |
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
| 2026-10-04 09:56 | ★★★ **`B5` 数据段闭环（`spec/institution-anchors.json` V1.0）＋ 新开 `N-048` / `A11` / 批 11** —— ★★ **交付**：机械抽取 `spec/**/*.json` **全量 324 处**「第X条」引用（**28 条**条款）⇒ 逐条款 **158 条重排稳定指针**（★ 逐条实测**全部可解析**）＋ `citation_count`/`citation_by_file` **全量计数**（反向方向）。★★★ **取证逼出三条硬结论**（可复现）：**①** 指针语法须规定「**括号内不切分**」（实证：`spec/params.json` **5 个含点扁平键** ＋ `change_log[version=1.5]` 选择器值含点）⇒ 不规定则 `split(".")` 两者都切错，★ 但**切错＝命中 0＝红**（**安全失败**，不会静默取错节点）；**②** 锚点原始形态是**数组索引** ⇒ **重排即静默错位**、存在性检查抓不住 ⇒ 一律换 `[k=v]` 选择器；**③** ★ **生成器必须排除索引文件自身**（实测**自引用污染 324 → 353**）。★ **诚实划界**：**不设 `topic`**（制度正本非本仓库 ⇒ 臆造标题＝假信息）；**指针级全量索引未做**（22 处落在无稳定键数组）；**接入门禁未启用**。★★ **我方 Python 侧原语已落地并自测**（`scripts/check_spec.py#prim_path_exists`，★ **未写进 `checks.json#primitives`** ⇒ 对门禁不可见）：正向 **0 报错** ＋ 三条反向**各精确 1 条** ＋ 还原后 0 条。★ 门禁 **8/8 ＋ 会报零命中**（**新 spec 文件对门禁透明**）。★ 台账：`COLLAB.md`（**新议题 `N-048`** ＋ `§1` 七处 ＋ 附录 C）· `spec/README.md` **V1.5**（§2 ＋ **新增 §3.2 指针语法** ＋ §4 ＋ §6）；★ 交付 [`MIMO-NEXT-BATCH-9.md`](./MIMO-NEXT-BATCH-9.md)（批 11）。 |
| 2026-10-04 09:45 | ★★★ **批 9 闭环（`N-045` · `A10`）：`emergency.backfill_approval` 档位展开（`emergency_max` 就高）⇒ `N-045` 结案（`AGREED`）** —— ★ **驱动 mimo 第 1 次即完成（13m50s）**，HEAD **`6173edb`**（mimo 自行推送）⇒ **判据③无结构性障碍**（我方 spec 已先行入库 `1783096`，**实测印证 `N-046` 教训**）。★★ **我方独立验收（不采信自报）**：门禁独立复跑 **8/8 ＋ 会报零命中**；逐行读实现（`Facts.RelatedPRAmountCents` ＋ `resolveTierForExpand` 的 `case "emergency_max"`：两值逐一检查、错误文案分别点名、取 `max` 后 `TierOf`、**零单边 fallback、零档位字面量**，与 `conventions.tier_source` ③ 逐字对齐）；★★ **我方自己重做三处单点变异**（A 单边可得取单边 ⇒ 恰红缺值一条、其余绿；B 只取 `AmountCents` ⇒ 红缺值 ＋ **跨档判别行**；C `exclude_roles` 失效 ⇒ 连带红 `TestTierExpandSS` 2 子例〔共享构造，连带红正确〕）⇒ 隔离性成立；`cp` ＋ `sha256sum -c` 还原 OK。★★★ **我方收尾（同批）**：`S19` **窄版 `pattern_absent` → 完整版 `ref_exists`**（`checks.json` **V1.11 → V1.12**；`actor` ⊆ `chain.json#roles` 键 ∪ `extra_allowed:["system"]`；★ 用**引用式白名单**而非写死字面量 —— 「**判据＝数据**」）＋ **红→绿同批实证**（探针加回描述性 `actor` ⇒ **Python ＋ Go `go test ./internal/specload` 同时红、文案一致**〔零引擎改动、两侧自动一致〕；移除 ⇒ 复绿）；`spec/README.md` **V1.4**。★★ **如实登记两处**：**(a) 我方失误** —— 任务包 `T4` 变异 B 要求「第 2 行必须转红」在给定数值下**结构上不可满足**（两值**同属采二档**、对该变异**无鉴别力**），mimo **自行补跨档判别行**承担鉴别力并**主动上报**；**(b) 鉴别力观察**（第 2/3 行行名「就高」但数值未跨档）⇒ **不返工**。★ **移交两个后续小项**（我方域、非阻塞）：Go 侧 `approverRoles` 改由 **spec 派生** ＋ 补一行**跨档就高正例**。★ 台账：`COLLAB.md`（`N-045` 验收块 ＋ `AGREED` ＋ `§1` 七处 ＋ 附录 C）；`REMAINING.md`（`B8`/`A10`/`C9`/批 9/§6）。 |
| 2026-10-04 09:21 | ★★★ **用户批复两个卡点 ⇒ 批 9 解锁（我方落规格，未派工）** —— ★ 用户原话：「**C9就高，C7每月报销，过期延下月，一直往后只提醒不限制。你所有的任务非阻塞的都要自动继续，不要等我。**」⇒ ⓐ **`C9` 采纳我方倾向案 ②「就高」**：紧急采购补审批档位 ＝ `tier_of(max(补录金额, 关联 PR 金额))`（裁定 **`R-31`**）⇒ **`N-045` 卡点解除**；ⓑ **`C7-A2` 复核确认**（与 2026-09-30 定案**逐条一致**）⇒ ★ **规格侧零改动**（`R-23` 的「超期处置待定」遗留闭合），★ **更正**本节 `§3.2 C7` 长期表述「`A2` 是唯一卡住制度定稿的一项」—— 实测该口径早已定案落规格，**真正遗留的只有制度正本第三十九条字样改写**（非本仓库）。★★ **我方规格已落**：`spec/chain.json`（`routes.emergency.nodes[4]` **删描述性 `actor`「按档位审批人」**〔惰性根因〕＋ 落 `tier_expand{kind=approval_chain, tier_source=emergency_max, exclude_roles=[supervisor,ops_supervisor]}` ＋ `routes.emergency.rules`〔`tier_formula`/`tier_formula_label`/`tier_formula_reason`/`dedupe_note`〕＋ ★★ **`conventions` 新增 `tier_source` 取值约定**〔把散落在实现注释里的 `amount_cents`/`r15_max` 与新增的 `emergency_max` **收成一份受控约定** —— 此前**无约定**，与 `checks_when` 当年同病〕）；`spec/RESOLUTIONS.md` 新增 **`R-31`**（V1.5→V1.6，裁定 30→**31**）；`spec/params.json` 记 `reconfirmed_2026_10_04`。★★ **关键设计（本批最值钱的一条）**：`emergency_max` **两值必须齐全、缺任一 ⇒ 可见失败** —— ★ 与 `N-044` 的 `r15_max`「单边可得取单边」**刻意不同**：label 是「**不得降档**」（**单调上界**），单边取值**可能低于应属档位**（紧急买 3,000 元、但属 5 万元 PR ⇒ 应采三档，取单边只得采二档 ＝ **静默降档**）。★ **附带裁定（次级、待用户复核）**：`exclude_roles=[supervisor, ops_supervisor]`（沿用户去重口径：`seq2` 紧急认定 / `seq6` 核销闭合已各有一个签字点 ⇒ 采一档展开为**空**、采二/三档只补 `project_general_manager`）。★ **未做（明确划界）**：**未**改 `S19`（须待实现落地、**同批**升级才不会当场红）、**未**做 `emergency` 的 `ResolveRoute`/`doc_chains` 接线（属 `A8`）。★ 台账：`COLLAB.md`（`§4 N-045` 追加口径与规格段 ＋ `§8` 新增 2026-10-04 批复块 ＋ `§1` 五处 ＋ 附录 C）· `REMAINING.md`（本节 · `§1 B8` · `§2 A10` · `§3.2 C7/C9` · `§5 批 9`）· 新任务包 `MIMO-NEXT-BATCH-8.md` |
| 2026-10-04 08:52 | ★★★ **批 7 的 `B4` 段闭环（我方先行，未派工）＋ 新开 `N-047`** —— ★ **先核实「是否已交付」**：`find . -iname "*acceptance*"`（排除 `node_modules`/`.git`）**零命中** ⇒ 确认 `N-017` 承诺的 `acceptance.csv` **从未交付**。★★ **交付 `spec/acceptance.csv` V1.0**：`spec/forms/*.json#checks` 的**全量 97 条**判据逐条给出**可判定表达式**与**承载者**（11 列）；★ **只引用 `(doc_type, check_id)`、不复制 `assert` 原文**（防第二份真相）。★ 落表统计：`when_kind`＝`submit` 73 / `lifecycle:*` 15 / `approval` 4 / `其他` **4** / `schema` 1；`carrier_kind`＝`code` 79 / `manual` 6 / `pending_implementation` **5** / `structural` 4 / `pending_wiring` **3**；`severity`＝`hard` 69 / `soft` 5 / **`(未声明)` 23**；`machinable`＝`yes` 91 / **`no` 6**。★★★ **落表实测出 5 组问题 ⇒ 新开 `N-047`**：① ★★ **23 条判据缺 `severity`（`BA` 8 / `PR` 6 / `SA` 9）⇒ 落在 `S15` 与 `evaluateHardChecks` 的缝里**（提交引擎 `continue` 跳过 ＋ `S15` 只管 `severity=hard` ⇒ **既不执行、也不报错**）；② **5 条无任何承载**；③ **3 条时点未接线**（回交凭据 / 结算补录）；④ **6 条 `when` 不合 `checks_when` 约定**；⑤ **2 处判据无字段可承载 ＋ 1 处条件必填被静默跳过**（`SA#invoice_info.required_conditional` 不含 `==` ⇒ `evalSimpleEqual` 不可解析 ⇒ `validateSubmitForm` **跳过不阻断** —— ★ **既没提示、也没拦**）。★★ **判据可判定性的受控词由 `N-017` 原拟 5 项扩为 10 项**（原 5 项塞不下占比最高的「数值比较」与「跨字段引用比较」）。★★ **顺手修 `spec/README.md` 两处自身陈旧**：`checks.json` 版本（`V1.6/9/18` → **`V1.11/10/23`**）＋ §4 索引**漏列 `S14`/`S15`/`S16`/`S18`/`S19`**、未写 `S17` 已撤销 ⇒ 已在 **V1.3** 中补齐（★ 该 README 自己警告过「索引漂移」）。★ **门禁**：必绿 **8/8 ＋ 会报零命中**（★ 新增 CSV 对门禁**透明**是**事先核对出来的**：`specload.Load` 的 `WalkDir` 只收 `*.json`、`S1` 只 glob `spec/**/*.json`）⇒ 判据 / Go 包 / 净检出**零波动**。★ **本轮明确不做**：不改判据状态、不改 `forms`（改 `severity` / 补字段 / 补求值器属**同批变更**）⇒ **登记不抢跑**。★ 台账：`COLLAB.md`（新 `N-047` ＋ `§1` 七处 ＋ 附录 C）· `REMAINING.md`（`§1 B4` · `§2 A6`（核实 `N-035` 已 `AGREED`、无需动作）· `§5 批 7` · 本节）· `spec/README.md` V1.3 |
| 2026-10-04 08:36 | ★★★ **批 10 闭环（`A9` / `N-046`）：看板 16 补「采购变更异常」指标（spec 与实现同批）⇒ `N-046` 结案（`AGREED`）** —— ★ **我方**：`spec/dashboard.json` 看板 16 新增**第 12 个指标 `change_anomaly_listed`**（`render=alert`；`formula`＝`L09.exception_type = 采购变更` ∧ `是否进入异常清单 = true` 的单数；`fields` 引 `L09.例外类型` / `L09.是否进入异常清单`）＋ 同步「11→12 个指标」「7→6 个台账」；交付 [`MIMO-NEXT-BATCH-7.md`](./MIMO-NEXT-BATCH-7.md)。★ **mimo**：`e05420d`（`countChangeAnomalyListed` 读 `Row.ArchiveExt`、`countExtRegistered(r09,"is_anomaly_listed")` **列级守卫**；测试硬编码 `11→12` ＋ 新增 handler 级 `TestDashboardChangeAnomalyListed`）。★★ **我方独立验收（不采信自报）**：逐行读实现 ＋ **两次单点变异**（守卫源 → `len(r09)` ⇒ `TestDashboardGuardPerIndicator` 精确转红、其余保持绿；去掉「且」⇒ `TestDashboardChangeAnomalyListed` 精确转红、守卫用例保持绿 ⇒ **隔离均成立**）＋ `cp` 还原 ＋ `sha256sum -c` ＋ 门禁 **8/8**（收口后 / 还原后各一次）。★★★ **本轮最重要的一条新教训**：`drive_mimo.sh` 判据③＝`check_all` 必绿，**而「净检出可构建」测的是 HEAD** ⇒ ★ **凡「spec 与实现须同批」的批次，我方 spec 改动必须先入库**，否则判据③**结构性不可满足**（本轮实测：驱动在 mimo 提交后空烧尝试次数 —— 已及时停手，由我方提交 `662ef25` 收口后复绿）。★ mimo **自己诊断出该根因、如实登记、且拒绝代提我方文件**（守纪律）。★★ **顺手清一笔规格债（实测发现）**：`spec/dashboard.json` 的 `connected_requires` 16③ 与 `known_gaps` 第 6 条原称「**现聚合路径仍用旧 key** ⇒ 不得翻转」—— ★ **实测证伪**（9 个旧 key 在非测试代码零命中；`TestDashboardAlertKeysMatchSpec` ⊆＋等量 ＋ `TestDashboardAlertKeysRejectOldName` 9 个旧名逐个必红，双向机检在位）⇒ ★ 该债**已于 2026-10-01 `BATCH-3 T1` 还清、只是规格文本没跟上**（同「实现了没人回填」族）⇒ 改为「✅ 已解决」并**保留历史对照**。★ **遗留（非阻塞）**：实现/测试注释「12 个指标分布在 **7** 个台账上」应为 **6**（请 mimo 下轮订正）。★ 台账：`COLLAB.md`（`N-046` 验收块 ＋ 状态 `AGREED` · `§1` 六处 · 附录 C）· `REMAINING.md`（本节 · `§2 A9` · `§5 批 10`）· `spec/forms/PC.json`（`known_gaps` 末条状态 → 已落地） |
| 2026-10-04 04:57 | ★★★ **批 5 闭环（`B3` → `A7`，**全在我方，未派工**）；`N-038` 结案；新开 `N-046`/`A9`/批 10** —— ★★ **`B3` 落点定稿＝表列**：`spec/ledger-mapping.json` **V1.1** 给 `L09` 新增**可写列 `anomaly_note`**（「异常变更专项说明」，`writer: 综合运营主管`、`when: 月度报送时`）＋ **补登记 `is_anomaly_listed`**（落账自检 6 列之一却从未登记）；字段定义登记进 `docs/reference/config-mapping.sample.json`；判据 `PC#anomaly_monthly_report` ⇒ **`pending_implementation` → `manual`** ＋ 落点/裁定/拆分写进 `carried_by`/`verify_note`。★ **选「表列」而非「看板指标」的三条理由**（①判据诉求是**文本**说明、指标承不了；②有 `is_emergency_closed` **现成同范式**；③**零新增单据类型**）。★★ **两次独立探针把「落点不是空话」验掉**：① `writable: true` 不登记字段定义 ⇒ `TestUnregisteredWritableLedgerFields` **红**，补登记 ⇒ **复绿 8/8**；② 单独给看板 16 加指标 ⇒ `TestDashboardAlertKeysMatchSpec` **精确转红**『输出 11 ≠ spec 12』⇒ ★ **据此把 `N-046` 定性为「须与实现同批」**。★★ **顺手清掉一笔规格债**：**8 条 `pending_implementation` → `code`**（`spec/forms/PC.json` V1.1 七条 ＋ `spec/forms/PR.json` V1.1 一条）—— ★ 取证＝**逐条读实现 ＋ 跑 `TestInjectPCSSSystemFields`**（逐字段断言全 PASS），**非按名 grep**。★★★ **本轮教训：「写了没人读」的反面＝「实现了没人回填」** —— `special_explanation_required` 规格里一直写「**无生产点** ＋ 阈值待集团确认」，而 rule **早已写明**、实现随 `N-039`（`c1eb311`）**已落且过测** ⇒ **`carried_by_kind` 必须与实现验收同批回填**。★ 门禁 **必绿 8/8 ＋ 会报零命中**（判据 / Go 包 / 净检出**零波动**）。★ 台账：`COLLAB.md`（`§1` 六处 ＋ `N-038` 结案说明 ＋ 新 `N-046` ＋ 附录 C）· `REMAINING.md`（`§1 B3` · `§2 A7 改判/A9` · `§5 批 5✅/批 10` · 本节） |
| 2026-10-04 02:31 | ★★ **`N-044` 规格批（`B7`）我方先行部分完成** —— ★ 用户 2026-10-04 口径「**流程上有重复的签批人，都是一次签批呀**」⇒ 采纳**去重**案。★ `spec/chain.json` **只新增键**：`thresholds.purchase.bands[*].approval_chain`（`tier1=[ops_supervisor]` · `tier2/3=[supervisor,project_general_manager]`；**只列审批人**、**已按角色去重**）＋ `sole_source.nodes[2]`/`change.nodes[2]` 的 `tier_expand`（`tier_source` ＝ `amount_cents`/`r15_max`；SS `exclude_roles=[project_general_manager]`）⇒ ★ **门禁 8/8 全绿 ＋ 会报零命中**（判据 / Go 包 / 净检出**零波动**）。★ 旧键（复合 `actor` ＋ 伪 `ref`）**刻意保留** —— 删它们须与「禁惰性必需节点」判据（拟 `S19`）**同批**。★ 交付 **`MIMO-NEXT-BATCH-6.md`**（批 8）⇒ ★ **批 8 提前于批 5 派工**（已证实的实现缺陷优先）。★ 另发现 `SS.tier_chain_record`/`PC.tier_approval_record` 的**规格内部矛盾**（`SS.json#_note` 与 `PC.json#filled_at_note` 三处不自洽）⇒ **本批不派、我方另行定稿**。 |
| 2026-10-04 03:58 | ★★★ **批 8（`N-044`）验收通过 ⇒ 结案（`AGREED`）＋ 我方收尾；★★ 收尾期新发现 ⇒ 新开 `N-045`** —— mimo `db18374`（`expandTierApproval` 按 `bands[*].approval_chain` 逐角色展开 · `SourceNodeID=<节点id>_<role>` · `amount_cents`/`r15_max` 双源 ＋ 单边 fallback、全缺**可见失败** · handler 侧 inject **前移**至 `chain.Compute` 之前）；我方**三次独立复跑门禁全 8/8** ＋ **三处单点变异 A/B/C 精确转红、隔离性成立** ＋ `sha256sum -c` 还原（★ **未用 `git checkout --`**）。★ **我方收尾**：新增 **`S19`**（`checks.json` V1.11：`routes.*.nodes[*].actor` 禁复合串；★ **红→绿同批实证** —— 加判据 ⇒ **Python 报 4 处 ＋ Go `go test` 同时转红**（零引擎改动、两侧自动一致），删 5 键 ⇒ **复绿 8/8**）＋ 删 **5 个失效键**（`sole_source.tier_chain` 复合 `actor` ＋ 伪 `ref` · `change.tier_approval` 复合 `actor` · `purchase_tier2/3.contract_two_level` 复合 `actor`）。★★ **新开 `N-045`**：`emergency.backfill_approval`（`required: true`、`actor="按档位审批人"`）＝**第三处惰性必需节点**；★ `emergency` **未被 `doc_chains` 引用** ⇒ 当前不可达；★ 卡「不得降档」档位源口径 ⇒ 新增 **`B8` / `C9` / 批 9**。 |
| 2026-10-04 02:08 | ★★ **批 4（`A4` · `N-043`）验收通过 ⇒ 结案（`AGREED`）**（mimo `655fdf1`；独立复跑门禁 ×3 全 8/8 ＋ 四轮单点变异 ＋ `sha256 -c` 还原）。★★ **验收期的证伪对照挖出新发现**：变异「给 `PC×tier_approval` 加必填」**不转红** ⇒ 实测反证该节点**根本不生成审批任务** ⇒ 新开 **`N-044`**（惰性必需节点）⇒ 新增 §1 的 **`B7`** 与 §5 的 **批 8**（★ 其中一个口径**需用户确认**：SS 档位链 PGM 与 `pgm_final` 是否重复签批 ⇒ **已上报，未得口径前不派工**）。 |
| 2026-10-03 23:10 | 首版：B/A/C/D 四档 ＋ 批 1–7 执行顺序 |
