# 16 · 回调链路修复技术方案（三方快捷审批回调端到端闭合）

| 项 | 内容 |
|---|---|
| 文档编号 | `16`（★ **未用派单所给 `15`**：`docs/15-Code-Collision-Register.md` 已占用该号 —— 该文档本身就是「同号不同物」登记总表，再造一个 `15-…` 恰好制造它要登记的那类缺陷；依据＝关键定案 **#48**「新增文档必须先定编号」＋ **#56**「编号是定位符，重号＝定位失效」） |
| 版本 | V1.1（2026-09-27，架构师 Bob 出稿；★ **V1.1 由产品经理补「落地状态」回填**） |
| 状态 | ★ **首稿为「仅设计」；现 A–F 与批次均已实现（见 §2 / §3 的「落地状态」）**；★ **本文仍不改任何代码、不 commit**。本批仅**回填事实**（提交号），**不新增设计** |
| 落地批次（4 批，提交号） | 第 1 批＝**C + D**（`5fe1671`）· 第 2 批＝**A + B + E + `0013`**（`d94580f`）· 第 3 批＝**F + 通知 Sender**（`36df709`）· 第 4 批＝**「新待办产生」通知接线**（`c6e26d7`，现行 HEAD） |
| 仍待联调实测（**不得写成已验**） | `V-1`（`action_context` 原样回传）· `V-3`（`contact/v3` 按 `user_id` 查 `open_id` 端点 / scope）· `V-4`（`approval_code` 双 code 池归属）；★ **`V-2` 已实测定稿（2026-09-28）**：`message/update` 请求体＝`{"message_id","status"}`（见 §7 V-2 行）；★ 落点＝`docs/09` §2（阶段一） |
| 输入 | `docs/05-API.md` §3.14（含「实测缺口清单 6 条」）· `docs/04a-Architecture-Increment-V2.md` §3/§4/§17 · `docs/reference/README.md` 实测台账（2026-09-27）· 实现：`internal/httpapi/handlers_approval.go` · `internal/flow/callback.go` · `internal/platform/feishu/push.go` · `internal/platform/feishu/external.go` · `internal/approval/defregistry.go` · `cmd/jxapproval/bootstrap.go` · `migrations/0007_approval_core.sql` |
| 读者 | 工程师（按 §3 修复）、PM（按 §4 同步文档）、QA（按 §6 测试） |

---

## 0. 结论先行（TL;DR）

1. **回调全链路当前一条都走不通**，且有三层独立断点：① 我方解析的字段名（`biz_no`/`open_id`/`instance_code`）与飞书官方报文（`approval_code`/`user_id`/`instance_id`）**不匹配**（实测铁证：官方格式打我方 ⇒ `回调缺少 biz_no/task_id`）；② `biz_no` 的回传载体 `task_list[].action_context` **从未被设置**（当前推的是纯 `task_id` 字符串）；③ 本地 `t_approval_def` / `t_instance` **0 行**（定义装载 P0-2 未实现，`defregistry.Register/Sync` 零生产调用者）。**三层须按 ③ → ②+① 顺序同批修复**。
2. 修复不引入新框架，全部为**既有文件内的定向改造**（`extCallbackBody` 字段校准、`ExternalTask` 加 `action_context`、`defregistry` 接通、回调 body 留痕、`message/update` 失败反馈），数据库仅 **1 个新增迁移 `0013`**（`t_flow_op_log` 加 `message_id`、`t_approval_def` 加 `feishu_code`，均可空、无回填）。
3. **ID 域结论（已查证）**：本系统全库统存 **`open_id`**（`t_user_role.open_id` 注释明示「飞书 open_id」；`t_flow_task.assignee_open_id`；会话 `access.Session.OpenID`；免登换回的是 open_id）。回调官方发的是 **`user_id`（租户内域）**，二者**不同域、不可互比** ⇒ 必须转换：按 `user_id` 调飞书通讯录接口换 `open_id` 后再进鉴权，**绝不能拿 `user_id` 直接与 `assignee_open_id` 比较**。
4. **一个派单之外的重大风险（建议纳入联调第一轮）**：飞书 `approval_code` 存在**「自定义 code / 真实 code」双池**（`reference/README.md` 实测条目：写用自定义、读/推实例必须用真实 code，且**读回字段 `approval_code` 返回的是自定义 code**）。`defregistry.Register` 现把平台回填的 code 直接落库、`Pusher.Push` 又用它推实例 ⇒ **存在推实例 `1390002` 的静默失败面**；且回调报文里的 `approval_code` 属哪个池**未经实测**（标注：待验证）。
5. 平台行为凡本文引用均标来源（官方文档页名 或 `docs/reference/README.md` 实测条目）；**未经实测的平台行为一律标「待验证」**，不以文档表述充当结论。

---

## 1. 现状与缺口（逐条，附实测证据）

> 端到端目标链路：审批人在飞书点「同意」→ 飞书回调我方 `POST /approval/external/callback` → 我方正确处理 → 状态机推进 → 重推飞书 `external_instances` → 飞书卡片/待办更新。

