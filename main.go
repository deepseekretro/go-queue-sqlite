package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
)

// ─────────────────────────────────────────────
// DB
// ─────────────────────────────────────────────

var db *sql.DB

// initLogger 根据环境变量 LOG_FORMAT 决定日志格式
// LOG_FORMAT=json  → JSON 结构化日志（生产推荐）
// LOG_FORMAT=text  → 人类可读文本（默认，开发友好）
func initLogger() {
	format := os.Getenv("LOG_FORMAT")
	var handler slog.Handler
	if format == "json" {
		handler = slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})
	} else {
		handler = slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})
	}
	slog.SetDefault(slog.New(handler))
	// 同时把标准 log 包重定向到 slog，兼容旧代码里的 log.Printf
	log.SetFlags(0)
	log.SetOutput(slogWriter{})
}

// slogWriter 把 log.Printf 的输出转发给 slog.Info
type slogWriter struct{}

func (slogWriter) Write(p []byte) (n int, err error) {
	msg := strings.TrimRight(string(p), "\n")
	slog.Info(msg)
	return len(p), nil
}

func initDB() {
	var err error
	db, err = sql.Open("sqlite", "./queue.db?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		log.Fatal(err)
	}
	db.SetMaxOpenConns(1)

	// 建表（首次运行）
	schema := `
	CREATE TABLE IF NOT EXISTS jobs (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		queue        TEXT    NOT NULL DEFAULT 'default',
		payload      TEXT    NOT NULL,
		attempts     INTEGER NOT NULL DEFAULT 0,
		status       TEXT    NOT NULL DEFAULT 'pending',
		priority     INTEGER NOT NULL DEFAULT 5,
		dedup_key    TEXT    DEFAULT NULL,
		available_at INTEGER NOT NULL DEFAULT 0,
		started_at   INTEGER NOT NULL DEFAULT 0,
		finished_at  INTEGER NOT NULL DEFAULT 0,
		created_at   INTEGER NOT NULL,
		updated_at   INTEGER NOT NULL,
		UNIQUE(dedup_key) ON CONFLICT IGNORE
	);
	CREATE TABLE IF NOT EXISTS failed_jobs (
		id        INTEGER PRIMARY KEY AUTOINCREMENT,
		queue     TEXT    NOT NULL,
		job_type  TEXT    NOT NULL DEFAULT '',
		payload   TEXT    NOT NULL,
		attempts  INTEGER NOT NULL DEFAULT 0,
		exception TEXT    NOT NULL,
		failed_at INTEGER NOT NULL
	);`
	if _, err = db.Exec(schema); err != nil {
		log.Fatal(err)
	}

	// 迁移：为旧库的 failed_jobs 表补充新字段（ALTER TABLE IF NOT EXISTS 列不存在才加）
	migrations := []string{
		`ALTER TABLE failed_jobs ADD COLUMN job_type TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE failed_jobs ADD COLUMN attempts INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE jobs ADD COLUMN started_at INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE jobs ADD COLUMN finished_at INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE jobs ADD COLUMN priority INTEGER NOT NULL DEFAULT 5`,
		`ALTER TABLE jobs ADD COLUMN dedup_key TEXT DEFAULT NULL`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_jobs_dedup_key ON jobs(dedup_key) WHERE dedup_key IS NOT NULL`,
	}
	for _, m := range migrations {
		if _, err := db.Exec(m); err != nil {
			// SQLite 不支持 IF NOT EXISTS on ALTER，重复执行会报 "duplicate column"，忽略即可
			if !strings.Contains(err.Error(), "duplicate column") {
				log.Printf("[DB] migration warning: %v", err)
			}
		}
	}

	log.Println("[DB] SQLite initialized ✓ (migrations applied)")
}

// ─────────────────────────────────────────────
// Queue core
// ─────────────────────────────────────────────

type Job struct {
	ID          int64  `json:"id"`
	Queue       string `json:"queue"`
	Payload     string `json:"payload"`
	Attempts    int    `json:"attempts"`
	Status      string `json:"status"`
	Priority    int    `json:"priority"`
	AvailableAt int64  `json:"available_at"`
	StartedAt   int64  `json:"started_at"`
	FinishedAt  int64  `json:"finished_at"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

type Payload struct {
	JobType     string          `json:"job_type"`
	Data        json.RawMessage `json:"data"`
	TimeoutSec  int             `json:"timeout_sec"`  // 0 = 使用全局默认（60s）
	MaxAttempts int             `json:"max_attempts"` // 0 = 使用全局默认（MaxAttempts）
	Backoff     []int           `json:"backoff"`      // P3-1: 自定义重试延迟数组（秒），第 N 次重试用第 N 个值
}

func (j *Job) JobType() string {
	var p Payload
	json.Unmarshal([]byte(j.Payload), &p)
	return p.JobType
}

// DispatchOptions 投递任务的可选参数
type DispatchOptions struct {
	Priority int    // 1-10，默认 5
	DedupKey string // 去重 key，相同 key 的任务只保留一个（pending 状态）
}

// dispatchJobRaw 直接用已序列化的 payload 字符串投递任务（handleDispatch 使用）
func dispatchJobRaw(queue string, rawPayload string, delaySeconds int64, opts ...DispatchOptions) (int64, error) {
	now := time.Now().Unix()
	prio := 5
	var dedupKey *string
	if len(opts) > 0 {
		if opts[0].Priority >= 1 && opts[0].Priority <= 10 {
			prio = opts[0].Priority
		}
		if opts[0].DedupKey != "" {
			dedupKey = &opts[0].DedupKey
		}
	}
	res, err := db.Exec(
		`INSERT OR IGNORE INTO jobs (queue, payload, status, priority, dedup_key, available_at, created_at, updated_at)
		 VALUES (?, ?, 'pending', ?, ?, ?, ?, ?)`,
		queue, rawPayload, prio, dedupKey, now+delaySeconds, now, now,
	)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return 0, nil
	}
	return id, nil
}

// dispatchJob 构造 payload 并投递任务（内部 Go 代码使用，支持 timeout_sec / max_attempts）
func dispatchJob(queue string, jobType string, data interface{}, delaySeconds int64, opts ...DispatchOptions) (int64, error) {
	dataBytes, _ := json.Marshal(data)
	type fullPayload struct {
		JobType string          `json:"job_type"`
		Data    json.RawMessage `json:"data"`
	}
	payloadBytes, _ := json.Marshal(fullPayload{JobType: jobType, Data: dataBytes})
	return dispatchJobRaw(queue, string(payloadBytes), delaySeconds, opts...)
}

func reserve(queue string) (*Job, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	row := tx.QueryRow(
		`SELECT id, queue, payload, attempts, status, priority, available_at, started_at, finished_at, created_at, updated_at
		 FROM jobs WHERE queue=? AND status='pending' AND available_at<=?
		 ORDER BY priority DESC, id ASC LIMIT 1`,
		queue, now,
	)
	var j Job
	if err := row.Scan(&j.ID, &j.Queue, &j.Payload, &j.Attempts, &j.Status, &j.Priority,
		&j.AvailableAt, &j.StartedAt, &j.FinishedAt, &j.CreatedAt, &j.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if _, err = tx.Exec(
		`UPDATE jobs SET status='running', attempts=attempts+1, started_at=?, updated_at=? WHERE id=?`,
		now, now, j.ID,
	); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	j.Status = "running"
	j.Attempts++
	j.StartedAt = now
	return &j, nil
}

const (
	MaxAttempts         = 3    // 最多尝试次数，超过后进死信队列
	StaleJobTimeout     = 5 * 60 // 任务卡在 running 超过 5 分钟视为 stale
	RetryBaseDelaySec   = 10  // 指数退避基础延迟（秒）
	DefaultJobTimeoutSec = 60 // 单个任务默认执行超时（秒）
)

// workerWg 追踪所有正在执行的 GoWorker goroutine，优雅关闭时等待它们完成
var workerWg sync.WaitGroup

func markDone(id int64) {
	now := time.Now().Unix()
	db.Exec(`UPDATE jobs SET status='done', finished_at=?, updated_at=? WHERE id=?`, now, now, id)
	broadcastStats()
}

// handleJobFailure：失败时先判断是否还有重试机会
//   - attempts < MaxAttempts → 放回 pending，指数退避延迟
//   - attempts >= MaxAttempts → 进死信队列（failed_jobs）
func handleJobFailure(j *Job, reason string) {
	now := time.Now().Unix()

	// per-job MaxAttempts：从 payload 读取，0 则使用全局默认
	maxAttempts := MaxAttempts
	var pf Payload
	if err := json.Unmarshal([]byte(j.Payload), &pf); err == nil && pf.MaxAttempts > 0 {
		maxAttempts = pf.MaxAttempts
	}

	if j.Attempts < maxAttempts {
		// P3-1: 优先使用 per-job Backoff 数组，否则指数退避
		var delaySec int64
		if len(pf.Backoff) > 0 {
			idx := j.Attempts - 1
			if idx >= len(pf.Backoff) {
				idx = len(pf.Backoff) - 1
			}
			delaySec = int64(pf.Backoff[idx])
		} else {
			// 指数退避：第 N 次重试延迟 2^(N-1) * RetryBaseDelaySec 秒
			delaySec = int64(1<<uint(j.Attempts-1)) * RetryBaseDelaySec
		}
		availAt := now + delaySec
		db.Exec(`UPDATE jobs SET status='pending', available_at=?, updated_at=? WHERE id=?`,
			availAt, now, j.ID)
		log.Printf("[Queue] Job #%d will retry (attempt %d/%d) in %ds — reason: %s",
			j.ID, j.Attempts, maxAttempts, delaySec, reason)
	} else {
		// 超过最大重试次数，进死信队列
		jobType := j.JobType()
		db.Exec(`UPDATE jobs SET status='failed', finished_at=?, updated_at=? WHERE id=?`, now, now, j.ID)
		db.Exec(`INSERT INTO failed_jobs (queue, job_type, payload, attempts, exception, failed_at) VALUES (?,?,?,?,?,?)`,
			j.Queue, jobType, j.Payload, j.Attempts, reason, now)
		log.Printf("[Queue] Job #%d → DLQ after %d attempts — reason: %s", j.ID, j.Attempts, reason)
	}
	broadcastStats()
}

// markFailed 保留作为直接进 DLQ 的快捷方式（payload 解析失败等不可重试的错误）
func markFailed(j *Job, reason string) {
	handleJobFailure(j, reason)
}

// ─────────────────────────────────────────────
// WebSocket Worker Hub
// ─────────────────────────────────────────────

type WsJobMessage struct {
	Type    string `json:"type"`
	JobID   int64  `json:"job_id"`
	Queue   string `json:"queue"`
	JobType string `json:"job_type"`
	Payload string `json:"payload"`
}

type WsResultMessage struct {
	Type    string `json:"type"`
	JobID   int64  `json:"job_id"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
	Log     string `json:"log,omitempty"`
}

type WsControl struct {
	Type    string `json:"type"`
	Message string `json:"message,omitempty"`
}

type WsWorker struct {
	id           string
	queue        string
	send         chan []byte
	result       chan WsResultMessage
	done         chan struct{}
	kick         chan struct{} // 收到信号后主动断开连接（被踢）
	currentJobID int64        // 当前正在处理的任务 ID，断连时用于放回 pending
	idle         bool         // true = 空闲，可接受新任务
}

type WorkerHub struct {
	mu      sync.Mutex
	workers map[string]*WsWorker
	rrKeys  []string // round-robin 顺序
	rrIdx   int      // round-robin 当前索引
}

var hub = &WorkerHub{workers: make(map[string]*WsWorker)}

func (h *WorkerHub) register(w *WsWorker) {
	h.mu.Lock()
	defer h.mu.Unlock()
	w.idle = true
	w.kick = make(chan struct{}, 1) // 初始化 kick channel
	h.workers[w.id] = w
	h.rrKeys = append(h.rrKeys, w.id)
	log.Printf("[Hub] Worker registered: id=%s queue=%s total=%d", w.id, w.queue, len(h.workers))
}

// kickWorker 向指定 worker 发送 kick 信号，使其主动断开连接
// 返回 true 表示找到并发送了信号，false 表示 worker 不存在
func (h *WorkerHub) kickWorker(id string) bool {
	h.mu.Lock()
	w, ok := h.workers[id]
	h.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case w.kick <- struct{}{}:
	default: // 已有 kick 信号在队列中，忽略重复
	}
	return true
}

func (h *WorkerHub) unregister(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.workers, id)
	// 从 rrKeys 中移除
	for i, k := range h.rrKeys {
		if k == id {
			h.rrKeys = append(h.rrKeys[:i], h.rrKeys[i+1:]...)
			if h.rrIdx >= len(h.rrKeys) && h.rrIdx > 0 {
				h.rrIdx = 0
			}
			break
		}
	}
	log.Printf("[Hub] Worker unregistered: id=%s total=%d", id, len(h.workers))
}

func (h *WorkerHub) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.workers)
}

// countForQueue 返回指定队列的在线 worker 数量
func (h *WorkerHub) countForQueue(queue string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	count := 0
	for _, w := range h.workers {
		if w.queue == queue {
			count++
		}
	}
	return count
}

// activeQueues 返回当前所有有 WS Worker 连接的队列列表（去重）
func (h *WorkerHub) activeQueues() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	seen := map[string]bool{}
	var queues []string
	for _, w := range h.workers {
		if w.queue != "" && !seen[w.queue] {
			seen[w.queue] = true
			queues = append(queues, w.queue)
		}
	}
	return queues
}

func (h *WorkerHub) dispatchToWs(j *Job) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := len(h.rrKeys)
	if n == 0 {
		return false
	}
	// round-robin：从当前索引开始，遍历一圈找第一个 idle worker
	for i := 0; i < n; i++ {
		idx := (h.rrIdx + i) % n
		w, ok := h.workers[h.rrKeys[idx]]
		if !ok {
			continue
		}
		if !w.idle {
			continue // 该 worker 正忙，跳过
		}
		if w.queue != j.Queue && w.queue != "" {
			continue
		}
		msg := WsJobMessage{
			Type:    "job",
			JobID:   j.ID,
			Queue:   j.Queue,
			JobType: j.JobType(),
			Payload: j.Payload,
		}
		b, _ := json.Marshal(msg)
		select {
		case w.send <- b:
			w.idle = false          // 标记为忙碌
			w.currentJobID = j.ID   // 记录当前任务
			h.rrIdx = (idx + 1) % n // 下次从下一个开始
			return true
		default:
		}
	}
	return false
}

// WS dispatcher: polls DB and sends to connected WS workers
// startWsDispatcher 为指定队列启动一个 WS 派发 goroutine
func startWsDispatcher(queue string) {
	go func() {
		for {
			select {
			case <-stopGoWorker:
				return
			default:
			}
			if hub.countForQueue(queue) == 0 {
				time.Sleep(500 * time.Millisecond)
				continue
			}
			j, err := reserve(queue)
			if err != nil {
				log.Printf("[WsDispatcher] reserve error: %v", err)
				time.Sleep(2 * time.Second)
				continue
			}
			if j == nil {
				time.Sleep(500 * time.Millisecond)
				continue
			}
			if !hub.dispatchToWs(j) {
				db.Exec(`UPDATE jobs SET status='pending', attempts=attempts-1, updated_at=? WHERE id=?`,
					time.Now().Unix(), j.ID)
				time.Sleep(500 * time.Millisecond)
			}
		}
	}()
	log.Printf("[WsDispatcher] Started for queue=%s", queue)
}

// startDynamicWsDispatcher 监控 hub，当有新队列的 WS Worker 连接时自动启动 dispatcher
func startDynamicWsDispatcher() {
	go func() {
		started := map[string]bool{}
		for {
			select {
			case <-stopGoWorker:
				return
			default:
			}
			// 获取当前所有已连接 worker 的队列列表
			queues := hub.activeQueues()
			for _, q := range queues {
				if !started[q] {
					startWsDispatcher(q)
					started[q] = true
					log.Printf("[DynamicDispatcher] Auto-started dispatcher for queue=%s", q)
				}
			}
			time.Sleep(1 * time.Second)
		}
	}()
	log.Printf("[DynamicDispatcher] Started — watching for new queues")
}

// ─────────────────────────────────────────────
// WebSocket Handler
// ─────────────────────────────────────────────

func handleWorkerWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgradeWS(w, r)
	if err != nil {
		log.Printf("[WS] upgrade error: %v", err)
		return
	}
	defer conn.Close()

	queue := r.URL.Query().Get("queue")
	if queue == "" {
		queue = "default"
	}
	workerID := fmt.Sprintf("ws-%d", time.Now().UnixNano())

	worker := &WsWorker{
		id:     workerID,
		queue:  queue,
		send:   make(chan []byte, 1), // size=1：一次只处理一个任务，防止积压
		result: make(chan WsResultMessage, 1),
		done:   make(chan struct{}),
	}
	hub.register(worker)
	defer hub.unregister(workerID)

	welcome, _ := json.Marshal(WsControl{
		Type:    "connected",
		Message: fmt.Sprintf("Worker %s connected, queue=%s", workerID, queue),
	})
	conn.WriteMessage(1, welcome)

	// read goroutine
	go func() {
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				close(worker.done)
				return
			}
			var result WsResultMessage
			if err := json.Unmarshal(msg, &result); err != nil {
				log.Printf("[WS] invalid message: %s", msg)
				continue
			}
			if result.Type == "result" {
				select {
				case worker.result <- result:
				default:
				}
			}
		}
	}()

	// rescheduleCurrentJob 把当前任务放回 pending（断连/超时时调用）
	rescheduleCurrentJob := func(reason string) {
		hub.mu.Lock()
		jobID := worker.currentJobID
		hub.mu.Unlock()
		if jobID > 0 {
			db.Exec(`UPDATE jobs SET status='pending', attempts=attempts-1, updated_at=? WHERE id=? AND status='running'`,
				time.Now().Unix(), jobID)
			log.Printf("[WS] Worker %s %s — job #%d put back to pending", workerID, reason, jobID)
		}
	}

	for {
		select {
		case <-worker.kick:
			// 被 dashboard 踢掉：发送关闭帧（opcode=8, code=1000），然后断开
			// RFC 6455: close frame payload = 2-byte status code (big-endian) + reason
			closePayload := []byte{0x03, 0xe8} // 1000 = normal closure
			closePayload = append(closePayload, []byte("kicked by admin")...)
			conn.WriteMessage(8, closePayload)
			rescheduleCurrentJob("kicked by admin")
			log.Printf("[WS] Worker %s kicked by admin", workerID)
			return
		case <-worker.done:
			rescheduleCurrentJob("disconnected")
			log.Printf("[WS] Worker %s disconnected", workerID)
			return
		case jobMsg := <-worker.send:
			if err := conn.WriteMessage(1, jobMsg); err != nil {
				log.Printf("[WS] write error: %v", err)
				rescheduleCurrentJob("write error")
				return
			}
			log.Printf("[WS] Job sent to worker %s", workerID)
			select {
			case result := <-worker.result:
				// 处理完毕，标记 worker 为 idle
				hub.mu.Lock()
				worker.idle = true
				worker.currentJobID = 0
				hub.mu.Unlock()
				if result.Success {
					markDone(result.JobID)
					log.Printf("[WS] Job #%d done by worker %s: %s", result.JobID, workerID, result.Log)
					ack, _ := json.Marshal(WsControl{Type: "ack", Message: fmt.Sprintf("job #%d done", result.JobID)})
					conn.WriteMessage(1, ack)
				} else {
					row := db.QueryRow(`SELECT id,queue,payload,attempts,status,priority,available_at,started_at,finished_at,created_at,updated_at FROM jobs WHERE id=?`, result.JobID)
					var j Job
					row.Scan(&j.ID, &j.Queue, &j.Payload, &j.Attempts, &j.Status, &j.Priority, &j.AvailableAt, &j.StartedAt, &j.FinishedAt, &j.CreatedAt, &j.UpdatedAt)
					markFailed(&j, result.Error)
					log.Printf("[WS] Job #%d failed by worker %s: %s", result.JobID, workerID, result.Error)
				}
			case <-worker.kick:
				// 任务处理中被踢：先把任务放回 pending，再关闭连接
				closePayload := []byte{0x03, 0xe8}
				closePayload = append(closePayload, []byte("kicked by admin")...)
				conn.WriteMessage(8, closePayload)
				rescheduleCurrentJob("kicked by admin while processing")
				log.Printf("[WS] Worker %s kicked by admin while processing", workerID)
				return
			case <-time.After(30 * time.Second):
				rescheduleCurrentJob("30s timeout")
				log.Printf("[WS] Worker %s timed out, disconnecting", workerID)
				return
			case <-worker.done:
				rescheduleCurrentJob("disconnected while processing")
				return
			}
		}
	}
}

// ─────────────────────────────────────────────
// Stale Job Reaper
// ─────────────────────────────────────────────

// startStaleJobReaper 每 30s 扫描一次：
// 把 status='running' 且超过 StaleJobTimeout 秒未更新的任务放回 pending
// 这能恢复服务崩溃、WS 断连未处理等场景下卡住的任务
func startStaleJobReaper() {
	go func() {
		for {
			time.Sleep(30 * time.Second)
			threshold := time.Now().Unix() - StaleJobTimeout
			res, err := db.Exec(
				`UPDATE jobs SET status='pending', attempts=attempts-1, updated_at=?
				 WHERE status='running' AND updated_at<?`,
				time.Now().Unix(), threshold,
			)
			if err != nil {
				log.Printf("[Reaper] error: %v", err)
				continue
			}
			n, _ := res.RowsAffected()
			if n > 0 {
				log.Printf("[Reaper] Rescued %d stale job(s) back to pending", n)
			}
		}
	}()
	log.Printf("[Reaper] Started — stale threshold=%ds, scan interval=30s", StaleJobTimeout)
}

// ─────────────────────────────────────────────
// Fallback Go Worker
// ─────────────────────────────────────────────

// processJobInternal 执行任务，支持 per-job 超时（context.WithTimeout）
// timeout 优先级：payload.TimeoutSec > DefaultJobTimeoutSec
func processJobInternal(j *Job) error {
	var p Payload
	if err := json.Unmarshal([]byte(j.Payload), &p); err != nil {
		return fmt.Errorf("invalid payload: %w", err)
	}

	// 确定超时时长
	timeoutSec := p.TimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = DefaultJobTimeoutSec
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()

	log.Printf("[GoWorker] Processing job #%d type=%s queue=%s attempt=%d timeout=%ds",
		j.ID, p.JobType, j.Queue, j.Attempts, timeoutSec)

	// 用 channel 包装实际执行，以便 context 超时可以中断
	errCh := make(chan error, 1)
	go func() {
		errCh <- runJob(ctx, &p)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return fmt.Errorf("job timed out after %ds", timeoutSec)
	}
}

// runJob 执行具体的任务逻辑，接受 context 以支持取消
func runJob(ctx context.Context, p *Payload) error {
	switch p.JobType {
	case "send_email":
		var d struct {
			To      string `json:"to"`
			Subject string `json:"subject"`
		}
		json.Unmarshal(p.Data, &d)
		select {
		case <-time.After(1 * time.Second):
		case <-ctx.Done():
			return ctx.Err()
		}
		log.Printf("[GoWorker] ✉  Email sent to %s: %s", d.To, d.Subject)
	case "generate_report":
		var d struct{ Name string `json:"name"` }
		json.Unmarshal(p.Data, &d)
		select {
		case <-time.After(2 * time.Second):
		case <-ctx.Done():
			return ctx.Err()
		}
		log.Printf("[GoWorker] 📊 Report generated: %s", d.Name)
	case "resize_image":
		var d struct{ URL string `json:"url"` }
		json.Unmarshal(p.Data, &d)
		select {
		case <-time.After(1500 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
		log.Printf("[GoWorker] 🖼  Image resized: %s", d.URL)
	case "fail_job":
		return fmt.Errorf("intentional failure for testing")
	default:
		return fmt.Errorf("unknown job type: %s", p.JobType)
	}
	return nil
}

// stopGoWorker 用于通知 GoWorker 停止接受新任务
var stopGoWorker = make(chan struct{})

func startGoWorker(queue string, concurrency int) {
	sem := make(chan struct{}, concurrency)
	go func() {
		for {
			select {
			case <-stopGoWorker:
				log.Printf("[GoWorker] queue=%s stopping, waiting for running jobs...", queue)
				return
			default:
			}
			sem <- struct{}{}
			go func() {
				defer func() { <-sem }()
				j, err := reserve(queue)
				if err != nil {
					log.Printf("[GoWorker] reserve error: %v", err)
					time.Sleep(2 * time.Second)
					return
				}
				if j == nil {
					time.Sleep(500 * time.Millisecond)
					return
				}
				// 追踪 running job，优雅关闭时等待
				workerWg.Add(1)
				defer workerWg.Done()
				// P2-1: 限流检查，超限则放回 pending
				if checkRateLimit(j) {
					return
				}
				if err := processJobInternal(j); err != nil {
					markFailed(j, err.Error())
					log.Printf("[GoWorker] job #%d failed: %v", j.ID, err)
				} else {
					// P2-3: 任务链支持，读取 next_job 字段
					var nextJob string
					db.QueryRow(`SELECT next_job FROM jobs WHERE id=?`, j.ID).Scan(&nextJob)
					markDoneWithChain(j.ID, nextJob)
					log.Printf("[GoWorker] job #%d done ✓", j.ID)
				}
			}()
		}
	}()
	log.Printf("[GoWorker/fallback] Started queue=%s concurrency=%d", queue, concurrency)
}

// ─────────────────────────────────────────────
// HTTP API
// ─────────────────────────────────────────────

func jsonResp(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func handleCancelJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", 405)
		return
	}
	// 从 URL 路径中提取 job id：/api/jobs/123
	path := r.URL.Path // e.g. "/api/jobs/123"
	parts := strings.Split(strings.TrimPrefix(path, "/api/jobs/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		jsonResp(w, 400, map[string]string{"error": "missing job id"})
		return
	}
	var id int64
	if _, err := fmt.Sscanf(parts[0], "%d", &id); err != nil || id <= 0 {
		jsonResp(w, 400, map[string]string{"error": "invalid job id"})
		return
	}
	// 只允许取消 pending 状态的任务
	res, err := db.Exec(`UPDATE jobs SET status='cancelled', updated_at=? WHERE id=? AND status='pending'`,
		time.Now().Unix(), id)
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		// 检查任务是否存在
		var status string
		err := db.QueryRow(`SELECT status FROM jobs WHERE id=?`, id).Scan(&status)
		if err == sql.ErrNoRows {
			jsonResp(w, 404, map[string]string{"error": "job not found"})
		} else {
			jsonResp(w, 409, map[string]string{"error": fmt.Sprintf("cannot cancel job in status '%s'", status)})
		}
		return
	}
	log.Printf("[API] Job #%d cancelled", id)
	jsonResp(w, 200, map[string]interface{}{"job_id": id, "status": "cancelled"})
}

func handleDispatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	// 限制请求体最大 1MB，防止恶意大 payload 撑爆内存
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req struct {
		Queue       string          `json:"queue"`
		JobType     string          `json:"job_type"`
		Data        json.RawMessage `json:"data"`
		Delay       int64           `json:"delay"`
		Priority    int             `json:"priority"`
		DedupKey    string          `json:"dedup_key"`
		TimeoutSec  int             `json:"timeout_sec"`
		MaxAttempts int             `json:"max_attempts"`
		NextJob      *ChainedJob     `json:"next_job,omitempty"` // P2-3: 任务链
		Backoff      []int           `json:"backoff,omitempty"`  // P3-1: 自定义重试延迟
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if strings.Contains(err.Error(), "http: request body too large") {
			jsonResp(w, 413, map[string]string{"error": "payload too large (max 1MB)"})
			return
		}
		jsonResp(w, 400, map[string]string{"error": err.Error()})
		return
	}
	if req.Queue == "" {
		req.Queue = "default"
	}
	// 把 per-job 控制字段合并进 payload data，以便 worker 读取
	type enrichedData struct {
		*json.RawMessage
		TimeoutSec  int `json:"timeout_sec,omitempty"`
		MaxAttempts int `json:"max_attempts,omitempty"`
	}
	// 直接把 timeout_sec / max_attempts 写入顶层 payload（Payload struct 字段）
	type fullPayload struct {
		JobType     string          `json:"job_type"`
		Data        json.RawMessage `json:"data"`
		TimeoutSec  int             `json:"timeout_sec,omitempty"`
		MaxAttempts int             `json:"max_attempts,omitempty"`
		Backoff     []int           `json:"backoff,omitempty"` // P3-1
	}
	payloadBytes, _ := json.Marshal(fullPayload{
		JobType:     req.JobType,
		Data:        req.Data,
		TimeoutSec:  req.TimeoutSec,
		MaxAttempts: req.MaxAttempts,
		Backoff:     req.Backoff,
	})
	id, err := dispatchJobRaw(req.Queue, string(payloadBytes), req.Delay, DispatchOptions{
		Priority: req.Priority,
		DedupKey: req.DedupKey,
	})
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	if id == 0 {
		// 被去重忽略
		jsonResp(w, 200, map[string]interface{}{"job_id": 0, "queue": req.Queue, "status": "deduplicated", "message": "job skipped: duplicate dedup_key"})
		return
	}
	// P2-3: 写入 next_job 到 jobs 表
	if req.NextJob != nil {
		nextJobBytes, _ := json.Marshal(req.NextJob)
		db.Exec(`UPDATE jobs SET next_job=? WHERE id=?`, string(nextJobBytes), id)
	}
	jsonResp(w, 201, map[string]interface{}{"job_id": id, "queue": req.Queue, "status": "pending"})
}

func handleListJobs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	queue, status, limit := q.Get("queue"), q.Get("status"), "50"
	if q.Get("limit") != "" {
		limit = q.Get("limit")
	}
	query := `SELECT id,queue,payload,attempts,status,priority,available_at,started_at,finished_at,created_at,updated_at FROM jobs WHERE 1=1`
	args := []interface{}{}
	if queue != "" {
		query += " AND queue=?"
		args = append(args, queue)
	}
	if status != "" {
		query += " AND status=?"
		args = append(args, status)
	}
	query += " ORDER BY id DESC LIMIT " + limit
	rows, err := db.Query(query, args...)
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	defer rows.Close()
	jobs := []Job{}
	for rows.Next() {
		var j Job
		rows.Scan(&j.ID, &j.Queue, &j.Payload, &j.Attempts, &j.Status, &j.Priority,
			&j.AvailableAt, &j.StartedAt, &j.FinishedAt, &j.CreatedAt, &j.UpdatedAt)
		jobs = append(jobs, j)
	}
	jsonResp(w, 200, jobs)
}

func handleStats(w http.ResponseWriter, r *http.Request) {
	rows, err := db.Query(`SELECT status, COUNT(*) FROM jobs GROUP BY status`)
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	defer rows.Close()
	stats := map[string]interface{}{}
	for rows.Next() {
		var s string
		var c int
		rows.Scan(&s, &c)
		stats[s] = c
	}
	var failed int
	db.QueryRow(`SELECT COUNT(*) FROM failed_jobs`).Scan(&failed)
	stats["failed_jobs_table"] = failed
	stats["ws_workers"] = hub.count()
	stats["version"] = "1.0.0"
	stats["queues"] = []string{"default", "emails"}

	// 平均耗时：只统计 finished_at > 0 且 started_at > 0 的已完成任务
	var avgMs sql.NullFloat64
	db.QueryRow(`SELECT AVG((finished_at - started_at) * 1000) FROM jobs
		WHERE status='done' AND started_at > 0 AND finished_at > 0`).Scan(&avgMs)
	if avgMs.Valid {
		stats["avg_duration_ms"] = int64(avgMs.Float64)
	} else {
		stats["avg_duration_ms"] = 0
	}

	// 各队列 pending 数量
	qRows, _ := db.Query(`SELECT queue, COUNT(*) FROM jobs WHERE status='pending' GROUP BY queue`)
	if qRows != nil {
		defer qRows.Close()
		queuePending := map[string]int{}
		for qRows.Next() {
			var q string
			var c int
			qRows.Scan(&q, &c)
			queuePending[q] = c
		}
		stats["queue_pending"] = queuePending
	}

	jsonResp(w, 200, stats)
}

func handleClearFailed(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", 405)
		return
	}
	db.Exec(`DELETE FROM failed_jobs`)
	jsonResp(w, 200, map[string]string{"message": "failed jobs cleared"})
}

func handleRetryFailed(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	now := time.Now().Unix()
	db.Exec(`UPDATE jobs SET status='pending', available_at=?, updated_at=? WHERE status='failed'`, now, now)
	jsonResp(w, 200, map[string]string{"message": "failed jobs re-queued"})
}

var startTime = time.Now()

// ─────────────────────────────────────────────
// SSE — Server-Sent Events 实时推送
// ─────────────────────────────────────────────

// sseHub 管理所有 SSE 客户端连接
var sseHub = &SSEHub{clients: make(map[chan string]struct{})}

type SSEHub struct {
	mu      sync.Mutex
	clients map[chan string]struct{}
}

func (h *SSEHub) subscribe() chan string {
	ch := make(chan string, 4)
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *SSEHub) unsubscribe(ch chan string) {
	h.mu.Lock()
	delete(h.clients, ch)
	h.mu.Unlock()
}

// Broadcast 向所有 SSE 客户端广播一条消息（非阻塞）
func (h *SSEHub) Broadcast(data string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.clients {
		select {
		case ch <- data:
		default: // 客户端消费太慢，丢弃
		}
	}
}

// handleSSE 处理 GET /api/events，返回 SSE 流
func handleSSE(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", 500)
		return
	}

	ch := sseHub.subscribe()
	defer sseHub.unsubscribe(ch)

	// 立即推送一次当前 stats
	go func() { sseHub.Broadcast("refresh") }()

	for {
		select {
		case msg := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", msg)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// broadcastStats 在任务状态变化时广播 SSE 事件
func broadcastStats() {
	sseHub.Broadcast("refresh")
}

// handleMetrics 输出 Prometheus text format 指标
// 指标：jobs_total{status,queue}、ws_workers_total、avg_duration_ms、uptime_seconds
func handleMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

	// jobs_total by status
	rows, _ := db.Query(`SELECT status, COUNT(*) FROM jobs GROUP BY status`)
	if rows != nil {
		defer rows.Close()
		fmt.Fprintln(w, "# HELP jobs_total Total number of jobs by status")
		fmt.Fprintln(w, "# TYPE jobs_total gauge")
		for rows.Next() {
			var s string
			var c int
			rows.Scan(&s, &c)
			fmt.Fprintf(w, `jobs_total{status=%q} %d`+"\n", s, c)
		}
	}

	// jobs_total by queue+status
	qRows, _ := db.Query(`SELECT queue, status, COUNT(*) FROM jobs GROUP BY queue, status`)
	if qRows != nil {
		defer qRows.Close()
		fmt.Fprintln(w, "# HELP jobs_by_queue_total Total jobs by queue and status")
		fmt.Fprintln(w, "# TYPE jobs_by_queue_total gauge")
		for qRows.Next() {
			var q, s string
			var c int
			qRows.Scan(&q, &s, &c)
			fmt.Fprintf(w, `jobs_by_queue_total{queue=%q,status=%q} %d`+"\n", q, s, c)
		}
	}

	// failed_jobs_total (DLQ)
	var dlq int
	db.QueryRow(`SELECT COUNT(*) FROM failed_jobs`).Scan(&dlq)
	fmt.Fprintln(w, "# HELP failed_jobs_total Total jobs in dead-letter queue")
	fmt.Fprintln(w, "# TYPE failed_jobs_total gauge")
	fmt.Fprintf(w, "failed_jobs_total %d\n", dlq)

	// ws_workers_total
	fmt.Fprintln(w, "# HELP ws_workers_total Currently connected WebSocket workers")
	fmt.Fprintln(w, "# TYPE ws_workers_total gauge")
	fmt.Fprintf(w, "ws_workers_total %d\n", hub.count())

	// avg_duration_ms
	var avgMs sql.NullFloat64
	db.QueryRow(`SELECT AVG((finished_at - started_at) * 1000) FROM jobs
		WHERE status='done' AND started_at > 0 AND finished_at > 0`).Scan(&avgMs)
	fmt.Fprintln(w, "# HELP job_avg_duration_ms Average job execution duration in milliseconds")
	fmt.Fprintln(w, "# TYPE job_avg_duration_ms gauge")
	if avgMs.Valid {
		fmt.Fprintf(w, "job_avg_duration_ms %.2f\n", avgMs.Float64*1000)
	} else {
		fmt.Fprintln(w, "job_avg_duration_ms 0")
	}

	// uptime_seconds
	fmt.Fprintln(w, "# HELP uptime_seconds Seconds since process start")
	fmt.Fprintln(w, "# TYPE uptime_seconds counter")
	fmt.Fprintf(w, "uptime_seconds %.0f\n", time.Since(startTime).Seconds())
}

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	// 检查 DB 连通性
	dbStatus := "ok"
	if err := db.Ping(); err != nil {
		dbStatus = "error: " + err.Error()
	}
	uptime := int64(time.Since(startTime).Seconds())
	status := "ok"
	if dbStatus != "ok" {
		status = "degraded"
	}
	w.Header().Set("Content-Type", "application/json")
	if status != "ok" {
		w.WriteHeader(503)
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":     status,
		"db":         dbStatus,
		"uptime_sec": uptime,
		"ws_workers": hub.count(),
		"version":    "1.0.0",
	})
}

// handleMe GET /api/me — 返回当前登录用户信息
func handleMe(w http.ResponseWriter, r *http.Request) {
	username := ""
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		if s := getSession(cookie.Value); s != nil {
			username = s.username
		}
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"username":%q}`, username)
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, indexHTML)
}

