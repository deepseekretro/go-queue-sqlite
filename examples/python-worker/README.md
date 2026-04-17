# GoQueue Python Worker

适用于 Python 3.8+ 环境的 GoQueue Worker 示例。

## 快速开始

```bash
pip install -r requirements.txt
python worker.py
```

## 环境变量

| 变量 | 默认值 | 说明 |
|---|---|---|
| `GOQUEUE_SERVER` | `ws://localhost:8080/ws/worker` | WebSocket 服务端地址 |
| `GOQUEUE_QUEUE` | `default` | 监听的队列名 |
| `GOQUEUE_API_KEY` | _(空)_ | API Key |
| `GOQUEUE_CONCURRENCY` | `4` | 最大并发任务数 |

## 添加 Handler

```python
# handler 签名：(data: dict, tags: list[str]) -> str
# data: payload 字典，tags: 任务标签列表（v4 新增）
def handle_my_job(data: dict, tags: list) -> str:
    if "dry-run" in tags:
        return "dry-run: skipped"
    # 正常处理...
    return "done"

worker.register("my_job", handle_my_job)
```

## 新特性（v4）

### 任务标签（Tags）

```bash
# 投递带标签的任务
curl -X POST http://localhost:8080/api/jobs \
  -H "Content-Type: application/json" \
  -d '{"queue":"default","job_type":"tag_task","data":{"message":"hello"},"tags":["urgent","notify"]}'

# 按标签过滤
curl http://localhost:8080/api/jobs?tag=urgent

# 获取所有标签
curl http://localhost:8080/api/tags
```

### Batch catch/finally 回调

```bash
curl -X POST http://localhost:8080/api/batches \
  -H "Content-Type: application/json" \
  -d '{
    "name": "my-batch",
    "jobs": [{"queue":"default","job_type":"send_email","data":{"to":"a@b.com"}}],
    "then_job":    {"queue":"default","job_type":"on_success","data":{}},
    "catch_job":   {"queue":"default","job_type":"on_failure","data":{}},
    "finally_job": {"queue":"default","job_type":"on_finally","data":{}}
  }'
```

| 回调 | 触发时机 |
|---|---|
| `then_job` | 所有任务全部成功 |
| `catch_job` | 有任意任务失败 |
| `finally_job` | 无论成功或失败，批次完成后必触发 |

### Queue Pause/Resume

```bash
curl -X POST http://localhost:8080/api/queues/default/pause
curl -X POST http://localhost:8080/api/queues/default/resume
curl http://localhost:8080/api/queues
```

### Cron 调度器

```bash
# 支持 s/m/h/d/w 单位
curl -X POST http://localhost:8080/api/crons \
  -H "Content-Type: application/json" \
  -d '{"name":"hourly","every":"1h","queue":"default","job_type":"generate_report","data":{}}'
```

## 内置 Handler 列表

| job_type | 说明 |
|---|---|
| `send_email` | 发送邮件示例 |
| `generate_report` | 生成报告示例 |
| `resize_image` | 图片缩放示例 |
| `data_sync` | 数据同步示例 |
| `tag_task` | 按 tags 路由处理示例（v4） |
| `on_success` / `on_failure` / `on_finally` | Batch 回调示例（v4） |

### 任务超时（timeout_sec）

投递任务时可指定 `timeout_sec`，服务端会将该值下发给 Worker，Worker 自动用 `concurrent.futures` 实现超时：

```bash
# 投递一个最长允许 10 分钟的任务
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

Worker 侧（Python）：`_handle_job` 会自动从 `msg["timeout_sec"]` 读取超时，无需额外代码。
