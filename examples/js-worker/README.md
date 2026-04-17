# GoQueue JavaScript Worker 示例

Node.js 实现的 GoQueue WebSocket Worker，含心跳、自动重连、多 handler 注册。

## 快速开始

```bash
npm install
node worker.js
```

## 环境变量

| 变量 | 默认值 | 说明 |
|---|---|---|
| `GOQUEUE_SERVER` | `ws://localhost:8080/ws/worker` | 服务端 WebSocket 地址 |
| `GOQUEUE_QUEUE` | `default` | 监听的队列名 |
| `GOQUEUE_API_KEY` | _(空)_ | API Key |

```bash
GOQUEUE_SERVER=ws://my-server:8080/ws/worker GOQUEUE_QUEUE=emails node worker.js
```

## 注册自定义 Handler

在 `worker.js` 的 `handlers` 对象中添加：

```js
const handlers = {
  my_job: async (data) => {
    // data 是任务的 data 字段（已解析为对象）
    // 返回字符串 → 成功日志
    // 抛出异常 → 任务失败
    return `processed: ${data.name}`;
  },
};
```

## 心跳机制

- 每 20 秒发送一次 `{"type":"ping"}`，服务端回复 `{"type":"pong"}`
- 连接断开后自动重连（默认 3 秒后）

## 投递任务

```bash
curl -X POST http://localhost:8080/api/jobs \
  -H "Content-Type: application/json" \
  -d '{"queue":"default","job_type":"send_email","data":{"to":"user@example.com","subject":"Hello"}}'
```

详细文档：[../../doc/README.md](../../doc/README.md)
