#!/usr/bin/env python3
"""
examples/consumer.py — goapp WebSocket Worker 消费者示例（含派发延迟监控）

用法：
    python3 consumer.py [--queue QUEUE] [--workers N] [--duration SEC]

依赖：
    pip install websocket-client
"""

import argparse
import json
import random
import signal
import sys
import threading
import time

import websocket  # pip install websocket-client

HOST        = "localhost:8080"
WS_ENDPOINT = f"ws://{HOST}/ws/worker"

stop_event = threading.Event()

# ── 统计 ─────────────────────────────────────────────────
stats = {
    "connected"    : 0,
    "jobs_received": 0,
    "jobs_done"    : 0,
    "jobs_failed"  : 0,
    "errors"       : 0,
}
stats_lock = threading.Lock()

# 派发延迟记录：每个 worker 完成任务后到收到下一个任务的等待时间（ms）
idle_gaps = []          # list of float (ms)
idle_gaps_lock = threading.Lock()

def inc(key, n=1):
    with stats_lock:
        stats[key] += n


# ── 任务处理逻辑 ─────────────────────────────────────────
def process_job(job_id: int, job_type: str, payload: str) -> tuple:
    """模拟处理耗时 0.05~0.3s，返回 (success, log, error)"""
    try:
        data = json.loads(payload) if payload else {}
    except Exception:
        data = {}
    process_time = random.uniform(0.05, 0.3)
    time.sleep(process_time)
    log_msg = f"processed job_type={job_type} in {process_time:.3f}s"
    return True, log_msg, ""


# ── 单个 Worker ──────────────────────────────────────────
def run_worker(worker_id: int, queue: str):
    url = f"{WS_ENDPOINT}?queue={queue}"

    # 记录上一个任务完成的时刻（用于计算 idle gap）
    last_done_at = [None]   # list 作为可变容器

    def on_open(ws):
        inc("connected")
        print(f"[Worker-{worker_id:02d}] connected  queue={queue}", flush=True)

    def on_message(ws, message):
        try:
            msg = json.loads(message)
        except Exception as e:
            print(f"[Worker-{worker_id:02d}] invalid JSON: {e}", flush=True)
            return

        msg_type = msg.get("type", "")

        if msg_type == "connected":
            pass  # 握手完成，等待任务

        elif msg_type == "job":
            job_id_val = msg["job_id"]
            job_type   = msg.get("job_type", "")
            payload    = msg.get("payload", "")
            recv_at    = time.time()

            inc("jobs_received")

            # 计算 idle gap（上一个任务完成 → 收到本任务）
            if last_done_at[0] is not None:
                gap_ms = (recv_at - last_done_at[0]) * 1000
                with idle_gaps_lock:
                    idle_gaps.append(gap_ms)
                gap_tag = f"  [idle_gap={gap_ms:.1f}ms]"
            else:
                gap_tag = "  [first job]"

            print(f"[Worker-{worker_id:02d}] ← job #{job_id_val:>7}  type={job_type}{gap_tag}", flush=True)

            success, log, error = process_job(job_id_val, job_type, payload)

            result = json.dumps({
                "type"   : "result",
                "job_id" : job_id_val,
                "success": success,
                "log"    : log,
                "error"  : error,
            })
            ws.send(result)
            last_done_at[0] = time.time()

            if success:
                inc("jobs_done")
                print(f"[Worker-{worker_id:02d}] ✓ job #{job_id_val:>7} done", flush=True)
            else:
                inc("jobs_failed")
                print(f"[Worker-{worker_id:02d}] ✗ job #{job_id_val:>7} failed: {error}", flush=True)

        elif msg_type == "ack":
            pass  # 服务端确认

        elif msg_type == "ping":
            ws.send(json.dumps({"type": "pong"}))

    def on_error(ws, error):
        inc("errors")
        print(f"[Worker-{worker_id:02d}] error: {error}", flush=True)

    def on_close(ws, code, msg):
        inc("connected", -1)
        print(f"[Worker-{worker_id:02d}] disconnected (code={code})", flush=True)

    while not stop_event.is_set():
        ws = websocket.WebSocketApp(
            url,
            on_open=on_open,
            on_message=on_message,
            on_error=on_error,
            on_close=on_close,
        )
        ws.run_forever(ping_interval=0)
        if not stop_event.is_set():
            print(f"[Worker-{worker_id:02d}] reconnecting in 1s ...", flush=True)
            time.sleep(1)


