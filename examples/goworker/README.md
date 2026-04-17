# GoWorker 示例程序

这是 GoQueue 的官方 Go Worker 示例，展示如何用 Go 编写一个完整的 WebSocket Worker。

## 快速开始

```bash
# 编译
go build -o goworker .

# 启动（确保 goapp 已在 localhost:8080 运行）
./goworker -server ws://localhost:8080/ws/worker -queue default -concurrency 4
```

## 启动参数

| 参数 | 默认值 | 说明 |
|---|---|---|
| `-server` | `ws://localhost:8080/ws/worker` | 服务端 WebSocket 地址 |
| `-queue` | `default` | 监听的队列名 |
| `-api-key` | _(空)_ | API Key（Bearer Token） |
| `-concurrency` | `4` | 单连接最大并发任务数 |
| `-connections` | `1` | 并行 WebSocket 连接数 |
| `-reconnect` | `3s` | 断线重连间隔 |

## 注册 Handler

```go
// 函数签名：data 是 payload，tags 是任务标签（v4 新增）
Register("my_job", func(ctx context.Context, data map[string]interface{}, tags []string) (string, error) {
    // 根据 tags 做不同处理
    for _, tag := range tags {
        if tag == "dry-run" {
            return "dry-run: skipped", nil
        }
    }
    // 正常处理...
    return "done", nil
})
```

## 新特性（v4）

### 任务标签（Tags）

投递任务时可携带标签，Worker 收到任务后可按标签路由或过滤：

```bash
# 投递带标签的任务
curl -X POST http://localhost:8080/api/jobs \
  -H "Content-Type: application/json" \
  -d '{"queue":"default","job_type":"tag_task","data":{"message":"hello"},"tags":["urgent","notify"]}'

# 按标签过滤任务列表
curl http://localhost:8080/api/jobs?tag=urgent

# 获取所有已使用的标签
curl http://localhost:8080/api/tags
```

### Batch catch/finally 回调

批次任务支持三路回调：

```bash
curl -X POST http://localhost:8080/api/batches \
  -H "Content-Type: application/json" \
  -d '{
    "name": "my-batch",
    "jobs": [
      {"queue":"default","job_type":"task_a","data":{}},
      {"queue":"default","job_type":"task_b","data":{}}
    ],
    "then_job":    {"queue":"notify","job_type":"on_success","data":{}},
    "catch_job":   {"queue":"notify","job_type":"on_failure","data":{}},
    "finally_job": {"queue":"notify","job_type":"on_finally","data":{}}
  }'
```

| 回调 | 触发时机 |
|---|---|
| `then_job` | 所有任务全部成功 |
| `catch_job` | 有任意任务失败 |
| `finally_job` | 无论成功或失败，批次完成后必触发 |

### Queue Pause/Resume

```bash
# 暂停队列（暂停期间任务不派发给 Worker）
curl -X POST http://localhost:8080/api/queues/default/pause

# 恢复队列
curl -X POST http://localhost:8080/api/queues/default/resume

# 查看所有队列状态
curl http://localhost:8080/api/queues
```

### Cron 调度器

```bash
# 创建定时任务（支持 s/m/h/d/w 单位）
curl -X POST http://localhost:8080/api/crons \
  -H "Content-Type: application/json" \
  -d '{"name":"daily-report","every":"1h","queue":"default","job_type":"generate_report","data":{"name":"daily"}}'

# 列出所有 cron
curl http://localhost:8080/api/crons

# 更新 cron
curl -X PUT http://localhost:8080/api/crons/1 \
  -H "Content-Type: application/json" \
  -d '{"name":"daily-report","every":"2h","queue":"default","job_type":"generate_report","data":{}}'

# 删除 cron
curl -X DELETE http://localhost:8080/api/crons/1
```

### 任务超时（timeout_sec）

投递任务时可指定 `timeout_sec`，服务端会将该值下发给 Worker，Worker 自动应用超时控制：

```bash
# 投递一个最长允许 10 分钟的任务（适合 AI API 调用等长耗时场景）
curl -X POST http://localhost:8080/api/jobs \
  -H "Content-Type: application/json" \
  -d '{"queue":"default","job_type":"ai_task","data":{"prompt":"..."},"timeout_sec":600}'
```

超时优先级（从高到低）：

| 优先级 | 方式 | 说明 |
|---|---|---|
| 1（最高） | per-job `timeout_sec` 字段 | 投递任务时指定，只影响该任务 |
| 2 | `WS_JOB_TIMEOUT_SEC` 环境变量 | 启动服务时设置，影响所有任务，默认 300s |
| 3（最低） | 代码默认值 | 300s（5 分钟） |

```bash
# 服务端全局超时（AI API 场景，设置为 1 小时）
WS_JOB_TIMEOUT_SEC=3600 STALE_JOB_TIMEOUT_SEC=3600 ./goapp
```

Worker 侧（Go）：`handleJob` 会自动从 `msg.TimeoutSec` 读取超时，无需额外代码。

## 内置 Handler 列表

| job_type | 说明 |
|---|---|
| `send_email` | 发送邮件示例 |
| `generate_report` | 生成报告示例 |
| `resize_image` | 图片缩放示例 |
| `data_sync` | 数据同步示例 |
| `tag_task` | 按 tags 路由处理示例（v4） |
| `batch_callback` / `on_success` / `on_failure` / `on_finally` | Batch 回调示例（v4） |