| # | 缺口 | 现状（代码/数据证据） | 实测证据（来源） | 等级 |
|---|---|---|---|---|
| G-1 | ★★★ **回调字段名与官方报文不匹配** | `handlers_approval.go` 的 `extCallbackBody` 解析 `biz_no` / `open_id` / `instance_code` / `action_name` / `operator.open_id`；代码自注「字段名待 Q17 实采校准」 | 官方报文（《三方快捷审批回调》，2025-04-21；`reference/README.md` ★★★ 条目）＝ `action_type`(必)/`user_id`(必)/`approval_code`(必)/`token`(必)/`action_context`/`instance_id`/`task_id`/`message_id`(卡片操作必填)/`id`/`reason`/`attachments`/`encrypt`，**不发顶层 `biz_no`**。实测：官方格式打我方 ⇒ `回调缺少 biz_no/task_id`；自造格式（含顶层 `biz_no`）⇒ 才走到 `无对应实例` | P0 |
| G-2 | ★★★ **`biz_no` 回传链路未闭合** | `push.go` 的 `ExternalTask` 结构体**无 `action_context` 字段**，`BuildSnapshot` 未设置；推给飞书的 `task_list[]` 每项只有 `task_id`/`node_id`/`node_name`/`assignee_open_id`/`status` | 官方对 `action_context` 的定义＝「操作上下文，用于传递该任务的上下文数据」（`reference/README.md` ★ 条目引用；★ **「飞书原样回传」属官方文档表述、尚未实测** ⇒ 见 §7 待验证 V-1）；回调侧 `handleExternalApprovalCallback` 已有「`action_context` 为 JSON 字符串时并入定位字段」的兼容逻辑，但因推侧从未写入而**恒走不进去** | P0 |
| G-3 | ★★★ **定义未装载（P0-2）** | `defregistry.go` 的 `Register`/`Sync` **无生产调用者**（`bootstrap.go` 仅 `approval.NewRegistry(...)` 构造，注释自述「管理端点另行排期」）；11 类定义清单（`config-mapping.sample.json` 的 `approval_code` 节，`REPLACE_ME_` 占位）未导入 | 服务器 `192.168.10.50` 实测：`~/services/jxapproval/data/jxapproval.db` 的 `t_approval_def` = **0 行**、`t_instance` = **0 行**（`reference/README.md` ★★ 实测条目）⇒ 回调即使过了字段关，也会在 `verifyCallbackToken`（`flow/callback.go`）查实例/定义时被拒 | P0 |
| G-4 | ★★ **`user_id` 与 `open_id` 域不匹配** | `extCallbackBody` 读 `open_id`；`flow.CallbackRequest.OperatorOpenID` 直接与 `task.AssigneeOpenID` 比较 | 官方字段＝**`user_id`（操作人的 user_id，租户内域）**（同 G-1 来源）；我方全库统存 `open_id`（`migrations/0001_init.sql:152` 注释「飞书 open_id」；`t_flow_task.assignee_open_id`；`access.Session.OpenID`）⇒ **不做域转换则 `admitCallback` 的 `operator==assignee` 恒不成立 → 403** | P0 |
| G-5 | ★★ **推「待办」≠「发通知」，通知零实现** | 推 `external_instances` 只进审批中心待办（`reference/README.md` ★★ 实测条目）；通知须另调 `POST /open-apis/approval/v1/message/send`（`template_id=1008`「收到审批待办」，实测已成功发出并返回 `data.message_id`） | 代码侧：`flow.notify.go` 的 `Sender` 端口在装配时传 **nil**（`bootstrap.go`：`flow.NewNotifier(db, nil, logger)`）→ 只落 `EXPECTED`、从不发送 ⇒ 审批人在飞书**完全看不到提醒**（「本次已踩中」） | P0 |
| G-6 | ★ **`message_id` 未解析、失败无用户反馈** | `extCallbackBody` 无 `message_id` 字段；`t_flow_op_log` 无该列 | 官方：卡片操作时 `message_id` 必填；**处理失败 ⇒ 卡片退化为「只显示查看详情」**，仍失败 ⇒ 需我方调【更新审批 Bot 消息】接口更新卡片状态（`reference/README.md` ★★ 实测条目引用官方）；★ 接口名已核＝`POST /open-apis/approval/v1/message/update`（官方 API 清单页《审批任务 · 审批 Bot 消息》；**请求体字段待验证** ⇒ §7 V-2） | P1 |
| G-7 | ★ **回调报文不留痕** | `router.go` 的 `logMiddleware` 只记 `method/path/status/duration_ms/trace_id`，**无 body** | 无法回放排障：本次实测排障只能靠外部重放报文还原（校验顺序「报文完整性 → 实例存在性 → token」即靠外部实测得出） | P1 |
| G-8 | ★★ **`approval_code` 双 code 池风险（派单外新增）** | `defregistry.Register` 落库 `code := res.ApprovalCode`（平台 POST 响应回填值）；`Pusher.Push` 用 `inst.ApprovalCode` 推实例；`verifyCallbackToken` 用 `inst.ApprovalCode` 查定义 | `reference/README.md` ★★ 实测：**写（POST）用自定义 code 匹配；读（GET）与推实例（`external_instances`）必须用真实 code；读回字段 `approval_code` 返回的却是自定义 code**。⇒ 若落库/推送用的是自定义 code，推实例将 `1390002`；且 POST 响应回填值属哪个池、回调报文里 `approval_code` 属哪个池均**未实测** | P0（联调必测） |

> ★ 校验顺序现状与实测一致：`HandleCallback`（`flow/callback.go`）顺序＝字段完整性（缺 `biz_no`/`task_id`/operator 即拒）→ `verifyCallbackToken`（**先查实例存在 → 再查定义 → 再比 token**）→ `admitCallback` 准入 → `recordCallback` 落盘。实测口径「报文完整性 → 实例存在性 → token」与之吻合，**顺序无需改动**（`reference/README.md` ★ 实测条目）。

---

## 2. 修复方案（按 A–F，逐项给改动文件与符号名）

> ★★ **落地状态（V1.1 回填，事实＝已落地代码）**：**A / B / C / D / E / F 全部已实现**（批次与提交号见各小节「落地状态」行与 §3）。★ 本节其余内容为**首稿设计**（保留作留痕）；**下标「落地状态」行为本批新增**。

### A. 回调报文字段校准（对应 G-1 / G-4）

> ★ **落地状态**：✅ **已实现**（第 2 批 **`d94580f`**）—— `extCallbackBody` 按官方 12 字段重写、旧字段降为**兼容读**（窗口＝一个发布版本）；`flow.CallbackRequest` 新增 `ApprovalCode` / `MessageID` / `OperatorUserID`；`verifyCallbackToken` 增 `approval_code` **双池宽松档**（命中才放行、不命中仅告警）。★ `A-3` 转换落点＝`internal/platform/feishu/contact.go` 的 `GetOpenIDByUserID`（★★ 红线：**绝不把 `user_id` 塞进 `OperatorOpenID`**）。★ **`V-3`（端点 / scope）仍待联调实测**。

**A-1 · `extCallbackBody` 重写为官方字段**

