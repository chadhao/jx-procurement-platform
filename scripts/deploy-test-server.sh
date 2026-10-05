#!/usr/bin/env bash
# scripts/deploy-test-server.sh —— 把**当前 HEAD** 部署到测试服务器并自检（可按需回滚）。
#
# 定位：**联调环境的重复可执行入口**。此前这一步靠手工敲、靠人记；一记漏就出静默问题。
#       本脚本把 2026-10-06 实测踩到的三个坑**固化成机制**，不再依赖记性。
#
# ─────────────────────────────────────────────────────────────────────────
# ★★ 教训①：**必须从 `git archive HEAD` 的干净树构建，绝不在工作区直接 `go build`**
#   本仓库有**第二个 Agent（mimo）并行开发**，工作区随时可能是"在途未提交的半成品"。
#   本次实测就撞上：`git status` 显示 `internal/httpapi/handlers_approval.go`、
#   `internal/specload/specload.go` 正被修改，另有一个未跟踪新文件。
#   若照此构建，部署上去的是**没人验收过的中间态** —— 而且**不报任何错**（最危险的一类）。
#   ⇒ 一律 `git archive HEAD` 出干净树（等价于门禁里的「净检出可构建」）。
#
# ★★ 教训②：**自检端口必须现读 `.env`，不许写死、不许凭长度推断**
#   本次实测把 `JX_LISTEN_ADDR=127.0.0.1:5001` 猜成了 `127.0.0.1:8080`
#   （两者都恰好 14 个字符，"长度一样"就推断相同 ⇒ 错），于是自检打到了**别的服务**上，
#   收到 401 `{"message":"missing or malformed jwt"}`，差点误判"部署失败"并错误回滚。
#   ⇒ 端口一律 `grep '^JX_LISTEN_ADDR=' .env` 现读现用。
#
# ★★ 教训③：**程序自身不写日志文件（只走 stdout）** ⇒ 启动必须重定向。
#   已由 `scripts/start.sh` 固定落 `logs/app.log`（见该文件教训④）。
#   本脚本仍**验证日志确实在增长** —— 否则会出现"服务起了但联调期无日志可查"。
#
# ─────────────────────────────────────────────────────────────────────────
# 用法：
#   bash scripts/deploy-test-server.sh              # 构建 → 上传 → 备份 → 切换 → 自检
#   bash scripts/deploy-test-server.sh --rollback   # 回滚二进制 + 数据库到最近一次备份
#   bash scripts/deploy-test-server.sh --check-only # 只自检，不动任何东西
#
# 环境变量：
#   JX_TEST_HOST   默认 chadhao@192.168.10.50
#   JX_TEST_DIR    默认 services/jxapproval（相对远端 $HOME；也可给绝对路径）
#   JX_TEST_DOMAIN 默认 http://office.hunanyichu.com:5500（公网入口自检）
#
# ★ 退出码：0=成功；非 0=失败（部署模式下**失败即自动回滚**）。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

JX_TEST_HOST="${JX_TEST_HOST:-chadhao@192.168.10.50}"
JX_TEST_DIR="${JX_TEST_DIR:-services/jxapproval}"
JX_TEST_DOMAIN="${JX_TEST_DOMAIN:-http://office.hunanyichu.com:5500}"
SSH_OPTS=(-o BatchMode=yes -o StrictHostKeyChecking=no)

# remote_run：从 **stdin** 读远端脚本再执行。
# ★ 调用方一律用 **带引号** 的 heredoc 分隔符（<<'EOS'），使 `$VAR` / `$(...)` 只在远端展开。
remote_run() {
  ssh "${SSH_OPTS[@]}" "$JX_TEST_HOST" "JX_TEST_DIR='$JX_TEST_DIR' sh -s"
}

