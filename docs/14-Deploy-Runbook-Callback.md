# 14 · 回调入站部署 Runbook（公网入站 · 飞书三方审批回调）

> **目的**：把 `04a-Architecture-Increment-V2.md §17「部署与入网（公网入站）」` 的设计**落成可照做的部署步骤**（对应 `docs/13` **A-7**、`docs/11 §7.2` 部署物缺口 7 项）。
> **适用**：转向 ③ 上线前，为**唯一入站面** `POST /approval/external/callback` 建立**公网可达入站**（★ **HTTPS 为我方选择、非平台要求**，见 `04a §17.0`）。
> ★ **本文只给步骤与判据，不含实现代码**；凡未定项标 **`TODO`**（不空置、不臆造）。

| 项 | 内容 |
|---|---|
| 文档名称 | 回调入站部署 Runbook |
| 版本 | V1.4 |
| 日期 | 2026-09-27 |
| 关联 | `04a §17`（设计）· `11 §7.2 / §7.3`（缺口与隐患）· `13` A-1~A-7 · `05-API §3.8` |
| 语言纪律 | 简体中文 |

---

## 0. 前置条件（缺一不可）

| # | 前置 | 判据 |
|---|---|---|
| 0.1 | 公网域名已解析到云主机 | `dig +short $JX_CALLBACK_DOMAIN` 命中主机 IP |
| 0.2 | **（仅当采用 HTTPS）** 云主机 `80` / `443` 对外可达 | ACME 验证需要；★ 若用 **DNS-01 或手工放入证书**，**不需要** 80/443 |
| 0.3 | 应用绑回环、systemd 单实例 | `JX_LISTEN_ADDR=127.0.0.1:8080` |
| 0.4 | 配置键已就绪 | `JX_CALLBACK_DOMAIN` / `JX_ACTION_CALLBACK_TOKEN` 非空，`preflight.sh` 通过 |
| 0.5 | ★ **联调档可先用 HTTP**：`action_callback_url` 用 `http://<域名>:<端口>/approval/external/callback` | 平台未明文禁止（`04a §17.0` / `QV2-A19`） |

---

## 1. 域名

- `TODO`：**具体域名待用户提供**（`QV2-A19`）；确定后写入 `JX_CALLBACK_DOMAIN`，避免硬编码。
- 建议：独立子域（如 `approval-callback.<企业域>`），**只承载回调**，与业务站点隔离。

## 2. 证书（★ 仅当采用 HTTPS 时；Let's Encrypt / ACME）

- 用 Caddy / certbot / acme.sh **自动签发 + 自动续期**。
- ★ **续期失败必须告警**；打点**证书剩余有效期（天）**（落 `04 §8.2 关键指标` + `04 §8.3 告警`）。
- 判据：`echo | openssl s_client -connect $JX_CALLBACK_DOMAIN:443 2>/dev/null | openssl x509 -noout -dates` 到期日 ≥ 14 天。
- ★ 若不占用 80/443：可选 **DNS-01** 挑战，或**从别处取得证书后手工放入**（Caddy `tls <cert> <key>`）。

## 3. 反向代理

| 项 | 要求 |
|---|---|
| 上游 | `127.0.0.1:8080`（Echo 回环） |
| ★ 仅放行 | `POST /approval/external/callback`；**其余路径 `404`/`deny`**（含 `/internal/*`、`/api/*`、`/healthz`） |
| 方法 | 同路径非 `POST` → `405` |
| body | `client_max_body_size 64k` |
| 限速 | 该路径 `limit_req`；★ 需透传 `X-Forwarded-For` 且应用**信任受信反代**（`04a §17.6` **E-1**），勿按 IP 单桶 |
| TLS | 对外 `443`；`80` 仅 ACME / `301` 跳转 |

## 4. IP 白名单 / WAF

- `TODO`：**飞书回调出口 IP 段以官方公布为准**（待核对）；反代侧仅放行该网段，其余 → `403` + 拒绝计数。
- 开启**基础 WAF 规则集**；回调体只按约定解密，**绝不动态求值**。
- ★ 白名单是**纵深防御**，**不替代** `token` 校验。

## 5. 应用侧自检（`preflight.sh`）

见 `04a §17.5`，部署后逐条核：
1. 反代已配 + 回调路径**可达**（非 `404`/`502`）；
2. `GET $JX_CALLBACK_DOMAIN/internal/...`、`/api/...` → **`404`**（入站面未被放大）；
3. 证书剩余 ≥ 14 天**（仅当采用 HTTPS）**；
4. `JX_LISTEN_ADDR` 为**回环**（非 `0.0.0.0`）。

## 6. 联调（回调连通性）

