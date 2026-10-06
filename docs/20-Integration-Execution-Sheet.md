# 20 · 联调执行单（Integration Execution Sheet）

> **定位**：把 `docs/09-Integration-Verification-Checklist.md`（**41 项需执行**）改写为**可逐项执行、可逐项回报**的操作单。
> `docs/09` 是**权威口径**（判定标准不得改）；本文只做**排序、分工、可分步性标注**。
> **编制**：WorkBuddy · 2026-10-05 · **进度**见 §8
> ★★ **更新 2026-10-06（联调环境实测核实）**：新增 **§1B 测试环境**（**已配好，可直接开跑**）；**更正 §1 的 `P3`/`P4`** —— 实测 `approval_code` **由我方自定义**（无需飞书侧值）、且**测试环境的回调 token 与域名均已配置** ⇒ `defs/sync` 的 503 门**已解除**。

---

## 0. 怎么用（★ 先看这节）

每一轮我们这样走：

1. **你（或我方）先跑一次环境自检**：`bash scripts/deploy-test-server.sh --check-only` —— 全绿即说明**测试环境活着、代码是当前 HEAD**（详见 **§1B**）。
2. **你看 §1 前置**：★ 实测后**只剩 `P1`（服务器 DNS）与 `P2`（飞书侧权限清单/事件订阅）**需要外部动作；`P3` 已由我方自办完成、`P4` 测试环境已配好。
3. **你说「开始第 N 项」** ⇒ 我方给出该项的**完整操作步骤 ＋ 可直接照做的请求体/脚本**。
4. **你执行并把结果（响应/截图/报文）发我** ⇒ 我方**判读**（对照 `docs/09` 的 A/B/C 判定），落到台账并给出「下一步」。
5. **不通过的项**，我方按 `docs/09` 给定的处置方向**直接改代码/规格**（不会只说"不通过"）。

★ **分步原则（本单最有用的东西）**：
把 12 项阻塞项按「**是否要求链路连续稳定**」拆开 ——
**一半是"单点调用"，链路抖动只影响成功率、不影响结论（可重试）⇒ 今天就能做**；
另一半要求**长连接/时序连续**，**必须先修 `P1`**，否则结论会被污染成**假阴性**。

---

## 1. 前置条件（★ 硬前置只有三项）

| 编号 | 前置 | 挡谁 | 谁办 | 现状 |
|---|---|---|---|---|
| **P1** | 服务器 `/etc/resolv.conf` 被 `dhcpcd` 周期性写空（`R33`） | **只挡 §3 第二批（4 项）** | 你/运维 | 未修 |
| **P2** | 飞书侧：应用权限清单齐全 ＋ 事件订阅已开 | 挡 §3 ＋ §4 的 `CHK-6` | 你（开放平台） | ★ 先做 `CHK-8` 核对 |
| **P3** | **`approval_code` ↔ 单据类型 映射表填值** | **挡所有"建定义/推实例"** | ★ **我方自办（已完成）** | ★★ **更正**：`approval_code` **由我方自定义**，**不需要飞书侧的值** —— 依据 `internal/approval/defregistry.go:29`「稳定标识；**本地配置给出**」与 `:132`「主键 ＝ **我方自定义 code**」。映射文件 `docs/reference/config-mapping.jx.json` 已产出并通过导入器实测（`approval_code 11 / ledger_type 9 / threshold 5 / ledger_field 25`） |
| **P4** | 回调入站面：回调 token ＋ 回调域名 | 挡回调相关（`QV2-A28` 等） | ★ **测试环境（已配好）** | ★★ **更正**：测试服务器 `.env` 里 `JX_ACTION_CALLBACK_TOKEN`（16 位测试 token）与 `JX_CALLBACK_DOMAIN`（`http://office.hunanyichu.com:5500`）**均已设置** ⇒ `defs/sync` 的 503 门**已解除**。★ `DEV_MODE=true` 是**测试环境有意为之**（允许 `?open_id=` 直连登录），**上线前**才需关闭 |
| **P6** | **测试环境代码版本** | 不阻塞 | ★ **我方自办（已完成）** | 已把仓库 **HEAD `f6ed88e`** 部署到测试服务器并自检通过（详见 §1B）；★ 重申入口：`bash scripts/deploy-test-server.sh` |
| **P5** | 回调地址确认 | 不阻塞 | — | ★ **`QV2-A19` 已实测**：飞书**接受 `http` ＋ 非 443 端口** ⇒ **HTTPS 不是阻塞项**（降为上线前可选加固） |