| 项 | 内容 |
|---|---|
| 文件 | `internal/httpapi/handlers_approval.go` |
| 符号 | `type extCallbackBody struct`（就地改字段；建议同步把注释里「字段名待 Q17 实采校准」改为「已按《三方快捷审批回调》2026-09-27 实测校准」） |
| 新字段 | `action_type`(必) · `user_id`(必) · `approval_code`(必) · `token`(必) · `action_context` · `instance_id` · `task_id` · `message_id` · `id` · `reason` · `attachments` · `encrypt`（官方字段清单来源＝《三方快捷审批回调》，`reference/README.md` ★★★ 条目） |
| 删除 | 顶层 `biz_no` / `instance_code` / `open_id` / `action_name` / `operator.open_id` 作为**主读**字段（兼容读法见 A-2） |

**A-2 · 双读兼容（兼容窗口内的过渡读法）**

| 规则 | 内容 |
|---|---|
| `action_type` | 主读；兼容期可读旧 `action_name`（取 `firstNonEmptyStr`，现有惯用法） |
| 操作人 | **`user_id` 优先**；无 `user_id` 时读 `open_id`（旧自造报文兼容）——**但二者不同域，读法差异见 A-3** |
| `biz_no` | 主读＝`action_context` 内 JSON 的 `biz_no`；兜底＝从 `instance_id` 反解（我方推的 `instance_id`＝`{app_id}:{biz_no}`，`flow/service.go` 的 `instanceCode()`；剥掉 `app_id:` 前缀即得 `biz_no`）。**不再读顶层 `biz_no`**（官方不发；兼容期保留作为最后兜底并打 warn 日志） |
| `task_id` | 顶层 `task_id`（官方：列表操作必填）；兼容期可读 `action_context` 内 JSON 的 `task_id` |
| 兼容窗口 | 建议保留**一个发布版本**；在代码注释与 §4 影响清单写明废弃时点（本仓库红线：不静默、写明边界） |

**A-3 · `user_id` ↔ `open_id`：统存口径与转换策略（已查证）**

| 项 | 结论 |
|---|---|
| 我方库现存的是哪种 | **全库统存 `open_id`**。证据：`migrations/0001_init.sql` `t_user_role.open_id`（注释「飞书 open_id」）、`migrations/0007_approval_core.sql` `t_flow_task.assignee_open_id` / `t_flow_op_log.actor_open_id`、`internal/access/session.go` `Session.OpenID`、免登链路（`access/auth.go` `Exchange` 换回 `feishu.FeishuIdentity{OpenID}`）。`t_permission_rule` **按 `role` 配置、不存任何用户 ID**（0001:163-175），不涉及此问题 |
| 官方回调发哪种 | **`user_id`（租户内域）**（《三方快捷审批回调》字段表） |
| 统存口径 | **继续统存 `open_id`，不改库内 ID 域**（改域＝全系统级重写，违反定案 #47「就地改造」） |
| 转换策略 | 回调拿到 `user_id` → 调飞书通讯录「获取单个用户信息」（`GET /open-apis/contact/v3/users/{user_id}?user_id_type=user_id`，响应含 `open_id`）换出 `open_id` 后再进 `admitCallback`。★ 落点：`internal/platform/feishu/` 新增方法（建议 `contact.go`：`func (c *HTTPClient) GetOpenIDByUserID(ctx, userID string) (string, error)`）；接口路径与 `user_id_type` 参数**按官方《通讯录》文档写、联调实测校准**（`reference/README.md` 已实测 `contact/v3` 可用、并探明所需 scope 候选：`contact:contact.base:readonly` 等 5 个任一；**该查询端点本身待验证** ⇒ §7 V-3） |
| 缓存 | 进程内 map + TTL（如 10 分钟）即可（回调频率低）；`0012_org_directory`（通讯录镜像，`docs/08` 设计、**迁移文件尚未落地**，`migrations/` 现止于 0011）落地后可改走镜像，本期**不依赖** |
| 转换失败 | **可见拒绝（40000）＋告警日志**，绝不静默放行（空 operator 会被 `HandleCallback` 入口拒，行为一致）；绝不把 `user_id` 直接塞给 `OperatorOpenID` 参与比较（恒不命中 ⇒ 假 403） |
| 兼容旧 `open_id` 读法 | 旧自造报文若带 `open_id`，直接作为 operator 的 open_id（同域，无需转换）；**判据＝优先级 `user_id` →（转换）→ `open_id`（免转换）** |

**A-4 · `flow.CallbackRequest` 扩展**

| 项 | 内容 |
|---|---|
| 文件 | `internal/flow/callback.go` |
| 符号 | `type CallbackRequest struct`（**新增字段，不改既有字段语义**）：`ApprovalCode string`（报文 `approval_code`，用于与实例定义一致性校验）、`MessageID string`（透传给落盘与失败反馈）、`OperatorUserID string`（转换前原值，排障留痕） |
| 校验扩展 | `HandleCallback` / `verifyCallbackToken` 增加：报文 `approval_code` 与 `inst.ApprovalCode`（以及 §2-C 的 `feishu_code`，若落地）**双池任一命中即可**（双 code 池归属未实测前不做强校验，只做「命中才放行、不命中告警但不拒」的宽松档；待 §7 V-4 实测后升格为强校验——**先宽后严，避免用未实测假设做硬拦截**） |
| `message_id` 用途 | ① 落盘（`recordCallback` 写入 `t_flow_op_log.message_id`，0013 新列）；② 供 F 的卡片更新调用 |

### B. `action_context` 承载 `biz_no`（对应 G-2）

> ★ **落地状态**：✅ **已实现**（第 2 批 **`d94580f`**，与 A **同批**）—— `push.go` 的 `ExternalTask` 新增 `action_context`，`BuildSnapshot` 为每个 `RELEASED` task 写 `{"biz_no":…,"task_id":…}`；解侧 `biz_no` **三级读法**（`action_context` → `instance_id` 反解 → 顶层兜底+warn）。★ **`V-1`（飞书是否原样回传 `action_context`）仍待联调实测** —— B 方案的唯一前提。