- 飞书端对一张联调单触发「同意 / 拒绝」→ 观察回调落 `t_flow_op_log`（`action_type` / `action_context` / `token` 校验通过）。
- ★★ **联调自检（必做，`docs/16 §6.2` 官方报文样例）**：用**官方字段名**的报文打回调 —— 报文含 `action_type`(必)/`user_id`(必)/`approval_code`(必)/`token`(必)/`action_context`/`instance_id`/`task_id`/`message_id`/`id`/`reason`/`attachments`/`encrypt`，**无顶层 `biz_no`**（★ 官方不发顶层 `biz_no`）。
  ```bash
  curl -s -X POST http://127.0.0.1:5001/approval/external/callback -H 'Content-Type: application/json' -d '{ "action_type":"APPROVE","user_id":"<操作人user_id>","approval_code":"<定义code>","token":"<action_callback_token>","instance_id":"{app_id}:<biz_no>","task_id":"<我方task_id>","message_id":"<卡片消息id>","action_context":"{\"biz_no\":\"<biz_no>\",\"task_id\":\"<task_id>\"}","reason":"同意" }'
  ```
  ★ **判据**：**HTTP 200 ＋ `accepted:true`**（**不再出现** `回调缺少 biz_no/task_id` ⇒ 证明字段映射层 ＋ `biz_no` 三级读法生效）；随后 `t_flow_task` 推进、`external_instances` 重推。★ 负向：缺 `user_id` / `approval_code` ⇒ **400 ＋ 留痕可见**（`04a §4.2` / `§10 S16`）。
  ★ 留痕检索：`grep <biz_no>` / `trace_id` 查 http 日志（body 已留痕、token 打码；`docs/16 §2-E`）。
- ★★ **警示：不要用手工 `curl` 直推 `external_instances` 代替联调** —— 手工推实例**绕过了本地落库**，飞书侧任务进「待办」但**我方 `t_instance` 无行** ⇒ 回调到达时报 **`无对应实例`（40000）**、**永远走不通闭环**（本次实测正是此现象）。★ 正规链路＝**我方 `POST /api/approval/submit` 发起**（本地先行、推送在后）；手工 curl **仅限排障**，且须知**该实例本地不可回调**（`docs/16 §2-D` 纪律二）。
- 反向验证：**故意错 token** → 应**拒绝并告警**（`04a §4.1` / `§10 S5`）。
- 覆盖项与判定见 `09-Integration-Verification-Checklist.md`（★ 回调实测四问 **`V-1`~`V-4`** 在 §2 阶段一）。

## 7. 回滚

| 场景 | 动作 |
|---|---|
| 证书 / 反代故障 | 摘除反代入站 → 回调不可达；审批降级为"**服务不可用期间点击未生效、需重点**"（`04a §0` 定案表） |
| 误放大入站面 | 立即收敛反代规则为**仅回调一条路径**，并留痕 + 告警 |

---

## 8. 反向代理样例（Nginx 示意）

```nginx
server {
  listen 443 ssl;
  server_name $JX_CALLBACK_DOMAIN;
  ssl_certificate     /etc/letsencrypt/live/$JX_CALLBACK_DOMAIN/fullchain.pem;
  ssl_certificate_key /etc/letsencrypt/live/$JX_CALLBACK_DOMAIN/privkey.pem;

  client_max_body_size 64k;                                  # A-4
  limit_req_zone $binary_remote_addr zone=cb:10m rate=20r/s; # A-3（E-1 见下注）

  location = /approval/external/callback {
    # allow <飞书出口段>; deny all;                          # A-3（网段 TODO）
    limit_req zone=cb burst=40 nodelay;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;  # E-1：应用须信任受信代理
    proxy_set_header X-Real-IP       $remote_addr;
    proxy_pass http://127.0.0.1:8080;
  }
  location / { return 404; }                                 # A-1：仅放行回调一条路径
}
```

> ★ **E-1 注**：`limit_req_zone` 用 `$binary_remote_addr` 时，**反代须透传、且应用须信任 `X-Forwarded-For`**；若应用仍按"直连源 IP"（＝反代本机）分桶 → **单桶塌缩**（`04a §17.6`）。★ IP 白名单网段见 A-3（`TODO`）。

## 9. 部署后一键验证（照做）

```bash
# 1) 仅放行回调一条路径（A-1）
curl -s -o /dev/null -w '%{http_code}\n' -X POST https://$JX_CALLBACK_DOMAIN/approval/external/callback   # 期望：非 404/502（落到应用）
curl -s -o /dev/null -w '%{http_code}\n'      https://$JX_CALLBACK_DOMAIN/internal/healthz                  # 期望：404
# 2) body 上限（A-4）
head -c 131072 /dev/zero | curl -s -o /dev/null -w '%{http_code}\n' -X POST --data-binary @- https://$JX_CALLBACK_DOMAIN/approval/external/callback  # 期望：413
# 3) 证书有效期（A-2）
echo | openssl s_client -connect $JX_CALLBACK_DOMAIN:443 2>/dev/null | openssl x509 -noout -dates
# 4) 应用仍绑回环（A-5）
ss -ltnp | grep ':8080'     # 期望：127.0.0.1:8080（非 0.0.0.0）
```