> ★ **结论：修好 `P1` 才能做 §3 那 4 项；其余批次**（§2 §4 §5）**不依赖 `P1`**。

---

## 1B. 测试环境（★★ 2026-10-06 实测核实 · **已配好，可直接开跑**）

### 1B.1 拓扑（实测确认，非推断）

```
公网   http://office.hunanyichu.com:5500
         │   （路由器端口转发 5500 → 192.168.10.50:5000）
         ▼
Caddy  :5000   ── reverse_proxy ──▶  127.0.0.1:5001
         ▼
jxapproval       监听 127.0.0.1:5001（★ 由 .env 的 JX_LISTEN_ADDR 决定）
部署目录         hnyc-server（192.168.10.50）:~/services/jxapproval
```

★ **踩坑记录（务必注意）**：我方应用在 **`127.0.0.1:5001`**，而 `*:8080` / `*:8088` 上是**别的服务**。
直接 `curl 127.0.0.1:8080/` 会拿到 `401 {"message":"missing or malformed jwt"}` —— **那不是我们的应用**。
⇒ ★ **自检端口一律现读 `.env`**（`deploy-test-server.sh --check-only` 已内建此规则）。

### 1B.2 环境现状（逐项实测）

| 项 | 值 | 证据来源 |
|---|---|---|
| 部署版本 | ★ **`0.3.5-s3-231-g0e2c4eb`**（＝ 仓库 HEAD `0e2c4eb`，含 `N-066` 的 `group_code` 修复） | `/healthz` 的 `data.version` |
| `JX_ENV` / `DEV_MODE` | `test` / `true` | 服务器 `.env` |
| 飞书凭据 | `JX_APP_ID=cli_aa33a8b22f78dcb4` **已配**，长连接**已建立** | 日志 `connected to wss://msg-frontier.feishu.cn` |
| `JX_CALLBACK_DOMAIN` | `http://office.hunanyichu.com:5500` | 服务器 `.env` |
| `JX_ACTION_CALLBACK_TOKEN` | **已设**（16 位测试 token） | 服务器 `.env` |
| `JX_APPROVAL_GROUP_CODE` | ★ **2026-10-06 实测新增**：飞书 `external_approvals` 的 `group_code` **必填** ⇒ 测试环境用租户内既有分组 **`JXQA-GROUP-1`**（名「江熙新材审批」） | 实测：不给 ⇒ 飞书 `1390001`；给 ⇒ `code=0` |
| 通讯录镜像 | ★ **据实更正（2026-10-06 实测）**：镜像**为空**（`org_sync.user_count=0` / `dept_count=0`）⇒ 本次测试用 `ou_test_*` 合成 open_id。★ 飞书侧**真实部门 5 个**（综合运营部 / 销售部 / 质检技术部 / 生产部 / 总经办），**用户 0**（`contact/v3/users` 返回 0 条） | `/readyz` 的 `org_sync`；`contact/v3/departments` |
| 权限规则 | 70 条 | 表 `t_permission_rule` |
| 系统管理员 | `郝端` / `ou_7a88…c40a`（`active=1`） | 表 `t_user_role` |
| ★ **业务角色（2026-10-06 新配）** | `ou_test_ops_supervisor` 综合运营主管（综合运营部）· `ou_test_pgm` 项目总经理（总经办）· `ou_test_inspector` 验收人（质检技术部）· `ou_test_supervisor` 主管领导（综合运营部） | 表 `t_user_role`；★ 配齐后 `preview` **15 组用例零缺失** |
| 已建飞书定义 | ★ **11 张**（`jx_ba`…`jx_sub`，每 `doc_type` **唯一**，`def_version=1`）；★ 旧 `JXQA-TEST-0001` 已按用户裁定 **A** 清除。★ **实测：飞书侧读回 11/11 全 `code=0`**、`group_code` 全为 `JXQA-GROUP-1` | 表 `t_approval_def`；`GET /external_approvals/<feishu_code>` |
| `/healthz` 五项 | `subscribe` / `longconn` / `db_writable` / `single_instance` / `approval_defs` **全 true** | `--check-only` 输出 |
| ★ **测试角色缺口的实测值** | 配角色**前**：启动自检 `unresolved=3`；配齐**后**：`unresolved=1`，★ 且**剩的那 1 个是探针偏差**（见 §1B.9.3） | 日志 `启动自检：…算不到人的角色` |

