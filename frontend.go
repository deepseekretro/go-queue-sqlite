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

  /* ── Jobs 分页控件 ─────────────────────────────────────────────────────── */
  #jobs-pagination { margin-top: 12px; }
  #jobs-pagination button:disabled { opacity: 0.35; cursor: not-allowed; }
  #jobs-pagination button { transition: background .15s, border-color .15s; }
  #jobs-pagination button:not(:disabled):hover { border-color: #6366f1 !important; color: #c7d2fe !important; }

  /* ── Cron Jobs 面板 ─────────────────────────────────────────────────────── */
  #crons-panel .table-wrap table th:last-child,
  #crons-panel .table-wrap table td:last-child { white-space: nowrap; }
  #cron-modal input:focus, #cron-modal textarea:focus {
    outline: none; border-color: #6366f1; box-shadow: 0 0 0 2px rgba(99,102,241,.25);
  }
  #cron-logs-modal .table-wrap { border-radius: 6px; overflow: hidden; }
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
        <select id="f-queue" onchange="jobsGoPage(1)">
          <option value="">All</option>
        </select>
      </div>
      <div>
        <label>Status</label>
        <select id="f-status" onchange="jobsGoPage(1)">
          <option value="">All</option>
          <option value="pending">pending</option>
          <option value="running">running</option>
          <option value="done">done</option>
          <option value="failed">failed</option>
        </select>
      </div>
      <div>
        <label>Per Page</label>
        <select id="f-per-page" onchange="jobsGoPage(1)" style="width:72px">
          <option value="20">20</option>
          <option value="50">50</option>
          <option value="100">100</option>
        </select>
      </div>
      <div>
        <label>Tag</label>
        <input type="text" id="f-tag" placeholder="filter by tag…" style="width:110px" oninput="jobsGoPage(1)">
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
    <div id="jobs-pagination" style="display:flex;align-items:center;justify-content:space-between;margin-top:12px;flex-wrap:wrap;gap:8px;">
      <span id="jobs-info" style="font-size:.78rem;color:#64748b;"></span>
      <div style="display:flex;align-items:center;gap:6px;">
        <button id="jobs-prev" class="btn btn-sm" style="background:#1e293b;border:1px solid #334155;color:#94a3b8;" onclick="jobsGoPage(jobsCurrentPage-1)" disabled>‹ Prev</button>
        <span id="jobs-pages" style="display:flex;gap:4px;"></span>
        <button id="jobs-next" class="btn btn-sm" style="background:#1e293b;border:1px solid #334155;color:#94a3b8;" onclick="jobsGoPage(jobsCurrentPage+1)" disabled>Next ›</button>
      </div>
    </div>
  </div>

  <!-- Crons panel -->
  <div class="panel" id="crons-panel">
    <div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:14px;">
      <h2 style="margin:0;">&#9200; Cron Jobs</h2>
      <button class="btn" onclick="openCronModal(null)"
        style="background:#4f46e5;border:1px solid #6366f1;color:#fff;padding:6px 14px;border-radius:6px;font-size:.82rem;cursor:pointer;">
        &#43; New Cron
      </button>
    </div>
    <div class="table-wrap">
      <table>
        <thead>
          <tr>
            <th>ID</th><th>Name</th><th>Queue</th><th>Job Type</th>
            <th>Schedule</th><th>Status</th><th>Last Run</th><th>Next Run</th>
            <th style="text-align:center">Runs</th><th>Actions</th>
          </tr>
        </thead>
        <tbody id="crons-tbody">
          <tr><td colspan="10" style="text-align:center;color:#64748b;padding:24px">Loading...</td></tr>
        </tbody>
      </table>
    </div>
  </div>

  <!-- Cron Create/Edit Modal -->
  <div id="cron-modal" style="display:none;position:fixed;inset:0;background:rgba(0,0,0,.65);z-index:1000;align-items:center;justify-content:center;">
    <div style="background:#1e293b;border:1px solid #334155;border-radius:12px;padding:28px 32px;width:520px;max-width:95vw;max-height:90vh;overflow-y:auto;position:relative;">
      <h3 id="cron-modal-title" style="margin:0 0 20px;color:#e2e8f0;font-size:1.1rem;">New Cron Job</h3>
      <form id="cron-form" onsubmit="submitCronForm(event)">
        <input type="hidden" id="cron-edit-id" value="">
        <div style="display:grid;grid-template-columns:1fr 1fr;gap:12px;">
          <div style="grid-column:1/-1;">
            <label style="font-size:.78rem;color:#94a3b8;display:block;margin-bottom:4px;">Name <span style="color:#64748b">(optional)</span></label>
            <input id="cf-name" type="text" placeholder="e.g. daily-report" style="width:100%;background:#0f172a;border:1px solid #334155;color:#e2e8f0;padding:7px 10px;border-radius:6px;font-size:.85rem;">
          </div>
          <div>
            <label style="font-size:.78rem;color:#94a3b8;display:block;margin-bottom:4px;">Queue <span style="color:#ef4444">*</span></label>
            <input id="cf-queue" type="text" placeholder="default" style="width:100%;background:#0f172a;border:1px solid #334155;color:#e2e8f0;padding:7px 10px;border-radius:6px;font-size:.85rem;">
          </div>
          <div>
            <label style="font-size:.78rem;color:#94a3b8;display:block;margin-bottom:4px;">Job Type <span style="color:#ef4444">*</span></label>
            <input id="cf-job-type" type="text" placeholder="send_report" style="width:100%;background:#0f172a;border:1px solid #334155;color:#e2e8f0;padding:7px 10px;border-radius:6px;font-size:.85rem;">
          </div>
          <div>
            <label style="font-size:.78rem;color:#94a3b8;display:block;margin-bottom:4px;">Every <span style="color:#64748b">(30s/5m/1h/1d)</span></label>
            <input id="cf-every" type="text" placeholder="5m" style="width:100%;background:#0f172a;border:1px solid #334155;color:#e2e8f0;padding:7px 10px;border-radius:6px;font-size:.85rem;">
          </div>
          <div>
            <label style="font-size:.78rem;color:#94a3b8;display:block;margin-bottom:4px;">Cron Expr <span style="color:#64748b">(* * * * *)</span></label>
            <input id="cf-expr" type="text" placeholder="0 9 * * 1-5" style="width:100%;background:#0f172a;border:1px solid #334155;color:#e2e8f0;padding:7px 10px;border-radius:6px;font-size:.85rem;">
          </div>
          <div>
            <label style="font-size:.78rem;color:#94a3b8;display:block;margin-bottom:4px;">Timezone</label>
            <input id="cf-timezone" type="text" placeholder="UTC" style="width:100%;background:#0f172a;border:1px solid #334155;color:#e2e8f0;padding:7px 10px;border-radius:6px;font-size:.85rem;">
          </div>
          <div>
            <label style="font-size:.78rem;color:#94a3b8;display:block;margin-bottom:4px;">Max Attempts</label>
            <input id="cf-max-attempts" type="number" value="3" min="1" style="width:100%;background:#0f172a;border:1px solid #334155;color:#e2e8f0;padding:7px 10px;border-radius:6px;font-size:.85rem;">
          </div>
          <div>
            <label style="font-size:.78rem;color:#94a3b8;display:block;margin-bottom:4px;">Max Runs <span style="color:#64748b">(0=unlimited)</span></label>
            <input id="cf-max-runs" type="number" value="0" min="0" style="width:100%;background:#0f172a;border:1px solid #334155;color:#e2e8f0;padding:7px 10px;border-radius:6px;font-size:.85rem;">
          </div>
          <div style="grid-column:1/-1;">
            <label style="font-size:.78rem;color:#94a3b8;display:block;margin-bottom:4px;">Payload (JSON)</label>
            <textarea id="cf-data" rows="3" placeholder='{"key":"value"}' style="width:100%;background:#0f172a;border:1px solid #334155;color:#e2e8f0;padding:7px 10px;border-radius:6px;font-size:.82rem;font-family:monospace;resize:vertical;"></textarea>
          </div>
          <div style="grid-column:1/-1;display:flex;gap:20px;flex-wrap:wrap;">
            <label style="display:flex;align-items:center;gap:6px;font-size:.82rem;color:#94a3b8;cursor:pointer;">
              <input type="checkbox" id="cf-without-overlapping" style="accent-color:#6366f1;">
              Without Overlapping
            </label>
            <label style="display:flex;align-items:center;gap:6px;font-size:.82rem;color:#94a3b8;cursor:pointer;">
              <input type="checkbox" id="cf-one-time" style="accent-color:#6366f1;">
              One-time
            </label>
          </div>
        </div>
        <div style="display:flex;gap:10px;margin-top:20px;justify-content:flex-end;">
          <button type="button" onclick="closeCronModal()"
            style="background:#1e293b;border:1px solid #334155;color:#94a3b8;padding:7px 18px;border-radius:6px;cursor:pointer;font-size:.85rem;">
            Cancel
          </button>
          <button type="submit" id="cron-submit-btn"
            style="background:#4f46e5;border:1px solid #6366f1;color:#fff;padding:7px 18px;border-radius:6px;cursor:pointer;font-size:.85rem;">
            Create
          </button>
        </div>
      </form>
    </div>
  </div>

  <!-- Cron Logs Modal -->
  <div id="cron-logs-modal" style="display:none;position:fixed;inset:0;background:rgba(0,0,0,.65);z-index:1000;align-items:center;justify-content:center;">
    <div style="background:#1e293b;border:1px solid #334155;border-radius:12px;padding:24px 28px;width:600px;max-width:95vw;max-height:80vh;overflow-y:auto;position:relative;">
      <div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:16px;">
        <h3 id="cron-logs-title" style="margin:0;color:#e2e8f0;font-size:1rem;">Run Logs</h3>
        <button onclick="closeCronLogsModal()" style="background:none;border:none;color:#64748b;font-size:1.4rem;cursor:pointer;line-height:1;">&times;</button>
      </div>
      <div class="table-wrap" style="max-height:55vh;overflow-y:auto;">
        <table>
          <thead>
            <tr><th>Log ID</th><th>Job ID</th><th>Fired At</th><th>Skipped</th><th>Reason</th></tr>
          </thead>
          <tbody id="cron-logs-tbody">
            <tr><td colspan="5" style="text-align:center;color:#64748b;padding:16px">Loading...</td></tr>
          </tbody>
        </table>
      </div>
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

