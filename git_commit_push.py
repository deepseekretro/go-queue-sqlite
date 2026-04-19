#!/usr/bin/env python3
"""
git_commit_push.py — 一键 git add / commit / push 脚本

用法：
    python3 git_commit_push.py "提交信息"
    python3 git_commit_push.py "提交信息" file1.go file2.go   # 只 add 指定文件
    python3 git_commit_push.py                                 # 交互式输入提交信息

说明：
    - 默认 git add -A（暂存所有变更），也可指定文件
    - 自动修复 GIT_SSH_COMMAND 末尾多余引号的问题
    - 自动注入 Deepnote gitkey，无需手动设置 GIT_SSH_COMMAND
    - 推送到当前分支的 upstream（origin/<branch>）
"""

import os
import subprocess
import sys

# ANSI 颜色
CYAN   = "\033[1;36m"
GREEN  = "\033[1;32m"
RED    = "\033[31m"
YELLOW = "\033[33m"
GRAY   = "\033[90m"
RESET  = "\033[0m"

GITKEY = "/work/.deepnote/gitkey"


# ─── 工具函数 ──────────────────────────────────────────────────────────────────

def run(cmd, env=None, cwd=None):
    """执行命令，打印输出，失败时退出。"""
    print(GRAY + "$ " + " ".join(cmd) + RESET)
    result = subprocess.run(cmd, capture_output=True, text=True, env=env, cwd=cwd)
    if result.stdout.strip():
        print(result.stdout.strip())
    if result.stderr.strip():
        print(result.stderr.strip())
    if result.returncode != 0:
        print(RED + "✗ 命令失败（exit {}）".format(result.returncode) + RESET)
        sys.exit(result.returncode)
    return result


def fixed_env():
    """
    返回修复后的环境变量字典：
    1. 若 gitkey 文件存在，直接覆盖写入正确的 GIT_SSH_COMMAND，
       彻底避免 Deepnote 环境中末尾多余引号导致的 SSH 语法错误。
    2. 若 gitkey 不存在（非 Deepnote 环境），保留系统原有配置。
    """
    env = os.environ.copy()
    if os.path.exists(GITKEY):
        env["GIT_SSH_COMMAND"] = (
            "ssh -i " + GITKEY +
            " -o UserKnownHostsFile=/dev/null"
            " -o StrictHostKeyChecking=no"
        )
        print(GRAY + "[git] 使用 gitkey: " + GITKEY + RESET)
    return env


def current_branch(cwd=None):
    """获取当前 git 分支名。"""
    result = subprocess.run(
        ["git", "rev-parse", "--abbrev-ref", "HEAD"],
        capture_output=True, text=True, cwd=cwd
    )
    return result.stdout.strip() or "master"


# ─── 主流程 ────────────────────────────────────────────────────────────────────

def main():
    # 脚本所在目录即为 git 仓库根目录
    repo_dir = os.path.dirname(os.path.abspath(__file__))

    args = sys.argv[1:]

    # 解析参数：第一个参数为提交信息，其余为文件列表
    if args:
        commit_msg = args[0]
        files = args[1:]
    else:
        commit_msg = input("请输入提交信息：").strip()
        if not commit_msg:
            print(RED + "✗ 提交信息不能为空" + RESET)
            sys.exit(1)
        files = []

    env = fixed_env()
    branch = current_branch(cwd=repo_dir)

    print(CYAN + "\n▶ git add" + RESET)
    if files:
        run(["git", "add"] + files, cwd=repo_dir)
    else:
        run(["git", "add", "-A"], cwd=repo_dir)

    # 检查是否有内容可提交
    status = subprocess.run(
        ["git", "diff", "--cached", "--stat"],
        capture_output=True, text=True, cwd=repo_dir
    )
    if not status.stdout.strip():
        print(YELLOW + "⚠ 没有需要提交的变更，退出。" + RESET)
        sys.exit(0)
    print(status.stdout.strip())

    print(CYAN + "\n▶ git commit" + RESET)
    run(["git", "commit", "-m", commit_msg], cwd=repo_dir)

    print(CYAN + "\n▶ git push → origin/" + branch + RESET)
    run(["git", "push", "origin", branch], env=env, cwd=repo_dir)

    print(GREEN + "\n🚀 推送完成！" + RESET + " branch=" + branch)


if __name__ == "__main__":
    main()
