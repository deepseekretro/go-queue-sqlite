package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

// =============================================================================
// P3-A: Queue Pause / Resume
// POST /api/queues/:name/pause  → 暂停队列（dispatcher 不再取任务）
// POST /api/queues/:name/resume → 恢复队列
// GET  /api/queues              → 列出所有队列及暂停状态
// =============================================================================

var queuePauseState = struct {
	mu     sync.RWMutex
	paused map[string]bool
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
		for rows.Next() {
			var q string
			rows.Scan(&q)
			dbQueues[q] = true
		}
		rows.Close() // 立即释放连接
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
// P3-B: Cron Scheduler（增强版，对齐 Laravel Schedule 特性）
//
// 新增特性：
//   1. withoutOverlapping  — 防重叠：上次触发的 job 仍在 pending/running 时跳过
//   2. 标准 cron 表达式    — expr 字段支持 "* * * * *"（5 字段），every 仍可用
//   3. timezone            — 配合 cron 表达式按指定时区计算下次触发时间
//   4. 触发历史记录        — cron_run_logs 表，记录每次触发的 job_id / fired_at
//   5. one_time            — 触发一次后自动 disabled（类似 runOnce）
//   6. max_runs            — 触发 N 次后自动 disabled（0 = 不限）
//   7. expires_at          — 超过此时间后不再触发（0 = 不限）
//   8. 立即触发 API        — POST /api/crons/:id/trigger（不影响 next_run_at）
//   9. tags 透传           — cron 定义的 tags 自动写入投递的 job
//  10. run_count           — 已触发次数统计
// =============================================================================

// CronJob 定时任务结构体
type CronJob struct {
	ID                int64    `json:"id"`
	Name              string   `json:"name"`
	Queue             string   `json:"queue"`
	JobType           string   `json:"job_type"`
	DataJSON          string   `json:"data"`
	Every             string   `json:"every"`              // 间隔语法：30s/5m/1h/1d/1w（与 expr 二选一）
	Expr              string   `json:"expr"`               // 标准 cron 表达式：* * * * *（5 字段）
	Timezone          string   `json:"timezone"`           // 时区，如 "Asia/Shanghai"，默认 UTC
	MaxAttempts       int      `json:"max_attempts"`
	Priority          int      `json:"priority"`
	Tags              []string `json:"tags,omitempty"`     // 投递 job 时自动附加的标签
	WithoutOverlapping bool    `json:"without_overlapping"` // 防重叠：上次 job 未完成则跳过
	OneTime           bool     `json:"one_time"`            // 触发一次后自动 disabled
	MaxRuns           int      `json:"max_runs"`            // 最大触发次数（0=不限）
	RunCount          int      `json:"run_count"`           // 已触发次数
	ExpiresAt         int64    `json:"expires_at"`          // 到期时间 Unix 时间戳（0=不限）
	Enabled           bool     `json:"enabled"`
	LastRunAt         int64    `json:"last_run_at"`
	NextRunAt         int64    `json:"next_run_at"`
	CreatedAt         int64    `json:"created_at"`
}

// initCronDB 初始化 cron_jobs 表（含迁移）
func initCronDB() {
	schema := `
	CREATE TABLE IF NOT EXISTS cron_jobs (
		id                  INTEGER PRIMARY KEY AUTOINCREMENT,
		name                TEXT    NOT NULL DEFAULT '',
		queue               TEXT    NOT NULL DEFAULT 'default',
		job_type            TEXT    NOT NULL,
		data                TEXT    NOT NULL DEFAULT '{}',
		every               TEXT    NOT NULL DEFAULT '',
		expr                TEXT    NOT NULL DEFAULT '',
		timezone            TEXT    NOT NULL DEFAULT '',
		max_attempts        INTEGER NOT NULL DEFAULT 3,
		priority            INTEGER NOT NULL DEFAULT 5,
		tags                TEXT    NOT NULL DEFAULT '',
		without_overlapping INTEGER NOT NULL DEFAULT 0,
		one_time            INTEGER NOT NULL DEFAULT 0,
		max_runs            INTEGER NOT NULL DEFAULT 0,
		run_count           INTEGER NOT NULL DEFAULT 0,
		expires_at          INTEGER NOT NULL DEFAULT 0,
		enabled             INTEGER NOT NULL DEFAULT 1,
		last_run_at         INTEGER NOT NULL DEFAULT 0,
		next_run_at         INTEGER NOT NULL DEFAULT 0,
		created_at          INTEGER NOT NULL
	);`
	if _, err := db.Exec(schema); err != nil {
		log.Printf("[Cron] initCronDB error: %v", err)
		return
	}

	// 迁移：为旧表补充新字段
	cronMigrations := []string{
		`ALTER TABLE cron_jobs ADD COLUMN expr                TEXT    NOT NULL DEFAULT ''`,
		`ALTER TABLE cron_jobs ADD COLUMN timezone            TEXT    NOT NULL DEFAULT ''`,
		`ALTER TABLE cron_jobs ADD COLUMN tags                TEXT    NOT NULL DEFAULT ''`,
		`ALTER TABLE cron_jobs ADD COLUMN without_overlapping INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE cron_jobs ADD COLUMN one_time            INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE cron_jobs ADD COLUMN max_runs            INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE cron_jobs ADD COLUMN run_count           INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE cron_jobs ADD COLUMN expires_at          INTEGER NOT NULL DEFAULT 0`,
	}
	for _, m := range cronMigrations {
		if _, err := db.Exec(m); err != nil {
			if !strings.Contains(err.Error(), "duplicate column") {
				log.Printf("[Cron] migration warning: %v", err)
			}
		}
	}

	// 触发历史记录表
	runLogSchema := `
	CREATE TABLE IF NOT EXISTS cron_run_logs (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		cron_id    INTEGER NOT NULL,
		job_id     INTEGER NOT NULL DEFAULT 0,
		fired_at   INTEGER NOT NULL,
		skipped    INTEGER NOT NULL DEFAULT 0,
		skip_reason TEXT   NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_cron_run_logs_cron_id ON cron_run_logs(cron_id);`
	if _, err := db.Exec(runLogSchema); err != nil {
		log.Printf("[Cron] initCronRunLogs error: %v", err)
		return
	}

	log.Println("[Cron] DB initialized ✓")
}

// parseDuration 解析 "30s" / "5m" / "1h" / "1d" / "1w"
func parseDuration(s string) (time.Duration, error) {
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

// calcNextRun 计算下次触发时间
// expr 优先（标准 cron 表达式），其次 every（间隔）
func calcNextRun(expr, every, timezone string, from time.Time) (int64, error) {
	if expr != "" {
		loc := time.UTC
		if timezone != "" {
			if l, err := time.LoadLocation(timezone); err == nil {
				loc = l
			}
		}
		parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
		schedule, err := parser.Parse(expr)
		if err != nil {
			return 0, fmt.Errorf("invalid cron expr %q: %w", expr, err)
		}
		next := schedule.Next(from.In(loc))
		return next.Unix(), nil
	}
	if every != "" {
		d, err := parseDuration(every)
		if err != nil || d <= 0 {
			return 0, fmt.Errorf("invalid every %q: %w", every, err)
		}
		return from.Unix() + int64(d.Seconds()), nil
	}
	return 0, fmt.Errorf("either expr or every is required")
}

// cronReqBody 创建/更新 cron 的请求体
type cronReqBody struct {
	Name               string          `json:"name"`
	Queue              string          `json:"queue"`
	JobType            string          `json:"job_type"`
	Data               json.RawMessage `json:"data"`
	Every              string          `json:"every"`
	Expr               string          `json:"expr"`
	Timezone           string          `json:"timezone"`
	MaxAttempts        int             `json:"max_attempts"`
	Priority           int             `json:"priority"`
	Tags               []string        `json:"tags"`
	WithoutOverlapping bool            `json:"without_overlapping"`
	OneTime            bool            `json:"one_time"`
	MaxRuns            int             `json:"max_runs"`
	ExpiresAt          int64           `json:"expires_at"`
}

func (r *cronReqBody) applyDefaults() {
	if r.Queue == "" {
		r.Queue = "default"
	}
	if r.MaxAttempts == 0 {
		r.MaxAttempts = 3
	}
	if r.Priority == 0 {
		r.Priority = 5
	}
	if len(r.Data) == 0 {
		r.Data = json.RawMessage("{}")
	}
}

// scanCronRow 从 DB 行扫描 CronJob
func scanCronRow(rows interface {
	Scan(...interface{}) error
}) (CronJob, error) {
	var c CronJob
	var enabled, withoutOverlapping, oneTime int
	var tagsStr string
	err := rows.Scan(
		&c.ID, &c.Name, &c.Queue, &c.JobType, &c.DataJSON,
		&c.Every, &c.Expr, &c.Timezone,
		&c.MaxAttempts, &c.Priority, &tagsStr,
		&withoutOverlapping, &oneTime,
		&c.MaxRuns, &c.RunCount, &c.ExpiresAt,
		&enabled, &c.LastRunAt, &c.NextRunAt, &c.CreatedAt,
	)
	if err != nil {
		return c, err
	}
	c.Enabled = enabled == 1
	c.WithoutOverlapping = withoutOverlapping == 1
	c.OneTime = oneTime == 1
	if tagsStr != "" {
		c.Tags = strings.Split(tagsStr, ",")
	}
	return c, nil
}

const cronSelectCols = `id,name,queue,job_type,data,every,expr,timezone,
max_attempts,priority,tags,without_overlapping,one_time,
max_runs,run_count,expires_at,enabled,last_run_at,next_run_at,created_at`

// handleCrons GET/POST /api/crons
func handleCrons(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		rows, err := db.Query(`SELECT ` + cronSelectCols + ` FROM cron_jobs ORDER BY id DESC`)
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": err.Error()})
			return
		}
		defer rows.Close()
		cronList := []CronJob{}
		for rows.Next() {
			c, err := scanCronRow(rows)
			if err != nil {
				continue
			}
			cronList = append(cronList, c)
		}
		jsonResp(w, 200, cronList)

	case http.MethodPost:
		var req cronReqBody
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonResp(w, 400, map[string]string{"error": err.Error()})
			return
		}
		if req.JobType == "" {
			jsonResp(w, 400, map[string]string{"error": "job_type required"})
			return
		}
		if req.Every == "" && req.Expr == "" {
			jsonResp(w, 400, map[string]string{"error": "either every or expr is required"})
			return
		}
		req.applyDefaults()

		now := time.Now()
		nextRun, err := calcNextRun(req.Expr, req.Every, req.Timezone, now)
		if err != nil {
			jsonResp(w, 400, map[string]string{"error": err.Error()})
			return
		}

		tagsStr := strings.Join(req.Tags, ",")
		woInt := boolToInt(req.WithoutOverlapping)
		otInt := boolToInt(req.OneTime)

		res, err := dbExecResult(
			`INSERT INTO cron_jobs
			(name,queue,job_type,data,every,expr,timezone,max_attempts,priority,tags,
			 without_overlapping,one_time,max_runs,expires_at,enabled,next_run_at,created_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,1,?,?)`,
			req.Name, req.Queue, req.JobType, string(req.Data),
			req.Every, req.Expr, req.Timezone,
			req.MaxAttempts, req.Priority, tagsStr,
			woInt, otInt, req.MaxRuns, req.ExpiresAt,
			nextRun, now.Unix(),
		)
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": err.Error()})
			return
		}
		id, _ := res.LastInsertId()
		log.Printf("[Cron] Created cron #%d job_type=%s every=%s expr=%s", id, req.JobType, req.Every, req.Expr)
		jsonResp(w, 201, map[string]interface{}{
			"id": id, "job_type": req.JobType,
			"every": req.Every, "expr": req.Expr,
			"next_run_at": nextRun,
		})

	default:
		http.Error(w, "method not allowed", 405)
	}
}