let jobsCurrentPage = 1;
let jobsTotalPages  = 1;

function jobsGoPage(page) {
  if (page < 1 || page > jobsTotalPages) return;
  jobsCurrentPage = page;
  loadJobs();
}

async function loadJobs() {
  const queue   = document.getElementById('f-queue').value;
  const status  = document.getElementById('f-status').value;
  const tag     = document.getElementById('f-tag') ? document.getElementById('f-tag').value : '';
  const perPage = document.getElementById('f-per-page') ? document.getElementById('f-per-page').value : 20;
  let url = ` + "`" + `/api/jobs?page=${jobsCurrentPage}&per_page=${perPage}` + "`" + `;
  if (queue)  url += ` + "`" + `&queue=${queue}` + "`" + `;
  if (status) url += ` + "`" + `&status=${status}` + "`" + `;
  if (tag)    url += ` + "`" + `&tag=${encodeURIComponent(tag)}` + "`" + `;

  const res  = await fetch(url);
  const data = await res.json();
  const tbody = document.getElementById('jobs-tbody');

  // 兼容旧格式（数组）和新格式（{jobs, total, page, per_page, pages}）
  const jobs  = Array.isArray(data) ? data : (data.jobs || []);
  const total = Array.isArray(data) ? jobs.length : (data.total || 0);
  const pages = Array.isArray(data) ? 1 : (data.pages || 1);
  jobsTotalPages = pages;

  if (!jobs || jobs.length === 0) {
    tbody.innerHTML = '<tr><td colspan="9" style="text-align:center;color:#64748b;padding:24px">No jobs found</td></tr>';
  } else {
    tbody.innerHTML = jobs.map(j => {
      const payload = (() => { try { return JSON.parse(j.payload); } catch { return {}; } })();
      const jt = payload.job_type || payload.type || j.queue;
      const payloadShort = j.payload.length > 60 ? j.payload.slice(0, 60) + '…' : j.payload;
      const tagsHtml = j.tags && j.tags.length
        ? j.tags.map(t => ` + "`" + `<span style="background:#1e3a5f;color:#93c5fd;padding:1px 6px;border-radius:4px;font-size:.72rem;cursor:pointer" onclick="document.getElementById('f-tag').value='${escHtml(t)}';jobsGoPage(1)">${escHtml(t)}</span>` + "`" + `).join('')
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

  // 更新分页信息
  const perPageNum = parseInt(perPage);
  const from = total === 0 ? 0 : (jobsCurrentPage - 1) * perPageNum + 1;
  const to   = Math.min(jobsCurrentPage * perPageNum, total);
  const infoEl = document.getElementById('jobs-info');
  if (infoEl) infoEl.textContent = total === 0 ? 'No jobs' : ` + "`" + `${from}–${to} of ${total} jobs` + "`" + `;

  // 上一页 / 下一页按钮
  const prevBtn = document.getElementById('jobs-prev');
  const nextBtn = document.getElementById('jobs-next');
  if (prevBtn) prevBtn.disabled = jobsCurrentPage <= 1;
  if (nextBtn) nextBtn.disabled = jobsCurrentPage >= jobsTotalPages;

  // 页码按钮（最多显示 7 个，超出用省略号）
  const pagesEl = document.getElementById('jobs-pages');
  if (pagesEl) {
    const cur = jobsCurrentPage, tot = jobsTotalPages;
    let nums = [];
    if (tot <= 7) {
      nums = Array.from({length: tot}, (_, i) => i + 1);
    } else {
      nums = [1];
      if (cur > 3) nums.push('…');
      for (let i = Math.max(2, cur-1); i <= Math.min(tot-1, cur+1); i++) nums.push(i);
      if (cur < tot - 2) nums.push('…');
      nums.push(tot);
    }
    pagesEl.innerHTML = nums.map(n => {
      if (n === '…') return '<span style="color:#475569;padding:0 4px">…</span>';
      const active = n === cur;
      return ` + "`" + `<button onclick="jobsGoPage(${n})" style="min-width:30px;padding:4px 8px;border-radius:5px;border:1px solid ${active?'#6366f1':'#334155'};background:${active?'#4f46e5':'#1e293b'};color:${active?'#fff':'#94a3b8'};cursor:pointer;font-size:.78rem;">${n}</button>` + "`" + `;
    }).join('');
  }
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


// ─── Cron Jobs ────────────────────────────────────────────────────────────────

async function loadCrons() {
  const res = await fetch('/api/crons');
  if (!res.ok) return;
  const crons = await res.json();
  const tbody = document.getElementById('crons-tbody');
  if (!crons || crons.length === 0) {
    tbody.innerHTML = '<tr><td colspan="10" style="text-align:center;color:#64748b;padding:24px">No cron jobs found. Click "+ New Cron" to create one.</td></tr>';
    return;
  }
  tbody.innerHTML = crons.map(c => {
    const schedule = c.expr ? ` + "`" + `<code style="color:#a78bfa">${escHtml(c.expr)}</code>` + "`" + `
                            : ` + "`" + `<span style="color:#fbbf24">${escHtml(c.every)}</span>` + "`" + `;
    const enabled = c.enabled;
    const toggleLabel = enabled ? 'Disable' : 'Enable';
    const toggleStyle = enabled
      ? 'background:#064e3b;border:1px solid #059669;color:#34d399;'
      : 'background:#1e293b;border:1px solid #334155;color:#64748b;';
    const statusBadgeHtml = enabled
      ? '<span class="badge" style="background:#064e3b;color:#34d399;border:1px solid #059669;">&#9679; Active</span>'
      : '<span class="badge" style="background:#1e293b;color:#64748b;border:1px solid #334155;">&#9679; Disabled</span>';
    const lastRun = c.last_run_at ? fmtTime(c.last_run_at) : '<span style="color:#475569">—</span>';
    const nextRun = c.enabled && c.next_run_at
      ? ` + "`" + `<span style="color:#60a5fa">${fmtTime(c.next_run_at)}</span>` + "`" + `
      : '<span style="color:#475569">—</span>';
    const nameHtml = c.name
      ? ` + "`" + `<span style="color:#e2e8f0">${escHtml(c.name)}</span>` + "`" + `
      : ` + "`" + `<span style="color:#475569;font-style:italic">—</span>` + "`" + `;
    const flags = [];
    if (c.without_overlapping) flags.push('<span style="font-size:.68rem;background:#1e3a5f;color:#93c5fd;padding:1px 5px;border-radius:4px;border:1px solid #3b82f6;">no-overlap</span>');
    if (c.one_time)            flags.push('<span style="font-size:.68rem;background:#3b1f5e;color:#c4b5fd;padding:1px 5px;border-radius:4px;border:1px solid #7c3aed;">one-time</span>');
    const flagsHtml = flags.length ? ' ' + flags.join(' ') : '';
    return ` + "`" + `<tr>
      <td style="color:#a78bfa;font-weight:600">#${c.id}</td>
      <td>${nameHtml}${flagsHtml}</td>
      <td><code style="color:#fbbf24">${escHtml(c.queue)}</code></td>
      <td><code style="color:#93c5fd">${escHtml(c.job_type)}</code></td>
      <td>${schedule}</td>
      <td>${statusBadgeHtml}</td>
      <td class="ts">${lastRun}</td>
      <td class="ts">${nextRun}</td>
      <td style="text-align:center;color:#94a3b8">${c.run_count}${c.max_runs > 0 ? ' / ' + c.max_runs : ''}</td>
      <td style="white-space:nowrap;">
        <button onclick="toggleCron(${c.id},${!enabled},this)"
          style="${toggleStyle}padding:3px 9px;border-radius:5px;font-size:.75rem;cursor:pointer;margin-right:4px;">
          ${toggleLabel}
        </button>
        <button onclick="triggerCron(${c.id},this)"
          style="background:#1c3a2e;border:1px solid #059669;color:#34d399;padding:3px 9px;border-radius:5px;font-size:.75rem;cursor:pointer;margin-right:4px;"
          title="Trigger now">
          &#9654; Run
        </button>
        <button onclick="openCronLogs(${c.id},'${escHtml(c.name || '#' + c.id)}')"
          style="background:#1e293b;border:1px solid #334155;color:#94a3b8;padding:3px 9px;border-radius:5px;font-size:.75rem;cursor:pointer;margin-right:4px;"
          title="View run logs">
          &#128196; Logs
        </button>
        <button onclick="openCronModal(${c.id})"
          style="background:#1e293b;border:1px solid #334155;color:#94a3b8;padding:3px 9px;border-radius:5px;font-size:.75rem;cursor:pointer;margin-right:4px;"
          title="Edit">
          &#9998;
        </button>
        <button onclick="deleteCron(${c.id},this)"
          style="background:#2d1515;border:1px solid #7f1d1d;color:#f87171;padding:3px 9px;border-radius:5px;font-size:.75rem;cursor:pointer;"
          title="Delete">
          &#128465;
        </button>
      </td>
    </tr>` + "`" + `;
  }).join('');
}

async function toggleCron(id, enable, btn) {
  btn.disabled = true;
  const res = await fetch(` + "`" + `/api/crons/${id}` + "`" + `, {
    method: 'PATCH',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({enabled: enable})
  });
  if (res.ok) {
    toast(enable ? '✅ Cron enabled' : '⏸ Cron disabled');
    loadCrons();
  } else {
    const d = await res.json();
    toast('❌ ' + (d.error || 'Failed'), 'err');
    btn.disabled = false;
  }
}

async function triggerCron(id, btn) {
  btn.disabled = true;
  btn.textContent = '…';
  const res = await fetch(` + "`" + `/api/crons/${id}/trigger` + "`" + `, {method: 'POST'});
  const d = await res.json();
  if (res.ok) {
    toast(` + "`" + `✅ Triggered → Job #${d.job_id}` + "`" + `);
    loadCrons();
  } else {
    toast('❌ ' + (d.error || 'Failed'), 'err');
  }
  btn.disabled = false;
  btn.innerHTML = '&#9654; Run';
}

async function deleteCron(id, btn) {
  if (!confirm(` + "`" + `Delete Cron #${id}? This will also remove all run logs.` + "`" + `)) return;
  btn.disabled = true;
  const res = await fetch(` + "`" + `/api/crons/${id}` + "`" + `, {method: 'DELETE'});
  if (res.ok) {
    toast('🗑 Cron deleted');
    loadCrons();
  } else {
    const d = await res.json();
    toast('❌ ' + (d.error || 'Failed'), 'err');
    btn.disabled = false;
  }
}

// ── Cron Create/Edit Modal ────────────────────────────────────────────────────

async function openCronModal(id) {
  document.getElementById('cron-edit-id').value = id || '';
  document.getElementById('cron-modal-title').textContent = id ? ` + "`" + `Edit Cron #${id}` + "`" + ` : 'New Cron Job';
  document.getElementById('cron-submit-btn').textContent = id ? 'Save' : 'Create';

  // 重置表单
  ['cf-name','cf-queue','cf-job-type','cf-every','cf-expr','cf-timezone','cf-data'].forEach(fid => {
    document.getElementById(fid).value = '';
  });
  document.getElementById('cf-max-attempts').value = '3';
  document.getElementById('cf-max-runs').value = '0';
  document.getElementById('cf-without-overlapping').checked = false;
  document.getElementById('cf-one-time').checked = false;

  // 如果是编辑，加载现有数据
  if (id) {
    const res = await fetch(` + "`" + `/api/crons/${id}` + "`" + `);
    if (res.ok) {
      const c = await res.json();
      document.getElementById('cf-name').value = c.name || '';
      document.getElementById('cf-queue').value = c.queue || '';
      document.getElementById('cf-job-type').value = c.job_type || '';
      document.getElementById('cf-every').value = c.every || '';
      document.getElementById('cf-expr').value = c.expr || '';
      document.getElementById('cf-timezone').value = c.timezone || '';
      document.getElementById('cf-max-attempts').value = c.max_attempts || 3;
      document.getElementById('cf-max-runs').value = c.max_runs || 0;
      document.getElementById('cf-data').value = c.data && c.data !== 'null' ? c.data : '';
      document.getElementById('cf-without-overlapping').checked = !!c.without_overlapping;
      document.getElementById('cf-one-time').checked = !!c.one_time;
    }
  }

  const modal = document.getElementById('cron-modal');
  modal.style.display = 'flex';
}

function closeCronModal() {
  document.getElementById('cron-modal').style.display = 'none';
}

async function submitCronForm(e) {
  e.preventDefault();
  const id = document.getElementById('cron-edit-id').value;
  const btn = document.getElementById('cron-submit-btn');
  btn.disabled = true;
  btn.textContent = '…';

  const queue    = document.getElementById('cf-queue').value.trim();
  const job_type = document.getElementById('cf-job-type').value.trim();
  const every    = document.getElementById('cf-every').value.trim();
  const expr     = document.getElementById('cf-expr').value.trim();

  if (!queue)    { toast('❌ Queue is required', 'err'); btn.disabled=false; btn.textContent=id?'Save':'Create'; return; }
  if (!job_type) { toast('❌ Job Type is required', 'err'); btn.disabled=false; btn.textContent=id?'Save':'Create'; return; }
  if (!every && !expr) { toast('❌ Either Every or Cron Expr is required', 'err'); btn.disabled=false; btn.textContent=id?'Save':'Create'; return; }

  let dataVal = document.getElementById('cf-data').value.trim();
  let dataJson = null;
  if (dataVal) {
    try { dataJson = JSON.parse(dataVal); } catch { toast('❌ Payload is not valid JSON', 'err'); btn.disabled=false; btn.textContent=id?'Save':'Create'; return; }
  }

  const body = {
    queue, job_type, every, expr,
    timezone:            document.getElementById('cf-timezone').value.trim() || 'UTC',
    name:                document.getElementById('cf-name').value.trim(),
    max_attempts:        parseInt(document.getElementById('cf-max-attempts').value) || 3,
    max_runs:            parseInt(document.getElementById('cf-max-runs').value) || 0,
    without_overlapping: document.getElementById('cf-without-overlapping').checked,
    one_time:            document.getElementById('cf-one-time').checked,
  };
  if (dataJson !== null) body.data = dataJson;

  const url    = id ? ` + "`" + `/api/crons/${id}` + "`" + ` : '/api/crons';
  const method = id ? 'PUT' : 'POST';
  const res = await fetch(url, {
    method,
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify(body)
  });
  const d = await res.json();
  if (res.ok) {
    toast(id ? '✅ Cron updated' : ` + "`" + `✅ Cron #${d.id} created` + "`" + `);
    closeCronModal();
    loadCrons();
  } else {
    toast('❌ ' + (d.error || 'Failed'), 'err');
  }
  btn.disabled = false;
  btn.textContent = id ? 'Save' : 'Create';
}

// ── Cron Run Logs Modal ───────────────────────────────────────────────────────

async function openCronLogs(id, name) {
  document.getElementById('cron-logs-title').textContent = ` + "`" + `Run Logs — ${name}` + "`" + `;
  document.getElementById('cron-logs-modal').style.display = 'flex';
  document.getElementById('cron-logs-tbody').innerHTML =
    '<tr><td colspan="5" style="text-align:center;color:#64748b;padding:16px">Loading...</td></tr>';

  const res = await fetch(` + "`" + `/api/crons/${id}/logs?limit=50` + "`" + `);
  if (!res.ok) {
    document.getElementById('cron-logs-tbody').innerHTML =
      '<tr><td colspan="5" style="text-align:center;color:#ef4444;padding:16px">Failed to load logs</td></tr>';
    return;
  }
  const logs = await res.json();
  if (!logs || logs.length === 0) {
    document.getElementById('cron-logs-tbody').innerHTML =
      '<tr><td colspan="5" style="text-align:center;color:#64748b;padding:16px">No run logs yet</td></tr>';
    return;
  }
  document.getElementById('cron-logs-tbody').innerHTML = logs.map(l => {
    const skippedBadge = l.skipped
      ? '<span style="color:#f59e0b;font-size:.75rem;">&#9888; Skipped</span>'
      : '<span style="color:#34d399;font-size:.75rem;">&#10003; Fired</span>';
    const jobLink = l.job_id
      ? ` + "`" + `<span style="color:#a78bfa">#${l.job_id}</span>` + "`" + `
      : '<span style="color:#475569">—</span>';
    const reason = l.skip_reason
      ? ` + "`" + `<span style="color:#64748b;font-size:.75rem">${escHtml(l.skip_reason)}</span>` + "`" + `
      : '<span style="color:#475569">—</span>';
    return ` + "`" + `<tr>
      <td style="color:#64748b">${l.id}</td>
      <td>${jobLink}</td>
      <td class="ts">${fmtTime(l.fired_at)}</td>
      <td>${skippedBadge}</td>
      <td>${reason}</td>
    </tr>` + "`" + `;
  }).join('');
}

function closeCronLogsModal() {
  document.getElementById('cron-logs-modal').style.display = 'none';
}

// 关闭 modal：点击背景
document.getElementById('cron-modal').addEventListener('click', function(e) {
  if (e.target === this) closeCronModal();
});
document.getElementById('cron-logs-modal').addEventListener('click', function(e) {
  if (e.target === this) closeCronLogsModal();
});


async function loadQueuesForFilter() {
  try {
    const res = await fetch('/api/queues');
    const queues = await res.json();
    const sel = document.getElementById('f-queue');
    const cur = sel.value;
    sel.innerHTML = '<option value="">All</option>' +
      queues.map(q => q.name).sort().map(n =>
        ` + "`" + `<option value="${n}"${n === cur ? ' selected' : ''}>${n}</option>` + "`" + `
      ).join('');
  } catch(e) { /* 保持现有选项 */ }
}

function loadAll() { loadStats(); loadJobs(); loadCrons(); }

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

loadQueuesForFilter();
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
