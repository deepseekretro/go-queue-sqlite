#!/usr/bin/env python3
"""
GoQueue Python Worker 示例
依赖：pip install websocket-client
启动：python worker.py

环境变量：
  GOQUEUE_SERVER   WebSocket 服务端地址，默认 ws://localhost:8080/ws/worker
  GOQUEUE_QUEUE    监听的队列名，默认 default
  GOQUEUE_API_KEY  API Key（对应服务端 API_KEY 环境变量）
"""

import json
import logging
import os
import threading
import time
from typing import Any, Callable, Dict, Optional

import websocket

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(message)s",
)
log = logging.getLogger("goqueue-worker")

# ─── 配置 ─────────────────────────────────────────────────────────────────────
SERVER_URL    = os.getenv("GOQUEUE_SERVER",  "ws://localhost:8080/ws/worker")
QUEUE         = os.getenv("GOQUEUE_QUEUE",   "default")
API_KEY       = os.getenv("GOQUEUE_API_KEY", "")
PING_INTERVAL = 20   # 心跳间隔（秒）
RECONNECT_DELAY = 3  # 断线重连间隔（秒）


# ─── GoQueueWorker ────────────────────────────────────────────────────────────

class GoQueueWorker:
    """
    GoQueue WebSocket Worker

    用法：
        worker = GoQueueWorker()
        worker.register("send_email", handle_send_email)
        worker.run()          # 阻塞；或 worker.start() 后台线程
    """

    def __init__(
        self,
        server_url: str = SERVER_URL,
        queue: str = QUEUE,
        api_key: str = API_KEY,
        ping_interval: int = PING_INTERVAL,
        reconnect_delay: int = RECONNECT_DELAY,
    ):
        self.server_url     = server_url
        self.queue          = queue
        self.api_key        = api_key
        self.ping_interval  = ping_interval
        self.reconnect_delay = reconnect_delay
        self._handlers: Dict[str, Callable] = {}
        self._ws: Optional[websocket.WebSocketApp] = None
        self._stop_event = threading.Event()

    def register(self, job_type: str, handler: Callable[[Dict[str, Any]], str]) -> None:
        """
        注册任务处理器。

        handler 签名：def handler(data: dict) -> str
          - data：任务的 data 字段（已解析为 dict）
          - 返回值：成功日志字符串
          - 抛出异常：任务标记为失败，异常信息作为 error
        """
        self._handlers[job_type] = handler
        log.info(f"Registered handler: {job_type}")

    def run(self) -> None:
        """阻塞运行，自动重连。"""
        while not self._stop_event.is_set():
            self._connect()
            if not self._stop_event.is_set():
                log.info(f"Reconnecting in {self.reconnect_delay}s...")
                time.sleep(self.reconnect_delay)

    def start(self) -> threading.Thread:
        """在后台线程中运行，返回线程对象。"""
        t = threading.Thread(target=self.run, daemon=True)
        t.start()
        return t

    def stop(self) -> None:
        """停止 Worker。"""
        self._stop_event.set()
        if self._ws:
            self._ws.close()

    # ── 内部方法 ──────────────────────────────────────────────────────────────

    def _connect(self) -> None:
        url = f"{self.server_url}?queue={self.queue}"
        headers = {}
        if self.api_key:
            headers["X-API-Key"] = self.api_key

        log.info(f"Connecting to {url}")
        self._ws = websocket.WebSocketApp(
            url,
            header=headers,
            on_open=self._on_open,
            on_message=self._on_message,
            on_error=self._on_error,
            on_close=self._on_close,
        )
        self._ws.run_forever()

    def _on_open(self, ws) -> None:
        log.info(f"Connected, queue={self.queue}")
        # 启动心跳线程：每 ping_interval 秒发送一次 JSON ping
        t = threading.Thread(target=self._heartbeat_loop, args=(ws,), daemon=True)
        t.start()

    def _heartbeat_loop(self, ws) -> None:
        """心跳线程：每 ping_interval 秒发送 {"type":"ping"}"""
        while not self._stop_event.is_set():
            time.sleep(self.ping_interval)
            try:
                if ws.sock and ws.sock.connected:
                    ws.send(json.dumps({"type": "ping"}))
                    log.debug("Sent ping")
                else:
                    break
            except Exception as e:
                log.warning(f"Ping failed: {e}")
                break

    def _on_message(self, ws, raw: str) -> None:
        try:
            msg = json.loads(raw)
        except json.JSONDecodeError:
            log.warning(f"Invalid JSON: {raw}")
            return

        msg_type = msg.get("type")

        if msg_type == "connected":
            log.info(f"Server: {msg.get('message')}")
        elif msg_type == "pong":
            log.debug("Received pong")   # 心跳响应，静默处理
        elif msg_type == "ack":
            log.info(f"ACK: {msg.get('message')}")
        elif msg_type == "job":
            self._handle_job(ws, msg)
        else:
            log.debug(f"Unknown message type: {msg_type}")

    def _handle_job(self, ws, msg: dict) -> None:
        job_id   = msg["job_id"]
        job_type = msg["job_type"]

        try:
            payload = json.loads(msg["payload"])
            data    = payload.get("data", {})
        except (json.JSONDecodeError, KeyError) as e:
            self._send_result(ws, job_id, success=False, error=f"Invalid payload: {e}")
            return

        log.info(f"Job #{job_id} type={job_type} queue={msg.get('queue')}")

        handler = self._handlers.get(job_type)
        if not handler:
            self._send_result(ws, job_id, success=False,
                              error=f"No handler for job_type: {job_type!r}")
            return

        try:
            result_log = handler(data)
            log.info(f"Job #{job_id} done: {result_log}")
            self._send_result(ws, job_id, success=True, log=result_log or "done")
        except Exception as e:
            log.error(f"Job #{job_id} failed: {e}")
            self._send_result(ws, job_id, success=False, error=str(e))

    def _send_result(self, ws, job_id: int, *, success: bool,
                     log: str = "", error: str = "") -> None:
        msg: Dict[str, Any] = {"type": "result", "job_id": job_id, "success": success}
        if success:
            msg["log"]   = log
        else:
            msg["error"] = error
        ws.send(json.dumps(msg))

    def _on_error(self, ws, error) -> None:
        log.error(f"WebSocket error: {error}")

    def _on_close(self, ws, close_status_code, close_msg) -> None:
        log.info(f"Disconnected (code={close_status_code}, msg={close_msg})")


