package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// ─────────────────────────────────────────────
// P2-1: 任务限流 (Rate Limiting)
// 同一 job_type 每分钟最多执行 N 次，超限放回 pending 延迟重试
// ─────────────────────────────────────────────

// RateLimitConfig 限流配置
type RateLimitConfig struct {
	JobType    string `json:"job_type"`
	MaxPerMin  int    `json:"max_per_min"`
}

// rateLimiter 滑动窗口限流器
type rateLimiter struct {
	mu      sync.Mutex
	windows map[string][]int64 // job_type -> 执行时间戳列表（最近1分钟）
	limits  map[string]int     // job_type -> 每分钟最大执行次数
}

var globalRateLimiter = &rateLimiter{
	windows: make(map[string][]int64),
	limits:  make(map[string]int),
}

// SetLimit 设置某 job_type 的限流规则
func (rl *rateLimiter) SetLimit(jobType string, maxPerMin int) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.limits[jobType] = maxPerMin
	log.Printf("[RateLimit] Set limit for job_type=%s: %d/min", jobType, maxPerMin)
}

// RemoveLimit 移除某 job_type 的限流规则
func (rl *rateLimiter) RemoveLimit(jobType string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	delete(rl.limits, jobType)
	delete(rl.windows, jobType)
	log.Printf("[RateLimit] Removed limit for job_type=%s", jobType)
}

// Allow 检查是否允许执行，允许则记录时间戳，返回 true；超限返回 false
func (rl *rateLimiter) Allow(jobType string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	limit, ok := rl.limits[jobType]
	if !ok || limit <= 0 {
		return true // 无限流规则，直接放行
	}
	now := time.Now().Unix()
	cutoff := now - 60 // 1分钟滑动窗口
	// 清理过期时间戳
	ts := rl.windows[jobType]
	valid := ts[:0]
	for _, t := range ts {
		if t > cutoff {
			valid = append(valid, t)
		}
	}
	rl.windows[jobType] = valid
	if len(valid) >= limit {
		return false // 超限
	}
	rl.windows[jobType] = append(rl.windows[jobType], now)
	return true
}

// GetStats 返回当前限流状态
func (rl *rateLimiter) GetStats() map[string]interface{} {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now().Unix()
	cutoff := now - 60
	result := map[string]interface{}{}
	for jobType, limit := range rl.limits {
		ts := rl.windows[jobType]
		count := 0
		for _, t := range ts {
			if t > cutoff {
				count++
			}
		}
		result[jobType] = map[string]interface{}{
			"max_per_min":  limit,
			"used_per_min": count,
			"remaining":    limit - count,
		}
	}
	return result
}

// handleRateLimits GET/POST /api/rate-limits
func handleRateLimits(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		jsonResp(w, 200, globalRateLimiter.GetStats())
	case http.MethodPost:
		var cfg RateLimitConfig
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			jsonResp(w, 400, map[string]string{"error": err.Error()})
			return
		}
		if cfg.JobType == "" {
			jsonResp(w, 400, map[string]string{"error": "job_type required"})
			return
		}
		if cfg.MaxPerMin <= 0 {
			globalRateLimiter.RemoveLimit(cfg.JobType)
			jsonResp(w, 200, map[string]string{"message": "rate limit removed", "job_type": cfg.JobType})
			return
		}
		globalRateLimiter.SetLimit(cfg.JobType, cfg.MaxPerMin)
		jsonResp(w, 200, map[string]interface{}{
			"message":     "rate limit set",
			"job_type":    cfg.JobType,
			"max_per_min": cfg.MaxPerMin,
		})
	case http.MethodDelete:
		jobType := r.URL.Query().Get("job_type")
		if jobType == "" {
			jsonResp(w, 400, map[string]string{"error": "job_type required"})
			return
		}
		globalRateLimiter.RemoveLimit(jobType)
		jsonResp(w, 200, map[string]string{"message": "rate limit removed", "job_type": jobType})
	default:
		http.Error(w, "method not allowed", 405)
	}
}

