#!/usr/bin/env python3
"""
rebuild.py — 一键编译、停止旧进程、部署、启动 goapp
用法：python3 rebuild.py [--no-start]
  --no-start   只编译+替换二进制，不自动启动新进程

耗时说明（本环境实测）：
  - 无修改（全缓存）：~16s（纯链接）
  - 修改任意 .go 文件：~26s（重编译 goapp 包 + 链接）
  - 修改依赖包（如 modernc.org/sqlite）：~60s+（重编译依赖 + 链接）
  瓶颈是链接器，不是编译器。go run 与 go build 速度完全相同。
"""

import os
import sys
import time
import subprocess

# ── 配置 ────────────────────────────────────────────────
SCRIPT_DIR  = os.path.dirname(os.path.abspath(__file__))
GO_BIN      = '/tmp/go/bin/go'
OUTPUT_TMP  = '/tmp/goapp_build_out'
BINARY      = os.path.join(SCRIPT_DIR, 'goapp')
LOG_FILE    = os.path.join(SCRIPT_DIR, 'goapp.log')

BUILD_ENV = {
    **os.environ,
    'GOROOT'      : '/tmp/go',
    'CGO_ENABLED' : '0',
    'HOME'        : '/root',
    'GOCACHE'     : '/tmp/gocache',   # 增量编译缓存（跨次调用有效）
    'GOPATH'      : '/tmp/gopath',
    'GO111MODULE' : 'on',
    # -buildvcs=false：禁用 git VCS 查询（每次 go build 会调用 git status/log，
    # 在本环境中单次 git status 耗时 ~7s，禁用后节省 ~19s）
    'GOFLAGS'     : '-buildvcs=false',
}

RUN_ENV = {
    **os.environ,
    'GOROOT' : '/tmp/go',
    'HOME'   : '/root',
}
# ────────────────────────────────────────────────────────


def step(msg):
    print(f'\n\033[1;36m▶ {msg}\033[0m')

def ok(msg):
    print(f'  \033[1;32m✓ {msg}\033[0m')

def err(msg):
    print(f'  \033[1;31m✗ {msg}\033[0m')


# ── 1. 编译 ──────────────────────────────────────────────
step('编译 goapp ...')
t0 = time.time()
res = subprocess.run(
    [GO_BIN, 'build', '-v', '-o', OUTPUT_TMP, '.'],
    capture_output=True, text=True,
    cwd=SCRIPT_DIR,
    env=BUILD_ENV,
    timeout=600,
)
elapsed = time.time() - t0

if res.returncode != 0:
    err(f'编译失败（{elapsed:.1f}s）')
    print(res.stderr)
    sys.exit(1)

recompiled = res.stderr.strip()
if recompiled:
    ok(f'编译成功（{elapsed:.1f}s）— 重新编译: {recompiled}')
else:
    ok(f'编译成功（{elapsed:.1f}s）— 全部命中缓存，仅重新链接')


# ── 2. 停止旧进程 ─────────────────────────────────────────
step('停止旧 goapp 进程 ...')

# 先优雅停止
subprocess.run(['pkill', '-SIGTERM', '-f', r'goapp/goapp'], capture_output=True)
time.sleep(2)

# 检查是否还在
r = subprocess.run(['pgrep', '-f', r'goapp/goapp'], capture_output=True, text=True)
if r.stdout.strip():
    print(f'  进程仍在（PID {r.stdout.strip()}），发送 SIGKILL ...')
    subprocess.run(['pkill', '-9', '-f', r'goapp/goapp'], capture_output=True)
    time.sleep(2)

r = subprocess.run(['pgrep', '-f', r'goapp/goapp'], capture_output=True, text=True)
if r.stdout.strip():
    err(f'无法停止旧进程（PID {r.stdout.strip()}），请手动处理')
    sys.exit(1)

ok('旧进程已停止')


# ── 3. 替换二进制 ─────────────────────────────────────────
step(f'替换二进制 → {BINARY}')
res = subprocess.run(['cp', OUTPUT_TMP, BINARY], capture_output=True, text=True)
if res.returncode != 0:
    err(f'cp 失败: {res.stderr.strip()}')
    sys.exit(1)
subprocess.run(['chmod', '+x', BINARY], capture_output=True)
ok(f'二进制已更新')


# ── 4. 启动新进程 ─────────────────────────────────────────
if '--no-start' in sys.argv:
    print('\n  --no-start 模式，跳过启动')
    sys.exit(0)

step(f'启动新 goapp（日志 → {LOG_FILE}）...')

# ── DB 冷启动恢复：/tmp/queue.db 不存在时从项目目录拷贝 ──
DB_TMP  = '/tmp/queue.db'
DB_SEED = os.path.join(SCRIPT_DIR, 'queue.db')
if not os.path.exists(DB_TMP):
    if os.path.exists(DB_SEED):
        import shutil
        shutil.copy2(DB_SEED, DB_TMP)
        ok(f'DB 已从 {DB_SEED} 恢复到 {DB_TMP}')
    else:
        ok(f'DB 不存在，goapp 将自动创建新数据库: {DB_TMP}')
else:
    ok(f'DB 已存在，直接使用: {DB_TMP}')

log_fh = open(LOG_FILE, 'w')
proc = subprocess.Popen(
    [BINARY],
    cwd=SCRIPT_DIR,
    env=RUN_ENV,
    stdout=log_fh,
    stderr=subprocess.STDOUT,
)
time.sleep(3)

poll = proc.poll()
if poll is not None:
    err(f'进程已退出，返回码: {poll}')
    log_fh.flush()
    with open(LOG_FILE) as f:
        print(f.read()[-2000:])
    sys.exit(1)

ok(f'新 goapp 已启动，PID={proc.pid}')

# 打印启动日志末尾
time.sleep(1)
log_fh.flush()
r = subprocess.run(['tail', '-20', LOG_FILE], capture_output=True, text=True)
print('\n\033[90m' + r.stdout + '\033[0m')
print(f'\033[1;32m🚀 部署完成！\033[0m  PID={proc.pid}  日志: {LOG_FILE}\n')
