#!/usr/bin/env python3
"""
GoQueue Python Worker 示例
依赖：pip install websocket-client
启动：python worker.py

环境变量：
  GOQUEUE_SERVER        WebSocket 服务端地址，默认 ws://localhost:8080/ws/worker
  GOQUEUE_QUEUE         监听的队列名，默认 default
  GOQUEUE_API_KEY       API Key（对应服务端 API_KEY 环境变量）
  GOQUEUE_CONCURRENCY   最大并发任务数，默认 4

新特性（v4）：
  - tags：任务可携带标签，handler 第二参数接收 tags 列表
  - batch catch/finally：批次失败/完成回调
  - queue pause/resume：队列可暂停，暂停期间任务不派发
"""

import json
import logging
import os
import threading
import time
from typing import Any, Callable, Dict, List, Optional

import websocket

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(message)s",
)
log = logging.getLogger("goqueue-worker")

# ─── 配置 ─────────────────────────────────────────────────────────────────────
SERVER_URL    = os.getenv("GOQUEUE_SERVER",      "ws://localhost:8080/ws/worker")
QUEUE         = os.getenv("GOQUEUE_QUEUE",       "default")
API_KEY       = os.getenv("GOQUEUE_API_KEY",     "")
CONCURRENCY   = int(os.getenv("GOQUEUE_CONCURRENCY", "4"))
PING_INTERVAL = 20      # 心跳间隔（秒）
RECONNECT_DELAY = 3.0   # 断线重连间隔（秒）

# ─── Handler 类型 ──────────────────────────────────────────────────────────────
# handler 签名：(data: dict, tags: list[str]) -> str
# data: 解析后的 payload 字典
# tags: 任务标签列表（v4 新增），例如 ["urgent", "notify"]
HandlerFunc = Callable[[Dict[str, Any], List[str]], str]

# ─── GoQueueWorker ─────────────────────────────────────────────────────────────
class GoQueueWorker:
    def __init__(self):
        self._handlers: Dict[str, HandlerFunc] = {}
        self._ws: Optional[websocket.WebSocketApp] = None
        self._stopped = False
        self._sem = threading.Semaphore(CONCURRENCY)

    def register(self, job_type: str, handler: HandlerFunc):
        """注册 job_type 对应的处理函数"""
        self._handlers[job_type] = handler
        log.info(f"Registered handler: {job_type}")

    def run(self):
        """启动 Worker（阻塞，自动重连）"""
        while not self._stopped:
            self._connect()
            if not self._stopped:
                log.info(f"Reconnecting in {RECONNECT_DELAY}s...")
                time.sleep(RECONNECT_DELAY)

    def stop(self):
        self._stopped = True
        if self._ws:
            self._ws.close()

    def _connect(self):
        url = f"{SERVER_URL}?queue={QUEUE}"
        headers = {}
        if API_KEY:
            headers["Authorization"] = f"Bearer {API_KEY}"

        log.info(f"Connecting to {url} ...")

        self._ws = websocket.WebSocketApp(
            url,
            header=headers,
            on_open=self._on_open,
            on_message=self._on_message,
            on_error=self._on_error,
            on_close=self._on_close,
        )
        self._ws.run_forever(ping_interval=PING_INTERVAL, ping_timeout=10)

    def _on_open(self, ws):
        log.info("Connected ✓")

    def _on_message(self, ws, raw: str):
        try:
            msg = json.loads(raw)
        except json.JSONDecodeError:
            return

        msg_type = msg.get("type")
        if msg_type == "connected":
            log.info(f"Server: {msg.get('message')}")
        elif msg_type == "job":
            # 并发控制
            if not self._sem.acquire(blocking=False):
                log.warning(f"[Job #{msg.get('job_id')}] Worker busy, rejecting")
                self._send_result(ws, msg["job_id"], False, error="worker busy")
                return
            t = threading.Thread(target=self._handle_job, args=(ws, msg), daemon=True)
            t.start()
        elif msg_type == "ack":
            log.info(f"ACK: {msg.get('message')}")
        else:
            log.debug(f"Unknown message type: {msg_type}")

    def _on_error(self, ws, error):
        log.error(f"WS error: {error}")

    def _on_close(self, ws, code, reason):
        log.info(f"Disconnected (code={code} reason={reason})")

    def _handle_job(self, ws, msg: dict):
        try:
            job_id   = msg["job_id"]
            job_type = msg.get("job_type", "")
            queue    = msg.get("queue", "")
            payload  = msg.get("payload", "{}")
            tags     = msg.get("tags") or []   # v4: 任务标签

            log.info(f"[Job #{job_id}] type={job_type} queue={queue} tags={tags}")

            # 解析 payload
            try:
                data = json.loads(payload)
            except json.JSONDecodeError as e:
                self._send_result(ws, job_id, False, error=f"payload parse error: {e}")
                return

            # 查找 handler
            handler = self._handlers.get(job_type)
            if not handler:
                log.warning(f"[Job #{job_id}] No handler for job_type={job_type}")
                self._send_result(ws, job_id, False, error=f"no handler for job_type: {job_type}")
                return

            # 执行
            result = handler(data, tags)
            log.info(f"[Job #{job_id}] OK: {result}")
            self._send_result(ws, job_id, True, log_msg=result)

        except Exception as e:
            log.exception(f"[Job #{msg.get('job_id')}] FAILED: {e}")
            self._send_result(ws, msg.get("job_id"), False, error=str(e))
        finally:
            self._sem.release()

    def _send_result(self, ws, job_id: int, success: bool,
                     log_msg: str = "", error: str = ""):
        msg = {"type": "result", "job_id": job_id, "success": success}
        if success:
            msg["log"] = log_msg
        else:
            msg["error"] = error
        try:
            ws.send(json.dumps(msg))
        except Exception as e:
            log.error(f"Failed to send result: {e}")


