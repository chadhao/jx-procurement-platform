#!/bin/sh
# scripts/stop.sh —— jxapproval 裸机（非 systemd）部署停止脚本的正本（与 start.sh 成对使用）。
#
# ★ 两条实测教训（2026-09-27 服务器实测，勿删）：
#   ① 老版用 `pkill -f ../jxapproval/jxapproval`：`-f` 匹配整条命令行，而实际进程 cmd 是
#      `./jxapproval` ⇒ **pattern 不匹配、杀不掉**（实测旧进程存活 3 小时）；且 `-f` 会拿
#      **调用方自身命令行**参与匹配，有自伤/误伤风险。⇒ 改用 **`pgrep -x jxapproval`**：
#      按进程名**精确**匹配，不会被本脚本或调用方的命令行误匹配（进程名 10 字符 ≤ 15，
#      兼容 pgrep 的 comm 截断）。
#   ② 老版**无条件** `rm -f run.pid` ⇒ 一旦没杀掉，进程还在但 pid 文件已删 ＝ **彻底失联**
#      （实测 `cat run.pid` 返回空，只能改用 pgrep 补救）。⇒ **先确认进程已退出
#      （SIGTERM → 最多等 30s → SIGKILL 兜底）再删 run.pid**；进程仍在则保留 pid 文件并报错。
#
# 幂等：找不到进程不报错、正常退出 0（重复执行安全）。

set -u

APP_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
PID_FILE="$APP_DIR/run.pid"

# wait_gone <seconds>：最多再等 <seconds> 秒，进程消失返回 0，仍存活返回非 0。
wait_gone() {
  _i=0
  while [ "$_i" -lt "$1" ]; do
    pgrep -x jxapproval >/dev/null 2>&1 || return 0
    _i=$((_i + 1))
    sleep 1
  done
  pgrep -x jxapproval >/dev/null 2>&1
}

pids=$(pgrep -x jxapproval 2>/dev/null || true)

if [ -n "$pids" ]; then
  # 先 SIGTERM：进程内有优雅退出逻辑（先停飞书长连接、再排空 worker），
  # 时限 30s 与 scripts/jxapproval.service 的 TimeoutStopSec=30 同口径。
  # shellcheck disable=SC2086  # 有意按空白拆分多个 pid
  kill $pids 2>/dev/null || true
  if ! wait_gone 30; then
    pids=$(pgrep -x jxapproval 2>/dev/null || true)
    echo "stop.sh: SIGTERM 30s 后仍存活，升级 SIGKILL: $pids" >&2
    # shellcheck disable=SC2086
    kill -9 $pids 2>/dev/null || true
    wait_gone 5 || true
  fi
fi

# 教训②：只有确认进程已退出（或本来就没有）才删 run.pid。
if pgrep -x jxapproval >/dev/null 2>&1; then
  echo "stop.sh: jxapproval 仍存活（SIGKILL 后未退出），保留 run.pid 供排查" >&2
  exit 1
fi
rm -f "$PID_FILE"
exit 0