# ─── Job Handlers ─────────────────────────────────────────────────────────────

def handle_send_email(data: dict) -> str:
    to      = data.get("to", "unknown")
    subject = data.get("subject", "")
    log.info(f"Sending email to {to}: {subject}")
    time.sleep(0.3)   # 模拟耗时
    return f"Email sent to {to}"


def handle_generate_report(data: dict) -> str:
    name = data.get("name", "unknown")
    log.info(f"Generating report: {name}")
    time.sleep(0.8)
    return f"Report \"{name}\" generated"


def handle_resize_image(data: dict) -> str:
    url = data.get("url", "")
    w   = data.get("width",  800)
    h   = data.get("height", 600)
    log.info(f"Resizing {url} to {w}x{h}")
    time.sleep(0.5)
    return f"Image {url} resized to {w}x{h}"


def handle_data_sync(data: dict) -> str:
    src = data.get("source", "")
    dst = data.get("target", "")
    log.info(f"Syncing {src} → {dst}")
    time.sleep(0.6)
    return f"Synced {src} → {dst}"


# ─── 入口 ─────────────────────────────────────────────────────────────────────

if __name__ == "__main__":
    worker = GoQueueWorker()

    worker.register("send_email",      handle_send_email)
    worker.register("generate_report", handle_generate_report)
    worker.register("resize_image",    handle_resize_image)
    worker.register("data_sync",       handle_data_sync)

    log.info("GoQueue Python Worker starting... (Ctrl+C to stop)")
    try:
        worker.run()
    except KeyboardInterrupt:
        worker.stop()
        log.info("Worker stopped.")
