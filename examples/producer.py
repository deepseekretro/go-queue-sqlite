#!/usr/bin/env python3
"""
examples/producer.py — goapp HTTP 生产者示例

向 goapp 的 /api/jobs 接口批量提交任务，支持并发发送、速率控制和统计输出。

用法：
    python3 producer.py [--queue QUEUE] [--jobs N] [--concurrency N]
                        [--rate N] [--job-type TYPE] [--delay SEC]
                        [--host HOST] [--interval SEC]

参数：
    --queue       目标队列名称（默认: default）
    --jobs        总共提交的任务数（默认: 100，0 表示无限循环）
    --concurrency 并发发送线程数（默认: 4）
    --rate        每秒最大提交速率，0 表示不限速（默认: 0）
    --job-type    任务类型字段（默认: test_job）
    --delay       任务延迟执行秒数（默认: 0，立即执行）
    --host        goapp 地址（默认: localhost:8080）
    --interval    每批次之间的间隔秒数（默认: 0）
    --priority    任务优先级 0~10（默认: 0）
    --verbose     打印每条任务的提交结果

依赖：
    pip install requests
"""

import argparse
import json
import queue
import random
import sys
import threading
import time
from datetime import datetime

try:
    import requests
except ImportError:
    print("[Error] 缺少依赖: pip install requests", file=sys.stderr)
    sys.exit(1)

# ── 全局统计 ──────────────────────────────────────────────
stats = {
    "submitted": 0,
    "success":   0,
    "failed":    0,
    "errors":    0,
}
stats_lock  = threading.Lock()
stop_event  = threading.Event()
latencies   = []          # 每次 HTTP 请求耗时（ms）
lat_lock    = threading.Lock()


def submit_job(session, url, payload, verbose=False):
    """提交单个任务，返回 (success: bool, latency_ms: float)"""
    t0 = time.monotonic()
    try:
        resp = session.post(url, json=payload, timeout=10)
        latency_ms = (time.monotonic() - t0) * 1000
        if resp.status_code in (200, 201):
            data = resp.json()
            if verbose:
                print(f"  [OK] job_id={data.get('job_id')}  queue={data.get('queue')}  "
                      f"status={data.get('status')}  latency={latency_ms:.1f}ms", flush=True)
            return True, latency_ms
        else:
            if verbose:
                print(f"  [FAIL] HTTP {resp.status_code}: {resp.text[:120]}", flush=True)
            return False, latency_ms
    except Exception as e:
        latency_ms = (time.monotonic() - t0) * 1000
        if verbose:
            print(f"  [ERROR] {e}", flush=True)
        return None, latency_ms   # None = 网络错误


def worker_thread(job_queue, url, verbose, rate_limiter):
    """消费 job_queue 里的 payload，逐个提交"""
    session = requests.Session()
    session.headers.update({"Content-Type": "application/json"})

    while not stop_event.is_set():
        try:
            payload = job_queue.get(timeout=0.5)
        except queue.Empty:
            continue

        # 速率限制
        if rate_limiter is not None:
            rate_limiter.acquire()

        ok, lat = submit_job(session, url, payload, verbose)

        with stats_lock:
            stats["submitted"] += 1
            if ok is True:
                stats["success"] += 1
            elif ok is False:
                stats["failed"] += 1
            else:
                stats["errors"] += 1

        with lat_lock:
            latencies.append(lat)

        job_queue.task_done()


class TokenBucket:
    """简单令牌桶，用于速率限制"""
    def __init__(self, rate):
        self.rate     = rate          # tokens/sec
        self.tokens   = rate
        self.last     = time.monotonic()
        self.lock     = threading.Lock()

    def acquire(self):
        with self.lock:
            now = time.monotonic()
            elapsed = now - self.last
            self.tokens = min(self.rate, self.tokens + elapsed * self.rate)
            self.last = now
            if self.tokens >= 1:
                self.tokens -= 1
                return
        # 不够令牌，等待
        time.sleep(1.0 / self.rate)


