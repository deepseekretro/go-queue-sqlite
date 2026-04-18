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

// =============================================================================
// P3-A: Queue Pause / Resume
// POST /api/queues/:name/pause   → 暂停队列（dispatcher 不再取任务）
// POST /api/queues/:name/resume  → 恢复队列
// GET  /api/queues               → 列出所有队列及暂停状态
// =============================================================================

var queuePauseState = struct {
	mu      sync.RWMutex
	paused  map[string]bool
}{paused: make(map[string]bool)}

// isQueuePaused 检查队列是否被暂停（dispatcher 调用）
func isQueuePaused(queue string) bool {
	queuePauseState.mu.RLock()
	defer queuePauseState.mu.RUnlock()
	return queuePauseState.paused[queue]
}

func pauseQueue(queue string) {
	queuePauseState.mu.Lock()
	defer queuePauseState.mu.Unlock()
	queuePauseState.paused[queue] = true
	log.Printf("[Queue] Queue %q paused", queue)
}

func resumeQueue(queue string) {
	queuePauseState.mu.Lock()
	defer queuePauseState.mu.Unlock()
	delete(queuePauseState.paused, queue)
	log.Printf("[Queue] Queue %q resumed", queue)
}

// handleQueuePauseResume POST /api/queues/:name/pause|resume
func handleQueuePauseResume(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	parts := splitPath(r.URL.Path) // ["api","queues","{name}","pause"|"resume"]
	if len(parts) < 4 {
		jsonResp(w, 400, map[string]string{"error": "invalid path"})
		return
	}
	queueName := parts[2]
	action := parts[3]
	switch action {
	case "pause":
		pauseQueue(queueName)
		jsonResp(w, 200, map[string]interface{}{"queue": queueName, "paused": true})
	case "resume":
		resumeQueue(queueName)
		jsonResp(w, 200, map[string]interface{}{"queue": queueName, "paused": false})
	default:
		jsonResp(w, 400, map[string]string{"error": "action must be pause or resume"})
	}
}

// handleQueueList GET /api/queues — 列出所有已知队列及暂停状态
func handleQueueList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	// 从 hub 获取活跃队列
	activeQs := hub.activeQueues()
	// 从 DB 获取所有有任务的队列
	rows, _ := db.Query(`SELECT DISTINCT queue FROM jobs`)
	dbQueues := map[string]bool{}
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var q string
			rows.Scan(&q)
			dbQueues[q] = true
		}
	}
	for _, q := range activeQs {
		dbQueues[q] = true
	}

	queuePauseState.mu.RLock()
	defer queuePauseState.mu.RUnlock()

	result := []map[string]interface{}{}
	for q := range dbQueues {
		result = append(result, map[string]interface{}{
			"name":   q,
			"paused": queuePauseState.paused[q],
		})
	}
	jsonResp(w, 200, result)
}

// =============================================================================
// P3-B: Cron Scheduler
// 周期性任务调度，cron 表达式风格（仅支持 @every <duration> 简化语法）
// POST   /api/crons        → 创建定时任务
// GET    /api/crons        → 列出所有定时任务
// DELETE /api/crons/:id   → 删除定时任务
// =============================================================================

type CronJob struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Queue       string `json:"queue"`
	JobType     string `json:"job_type"`
	DataJSON    string `json:"data"`
	Every       string `json:"every"`        // e.g. "30s", "5m", "1h"
	MaxAttempts int    `json:"max_attempts"`
	Priority    int    `json:"priority"`
	Enabled     bool   `json:"enabled"`
	LastRunAt   int64  `json:"last_run_at"`
	NextRunAt   int64  `json:"next_run_at"`
	CreatedAt   int64  `json:"created_at"`
}

// initCronDB 初始化 cron_jobs 表
func initCronDB() {
	schema := `
	CREATE TABLE IF NOT EXISTS cron_jobs (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		name        TEXT    NOT NULL DEFAULT '',
		queue       TEXT    NOT NULL DEFAULT 'default',
		job_type    TEXT    NOT NULL,
		data        TEXT    NOT NULL DEFAULT '{}',
		every       TEXT    NOT NULL,
		max_attempts INTEGER NOT NULL DEFAULT 3,
		priority    INTEGER NOT NULL DEFAULT 5,
		enabled     INTEGER NOT NULL DEFAULT 1,
		last_run_at INTEGER NOT NULL DEFAULT 0,
		next_run_at INTEGER NOT NULL DEFAULT 0,
		created_at  INTEGER NOT NULL
	);`
	if _, err := db.Exec(schema); err != nil {
		log.Printf("[Cron] initCronDB error: %v", err)
		return
	}
	log.Println("[Cron] DB initialized ✓")
}

