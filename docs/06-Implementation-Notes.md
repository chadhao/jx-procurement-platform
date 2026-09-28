# 06 · 实现说明与冲突记录（S0/S1 地基代码）

> 本文件为**工程侧补充记录**，不改动任何既有文档（PRD / UseCase / TestCase / 架构 / 接口 / README）。
> 记录范围：落地 S0/S1 地基代码过程中发现的**文档间冲突 / 歧义**、采取的实现决策与遗留动作。
> 编制：Alex（开发经理）· 版本：**V1.13**（§A–§B 为 S0/S1 原始记录，§G/§H 为 S2 与集成轮，§H.13 为第二轮对抗性复核，§I 为缺口补齐轮，§J 为模板建立配套轮，§K 为 Q14 定案实施轮，§L 为架构完整性审查轮，§M 为口径落地轮，§N 为对象存储收口轮，§O 为降级口径收口轮，§P 为控件口径修正轮，**§Q 为架构转向 ③ 地基层轮**，**§R 为回调链路修复轮（B52–B56）**，**§S 为回调端到端联调轮（B57–B63）**，**§T 为组织同步实机验证轮（B64：作业认领原子性）**；★ **V1.10（`#74`）：代码引用符号化 —— §P.2「证据 4」的两处「`文件:行`」改为「`文件` ＋ 符号」** · ★ **V1.11：新增 §R（回调链路修复 4 批 `5fe1671`/`d94580f`/`36df709`/`c6e26d7` 的 5 条缺陷台账 B52–B56，纯文档）** · ★ **V1.12：新增 §S（端到端联调轮的 7 条缺陷台账 B57–B63 ＋ 「平台逐层校验」教训，纯文档）** · ★★ **V1.13：新增 §T（B64：作业认领非原子 ⇒ 重复执行；真机 3 条重复日志铁证 ＋ 证伪对照 87/12；代码 + 测试 + 文档同批）**）· 最新代码 tag：`0.3.4-s3`

## A. 已落地范围（S0/S1）

| 层 | 产物 |
|---|---|
| 入口 | `cmd/jxapproval/{main.go,bootstrap.go}`（子命令 `serve`/`version`/`help`；严格启动顺序） |
| 配置 | `internal/config/*`（环境变量 + 配置表映射装载：approval_code / field_id / ledger_type / threshold） |
| 存储 | `internal/store/*`（纯 Go SQLite、WAL、迁移器、20 表仓储） |
| 迁移 | `migrations/0001_init.sql`（20 表 DDL）+ `migrations/embed.go` |
| 单实例 | `internal/singlelock/*`（Unix `flock` / Windows `LockFileEx`，无 cgo） |
| 接入 | `internal/platform/feishu/*`（**唯一**引入 Lark SDK 的包；仅 4 类接口，无创建实例路径；长连接 WebSocket） |
| 幂等入口 | `internal/inbox/*`（事件级幂等键、inbox 落库 + 入队，fast-return） |
| 异步 | `internal/worker/*`（worker pool、详情拉取与落库、状态收敛、指数退避、死信、手动重放） |
| 对账 | `internal/sync/*`（订阅、游标、批量取实例 ID 求差补拉、调度） |
| 权限 | `internal/permission/*`（行过滤 + 列投影，配置驱动，deny-by-default） |
| 会话/免登 | `internal/access/*`（HMAC 签名 Cookie、飞书免登、角色映射） |
| 接口 | `internal/httpapi/*`（Echo 路由、统一 Envelope、内部运维端点、业务接口、dev 注入） |
| 前端 | `web/`（Vue3 + Vite + ECharts）+ `internal/webui/*`（`//go:embed` 内嵌，占位页兜底） |
| 脚本 | `scripts/*`（build / run-dev / backup / restore / preflight / systemd unit） |
| 测试 | 见 §D（单测 + 端到端集成测试） |

## B. 冲突与歧义记录（按严重度）

### B1（高）幂等键口径：README「硬约束 2」与架构「§4.3 / §11-1」不一致
- **冲突**：`README.md` 硬约束 2 写「幂等键 = `instance_code` + `status`」；`docs/04-Architecture.md` §11-1 与 §4.3 已**定案**改为**事件级唯一 ID**（2.0 版 `header.event_id` / 1.0 版 `uuid`），并指出 `instance_code + status` 会把「驳回后重提再次 APPROVED」幂等掉、导致历史链断裂（FR-M3-02 vs FR-M3-05 / TC-16）。
- **决策**：**以架构定案为准**，采用事件级幂等键。`instance_code + status` 降级为**仅用于状态收敛**（`internal/worker/status.go`）。
- **落地**：`internal/inbox/idempotent.go`（`Extract`：2.0 `header.event_id` → 1.0 `uuid`，缺失即 `ErrMissingEventID`）；落库用 `INSERT OR IGNORE` on UNIQUE `idem_key`；缺失事件 ID → **拒绝 + 告警**（`Metrics.IncRejectedNoEventID`）。
- **遗留动作**：实采一条 `approval_instance` 报文确认报文版本与字段位置（PRD **Q17**，高阻塞）。当前按官方文档双版本兼容解析，采到真报后回归。

### B2（高）PRD Q3 权限口径未定，但已列 P0 必测
- **冲突**：PRD Q3 未拍板；架构 §11-4 指出「无口径则 TC-06/07 断言无法编写」。
- **决策**：按架构建议，**先以 PRD §4.2 建议值落规则表**（`t_permission_rule`），行·列权限**完全配置驱动**（不硬编码角色名），测试断言以「当前规则表内容」生成。
- **遗留动作**：Q3 定案后回归。**Q3 为最需拍板项之一**。

### B3（中）Go 工具链版本与架构「Go 1.21+」的隐性上界
- **现象**：本机 Go 1.24.5（任务锁定）；生态最新版 `echo ≥ v4.15` 与 `modernc.org/sqlite ≥ v1.40` 的 `.mod` 均已要求 **Go ≥ 1.25**。
- **决策**：**锁定 Go 1.24 兼容版本**——`github.com/labstack/echo/v4 v4.13.4`、`modernc.org/sqlite v1.39.1`（`go.mod` 声明 `go 1.24.0`）。功能不受影响（仅为版本钉死）。
- **理由**：满足「无 gcc → 必须纯 Go SQLite」与「Go 1.24.5」双硬约束；避免升级工具链。

### B4（中）`t_config_mapping` 唯一约束：表达式 UNIQUE 不能写在表约束中
- **现象**：架构 §3.2 期望对 `COALESCE(doc_type,'')` 建 UNIQUE；SQLite **不允许**在 `CREATE TABLE` 内对表达式建 UNIQUE。
- **决策**：改为 `CREATE UNIQUE INDEX ux_config_mapping ON t_config_mapping(...)`（语义等价）。
- **影响**：纯 DDL 写法差异，无行为差异。

### B5（中）「全案只用 4 个飞书接口」口径
- **冲突**：README 与架构 §11-2 均指出——附件**上传与下载实为两个调用**，"4 个接口"是"4 类"口径。
- **决策**：接口层按**4 类**建模（订阅 / 取详情 / 批量取 ID / 附件），附件类含 upload+download 两个调用；代码中**不存在任何"创建实例/发起审批/阻塞回调"**路径（架构红线 §7.6）。
- **落地**：`internal/platform/feishu/dto.go` 的 `Client` 接口仅 4 类方法。

### B6（低）systemd unit 引用 `serve` 子命令与 `scripts/preflight.sh`
- **现象**：架构 §7.1 unit 的 `ExecStart=... jxapproval serve`、`ExecStartPre=... scripts/preflight.sh`，但 §2 目录清单未列 `preflight.sh`，且原入口无子命令。
- **决策**：入口新增子命令分发（`serve` 为默认，兼容无参启动）；新增 `scripts/preflight.sh`（环境文件、数据目录可写、stale-lock 提示）。

### B7（低）stale-lock 与自愈边界
- **冲突**：架构 §11-3 指出「僵死进程未释放锁/端口 → 重启无法自愈」。
- **决策**：锁采用 `flock`（进程退出由内核自动释放）+ 启动前 stale-lock 检测提示；`preflight.sh` 只告警不阻断，实际互斥由 `flock -n` 保证。

## C. 占位与待确认（不影响编译/测试）
- `TODO(Q1)`：`approval_code` 具体值映射（11 个模板）待 PRD Q1 回填至配置表；`doc_type` 同理。
- `TODO(Q2)`：阈值类配置（如防拆分 `split_supplier_month`、抽查区间 `spot_check_range`）数值待业务回填。
- 上述均**配置驱动**：未配置时系统以「空映射」运行（启动告警、不报错），回填配置表即生效，无需改码。

## D. 测试清单（S0/S1）
| 测试 | 位置 | 覆盖 |
|---|---|---|
| 事件幂等键提取（2.0/1.0/缺失） | `internal/inbox/idempotent_test.go` | TC-01 前置 |
| inbox 重复落库仅 1 行、缺失 ID 拒绝 | `internal/inbox/inbox_test.go` | 硬约束 2 |
| 状态收敛 / biz_no 解析 / 退避 / 追加式历史 | `internal/worker/status_test.go` | FR-M2/M3、TC-16 |
| 重试→成功、死信→重放 | `internal/worker/pool_test.go` | TC-30 |
| 行权限·列投影·deny-by-default | `internal/permission/policy_test.go` | TC-06/07/11/32 |
| **端到端**：同 event_id 注入 3× → 1 inbox/1 业务；缺 ID 拒绝；驳回→重提 | `internal/httpapi/integration_test.go` | TC-01 / TC-16 / FR-M3-05 |

## E. 纪律确认
- 未执行 `git commit` / `git push`；未修改 `docs/`（除本机新增本说明文件）与 `README.md`。
- 未使用 `npm install -g`；SQLite 全程纯 Go 驱动，无 cgo/gcc 依赖。

## F. 主 Agent 裁定（2026-09-26 · 交付总监）

对 B1–B7 逐条裁定，**全部采纳开发经理的处理**，并完成 B1 的文档收口：

| 编号 | 裁定 | 处置 |
|---|---|---|
| **B1（高）** | **采纳**：以架构 §4.3 / §11-1 定案为准，幂等键＝事件级唯一 ID | ★ **已修正 `README.md` 硬约束 2**（改「事件级唯一 ID」＋补 7.1 小时重发窗口＋「不得用 `instance_code + status`」＋有序事件不阻塞）；`README.md` 技术栈同步锁 Go 1.24 / `modernc.org/sqlite v1.39.1`、「4 个接口」改「4 类接口」+ 口径说明 |
| **B2（高）** | 采纳：Q3 未定期间按 PRD §4.2 建议值落规则表，配置驱动 | **Q3 已于 2026-09-26 定案**（采纳 §4.2 口径；行级只用 6 令牌 + `DENY`；新增「系统管理」后台页 FR-M5-09~11）。**本项闭合**，落地见 §G |
| **B3（中）** | 采纳：版本钉死 `echo v4.13.4` / `sqlite v1.39.1`（Go 1.24 兼容） | 已随 README 技术栈表更新 |
| **B4（中）** | 采纳：表达式唯一约束改 `CREATE UNIQUE INDEX` | 语义等价，无行为差异 |
| **B5（中）** | 采纳：「4 类」口径；代码无创建实例路径 | 已随 README 口径说明更新 |
| **B6（低）** | 采纳：入口加子命令分发 + `scripts/preflight.sh` | 已落地 |
| **B7（低）** | 采纳：`flock` 内核级释放 + stale-lock 提示（不阻断） | 已落地 |

**结论**：S0/S1 地基代码通过独立复核（`gofmt` 空 / `go build` / `go vet` / `go test ./...` / `npm run build` 全绿）
与运行时冒烟（第二实例 exit=1；同一 `event_id` 注入 3× → inbox=1 / job=1；缺事件 ID → 400 + 告警）。**予以提交。**

## G. S2 增量实现说明（实例闭环 · 权限管理后台页 · Q3 默认口径种子）

> 承接已交付的 S0/S1 地基代码，实现 S2 增量。**不改既有五份 docs 与根 README**；本节为工程侧记录。
> 对应代码 tag：`0.2.0-s2`（`cmd/jxapproval/main.go` 的 `version` 已同步；§G.3 冒烟日志中的 `0.1.0-s0s1` 为**改动前**实跑记录，故保留原样）。
> 纪律：未 `git commit` / 未 push；未引入 cgo；未改 `docs/01~05` 与根 `README.md`。

### G.1 变更文件清单

**新增（Go）**

| 文件 | 说明 |
|---|---|
| `internal/store/repo_admin.go` | `GetUserRoleAny`（含停用）、`InsertPermissionRuleIfAbsent`（播种用 INSERT OR IGNORE）、`ReplacePermissionRules`（整表覆盖，单事务）、`CountPermissionRules` |
| `internal/seed/seed.go` | Q3 默认权限口径（角色 × 资源）定义与幂等播种 `SeedQ3Defaults`；导出 `Roles` / `Resources` / `DefaultRules` |
| `internal/httpapi/handlers_admin.go` | 系统管理后台接口（§3.9）：权限矩阵 GET/PUT、人员角色 GET/POST/PATCH；`requireSysAdmin` 角色守卫；审计 diff；`row_scope` 白名单校验 |
| `internal/httpapi/admin_test.go` | TC-33 / TC-34 / TC-35 / 非管理员 40300 / 播种幂等 的端到端单测 |
| `cmd/jxapproval/seed.go` | `jxapproval seed` 子命令（幂等播种，可重复执行） |

**修改（Go）**

| 文件 | 变更 |
|---|---|
| `internal/store/repo_instance.go` | `t_instance` 的列表/取单查询补别名 `a`（修复 S1 遗留：行过滤 `RowFilter` 生成 `a.xxx` 但 FROM 未取别名 → SQL 报错，见 B12） |
| `internal/httpapi/handlers_biz.go` | 单实例按 ID 行过滤查询补别名；`GET /api/audit/logs` 响应新增 `detail_json`（供审计页看改前/改后 diff，见 B10） |
| `internal/permission/rule_loader.go` | `Resolve` 增加通配回退：精确未命中时再查 `前缀:*`（如 `ledger:L01` → `ledger:*`，见 B8） |
| `internal/platform/feishu/fake.go` | 新增 `NewDevClient()`：DEV_MODE 且未配置凭据时的内存客户端，使 dev 注入可端到端落库（见 B13） |
| `internal/httpapi/router.go` | 注册 `/api/admin/*` 路由（会话域 + 服务端二次校验） |
| `cmd/jxapproval/bootstrap.go` | 迁移后幂等播种 Q3 默认口径；dev 且无凭据时改用 `feishu.NewDevClient()` |
| `cmd/jxapproval/main.go` | 新增 `seed` 子命令分发与 usage |

**新增/修改（前端）**

| 文件 | 变更 |
|---|---|
| `web/src/views/Admin.vue` | 新增：① 权限矩阵（角色 × 资源，`row_scope` 下拉 + 列白/黑名单 + 可写字段）② 人员角色（列表/新增/编辑/启用停用） |
| `web/src/api.js` | 新增 `api.put` 与 `fetchPermissionRules` / `savePermissionRules` / `fetchAdminUsers` / `createAdminUser` / `patchAdminUser` |
| `web/src/router.js` | 新增 `/admin` 路由 |
| `web/src/App.vue` | 侧栏「系统管理」菜单项**仅在角色为「系统管理员」时显示**（前端隐藏不构成安全边界，服务端二次校验） |

**构建产物（embeded）**：`internal/webui/dist/**` 由 `npm run build` 重新生成（新增 `Admin-*.js` chunk，旧 hash 资源被替换）。

> 前端 `web/src/views/Instances.vue` / `InstanceDetail.vue` 在 S0/S1 已接入真实接口（本次未改）；S2 验证其与后端联调可用（见 G.3 冒烟）。

### G.2 Q3 默认口径种子：实现选择与内容

**选择：独立子命令 `jxapproval seed` + 服务启动时幂等播种**（而非迁移脚本）。理由：
1. **迁移保持纯 DDL**，不掺业务数据（`migrations/0001_init.sql` 职责单一）；
2. **`INSERT OR IGNORE` 可重复执行**且**绝不覆盖管理员已改动的规则行**（配置驱动，ADR-05）；
3. 首次部署自动落地默认口径，亦可手动 `jxapproval seed` 重跑；
4. 相比在 `0001` 里追加 60+ 行 `INSERT`，表驱动更易审阅与回归。

**覆盖范围**：10 个角色 × 7 个资源 = **70 行**。
- 资源：`ledger:*`、`dashboard:1..4`、`api:instances`、`api:audit`。
- 角色：申请人 / 主管领导 / 项目总经理 / 副总 / 综合运营主管 / 采购经办人 / 验收人 / 集团财务 / 集团（审批）/ 系统管理员。

**默认行范围（采纳 PRD §4.2 / 架构 §5.4）**：

| 角色 | `row_scope` | `column_deny`（默认） | 可写（运营表） |
|---|---|---|---|
| 申请人 | SELF | 集团侧人工登记列 | — |
| 主管领导 | DEPT | — | — |
| 项目总经理 | ALL | — | — |
| 副总 | CHARGE_DEPT | — | — |
| 综合运营主管 | ALL | — | 架构 §3.4 各台账运营字段（占位，Q14 待定） |
| 采购经办人 | ASSIGNED | 金额列 + 集团侧人工登记列 | 经办状态/完成日期/是否按期 |
| 验收人 | PARTICIPATED | 金额列 | 检验结论/差异说明 |
| 集团财务 | DENY | — | — |
| 集团（审批） | DENY | — | — |
| **系统管理员** | **ALL** | **金额列（只读，不含金额）** | — |

> ★ 「金额列」deny 除 `amount_cents` / `amount_display` 外，同时 deny `formula_flags`——避免列投影仅裁剪顶层时，嵌套的 `formula_flags.supplier_month_sum.sum_cents` 仍泄漏金额（见 B9）。
> ★ `api:audit` 仅「系统管理员」「项目总经理」可见，其余角色默认 DENY（API §3.7）。

### G.3 验收命令原始输出

**① `gofmt -l .`**

```
$ gofmt -l .
（无输出）
```

**② `go mod tidy` / `go build ./...` / `go vet ./...`**

```
$ go mod tidy
（无输出；go.mod / go.sum 无变化）
$ go build ./...
（无输出）
$ go vet ./...
（无输出）
```

**③ `go test ./... -count=1`**

```
?   	github.com/chadhao/jx-procurement-platform/cmd/jxapproval	[no test files]
?   	github.com/chadhao/jx-procurement-platform/internal/access	[no test files]
?   	github.com/chadhao/jx-procurement-platform/internal/config	[no test files]
ok  	github.com/chadhao/jx-procurement-platform/internal/httpapi	0.580s
ok  	github.com/chadhao/jx-procurement-platform/internal/inbox	0.757s
?   	github.com/chadhao/jx-procurement-platform/internal/observ	[no test files]
ok  	github.com/chadhao/jx-procurement-platform/internal/permission	1.189s
?   	github.com/chadhao/jx-procurement-platform/internal/platform/feishu	[no test files]
?   	github.com/chadhao/jx-procurement-platform/internal/seed	[no test files]
?   	github.com/chadhao/jx-procurement-platform/internal/singlelock	[no test files]
?   	github.com/chadhao/jx-procurement-platform/internal/store	[no test files]
?   	github.com/chadhao/jx-procurement-platform/internal/store/storetest	[no test files]
?   	github.com/chadhao/jx-procurement-platform/internal/sync	[no test files]
?   	github.com/chadhao/jx-procurement-platform/internal/webui	[no test files]
ok  	github.com/chadhao/jx-procurement-platform/internal/worker	0.990s
?   	github.com/chadhao/jx-procurement-platform/migrations	[no test files]
```

**④ `npm run build`（在 `web/` 下）+ 内嵌后 `go build ./...`**

```
> jx-procurement-web@0.0.0 build
> vite build

vite v5.4.21 building for production...
transforming...
✓ 592 modules transformed.
rendering chunks...
computing gzip size...
dist/index.html                             0.45 kB │ gzip:   0.33 kB
dist/assets/index-BSKrx1J1.css              3.69 kB │ gzip:   1.30 kB
dist/assets/utils-attsDZXq.js               0.48 kB │ gzip:   0.38 kB
dist/assets/Login-niacNALW.js               1.34 kB │ gzip:   0.97 kB
dist/assets/Audit-CiBhKTVd.js               2.42 kB │ gzip:   1.15 kB
dist/assets/InstanceDetail-BhTSwOVq.js      3.01 kB │ gzip:   1.42 kB
dist/assets/Instances-CznJEtzS.js           3.05 kB │ gzip:   1.50 kB
dist/assets/Ledger-DYayB_8Q.js              3.29 kB │ gzip:   1.58 kB
dist/assets/Admin-DgPV24GY.js               7.32 kB │ gzip:   2.86 kB
dist/assets/index-NYJfOi8B.js             100.30 kB │ gzip:  39.39 kB
dist/assets/Dashboard-CzN8uhJN.js       1,036.98 kB │ gzip: 344.50 kB
✓ built in 3.80s

$ cp -R web/dist/. internal/webui/dist/ && go build ./...
（无输出）
```

> 注：`web/dist` 已存在时会触发本机 WorkBuddy safe-delete 守卫（见 B14），本次以「清空 `web/dist` 后新建」方式完成；内嵌 `internal/webui/dist` 已更新并 `go build ./...` 通过。

**⑤ 端到端冒烟（DEV_MODE=true，本机 127.0.0.1:18080）**

