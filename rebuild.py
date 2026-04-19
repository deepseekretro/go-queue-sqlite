#!/usr/bin/env python3
"""
rebuild.py — 一键编译、部署到 /tmp/goapp/、启动 goapp
用法：python3 rebuild.py [--no-start]
  --no-start   只编译，不自动启动新进程

部署目录：/tmp/goapp/
  - 二进制：/tmp/goapp/goapp
  - 数据库：/tmp/goapp/queue.db
  - 日志：  /tmp/goapp/goapp.log

耗时说明（本环境实测）：
  - 无修改（全缓存）：~16s（纯链接）
  - 修改任意 .go 文件：~26s（重编译 goapp 包 + 链接）
  - 修改依赖包（如 modernc.org/sqlite）：~60s+（重编译依赖 + 链接）
  瓶颈是链接器，不是编译器。go run 与 go build 速度完全相同。
"""

import os
import sys
import time
import shutil
import subprocess

# ── 配置 ────────────────────────────────────────────────
SCRIPT_DIR  = os.path.dirname(os.path.abspath(__file__))
GO_BIN      = '/tmp/go/bin/go'

# 部署目录：全部放在 /tmp/goapp/，不污染源码目录
DEPLOY_DIR  = '/tmp/goapp'
BINARY      = os.path.join(DEPLOY_DIR, 'goapp')
BINARY_NEW  = os.path.join(DEPLOY_DIR, 'goapp.new')   # 编译临时产物
DB_FILE     = os.path.join(DEPLOY_DIR, 'queue.db')
LOG_FILE    = os.path.join(DEPLOY_DIR, 'goapp.log')
DB_SEED     = os.path.join(SCRIPT_DIR, 'queue.db')    # 源码目录里的种子 DB（可选）

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


# ── 0. 确保部署目录存在 ───────────────────────────────────
os.makedirs(DEPLOY_DIR, exist_ok=True)


# ── 1. 编译到临时路径 goapp.new ───────────────────────────
step(f'编译 goapp → {BINARY_NEW} ...')
t0 = time.time()
res = subprocess.run(
    [GO_BIN, 'build', '-v', '-o', BINARY_NEW, '.'],
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


# ── 2. 停止旧 goapp 进程 ─────────────────────────────────
step('停止旧 goapp 进程 ...')

# 同时覆盖新路径（/tmp/goapp/goapp）和旧路径（.../goapp/goapp）
for pattern in [r'/tmp/goapp/goapp', r'goapp/goapp']:
    subprocess.run(['pkill', '-SIGTERM', '-f', pattern], capture_output=True)
time.sleep(2)

# 检查是否还有残留
r = subprocess.run(['pgrep', '-f', r'goapp/goapp'], capture_output=True, text=True)
if r.stdout.strip():
    print(f'  进程仍在（PID {r.stdout.strip()}），发送 SIGKILL ...')
    for pattern in [r'/tmp/goapp/goapp', r'goapp/goapp']:
        subprocess.run(['pkill', '-9', '-f', pattern], capture_output=True)
    time.sleep(2)

r = subprocess.run(['pgrep', '-f', r'goapp/goapp'], capture_output=True, text=True)
if r.stdout.strip():
    err(f'无法停止旧进程（PID {r.stdout.strip()}），请手动处理')
    sys.exit(1)

ok('旧进程已停止')


# ── 3. 原子替换二进制 mv goapp.new → goapp ───────────────
step(f'替换二进制: {BINARY_NEW} → {BINARY}')
os.replace(BINARY_NEW, BINARY)   # os.replace = atomic rename(2) on same filesystem
ok(f'二进制已替换')


# ── 4. 启动新进程 ─────────────────────────────────────────
if '--no-start' in sys.argv:
    print('\n  --no-start 模式，跳过启动')
    sys.exit(0)

step(f'启动新 goapp（运行目录: {DEPLOY_DIR}，日志: {LOG_FILE}）...')

# DB 冷启动恢复：queue.db 不存在时从源码目录拷贝种子
if not os.path.exists(DB_FILE):
    if os.path.exists(DB_SEED):
        shutil.copy2(DB_SEED, DB_FILE)
        ok(f'DB 已从种子恢复: {DB_SEED} → {DB_FILE}')
    else:
        ok(f'DB 不存在，goapp 将自动创建新数据库: {DB_FILE}')
else:
    ok(f'DB 已存在，直接使用: {DB_FILE}')

log_fh = open(LOG_FILE, 'w')
proc = subprocess.Popen(
    [BINARY, '-db', DB_FILE],
    cwd=DEPLOY_DIR,
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
print(f'\033[1;32m🚀 部署完成！\033[0m  PID={proc.pid}  运行目录: {DEPLOY_DIR}  日志: {LOG_FILE}\n')