# ── 统计打印线程 ─────────────────────────────────────────
def stats_printer(interval: float = 5.0):
    while not stop_event.is_set():
        time.sleep(interval)
        with stats_lock:
            s = dict(stats)
        with idle_gaps_lock:
            gaps = list(idle_gaps)

        if gaps:
            avg_gap  = sum(gaps) / len(gaps)
            min_gap  = min(gaps)
            max_gap  = max(gaps)
            p50      = sorted(gaps)[len(gaps) // 2]
            p95_idx  = int(len(gaps) * 0.95)
            p95      = sorted(gaps)[p95_idx]
            gap_info = (f"idle_gap: avg={avg_gap:.1f}ms  min={min_gap:.1f}ms  "
                        f"p50={p50:.1f}ms  p95={p95:.1f}ms  max={max_gap:.1f}ms  n={len(gaps)}")
        else:
            gap_info = "idle_gap: no data yet"

        print(
            f"\n[Stats] connected={s['connected']}  "
            f"received={s['jobs_received']}  "
            f"done={s['jobs_done']}  "
            f"failed={s['jobs_failed']}  "
            f"errors={s['errors']}",
            flush=True
        )
        print(f"[Stats] {gap_info}\n", flush=True)


# ── 主程序 ───────────────────────────────────────────────
def main():
    parser = argparse.ArgumentParser(description="goapp WebSocket Worker 消费者示例")
    parser.add_argument("--queue",    default="default", help="监听的队列名（默认: default）")
    parser.add_argument("--workers",  type=int, default=10, help="并发 worker 数量（默认: 10）")
    parser.add_argument("--duration", type=int, default=0,  help="运行时长（秒），0=永久（默认: 0）")
    args = parser.parse_args()

    def handle_signal(sig, frame):
        print("\n[Main] 收到停止信号，正在退出...", flush=True)
        stop_event.set()
    signal.signal(signal.SIGINT,  handle_signal)
    signal.signal(signal.SIGTERM, handle_signal)

    print(f"[Main] 启动 {args.workers} 个 worker，队列={args.queue}", flush=True)

    t_stats = threading.Thread(target=stats_printer, args=(5.0,), daemon=True)
    t_stats.start()

    threads = []
    for i in range(args.workers):
        t = threading.Thread(target=run_worker, args=(i, args.queue), daemon=True)
        t.start()
        threads.append(t)
        time.sleep(0.05)

    if args.duration > 0:
        print(f"[Main] 将在 {args.duration}s 后自动停止", flush=True)
        stop_event.wait(timeout=args.duration)
        stop_event.set()
    else:
        stop_event.wait()

    with stats_lock:
        s = dict(stats)
    with idle_gaps_lock:
        gaps = list(idle_gaps)

    print(f"\n[Main] 最终统计: received={s['jobs_received']}  done={s['jobs_done']}  "
          f"failed={s['jobs_failed']}  errors={s['errors']}", flush=True)
    if gaps:
        avg_gap = sum(gaps) / len(gaps)
        min_gap = min(gaps)
        max_gap = max(gaps)
        p50     = sorted(gaps)[len(gaps) // 2]
        p95     = sorted(gaps)[int(len(gaps) * 0.95)]
        print(f"[Main] idle_gap: avg={avg_gap:.1f}ms  min={min_gap:.1f}ms  "
              f"p50={p50:.1f}ms  p95={p95:.1f}ms  max={max_gap:.1f}ms  n={len(gaps)}", flush=True)
    print("[Main] 已退出", flush=True)


if __name__ == "__main__":
    main()