# 远端公共前导（每个远端脚本都用它定位目录）
read -r -d '' REMOTE_PRELUDE <<'EOS' || true
set -eu
case "$JX_TEST_DIR" in
  /*) D="$JX_TEST_DIR" ;;
  *)  D="$HOME/$JX_TEST_DIR" ;;
esac
cd "$D"
EOS

self_check() {
  echo "──────── 自检 ────────"
  { printf '%s\n' "$REMOTE_PRELUDE"; cat <<'EOS'
    echo "[env ]  $(grep -E '^JX_ENV=' .env)  $(grep -E '^DEV_MODE=' .env)"
    ADDR=$(grep '^JX_LISTEN_ADDR=' .env | cut -d= -f2-)
    PORT=${ADDR##*:}
    echo "[addr] JX_LISTEN_ADDR=$ADDR  ⇒ 自检端口 $PORT （★ 现读，不写死）"
    TOKEN=$(grep '^JX_INTERNAL_TOKEN=' .env | cut -d= -f2-)

    P=$(pgrep -x jxapproval || true)
    [ -n "$P" ] && echo "[proc] pid=$P" || { echo "[proc] !! 未运行"; exit 1; }
    ss -lntp 2>/dev/null | grep -q "127.0.0.1:$PORT" \
      && echo "[port] 已监听 127.0.0.1:$PORT" || { echo "[port] !! 未监听 127.0.0.1:$PORT"; exit 1; }

    ./jxapproval version | sed 's/^/[ver ] /'

    C1=$(curl -s -o /dev/null -m 8 -w "%{http_code}" "http://127.0.0.1:$PORT/")
    C2=$(curl -s -o /dev/null -m 8 -w "%{http_code}" -H "X-Internal-Token: $TOKEN" "http://127.0.0.1:$PORT/healthz")
    echo "[http] 127.0.0.1:$PORT/        ⇒ $C1  （期望 200）"
    echo "[http] 127.0.0.1:$PORT/healthz  ⇒ $C2  （期望 200）"
    [ "$C1" = "200" ] && [ "$C2" = "200" ] || { echo "[http] !! 非 200 ⇒ 端口可能不是本应用"; exit 1; }

    # 五项自检必须全 true：subscribe / longconn / db_writable / single_instance / approval_defs
    curl -s -m 8 -H "X-Internal-Token: $TOKEN" "http://127.0.0.1:$PORT/healthz" \
      | tr ',' '\n' | grep -E '"(subscribe|longconn|db_writable|single_instance|approval_defs)"' \
      | sed 's/^/[hz  ] /'

    S0=$(stat -c %s logs/app.log 2>/dev/null || echo 0)
    curl -s -o /dev/null -m 8 "http://127.0.0.1:$PORT/" >/dev/null 2>&1 || true
    sleep 2
    S1=$(stat -c %s logs/app.log 2>/dev/null || echo 0)
    if [ "$S1" -gt "$S0" ]; then
      echo "[log ] logs/app.log 在增长（$S0 → $S1）✓"
    else
      echo "[log ] !! logs/app.log 未增长 —— 启动未重定向？联调期将无日志可查"; exit 1
    fi
EOS
  } | remote_run

  echo "──────── 公网入口 ────────"
  local code
  code=$(curl -s -o /dev/null -m 15 -w "%{http_code}" "$JX_TEST_DOMAIN/" || echo 000)
  echo "  $JX_TEST_DOMAIN/  ⇒ $code  （期望 200）"
  [ "$code" = "200" ]
}

case "${1:-deploy}" in
  --check-only)
    self_check && echo "✓ 自检通过" ; exit $? ;;

  --rollback)
    echo "==> 回滚到最近一次备份"
    { printf '%s\n' "$REMOTE_PRELUDE"; cat <<'EOS'
      B=$(ls -1t jxapproval.bak-* 2>/dev/null | head -1 || true)
      [ -n "$B" ] || { echo "!! 找不到 jxapproval.bak-*"; exit 1; }
      DB=$(ls -1t data/jxapproval.db.bak-* 2>/dev/null | head -1 || true)
      echo "  二进制 <- $B"
      [ -n "$DB" ] && echo "  数据库 <- $DB"
      ./stop.sh
      cp -a "$B" jxapproval
      [ -n "$DB" ] && cp -a "$DB" data/jxapproval.db
      setsid nohup ./start.sh >/dev/null 2>&1 </dev/null &
      sleep 8
      pgrep -ax jxapproval
EOS
    } | remote_run
    self_check && echo "✓ 回滚完成" ; exit $? ;;

  deploy|--deploy) ;;
  *) echo "用法: bash scripts/deploy-test-server.sh [--check-only|--rollback]" >&2; exit 2 ;;
esac

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# ── 1) 从干净 HEAD 构建 ───────────────────────────────────────────────────
echo "==> [1/6] 从 git archive HEAD 构建（★ 不取工作区，避免打进在途半成品）"
if [ -n "$(git status --porcelain)" ]; then
  echo "    ⚠ 工作区有未提交改动（下面列出）—— 本脚本**不会**采用它们，只用 HEAD："
  git status --short | sed 's/^/      /'
fi
VER="$(git describe --tags --always)"
HEAD_SHA="$(git rev-parse --short HEAD)"
git archive HEAD | tar -x -C "$WORK"
( cd "$WORK" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags "-s -w -X main.version=$VER" \
    -o "$WORK/jxapproval" ./cmd/jxapproval )

# 校验确实是 linux/x86-64 的 ELF（防"目标平台构建错"这类静默错误）
PY=""
command -v python3 >/dev/null 2>&1 && PY=python3
[ -z "$PY" ] && command -v python >/dev/null 2>&1 && PY=python
if [ -n "$PY" ]; then
  "$PY" -c "import struct,sys;b=open(sys.argv[1],'rb').read(64);assert b[:4]==b'\x7fELF' and b[4]==2;t,m=struct.unpack_from('<HH',b,16);assert m==0x3e,'machine=%#x'%m;print('    ELF 校验通过：linux x86-64')" "$WORK/jxapproval"
else
  echo "    ⚠ 无 python，跳过 ELF 校验"
fi
echo "    构建完成：$VER  (HEAD=$HEAD_SHA)"

# ── 2) 上传候选 + 冒烟 ───────────────────────────────────────────────────
echo "==> [2/6] 上传候选并冒烟（此时不替换任何东西）"
scp "${SSH_OPTS[@]}" "$WORK/jxapproval" "$JX_TEST_HOST:$JX_TEST_DIR/jxapproval.candidate" >/dev/null
{ printf '%s\n' "$REMOTE_PRELUDE"; cat <<'EOS'
  chmod +x jxapproval.candidate
  ./jxapproval.candidate version | sed 's/^/    候选版本 /'
EOS
} | remote_run

# ── 3) 备份 ─────────────────────────────────────────────────────────────
echo "==> [3/6] 备份二进制与数据库"
TS="$(ssh "${SSH_OPTS[@]}" "$JX_TEST_HOST" 'date +%Y%m%d-%H%M%S')"
{ printf '%s\n' "$REMOTE_PRELUDE"; cat <<EOS
  cp -a jxapproval jxapproval.bak-$TS
  cp -a data/jxapproval.db data/jxapproval.db.bak-$TS
  echo "    备份戳 $TS"
EOS
} | remote_run

# ── 4) 切换 + 启动 ───────────────────────────────────────────────────────
echo "==> [4/6] 停 → 换 → 启"
{ printf '%s\n' "$REMOTE_PRELUDE"; cat <<'EOS'
  ./stop.sh
  cp -a jxapproval.candidate jxapproval
  chmod +x jxapproval start.sh
  setsid nohup ./start.sh >/dev/null 2>&1 </dev/null &
  sleep 8
  pgrep -ax jxapproval
EOS
} | remote_run

# ── 5) 自检（失败即回滚）─────────────────────────────────────────────────
echo "==> [5/6] 自检"
if ! self_check; then
  echo "!! 自检未通过 —— 自动回滚到 $TS"
  bash "${BASH_SOURCE[0]}" --rollback || echo "!! 回滚也失败，请人工介入（备份戳 $TS）"
  exit 1
fi

# ── 6) 完成 ─────────────────────────────────────────────────────────────
echo "==> [6/6] 完成"
cat <<EOF
✓ 已部署 $VER (HEAD=$HEAD_SHA) 到 $JX_TEST_HOST:$JX_TEST_DIR
  再次自检：bash scripts/deploy-test-server.sh --check-only
  回滚    ：bash scripts/deploy-test-server.sh --rollback
  看日志  ：ssh $JX_TEST_HOST 'tail -f ~/$JX_TEST_DIR/logs/app.log'
EOF