| 项 | 内容 |
|---|---|
| 推侧文件 | `internal/platform/feishu/push.go` |
| 推侧符号 | `type ExternalTask struct` **新增** `ActionContext string \`json:"action_context,omitempty"\``；`BuildSnapshot()` 为每个 `RELEASED` task 写入压缩 JSON 字符串：`{"biz_no":"PR-2609-0001","task_id":"t-..."}`（★ `biz_no`/`task_id` 键名与解侧约定**必须同批**；`UpsertExternalInstance` 的 body 已整体透传 `snap.TaskList`，无需另改序列化） |
| 解侧文件 | `internal/httpapi/handlers_approval.go` |
| 解侧符号 | `handleExternalApprovalCallback` 内的 `action_context` 并入逻辑（现有骨架保留，改字段映射为 A-1/A-2 新口径） |
| ★ 同批约束 | 推侧（写入 `action_context`）与解侧（解出 `biz_no`）**必须同一提交**——否则链路断裂且两侧各自"看起来正常"（定案 #53：跨包特性按特性切提交）；`snapshotHash` 含 task 序列化 ⇒ 格式变化会自然触发 `update_time` 递增重推，无残留 |
| 兼容旧数据 | ① `action_context` 非 `{` 开头（旧纯 `task_id` 字符串）→ 忽略，走 `instance_id` 反解兜底（A-2）；② 已在飞书侧的旧待办：下一次任意状态变更触发重推（`flowPushSubscriber` → `Pusher.Push`，`update_time` 严格递增）即带新格式；无需 migration |
| ★ 前提声明 | 「飞书原样回传 `action_context`」为官方文档表述（`reference/README.md` ★ 条目），**尚未实测** ⇒ 本方案全部依赖该行为，联调第一轮必须首验（§7 V-1；§6 测试要点含实测命令） |

### C. 定义装载源（对应 G-3 / G-8）

> ★ **落地状态**：✅ **已实现**（第 1 批 **`5fe1671`**）—— 管理端点 **`POST /api/admin/approval/defs/sync`**（仅系统管理员；读 `t_config_mapping(map_kind='approval_code')` → `ApprovalDefs.Sync`；响应计数 `synced`/`created`/`updated`/`skipped`/`failed` ＋ `items`；错误码 **400**＝清单空或含占位符、**503**＝`JX_ACTION_CALLBACK_TOKEN` 未配）＋ **启动自检**（`t_approval_def` 为空 ⇒ warn ＋ `/healthz` 状态位 `approval_defs`，★ **只进 `/healthz` 快照、不进 `Ready()`**）＋ 补齐配置键 `JX_CALLBACK_DOMAIN` / `JX_ACTION_CALLBACK_TOKEN`。★ **`G-8` 双 code 池**：`0013` 加 `feishu_code`；`Registry.Register` 主键仍为我方自定义 code、响应回填值落 `feishu_code`；`Pusher.Push` **优先 `feishu_code`、空则回退**。★ **`V-4`（双池归属）仍待联调实测** —— 实测前先**双写、不猜**。

| 项 | 内容 |
|---|---|
| 现状 | `defregistry.go` 的 `Register`/`Sync` 无生产调用者（`bootstrap.go:168` 仅构造）；未装载时**静默无数据**——提交侧 `flow.Submit` 有 S7 校验（`ErrDefinitionMissing` → 40901），但**回调侧 / 启动侧无任何显式提示** |
| 11 类清单来源 | `t_config_mapping`（`map_kind='approval_code'`；写入通道＝配置映射导入，清单正本＝`docs/reference/config-mapping.sample.json` 的 `approval_code` 节，11 类 BA/PR/SA/RFQ/BJ/SS/CT/PC/GR/QC/SUB）。★ 该清单当前是 `REPLACE_ME_` 占位，**导入前必须完成占位符替换**（配置映射导入层已有占位符硬拦，定案 #21） |
| 接通方式（两通道） | ① **管理端点**（主通道）：`internal/httpapi/router.go` admin 组新增 `POST /api/admin/approval/defs/sync` → handler（`handlers_approval.go` 新增 `handleAdminApprovalDefsSync`，复用 `d.requireSysAdmin`）→ 组装 `[]approval.DefInput`（从 `t_config_mapping` 读 code/doc_type，名称/分组/回调 URL/token 由配置项给出）→ `d.ApprovalDefs.Sync(...)`（`defregistry.go` 现成，幂等：approval_code 命中即更新）；② **启动自检**（不自动建，避免启动期网络依赖）：`bootstrap.go` 启动序列加一条——`t_approval_def` 行数 ＝ 0 ⇒ `logger.Warn("★ 启动自检：t_approval_def 为空（三方定义未装载，回调/提交将 40901）")`，并接入 `health` 包状态位 |
| `approval_code ↔ action_callback_token` 映射存哪 | `t_approval_def.callback_token`（`migrations/0007_approval_core.sql:81`，**列已存在、无需迁移**）；`verifyCallbackToken` 已按 `inst.ApprovalCode → GetApprovalDef → CallbackToken` 比对（常数时间比较）。token 值来源＝配置键（`docs/14` runbook 已列 `JX_ACTION_CALLBACK_TOKEN`），注册定义时随 `DefInput.CallbackToken` 下发飞书（`external.action_callback_token`），本地落同值——**一处配置、两侧一致** |
| ★★ 双 code 池处置（G-8） | 0013 迁移给 `t_approval_def` 加 `feishu_code TEXT`（**真实 code** 列）：`Registry.Register` 落库时把平台返回的 code 归位——`POST external_approvals` 响应回填值属哪个池**待验证**（§7 V-4），验证前先**双写**（`approval_code`＝我方自定义 code 作 PK 不变，`feishu_code`＝响应回填值），推实例侧 `Pusher.Push` 的 `snap.ApprovalCode` 改为**优先 `feishu_code`、空则回退 `approval_code`**；回调校验侧按 §2-A-4 宽松档。★ 若实测证明 POST 响应回填即真实 code，则现状 `Register` 行为碰巧正确，仍建议落双列消歧（`reference/README.md` 已证 GET 读回的 `approval_code`＝自定义 code，字段名同指两物） |
| 未装载时的行为（改后） | 回调：`ErrDefinitionMissing` → **409**（`callbackErrorStatus` 已枚举映射，实现 `56d6138`）；提交：S7 → 40901；启动：warn 日志 + health 状态位；管理页：`GET /api/approval/defs` 可见清单（已实现）。**不再有"静默无数据"** |

