// ==UserScript==
// @name         GoQueue Dashboard Worker
// @namespace    https://github.com/deepseekretro/go-queue-sqlite
// @version      1.0.0
// @description  在任意网页中以油猴脚本身份运行 GoQueue WebSocket Worker，处理后台任务
// @author       GoQueue
// @match        *://*/*
// @grant        GM_getValue
// @grant        GM_setValue
// @grant        GM_registerMenuCommand
// @grant        GM_notification
// @run-at       document-idle
// ==/UserScript==

(function () {
  'use strict';

  // ─── 配置（可通过油猴菜单修改，持久化存储）────────────────────────────────
  const CFG = {
    serverUrl:      GM_getValue('gq_server',   'ws://localhost:8080/ws/worker'),
    queue:          GM_getValue('gq_queue',    'default'),
    apiKey:         GM_getValue('gq_api_key',  ''),
    pingInterval:   GM_getValue('gq_ping',     20000),   // 心跳间隔（ms）
    reconnectDelay: GM_getValue('gq_reconnect',3000),    // 断线重连间隔（ms）
    autoStart:      GM_getValue('gq_autostart',true),    // 页面加载后自动启动
    notify:         GM_getValue('gq_notify',   false),   // 任务完成后桌面通知
  };

  // ─── 状态 ─────────────────────────────────────────────────────────────────
  let ws        = null;
  let pingTimer = null;
  let stopped   = !CFG.autoStart;
  let jobCount  = 0;

  // ─── Job Handlers ─────────────────────────────────────────────────────────
  // 在此注册你的任务处理函数：job_type → async function(data) → string
  const handlers = {
    /**
     * 示例：抓取网页内容
     * 投递：{"queue":"default","job_type":"fetch_url","data":{"url":"https://example.com"}}
     */
    fetch_url: async (data) => {
      const resp = await fetch(data.url);
      const text = await resp.text();
      return `Fetched ${data.url} (${text.length} bytes, status=${resp.status})`;
    },

    /**
     * 示例：延迟执行（模拟耗时任务）
     * 投递：{"queue":"default","job_type":"delay","data":{"ms":1000,"message":"hello"}}
     */
    delay: async (data) => {
      await sleep(data.ms || 1000);
      return `Delayed ${data.ms}ms: ${data.message || 'done'}`;
    },

    /**
     * 示例：localStorage 操作
     * 投递：{"queue":"default","job_type":"local_storage_set","data":{"key":"foo","value":"bar"}}
     */
    local_storage_set: async (data) => {
      localStorage.setItem(data.key, data.value);
      return `localStorage.setItem(${data.key}, ${data.value})`;
    },

    /**
     * 示例：点击页面元素
     * 投递：{"queue":"default","job_type":"click_element","data":{"selector":"#submit-btn"}}
     */
    click_element: async (data) => {
      const el = document.querySelector(data.selector);
      if (!el) throw new Error(`Element not found: ${data.selector}`);
      el.click();
      return `Clicked: ${data.selector}`;
    },
  };

  // ─── Worker 核心 ──────────────────────────────────────────────────────────

  function connect() {
    if (stopped) return;

    const url = `${CFG.serverUrl}?queue=${CFG.queue}`;
    log(`Connecting to ${url}`);

    try {
      ws = new WebSocket(url);
    } catch (e) {
      log(`WebSocket init failed: ${e.message}`, 'error');
      scheduleReconnect();
      return;
    }

    ws.onopen = () => {
      log(`Connected ✓  queue=${CFG.queue}`);
      updateBadge('🟢');

      // 心跳：每 20s 发一次 JSON ping，防止连接被中间代理超时断开
      pingTimer = setInterval(() => {
        if (ws && ws.readyState === WebSocket.OPEN) {
          ws.send(JSON.stringify({ type: 'ping' }));
        }
      }, CFG.pingInterval);
    };

    ws.onmessage = async (event) => {
      let msg;
      try { msg = JSON.parse(event.data); } catch { return; }

      switch (msg.type) {
        case 'connected':
          log(`Server: ${msg.message}`);
          break;

        case 'pong':
          // 心跳响应，静默处理
          break;

        case 'ack':
          log(`ACK: ${msg.message}`);
          break;

        case 'job':
          await handleJob(msg);
          break;

        default:
          log(`Unknown type: ${msg.type}`);
      }
    };

    ws.onclose = () => {
      log('Disconnected');
      updateBadge('🔴');
      clearPing();
      if (!stopped) scheduleReconnect();
    };

    ws.onerror = (e) => {
      log(`WebSocket error: ${e.message || 'unknown'}`, 'error');
    };
  }

  async function handleJob(msg) {
    const jobId   = msg.job_id;
    const jobType = msg.job_type;

    let payload, data;
    try {
      payload = JSON.parse(msg.payload);
      data    = payload.data || {};
    } catch (e) {
      return sendResult(jobId, false, '', `Invalid payload: ${e.message}`);
    }

    log(`Job #${jobId} type=${jobType}`);
    jobCount++;
    updateBadge('🟡');

    const handler = handlers[jobType];
    if (!handler) {
      updateBadge('🟢');
      return sendResult(jobId, false, '', `No handler for job_type: "${jobType}"`);
    }

    try {
      const result = await handler(data);
      log(`Job #${jobId} ✓ ${result}`);
      sendResult(jobId, true, result, '');
      if (CFG.notify) {
        GM_notification({ title: 'GoQueue', text: `Job #${jobId} done: ${result}`, timeout: 3000 });
      }
    } catch (err) {
      log(`Job #${jobId} ✗ ${err.message}`, 'error');
      sendResult(jobId, false, '', err.message);
    } finally {
      updateBadge('🟢');
    }
  }

  function sendResult(jobId, success, logStr, error) {
    if (!ws || ws.readyState !== WebSocket.OPEN) return;
    const msg = { type: 'result', job_id: jobId, success };
    if (success) msg.log   = logStr;
    else         msg.error = error;
    ws.send(JSON.stringify(msg));
  }

  function clearPing() {
    if (pingTimer) { clearInterval(pingTimer); pingTimer = null; }
  }

  function scheduleReconnect() {
    log(`Reconnecting in ${CFG.reconnectDelay}ms...`);
    setTimeout(connect, CFG.reconnectDelay);
  }

  function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }

  // ─── 日志 & UI ────────────────────────────────────────────────────────────

  function log(msg, level = 'info') {
    const prefix = '[GoQueue Worker]';
    if (level === 'error') console.error(prefix, msg);
    else                   console.log(prefix, msg);
  }

  // 在页面标题前显示状态 emoji
  const origTitle = document.title;
  function updateBadge(emoji) {
    document.title = `${emoji} ${origTitle}`;
  }

  // ─── 油猴菜单命令 ─────────────────────────────────────────────────────────

  GM_registerMenuCommand('⚙️ 配置 GoQueue Worker', () => {
    const server = prompt('服务端 WebSocket 地址：', CFG.serverUrl);
    if (server === null) return;
    const queue  = prompt('队列名：', CFG.queue);
    if (queue  === null) return;
    const apiKey = prompt('API Key（留空表示不鉴权）：', CFG.apiKey);
    if (apiKey === null) return;

    GM_setValue('gq_server',  server);
    GM_setValue('gq_queue',   queue);
    GM_setValue('gq_api_key', apiKey);

    CFG.serverUrl = server;
    CFG.queue     = queue;
    CFG.apiKey    = apiKey;

    alert('配置已保存，刷新页面生效。');
  });

  GM_registerMenuCommand('▶️ 启动 Worker', () => {
    if (!stopped) { alert('Worker 已在运行中。'); return; }
    stopped = false;
    connect();
  });

  GM_registerMenuCommand('⏹ 停止 Worker', () => {
    stopped = true;
    clearPing();
    if (ws) { ws.close(); ws = null; }
    updateBadge('⏹');
    log('Worker stopped by user.');
  });

  GM_registerMenuCommand('📊 查看状态', () => {
    const state = ws ? ['CONNECTING','OPEN','CLOSING','CLOSED'][ws.readyState] : 'NOT_CONNECTED';
    alert(
      `GoQueue Worker 状态\n` +
      `─────────────────────\n` +
      `服务端：${CFG.serverUrl}\n` +
      `队列：${CFG.queue}\n` +
      `连接状态：${state}\n` +
      `已处理任务：${jobCount}`
    );
  });

  // ─── 自动启动 ─────────────────────────────────────────────────────────────
  if (CFG.autoStart) {
    log(`Auto-starting worker (queue=${CFG.queue})...`);
    connect();
  } else {
    log('Auto-start disabled. Use Tampermonkey menu → ▶️ 启动 Worker');
    updateBadge('⏹');
  }

})();
