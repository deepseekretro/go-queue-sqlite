package main

// goworker — 独立 WebSocket Worker 程序
//
// 协议（与 goapp 服务端约定）：
//
//  服务端 → Worker（JSON）：
//    连接成功：{"type":"connected","message":"..."}
//    派发任务：{"type":"job","job_id":1,"queue":"default","job_type":"send_email","payload":"{...}","tags":["email","promo"]}
//    任务确认：{"type":"ack","message":"job #1 done"}
//
//  Worker → 服务端（JSON）：
//    任务成功：{"type":"result","job_id":1,"success":true,"log":"..."}
//    任务失败：{"type":"result","job_id":1,"success":false,"error":"..."}
//
// 新特性（v4）：
//   - tags：任务可携带标签，Worker 可按标签路由或过滤
//   - batch catch/finally：批次失败/完成回调
//   - queue pause/resume：队列可暂停，暂停期间任务不派发

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

// WsMessage 是服务端下发的消息
type WsMessage struct {
	Type    string          `json:"type"`
	JobID   int64           `json:"job_id,omitempty"`
	Queue   string          `json:"queue,omitempty"`
	JobType string          `json:"job_type,omitempty"`
	Payload string          `json:"payload,omitempty"`
	Tags    []string        `json:"tags,omitempty"`   // v4: 任务标签
	Message string          `json:"message,omitempty"`
	Error   string          `json:"error,omitempty"`
}

// ResultMessage 是 Worker 上报的结果
type ResultMessage struct {
	Type    string `json:"type"`
	JobID   int64  `json:"job_id"`
	Success bool   `json:"success"`
	Log     string `json:"log,omitempty"`
	Error   string `json:"error,omitempty"`
}

// ─────────────────────────────────────────────
// Handler 注册表
// ─────────────────────────────────────────────

// JobHandler 是任务处理函数类型
// data: 解析后的 payload（map）
// tags: 任务标签（v4 新增）
type JobHandler func(ctx context.Context, data map[string]interface{}, tags []string) (string, error)

var (
	handlers   = map[string]JobHandler{}
	handlersMu sync.RWMutex
)

// Register 注册一个 job_type 对应的处理函数
func Register(jobType string, h JobHandler) {
	handlersMu.Lock()
	defer handlersMu.Unlock()
	handlers[jobType] = h
}

// ─────────────────────────────────────────────
// Worker 配置
// ─────────────────────────────────────────────

type WorkerConfig struct {
	ServerURL      string
	Queue          string
	APIKey         string
	Concurrency    int
	ReconnectDelay time.Duration
}

// ─────────────────────────────────────────────
// Worker 核心
// ─────────────────────────────────────────────

func runWorker(cfg WorkerConfig, stopCh <-chan struct{}) {
	sem := make(chan struct{}, cfg.Concurrency)

	for {
		select {
		case <-stopCh:
			return
		default:
		}

		wsURL := fmt.Sprintf("%s?queue=%s", cfg.ServerURL, cfg.Queue)
		dialHeader := map[string][]string{}
		if cfg.APIKey != "" {
			dialHeader["Authorization"] = []string{"Bearer " + cfg.APIKey}
		}

		log.Printf("[Worker] Connecting to %s ...", wsURL)
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, dialHeader)
		if err != nil {
			log.Printf("[Worker] Connect failed: %v, retry in %v", err, cfg.ReconnectDelay)
			select {
			case <-stopCh:
				return
			case <-time.After(cfg.ReconnectDelay):
			}
			continue
		}
		log.Printf("[Worker] Connected ✓")

		// 心跳
		pingStop := make(chan struct{})
		go func() {
			ticker := time.NewTicker(20 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					conn.WriteMessage(websocket.PingMessage, nil)
				case <-pingStop:
					return
				}
			}
		}()

		// 读消息循环
		disconnected := false
		for !disconnected {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				log.Printf("[Worker] Read error: %v", err)
				disconnected = true
				break
			}

			var msg WsMessage
			if err := json.Unmarshal(raw, &msg); err != nil {
				log.Printf("[Worker] JSON parse error: %v", err)
				continue
			}

			switch msg.Type {
			case "connected":
				log.Printf("[Worker] Server: %s", msg.Message)
			case "job":
				sem <- struct{}{}
				go func(m WsMessage) {
					defer func() { <-sem }()
					handleJob(conn, m)
				}(msg)
			case "ack":
				log.Printf("[Worker] ACK: %s", msg.Message)
			default:
				log.Printf("[Worker] Unknown message type: %s", msg.Type)
			}
		}

		close(pingStop)
		conn.Close()

		select {
		case <-stopCh:
			return
		case <-time.After(cfg.ReconnectDelay + time.Duration(rand.Intn(1000))*time.Millisecond):
		}
	}
}

// handleJob 处理单个任务
func handleJob(conn *websocket.Conn, msg WsMessage) {
	log.Printf("[Job #%d] type=%s queue=%s tags=%v", msg.JobID, msg.JobType, msg.Queue, msg.Tags)

	// 解析 payload
	var data map[string]interface{}
	if err := json.Unmarshal([]byte(msg.Payload), &data); err != nil {
		sendResult(conn, msg.JobID, false, "", fmt.Sprintf("payload parse error: %v", err))
		return
	}

	// 查找 handler
	handlersMu.RLock()
	h, ok := handlers[msg.JobType]
	handlersMu.RUnlock()

	if !ok {
		log.Printf("[Job #%d] No handler for job_type=%s", msg.JobID, msg.JobType)
		sendResult(conn, msg.JobID, false, "", fmt.Sprintf("no handler for job_type: %s", msg.JobType))
		return
	}

	// 执行（带超时）
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	result, err := h(ctx, data, msg.Tags)
	if err != nil {
		log.Printf("[Job #%d] FAILED: %v", msg.JobID, err)
		sendResult(conn, msg.JobID, false, "", err.Error())
	} else {
		log.Printf("[Job #%d] OK: %s", msg.JobID, result)
		sendResult(conn, msg.JobID, true, result, "")
	}
}