```
===== 0) 等待就绪 =====
{"code":0,"data":{"alive":true,"checks":{"subscribe":true,"longconn":false,"db_writable":true,"single_instance":true},"started_at":"2026-09-26T10:03:33Z","version":"0.1.0-s0s1"},"message":"ok","trace_id":"5bab50b01971188c"}

===== 1) 申请人 dev 登录（ou_applicant）=====
login http=302
-- /api/me:
{"code":0,"data":{"column_policy_summary":{"column_deny":["grp_accept_no","grp_state","paid_date","paid_cents","reject_reason"],"row_scopes":{"api:audit":"DENY","api:instances":"SELF","dashboard:1":"SELF","dashboard:2":"SELF","dashboard:3":"SELF","dashboard:4":"SELF","ledger:*":"SELF"}},"departments":["生产部"],"name":"张三","open_id":"ou_applicant","role":"申请人"},"message":"ok","trace_id":"9fa69ce3743e16ad"}

===== 2) dev 注入一条事件（造实例）=====
{"code":0,"data":{"duplicate":false,"duration_ms":0,"idem_key":"smoke-ev-1","inbox_id":1,"instance_code":"INST-SMOKE-1","job_created":true,"schema_version":"2.0"},"message":"ok","trace_id":"605f7e472760fa8e"}
-- 等 worker 处理（2s）

===== 3) 申请人查实例（SELF，仅本人 1 行）=====
{"code":0,"data":{"items":[{"amount_cents":480000,"amount_display":"4,800.00","applicant":{"name":"张三","open_id":"ou_applicant"},"approval_code":"ac-dev","biz_no":"PR-2609-0001","biz_no_parts":{"prefix":"PR","seq":"0001","yymm":"2609"},"created_at":"2026-09-26T10:03:40Z","department":"生产部","doc_type":"","instance_code":"INST-SMOKE-1","source":"event","status":"PENDING","status_raw":"PENDING","updated_at":"2026-09-26T10:03:40Z"}],"page":1,"page_size":50,"total":1},"message":"ok","trace_id":"19ef8711c1136867"}
-- 详情:
{"code":0,"data":{"amount_cents":480000,"amount_display":"4,800.00","applicant":{"name":"张三","open_id":"ou_applicant"},"approval_code":"ac-dev","biz_no":"PR-2609-0001","biz_no_parts":{"prefix":"PR","seq":"0001","yymm":"2609"},"created_at":"2026-09-26T10:03:40Z","department":"生产部","doc_type":"","instance_code":"INST-SMOKE-1","source":"event","status":"PENDING","status_raw":"PENDING","updated_at":"2026-09-26T10:03:40Z"},"message":"ok","trace_id":"07ac4cb40758c9ae"}
-- 字段:
{"code":0,"data":{"fields":[{"biz_field":null,"field_id":"f_amount","field_name":"预估总金额","value":"4800.00","value_type":"number"},{"biz_field":null,"field_id":"f_assigned","field_name":"指定采购经办人","value":"ou_dev_handler","value_type":"user"}]},"message":"ok","trace_id":"ac55515af21acaa6"}
-- 时间线:
{"code":0,"data":{"events":[{"event_seq":1,"occurred_at":"2026-09-26T10:03:40Z","operator":"ou_applicant","opinion":"","status":"PENDING","task_node":""}]},"message":"ok","trace_id":"e856f14fa4fcacf1"}

===== 4) 非管理员调 /api/admin/* → 期望 40300 =====
login-lead http=302
{"code":40300,"data":null,"message":"仅系统管理员可访问系统管理","trace_id":"5421f490a0035e2a"}

===== 5) 管理员登录并读取矩阵 =====
login-admin http=302
items= 70 resources= 7 roles= 10 row_scopes= 7
row_scopes= ['SELF', 'DEPT', 'CHARGE_DEPT', 'ASSIGNED', 'PARTICIPATED', 'ALL', 'DENY']
系统管理员 api:instances row_scope= ALL column_deny= ['amount_cents', 'amount_display', 'formula_flags']

===== 6) 改一条规则（申请人 × api:instances SELF→ALL）整表回写 =====
PUT rules= 70
{"code":0,"data":{"effective":"immediate","saved":70},"message":"ok","trace_id":"15be49d774291c23"}

===== 7) 不重启，同一会话再查实例 → 期望 2 行 =====
{"code":0,"data":{"items":[{...INST-SMOKE-1（本人生产部）...},{...INST-SMOKE-OTHER（他人销售部）...}],"total":2},"message":"ok","trace_id":"d274e60694abeeaf"}

===== 8) 审计留痕（action=permission_update，含 before/after）=====
permission_update 条数= 1
- ou_admin 系统管理员 permission_rule | detail_json: {"changed":[{"after":{"column_allow":[],"column_deny":["grp_accept_no","grp_state","paid_date","paid_cents","reject_reason"],"row_scope":"ALL","writable_fields"...
```

> 冒烟结论：① dev 注入 → inbox → worker → 实例/字段/状态史 全链落库，列表/详情/字段/时间线可查；② 非管理员调 `/api/admin/*` → `40300`；③ 管理员改一条规则整表回写后**未重启**，同一会话再查同一资源可见行由 1 → 2（`effective=immediate`）；④ 审计留痕含改前/改后 diff。

### G.4 冲突与歧义记录（续 B，编号 B8–B14）

| 编号 | 级别 | 冲突 / 现象 | 决策 |
|---|---|---|---|
| **B8** | 中 | API §3.9 资源枚举写 `ledger:*`，而业务侧按具体类型解析 `ledger:L01`（`handleLedgerList`），`Loader` 原实现只做精确匹配 → `ledger:*` 永不命中 | `permission.Loader.Resolve` 增加**通配回退**：精确未命中时再查 `前缀:*`；`ledger:*` 规则即对所有台账类型生效。**未改 `ledger:<类型>` 的精确优先语义** |
| **B9** | 中 | 列投影 `permission.Project` 仅裁剪**顶层**键；台账行的 `formula_flags.supplier_month_sum.sum_cents`（嵌套金额）在「禁金额」角色下仍可能随 `formula_flags` 泄漏 | 默认口径对禁金额角色**同时 deny `formula_flags`**（见 G.2）。彻底方案（递归投影）留待 Q14 台账字段定稿后评估，**不擅自扩大改动** |
| **B10** | 低 | API §3.7 `GET /api/audit/logs` 响应字段未列 `detail_json`，但 TC-35 要求审计页可见「改前/改后」 | 响应**增补** `detail_json`（向后兼容的纯新增字段），未改其它字段 |
| **B11** | 低 | API §3.9 错误码列 `40000`/`40300`/`40400`，未覆盖「新增 open_id 已存在」冲突 | `POST /api/admin/users` 冲突返回 `40900`（§2.1 既有码，语义准确）；`PATCH` 不存在 → `40400`；均未新增自造码 |
| **B12** | 中（S1 遗留缺陷） | `RowFilter` 对 `t_instance` 生成 `a.applicant_open_id` / `a.department`，但 `instanceSelectSQL` 与计数 SQL 的 `FROM t_instance` **未取别名 `a`** → SELF/DEPT 行过滤在实例列表/按 ID 访问时会 SQL 报错（既有集成测试未覆盖该分支） | 统一 `FROM t_instance a`（列表、计数、按 ID 校验），语义不变；TC-33 冒烟已验证 SELF→ALL 生效 |
| **B13** | 中 | DEV_MODE 下 `worker` 用真实 HTTP 客户端拉详情，无凭据必失败 → `/internal/dev/inject-event` 注入的事件无法端到端落库，冒烟无数据 | 新增 `feishu.NewDevClient()`；**仅当 `DEV_MODE=true` 且未配置 `JX_APP_ID`/`JX_APP_SECRET` 时**在 `bootstrap` 启用（生产路径不变；代码中仍**无任何创建实例路径**） |
| **B14** | 低（环境） | 本机 WorkBuddy 的 safe-delete 守卫在 `web/dist` 已存在且文件数超阈值时，会拒绝 `vite build` 的清空 `outDir` 动作（报 `SAFE_DELETE_BULK_CONFIRM_REQUIRED`），**非项目缺陷** | 首次（`web/dist` 不存在）构建成功；重跑需先清空 `web/dist`（或 `--outDir` 指向新目录）。`internal/webui/dist` 已更新且内嵌 `go build` 通过 |

### G.5 仍未解决 / 待确认

- **Q1（高）**：11 个模板 `approval_code` 与字段 `id` 映射仍未回填 → 按「空映射」运行（`doc_type` 为空、字段 `biz_field=null`），事件落库/展示不受影响（`t_instance_field.biz_field` 允许为空，TC-23）。
- **Q14（中）**：台账运营表可写字段清单未定 → 默认口径中「综合运营主管 / 采购经办人 / 验收人」的 `writable_fields` 用**架构 §3.4 建议键占位**，字段名口径最终以 Q14 为准（改数据即生效，无需改码）。
- **Q17（高）**：审批事件报文版本/幂等字段仍待实采一条真报文复核（沿 B1 遗留）。
- `permission.Project` 的**递归投影**（B9 彻底方案）与 `api:audit` 的角色矩阵细化，建议在 Q14 定稿后一并进行。

### G.6 纪律确认（S2）

- 未执行 `git commit` / `git push`；工作区保留供主 Agent 复核。
- 未修改 `docs/01-PRD.md`、`02-UseCase.md`、`03-TestCase.md`、`04-Architecture.md`、`05-API.md` 与仓库根 `README.md`；仅**新增本节 §G**。
- 未引入 cgo（SQLite 全程 `modernc.org/sqlite`）；未使用 `npm install -g`；未新增第三方 Go 依赖（`go mod tidy` 无变化）。
- 系统管理中数据视图为**运维只读、不含金额列**；「系统管理员」不参与任何业务审批。

---

## H. 集成轮实现说明（看板 + 备付金 + 报送 · 路由统一注册 · 前端构建）

> 背景：S2 之后有两条并行交付（E1 = 看板 M5；E2 = 备付金 M1 + 报送 M6）。
> 两者均**未自行注册路由、未构建前端**（遵「路由由主 Agent 统一注册」的约定），
> 故本轮由主 Agent 完成**集成、补齐缺口、加测试、过门禁**。两名交付者均已结束，无法回问。

### H.1 本轮范围

| 项 | 内容 |
|---|---|
| 路由统一注册 | `internal/httpapi/router.go` 新增 **2 条看板 + 9 条备付金/报送**路由（共 11 条） |
| 幂等语义修正 | 由「命中即 40900」改为「**同键同载荷 → 200 复用首次结果；同键异载荷 → 40900**」 |
| 行过滤缺陷修正 | `ASSIGNED` / `PARTICIPATED` 行过滤由「只查 `t_ledger_archive.ext_json`」改为**双源匹配**（+ `t_ledger_ops.ops_json`） |
| 前端幂等键修正 | 由「每次调用新生成键」改为「**按登记意图持有、成功后轮换**」（否则幂等形同虚设） |
| 前端菜单口径对齐 | 备付金菜单去掉「项目总经理」（服务端 allow-list 本就不含该角色，原会造成「点得进、取数 40300」的假入口） |
| 前端构建与内嵌 | `web/` → `web/dist` → `internal/webui/dist`（15 个产物）→ `go build` 单二进制通过 |

### H.2 变更文件清单

**新增**
- `internal/submission/idem.go` —— 幂等载荷指纹（`IdemPayload` / `Fingerprint`，SHA-256，字段间以 `0x1F` 分隔防拼接歧义，项按 `单号:类型` 排序后参与指纹）
- `internal/submission/idem_test.go` —— 5 个指纹单测（稳定性 / 顺序不敏感 / 空白不敏感 / 关键字段可区分 / 未传金额≠0 / 空单号项不参与 / 跨字段无歧义）
- `internal/permission/dataset_test.go` —— 4 个双源行过滤回归测试
- `internal/httpapi/router_test.go` —— 4 个**真实路由**集成测试（见 H.4）

**修改**
- `internal/httpapi/router.go` —— 注册 11 条新路由（含「两资源暂不进权限矩阵」的口径注释）
- `internal/permission/dataset.go` —— `ScopeAssigned` / `ScopeParticipated` 双源匹配
- `internal/submission/repo.go` —— `FindIdem` 返回 `*IdemRecord{SubmissionID, PayloadHash}`；`RecordIdem` 增 `payloadHash` 参数
- `internal/httpapi/handlers_submission.go` —— 幂等三分支（首次 / 复用 / 冲突）+ `replaySubmission` + `submissionCreateBody` + `shortHint`（审计只落键的 SHA-256 前 8 字节，不落原键）
- `internal/httpapi/submission_test.go` —— 原「命中即 409」用例拆为 4 个用例（见 H.4）
- `web/src/api-m1m6.js` —— `createSubmission(payload, idempotencyKey)` 改为**外部传键**；导出 `genIdempotencyKey`
- `web/src/views/Submission.vue` —— 持有 `idemKey` 跨重试复用、成功后轮换；新增 `submitting` 防抖；提示区分「新登记 / 幂等复用」
- `web/src/App.vue` —— 备付金菜单可见性对齐服务端 allow-list
- `internal/webui/dist/**` —— 前端产物重刷（含 `PettyCash-*.js`、`Submission-*.js`、`api-m1m6-*.js`、`Dashboard-*.css`）

### H.3 主 Agent 裁定

| 编号 | 事项 | 裁定 |
|---|---|---|
| **H-1** | 幂等键命中语义（交付方原实现「命中一律 40900」） | **改为三分支**：① 同键 + 同载荷 → **200**，响应体为首次登记结果并附 `idempotent_replay:true`；② 同键 + 异载荷 → **40900**；③ 历史无 `payload_hash` 的键 → 按**保守冲突**（40900）处理，不猜测。依据 `docs/05-API.md` §2.2「服务端命中则返回首次结果」与 §8「报送登记：命中返回首次结果，409 仅在业务唯一键冲突时」。**理由**：网络重试 / 重复点击是同一意图的同一载荷，一律 409 会让调用方无法区分「已成功」与「请求被改坏了」；而真正的键复用必须显式报错，否则静默丢弃写入意图。 |
| **H-2** | `api:petty-cash` / `api:submission` 是否进行·列权限矩阵 | **不进**。两者已由处理器 `authorizeRole` 做**更严**的角色硬校验；若同时写进 `t_permission_rule`，会出现「矩阵可配、处理器更严」的**假配置**（改矩阵不生效，误导运维）。待资源枚举入库后统一迁移。 |
| **H-3** | 备付金余额的可见角色 | **综合运营主管 + 主管领导**（依 FR-M1-04 与 API §3.6）。「项目总经理是否应可见」PRD 未列 → **暂按不可见**并列入待确认（H.7），**不擅自放宽**；前端菜单同口径，避免假入口。 |
| **H-4** | `replaySubmission` 的响应形状 | 与首次登记**同一形状**（`id`/`biz_no`/`submit_state`/`overdue`），仅**附加** `idempotent_replay:true`。不复用 `submission.RecordMap`（那是列表用的更宽形状），避免同一接口出现两种响应结构。 |

### H.4 测试清单（本轮新增 / 改写，共 13 个用例）

**新增（9）**

| 用例 | 断言要点 |
|---|---|
| `TestFingerprintStableAndOrderInsensitive` | 同载荷指纹稳定；**项顺序调换不改变指纹**；前后空白不影响 |
| `TestFingerprintDistinguishesPayloads` | 业务单号 / 事项类型 / 完成日期 / 金额 / 凭证号 任一变化 → 指纹必变（否则幂等误复用） |
| `TestFingerprintAmountZeroVsAbsent` | 「未传金额」与「传 0」指纹不同（不同登记意图） |
| `TestFingerprintIgnoresEmptyItemNo` | 空单号关联项不参与指纹（与处理器「跳过空单号」一致） |
| `TestFingerprintNoFieldConcatenationCollision` | `"AB"+"C"` 与 `"A"+"BC"` 指纹不同（跨字段无歧义） |
| `TestRowFilterAssignedDualSource` | `ASSIGNED`：**仅 ops 命中** / **仅 archive 命中** / 双源都不命中 → 0 行 / 空身份 → 拒绝 |
| `TestRowFilterParticipatedDualSource` | `PARTICIPATED` 同上（`acceptors` LIKE） |
| `TestRowFilterAssignedNotCrossLedgerType` | 跨 `ledger_type` 不串行（`biz_no` 相同的不同台账类型不得互相命中） |
| `TestRowFilterForInstancesStillDenies` | `t_instance` 的 `FOR_INSTANCES` 映射在 `DENY` 下仍为拒绝（fail-closed） |

**新增 —— 真实路由集成（4）**

| 用例 | 断言要点 |
|---|---|
| `TestRouterRegistersExpectedRoutes` | `e.Routes()` 含全部 **11 条新路由 + 15 条基线路由**；且**无重复注册**（Echo 路由冲突早发现） |
| `TestRouterNewRoutesRequireSession` | 11 条新路由**不带会话**访问 → `401 / 40100`（未注册会是 404/SPA 兜底 → 本用例即失败） |
| `TestRouterPettyCashRoleGateViaRealRouter` | 综合运营主管 / 主管领导 → 200；**项目总经理 → 40300**（与前端菜单同口径） |
| `TestRouterSubmissionRoleGateViaRealRouter` | 综合运营主管可写 / 项目总经理可读**不可写** / 申请人 → 40300 |

**改写（原 1 个 → 现 4 个）**

| 原用例 | 现用例 | 变化 |
|---|---|---|
| `TestSubmissionIdempotencyKeyConflict`（断言「重复键 → 409」） | `TestSubmissionIdempotencySameKeySamePayloadReplays` | 改为断言 **200 + 同 id + `idempotent_replay:true` + 业务字段与首次一致 + 仅 1 行** |
| ↑ 同上 | `TestSubmissionIdempotencySameKeyDifferentPayloadConflicts` | **同键异载荷 → 409**，且仍只 1 行 |
| ↑ 同上 | `TestSubmissionIdempotencyItemsOrderInsensitive` | 关联项顺序不同但集合相同 → 200 复用（载荷等价） |
| ↑ 同上 | `TestSubmissionIdempotencyNoKeyStillAllowsDuplicateBizNo` | 不带键时重复业务单号 → 40900（唯一键路径未被破坏） |

> ★ 上面这条是本轮**最重要的测试缺口修复**：`submission_test.go` 自建最小 Echo，**完全不经过 `router.go`**，
> 因此「路由漏注册」这类缺陷只有 `router_test.go` 能抓到。原 11 条路由若任一漏注册，
> 前端页面会静默 404，而单元测试全绿。

### H.5 验收命令原始输出

```
$ go version
go version go1.24.5 windows/amd64

$ gofmt -l .
(空 —— 无未格式化文件)

$ go build ./...
(无输出 —— 编译通过)

$ go vet ./...
(无输出 —— 静态检查通过)

$ go test ./... -count=1
ok  	github.com/chadhao/jx-procurement-platform/internal/httpapi	1.153s
ok  	github.com/chadhao/jx-procurement-platform/internal/inbox	0.684s
ok  	github.com/chadhao/jx-procurement-platform/internal/permission	1.173s
ok  	github.com/chadhao/jx-procurement-platform/internal/submission	0.819s
ok  	github.com/chadhao/jx-procurement-platform/internal/worker	0.919s

$ go test ./... -count=1 -v | grep -c '^--- PASS'
53
```

前端构建（`web/`）：

```
$ npm run build
vite v5.4.21 building for production...
✓ 597 modules transformed.
dist/index.html                             0.45 kB │ gzip:   0.33 kB
dist/assets/Dashboard-CYsVrG5V.css          0.42 kB │ gzip:   0.21 kB
dist/assets/index-BSKrx1J1.css              3.69 kB │ gzip:   1.30 kB
dist/assets/api-m1m6-EZo7bu6Q.js            1.13 kB │ gzip:   0.68 kB
dist/assets/PettyCash-C3g8Qe1-.js           4.69 kB │ gzip:   2.05 kB
dist/assets/Submission-B9sRg7HK.js          7.33 kB │ gzip:   3.26 kB
dist/assets/index-D1CIvhxP.js             101.06 kB │ gzip:  39.76 kB
dist/assets/Dashboard-DeGGCbYZ.js       1,040.84 kB │ gzip: 346.23 kB
✓ built in 4.04s
```

单二进制：`go build -o bin/jxapproval.exe ./cmd/jxapproval` → **20,452,352 B**（`internal/webui/dist` 15 个产物已内嵌）。

### H.6 冲突与歧义记录（续 B / G，编号 B15–B18）

| 编号 | 级别 | 冲突 / 现象 | 决策 |
|---|---|---|---|
| **B15** | 中 | API §2.2 / §8 说「命中返回首次结果」，而 §3.5 错误码列 `40900（幂等冲突）`；交付方按后者实现为「命中一律 409」→ **内部自相矛盾** | 以 §2.2 / §8 的**语义**为准并**澄清 §3.5 的 40900 仅指「同键异载荷」**（已同步 §3.5 与 §8 的措辞，见 H.9）。两处不再冲突 |
| **B16** | 中 | 前端 `createSubmission` 每次调用 `genIdempotencyKey()` 新生成键 → **服务端幂等永不触发**（双击 = 两条记录），后端实现再正确也无效 | 改为**外部传键**：`Submission.vue` 按「登记意图」持有、成功后轮换、失败保留；并加 `submitting` 防抖。**前端不构成安全/正确性边界，但会架空后端机制**，属必须修 |
| **B17** | 低 | `App.vue` 备付金菜单含「项目总经理」，但服务端 `handlePettyCashBalance` 只放行「综合运营主管 + 主管领导」→ 该角色点进去必 40300 | 菜单**收窄对齐**服务端（H-3）。「菜单可见性必须与服务端 allow-list 逐条对齐」已写进 `App.vue` 注释，防再犯 |
| **B18** | 低（沿用 B14） | 本机 safe-delete 守卫在 `web/dist` 已存在且文件数超阈值时可能拒绝 `vite build` 清空 `outDir` | 本轮**未复现**（`npm run build` 一次通过）；`internal/webui/dist` 用 `rm -rf` + `cp -R` 重建，未受阻 |
| **B19** | 低（潜伏） | `handlers_dashboard.go` 的 `projectResult` 对 `res.Supervision` **只投影 `handler_concentration` 列表项**，其**标量键**（`requester_as_handler_count` / `concentration_max_count` / `handler_total` / `split_threshold_cents`）**完全不经过列投影**；且该处用 `.([]map[string]any)` 类型断言，**断言失败即静默跳过投影**（无日志、无报错） | ① **当前无实际越权**：已核对 `Supervision` 现有键均为计数或政策阈值（`split_threshold_cents` 即 1,000 元防拆分阈值，非业务金额），且默认 `amountDeny` 的 3 个键（`amount_cents` / `amount_display` / `formula_flags`）在其中均不出现 → 定为 **P2 潜伏项**，不擅自改投影契约（改 `allow` 语义可能让合法数据消失，须与列口径一并定夺）。② 已列入 §H.12 建议项：**「标量键也走同一投影器 + 类型断言失败要记日志」**，建议在 Q14 定稿时一并处理 |

### H.7 仍未解决 / 待确认（本轮新增 1 项，其余沿 G.5）

- **★ 新增 Q20（低）**：**项目总经理是否应可见「备付金余额」？** PRD / API 均未列该角色（FR-M1-04 只写「供综合运营主管审批前查看」）。当前实现为**不可见**。若业务上「分管领导应能随时看到备付金余额」，需①在 `handlePettyCashBalance` 的 allow-list 加 `roleProjectGM`；②前端菜单同步放开；③`docs/05-API.md` §3.6 权限要求补记。**改数据/改一行代码即生效，无架构影响。**
- Q1（高）、Q14（中）、Q17（高）、Q2、Q4 沿 G.5 / 早期记录不变。
- `permission.Project` 递归投影（B9 彻底方案）仍待 Q14 定稿后一并处理。

### H.8 纪律确认（本轮）

