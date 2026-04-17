# GoWorker 示例程序

这是 GoQueue 的官方 Go Worker 示例，展示如何用 Go 编写一个完整的 WebSocket Worker。

## 快速开始

```bash
# 编译
go build -o goworker .

# 启动（确保 goapp 已在 localhost:8080 运行）
./goworker -server ws://localhost:8080/ws/worker -queue default -concurrency 2
```

## 启动参数

| 参数 | 默认值 | 说明 |
|---|---|---|
| `-server` | `ws://localhost:8080/ws/worker` | 服务端 WebSocket 地址 |
| `-queue` | `default` | 监听的队列名 |
| `-api-key` | _(空)_ | API Key（对应服务端 `API_KEY` 环境变量） |
| `-concurrency` | `1` | 每个连接的并发任务数 |
| `-connections` | `1` | 并行 WebSocket 连接数 |
| `-reconnect` | `3s` | 断线重连间隔 |

环境变量覆盖：`QUEUE_SERVER`、`QUEUE_NAME`、`API_KEY`

## 内置 Job Handlers

| job_type | 说明 |
|---|---|
| `generate_report` | 模拟生成报告（100–500ms） |
| `send_email` | 模拟发送邮件（50–200ms） |
| `resize_image` | 模拟图片处理（200–800ms） |
| `data_sync` | 模拟数据同步（300–1000ms） |
| `fail_test` | 总是失败，用于测试重试机制 |

## 注册自定义 Handler

```go
func init() {
    RegisterHandler("my_job", func(ctx context.Context, jobID int64, data json.RawMessage) (string, error) {
        var d struct {
            Name string `json:"name"`
        }
        json.Unmarshal(data, &d)

        // 实际处理逻辑（ctx 已设置超时）
        select {
        case <-ctx.Done():
            return "", fmt.Errorf("cancelled: %v", ctx.Err())
        default:
        }

        return fmt.Sprintf("processed: %s", d.Name), nil
    })
}
```

## 心跳机制

Worker 内置双层心跳，防止连接被中间代理超时断开：

- **WebSocket 协议级**：每 20 秒发送一次 `PingMessage`（RFC 6455 opcode=9）
- **JSON 应用级**：服务端 read goroutine 处理 `{"type":"ping"}` 并回复 `{"type":"pong"}`

## 投递任务示例

```bash
curl -X POST http://localhost:8080/api/jobs \
  -H "Content-Type: application/json" \
  -d '{"queue":"default","job_type":"send_email","data":{"to":"user@example.com","subject":"Hello"}}'
```

详细文档请参阅 [../../doc/README.md](../../doc/README.md)。
