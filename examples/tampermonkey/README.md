# GoQueue 油猴脚本 Worker 示例

通过 Tampermonkey（油猴）在浏览器中运行 GoQueue WebSocket Worker，
可在任意网页后台处理任务，天然具备浏览器 API 访问能力（fetch、DOM、localStorage 等）。

## 安装

1. 安装 [Tampermonkey](https://www.tampermonkey.net/) 浏览器扩展
2. 点击 Tampermonkey 图标 → **创建新脚本**
3. 将 `goqueue-worker.user.js` 的内容粘贴进去，保存

或直接从本地文件安装：Tampermonkey → 实用工具 → 从文件导入 → 选择 `goqueue-worker.user.js`

## 配置

安装后，点击 Tampermonkey 图标 → **GoQueue Dashboard Worker** → **⚙️ 配置 GoQueue Worker**，
填写服务端地址、队列名、API Key，刷新页面生效。

也可直接修改脚本顶部的 `GM_getValue` 默认值：

```js
const CFG = {
  serverUrl:      GM_getValue('gq_server',   'ws://your-server:8080/ws/worker'),
  queue:          GM_getValue('gq_queue',    'default'),
  apiKey:         GM_getValue('gq_api_key',  ''),
  pingInterval:   GM_getValue('gq_ping',     20000),
  reconnectDelay: GM_getValue('gq_reconnect',3000),
  autoStart:      GM_getValue('gq_autostart',true),
  notify:         GM_getValue('gq_notify',   false),
};
```

## 菜单命令

| 命令 | 说明 |
|---|---|
| ⚙️ 配置 GoQueue Worker | 修改服务端地址、队列名、API Key |
| ▶️ 启动 Worker | 手动启动（autoStart=false 时使用） |
| ⏹ 停止 Worker | 断开连接，停止处理任务 |
| 📊 查看状态 | 显示连接状态和已处理任务数 |

## 内置 Handler

| job_type | 说明 | data 字段 |
|---|---|---|
| `fetch_url` | 抓取网页内容（利用浏览器 fetch，可绕过部分跨域限制） | `url` |
| `delay` | 延迟执行 | `ms`, `message` |
| `local_storage_set` | 写入 localStorage | `key`, `value` |
| `click_element` | 点击页面元素 | `selector`（CSS 选择器） |

## 注册自定义 Handler

在脚本的 `handlers` 对象中添加：

```js
const handlers = {
  my_job: async (data) => {
    // 可使用所有浏览器 API：fetch、DOM、GM_* 等
    const result = await fetch(data.api_url).then(r => r.json());
    return `Got: ${JSON.stringify(result)}`;
  },
};
```

## 心跳机制

- 连接成功后每 20 秒发送一次 `{"type":"ping"}`，防止连接被超时断开
- 连接断开后自动重连（默认 3 秒后）
- 页面标题前会显示状态 emoji：🟢 已连接 / 🔴 断开 / 🟡 处理中 / ⏹ 已停止

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
- 使用 `@grant GM_xmlhttpRequest` 可突破跨域限制（需在脚本头部声明）
- 关闭标签页或浏览器后 Worker 停止运行
- 建议在专用标签页（如 Dashboard 页面）中运行，避免影响正常浏览

详细文档：[../../doc/README.md](../../doc/README.md)