### D. 实例/任务落库与重推语义（对应 G-3 的实例半边）

> ★ **落地状态**：✅ **已实现**（第 1 批 **`5fe1671`** 的落库纪律 ＋ 后续各批的实例/任务写入者）—— 「本地先行、推送在后」纪律成立；`BuildSnapshot` 守卫（`PENDING` 但 `RELEASED` 任务数为 0 ⇒ warn）已落。★ 手工 curl 推实例的警示见 `docs/14`。

| 项 | 内容 |
|---|---|
| 现状澄清 | 正规链路**本地先行、推送在后**：`flow.Submit`（`internal/flow/service.go:169` 起）同事务写 `t_instance` + `t_flow_task` + 状态史 + op_log，事务提交后经 `flow.emit` 分发给 `flowPushSubscriber`（`cmd/jxapproval/bootstrap.go:177`、`:395`）推 `external_instances` ⇒ **本地行天然先于推送存在，回调可命中**。服务器 0 行的根因＝G-3 定义未装载致该链路从未真实走通 ＋ 此前联调用手工 curl 直接推实例（**绕过了本地落库**） |
| 纪律一（写进方案与代码注释） | **禁止任何「只推飞书、不落本地」的生产路径**：推实例唯一入口＝`Pusher.Push`，其数据源就是 `GetInstanceByBizNo`/`ListFlowTasks`（`push.go` 现状即如此）——本地行是推送的前提，而非推送的副产品 |
| 纪律二 | 手工 curl 推 `external_instances` 仅限联调排障，且须知道**该实例本地不可回调**（无 `t_instance` 行 ⇒ `无对应实例`，正是本次实测现象）；`docs/14` runbook 补一句警示（§4 影响清单） |
| 纪律三（小改动） | `BuildSnapshot`（`push.go`）补一条守卫：实例 `PENDING` 但 `RELEASED` 任务数为 0 ⇒ **warn 日志**（无任务快照是异常态，推出去也没有可操作待办），不拦截（REPLACE 首推等场景由上层判断） |
| 幂等与重推语义 | 沿用既有机制，不改：`update_time` 严格递增（`Pusher.Push` 仅当大于已推最大 `PushSeq` 才真推）＋ `t_push_record` 流水（`Pending/Sent/Failed`）＋ `ChooseUpdateMode`（首推 REPLACE、其余 UPDATE，04a §3.1）；回调命中键＝`t_flow_task.task_id`（我方 `createTasksTx` 生成、推给飞书、飞书原样回传） |
| 回调后的「重推更新卡片」 | 状态机推进成功 ⇒ `flowPushSubscriber` 收事件自动重推（已有）⇒ 飞书审批中心待办状态更新。**这条链路已通，无需新增代码**；唯一前提＝G-3 定义装载＋实例落库修复 |

### E. 回调报文留痕（对应 G-7）

> ★ **落地状态**：✅ **已实现**（第 2 批 **`d94580f`**）—— 新增 **`internal/httpapi/middleware_callback.go`**，`callbackBodyLog` **只挂回调一条路由**；★ token **全打码**（记 `token=<len:N>`）、`reason` 截断至 200 字符、`attachments` 只记条数、超 **64KB** 截断（标 `[truncated]`）。★ 这正是 `V-1` 实测取证的手段（留痕看 `action_context` 是否原样回来）。

| 项 | 内容 |
|---|---|
| 现状 | `logMiddleware`（`internal/httpapi/router.go:191`）只记 `method/path/status/duration_ms/trace_id` |
| 方案 | **回调路由单独包一层 `callbackBodyLog` 中间件**（新增函数放 `router.go` 或新文件 `internal/httpapi/middleware_callback.go`），**只挂** `e.POST("/approval/external/callback", …)` 一条路由，不全局生效（其他路由不受影响） |
| 读取与上限 | `echo` 下用 `c.Request().Body` 读取后**重建 body**（`io.ReadAll` + `bytes.NewReader` 回填 `c.Request().Body`）；上限 **64KB**（对齐 `docs/14` runbook 的 `body ≤ 64KB` 门禁），超限截断并标 `[truncated]` |
| 脱敏要求 | ① `token`：**全打码**（不落明文，记 `token=<len:N>`）；② `action_context`：原样落（含 `biz_no`，非敏感、排障必需）；③ `reason`：截断至 200 字符（审批意见可能含敏感文本）；④ `attachments`：只记条数；⑤ 其余字段（`action_type`/`user_id`/`approval_code`/`instance_id`/`task_id`/`message_id`/`id`/`encrypt`）原样落 |
| 日志键 | `trace_id`（与 `logMiddleware` 同源）＋ `biz_no`（解出后，便于按单检索）＋ `message_id` ＋ `status` |
| 替代方案（不推荐） | 改 `logMiddleware` 全局记 body ⇒ 所有接口 body 落日志，敏感面（台账/附件/凭据）扩大，违反最小化 |

### F. 失败路径的用户反馈（对应 G-6）

> ★ **落地状态**：✅ **已实现**（第 3 批 **`36df709`**）—— 新增 `internal/platform/feishu/message.go`（`UpdateApprovalMessage` → `POST /open-apis/approval/v1/message/update`；★ **`message_id` 为空不发请求**）＋ `RepairCardFeedback`；修复循环对**最终失败**行据此更新卡片。★ **`V-2`（`message/update` 请求体字段）已实测定稿（2026-09-28）**：请求体＝`{"message_id":"<id>","status":"<status>"}`（只传 `message_id` ⇒ `60001 no Status error`；`status` 取值与审批状态一致，实测 `"APPROVED"` 成功）。★ **本批追加（推进成功路径）**：实测回调处理成功后平台**未自动刷新**卡片（仍带「同意/拒绝」两键），且卡片操作回调报文**不带** `message_id` ⇒ 新增 `CardRefresher`（推进成功后按 `t_notify_log.message_id`（`0014` 列）主动调 `message/update` 刷成终态）；`RepairCardFeedback` 的「失败态」标注**暂缓调用**（失败态 `status` 取值未实测，不臆造）。

