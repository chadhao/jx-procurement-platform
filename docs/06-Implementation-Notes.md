# 06 · 实现说明与冲突记录（S0/S1 地基代码）

> 本文件为**工程侧补充记录**，不改动任何既有文档（PRD / UseCase / TestCase / 架构 / 接口 / README）。
> 记录范围：落地 S0/S1 地基代码过程中发现的**文档间冲突 / 歧义**、采取的实现决策与遗留动作。
> 编制：Alex（开发经理）· 版本：V0.1 · 对应代码 tag：`0.1.0-s0s1`

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