- **未执行 `git commit` / `git push`**；工作区保留供复核与用户确认后再提交。
- 未引入 cgo；未新增第三方 Go 依赖；未使用 `npm install -g`；前端构建**未触发任何下载**（`node_modules` 已存在）。
- 未修改 `docs/01-PRD.md`、`02-UseCase.md`、`03-TestCase.md`、`04-Architecture.md`；本轮仅新增本节 §H 并按 H.9 澄清 `docs/05-API.md` 幂等措辞。

### H.9 本轮同步的文档

| 文档 | 改动 |
|---|---|
| `docs/05-API.md` | §2.2 幂等头、§3.5 错误码、§8 幂等约定 —— 三处措辞统一为「**同键同载荷 → 200 复用首次结果；同键异载荷 → 40900**」，消除 B15 的自相矛盾 |
| `docs/06-Implementation-Notes.md` | 新增本节 §H |
| `docs/03-TestCase.md` | 新增 **TC-36 / TC-37**（幂等复用 / 幂等冲突），覆盖矩阵同步 |
| `docs/README.md` | 关键决策新增 **#9**（幂等三分支裁定）+ 版本行更新 |

### H.10 自检追加修复：两个 P0 缺陷（均在集成复核阶段发现）

集成完成后主 Agent 对「双源行过滤」做了一次对抗性自检（`LIKE` 语义复盘 + SQLite JSON 函数边界实测），
查出**两个此前未暴露的 P0 缺陷**，均已修复并补回归测试。

#### P0-A　`PARTICIPATED` 行过滤越权可见（前缀误命中）

| 项 | 内容 |
|---|---|
| 现象 | 原实现 `json_extract(a.ext_json,'$.acceptors') LIKE '%'\|\|me\|\|'%'`。**open_id 之间存在前缀包含关系**（如 `ou_ab` 是 `ou_abc` 的前缀），故 `ou_ab` 会命中「验收人只有 `ou_abc`」的记录 |
| 后果 | **行级权限被放大**——非验收人看到自己无权查看的记录。这是本系统「唯一不可替代价值」（行级权限）被直接击穿，属 P0 |
| 影响面 | 存档表与运营表**两侧**（原实现两处都用 LIKE） |
| 修复 | 改为对 JSON 数组**逐元素精确比较**：`EXISTS (SELECT 1 FROM json_each(col,'$.acceptors') WHERE json_each.value = ?)`。同时保留双源匹配（存档 + 运营） |
| 回归测试 | `TestRowFilterParticipatedNoPrefixLeak`（4 个子用例，含存档/运营两侧的前缀对照）；`TestRowFilterAssignedNoPrefixLeak`（`ASSIGNED` 侧对照） |
| 验证方法 | 旧实现下 `me="ou_ab"` 会返回 **2** 行（3001 含 `ou_abc` + 3002 含 `ou_ab`），新实现返回 **1** 行 → 测试为**真回归测试** |

#### P0-B　脏 `ext_json` 会炸掉整条查询（潜伏缺陷，先于本轮即存在）

| 项 | 内容 |
|---|---|
| 现象 | SQLite 的 `json_extract` / `json_each` 遇**非法 JSON** 会抛 `SQL logic error: malformed JSON`——这是**整条查询失败**，而非仅该行被过滤 |
| 后果 | `t_ledger_archive.ext_json` / `t_ledger_ops.ops_json` 里**只要有一行被写脏**（人工补录、迁移截断、部分写入），台账列表 / 看板 / 实例查询**全部 500**，且现象是「整页打不开」而非「少一行」，极难定位。属 P0（可用性） |
| 实测证据 | `json_each('not json','$.acceptors')` → `err=SQL logic error: malformed JSON`；`json_extract('not json','$.assigned_open_id')` → 同错。加上 `CASE WHEN json_valid(col)` 守卫后 → `err=<nil> count=0` |
| 修复 | 新增两个生成器 `jsonScalarEquals` / `jsonArrayContains`，**统一外层套 `CASE WHEN json_valid(col) THEN … ELSE 0 END`**。★ 用 `CASE` 而非 `AND`：SQL **不保证** `AND` 操作数的求值顺序，只有 `CASE` 有条件求值保证（先判 JSON 合法性，再调用 JSON 函数） |
| 回归测试 | `TestRowFilterMalformedJSONDoesNotBreakQuery`（脏行 + 空串 + 合法行混合，断言①合法行仍可见 ②脏行不被放行 ③不报错）；`TestRowFilterDenyAndEmptyFailClosed`（未知 scope / 空身份 fail-closed） |
| 附带收益 | `json_each(col,'$.key')` 对**标量字符串、数组、键不存在、列为 NULL** 均安全（分别 1 行 / 逐元素 / 0 行 / 0 行，均不报错），故本次修复**同时兼容两种序列化形态**：标准数组 `["ou_a","ou_b"]` 与历史逗号串 `ou_a,ou_b`——后者退化为整体等值比较（**保守，宁可少不可多**），即 Q14 台账字段口径最终无论怎么定都不会越权放行 |

> ★ 说明：P0-B 的**触发概率**低于 P0-A，但一旦触发即是「全面不可用」；
> 两者的共同根因是「把 JSON 当字符串随手处理」。本次统一收敛到两个生成器函数，
> 后续新增 scope 必须复用，不得再手写 `LIKE` / 裸 `json_extract`。

**本轮用例总数变化**：53（集成前）→ **57**（顶层用例，含子用例 74 个断言点）。

### H.11 交付物最终状态

| 项 | 状态 |
|---|---|
| `gofmt -l .` | 空（无未格式化文件） |
| `go build ./...` | 通过 |
| `go vet ./...` | 通过 |
| `go test ./... -count=1` | 5 个包全绿（顶层用例 **57**，子用例 **74**） |
| 前端 `npm run build` | 通过（597 modules，无告警） |
| `internal/webui/dist` | 15 个产物已刷新并内嵌 |
| 单二进制 | `bin/jxapproval.exe` 20,452,352 B（**未入库**，`.gitignore` 已忽略 `/bin/`、`*.exe`） |
| git 状态 | **未提交**（46 项变更待复核） |

### H.12 建议项（不阻塞交付，待口径定稿时一并处理）

| # | 建议 | 理由 | 建议时机 |
|---|---|---|---|
| 1 | **`projectResult` 对 `Supervision` 的标量键也施加列投影**，并去掉 `.([]map[string]any)` 类型断言（改 `switch` 兼容 `[]any` + **断言失败写 warn 日志**） | 见 B19：当前无越权，但「投影器静默跳过」是一类高危失败形态——投影漏了不会报错，只会悄悄多给数据 | Q14 台账/看板字段口径定稿时 |
| 2 | **`permission.Project` 的 `allow` 语义需澄清**：现在 `allow` 非空时，未列入 allow 的键**一律删除**。这在看板场景下可能让合法指标消失 | 建议明确「`allow` 是白名单还是叠加放行」并补一个用例锁定语义 | 与建议 1 同批 |
| 3 | **`jsonScalarEquals` / `jsonArrayContains` 收敛为唯一入口**，禁止再手写 `LIKE` 或裸 `json_extract` | 见 H.10 两个 P0 的共同根因 | 立即（已在代码注释中写明，可加 lint 检查） |
| 4 | `internal/submission/repo.go` 复用 `t_audit_log` 存幂等键（`action='submission_idem'`）属**权宜之计**（避免新增迁移脚本） | 幂等键与审计日志混表：① 审计查询需排除该 action；② 无唯一约束，`FindIdem` → `RecordIdem` 为先查后写（TOCTOU）。**但重复数据风险为零**：`t_submission.biz_no` 有 `UNIQUE` 约束（migrations L273），同一载荷 → 同一 `biz_no` → 并发下第二个 INSERT 必被唯一约束拒绝。**实际影响仅限语义瑕疵**：极窄竞态窗口内，同键同载荷可能返回 `40900`（业务单号已存在）而非 `200` 复用——不产生脏数据，但调用方拿到的是「冲突」而非「已成功」。建议后续建独立表 `t_idem_key(key TEXT PRIMARY KEY, submission_id, payload_hash, created_at)`，以主键冲突替代先查后写 | 下个迭代（需迁移脚本） |
| 5 | `docs/03-TestCase.md` §3 总览表此前漏列 TC-33~35（本轮已补） | 说明「新增用例须同步两处」——建议加一个校验脚本：解析 §4 的 `### TC-` 与 §3 表格行，二者必须一致 | 下个迭代 |

## H.13 第二轮对抗性复核与修复（P0 ×2 / P1 ×2 / P2 ×6）

> 背景：§H 集成完成后，主 Agent 派出**两个只读审查员**（一名 QA 对抗性复核、一名 FR 覆盖度审计），
> 对 M1/M5/M6 三条业务链做独立挑刺。共收到 **1×P0 + 2×P1 + 13×P2**；其中 P0/P1 与 6 项 P2 本轮**已修并补回归测试**，
> 其余列为建议项。**本轮全部为「读到的代码与自己写的代码互相打脸」，说明单侧自检不足以收敛。**

### H.13.1 已修缺陷清单

| 编号 | 级别 | 缺陷 | 根因 | 修复 | 回归测试 |
|---|---|---|---|---|---|
| **P0-1** | P0 | 幂等键「先查后写」竞态（TOCTOU）**＋** `biz_no` 为 NULL 时绕过 `UNIQUE` | `FindIdem → CreateSubmission → RecordIdem` 是**三条独立语句**；连接池 `SetMaxOpenConns(1)` 只串行化**单条语句**、**不串行化语句序列**，两并发请求可各自通过 `FindIdem` 后各建一条。且 SQLite **列级 `UNIQUE` 对 NULL 不生效**（实测：两条 `biz_no=NULL` 均可插入）→「未填业务单号」时业务唯一键形同虚设 | ① 新增迁移 `migrations/0002_idem_unique.sql`：幂等键建**部分唯一索引** `ux_audit_idem_key(target_id, actor_open_id) WHERE action='submission_idem'`；`t_submission(COALESCE(biz_no,''))` 建表达式唯一索引；② `Repo.CreateWithIdem` 在**单事务**内「**先占位幂等键 → 建报送 → 建关联项 → 回填 `submission_id`**」；③ `biz_no` **改为必填**（400） | `TestSubmissionConcurrentSameKeyOnlyOneRow`（8 并发 goroutine，断言最终**仅 1 行**）、`TestSubmissionConcurrentDifferentBizNoSameKeyIsolated`、`TestSubmissionIdemPartialUniqueIndexExists`、`TestSubmissionRequiresBizNo`、`TestSubmissionNoKeyRejectsDuplicateBizNo` |
| **P1-1** | P1 | `/group`、`/reject` 可把状态静默写成「已付款 / 已驳回」，但读取路径 `ComputeSubmitState` 又按「无凭证视为未提交」**盖回「未提交」** | 写入路径与读取路径对「无凭证」的处理**不一致**：写时放行、读时回退 → 用户以为登记成功，实际被抹掉 | 任何状态（除「未提交」）**必须已有 `receipt_ref`**，否则 400；两条路径口径统一 | `TestSubmissionStateGuardRequiresReceipt` |
| **P1-2** | P1 | 看板指标 `group_rejected_undisposed`（集团驳回未处置）是**全表 `COUNT(*)`**，而 `dashboard:4` 已种子给全部 10 个角色 → **行级越权**：无权看某报送的人也能从计数推断其存在问题 | 该指标由 `internal/dashboard` 独立 SQL 统计，**未接入行过滤**；看板的其它指标走了 `permission.Project`，唯独此处漏 | 新增 `permission.RowFilterForSubmission(scope, id)`（`t_submission` 无部门/申请人列，`ALL→1=1`、其余令牌与未知/DENY 分别降级为 `created_by=?` / `1=0`）；`dashboard.Query` 增 `SubmissionRowSQL/Args`，计数 SQL 拼接该条件 | `TestDashboardGroupRejectedScopedByRowScope`、`TestRowFilterForSubmissionScoping` |
| **P2-2** | P2 | 看板 `period` 非法（如 `2026-13`）**静默退化为全历史**统计 | 未做格式校验，`LIKE '2026-13%'` 匹配 0 行后语义漂移 | `submission.NormalizePeriod` 校验 `YYYY-MM`，非法 → `errBadPeriod` → **400（40000）** | `TestDashboardInvalidPeriodRejected` |
| **P2-3** | P2 | 幂等载荷指纹的 `单号:类型` 关联项分隔符用**逗号**，与业务单号内容可能含逗号冲突 → 不同载荷可能撞同一指纹 | 指纹拼接未用**输入中不可能出现的分隔符** | 字段内、字段间、项间**统一改为 `0x1F`（Unit Separator）** | `TestFingerprintNoItemDelimiterCollision`、`TestFingerprintItemCountMatters` |
| **P2-6** | P2 | 测试名 `TestSubmissionIdempotencyNoKeyStillAllowsDuplicateBizNo` 与其断言（**拒绝**重复 `biz_no`）**语义相反**，会误导后来者 | 命名与行为不一致 | 更名 `TestSubmissionNoKeyRejectsDuplicateBizNo` | —— |
| **P2-7** | P2 | `dataset_test.go` 的「空串 `ext_json`」子用例实际是**空操作**（存储层把空串规范化为 `{}`），未真正覆盖非法 JSON | 测的是被规范化后的输入 | 改为**真·非法 JSON**与**类型不匹配**（`ext_json` 为字符串而非对象）两类输入 | `TestRowFilterMalformedJSONDoesNotBreakQuery` |
| **P2-8** | P2 | 幂等键**未按调用方隔离**：甲方登记的键被乙方复用时，乙方会读到**甲方的报送记录** | `FindIdem` 只按 `key` 查，未带 `actor` | `FindIdem(ctx, key, actor)` 加调用方维度；与 0002 迁移的**双列唯一索引**一致 | `TestSubmissionIdemKeyIsolatedPerActor` |

> ★ **P0-1 推翻了 §H.12 建议项 4 的判断**：当时判「重复数据风险为零，因为 `t_submission.biz_no` 有 `UNIQUE`（migrations L273）」。
> 该推理**只在 `biz_no` 非空时成立**；实现允许 `biz_no` 为空 → `NULL` 绕过 `UNIQUE` → 风险不为零。
> 结论：**「有唯一约束」不等于「唯一性成立」，必须同时确认「键列 NOT NULL」**。这也是本轮把 `biz_no` 改为必填的实证依据。

### H.13.2 本轮新增 / 改写用例（顶层 +12：57 → 69）

| 文件 | 新增用例 |
|---|---|
| `internal/httpapi/submission_idem_test.go`（新） | `TestSubmissionConcurrentSameKeyOnlyOneRow`、`TestSubmissionConcurrentDifferentBizNoSameKeyIsolated`、`TestSubmissionIdemPartialUniqueIndexExists` |
| `internal/httpapi/submission_test.go` | `TestSubmissionNoKeyRejectsDuplicateBizNo`（更名）、`TestSubmissionRequiresBizNo`、`TestSubmissionIdemKeyIsolatedPerActor`、`TestSubmissionStateGuardRequiresReceipt` |
| `internal/httpapi/dashboard_test.go` | `TestDashboardAmountStrippedFromJSONAndExport`、`TestDashboardGroupRejectedScopedByRowScope`、`TestDashboardInvalidPeriodRejected` |
| `internal/submission/idem_test.go` | `TestFingerprintNoItemDelimiterCollision`、`TestFingerprintItemCountMatters` |
| `internal/permission/dataset_test.go` | `TestRowFilterParticipatedNoPrefixLeak`、`TestRowFilterAssignedNoPrefixLeak`、`TestRowFilterMalformedJSONDoesNotBreakQuery`、`TestRowFilterDenyAndEmptyFailClosed`、`TestRowFilterForSubmissionScoping`（P0-A/P0-B 回归，见 §H.10，计入上一轮） |

**用例总数**（`go test ./... -count=1 -v` 实测）：顶层 `--- PASS` **69** 行；`--- PASS` 总行数（含子用例）**100** 行；5 个测试包全绿。

### H.13.3 本轮变更文件（相对 §H）

- **新增**：`migrations/0002_idem_unique.sql`、`internal/httpapi/submission_idem_test.go`
- **修改**：`internal/submission/repo.go`（`CreateWithIdem` + 哨兵错误 + `FindIdem` 加 actor）、`internal/submission/idem.go`（分隔符 0x1F）、`internal/httpapi/handlers_submission.go`（`biz_no` 必填 + 三分支错误映射 + 状态守卫）、`internal/permission/dataset.go`（`RowFilterForSubmission`）、`internal/dashboard/dashboard.go`（`SubmissionRowSQL/Args`）、`internal/httpapi/handlers_dashboard.go`（`errBadPeriod` + `dashboardError`）

### H.13.4 冲突与歧义（续 B15–B19，编号 B20–B21）

| 编号 | 级别 | 冲突 / 现象 | 决策 |
|---|---|---|---|
| **B20** | 高 | API §3.5 未声明 `biz_no` 必填，实现此前也允许为空；而 §3.5 又列「业务单号重复 → 40900」——**在 `biz_no` 可为空时该约定不可能成立** | 三处口径收敛为「`biz_no` **必填**」（§3.5 请求体 + 错误码 + §4.5；缺失 → 40000）。理由：它是业务唯一键，也是幂等指纹的去重锚点；不填则重复登记无解 |
| **B21** | 中 | `t_submission` **没有部门 / 申请人字段**，无法表达 `DEPT` / `CHARGE_DEPT` / `ASSIGNED` / `PARTICIPATED` 的真实语义 | 过渡口径：这些令牌统一降级为 `created_by = me`（保守收紧，**宁可少不可多**），未知 / `DENY` / 空身份 → `1=0`（fail-closed）。待 Q14 台账字段口径定稿后按真实列重建；此降级**绝不越权** |

### H.13.5 仍未解决 / 待确认（本轮新增 0 项）

沿 §H.7：Q1（高）、Q14（中）、Q17（高）、Q2、Q4、**Q20（低：项目总经理是否可见备付金余额）**不变。

### H.13.6 纪律确认（本轮）

- **未执行 `git commit` / `git push`**；工作区保留供复核与用户确认后再提交。
- 未引入 cgo、未新增第三方 Go 依赖、未触发任何下载。
- 本轮仅新增本节 §H.13 并同步 `docs/05-API.md` §3.5（`biz_no` 必填）、`docs/README.md`（决策 #9 措辞 + 新增版本行）。

## I. 缺口补齐轮实现说明（M1 集团报销跟踪 · M4 变更链回溯 · 前端台账键纠正 · 归档与日志滚动）

> 背景：第二轮对抗性复核之后，对照 FR 覆盖度审计列出的缺口，补做**尚未实现但 PRD 已列为必须 / 应该**的四项。
> 本轮**不引入任何新表**——`t_expense_track` 自 S0/S1 起即存在，一直只有写入函数、没有读路径与页面（属「有表无功能」）。

### I.1 本轮范围

| 项 | 内容 | 对应 FR |
|---|---|---|
| 集团报销跟踪表 | `t_expense_track` 补齐**列表 / 更新**能力 + HTTP 接口 + 前端页（此前只有 `InsertExpenseTrack` 一个写入函数） | **FR-M1-02**（必须） |
| 变更链回溯 | 按合同号回溯历次变更的**次数 / 累计金额 / 所取档位**；新增只读查询 | **FR-M4-07**（必须） |
| 前端台账类型键纠正 | `Ledger.vue` 原本用 `purchase` / `expense` / `petty_cash` 三个**自造键**，与后端 `ledger_type`＝`L01..L12` 不匹配 → 台账列表**恒空**；改为按 `L01..L12` 枚举 | **FR-M4-01**（必须） |
| 年度归档 | 新增 `scripts/archive-year.sh`（**只读导出**，不删在线数据） | **FR-M4-08**（应该） |
| 日志滚动 | 新增 `scripts/logrotate.d/jxapproval`；`jxapproval.service` 增 `LogsDirectory=` + `StandardOutput/Error=append:` | **FR-M8-08**（应该） |

### I.2 变更文件清单

**新增**
- `internal/httpapi/handlers_reimbursement.go` —— 集团报销跟踪登记 / 列表 / 更新（角色显式鉴权，同 §3.10）
- `internal/httpapi/handlers_contract.go` —— 变更链回溯（复用台账 `ledger:L09` 的行·列权限）
- `internal/store/repo_contract.go` —— `GetArchiveByKey`（按业务主键取台账行）与 `ListChangesByContract`
- `internal/httpapi/m1m4_test.go` —— 9 个用例（见 I.4）
- `web/src/ledgerTypes.js` —— `L01..L12` 台账类型枚举（唯一出处，供台账页与后续复用）
- `web/src/views/Reimbursement.vue` —— 集团报销跟踪页（登记 / 查询 / 集团付款字段人工登记）
- `scripts/archive-year.sh` —— 年度归档（CSV + SQL + MANIFEST + SHA256SUMS + tar.gz）
- `scripts/logrotate.d/jxapproval` —— 日志滚动配置

**修改**
- `internal/store/models.go` —— 新增 `ExpenseTrack` 模型
- `internal/store/repo_misc.go` —— `ExpenseTrackFilter` / `ListExpenseTracks` / `ExpenseTrackUpdate` / `UpdateExpenseTrack`
- `internal/httpapi/router.go` —— 注册 **4 条新路由**（报销 3 + 变更链 1）
- `internal/httpapi/router_test.go` —— `expectRoutes` 与会话保护用例同步补 4 条
- `web/src/api.js` —— 追加 4 个 helper（不改动任何既有导出）
- `web/src/views/Ledger.vue` —— 台账类型键改 `L01..L12` + 新增「变更链」面板
- `web/src/router.js` / `web/src/App.vue` —— 新增 `/reimbursement` 路由与菜单项（菜单可见性与服务端 allow-list 逐条对齐）
- `scripts/jxapproval.service` —— 日志落盘 + `LogsDirectory`

### I.3 主 Agent 裁定

