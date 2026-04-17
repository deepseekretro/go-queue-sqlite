# GoQueue 使用文档


## 示例仓库

本项目在 GitHub 仓库的 `examples/` 目录下提供以下官方示例：

| 示例 | 语言 / 环境 | 链接 |
|---|---|---|
| GoWorker | Go | [examples/goworker](https://github.com/deepseekretro/go-queue-sqlite/tree/master/examples/goworker) |
| JS Worker | Node.js | [examples/js-worker](https://github.com/deepseekretro/go-queue-sqlite/tree/master/examples/js-worker) |
| Python Worker | Python | [examples/python-worker](https://github.com/deepseekretro/go-queue-sqlite/tree/master/examples/python-worker) |
| Tampermonkey 油猴脚本 | 浏览器 | [examples/tampermonkey](https://github.com/deepseekretro/go-queue-sqlite/tree/master/examples/tampermonkey) |

---

> 版本 1.0.0 · 基于 WebSocket 的轻量级任务队列系统

---

## 目录

1. [快速开始](#1-快速开始)
2. [架构概览](#2-架构概览)
3. [服务端（goapp）](#3-服务端goapp)
   - 3.1 [启动与配置](#31-启动与配置)
   - 3.2 [REST API 参考](#32-rest-api-参考)
   - 3.3 [WebSocket Worker 协议](#33-websocket-worker-协议)
   - 3.4 [心跳机制](#34-心跳机制)
4. [GoWorker 示例程序](#4-goworker-示例程序)
5. [JavaScript 示例程序](#5-javascript-示例程序)
6. [Python 示例程序](#6-python-示例程序)
7. [高级功能](#7-高级功能)
   - 7.1 [任务优先级与延迟](#71-任务优先级与延迟)
   - 7.2 [去重（Dedup）](#72-去重dedup)
   - 7.3 [任务链（Chain）](#73-任务链chain)
   - 7.4 [批量任务（Batch）](#74-批量任务batch)
   - 7.5 [限流（Rate Limit）](#75-限流rate-limit)
   - 7.6 [动态扩缩容（AutoScale）](#76-动态扩缩容autoscale)
8. [Dashboard 说明](#8-dashboard-说明)

---

## 1. 快速开始

```bash
# 克隆并编译
cd goapp
go build -o goapp .

# 启动服务（默认端口 8080，默认账号 admin/admin）
./goapp

# 另一个终端：启动 GoWorker
cd goworker
go build -o goworker .
./goworker -server ws://localhost:8080/ws/worker -queue default -concurrency 2

# 投递一个任务
curl -X POST http://localhost:8080/api/jobs \
  -H "Content-Type: application/json" \
  -d '{"queue":"default","job_type":"send_email","data":{"to":"user@example.com","subject":"Hello"}}'
```

打开浏览器访问 http://localhost:8080 即可看到 Dashboard。

---

## 2. 架构概览

```
┌─────────────────────────────────────────────────────┐
│                    goapp (服务端)                    │
│                                                     │
│  REST API (/api/*)  ←→  SQLite (jobs / batches)    │
│  WebSocket (/ws/worker)  ←→  WsHub (Worker 注册表) │
│  Dashboard (/)  ←→  SSE (/api/events)              │
└──────────────┬──────────────────────────────────────┘
               │  WebSocket (ws://host/ws/worker?queue=xxx)
    ┌──────────┴──────────┐
    │   Worker 进程/页面   │
    │  (Go / JS / Python) │
    └─────────────────────┘
```

**任务流转：**

```
投递方 POST /api/jobs
  → jobs 表 status=pending
  → WsDispatcher 轮询 pending 任务
  → 推送给空闲 WS Worker（status=running）
  → Worker 处理完毕发回 result
  → status=done / failed
```

---

## 3. 服务端（goapp）

### 3.1 启动与配置

所有配置均通过**环境变量**注入，无需配置文件：

| 环境变量 | 默认值 | 说明 |
|---|---|---|
| `DASHBOARD_USER` | `admin` | Dashboard 登录用户名 |
| `DASHBOARD_PASS` | `admin` | Dashboard 登录密码 |
| `API_KEY` | _(空，不鉴权)_ | REST API 鉴权 Key；设置后所有 `/api/*` 请求须携带 `X-API-Key` header |
| `DB_PATH` | `queue.db` | SQLite 数据库文件路径 |

```bash
# 生产环境示例
DASHBOARD_USER=ops DASHBOARD_PASS=s3cr3t API_KEY=my-api-key ./goapp
```

服务固定监听 `:8080`，支持优雅关闭（SIGINT / SIGTERM）。

---

### 3.2 REST API 参考

所有 `/api/*` 接口均支持 CORS（`Access-Control-Allow-Origin: *`）。  
若设置了 `API_KEY` 环境变量，每个请求须携带：

```
X-API-Key: <your-api-key>
```

---

#### POST /api/jobs — 投递任务

**请求体（JSON）：**

```json
{
  "queue":        "default",        // 队列名，默认 "default"
  "job_type":     "send_email",     // 任务类型（Worker 按此路由到 handler）
  "data":         { ... },          // 任意 JSON，传给 Worker
  "delay":        0,                // 延迟秒数（0 = 立即可用）
  "priority":     5,                // 优先级 1-10，数字越大越优先，默认 5
  "dedup_key":    "",               // 去重 Key，相同 key 的 pending 任务只保留一个
  "timeout_sec":  60,               // 任务超时秒数（传给 Worker，默认 60）
  "max_attempts": 3,                // 最大重试次数（传给 Worker）
  "backoff":      [10, 30, 60],     // 自定义重试延迟（秒），按次数依次使用
  "next_job":     { ... }           // 任务链：本任务完成后自动投递的下一个任务
}
```

**响应（201）：**

```json
{ "job_id": 42, "queue": "default", "status": "pending" }
```

**去重响应（200）：**

```json
{ "job_id": 0, "queue": "default", "status": "deduplicated", "message": "job skipped: duplicate dedup_key" }
```

---

#### GET /api/jobs — 查询任务列表

**Query 参数：**

| 参数 | 说明 | 示例 |
|---|---|---|
| `queue` | 按队列过滤 | `?queue=default` |
| `status` | 按状态过滤（pending/running/done/failed） | `?status=pending` |
| `limit` | 返回条数，默认 50 | `?limit=100` |

**响应（200）：** Job 对象数组

```json
[
  {
    "id": 42,
    "queue": "default",
    "payload": "{"job_type":"send_email","data":{...}}",
    "attempts": 1,
    "status": "done",
    "priority": 5,
    "available_at": 1713340800,
    "started_at": 1713340801,
    "finished_at": 1713340802,
    "created_at": 1713340800,
    "updated_at": 1713340802
  }
]
```

---

#### DELETE /api/jobs/{id} — 取消任务

取消一个 `pending` 状态的任务（`running` 任务无法取消）。

**响应（200）：**

```json
{ "message": "job 42 cancelled" }
```

---

#### GET /api/stats — 队列统计

**响应（200）：**

```json
{
  "pending": 5,
  "running": 2,
  "done": 1024,
  "failed": 3,
  "failed_jobs_table": 3,
  "ws_workers": 2,
  "version": "1.0.0",
  "queues": ["default", "emails"],
  "avg_duration_ms": 312,
  "queue_pending": { "default": 3, "emails": 2 }
}
```

---

#### POST /api/jobs/retry-failed — 重试所有失败任务

将所有 `failed` 状态任务重置为 `pending`。

**响应（200）：** `{ "message": "failed jobs requeued" }`

---

#### DELETE /api/jobs/failed — 清空失败任务

**响应（200）：** `{ "message": "failed jobs cleared" }`

---

#### GET /api/workers — 查看在线 Worker

**响应（200）：**

```json
[
  {
    "id": "ws-1713340800000000000",
    "queue": "default",
    "idle": true,
    "current_job_id": 0,
    "heartbeat": { ... }
  }
]
```

---

#### DELETE /api/workers/{id} — 踢掉 Worker

强制断开指定 Worker 的 WebSocket 连接，当前任务会被放回 `pending`。

**响应（200）：** `{ "message": "worker kicked" }`

---

#### GET /api/rate-limits — 查看限流配置

**响应（200）：** 各 job_type 的限流统计

---

#### POST /api/rate-limits — 设置限流

```json
{ "job_type": "send_email", "max_per_min": 60 }
```

`max_per_min <= 0` 表示移除该 job_type 的限流。

---

#### GET /api/autoscale — 查看自动扩缩容池

---

#### POST /api/autoscale — 配置自动扩缩容

```json
{
  "queue":        "default",
  "min_workers":  1,
  "max_workers":  10,
  "scale_up_at":  20,
  "scale_down_at": 2,
  "check_sec":    10
}
```

| 字段 | 说明 |
|---|---|
| `scale_up_at` | pending 任务数超过此值时扩容 |
| `scale_down_at` | pending 任务数低于此值时缩容 |
| `check_sec` | 检查间隔（秒） |

---

#### GET /api/backend — 存储后端信息

返回当前使用的存储后端（SQLite）及统计信息。

---

#### POST /api/batches — 创建批量任务

```json
{
  "name": "daily-report-batch",
  "jobs": [
    { "queue": "default", "job_type": "generate_report", "data": { "name": "report-A" } },
    { "queue": "default", "job_type": "generate_report", "data": { "name": "report-B" } }
  ],
  "then_job": {
    "queue": "default",
    "job_type": "send_email",
    "data": { "to": "admin@example.com", "subject": "All reports done" }
  }
}
```

`then_job` 为可选，批次内所有任务完成后自动投递。

**响应（201）：** `{ "batch_id": 7, "job_ids": [43, 44], "status": "pending" }`

---

#### GET /api/batches/{id} — 查询批次状态

**响应（200）：**

```json
{
  "id": 7,
  "name": "daily-report-batch",
  "total": 2,
  "done": 2,
  "failed": 0,
  "pending": 0,
  "status": "done"
}
```

---

#### GET /api/events — SSE 实时推送

Dashboard 使用的 Server-Sent Events 流，每次任务状态变化时推送 `stats` 事件。

---

### 3.3 WebSocket Worker 协议

Worker 通过 WebSocket 连接到服务端，URL 格式：

```
ws://host:8080/ws/worker?queue=<队列名>
```

**消息方向与类型：**

| 方向 | type | 说明 |
|---|---|---|
| Server → Worker | `connected` | 连接成功确认 |
| Server → Worker | `job` | 下发任务 |
| Server → Worker | `ack` | 任务完成确认 |
| Server → Worker | `pong` | 心跳响应 |
| Worker → Server | `result` | 任务处理结果 |
| Worker → Server | `ping` | 心跳探测（JSON 格式） |

**connected 消息：**

```json
{ "type": "connected", "message": "Worker ws-xxx connected, queue=default" }
```

**job 消息（Server → Worker）：**

```json
{
  "type":    "job",
  "job_id":  42,
  "queue":   "default",
  "job_type": "send_email",
  "payload": "{"job_type":"send_email","data":{"to":"user@example.com"},"timeout_sec":60}"
}
```

**result 消息（Worker → Server）：**

```json
// 成功
{ "type": "result", "job_id": 42, "success": true, "log": "Email sent to user@example.com" }

// 失败
{ "type": "result", "job_id": 42, "success": false, "error": "SMTP connection refused" }
```

---

### 3.4 心跳机制

为防止 WebSocket 连接被中间代理（Nginx、负载均衡器等）因空闲超时断开，系统实现了双层心跳：

**层 1 — WebSocket 协议级 Ping/Pong（RFC 6455）**

服务端 `ws.go` 的 `ReadMessage()` 自动处理 opcode=9（Ping）帧，立即回复 opcode=10（Pong）帧，无需 Worker 感知。

**层 2 — JSON 应用级 Ping/Pong**

Worker 每 20 秒发送一条 JSON 消息：

```json
{ "type": "ping" }
```

服务端 read goroutine 识别后立即回复：

```json
{ "type": "pong", "message": "pong" }
```

**服务端超时保护：**

- Worker 处理任务时，若 30 秒内未收到 result，服务端自动断开连接并将任务放回 `pending`
- Stale Job Reaper 每 30 秒扫描一次，将长时间处于 `running` 状态的任务放回 `pending`

---

## 4. GoWorker 示例程序

`goworker/` 目录是一个完整的 Go Worker 示例，可直接编译使用，也可作为自定义 Worker 的模板。

### 启动参数

```bash
./goworker [选项]

  -server      ws://localhost:8080/ws/worker   服务端 WebSocket 地址
  -queue       default                          监听的队列名
  -api-key     ""                               API Key（对应服务端 API_KEY 环境变量）
  -concurrency 1                                每个连接的并发任务数
  -connections 1                                并行 WebSocket 连接数
  -reconnect   3s                               断线重连间隔
```

环境变量覆盖：`QUEUE_SERVER`、`QUEUE_NAME`、`API_KEY`

### 注册自定义 Handler

```go
package main

import (
    "context"
    "encoding/json"
    "fmt"
)

func init() {
    // 注册 "resize_image" 任务处理器
    RegisterHandler("resize_image", func(ctx context.Context, jobID int64, data json.RawMessage) (string, error) {
        var d struct {
            URL    string `json:"url"`
            Width  int    `json:"width"`
            Height int    `json:"height"`
        }
        if err := json.Unmarshal(data, &d); err != nil {
            return "", err
        }

        // 实际处理逻辑...
        // ctx 已设置超时，可用于取消长时间操作
        select {
        case <-ctx.Done():
            return "", fmt.Errorf("cancelled: %v", ctx.Err())
        default:
        }

        return fmt.Sprintf("Image %s resized to %dx%d", d.URL, d.Width, d.Height), nil
    })
}
```

### 心跳实现（已内置）

GoWorker 在 `runSession()` 中已内置心跳，每 20 秒发送一次 WebSocket Ping 帧：

```go
// 心跳 ticker（每 20s 发一次 ping，防止连接被服务端超时断开）
pingTicker := time.NewTicker(20 * time.Second)
defer pingTicker.Stop()

// 在 select 循环中：
case <-pingTicker.C:
    if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
        log.Printf("[Worker] Ping failed: %v", err)
        return true // 触发重连
    }
```

---

## 5. JavaScript 示例程序

以下示例可直接在浏览器中运行，也可在 Node.js（需 `ws` 包）中使用。

```javascript
// ─── GoQueue JavaScript Worker ───────────────────────────────────────────────
// 依赖：浏览器原生 WebSocket（或 Node.js: npm install ws）

const WS_URL = 'ws://localhost:8080/ws/worker';

/**
 * 创建一个 WebSocket Worker
 * @param {string} queue      - 监听的队列名，默认 "default"
 * @param {Object} handlers   - job_type → async function(data) 的映射
 * @param {Object} [options]  - 可选配置
 * @param {string} [options.apiKey]       - API Key（X-API-Key header，仅 Node.js 有效）
 * @param {number} [options.pingInterval] - 心跳间隔毫秒，默认 20000
 * @param {number} [options.reconnectDelay] - 断线重连间隔毫秒，默认 3000
 * @returns {{ stop: Function }} - 返回控制对象
 */
function createWorker(queue = 'default', handlers = {}, options = {}) {
  const {
    pingInterval = 20000,
    reconnectDelay = 3000,
  } = options;

  let ws = null;
  let pingTimer = null;
  let stopped = false;

  function connect() {
    if (stopped) return;

    const url = `${WS_URL}?queue=${queue}`;
    console.log(`[Worker] Connecting to ${url}`);
    ws = new WebSocket(url);

    // ── 连接成功 ──────────────────────────────────────────────────────────────
    ws.onopen = () => {
      console.log(`[Worker] Connected, queue=${queue}`);

      // 心跳：每 20s 发一次 JSON ping，防止连接被中间代理超时断开
      pingTimer = setInterval(() => {
        if (ws && ws.readyState === WebSocket.OPEN) {
          ws.send(JSON.stringify({ type: 'ping' }));
        }
      }, pingInterval);
    };

    // ── 接收消息 ──────────────────────────────────────────────────────────────
    ws.onmessage = async (event) => {
      let msg;
      try { msg = JSON.parse(event.data); } catch { return; }

      switch (msg.type) {
        case 'connected':
          console.log(`[Worker] Server: ${msg.message}`);
          break;

        case 'pong':
          // 心跳响应，静默处理
          break;

        case 'ack':
          console.log(`[Worker] ACK: ${msg.message}`);
          break;

        case 'job': {
          const payload = JSON.parse(msg.payload);
          const data = payload.data;
          console.log(`[Worker] Job #${msg.job_id} type=${msg.job_type}`);

          try {
            const handler = handlers[msg.job_type];
            if (!handler) throw new Error(`No handler for: ${msg.job_type}`);

            const log = await handler(data);

            ws.send(JSON.stringify({
              type: 'result',
              job_id: msg.job_id,
              success: true,
              log: log || 'done',
            }));
          } catch (err) {
            console.error(`[Worker] Job #${msg.job_id} failed:`, err.message);
            ws.send(JSON.stringify({
              type: 'result',
              job_id: msg.job_id,
              success: false,
              error: err.message,
            }));
          }
          break;
        }
      }
    };

    // ── 连接关闭 ──────────────────────────────────────────────────────────────
    ws.onclose = () => {
      console.log('[Worker] Disconnected');
      // 清除心跳定时器
      if (pingTimer) { clearInterval(pingTimer); pingTimer = null; }

      // 自动重连
      if (!stopped) {
        console.log(`[Worker] Reconnecting in ${reconnectDelay}ms...`);
        setTimeout(connect, reconnectDelay);
      }
    };

    ws.onerror = (e) => {
      console.error('[Worker] WebSocket error:', e.message || e);
    };
  }

  connect();

  return {
    stop() {
      stopped = true;
      if (pingTimer) { clearInterval(pingTimer); pingTimer = null; }
      if (ws) { ws.close(); ws = null; }
    },
  };
}

// ─── 使用示例 ─────────────────────────────────────────────────────────────────

const worker = createWorker('default', {
  send_email: async (data) => {
    console.log(`Sending email to ${data.to}: ${data.subject}`);
    await sleep(500); // 模拟耗时
    return `Email sent to ${data.to}`;
  },

  generate_report: async (data) => {
    console.log(`Generating report: ${data.name}`);
    await sleep(1000);
    return `Report ${data.name} generated`;
  },

  resize_image: async (data) => {
    console.log(`Resizing image: ${data.url}`);
    await sleep(800);
    return `Image resized: ${data.url}`;
  },
});

// 停止 Worker（如需要）
// worker.stop();

function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }
```

### Node.js 使用

```bash
npm install ws
```

```javascript
const WebSocket = require('ws');
// 将上方代码中的 WebSocket 替换为 require('ws') 即可
// 注意：Node.js 的 ws 库不支持在构造函数中传 header，
// 若需要 API Key，可在 URL 中传 query 参数（服务端暂不支持），
// 或通过 ws 库的 headers 选项：
// new WebSocket(url, { headers: { 'X-API-Key': 'your-key' } })
```

---

## 6. Python 示例程序

```bash
pip install websocket-client
```

```python
#!/usr/bin/env python3
"""
GoQueue Python Worker 示例
依赖：pip install websocket-client
"""

import json
import threading
import time
import logging
from typing import Callable, Dict, Any, Optional
import websocket

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(message)s",
)
log = logging.getLogger("goqueue-worker")


class GoQueueWorker:
    """
    GoQueue WebSocket Worker

    用法：
        worker = GoQueueWorker(
            server_url="ws://localhost:8080/ws/worker",
            queue="default",
            api_key="",          # 可选，对应服务端 API_KEY 环境变量
            ping_interval=20,    # 心跳间隔（秒）
            reconnect_delay=3,   # 断线重连间隔（秒）
        )
        worker.register("send_email", handle_send_email)
        worker.run()             # 阻塞运行；或 worker.start() 后台线程
    """

    def __init__(
        self,
        server_url: str = "ws://localhost:8080/ws/worker",
        queue: str = "default",
        api_key: str = "",
        ping_interval: int = 20,
        reconnect_delay: int = 3,
    ):
        self.server_url = server_url
        self.queue = queue
        self.api_key = api_key
        self.ping_interval = ping_interval
        self.reconnect_delay = reconnect_delay
        self._handlers: Dict[str, Callable] = {}
        self._ws: Optional[websocket.WebSocketApp] = None
        self._ping_thread: Optional[threading.Thread] = None
        self._stop_event = threading.Event()

    def register(self, job_type: str, handler: Callable[[Dict[str, Any]], str]) -> None:
        """
        注册任务处理器

        handler 签名：def handler(data: dict) -> str
          - data：任务的 data 字段（已解析为 dict）
          - 返回值：日志字符串（成功时记录）
          - 抛出异常：任务标记为失败，异常信息作为 error
        """
        self._handlers[job_type] = handler
        log.info(f"Registered handler: {job_type}")

    def run(self) -> None:
        """阻塞运行，自动重连"""
        while not self._stop_event.is_set():
            self._connect()
            if not self._stop_event.is_set():
                log.info(f"Reconnecting in {self.reconnect_delay}s...")
                time.sleep(self.reconnect_delay)

    def start(self) -> threading.Thread:
        """在后台线程中运行，返回线程对象"""
        t = threading.Thread(target=self.run, daemon=True)
        t.start()
        return t

    def stop(self) -> None:
        """停止 Worker"""
        self._stop_event.set()
        if self._ws:
            self._ws.close()

    # ── 内部方法 ──────────────────────────────────────────────────────────────

    def _connect(self) -> None:
        url = f"{self.server_url}?queue={self.queue}"
        headers = {}
        if self.api_key:
            headers["X-API-Key"] = self.api_key

        log.info(f"Connecting to {url}")

        self._ws = websocket.WebSocketApp(
            url,
            header=headers,
            on_open=self._on_open,
            on_message=self._on_message,
            on_error=self._on_error,
            on_close=self._on_close,
        )
        self._ws.run_forever()

    def _on_open(self, ws) -> None:
        log.info(f"Connected, queue={self.queue}")
        # 启动心跳线程
        self._ping_thread = threading.Thread(
            target=self._heartbeat_loop, args=(ws,), daemon=True
        )
        self._ping_thread.start()

    def _heartbeat_loop(self, ws) -> None:
        """每 ping_interval 秒发送一次 JSON ping"""
        while not self._stop_event.is_set():
            time.sleep(self.ping_interval)
            try:
                if ws.sock and ws.sock.connected:
                    ws.send(json.dumps({"type": "ping"}))
                    log.debug("Sent ping")
                else:
                    break
            except Exception as e:
                log.warning(f"Ping failed: {e}")
                break

    def _on_message(self, ws, raw: str) -> None:
        try:
            msg = json.loads(raw)
        except json.JSONDecodeError:
            log.warning(f"Invalid JSON: {raw}")
            return

        msg_type = msg.get("type")

        if msg_type == "connected":
            log.info(f"Server: {msg.get('message')}")

        elif msg_type == "pong":
            log.debug("Received pong")  # 心跳响应，静默处理

        elif msg_type == "ack":
            log.info(f"ACK: {msg.get('message')}")

        elif msg_type == "job":
            self._handle_job(ws, msg)

        else:
            log.debug(f"Unknown message type: {msg_type}")

    def _handle_job(self, ws, msg: dict) -> None:
        job_id = msg["job_id"]
        job_type = msg["job_type"]

        try:
            payload = json.loads(msg["payload"])
            data = payload.get("data", {})
        except (json.JSONDecodeError, KeyError) as e:
            self._send_result(ws, job_id, success=False, error=f"Invalid payload: {e}")
            return

        log.info(f"Job #{job_id} type={job_type} queue={msg.get('queue')}")

        handler = self._handlers.get(job_type)
        if not handler:
            self._send_result(ws, job_id, success=False,
                              error=f"No handler for job_type: {job_type!r}")
            return

        try:
            result_log = handler(data)
            log.info(f"Job #{job_id} done: {result_log}")
            self._send_result(ws, job_id, success=True, log=result_log or "done")
        except Exception as e:
            log.error(f"Job #{job_id} failed: {e}")
            self._send_result(ws, job_id, success=False, error=str(e))

    def _send_result(self, ws, job_id: int, *, success: bool,
                     log: str = "", error: str = "") -> None:
        msg = {"type": "result", "job_id": job_id, "success": success}
        if success:
            msg["log"] = log
        else:
            msg["error"] = error
        ws.send(json.dumps(msg))

    def _on_error(self, ws, error) -> None:
        log.error(f"WebSocket error: {error}")

    def _on_close(self, ws, close_status_code, close_msg) -> None:
        log.info(f"Disconnected (code={close_status_code}, msg={close_msg})")


# ─── 使用示例 ─────────────────────────────────────────────────────────────────

def handle_send_email(data: dict) -> str:
    to = data.get("to", "unknown")
    subject = data.get("subject", "")
    log.info(f"Sending email to {to}: {subject}")
    time.sleep(0.5)  # 模拟耗时
    return f"Email sent to {to}"


def handle_generate_report(data: dict) -> str:
    name = data.get("name", "unknown")
    log.info(f"Generating report: {name}")
    time.sleep(1.0)
    return f"Report {name} generated"


def handle_resize_image(data: dict) -> str:
    url = data.get("url", "")
    w, h = data.get("width", 800), data.get("height", 600)
    log.info(f"Resizing {url} to {w}x{h}")
    time.sleep(0.8)
    return f"Image {url} resized to {w}x{h}"


if __name__ == "__main__":
    worker = GoQueueWorker(
        server_url="ws://localhost:8080/ws/worker",
        queue="default",
        # api_key="your-api-key",  # 若服务端设置了 API_KEY
        ping_interval=20,
        reconnect_delay=3,
    )

    worker.register("send_email", handle_send_email)
    worker.register("generate_report", handle_generate_report)
    worker.register("resize_image", handle_resize_image)

    log.info("GoQueue Python Worker starting...")
    worker.run()  # 阻塞，Ctrl+C 退出
```

---

## 7. 高级功能

### 7.1 任务优先级与延迟

```bash
# 高优先级任务（priority=9，数字越大越优先）
curl -X POST http://localhost:8080/api/jobs \
  -H "Content-Type: application/json" \
  -d '{"queue":"default","job_type":"send_email","data":{"to":"vip@example.com"},"priority":9}'

# 延迟 60 秒后执行
curl -X POST http://localhost:8080/api/jobs \
  -H "Content-Type: application/json" \
  -d '{"queue":"default","job_type":"generate_report","data":{"name":"daily"},"delay":60}'
```

### 7.2 去重（Dedup）

相同 `dedup_key` 的任务在 `pending` 状态下只保留一个，重复投递会被静默忽略：

```bash
curl -X POST http://localhost:8080/api/jobs \
  -H "Content-Type: application/json" \
  -d '{"queue":"default","job_type":"generate_report","data":{"name":"daily"},"dedup_key":"daily-report-2026-04-17"}'
```

### 7.3 任务链（Chain）

任务完成后自动投递下一个任务：

```json
{
  "queue": "default",
  "job_type": "generate_report",
  "data": { "name": "monthly" },
  "next_job": {
    "queue": "default",
    "job_type": "send_email",
    "data": { "to": "admin@example.com", "subject": "Monthly report ready" }
  }
}
```

### 7.4 批量任务（Batch）

```bash
curl -X POST http://localhost:8080/api/batches \
  -H "Content-Type: application/json" \
  -d '{
    "name": "weekly-reports",
    "jobs": [
      {"queue":"default","job_type":"generate_report","data":{"name":"report-A"}},
      {"queue":"default","job_type":"generate_report","data":{"name":"report-B"}},
      {"queue":"default","job_type":"generate_report","data":{"name":"report-C"}}
    ],
    "then_job": {
      "queue":"default","job_type":"send_email",
      "data":{"to":"admin@example.com","subject":"All reports done"}
    }
  }'

# 查询批次状态
curl http://localhost:8080/api/batches/1
```

### 7.5 限流（Rate Limit）

```bash
# 设置 send_email 每分钟最多 60 次
curl -X POST http://localhost:8080/api/rate-limits \
  -H "Content-Type: application/json" \
  -d '{"job_type":"send_email","max_per_min":60}'

# 查看当前限流配置
curl http://localhost:8080/api/rate-limits

# 移除限流（max_per_min=0）
curl -X POST http://localhost:8080/api/rate-limits \
  -H "Content-Type: application/json" \
  -d '{"job_type":"send_email","max_per_min":0}'
```

### 7.6 动态扩缩容（AutoScale）

```bash
# 为 default 队列配置自动扩缩容
curl -X POST http://localhost:8080/api/autoscale \
  -H "Content-Type: application/json" \
  -d '{
    "queue": "default",
    "min_workers": 1,
    "max_workers": 10,
    "scale_up_at": 20,
    "scale_down_at": 2,
    "check_sec": 10
  }'

# 查看当前扩缩容池状态
curl http://localhost:8080/api/autoscale
```

---

## 8. Dashboard 说明

访问 http://localhost:8080 后使用配置的账号密码登录（默认 `admin` / `admin`）。

Dashboard 提供以下功能：

| 功能 | 说明 |
|---|---|
| 实时统计 | pending / running / done / failed 任务数，通过 SSE 实时刷新 |
| 任务列表 | 按队列、状态过滤，支持取消 pending 任务 |
| 投递任务 | 图形界面投递任务，支持所有参数 |
| WS Worker 面板 | 在浏览器中直接启动 WebSocket Worker，实时查看处理日志 |
| Worker 管理 | 查看在线 Worker 列表，支持强制踢掉 Worker |
| 失败任务 | 查看失败详情，一键重试或清空 |
| 代码示例 | 内置 Go / JavaScript / Python Worker 示例代码 |
| 限流配置 | 图形界面设置各 job_type 的限流规则 |
| 数据库重置 | 一键清空所有任务数据（开发调试用） |

---

*文档生成时间：2026-04-17*
