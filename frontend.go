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

  /* Workers Panel */
  .workers-panel { border-color: #0e7490; }
  .workers-panel h2 { color: #67e8f9; }
  .worker-row-idle td { }
  .worker-row-busy td { background: rgba(52,211,153,0.04); }
  .badge { display: inline-block; padding: 2px 8px; border-radius: 12px; font-size: 0.72rem; font-weight: 600; }
  .badge-idle  { background: #1e3a2f; color: #34d399; border: 1px solid #34d399; }
  .badge-busy  { background: #1a3a1a; color: #86efac; border: 1px solid #22c55e; animation: pulse 1.5s infinite; }
  .btn-kick { background: #450a0a; border: 1px solid #ef4444; color: #f87171; padding: 3px 10px; border-radius: 6px; font-size: 0.75rem; cursor: pointer; transition: background .2s; }
  .btn-kick:hover { background: #7f1d1d; }

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
  <div style="margin-left:auto;display:flex;align-items:center;gap:12px;">
    <span class="dot dot-running"></span>
    <span style="font-size:.8rem;color:#34d399">Server Active</span>
    <a href="/examples/ws_worker_demo/" style="font-size:.8rem;padding:5px 12px;background:#1e293b;border:1px solid #334155;color:#a78bfa;border-radius:6px;text-decoration:none;" target="_blank">⚡ WS Worker Demo</a>
    <button class="btn btn-sm" style="background:#1e293b;border:1px solid #334155;color:#94a3b8;" onclick="loadAll()">↻ Refresh</button>
    <button class="btn btn-sm" style="background:#450a0a;border:1px solid #7f1d1d;color:#fca5a5;" onclick="confirmDBReset()">🗑 重置数据库</button>
    <div style="display:flex;align-items:center;gap:8px;margin-left:8px;padding-left:12px;border-left:1px solid #334155;">
      <span style="font-size:.8rem;color:#94a3b8;">👤 <span id="current-user">admin</span></span>
      <a href="/logout" style="font-size:.8rem;padding:5px 12px;background:#7f1d1d;border:1px solid #991b1b;color:#fca5a5;border-radius:6px;text-decoration:none;transition:opacity .2s;" onmouseover="this.style.opacity='.8'" onmouseout="this.style.opacity='1'">登出</a>
    </div>
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

  <!-- Workers Panel -->
  <div class="panel workers-panel">
    <h2>🖥 Connected Workers</h2>
    <div class="table-wrap">
      <table id="workers-table">
        <thead>
          <tr>
            <th>Worker ID</th>
            <th>Queue</th>
            <th>Status</th>
            <th>Current Job</th>
            <th>Action</th>
          </tr>
        </thead>
        <tbody id="workers-tbody">
          <tr><td colspan="5" style="text-align:center;color:#64748b;padding:24px">Loading...</td></tr>
        </tbody>
      </table>
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
      <div>
        <label>Tag</label>
        <input type="text" id="f-tag" placeholder="filter by tag…" style="width:110px" oninput="loadJobs()">
      </div>
    </div>
    <div class="table-wrap">
      <table>
        <thead>
          <tr><th>ID</th><th>Queue</th><th>Job Type</th><th>Status</th><th>Attempts</th><th>Tags</th><th>Payload</th><th>Created</th><th>Updated</th></tr>
        </thead>
        <tbody id="jobs-tbody">
          <tr><td colspan="9" style="text-align:center;color:#64748b;padding:24px">Loading...</td></tr>
        </tbody>
      </table>
    </div>
  </div>

</div>

<div id="toast"></div>

<script>
function escHtml(s) {
  return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;');
}

function toast(msg, type='ok') {
  const el = document.getElementById('toast');
  el.textContent = msg;
  el.className = 'show ' + type;
  clearTimeout(el._t);
  el._t = setTimeout(() => el.className = '', 3000);
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
  loadWorkers();
}

async function loadWorkers() {
  const res = await fetch('/api/workers');
  if (!res.ok) return;
  const workers = await res.json();
  const tbody = document.getElementById('workers-tbody');
  if (!workers || workers.length === 0) {
    tbody.innerHTML = '<tr><td colspan="5" style="text-align:center;color:#64748b;padding:24px">No workers connected</td></tr>';
    return;
  }
  tbody.innerHTML = workers.map(function(w) {
    var isIdle = w.idle;
    var statusBadge = isIdle
      ? '<span class="badge badge-idle">&#9679; Idle</span>'
      : '<span class="badge badge-busy">&#9679; Busy</span>';
    var currentJob = w.current_job_id
      ? '<a href="#" onclick="filterByJob(' + w.current_job_id + ');return false;" style="color:#60a5fa">#' + w.current_job_id + '</a>'
      : '<span style="color:#475569">&mdash;</span>';
    var shortId = w.id.length > 20 ? '\u2026' + w.id.slice(-16) : w.id;
    var rowClass = isIdle ? 'worker-row-idle' : 'worker-row-busy';
    return '<tr class="' + rowClass + '">'
      + '<td style="font-family:monospace;font-size:0.75rem" title="' + escHtml(w.id) + '">' + escHtml(shortId) + '</td>'
      + '<td><span style="color:#a78bfa">' + escHtml(w.queue) + '</span></td>'
      + '<td>' + statusBadge + '</td>'
      + '<td>' + currentJob + '</td>'
      + '<td><button class="btn-kick" onclick="kickWorker(\'' + escHtml(w.id) + '\', this)">&#9889; Kick</button></td>'
      + '</tr>';
  }).join('');
}

async function kickWorker(workerId, btn) {
  if (!confirm('确定要踢掉 Worker ' + workerId + ' 吗？\n当前任务将被放回队列重新处理。')) return;
  btn.disabled = true;
  btn.textContent = '...';
  const res = await fetch('/api/workers/' + encodeURIComponent(workerId), { method: 'DELETE' });
  const data = await res.json();
  if (res.ok) {
    toast('✅ ' + (data.message || 'Worker kicked'));
    setTimeout(loadWorkers, 1000);
  } else {
    toast('❌ ' + (data.error || 'Failed to kick worker'), true);
    btn.disabled = false;
    btn.textContent = '⚡ Kick';
  }
}

function filterByJob(jobId) {
  document.getElementById('f-queue').value = '';
  document.getElementById('f-status').value = '';
  loadJobs();
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
  const tag    = document.getElementById('f-tag') ? document.getElementById('f-tag').value : '';
  const limit  = document.getElementById('f-limit').value || 30;
  let url = ` + "`" + `/api/jobs?limit=${limit}` + "`" + `;
  if (queue)  url += ` + "`" + `&queue=${queue}` + "`" + `;
  if (status) url += ` + "`" + `&status=${status}` + "`" + `;
  if (tag)    url += ` + "`" + `&tag=${encodeURIComponent(tag)}` + "`" + `;

  const res = await fetch(url);
  const jobs = await res.json();
  const tbody = document.getElementById('jobs-tbody');

  if (!jobs || jobs.length === 0) {
    tbody.innerHTML = '<tr><td colspan="9" style="text-align:center;color:#64748b;padding:24px">No jobs found</td></tr>';
    return;
  }

  tbody.innerHTML = jobs.map(j => {
    const jt = jobTypeBadge(j.payload);
    const payloadShort = j.payload.length > 80 ? j.payload.slice(0,80)+'…' : j.payload;
    const tagsHtml = (j.tags && j.tags.length > 0)
      ? j.tags.map(t => ` + "`" + `<span style="display:inline-block;padding:1px 6px;border-radius:10px;font-size:0.68rem;background:#1e3a5f;color:#93c5fd;border:1px solid #3b82f6;margin:1px;cursor:pointer" onclick="document.getElementById('f-tag').value='${escHtml(t)}';loadJobs()">${escHtml(t)}</span>` + "`" + `).join('')
      : '<span style="color:#475569">&mdash;</span>';
    return ` + "`" + `<tr>
      <td style="color:#a78bfa;font-weight:600">#${j.id}</td>
      <td><code style="color:#fbbf24">${j.queue}</code></td>
      <td><code style="color:#93c5fd">${jt}</code></td>
      <td>${statusBadge(j.status)}</td>
      <td style="text-align:center">${j.attempts}</td>
      <td>${tagsHtml}</td>
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

async function confirmDBReset() {
  const confirmed = confirm(
    '⚠️ 确认重置数据库？\n\n' +
    '此操作将清空所有 Jobs、Batches 和 Job Chains 数据，\n' +
    '且无法撤销。\n\n' +
    '确定要继续吗？'
  );
  if (!confirmed) return;

  // 二次确认
  const confirmed2 = confirm('再次确认：所有任务数据将被永久删除，继续？');
  if (!confirmed2) return;

  try {
    const res = await fetch('/api/db/reset', { method: 'POST' });
    const data = await res.json();
    if (res.ok) {
      toast('✅ 数据库已重置，所有任务数据已清空');
      loadAll();
    } else {
      toast('❌ 重置失败：' + (data.error || '未知错误'), true);
    }
  } catch (e) {
    toast('❌ 请求失败：' + e.message, true);
  }
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

// ─── 获取当前登录用户名 ─────────────────────────────────────────────────────
fetch('/api/me', {credentials: 'same-origin'})
  .then(r => r.json())
  .then(d => {
    const el = document.getElementById('current-user');
    if (el && d.username) el.textContent = d.username;
  })
  .catch(() => {});

// ─── 全局 fetch 拦截：API 返回 401 时自动跳转登录页 ─────────────────────────
(function patchFetch() {
  const _fetch = window.fetch;
  window.fetch = function(...args) {
    return _fetch.apply(this, args).then(resp => {
      if (resp.status === 401 && !resp.url.includes('/login')) {
        window.location.href = '/login?next=' + encodeURIComponent(window.location.pathname);
      }
      return resp;
    });
  };
})();
</script>
</body>
</html>
`