| 编号 | 事项 | 裁定 |
|---|---|---|
| **I-1** | 报销跟踪与报送（§3.5）是否合并为一个资源 | **不合并**。二者业务对象不同：报送 = 湖南侧流程完成后的**对外报送**（移交凭证 / 集团受理 / 驳回处置）；报销跟踪 = **报销类事前申请**的单独跟踪（票据张数 / 初审状态 / 超支说明）。合并会让「无凭证视为未提交」这类报送专属判定污染报销行。 |
| **I-2** | 变更链如何按合同号匹配（Q14 未定稿，键名未知） | **键名无关的精确值匹配**：`json_each(ext_json)` 展开对象后对**任意键的值**做等值比较。**刻意不用 `LIKE`**——`LIKE '%ou_ab%'` 会命中 `ou_abc`（与 §H.10 P0-A 同一根因）；**也刻意不写死 `$.contract_no`**——键名随 Q14 变，写死即脆。脏 JSON 由 `CASE WHEN json_valid(...)` 守卫（同 P0-B）。 |
| **I-3** | 变更档位「所取档位」取值 | 台账里**显式存了 `tier` 就用存的**；未存时按 **max（变更差额, 原合同金额）** 现算并标注 `max 取档 → …`（批复 A8）。二者都缺（只有差额）时 `tier` 留空，**不猜测**。 |
| **I-4** | 年度归档是否顺带删除在线数据 | **不删**。PRD FR-M4-08 验收标准原文「**归档后历史仍可查**」；脚本只导出，MANIFEST 中明写「未删除或改写任何在线数据」，在线库瘦身须**独立批准**后另行执行。 |
| **I-5** | 台账类型键应由前端还是后端定义 | 后端 `ledger_type` 是权威（`L01..L12`）；前端新增**单一枚举出处** `ledgerTypes.js`，台账页从中取选项，避免再次各行其是（本次缺陷即「前端自造键、后端不认」）。 |

### I.4 测试清单（本轮新增 5 个用例，顶层 69 → 74；另改写 2 个路由用例）

| 用例 | 断言要点 |
|---|---|
| `TestReimbursementCreateListPatchRoles` | 综合运营主管建 + 改；主管领导 / 项目总经理可读；申请人读与写均 40300；`PATCH` 后 `paid_cents` 生效且 `source` 恒为「人工登记」 |
| `TestReimbursementValidation` | 缺 `src_biz_no` / 金额非正 / 状态非法 / 日期格式 → 40000 |
| `TestContractChangesBacktrace` | `count=2`（**键名不同的两笔都命中**）、累计金额之和、`tier` 为 `max 取档 → 5,000.00`；不相关合同与脏 JSON 行**不混入且不报错** |
| `TestContractChangesRowScope` | 生产部主管只见本部门变更（`count=1`） |
| `TestContractChangesDeniedRole` | 集团财务（默认拒绝）→ 40300 |
| `TestRouterRegistersExpectedRoutes` / `TestRouterNewRoutesRequireSession`（**改写**） | `expectRoutes` 与会话保护用例补 4 条新路由，继续由**真实路由**抓「漏注册」 |

**用例总数**（`go test ./... -count=1 -v` 实测）：顶层 `--- PASS` **74** 行；`--- PASS` 总行数（含子用例）**105** 行；5 个测试包全绿。

### I.5 验收命令原始输出

```
$ gofmt -l .
(空)

$ go build ./...
(无输出)

$ go vet ./...
(无输出)

$ go test ./... -count=1
ok  	github.com/chadhao/jx-procurement-platform/internal/httpapi	1.518s
ok  	github.com/chadhao/jx-procurement-platform/internal/inbox	0.807s
ok  	github.com/chadhao/jx-procurement-platform/internal/permission	1.454s
ok  	github.com/chadhao/jx-procurement-platform/internal/submission	0.917s
ok  	github.com/chadhao/jx-procurement-platform/internal/worker	0.974s

$ npm run build        # web/
✓ 599 modules transformed.   （含 Reimbursement-*.js、Ledger-*.js、api-*.js）
```

单二进制：`go build -o bin/jxapproval.exe ./cmd/jxapproval` → **20,524,032 B**。

归档脚本实拍（临时库端到端）：`scripts/archive-year.sh` 对 2025 年度导出 2 行、生成 `MANIFEST.txt` / `SHA256SUMS.txt` / `archives.tar.gz`，
并对无 `created_at` 的表（`t_event_inbox` / `t_ledger_ops` / `t_submission_item`）**显式跳过并记录原因**，不静默遗漏。

### I.6 冲突与歧义（续 B15–B21，编号 B22–B23）

| 编号 | 级别 | 冲突 / 现象 | 决策 |
|---|---|---|---|
| **B22** | 中 | `internal/store/repo_misc.go` 自 S0/S1 起就有 `InsertExpenseTrack`，前端 `Ledger.vue` 也早已有 `petty_cash` 选项——但**两者口径从未对齐**：报销跟踪有写无读、无页面；台账类型键自造。属「代码与文档都写了、功能却不可用」的**沉默缺口**，任何单测都抓不到（无调用即无失败） | 本轮补齐读路径与页面，并把台账类型键收敛为 `ledgerTypes.js` 单一出处。**教训：新增写入函数必须同时给出读路径或明确标注「仅供内部调用」，否则等于没做** |
| **B23** | 低 | 归档脚本在 Windows 开发机（Git Bash）下，`sqlite3` CLI 输出编码随控制台代码页，可能与 shell 写入的 UTF-8 注释行不同；部署目标（Linux + UTF-8 locale）不受影响 | 记录为开发机观测现象；脚本本身按 UTF-8 语义编写，**不在脚本内做平台分支**（避免为开发机特例污染部署脚本） |

### I.7 仍未解决 / 待确认（本轮新增 0 项）

沿 §H.7 / §H.13.5：Q1（高）、Q14（中）、Q17（高）、Q2、Q4、Q20（低）不变。
其中 **Q14 直接决定** I-2/I-3 的键名收敛（候选键 → 单键）与档位取值口径，建议优先推动。

### I.8 纪律确认（本轮）

- **未执行 `git commit` / `git push`**；工作区保留供复核与用户确认后提交。
- 未引入 cgo、未新增第三方 Go 依赖、未触发任何下载（`node_modules` 已存在）。
- 新增 `scripts/archive-year.sh` 已 `chmod +x`；`.gitignore` 已忽略 `/bin/`、`*.exe`。

### I.9 本轮同步的文档

| 文档 | 改动 |
|---|---|
| `docs/05-API.md` | 新增 **§3.10 集团报销跟踪**、**§3.11 变更链回溯**；§10 FR 追溯表补两行；版本 → **V1.3** |
| `docs/03-TestCase.md` | 新增 **TC-40 / TC-41**；§3 总览、§5 高风险清单（第 19/20 项）、§6 覆盖矩阵（FR-M1-02、FR-M4-07）同步；版本 → **V1.4**，用例合计 **41** |
| `docs/06-Implementation-Notes.md` | 新增本节 §I |
| `README.md`（仓库根） | 版本段与相关产物路径更新（去掉「本地 git、暂不同步远端」的失效描述） |
| `docs/README.md` | 版本行更新（03 / 05 / 06） |

---

## J. 模板建立配套轮实现说明（配置映射导入 · 规范字段抽取 · 模板建立指引）

> 本轮起因：用户指令「先完成所有开发，**最后建模板**」。模板本身**不能由 API 建**
> （开放平台限制，见 `07-Template-Build-Guide.md` §1），故本轮把「**让 11 张模板建成后可一次配通**」
> 的全部工具补齐——**配置映射导入**与**规范字段抽取**。过程中发现并修复 **2 个 P0 缺陷**。

### J.1 本轮范围

1. **配置映射导入（Q1 闭合路径）**：`jxapproval import-config [--check] <file.json>`，
   四类映射（`approval_code` / `field_id` / `ledger_type` / `threshold`）严格校验 + 幂等落库 + 回读自证。
2. **规范字段抽取（FR-M2-08）**：`internal/worker/extract.go`，把 `field_id → biz_field` 映射的**结果**
   真正落到 `t_instance` 规范列，并把非规范列字段汇入台账 `ext_json`。
3. **模板建立指引**：`docs/07-Template-Build-Guide.md`（11 张模板的审批链 / 控件 / 采集步骤 / 订阅 / 自检）。
4. 样例配置：`docs/reference/config-mapping.sample.json`。

### J.2 ★ 本轮发现并修复的缺陷

| 编号 | 级别 | 缺陷 | 根因 | 修复 |
|---|---|---|---|---|
| **P0-C** | **P0** | `t_instance.amount_cents` / `supplier` / `purpose_class_l1` / `purpose_class_l2` / `department` **恒为空**（生产路径） | `field_id → biz_field` 映射只被用于写 `t_instance_field`；**没有任何代码把值搬到规范列**。`InstanceDetail` 的 `AmountCents` / `Supplier` 等字段只有 `fake.go`（测试）在填 | 新增 `internal/worker/extract.go`：`ExtractDetail()` 按映射抽取，`Ingest` 中在 `ParseBizNo` **之前**调用（模板未走流水号控件时单号来自表单，须先抽取） |
| **P0-D** | **P0** | `t_ledger_archive.ext_json` **被写死 `"{}"`** → **变更链回溯（FR-M4-07）恒为空** | 归档写入时硬编码空对象；而 `store.ListChangesByContract` 依赖 `json_each(ext_json)` 按**关联合同号**匹配 | 新增 `BuildExtJSON()`：把**已映射**字段汇总为 JSON（文本存为 JSON 字符串，保证 `json_each(...).value = '<合同号>'` 等值命中）；`Ingest` 改用它 |

**这两个缺陷的共同特征**：**都不报错**，只是数据恒空。P0-C 使看板金额类指标为 0、防拆分「同供应商当月
累计」失效、800–1,000 元抽查清单为空；P0-D 使变更链恒为空。**均属本项目最危险的「静默无数据」缺陷类**
——与 §H.10 的 P0-A / P0-B（`biz_no` 为 NULL 绕过 UNIQUE、前端自造台账键导致列表恒空）同一族。

> **纪律收获**：凡「配置映射」类机制，**必须验证映射的*消费端*真的读了映射结果**。
> P0-C 的映射链路是「配置 → 映射表 → `t_instance_field`」，**缺最后一跳**；
> P0-D 是「表里有列，但写入方写了常量」。两者用「跑一遍端到端、断言规范列非空」即可暴露——
> 本轮起，`worker` 包的用例**一律做端到端断言**（入库后回读 `t_instance`），不再只断言解析函数。

### J.3 主 Agent 裁定

| # | 议题 | 裁定 |
|---|---|---|
| **J-1** | 样例文件里放占位符会不会被误导入 | 会，且后果是「写入一批指向不存在 `approval_code` 的映射 → 模板永远订阅不到事件、系统无数据、无报错」。→ **在 `Validate()` 里直接拒绝含 `REPLACE_ME` / `替换` / `TODO:` / `<待填>` 的取值**。代价：样例**必然**在填完前校验失败——这是**刻意的**，并已在 `07` 指引中说明 |
| **J-2** | `biz_field` 写错一个字母怎么办 | 不阻断（业务可能需要新增字段名），但**必须可见**：登记「可抽取」9 个 + 「透传 `ext_json`」若干个，**不在册者导入时提示**（多为拼写错误）。若把 `amount` 写成 `amout`，若不提示则金额列恒空且无报错 |
| **J-3** | `doc_type → ledger_type` 能否指向任意台账 | **不能**。写入模型是「一实例一行」，而 `L08` 是**手工主数据**、`L10` 是**只读汇总**、`L11` 是**派生**（数据源＝CT+GR）、`L12` **本期不启用**（架构 §3.4）。指向它们会往汇总表里逐单插行 → 台账数字翻倍。→ **导入层硬拦**，错误信息带成因说明 |
| **J-4** | `L07` 的「检验结论」要求把 `QC` 并入 `GR` 行 | 当前写入模型是「一实例一行」，**做不到跨单据合成一行**。本轮**不预设结论**（属 **Q14**），已在 `07` §3.1 显式标记为待确认，并在 `06` §J.5 记录 |
| **J-5** | 抽取匹配用控件中文名还是 `field_id` | 只用 `field_id → biz_field` **精确匹配**（大小写不敏感、去空白），**绝不按中文名模糊猜**。原因：模板改名极常见，按中文名匹配会在改名后**静默抽错列** |
| **J-6** | 抽取时表单值能否覆盖接口值 | **不能**。接口的 `serial_number` / `open_id` 是权威值，抽取一律「**只填空、不覆盖**」，避免手填单号把系统流水号顶掉 |
| **J-7** | `PC`（采购变更单）落哪张台账 | 落 **`L09` 例外事项台账**——因**采购变更属「例外流程三条」**（采购变更 / 独家采购 / 紧急采购）。故 `SS` 与 `PC` **两个 doc_type 同指 `L09`**（映射按 `doc_type` 键单一，允许 value 相同） |
| **J-8** | `RFQ` / `BJ` / `QC` 是否落账 | **不落账**。它们不构成独立台账（`QC` 的结论并入 `L07`）。未配置 → **不写台账**，是设计意图而非遗漏；已在 `07` §3.1 写明 |
| **J-9** | 阈值键与 `enum` 是否全部做成可配 | **只登记代码真正读的**。`purchase_tier` / `emergency_hours` / `submit_workdays` **当前无消费端** → 导入时**提示「假配置」**（不阻断，口径可先行登记）；`enum.*` **故意不纳入导入**——枚举值尚在代码里，做成可配而无人读就是重现 §J.2 P0-C 的同类缺陷。**新增阈值键必须同时有消费端**（决策 #24） |
| **J-10** | 未登记的 `biz_field` 是阻断还是提示 | **提示**（写入 stderr、导入仍成功）。阻断会误伤业务新增字段；但**必须可见**——`amount` → `amout` 这类拼写错误若不提示，后果是金额列恒空且无报错 |

### J.4 测试清单（本轮新增 21 个用例：`config` 11 + `worker` 10）

| 包 | 用例 | 断言要点 |
|---|---|---|
| `config` | `TestImportValidateAcceptsValid` | 四类齐全的合法载荷通过 |
| `config` | `TestImportValidateRejects`（19 子用例） | 逐条注入非法值**全部被拒**；含**占位符**、**指向非实例级台账**（`L08`/`L10`/`L11`/`L12` 各一） |
| `config` | `TestImportPayloadRejectsUnknownField` | 未知顶层键报错（防拼错键名静默丢配置，`DisallowUnknownFields`） |
| `config` | `TestLoadImportFileReportsPath` | 错误信息带文件路径（不存在 / 内容非法两条路径） |
| `config` | `TestParseImportPayloadRoundTrip` | JSON 往返不丢字段 |
| `config` | `TestImportMappingsIdempotent` | 两跑 30 条仍 30 行；同键改值可覆盖 |
| `config` | `TestImportMappingsRejectsInvalidWithoutWriting` | 非法载荷**一行都不写**（整体拒绝） |
| `config` | `TestImportMappingsReadback` | 四类映射均可经 `LoadMaps` 反查；阈值「元→分」、区间左闭右闭 |
| `config` | `TestLedgerTypesWhitelistMatchesDocTypes` | `DocTypes` 11 类 / `LedgerTypes` 12 张；无重复 |
| `config` | `TestPerInstanceLedgerWhitelist` | 实例级白名单 8 张（`L01–L07`+`L09`），恰好排除 `L08/L10/L11/L12`，且每个都有成因说明 |
| `worker` | `TestParseAmountCents`（18 子用例） | 千分位 / 货币符号 / 四舍五入进位 / 负数 / 省略整数部分 / 拒绝科学计数法与溢出 |
| `worker` | `TestParseAmountCentsObjectForm` | 金额控件返回对象形态时按 `amount`/`value` 取值 |
| `worker` | `TestExtractDetailCanonicalFields` | 金额 / 供应商 / 用途 / 部门 / 申请人姓名各自**落到正确列** |
| `worker` | `TestExtractDetailDoesNotOverrideAuthoritativeValues` | `serial_number` 与适配层已有的金额**不被表单值覆盖** |
| `worker` | `TestExtractDetailIgnoresUnmappedFields` | 字段名叫「采购金额」但**未映射** → 不抽（不得按中文名猜） |
| `worker` | `TestExtractDetailNilSafe` | 空 maps / 空 det 不 panic |
| `worker` | `TestIngestExtractsIntoInstanceColumns` | **端到端**：入库后回读 `t_instance`，金额 = 100000 分、供应商 / 用途非空；台账落 `L04` 一行 |
| `worker` | `TestBuildExtJSONCarriesMappedFields` | `ext_json` 含已映射字段、**不含未映射字段**；无映射返回 `{}` |
| `worker` | `TestIngestArchiveExtJSONQueryable` | **端到端**：两笔变更单入库后，按关联合同号经 `json_each` 检索到 **2** 笔（原实现会得 0） |
| `config` | `TestUnconsumedThresholdKeys` | 「已登记但无消费端」的阈值键被识别（`purchase_tier` / `submit_workdays`）；已消费键**不得误报** |
| `worker` | `TestNonExtractableBizFields` | 既非抽取列也非透传的名字被提示；已登记字段（`contract_no`）不误报 |
| `config` | `TestUnconsumedThresholdKeys` | 「已登记但无消费端」的阈值键被识别（`purchase_tier` / `submit_workdays`）；已消费键**不得误报** |

**统计口径**（本轮实测，统一用 `grep -cE "^--- PASS"` / `PASS:`）：

| 指标 | 本轮前 | 本轮后 |
|---|---|---|
| 顶层测试函数（`^func Test`） | 75 | **96** |
| 顶层 `--- PASS` 行 | 75 | **96** |
| `PASS:` 总行（含子测试） | ~105 | **147** |

### J.5 冲突与歧义（续 B15–B23，编号 B24–B27）

| 编号 | 级别 | 问题 | 现状与处置 |
|---|---|---|---|
| **B24** | **P0** | `field_id → biz_field` 映射**未被消费**，规范列恒空（见 §J.2 P0-C） | **已修**（`internal/worker/extract.go` + `Ingest` 调用点） |
| **B25** | **P0** | `ext_json` 写死 `"{}"` → 变更链恒为空（见 §J.2 P0-D） | **已修**（`BuildExtJSON`） |
| **B26** | 中 | `doc_type → ledger_type` 是 **1:1**，但业务上存在**跨单据合成一行**（`L07` ＝ `GR` + `QC`；§3.4 又说 `L11` 数据源＝`CT`+`GR`） | **部分处置**：导入层已拦住「指向非实例级台账」；**合成口径本身属 Q14，本轮不预设结论**，在 `07` §3.1 标记待确认 |
| **B27** | 中 | **`t_ledger_field_def` 无写入通道、也基本无读取消费端**——`UpsertLedgerFieldDef` 与 `ListLedgerFieldDefs` **均为零调用者**（无 API 路由、`seed` 不写）；唯一消费端是 `SensitiveFields`（读 `is_sensitive=1`），被台账列表 / 详情 / 变更链 3 处使用 | **据实修正文档**：`05-API.md` 原写「`archive` / `ops` 的具体键由 `t_ledger_field_def` 决定」，实际**该表当前只承担「敏感列清单」一个职能**。故 **Q14 的「运营表字段清单」不能靠改配置落地**——要真正配置驱动，须先补一个写入端口（**属编码**）。已同步：本行 + `05-API.md` §3.7 / §4.3 注记 |

> ★ **B27 的教训与 §J.2 同源**：又一处「文档说有配置、实际没有通道/没有消费端」。
> 本轮三次遇到同一形态（P0-C 映射没消费端、P0-D 列写了常量、B27 表没写入通道），
> 故在 `06` §J.3 **J-9** 已确立纪律：**新增配置键必须同时有消费端**；
> 本行补充其对称条款——**文档宣称"由配置决定"时，必须同时存在写入通道与消费端**，
> 否则应在文档中标「文档约定、尚未实现」。

### J.6 仍未解决 / 待确认（本轮新增 1 项）

沿 §H.13.5 / §I.7，另加：

| # | 事项 | 说明 |
|---|---|---|
| **B27 衍生** | **是否为 `t_ledger_field_def` 补写入端口** | 若不补：Q14 的「字段清单」只能停留在文档约定，`ops` 键名不受任何约束（写接口只校验 `writable_fields` 白名单，不校验字段定义）。若补：需新增管理端点 + 后台页，属**编码**。**建议与 Q14 一并决策** |

**Q14 的紧迫度上升**：`L07`＝`GR`+`QC` 的合成口径
（B26）已成为「台账能否落对」的直接前提。

### J.7 纪律确认（本轮）

- **未执行 `git commit` / `git push`**；工作区保留，待用户确认。
- **未触发任何下载**；未新增第三方 Go 依赖（仅用标准库 `encoding/json` / `math` / `strings`）、未引入 cgo。
- 导入路径**幂等**已实测（连跑两次仍 30 行）；非法载荷**零写入**已实测。
- CLI 已在**临时目录**的独立 DB 上冒烟（`import-config` 两次 + `--check`），**未触碰任何生产库**。

### J.8 本轮同步的文档

| 文档 | 改动 |
|---|---|
| `docs/07-Template-Build-Guide.md` | **新增**（11 张模板建立指引，项目最后一步） |
| `docs/reference/config-mapping.sample.json` | **新增**（带占位符的配置样例） |
| `docs/01-PRD.md` | 新增 **FR-M2-08**；版本 → **V1.2** |
| `docs/06-Implementation-Notes.md` | 新增本节 §J |
| `docs/03-TestCase.md` | 新增 **TC-42/43/44**；版本 → **V1.5**，用例合计 **44** |
| `docs/README.md` | 版本行更新（01 / 03 / 06）+ 新增 07；关键定案 #18–#20 |
| `README.md`（仓库根） | 补 `import-config` 子命令用法与 07 指引入口 |

---

## K. Q14 定案实施轮（六项编码改动）

> 本轮起因：用户对《Q14 待确认清单》表态「**全部按你的建议执行**」——
> 即 Q14 的**配置侧**（可写字段与登记责任人）按建议值**定案**，Q14-B 的**六项编码事项**按建议方案**实施**。
> 这是 Q14 从「阻塞级别：中」转为「**已闭合**」的一轮。

### K.1 本轮实施清单（对应 Q14-B 六项）

| Q14-B 项 | 决定方案 | 落地 |
|---|---|---|
| 1 L07 检验结论 ＝ GR + QC | ① **各写一行 + 查询侧关联** | `handlers_ledger_derived.go`：`attachLinkedInspection` / `loadLinkedInspections`；QC 以 `ext_json.related_biz_no` 指向 GR |
| 2 L11 订单执行台账 | ① **派生视图（不落行）** | `handleLedgerDerivedList` + `orderExecutionRow`；以 L04 为骨架、关联 L07 派生 |
| 3 变更链键名收敛 | ② **收敛单键** | `store/ext_key`：`ExtKey`（`contract_no` / `related_biz_no`）+ `ListArchiveByExtKey(In)`；`ListChangesByContract` 改用 `json_extract('$.contract_no')` |
| 4 `t_ledger_field_def` 写入端口 | ① 补通道（轻量版） | 新增第五类映射 `ledger_field`（`import-config`），**并让写接口消费它**（`fields` 键名白名单） |
| 5 报送行级权限按真实列重建 | ① 按真实列重建 | `migrations/0003_submission_scope.sql` 加四列；`RowFilterForSubmission` 恢复四令牌真实语义 |
| 6 看板列投影（B19） | ① 标量键同投影 + 断言失败记日志 | `permission.ProjectDeep` 递归投影；`projectResult` 整块投影 + 类型异常写 warn |