// ─────────────────────────────────────────────
// main
// ─────────────────────────────────────────────

func main() {
	initLogger()
	initDB()
	initBatchDB()

	startStaleJobReaper()
	startHeartbeatReaper()
	startSessionReaper()
	// 内置 GoWorker 已移除 — 任务由外部 WS Worker 处理
	// 启动动态 WsDispatcher（自动感知所有有 WS Worker 连接的队列）
	startDynamicWsDispatcher()

	mux := http.NewServeMux()
	// API 鉴权中间件：读取环境变量 API_KEY，若设置则要求请求携带 X-API-Key header
	apiKey := os.Getenv("API_KEY")
	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if apiKey != "" && r.Header.Get("X-API-Key") != apiKey {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(401)
				w.Write([]byte(`{"error":"unauthorized: invalid or missing X-API-Key"}`))
				return
			}
			h(w, r)
		}
	}

	cors := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET,POST,DELETE,OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type,X-API-Key")
			if r.Method == http.MethodOptions {
				w.WriteHeader(204)
				return
			}
			h(w, r)
		}
	}

	mux.HandleFunc("/", requireLogin(handleIndex))
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			handleLoginSubmit(w, r)
		} else {
			handleLoginPage(w, r)
		}
	})
	mux.HandleFunc("/logout", handleLogout)
	mux.HandleFunc("/healthz", handleHealthz)
	mux.HandleFunc("/metrics", handleMetrics)
	mux.HandleFunc("/ws/worker", handleWorkerWS)
	mux.HandleFunc("/api/jobs", cors(auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			handleDispatch(w, r)
		} else {
			handleListJobs(w, r)
		}
	})))
	// /api/jobs/:id — DELETE to cancel a pending job
	mux.HandleFunc("/api/jobs/", cors(auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			handleCancelJob(w, r)
		} else {
			http.NotFound(w, r)
		}
	})))
	mux.HandleFunc("/api/rate-limits", cors(auth(handleRateLimits)))
	mux.HandleFunc("/api/workers", cors(auth(handleWorkersList)))
	mux.HandleFunc("/api/workers/", cors(auth(func(w http.ResponseWriter, r *http.Request) {
		// DELETE /api/workers/{id}  → kick worker
		// POST   /api/workers/{id}/heartbeat → heartbeat
		if r.Method == http.MethodDelete {
			handleKickWorker(w, r)
		} else {
			handleWorkerHeartbeat(w, r)
		}
	})))
	mux.HandleFunc("/api/batches", cors(auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			handleBatches(w, r)
		} else if r.Method == http.MethodGet {
			handleBatchList(w, r)
		} else {
			http.Error(w, "method not allowed", 405)
		}
	})))
	mux.HandleFunc("/api/batches/", cors(auth(handleBatchStatus)))
	mux.HandleFunc("/api/backend", cors(auth(handleBackendInfo)))    // P3-2: 后端信息
	mux.HandleFunc("/api/autoscale", cors(auth(handleAutoScale)))    // P3-3: 动态扩缩容
	mux.HandleFunc("/api/stats", cors(auth(handleStats)))
	mux.HandleFunc("/api/me", requireLogin(handleMe))
	mux.HandleFunc("/api/events", requireLogin(handleSSE))
	mux.HandleFunc("/api/jobs/failed", cors(auth(handleClearFailed)))
	mux.HandleFunc("/api/jobs/retry-failed", cors(auth(handleRetryFailed)))

	srv := &http.Server{Addr: ":8080", Handler: mux}
	go func() {
		log.Println("[HTTP] Listening on :8080")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("[HTTP] Shutting down gracefully...")

	// 1. 停止 GoWorker 接受新任务
	close(stopGoWorker)

	// 2. 等待所有 running job 完成（最多 DefaultJobTimeoutSec + 5s）
	waitDone := make(chan struct{})
	go func() {
		workerWg.Wait()
		close(waitDone)
	}()
	shutdownTimeout := time.Duration(DefaultJobTimeoutSec+5) * time.Second
	select {
	case <-waitDone:
		log.Println("[HTTP] All running jobs finished ✓")
	case <-time.After(shutdownTimeout):
		log.Printf("[HTTP] Shutdown timeout (%s), forcing exit", shutdownTimeout)
	}

	// 3. 优雅关闭 HTTP server（5s 超时）
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("[HTTP] Server forced to shutdown: %v", err)
	}
	log.Println("[HTTP] Server stopped ✓")
}