// checkRateLimit 在 processJobInternal 前调用，超限则 reschedule 并返回 true
func checkRateLimit(j *Job) bool {
	var p Payload
	if err := json.Unmarshal([]byte(j.Payload), &p); err != nil {
		return false
	}
	if globalRateLimiter.Allow(p.JobType) {
		return false // 未超限，正常执行
	}
	// 超限：放回 pending，延迟 60s 重试
	now := time.Now().Unix()
	db.Exec(`UPDATE jobs SET status='pending', attempts=attempts-1, available_at=?, updated_at=? WHERE id=?`,
		now+60, now, j.ID)
	log.Printf("[RateLimit] job #%d type=%s rate limited, rescheduled in 60s", j.ID, p.JobType)
	return true
}

// ─────────────────────────────────────────────
// P2-2: Worker 心跳 / 存活检测
// WS Worker 定期发送 heartbeat，服务端记录最后心跳时间，超时断开
// ─────────────────────────────────────────────

// heartbeatTimeout WS Worker 心跳超时时间（秒）
const heartbeatTimeout = 60

// workerLastSeen 记录每个 worker 最后一次心跳时间
var workerLastSeen = struct {
	mu   sync.Mutex
	seen map[string]int64 // workerID -> unix timestamp
}{seen: make(map[string]int64)}

// updateHeartbeat 更新 worker 心跳时间
func updateHeartbeat(workerID string) {
	workerLastSeen.mu.Lock()
	defer workerLastSeen.mu.Unlock()
	workerLastSeen.seen[workerID] = time.Now().Unix()
}

// removeHeartbeat 移除 worker 心跳记录
func removeHeartbeat(workerID string) {
	workerLastSeen.mu.Lock()
	defer workerLastSeen.mu.Unlock()
	delete(workerLastSeen.seen, workerID)
}

// getWorkerHeartbeats 返回所有 worker 的心跳状态
func getWorkerHeartbeats() map[string]interface{} {
	workerLastSeen.mu.Lock()
	defer workerLastSeen.mu.Unlock()
	now := time.Now().Unix()
	result := map[string]interface{}{}
	for id, ts := range workerLastSeen.seen {
		age := now - ts
		result[id] = map[string]interface{}{
			"last_seen_sec": age,
			"alive":         age < heartbeatTimeout,
		}
	}
	return result
}

// startHeartbeatReaper 定期检查 worker 心跳，超时则从 hub 注销
func startHeartbeatReaper() {
	go func() {
		for {
			time.Sleep(15 * time.Second)
			now := time.Now().Unix()
			workerLastSeen.mu.Lock()
			var stale []string
			for id, ts := range workerLastSeen.seen {
				if now-ts > heartbeatTimeout {
					stale = append(stale, id)
				}
			}
			workerLastSeen.mu.Unlock()
			for _, id := range stale {
				log.Printf("[Heartbeat] Worker %s timed out (no heartbeat for >%ds), unregistering", id, heartbeatTimeout)
				hub.unregister(id)
				removeHeartbeat(id)
			}
		}
	}()
	log.Printf("[Heartbeat] Reaper started — timeout=%ds, scan interval=15s", heartbeatTimeout)
}

// handleWorkerHeartbeat POST /api/workers/:id/heartbeat
func handleWorkerHeartbeat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	// 从 URL 提取 worker ID: /api/workers/{id}/heartbeat
	path := r.URL.Path // /api/workers/ws-123/heartbeat
	// 简单解析
	workerID := ""
	parts := splitPath(path)
	// parts: ["api", "workers", "{id}", "heartbeat"]
	if len(parts) >= 3 {
		workerID = parts[2]
	}
	if workerID == "" {
		jsonResp(w, 400, map[string]string{"error": "worker id required"})
		return
	}
	updateHeartbeat(workerID)
	jsonResp(w, 200, map[string]interface{}{
		"worker_id": workerID,
		"timestamp": time.Now().Unix(),
		"message":   "heartbeat received",
	})
}