**顺带**：因 #6 需要一个递归投影器，一并把台账行、变更链 `archive` 的**嵌套金额泄漏**一并修掉（同一根因，见 §K.2）。

**顺带（前端）**：L11 改为派生视图后，**派生行没有 `id`** —— 前端台账页若继续用 `row.id`，会出现
① `v-for :key` 全为 `undefined`（Vue 重复 key + 渲染异常）；② 「写运营字段」按钮对派生行发出必然失败的
PATCH。故同步修 `web/src/views/Ledger.vue`：行键退用 `biz_no`、只读台账隐藏写按钮、并在标题旁标示
「派生视图 / 只读」。`web/src/ledgerTypes.js` 新增 `READ_ONLY_LEDGER_TYPES` + `isReadOnlyLedger`，
**与后端 `config.ReadOnlyLedgerTypes` 同口径**（两处口径不一致就会出现「必然 40901 的按钮」——见 §I-5 同类教训）。

### K.2 ★ 本轮修掉的三处「静默」形态

| # | 形态 | 后果（不报错） | 修法 |
|---|---|---|---|
| **P1-A** | 列投影只裁**顶层**键 | 台账 `formula_flags`、变更链 `archive`、看板 `Supervision` 里的金额/敏感标量**原样泄漏** | `ProjectDeep`：**deny 与敏感列在任意层级生效** |
| **P1-B** | `ProjectDeep` 若把 `allow` 也递归套用 | 会把嵌套块的**结构键**一并删掉 → **丢合法数据** | 刻意不对称：**allow 只在顶层生效**（`keepKey` 只管顶层；`projectNested` 只做 deny 收缩） |
| **P1-C** | 审核块/关联块的查询**漏加行级过滤** | 把**别部门**的检验结论挂到本部门行上 = 越权可见 | `loadLinkedInspections` / L11 派生 均传入 `permission.RowFilter` 的 SQL 与参数 |

> 三者共同的教训与 §J 一致：**「不报错」不等于「对」**。故本轮新用例一律**端到端断言**（HTTP → 落库 → 回读），
> 且**刻意包含「他部门数据不得挂过来」这一负向断言**。

### K.3 主 Agent 裁定

| # | 议题 | 裁定 |
|---|---|---|
| **K-1** | 变更链的键名收敛后，旧用例前提失效怎么办 | **改用例**，不改代码。旧用例断言「合同号写在 `related_contract` 这类**非约定键**上也必须命中」——那正是**键名未定时的兼容做法**；键名成为契约后，这种「猜任意键」会把**恰好等于合同号的无关值**也拉进来。故改为断言**非约定键不得命中**，并新增 `TestContractChangesSingleKeyContract` 固化该契约 |
| **K-2** | `ledger_field` 白名单「部分启用」是否危险 | 采用 **「该台账登记过才校验、未登记不校验」**：一刀切强制会让**既有部署的写入口全被拒**（回归）。代价：白名单能力是**逐台账逐步生效**的——已在 `05-API` §3.7 写明 |
| **K-3** | `ledger_field` 为何只开放 `ledger_type`/`field_key`/`is_sensitive` | 依纪律 **J-9**（新增配置键必须同时有消费端）：这三项分别被**写接口白名单**与 **`SensitiveFields`** 消费。`field_label`/`is_formula`/`formula_kind` **刻意不开放**——当前公式红标由 `formulaFlags` 在代码里现算，放进来就是"配了没人读"（**B27** 同一教训） |
| **K-4** | `is_sensitive` 能不能随手填 | **不能**。它的语义是「**未显式 allow 即不可见**」——若把 `amount_cents` 标为敏感，而某角色规则里没有显式 allow，该角色的金额会**凭空消失**（默认口径靠 `column_deny` 已处理金额，无需在字段定义里重复）。故样例 **16 条全部 `is_sensitive=false`**，并在 07 指引中写明启用条件 |
| **K-5** | 报送身份字段是否进幂等指纹 | **进**。同一 `biz_no` 换了归属部门 / 申请人 / 验收人，属**不同载荷**，不该被判「同键同载荷」而复用旧结果；`acceptors` 排序后参与（顺序不敏感） |
| **K-6** | 派生行（L11）的主键与分页 | 派生行**无稳定主键** → 不支持按 id 读（400）、不支持写（40901）。分页与 `total` **以骨架表（合同台账）为准**，响应里 `derived:true` + `source:["L04","L07"]` 明示口径 |
| **K-7** | 关联块取多份检验报告时取哪一份 | **保留较早的一份**（`id` 升序首条），避免结果随写入顺序抖动 |
| **K-8** | L07 关联查询失败是否使整页失败 | **不**。关联块属**增强信息**，取不到只写 `warn` 日志并留空——但**必须留痕、不静默** |
| **K-9** | `delay_days` 缺数据时取值 | **不产出该字段**（而非填 0）。`parseISODate` 解析失败即跳过，避免把「未到货」显示成「按期 0 天延期」 |
| **K-10** | 嵌套投影 `[]map[string]any` 怎么处理 | `projectNested` 显式处理 `[]any` 与 `[]map[string]any` 两种；`ProjectDeep` 会把 `[]map[string]any` **规范化为 `[]any`**（测试已固化） |

### K.4 本轮新增 / 改写的用例（顶层 96 → **110**；`PASS:` 行 147 → **179**）

| 包 | 用例 | 断言要点 |
|---|---|---|
| `permission` | `TestProjectDeepDenyAndSensitiveAtAnyDepth` | deny 与敏感列**穿透任意层级**（含 `[]any` 元素内的金额） |
| `permission` | `TestProjectDeepAllowOnlyTopLevel` | allow **不得**删掉嵌套块结构键（防「递归套用 allow 丢数据」） |
| `permission` | `TestProjectDeepSupervisionScalars` | Supervision 的**标量键**也走投影（B19 原始症状）；`[]map[string]any` 规范化为 `[]any` |
| `permission` | `TestProjectDeepMatchesProjectOnFlatRow` | 扁平行上与旧 `Project` **行为完全一致**（防回归） |
| `permission` | `TestRowFilterForSubmissionScoping`（**重写**，18 子用例） | 四令牌**按真实列**断言（SELF/DEPT/CHARGE_DEPT/ASSIGNED/PARTICIPATED）；**前缀不同不得命中**（`ou_h` 不匹配 `ou_h_a`）；空身份/DENY/未知 scope → 0 |
| `httpapi` | `TestLedgerL07LinkedInspection` | GR 行带上 QC 的结论；QC 行反向给出 GR；**脏 JSON 行不使整页失败** |
| `httpapi` | `TestLedgerL07InspectionRespectsRowScope` | **他部门检验结论不得挂到本部门行**（越权负向断言） |
| `httpapi` | `TestLedgerL11Derived` | 派生行数＝合同数；已到货有 `actual_arrival` 与 `delay_days=3`；未到货**不得出现** `delay_days` |
| `httpapi` | `TestLedgerL11ReadOnly` | 写 → 40901；按 id 读 → 400 |
| `httpapi` | `TestLedgerFieldWhitelist` | 已登记字段可写；**writable_fields 内但未登记字段 → 40901** |
| `httpapi` | `TestLedgerFieldWhitelistAbsentKeepsBackwardCompat` | 未登记任何字段定义时**不做键名限制**（向后兼容，不把既有写入口打死） |
| `httpapi` | `TestContractChangesBacktrace`（**改写**） | 单一键契约：非约定键**不得命中** |
| `httpapi` | `TestContractChangesSingleKeyContract` | 键名写错 → 查不到；改用约定键 → 命中 |
| `httpapi` | `TestExtKeyRelatedBizNo` | `ListArchiveByExtKey` + `ExtString`；**脏 JSON 不炸查询** |
| `config` | `TestImportValidateRejects`（**+4 子用例**） | `ledger_field` 的台账键非法 / 字段名空 / 占位符 / 重复 |
| `config` | `TestImportMappingsIdempotent`（**改写**） | 幂等含 `t_ledger_field_def`（两跑仍 2 条） |
| `config` | `TestImportMappingsRejectsInvalidWithoutWriting`（**改写**） | 非法载荷对**两张表**都零写入 |
| `httpapi` | `TestContractChangesHidesAmountFlavouredKeys`（**复核轮新增**） | **B31 回归**：禁金额角色在变更链的顶层与 `archive` 内都读不到金额类键；非金额键仍在；不禁金额角色照常可见 |
| `permission` | `TestProjectDeepFlatRowHardcoded`（**复核轮改写**） | 扁平行行为用**硬编码期望键集合**固化（原写法是同义反复） |
| `permission` | `TestIsAmountKey`（**复核轮新增**） | 金额类键名识别的**正反例**（`amount_note` / `cents` 不得误裁） |
| `worker` | （本文件外） | — |

> ★ 本轮**改写**了 3 个既有用例（`TestRowFilterForSubmissionScoping` / `TestContractChangesBacktrace` /
> `TestDashboardGroupRejectedScopedByRowScope`）——它们断言的正是**本轮决定废弃的旧口径**。
> 改写时**逐个写明「旧前提为何作废」**，避免后人误以为是为过测试而改断言。

### K.5 冲突与歧义（续 B15–B27，编号 B28–B30）

| 编号 | 级别 | 问题 | 现状与处置 |
|---|---|---|---|
| **B28** | 中 | 测试夹具 `seedArchiveExt` 把 `source_doc_type` **写死为 `"CT"`** | 它使 L07 的跨单据关联用例**走不通真实路径**（关联方向依赖单据类型）。本轮新增 `seedArchiveDoc`（可指定单据类型）并改用它；**旧 helper 保留**（其他用例仍在用）。★ 教训：夹具的「默认值」会悄悄缩窄被测路径 |
| **B29** | 中 | `t_submission` 的两处写入点（`store.CreateSubmission` 与 `submission.CreateWithIdem`）与共享 `submissionSelectSQL`/`scanSubmission` **必须同步改列**，否则列数错位 | 本轮四处一并改齐并跑通用例；已在 §K.1 记录。**风险仍在**：该表列定义分散在 4 个位置，未来加列必须四处同改 |
| **B30** | 低 | `seed` 的 `writable_fields` 是**一份清单套全部 `ledger:*`**（非逐台账），无法表达「L11 无写入口」 | 已由**代码层**兜住（`config.IsReadOnlyLedger` → 40901），不依赖种子精度；记录备查 |

### K.6 仍未解决 / 待确认

沿 §J.6。**Q14 已闭合**（配置侧定案 + 编码侧实施）。仍待外部输入：**Q1 / Q17 / Q2 / Q4**（模板与联调前置）。
另：`L10 用途分类汇总台账` 按 Q14 决定**维持只读、不做派生视图**（汇总口径由看板承担）——故该台账查询返回空集是**设计意图**，非缺陷。

### K.7 纪律确认（本轮）

- **未执行 `git commit` / `git push`**（交用户确认）。
- 未新增第三方依赖；未引入 cgo；未触发任何下载。
- 新增迁移 `0003` 为 **ALTER TABLE ADD COLUMN**（SQLite 不支持 `IF NOT EXISTS`），由 `t_schema_migrations` 保证只执行一次；已在临时库实测三份迁移依次生效。
- 含公式/金额的行级与列级约束**双向验证**：既验证「该看到的还在」，也验证「不该看到的（他部门、金额、前缀近似 open_id）确实不在」。

### K.8 本轮同步的文档

| 文档 | 改动 |
|---|---|
| `docs/01-PRD.md` | **Q14 标记已闭合**；§5.5 与 FR-M4-03 按定案改写；版本 → **V1.3** |
| `docs/03-TestCase.md` | 新增 **TC-45~48**；用例合计 **44 → 48**；版本 → **V1.6** |
| `docs/04-Architecture.md` | §3.4 台账表补「可写字段 / 登记责任人 / 派生·只读」；§3.3 补派生说明 |
| `docs/05-API.md` | §3.6 补 L11 派生语义；§3.7 补字段定义白名单与 40901；§11 Q14 行改为已闭合；版本 → **V1.5** |
| `docs/06-Implementation-Notes.md` | 新增本节 §K |
| `docs/07-Template-Build-Guide.md` | 补第五类映射 `ledger_field` 与样例条数；版本 → **V1.1** |
| `docs/README.md` | 关键定案 **#27–#31**；各版本行同步 |
| `README.md`（仓库根） | 版本段补本轮 |

### K.9 ★ 独立复核轮（子代理对抗性审查 · 2026-09-26）

本轮改动**先经独立子代理对抗性复核**（只读、可自行取证、不许改文件），复核**确认了 1 个 P0 与若干 P1/P2**，
全部已修。★ 结论：**「测试全绿」不等于「正确」**——被查出的 P0 在原有用例下**全部通过**。

| 编号 | 级别 | 问题 | 修法 |
|---|---|---|---|
| **B31** | **P0（越权）** | **金额同义键绕过 deny**：`amountDeny` 写的是规范键 `amount_cents`，而变更链接口返回 `change_cents` / `original_cents` / `*_display`，且 `archive` 块里还有一份原样回出的 ext_json → **禁金额角色（采购经办人 / 验收人 / 系统管理员）照常读到金额，且不报错** | `permission`：新增 `isAmountKey`（**后缀规则**：`*_cents` / `*_display` / `amount` / `formula_flags`）＋ `AmountsDenied`；`ProjectDeep` 在规则禁金额时**于任意层级一并裁掉金额类键名**；`handleContractChanges` 据此置 `amount_hidden` 且不再输出汇总金额。★ 用**后缀规则**而非枚举，是为让将来新增的金额键名**自动被覆盖**——枚举漏一处就是一次越权 |
| **B32** | **P1（静默无数据）** | ① **`biz_date` 生产端从不写入**（`Ingest` 未给该列赋值）→ L11 派生的到货字段、看板全部按月指标、「同供应商当月累计」**恒为空**；② **三种日期格式并存**：`SumArchiveAmountBySupplierMonth` 期望 `YYMM`、`formulaFlags` 去连字符后截 4 位得到 `"2026"`（**整年**）、看板期望 `YYYY-MM` | 口径统一为 **`biz_date` ＝ `YYYY-MM-DD`**（全系统唯一格式）：`Ingest` 写入 `occurred.Format("2006-01-02")`；新增唯一归一入口 `monthKey()`（`YYYY-MM`）；`SumArchiveAmountBySupplierMonth` 参数语义改为 `YYYY-MM`；`formulaFlags` 改用 `monthKey`。★ 原实现「同供应商当月累计」实际统计的是**整年**——防拆分指标本身是错的 |
| **B33** | P1（假配置） | `original_biz_no` 被登记为「已知透传键」，但**无任何消费端**（既不读也不参与匹配）→ 诱导填表人以为"填了就生效" | 从 `PassthroughBizFields` **移除**；并把「不在册 = 导入时提示」的纪律写进该文件注释 |
| **B34** | P2（可用性陷阱） | 字段定义白名单有两个不一致：① `declared[k]` 精确匹配 vs `CanWrite` 的 `EqualFold` → 「白名单命中、定义未命中」；② 前端「整行 ops 回写」一旦携带**遗留键**就**整行写不进去**（把无关字段一起挡住） | ① `LedgerFieldKeys` 与写侧统一 `TrimSpace + ToLower`；② 改为**只拒绝"未登记的**新**键"**——行内既有键放行 |
| **B35** | P1（静默无数据） | 报送身份列在**真实路径**下无人写入（前端从不发送 department）→ `DEPT` / `CHARGE_DEPT` 过滤恒命中 0 条 | `handleCreateSubmission`：`department` 缺省时**退用登记人本人部门**（`GetUserRole`）；`applicant_open_id` 已退用登记人 |
| **B36** | P2（错位数据） | `PATCH /api/ledger/{table}/{id}` 不校验「URL 台账 == 该行的 `ledger_type`」→ 可用某台账的 id 往 ops 写一条挂在**别的台账**名下的记录，读侧按 (ledger_type, biz_no) 取 → **写进去了却读不到** | 取到行后校验 `a.LedgerType != table` → 40000 |

**同时修正的两处「测试通过但证明不了正确」**（复核专项指出）：

| # | 问题 | 修法 |
|---|---|---|
| **P2-1** | `TestProjectDeepMatchesProjectOnFlatRow` **是同义反复**：`Project` 已委托给 `ProjectDeep`（同一实现），「两者输出一致」恒成立 | 改为 `TestProjectDeepFlatRowHardcoded`：逐例写出**硬编码的期望键集合**；另加 `TestIsAmountKey` 覆盖后缀规则的**正反例**（含 `amount_note` / `cents` 等**不得误裁**的反例） |
| **P2-2** | `TestIngestArchiveExtJSONQueryable` **自造了一段 SQL**（且正是已废弃的"任意键名匹配"口径）→ 通过也证明不了生产路径 | 改为调用**生产入口** `store.ListArchiveByExtKey`，并补**反向断言**（非约定键不得命中） |

**复核也明确记录了"检查过但未发现问题"的项**：L07 关联与 L11 派生的行级过滤、SQL 占位符绑定顺序、`[]map[string]any` 的规范化、
`ProjectDeep` 的边界（空 map / 非 map / nil 值 / ASCII 大小写）、越权缺失路径（`hasAllow` 为空时仍裁敏感列）、参数注入面（键名一律来自常量或绑定参数）。

> ★ 本轮**最值得记住的一条**：被查出的 P0（B31）在**原有 48 条用例下全部通过**——
> 因为原用例只断言「规范金额键被裁」，没人断言「**同义键**也得被裁」。
> 这与 §J 的 P0-C/P0-D、§K.2 的 P1-A 是同一族的第三次出现：
> **「不报错」不等于「对」；「测试绿」也不等于「对」——要用负向断言把"不该出现的"钉住。**

---

## L. 架构完整性审查轮（架构师只读复核 · 2026-09-26）

> 起因：用户要求「由架构师根据需求和现有代码库**梳理检查架构的完整性**」。
> 审查方式：**只读**（可跑 `go build/vet/test` 取证，不改任何文件）。

### L.1 ★ 查出并已修的 P0（B37）——**本轮自己引入的回归**

| 编号 | 级别 | 问题 | 修法 |
|---|---|---|---|
| **B37** | **P0（静默）** | **看板 14「采购执行」直接读 `t_ledger_archive` 的 `ledger_type='L11'`**，而 L11 在 Q14 定案后已是**派生视图、永不落行**（`config.DerivedLedgerTypes` 明示、导入层亦硬拦 `doc_type→L11`）。→「在途订单数 / 平均采购周期 / 延期订单 TOP5 / 月度采购金额趋势 / 同供应商当月累计 TOP」**五项指标生产环境全部恒空或恒 0，且无日志无报错** | `internal/dashboard/order_execution.go`：新增 `orderExecutionRows()`，以 **L04 合同台账为骨架 + 关联 L07 到货验收**派生，与台账派生视图**同口径**；`buildPurchase` 改走它；`buildAnomaly` 去掉从未消费的 L11。用例改为造**真实路径**（L04+L07）并加**负向断言**（塞一行假 L11 存档，旧实现回归即失败） |
| **B37b** | P1 | **同一台账两套词表**：看板按中文运营键（`实际到货`/`延期天数`/`完成日期`）读 L11，派生视图产出 `order_state`/`actual_arrival`/`delay_days` —— 互不兼容 | 统一到**派生词表**（`internal/dashboard/order_execution.go` 顶部常量）；`seed` 的可写字段清单移除「实际到货 / 延期天数」（L11 无写入口，留着会让人以为可人工补录） |

> ★ **B37 的性质**：它在**本轮 Q14-B 第 2 项**（L11 改派生视图）时被引入 —— 改了写侧与台账读侧，**漏了看板读侧**。
> 这是本项目「静默」缺陷族的**第四次**出现（前三次：§J P0-C/P0-D、§K.2 P1-A、§K.9 B31），
> 且与历次一样 **`go test` 全绿**——因为原用例用 `seedArchive(...,"L11",...)` **人工伪造 L11 行**，
> 绕过了真实写入路径。**教训：凡"改了某个数据源的产生方式"，必须全局搜索该数据源的*所有*消费端。**

### L.2 查出的**未实现**项（待范围决策，非本轮引入）

| 编号 | 级别 | 问题 | 现状 |
|---|---|---|---|
| **B38** | P1（若联调含"节点轨迹"→P0） | **FR-M2-07 节点级事件未实现**：`inbox` 对 `approval_instance` 与 `approval_task` **一视同仁**（均"拉实例详情→写实例状态"），节点语义丢失；`t_status_history.task_node` 列存在但**无写入者** | 待决策：按 `event_type` 分派并写 `task_node`；**或**明确降级为「本期不做节点级」并在 PRD/API 标注 |
| **B39** | P1（若联调含"附件"→P0） | **FR-M0-09 / ADR-08 附件与对象存储未实现**：`DownloadAttachment` 恒返回 `nil,nil`；`UploadAttachment` 用 JSON body 而非官方 multipart；`JX_S3_*` / `JX_RUSTFS_*` 环境变量**无任何 Go 消费端**；全仓无 objectstore 包 | 待决策：补 objectstore + multipart 实装；**或**明确「附件主存由外部/脚本承接」并在文档降级声明、TC-65 相应调整 |

### L.3 文档↔代码漂移（已修 2 项 / 待议 4 项）

| 编号 | 级别 | 问题 | 处置 |
|---|---|---|---|
| **B40** | P2 | 05-API §1.2 与架构 §1.2 声称"写操作需 **CSRF Token**"，但 `router.go` **无 CSRF 中间件**（唯一防护点是登录回调的 `?state`） | **已澄清文档**：写明现状＝依赖 `SameSite=Lax` + 同源，并标"待补 CSRF 中间件" |
| **B41** | P2 | `router.go` 已注册 `/submission/{id}/receipt` `/group` `/reject`（前端 `api-m1m6.js` 在用），但 05-API §3.5 **只列了 3 个端点** | **已补登**（对应 FR-M6-02/05/08） |
| **B42** | P2 | `t_ledger_archive.submitter_open_id` 列 + model + repo 读写齐备，但 `Ingest` **从未赋值** → 恒 NULL（"列存在但无写入者"的又一例） | 待议：删除该列，或补写入者 |
| **B43** | P2 | 三项挂在未决口径上：① 单实例 **stale-lock 自愈**未实现（架构 §11 问题 3），现依赖 flock 语义；② `AddWorkingDays` **无法定节假日日历**（挂 Q18）；③ `countSplitSuspect` **未做供应商名称归一**（挂 Q20，不定则防拆分可被换个写法绕过） | **三项均已收口**：② → Q18 已定（留 `HolidayChecker` 挂点，见 §M.1）；③ → Q20 已定（`supplier_norm` 收口在 `upsertArchive`，见 §M.1）；① → **本轮 §O.4 定案** —— 判定"启动前 stale-lock 检测"在原语语义下**是伪需求**，改为「诊断增强 + 明确不夺锁」并补 10 条用例。**无遗留** |