// handleCronItem PUT/DELETE/PATCH /api/crons/:id
// handleCronTrigger POST /api/crons/:id/trigger
// handleCronRunLogs GET /api/crons/:id/logs
func handleCronItem(w http.ResponseWriter, r *http.Request) {
	parts := splitPath(r.URL.Path) // ["api","crons","{id}"] or ["api","crons","{id}","trigger"|"logs"]
	if len(parts) < 3 {
		jsonResp(w, 400, map[string]string{"error": "cron id required"})
		return
	}
	id, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		jsonResp(w, 400, map[string]string{"error": "invalid id"})
		return
	}

	// 子路径路由：/api/crons/:id/trigger 和 /api/crons/:id/logs
	if len(parts) >= 4 {
		switch parts[3] {
		case "trigger":
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", 405)
				return
			}
			handleCronTriggerNow(w, id)
			return
		case "logs":
			if r.Method != http.MethodGet {
				http.Error(w, "method not allowed", 405)
				return
			}
			handleCronRunLogs(w, r, id)
			return
		}
	}

	switch r.Method {
	case http.MethodGet:
		row := db.QueryRow(`SELECT `+cronSelectCols+` FROM cron_jobs WHERE id=?`, id)
		c, err := scanCronRow(row)
		if err != nil {
			jsonResp(w, 404, map[string]string{"error": "cron not found"})
			return
		}
		jsonResp(w, 200, c)
	case http.MethodPut:
		var req cronReqBody
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonResp(w, 400, map[string]string{"error": err.Error()})
			return
		}
		if req.Every == "" && req.Expr == "" {
			jsonResp(w, 400, map[string]string{"error": "either every or expr is required"})
			return
		}
		req.applyDefaults()

		nextRun, err := calcNextRun(req.Expr, req.Every, req.Timezone, time.Now())
		if err != nil {
			jsonResp(w, 400, map[string]string{"error": err.Error()})
			return
		}
		tagsStr := strings.Join(req.Tags, ",")
		woInt := boolToInt(req.WithoutOverlapping)
		otInt := boolToInt(req.OneTime)

		err = dbExec(
			`UPDATE cron_jobs SET name=?,queue=?,job_type=?,data=?,every=?,expr=?,timezone=?,
			max_attempts=?,priority=?,tags=?,without_overlapping=?,one_time=?,
			max_runs=?,expires_at=?,next_run_at=? WHERE id=?`,
			req.Name, req.Queue, req.JobType, string(req.Data),
			req.Every, req.Expr, req.Timezone,
			req.MaxAttempts, req.Priority, tagsStr,
			woInt, otInt, req.MaxRuns, req.ExpiresAt,
			nextRun, id,
		)
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": err.Error()})
			return
		}
		jsonResp(w, 200, map[string]interface{}{"id": id, "updated": true, "next_run_at": nextRun})

	case http.MethodDelete:
		dbExec(`DELETE FROM cron_jobs WHERE id=?`, id)           //nolint
		dbExec(`DELETE FROM cron_run_logs WHERE cron_id=?`, id)  //nolint
		jsonResp(w, 200, map[string]string{"message": "cron deleted"})

	case http.MethodPatch:
		// 局部更新：支持 enabled / every / expr / timezone / without_overlapping / one_time / max_runs / expires_at / tags
		var req struct {
			Enabled            *bool    `json:"enabled"`
			Every              *string  `json:"every"`
			Expr               *string  `json:"expr"`
			Timezone           *string  `json:"timezone"`
			WithoutOverlapping *bool    `json:"without_overlapping"`
			OneTime            *bool    `json:"one_time"`
			MaxRuns            *int     `json:"max_runs"`
			ExpiresAt          *int64   `json:"expires_at"`
			Tags               []string `json:"tags"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonResp(w, 400, map[string]string{"error": err.Error()})
			return
		}
		if req.Enabled != nil {
			dbExec(`UPDATE cron_jobs SET enabled=? WHERE id=?`, boolToInt(*req.Enabled), id) //nolint
		}
		if req.WithoutOverlapping != nil {
			dbExec(`UPDATE cron_jobs SET without_overlapping=? WHERE id=?`, boolToInt(*req.WithoutOverlapping), id) //nolint
		}
		if req.OneTime != nil {
			dbExec(`UPDATE cron_jobs SET one_time=? WHERE id=?`, boolToInt(*req.OneTime), id) //nolint
		}
		if req.MaxRuns != nil {
			dbExec(`UPDATE cron_jobs SET max_runs=? WHERE id=?`, *req.MaxRuns, id) //nolint
		}
		if req.ExpiresAt != nil {
			dbExec(`UPDATE cron_jobs SET expires_at=? WHERE id=?`, *req.ExpiresAt, id) //nolint
		}
		if req.Tags != nil {
			dbExec(`UPDATE cron_jobs SET tags=? WHERE id=?`, strings.Join(req.Tags, ","), id) //nolint
		}
		// 如果修改了调度规则，重新计算 next_run_at
		if req.Every != nil || req.Expr != nil || req.Timezone != nil {
			// 读取当前值
			var curEvery, curExpr, curTZ string
			db.QueryRow(`SELECT every,expr,timezone FROM cron_jobs WHERE id=?`, id).Scan(&curEvery, &curExpr, &curTZ)
			if req.Every != nil {
				curEvery = *req.Every
			}
			if req.Expr != nil {
				curExpr = *req.Expr
			}
			if req.Timezone != nil {
				curTZ = *req.Timezone
			}
			if req.Every != nil {
				dbExec(`UPDATE cron_jobs SET every=? WHERE id=?`, curEvery, id) //nolint
			}
			if req.Expr != nil {
				dbExec(`UPDATE cron_jobs SET expr=? WHERE id=?`, curExpr, id) //nolint
			}
			if req.Timezone != nil {
				dbExec(`UPDATE cron_jobs SET timezone=? WHERE id=?`, curTZ, id) //nolint
			}
			if nextRun, err := calcNextRun(curExpr, curEvery, curTZ, time.Now()); err == nil {
				dbExec(`UPDATE cron_jobs SET next_run_at=? WHERE id=?`, nextRun, id) //nolint
			}
		}
		jsonResp(w, 200, map[string]interface{}{"id": id, "patched": true})

	default:
		http.Error(w, "method not allowed", 405)
	}
}

// handleCronTriggerNow POST /api/crons/:id/trigger — 立即触发一次，不影响 next_run_at
func handleCronTriggerNow(w http.ResponseWriter, cronID int64) {
	row := db.QueryRow(`SELECT `+cronSelectCols+` FROM cron_jobs WHERE id=?`, cronID)
	c, err := scanCronRow(row)
	if err != nil {
		jsonResp(w, 404, map[string]string{"error": "cron not found"})
		return
	}
	now := time.Now().Unix()
	jobID, err := fireCronJob(c, true)
	if err != nil {
		writeCronRunLog(cronID, 0, now, true, "manual_trigger_error: "+err.Error())
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	// 手动触发也写 run_log（skipped=false），但不更新 run_count / next_run_at
	writeCronRunLog(cronID, jobID, now, false, "manual_trigger")
	jsonResp(w, 200, map[string]interface{}{
		"message": "triggered",
		"job_id":  jobID,
		"cron_id": cronID,
	})
}

// handleCronRunLogs GET /api/crons/:id/logs — 查询触发历史
func handleCronRunLogs(w http.ResponseWriter, r *http.Request, cronID int64) {
	limit := "50"
	if l := r.URL.Query().Get("limit"); l != "" {
		limit = l
	}
	rows, err := db.Query(
		`SELECT id,cron_id,job_id,fired_at,skipped,skip_reason,created_at
		 FROM cron_run_logs WHERE cron_id=? ORDER BY id DESC LIMIT `+limit,
		cronID,
	)
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	defer rows.Close()
	type RunLog struct {
		ID         int64  `json:"id"`
		CronID     int64  `json:"cron_id"`
		JobID      int64  `json:"job_id"`
		FiredAt    int64  `json:"fired_at"`
		Skipped    bool   `json:"skipped"`
		SkipReason string `json:"skip_reason"`
		CreatedAt  int64  `json:"created_at"`
	}
	logs := []RunLog{}
	for rows.Next() {
		var l RunLog
		var skipped int
		rows.Scan(&l.ID, &l.CronID, &l.JobID, &l.FiredAt, &skipped, &l.SkipReason, &l.CreatedAt)
		l.Skipped = skipped == 1
		logs = append(logs, l)
	}
	jsonResp(w, 200, logs)
}

// startCronScheduler 启动 cron 调度器 goroutine，每 10s 扫描一次
func startCronScheduler() {
	go func() {
		// 启动时立即扫描一次，处理服务重启期间积压的到期任务（Bug 11 修复）
		runDueCrons()
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stopGoWorker: // 优雅退出（Bug 12 修复）
				return
			case <-ticker.C:
				runDueCrons()
			}
		}
	}()
	log.Println("[Cron] Scheduler started — scan interval=10s")
}

// runDueCrons 检查并触发到期的 cron 任务
func runDueCrons() {
	now := time.Now().Unix()
	// 扫描：到期需触发的 OR 已过期需禁用的
	rows, err := db.Query(
		`SELECT `+cronSelectCols+` FROM cron_jobs
		 WHERE enabled=1 AND (
		   (next_run_at<=? AND next_run_at>0)
		   OR (expires_at>0 AND expires_at<=?)
		 )`,
		now, now,
	)
	if err != nil {
		log.Printf("[Cron] query error: %v", err)
		return
	}
	defer rows.Close()

	var due []CronJob
	for rows.Next() {
		c, err := scanCronRow(rows)
		if err != nil {
			continue
		}
		due = append(due, c)
	}
	rows.Close()

	for _, c := range due {
		// 检查 expires_at
		if c.ExpiresAt > 0 && now > c.ExpiresAt {
			dbExec(`UPDATE cron_jobs SET enabled=0 WHERE id=?`, c.ID) //nolint
			log.Printf("[Cron] Cron #%d expired, disabled", c.ID)
			writeCronRunLog(c.ID, 0, now, true, "expired")
			continue
		}

		// 检查 max_runs
		if c.MaxRuns > 0 && c.RunCount >= c.MaxRuns {
			dbExec(`UPDATE cron_jobs SET enabled=0 WHERE id=?`, c.ID) //nolint
			log.Printf("[Cron] Cron #%d reached max_runs=%d, disabled", c.ID, c.MaxRuns)
			writeCronRunLog(c.ID, 0, now, true, "max_runs_reached")
			continue
		}

		// 检查 without_overlapping：只检查 running 状态（真正执行中），pending 不算
		// 原因：pending job 可能因为没有 Worker 而长期积压，不应阻止下次触发
		if c.WithoutOverlapping {
			var cnt int
			// Bug 13 修复：用 "," || tags || "," LIKE "%,cron:ID,%" 精确匹配，
			// 避免 cron:10 误匹配 cron:100、cron:101 等
			cronTag := ",cron:" + strconv.FormatInt(c.ID, 10) + ","
			db.QueryRow(
				`SELECT COUNT(*) FROM jobs WHERE "," || tags || "," LIKE ? AND status = 'running'`,
				"%"+cronTag+"%",
			).Scan(&cnt)
			if cnt > 0 {
				log.Printf("[Cron] Cron #%d skipped (overlapping, %d active jobs)", c.ID, cnt)
				writeCronRunLog(c.ID, 0, now, true, "overlapping")
				// 仍然推进 next_run_at，避免下次也跳过
				nextRun, _ := calcNextRun(c.Expr, c.Every, c.Timezone, time.Now())
				dbExec(`UPDATE cron_jobs SET next_run_at=? WHERE id=?`, nextRun, c.ID) //nolint
				continue
			}
		}

		// 触发
		jobID, err := fireCronJob(c, false)
		if err != nil {
			// dispatch 失败：只推进 next_run_at，不递增 run_count，不 disable one_time
			log.Printf("[Cron] dispatch error for cron #%d: %v", c.ID, err)
			writeCronRunLog(c.ID, 0, now, true, "dispatch_error: "+err.Error())
			nextRun, _ := calcNextRun(c.Expr, c.Every, c.Timezone, time.Now())
			dbExec(`UPDATE cron_jobs SET next_run_at=? WHERE id=?`, nextRun, c.ID) //nolint
		} else {
			log.Printf("[Cron] Cron #%d fired → job #%d (type=%s)", c.ID, jobID, c.JobType)
			writeCronRunLog(c.ID, jobID, now, false, "")

			// 仅 dispatch 成功时才更新 last_run_at / next_run_at / run_count
			nextRun, _ := calcNextRun(c.Expr, c.Every, c.Timezone, time.Now())
			newRunCount := c.RunCount + 1
			dbExec(`UPDATE cron_jobs SET last_run_at=?, next_run_at=?, run_count=? WHERE id=?`,
				now, nextRun, newRunCount, c.ID) //nolint

			// one_time：触发一次后 disabled
			if c.OneTime {
				dbExec(`UPDATE cron_jobs SET enabled=0 WHERE id=?`, c.ID) //nolint
				log.Printf("[Cron] Cron #%d is one_time, disabled after first run", c.ID)
			}

			// max_runs 达到上限
			if c.MaxRuns > 0 && newRunCount >= c.MaxRuns {
				dbExec(`UPDATE cron_jobs SET enabled=0 WHERE id=?`, c.ID) //nolint
				log.Printf("[Cron] Cron #%d reached max_runs=%d, disabled", c.ID, c.MaxRuns)
			}
		}
	}
}

// fireCronJob 构造 payload 并投递 job，返回 job_id
// manualTrigger=true 时不更新 run_count / next_run_at（立即触发 API 使用）
func fireCronJob(c CronJob, manualTrigger bool) (int64, error) {
	type fullPayload struct {
		JobType     string          `json:"job_type"`
		Data        json.RawMessage `json:"data"`
		MaxAttempts int             `json:"max_attempts,omitempty"`
	}
	payloadBytes, _ := json.Marshal(fullPayload{
		JobType:     c.JobType,
		Data:        json.RawMessage(c.DataJSON),
		MaxAttempts: c.MaxAttempts,
	})

	// 自动附加 cron 标识 tag，用于 without_overlapping 检测
	tags := append([]string{}, c.Tags...)
	tags = append(tags, "cron:"+strconv.FormatInt(c.ID, 10))

	jobID, err := dispatchJobRaw(c.Queue, string(payloadBytes), 0, DispatchOptions{Priority: c.Priority})
	if err != nil {
		return 0, err
	}
	// 写入 tags
	if len(tags) > 0 {
		dispatchJobWithTags(jobID, tags)
	}
	return jobID, nil
}

// writeCronRunLog 写入触发历史记录
func writeCronRunLog(cronID, jobID, firedAt int64, skipped bool, skipReason string) {
	dbExec(
		`INSERT INTO cron_run_logs (cron_id,job_id,fired_at,skipped,skip_reason,created_at) VALUES (?,?,?,?,?,?)`,
		cronID, jobID, firedAt, boolToInt(skipped), skipReason, firedAt,
	) //nolint
}

// boolToInt 辅助函数
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
