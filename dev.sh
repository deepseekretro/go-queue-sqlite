#!/usr/bin/env bash
# dev.sh — GoApp + GoWorker 开发环境一键启动脚本
# 用法：bash dev.sh [stop|restart|status|logs]
set -euo pipefail

GOAPP_DIR="$(cd "$(dirname "$0")" && pwd)"
WORKER_DIR="$(dirname "$GOAPP_DIR")/goworker"
GOAPP_LOG="$GOAPP_DIR/goapp.log"
WORKER_LOG="$WORKER_DIR/goworker.log"
GOAPP_PID="$GOAPP_DIR/.goapp.pid"
WORKER_PID="$WORKER_DIR/.goworker.pid"
export PATH="/usr/local/go/bin:$PATH"

# ── 颜色 ──────────────────────────────────────────────────────────────────────
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
BLUE='\033[0;34m'; CYAN='\033[0;36m'; NC='\033[0m'
info()    { echo -e "${CYAN}[INFO]${NC}  $*"; }
ok()      { echo -e "${GREEN}[OK]${NC}    $*"; }
warn()    { echo -e "${YELLOW}[WARN]${NC}  $*"; }
err()     { echo -e "${RED}[ERR]${NC}   $*"; }

# ── 停止函数 ──────────────────────────────────────────────────────────────────
stop_all() {
    info "停止所有进程..."
    pkill -f './goapp'   2>/dev/null && ok "goapp 已停止"   || true
    pkill -f 'goworker'  2>/dev/null && ok "goworker 已停止" || true
    rm -f "$GOAPP_PID" "$WORKER_PID"
    sleep 1
}

# ── 状态函数 ──────────────────────────────────────────────────────────────────
status_all() {
    echo -e "\n${BLUE}=== 进程状态 ===${NC}"
    if pgrep -f './goapp' > /dev/null 2>&1; then
        ok "goapp     运行中 (PID: $(pgrep -f './goapp' | head -1))"
    else
        err "goapp     未运行"
    fi
    if pgrep -f 'goworker' > /dev/null 2>&1; then
        ok "goworker  运行中 (PID: $(pgrep -f 'goworker' | head -1))"
    else
        err "goworker  未运行"
    fi
    echo ""
    echo -e "${BLUE}=== 健康检查 ===${NC}"
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
    stop)    stop_all; exit 0 ;;
    status)  status_all; exit 0 ;;
    logs)    show_logs; exit 0 ;;
    restart) stop_all ;;
    start)   ;;
    *)       echo "用法: $0 [start|stop|restart|status|logs]"; exit 1 ;;
esac

# ── 编译 ──────────────────────────────────────────────────────────────────────
echo -e "\n${BLUE}╔══════════════════════════════════════════╗${NC}"
echo -e "${BLUE}║   GoApp + GoWorker 开发环境启动          ║${NC}"
echo -e "${BLUE}╚══════════════════════════════════════════╝${NC}\n"

info "编译 goapp..."
cd "$GOAPP_DIR"
if go build -o goapp . 2>&1; then
    ok "goapp 编译成功"
else
    err "goapp 编译失败，退出"
    exit 1
fi

info "编译 goworker..."
cd "$WORKER_DIR"
if go build -o goworker . 2>&1; then
    ok "goworker 编译成功"
else
    err "goworker 编译失败，退出"
    exit 1
fi

# ── 启动 goapp ────────────────────────────────────────────────────────────────
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
        err "goapp 启动超时，查看日志："
        tail -20 "$GOAPP_LOG"
        exit 1
    fi
done

# ── 启动 goworker ─────────────────────────────────────────────────────────────
QUEUE="${QUEUE:-default}"
CONCURRENCY="${CONCURRENCY:-2}"
CONNECTIONS="${CONNECTIONS:-1}"
SERVER_URL="${QUEUE_SERVER:-ws://localhost:8080/ws/worker}"

info "启动 goworker (queue=$QUEUE, concurrency=$CONCURRENCY, connections=$CONNECTIONS)..."
cd "$WORKER_DIR"
./goworker \
    -server "$SERVER_URL" \
    -queue "$QUEUE" \
    -concurrency "$CONCURRENCY" \
    -connections "$CONNECTIONS" \
    > "$WORKER_LOG" 2>&1 &
WORKER_PID_VAL=$!
echo $WORKER_PID_VAL > "$WORKER_PID"

# 等待 worker 连接（最多 5s）
for i in $(seq 1 10); do
    WS_COUNT=$(curl -s http://localhost:8080/healthz 2>/dev/null | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('ws_workers',0))" 2>/dev/null || echo 0)
    if [ "$WS_COUNT" -ge 1 ] 2>/dev/null; then
        ok "goworker 已连接 (PID: $WORKER_PID_VAL, ws_workers: $WS_COUNT)"
        break
    fi
    sleep 0.5
    if [ $i -eq 10 ]; then
        warn "goworker 连接超时（可能仍在重试），查看日志："
        tail -10 "$WORKER_LOG"
    fi
done

# ── 最终状态 ──────────────────────────────────────────────────────────────────
echo ""
echo -e "${GREEN}╔══════════════════════════════════════════╗${NC}"
echo -e "${GREEN}║   ✅ 启动完成                            ║${NC}"
echo -e "${GREEN}╚══════════════════════════════════════════╝${NC}"
echo ""
echo -e "  Dashboard : ${CYAN}http://localhost:8080/${NC}  (需登录)"
echo -e "  Login     : ${CYAN}http://localhost:8080/login${NC}"
echo -e "  Health    : ${CYAN}http://localhost:8080/healthz${NC}"
echo -e "  Metrics   : ${CYAN}http://localhost:8080/metrics${NC}"
echo ""
echo -e "  默认凭据  : admin / admin"
echo -e "  自定义    : DASHBOARD_USER=xxx DASHBOARD_PASS=xxx bash dev.sh"
echo ""
echo -e "  日志      : tail -f $GOAPP_LOG"
echo -e "  停止      : bash dev.sh stop"
echo -e "  状态      : bash dev.sh status"
echo ""
