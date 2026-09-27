# 参考输入（仓库外，只读）

本仓库的需求与设计**不复制**下列文件，只引用其绝对路径。这些文件位于 WorkBuddy 交付目录，
由业务线维护；**改动业务口径时须同步评估本仓库文档**。

| 文件 | 绝对路径 | 在本项目中的作用 |
|---|---|---|
| 技术方案书 V1.0-r1 | `C:\Users\haoduan\WorkBuddy\江熙新材\deliverables\procurement-system\技术方案书V1.0.html` | **本仓库的总纲**：架构、选型、M0–M8 模块、关键设计、接口清单、风险 |
| 配置工具表 v3.0（xlsx） | `C:\Users\haoduan\WorkBuddy\江熙新材\deliverables\procurement-approval\采购与费用审批体系配置工具表_v3.0.xlsx` | **业务口径第一权威来源**：单据清单 11 张、表单字段、审批阈值矩阵、角色权限矩阵、台账与看板设计 12+4、待确认事项 27 项 |
| 配置工具表 v3.0（纯文本导出） | `C:\Users\haoduan\WorkBuddy\江熙新材\_build\xlsx_dump_v3.txt` | 上表的全文文本导出，供无 xlsx 读取能力时使用 |
| 费用管理办法 V3.0 | `C:\Users\haoduan\WorkBuddy\江熙新材\deliverables\procurement-approval\费用管理办法V3.0.html` | 制度正文 12 章 67 条 + 附录 A/B/C |
| 审批流程图集 V3.0 | `C:\Users\haoduan\WorkBuddy\江熙新材\deliverables\procurement-approval\审批流程图集V3.0.html` | 图 0 分界总览 + 图 1–9（含驳回分支） |
| 飞书平台适配性评估 V1.9 | `C:\Users\haoduan\WorkBuddy\江熙新材\deliverables\procurement-approval\飞书平台承载V3.0流程的适配性评估.html` | 飞书侧能力与额度的核证结论（适配度 7 可配 / 5 需绕 / 0 缺口） |

> 上述文件**不在本仓库内**，本仓库不做副本，以避免口径分叉。

## 已核清的外部事实（一手来源）