### 1B.3 开动序列（★ 可直接复制）

| 步 | 谁 | 命令 | 期望 |
|---|---|---|---|
| **① 环境自检** | 任意 | `bash scripts/deploy-test-server.sh --check-only` | 全绿（含公网入口 200） |
| **② 准备映射** | 我方 | 见 §1B.4 —— ★ **先裁定 `doc_type` 重复问题** | — |
| **③ 配置分组 code** | 服务器 `.env` | `JX_APPROVAL_GROUP_CODE=JXQA-GROUP-1` ＋ 重启 | ★ **飞书 `group_code` 必填**；缺 ⇒ 第 ⑤ 步 11 张全失败（见 §1B.8） |
| **④ 导入配置** | 服务器 | `cd ~/services/jxapproval && set -a; . ./.env; set +a && ./jxapproval import-config <config.json>` | 打印 `校验通过：… 合计 N 条`（★ **本载荷涉及的映射类全量替换**） |
| **⑤ 装载定义** | ★ **我方自办**（会话见 §1B.7，**无需人工点击**） | `POST /api/admin/approval/defs/sync`（无请求体） | 返回 `synced/created/updated/skipped/failed` 计数；飞书侧出现对应三方定义 |
| **⑥ 开测** | 按 §2 逐项 | — | — |

> ★★ **实测执行记录（2026-10-06，用户裁定 A 之后）** —— 上表 ①–⑤ **已全部执行完毕**：
> | 步 | 结果 |
> |---|---|
> | ① 环境自检 | 全绿（含公网 200） |
> | ② 映射（含 `group_code` 预置） | `approval_code 11 / ledger_type 9 / threshold 5 / ledger_field 25`，回读校验通过 |
> | ③ 配置分组 code | 服务器 `.env` 已加 `JX_APPROVAL_GROUP_CODE=JXQA-GROUP-1` |
> | ④ 导入配置 | ★ **本载荷涉及映射类全量替换** ⇒ 旧 `JXQA-TEST-0001` **导入即自动清除**（另手工清 `t_approval_def` 同 `doc_type` 行，防反查二义） |
> | ⑤ 装载定义 | ★★ **`created=11 failed=0 synced=11`**（修复前为 **11 张全失败**）；幂等重跑 ⇒ `updated=11`、库内未增 |
> ★ **下一步＝第 ⑥ 步：按 §2 第一批 8 项开测。**

### 1B.4 ★★ 开动前必须先裁定的口径（否则第 ④ 步会踩静默歧义）

`internal/store/repo_approval_def.go#GetApprovalDefByDocType` 的 SQL 是 `WHERE doc_type = ?`，
**没有 `ORDER BY`、没有 `LIMIT`**。
⇒ ★ **同一个 `doc_type` 若存在两条定义，反查结果不确定**（返回哪条取决于存储顺序），**而且不报错**。

现状：库里已有 `JXQA-TEST-0001 → PR`。若要导入 11 类新 code（`jx_pr` 等），`PR` 会**有两条**。
⇒ ★ **二选一**：① 删掉旧的 `JXQA-TEST-0001`（联调基线要"11 类各一条"）；② 或沿用其 code、不给 PR 新增。
**建议 ①** —— 并建议同批把"`doc_type` 唯一"做成机检（避免下次再踩）。

### 1B.5 日志（★ 联调期排障入口）

★ **程序自身不写日志文件（只走 stdout）** ⇒ 已由 `scripts/start.sh` **固定落 `logs/app.log`**。
（实测教训：换二进制后若按旧文档 `nohup ./start.sh >/dev/null 2>&1 &` 启动，日志**整条丢失**，
`logs/app.log` 停在旧时间戳、新请求一条不落 ⇒ 联调期等于没有日志。）

```
ssh chadhao@192.168.10.50 'tail -f ~/services/jxapproval/logs/app.log'
```

### 1B.6 部署 / 回滚入口（★ 不再手工敲）

```
bash scripts/deploy-test-server.sh              # 构建(取 HEAD 干净树) → 上传 → 备份 → 切换 → 自检
bash scripts/deploy-test-server.sh --check-only # 只自检
bash scripts/deploy-test-server.sh --rollback   # 回滚二进制 + 数据库到最近一次备份
```