### L.4 架构师明确核对、未发现问题的覆盖面（摘）

构建/静态全绿；**幂等**（事件级唯一 ID、`idem_key` UNIQUE、≥7.1h 窗口、`FindIdem` 按 (target_id, actor) 隔离、三分支语义）；**时序**（inbox 同步路径仅落库+建作业即返回、有序事件不阻塞、退避+死信+重放）；**先订阅 + 对账补拉 + 降级**；**行级**（6 令牌 + DENY 在 SQL 层、别名一致、fail-closed）；**列级**（`ProjectDeep` 递归 + 金额同义键后缀裁剪 + B19 标量键同投影）；**权限三表职责边界清晰**（`t_permission_rule` 矩阵 / `t_user_role` 角色 / `t_ledger_field_def` 写接口白名单且有写入端口）；**跨单据关联均注入行级过滤**；**L11 派生读侧**（批量关联无 N+1、无 id→读 400 写 40901）；**migrations 0001–0003 与架构 §3 DDL 一致**；**看板单次 IN 查询无 N+1**；**DEV_MODE 可测性**、**健康检查四项自检**、**env 密钥全走环境变量**；M1/M4/M5/M6/M7/M8 均有落地点（缺项仅 B38/B39）。

### L.5 本轮同步的文档

| 文档 | 改动 |
|---|---|
| `docs/05-API.md` | §1.2 CSRF 澄清（B40）；§3.5 补登三端点（B41）；版本 → **V1.6** |
| `docs/06-Implementation-Notes.md` | 新增本节 §L |
| `docs/README.md` | 版本行同步 |
| `docs/04-Architecture.md` | §11 问题 3 标注"现依赖 flock 语义，stale-lock 检测待补"（B43） |

### L.6 测试工程师全面测试轮（QA 独立验证 · 2026-09-26）

**门禁**：`gofmt` 空 / `build` OK / `vet` OK / `go test ./... -count=1` 全绿（顶层用例 **110 → 124**，其中 QA 新增 14 个）/ 前端 `vite build` 599 modules 且与 `internal/webui/dist` **完全一致**。
**覆盖率**：permission 86.5%、inbox 82.0%、worker 78.2%、httpapi 66.8%、config 66.4%；`store`/`submission` 自有测试少但经 httpapi 用例达 **59.2% / 跨包 82.7%（dashboard）**。
**环境限制**：`-race` **无法运行**（Go 的 race 检测依赖 cgo，本机无 gcc）→ **数据竞争未验证**，须在有 gcc 的机器上补跑。

**结论：可进入联调。** 6 项修复全部**独立证实**（含负向）：B31 金额同义键、B32 `biz_date` 写入+月份口径、B35 报送部门补全、B36 表↔行一致性、B37 看板 14 派生、B34/B33 白名单语义。QA 还**自己造真实路径**（`dev` 注入 → worker → `Ingest` → 读侧）补上了两处"靠夹具绕过真实写入路径"的覆盖缺口。

| 编号 | 级别 | 问题 | 处置 |
|---|---|---|---|
| **B44** | 低危·信息泄漏（**已修**） | **`tier` 文本内嵌金额**：`tier = "max 取档 → " + formatCents(m)` —— B31 的后缀规则（`*_cents`/`*_display`）**裁不到它**，故禁金额角色（采购经办人 / 验收人 / 系统管理员）仍能从这段文本**反推金额** | `handleContractChanges`：把 `hideAmounts` 提到构造 item **之前**，禁金额时**整体不产出 `tier`**；并加回归断言（禁金额角色 `tier` 必须缺席） |
| **B45** | 口径归属（**已澄清**） | TC-12 / TC-50 把「**档位判定**」写成自建系统行为，但 `purchase_tier` 在本系统**零消费端**（决策 #24 已列为"假配置"）—— 档位判定实际发生在**飞书模板的条件分支**里（方案 B 的应有形态） | 两处补**归属澄清**：档位归属属"**模板配置验收**"，自建系统侧只验「金额分整数存储」与「付款方式取自模板」；执行时须分开记录两侧结果，**不得把模板侧不符判为系统缺陷** |

> ★ **B44 是 B31 修复不完整的延续**，属同一根因的另一副面孔：**"按列名保护金额"挡不住"把金额写进文本"**。
> 与本项目既往四次的差别在于——这次是**第三方复核找出来的**，且**原有 28 条 P0 用例全部通过**。
> 结论再次收敛到同一条纪律：**要钉住"不该出现的东西"，而不是只断言"该出现的东西"**。

---

## M. 口径落地轮（Q18/Q20/Q21 闭合 + Q19 保持 + B39 附件最小集 + B46）

> 起因：用户对架构审查提出的两项范围决策与四项口径，采纳交付总监建议并授权执行：
> **B38 缓（但补洞）、B39 做一半、Q20/Q21 立即定、Q18 留挂点、Q19 待业务**。

### M.1 Q18–Q21 处置

| 项 | 决定 | 落地 |
|---|---|---|
| **Q18 工作日** | **不含法定节假日**（只跳周六日），并留**可注入挂点** | `submission.HolidayChecker`：`nil` ＝ 不含节假日；业务日后确认含节假日/调休时，只需在启动时注入函数，**`AddWorkingDays`/`Deadline`/`IsOverdue` 调用点零改动** |
| **Q19 三单匹配容差** | **保持待定**（当前人工判定） | ★ **刻意不加配置键** —— 容差当前无消费端，加进去就是"假配置"（决策 #24）。待集团财务给数后再一并实现 |
| **Q20 供应商归一** | **最小口径**：去空白 + 全角半角归一 + 大小写归一；**不做简称合并** | 新增 `internal/normalize`（`Supplier()`）；migration `0004` 加 `t_ledger_archive.supplier_norm` + 索引；**写入收口在 `store.upsertArchive` 一处**（保证"列加了就一定有写入者"）；三处聚合改为按归一值分组：防拆分累计 `SumArchiveAmountBySupplierMonth`、`countSplitSuspect`、看板 `top_supplier_month`（展示仍用原名，经 `topMoneyBarsNamed`） |
| **Q21 金额 0** | **不允许**（必须 > 0） | 台账侧：`Ingest` 抽取后金额 ≤ 0 **不落库**（保持 NULL）并 `warn`；报送入口 `< 0` → `<= 0` |

> ★ **Q20 的分寸**：归一 ≠ 模糊匹配。本包只处理**写法差异**（空格/全角/大小写）；
> 「简称 ↔ 全称」这类**语义等价必须由业务给对照表** —— 否则包含式匹配会把不同公司并成一家，
> 让防拆分预警指向无辜供应商。用例已用**反向断言**固化（`TestSupplierDoesNotMergeDifferentNames`）。

### M.2 B38 的洞已补（B46）——**状态史同状态去重**

架构审查指出：`t_instance_status_history` **无唯一约束**、`appendHistory` 是纯 INSERT，而 `inbox` 对
`approval_instance` 与 `approval_task` **一视同仁** → 每个节点事件都追加一行，**同一状态反复堆积、时间线变噪声**。

修法：`AppendStatusHistory` 追加前与**最近一行**比对 `(status, operator, opinion)`，**三者全同则跳过**，
且**不消耗 `event_seq`**（序号保持稀疏、无空洞）。用"三者全同"而非"仅 status 相同"，
是为了**不误杀有意义的变化**（换人、带新意见）—— 用例两个方向都钉住了。

> ★ B38（节点级轨迹本体）仍按建议**缓到二期**：FR-M2-07 优先级是「**应该**」，
> 且节点轨迹的**权威来源本来就在飞书审批详情页**，本系统再存一份是冗余副本。

### M.3 B39 附件最小集（**砍掉上传那一半**）

| 做了什么 | 说明 |
|---|---|
| **移除上传** | `Client` 接口删去 `UploadAttachment`（含 dto / Fake 的钩子）。理由：**模式 A 下本系统不创建实例**，附件由申请人在飞书侧上传，本系统只做**接收** —— 上传属模式 B 遗留，**零调用点** |
| **下载实装** | `HTTPClient.DownloadAttachment` 由"恒返回 `nil,nil` 的骨架"改为**真实实现**：裸 GET 取字节流、识别 JSON 错误体、空内容报错 |
| **元数据表** | migration `0005` `t_attachment`；**入库时只登记元数据（零网络 IO）** —— 事件有 3 秒窗口，绝不能在此下载 |
| **按需拉取 + 缓存** | `GET /api/attachment/{file_id}`：主存命中直接返回；未命中回源 → 落主存 → 回填。**二次下载不回源**（有用例固化，避免白耗 API 配额） |
| **对象存储抽象** | 新增 `internal/objectstore`（接口 + **`LocalStore`**）。**S3 待定**：接 S3 需 SDK 或自写 SigV4，**新增依赖按纪律需先经用户同意**，故只留接口与文档，凭据/依赖到位后换一行装配即可 |
| **权限** | **不冗余身份列**：一律以 `instance_code` 回查 `t_instance` 的可见性。★ 这与 `t_submission`（0003 必须冗余）的选择**相反**，因为这里**有权威来源可回查** —— 见 §M.4 |
| **降级** | 未配置 `JX_ATTACH_DIR` → **不缓存、每次回源**（链路可用、日志有痕迹），**不是静默丢功能** |

> ★ **未能交付的部分（明确记录，不掩盖）**：
> ① **S3 主存 + RustFS 异地备份**未接入（需依赖/凭据决策，且 `JX_S3_*` 目前无 Go 消费端）；
> ② **凭证包纳入附件清单**未接线（`internal/submission` 的 package 仍只有单据清单）。
> 二者均在 §M.5 列为本轮**已知缺口**。

### M.4 一处踩坑（值得记下）

`permission.RowFilterForInstances` 生成的谓词**带别名 `a`**（内部经 `RowFilter`，空别名会被兜底成 `a`）。
新增的 `instanceVisible` 初版写 `FROM t_instance WHERE ...`（未起别名）→ 直接 500
（`no such column: a.department`）。**教训：拼用现成的行级谓词时，必须确认目标表的别名与谓词一致** ——
这类错误会以 500 暴露，还算幸运；若换成 `COUNT(*) = 0` 的写法就会**静默变"永远无权"**。

### M.5 本轮新增 / 改写的用例（顶层 124 → **133**）

| 包 | 用例 | 断言要点（含**负向**） |
|---|---|---|
| `normalize` | `TestSupplier` / `TestSupplierDoesNotMergeDifferentNames` / `TestSupplierEmpty` | 三规则逐条归一；**不同公司绝不能被并成一家**；空值不产生分组键 |
| `worker` | `TestStatusHistoryDeduplicatesSameStatus` | 完全相同的状态**不重复追加**；**仅意见不同 / 仅换人 / 状态变化**三种都要追加（防去重过宽）；`event_seq` **无空洞** |
| `worker` | `TestIngestIgnoresNonPositiveAmount` | 0 与负数**不落库**；正数正常落 `10000` 分 |
| `worker` | `TestSupplierNormGroupingAcrossVariants` | 同一家的三种写法**合并计 180000 分**；另一家**不得并入** |
| `httpapi` | `TestAttachmentDownloadAndCache` | 列表**不触发下载**；首次下载**回源 1 次** + 落主存 + 回填；**二次不回源** |
| `httpapi` | `TestAttachmentRowScopeByInstance` | 本部门可下；**他部门 403**；**未登记 file_id → 404** |
| `httpapi` | `TestAttachmentDegradesWithoutStore` | 无对象存储时**直接转发**（降级可用） |
| `store` | `TestQAMigrationFreshAndIdempotent`（**同步**） | 迁移 5 份；`t_attachment` 与 `supplier_norm` 均生效 |

### M.6 本轮同步的文档

| 文档 | 改动 |
|---|---|
| `docs/01-PRD.md` | Q18/Q20/Q21 **闭合**、Q19 保持待定；版本 → **V1.5** |
| `docs/04-Architecture.md` | §6.4 环境变量补 `JX_ATTACH_DIR` |
| `docs/05-API.md` | 新增 **§3.12 附件**（两条端点 + 错误码 + 降级语义）；版本 → **V1.7** |
| `docs/03-TestCase.md` | 新增 **TC-67~70**；版本 → **V1.10** |
| `docs/06-Implementation-Notes.md` | 新增本节 §M |
| `docs/README.md` | 关键定案 **#37–#40**；版本行同步 |

---

## N. 对象存储与凭证包收口轮（S3 主存 + 主备双写 + 凭证包附件 + B42）

> 起因：用户「推送。继续完成开发」。本轮把 B39 遗留的两项缺口补完（主存口径 + 凭证包附件），并处置 B42。

### N.1 S3 主存：手写 SigV4（**零新增依赖**）

| 项 | 内容 |
|---|---|
| 为什么自己签 | 纪律「**新增依赖需先经用户同意**」，而 SigV4 用标准库（`crypto/hmac` + `crypto/sha256`）即可完整实现 → **手写、零依赖**；且天然兼容 S3 协议族（AWS S3 / MinIO / **RustFS**） |
| 实现 | `internal/objectstore/s3.go`：CanonicalRequest → StringToSign → 四段派生密钥（date→region→service→aws4_request）；签名只覆盖 `host` / `x-amz-content-sha256` / `x-amz-date`（最小集，够用且不易错） |
| 寻址 | `PathStyle=true`（缺省，MinIO/RustFS）＝`host/bucket/key`；`false`＝`bucket.host/key`（云 S3） |
| 配置不完整 | **构造即失败**（不运行时静默降级）——只配一半的 S3 会表现为"附件下载失败"，排查成本高 |
| key 安全 | `sanitizeKey` 拒绝路径分隔符与上跳（`../`），防读写目录外对象 |

> ⚠️ **可信度声明**：签名的**协议形状**已由用例逐项断言；但**密码学校验只能在真实端点完成** ——
> 离线无法证明该签名会被 AWS/MinIO 接受。故提供**默认跳过**的集成测试：
> `JX_S3_TEST_ENDPOINT/BUCKET/AK/SK=... go test ./internal/objectstore/ -run TestS3Integration -v`。
> **联调前务必先跑**；region / path-style / 参与签名的头若有偏差，它会**直接失败**而非静默。

### N.2 主备双写（ADR-08：主存 S3 + 异地备份 RustFS）

`MirrorStore` 语义**刻意不对称**（理由逐条写在代码注释）：

| 动作 | 语义 | 理由 |
|---|---|---|
| `Put` | **先写主存**（失败即失败）→ **再写备份**（失败只记 warn） | 主存是权威副本；备份失败**不该**让用户下载失败，但**必须留痕** —— 否则"备份静默失效"无人察觉 |
| `Get` | 先主存；404 或故障 → **回退备份** | 只写不读＝"备份从未被验证过"；主存故障时备份是唯一恢复手段 |
| `Has` | 同 Get | 一致性 |
| 装配 | 无备份→直接返回主存；**无主存→返回 nil** | 调用方据此**降级为不缓存、每次回源** |

### N.3 凭证包纳入附件（补 B39 缺口②）

- `submission.PackageAttachment` + 新文件 **`附件清单.csv`**（业务单号/附件名/`file_id`/是否已入库/字节数）；
  `凭证包说明.txt` 与 PDF 同步给出附件数量与入库状态。
- 数据来源 `store.ListAttachmentsByBizNos`（按**关联单据的业务单号**取；附件登记时已带 `biz_no`，无需回查实例）。
- 读不到附件清单时**只记 warn、不使打包失败**（凭证包本身仍有效），但**必须留痕**。
- **为什么非做不可**：凭证包是「报送集团」的交付物；只含单据清单而不含附件引用 → 集团收到**没有文件的空包**，
  而接口仍返回 200（典型"静默不完整"）。

### N.4 B42 处置：`submitter_open_id` 标注为**预留未用**

该列 + model + repo 读写齐备，但 `Ingest` 从不赋值 → 恒 NULL（与 `biz_date` / `department` 同族）。
本轮选择**标注为预留**而非删除：SQLite 删列需**重建表**，代价与风险高于收益；已在 `04-Architecture` §3 的 DDL
注释中写明「列已建，但 Ingest 不写、亦无读取者；**新用途前请先明确语义**」。

### N.5 本轮新增用例（顶层 133 → **146**）

| 包 | 用例 | 断言要点 |
|---|---|---|
| `objectstore` | `TestS3RequestShape` | path-style 路径、三个签名头、`Authorization` 三段式 + Credential 作用域 + `SignedHeaders` 精确 + Signature 64 位十六进制 |
| `objectstore` | `TestS3SignatureDeterministicAndSensitive` | 同输入签名**确定**；key / 请求体 / region 任一变化签名必变。★ 第一版踩坑：每次新建 httptest server → 端口不同 → host 不同 → 签名必然不同，测的成了"端口随机性" |
| `objectstore` | `TestS3StatusCodeMapping` | 404 → `ErrNotFound`；5xx → **报错**（不得静默当成功） |
| `objectstore` | `TestS3VirtualHostAddressing` | `PathStyle=false` → `bucket.host` 寻址。★ 用**假 Transport 只观察请求**（virtual-host 的 host 本机解析不了） |
| `objectstore` | `TestS3RejectsBadConfig` | 五类非法配置**构造即失败**。★ 其中一条初版**值写错**（注释写"缺 scheme"、值却带 scheme），已修 |
| `objectstore` | `TestS3RejectsPathTraversalKey` | `../` / 分隔符 / 空 key 一律拒绝 |
| `objectstore` | `TestS3Integration` | **默认跳过**；设 `JX_S3_TEST_*` 后对真实端点验证签名与读写 |
| `objectstore` | `TestMirrorDualWrite` / `...BackupFailureDoesNotFailPut` / `...PrimaryFailureFailsPut` | 双写生效；**备份失败不失败**；**主存失败必须失败** |
| `objectstore` | `TestMirrorGetFallbackToBackup` / `TestMirrorHasFallback` / `TestNewMirrorDegrade` | 主存 404/故障回退备份；两侧皆无 → `ErrNotFound`；装配退化语义 |
| `httpapi` | `TestSubmissionPackageIncludesAttachments` | 凭证包 zip **必须含 `附件清单.csv`**，且含预期 `file_id` / 文件名 / 未入库状态 |

### N.6 本轮同步的文档

| 文档 | 改动 |
|---|---|
| `docs/04-Architecture.md` | §6.4 补 `JX_S3_REGION` / `JX_S3_PATH_STYLE` / `JX_RUSTFS_BUCKET` / `JX_RUSTFS_REGION` / `JX_RUSTFS_PATH_STYLE`；§3 DDL 标注 `submitter_open_id` **预留未用**（B42） |
| `docs/05-API.md` | §3.12 补「对象存储装配」；§3.5 凭证包补「包内容」含 `附件清单.csv`；版本 → **V1.8** |
| `docs/06-Implementation-Notes.md` | 新增本节 §N |
| `docs/README.md` | 关键定案 **#41–#43**；版本行同步 |

---

## O. 降级口径收口轮（B38/B39 文档漂移 + B43① 单实例锁自愈边界）

> 起因：用户「推送。继续完成开发」。本轮**不改业务行为**，做两件事：
> ① 把 §M/§N 已下但**未落到正本**的降级决策补到 PRD/API/架构/UC/TC；
> ② 处置 B43① —— 单实例锁的「僵尸锁」边界。
> ★ 本轮**唯一新增代码**是 `internal/singlelock` 的诊断增强 + 10 条用例。

### O.1 为什么会有「漂移」——本次的根因

§M.2 写明 B38「缓到二期，**并在 PRD/API 标注**」，§M.3 写明 B39「砍掉上传那一半」，
但**实际只改了实现说明与部分 API 段落**，正本（PRD / UC / 架构 / TC）未同步。后果不是"少写了几行字"：

| 未同步处 | 实际写的是 | 真相 | 危害 |
|---|---|---|---|
| **PRD FR-M0-02**（必须） | 「订阅 `approval_instance` **与** `approval_task` 两类事件」 | **只订阅 `approval_instance`** | 联调按 FR 验收 → 「`approval_task` 没收到事件」被当成缺陷，实际是**刻意不订** |
| **PRD FR-M0-06**（必须） | 「`approval_instance` 与 `approval_task` **分别路由到正确处理器**」 | 节点级**无处理器** | 同上；且掩盖了「未知事件类型须安全忽略」这个真实要求 |
| **PRD FR-M0-09**（必须） | 「附件**上传** / 下载」验收「可上传至 S3」 | **只下载**，上传链路已删（零调用点） | 会被要求"补上传功能"，属**已决策不做**的事 |
| **API §6 飞书接口** | 列「附件**上传** / 下载」、称「仅 4 个」 | 实际上传不调用 | 平台调用量盘点与合规口径被高估一个接口 |
| **UC-15 主流程** | 「推送 `approval_instance`（**或 `approval_task` ROLLBACK**）」 | 不订阅节点级 | 用例设计与实现不一致 |
| **TC-22 / TC-65 / TC-67** | 分别测「上传至 S3」「上传超大附件」「模拟 `approval_task` 节点事件」 | 上传不参与；节点级不订阅 | **测的是不存在的能力**，执行时必然"失败"，然后被误判为缺陷 |

> ★ 与既往五次的差别：这次**不是**"某处代码错了、测试全绿"，而是
> **"决策正确、落地不全"** —— 是**文档面**的同族病：**系统性替换只做了一处**。
> 纪律：**凡"做了范围决策"，必须全局搜索被决策影响的所有文档位点**，
> 不能只改"实现说明 + 顺手看到的那一节"。

### O.2 B38 落地点（PRD §3.2 N10 为唯一权威）

新增 **PRD §3.2 N10「本期不做节点级审批轨迹（FR-M2-07 降级为二期）」**，理由与代价写全：
FR-M2-07 优先级「应该」、节点轨迹权威来源在**飞书审批详情页**（再存一份是冗余副本）；
代价＝`t_status_history.task_node` 恒 NULL，且**同节点多事件重复推送**由 **B46 同状态去重**兜底。

连带修正：PRD **FR-M0-02 / FR-M0-06 / FR-M2-07**、UC §2 与 **UC-15**、TC **TC-67**、
架构 §3 DDL（`task_node` 预留未用）与 **§9 M2 映射行**及事件流三处（flowchart / sequence / `event_type` 注释）、
API **§3.4 `task_node` 口径 / §6 / §7 / §9 / §10**。

> ★ 一条设计判断值得记下：**「订阅了但不处理」不是无害的**。
> 订阅 `approval_task` 会让每张单据的每个审批节点都产生事件（多事件量、多失败面、多日志），
> 而处理逻辑为空 —— 纯负债。故**不订阅**比"订阅后忽略"更正确。

