package main

// goworker — 独立 WebSocket Worker 程序
//
// 协议（与 goapp 服务端约定）：
//
//  服务端 → Worker（JSON）：
//    连接成功：{"type":"connected","message":"..."}
//    派发任务：{"type":"job","job_id":1,"queue":"default","job_type":"send_email","payload":"{...}"}
//    任务确认：{"type":"ack","message":"job #1 done"}
//
//  Worker → 服务端（JSON）：
//    任务成功：{"type":"result","job_id":1,"success":true,"log":"..."}
//    任务失败：{"type":"result","job_id":1,"success":false,"error":"..."}

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

// ─────────────────────────────────────────────
// 消息结构（与服务端保持一致）
// ─────────────────────────────────────────────

// WsJobMessage 服务端推送的任务消息
type WsJobMessage struct {
	Type    string `json:"type"`
	JobID   int64  `json:"job_id"`
	Queue   string `json:"queue"`
	JobType string `json:"job_type"`
	Payload string `json:"payload"`
}

// WsResultMessage Worker 回报的结果消息
type WsResultMessage struct {
	Type    string `json:"type"`
	JobID   int64  `json:"job_id"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
	Log     string `json:"log,omitempty"`
}

// WsControl 控制消息（connected / ack / ping）
type WsControl struct {
	Type    string `json:"type"`
	Message string `json:"message,omitempty"`
}

// Payload 任务 payload 结构（与服务端一致）
type Payload struct {
	JobType     string          `json:"job_type"`
	Data        json.RawMessage `json:"data"`
	TimeoutSec  int             `json:"timeout_sec"`
	MaxAttempts int             `json:"max_attempts"`
	Backoff     []int           `json:"backoff"`
}

// ─────────────────────────────────────────────
// Job Handler 注册表
// ─────────────────────────────────────────────

// JobHandlerFunc 任务处理函数签名
// ctx: 超时控制 context
// jobID: 任务 ID
// data: payload.data 原始 JSON
// 返回 log 字符串（成功时）或 error（失败时）
type JobHandlerFunc func(ctx context.Context, jobID int64, data json.RawMessage) (string, error)

var (
	handlersMu sync.RWMutex
	handlers   = map[string]JobHandlerFunc{}
)

// RegisterHandler 注册一个 job_type 的处理函数
func RegisterHandler(jobType string, fn JobHandlerFunc) {
	handlersMu.Lock()
	defer handlersMu.Unlock()
	handlers[jobType] = fn
	log.Printf("[Worker] Registered handler: %s", jobType)
}

// ─────────────────────────────────────────────
// 内置 Job Handlers（示例实现）
// ─────────────────────────────────────────────

func init() {
	// generate_report: 模拟生成报告（随机耗时 100-500ms）
	RegisterHandler("generate_report", func(ctx context.Context, jobID int64, data json.RawMessage) (string, error) {
		var d struct {
			Name   string `json:"name"`
			Report string `json:"report"`
		}
		json.Unmarshal(data, &d)
		name := d.Name
		if name == "" {
			name = d.Report
		}
		if name == "" {
			name = "unknown"
		}

		// 模拟耗时操作
		delay := time.Duration(100+rand.Intn(400)) * time.Millisecond
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return "", fmt.Errorf("generate_report cancelled: %v", ctx.Err())
		}

		return fmt.Sprintf("Report '%s' generated in %v", name, delay.Round(time.Millisecond)), nil
	})

	// send_email: 模拟发送邮件（随机耗时 50-200ms）
	RegisterHandler("send_email", func(ctx context.Context, jobID int64, data json.RawMessage) (string, error) {
		var d struct {
			To      string `json:"to"`
			Subject string `json:"subject"`
		}
		json.Unmarshal(data, &d)
		if d.To == "" {
			d.To = "unknown@example.com"
		}

		delay := time.Duration(50+rand.Intn(150)) * time.Millisecond
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return "", fmt.Errorf("send_email cancelled: %v", ctx.Err())
		}

		return fmt.Sprintf("Email sent to %s (subject: %q) in %v", d.To, d.Subject, delay.Round(time.Millisecond)), nil
	})

	// resize_image: 模拟图片处理（随机耗时 200-800ms）
	RegisterHandler("resize_image", func(ctx context.Context, jobID int64, data json.RawMessage) (string, error) {
		var d struct {
			URL    string `json:"url"`
			Width  int    `json:"width"`
			Height int    `json:"height"`
		}
		json.Unmarshal(data, &d)

		delay := time.Duration(200+rand.Intn(600)) * time.Millisecond
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return "", fmt.Errorf("resize_image cancelled: %v", ctx.Err())
		}

		return fmt.Sprintf("Image %s resized to %dx%d in %v", d.URL, d.Width, d.Height, delay.Round(time.Millisecond)), nil
	})

	// data_sync: 模拟数据同步（随机耗时 300-1000ms）
	RegisterHandler("data_sync", func(ctx context.Context, jobID int64, data json.RawMessage) (string, error) {
		var d struct {
			Source string `json:"source"`
			Target string `json:"target"`
		}
		json.Unmarshal(data, &d)

		delay := time.Duration(300+rand.Intn(700)) * time.Millisecond
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return "", fmt.Errorf("data_sync cancelled: %v", ctx.Err())
		}

		return fmt.Sprintf("Synced %s → %s in %v", d.Source, d.Target, delay.Round(time.Millisecond)), nil
	})

	// fail_test: 专门用于测试失败重试的 handler（总是失败）
	RegisterHandler("fail_test", func(ctx context.Context, jobID int64, data json.RawMessage) (string, error) {
		return "", fmt.Errorf("intentional failure for testing (job #%d)", jobID)
	})
}

// ─────────────────────────────────────────────
// processJob 执行一个任务
// ─────────────────────────────────────────────

func processJob(jobMsg WsJobMessage) WsResultMessage {
	// 解析 payload
	var pf Payload
	if err := json.Unmarshal([]byte(jobMsg.Payload), &pf); err != nil {
		return WsResultMessage{
			Type:    "result",
			JobID:   jobMsg.JobID,
			Success: false,
			Error:   fmt.Sprintf("invalid payload JSON: %v", err),
		}
	}

	// 确定 job_type（优先用 payload 里的，其次用消息里的）
	jobType := pf.JobType
	if jobType == "" {
		jobType = jobMsg.JobType
	}

	// 查找 handler
	handlersMu.RLock()
	fn, ok := handlers[jobType]
	handlersMu.RUnlock()

	if !ok {
		return WsResultMessage{
			Type:    "result",
			JobID:   jobMsg.JobID,
			Success: false,
			Error:   fmt.Sprintf("unknown job type: %q", jobType),
		}
	}

	// 超时控制
	timeoutSec := pf.TimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = 60
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()

	log.Printf("[Worker] Processing job #%d type=%s queue=%s timeout=%ds",
		jobMsg.JobID, jobType, jobMsg.Queue, timeoutSec)

	start := time.Now()
	logStr, err := fn(ctx, jobMsg.JobID, pf.Data)
	elapsed := time.Since(start).Round(time.Millisecond)

	if err != nil {
		log.Printf("[Worker] Job #%d FAILED in %v: %v", jobMsg.JobID, elapsed, err)
		return WsResultMessage{
			Type:    "result",
			JobID:   jobMsg.JobID,
			Success: false,
			Error:   err.Error(),
		}
	}

	log.Printf("[Worker] Job #%d DONE in %v: %s", jobMsg.JobID, elapsed, logStr)
	return WsResultMessage{
		Type:    "result",
		JobID:   jobMsg.JobID,
		Success: true,
		Log:     logStr,
	}
}

// ─────────────────────────────────────────────
// Worker 连接主循环
// ─────────────────────────────────────────────

// WorkerConfig Worker 配置
type WorkerConfig struct {
	ServerURL   string        // WebSocket 服务端地址，如 ws://localhost:8080/ws/worker
	Queue       string        // 监听的队列名
	APIKey      string        // 可选：API Key（X-Api-Key header）
	Concurrency int           // 并发处理任务数（默认 1）
	ReconnectDelay time.Duration // 断线重连间隔（默认 3s）
	MaxReconnects  int           // 最大重连次数（0 = 无限）
}

// runWorker 启动一个 Worker 连接（带自动重连）
func runWorker(cfg WorkerConfig, stopCh <-chan struct{}) {
	if cfg.ReconnectDelay <= 0 {
		cfg.ReconnectDelay = 3 * time.Second
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 1
	}

	reconnects := 0
	for {
		select {
		case <-stopCh:
			log.Printf("[Worker] Stopped (queue=%s)", cfg.Queue)
			return
		default:
		}

		if cfg.MaxReconnects > 0 && reconnects >= cfg.MaxReconnects {
			log.Printf("[Worker] Max reconnects (%d) reached, stopping", cfg.MaxReconnects)
			return
		}

		url := cfg.ServerURL
		if !strings.Contains(url, "queue=") {
			sep := "?"
			if strings.Contains(url, "?") {
				sep = "&"
			}
			url = url + sep + "queue=" + cfg.Queue
		}

		log.Printf("[Worker] Connecting to %s (attempt %d)", url, reconnects+1)

		header := make(map[string][]string)
		if cfg.APIKey != "" {
			header["X-Api-Key"] = []string{cfg.APIKey}
		}

		dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
		conn, _, err := dialer.Dial(url, header)
		if err != nil {
			log.Printf("[Worker] Connection failed: %v, retrying in %v", err, cfg.ReconnectDelay)
			reconnects++
			select {
			case <-time.After(cfg.ReconnectDelay):
			case <-stopCh:
				return
			}
			continue
		}

		log.Printf("[Worker] Connected to %s (queue=%s)", cfg.ServerURL, cfg.Queue)
		reconnects = 0

		// 运行连接会话
		disconnected := runSession(conn, cfg, stopCh)
		conn.Close()

		if !disconnected {
			// 主动停止
			return
		}

		log.Printf("[Worker] Disconnected, reconnecting in %v...", cfg.ReconnectDelay)
		select {
		case <-time.After(cfg.ReconnectDelay):
		case <-stopCh:
			return
		}
	}
}

// runSession 在一个 WebSocket 连接上运行任务处理循环
// 返回 true 表示需要重连，false 表示主动停止
func runSession(conn *websocket.Conn, cfg WorkerConfig, stopCh <-chan struct{}) bool {
	// 并发控制信号量
	sem := make(chan struct{}, cfg.Concurrency)
	var wg sync.WaitGroup

	// 读取消息循环
	msgCh := make(chan []byte, 16)
	errCh := make(chan error, 1)

	go func() {
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				errCh <- err
				return
			}
			msgCh <- msg
		}
	}()

	// 心跳 ticker（每 20s 发一次 ping，防止连接被服务端超时断开）
	pingTicker := time.NewTicker(20 * time.Second)
	defer pingTicker.Stop()

	for {
		select {
		case <-stopCh:
			log.Printf("[Worker] Graceful shutdown: waiting for running jobs...")
			wg.Wait()
			return false

		case err := <-errCh:
			log.Printf("[Worker] Read error: %v", err)
			wg.Wait()
			return true

		case <-pingTicker.C:
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				log.Printf("[Worker] Ping failed: %v", err)
				wg.Wait()
				return true
			}

		case msg := <-msgCh:
			// 解析消息类型
			var ctrl WsControl
			if err := json.Unmarshal(msg, &ctrl); err != nil {
				log.Printf("[Worker] Invalid message: %s", msg)
				continue
			}

			switch ctrl.Type {
			case "connected":
				log.Printf("[Worker] Server: %s", ctrl.Message)

			case "ack":
				log.Printf("[Worker] ACK: %s", ctrl.Message)

			case "job":
				// 解析任务消息
				var jobMsg WsJobMessage
				if err := json.Unmarshal(msg, &jobMsg); err != nil {
					log.Printf("[Worker] Invalid job message: %v", err)
					continue
				}

				// 获取并发槽
				sem <- struct{}{}
				wg.Add(1)

				go func(jm WsJobMessage) {
					defer func() {
						<-sem
						wg.Done()
					}()

					result := processJob(jm)
					resultBytes, _ := json.Marshal(result)

					// 发送结果（需要加锁，WebSocket 写操作非线程安全）
					if err := conn.WriteMessage(websocket.TextMessage, resultBytes); err != nil {
						log.Printf("[Worker] Failed to send result for job #%d: %v", jm.JobID, err)
					}
				}(jobMsg)

			default:
				log.Printf("[Worker] Unknown message type: %s (raw: %s)", ctrl.Type, msg)
			}
		}
	}
}

// ─────────────────────────────────────────────
// main
// ─────────────────────────────────────────────

func main() {
	// 命令行参数
	serverURL   := flag.String("server", "ws://localhost:8080/ws/worker", "Queue server WebSocket URL")
	queue       := flag.String("queue", "default", "Queue name to consume")
	apiKey      := flag.String("api-key", "", "API key (X-Api-Key header)")
	concurrency := flag.Int("concurrency", 1, "Number of concurrent jobs per connection")
	connections := flag.Int("connections", 1, "Number of parallel WebSocket connections")
	reconnDelay := flag.Duration("reconnect", 3*time.Second, "Reconnect delay on disconnect")
	flag.Parse()

	// 环境变量覆盖
	if v := os.Getenv("QUEUE_SERVER"); v != "" {
		*serverURL = v
	}
	if v := os.Getenv("QUEUE_NAME"); v != "" {
		*queue = v
	}
	if v := os.Getenv("API_KEY"); v != "" {
		*apiKey = v
	}

	log.Printf("=== GoWorker Starting ===")
	log.Printf("  Server:      %s", *serverURL)
	log.Printf("  Queue:       %s", *queue)
	log.Printf("  Concurrency: %d jobs/connection", *concurrency)
	log.Printf("  Connections: %d", *connections)
	log.Printf("  Reconnect:   %v", *reconnDelay)
	log.Printf("  Handlers:    %s", registeredHandlers())

	// 优雅关闭
	stopCh := make(chan struct{})
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// 启动多个并行连接
	var wg sync.WaitGroup
	for i := 0; i < *connections; i++ {
		wg.Add(1)
		go func(connIdx int) {
			defer wg.Done()
			cfg := WorkerConfig{
				ServerURL:      *serverURL,
				Queue:          *queue,
				APIKey:         *apiKey,
				Concurrency:    *concurrency,
				ReconnectDelay: *reconnDelay,
			}
			runWorker(cfg, stopCh)
		}(i)
	}

	// 等待信号
	sig := <-sigCh
	log.Printf("[Worker] Received signal %v, shutting down...", sig)
	close(stopCh)
	wg.Wait()
	log.Printf("[Worker] Shutdown complete")
}

// registeredHandlers 返回已注册的 handler 列表
func registeredHandlers() string {
	handlersMu.RLock()
	defer handlersMu.RUnlock()
	names := make([]string, 0, len(handlers))
	for k := range handlers {
		names = append(names, k)
	}
	return strings.Join(names, ", ")
}
