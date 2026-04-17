# GoQueue Python Worker 示例

Python 实现的 GoQueue WebSocket Worker，含心跳、自动重连、多 handler 注册。

## 快速开始

```bash
pip install -r requirements.txt
python worker.py
```

## 环境变量

| 变量 | 默认值 | 说明 |
|---|---|---|
| `GOQUEUE_SERVER` | `ws://localhost:8080/ws/worker` | 服务端 WebSocket 地址 |
| `GOQUEUE_QUEUE` | `default` | 监听的队列名 |
| `GOQUEUE_API_KEY` | _(空)_ | API Key |

```bash
GOQUEUE_SERVER=ws://my-server:8080/ws/worker GOQUEUE_QUEUE=emails python worker.py
```

## 注册自定义 Handler

```python
def handle_my_job(data: dict) -> str:
    # data 是任务的 data 字段（已解析为 dict）
    # 返回字符串 → 成功日志
    # 抛出异常 → 任务失败
    name = data.get("name", "")
    return f"processed: {name}"

worker.register("my_job", handle_my_job)
```

## 心跳机制

- 连接成功后启动后台心跳线程，每 20 秒发送 `{"type":"ping"}`
- 服务端回复 `{"type":"pong"}`，静默处理
- 连接断开后自动重连（默认 3 秒后）

## 后台线程模式

```python
worker = GoQueueWorker()
worker.register("send_email", handle_send_email)
t = worker.start()   # 非阻塞，返回线程对象
# ... 主线程做其他事 ...
worker.stop()        # 停止 Worker
```

## 投递任务

```bash
curl -X POST http://localhost:8080/api/jobs \
  -H "Content-Type: application/json" \
  -d '{"queue":"default","job_type":"send_email","data":{"to":"user@example.com","subject":"Hello"}}'
```

详细文档：[../../doc/README.md](../../doc/README.md)