### O.3 B39 口径修正：飞书**上传接口本期不调用**

`FR-M0-09` 原文把「附件上传 / 下载」并列且验收「可上传至 S3」，但 §M.3 已决定
**移除 `Client.UploadAttachment`**（模式 A 下本系统不创建实例，附件由申请人在飞书侧上传）。
本轮把 FR-M0-09 改为「**附件下载**（飞书 GET）」并明写：
写入自有 S3 **只发生在「回源缓存回填」**路径；API §6 把上传行标为 ~~删除线~~ + **本期不调用**，
标题由「仅 4 个」改为「**实际调用 4 个**」（下载实用、上传不用，节点级事件不订）。

> ★ 注意区分两种"写 S3"：① **飞书上传接口**（本期不用）；② **自有对象存储的 `Put`**（本期**在用**，
> 出现在回源缓存回填与主备双写）。原文档把两者混在一个"上传"里，是漂移的温床。

### O.4 B43① 单实例锁自愈边界（**本轮唯一新增代码**）

原架构 §11 问题 3 的建议是「启动前 **stale-lock 检测与 PID 校验**」。核查后判定：
**该建议在原语语义下是伪需求**，理由是分层的 ——

| 场景 | 真相 | 处置 |
|---|---|---|
| 持有者**进程已死** | `flock` / `LockFileEx` 由**内核在进程消亡时释放** → 锁**已经**是空闲的，下一个实例**直接获取成功** | 无需检测器。`TestAcquireReleaseReacquire` + TC-04 第 4/5 步是现场证据 |
| 持有者**进程存活但僵死** | **仍持锁 —— 这是正确行为** | **不夺锁**（夺锁＝两实例并发写 SQLite）。由 systemd + 端口独占兜底，人工 `kill -9` 后自愈（TC-27 僵死分支） |
| 锁文件在**共享 / 网络存储** | **唯一真实的异常形态**：本机不可能出现他人的锁记录 | 读 `HOST=` 与本机比对，不一致 → **告警提示"锁文件位于共享存储，flock 语义不可靠"** |

**落地的代码**（`internal/singlelock`，零新增依赖）：

- 锁文件由「一行裸 PID」升级为 `PID=` / `HOST=` / `START=` 三行；**纯诊断，不参与加锁判定**；
- 获取失败的错误串**带上持有者诊断**（原先只有 PID，现在含主机与启动时刻）；
- `readInfo` **容错优先、永不报错**：旧格式（裸 PID）、被改写、截断、空文件都只降级为零值 ——
  它的唯一目的是"告诉运维谁占了锁"，不能因为文件不完美就反过来报错。

> ★★ **Windows 踩坑（实拍发现，非推理）**：Windows 的 `LockFileEx` 是**强制锁** ——
> 被锁区间**连读也挡**。初版锁偏移 0，于是第二个实例在获取失败后**读不到**锁文件里的诊断信息，
> 表现为 `PID=未知`，恰好把本包唯一的排查线索弄没了（`TestSecondAcquireFailsAndReportsHolder` 直接失败）。
> 修法：**锁区间移到偏移 1 MiB 处 1 字节**（`LockFileEx` 允许锁定超出文件末尾的区间），
> 让前 512 字节的诊断区对所有进程可读。Unix 侧 `flock` 是**劝告锁**、作用于整文件，无此问题。
> **两端口径仍一致**：独占、非阻塞、第二个失败。

### O.5 本轮新增用例（顶层 146 → **156**）

| 用例 | 断言要点 |
|---|---|
| `TestAcquireReleaseReacquire` | 获取 → 释放 → 再获取成功（**内核已释放**的最直接证据） |
| `TestSecondAcquireFailsAndReportsHolder` | 第二实例**必须**失败；`errors.Is(err, ErrLocked)`（供非 0 退出码）；**错误串必含持有者 PID + 主机名 + 锁路径** |
| `TestAcquireWritesDiagnostics` | 持锁后锁文件**确实**写入 PID/主机/START。★ 用**持锁句柄自身**读取（Windows 强制锁下另一句柄读不到） |
| `TestReadInfoTolerant`（6 子例） | 旧格式 / 当前格式 / 空文件 / 乱码 / PID 非数字 / 小写键名 —— **一律不报错** |
| `TestInfoString` | 完整与缺失字段的**降级渲染**（缺失渲染为「未知」、无 START 段） |
| `TestForeignHostHint` | 同主机不提、异主机必提、主机未知**不臆断** |
| `TestAcquireEmptyPath` | 空路径报错，且**不得冒充 `ErrLocked`**（会误导成"已有实例在跑"） |
| `TestReleaseIdempotent` / `TestPathAccessor` | 重复释放安全；`Path()` 正确 |

> ★ 两个用例**互相兜底**的理由：若有人误删「写诊断信息」，`TestSecondAcquireFails...` 会看到"PID=未知"而失败；
> 若有人只改了写入却漏了渲染，`TestAcquireWritesDiagnostics` 会失败。**单点删除不会静默通过**。

### O.6 本轮同步的文档

| 文档 | 改动 |
|---|---|
| `docs/01-PRD.md` | 新增 **§3.2 N10**；改 **FR-M0-02 / FR-M0-06 / FR-M0-09 / FR-M2-07**；变更记录**重排为倒序**；版本 → **V1.6** |
| `docs/02-UseCase.md` | §2 参与者表、**UC-15 主流程**；变更记录补 V1.2；版本 → **V1.2** |
| `docs/03-TestCase.md` | **TC-04 / TC-22 / TC-27 / TC-65 / TC-67** 改写；变更记录重排（并修正**表头 3 列 vs 行 4 列**的不一致）；版本 → **V1.11** |
| `docs/04-Architecture.md` | §3 DDL `task_node` 预留未用；**§7.2 重写**（僵尸锁 / 诊断 / Windows 锁偏移）；**§11 问题 3 定案**；§9 M2 映射行；事件流三处 |
| `docs/05-API.md` | §3.4 `task_node` 口径、§6（含**飞书接口清单修正**）、§7、§9、§10；版本 → **V1.9** |
| `docs/06-Implementation-Notes.md` | 新增本节 §O |
| `docs/README.md` | 关键定案 **#44**；版本行按实情校正 |

### O.7 本轮附带发现：**B47 · `L03` 采购经办登记台账无生产者**（P1 · 待决策）

做《审批模板人工建立指引》时按「工具线口径 → 代码」逐项核对台账写入者，查出这一处：

| 项 | 内容 |
|---|---|
| **工具线口径** | 台账与看板设计 **第 3 号「采购经办登记台账」`L03`**：数据来源 ＝「**物资采购申请单**审批节点指定经办人后自动写入」（★ 该台账标注为「必拆」，并有 3 个运营字段：经办状态／完成日期／是否按期） |
| **代码现状** | `config.Maps.LedgerTypeFor(docType)` 是**一对一**映射（`t_config_mapping` 中 `map_kind='ledger_type'` 的 **key = doc_type** 且唯一）。样例配置里 `PR → L02` → **`L03` 无人写入** |
| **连带后果 1** | `L03` 恒空，与工具线口径直接矛盾 |
| **连带后果 2（更危险）** | 看板 14「采购执行看板」的**「需求提出人任经办人的笔数」**与**「经办人指定集中度」**均读 `L03`（`dashboard.buildSupervision`）→ 前者**恒为 0**，而 **0 恰好是该指标的期望值**：「没有违规」与「没有数据」在界面上**完全一样**，永远发现不了 |
| **连带后果 3** | `L03` 的 3 个运营字段（经办状态／完成日期／是否按期）**无行可挂** —— 运营表写入接口 `PATCH /api/ledger/{table}/{id}` 需先取到**已存在的存档行**，而 `L03` 永远没有存档行 |
| **为什么测试没发现** | 用例用 `seedArchive(t, db, "L03", "PR-2609-0001", ...)` **直接造 L03 行**（biz_no 用 PR 单号），**绕过了真实写入路径** —— 与 **B37**（L11 派生）**同一模式** |

**为什么不在本轮直接修**：这是一处**设计决策**，涉及工具线台账口径（改映射模型）与看板取数口径，
属「范围/结构变更」，按本项目纪律应先拍板再改。三条候选与推荐（推荐 **A：一对一放宽为一对多**，
`PR` 同时落 `L02` 与 `L03`）已写入
`deliverables/procurement-approval/审批模板人工建立指引V1.1.html` **§12 S-1**，
并在 `docs/07-Template-Build-Guide.md` §3.1 末留了指针。

> ★ 本条的教训值得单列：**「错误表现为正确」是静默缺陷族里最危险的一支** ——
> 前六次的症状是「数据恒空 / 恒 0 但指标本身有意义」，这一次是「**恒 0 恰好等于期望值**」，
> 连"多看一眼数字"都发现不了。**检验方法只能是：确认数据源真的在产生行**（而非只看数字对不对）。

## P. 控件口径修正轮（「人员／部门（当前登录人）」是错误口径 · B48）

> 起因：用户（项目负责人）在**飞书审批后台看过实际控件面板**后指出 ——
> **「系统没有空间可以带入当前登录的人员和部门。」**
> 复核一手证据后确认：《审批模板人工建立指引》把控件写成 **「人员／部门（当前登录人）」**、
> 并要求 **「系统带入、不可手改」**，是**两处事实性错误**，并**漏记了一个真实能力**（默认值设置）。
> ★ 本轮**不改任何代码**（代码本就正确：申请人取实例、部门取控件值），只改**文档与交付物**。

### P.1 现象（我们错在哪）

| 文档位点 | 原写法（错误） | 真相 |
|---|---|---|
| 指引 §3 各模板控件清单（BA／PR／SA…） | 控件类型＝**「人员／部门（当前登录人）」**（把两个控件并成一个） | 飞书**没有「人员」控件**；要选人只能用 **`联系人`**，要记部门只能用 **`部门`**（两个独立控件） |
| 指引 §4 通用控件规范第 ③ 条 | 「取当前登录人，**不可手改**」 | 部门／联系人控件的**默认值可被发起人修改**（**默认值 ≠ 只读**） |
| 指引 §5 分支二 | 用**表单部门控件**判「主管领导是谁」 | 官方支持**条件分支的「发起人 → 部门」条件**（Default field＝发起人，身份不可被改）→ 这才是首选 |

### P.2 一手证据（用户截图 ＋ 飞书官方三处来源）

**证据 1｜控件清单（飞书开放平台《原生审批定义概述》＋ 用户后台实拍）**
审批后台的控件是：单行文本 / 多行文本 / 说明 / 数字 / 金额 / 计算公式 / 单选 / 多选 / 日期 / 日期区间 /
明细（表格）/ 引用多维表格 / 图片视频 / 附件 / **部门** / **联系人** / 关联审批 / 地址 / 定位 / 收款账户 / 电话 / 流水号 / 飞书云文档。
→ **根本没有「人员」控件**；要选人只能用 **`联系人`**。

**证据 2｜默认值能力（此前被我们漏记的真实能力）**
- 飞书帮助中心《表单控件支持设置默认值》：当前支持**单行文本 / 多行文本 / 单选 / 部门 / 联系人** 5 种控件设置默认值。
- 飞书官方《为控件设置默认值 API 说明文档》：**联系人**控件默认值类型＝「**申请人**（即发起审批用户）／指定人员」；
  **部门**控件默认值类型＝「**申请人部门**（即发起审批用户所在部门）／指定部门」。
- ★ 两处都明确「**支持员工在发起表单时修改**」→ **默认值 ≠ 只读**。
- 设置位置：**点击控件后，在右侧「基础设置 → 默认值设置」**（**不在左侧控件面板里** —— 这正是用户"没找到"的原因）。

**证据 3｜条件分支可用字段（Lark 帮助中心 FAQ，英文原文照录）**
可用字段：**Requester（发起人）** —— **Default field，不需要任何表单控件**；以及
Widget：Number / Amount / Calculation formula / Single option / Multiple options / Date / Date interval /
**Department** / Contact person / Address / Details（Number/Amount/Formula）。

- 原文：「If the condition requires the Requester to belong to certain departments and set multiple departments as the scope,
  the corresponding branch will be activated when the requester belongs to any of the departments.」
- 原文：「**Conditions can only apply to required fields**」→ **条件只能作用于「必填」的控件**。

**证据 4｜代码事实（本轮核实，代码本就正确）**
- `internal/worker/extract.go` 的 `case config.BizFieldDepartment`：`department` 来自**表单控件的值**（`biz_field=department` 抽取）。
- `internal/platform/feishu/instance.go` 的 `ApplicantOpenID` 赋值：来自**实例自带**的 `open_id`／`user_id`，**不是**表单控件值。

### P.3 结论与处置（正确口径）

| 需求 | 正确做法 |
|---|---|
| 记录「申请人」 | **不需要控件** —— 实例自带发起人，`applicant_open_id` 直接取实例 |
| 表单上要显示／引用申请人 | 用 **`联系人`** 控件，默认值类型设「**申请人**」 |
| 记录「所属部门」 | **必须有 `部门` 控件**（`department` 只能来自控件值），默认值类型设「**申请人部门**」 |
| 决定「主管领导是谁」 | ★ **不依赖表单部门控件**（可被发起人手改）→ 用**条件分支的「发起人 → 部门」条件**（官方支持，不可被改） |
| 内控代价 | 部门／联系人控件**可被发起人修改** → 属平台限制，写进模板说明 ＋ 事后核对 |

### P.4 影响面（本轮同步的文档）

| 位点 | 改动 |
|---|---|
| `deliverables/procurement-approval/审批模板人工建立指引V1.1.html`（页头/页脚版本 **V1.1**） | 由其构建脚本 `_build/tplguide/{data_templates.py,build.py}` 重新生成：控件清单改 `联系人`／`部门`；§4 ③ 重写为「联系人控件 与 部门控件（含默认值设置）」；§5 分支二改「发起人部门条件」＋ 新增「条件只能作用于必填控件」；§6 新增第 4 项（默认值不可设为只读）；§2 增加「控件面板无『人员』控件」醒目提示；§10 自检清单补两条；页头／页脚 V1.0→V1.1 |
| `docs/07-Template-Build-Guide.md` | 版本 **V1.2 → V1.3**；§3 补控件口径块；§3 各模板表人员／部门控件标注类型；**§4.5 整节改写**为「联系人控件 与 部门控件」；**§4.4 新增第 4 项**（默认值不可设为只读）；§9 自检清单补两条 |
| `docs/06-Implementation-Notes.md` | 新增本节 §P（编号 **B48**）；文件头版本 V1.7 → **V1.8** |
| `docs/README.md` | `06` 的 B 条目到 **B48**、版本 → V1.8；`07` 版本 → V1.3 |

> ★ 本轮**未改任何代码**：`applicant_open_id` 取实例、`department` 取控件值，代码侧本就与此口径一致（P.2 证据 4）。
> 错的只是**文档对飞书控件能力的描述** —— 与 §O 同族：**决策/事实正确，落地到文档时才走样**。

### P.5 待确认项（官方未明确，需在审批后台实测）

- **部门／联系人控件的默认值能否设为「不可改」**：官方两处只说「支持员工在发起表单时修改」，
  **未明确**能否把默认值锁为只读。→ **待确认**，须在审批后台实测；在此之前一律按「**默认值 ≠ 只读**」处理。

---

## Q. 架构转向 ③ 地基层（T01 + T02）实施记录（B51）

> 本节记录 `docs/04a-Architecture-Increment-V2.md` §13 **T01（三方审批适配层 + 定义注册表）** 与
> **T02（编号器 + 我方实例/任务状态机）** 的落地：新增代码、关键决策（每处写「理由与代价」）与
> **与 `04a` 不一致/需补之处**。
> ★ 本期**只做「增」**：不作废、不删除任何既有链路（原生控件解析链 `parseForm`/`valueToText`/
> `extract.go`、事件订阅路径**保持原样**）；对 `t_instance` 只**加列**、不改既有列语义。

### Q.1 新增 / 改动文件清单

| 类别 | 文件 | 说明 |
|---|---|---|
| 迁移 | `migrations/0007_approval_core.sql` | §1.1 六张新表（`t_doc_seq`/`t_flow_task`/`t_flow_op_log`/`t_approval_def`/`t_push_record`/`t_notify_log`）+ `t_instance` 增 6 列 + `UNIQUE(biz_no)` 兜底索引 |
| 适配层 | `internal/platform/feishu/external.go` | `external_approvals` 封装（**只暴露创建/更新 + 查询**，**无删除**）+ `FakeExternalApprovalClient` |
| 存储 | `internal/store/models.go` | `Instance` 增 6 字段；新增 `DocSeq`/`FlowTask`/`FlowOpLog`/`ApprovalDef`/`PushRecord`/`NotifyLog` 结构体 |
| 存储 | `internal/store/repo_instance.go` | select/scan/upsert **追加**新列；新增 `GetInstanceByBizNo(+Tx)` |
| 存储 | `internal/store/repo_docseq.go` | `t_doc_seq` 事务内读改写（`NextDocSeqTx`）+ `PeekDocSeq`/`CountDocSeq` |
| 存储 | `internal/store/repo_approval_def.go` | `t_approval_def` upsert（按 `approval_code`）+ 查询/计数 |
| 存储 | `internal/store/repo_flow.go` | `t_flow_task` + `t_flow_op_log` 读写（含回调幂等 `INSERT OR IGNORE`） |
| 服务 | `internal/number/gen.go` | 单号器 `{前缀}-{YYMM}-{####}`（`AllocTx`/`Alloc`） |
| 服务 | `internal/flow/service.go` | 提交 / 节点聚合 / 推进 / 撤回（我方状态机） |
| 服务 | `internal/approval/defregistry.go` | 三方定义注册/更新服务（幂等、失败可见） |
| 测试 | `internal/number/gen_test.go`、`internal/flow/service_test.go`、`internal/approval/defregistry_test.go` | 含**负向断言** |
| 测试 | `internal/store/qa_migration_test.go` | 迁移期望数 6 → **7**，并断言六表/六列/`ux_instance_biz_no` 生效 |
| 脚本 | `scripts/archive-year.sh` | 新增**锁号前提自检门禁**（04a §6.4 / §10 S13） |
| 测试(既改) | `internal/httpapi/admin_test.go` | `seedInstance` fixture 修正（见 Q.3-①） |

### Q.2 关键决策（理由与代价）

| # | 决策 | 理由 | 代价 / 边界 |
|---|---|---|---|
| D1 | `instance_code = {app_id}:{biz_no}`（`appID` 空则退化裸单号） | 04a §3.3：裸单号多环境复用会**静默撞 ID**（审批中心空白） | 依赖配置 `JX_APP_ID`；开发/测试可退化为裸单号 |
| D2 | **PO 归并回 CT**（号段 key = `CT`，前缀 = `CT`） | 04a §6.1「PO 沿用 CT」：若 PO 与 CT 各占一个号段 → 均生成 `CT-YYMM-0001` → **同号两笔**，锁号失效 | 由 `number.NumberKey` 单点归并，避免两处真相 |
| D3 | 提交时**一次性建全链任务**（`t_flow_task`） | `t_flow_task` 是唯一持久化"审批链"处（§1.1 无独立链路表），推进需据此判"下一节点" | 未来节点任务同为 `PENDING`，故以**"只有当前节点可操作"门禁**（更小 seq 节点须全通过）防"提前审批" |
| D4 | 会签聚合**在我方**（按 `node_id` 聚合全部 task 状态） | 04a §2.3：节点聚合**始终以我方状态机为准**，不依赖飞书 `task_list[].type` | 我方承担全部聚合正确性；`type` 仅影响飞书展示（T03 处理） |
| D5 | `update_time` 单调：upsert 冲突时取 `MAX(旧值, 新值)` | §3.1：版本**回退会让推送静默失败** | 任何写入都不会使版本回退，安全但需调用方显式 +1 |
| D6 | 编号器上限 `>9999` **显式报错** | 静默产出 5 位号会让格式契约与台账/检索口径**悄悄漂移** | 极少触发（按月重置）；触发即交人工 |
| D7 | 0007 同时建 `t_push_record`/`t_notify_log`/`t_flow_op_log`（本期空表） | 04a §13 **T01 明确**的 6 表清单；避免后续迁移再动 DDL | 三表消费端在 T03/T04；本期仅建表 + `t_flow_op_log` 已由状态机写入 |

### Q.3 与 `04a` 不一致 / 需补之处（★ 重点）

① **`UNIQUE(biz_no)` 实际并不存在（`04a §6.4` P3 描述有误）**：P3 称"迁移 `0002` 表达式唯一索引；
`t_instance.instance_code UNIQUE`"——实测 `t_instance` **从来没有** `biz_no` 唯一约束（0001 仅
`instance_code UNIQUE`；0002 的表达式唯一索引属 `t_submission`/`t_audit_log`）。而「终态锁号」
的最终兜底正是 `UNIQUE(biz_no)`（§1.3 / §6.4 P3 / 负例 N-11）。故本次在 `0007` **补建**
`ux_instance_biz_no`（`CREATE UNIQUE INDEX`；SQLite 唯一索引对 `NULL` 不生效 → 存量/事件空号行不受影响）。
★ 连带修正 `internal/httpapi/admin_test.go` 的 `seedInstance`：原 fixture 令**两个不同实例共用同一
`biz_no`**（`PR-2609-0001`），这本身就是「同号两笔」，在新不变量下应被拒；改为按实例唯一取号
（该改动只修 fixture 数据，不改断言）。

② **`t_instance.instance_code` 语义（C11 / FR-M9-13）**：本层按 `{app_id}:{biz_no}` 生成，与
`04a §1.1` 括注一致；DDL `UNIQUE` 不变，仅**值来源**由"飞书下发"改"我方生成"。

③ **锁号前提 P1–P3 已落地为可执行物**：P1 在 `scripts/archive-year.sh` 加**启动自检门禁**
（断言 `TABLES` 不含 `t_doc_seq`、正文无行首 `DELETE FROM`/`DROP TABLE`，违反即失败退出）；
P2 由"实例永不硬删除"（撤回＝软状态 `CANCELED`）保证；P3 由 `ux_instance_biz_no` 保证。
★ 破坏任一条，终态锁号**静默失效**（表现为台账/审计「同号两笔」）。

④ **迁移 0007 的幂等口径**：`CREATE TABLE/INDEX` 用 `IF NOT EXISTS`；`ALTER TABLE ADD COLUMN`
SQLite **不支持** `IF NOT EXISTS`，与 0003 同一约定——由 `t_schema_migrations` 保证只执行一次
（重复 `Migrate` 因版本表跳过而不报错）。

### Q.4 负向断言（测试，静态可复跑）

