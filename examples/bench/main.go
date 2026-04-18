package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

type WsMessage struct {
	Type       string   `json:"type"`
	Message    string   `json:"message,omitempty"`
	JobID      int64    `json:"job_id,omitempty"`
	Queue      string   `json:"queue,omitempty"`
	JobType    string   `json:"job_type,omitempty"`
	Payload    string   `json:"payload,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	TimeoutSec int      `json:"timeout_sec,omitempty"`
}

type WsResult struct {
	Type    string `json:"type"`
	JobID   int64  `json:"job_id"`
	Success bool   `json:"success"`
	Log     string `json:"log,omitempty"`
	Error   string `json:"error,omitempty"`
}

var (
	enqueued       int64
	processed      int64
	succeeded      int64
	failedCnt      int64
	totalLatencyNs int64
	latencyCount   int64
	jobStartTimes  sync.Map
)

// enqueueJobs 并发投递任务（concurrency 个 goroutine 同时 POST）
func enqueueJobs(serverURL, queue, apiKey string, total int, concurrency int) {
	enqURL := serverURL + "/api/jobs"
	log.Printf("[Enqueuer] 开始并发投递 %d 条任务到队列 %q (并发=%d)", total, queue, concurrency)
	startTime := time.Now()

	taskCh := make(chan int, concurrency*2)
	var wg sync.WaitGroup

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client := &http.Client{Timeout: 10 * time.Second}
			for idx := range taskCh {
				payload := map[string]interface{}{
					"job_type":    "bench_job",
					"queue":       queue,
					"timeout_sec": 30,
					"data": map[string]interface{}{
						"index":   idx,
						"message": fmt.Sprintf("bench task #%d", idx),
					},
				}
				body, _ := json.Marshal(payload)
				req, _ := http.NewRequest("POST", enqURL, bytes.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				if apiKey != "" {
					req.Header.Set("X-API-Key", apiKey)
				}
				var (
					resp *http.Response
					err  error
				)
				for retry := 0; retry < 5; retry++ {
					if retry > 0 {
						time.Sleep(time.Duration(retry*200) * time.Millisecond)
						req2, _ := http.NewRequest("POST", enqURL, bytes.NewReader(body))
						req2.Header.Set("Content-Type", "application/json")
						if apiKey != "" {
							req2.Header.Set("X-API-Key", apiKey)
						}
						req = req2
					}
					resp, err = client.Do(req)
					if err == nil {
						break
					}
				}
				if err != nil {
					log.Printf("[Enqueuer] 投递失败(重试5次) #%d: %v", idx, err)
					continue
				}
				resp.Body.Close()
				cur := atomic.AddInt64(&enqueued, 1)
				if cur%100 == 0 {
					elapsed := time.Since(startTime)
					rate := float64(cur) / elapsed.Seconds()
					log.Printf("[Enqueuer] 已投递 %d/%d (%.0f req/s)", cur, total, rate)
				}
			}
		}()
	}

	for i := 0; i < total; i++ {
		taskCh <- i
	}
	close(taskCh)
	wg.Wait()

	elapsed := time.Since(startTime)
	rate := float64(total) / elapsed.Seconds()
	log.Printf("[Enqueuer] ✅ 投递完成: %d 条, 耗时 %v, 平均 %.0f req/s",
		total, elapsed.Round(time.Millisecond), rate)
}

// runWorker 每个 Worker 维护一条 WebSocket 连接，持续处理任务
func runWorker(id int, serverURL, queue, apiKey string, stopCh <-chan struct{}, wg *sync.WaitGroup) {
	defer wg.Done()

	u, _ := url.Parse(serverURL)
	scheme := "ws"
	if u.Scheme == "https" {
		scheme = "wss"
	}
	wsURL := fmt.Sprintf("%s://%s/ws/worker?queue=%s", scheme, u.Host, queue)

	dialHeader := http.Header{}
	if apiKey != "" {
		dialHeader.Set("X-API-Key", apiKey)
	}

	for {
		select {
		case <-stopCh:
			return
		default:
		}

		conn, _, err := websocket.DefaultDialer.Dial(wsURL, dialHeader)
		if err != nil {
			select {
			case <-stopCh:
				return
			case <-time.After(300 * time.Millisecond):
			}
			continue
		}

		// 独立 goroutine 读消息，通过 channel 传递，避免 gorilla/websocket panic
		type readResult struct {
			raw []byte
			err error
		}
		readCh := make(chan readResult, 64)

		go func() {
			defer close(readCh)
			for {
				_, raw, err := conn.ReadMessage()
				readCh <- readResult{raw, err}
				if err != nil {
					return
				}
			}
		}()

		// 心跳 goroutine
		pingStop := make(chan struct{})
		go func() {
			ticker := time.NewTicker(15 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					conn.WriteJSON(map[string]string{"type": "ping"})
				case <-pingStop:
					return
				}
			}
		}()

	connLoop:
		for {
			select {
			case <-stopCh:
				break connLoop
			case res, ok := <-readCh:
				if !ok {
					break connLoop
				}
				if res.err != nil {
					break connLoop
				}

				var msg WsMessage
				if err := json.Unmarshal(res.raw, &msg); err != nil {
					continue
				}

				switch msg.Type {
				case "connected":
					// 静默
				case "job":
					jobStartTimes.Store(msg.JobID, time.Now())
					go processJob(conn, msg)
				case "ack", "pong":
					// 忽略
				}
			}
		}

		close(pingStop)
		conn.Close()
		for range readCh {
		}

		select {
		case <-stopCh:
			return
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// processJob 处理单个任务（极轻量，无人工延时）
func processJob(conn *websocket.Conn, msg WsMessage) {
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(msg.Payload), &payload); err != nil {
		sendResult(conn, msg.JobID, false, "", fmt.Sprintf("payload parse error: %v", err))
		return
	}

	data, _ := payload["data"].(map[string]interface{})
	result := fmt.Sprintf("bench_job done: index=%v", data["index"])

	if startVal, ok := jobStartTimes.LoadAndDelete(msg.JobID); ok {
		latNs := time.Since(startVal.(time.Time)).Nanoseconds()
		atomic.AddInt64(&totalLatencyNs, latNs)
		atomic.AddInt64(&latencyCount, 1)
	}

	atomic.AddInt64(&processed, 1)
	atomic.AddInt64(&succeeded, 1)
	sendResult(conn, msg.JobID, true, result, "")
}

func sendResult(conn *websocket.Conn, jobID int64, success bool, logStr, errStr string) {
	res := WsResult{Type: "result", JobID: jobID, Success: success}
	if success {
		res.Log = logStr
	} else {
		res.Error = errStr
		atomic.AddInt64(&failedCnt, 1)
	}
	conn.WriteJSON(res)
}

func monitor(totalJobs int, stopCh <-chan struct{}, doneCh chan<- struct{}) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	startTime := time.Now()
	var lastProcessed int64
	var doneOnce sync.Once

	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			elapsed := time.Since(startTime)
			cur := atomic.LoadInt64(&processed)
			enq := atomic.LoadInt64(&enqueued)
			succ := atomic.LoadInt64(&succeeded)
			fail := atomic.LoadInt64(&failedCnt)

			delta := cur - lastProcessed
			lastProcessed = cur
			tps := float64(delta) / 2.0

			var avgLatMs float64
			lc := atomic.LoadInt64(&latencyCount)
			if lc > 0 {
				avgLatMs = float64(atomic.LoadInt64(&totalLatencyNs)) / float64(lc) / 1e6
			}

			log.Printf("[Monitor] elapsed=%-8s enqueued=%-5d processed=%-5d succ=%-5d fail=%-4d tps=%.0f/s avgLat=%.1fms",
				elapsed.Round(time.Second), enq, cur, succ, fail, tps, avgLatMs)

			if enq >= int64(totalJobs) && cur >= enq {
				doneOnce.Do(func() { close(doneCh) })
				return
			}
		}
	}
}

func main() {
	serverURL  := flag.String("url",        "http://localhost:8080", "服务端地址")
	queue      := flag.String("queue",      "bench",                 "队列名")
	apiKey     := flag.String("apikey",     "",                      "API Key（可选）")
	totalJobs  := flag.Int("jobs",          300,                     "投递任务总数")
	numWorkers := flag.Int("workers",       20,                      "Worker 并发数（WebSocket 连接数）")
	enqConc    := flag.Int("enq-concurrency", 10,                   "投递并发数（HTTP goroutine 数）")
	flag.Parse()

	log.SetFlags(log.Ltime | log.Lmicroseconds)
	log.Printf("═══════════════════════════════════════════════════════")
	log.Printf("  GoQueue 压力测试")
	log.Printf("  服务端:   %s", *serverURL)
	log.Printf("  队列:     %s", *queue)
	log.Printf("  任务数:   %d", *totalJobs)
	log.Printf("  Worker:   %d", *numWorkers)
	log.Printf("  投递并发: %d", *enqConc)
	log.Printf("═══════════════════════════════════════════════════════")

	stopCh := make(chan struct{})
	doneCh := make(chan struct{})

	var workerWg sync.WaitGroup
	for i := 0; i < *numWorkers; i++ {
		workerWg.Add(1)
		go runWorker(i, *serverURL, *queue, *apiKey, stopCh, &workerWg)
	}
	log.Printf("[Main] 已启动 %d 个 Worker，等待连接稳定...", *numWorkers)
	time.Sleep(1 * time.Second)

	go monitor(*totalJobs, stopCh, doneCh)

	benchStart := time.Now()
	// 先并发投递所有任务，再等 Worker 处理完
	go enqueueJobs(*serverURL, *queue, *apiKey, *totalJobs, *enqConc)

	timeout := time.Duration(*totalJobs/10+120) * time.Second
	select {
	case <-doneCh:
		log.Printf("[Main] ✅ 所有任务处理完毕！")
	case <-time.After(timeout):
		log.Printf("[Main] ⚠️  超时（%v），强制结束", timeout)
	}

	close(stopCh)
	workerWg.Wait()

	elapsed := time.Since(benchStart)
	enq  := atomic.LoadInt64(&enqueued)
	proc := atomic.LoadInt64(&processed)
	succ := atomic.LoadInt64(&succeeded)
	fail := atomic.LoadInt64(&failedCnt)

	var avgLatMs float64
	lc := atomic.LoadInt64(&latencyCount)
	if lc > 0 {
		avgLatMs = float64(atomic.LoadInt64(&totalLatencyNs)) / float64(lc) / 1e6
	}
	throughput := 0.0
	if elapsed.Seconds() > 0 {
		throughput = float64(proc) / elapsed.Seconds()
	}

	log.Printf("")
	log.Printf("═══════════════════════════════════════════════════════")
	log.Printf("  压力测试报告")
	log.Printf("  总耗时:     %v", elapsed.Round(time.Millisecond))
	log.Printf("  投递任务:   %d", enq)
	log.Printf("  处理完成:   %d", proc)
	if proc > 0 {
		log.Printf("  成功:       %d (%.1f%%)", succ, float64(succ)/float64(proc)*100)
		log.Printf("  失败:       %d (%.1f%%)", fail, float64(fail)/float64(proc)*100)
	}
	log.Printf("  吞吐量:     %.1f jobs/s", throughput)
	log.Printf("  平均延迟:   %.1f ms", avgLatMs)
	log.Printf("  Worker 数:  %d", *numWorkers)
	log.Printf("═══════════════════════════════════════════════════════")
}