// handleWorkersList GET /api/workers
// handleKickWorker DELETE /api/workers/{id} — 踢掉指定 worker
func handleKickWorker(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", 405)
		return
	}
	parts := splitPath(r.URL.Path)
	// parts: ["api", "workers", "{id}"]
	if len(parts) < 3 {
		jsonResp(w, 400, map[string]string{"error": "worker id required"})
		return
	}
	workerID := parts[2]
	if hub.kickWorker(workerID) {
		log.Printf("[API] Worker %s kicked via dashboard", workerID)
		jsonResp(w, 200, map[string]interface{}{
			"worker_id": workerID,
			"message":   "worker kicked, connection will close shortly",
		})
	} else {
		jsonResp(w, 404, map[string]string{"error": "worker not found: " + workerID})
	}
}

func handleWorkersList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	beats := getWorkerHeartbeats()
	hub.mu.Lock()
	workers := []map[string]interface{}{}
	for id, wk := range hub.workers {
		info := map[string]interface{}{
			"id":             id,
			"queue":          wk.queue,
			"idle":           wk.idle,
			"current_job_id": wk.currentJobID,
		}
		if hb, ok := beats[id]; ok {
			info["heartbeat"] = hb
		}
		workers = append(workers, info)
	}
	hub.mu.Unlock()
	jsonResp(w, 200, workers)
}

// splitPath 把 URL path 按 "/" 分割，去掉空串
func splitPath(path string) []string {
	var parts []string
	for _, p := range splitStr(path, "/") {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return parts
}

func splitStr(s, sep string) []string {
	var result []string
	start := 0
	for i := 0; i <= len(s)-len(sep); i++ {
		if s[i:i+len(sep)] == sep {
			result = append(result, s[start:i])
			start = i + len(sep)
		}
	}
	result = append(result, s[start:])
	return result
}

// ─────────────────────────────────────────────
// P2-3: 任务链 (Job Chaining)
// 任务完成后自动触发下一个任务，支持链式失败处理
// ─────────────────────────────────────────────

// ChainedJob 链式任务定义
type ChainedJob struct {
	Queue       string          `json:"queue"`
	JobType     string          `json:"job_type"`
	Data        json.RawMessage `json:"data"`
	TimeoutSec  int             `json:"timeout_sec,omitempty"`
	MaxAttempts int             `json:"max_attempts,omitempty"`
	Priority    int             `json:"priority,omitempty"`
}

// dispatchChainedJob 在任务完成后触发链式任务
func dispatchChainedJob(nextJobJSON string) {
	if nextJobJSON == "" {
		return
	}
	var next ChainedJob
	if err := json.Unmarshal([]byte(nextJobJSON), &next); err != nil {
		log.Printf("[Chain] Failed to parse next_job: %v", err)
		return
	}
	if next.Queue == "" {
		next.Queue = "default"
	}
	type fullPayload struct {
		JobType     string          `json:"job_type"`
		Data        json.RawMessage `json:"data"`
		TimeoutSec  int             `json:"timeout_sec,omitempty"`
		MaxAttempts int             `json:"max_attempts,omitempty"`
	}
	payloadBytes, _ := json.Marshal(fullPayload{
		JobType:     next.JobType,
		Data:        next.Data,
		TimeoutSec:  next.TimeoutSec,
		MaxAttempts: next.MaxAttempts,
	})
	prio := next.Priority
	if prio == 0 {
		prio = 5
	}
	id, err := dispatchJobRaw(next.Queue, string(payloadBytes), 0, DispatchOptions{Priority: prio})
	if err != nil {
		log.Printf("[Chain] Failed to dispatch next_job: %v", err)
		return
	}
	log.Printf("[Chain] Dispatched chained job #%d type=%s queue=%s", id, next.JobType, next.Queue)
}

// markDoneWithChain 完成任务并触发链式任务
func markDoneWithChain(id int64, nextJobJSON string) {
	now := time.Now().Unix()
	db.Exec(`UPDATE jobs SET status='done', finished_at=?, updated_at=? WHERE id=?`, now, now, id)
	broadcastStats()
	// 触发链式任务
	if nextJobJSON != "" {
		dispatchChainedJob(nextJobJSON)
	}
	// 检查批次完成
	checkBatchCompletion(id)
}

// ─────────────────────────────────────────────
// P2-4: 任务批次 (Job Batching)
// 一组任务作为批次，追踪整体进度，全部完成后触发回调
// ─────────────────────────────────────────────

// BatchStatus 批次状态
type BatchStatus struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Total       int    `json:"total"`
	Done        int    `json:"done"`
	Failed      int    `json:"failed"`
	Pending     int    `json:"pending"`
	Status      string `json:"status"` // pending/running/done/failed
	ThenJobJSON    string `json:"then_job,omitempty"`
	CatchJobJSON   string `json:"catch_job,omitempty"`
	FinallyJobJSON string `json:"finally_job,omitempty"`
	CreatedAt      int64  `json:"created_at"`
	FinishedAt     int64  `json:"finished_at,omitempty"`
}

