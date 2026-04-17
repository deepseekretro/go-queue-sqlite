package main

const indexHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Go Queue Dashboard</title>
<style>
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body { font-family: 'Segoe UI', sans-serif; background: #0f172a; color: #e2e8f0; min-height: 100vh; }
  header { background: linear-gradient(135deg, #1e3a5f, #0f172a); padding: 20px 32px; border-bottom: 1px solid #1e40af; display: flex; align-items: center; gap: 12px; }
  header h1 { font-size: 1.5rem; color: #60a5fa; }
  header span { font-size: 0.85rem; color: #64748b; }
  .badge { display: inline-block; padding: 2px 8px; border-radius: 9999px; font-size: 0.72rem; font-weight: 600; }
  .badge-pending  { background:#1e3a5f; color:#60a5fa; }
  .badge-running  { background:#1c3a2a; color:#34d399; }
  .badge-done     { background:#1a2e1a; color:#86efac; }
  .badge-failed   { background:#3b1a1a; color:#f87171; }

  .container { max-width: 1200px; margin: 0 auto; padding: 24px 16px; }

  .stats { display: grid; grid-template-columns: repeat(auto-fit, minmax(140px, 1fr)); gap: 12px; margin-bottom: 28px; }
  .stat-card { background: #1e293b; border: 1px solid #334155; border-radius: 12px; padding: 16px; text-align: center; }
  .stat-card .num { font-size: 2rem; font-weight: 700; }
  .stat-card .lbl { font-size: 0.75rem; color: #94a3b8; margin-top: 4px; }
  .num-pending { color: #60a5fa; }
  .num-running { color: #34d399; }
  .num-done    { color: #86efac; }
  .num-failed  { color: #f87171; }
  .num-ws      { color: #a78bfa; }

  .panel { background: #1e293b; border: 1px solid #334155; border-radius: 12px; padding: 20px; margin-bottom: 24px; }
  .panel h2 { font-size: 1rem; color: #93c5fd; margin-bottom: 16px; display: flex; align-items: center; gap: 8px; }
  .form-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(180px, 1fr)); gap: 12px; }
  label { font-size: 0.78rem; color: #94a3b8; display: block; margin-bottom: 4px; }
  input, select, textarea { width: 100%; background: #0f172a; border: 1px solid #334155; border-radius: 8px; color: #e2e8f0; padding: 8px 10px; font-size: 0.85rem; outline: none; transition: border .2s; }
  input:focus, select:focus, textarea:focus { border-color: #3b82f6; }
  textarea { resize: vertical; min-height: 72px; font-family: monospace; }
  .btn { padding: 9px 20px; border-radius: 8px; border: none; cursor: pointer; font-size: 0.85rem; font-weight: 600; transition: opacity .2s; }
  .btn:hover { opacity: .85; }
  .btn-primary { background: #3b82f6; color: #fff; }
  .btn-success { background: #22c55e; color: #fff; }
  .btn-danger  { background: #ef4444; color: #fff; }
  .btn-warning { background: #f59e0b; color: #000; }
  .btn-sm { padding: 5px 12px; font-size: 0.75rem; }
  .actions { display: flex; gap: 10px; flex-wrap: wrap; margin-top: 12px; }

  .batch-btns { display: flex; gap: 8px; flex-wrap: wrap; margin-top: 10px; }
  .batch-btn { padding: 7px 14px; border-radius: 8px; border: 1px solid #334155; background: #0f172a; color: #cbd5e1; cursor: pointer; font-size: 0.8rem; transition: all .2s; }
  .batch-btn:hover { border-color: #3b82f6; color: #60a5fa; }

  .table-wrap { overflow-x: auto; }
  table { width: 100%; border-collapse: collapse; font-size: 0.82rem; }
  th { background: #0f172a; color: #64748b; padding: 10px 12px; text-align: left; border-bottom: 1px solid #1e293b; white-space: nowrap; }
  td { padding: 9px 12px; border-bottom: 1px solid #1e293b; vertical-align: top; }
  tr:hover td { background: #1a2744; }
  .payload-cell { max-width: 260px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: #94a3b8; font-family: monospace; font-size: 0.75rem; }
  .ts { color: #64748b; font-size: 0.75rem; }

  /* WebSocket Worker Panel */
  .ws-panel { border-color: #4c1d95; }
  .ws-panel h2 { color: #c4b5fd; }
  .ws-status { display: flex; align-items: center; gap: 8px; margin-bottom: 14px; }
  .ws-dot { width: 10px; height: 10px; border-radius: 50%; background: #64748b; transition: background .3s; }
  .ws-dot.connected { background: #22c55e; animation: pulse 1.5s infinite; }
  .ws-dot.connecting { background: #f59e0b; animation: pulse .6s infinite; }
  .ws-dot.error { background: #ef4444; }
  .ws-label { font-size: 0.85rem; color: #94a3b8; }
  .ws-label.connected { color: #86efac; }
  .ws-label.error { color: #f87171; }

  .log-box { background: #0f172a; border: 1px solid #1e293b; border-radius: 8px; padding: 12px; height: 220px; overflow-y: auto; font-family: monospace; font-size: 0.78rem; line-height: 1.6; }
  .log-box div { padding: 1px 0; border-bottom: 1px solid rgba(255,255,255,0.03); }
  .log-box .log-info  { color: #60a5fa; }
  .log-box .log-ok    { color: #86efac; }
  .log-box .log-warn  { color: #fbbf24; }
  .log-box .log-err   { color: #f87171; }
  .log-box .log-job   { color: #c4b5fd; }
  .log-box .log-ts    { color: #475569; margin-right: 6px; }

  .worker-config { display: grid; grid-template-columns: 1fr 1fr auto; gap: 10px; align-items: end; margin-bottom: 14px; }

  #toast { position: fixed; bottom: 24px; right: 24px; background: #1e293b; border: 1px solid #334155; border-radius: 10px; padding: 12px 18px; font-size: 0.85rem; opacity: 0; transition: opacity .3s; pointer-events: none; max-width: 320px; z-index: 999; }
  #toast.show { opacity: 1; }
  #toast.ok   { border-color: #22c55e; color: #86efac; }
  #toast.err  { border-color: #ef4444; color: #f87171; }

  .filter-bar { display: flex; gap: 10px; flex-wrap: wrap; align-items: flex-end; margin-bottom: 14px; }
  .filter-bar select, .filter-bar input { width: auto; }
  .dot { width: 8px; height: 8px; border-radius: 50%; display: inline-block; margin-right: 4px; }
  .dot-running { background: #34d399; animation: pulse 1s infinite; }
  @keyframes pulse { 0%,100%{opacity:1} 50%{opacity:.4} }

  .code-block { background: #0f172a; border: 1px solid #1e293b; border-radius: 8px; padding: 14px; font-family: monospace; font-size: 0.78rem; color: #94a3b8; white-space: pre-wrap; word-break: break-all; margin-top: 10px; }
  .tab-bar { display: flex; gap: 4px; margin-bottom: 14px; }
  .tab { padding: 6px 14px; border-radius: 6px; border: 1px solid #334155; background: #0f172a; color: #64748b; cursor: pointer; font-size: 0.8rem; }
  .tab.active { background: #1e3a5f; color: #60a5fa; border-color: #3b82f6; }
</style>
</head>
<body>
<header>
  <div>
    <h1>⚡ Go Queue Dashboard</h1>
    <span>Laravel-style Database Queue · SQLite · WebSocket Workers</span>
  </div>
  <div style="margin-left:auto;display:flex;align-items:center;gap:8px;">
    <span class="dot dot-running"></span>
    <span style="font-size:.8rem;color:#34d399">Server Active</span>
    <button class="btn btn-sm" style="background:#1e293b;border:1px solid #334155;color:#94a3b8;margin-left:12px" onclick="loadAll()">↻ Refresh</button>
  </div>
</header>

<div class="container">

  <!-- Stats -->
  <div class="stats" id="stats">
    <div class="stat-card"><div class="num num-pending" id="s-pending">-</div><div class="lbl">Pending</div></div>
    <div class="stat-card"><div class="num num-running" id="s-running">-</div><div class="lbl">Running</div></div>
    <div class="stat-card"><div class="num num-done"    id="s-done">-</div><div class="lbl">Done</div></div>
    <div class="stat-card"><div class="num num-failed"  id="s-failed">-</div><div class="lbl">Failed</div></div>
    <div class="stat-card"><div class="num" style="color:#a78bfa" id="s-total">-</div><div class="lbl">Total Jobs</div></div>
    <div class="stat-card"><div class="num num-ws" id="s-ws">-</div><div class="lbl">WS Workers</div></div>
  </div>

  <!-- WebSocket Worker Panel -->
  <div class="panel ws-panel">
    <h2>🔌 WebSocket Worker 示例</h2>

    <div class="tab-bar">
      <div class="tab active" onclick="switchTab('demo')">在线演示</div>
      <div class="tab" onclick="switchTab('code')">接入代码</div>
    </div>

    <!-- Demo Tab -->
    <div id="tab-demo">
      <div class="worker-config">
        <div>
          <label>Worker 监听队列</label>
          <select id="ws-queue">
            <option value="default">default</option>
            <option value="emails">emails</option>
          </select>
        </div>
        <div>
          <label>处理延迟模拟 (ms)</label>
          <input type="number" id="ws-delay" value="800" min="0" max="5000">
        </div>
        <div>
          <button class="btn btn-success" id="ws-connect-btn" onclick="toggleWorker()">▶ 启动 WS Worker</button>
        </div>
      </div>

      <div class="ws-status">
        <div class="ws-dot" id="ws-dot"></div>
        <span class="ws-label" id="ws-label">未连接 — 点击「启动 WS Worker」连接到服务器</span>
      </div>

      <div class="log-box" id="ws-log">
        <div><span class="log-info">// WebSocket Worker 日志将在此显示...</span></div>
      </div>

      <div class="actions" style="margin-top:12px;">
        <button class="btn btn-primary btn-sm" onclick="dispatchToWs('send_email')">发送 send_email 任务</button>
        <button class="btn btn-primary btn-sm" onclick="dispatchToWs('generate_report')">发送 generate_report 任务</button>
        <button class="btn btn-primary btn-sm" onclick="dispatchToWs('resize_image')">发送 resize_image 任务</button>
        <button class="btn btn-danger  btn-sm" onclick="dispatchToWs('fail_job')">发送 fail_job 任务</button>
        <button class="btn btn-sm" style="background:#1e293b;border:1px solid #334155;color:#94a3b8" onclick="clearLog()">清空日志</button>
      </div>
    </div>

    <!-- Code Tab -->
    <div id="tab-code" style="display:none">
      <p style="font-size:.82rem;color:#94a3b8;margin-bottom:12px;">
        将以下代码复制到你的项目中，即可接入队列系统作为真实 Worker。
        Worker 通过 WebSocket 连接到服务器，接收任务、处理后返回结果。
      </p>
      <div class="tab-bar" style="margin-bottom:8px;">
        <div class="tab active" onclick="switchCodeTab('js')">JavaScript</div>
        <div class="tab" onclick="switchCodeTab('python')">Python</div>
      </div>
      <div id="code-js">
        <div class="code-block" id="code-js-content"></div>
      </div>
      <div id="code-python" style="display:none">
        <div class="code-block" id="code-python-content"></div>
      </div>
    </div>
  </div>

  <!-- Dispatch -->
  <div class="panel">
    <h2>📤 Dispatch Job</h2>
    <div class="form-grid">
      <div>
        <label>Queue</label>
        <select id="d-queue">
          <option value="default">default</option>
          <option value="emails">emails</option>
          <option value="reports">reports</option>
        </select>
      </div>
      <div>
        <label>Job Type</label>
        <select id="d-type" onchange="fillTemplate()">
          <option value="send_email">send_email</option>
          <option value="generate_report">generate_report</option>
          <option value="resize_image">resize_image</option>
          <option value="fail_job">fail_job (test failure)</option>
        </select>
      </div>
      <div>
        <label>Delay (seconds)</label>
        <input type="number" id="d-delay" value="0" min="0">
      </div>
      <div style="grid-column:1/-1">
        <label>Payload (JSON)</label>
        <textarea id="d-payload">{"to":"user@example.com","subject":"Welcome!"}</textarea>
      </div>
    </div>
    <div class="actions">
      <button class="btn btn-primary" onclick="dispatchJob()">Dispatch Job</button>
    </div>
    <div style="margin-top:16px;border-top:1px solid #334155;padding-top:14px;">
      <label style="color:#64748b;font-size:.78rem;">⚡ Quick Batch Dispatch</label>
      <div class="batch-btns">
        <button class="batch-btn" onclick="batchDispatch('send_email',5)">5× send_email</button>
        <button class="batch-btn" onclick="batchDispatch('generate_report',3)">3× generate_report</button>
        <button class="batch-btn" onclick="batchDispatch('resize_image',4)">4× resize_image</button>
        <button class="batch-btn" onclick="batchDispatch('fail_job',3)">3× fail_job</button>
        <button class="batch-btn" onclick="batchDispatch('send_email',10)">10× send_email (stress)</button>
      </div>
    </div>
  </div>

  <!-- Actions -->
  <div class="panel">
    <h2>🔧 Queue Actions</h2>
    <div class="actions">
      <button class="btn btn-warning" onclick="retryFailed()">↩ Retry All Failed</button>
      <button class="btn btn-danger"  onclick="clearFailed()">🗑 Clear Failed Table</button>
    </div>
  </div>

  <!-- Jobs list -->
  <div class="panel">
    <h2>📋 Jobs</h2>
    <div class="filter-bar">
      <div>
        <label>Queue</label>
        <select id="f-queue" onchange="loadJobs()">
          <option value="">All</option>
          <option value="default">default</option>
          <option value="emails">emails</option>
          <option value="reports">reports</option>
        </select>
      </div>
      <div>
        <label>Status</label>
        <select id="f-status" onchange="loadJobs()">
          <option value="">All</option>
          <option value="pending">pending</option>
          <option value="running">running</option>
          <option value="done">done</option>
          <option value="failed">failed</option>
        </select>
      </div>
      <div>
        <label>Limit</label>
        <input type="number" id="f-limit" value="30" style="width:80px" onchange="loadJobs()">
      </div>
    </div>
    <div class="table-wrap">
      <table>
        <thead>
          <tr><th>ID</th><th>Queue</th><th>Job Type</th><th>Status</th><th>Attempts</th><th>Payload</th><th>Created</th><th>Updated</th></tr>
        </thead>
        <tbody id="jobs-tbody">
          <tr><td colspan="8" style="text-align:center;color:#64748b;padding:24px">Loading...</td></tr>
        </tbody>
      </table>
    </div>
  </div>

</div>

<div id="toast"></div>

<script>
// ─── WebSocket URL ───────────────────────────────────────────────────────────
const WS_URL = 'wss://24f7f8fe-1114-4510-9f39-bdbd913f8772.deepnoteproject.com/ws/worker';

// ─── Code examples ───────────────────────────────────────────────────────────
const JS_CODE = ` + "`" + `// JavaScript WebSocket Worker 示例
// 连接到: wss://24f7f8fe-1114-4510-9f39-bdbd913f8772.deepnoteproject.com/ws/worker?queue=default

const WS_URL = 'wss://24f7f8fe-1114-4510-9f39-bdbd913f8772.deepnoteproject.com/ws/worker';

function createWorker(queue = 'default', handlers = {}) {
  const ws = new WebSocket(\` + "`" + `\${WS_URL}?queue=\${queue}\` + "`" + `);

  ws.onopen = () => console.log('[Worker] Connected, queue=' + queue);

  ws.onmessage = async (event) => {
    const msg = JSON.parse(event.data);

    if (msg.type === 'connected') {
      console.log('[Worker] ' + msg.message);
      return;
    }

    if (msg.type === 'job') {
      console.log(\` + "`" + `[Worker] Received job #\${msg.job_id} type=\${msg.job_type}\` + "`" + `);
      const payload = JSON.parse(msg.payload);
      const data = payload.data;

      try {
        const handler = handlers[msg.job_type];
        if (!handler) throw new Error('No handler for: ' + msg.job_type);

        const log = await handler(data);

        ws.send(JSON.stringify({
          type: 'result',
          job_id: msg.job_id,
          success: true,
          log: log || 'done'
        }));
      } catch (err) {
        ws.send(JSON.stringify({
          type: 'result',
          job_id: msg.job_id,
          success: false,
          error: err.message
        }));
      }
    }
  };

  ws.onclose = () => console.log('[Worker] Disconnected');
  ws.onerror = (e) => console.error('[Worker] Error', e);
  return ws;
}

// 注册任务处理器
createWorker('default', {
  send_email: async (data) => {
    console.log('Sending email to', data.to);
    await sleep(500);
    return \` + "`" + `Email sent to \${data.to}\` + "`" + `;
  },
  generate_report: async (data) => {
    console.log('Generating report:', data.name);
    await sleep(1000);
    return \` + "`" + `Report \${data.name} generated\` + "`" + `;
  },
  resize_image: async (data) => {
    console.log('Resizing image:', data.url);
    await sleep(800);
    return \` + "`" + `Image resized: \${data.url}\` + "`" + `;
  }
});

function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }` + "`" + `;

const PYTHON_CODE = ` + "`" + `# Python WebSocket Worker 示例
# pip install websocket-client

import json, time, websocket

WS_URL = 'wss://24f7f8fe-1114-4510-9f39-bdbd913f8772.deepnoteproject.com/ws/worker?queue=default'

HANDLERS = {
    'send_email': lambda data: (
        time.sleep(0.5),
        f"Email sent to {data['to']}"
    )[-1],
    'generate_report': lambda data: (
        time.sleep(1),
        f"Report {data['name']} generated"
    )[-1],
    'resize_image': lambda data: (
        time.sleep(0.8),
        f"Image resized: {data['url']}"
    )[-1],
}

def on_message(ws, message):
    msg = json.loads(message)

    if msg['type'] == 'connected':
        print(f"[Worker] {msg['message']}")
        return

    if msg['type'] == 'job':
        job_id  = msg['job_id']
        job_type = msg['job_type']
        payload = json.loads(msg['payload'])
        data    = payload['data']
        print(f"[Worker] Job #{job_id} type={job_type}")

        handler = HANDLERS.get(job_type)
        if not handler:
            ws.send(json.dumps({
                'type': 'result', 'job_id': job_id,
                'success': False, 'error': f'No handler for {job_type}'
            }))
            return
        try:
            log = handler(data)
            ws.send(json.dumps({
                'type': 'result', 'job_id': job_id,
                'success': True, 'log': log
            }))
        except Exception as e:
            ws.send(json.dumps({
                'type': 'result', 'job_id': job_id,
                'success': False, 'error': str(e)
            }))

    if msg['type'] == 'ack':
        print(f"[Worker] Server ack: {msg['message']}")

def on_error(ws, error): print(f"[Worker] Error: {error}")
def on_close(ws, *a):    print("[Worker] Disconnected")
def on_open(ws):         print("[Worker] Connected")

ws = websocket.WebSocketApp(WS_URL,
    on_open=on_open, on_message=on_message,
    on_error=on_error, on_close=on_close)
ws.run_forever()` + "`" + `;

document.getElementById('code-js-content').textContent = JS_CODE;
document.getElementById('code-python-content').textContent = PYTHON_CODE;

// ─── Tab switching ────────────────────────────────────────────────────────────
function switchTab(name) {
  document.getElementById('tab-demo').style.display = name === 'demo' ? '' : 'none';
  document.getElementById('tab-code').style.display = name === 'code' ? '' : 'none';
  document.querySelectorAll('.tab-bar:first-of-type .tab').forEach((t, i) => {
    t.classList.toggle('active', (i === 0 && name === 'demo') || (i === 1 && name === 'code'));
  });
}

function switchCodeTab(lang) {
  document.getElementById('code-js').style.display     = lang === 'js'     ? '' : 'none';
  document.getElementById('code-python').style.display = lang === 'python' ? '' : 'none';
}

// ─── WebSocket Worker (in-browser demo) ──────────────────────────────────────
let ws = null;
let wsRunning = false;

function wsLog(msg, cls = 'log-info') {
  const box = document.getElementById('ws-log');
  const ts = new Date().toLocaleTimeString('zh-CN', {hour12: false});
  box.innerHTML += ` + "`" + `<div><span class="log-ts">${ts}</span><span class="${cls}">${escHtml(msg)}</span></div>` + "`" + `;
  box.scrollTop = box.scrollHeight;
}

function escHtml(s) {
  return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;');
}

function clearLog() {
  document.getElementById('ws-log').innerHTML = '<div><span class="log-info">// 日志已清空</span></div>';
}

function setWsStatus(state, text) {
  const dot = document.getElementById('ws-dot');
  const lbl = document.getElementById('ws-label');
  dot.className = 'ws-dot ' + state;
  lbl.className = 'ws-label ' + state;
  lbl.textContent = text;
}

function toggleWorker() {
  if (wsRunning) {
    stopWorker();
  } else {
    startWorker();
  }
}

function startWorker() {
  const queue = document.getElementById('ws-queue').value;
  const delay = parseInt(document.getElementById('ws-delay').value) || 0;
  const url = WS_URL + '?queue=' + queue;

  setWsStatus('connecting', '连接中...');
  wsLog(` + "`" + `正在连接 ${url}` + "`" + `, 'log-info');

  ws = new WebSocket(url);

  ws.onopen = () => {
    wsRunning = true;
    setWsStatus('connected', ` + "`" + `已连接 — 监听队列: ${queue}` + "`" + `);
    wsLog('WebSocket 连接成功 ✓', 'log-ok');
    document.getElementById('ws-connect-btn').textContent = '⏹ 停止 WS Worker';
    document.getElementById('ws-connect-btn').className = 'btn btn-danger';
  };

  ws.onmessage = async (event) => {
    let msg;
    try { msg = JSON.parse(event.data); } catch { return; }

    if (msg.type === 'connected') {
      wsLog(` + "`" + `[Server] ${msg.message}` + "`" + `, 'log-info');
      return;
    }

    if (msg.type === 'ack') {
      wsLog(` + "`" + `[Server] ${msg.message}` + "`" + `, 'log-ok');
      return;
    }

    if (msg.type === 'job') {
      const payload = JSON.parse(msg.payload);
      const data = payload.data;
      wsLog(` + "`" + `📥 收到任务 #${msg.job_id}  type=${msg.job_type}  queue=${msg.queue}` + "`" + `, 'log-job');
      wsLog(` + "`" + `   payload: ${JSON.stringify(data)}` + "`" + `, 'log-info');

      // 模拟处理
      await sleep(delay);

      if (msg.job_type === 'fail_job') {
        wsLog(` + "`" + `❌ 任务 #${msg.job_id} 处理失败（intentional）` + "`" + `, 'log-err');
        ws.send(JSON.stringify({
          type: 'result', job_id: msg.job_id,
          success: false, error: 'intentional failure from WS worker'
        }));
        return;
      }

      const logMsg = processJobLocally(msg.job_type, data);
      wsLog(` + "`" + `✅ 任务 #${msg.job_id} 完成: ${logMsg}` + "`" + `, 'log-ok');
      ws.send(JSON.stringify({
        type: 'result', job_id: msg.job_id,
        success: true, log: logMsg
      }));
    }
  };

  ws.onclose = () => {
    wsRunning = false;
    setWsStatus('', '已断开连接');
    wsLog('WebSocket 连接已关闭', 'log-warn');
    document.getElementById('ws-connect-btn').textContent = '▶ 启动 WS Worker';
    document.getElementById('ws-connect-btn').className = 'btn btn-success';
  };

  ws.onerror = (e) => {
    setWsStatus('error', '连接错误');
    wsLog('WebSocket 错误: ' + (e.message || '请检查服务器是否运行'), 'log-err');
  };
}

function stopWorker() {
  if (ws) { ws.close(); ws = null; }
  wsRunning = false;
}

function processJobLocally(jobType, data) {
  switch (jobType) {
    case 'send_email':      return ` + "`" + `Email sent to ${data.to}: ${data.subject}` + "`" + `;
    case 'generate_report': return ` + "`" + `Report generated: ${data.name}` + "`" + `;
    case 'resize_image':    return ` + "`" + `Image resized: ${data.url}` + "`" + `;
    default:                return ` + "`" + `Job type ${jobType} processed` + "`" + `;
  }
}

function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }

async function dispatchToWs(jobType) {
  const queue = document.getElementById('ws-queue').value;
  const payloads = {
    send_email:      {to: 'ws-worker@example.com', subject: 'WS Worker Test'},
    generate_report: {name: 'ws_test_report'},
    resize_image:    {url: 'https://cdn.example.com/ws-test.jpg'},
    fail_job:        {}
  };
  const res = await fetch('/api/jobs', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({queue, job_type: jobType, data: payloads[jobType] || {}, delay: 0})
  });
  const j = await res.json();
  if (res.ok) {
    toast(` + "`" + `✅ Job #${j.job_id} dispatched → [${queue}]` + "`" + `);
    if (!wsRunning) wsLog('⚠ WS Worker 未连接，任务将由内置 Go Worker 处理', 'log-warn');
  }
}

// ─── Dashboard ────────────────────────────────────────────────────────────────
const templates = {
  send_email:      '{"to":"user@example.com","subject":"Welcome!"}',
  generate_report: '{"name":"monthly_sales_2026"}',
  resize_image:    '{"url":"https://example.com/photo.jpg"}',
  fail_job:        '{}'
};

function fillTemplate() {
  const t = document.getElementById('d-type').value;
  document.getElementById('d-payload').value = templates[t] || '{}';
}

function toast(msg, type='ok') {
  const el = document.getElementById('toast');
  el.textContent = msg;
  el.className = 'show ' + type;
  clearTimeout(el._t);
  el._t = setTimeout(() => el.className = '', 3000);
}

async function dispatchJob() {
  const queue   = document.getElementById('d-queue').value;
  const jobType = document.getElementById('d-type').value;
  const delay   = parseInt(document.getElementById('d-delay').value) || 0;
  let data;
  try { data = JSON.parse(document.getElementById('d-payload').value); }
  catch(e) { toast('Invalid JSON payload', 'err'); return; }

  const res = await fetch('/api/jobs', {
    method: 'POST',
    headers: {'Content-Type':'application/json'},
    body: JSON.stringify({queue, job_type: jobType, data, delay})
  });
  const j = await res.json();
  if (res.ok) { toast(` + "`" + `✅ Job #${j.job_id} dispatched to [${j.queue}]` + "`" + `); loadAll(); }
  else { toast('Error: ' + j.error, 'err'); }
}

async function batchDispatch(jobType, count) {
  const queue = document.getElementById('d-queue').value;
  const payloads = {
    send_email:      (i) => ({to:` + "`" + `user${i}@example.com` + "`" + `, subject:` + "`" + `Batch email #${i}` + "`" + `}),
    generate_report: (i) => ({name:` + "`" + `report_batch_${i}` + "`" + `}),
    resize_image:    (i) => ({url:` + "`" + `https://cdn.example.com/img${i}.jpg` + "`" + `}),
    fail_job:        ()  => ({})
  };
  const fn = payloads[jobType] || (() => ({}));
  let ok = 0;
  for (let i = 1; i <= count; i++) {
    const res = await fetch('/api/jobs', {
      method: 'POST',
      headers: {'Content-Type':'application/json'},
      body: JSON.stringify({queue, job_type: jobType, data: fn(i), delay: 0})
    });
    if (res.ok) ok++;
  }
  toast(` + "`" + `✅ Dispatched ${ok}/${count} ${jobType} jobs` + "`" + `);
  loadAll();
}

async function loadStats() {
  const res = await fetch('/api/stats');
  const s = await res.json();
  document.getElementById('s-pending').textContent = s.pending || 0;
  document.getElementById('s-running').textContent = s.running || 0;
  document.getElementById('s-done').textContent    = s.done    || 0;
  document.getElementById('s-failed').textContent  = s.failed  || 0;
  document.getElementById('s-ws').textContent      = s.ws_workers || 0;
  const total = (s.pending||0)+(s.running||0)+(s.done||0)+(s.failed||0);
  document.getElementById('s-total').textContent = total;
}

function fmtTime(unix) {
  if (!unix) return '-';
  return new Date(unix * 1000).toLocaleTimeString('zh-CN', {hour12:false});
}

function jobTypeBadge(payload) {
  try { return JSON.parse(payload).job_type || '-'; } catch { return '-'; }
}

function statusBadge(s) {
  return ` + "`" + `<span class="badge badge-${s}">${s}</span>` + "`" + `;
}

async function loadJobs() {
  const queue  = document.getElementById('f-queue').value;
  const status = document.getElementById('f-status').value;
  const limit  = document.getElementById('f-limit').value || 30;
  let url = ` + "`" + `/api/jobs?limit=${limit}` + "`" + `;
  if (queue)  url += ` + "`" + `&queue=${queue}` + "`" + `;
  if (status) url += ` + "`" + `&status=${status}` + "`" + `;

  const res = await fetch(url);
  const jobs = await res.json();
  const tbody = document.getElementById('jobs-tbody');

  if (!jobs || jobs.length === 0) {
    tbody.innerHTML = '<tr><td colspan="8" style="text-align:center;color:#64748b;padding:24px">No jobs found</td></tr>';
    return;
  }

  tbody.innerHTML = jobs.map(j => {
    const jt = jobTypeBadge(j.payload);
    const payloadShort = j.payload.length > 80 ? j.payload.slice(0,80)+'…' : j.payload;
    return ` + "`" + `<tr>
      <td style="color:#a78bfa;font-weight:600">#${j.id}</td>
      <td><code style="color:#fbbf24">${j.queue}</code></td>
      <td><code style="color:#93c5fd">${jt}</code></td>
      <td>${statusBadge(j.status)}</td>
      <td style="text-align:center">${j.attempts}</td>
      <td class="payload-cell" title="${j.payload.replace(/"/g,'&quot;')}">${payloadShort}</td>
      <td class="ts">${fmtTime(j.created_at)}</td>
      <td class="ts">${fmtTime(j.updated_at)}</td>
    </tr>` + "`" + `;
  }).join('');
}

async function retryFailed() {
  const res = await fetch('/api/jobs/retry-failed', {method:'POST'});
  const j = await res.json();
  toast('↩ ' + j.message);
  loadAll();
}

async function clearFailed() {
  if (!confirm('Clear all failed jobs from failed_jobs table?')) return;
  const res = await fetch('/api/jobs/failed', {method:'DELETE'});
  const j = await res.json();
  toast('🗑 ' + j.message);
  loadAll();
}

function loadAll() { loadStats(); loadJobs(); }

// SSE 实时推送：stats 变化时自动刷新，无需轮询
(function initSSE() {
  const es = new EventSource('/api/events');
  es.onmessage = (e) => {
    if (e.data === 'refresh') loadAll();
  };
  es.onerror = () => {
    // SSE 断开后 5s 重连，同时保底轮询
    setTimeout(initSSE, 5000);
  };
  // 保底：每 30s 刷新一次（防止 SSE 静默失效）
  setInterval(loadAll, 30000);
})();

loadAll();
</script>
</body>
</html>
`