# ─── 示例 Job Handlers ─────────────────────────────────────────────────────────

def handle_send_email(data: dict, tags: list) -> str:
    to      = data.get("to", "")
    subject = data.get("subject", "")
    log.info(f"[send_email] to={to} subject={subject} tags={tags}")
    time.sleep(0.3)
    return f"Email sent to {to}"


def handle_generate_report(data: dict, tags: list) -> str:
    name = data.get("name", "")
    log.info(f"[generate_report] name={name} tags={tags}")
    time.sleep(0.8)
    return f"Report '{name}' generated"


def handle_resize_image(data: dict, tags: list) -> str:
    url = data.get("url", "")
    w   = data.get("width", 0)
    h   = data.get("height", 0)
    log.info(f"[resize_image] url={url} size={w}x{h} tags={tags}")
    time.sleep(0.5)
    return f"Image {url} resized to {w}x{h}"


def handle_data_sync(data: dict, tags: list) -> str:
    src = data.get("source", "")
    dst = data.get("target", "")
    log.info(f"[data_sync] {src} → {dst} tags={tags}")
    time.sleep(0.6)
    return f"Synced {src} → {dst}"


def handle_tag_task(data: dict, tags: list) -> str:
    """v4: 演示如何根据 tags 做不同处理"""
    message = data.get("message", "")
    log.info(f"[tag_task] message={message} tags={tags}")

    if "dry-run" in tags:
        log.info("[tag_task] dry-run 模式，跳过实际操作")
        return "dry-run: skipped"
    if "urgent" in tags:
        log.info("[tag_task] 紧急任务，优先处理")
    if "notify" in tags:
        log.info("[tag_task] 需要发送通知")

    time.sleep(0.2)
    return f"tag_task done: {message} (tags={tags})"


def handle_batch_callback(data: dict, tags: list) -> str:
    """v4: 处理 batch 的 then/catch/finally 回调"""
    batch_id = data.get("batch_id", "")
    status   = data.get("status", "")
    log.info(f"[batch_callback] batch_id={batch_id} status={status} tags={tags}")
    # 在这里可以发送通知、更新数据库等
    return f"batch {batch_id} callback handled (status={status})"


# ─── 入口 ─────────────────────────────────────────────────────────────────────

if __name__ == "__main__":
    worker = GoQueueWorker()

    # 基础 handlers
    worker.register("send_email",      handle_send_email)
    worker.register("generate_report", handle_generate_report)
    worker.register("resize_image",    handle_resize_image)
    worker.register("data_sync",       handle_data_sync)

    # v4: tags 示例
    worker.register("tag_task",        handle_tag_task)

    # v4: batch 回调示例
    worker.register("batch_callback",  handle_batch_callback)
    worker.register("on_success",      handle_batch_callback)   # batch then_job
    worker.register("on_failure",      handle_batch_callback)   # batch catch_job
    worker.register("on_finally",      handle_batch_callback)   # batch finally_job

    log.info(f"GoQueue Python Worker starting... Queue={QUEUE} Concurrency={CONCURRENCY} (Ctrl+C to stop)")
    try:
        worker.run()
    except KeyboardInterrupt:
        worker.stop()
        log.info("Worker stopped.")
