#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────
# rebuild.sh  —  停服 → Git 提交 → 编译 → 重启服务
# 用法:
#   ./rebuild.sh
#   ./rebuild.sh "fix: resolve db lock issue"
# ─────────────────────────────────────────────────────────────

set -euo pipefail

WORK_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$WORK_DIR"

GREEN='\033[0;32m'; YELLOW='\033[1;33m'; BLUE='\033[0;34m'; RESET='\033[0m'
info()    { echo -e "${BLUE}[rebuild]${RESET} $*"; }
success() { echo -e "${GREEN}[rebuild]${RESET} $*"; }
warn()    { echo -e "${YELLOW}[rebuild]${RESET} $*"; }

# ── Step 1: 停止旧进程 ────────────────────────────────────────
info "Step 1/4 — 停止旧进程..."
if pkill -f "$(pwd)/goapp" 2>/dev/null; then
    warn "旧进程已停止"
    sleep 1
else
    warn "没有运行中的进程"
fi

# ── Step 2 & 3: Git 提交 + 编译（复用 build.sh）────────────────
COMMIT_MSG="${1:-}"
if [ -n "$COMMIT_MSG" ]; then
    bash "$(pwd)/build.sh" "$COMMIT_MSG"
else
    bash "$(pwd)/build.sh"
fi

# ── Step 4: 启动新服务 ────────────────────────────────────────
info "Step 4/4 — 启动服务..."
nohup "$(pwd)/goapp" > "$(pwd)/goapp.log" 2>&1 &
NEW_PID=$!
sleep 1

if kill -0 "$NEW_PID" 2>/dev/null; then
    success "服务已启动 PID=${NEW_PID}  日志: $(pwd)/goapp.log"
    success "访问: http://localhost:8080"
else
    echo "[rebuild] 服务启动失败，查看日志:"
    cat "$(pwd)/goapp.log"
    exit 1
fi
