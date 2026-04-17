#!/usr/bin/env bash
# log.sh — 查看构建历史 Git 日志
WORK_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$WORK_DIR"

echo ""
echo "  📦 Go Queue — Build History"
echo "  ─────────────────────────────────────────────────────────"
git log --oneline --graph --decorate --color=always | head -30
echo ""
echo "  最近修改的文件:"
git diff --stat HEAD~1 HEAD 2>/dev/null || echo "  (仅一次提交)"
echo ""