★ **为什么必须"取 HEAD 干净树"构建**：本仓库有**第二个 Agent 并行开发**，工作区随时可能是在途半成品。
实测撞上过（`handlers_approval.go` / `specload.go` 正被改 ＋ 一个未跟踪新文件）——
那种状态构建出的二进制**没有任何报错**，但内容是**没人验收过的中间态**（最危险的一类）。

---

### 1B.7 管理员会话（第 ④ 步前置）—— ★ **已实测打通，无需人工点击**

`defs/sync` 挂在 `admin` 组，鉴权＝`requireSysAdmin`（`handlers_admin.go:46`，判据是 `t_user_role.role == 系统管理员`）。
★ **DEV_MODE 下有直连登录路径**，⇒ **我方可在服务器上自行建会话来驱动第 ④ 步**，不必等人点后台：

```bash
# 在测试服务器上执行（cd ~/services/jxapproval）
ADMIN=ou_7a886a454ab1e8d249dcbd01aec1c40a        # 郝端（t_user_role，role=系统管理员，active=1）
curl -s -c /tmp/jxck -o /dev/null   "http://127.0.0.1:5001/auth/feishu/callback?open_id=$ADMIN&state=dev-check"   # ⇒ 302，种下 jx_session
curl -s -b /tmp/jxck "http://127.0.0.1:5001/api/admin/users"                     # ⇒ 200（验证会话有效）
```

**实测记录（2026-10-06，测试实例 `0.3.5-s3-209-gf6ed88e`）**：

| 情形 | 结果 |
|---|---|
| 无会话 `GET /api/admin/permission-rules` | **401**（对照：门是关着的） |
| `GET /auth/feishu/callback?open_id=…&state=dev-check` | **302**，种下 `jx_session`（另见 `jx_oauth_redirect`） |
| 带会话 `GET /api/admin/permission-rules` | **200** |
| 带会话 `GET /api/admin/users` | **200**（返回 `郝端` / `department=江熙新材`） |
| 带会话 `GET /api/admin/role-agents` | **200** |
| 带会话 `GET /api/admin/constants` | **400**「table 不能为空」—— ★ **端点可达且已授权**，只是缺必填查询参数 |

★ **注意**：`state` 即使在 DEV 直连下也**必填** —— `handlers_biz.go:51` 的校验位于 dev 分支**之前**（缺 state ⇒ 400「缺少 state（防 CSRF）」）。

---

### 1B.8 ★★ 实测发现：飞书建定义**必须带 `group_code`**（本轮联调第一号阻塞，已定位）

**现象**（2026-10-06，测试实例，导入映射后首次 `defs/sync`）：`HTTP 500`，11 张**全部**失败，每条形如

```
code=1390001 msg=Group code cannot be empty when create approval definition
```

**根因（逐层取证）**：

1. ★ `internal/httpapi/handlers_approval.go:1140`（装配 `DefInput`）**从未赋值 `GroupName`/`GroupCode`**；
2. ★ `internal/platform/feishu/external.go:97` 只在 `GroupName != ""` 时才送 `group_name`，且**从不送 `group_code`**；
3. ★★ 而**飞书官方文档写明 `group_code` 是「必选」**（用户自定义；不存在则新建分组；`group_name` 仅用于更新分组显示名）；
4. ★ 代码注释 `:1126`「分组留空（飞书 `group_name` **可选**）」—— ★ **该前提是错的**，是 1390001 的直接来源。

**决定性取证**（不经应用、直接调平台）：

| 探针 | 请求 | 结果 |
|---|---|---|
| A | 不传分组 | `1390001` |
| B | 只传 `group_name`（顶层） | `1390001`（★ 证明"补 name 就好"**是错的假设**） |
| C | 传**新** `group_code`（`jx_approval`） | `1390001 审批分组Code和名称不匹配…` |
| **D** | 传**既有** `group_code=JXQA-GROUP-1`（不传 `group_name`） | ★ **`code=0`**，回填 `approval_code=80C5FF8D-B12A-4BB9-9E84-C35C77CD8EC8` |

★ 既有分组值由**读回当年成功建的定义**得到（`GET /external_approvals/6AC44B6B-…` 返回 `group_code=JXQA-GROUP-1`）。

**修法（最小）**：装配处补 `group_code`（来自配置），**不传 `group_name`**；并新增配置键与装载门禁。

