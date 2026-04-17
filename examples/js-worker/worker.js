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
 */

'use strict';

const WebSocket = require('ws');

// ─── 配置 ────────────────────────────────────────────────────────────────────
const SERVER_URL  = process.env.GOQUEUE_SERVER  || 'ws://localhost:8080/ws/worker';
const QUEUE       = process.env.GOQUEUE_QUEUE   || 'default';
const API_KEY     = process.env.GOQUEUE_API_KEY || '';
const PING_INTERVAL   = 20_000;   // 心跳间隔（ms）
const RECONNECT_DELAY = 3_000;    // 断线重连间隔（ms）

// ─── Job Handlers ─────────────────────────────────────────────────────────────
// 每个 handler 接收 data（已解析的 JSON 对象），返回日志字符串（或 Promise<string>）
// 抛出异常 → 任务标记为失败
const handlers = {
  send_email: async (data) => {
    console.log(`[send_email] to=${data.to} subject=${data.subject}`);
    await sleep(300);
    return `Email sent to ${data.to}`;
  },

  generate_report: async (data) => {
    console.log(`[generate_report] name=${data.name}`);
    await sleep(800);
    return `Report "${data.name}" generated`;
  },

  resize_image: async (data) => {
    console.log(`[resize_image] url=${data.url} size=${data.width}x${data.height}`);
    await sleep(500);
    return `Image ${data.url} resized to ${data.width}x${data.height}`;
  },

  data_sync: async (data) => {
    console.log(`[data_sync] ${data.source} → ${data.target}`);
    await sleep(600);
    return `Synced ${data.source} → ${data.target}`;
  },
};

// ─── Worker 核心 ──────────────────────────────────────────────────────────────
let ws        = null;
let pingTimer = null;
let stopped   = false;

function connect() {
  if (stopped) return;

  const url = `${SERVER_URL}?queue=${QUEUE}`;
  const wsOptions = API_KEY ? { headers: { 'X-API-Key': API_KEY } } : {};

  console.log(`[Worker] Connecting to ${url}`);
  ws = new WebSocket(url, wsOptions);

  // ── 连接成功 ────────────────────────────────────────────────────────────────
  ws.on('open', () => {
    console.log(`[Worker] Connected, queue=${QUEUE}`);

    // 心跳：每 20s 发一次 JSON ping，防止连接被中间代理超时断开
    pingTimer = setInterval(() => {
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: 'ping' }));
      }
    }, PING_INTERVAL);
  });

  // ── 接收消息 ────────────────────────────────────────────────────────────────
  ws.on('message', async (raw) => {
    let msg;
    try { msg = JSON.parse(raw); } catch { return; }

    switch (msg.type) {
      case 'connected':
        console.log(`[Worker] Server: ${msg.message}`);
        break;

      case 'pong':
        // 心跳响应，静默处理
        break;

      case 'ack':
        console.log(`[Worker] ACK: ${msg.message}`);
        break;

      case 'job':
        await handleJob(msg);
        break;

      default:
        console.log(`[Worker] Unknown message type: ${msg.type}`);
    }
  });

  // ── 连接关闭 ────────────────────────────────────────────────────────────────
  ws.on('close', () => {
    console.log('[Worker] Disconnected');
    clearPing();
    if (!stopped) {
      console.log(`[Worker] Reconnecting in ${RECONNECT_DELAY}ms...`);
      setTimeout(connect, RECONNECT_DELAY);
    }
  });

  ws.on('error', (err) => {
    console.error('[Worker] WebSocket error:', err.message);
  });
}

async function handleJob(msg) {
  const jobId   = msg.job_id;
  const jobType = msg.job_type;

  let payload, data;
  try {
    payload = JSON.parse(msg.payload);
    data    = payload.data || {};
  } catch (e) {
    return sendResult(jobId, false, '', `Invalid payload JSON: ${e.message}`);
  }

  console.log(`[Worker] Job #${jobId} type=${jobType} queue=${msg.queue}`);

  const handler = handlers[jobType];
  if (!handler) {
    return sendResult(jobId, false, '', `No handler for job_type: "${jobType}"`);
  }

  try {
    const log = await handler(data);
    console.log(`[Worker] Job #${jobId} done: ${log}`);
    sendResult(jobId, true, log, '');
  } catch (err) {
    console.error(`[Worker] Job #${jobId} failed:`, err.message);
    sendResult(jobId, false, '', err.message);
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
connect();
