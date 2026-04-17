# GoQueue 油猴脚本 Worker 示例

通过 Tampermonkey（油猴）在浏览器中运行 GoQueue WebSocket Worker。  
脚本在**页面右上角**注入一个悬浮控制面板，可实时查看连接状态、任务日志，并支持一键启停和配置修改。

## 面板预览

```
┌─────────────────────────────┐
│ ⚡ GoQueue Worker      🟢 ▾ │  ← 标题栏（点击 ▾ 折叠）
├─────────────────────────────┤
│ 已连接 · default  ✓ 12  ✗ 1│  ← 状态栏（成功/失败计数）
├─────────────────────────────┤
│  ▶ 启动  │  ■ 停止  │ ⚙ 配置│  ← 控制按钮
├─────────────────────────────┤
│ 队列：default | 服务端：... │  ← 当前配置信息
├─────────────────────────────┤
│ 日志                   清空 │
│ 10:23:01 已连接，队列=default│
│ 10:23:05 Job #42 [send_email]│
│ 10:23:05 #42 ✓ Email sent   │
│ ...                         │
└─────────────────────────────┘
```

**状态指示灯颜色：**

| 颜色 | 含义 |
|---|---|
| 🔴 红色 | 未连接 / 已断开 |
| 🟡 黄色（闪烁） | 连接中 |
| 🟢 绿色 | 已连接，空闲 |
| 🔵 蓝色（闪烁） | 正在处理任务 |

## 安装

1. 安装 [Tampermonkey](https://www.tampermonkey.net/) 浏览器扩展
2. 点击 Tampermonkey 图标 → **创建新脚本**
3. 将 `goqueue-worker.user.js` 的内容粘贴进去，保存

或直接从本地文件安装：Tampermonkey → 实用工具 → 从文件导入 → 选择 `goqueue-worker.user.js`

## 使用

1. 打开任意网页，右上角会出现 GoQueue Worker 面板
2. 点击 **⚙ 配置** 填写服务端地址、队列名、API Key，保存
3. 点击 **▶ 启动** 连接服务端，开始处理任务
4. 点击 **■ 停止** 断开连接
5. 点击标题栏右侧 **▾** 可折叠面板

## 配置项

| 配置项 | 默认值 | 说明 |
|---|---|---|
| 服务端地址 | `ws://localhost:8080/ws/worker` | GoQueue 服务端 WebSocket 地址 |
| 队列名 | `default` | 监听的队列名 |
| API Key | _(空)_ | 对应服务端 `API_KEY` 环境变量 |

配置通过 `GM_getValue/GM_setValue` 持久化存储，刷新页面后保留。

若需默认自动启动，将脚本中 `gq_autostart` 的默认值改为 `true`：

```js
get autoStart() { return GM_getValue('gq_autostart', true); },
```

## 内置 Handler

| job_type | 说明 | data 字段 |
|---|---|---|
| `fetch_url` | 抓取网页内容（利用浏览器 fetch） | `url` |
| `delay` | 延迟执行 | `ms`, `message` |
| `local_storage_set` | 写入 localStorage | `key`, `value` |
| `click_element` | 点击页面元素 | `selector`（CSS 选择器） |

## 注册自定义 Handler

在脚本的 `handlers` 对象中添加：

```js
const handlers = {
  my_job: async (data) => {
    // 可使用所有浏览器 API：fetch、DOM 操作等
    const result = await fetch(data.api_url).then(r => r.json());
    return `Got: ${JSON.stringify(result)}`;
  },
};
```

## 心跳机制

- 连接成功后每 20 秒发送一次 `{"type":"ping"}`，防止连接被超时断开
- 服务端回复 `{"type":"pong"}`，静默处理
- 连接断开后自动重连（默认 3 秒后）

## 投递任务示例

```bash
# 让浏览器抓取一个 URL
curl -X POST http://localhost:8080/api/jobs \
  -H "Content-Type: application/json" \
  -d '{"queue":"default","job_type":"fetch_url","data":{"url":"https://httpbin.org/get"}}'

# 点击页面上的某个按钮
curl -X POST http://localhost:8080/api/jobs \
  -H "Content-Type: application/json" \
  -d '{"queue":"default","job_type":"click_element","data":{"selector":"#submit-btn"}}'
```

## 注意事项

- 脚本运行在浏览器页面上下文中，受同源策略限制（fetch 跨域需目标服务器允许）
- 关闭标签页或浏览器后 Worker 停止运行
- 建议在专用标签页（如 Dashboard 页面）中运行

详细文档：[../../doc/README.md](../../doc/README.md)

## 新特性（v4）

### 任务标签（Tags）

投递任务时可携带标签，面板日志中会显示 tags 信息，handler 第二参数接收 tags 数组：

```bash
# 投递带标签的任务
curl -X POST http://localhost:8080/api/jobs \
  -H "Content-Type: application/json" \
  -d '{"queue":"default","job_type":"tag_task","data":{"message":"hello"},"tags":["urgent","dry-run"]}'

# 按标签过滤任务列表
curl http://localhost:8080/api/jobs?tag=urgent

# 获取所有已使用的标签
curl http://localhost:8080/api/tags
```

内置 `tag_task` handler 演示了如何根据 tags 做不同处理（`dry-run` 跳过、`urgent` 优先等）。

### Batch catch/finally 回调

```bash
curl -X POST http://localhost:8080/api/batches \
  -H "Content-Type: application/json" \
  -d '{
    "name": "my-batch",
    "jobs": [{"queue":"default","job_type":"fetch_url","data":{"url":"https://example.com"}}],
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
  -d '{"name":"hourly","every":"1h","queue":"default","job_type":"fetch_url","data":{"url":"https://example.com"}}'

# 列出 / 更新 / 删除
curl http://localhost:8080/api/crons
curl -X PUT    http://localhost:8080/api/crons/1 -H "Content-Type: application/json" -d '{"name":"hourly","every":"2h","queue":"default","job_type":"fetch_url","data":{}}'
curl -X DELETE http://localhost:8080/api/crons/1
```

## 内置 Handler 列表

| job_type | 说明 |
|---|---|
| `fetch_url` | 抓取网页内容 |
| `delay` | 延迟执行 |
| `local_storage_set` | 写入 localStorage |
| `click_element` | 点击页面元素 |
| `tag_task` | 按 tags 路由处理示例（v4） |
| `on_success` / `on_failure` / `on_finally` | Batch 回调示例（v4） |