// initBatchDB 初始化批次相关表
func initBatchDB() {
	schema := `
	CREATE TABLE IF NOT EXISTS job_batches (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL DEFAULT '',
		total INTEGER NOT NULL DEFAULT 0,
		then_job TEXT NOT NULL DEFAULT '',
		catch_job TEXT NOT NULL DEFAULT '',
		finally_job TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT 'pending',
		created_at INTEGER NOT NULL,
		finished_at INTEGER NOT NULL DEFAULT 0
	);`
	if _, err := db.Exec(schema); err != nil {
		log.Printf("[Batch] initBatchDB error: %v", err)
		return
	}
	// 为 jobs 表添加 batch_id 和 next_job 列（迁移）
	migrations := []string{
		`ALTER TABLE jobs ADD COLUMN batch_id INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE jobs ADD COLUMN next_job TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE job_batches ADD COLUMN catch_job TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE job_batches ADD COLUMN finally_job TEXT NOT NULL DEFAULT ''`,
	}
	for _, m := range migrations {
		if _, err := db.Exec(m); err != nil {
			if !containsStr(err.Error(), "duplicate column") {
				log.Printf("[Batch] migration warning: %v", err)
			}
		}
	}
	log.Println("[Batch] DB initialized ✓")
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStrHelper(s, sub))
}