★★ **顺带实测回答 `docs/16 §7 V-4`**（此前"响应回填值属自定义池还是真实池"**未实测**）：
→ **响应回填的 `approval_code` 是「真实池」值**（平台生成的 UUID `80C5FF8D-…`，**不等于**我方入参 `jx_ba`）⇒ `t_approval_def.feishu_code` 存的正是它，推送实例时**应优先取用**（现有代码已如此）。

---

---

### 1B.9 ★★ 三个「验证口径」—— 查错了会得出相反结论（2026-10-06 实测）

联调期要做大量「成了没有」的判断。以下三处**第一直觉是错的**，实测已各踩一次：

#### 1B.9.1 ★★ 验证「定义是否已装载」**不能看 `/healthz`**

`healthz.checks.approval_defs` 是**启动时点快照**（`cmd/jxapproval/bootstrap.go:170-179`：仅在启动期读一次 `CountApprovalDefs`）。
★ 实测：装载 **11 张之后**、**未重启**时该值仍为 **`false`**；**重启后变 `true`**（日志 `启动自检：三方审批定义已装载 def_count=11`）。

| 想验证 | ❌ 不要看 | ✅ 应该看 |
|---|---|---|
| 定义是否装载成功 | `/healthz` 的 `approval_defs`（时点快照，会滞后到下次重启） | ★ `POST /api/admin/approval/defs/sync` 的**响应计数**（`created/updated/failed`） |
| 本地库里有哪些定义 | — | ★ `GET /api/approval/defs`（**不重启即可查**，实测返回 11 条） |
| 飞书侧是否真的建成了 | 本地库（只能证明本地有记录） | ★ **直调平台读回**（见 §1B.9.2） |

#### 1B.9.2 ★★ 读回飞书定义**必须用 `feishu_code`，不是我方 `jx_*` code**

`GET /open-apis/approval/v4/external_approvals/<code>` 的 `<code>` 是**平台返回的 UUID**（真实池），**不是**我方入参的 `jx_ba`。
★ 实测：用 `jx_ba` 读 ⇒ **`1390002`**；用库里的 `feishu_code`（如 `80C5FF8D-B12A-4BB9-9E84-C35C77CD8EC8`）读 ⇒ **`code=0`**。

```bash
# 用库里的 feishu_code 逐个读回（11/11 应全 code=0）
sqlite3 data/jxapproval.db "select doc_type||' '||approval_code||' '||feishu_code from t_approval_def order by doc_type;"
```

★ 这同时**实测回答了 `docs/16 §7 V-4`**（此前标「未实测」）：响应回填的 `approval_code` 属**真实池**（平台 UUID），`t_approval_def.feishu_code` 存的正是它，推送实例时应优先取用。

#### 1B.9.3 ★ 启动自检的 `unresolved` 计数**恒比真实缺失多 1**（探针偏差，非配置问题）

`bootstrap.go:188-195` 的烟测只传 `DocType/AmountCents/UsageCategoryL1`，**未传 `ApplicantOpenID`**；
而 `internal/chain/assign.go:90-97` 对 `actor=applicant` 的节点在缺申请人身份时**记一条 unresolved** ⇒ **恒存在一条，与配置无关**。

★ 实测对照（同一采一链）：

| 查法 | 配角色前 | 配齐 4 个业务角色后 |
|---|---|---|
| 启动自检日志 `unresolved` | **3** | ★ **1**（正是 `回交凭据` 那个 `applicant` 节点） |
| `POST /api/approval/preview`（携带会话身份） | 2（`综合运营主管`×2 节点） | ★ **0** |

⇒ ★★ **判断「到底缺哪个角色」一律用 `POST /api/approval/preview`**（它给 `unresolved_roles` 明细：节点名 ＋ 角色 ＋ 原因），**不要照启动日志的数字去权限管理页找** —— 找不到对应关系。
★ **已修复（`N-068` ② · 2026-10-06）**：启动自检现带**具名探针身份**（`smoke-probe`，`cmd/jxapproval/bootstrap.go#chainSmokeFacts`）使 applicant 类节点可解析 ⇒ **计数与 preview 口径对齐**；且日志**逐条点名**（节点名 ＋ 角色 ＋ 原因，`#logChainSmokeUnresolved`）并随计数给出本次探针身份 —— 「配置缺失」与「探针身份」两件事在日志里可区分。★ 上方实测对照表为**修复前历史形态**（保留不失真）；判据＝`cmd/jxapproval#TestChainSmokeFactsResolveApplicantN068`（含反面对照：不带身份时 applicant 恒 unresolved）。