| 情形 | 用户可见行为 | 我方动作 |
|---|---|---|
| ① 回调 10s 内返回 200（正常，绝大多数） | 飞书自动更新卡片为已同意/已拒绝（官方行为，`reference/README.md` ★★ 条目） | 无需动作（「落盘即 200」已实现，`#69`） |
| ② 已受理但异步推进失败 | 卡片先不动；飞书侧异步继续处理，10s 内成功则正常更新 | 派生式修复循环（`RepairPendingApprovals`，每 30s 扫未推进行经 `act` 重驱动，已实现 `5f8e35b`）**先兜底**；推进成功后 `flowPushSubscriber` 自动重推实例 |
| ③ 修复循环也最终失败（不可恢复） | 若不做 F：卡片**退化为「只显示查看详情」**（按钮消失，官方行为） | **调「更新审批 Bot 消息」**：`POST /open-apis/approval/v1/message/update`（接口名已核＝官方 API 清单页《审批任务 · 审批 Bot 消息》；**请求体字段待验证** ⇒ §7 V-2），用落盘的 `message_id` 把卡片标注为「处理失败，请到我方页面重试」 |
| ④ 未受理 4xx（token/准入/字段） | 卡片退化为「只显示查看详情」 | 可接受降级：已有审计留痕（`ErrInvalidToken` → `deny` 审计）＋ 错误日志；不调 message/update（无 message_id 或操作本身非法） |

**F 的落地改动**：

| 文件 | 改动 |
|---|---|
| `migrations/0013_callback_repair.sql`（新） | `ALTER TABLE t_flow_op_log ADD COLUMN message_id TEXT;` ＋ `ALTER TABLE t_approval_def ADD COLUMN feishu_code TEXT;`（C 项共用；均 NULL 可空、无回填——`t_flow_op_log` 现有行无对应卡片语义） |
| `internal/store/`（FlowOpLog repo） | `FlowOpLog` 结构体加 `MessageID`；`InsertFlowOpLogTx` 增列写入；`flow/callback.go` 的 `recordCallback` 从 `req.MessageID` 带入 |
| `internal/platform/feishu/message.go`（新） | `UpdateApprovalMessage(ctx, messageID string, body map[string]any) error`（字段结构待 §7 V-2 验证后定稿；先建端口+Fake，便于装配） |
| `cmd/jxapproval/bootstrap.go` | 修复循环（`RepairPendingApprovals` 所在启动块）对「最终失败」行回调 `UpdateApprovalMessage`；装配 message 客户端（生产＝`*HTTPClient`，开发＝Fake，对齐现有三分支装配惯例） |

**关联项（不在 A–F，但端到端必需，一并列出）**：待办通知 `POST /open-apis/approval/v1/message/send`（`template_id=1008`）的发送端口——`flow.notify.go` 的 `Sender` 端口已定义、装配传 nil。落点：`internal/platform/feishu/` 新增 `NotifySender` 实现 `flow.Sender`（`notify.go` 接口），`bootstrap.go` 把 nil 换成真实现（沿用 `flow.NewNotifier` 的两阶段 EXPECTED→SENT/FAILED 机制，漏发可检出已内建）。★ `actions[]` 四 URL 缺一不可（实测 `60001 actionUrls incomplete error`）、`i18n_resources` 该接口接受 map 形态（与 `external_approvals` 的数组形态**不同**）——均已实测，来源＝`reference/README.md` ★★ 条目。

---

## 3. 修复顺序与批次划分

| 批次 | 内容 | 理由 | ★ 落地状态（提交） |
|---|---|---|---|
| 第 1 批 | **C（定义装载）＋ D（落库纪律）** | G-3 是 G-1/G-2 的前置：定义与实例不落库，字段修对了也过不了「实例存在性」关（实测校验顺序） | ✅ **已实现**（**`5fe1671`**）；★ 启动自检**只进 `/healthz`、不进 `Ready()`** |
| 第 2 批 | **A ＋ B ＋ E ＋ 0013 迁移**（同批一个提交） | A/B 必须同批（回调取不到 `biz_no` 则永远 400）；E 与迁移 0013（message_id 列）随批；`feishu_code` 列随批（C 的双 code 池部分） | ✅ **已实现**（**`d94580f`**） |
| 第 3 批 | **F（message/update ＋ 通知 sender）** | 依赖第 2 批落盘的 `message_id`；联调实测 V-2/V-3 后定稿字段 | ✅ **已实现**（**`36df709`**）；★ **`message/update` 请求体字段仍待实测 `V-2`**（先建端口 + Fake） |
| （第 4 批 · 派单后续） | **「新待办产生」通知接线**（`flow/notify.go` 的 `activateOnType` / `ActivatedNotifyTargets` / `OnFlowEvent` 拆分） | 第 3 批只通了「通知发送端口」；**本条补上「新待办」这一触发事件**（此前 `notifyOnType` 无该事件 ⇒ 无触发点，通知发不出） | ✅ **已实现**（**`c6e26d7`**，现行 HEAD）；★ 见 `docs/06 §R`（B52–B56） |

★ 依据 `05-API §3.14` 末「修复顺序」注：#2 与 #1 同批、#6 是前置——与本表一致。

---

## 4. 对既有文档的影响清单（逐条位点；PM 在改 `02`/`03`，本节给出「需要什么」）

