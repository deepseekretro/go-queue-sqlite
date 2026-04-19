# AGENTS.md — go-queue-sqlite 项目 AI 协作规范

> 本文件供 AI 助手（Deepnote AI、GitHub Copilot、Cursor 等）读取，
> 描述项目结构、开发规范和常用操作，避免重复解释。

---

## 项目概览

- **项目名**：go-queue-sqlite
- **仓库**：git@github.com:deepseekretro/go-queue-sqlite.git
- **语言**：Go 1.25（主服务）+ Python 3（工具脚本）
- **核心功能**：基于 SQLite + WebSocket 的轻量级任务队列系统，内置 Cache 服务、Cron 调度、Dashboard
- **主服务目录**：`goapp/`

---

## 目录结构

```
goapp/
├── main.go              # 主入口：DB 初始化、HTTP 路由、后台 goroutine
├── cache.go             # Cache 服务：SQLite 缓存，memory/file 两种后端
├── ws.go                # WebSocket Worker 协议处理
├── auth.go              # 登录鉴权（Dashboard）
├── autoscale.go         # 动态扩缩容
├── backend.go           # 队列核心逻辑
├── p2.go / p3.go / p4.go  # 批量任务、限流、Cron、Tags 等高级功能
├── static.go            # 静态资源嵌入
├── frontend.go          # Dashboard HTML
├── README.md            # 用户文档（中文）
├── rebuild.py           # 编译 + 热重启脚本（在 goapp/ 目录下运行）
├── git_commit_push.py   # 一键 git add / commit / push（自动注入 gitkey）
├── go.mod / go.sum
└── examples/
    ├── cache_bench/     # Cache API 压力测试工具
    ├── goworker/        # Go Worker 示例
    ├── js-worker/       # JavaScript Worker 示例
    └── python-worker/   # Python Worker 示例
```

---

## 数据库

| 变量 | 文件 | 用途 |
|---|---|---|
| `db`（`*sql.DB`） | `/tmp/goapp/queue.db` | 队列任务、Cron、Tags、Batch、限流 |
| `cacheDB`（`*sql.DB`） | `:memory:` 或 `cache.db` | Cache 服务，与主 DB 完全隔离 |

- 两个连接均使用 WAL 模式，`SetMaxOpenConns(1)` 避免写锁冲突
- 所有写操作通过 `dbArbiterCh` channel 串行化

---

## 启动参数

```bash
./goapp                          # 默认：memory cache，DB=/tmp/queue.db，端口 8080
./goapp -cache-backend=file      # 使用文件 cache（低内存，重启不丢失）
./goapp -db=/data/queue.db       # 自定义 DB 路径
```

**环境变量：**

| 变量 | 默认 | 说明 |
|---|---|---|
| `DASHBOARD_USER` | `admin` | Dashboard 登录用户名 |
| `DASHBOARD_PASS` | `admin` | Dashboard 登录密码 |
| `WS_JOB_TIMEOUT_SEC` | `300` | WS Worker 单任务超时（秒） |
| `STALE_JOB_TIMEOUT_SEC` | `300` | Stale Job Reaper 阈值（秒） |
| `VACUUM_INTERVAL_SEC` | `300` | 空闲 VACUUM 检查间隔 |
| `VACUUM_MIN_IDLE_SEC` | `60` | 触发 VACUUM 所需最小空闲时间 |

---

## 常用操作

### 编译 & 热重启

```bash
cd goapp
python3 rebuild.py
```

### 提交 & 推送到 GitHub

```bash
cd goapp

# 提交所有变更
python3 git_commit_push.py "提交信息"

# 只提交指定文件
python3 git_commit_push.py "提交信息" cache.go README.md
```

> 脚本会自动注入 `/work/.deepnote/gitkey`，无需手动设置 `GIT_SSH_COMMAND`。

### 查看日志

```bash
tail -f /tmp/goapp/goapp.log
```

---

## 开发规范

### Go 代码

- 所有新功能写在独立文件（如 `cache.go`、`p4.go`），不要把所有代码堆在 `main.go`
- 写操作必须通过 `dbExec()` / `dbExecResult()` 走 `dbArbiterCh`，禁止直接调用 `db.Exec()`
- 新增 goroutine 必须监听 `stopGoWorker` channel 以支持优雅退出
- 日志格式：`[模块名] 描述`，例如 `[Cache] 初始化成功`
- 每次修改后必须用 `rebuild.py` 编译验证，确保无语法错误

### 文档

- 用户文档统一维护在 `goapp/README.md`（中文）
- 新增功能需同步更新 README 目录和对应章节
- 文档末尾时间戳格式：`*文档更新时间：YYYY-MM-DD（变更说明）*`

### Git

- 提交信息格式：`<type>: <描述>`
  - `feat:` 新功能
  - `fix:` Bug 修复
  - `docs:` 文档更新
  - `chore:` 工具/配置变更
  - `perf:` 性能优化
- 使用 `git_commit_push.py` 提交，不要手动设置 `GIT_SSH_COMMAND`

---

## Cache 服务速查

| 接口 | 方法 | 说明 |
|---|---|---|
| `/api/cache/:key` | POST | 写入缓存（body: `{data, ttl}`） |
| `/api/cache/:key` | GET | 读取缓存 |
| `/api/cache/:key` | DELETE | 删除缓存 |
| `/api/cache-stats` | GET | 统计（total/active/expired/backend） |
| `/api/cache-keys` | GET | 列出所有缓存项（最多 500 条） |
| `/cache-dashboard` | GET | 可视化管理界面 |

---

## 注意事项

- 当前环境**没有 `go` 命令**，编译必须通过 `python3 rebuild.py` 完成
- `GIT_SSH_COMMAND` 环境变量末尾有多余引号（Deepnote 已知问题），`git_commit_push.py` 已自动处理
- SQLite 使用 `modernc.org/sqlite`（纯 Go 实现，无 CGO 依赖）
- 服务固定监听 `:8080`，运行目录 `/tmp/goapp/`