> ★ 附：`unresolved_roles` 此前的响应键名曾是 **`NodeID/NodeName/Role/Reason`**（大写驼峰），
> 与 `docs/05` 契约声明的 `node_id/node_name/role/reason` **不一致**（`chain/assign.go` 漏 `json tag`）
> ⇒ ★ 脚本取值时**两种都试**，以实测为准；已登记 `N-068` 修复。
> ★ **已修复（`N-068` ① · 2026-10-06）**：四个字段补 `json` tag ⇒ 响应键名＝**`node_id/node_name/role/reason`**
> （与契约逐字一致；前端 `Submit.vue` 本就按 snake_case 取值 ⇒ 此前该处展示实为 undefined 串，
> 修复后与前端依赖对齐）。判据＝`internal/httpapi#TestPreviewUnresolvedRoleKeysN068`（先红后绿）。
> ★ 上段为**修复前历史对照**（保留）。**此后取值一律用 snake_case**。

---

## 2. 第一批 · **今天就能做**（单点接口 · 8 项）

> 前置：**`P3` ＋ `P4`**（不依赖 `P1`）。★ 链路抖了重试即可，不影响结论。

| # | 编号 | 一句话 | 怎么测（我方会给现成请求体） | 判定（记录 A/B/C） | 结果 |
|---|---|---|---|---|---|
| 2.1 | **QV2-08** | `create_link_pc` / `create_link_mobile` 是否**必填** | 建三方定义时**省略**两字段 → 看是否报错；再带上我方发起页链接，从飞书"发起"点入看是否跳到我方页面 | A＝必填 / B＝可选 | ☐ |
| 2.2 | **QV2-A21** | `instance_id` 是否必须 `{app_id}:{biz_no}` | 查本应用是否被**其它系统复用**；若复用，用两应用推同 `biz_no` 实例，看审批中心是否**空白**（撞 ID） | A＝独立应用（裸 `biz_no` 亦可）/ B＝共用（**必须**加前缀） | ☐ |
| 2.3 | **QV2-A22** | `update_time` 递增用**逻辑版本**还是**时间戳** | 连推两次同实例，分别传整数递增／毫秒时间戳；再把**旧值重推**一次看是否被拒 | A＝接受逻辑版本 / B＝接受时间戳 / C＝都接受但须严格递增 | ☐ |
| 2.4 | **QV2-A24** ★ | `external_instances/check` 是否支持**批量** | 一次传**多个** `instance_id`（★ 注意入参字段名是 **`instances[]`**，每项**必须含 `update_time` ＋ `tasks`**，否则报 `99992402`） | A＝支持批量 / B＝仅单实例 | ☐ |
| 2.5 | **QV2-A25** ★ | 超配额是**允许超量**还是报 `99991403`/429 | 打到接近/超过月度上限（或向官方确认），看返回值 | A＝软上限 / B＝硬上限 | ☐ |
| 2.6 | **QV2-A26** | `display_method` 取 `SIDEBAR` 还是 `TRUSTEESHIP` | 推实例时两值各试一次（后者按需补 `trusteeship_urls`），在飞书待办点进去看打开形态 | A＝`SIDEBAR` 可用 / B＝需 `TRUSTEESHIP` / C＝均不可用（退 `BROWSER`） | ☐ |
| 2.7 | **QV2-A27** ★ | 同 `node_id` 多 task **三问** | 同 `node_id` 放 2 个 task（不同审批人）、`type` 分别取会签/或签：①推送是否成功 ②`type` 缺失是否报 400 ③传"或签"后让一人同意，看另一 task 是否被**自动关闭** | A＝可忽略 / B＝必填但不聚合 / **C＝必填且飞书自行聚合** | ☐ |
| 2.8 | **QV2-05** / **QV2-A23** | 三方审批实例**是否仍触发** `approval_instance` 事件 | 用 `external_instances` 推一个实例 → 看是否收到该事件 | A＝**不触发**（预期）/ B＝触发（须幂等去重） | ☐ |

---

## 3. 第二批 · 需链路连续稳定（4 项 · ★ **必须先修 `P1`**）

> 前置：**`P1` ＋ `P2` ＋ `P3` ＋ `P4`**。★ 链路不稳会把结论做成**假阴性**，故必须最后做。

