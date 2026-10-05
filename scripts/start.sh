#!/bin/sh
# scripts/start.sh —— jxapproval 裸机（非 systemd）部署启动脚本的正本。
#
# 部署形态：生产服务器（192.168.10.50）以 ~/services/jxapproval/{start.sh,stop.sh,.env,jxapproval}
# 手工启停（scripts/jxapproval.service 是 systemd 草案，未启用）。本文件复制到服务器同名位置使用，
# 与 stop.sh 成对使用。
#
# ★ 三条实测教训（2026-09-27 服务器实测，勿删）：
#   ① 程序**不自行读取 .env**：直接 `nohup ./jxapproval` 会让进程丢光全部环境变量
#      → 退回默认 127.0.0.1:8080、JX_APP_ID 为空、飞书长连接报
#      `7104 appSecret and clientAssertionProvider cannot be nil`、`bind: address already in use`。
#      ⇒ **必须以 `set -a; . ./.env; set +a` 注入后再启动**（set -a 使 source 进来的变量自动 export）。
#   ② stop.sh 以 `pgrep -x jxapproval` 精确按进程名定位（见 stop.sh 头注）——本脚本启动前
#      若已有存活实例，属"重复启动"，交由程序自身的端口绑定失败报错兜底（单实例硬约束 TC-04）。
#   ③ run.pid 必须可信：本脚本在 exec 前把 $$ 写入 run.pid（exec 后 pid 不变），
#      保证 run.pid ＝ 最终进程 pid；stop.sh 只在确认进程退出后才删它。
#
# 用法：后台启动 `nohup ./start.sh &`，观察 `tail -f logs/app.log`；
#       前台调试（日志直接打屏）`JX_LOG_DEST=/dev/stdout ./start.sh`。

set -eu

APP_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
cd "$APP_DIR"

# 教训①：先注入 .env 再启动。缺 .env 拒绝启动（失败可见，绝不静默退回默认配置）。
if [ ! -f ./.env ]; then
  echo "start.sh: ./.env 不存在，拒绝启动（程序不自行读取 .env，缺它即丢全部配置）" >&2
  exit 1
fi
set -a
. ./.env
set +a

# 记录 pid：$$ 经 exec 后不变，run.pid 即最终 jxapproval 进程的 pid。
echo $$ > ./run.pid

# ★★ 教训④（2026-10-06 联调环境搭建时实测，勿删）：**程序自身不打开任何日志文件**，
#   日志只写 stdout/stderr（全仓无 `logs/app.log` 的 OpenFile；唯一的 OpenFile 是单实例锁）。
#   ⇒ 旧脚本头推荐的 `nohup ./start.sh >/dev/null 2>&1 &` 会把日志**整条丢掉**：
#     实测换二进制重启后，`logs/app.log` 停在旧时间戳、新请求一条不落
#     ⇒ **联调期等于没有日志，排障全靠猜**。
#   ⇒ 故改为**本脚本固定落日志**（默认 `logs/app.log`，可由 `JX_LOG_DEST` 覆盖）。
#     后台启动只需 `nohup ./start.sh &`，不必再记重定向 —— 去掉一个"记不住就静默丢日志"的坑。
LOG_DEST="${JX_LOG_DEST:-$APP_DIR/logs/app.log}"
mkdir -p "$(dirname "$LOG_DEST")"
exec ./jxapproval >>"$LOG_DEST" 2>&1