| 事实 | 来源 |
|---|---|
| 审批事件类型 `approval_instance` / `approval_task`，且**必须先按 `approval_code` 调「订阅审批事件」接口** | 飞书开放平台《审批实例状态变更》《审批任务状态变更》 |
| 长连接仅需出公网，无需公网 IP / 入站端口；**3 秒内须处理完**；**集群不广播** | 飞书开放平台《事件订阅概述》 |
| 审批模板**不能用 API 建**（不支持条件分支，且建后无法停用/删除） | 飞书开放平台《创建审批定义》 |
| **流水号控件**：固定字符 + 提交日期（年/年月/年月日）+ 自增序号（1–9 位，重置周期可选），**不支持审批人/办理人编辑** | 飞书帮助中心《管理员使用流水号控件》 |
| 免费版自建应用 API 调用量：2024-11-13 起基准合计 10,000 次/月；另有「2026 年 6 月限时 100 万次」表述；**事件订阅不计入、审批 API 计入** | 飞书开放平台《自建应用 API 调用量上限调整说明》（当前生效值须书面向飞书确认） |
| 三方快捷审批回调 `action_callback_url`：**一级来源未提出 HTTPS / 端口要求**（官方两页只描述回调语义与参数；示例 URL 自注「仅为示例」） | 飞书开放平台《三方快捷审批回调》（2025-04-21）·《创建三方审批定义》 |
| 三方快捷回调**超时口径官方不一致**：CN 文档 **10s** ／ 国际文档 **5s** | 同上两页（⇒ ★ 归 `QV2-02`） |
| 创建三方审批定义：接口频率限制 **1000 次/分钟、50 次/秒** | 《创建三方审批定义》 |
| ★★ **三方审批在免费档可用**：全程**无 `1390013`**（unsupported approval for free process），且**建定义成功**（`{"code":0,…}`）⇒ ★ **`QV2-09` 正面定证** | **2026-09-27 实测**（应用 `cli_aa33a8b22f78dcb4`：`external_approvals` / `external_instances/check` / `contact/v3` / `tenant_access_token`） |
| ★★ **平台接受 `http` ＋ 非 443 端口**：用 `http://office.hunanyichu.com:5500/approval/external/callback` 作 `action_callback_url` **建定义成功** ⇒ **HTTPS 不是阻塞项**（HTTPS 化由 P0 降为**上线前可选加固**）⇒ ★ **`QV2-A19` 正面定证** | **2026-09-27 实测**（应用 `cli_aa33a8b22f78dcb4`：`external_approvals` / `external_instances/check` / `contact/v3` / `tenant_access_token`） |
| ★★ **`approval_code` 由客户端指定并生效**（读回 `data.approval_code` ＝ 传入值 `JXQA-TEST-0001`）；★★ **但"查询键"不是它**：`GET /open-apis/approval/v4/external_approvals/{创建响应返回的那个值}`（例 `6AC44B6B-FD5E-424A-AA22-DC7E42A5BA72`）才通；**用传入的 code 做路径参数查不到**（`1390002 approval code not found`）；**`?approval_code=` 形态也不通** ⇒ ★ **同一字段名指两样东西，必须写清** | **2026-09-27 实测**（应用 `cli_aa33a8b22f78dcb4`：`external_approvals` / `external_instances/check` / `contact/v3` / `tenant_access_token`） |
| ★ **同一 `approval_code` 反复推 ＝ 同一条定义**（**幂等更新成立**，不会累积）：连推 6 次（含改名 / 换分组 / 改开关）**全部落回同一 code** | **2026-09-27 实测**（应用 `cli_aa33a8b22f78dcb4`：`external_approvals` / `external_instances/check` / `contact/v3` / `tenant_access_token`） |
| ★ **`group_code` 与 `group_name` 必须成对提供**：只给 `group_name` ⇒ `1390001 Group code cannot be empty`（原样英文串）；只给 `group_code` ⇒ `1390001 审批分组Code和名称不匹配`；**两者同时给 ⇒ `code:0`** | **2026-09-27 实测**（应用 `cli_aa33a8b22f78dcb4`：`external_approvals` / `external_instances/check` / `contact/v3` / `tenant_access_token`） |
| ★ **`approval_name` 传纯文本会被服务端转成 `@i18n@<uuid>`**（读回所见），`description`/`biz_name` 亦自动生成 ⇒ 读回看不到原文，但不影响使用 | **2026-09-27 实测**（应用 `cli_aa33a8b22f78dcb4`：`external_approvals` / `external_instances/check` / `contact/v3` / `tenant_access_token`） |
| ★★ **服务端默认填充**（我们没传的字段）：`enable_quick_operate:false` · `allow_batch_operate:false` · `support_batch_read:false` · `enable_mark_readed:false` · `exclude_efficiency_statistics:false`；★★ **且 `enable_quick_operate` / `allow_batch_operate` / `support_batch_read` 可显式设 `true` 并生效**（已实测读回 `true`）。★ **但 `enable_quick_operate` 的语义待核** —— **可能**是飞书侧「同意/拒绝」快捷操作的**总开关**；若如此，**定义装载必须显式开 `true`**，否则口径 2（"飞书只做同意·拒绝"）会是空的 ⇒ ★ **按"待实测"记录，不是结论** | **2026-09-27 实测**（应用 `cli_aa33a8b22f78dcb4`：`external_approvals` / `external_instances/check` / `contact/v3` / `tenant_access_token`） |
| ★ **`external_instances/check` 入参**：`instances[]` 每项**必须含 `update_time` ＋ `tasks`**（只给 `instance_id` ⇒ `99992402 field validation failed`） | **2026-09-27 实测**（应用 `cli_aa33a8b22f78dcb4`：`external_approvals` / `external_instances/check` / `contact/v3` / `tenant_access_token`） |
| ★ **通讯录可用**：`GET /open-apis/contact/v3/departments?page_size=1&department_id_type=open_department_id` → `{"code":0,"data":{"has_more":false,"items":[{"department_id":"0","open_department_id":"0","member_count":1}]},"msg":"success"}` ⇒ ★ 响应**无 `total`**、用 **`has_more`**（**印证 `08 F16`**） | **2026-09-27 实测**（应用 `cli_aa33a8b22f78dcb4`：`external_approvals` / `external_instances/check` / `contact/v3` / `tenant_access_token`） |
| ★ **权限错 `99991672` 会附带官方直达申请链接**（形如 `/app/{app_id}/auth?q=<scopes>&op_from=openapi`）⇒ 遇权限缺口**不必猜**。实测所需 scope：审批侧 **`approval:approval` 或 `approval:external_approval`**（任一）；通讯录侧 **`contact:contact.readonly`** 等任一（候选共 5 个：`contact:contact.base:readonly` / `contact:department.organize:readonly` / `contact:contact:access_as_app` / `contact:contact:readonly` / `contact:contact:readonly_as_app`） | **2026-09-27 实测**（应用 `cli_aa33a8b22f78dcb4`：`external_approvals` / `external_instances/check` / `contact/v3` / `tenant_access_token`） |
| ★ **`directory/v1/departments` → `404 page not found`** ⇒ `08 F16` 记的该路径**待核**；`contact/v3` 可用且权限已探明 | **2026-09-27 实测**（应用 `cli_aa33a8b22f78dcb4`：`external_approvals` / `external_instances/check` / `contact/v3` / `tenant_access_token`） |
| ★ **官方超时口径不一致**（CN 文档 **10s** / 国际文档 **5s**）—— ★ 本批**仅引用**，原条目已在上表（⇒ ★ 归 `QV2-02`） | 官方 CN 文档 / 国际文档（**上一批已记入本表**；本批引用） |

> ★ **方法论注（非官方来源的处置）**：**非官方来源（社区问答）称"强制 443 / 必须 HTTPS"**，与一级来源比对后**不采用** —— 该说法实际指向**事件订阅 / 免登那类回调**，与本项目使用的**三方快捷回调**不是同一条链路。