func containsStrHelper(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// createBatch 创建一个批次，返回 batch_id
func createBatch(name string, thenJobJSON string, catchJobJSON string, finallyJobJSON string) (int64, error) {
	now := time.Now().Unix()
	res, err := db.Exec(
		`INSERT INTO job_batches (name, total, then_job, catch_job, finally_job, status, created_at) VALUES (?, 0, ?, ?, ?, 'pending', ?)`,
		name, thenJobJSON, catchJobJSON, finallyJobJSON, now,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// addJobToBatch 将任务关联到批次，并更新批次 total
func addJobToBatch(jobID int64, batchID int64) error {
	_, err := db.Exec(`UPDATE jobs SET batch_id=? WHERE id=?`, batchID, jobID)
	if err != nil {
		return err
	}
	_, err = db.Exec(`UPDATE job_batches SET total=total+1 WHERE id=?`, batchID)
	return err
}

// checkBatchCompletion 检查任务所属批次是否全部完成
func checkBatchCompletion(jobID int64) {
	// 查询该任务的 batch_id
	var batchID int64
	row := db.QueryRow(`SELECT batch_id FROM jobs WHERE id=?`, jobID)
	if err := row.Scan(&batchID); err != nil || batchID == 0 {
		return
	}
	// 统计批次内各状态任务数
	var total, done, failed int
	db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE batch_id=?`, batchID).Scan(&total)
	db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE batch_id=? AND status='done'`, batchID).Scan(&done)
	db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE batch_id=? AND status IN ('failed','cancelled')`, batchID).Scan(&failed)

	if done+failed < total {
		// 批次未完成
		db.Exec(`UPDATE job_batches SET status='running' WHERE id=? AND status='pending'`, batchID)
		return
	}
	// 批次全部完成
	now := time.Now().Unix()
	finalStatus := "done"
	if failed > 0 {
		finalStatus = "failed"
	}
	db.Exec(`UPDATE job_batches SET status=?, finished_at=? WHERE id=?`, finalStatus, now, batchID)
	log.Printf("[Batch] Batch #%d completed: status=%s total=%d done=%d failed=%d",
		batchID, finalStatus, total, done, failed)

	// 触发回调
	var thenJob, catchJob, finallyJob string
	db.QueryRow(`SELECT then_job, catch_job, finally_job FROM job_batches WHERE id=?`, batchID).Scan(&thenJob, &catchJob, &finallyJob)

	// then_job：全部成功时触发
	if finalStatus == "done" && thenJob != "" {
		dispatchChainedJob(thenJob)
		log.Printf("[Batch] Batch #%d then_job dispatched", batchID)
	}
	// catch_job：有任何失败时触发
	if finalStatus == "failed" && catchJob != "" {
		dispatchChainedJob(catchJob)
		log.Printf("[Batch] Batch #%d catch_job dispatched", batchID)
	}
	// finally_job：无论成功失败都触发
	if finallyJob != "" {
		dispatchChainedJob(finallyJob)
		log.Printf("[Batch] Batch #%d finally_job dispatched", batchID)
	}
	broadcastStats()
}

// getBatchStatus 获取批次状态
func getBatchStatus(batchID int64) (*BatchStatus, error) {
	var b BatchStatus
	// 用单次 JOIN 查询替代 3 次独立查询，避免 SQLite 锁竞争
	const q = `
		SELECT
			b.id, b.name, b.total, b.then_job, b.catch_job, b.finally_job,
			b.status, b.created_at, b.finished_at,
			COUNT(CASE WHEN j.status='done' THEN 1 END)                    AS done_cnt,
			COUNT(CASE WHEN j.status IN ('failed','cancelled') THEN 1 END) AS fail_cnt
		FROM job_batches b
		LEFT JOIN jobs j ON j.batch_id = b.id
		WHERE b.id = ?
		GROUP BY b.id`
	row := db.QueryRow(q, batchID)
	if err := row.Scan(
		&b.ID, &b.Name, &b.Total, &b.ThenJobJSON, &b.CatchJobJSON, &b.FinallyJobJSON,
		&b.Status, &b.CreatedAt, &b.FinishedAt,
		&b.Done, &b.Failed,
	); err != nil {
		return nil, err
	}
	b.Pending = b.Total - b.Done - b.Failed
	if b.Pending < 0 {
		b.Pending = 0
	}
	return &b, nil
}

// handleBatches POST /api/batches — 创建批次并派发任务
func handleBatches(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var req struct {
		Name       string       `json:"name"`
		Jobs       []ChainedJob `json:"jobs"`
		ThenJob    *ChainedJob  `json:"then_job,omitempty"`
		CatchJob   *ChainedJob  `json:"catch_job,omitempty"`
		FinallyJob *ChainedJob  `json:"finally_job,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResp(w, 400, map[string]string{"error": err.Error()})
		return
	}
	if len(req.Jobs) == 0 {
		jsonResp(w, 400, map[string]string{"error": "jobs array required"})
		return
	}

	// 序列化 then_job / catch_job / finally_job
	thenJobJSON := ""
	if req.ThenJob != nil {
		b, _ := json.Marshal(req.ThenJob)
		thenJobJSON = string(b)
	}
	catchJobJSON := ""
	if req.CatchJob != nil {
		b, _ := json.Marshal(req.CatchJob)
		catchJobJSON = string(b)
	}
	finallyJobJSON := ""
	if req.FinallyJob != nil {
		b, _ := json.Marshal(req.FinallyJob)
		finallyJobJSON = string(b)
	}

	// 创建批次
	batchID, err := createBatch(req.Name, thenJobJSON, catchJobJSON, finallyJobJSON)
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}

	// 派发批次内所有任务
	jobIDs := []int64{}
	for _, job := range req.Jobs {
		if job.Queue == "" {
			job.Queue = "default"
		}
		type fullPayload struct {
			JobType     string          `json:"job_type"`
			Data        json.RawMessage `json:"data"`
			TimeoutSec  int             `json:"timeout_sec,omitempty"`
			MaxAttempts int             `json:"max_attempts,omitempty"`
		}
		payloadBytes, _ := json.Marshal(fullPayload{
			JobType:     job.JobType,
			Data:        job.Data,
			TimeoutSec:  job.TimeoutSec,
			MaxAttempts: job.MaxAttempts,
		})
		prio := job.Priority
		if prio == 0 {
			prio = 5
		}
		jobID, err := dispatchJobRaw(job.Queue, string(payloadBytes), 0, DispatchOptions{Priority: prio})
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": fmt.Sprintf("dispatch job failed: %v", err)})
			return
		}
		if err := addJobToBatch(jobID, batchID); err != nil {
			log.Printf("[Batch] addJobToBatch error: %v", err)
		}
		jobIDs = append(jobIDs, jobID)
	}

	jsonResp(w, 201, map[string]interface{}{
		"batch_id": batchID,
		"name":     req.Name,
		"total":    len(req.Jobs),
		"job_ids":  jobIDs,
		"status":   "pending",
	})
}

