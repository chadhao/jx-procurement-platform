# 14 · 回调入站部署 Runbook（公网入站 · 飞书三方审批回调）

> **目的**：把 `04a-Architecture-Increment-V2.md §17「部署与入网（公网入站）」` 的设计**落成可照做的部署步骤**（对应 `docs/13` **A-7**、`docs/11 §7.2` 部署物缺口 7 项）。
> **适用**：转向 ③ 上线前，为**唯一入站面** `POST /approval/external/callback` 建立**公网可达入站**（★ **HTTPS 为我方选择、非平台要求**，见 `04a §17.0`）。
> ★ **本文只给步骤与判据，不含实现代码**；凡未定项标 **`TODO`**（不空置、不臆造）。

| 项 | 内容 |
|---|---|
| 文档名称 | 回调入站部署 Runbook |
| 版本 | V1.7 |
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
- 覆盖项与判定见 `09-Integration-Verification-Checklist.md`（★ 回调实测四问 **`V-1`~`V-4`** **已定论（2026-09-28 真机）**，结论留痕在 **§7**；原「在 §2 阶段一」已作废）。

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

### 9.2 ★★ 启停操作要点：`start.sh` **必须重定向**，否则日志全丢（2026-09-28 实测教训）

> ★★★ **踩坑实录**：用 `ssh host './start.sh'` **直接**起服务（脚本不带重定向），进程虽起得来、HTTP 也正常响应，但**日志文件一行不写**。
> ★ **根因**：`start.sh` 的最后一行为 **`exec ./jxapproval`**（**前台形态**，见脚本头注用法：`前台观察日志 ./start.sh；后台 nohup ./start.sh >/dev/null 2>&1 &`）。前台形态下 **stdout/stderr 继承调用者的管道** —— 经 ssh 调用即继承 **ssh 的 pipe**。
> ★ **现场取证**（判据：**看进程的 fd，不看日志文件**）：
>
> ```bash
> PID=$(pgrep -x jxapproval); ls -l /proc/$PID/fd/1 /proc/$PID/fd/2
> # ✗ 错误形态：fd 1 -> pipe:[…]          （日志进了 ssh 管道 ⇒ 全丢）
> # ✓ 正确形态：fd 1 -> /home/…/logs/app.log
> ```
>
> ★★ **危害**：**看起来一切正常**（进程在、端口通、接口有响应），但**日志为空** ⇒ **排障时无据可依**，且**联调现场会误判为"事件没推过来 / 我方没处理"**（本次即因此多绕一轮）。
> ★ **正确起法**（后台 + 显式重定向 + `< /dev/null` 断输入 + `setsid` 脱会话）：
>
> ```bash
> cd ~/services/jxapproval
> setsid nohup ./start.sh >> logs/app.log 2>&1 < /dev/null &
> sleep 3
> PID=$(pgrep -x jxapproval); ls -l /proc/$PID/fd/1   # 必须指向 logs/app.log
> ```
>
> ★ **注意副作用**：**不带重定向**的起法会让 `ssh` **不回话**（子进程一直持有 ssh 的 stdout ⇒ 会话看起来"卡住"）—— 这本身就是"日志进了管道"的旁证。
> ★ 与既有教训同族：`start.sh` 必须 `set -a; . ./.env; set +a`（**缺它则丢全部环境变量**）—— **"能起来" ≠ "起对了"**。

### 9.3 ★★★ 环境硬前置：`/etc/resolv.conf` 抖动会让**整条飞书链路时好时坏**（2026-09-28 实测 · **本环境已发生**）

> ★★★ **本文档最容易被忽略、却最致命的一条**：`/etc/resolv.conf` 若被周期性重写成**不含 `nameserver`** 的版本，
> 则**所有出方向调用随机失败**，且**表现像"平台/代码有问题"**，极易误判。

**实测现象（本环境，`192.168.10.50`）**：`resolv.conf` 在两种状态之间**每 15–30 秒翻转一次**：