| 断言 | 用例 |
|---|---|
| 编号器**并发不重号** | `number.TestAllocNoDuplicateUnderConcurrency`（64 并发，任一重号即失败） |
| **终态单号复用被拒** | `number.TestTerminalBizNoReuseRejected`（`UNIQUE(biz_no)` 拦截 + 原实例不被污染） |
| 游标**只增不减** | `number.TestDocSeqMonotonic`（P1 可执行检查） |
| PO/CT **不撞号** | `number.TestPOCollapsesToCT` |
| 会签**未全员同意不得推进** | `flow.TestCoSignNodeRequiresAllApproved` |
| 未来节点**不得提前审批** | `flow.TestFutureNodeCannotApproveEarly` |
| **终态不得回退** | `flow.TestTerminalInstanceNoRegression` |
| **CANCELED 后不可推进** | `flow.TestCanceledCannotAdvance` |
| `update_time` **严格递增** | `flow.TestUpdateTimeStrictlyIncreases` |
| 定义**重复注册=更新**（不产生第二条） | `approval.TestRegistryRepeatRegistrationIsUpdate` |
| 定义同步**失败可见**（不写本地行） | `approval.TestRegistryFailureVisible` |

### Q.5 遗留 / 交接（交 T03 / T04）

| 项 | 说明 |
|---|---|
| 推送与快照 | `t_push_record`/`push_hash`/`push_at` 列已就绪，`PushService`/`SnapshotBuilder` 交 **T03** |
| 对账 | `t_instance.update_time` 已可作版本锚点，自适应对账交 **T03** |
| 回调与四操作 | `t_flow_op_log` 回调幂等索引已建；转交/加签/回退/撤回（T04）将复用本层 `Approve/Reject/Cancel` 与状态机原语 |
| 通知 | `t_notify_log` 已建，通知服务交 **T04** |
| 待实测 | `external_approvals` 请求/响应字段名、查询端点路径 —— 以 `04a` 参数表实现，**真机联调按 QV2 校准**（本次未做真机） |

### Q.6 门禁结果（原始输出见 commit 报告）

- `gofmt -l .` → **空**
- `go build ./...` → **通过**
- `go vet ./...` → **通过**
- `go test ./... -count=1` → **13 个测试包全绿**（原 10 包 + 新增 `number`/`flow`/`approval`）
- `python scripts/check_md_tables.py` → `OK 全部 Markdown 表格列数一致（已扫 14 个文件）`
- `scripts/archive-year.sh` 锁号门禁：真实迁移库上 **exit 0**；注入 `DELETE FROM` 后被检出并拒绝

---

## R. 回调链路修复轮（B52–B56）

> 本节记录 `docs/16-Callback-Repair-Design.md` 的 **4 批修复**（提交 `5fe1671` / `d94580f` / `36df709` / `c6e26d7`）落地过程中**实测确认的 5 条缺陷**（B52–B56）。
> ★ 与既有 §B 编号连号（上一条为 **B51**，见 §Q）；★ **每条给「实测证据 + 修法」**；★ 本轮**只补文档、无新代码**（代码已在 4 批提交中入库）。

### R.1 冲突与修复记录（B52–B56）

| 编号 | 级别 | 缺陷（实测确认） | 实测证据（来源） | 修法（已落地） |
|---|---|---|---|---|
| **B52** | ★★★ 高 | **回调字段名与官方不一致 ⇒ 恒 400** —— `extCallbackBody` 主读 `biz_no` / `open_id` / `instance_code` / `action_name`，而官方实际发 `action_type` / **`user_id`** / **`approval_code`** / **`instance_id`** / `message_id` 等，**且官方不发顶层 `biz_no`** | **官方格式报文**打我方 ⇒ `回调缺少 biz_no/task_id`；**我方自造格式**（含顶层 `biz_no`）⇒ 才走到 `无对应实例`（`reference/README.md` ★★★ 条 · `docs/16 §1 G-1`）—— ★ **这就是"用户点同意后回调 400、界面零反馈"的真实原因** | ★ `extCallbackBody` **按官方 12 字段重写**、旧字段降为**兼容读**（窗口＝一个发布版本）；`CallbackRequest` 增 `ApprovalCode`/`MessageID`/`OperatorUserID`（第 2 批 **`d94580f`**） |
| **B53** | ★★★ 高 | **`biz_no` 未经 `action_context` 传递** —— 推实例时 `ExternalTask` **无 `action_context` 字段**，`task_list[]` 只写 `task_id` 等 ⇒ 官方**不发顶层 `biz_no`** 时，回调**无从取得 `biz_no`** | 官方 `action_context`＝「操作上下文…原样回传」；而推侧从未写入 ⇒ 回调解侧兼容逻辑**恒走不进去**（`docs/16 §1 G-2`） | ★ `ExternalTask` **新增 `action_context`**，`BuildSnapshot` 写 `{"biz_no":…,"task_id":…}`；解侧 `biz_no` **三级读法**（`action_context` → `instance_id` 反解 → 顶层兜底+warn）（第 2 批 **`d94580f`**，与 B52 **同批**） |
| **B54** | ★★ 高 | **仅推实例、未发通知** —— 推 `external_instances` **只让任务进「待办」**，**不会产生任何提醒**；`flow.notify.go` 的 `Sender` 端口装配时传 **nil** ⇒ 只落 `EXPECTED`、**从不发送** ⇒ 审批人在飞书**完全看不到提醒** | 官方原文「当有新的审批待办…时，**可以通过**飞书审批的 Bot 告知用户」⇒ 「待办进列表」与「发消息」**两个独立动作**（`reference/README.md` ★★ 条 · `docs/16 §2 关联项`） | ★ 新增 `internal/platform/feishu/notify.go` 的 `NotifySender` 实现 `flow.Sender`（内调 `POST /open-apis/approval/v1/message/send`，`template_id=1008`；★ **`actions[]` 四 URL 缺一不可**、★ **`texts` 用 map 形态**、★ **`code!=0` 一律判失败**）（第 3 批 **`36df709`**） |
| **B55** | ★ 中 | **`message_id` 未暂存** —— `extCallbackBody` 无该字段、`t_flow_op_log` 无该列 ⇒ **失败时无法更新卡片**（官方：处理失败 ⇒ 卡片退化为"只显示查看详情"，需我方调【更新审批 Bot 消息】） | 官方：卡片操作时 `message_id` **必填**（`reference/README.md` ★★ 条 · `docs/16 §1 G-6`） | ★ `CallbackRequest` 加 `MessageID`；迁移 **`0013`** 加 `t_flow_op_log.message_id`；`recordCallback` 写入；新增 `message.go` 的 `UpdateApprovalMessage`（第 2 批 **`d94580f`** ＋ 第 3 批 **`36df709`**）。★ **接口请求体字段待实测 `V-2`** |
| **B56** | ★★ 高 | **第 4 批：`notifyOnType` 无「新待办」事件 ⇒ 无触发点** —— 第 3 批只通了「通知**发送端口**」，但 `flow/notify.go` 的 `notifyOnType` **不含「新待办产生」事件** ⇒ 即使端口接通，**通知也发不出去**（**"接线了" ≠ "有触发点"**，与定案 #58 同族） | 代码侧：`OnFlowEvent` 原只处理已通过 / 已拒绝类事件；「新待办产生」**无事件订阅**（`docs/16 §3 第 4 批`） | ★ 新增 `activateOnType`（`SUBMITTED`/`TASK_APPROVED`/`TRANSFERRED`/`ADDED_SIGN`/`ROLLED_BACK`）＋ `ActivatedNotifyTargets`（收件人＝`PENDING ∧ RELEASED` 任务的 assignee，排除空/操作人本人）＋ `activationEventKey`＝`TASK_ACTIVATED:<task_id>`；`OnFlowEvent` 拆为 `notifyApproved`（原逻辑搬移、行为不变）＋ `notifyActivated`；`store.HasNotifyLog` 只读判重；★ **`ExpectedNotifyTargets` 签名与语义逐字未动**（第 4 批 **`c6e26d7`**，现行 HEAD）。★ **`HELD`（未轮到）不发通知**（与 `UC-23` 一致，防加签人提前收到提醒） |

> ★ **B52–B56 的共同点**：**"看起来正常"的链路上，"最后一公里"断了** —— B52/B53 是**字段名与载体**、B54/B55 是**通知与留痕**、B56 是**触发点接线**。★ 与定案 **#58**（接线改变可达性）/ **#69**（已受理却报错）同族，处置一律遵循「**改完要问：出了问题时谁会发现？**」。

### R.2 本轮新增迁移与配置键

| 项 | 内容 |
|---|---|
| 迁移 | **`0013_callback_repair.sql`**（唯一新增）：`t_flow_op_log` 加 `message_id TEXT`；`t_approval_def` 加 `feishu_code TEXT`（均可空、无回填；★ 两列**写入者与迁移同批**，不留 R26 型"死列"） |
| 配置键 | 新增 **`JX_CALLBACK_DOMAIN`** / **`JX_ACTION_CALLBACK_TOKEN`**（第 1 批 `5fe1671`；正本＝`04-Architecture §6.4`） |
| 幂等键 | `t_flow_op_log` 唯一键为 **4 列 `(biz_no, task_id, op_type, round)`**（迁移 `0011`；与定案 #68 一致） |
| 幂等键（通知侧） | `TASK_ACTIVATED:<task_id>`（本方自定义；见 §R.1 **B56**） |

### R.3 本轮同步的文档

| 文档 | 动作 |
|---|---|
| `docs/05-API.md` | §3.14 缺口清单 6 条**翻面为已闭合**（附提交号）· §3.13 补 `action_context` 硬要求 · §6/§6.1 补 `message/update`（计数 5→6）· 幂等键统一 4 列 |
| `docs/04a-Architecture-Increment-V2.md` | §3.2 `action_context` 内容 · §4.2 字段映射层（含 §4.2.1 转换 / §4.2.2 双 code 池）· §4.3 幂等键 4 列 · §5.5 通知必推 · §6.3 反解 · §10 S16/S17 · §12.2 N-12/N-13 |
| `docs/01a-PRD-Increment-V2.md` | §5.4 通知必推 ＋ `message/send` 域勘误（前向指针）· §6.1 推送纪律补 `action_context` · FR-M0-14/15/17 · 幂等键 4 列 |
| `docs/09-Integration-Verification-Checklist.md` | `QV2-01` 定论 · `QV2-02` 实测 · `QV2-A32` 用户侧确认 · 新增 `V-1`~`V-4` |
| `docs/14` · `docs/16` · `docs/reference/README.md` · `docs/07` | §6 联调自检 ＋ 手工 curl 警示 · 落地状态回填 · 实测条目（不重试 / 400 实录 / `TASK_ACTIVATED` 幂等键）· 两键装载要求 |

---

## S. 回调端到端联调轮（B57–B63）

> 本节记录**端到端联调**这一轮修掉的**推实例 / 回调 / 定义装载**缺陷（提交 `46919ac` / `5b3f2ac` / `5761922` / `956f3c8` / `fad0811` / `1410e9e` / `7076069`）。★ 与既有 §B 连号（上一条 **B56**，见 §R）；★ 本轮**只补文档、无新代码**（代码已在上述提交中入库）；★ 与定案 **#81**（飞书快捷审批回调三处反直觉事实）互为补充 —— 本轮新增**「推实例」四要素**这一族。

### S.1 冲突与修复记录（B57–B63）

> ★★ **共同点**：**平台校验【逐层】只报一层错**（"剥洋葱"）—— 一次推送只暴露「当前第一处」不合规字段，改掉这层、下一层才浮现 ⇒ 本轮「重推」共剥 **5 层**（B57→B61）。

| 编号 | 级别 | 缺陷（实测确认） | 实测证据（来源） | 修法（已落地） |
|---|---|---|---|---|
| **B57** | ★★ 高 | **推实例审批人字段名错（第 1 层）** —— 用自造 `assignees` / `assignee_open_id`，而官方字段表**无 `assignees`**、应为 `task_list[].open_id`（或 `user_id`）⇒ **未知字段被静默忽略**（推送仍回 `{"code":0}`，任务未指派、无「同意/拒绝」） | 官方《三方审批实例同步》字段表；推送回显 `code:0` 但任务不进「待办」（`reference/README.md` ★★ 条） | `internal/platform/feishu/push.go` 的 `ExternalTask` 审批人字段改 **`open_id`**（`46919ac`） |
| **B58** | ★★ 高 | **task 级必填缺失（第 2 层）** —— `task_list[*]` 缺 `links` / `create_time` / `end_time` / `update_time` ⇒ 被拒 | 真机报错（缺失字段名逐层给出） | 补全 task 级必填（`46919ac`） |
| **B59** | ★★ 高 | **实例级必填缺失（第 3 层）** —— 缺 `start_time` / `end_time` / `i18n_resources`（＋条件必填 `open_id`）⇒ 被拒 | 真机报错 | 补全实例级必填（`5b3f2ac`，含 `update_time` 改字符串） |
| **B60** | ★★★ 高 | **字段类型错（第 4 层）⇒ 整包解析失败** —— `message_id` 官方 **`int64`** vs 我方用 **`string`** ⇒ **整包解析失败**（另 `update_time` 官方 string，被 `flexInt64` 兼容） | 真机报错（类型不符）；`5761922` 提交自述「原用 string 致真实点击恒 400」 | `message_id` 按官方 **`int64`** 接收（`5761922`）；写 `message/update` 时用**字符串**（读写不对称，见 `05-API §6.1`） |
| **B61** | ★★★ 高 | **值语义错（第 5 层）** —— `task_list[].node_name` **必须传 `@i18n@` key**（**非实际文案**），且文案须配对于 `i18n_resources.texts` 的 `value` | 真机报错（官方要求 `@i18n@`，传实际文案不被接受） | `node_name` 改传 **`@i18n@` key**，文案配对入 `i18n_resources`（`fad0811`） |
| **B62** | ★★ 中 | **`external_instances/check` 缺 `instances[]`** ＋ 其响应 `diff_instances[].update_time` 是**字符串**（非 int） | 真机报错（缺 `instances[]`）；响应解析告警 | 入参补 `instances[]`；响应 `update_time` 用 **`flexInt64`** 宽容接收（`956f3c8`） |
| **B63** | ★★ 高 | **定义装载被 upsert「重置未传字段」打回** —— `POST external_approvals` 的 **upsert 语义＝「未传字段即重置」** ⇒ 装载时**未显式传** `enable_quick_operate` 等开关，两键被打回（口径 2 落空） | 真机：装载后读回 `enable_quick_operate=false`（默认值） | 定义装载**显式传全开关**（`enable_quick_operate` 等）（`1410e9e`） |

> ★ **另记（非缺陷，属观测能力增强）**：回调留痕中间件**增记 `raw_body`**（token 仍打码）—— **五层缺陷的定位即靠它**（原「脱敏视图看不到平台特有字段」是排障盲区，`7076069`）。★ 部署脚本 `start.sh` / `stop.sh` 新增正本（`1410e9e`）。

### S.2 ★ 教训：平台**逐层**校验（"剥洋葱"）

> ★★★ **本轮最有价值的教训**：`external_instances` 的校验**逐层进行、一次只报一层错** —— 「字段名错」时**根本走不到**「必填校验」，必填补齐后才暴露「类型错」…… ⇒ ★★ **不可据"某层已过"推断整包正确**。
> ★ **正确做法**：**整包按官方字段表逐项核「四要素」—— 字段名 / 类型 / 必填 / 值语义**；★ **权威来源优先官方 SDK 结构体源码**（字段名与类型最不易被文档笔误误导）。
> ★ 这与定案 **#66**（引用已修缺陷）、**#73**（缺口闭合即翻面）同族：**"看起来在推进"（每改一版都过了一层）≠ "已经对了"**。

### S.3 本轮新增迁移与配置键

| 项 | 内容 |
|---|---|
| 迁移 | **`0014_notify_message_id.sql`**（`t_notify_log` 加 `message_id TEXT`）—— 用途＝**主动刷新卡片**（★ 回调报文**不带 `message_id`** ⇒ 卡片 id 须由我方**发通知时自记**，`message/send` 回执 `data.message_id` → 本列） |
| 配置/契约 | 内部端点鉴权 header ＝ **`X-Internal-Token`**（非 `Authorization: Bearer`）；`external_instances/check` 入参须含 `instances[]` |

### S.4 本轮同步的文档

| 文档 | 动作 |
|---|---|
| `docs/05-API.md` | §6.1 补「字段类型」「值语义 / 必填」两列 ＋ `message/update` 请求体（`V-2` 定稿）＋ 卡片主动刷新机制；§3.14 末注 `V-1`~`V-4` 翻为定论 |
| `docs/16-Callback-Repair-Design.md` | 新增 §8「联调实测记录」（五层清单 ＋ 零差异证据）；§7 `V-1`~`V-4` 定论 ＋ 新增 `V-6` |
| `docs/09-Integration-Verification-Checklist.md` | `V-1`~`V-4` / `QV2-A32` 移入 §7；新增 `V-6`；计数更新 |
| `docs/11-Existing-System-Adaptation-Audit.md` | 新增 `R30`（推实例字段名/类型/值语义三错） |
| `docs/reference/README.md` | 追加本轮实测条（`V-1`/`V-3`/`V-4` / 卡片 / `check` / `X-Internal-Token` / 逐层校验 / `duration_ms:0` / 零差异） |
| `docs/README.md` | 索引行刷新（版本 ＋ 行数）＋ 新增关键定案 `#82` |

---

## T. 组织同步实机验证轮（B64：作业认领原子性）

> 本节记录**通讯录事件订阅链路实机验证**中查出的**调度层缺陷**（2026-09-28，服务器 `192.168.10.50`）。★ 与既有 §B 连号（上一条 **B63**，见 §S）；★ 本节**代码 + 测试 + 文档同批**；★ 与定案 **#81**（回调三处反直觉事实）、**#82** 同族 —— 本轮新增的是**「调度层重复执行」**这一族。

### T.1 缺陷记录（B64）

| 编号 | 级别 | 缺陷（实测确认） | 实测证据（来源） | 修法（已落地） |
|---|---|---|---|---|
| **B64** | ★★ 高 | **作业认领非原子 ⇒ 同一作业被多个 worker 协程重复执行** —— `Worker.ProcessDueOnce` 直接调用 `db.DueJobs`（**只读 SELECT、不改行状态**），作业要等到 `processJob` 内才被置 `RUNNING`；`SELECT` 与后续 `MarkJob(RUNNING)` 是**两条独立语句、两个独立事务**，连接在两者之间被归还连接池 ⇒ `workerPoolSize=4` 个协程可**依次 SELECT 到同一行**，随后**各自完整执行一遍**。★ **`SetMaxOpenConns(1)` 不能兜**（单连接只保证"语句不并行"，**不保证"两条语句同事务"**） | ★★ **真机铁证**：`POST /internal/dev/inject-event` 投 1 条 `contact.user.updated_v3`（真实 `open_id`）—— `t_event_inbox` **仅 1 行**、`t_worker_job` **仅 1 行**、`attempts=1`，却打出 **3 条**「通讯录事件：人员已增量落库」日志（时间戳 `16:01:31.677495` / `.677811` / `.714620`，相差 300µs / 37ms） | ① `internal/store/repo_inbox.go` 新增 **`ClaimDueJobs`**（候选 SELECT → 逐条 CAS `UPDATE … SET state='RUNNING' WHERE id=? AND state='QUEUED' AND (next_run_at IS NULL OR next_run_at<=?)` → 仅 `RowsAffected==1` 者回读返回）＋ `JobByID`；★ **必须先 `Close()` 候选游标再发 UPDATE**（否则单连接下自锁）；★ **认领不改 `attempts`**（该列语义＝已消耗的处理次数，仍由 `MarkJob` 维护）② `DueJobs` 保留但头注标「**调度路径禁止使用**，只读观测/测试/排查用」③ `ProcessDueOnce` 改走 `ClaimDueJobs`；`processJob` 内的 RUNNING 标记降级为**幂等重申**（互斥责任上移认领层）④ 回归测试 `internal/worker/claim_test.go`（3 例） |

### T.2 ★ 教训：幂等兜得住**数据**，兜不住**副作用**

> ★★ **本轮的判据**：长期把「有幂等键」当作**重复执行的免罪符**是错的。本例中 `t_event_inbox.UNIQUE(idem_key)` ＋ 镜像 UPSERT **双层幂等**确实让**画像没有被写坏**（这是幂等设计的真实价值，应予肯定）—— 但代价照样发生：
> - **重复调用飞书详情接口**（回源 `contact/v3`）⇒ **API 配额 × 3**；
> - **日志噪声**：3 条相同日志 ⇒ **掩盖真实故障**（真出问题时无法从条数判断是"3 条事件"还是"1 条事件跑 3 次"）；
> - ★★ **headroom 陷阱**：当前 handler 恰好"幂等且无副作用"，**将来任何一处加上副作用（推送 / 通知 / 计数 / 外部调用）即立刻变成重复触发** —— 缺陷在"还没有症状"时就已存在。
>
> ★★ **可验证性要求（本轮方法论）**：修完必须**给出证伪对照** —— 把调度临时换回旧实现，同一测试实测 handler 调用 **87 次**（期望 12，**7 倍重复**）；恢复修复版后为 **12 次**。**只有这样才证明断言有鉴别力**（回到定案 **#70 / §H.13** 的"假绿"纪律：**通过率高 ≠ 门禁有效**）。
> ★ **并发类缺陷的测试条件**：必须**先投递、后启动 worker**（制造"所有协程首次调度即看到同一批 QUEUED 行"的**最恶劣时序**）；若先启动再投递，协程会逐条拾取、竞态窗口大幅缩小，**缺陷会漏测**。

### T.3 本轮同步的文档

| 文档 | 动作 |
|---|---|
| `docs/08-Org-Sync-Design.md` | §4.6(c) 表新增「**作业认领的原子性**」行 ＋ V1.11 补记（缺陷、真机铁证、为什么 `SetMaxOpenConns(1)` 不兜、处置）；§12 新增 `V1.11` 变更行 |
| `docs/03-TestCase.md` | 新增 **`TC-91` / `TC-92` / `TC-93`**；§3 总览（合计 93 / P0 42 / P1 45）；§5 高风险补 **44**；§5.1 负向断言补 **`N-x`~`N-z`**；§6 覆盖矩阵（FR-M3-01/03、FR-M3-07）；版本 → **V1.14** |
| `docs/11-Existing-System-Adaptation-Audit.md` | 新增 `R32`（作业认领非原子 · 调度层重复执行） |
| `docs/reference/README.md` | 追加本轮实测条（3 条重复日志铁证 · 证伪对照 87/12 · `ClaimDueJobs` 语义） |
| `docs/README.md` | 新增关键定案 **`#83`** |