// handleBatchStatus GET /api/batches/:id
func handleBatchStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	// 解析 batch ID
	parts := splitPath(r.URL.Path)
	if len(parts) < 2 {
		jsonResp(w, 400, map[string]string{"error": "batch id required"})
		return
	}
	batchID, err := strconv.ParseInt(parts[len(parts)-1], 10, 64)
	if err != nil {
		jsonResp(w, 400, map[string]string{"error": "invalid batch id"})
		return
	}
	status, err := getBatchStatus(batchID)
	if err != nil {
		jsonResp(w, 404, map[string]string{"error": "batch not found"})
		return
	}
	jsonResp(w, 200, status)
}

// handleBatchList GET /api/batches
func handleBatchList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	// 用单次 LEFT JOIN + GROUP BY 替代 N 次子查询，避免 SQLite 锁竞争导致超时
	const q = `
		SELECT
			b.id, b.name, b.total, b.then_job, b.catch_job, b.finally_job,
			b.status, b.created_at, b.finished_at,
			COUNT(CASE WHEN j.status='done' THEN 1 END)                    AS done_cnt,
			COUNT(CASE WHEN j.status IN ('failed','cancelled') THEN 1 END) AS fail_cnt
		FROM job_batches b
		LEFT JOIN jobs j ON j.batch_id = b.id
		GROUP BY b.id
		ORDER BY b.id DESC
		LIMIT 50`
	rows, err := db.Query(q)
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	defer rows.Close()
	batches := []BatchStatus{}
	for rows.Next() {
		var b BatchStatus
		rows.Scan(
			&b.ID, &b.Name, &b.Total, &b.ThenJobJSON, &b.CatchJobJSON, &b.FinallyJobJSON,
			&b.Status, &b.CreatedAt, &b.FinishedAt,
			&b.Done, &b.Failed,
		)
		b.Pending = b.Total - b.Done - b.Failed
		if b.Pending < 0 {
			b.Pending = 0
		}
		batches = append(batches, b)
	}
	jsonResp(w, 200, batches)
}