| 状态 | 大小 | `nameserver` 行数 | md5 前缀 | 内容要点 |
|---|---|---|---|---|
| **A 正常** | 166 B | 1 | `dc5c4d9f` | `# Generated by dhcpcd from enp1s0.dhcp` + `domain lan` + `nameserver 192.168.10.1` |
| **B 坏** | 114 B | **0** | `b43fb671` | `# Generated by dhcpcd`（**无 `from enp1s0.dhcp`**）+ `domain` / `nameserver` **全无** |

**为什么它会打挂应用**：★ **Go 的 `net` 解析器在 `/etc/resolv.conf` 没有任何 `nameserver` 行时，会回退到内置默认服务器 `127.0.0.1:53` 与 `[::1]:53`**（且尝试全部失败后报的是**最后一个**）⇒ 错误长这样：

```
dial tcp: lookup open.feishu.cn on [::1]:53:
  read udp [::1]:…->[::1]:53: read: connection refused
```

★★ **看到 `on [::1]:53` 或 `on 127.0.0.1:53` 基本可直接判定"读到的 resolv.conf 是空的"** —— 这不是 DNS 服务器故障，而是**本地配置文件被写空**。

**实测的实际影响（本环境当日）**：
- **审批对账**（每 5 分钟一拍）连续失败多次；
- ★★ **长连接**：`17:31:19` 断开 → `17:31:35` / `17:33:05` / `17:34:35` / `17:36:05` **连续 4 次重连失败** → `17:37:36` 才恢复 ⇒ **约 6 分钟完全收不到任何事件**；而它**每 ~5 分钟就断一次** ⇒ **入站事件订阅的验收结论会被这个抖动污染**（"没收到"可能只是断线窗口，不是订阅没配好）。

**根因（本环境）**：**两个 DHCP 客户端同时在管理同一张网卡** —— `ifupdown`（`networking.service` 为 `active`；`/etc/network/interfaces` 内 `iface enp1s0 inet dhcp`）＋ **遗留的独立 `dhcpcd` 进程**（`PID 660` 自 2026-06-27 起运行，但 `systemctl is-active dhcpcd` ＝ `inactive`，即**不由 systemd 单元管理**）。两者交替重写 `resolv.conf`，其中 dhcpcd 的一条写入路径产出"仅注释"的**空壳版本**。

**★ 排查命令（照做，判据明确）**：

```bash
# ① 看是否在抖（连续采样，看 size / nameserver 计数是否跳变）
for i in $(seq 1 40); do echo "$(date +%H:%M:%S) size=$(stat -c %s /etc/resolv.conf) ns=$(grep -c '^nameserver' /etc/resolv.conf)"; sleep 1; done

# ② 抓"坏状态"的真实内容（这才是判据，别只看大小）
for i in $(seq 1 60); do grep -q '^nameserver' /etc/resolv.conf || { echo ">>> BAD"; cat /etc/resolv.conf; break; }; sleep 1; done

# ③ 找出谁在写
ps -ef | grep -E 'dhcpcd|dhclient|udhcpc' | grep -v grep
systemctl is-active dhcpcd NetworkManager systemd-networkd networking
cat /etc/network/interfaces
```

**★ 修复方案（系统级变更 —— 按项目纪律由**人工执行**，且**务必先备份**）**：

```bash
# 方案 A（推荐：最小侵入、可回滚）—— 让 dhcpcd 不再接管 resolv.conf，并静态写死 DNS
sudo cp /etc/resolv.conf  /etc/resolv.conf.bak-$(date +%F-%H%M%S)
sudo cp /etc/dhcpcd.conf  /etc/dhcpcd.conf.bak-$(date +%F-%H%M%S)
echo 'nohook resolv.conf' | sudo tee -a /etc/dhcpcd.conf
sudo tee /etc/resolv.conf >/dev/null <<'EOF'
nameserver 192.168.10.1
nameserver 223.5.5.5
options timeout:2 attempts:3
EOF
sudo kill -HUP 660        # 让 dhcpcd 重读配置（PID 以实际为准）

# 验证：应恒为同一内容、不再跳变
for i in $(seq 1 30); do stat -c '%s %y' /etc/resolv.conf; sleep 2; done
```