| # | 编号 | 一句话 | 怎么测 | 判定 | 结果 |
|---|---|---|---|---|---|
| 3.1 | **Q17** ★ | `approval_instance` 报文**版本与幂等键**（含通讯录事件） | 触发一条真实审批实例抓原始报文，看是 `header.event_id`（2.0）还是顶层 `uuid`（1.0）；★ **通讯录事件逐个抓**：`contact.user.created_v3` / `contact.user.updated_v3` / `contact.department.*` | A＝全部 2.0 / B＝全部 1.0 / **C＝两类事件信封不同** | ☐ |
| 3.2 | **CHK-1** ★ | 事件订阅**可达性** ＋ 通讯录事件能否走**长连接** | 「事件订阅」页看"推送方式"可选哪几种；实际建一条长连接，确认能收 `contact.*_v3` | A＝长连接可选且能收 / B＝仅 Webhook / C＝文档矛盾须问官方 | ☐ |
| 3.3 | **QV2-A28** ★★ | `REPLACE`/`UPDATE` 重推对飞书侧**已 `APPROVED`** 任务的影响 | ①推一条实例、让审批人在飞书点"同意"（**先不处理回调**）②立即用**落后快照**（task 仍 `PENDING`）分别以 `REPLACE`/`UPDATE` 重推 ③读回 `external_tasks`/审批中心看状态是否被**回退** | A＝不允许回退（落后 `REPLACE` 亦安全）/ **B＝允许回退** | ☐ |
| 3.4 | **QV2-02** | 回调超时 10s 还是 5s | ★ **2026-09-27 已实测实质定论**：我方"落盘即 200"毫秒级 ⇒ 5s/10s 都远超满足；且实测飞书对快捷回调**不重试** | 已在 `docs/09` 内定论 ⇒ **本项只需**：`P1` 修好后**复跑一次**确认仍在 | ☐ |

---

## 4. 第三批 · 用户后台自查（6 项 · ★ 你独立可做，不需开发参与）

| # | 编号 | 去哪儿看 | 记录什么 | 判定 | 结果 |
|---|---|---|---|---|---|
| 4.1 | **CHK-5** | 管理后台 > 费用中心 > 权益数据 | 本月 API 调用上限与已用量 | A＝仍在 100 万 / **B＝已回落至 1 万** | ☐ |
| 4.2 | **Q2** ★ | 同 CHK-5 ＋ **向商务侧确认档位** | ①额度数值 ②档位（免费/商业专业版）③**「免费版不支持行·列权限」是否为真** | A＝专业版且行·列权限可用 / **B＝免费档** | ☐ |
| 4.3 | **CHK-6** | 「事件与回调 > 事件订阅」 | 推送方式是否可选**长连接** | A＝可选 / B＝仅 Webhook | ☐ |
| 4.4 | **CHK-8** | 开放平台应用详情 | 权限清单：审批 API（三方审批）＋ 通讯录 API（部门/员工列表、事件）是否齐 | A＝齐全 / **B＝缺项**（表现为 403 或收不到事件） | ☐ |
| 4.5 | **CHK-9** | 开放平台「能力/应用发布」 | 是否提示需**企业认证/付费档/白名单**才能用三方审批 | ★ 与 4.2、`QV2-09` **同源、"两个场地验证"**（用户侧控制台提示 vs 开发侧接口行为） | ☐ |
| 4.6 | **CHK-7** | 审批后台任一模板的控件配置页 | 可设置的控件类型与默认值入口 | **记录即可**（转向后已作废能力的确认） | ☐ |

---

## 5. 第四批 · 联调第二轮（14 项 · 影响功能完整度）

> 前置：第一批通过。★ 逐项来自 `docs/09 §3`，**操作步骤我方按需逐项展开**。