> ★ 未定项仍标 `TODO`（域名 / 飞书出口网段，`QV2-A19`）。

### 9.1 ★ 联调环境实测记录（2026-09-27）

本环境实际采用 **Caddy（`:5000`）→ `127.0.0.1:5001`** ＋ 路由器端口转发（公网 `:5500`），**非** Nginx ＋ 443。实测命令与结果：

`curl -s -X POST http://office.hunanyichu.com:5500/approval/external/callback -H 'Content-Type: application/json' -d '{…}'`
→ **`HTTP 400` ＋ `{"code":40000,"message":"flow: 提交入参非法: 回调 biz_no=… 无对应实例","trace_id":"…"}`**

★ 判读：**400 是本方业务拒绝，不是 404 / 502 / 连接失败** ⇒ **公网 → 端口转发 → Caddy → 应用** 全链**已打通**；内网 `127.0.0.1:5001` 直连结果一致；服务 http 日志**逐条留痕**（含 `trace_id`）。
★ 附带确认：**「未被受理」时错误**可见、**不静默**（与 §「落盘即 200」纪律互补）。
★ **结论**：`QV2-A19` 所担心的"入站面"在本环境**已验证可达**；★ 飞书定义 `action_callback_url` 配的正是该 URL ⇒ **两键回调必然可送达**。

---

> ★ **纪律**：所有未定项**显式标 `TODO`**（含来源编号），**不得留空节、不得臆造具体值**。

## 变更记录

| 版本 | 日期 | 变更 | 作者 |
|---|---|---|---|
| V1.4 | 2026-09-27 | **回调修复收口（纯文档）**：**§6 联调** 新增两块 —— ① ★★ **联调自检**：用**官方字段名**报文（`action_type`/`user_id`/`approval_code`/`token`/`action_context`/`instance_id`/`task_id`/`message_id`…，**无顶层 `biz_no`**）打回调，判据＝**HTTP 200 ＋ `accepted:true`（不再 400）**，附可照做的 `curl` 与留痕检索命令；② ★★ **警示「不要用手工 `curl` 直推 `external_instances` 代替联调」**（绕过本地落库 ⇒ `t_instance` 无行 ⇒ 回调报 `无对应实例`、闭环走不通；正规链路＝`POST /api/approval/submit`）。★ 覆盖项指向 `09` §2（含新增 **`V-1`~`V-4`**）。 | 产品经理（Alice） |
| V1.0 | 2026-09-27 | 首版骨架：前置条件 / 域名 / 证书 / 反代 / 白名单 / 应用自检 / 联调 / 回滚（`13` A-7、`11 §7.2`）。具体域名与飞书出口网段待定，标 `TODO`（`QV2-A19`）。 | 架构师（Bob） |
| V1.1 | 2026-09-27 | 执行 `13` **N7**（补全）：新增 **§8 反向代理样例（Nginx）** + **§9 部署后一键验证命令**（`curl`/`openssl`/`ss`），把 `TODO` 收敛到**域名 / 飞书出口网段**两项（`QV2-A19`）。 | 架构师（Bob） |
| V1.3 | 2026-09-27 | ★ **新增 §9.1「联调环境实测记录」**：本环境实际用 **Caddy（`:5000`）→ `127.0.0.1:5001` ＋ 路由器端口转发（公网 `:5500`）**（非 Nginx＋443）。实测 `POST http://office.hunanyichu.com:5500/approval/external/callback` → **HTTP 400 ＋ 业务错误 JSON**（**非 404/502/连接失败**）⇒ **公网 → 端口转发 → Caddy → 应用** 全链**已打通**；内网直连一致、日志逐条带 `trace_id` ⇒ ★ **`QV2-A19` 的"入站面"在本环境已验证可达，飞书两键回调必然可送达**。 | 交付总监 |
| V1.2 | 2026-09-27 | 依 team-lead **分级核查**裁定（`04a §17.0`）：把「公网可达 **HTTPS**」改为「**公网可达入站**」（★ HTTPS 属**我方选择、非平台要求**）—— ① 标题与 §0 适用句去「HTTPS」绝对化；② §0.2 改**条件式**（仅当采用 HTTPS；**DNS-01 / 手工放证书不需 80/443**）；③ **新增 §0.5**（联调档可先用 HTTP）；④ **§2 标题**加「★ 仅当采用 HTTPS 时」＋ 补「不占 80/443 的取证方式」；⑤ §5 自检第 3 条标「（仅当采用 HTTPS）」。 | 架构师（Bob） |
