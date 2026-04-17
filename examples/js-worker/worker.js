/**
 * GoQueue JavaScript Worker
 *
 * 适用环境：Node.js（需安装 ws 包）
 * 安装依赖：npm install ws
 * 启动方式：node worker.js
 *
 * 环境变量：
 *   GOQUEUE_SERVER   WebSocket 服务端地址，默认 ws://localhost:8080/ws/worker
 *   GOQUEUE_QUEUE    监听的队列名，默认 default
 *   GOQUEUE_API_KEY  API Key（对应服务端 API_KEY 环境变量）
 *
 * 新特性（v4）：
 *   - tags：任务可携带标签，handler 第二参数接收 tags 数组
 *   - batch catch/finally：批次失败/完成回调
 *   - queue pause/resume：队列可暂停，暂停期间任务不派发
 */

'use strict';

const WebSocket = require('ws');

// ─── 配置 ────────────────────────────────────────────────────────────────────
const SERVER_URL      = process.env.GOQUEUE_SERVER  || 'ws://localhost:8080/ws/worker';
const QUEUE           = process.env.GOQUEUE_QUEUE   || 'default';
const API_KEY         = process.env.GOQUEUE_API_KEY || '';
const CONCURRENCY     = parseInt(process.env.GOQUEUE_CONCURRENCY || '4', 10);
const PING_INTERVAL   = 20_000;   // 心跳间隔（ms）
const RECONNECT_DELAY = 3_000;    // 断线重连间隔（ms）

// ─── Job Handlers ─────────────────────────────────────────────────────────────
// handler 签名：async (data, tags) => string
// data: 解析后的 payload 对象
// tags: 任务标签数组（v4 新增），例如 ["urgent", "notify"]

const handlers = {

  send_email: async (data, tags) => {
    console.log(`[send_email] to=${data.to} subject=${data.subject} tags=${JSON.stringify(tags)}`);
    await sleep(300);
    return `Email sent to ${data.to}`;
  },

  generate_report: async (data, tags) => {
    console.log(`[generate_report] name=${data.name} tags=${JSON.stringify(tags)}`);
    await sleep(800);
    return `Report "${data.name}" generated`;
  },

  resize_image: async (data, tags) => {
    console.log(`[resize_image] url=${data.url} size=${data.width}x${data.height} tags=${JSON.stringify(tags)}`);
    await sleep(500);
    return `Image ${data.url} resized to ${data.width}x${data.height}`;
  },

  data_sync: async (data, tags) => {
    console.log(`[data_sync] ${data.source} → ${data.target} tags=${JSON.stringify(tags)}`);
    await sleep(600);
    return `Synced ${data.source} → ${data.target}`;
  },

  // v4: 按 tags 路由处理示例
  tag_task: async (data, tags) => {
    console.log(`[tag_task] message=${data.message} tags=${JSON.stringify(tags)}`);
    if (tags.includes('dry-run')) {
      console.log('[tag_task] dry-run 模式，跳过实际操作');
      return 'dry-run: skipped';
    }
    if (tags.includes('urgent')) {
      console.log('[tag_task] 紧急任务，优先处理');
    }
    await sleep(200);
    return `tag_task done: ${data.message} (tags=${JSON.stringify(tags)})`;
  },

  // v4: batch 回调示例（then_job / catch_job / finally_job 均可复用）
  batch_callback: async (data, tags) => {
    console.log(`[batch_callback] batch_id=${data.batch_id} status=${data.status} tags=${JSON.stringify(tags)}`);
    // 在这里可以发送通知、更新数据库等
    return `batch ${data.batch_id} callback handled`;
  },

  on_success: async (data, tags) => {
    console.log(`[on_success] batch_id=${data.batch_id} tags=${JSON.stringify(tags)}`);
    return `batch ${data.batch_id} succeeded`;
  },

  on_failure: async (data, tags) => {
    console.log(`[on_failure] batch_id=${data.batch_id} tags=${JSON.stringify(tags)}`);
    return `batch ${data.batch_id} failed`;
  },

  on_finally: async (data, tags) => {
    console.log(`[on_finally] batch_id=${data.batch_id} tags=${JSON.stringify(tags)}`);
    return `batch ${data.batch_id} finished`;
  },
};

