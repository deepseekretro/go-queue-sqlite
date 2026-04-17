#!/usr/bin/env bash
# dev.sh — GoApp + GoWorker 开发环境一键启动脚本
# 用法：bash dev.sh [stop|restart|status|logs]
#
# 启动后会创建一个名为 godev 的 tmux session，布局如下：
#   ┌─────────────────────────────┐
#   │  上窗格：goapp  (port 8080) │
#   ├─────────────────────────────┤
#   │  下窗格：goworker           │
#   └─────────────────────────────┘
# 连接到 session：tmux attach -t godev
# 退出 session（不停止进程）：Ctrl-b d
set -euo pipefail

GOAPP_DIR="$(cd "$(dirname "$0")" && pwd)"
WORKER_DIR="$(dirname "$GOAPP_DIR")/goworker"
GOAPP_LOG="$GOAPP_DIR/goapp.log"
WORKER_LOG="$WORKER_DIR/goworker.log"
GOAPP_PID="$GOAPP_DIR/.goapp.pid"
WORKER_PID="$WORKER_DIR/.goworker.pid"
TMUX_SESSION="godev"
export PATH="/usr/local/go/bin:$PATH"

# ── 颜色 ──────────────────────────────────────────────────────────────────────
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
BLUE='\033[0;34m'; CYAN='\033[0;36m'; NC='\033[0m'
info()  { echo -e "${CYAN}[INFO]${NC}  $*"; }
ok()    { echo -e "${GREEN}[OK]${NC}    $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC}  $*"; }
err()   { echo -e "${RED}[ERR]${NC}   $*"; }

# ── 确保 tmux 已安装 ──────────────────────────────────────────────────────────
ensure_tmux() {
  if ! command -v tmux &>/dev/null; then
    warn "tmux 未安装，正在自动安装..."
    if command -v apt-get &>/dev/null; then
      apt-get install -y tmux >/dev/null 2>&1 && ok "tmux 安装完成"
    elif command -v yum &>/dev/null; then
      yum install -y tmux >/dev/null 2>&1 && ok "tmux 安装完成"
    elif command -v apk &>/dev/null; then
      apk add --no-cache tmux >/dev/null 2>&1 && ok "tmux 安装完成"
    else
      err "无法自动安装 tmux，请手动安装后重试"; exit 1
    fi
  fi
}

# ── 停止函数 ──────────────────────────────────────────────────────────────────
stop_all() {
  info "停止所有进程..."
  pkill -f './goapp'   2>/dev/null && ok "goapp 已停止"   || true
  pkill -f 'goworker'  2>/dev/null && ok "goworker 已停止" || true
  rm -f "$GOAPP_PID" "$WORKER_PID"
  # 销毁 tmux session（如果存在）
  if tmux has-session -t "$TMUX_SESSION" 2>/dev/null; then
    tmux kill-session -t "$TMUX_SESSION" && ok "tmux session '$TMUX_SESSION' 已销毁" || true
  fi
  sleep 1
}

# ── 状态函数 ──────────────────────────────────────────────────────────────────
status_all() {
  echo -e "\n${BLUE}=== 进程状态 ===${NC}"
  if pgrep -f './goapp' > /dev/null 2>&1; then
    ok "goapp    运行中 (PID: $(pgrep -f './goapp' | head -1))"
  else
    err "goapp    未运行"
  fi
  if pgrep -f 'goworker' > /dev/null 2>&1; then
    ok "goworker 运行中 (PID: $(pgrep -f 'goworker' | head -1))"
  else
    err "goworker 未运行"
  fi
  echo -e "\n${BLUE}=== tmux session ===${NC}"
  if tmux has-session -t "$TMUX_SESSION" 2>/dev/null; then
    ok "tmux session '$TMUX_SESSION' 存在  →  tmux attach -t $TMUX_SESSION"
  else
    warn "tmux session '$TMUX_SESSION' 不存在"
  fi
  echo -e "\n${BLUE}=== 健康检查 ===${NC}"
  curl -s http://localhost:8080/healthz 2>/dev/null | python3 -m json.tool 2>/dev/null || err "goapp 未响应"
}

# ── 日志函数 ──────────────────────────────────────────────────────────────────
show_logs() {
  echo -e "\n${BLUE}=== goapp 最近日志 ===${NC}"
  tail -30 "$GOAPP_LOG" 2>/dev/null || warn "日志文件不存在"
  echo -e "\n${BLUE}=== goworker 最近日志 ===${NC}"
  tail -20 "$WORKER_LOG" 2>/dev/null || warn "日志文件不存在"
}

# ── 命令分发 ──────────────────────────────────────────────────────────────────
CMD="${1:-start}"
case "$CMD" in
  stop)    stop_all;    exit 0 ;;
  status)  status_all;  exit 0 ;;
  logs)    show_logs;   exit 0 ;;
  restart) stop_all ;;
  start)   ;;
  *) echo "用法: $0 [start|stop|restart|status|logs]"; exit 1 ;;
esac

# ── 确保 tmux 可用 ────────────────────────────────────────────────────────────
ensure_tmux

# ── 编译 ──────────────────────────────────────────────────────────────────────
echo -e "\n${BLUE}╔══════════════════════════════════════════╗${NC}"
echo -e "${BLUE}║   GoApp + GoWorker 开发环境启动          ║${NC}"
echo -e "${BLUE}╚══════════════════════════════════════════╝${NC}\n"

