#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────
# build.sh  —  Go Queue 构建工作流
# 用法:
#   ./build.sh                        # 自动生成 commit message
#   ./build.sh "feat: add retry logic" # 自定义 commit message
# ─────────────────────────────────────────────────────────────

set -euo pipefail

WORK_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$WORK_DIR"

# ── 颜色输出 ──────────────────────────────────────────────────
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
BLUE='\033[0;34m'; CYAN='\033[0;36m'; RESET='\033[0m'
info()    { echo -e "${BLUE}[build]${RESET} $*"; }
success() { echo -e "${GREEN}[build]${RESET} $*"; }
warn()    { echo -e "${YELLOW}[build]${RESET} $*"; }
error()   { echo -e "${RED}[build]${RESET} $*"; exit 1; }

# ── Step 1: 检查是否有变更 ────────────────────────────────────
info "Step 1/3 — 检查 Git 状态..."
if git diff --quiet && git diff --cached --quiet; then
    warn "没有检测到文件变更，跳过 Git 提交"
    COMMITTED=false
else
    # ── Step 2: Git 提交 ──────────────────────────────────────
    info "Step 2/3 — 提交到本地 Git 仓库..."

    # 自动生成 commit message（或使用传入参数）
    if [ $# -ge 1 ] && [ -n "$1" ]; then
        COMMIT_MSG="$1"
    else
        TIMESTAMP=$(date '+%Y-%m-%d %H:%M:%S')
        CHANGED_FILES=$(git diff --name-only; git diff --cached --name-only; git ls-files --others --exclude-standard)
        FILE_LIST=$(echo "$CHANGED_FILES" | tr '\n' ' ' | sed 's/ $//')
        COMMIT_MSG="build: auto-commit before build @ ${TIMESTAMP} [${FILE_LIST}]"
    fi

    git add .
    git commit -m "$COMMIT_MSG"
    COMMIT_HASH=$(git rev-parse --short HEAD)
    success "已提交: ${COMMIT_HASH} — ${COMMIT_MSG}"
    COMMITTED=true
fi

# ── Step 3: 编译 ──────────────────────────────────────────────
info "Step 3/3 — 编译 Go 程序 (CGO_ENABLED=0)..."
BUILD_START=$(date +%s%N)

if CGO_ENABLED=0 /usr/local/go/bin/go build -o goapp .; then
    BUILD_END=$(date +%s%N)
    ELAPSED=$(( (BUILD_END - BUILD_START) / 1000000 ))
    success "编译成功 ✓  耗时 ${ELAPSED}ms  →  $(pwd)/goapp"
    if [ "$COMMITTED" = true ]; then
        success "Git commit: $(git rev-parse --short HEAD)"
    fi
else
    error "编译失败 ✗"
fi