def print_stats(label="[Stats]"):
    with stats_lock:
        s = dict(stats)
    with lat_lock:
        lats = list(latencies)

    print(f"\n{label} submitted={s['submitted']}  success={s['success']}  "
          f"failed={s['failed']}  errors={s['errors']}", flush=True)
    if lats:
        lats_sorted = sorted(lats)
        n = len(lats_sorted)
        avg = sum(lats_sorted) / n
        p50 = lats_sorted[n // 2]
        p95 = lats_sorted[int(n * 0.95)]
        mx  = lats_sorted[-1]
        print(f"{label} http_latency: avg={avg:.1f}ms  p50={p50:.1f}ms  "
              f"p95={p95:.1f}ms  max={mx:.1f}ms  n={n}", flush=True)


def make_payload(args, seq):
    """构造任务 payload"""
    data = {
        "seq":       seq,
        "timestamp": datetime.utcnow().isoformat() + "Z",
        "message":   f"job #{seq} from producer.py",
        "random":    random.randint(1, 10000),
    }
    payload = {
        "queue":    args.queue,
        "job_type": args.job_type,
        "data":     data,
        "priority": args.priority,
    }
    if args.delay > 0:
        payload["delay"] = args.delay
    return payload


def main():
    parser = argparse.ArgumentParser(description="goapp 生产者 — 批量提交任务")
    parser.add_argument("--queue",       default="default",   help="目标队列（默认: default）")
    parser.add_argument("--jobs",        type=int, default=100, help="提交任务总数，0=无限（默认: 100）")
    parser.add_argument("--concurrency", type=int, default=4,   help="并发线程数（默认: 4）")
    parser.add_argument("--rate",        type=float, default=0, help="每秒最大提交速率，0=不限速（默认: 0）")
    parser.add_argument("--job-type",    default="test_job",  help="任务类型（默认: test_job）")
    parser.add_argument("--delay",       type=int, default=0,  help="任务延迟执行秒数（默认: 0）")
    parser.add_argument("--host",        default="localhost:8080", help="goapp 地址（默认: localhost:8080）")
    parser.add_argument("--interval",    type=float, default=0, help="每批次间隔秒数（默认: 0）")
    parser.add_argument("--priority",    type=int, default=0,  help="任务优先级 0~10（默认: 0）")
    parser.add_argument("--verbose",     action="store_true",  help="打印每条任务的提交结果")
    args = parser.parse_args()

    url = f"http://{args.host}/api/jobs"
    infinite = (args.jobs == 0)

    print(f"[Producer] 目标: {url}", flush=True)
    print(f"[Producer] 队列={args.queue}  任务数={'∞' if infinite else args.jobs}  "
          f"并发={args.concurrency}  速率={'不限' if args.rate == 0 else f'{args.rate}/s'}  "
          f"job_type={args.job_type}  delay={args.delay}s  priority={args.priority}", flush=True)

    rate_limiter = TokenBucket(args.rate) if args.rate > 0 else None
    jq = queue.Queue(maxsize=args.concurrency * 8)

    # 启动 worker 线程
    threads = []
    for _ in range(args.concurrency):
        t = threading.Thread(target=worker_thread,
                             args=(jq, url, args.verbose, rate_limiter),
                             daemon=True)
        t.start()
        threads.append(t)

    # 定期打印统计
    last_report = time.monotonic()
    report_interval = 5.0   # 每 5s 打印一次

    seq = 0
    t_start = time.monotonic()

    try:
        while not stop_event.is_set():
            if not infinite and seq >= args.jobs:
                break

            payload = make_payload(args, seq + 1)
            jq.put(payload)   # 阻塞直到队列有空位
            seq += 1

            # 批次间隔
            if args.interval > 0 and seq % args.concurrency == 0:
                time.sleep(args.interval)

            # 定期打印统计
            now = time.monotonic()
            if now - last_report >= report_interval:
                print_stats()
                last_report = now

    except KeyboardInterrupt:
        print("\n[Producer] 收到中断信号，等待已提交任务完成...", flush=True)
        stop_event.set()

    # 等待队列清空
    jq.join()
    stop_event.set()

    elapsed = time.monotonic() - t_start
    print_stats(f"[Producer] 完成（耗时 {elapsed:.1f}s，吞吐 {seq/elapsed:.1f} jobs/s）")
    print("[Producer] 已退出", flush=True)


if __name__ == "__main__":
    main()