| 文档 | 位点 | 动作 | 归属 |
|---|---|---|---|
| `docs/README.md` | 文档索引表 | 新增 `16-Callback-Repair-Design.md` 行（编号、版本、行数） | 交付总监/PM |
| `docs/05-API.md` | §3.14 请求体行 | 已按官方字段校准（V2.6 ✅）；**追加**：`message_id` 暂存口径、`user_id→open_id` 转换规则、`action_context` JSON 结构 `{"biz_no":…,"task_id":…}`、`instance_id` 反解兜底 | 架构师/PM |
| `docs/05-API.md` | §3.14 错误码表 | 追加「operator 域转换失败 → 40000」一行；注明 `approval_code` 双池校验现处**宽松档**（命中才放行），V-4 实测后升格 | PM |
| `docs/05-API.md` | §3.13 / 全路径清单 | 新增 `POST /api/admin/approval/defs/sync` 行（C-通道①）；`GET /api/approval/defs` 旁注「装载入口见 sync 端点」 | PM |
| `docs/04a-Architecture-Increment-V2.md` | §3（出方向推送） | 快照 `task_list[]` 补 `action_context` 字段定义与同批约束 | 架构师 |
| `docs/04a` | §3.5（定义装载源） | 补「装载入口＝`POST /api/admin/approval/defs/sync`；清单源＝`t_config_mapping` map_kind='approval_code'；token 存 `t_approval_def.callback_token`」；补启动自检 warn | 架构师 |
| `docs/04a` | §4（入站回调端点） | 补官方字段口径交叉引用（指向 `16` §2-A）＋ 留痕/脱敏 ＋ F 的四情形反馈表 | 架构师 |
| `docs/01-PRD.md` | FR-M0-15（回调）/ Q17 | Q17（回调报文实采校准）可标**闭合**：字段已按《三方快捷审批回调》实采定案（除待验证 V-1~V-4 项，见 §7）；FR 验收标准补「官方格式报文回调 200 并推进」 | PM |
| `docs/01a-PRD-Increment-V2.md` | 通知渠道章节（飞书 Bot 主＋站内兜底） | 补「发送＝`message/send`、失败反馈＝`message/update`」的接口归属与实现批次（第 3 批）；「先落后回填定义」已有，补 sender 端口接通批次 | PM |
| `docs/02-UseCase.md`（PM 在改，给需求） | UC-20（回调用例） | 需要补：① 官方字段报文主流程；② `action_context` 缺失但 `instance_id` 可反解的分支；③ `user_id` 域转换（含转换失败可见拒绝）；④ 双 code 池宽松校验 | PM |
| `docs/03-TestCase.md`（PM 在改，给需求） | 新增 TC | 需要覆盖 §6 测试矩阵全部行（至少：官方格式 200、幂等 Duplicate、`user_id` 转换失败 400、留痕脱敏断言、旧自造报文兼容窗口、修复循环最终失败 → message/update） | PM/QA |
| `docs/09-Integration-Verification-Checklist.md` | §联调第一轮 | 新增 4 条待验证实测项：V-1（`action_context` 原样回传）、V-2（`message/update` 请求体）、V-3（按 `user_id` 查 open_id 的端点/scope）、V-4（`external_approvals` POST 响应 code 池归属 ＋ 回调报文 `approval_code` 池归属） | 架构师 |
| `docs/11-Existing-System-Adaptation-Audit.md` | §5 R 台账 | 建议**新增 R29**：「回调字段与官方报文不匹配（G-1~G-4），修复前回调恒 400」——是否入册由 11 作者按其登记判据定 | 11 作者 |
| `docs/14-Deploy-Runbook-Callback.md` | §一键验证命令清单 | 补：① 手工 curl 推实例的警示（本地不可回调）；② 回调留痕日志的检索命令（`trace_id`/`biz_no` 键） | 14 作者 |
| `docs/15-Code-Collision-Register.md` | §2 | 本文档用 `16` 号、放弃派单所给 `15` 号——可在其「不是重号」节记一笔（`15` 号语义唯一化） | 15 作者 |

---

## 5. 数据迁移与向后兼容

| 项 | 内容 |
|---|---|
| 迁移 | **`0013_callback_repair.sql`**（唯一新增迁移）：`t_flow_op_log` 加 `message_id TEXT`；`t_approval_def` 加 `feishu_code TEXT`。两列均可空、无 NOT NULL、无回填（`t_flow_op_log` 既有行无卡片语义；`t_approval_def` 现为 0 行） |
| 迁移纪律 | 对齐既有惯例：`ALTER TABLE ADD COLUMN` 由 `t_schema_migrations` 驱动、幂等（`0007` 头注释既定）；★ 门禁 `C1` 已覆盖 `ALTER` 路径（`R26` 修复，定案 #64），新列须有写入者（`recordCallback` 写 `message_id`、`Register` 写 `feishu_code`）——**不留「建了列没有写入者」的 R26 型缺口** |
| 向后兼容·报文 | 旧自造报文（顶层 `biz_no`/`open_id`）按 A-2 兼容读法保留一个发布版本，注释与 05-API 均写明废弃时点 |
| 向后兼容·快照 | 旧推送快照（无 `action_context`）由 `instance_id` 反解兜底；无需清洗飞书侧数据（下次重推自然带新格式） |
| 回滚 | 0013 仅加可空列，回滚＝不使用即可；代码回滚后旧行为（400）重现，无数据损坏面 |

---

## 6. 测试要点（含可执行验证命令）

**6.1 自动化（本地）**

| 命令 | 验证什么 |
|---|---|
| `go test ./internal/httpapi/ -run 'TestCallback' -v` | 回调 handler 字段校准（官方格式 → 200；缺 `user_id`/`approval_code` → 400；`instance_id` 反解；兼容读法） |
| `go test ./internal/flow/ ./internal/platform/feishu/ -v` | `HandleCallback` 顺序纪律、`ExternalTask.ActionContext` 序列化、`BuildSnapshot` 守卫 |
| `go test ./...` ＋ `bash scripts/check_head_buildable.sh` | 净检出可构建可测试（定案 #51 门禁） |
| `python scripts/check_md_tables.py` | 本文档表格列数门禁 |

**6.2 端到端（联调，官方格式报文样例）**

```bash
# 前提：定义已装载（POST /api/admin/approval/defs/sync 200）、实例已提交（t_instance 有行）
curl -s -X POST http://127.0.0.1:5001/approval/external/callback \
  -H 'Content-Type: application/json' \
  -d '{
    "action_type": "APPROVE",
    "user_id": "<操作人user_id>",
    "approval_code": "<定义code>",
    "token": "<action_callback_token>",
    "instance_id": "{app_id}:<biz_no>",
    "task_id": "<我方task_id>",
    "message_id": "<卡片消息id>",
    "action_context": "{\"biz_no\":\"<biz_no>\",\"task_id\":\"<task_id>\"}",
    "reason": "同意"
  }'
# 预期：HTTP 200 + {"code":0,...,"accepted":true}；随后 t_flow_task 推进、external_instances 重推
```