// parseDuration 解析 "30s" / "5m" / "1h" 等
func parseDuration(s string) (time.Duration, error) {
	// 扩展支持 d（天）和 w（周），Go 标准库不支持
	if len(s) >= 2 {
		suffix := s[len(s)-1]
		num := s[:len(s)-1]
		var n int64
		if _, err := fmt.Sscanf(num, "%d", &n); err == nil {
			switch suffix {
			case 'd':
				return time.Duration(n) * 24 * time.Hour, nil
			case 'w':
				return time.Duration(n) * 7 * 24 * time.Hour, nil
			}
		}
	}
	return time.ParseDuration(s)
}

// handleCrons GET/POST /api/crons
func handleCrons(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		rows, err := db.Query(`SELECT id,name,queue,job_type,data,every,max_attempts,priority,enabled,last_run_at,next_run_at,created_at FROM cron_jobs ORDER BY id DESC`)
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": err.Error()})
			return
		}
		defer rows.Close()
		crons := []CronJob{}
		for rows.Next() {
			var c CronJob
			var enabled int
			rows.Scan(&c.ID, &c.Name, &c.Queue, &c.JobType, &c.DataJSON, &c.Every,
				&c.MaxAttempts, &c.Priority, &enabled, &c.LastRunAt, &c.NextRunAt, &c.CreatedAt)
			c.Enabled = enabled == 1
			crons = append(crons, c)
		}
		jsonResp(w, 200, crons)

	case http.MethodPost:
		var req struct {
			Name        string          `json:"name"`
			Queue       string          `json:"queue"`
			JobType     string          `json:"job_type"`
			Data        json.RawMessage `json:"data"`
			Every       string          `json:"every"`
			MaxAttempts int             `json:"max_attempts"`
			Priority    int             `json:"priority"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonResp(w, 400, map[string]string{"error": err.Error()})
			return
		}
		if req.JobType == "" {
			jsonResp(w, 400, map[string]string{"error": "job_type required"})
			return
		}
		if req.Every == "" {
			jsonResp(w, 400, map[string]string{"error": "every required (e.g. '30s','5m','1h')"})
			return
		}
		d, err := parseDuration(req.Every)
		if err != nil || d <= 0 {
			jsonResp(w, 400, map[string]string{"error": "invalid every: " + req.Every})
			return
		}
		if req.Queue == "" {
			req.Queue = "default"
		}
		if req.MaxAttempts == 0 {
			req.MaxAttempts = 3
		}
		if req.Priority == 0 {
			req.Priority = 5
		}
		dataStr := "{}"
		if len(req.Data) > 0 {
			dataStr = string(req.Data)
		}
		now := time.Now().Unix()
		nextRun := now + int64(d.Seconds())
		res, err := dbExecResult(
			`INSERT INTO cron_jobs (name,queue,job_type,data,every,max_attempts,priority,enabled,next_run_at,created_at) VALUES (?,?,?,?,?,?,?,1,?,?)`,
			req.Name, req.Queue, req.JobType, dataStr, req.Every, req.MaxAttempts, req.Priority, nextRun, now,
		)
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": err.Error()})
			return
		}
		id, _ := res.LastInsertId()
		log.Printf("[Cron] Created cron #%d job_type=%s every=%s", id, req.JobType, req.Every)
		jsonResp(w, 201, map[string]interface{}{
			"id": id, "job_type": req.JobType, "every": req.Every, "next_run_at": nextRun,
		})

	default:
		http.Error(w, "method not allowed", 405)
	}
}

// handleCronItem DELETE/PATCH /api/crons/:id
func handleCronItem(w http.ResponseWriter, r *http.Request) {
	parts := splitPath(r.URL.Path) // ["api","crons","{id}"]
	if len(parts) < 3 {
		jsonResp(w, 400, map[string]string{"error": "cron id required"})
		return
	}
	id, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		jsonResp(w, 400, map[string]string{"error": "invalid id"})
		return
	}
	switch r.Method {
	case http.MethodPut:
		// 完整更新 cron
		var req struct {
			Name        string          `json:"name"`
			Queue       string          `json:"queue"`
			JobType     string          `json:"job_type"`
			Data        json.RawMessage `json:"data"`
			Every       string          `json:"every"`
			MaxAttempts int             `json:"max_attempts"`
			Priority    int             `json:"priority"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonResp(w, 400, map[string]string{"error": err.Error()})
			return
		}
		if req.Every == "" {
			jsonResp(w, 400, map[string]string{"error": "every required"})
			return
		}
		if _, err := parseDuration(req.Every); err != nil {
			jsonResp(w, 400, map[string]string{"error": "invalid every: " + req.Every})
			return
		}
		if req.MaxAttempts == 0 {
			req.MaxAttempts = 3
		}
		if req.Priority == 0 {
			req.Priority = 5
		}
		data := req.Data
		if len(data) == 0 {
			data = json.RawMessage("{}")
		}
		err = dbExec(
			`UPDATE cron_jobs SET name=?,queue=?,job_type=?,data=?,every=?,max_attempts=?,priority=? WHERE id=?`,
			req.Name, req.Queue, req.JobType, string(data), req.Every, req.MaxAttempts, req.Priority, id,
		)
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": err.Error()})
			return
		}
		jsonResp(w, 200, map[string]interface{}{"id": id, "updated": true})
	case http.MethodDelete:
		dbExec(`DELETE FROM cron_jobs WHERE id=?`, id) //nolint
		jsonResp(w, 200, map[string]string{"message": "cron deleted"})
	case http.MethodPatch:
		// toggle enabled
		var req struct {
			Enabled *bool `json:"enabled"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if req.Enabled != nil {
			v := 0
			if *req.Enabled {
				v = 1
			}
			dbExec(`UPDATE cron_jobs SET enabled=? WHERE id=?`, v, id) //nolint
			jsonResp(w, 200, map[string]interface{}{"id": id, "enabled": *req.Enabled})
		} else {
			jsonResp(w, 400, map[string]string{"error": "enabled field required"})
		}
	default:
		http.Error(w, "method not allowed", 405)
	}
}

// startCronScheduler 启动 cron 调度器 goroutine，每 10s 扫描一次
func startCronScheduler() {
	go func() {
		for {
			time.Sleep(10 * time.Second)
			runDueCrons()
		}
	}()
	log.Println("[Cron] Scheduler started — scan interval=10s")
}

// runDueCrons 检查并触发到期的 cron 任务
func runDueCrons() {
	now := time.Now().Unix()
	rows, err := db.Query(
		`SELECT id,queue,job_type,data,every,max_attempts,priority FROM cron_jobs WHERE enabled=1 AND next_run_at<=?`, now,
	)
	if err != nil {
		log.Printf("[Cron] query error: %v", err)
		return
	}
	defer rows.Close()

	type cronRow struct {
		id, maxAttempts, priority int64
		queue, jobType, dataStr, every string
	}
	var due []cronRow
	for rows.Next() {
		var cr cronRow
		rows.Scan(&cr.id, &cr.queue, &cr.jobType, &cr.dataStr, &cr.every, &cr.maxAttempts, &cr.priority)
		due = append(due, cr)
	}
	rows.Close()

	for _, cr := range due {
		d, err := parseDuration(cr.every)
		if err != nil {
			continue
		}
		nextRun := now + int64(d.Seconds())

		// 构造 payload
		type fullPayload struct {
			JobType     string          `json:"job_type"`
			Data        json.RawMessage `json:"data"`
			MaxAttempts int             `json:"max_attempts,omitempty"`
		}
		payloadBytes, _ := json.Marshal(fullPayload{
			JobType:     cr.jobType,
			Data:        json.RawMessage(cr.dataStr),
			MaxAttempts: int(cr.maxAttempts),
		})
		jobID, err := dispatchJobRaw(cr.queue, string(payloadBytes), 0, DispatchOptions{Priority: int(cr.priority)})
		if err != nil {
			log.Printf("[Cron] dispatch error for cron #%d: %v", cr.id, err)
		} else {
			log.Printf("[Cron] Cron #%d fired → job #%d (type=%s)", cr.id, jobID, cr.jobType)
		}

		// 更新 last_run_at 和 next_run_at
		dbExec(`UPDATE cron_jobs SET last_run_at=?, next_run_at=? WHERE id=?`, now, nextRun, cr.id) //nolint
	}
}