// sendResult 上报任务结果
func sendResult(conn *websocket.Conn, jobID int64, success bool, logMsg, errMsg string) {
	res := ResultMessage{
		Type:    "result",
		JobID:   jobID,
		Success: success,
		Log:     logMsg,
		Error:   errMsg,
	}
	b, _ := json.Marshal(res)
	conn.WriteMessage(websocket.TextMessage, b)
}

// ─────────────────────────────────────────────
// 示例 Job Handlers
// ─────────────────────────────────────────────

func handleSendEmail(ctx context.Context, data map[string]interface{}, tags []string) (string, error) {
	to, _ := data["to"].(string)
	subject, _ := data["subject"].(string)
	log.Printf("[send_email] to=%s subject=%s tags=%v", to, subject, tags)
	time.Sleep(300 * time.Millisecond)
	return fmt.Sprintf("Email sent to %s", to), nil
}

func handleGenerateReport(ctx context.Context, data map[string]interface{}, tags []string) (string, error) {
	name, _ := data["name"].(string)
	log.Printf("[generate_report] name=%s tags=%v", name, tags)
	time.Sleep(800 * time.Millisecond)
	return fmt.Sprintf("Report %q generated", name), nil
}

func handleResizeImage(ctx context.Context, data map[string]interface{}, tags []string) (string, error) {
	url, _ := data["url"].(string)
	w, _ := data["width"].(float64)
	h, _ := data["height"].(float64)
	log.Printf("[resize_image] url=%s size=%.0fx%.0f tags=%v", url, w, h, tags)
	time.Sleep(500 * time.Millisecond)
	return fmt.Sprintf("Image %s resized to %.0fx%.0f", url, w, h), nil
}

func handleDataSync(ctx context.Context, data map[string]interface{}, tags []string) (string, error) {
	src, _ := data["source"].(string)
	dst, _ := data["target"].(string)
	log.Printf("[data_sync] %s → %s tags=%v", src, dst, tags)
	time.Sleep(600 * time.Millisecond)
	return fmt.Sprintf("Synced %s → %s", src, dst), nil
}

// handleTagTask 演示如何根据 tags 做不同处理（v4 新增）
func handleTagTask(ctx context.Context, data map[string]interface{}, tags []string) (string, error) {
	msg, _ := data["message"].(string)
	log.Printf("[tag_task] message=%s tags=%v", msg, tags)

	// 根据 tag 做不同处理
	for _, tag := range tags {
		switch tag {
		case "urgent":
			log.Printf("[tag_task] 紧急任务，优先处理")
		case "dry-run":
			log.Printf("[tag_task] dry-run 模式，跳过实际操作")
			return "dry-run: skipped", nil
		case "notify":
			log.Printf("[tag_task] 需要发送通知")
		}
	}

	time.Sleep(200 * time.Millisecond)
	return fmt.Sprintf("tag_task done: %s (tags=%v)", msg, tags), nil
}

// handleBatchCallback 处理 batch 的 then/catch/finally 回调（v4 新增）
func handleBatchCallback(ctx context.Context, data map[string]interface{}, tags []string) (string, error) {
	batchID, _ := data["batch_id"].(float64)
	status, _ := data["status"].(string)
	log.Printf("[batch_callback] batch_id=%.0f status=%s tags=%v", batchID, status, tags)
	// 在这里可以发送通知、更新数据库等
	return fmt.Sprintf("batch %.0f callback handled (status=%s)", batchID, status), nil
}

// ─────────────────────────────────────────────
// main
// ─────────────────────────────────────────────

func main() {
	serverURL   := flag.String("server",      "ws://localhost:8080/ws/worker", "WebSocket server URL")
	queue       := flag.String("queue",       "default",                       "Queue name to listen on")
	apiKey      := flag.String("api-key",     "",                              "API key (Bearer token)")
	concurrency := flag.Int("concurrency",    4,                               "Max concurrent jobs per connection")
	connections := flag.Int("connections",    1,                               "Number of parallel WebSocket connections")
	reconnDelay := flag.Duration("reconnect", 3*time.Second,                   "Reconnect delay on disconnect")
	flag.Parse()

	// 注册 handlers
	Register("send_email",       handleSendEmail)
	Register("generate_report",  handleGenerateReport)
	Register("resize_image",     handleResizeImage)
	Register("data_sync",        handleDataSync)
	Register("tag_task",         handleTagTask)         // v4: tags 示例
	Register("batch_callback",   handleBatchCallback)   // v4: batch 回调示例
	Register("on_success",       handleBatchCallback)   // batch then_job
	Register("on_failure",       handleBatchCallback)   // batch catch_job
	Register("on_finally",       handleBatchCallback)   // batch finally_job

	log.Printf("[Worker] Handlers: %s", registeredHandlers())
	log.Printf("[Worker] Queue=%s Concurrency=%d Connections=%d", *queue, *concurrency, *connections)

	// 信号处理
	sigCh  := make(chan os.Signal, 1)
	stopCh := make(chan struct{})
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

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
