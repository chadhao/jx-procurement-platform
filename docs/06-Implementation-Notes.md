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
| **B2（高）** | 采纳：Q3 未定期间按 PRD §4.2 建议值落规则表，配置驱动 | 待 Q3 拍板后回归；**Q3 列入高阻塞待拍板** |
| **B3（中）** | 采纳：版本钉死 `echo v4.13.4` / `sqlite v1.39.1`（Go 1.24 兼容） | 已随 README 技术栈表更新 |
| **B4（中）** | 采纳：表达式唯一约束改 `CREATE UNIQUE INDEX` | 语义等价，无行为差异 |
| **B5（中）** | 采纳：「4 类」口径；代码无创建实例路径 | 已随 README 口径说明更新 |
| **B6（低）** | 采纳：入口加子命令分发 + `scripts/preflight.sh` | 已落地 |
| **B7（低）** | 采纳：`flock` 内核级释放 + stale-lock 提示（不阻断） | 已落地 |

**结论**：S0/S1 地基代码通过独立复核（`gofmt` 空 / `go build` / `go vet` / `go test ./...` / `npm run build` 全绿）
与运行时冒烟（第二实例 exit=1；同一 `event_id` 注入 3× → inbox=1 / job=1；缺事件 ID → 400 + 告警）。**予以提交。**