| # | 编号 | 一句话 | 依赖 |
|---|---|---|---|
| 5.1 | **QV2-03** | 三方审批定义**数量上限**（能否建下 11 类） | `P3` |
| 5.2 | **QV2-10** | 定义能否删除/停用（★ 已实测：**未发现删除接口**；读回须用创建响应的返回值作路径参数，`?approval_code=` 不通） | `P3` |
| 5.3 | **QV2-07** | 同一应用能否**同时用原生＋三方审批** | — |
| 5.4 | **QV2-06** | 审批中心对三方实例是否支持**催办/打印/批量** | — |
| 5.5 | **QV2-18** | 待办 Bot 的**配额/计费** | — |
| 5.6 | **CHK-2** | `message/send` 是否**计入配额** | — |
| 5.7 | **08-C2** | `contact.user.updated_v3` 的 `object.department_ids` 是否为空须用 `old_object` | `P1` |
| 5.8 | **08-C3** | `directory/v1` 的 `page_size` 默认值与是否返 `total` | — |
| 5.9 | **08-C7** | 线上表单「部门控件」报文是否确为 `od-` | `P3` |
| 5.10 | **CHK-3** | 飞书待办**实际展示什么**（`form` 前 3 条）＋ `SIDEBAR` 真实体验 | `P3` |
| 5.11 | **CHK-4** | 发起人「已发起」状态文案；机器人/员工发起时的展示差异 | `P3` |
| 5.12 | **A29** | `t_approval_def` 的 **11 类定义如何装载**（能否清库重建后自动齐备） | — |
| 5.13 | **A31** | `external_tasks` / `check` **报文未实测**（★ `check` 入参已知为 `instances[]`；`external_tasks` 仍待测） | `P3` |
| 5.14 | **V-6** | 实例级 `title` **是否可下发**（当前实现＝不下发，依赖官方回退） | `P3` |

---

## 6. 第五批 · 上线前（1 项）

| # | 编号 | 一句话 | 判定 |
|---|---|---|---|
| 6.1 | **A30** | 存量 `t_instance.instance_code`（旧＝飞书 code / 新＝`{app_id}:{biz_no}`）如何**共存** | A＝共存无冲突 / **B＝冲突或旧行不可见（须迁移）** |

---

## 7. 第六批 · 需写进制度 / 集团确认（8 项 · ★ 非平台，须管理层或集团拍板）

| # | 编号 | 要确认什么 | 判定 |
|---|---|---|---|
| 7.1 | **QV2-11** ★ | 四操作（**转交/加签/回退/撤回**）作为**新增制度条款**；并澄清「制度第五十八条『已执行的操作不撤回』**不含**流程内回退/撤回」 | A＝批准 / B＝调整 |
| 7.2 | **QV2-12** | **撤回后旧单的台账口径**（状态枚举「已撤回」、是否入异常预警） | A＝批准 / B＝调整 |
| 7.3 | **QV2-13** | 加签/转交/回退的**上限数值**（当前默认 3 / 3 / 2） | A＝批准默认 / B＝改值 |
| 7.4 | **QV2-14** | 四操作**是否必填原因** | A＝必填 / B＝选填 |
| 7.5 | **G3** | 「代理人」语义是否与 FlowLong 对齐 ＋ **是否要「委派」**（B 审后回 A、会后回任） | A＝已对齐且不加委派 / **B＝须新增（要扩状态机）** |
| 7.6 | **QV2-16** | **附件是否复用原 ADR-08 主存** | A＝复用 / B＝另定（现行"本地暂存＋待确认"，**不阻塞**） |
| 7.7 | **Q16** | **年度归档保留策略**（年限、存储位置） | A＝给出 / B＝沿用"只导出不删除" |
| 7.8 | **Q19** ★ | **三单匹配容差数值**（交**集团财务**） | A＝给数 / **B＝未给 ⇒ 暂缓"三单匹配结论"自动判定**，以"严格相等"兜底标"待定"；★ **不得自行假定** |

---

## 8. 进度总览（可勾选）

| 批次 | 项数 | 前置 | 已完成 | 状态 |
|---|---|---|---|---|
| §2 第一批（单点接口 · 今天可做） | 8 | `P3` `P4` | 0 | 未开始 |
| §3 第二批（需 `P1`） | 4 | `P1`~`P4` | 0 | 阻塞于 `P1` |
| §4 第三批（用户自查） | 6 | 无 | 0 | **可随时开始** |
| §5 第四批（联调第二轮） | 14 | 第一批 | 0 | 未开始 |
| §6 第五批（上线前） | 1 | — | 0 | 未开始 |
| §7 第六批（集团/制度） | 8 | — | 0 | **可随时开始（并行）** |
| **合计需执行** | **41** | | **0** | |

---

## 9. 与其它文档的关系

| 文档 | 关系 |
|---|---|
| `docs/09-Integration-Verification-Checklist.md` | ★ **权威口径源**（判定标准、来源、影响与处置）—— 本文**不改其结论**，只排序/分工/标注可分步性 |
| `docs/18 §3.2` | 历史未闭合项（本文 §2~§4 与之有交集，以 `docs/09` 为准） |
| `REMAINING.md` | 剩余**开发**需求（★ 已全闭环）；本文是**联调**执行单，两者不同层 |
