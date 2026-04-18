# YouTube → GoQueue 油猴脚本示例

在 YouTube 首页、频道页、搜索页、播放页的每个视频卡片上注入「**加入队列**」按钮，  
点击后将视频标题和链接作为任务 data 投递到 GoQueue，由后端 Worker 异步处理。

## 效果预览

```
┌──────────────────────────────────────┐
│  [缩略图]  视频标题                   │
│            频道名 · 100万次观看        │
│            [＋ 加入队列]  ← 注入的按钮 │
└──────────────────────────────────────┘
```

- 首页 / 频道 / 搜索页：每个视频卡片下方出现「加入队列」按钮
- 播放页：在点赞/分享按钮旁出现「加入队列」按钮
- 投递成功后按钮变为绿色「✅ 已加入」，防止重复投递

## 快速开始

### 1. 启动 GoQueue 服务

```bash
./goapp          # 默认监听 :8080
```

### 2. 安装油猴脚本

1. 安装 [Tampermonkey](https://www.tampermonkey.net/) 浏览器扩展
2. 新建脚本，将 `youtube-queue.user.js` 的内容粘贴进去，保存
3. 打开 YouTube，视频卡片上即可看到「加入队列」按钮

### 3. 编写 Worker 处理视频任务

脚本投递的任务格式如下：

```json
{
  "queue":        "youtube",
  "job_type":     "process_youtube_video",
  "priority":     5,
  "max_attempts": 3,
  "tags":         ["youtube"],
  "data": {
    "title":       "视频标题",
    "url":         "https://www.youtube.com/watch?v=xxxxx",
    "queued_at":   "2026-04-18T14:00:00.000Z",
    "source_page": "https://www.youtube.com/"
  }
}
```

**JS Worker 示例：**

```javascript
// examples/js-worker/worker.js
handlers['process_youtube_video'] = async (data, tags) => {
  console.log(`处理视频: ${data.title}`);
  console.log(`链接: ${data.url}`);
  // TODO: 调用 yt-dlp 下载、存档、转码等业务逻辑
  return `已处理: ${data.title}`;
};
```

**Python Worker 示例：**

```python
# examples/python-worker/worker.py
@worker.handler('process_youtube_video')
async def handle_youtube_video(data, tags):
    print(f"处理视频: {data['title']}")
    print(f"链接: {data['url']}")
    # TODO: 调用 yt-dlp 下载、存档、转码等业务逻辑
    return f"已处理: {data['title']}"
```

**Go Worker 示例（内置 GoWorker）：**

```go
// examples/goworker/main.go
func init() {
    goqueue.Register("process_youtube_video", func(ctx context.Context, data json.RawMessage) (string, error) {
        var d struct {
            Title string `json:"title"`
            URL   string `json:"url"`
        }
        json.Unmarshal(data, &d)
        log.Printf("处理视频: %s  %s", d.Title, d.URL)
        // TODO: 业务逻辑
        return "已处理: " + d.Title, nil
    })
}
```

## 配置说明

脚本顶部配置区域支持通过 `GM_getValue` / `GM_setValue` 持久化：

| 配置项 | 默认值 | 说明 |
|---|---|---|
| `goqueue_host` | `http://localhost:8080` | GoQueue 服务地址，生产环境改为实际地址 |
| `goqueue_queue` | `youtube` | 投递到哪个队列 |
| `JOB_TYPE` | `process_youtube_video` | Worker 侧路由标识，直接修改脚本常量 |
| `JOB_PRIORITY` | `5` | 任务优先级（1-10） |
| `MAX_ATTEMPTS` | `3` | 失败后最多重试次数 |

**修改服务地址（在浏览器控制台执行）：**

```javascript
GM_setValue('goqueue_host', 'https://your-server.example.com');
GM_setValue('goqueue_queue', 'youtube');
```

## 与原始脚本的差异

| 项目 | 原始脚本 | 本示例 |
|---|---|---|
| 目标 API | `https://files.10w123.com/ops/youtube/api/youtube` | `http://localhost:8080/api/jobs`（GoQueue 标准接口） |
| 请求格式 | `{room_id, videos:[{title,link}]}` | GoQueue 标准 Job payload（`queue/job_type/data/tags`） |
| 按钮文字 | 「发送」 | 「加入队列」 |
| 按钮图标 | 发送箭头 | 队列加号图标 |
| 投递成功 | Toast 提示 | Toast 提示 + 按钮变绿「✅ 已加入」（防重复投递） |
| CSS 类名 | `notebooklm-btn` | `goqueue-btn` |
| 服务地址 | 硬编码 | `GM_getValue` 可持久化配置 |