info "编译 goapp..."
cd "$GOAPP_DIR"
if go build -o goapp . 2>&1; then
  ok "goapp 编译成功"
else
  err "goapp 编译失败，退出"; exit 1
fi

info "编译 goworker..."
cd "$WORKER_DIR"
if go build -o goworker . 2>&1; then
  ok "goworker 编译成功"
else
  err "goworker 编译失败，退出"; exit 1
fi

# ── 启动 goapp（后台，写日志）────────────────────────────────────────────────
info "启动 goapp..."
cd "$GOAPP_DIR"
./goapp > "$GOAPP_LOG" 2>&1 &
GOAPP_PID_VAL=$!
echo $GOAPP_PID_VAL > "$GOAPP_PID"

# 等待 goapp 就绪（最多 10s）
for i in $(seq 1 20); do
  if curl -s http://localhost:8080/healthz > /dev/null 2>&1; then
    ok "goapp 已就绪 (PID: $GOAPP_PID_VAL, port: 8080)"
    break
  fi
  sleep 0.5
  if [ $i -eq 20 ]; then
    err "goapp 启动超时，查看日志："; tail -20 "$GOAPP_LOG"; exit 1
  fi
done

# ── 启动 goworker（后台，写日志）─────────────────────────────────────────────
QUEUE="${QUEUE:-default}"
CONCURRENCY="${CONCURRENCY:-2}"
CONNECTIONS="${CONNECTIONS:-1}"
SERVER_URL="${QUEUE_SERVER:-ws://localhost:8080/ws/worker}"

info "启动 goworker (queue=$QUEUE, concurrency=$CONCURRENCY, connections=$CONNECTIONS)..."
cd "$WORKER_DIR"
./goworker \
  -server  "$SERVER_URL" \
  -queue   "$QUEUE" \
  -concurrency "$CONCURRENCY" \
  -connections "$CONNECTIONS" \
  > "$WORKER_LOG" 2>&1 &
WORKER_PID_VAL=$!
echo $WORKER_PID_VAL > "$WORKER_PID"

# 等待 worker 连接（最多 5s）
for i in $(seq 1 10); do
  WS_COUNT=$(curl -s http://localhost:8080/healthz 2>/dev/null \
    | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('ws_workers',0))" 2>/dev/null || echo 0)
  if [ "$WS_COUNT" -ge 1 ] 2>/dev/null; then
    ok "goworker 已连接 (PID: $WORKER_PID_VAL, ws_workers: $WS_COUNT)"
    break
  fi
  sleep 0.5
  if [ $i -eq 10 ]; then
    warn "goworker 连接超时（可能仍在重试），查看日志："; tail -10 "$WORKER_LOG"
  fi
done

# ── 创建 tmux session，上下两个窗格分别 tail 日志 ────────────────────────────
info "创建 tmux session '$TMUX_SESSION'（上: goapp | 下: goworker）..."

# 如果 session 已存在则先销毁
tmux kill-session -t "$TMUX_SESSION" 2>/dev/null || true

# 新建 session，第一个窗口命名为 logs，默认窗格 tail goapp 日志
tmux new-session  -d -s "$TMUX_SESSION" -n "logs" \
  "echo -e '\033[0;36m[goapp log]\033[0m  tail -f $GOAPP_LOG\n'; tail -f '$GOAPP_LOG'"

# 水平分割（上下），下窗格 tail goworker 日志
tmux split-window -v -t "$TMUX_SESSION:logs" \
  "echo -e '\033[0;32m[goworker log]\033[0m  tail -f $WORKER_LOG\n'; tail -f '$WORKER_LOG'"

# 上窗格占 65%，下窗格占 35%
tmux resize-pane  -t "$TMUX_SESSION:logs.0" -y "65%"

# 焦点回到上窗格
tmux select-pane  -t "$TMUX_SESSION:logs.0"

ok "tmux session '$TMUX_SESSION' 已创建"

# ── 最终状态 ──────────────────────────────────────────────────────────────────
echo ""
echo -e "${GREEN}╔══════════════════════════════════════════╗${NC}"
echo -e "${GREEN}║   ✅ 启动完成                            ║${NC}"
echo -e "${GREEN}╚══════════════════════════════════════════╝${NC}"
echo ""
echo -e "  Dashboard  : ${CYAN}http://localhost:8080/${NC}  (需登录)"
echo -e "  Login      : ${CYAN}http://localhost:8080/login${NC}"
echo -e "  Health     : ${CYAN}http://localhost:8080/healthz${NC}"
echo -e "  Metrics    : ${CYAN}http://localhost:8080/metrics${NC}"
echo ""
echo -e "  默认凭据   : admin / admin"
echo -e "  自定义     : DASHBOARD_USER=xxx DASHBOARD_PASS=xxx bash dev.sh"
echo ""
echo -e "  ${BLUE}tmux 日志面板${NC}"
echo -e "    连接      : ${CYAN}tmux attach -t $TMUX_SESSION${NC}"
echo -e "    布局      : 上窗格 = goapp 日志 │ 下窗格 = goworker 日志"
echo -e "    退出面板  : Ctrl-b d  （进程继续在后台运行）"
echo -e "    停止一切  : bash dev.sh stop"
echo ""
echo -e "  文件日志"
echo -e "    goapp     : tail -f $GOAPP_LOG"
echo -e "    goworker  : tail -f $WORKER_LOG"
echo -e "  停止        : bash dev.sh stop"
echo -e "  状态        : bash dev.sh status"
echo ""
