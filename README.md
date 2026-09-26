# jx-procurement-platform

湖南江熙新材料科技 · **采购与费用审批平台**（自建侧）

> 方案 B / 模式 A：**审批流转仍在飞书原生审批引擎内完成**，本系统只做
> 「接收事件 → 落库 → 台账 / 看板 / 行·列权限 / 报送凭证包 / 审计留痕」。

## 一句话定位

飞书免费版有三项绕不过或绕得很累的限制——**不支持行·列权限**、**单表 2,000 行**、
**仪表盘只能挂 1 张数据源表**。本系统的存在理由就是补齐这三项，**审批侧一点不重造**。

## 技术栈（已锁定）

| 层 | 选定 |
|---|---|
| 后端 | Go 1.24（本机 1.24.5）/ Echo v4 `v4.13.4` |
| 数据库 | SQLite（WAL）· **纯 Go 驱动 `modernc.org/sqlite v1.39.1`**（本机无 gcc，禁用 cgo 驱动） |
| 前端 | Vue3，构建产物经 `//go:embed` 内嵌，交付为单二进制 |
| 图表 | ECharts |
| 对象存储 | 云侧 S3 兼容（主）+ RustFS（异地备份，每日增量） |
| 部署 | 境外云主机 · **单实例** · systemd |
| 登录 | 飞书免登（open_id → 角色） |

## 硬约束（写在代码里，不是写在文档里）

1. **单实例**：长连接在集群模式下**不广播**（多实例只有一个随机实例收到事件）。禁止多副本。
2. **必须幂等**：事件未在 **3 秒内**处理完会超时重推；平台按「**至少投递一次**」语义（成功接收也可能重推），按
   **15 秒 / 5 分钟 / 1 小时 / 6 小时** 重推、**最多 4 次**（**最长窗口约 7.1 小时**）。
   **幂等键 = 事件级唯一 ID**（2.0 版 `header.event_id` / 1.0 版顶层 `uuid`）；投递幂等窗口 **≥ 7.1 小时**。
   ★ **不得用 `instance_code` + `status`**——驳回重提会让同一实例**再次进入 PENDING**，该组合会把第二次 PENDING
   幂等吞掉、状态再也回不去；该组合仅用于**状态收敛**（`internal/worker/status.go`），全量变迁落**追加式状态历史表**。
3. **必须先订阅**：除配置事件订阅外，**必须按 `approval_code` 调用「订阅审批事件」接口**，
   否则一条事件都不会推送，且现象是「静默无数据」。启动自检 + 告警。
4. **事件处理路径必须极短**：收到事件 → 写「待处理」记录 → 立即返回；详情拉取走异步。
   飞书**部分事件为「有序事件」**（前一事件被成功消费后才推下一事件），**同步路径绝不阻塞**，否则会卡住整条事件流。
5. **对账补拉兜底**：每日「批量取实例 ID」求差 + 「取实例详情」补录，把事件丢失从**故障**降为**延迟**。

## 全案只用 4 类飞书接口

| 用途 | 接口 |
|---|---|
| 订阅审批事件 | `POST /open-apis/approval/v4/approvals/:approval_code/subscribe` |
| 取实例详情 | `GET /open-apis/approval/v4/instances/:instance_id` |
| 批量取实例 ID（对账） | `GET /open-apis/approval/v4/instances` |
| 附件上传 / 下载 | `POST /open-apis/approval/openapi/v2/file/upload` 等 |
| 事件接收 | 长连接 WebSocket（`approval_instance` / `approval_task`） |

**设计纪律：用事件订阅，绝不轮询。**（事件订阅不计入 API 调用量，IM 发消息与审批 API 计费）

**口径说明**：「4 类」指 4 类 REST 调用（订阅 / 取详情 / 批量取 ID / 附件），其中附件类含 **upload + download 两个调用**；
事件接收走长连接、不计入 API 调用量。代码中**不存在任何「创建实例 / 发起审批 / 阻塞回调」路径**。

## 已知代价（不得含糊）

发起入口为**飞书原生发起**，因此**事前硬校验（备付金是否充足、防拆分、次数上限）
无法在发起时拦截**，只能做成台账红标 + 审批人参考。须在制度与配置工具表中同步注明这条弱化。

## 目录结构

```
docs/     需求与设计文档（PRD / UseCase / TestCase / 架构 / 接口 / 实现说明 / 模板建立指引）
cmd/      可执行入口
internal/ 业务实现
web/      前端源码（构建产物 embed 进二进制）
scripts/  构建、备份、部署脚本
```

## 子命令

```bash
jxapproval serve                                   # 启动服务（默认；供 systemd 使用）
jxapproval seed                                    # 幂等播种 Q3 默认权限口径
jxapproval import-config --check <config.json>     # 只校验配置映射（模板建好后先跑这个）
jxapproval import-config <config.json>             # 导入四类映射 + 回读自证
jxapproval version | help
```

> **配置映射**（`approval_code` / `field_id` / `ledger_type` / `threshold`）是**唯一**「错一处就全线静默无数据」
> 的口子——模板订阅不到事件时系统不报错，只是永远没有数据。故导入带**严格校验**（占位符 / 白名单 /
> 非实例级台账一律拒绝）、**幂等**、以及按 `approval_code` 的**回读自证**。
> 样例见 `docs/reference/config-mapping.sample.json`，建模板步骤见 `docs/07-Template-Build-Guide.md`。

## 相关产物（非本仓库）

- `费用管理办法V3.0.html` · `审批流程图集V3.0.html` · `配置工具表_v3.0.xlsx` · `飞书适配性评估V1.9.html`
- `技术方案书V1.0.html`（本仓库的输入，位于 WorkBuddy 交付目录 `deliverables/procurement-approval/`）

## 版本

- 开发进度与提交记录见 `docs/README.md` 与 `docs/06-Implementation-Notes.md`。
- 最近阶段：S0/S1 地基 → S2 实例闭环 + 系统管理后台 → 集成轮（看板 M5 / 备付金 M1 / 报送 M6）→ 第二轮对抗性复核修复 → M1 集团报销跟踪（FR-M1-02）/ M4 变更链回溯（FR-M4-07）/ 前端台账类型键纠正（FR-M4-01）/ 年度归档与日志滚动（FR-M4-08、FR-M8-08）→ **模板建立配套轮**（配置映射导入 + 规范字段抽取 FR-M2-08，修复两个「静默无数据」P0）→ **Q14 定案实施轮**（六项编码：`L07` GR+QC 各写一行+查询侧关联 / `L11` 派生视图不落行 / 变更链键名收敛 / `t_ledger_field_def` 补写入端口并作写接口白名单 / 报送行级权限按真实列重建 / 看板标量键同投影）。
- **Q14 已闭合**（原阻塞级别：中）。配置侧＝`t_permission_rule.writable_fields`（纯配置，空＝只读）；编码侧六项见 `docs/06-Implementation-Notes.md` §K。
- **下一步（项目最后一步）**：人工在飞书审批后台建 11 张模板，然后采集映射 → 导入 → **逐模板订阅**，见 `docs/07-Template-Build-Guide.md`。
- 远端：`git@github.com:chadhao/jx-procurement-platform.git`（分支 `main`；**未经用户明确要求不推送**）。