> ★ **方案 B（更彻底但风险更高）**：消除"双 DHCP 管理" —— 把 `/etc/network/interfaces` 的 `iface enp1s0 inet dhcp` 改为 `manual`，只留 dhcpcd；或反向停用 dhcpcd 只留 ifupdown。★ **风险**：改错会**失去服务器网络**，而运维通常只经 ssh 访问 ⇒ **必须有人在物理/IPMI 侧兜底**方可执行。
> ★ **不推荐方案 C**：在 `/etc/hosts` 固定 `open.feishu.cn` 的 IP —— 该域名为 **CDN**（实测 IP 在 `36.x` / `120.x` / `223.x` 段间变化、单次解析可返回 30+ 个），固定会**丧失容灾**。
> ★★ **纪律**：**入站事件订阅 / 回调 / 推实例 / 对账 / 免登 任一项的验收，都必须先排除本抖动**；否则验收结论**不可信**。

---

> ★ **纪律**：所有未定项**显式标 `TODO`**（含来源编号），**不得留空节、不得臆造具体值**。

## 变更记录

| 版本 | 日期 | 变更 | 作者 |
|---|---|---|---|
| V1.7 | 2026-09-28 | ★★★ **新增 §9.3「环境硬前置：`/etc/resolv.conf` 抖动会让整条飞书链路时好时坏」**（联调实测发现，**本环境已发生**）：本环境 `resolv.conf` **每 15–30 秒在「166 B 含 1 条 `nameserver`」（md5 `dc5c4d9f`）与「114 B 含 0 条 `nameserver`」（md5 `b43fb671`，内容仅剩注释）之间翻转**。★ **判据**：**Go 解析器在 `resolv.conf` 无 `nameserver` 行时会回退到内置默认 `127.0.0.1:53` / `[::1]:53`** ⇒ 错误形如 `lookup open.feishu.cn on [::1]:53: … connection refused` ⇒ ★★ **见到 `on [::1]:53` / `on 127.0.0.1:53` 即可判定"读到的 resolv.conf 是空的"，不是 DNS 服务器故障**。★★ **实测影响**：审批对账每 5 分钟一拍连续失败；**长连接 `17:31:19` 断开后连续 4 次重连失败（17:31:35/17:33:05/17:34:35/17:36:05），`17:37:36` 才恢复 ⇒ 约 6 分钟完全收不到事件**，而它每 ~5 分钟就断一次 ⇒ **入站事件订阅的验收结论会被该抖动污染**（"没收到"可能只是断线窗口，不是订阅没配好）。★ **根因**：**双 DHCP 客户端同管一张网卡** —— `ifupdown`（`networking` active，`/etc/network/interfaces` 内 `iface enp1s0 inet dhcp`）＋ **遗留独立 `dhcpcd`（PID 660，6/27 起运行，但 systemd 显示 `inactive`）**。★ **给出三段排查命令（采样抖动 / 抓坏状态真实内容 / 定位写入者）＋ 方案 A（推荐：`nohook resolv.conf` ＋ 静态 DNS，可回滚）/ 方案 B（消除双 DHCP 管理，高风险需物理兜底）/ 不推荐方案 C（`/etc/hosts` 固定 CDN IP）**，并立纪律：**入站订阅 / 回调 / 推实例 / 对账 / 免登 任一项的验收，都必须先排除本抖动**。 | 交付总监 |
| V1.6 | 2026-09-28 | **新增 §9.2「启停操作要点：`start.sh` 必须重定向，否则日志全丢」**（组织同步实机验证轮附带发现的部署踩坑）：★ `start.sh` 末行是 **`exec ./jxapproval`**（前台形态）⇒ 经 ssh 直调时 **stdout/stderr 继承 ssh 的 pipe**，日志**一行不进文件**；★ 现场判据＝**看 `/proc/<pid>/fd/1`，不看日志文件**（`pipe:[…]` = 错、`…/logs/app.log` = 对）；★ 危害＝**进程在/端口通/接口有响应，唯日志为空** ⇒ 排障无据、联调现场易**误判为"事件没推过来"**（本次即因此多绕一轮）；★ 正确起法 `setsid nohup ./start.sh >> logs/app.log 2>&1 < /dev/null &`＋起后必查 fd。★ 与既有「`set -a; . ./.env` 缺则丢环境变量」同族：**"能起来" ≠ "起对了"**。 | 交付总监 |
| V1.5 | 2026-09-28 | 联调收口连带：§6 覆盖项指向的**回调实测四问 `V-1`~`V-4` 已由「待实测」改为「已定论」**（声结论留痕在 `09` **§7**，原「在 §2 阶段一」已作废）—— ★ 属定案 #59「同事实他处仍写旧态」的连带翻面。 | 交付总监 |
| V1.4 | 2026-09-27 | **回调修复收口（纯文档）**：**§6 联调** 新增两块 —— ① ★★ **联调自检**：用**官方字段名**报文（`action_type`/`user_id`/`approval_code`/`token`/`action_context`/`instance_id`/`task_id`/`message_id`…，**无顶层 `biz_no`**）打回调，判据＝**HTTP 200 ＋ `accepted:true`（不再 400）**，附可照做的 `curl` 与留痕检索命令；② ★★ **警示「不要用手工 `curl` 直推 `external_instances` 代替联调」**（绕过本地落库 ⇒ `t_instance` 无行 ⇒ 回调报 `无对应实例`、闭环走不通；正规链路＝`POST /api/approval/submit`）。★ 覆盖项指向 `09` §2（含新增 **`V-1`~`V-4`**）。 | 产品经理（Alice） |
| V1.0 | 2026-09-27 | 首版骨架：前置条件 / 域名 / 证书 / 反代 / 白名单 / 应用自检 / 联调 / 回滚（`13` A-7、`11 §7.2`）。具体域名与飞书出口网段待定，标 `TODO`（`QV2-A19`）。 | 架构师（Bob） |
| V1.1 | 2026-09-27 | 执行 `13` **N7**（补全）：新增 **§8 反向代理样例（Nginx）** + **§9 部署后一键验证命令**（`curl`/`openssl`/`ss`），把 `TODO` 收敛到**域名 / 飞书出口网段**两项（`QV2-A19`）。 | 架构师（Bob） |
| V1.3 | 2026-09-27 | ★ **新增 §9.1「联调环境实测记录」**：本环境实际用 **Caddy（`:5000`）→ `127.0.0.1:5001` ＋ 路由器端口转发（公网 `:5500`）**（非 Nginx＋443）。实测 `POST http://office.hunanyichu.com:5500/approval/external/callback` → **HTTP 400 ＋ 业务错误 JSON**（**非 404/502/连接失败**）⇒ **公网 → 端口转发 → Caddy → 应用** 全链**已打通**；内网直连一致、日志逐条带 `trace_id` ⇒ ★ **`QV2-A19` 的"入站面"在本环境已验证可达，飞书两键回调必然可送达**。 | 交付总监 |
| V1.2 | 2026-09-27 | 依 team-lead **分级核查**裁定（`04a §17.0`）：把「公网可达 **HTTPS**」改为「**公网可达入站**」（★ HTTPS 属**我方选择、非平台要求**）—— ① 标题与 §0 适用句去「HTTPS」绝对化；② §0.2 改**条件式**（仅当采用 HTTPS；**DNS-01 / 手工放证书不需 80/443**）；③ **新增 §0.5**（联调档可先用 HTTP）；④ **§2 标题**加「★ 仅当采用 HTTPS 时」＋ 补「不占 80/443 的取证方式」；⑤ §5 自检第 3 条标「（仅当采用 HTTPS）」。 | 架构师（Bob） |