| # | 实测断言 | 预期 |
|---|---|---|
| E-1 | 官方格式报文（如上） | 200，任务 `PENDING→APPROVED`，`t_flow_op_log` 有 round 对齐留痕 |
| E-2 | 同报文重发 | 200 `duplicate:true`，不二次推进（幂等） |
| E-3 | 只给 `instance_id`、不给 `action_context` | 200（反解兜底生效） |
| E-4 | `user_id` 换不出 open_id（假 user_id） | 400 ＋ 日志告警（不落 op_log、不占幂等键，定案 #62） |
| E-5 | `user_id` 正确但非 assignee | 403（域转换后仍命中本人校验，证明转换真的生效——**这是转换链路的关键反证**） |
| E-6 | 日志留痕 | http 日志含 body（token 打码）；`grep <biz_no>` 可检索 |
| E-7 | `action_context` 原样回传（V-1） | 若飞书**不**回传或改写 ⇒ B 方案兜底链（`instance_id` 反解）仍通，但须回 `09` 台账记实测结果 |

**6.3 联调第一轮新增实测项**（并入 `docs/09`）：§7 的 V-1 ~ V-4。

---

## 7. 未定项与风险（★ 全部标「待验证」，不得当结论使用）

> ★★ **V1.1 状态确认**：`V-1`~`V-5` 与 `R-1`~`R-3` **均仍为「待验证 / 待实测」** —— ★ **代码已按"先宽后严 / 端口 + Fake"落地，但平台侧行为未经实测，一律不得写成结论**。★ **`V-2` / `V-3` / `V-4` 保持「仍待联调实测」**（**不得写成已验**）。★ 落点＝`docs/09` §2 阶段一（`V-1`~`V-4`）。

| # | 未定项 | 影响 | 验证方法 |
|---|---|---|---|
| V-1 | ★★ **飞书是否原样回传 `action_context`**（官方文档如此表述，**未实测**） | B 方案的前提；若不回传/改写，`biz_no` 只剩 `instance_id` 反解一条兜底链 | 联调：推实例带 `action_context` → 在飞书点同意 → 看回调日志 body（E 项留痕正是为此） |
| V-2 | ✅ **已实测定稿（2026-09-28）**：`POST /open-apis/approval/v1/message/update` 请求体＝**`{"message_id":"<id>","status":"<status>"}`** —— 只传 `message_id` ⇒ `60001 "no Status error"`（HTTP 仍 200）；加 `status` ⇒ `{"code":0,"data":{"message_id":…},"success"}`；`status` 取值与审批状态一致（实测 `"APPROVED"` 成功）。★ 「失败态」`status` 取值仍未实测（`RepairCardFeedback` 暂缓调用、Error 告警，不臆造） | 已消解主项；实现＝`HTTPClient.UpdateApprovalMessage` ＋ `CardRefresher`（推进成功路径） | 真机已验（成功形态）；「失败态」取值留待联调补验 |
| V-3 | **按 `user_id` 查 open_id 的端点与 scope**（本方案写 `GET /open-apis/contact/v3/users/{user_id}?user_id_type=user_id`，按官方《通讯录》文档；该形态未实测） | A-3 转换策略 | `reference/README.md` 已证 `contact/v3` 可用且 scope 候选已知 ⇒ 联调直接试；失败则按 `99991672` 附带的官方申请链接补 scope |
| V-4 | ★★ **`approval_code` 双池归属**：① `POST external_approvals` 响应回填值属自定义池还是真实池；② 回调报文里的 `approval_code` 属哪个池 | C 的落库归位与回调校验宽严档；若处理错 ⇒ 推实例 `1390002`（静默失败族） | 联调：按 `reference/README.md` 双池实测法（自定义 code 建定义 → 看响应 → 用两个值分别推实例）；回调侧看留痕 body 里的实际值 |
| V-5 | `enable_quick_operate` 定义级开关与 `action_configs` 明细的配合语义（`reference/README.md` 已标「按待实测记录」；`docs/09` `QV2-A32`） | 定义装载时是否必须显式置 true（否则两键不存在，回调无从谈起） | `docs/09` 既定联调项；C 装载入参应**显式设 true**（实测可生效），不赌默认值 |
| R-1 | （风险）回调通知 `message/send` 与本方案 F 的 `message/update` 同族但**接口形态不一**（`i18n_resources` 一个收 map、`external_approvals` 收数组——实测已证） | 实现时不能复用同一序列化惯例 | 已实测（`reference/README.md` ★★ 条目），按接口分别写 |
| R-2 | （风险）`user_id` 转换依赖外部 API ⇒ 回调同步路径多一次网络调用 | 与「落盘即 200、毫秒级返回」目标冲突 | 缓解：进程内 TTL 缓存；转换失败仍走可见拒绝（毫秒级）；**不**把转换放异步（准入必须先于落盘，定案 #62） |
| R-3 | （风险）本方案若第 1、2 批分次上线，中间态「定义已装载但回调字段未修」仍 400 | 中间态用户观感 | 在 05-API §3.14 与本文档均明示批次顺序（§3）；不宣称中间态可用 |

---

## 8. 变更记录

| 版本 | 日期 | 内容 |
|---|---|---|
| V1.1 | 2026-09-27 | **落地状态回填（纯文档，事实＝已落地代码；PM 补）**：① 文首新增「落地批次」「仍待联调实测」两行（4 批：`5fe1671` / `d94580f` / `36df709` / `c6e26d7`）；② §2 各小节（**A / B / C / D / E / F**）各加「落地状态」行（附提交号），**原文一字不删（留痕）**；③ §3 批次表加「★ 落地状态（提交）」列 ＋ 补「第 4 批（`c6e26d7`）」行；④ §7 加状态确认 —— **`V-2` / `V-3` / `V-4` 保持「仍待联调实测」，不得写成已验**。★ 本文**仍不含实现改动**。 |
| V1.0 | 2026-09-27 | 首版：A–F 逐项方案 ＋ G-1~G-8 缺口台账 ＋ 影响/迁移/测试/待验证；编号取 `16`（`15` 已占用）并说明理由 |