// ─── Worker 核心 ──────────────────────────────────────────────────────────────
let ws        = null;
let pingTimer = null;
let stopped   = false;
let running   = 0;   // 当前并发任务数

function connect() {
  if (stopped) return;

  const url = `${SERVER_URL}?queue=${QUEUE}`;
  const wsOptions = API_KEY ? { headers: { Authorization: `Bearer ${API_KEY}` } } : {};

  console.log(`[Worker] Connecting to ${url} ...`);
  ws = new WebSocket(url, wsOptions);

  ws.on('open', () => {
    console.log('[Worker] Connected ✓');
    pingTimer = setInterval(() => {
      if (ws.readyState === WebSocket.OPEN) ws.ping();
    }, PING_INTERVAL);
  });

  ws.on('message', (raw) => {
    let msg;
    try { msg = JSON.parse(raw); } catch { return; }

    switch (msg.type) {
      case 'connected':
        console.log(`[Worker] Server: ${msg.message}`);
        break;
      case 'job':
        if (running >= CONCURRENCY) {
          // 超出并发限制，拒绝（服务端会重新入队）
          sendResult(msg.job_id, false, null, 'worker busy');
          return;
        }
        running++;
        handleJob(msg).finally(() => running--);
        break;
      case 'ack':
        console.log(`[Worker] ACK: ${msg.message}`);
        break;
      default:
        console.log(`[Worker] Unknown type: ${msg.type}`);
    }
  });

  ws.on('close', () => {
    clearPing();
    if (!stopped) {
      console.log(`[Worker] Disconnected, reconnecting in ${RECONNECT_DELAY}ms...`);
      setTimeout(connect, RECONNECT_DELAY + Math.random() * 1000);
    }
  });

  ws.on('error', (err) => {
    console.error(`[Worker] WS error: ${err.message}`);
  });
}

async function handleJob(msg) {
  const { job_id, job_type, queue, payload, tags = [] } = msg;
  console.log(`[Job #${job_id}] type=${job_type} queue=${queue} tags=${JSON.stringify(tags)}`);

  let data;
  try { data = JSON.parse(payload); } catch (e) {
    sendResult(job_id, false, null, `payload parse error: ${e.message}`);
    return;
  }

  const handler = handlers[job_type];
  if (!handler) {
    console.warn(`[Job #${job_id}] No handler for job_type=${job_type}`);
    sendResult(job_id, false, null, `no handler for job_type: ${job_type}`);
    return;
  }

  try {
    const result = await handler(data, tags);
    console.log(`[Job #${job_id}] OK: ${result}`);
    sendResult(job_id, true, result, null);
  } catch (err) {
    console.error(`[Job #${job_id}] FAILED: ${err.message}`);
    sendResult(job_id, false, null, err.message);
  }
}

function sendResult(jobId, success, log, error) {
  if (!ws || ws.readyState !== WebSocket.OPEN) return;
  const msg = { type: 'result', job_id: jobId, success };
  if (success) msg.log   = log;
  else         msg.error = error;
  ws.send(JSON.stringify(msg));
}

function clearPing() {
  if (pingTimer) { clearInterval(pingTimer); pingTimer = null; }
}

function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }

// ─── 优雅退出 ─────────────────────────────────────────────────────────────────
process.on('SIGINT',  () => { stopped = true; clearPing(); ws && ws.close(); process.exit(0); });
process.on('SIGTERM', () => { stopped = true; clearPing(); ws && ws.close(); process.exit(0); });

// ─── 启动 ─────────────────────────────────────────────────────────────────────
console.log(`[Worker] Queue=${QUEUE} Concurrency=${CONCURRENCY} Handlers=${Object.keys(handlers).join(', ')}`);
connect();
