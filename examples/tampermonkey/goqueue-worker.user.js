// ==UserScript==
// @name         GoQueue Worker Panel
// @namespace    https://github.com/deepseekretro/go-queue-sqlite
// @version      4.0.0
// @description  在页面右上角显示 GoQueue Worker 控制面板，实时展示连接状态与任务日志（v4: tags/batch catch-finally/cron/queue pause）
// @author       GoQueue
// @match        *://*/*
// @grant        GM_getValue
// @grant        GM_setValue
// @grant        GM_addStyle
// @run-at       document-idle
// ==/UserScript==

(function () {
  'use strict';

  // ─── 配置读写（带明确默认值，避免 GM_getValue 沙箱返回空值）────────────────
  const DEFAULT_SERVER = 'wss://24f7f8fe-1114-4510-9f39-bdbd913f8772.deepnoteproject.com/ws/worker';
  const DEFAULT_QUEUE  = 'default';

  function getCfg() {
    return {
      serverUrl:      GM_getValue('gq_server',    DEFAULT_SERVER) || DEFAULT_SERVER,
      queue:          GM_getValue('gq_queue',     DEFAULT_QUEUE)  || DEFAULT_QUEUE,
      apiKey:         GM_getValue('gq_api_key',   '') || '',
      pingInterval:   GM_getValue('gq_ping',      20000) || 20000,
      reconnectDelay: GM_getValue('gq_reconnect', 3000)  || 3000,
      autoStart:      GM_getValue('gq_autostart', false),
    };
  }

  // ─── 状态 ─────────────────────────────────────────────────────────────────
  let ws          = null;
  let pingTimer   = null;
  let stopped     = true;
  let jobCount    = 0;
  let failCount   = 0;
  let reconnTimer = null;

  // ─── 样式注入 ──────────────────────────────────────────────────────────────
  GM_addStyle(`
    #gq-panel {
      position: fixed;
      top: 16px;
      right: 16px;
      z-index: 2147483647;
      width: 320px;
      font-family: 'Segoe UI', system-ui, -apple-system, sans-serif;
      font-size: 13px;
      color: #e2e8f0;
      box-shadow: 0 8px 32px rgba(0,0,0,.55);
      border-radius: 12px;
      overflow: hidden;
      border: 1px solid #334155;
      user-select: none;
    }
    #gq-panel.gq-collapsed #gq-body { display: none; }
    #gq-header {
      display: flex;
      align-items: center;
      gap: 8px;
      padding: 10px 12px;
      background: #1e293b;
      cursor: pointer;
      border-bottom: 1px solid #334155;
    }
    #gq-header:hover { background: #263348; }
    #gq-logo { font-size: 16px; flex-shrink: 0; }
    #gq-title { font-weight: 700; color: #60a5fa; flex: 1; letter-spacing: .02em; }
    #gq-status-dot {
      width: 9px; height: 9px;
      border-radius: 50%;
      background: #475569;
      flex-shrink: 0;
      transition: background .3s;
      box-shadow: 0 0 0 0 transparent;
    }
    #gq-status-dot.connected    { background: #22c55e; box-shadow: 0 0 6px #22c55e88; }
    #gq-status-dot.connecting   { background: #f59e0b; animation: gq-pulse .8s infinite; }
    #gq-status-dot.working      { background: #3b82f6; box-shadow: 0 0 6px #3b82f688; animation: gq-pulse .6s infinite; }
    #gq-status-dot.disconnected { background: #ef4444; }
    @keyframes gq-pulse {
      0%,100% { opacity: 1; } 50% { opacity: .4; }
    }
    #gq-collapse-btn {
      background: none; border: none; color: #64748b;
      cursor: pointer; font-size: 14px; padding: 0 2px;
      line-height: 1; flex-shrink: 0;
    }
    #gq-collapse-btn:hover { color: #94a3b8; }
    #gq-body { background: #0f172a; }

    /* 状态栏 */
    #gq-statusbar {
      display: flex;
      align-items: center;
      gap: 6px;
      padding: 7px 12px;
      background: #1e293b;
      border-bottom: 1px solid #1e293b;
      font-size: 11px;
      color: #94a3b8;
    }
    #gq-status-text { flex: 1; }
    .gq-badge {
      display: inline-flex; align-items: center; gap: 3px;
      padding: 1px 7px; border-radius: 99px; font-size: 11px; font-weight: 600;
    }
    .gq-badge-green  { background: #14532d; color: #4ade80; }
    .gq-badge-red    { background: #450a0a; color: #f87171; }

    /* 控制按钮 */
    #gq-controls {
      display: flex;
      gap: 6px;
      padding: 10px 12px;
      border-bottom: 1px solid #1e293b;
    }
    .gq-btn {
      flex: 1;
      padding: 6px 0;
      border: none;
      border-radius: 7px;
      font-size: 12px;
      font-weight: 600;
      cursor: pointer;
      transition: opacity .15s, transform .1s;
    }
    .gq-btn:active { transform: scale(.96); }
    .gq-btn:disabled { opacity: .4; cursor: not-allowed; }
    .gq-btn-start  { background: #16a34a; color: #fff; }
    .gq-btn-start:hover:not(:disabled)  { background: #15803d; }
    .gq-btn-stop   { background: #dc2626; color: #fff; }
    .gq-btn-stop:hover:not(:disabled)   { background: #b91c1c; }
    .gq-btn-config { background: #334155; color: #cbd5e1; }
    .gq-btn-config:hover:not(:disabled) { background: #475569; }

    /* 信息行 */
    #gq-info {
      padding: 6px 12px 8px;
      font-size: 11px;
      color: #475569;
      border-bottom: 1px solid #1e293b;
      line-height: 1.6;
      word-break: break-all;
    }
    #gq-info span { color: #64748b; }

    /* 日志区 */
    #gq-log-header {
      display: flex;
      align-items: center;
      padding: 6px 12px 4px;
      font-size: 11px;
      color: #475569;
      font-weight: 600;
      letter-spacing: .04em;
      text-transform: uppercase;
    }
    #gq-log-clear {
      margin-left: auto;
      background: none; border: none;
      color: #475569; font-size: 11px; cursor: pointer;
      padding: 0;
    }
    #gq-log-clear:hover { color: #94a3b8; }
    #gq-log {
      height: 160px;
      overflow-y: auto;
      padding: 0 12px 10px;
      display: flex;
      flex-direction: column;
      gap: 2px;
    }
    #gq-log::-webkit-scrollbar { width: 4px; }
    #gq-log::-webkit-scrollbar-track { background: transparent; }
    #gq-log::-webkit-scrollbar-thumb { background: #334155; border-radius: 2px; }
    .gq-log-line {
      display: flex;
      gap: 6px;
      font-size: 11px;
      line-height: 1.5;
      font-family: 'Cascadia Code', 'Fira Code', 'Consolas', monospace;
    }
    .gq-log-time { color: #334155; flex-shrink: 0; }
    .gq-log-msg  { color: #94a3b8; word-break: break-all; }
    .gq-log-msg.info    { color: #94a3b8; }
    .gq-log-msg.success { color: #4ade80; }
    .gq-log-msg.error   { color: #f87171; }
    .gq-log-msg.warn    { color: #fbbf24; }
    .gq-log-msg.job     { color: #60a5fa; }

    /* 配置弹层 */
    #gq-config-modal {
      display: none;
      position: absolute;
      inset: 0;
      background: #0f172a;
      z-index: 10;
      padding: 14px 14px 10px;
      flex-direction: column;
      gap: 8px;
    }
    #gq-config-modal.open { display: flex; }
    #gq-config-modal label {
      font-size: 11px; color: #64748b; font-weight: 600;
      letter-spacing: .04em; text-transform: uppercase;
      margin-bottom: 2px; display: block;
    }
    #gq-config-modal input {
      width: 100%;
      padding: 6px 9px;
      background: #1e293b;
      border: 1px solid #334155;
      border-radius: 6px;
      color: #e2e8f0;
      font-size: 12px;
      outline: none;
      box-sizing: border-box;
    }
    #gq-config-modal input:focus { border-color: #3b82f6; }
    #gq-config-actions {
      display: flex; gap: 6px; margin-top: 4px;
    }
  `);

  // ─── 面板 HTML（不在模板字符串里调用 GM_getValue，避免沙箱时序问题）────────
  const panel = document.createElement('div');
  panel.id = 'gq-panel';
  panel.innerHTML = `
    <div id="gq-header">
      <span id="gq-logo">⚡</span>
      <span id="gq-title">GoQueue Worker</span>
      <span id="gq-status-dot" title="disconnected"></span>
      <button id="gq-collapse-btn" title="折叠/展开">▾</button>
    </div>
    <div id="gq-body">
      <div id="gq-statusbar">
        <span id="gq-status-text">未连接</span>
        <span id="gq-done-badge"  class="gq-badge gq-badge-green">✓ 0</span>
        <span id="gq-fail-badge"  class="gq-badge gq-badge-red">✗ 0</span>
      </div>
      <div id="gq-controls">
        <button class="gq-btn gq-btn-start"  id="gq-btn-start">▶ 启动</button>
        <button class="gq-btn gq-btn-stop"   id="gq-btn-stop"  disabled>■ 停止</button>
        <button class="gq-btn gq-btn-config" id="gq-btn-config">⚙ 配置</button>
      </div>
      <div id="gq-info">
        队列：<span id="gq-info-queue">-</span> &nbsp;|&nbsp;
        服务端：<span id="gq-info-server">-</span>
      </div>
      <div id="gq-log-header">
        日志
        <button id="gq-log-clear">清空</button>
      </div>
      <div id="gq-log"></div>

      <!-- 配置弹层 -->
      <div id="gq-config-modal">
        <div>
          <label>服务端地址</label>
          <input id="cfg-server" type="text" placeholder="ws://localhost:8080/ws/worker">
        </div>
        <div>
          <label>队列名</label>
          <input id="cfg-queue" type="text" placeholder="default">
        </div>
        <div>
          <label>API Key（留空表示不鉴权）</label>
          <input id="cfg-apikey" type="text" placeholder="">
        </div>
        <div id="gq-config-actions">
          <button class="gq-btn gq-btn-start" id="cfg-save" style="flex:2">保存</button>
          <button class="gq-btn gq-btn-config" id="cfg-cancel" style="flex:1">取消</button>
        </div>
      </div>
    </div>
  `;
  document.body.appendChild(panel);

  // ─── DOM 引用 ──────────────────────────────────────────────────────────────
  const $dot        = panel.querySelector('#gq-status-dot');
  const $statusText = panel.querySelector('#gq-status-text');
  const $doneBadge  = panel.querySelector('#gq-done-badge');
  const $failBadge  = panel.querySelector('#gq-fail-badge');
  const $log        = panel.querySelector('#gq-log');
  const $btnStart   = panel.querySelector('#gq-btn-start');
  const $btnStop    = panel.querySelector('#gq-btn-stop');
  const $btnConfig  = panel.querySelector('#gq-btn-config');
  const $collapseBtn= panel.querySelector('#gq-collapse-btn');
  const $modal      = panel.querySelector('#gq-config-modal');
  const $cfgServer  = panel.querySelector('#cfg-server');
  const $cfgQueue   = panel.querySelector('#cfg-queue');
  const $cfgApiKey  = panel.querySelector('#cfg-apikey');
  const $infoQueue  = panel.querySelector('#gq-info-queue');
  const $infoServer = panel.querySelector('#gq-info-server');

  // ─── 初始化：读取配置后填充 info 行（避免在 innerHTML 模板里调用 GM_getValue）
  function refreshInfoBar() {
    const cfg = getCfg();
    $infoQueue.textContent  = cfg.queue;
    $infoServer.textContent = shortUrl(cfg.serverUrl);
    $infoServer.title       = cfg.serverUrl;
  }
  refreshInfoBar();

  // ─── 折叠 ─────────────────────────────────────────────────────────────────
  $collapseBtn.addEventListener('click', (e) => {
    e.stopPropagation();
    panel.classList.toggle('gq-collapsed');
    $collapseBtn.textContent = panel.classList.contains('gq-collapsed') ? '▸' : '▾';
  });

  // ─── 配置弹层 ─────────────────────────────────────────────────────────────
  $btnConfig.addEventListener('click', () => {
    const cfg = getCfg();
    $cfgServer.value = cfg.serverUrl;
    $cfgQueue.value  = cfg.queue;
    $cfgApiKey.value = cfg.apiKey;
    $modal.classList.add('open');
  });
  panel.querySelector('#cfg-cancel').addEventListener('click', () => {
    $modal.classList.remove('open');
  });
  panel.querySelector('#cfg-save').addEventListener('click', () => {
    const server = $cfgServer.value.trim();
    const queue  = $cfgQueue.value.trim() || DEFAULT_QUEUE;
    const apiKey = $cfgApiKey.value.trim();

    // 校验：必须是合法的 ws:// 或 wss:// 地址
    if (!server.startsWith('ws://') && !server.startsWith('wss://')) {
      $cfgServer.style.borderColor = '#ef4444';
      $cfgServer.focus();
      addLog('地址必须以 ws:// 或 wss:// 开头', 'error');
      return;
    }
    $cfgServer.style.borderColor = '';

    GM_setValue('gq_server',  server);
    GM_setValue('gq_queue',   queue);
    GM_setValue('gq_api_key', apiKey);

    refreshInfoBar();
    $modal.classList.remove('open');
    addLog(`配置已保存 → ${server}  queue=${queue}`, 'warn');
  });

  // ─── 启动 / 停止按钮 ──────────────────────────────────────────────────────
  $btnStart.addEventListener('click', () => {
    if (!stopped) return;
    stopped = false;
    $btnStart.disabled = true;
    $btnStop.disabled  = false;
    connect();
  });
  $btnStop.addEventListener('click', () => {
    stopped = true;
    clearPing();
    clearReconn();
    if (ws) { ws.close(); ws = null; }
    $btnStart.disabled = false;
    $btnStop.disabled  = true;
    setDot('disconnected', '已停止');
    addLog('Worker 已停止', 'warn');
  });

  // ─── 日志清空 ─────────────────────────────────────────────────────────────
  panel.querySelector('#gq-log-clear').addEventListener('click', () => {
    $log.innerHTML = '';
  });

  // ─── 日志工具 ─────────────────────────────────────────────────────────────
  function addLog(msg, level = 'info') {
    const time = new Date().toTimeString().slice(0, 8);
    const line = document.createElement('div');
    line.className = 'gq-log-line';
    line.innerHTML = `<span class="gq-log-time">${time}</span><span class="gq-log-msg ${level}">${escHtml(msg)}</span>`;
    $log.appendChild(line);
    while ($log.children.length > 200) $log.removeChild($log.firstChild);
    $log.scrollTop = $log.scrollHeight;
  }

  function setDot(state, text) {
    $dot.className = '';
    $dot.classList.add(state);
    $dot.title = text;
    $statusText.textContent = text;
  }

  function updateBadges() {
    $doneBadge.textContent = `✓ ${jobCount}`;
    $failBadge.textContent = `✗ ${failCount}`;
  }

  // ─── Job Handlers ─────────────────────────────────────────────────────────
  const handlers = {
    /**
     * 抓取网页内容
     * 投递：{"queue":"default","job_type":"fetch_url","data":{"url":"https://example.com"}}
     */
    fetch_url: async (data) => {
      const resp = await fetch(data.url);
      const text = await resp.text();
      return `Fetched ${data.url} (${text.length} bytes, status=${resp.status})`;
    },

    /**
     * 延迟执行
     * 投递：{"queue":"default","job_type":"delay","data":{"ms":1000,"message":"hello"}}
     */
    delay: async (data) => {
      await sleep(data.ms || 1000);
      return `Delayed ${data.ms}ms: ${data.message || 'done'}`;
    },

    /**
     * 写入 localStorage
     * 投递：{"queue":"default","job_type":"local_storage_set","data":{"key":"foo","value":"bar"}}
     */
    local_storage_set: async (data) => {
      localStorage.setItem(data.key, data.value);
      return `localStorage[${data.key}] = ${data.value}`;
    },

    /**
     * 点击页面元素
     * 投递：{"queue":"default","job_type":"click_element","data":{"selector":"#submit-btn"}}
     */
    click_element: async (data) => {
      const el = document.querySelector(data.selector);
      if (!el) throw new Error(`Element not found: ${data.selector}`);
      el.click();
      return `Clicked: ${data.selector}`;
    },

    // v4: 按 tags 路由处理示例
    // 投递：{"queue":"default","job_type":"tag_task","data":{"message":"hello"},"tags":["urgent","dry-run"]}
    tag_task: async (data, tags) => {
      if (tags.includes('dry-run')) {
        addLog('[tag_task] dry-run 模式，跳过实际操作', 'warn');
        return 'dry-run: skipped';
      }
      if (tags.includes('urgent')) {
        addLog('[tag_task] 紧急任务', 'warn');
      }
      await sleep(200);
      return `tag_task done: ${data.message} (tags=${JSON.stringify(tags)})`;
    },

    // v4: batch then_job 回调（所有任务成功时触发）
    on_success: async (data, tags) => {
      addLog(`[on_success] batch_id=${data.batch_id}`, 'success');
      return `batch ${data.batch_id} succeeded`;
    },

    // v4: batch catch_job 回调（有任务失败时触发）
    on_failure: async (data, tags) => {
      addLog(`[on_failure] batch_id=${data.batch_id}`, 'error');
      return `batch ${data.batch_id} failed`;
    },

    // v4: batch finally_job 回调（无论成功失败，批次完成后必触发）
    on_finally: async (data, tags) => {
      addLog(`[on_finally] batch_id=${data.batch_id}`, 'info');
      return `batch ${data.batch_id} finished`;
    },
  };

  // ─── Worker 核心 ──────────────────────────────────────────────────────────

  function connect() {
    if (stopped) return;

    // 每次 connect 时重新读取配置，确保使用最新值
    const cfg = getCfg();
    const serverUrl = cfg.serverUrl;
    const queue     = cfg.queue;

    // 防御：地址必须是合法的 ws:// 或 wss:// 绝对 URL
    if (!serverUrl || (!serverUrl.startsWith('ws://') && !serverUrl.startsWith('wss://'))) {
      setDot('disconnected', '配置错误');
      addLog(`服务端地址无效: "${serverUrl}"，请点击 ⚙ 配置`, 'error');
      stopped = true;
      $btnStart.disabled = false;
      $btnStop.disabled  = true;
      return;
    }

    const url = `${serverUrl}?queue=${encodeURIComponent(queue)}`;
    setDot('connecting', '连接中...');
    addLog(`连接 → ${url}`, 'info');

    try {
      ws = new WebSocket(url);
    } catch (e) {
      addLog(`WebSocket 初始化失败: ${e.message}`, 'error');
      scheduleReconnect(cfg);
      return;
    }

    ws.onopen = () => {
      setDot('connected', `已连接 · ${queue}`);
      addLog(`已连接，队列=${queue}`, 'success');

      // 心跳：每 20s 发一次 JSON ping，防止连接被中间代理超时断开
      pingTimer = setInterval(() => {
        if (ws && ws.readyState === WebSocket.OPEN) {
          ws.send(JSON.stringify({ type: 'ping' }));
        }
      }, cfg.pingInterval);
    };

    ws.onmessage = async (event) => {
      let msg;
      try { msg = JSON.parse(event.data); } catch { return; }

      switch (msg.type) {
        case 'connected':
          addLog(`Server: ${msg.message}`, 'info');
          break;
        case 'pong':
          // 心跳响应，静默处理
          break;
        case 'ack':
          addLog(`ACK: ${msg.message}`, 'info');
          break;
        case 'job':
          await handleJob(msg);
          break;
        default:
          addLog(`未知消息类型: ${msg.type}`, 'warn');
      }
    };

    ws.onclose = () => {
      clearPing();
      if (!stopped) {
        setDot('disconnected', '已断开，重连中...');
        addLog('连接断开，等待重连...', 'warn');
        scheduleReconnect(cfg);
      }
    };

    ws.onerror = () => {
      addLog('WebSocket 连接错误（检查服务端地址和网络）', 'error');
    };
  }

  async function handleJob(msg) {
    const jobId   = msg.job_id;
    const jobType = msg.job_type;
    const tags    = msg.tags || [];   // v4: 任务标签
    const cfg     = getCfg();

    let payload, data;
    try {
      payload = JSON.parse(msg.payload);
      data    = payload.data || {};
    } catch (e) {
      return sendResult(jobId, false, '', `Invalid payload: ${e.message}`);
    }

    addLog(`Job #${jobId} [${jobType}]${tags.length ? ' tags=' + JSON.stringify(tags) : ''}`, 'job');
    setDot('working', `处理中 #${jobId}`);

    const handler = handlers[jobType];
    if (!handler) {
      failCount++;
      updateBadges();
      setDot('connected', `已连接 · ${cfg.queue}`);
      addLog(`无 handler: "${jobType}"`, 'error');
      return sendResult(jobId, false, '', `No handler for job_type: "${jobType}"`);
    }

    try {
      const result = await handler(data, tags);   // v4: 传递 tags
      jobCount++;
      updateBadges();
      setDot('connected', `已连接 · ${cfg.queue}`);
      addLog(`#${jobId} ✓ ${result}`, 'success');
      sendResult(jobId, true, result, '');
    } catch (err) {
      failCount++;
      updateBadges();
      setDot('connected', `已连接 · ${cfg.queue}`);
      addLog(`#${jobId} ✗ ${err.message}`, 'error');
      sendResult(jobId, false, '', err.message);
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

  function clearReconn() {
    if (reconnTimer) { clearTimeout(reconnTimer); reconnTimer = null; }
  }

  function scheduleReconnect(cfg) {
    clearReconn();
    reconnTimer = setTimeout(() => { if (!stopped) connect(); }, (cfg || getCfg()).reconnectDelay);
  }

  function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }

  function shortUrl(url) {
    if (!url) return '(未配置)';
    return url.replace(/^wss?:\/\//, '').replace(/\/.*$/, '');
  }

  function escHtml(s) {
    return String(s)
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;');
  }

  // ─── 自动启动 ─────────────────────────────────────────────────────────────
  const initCfg = getCfg();
  if (initCfg.autoStart) {
    stopped = false;
    $btnStart.disabled = true;
    $btnStop.disabled  = false;
    connect();
  } else {
    setDot('disconnected', '未连接');
    addLog('点击「▶ 启动」开始连接', 'info');
    addLog(`当前配置：${initCfg.serverUrl}`, 'info');
  }

})();
