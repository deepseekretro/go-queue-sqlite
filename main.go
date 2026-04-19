package main

import (
	"context"
	"database/sql"
	"flag"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
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
var dbPath = "/tmp/queue.db" // DB 文件路径，可通过 -db 命令行参数覆盖

// vacuumState 记录空闲压缩的运行状态，暴露到 /api/stats
var vacuumState = struct {
	lastRunAt   int64   // 上次 VACUUM 完成的 Unix 时间戳（0=从未运行）
	lastDurMs   int64   // 上次 VACUUM 耗时（毫秒）
	totalRuns   int64   // 累计触发次数
	running     bool    // 当前是否正在执行 VACUUM
	lastSizeBefore int64 // 上次 VACUUM 前 DB 文件大小（字节）
	lastSizeAfter  int64 // 上次 VACUUM 后 DB 文件大小（字节）
}{}

// VacuumIntervalSec：自动 VACUUM 检查间隔（秒），环境变量 VACUUM_INTERVAL_SEC，默认 300
var VacuumIntervalSec = 300

// VacuumMinIdleSec：触发 VACUUM 所需的最短连续空闲时间（秒），环境变量 VACUUM_MIN_IDLE_SEC，默认 60
var VacuumMinIdleSec = 60

// ─────────────────────────────────────────────
// DB Writer — 显式写 channel，单一 goroutine 串行执行所有写操作
// 读操作（SELECT）直接用 db，可并发；写操作通过 dbExec/dbTxFunc 排队
// ─────────────────────────────────────────────

// dbWriteReq 封装一次写操作请求
type dbWriteReq struct {
	fn     func() error // 实际执行的写操作（在 arbiter goroutine 中调用）
	result chan error    // 写操作完成后把 error 发回给调用方（nil = fire-and-forget）
}

// dbArbiterCh 是所有 DB 写操作的统一入口，单 goroutine 串行消费
// 缓冲 4096：insertCoalescer/doneCoalescer fire-and-forget，不会阻塞
var dbArbiterCh = make(chan dbWriteReq, 4096)

// ── INSERT Coalescer 类型定义 ─────────────────────────────────────────────
type insertReq struct {
	queue    string
	payload  string
	delaySec int64
	priority int
	dedupKey *string
	resultCh chan insertResult
}
type insertResult struct {
	id  int64
	err error
}

var insertCoalesceCh = make(chan insertReq, 4096)

// ── Done Coalescer 类型定义 ───────────────────────────────────────────────
type doneReq struct {
	id int64
	ts int64
}

var doneCoalesceCh = make(chan doneReq, 4096)

// startDBWriter 启动单一 writer goroutine，串行消费 dbArbiterCh
// 必须在 initDB() 之后、其他 goroutine 启动之前调用
func startDBWriter() {
	go func() {
		for req := range dbArbiterCh {
			t0Arb := time.Now()
			err := req.fn()
			arbMs := time.Since(t0Arb).Milliseconds()
			if arbMs > 50 {
				log.Printf("[DBArbiter] slow op: %dms", arbMs)
			}
			if req.result != nil {
				req.result <- err
			}
		}
	}()
	log.Println("[DBArbiter] Started — all writes serialized via channel")
}

// startInsertCoalescer 启动 INSERT 合并器 goroutine
// HTTP handler 把 insertReq 投入 insertCoalesceCh，coalescer 积累后批量 INSERT
// 批量 INSERT 通过 dbArbiterCh fire-and-forget（result=nil），fn() 在 Arbiter 里执行并回写 resultCh
func startInsertCoalescer() {
	go func() {
		ticker := time.NewTicker(500 * time.Microsecond)
		defer ticker.Stop()
		pending := make([]insertReq, 0, 64)
		flush := func() {
			if len(pending) == 0 {
				return
			}
			batch := pending
			pending = make([]insertReq, 0, 64)
			// fire-and-forget：result=nil，Arbiter 执行完 fn() 后不回写 result channel
			// fn() 内部直接向每个 req.resultCh 发送 job_id，HTTP handler 等待 resultCh
			dbArbiterCh <- dbWriteReq{
				fn: func() error {
					now := time.Now().Unix()
					tx, err := db.Begin()
					if err != nil {
						for _, req := range batch {
							req.resultCh <- insertResult{0, err}
						}
						return err
					}
					stmt, err := tx.Prepare(
						`INSERT OR IGNORE INTO jobs (queue, payload, status, priority, dedup_key, available_at, created_at, updated_at)
						VALUES (?, ?, 'pending', ?, ?, ?, ?, ?)`)
					if err != nil {
						tx.Rollback()
						for _, req := range batch {
							req.resultCh <- insertResult{0, err}
						}
						return err
					}
					defer stmt.Close()
					for _, req := range batch {
						res, err := stmt.Exec(req.queue, req.payload, req.priority, req.dedupKey,
							now+req.delaySec, now, now)
						if err != nil {
							req.resultCh <- insertResult{0, err}
							continue
						}
						id, _ := res.LastInsertId()
						rows, _ := res.RowsAffected()
						if rows == 0 {
							id = 0 // dedup
						}
						req.resultCh <- insertResult{id, nil}
					}
					return tx.Commit()
				},
				result: nil, // fire-and-forget
			}
		}
		for {
			select {
			case req := <-insertCoalesceCh:
				pending = append(pending, req)
				if len(pending) >= 50 {
					flush()
				}
			case <-ticker.C:
				flush()
			case <-stopGoWorker:
				flush()
				return
			}
		}
	}()
	log.Println("[InsertCoalescer] Started — batch INSERT every 500μs or 50 reqs")
}

// startDoneCoalescer 启动 markDone 合并器 goroutine
// WS handler 把 doneReq 投入 doneCoalesceCh，coalescer 积累后批量 UPDATE
// 批量 UPDATE 通过 dbArbiterCh fire-and-forget（result=nil）
func startDoneCoalescer() {
	go func() {
		ticker := time.NewTicker(1 * time.Millisecond)
		defer ticker.Stop()
		pending := make([]doneReq, 0, 64)
		flush := func() {
			if len(pending) == 0 {
				return
			}
			batch := pending
			pending = make([]doneReq, 0, 64)
			dbArbiterCh <- dbWriteReq{
				fn: func() error {
					t0Done := time.Now()
					tx, err := db.Begin()
					if err != nil {
						return err
					}
					stmt, err := tx.Prepare(
						`UPDATE jobs SET status='done', finished_at=?, updated_at=? WHERE id=?`)
					if err != nil {
						tx.Rollback()
						return err
					}
					defer stmt.Close()
					for _, req := range batch {
						stmt.Exec(req.ts, req.ts, req.id) //nolint
					}
					err2 := tx.Commit()
					log.Printf("[DoneCoalescer] flush n=%d tx_ms=%d", len(batch), time.Since(t0Done).Milliseconds())
					if err2 == nil {
						broadcastStats()
					}
					return err2
				},
				result: nil, // fire-and-forget
			}
		}
		for {
			select {
			case req := <-doneCoalesceCh:
				pending = append(pending, req)
				if len(pending) >= 50 {
					flush()
				}
			case <-ticker.C:
				flush()
			case <-stopGoWorker:
				flush()
				return
			}
		}
	}()
	log.Println("[DoneCoalescer] Started — batch markDone every 1ms or 50 reqs")
}

// dbExec 通过写 channel 执行单条写 SQL，等待结果返回
// 用于替代直接调用 db.Exec(...)
func dbExec(query string, args ...interface{}) error {
	result := make(chan error, 1)
	dbArbiterCh <- dbWriteReq{
		fn: func() error {
			_, err := db.Exec(query, args...)
			return err
		},
		result: result,
	}
	return <-result
}

// dbExecResult 通过写 channel 执行单条写 SQL，返回 sql.Result 和 error
// 用于需要 LastInsertId / RowsAffected 的场景
func dbExecResult(query string, args ...interface{}) (sql.Result, error) {
	type resultPair struct {
		res sql.Result
		err error
	}
	resultCh := make(chan resultPair, 1)
	result := make(chan error, 1)
	dbArbiterCh <- dbWriteReq{
		fn: func() error {
			res, err := db.Exec(query, args...)
			resultCh <- resultPair{res, err}
			return err
		},
		result: result,
	}
	p := <-resultCh
	return p.res, p.err
}

// dbTxFunc 通过写 channel 执行一个事务函数（BEGIN → fn(tx) → COMMIT/ROLLBACK）
// fn 在 writer goroutine 中串行执行，保证事务原子性且无锁竞争
func dbTxFunc(fn func(*sql.Tx) error) error {
	result := make(chan error, 1)
	dbArbiterCh <- dbWriteReq{
		fn: func() error {
			tx, err := db.Begin()
			if err != nil {
				return err
			}
			if err := fn(tx); err != nil {
				tx.Rollback()
				return err
			}
			return tx.Commit()
		},
		result: result,
	}
	return <-result
}

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
	db, err = sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=synchronous(NORMAL)&_pragma=wal_autocheckpoint(200)")
	if err != nil {
		log.Fatal(err)
	}
	// WAL 模式下单连接即可：读写并发由 WAL 保证，单连接避免多连接持有读事务阻止 checkpoint
	// wal_autocheckpoint(200)：WAL 达到 200 pages 时自动 checkpoint，防止 WAL 无限增长
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

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
		job_id    INTEGER NOT NULL DEFAULT 0,
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
		`ALTER TABLE failed_jobs ADD COLUMN job_id INTEGER NOT NULL DEFAULT 0`,
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

	// 重启恢复：把上次崩溃/重启时遗留的 running 任务放回 pending
	// 这些任务的 worker 连接已经不存在，必须重新派发
	// Bug 18 修复：启动恢复时不减少 attempts，这些任务是因为进程崩溃/重启而中断，不是任务本身失败
	if res, err := db.Exec(`UPDATE jobs SET status='pending', updated_at=? WHERE status='running'`,
		time.Now().Unix()); err == nil {
		if n, _ := res.RowsAffected(); n > 0 {
			log.Printf("[DB] Startup recovery: reset %d running job(s) back to pending", n)
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
	resultCh := make(chan insertResult, 1)
	insertCoalesceCh <- insertReq{
		queue:    queue,
		payload:  rawPayload,
		delaySec: delaySeconds,
		priority: prio,
		dedupKey: dedupKey,
		resultCh: resultCh,
	}
	r := <-resultCh
	return r.id, r.err
}


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
	var result *Job
	err := dbTxFunc(func(tx *sql.Tx) error {
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
				return nil
			}
			return err
		}
		if _, err := tx.Exec(
			`UPDATE jobs SET status='running', attempts=attempts+1, started_at=?, updated_at=? WHERE id=?`,
			now, now, j.ID,
		); err != nil {
			return err
		}
		j.Status = "running"
		j.Attempts++
		j.StartedAt = now
		result = &j
		return nil
	})
	return result, err
}

// reserveBatch 一次性取最多 n 个 pending 任务并标记为 running
// 用于批量派发，减少 DB 事务次数
func reserveBatch(queue string, n int) ([]*Job, error) {
	if n <= 0 {
		return nil, nil
	}

	type result struct {
		jobs []*Job
		err  error
	}
	resultCh := make(chan result, 1)

	// 把 SELECT + UPDATE 全部放入 writer goroutine，原子执行，消除并发写冲突
	t0Reserve := time.Now()
	dbArbiterCh <- dbWriteReq{
		fn: func() error {
			log.Printf("[reserveBatch:%s] queue_wait=%dms", queue, time.Since(t0Reserve).Milliseconds())
			t1Reserve := time.Now()
			now := time.Now().Unix()
			rows, err := db.Query(
				`SELECT id, queue, payload, attempts, status, priority, available_at, started_at, finished_at, created_at, updated_at
				FROM jobs WHERE queue=? AND status='pending' AND available_at<=?
				ORDER BY priority DESC, id ASC LIMIT ?`,
				queue, now, n,
			)
			if err != nil {
				resultCh <- result{nil, err}
				return err
			}
			var candidates []*Job
			for rows.Next() {
				var j Job
				if err := rows.Scan(&j.ID, &j.Queue, &j.Payload, &j.Attempts, &j.Status, &j.Priority,
					&j.AvailableAt, &j.StartedAt, &j.FinishedAt, &j.CreatedAt, &j.UpdatedAt); err != nil {
					continue
				}
				candidates = append(candidates, &j)
			}
			rows.Close()
			if len(candidates) == 0 {
				resultCh <- result{nil, nil}
				return nil
			}
			// 批量 UPDATE（在同一 writer goroutine 里，无并发冲突）
			var jobs []*Job
			for _, j := range candidates {
				res, err := db.Exec(
					`UPDATE jobs SET status='running', attempts=attempts+1, started_at=?, updated_at=? WHERE id=? AND status='pending'`,
					now, now, j.ID,
				)
				if err != nil {
					continue
				}
				affected, _ := res.RowsAffected()
				if affected == 0 {
					continue
				}
				j.Status = "running"
				j.Attempts++
				j.StartedAt = now
				jobs = append(jobs, j)
			}
			log.Printf("[reserveBatch:%s] sql_ms=%d n=%d", queue, time.Since(t1Reserve).Milliseconds(), len(jobs))
			resultCh <- result{jobs, nil}
			return nil
		},
	}
	r := <-resultCh
	return r.jobs, r.err
}

const (
	MaxAttempts       = 3  // 最多尝试次数，超过后进死信队列
	RetryBaseDelaySec = 10 // 指数退避基础延迟（秒）
)

// 以下超时参数支持通过环境变量覆盖（在 initTimeouts() 中初始化）
var (
	// WsJobTimeoutSec：WS Worker 处理单个任务的最长等待时间（秒）
	// 环境变量：WS_JOB_TIMEOUT_SEC，默认 300（5 分钟）
	WsJobTimeoutSec = 300

	// StaleJobTimeout：Stale Job Reaper 判定任务卡死的阈值（秒）
	// 环境变量：STALE_JOB_TIMEOUT_SEC，默认 300（5 分钟）
	StaleJobTimeout = 300

	// DefaultJobTimeoutSec：内置 GoWorker 单任务默认超时（秒）
	// 环境变量：DEFAULT_JOB_TIMEOUT_SEC，默认 60
	DefaultJobTimeoutSec = 60
)


func initTimeouts() {
	if v := os.Getenv("WS_JOB_TIMEOUT_SEC"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			WsJobTimeoutSec = n
		}
	}
	if v := os.Getenv("STALE_JOB_TIMEOUT_SEC"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			StaleJobTimeout = n
		}
	}
	if v := os.Getenv("DEFAULT_JOB_TIMEOUT_SEC"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			DefaultJobTimeoutSec = n
		}
	}
	if v := os.Getenv("VACUUM_INTERVAL_SEC"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			VacuumIntervalSec = n
		}
	}
	if v := os.Getenv("VACUUM_MIN_IDLE_SEC"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			VacuumMinIdleSec = n
		}
	}
	log.Printf("[Config] Timeouts — ws_job=%ds stale=%ds default_job=%ds vacuum_interval=%ds vacuum_min_idle=%ds",
		WsJobTimeoutSec, StaleJobTimeout, DefaultJobTimeoutSec, VacuumIntervalSec, VacuumMinIdleSec)
}

// workerWg 追踪所有正在执行的 GoWorker goroutine，优雅关闭时等待它们完成
var workerWg sync.WaitGroup

func markDone(id int64) {
	doneCoalesceCh <- doneReq{id: id, ts: time.Now().Unix()}
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
			// Bug 22 修复：限制位移量防止 int64 溢出，并设置最大延迟上限（3600s）
			const maxDelaySec = int64(3600)
			shift := uint(j.Attempts - 1)
			if shift > 30 {
				shift = 30 // 防止 int64 溢出（2^30 * 10 = 10737418240s，已超上限）
			}
			delaySec = int64(1<<shift) * RetryBaseDelaySec
			if delaySec > maxDelaySec {
				delaySec = maxDelaySec
			}
		}
		availAt := now + delaySec
		dbExec(`UPDATE jobs SET status='pending', available_at=?, updated_at=? WHERE id=?`,
			availAt, now, j.ID) //nolint
		log.Printf("[Queue] Job #%d will retry (attempt %d/%d) in %ds — reason: %s",
			j.ID, j.Attempts, maxAttempts, delaySec, reason)
	} else {
		// 超过最大重试次数，进死信队列
		jobType := j.JobType()
		dbExec(`UPDATE jobs SET status='failed', finished_at=?, updated_at=? WHERE id=?`, now, now, j.ID)         //nolint
		dbExec(`INSERT INTO failed_jobs (job_id, queue, job_type, payload, attempts, exception, failed_at) VALUES (?,?,?,?,?,?,?)`,
			j.ID, j.Queue, jobType, j.Payload, j.Attempts, reason, now) //nolint
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
	Type       string   `json:"type"`
	JobID      int64    `json:"job_id"`
	Queue      string   `json:"queue"`
	JobType    string   `json:"job_type"`
	Payload    string   `json:"payload"`
	Tags       []string `json:"tags,omitempty"`
	TimeoutSec int      `json:"timeout_sec,omitempty"` // 任务超时秒数，Worker 可据此设置本地超时
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
	currentJob   *Job         // 缓存当前正在处理的 job 对象（含最新 attempts，供 markFailed 使用）
	idle         bool         // true = 空闲，可接受新任务
	timeoutSec   int          // 当前任务的超时秒数（0 = 使用 WsJobTimeoutSec 全局默认）
	connectedAt  int64        // worker 连接时的 Unix 时间戳（秒）
	jobsDone     int64        // 本次连接内已完成的任务数
}

type WorkerHub struct {
	mu      sync.Mutex
	workers map[string]*WsWorker
	rrKeys  []string // round-robin 顺序
	rrIdx   int      // round-robin 当前索引
}

var hub = &WorkerHub{workers: make(map[string]*WsWorker)}

// notifyDispatcher: per-queue 通知 channel，投递新任务或 worker 变 idle 时发信号
// dispatcher 收到信号后立即尝试取任务，无需等待 sleep
var (
	notifyMu          sync.Mutex
	notifyDispatchMap = make(map[string]chan struct{})
)

// getNotifyCh 获取（或创建）指定队列的通知 channel
func getNotifyCh(queue string) chan struct{} {
	notifyMu.Lock()
	defer notifyMu.Unlock()
	if ch, ok := notifyDispatchMap[queue]; ok {
		return ch
	}
	ch := make(chan struct{}, 1)
	notifyDispatchMap[queue] = ch
	return ch
}

// notifyQueue 向指定队列的 dispatcher 发送非阻塞通知
func notifyQueue(queue string) {
	ch := getNotifyCh(queue)
	select {
	case ch <- struct{}{}:
	default: // 已有信号在队列中，忽略
	}
}

func (h *WorkerHub) register(w *WsWorker) {
	h.mu.Lock()
	w.idle = true
	w.kick = make(chan struct{}, 1) // 初始化 kick channel
	w.connectedAt = time.Now().Unix()  // 记录连接时间
	h.workers[w.id] = w
	h.rrKeys = append(h.rrKeys, w.id)
	total := len(h.workers)
	h.mu.Unlock()
	log.Printf("[Hub] Worker registered: id=%s queue=%s total=%d", w.id, w.queue, total)
	// 新 worker 上线，立即通知 dispatcher 尝试派发 pending 任务（无需等 100ms 轮询）
	if w.queue != "" {
		notifyQueue(w.queue)
	} else {
		// queue="" 的 worker 可处理所有队列，通知所有活跃队列
		for _, q := range h.activeQueues() {
			notifyQueue(q)
		}
	}
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
	total := len(h.workers)
	h.mu.Unlock()
	log.Printf("[Hub] Worker unregistered: id=%s total=%d", id, total)
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

// idleCountForQueue 返回指定队列的空闲 worker 数量
func (h *WorkerHub) idleCountForQueue(queue string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	count := 0
	for _, w := range h.workers {
		// queue="" 的 worker 可处理任意队列的任务，也计入空闲数
		if (w.queue == queue || w.queue == "") && w.idle {
			count++
		}
	}
	return count
}

// activeQueues 返回当前所有有 WS Worker 连接的队列列表（去重）。
// 对于 queue="" 的通配 worker，额外从 DB 查询所有有 pending 任务的队列，
// 确保 DynamicDispatcher 能为这些队列启动 dispatcher。
func (h *WorkerHub) activeQueues() []string {
	h.mu.Lock()
	seen := map[string]bool{}
	var queues []string
	hasWildcard := false
	for _, w := range h.workers {
		if w.queue != "" {
			if !seen[w.queue] {
				seen[w.queue] = true
				queues = append(queues, w.queue)
			}
		} else {
			hasWildcard = true
		}
	}
	h.mu.Unlock()
	// 通配 worker（queue=""）可处理任意队列：把 DB 中有 pending 任务的队列也加入列表
	if hasWildcard {
		rows, err := db.Query(`SELECT DISTINCT queue FROM jobs WHERE status='pending' LIMIT 64`)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var q string
				if rows.Scan(&q) == nil && !seen[q] {
					seen[q] = true
					queues = append(queues, q)
				}
			}
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
		// 从 payload 解析 timeout_sec，用于 WS 等待超时
		var pTimeout struct {
			TimeoutSec int      `json:"timeout_sec"`
			Tags       []string `json:"tags"`
		}
		_ = json.Unmarshal([]byte(j.Payload), &pTimeout)
		jobTimeoutSec := pTimeout.TimeoutSec
		if jobTimeoutSec <= 0 {
			jobTimeoutSec = WsJobTimeoutSec // 使用全局默认
		}

		msg := WsJobMessage{
			Type:       "job",
			JobID:      j.ID,
			Queue:      j.Queue,
			JobType:    j.JobType(),
			Payload:    j.Payload,
			Tags:       pTimeout.Tags,
			TimeoutSec: jobTimeoutSec,
		}
		b, _ := json.Marshal(msg)
		select {
		case w.send <- b:
			w.idle = false          // 标记为忙碌
			w.currentJobID = j.ID   // 记录当前任务
			w.currentJob = j        // 缓存 job 对象（attempts 已+1），供 markFailed 使用
			w.timeoutSec = jobTimeoutSec // 记录超时，供 WS handler 使用
			h.rrIdx = (idx + 1) % n // 下次从下一个开始
			return true
		default:
		}
	}
	return false
}

// WS dispatcher: event-driven + batch reserve
// startWsDispatcher 为指定队列启动一个 WS 派发 goroutine
// 优化：通知驱动（投递/完成时立即唤醒）+ 批量取任务 + 50ms 兜底轮询
func startWsDispatcher(queue string) {
	notifyCh := getNotifyCh(queue)
	go func() {
		for {
			wakeReason := "poll"
			select {
			case <-stopGoWorker:
				return
			case <-notifyCh:
				wakeReason = "notify"
			case <-time.After(10 * time.Millisecond):
				wakeReason = "poll"
			}

			// drain：把 notifyCh 里积压的所有信号一次性消费掉
			for {
				select {
				case <-notifyCh:
				default:
					goto drained
				}
			}
			drained:

			// P3-A: 队列暂停时跳过派发
			if isQueuePaused(queue) {
				continue
			}

			// 循环派发：只要有空闲 worker 且有待处理任务，就持续派发
			for {
				// 计算当前空闲 worker 数，批量取任务
				idleCount := hub.idleCountForQueue(queue)
				if idleCount == 0 {
					break
				}

				// 批量取任务（最多取 idleCount 个，上限 32）
				batchSize := idleCount
				if batchSize > 32 {
					batchSize = 32
				}
				log.Printf("[WsDispatcher:%s] wake=%s idle=%d batch=%d", queue, wakeReason, idleCount, batchSize)
				jobs, err := reserveBatch(queue, batchSize)
				if err != nil {
					log.Printf("[WsDispatcher] reserveBatch error: %v", err)
					break
				}
				if len(jobs) == 0 {
					log.Printf("[WsDispatcher:%s] reserveBatch=0 (idle=%d)", queue, idleCount)
					break
				}

				// 逐个派发给空闲 worker
				dispatched := 0
				for _, j := range jobs {
					if checkRateLimit(j) {
						continue
					}
					if !hub.dispatchToWs(j) {
						dbExec(`UPDATE jobs SET status='pending', updated_at=? WHERE id=?`,
							time.Now().Unix(), j.ID) //nolint
					} else {
						dispatched++
					}
				}
				log.Printf("[WsDispatcher:%s] dispatched=%d/%d wake=%s", queue, dispatched, len(jobs), wakeReason)
				if dispatched == 0 {
					break
				}
				wakeReason = "loop"
			}
		}
	}()
	log.Printf("[WsDispatcher] Started for queue=%s (event-driven+batch)", queue)
}
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
			// 同时扫描 DB 中有 pending 任务的队列，提前启动 dispatcher
			// 这样 worker 一连接就能立即响应，无需等待下次扫描
			if rows, err := db.Query(`SELECT DISTINCT queue FROM jobs WHERE status='pending' LIMIT 64`); err == nil {
				for rows.Next() {
					var q string
					if rows.Scan(&q) == nil {
						queues = append(queues, q)
					}
				}
				rows.Close()
			}
			// 去重并启动 dispatcher
			seen := map[string]bool{}
			for _, q := range queues {
				if q == "" || seen[q] {
					continue
				}
				seen[q] = true
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

	// 设置 Pong handler：收到 pong 时重置读超时，证明连接仍然活跃
	const wsPingInterval = 30 * time.Second
	const wsPongTimeout = 60 * time.Second
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(wsPongTimeout))
		return nil
	})
	// 设置初始读超时
	conn.SetReadDeadline(time.Now().Add(wsPongTimeout))

	// ping goroutine：定期发送 ping，检测半开连接（TCP 连接存在但对端已死）
	go func() {
		ticker := time.NewTicker(wsPingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				// 发送 ping，同时推进读超时
				if err := conn.WritePing(nil); err != nil {
					return // 写失败，连接已断，read goroutine 会关闭 worker.done
				}
			case <-worker.done:
				return // worker 已断线，退出
			}
		}
	}()

	// read goroutine：处理 result / ping 心跳消息
	go func() {
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				close(worker.done)
				return
			}
			// 心跳 ping：回复 pong
			var ctrl WsControl
			if json.Unmarshal(msg, &ctrl) == nil && ctrl.Type == "ping" {
				pong, _ := json.Marshal(WsControl{Type: "pong", Message: "pong"})
				conn.WriteMessage(1, pong)
				continue
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
		jobQueue := worker.queue
		hub.mu.Unlock()
		if jobID > 0 {
			// Bug 18 修复：WS Worker 断开时不减少 attempts，任务会被重新派发
			dbExec(`UPDATE jobs SET status='pending', updated_at=? WHERE id=? AND status='running'`,
				time.Now().Unix(), jobID) //nolint
			log.Printf("[WS] Worker %s %s — job #%d put back to pending", workerID, reason, jobID)
			// 立即通知 dispatcher 重新派发该任务
			if jobQueue != "" {
				notifyQueue(jobQueue)
			} else {
				for _, q := range hub.activeQueues() {
					notifyQueue(q)
				}
			}
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
			// 确定本次任务的等待超时：优先用 per-job timeout_sec，fallback 到全局 WsJobTimeoutSec
			wsTimeout := worker.timeoutSec
			if wsTimeout <= 0 {
				wsTimeout = WsJobTimeoutSec
			}
			log.Printf("[WS] Job sent to worker %s (timeout=%ds)", workerID, wsTimeout)
			select {
			case result := <-worker.result:
				// 处理完毕，标记 worker 为 idle
				hub.mu.Lock()
				worker.idle = true
				worker.currentJobID = 0
				worker.currentJob = nil // 清空缓存的 job 对象
				worker.timeoutSec = 0
				worker.jobsDone++
				hub.mu.Unlock()
				// worker 变为 idle，立即通知 dispatcher 可以派发下一个任务
				if worker.queue != "" {
				log.Printf("[WS] worker %s done, notifying queue=%s", workerID, worker.queue)
					notifyQueue(worker.queue)
				} else {
					// 通配 worker（queue=""）：通知所有活跃队列的 dispatcher
					for _, q := range hub.activeQueues() {
						notifyQueue(q)
					}
				}
				if result.Success {
					markDone(result.JobID)
					log.Printf("[WS] Job #%d done by worker %s: %s", result.JobID, workerID, result.Log)
					ack, _ := json.Marshal(WsControl{Type: "ack", Message: fmt.Sprintf("job #%d done", result.JobID)})
					conn.WriteMessage(1, ack)
				} else {
					// Bug A 修复：直接用 dispatcher 缓存的 job 对象，避免从 DB 读到旧的 attempts
					// reserveBatch 已经把 attempts+1 写入 DB（通过 dbArbiter），
					// 但 db.QueryRow 可能在 UPDATE 完成前读到旧值，导致重试次数判断错误
					hub.mu.Lock()
					cachedJob := worker.currentJob
					hub.mu.Unlock()
					if cachedJob != nil {
						markFailed(cachedJob, result.Error)
					} else {
						// fallback：从 DB 读（兜底，正常不会走到这里）
						row := db.QueryRow(`SELECT id,queue,payload,attempts,status,priority,available_at,started_at,finished_at,created_at,updated_at FROM jobs WHERE id=?`, result.JobID)
						var j Job
						row.Scan(&j.ID, &j.Queue, &j.Payload, &j.Attempts, &j.Status, &j.Priority, &j.AvailableAt, &j.StartedAt, &j.FinishedAt, &j.CreatedAt, &j.UpdatedAt)
						markFailed(&j, result.Error)
					}
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
			case <-time.After(time.Duration(wsTimeout) * time.Second):
				rescheduleCurrentJob(fmt.Sprintf("%ds timeout", wsTimeout))
				log.Printf("[WS] Worker %s timed out after %ds, disconnecting", workerID, wsTimeout)
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
// ─────────────────────────────────────────────────────────────────────────────
// Idle VACUUM — 空闲时自动压缩 SQLite 数据库，回收已删除行占用的磁盘空间
// ─────────────────────────────────────────────────────────────────────────────

// isSystemIdle 判断系统当前是否空闲：
//   - DB 中无 pending / running 任务
//   - insertCoalesceCh / doneCoalesceCh / dbArbiterCh 均无积压
func isSystemIdle() bool {
	var pending, running int
	db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE status='pending'`).Scan(&pending)
	db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE status='running'`).Scan(&running)
	if pending > 0 || running > 0 {
		return false
	}
	if len(insertCoalesceCh) > 0 || len(doneCoalesceCh) > 0 || len(dbArbiterCh) > 0 {
		return false
	}
	return true
}

// dbFilePath 返回当前 DB 文件路径
func dbFilePath() string {
	return dbPath
}

// dbFileSize 返回 DB 的逻辑大小（page_count × page_size，字节）。
// 使用 PRAGMA 而非 os.Stat，因为 WAL 模式下 VACUUM 后主文件的物理大小
// 不会立即缩小（mmap 延迟），但 page_count 会立即反映压缩后的真实页数。
// 注意：此函数必须在 dbArbiterCh 的 fn() 内部调用（持有写锁时），
//       或在只读场景下调用（读取 page_count 是只读操作）。
func dbFileSize() int64 {
	var pageCount, pageSize int64
	if err := db.QueryRow(`PRAGMA page_count`).Scan(&pageCount); err != nil {
		// 降级：用 os.Stat
		fi, err2 := os.Stat(dbFilePath())
		if err2 != nil {
			return -1
		}
		return fi.Size()
	}
	if err := db.QueryRow(`PRAGMA page_size`).Scan(&pageSize); err != nil || pageSize == 0 {
		pageSize = 4096
	}
	return pageCount * pageSize
}

// runVacuum 执行一次完整的 SQLite 压缩：
//  1. wal_checkpoint(TRUNCATE)：把旧 WAL 合并回主文件并截断
//  2. VACUUM：重建主文件，回收碎片空间（VACUUM 的输出写入新 WAL）
//  3. wal_checkpoint(TRUNCATE)：把 VACUUM 产生的新 WAL 合并回主文件并截断
//
// 三步完成后，主文件即为压缩后的真实大小，WAL 被清空。
// 通过 dbArbiterCh 串行执行，保证不与其他写操作并发。
// 返回 (sizeBefore, sizeAfter, durationMs, error)
func runVacuum() (int64, int64, int64, error) {
	type vacResult struct {
		sizeBefore int64
		sizeAfter  int64
		durMs      int64
		err        error
	}
	resultCh := make(chan vacResult, 1)

	dbArbiterCh <- dbWriteReq{
		fn: func() error {
			t0 := time.Now()
			sizeBefore := dbFileSize()

			// Step 1: 把旧 WAL 合并回主文件并截断
			if _, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
				resultCh <- vacResult{sizeBefore, -1, 0, fmt.Errorf("pre-checkpoint: %w", err)}
				return err
			}

			// Step 2: VACUUM — 重建主文件，回收碎片（输出写入新 WAL）
			if _, err := db.Exec(`VACUUM`); err != nil {
				resultCh <- vacResult{sizeBefore, -1, 0, fmt.Errorf("vacuum: %w", err)}
				return err
			}

			// Step 3: 把 VACUUM 产生的新 WAL 合并回主文件并截断
			// 完成后主文件即为压缩后的真实大小
			if _, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
				resultCh <- vacResult{sizeBefore, -1, 0, fmt.Errorf("post-checkpoint: %w", err)}
				return err
			}

			sizeAfter := dbFileSize()
			durMs := time.Since(t0).Milliseconds()
			resultCh <- vacResult{sizeBefore, sizeAfter, durMs, nil}
			return nil
		},
		result: nil, // fire-and-forget to arbiter; result comes via resultCh
	}

	r := <-resultCh
	return r.sizeBefore, r.sizeAfter, r.durMs, r.err
}

// startIdleVacuumer 启动空闲压缩 goroutine：
//   - 每 VacuumIntervalSec 秒检查一次系统是否空闲
//   - 连续 2 次检查均空闲（即空闲时间 >= VacuumMinIdleSec）才触发 VACUUM
//   - 触发后更新 vacuumState，日志记录压缩前后文件大小
//   - 环境变量：VACUUM_INTERVAL_SEC（默认 300）、VACUUM_MIN_IDLE_SEC（默认 60）
func startIdleVacuumer() {
	go func() {
		idleCount := 0 // 连续空闲检查次数
		for {
			time.Sleep(time.Duration(VacuumIntervalSec) * time.Second)

			// 跳过：正在执行 VACUUM
			if vacuumState.running {
				idleCount = 0
				continue
			}

			if isSystemIdle() {
				idleCount++
			} else {
				idleCount = 0
				continue
			}

			// 需要连续 2 次空闲（即空闲时间 >= VacuumMinIdleSec）才触发
			requiredIdle := max(2, (VacuumMinIdleSec/VacuumIntervalSec)+1)
			if idleCount < requiredIdle {
				log.Printf("[Vacuumer] System idle (%d/%d checks), waiting...", idleCount, requiredIdle)
				continue
			}

			// 触发 VACUUM
			idleCount = 0
			vacuumState.running = true
			log.Printf("[Vacuumer] System idle — starting VACUUM (db_size=%d bytes)", dbFileSize())

			sizeBefore, sizeAfter, durMs, err := runVacuum()
			vacuumState.running = false

			if err != nil {
				log.Printf("[Vacuumer] VACUUM failed: %v", err)
				continue
			}

			vacuumState.lastRunAt = time.Now().Unix()
			vacuumState.lastDurMs = durMs
			vacuumState.totalRuns++
			vacuumState.lastSizeBefore = sizeBefore
			vacuumState.lastSizeAfter = sizeAfter

			saved := sizeBefore - sizeAfter
			log.Printf("[Vacuumer] VACUUM done in %dms — %d → %d bytes (saved %d bytes, %.1f%%)",
				durMs, sizeBefore, sizeAfter, saved,
				func() float64 {
					if sizeBefore <= 0 {
						return 0
					}
					return float64(saved) / float64(sizeBefore) * 100
				}())
		}
	}()
	log.Printf("[Vacuumer] Started — check_interval=%ds min_idle=%ds (env: VACUUM_INTERVAL_SEC, VACUUM_MIN_IDLE_SEC)",
		VacuumIntervalSec, VacuumMinIdleSec)
}

// startWalCheckpointer 后台定期执行 WAL checkpoint，防止 WAL 文件无限增长
// WAL 文件过大会导致每次读操作都需要扫描整个 WAL，读性能急剧下降
// 每 30s 执行一次 PASSIVE checkpoint（不阻塞读写，尽量合并 WAL 到主文件）
func startWalCheckpointer() {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stopGoWorker:
				return
			case <-ticker.C:
				// PASSIVE：不阻塞任何读写，尽量把 WAL 合并到主文件
				// 如果有活跃读事务，部分 pages 可能无法 checkpoint，下次再试
				var busy, log2, ckpt int
				if err := db.QueryRow(`PRAGMA wal_checkpoint(PASSIVE)`).Scan(&busy, &log2, &ckpt); err == nil {
					if log2 > 0 {
						slog.Info("[WALCheckpointer] checkpoint done",
							"busy", busy, "log_pages", log2, "checkpointed", ckpt)
					}
				} else {
					slog.Warn("[WALCheckpointer] checkpoint failed", "err", err)
				}
			}
		}
	}()
	slog.Info("[WALCheckpointer] Started — WAL checkpoint every 30s")
}


// handleAdminVacuum 处理 POST /api/admin/vacuum 手动触发压缩请求
func handleAdminVacuum(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonResp(w, 405, map[string]string{"error": "method not allowed, use POST"})
		return
	}
	if vacuumState.running {
		jsonResp(w, 409, map[string]string{"error": "vacuum already running"})
		return
	}

	vacuumState.running = true
	log.Printf("[Vacuumer] Manual VACUUM triggered via API (db_size=%d bytes)", dbFileSize())

	// 异步执行，立即返回 202 Accepted
	go func() {
		sizeBefore, sizeAfter, durMs, err := runVacuum()
		vacuumState.running = false
		if err != nil {
			log.Printf("[Vacuumer] Manual VACUUM failed: %v", err)
			return
		}
		vacuumState.lastRunAt = time.Now().Unix()
		vacuumState.lastDurMs = durMs
		vacuumState.totalRuns++
		vacuumState.lastSizeBefore = sizeBefore
		vacuumState.lastSizeAfter = sizeAfter
		saved := sizeBefore - sizeAfter
		log.Printf("[Vacuumer] Manual VACUUM done in %dms — saved %d bytes", durMs, saved)
	}()

	jsonResp(w, 202, map[string]interface{}{
		"status":  "accepted",
		"message": "VACUUM started in background",
		"db_size_before": dbFileSize(),
	})
}

func startStaleJobReaper() {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stopGoWorker: // Bug 19 修复：优雅退出
				return
			case <-ticker.C:
			}
			threshold := time.Now().Unix() - int64(StaleJobTimeout)
			// Bug 18 修复：stale job 放回 pending 时不减少 attempts
			// stale 是 worker 崩溃/超时导致，不是任务本身失败，不应消耗重试次数
			res, err := dbExecResult(
				`UPDATE jobs SET status='pending', updated_at=?
				 WHERE status='running' AND updated_at<?`,
				time.Now().Unix(), threshold,
			)
			if err != nil {
				log.Printf("[Reaper] error: %v", err)
				continue
			}
			n, _ := res.RowsAffected()
			if n > 0 {
				log.Printf("[Reaper] Rescued %d stale job(s) back to pending (attempts unchanged)", n)
				// 通知所有活跃队列有新的 pending 任务
				for _, q := range hub.activeQueues() {
					notifyQueue(q)
				}
			}
		}
	}()
	log.Printf("[Reaper] Started — stale threshold=%ds, scan interval=30s (env: STALE_JOB_TIMEOUT_SEC)", StaleJobTimeout)
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
	if r.Method != http.MethodDelete && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	// 从 URL 路径中提取 job id：/api/jobs/123 或 /api/jobs/123/cancel
	path := r.URL.Path // e.g. "/api/jobs/123" or "/api/jobs/123/cancel"
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
	res, err := dbExecResult(`UPDATE jobs SET status='cancelled', updated_at=? WHERE id=? AND status='pending'`,
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
		Tags         []string        `json:"tags,omitempty"`     // P4: 任务标签
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
		dbExec(`UPDATE jobs SET next_job=? WHERE id=?`, string(nextJobBytes), id) //nolint
	}
	// P4: 写入 tags
	if len(req.Tags) > 0 {
		dispatchJobWithTags(id, req.Tags)
	}
	// 通知 dispatcher 有新任务到达，立即唤醒（无需等待轮询间隔）
	notifyQueue(req.Queue)
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
	// 预设默认值，确保表为空时也返回 0 而非 null
	stats := map[string]interface{}{
		"pending": 0,
		"running": 0,
		"done":    0,
		"failed":  0,
	}
	for rows.Next() {
		var s string
		var c int
		rows.Scan(&s, &c)
		stats[s] = c
	}
	rows.Close() // 立即释放连接，不用 defer（defer 会延迟到函数返回）
	var failed int
	db.QueryRow(`SELECT COUNT(*) FROM failed_jobs`).Scan(&failed)
	stats["failed_jobs_table"] = failed
	stats["ws_workers"] = hub.count()
	stats["version"] = "4.0.0"
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
		queuePending := map[string]int{}
		for qRows.Next() {
			var q string
			var c int
			qRows.Scan(&q, &c)
			queuePending[q] = c
		}
		qRows.Close() // 立即释放连接
		stats["queue_pending"] = queuePending
	}

	// vacuum 状态
	stats["vacuum"] = map[string]interface{}{
		"running":          vacuumState.running,
		"total_runs":       vacuumState.totalRuns,
		"last_run_at":      vacuumState.lastRunAt,
		"last_dur_ms":      vacuumState.lastDurMs,
		"last_size_before": vacuumState.lastSizeBefore,
		"last_size_after":  vacuumState.lastSizeAfter,
		"db_size_now":      dbFileSize(),
	}

	// 运行路径信息
	if exePath, err2 := os.Executable(); err2 == nil {
		stats["exe_path"] = exePath
	} else {
		stats["exe_path"] = "(unknown)"
	}
	stats["db_path"] = dbPath

	jsonResp(w, 200, stats)
}

func handleClearFailed(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", 405)
		return
	}
	// Bug 21 修复：同时清理 jobs 表中 status='failed' 的记录
	dbExec(`DELETE FROM failed_jobs`) //nolint
	dbExec(`DELETE FROM jobs WHERE status='failed'`) //nolint
	jsonResp(w, 200, map[string]string{"message": "failed jobs cleared"})
}

func handleRetryFailed(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	now := time.Now().Unix()
	// Bug 20 修复：重置 attempts=0，让任务从头开始重试
	// 否则 attempts >= maxAttempts，reserve 后立即再次进 DLQ
	dbExec(`UPDATE jobs SET status='pending', attempts=0, available_at=?, updated_at=? WHERE status='failed'`, now, now) //nolint
	// 同时清理 failed_jobs 表中对应的记录，避免重复
	dbExec(`DELETE FROM failed_jobs WHERE job_id IN (SELECT id FROM jobs WHERE status='pending' AND attempts=0 AND updated_at=?)`, now) //nolint
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
		fmt.Fprintln(w, "# HELP jobs_total Total number of jobs by status")
		fmt.Fprintln(w, "# TYPE jobs_total gauge")
		for rows.Next() {
			var s string
			var c int
			rows.Scan(&s, &c)
			fmt.Fprintf(w, `jobs_total{status=%q} %d`+"\n", s, c)
		}
		rows.Close() // 立即释放连接
	}

	// jobs_total by queue+status
	qRows, _ := db.Query(`SELECT queue, status, COUNT(*) FROM jobs GROUP BY queue, status`)
	if qRows != nil {
		fmt.Fprintln(w, "# HELP jobs_by_queue_total Total jobs by queue and status")
		fmt.Fprintln(w, "# TYPE jobs_by_queue_total gauge")
		for qRows.Next() {
			var q, s string
			var c int
			qRows.Scan(&q, &s, &c)
			fmt.Fprintf(w, `jobs_by_queue_total{queue=%q,status=%q} %d`+"\n", q, s, c)
		}
		qRows.Close() // 立即释放连接
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
		"version":    "4.0.0",
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
	// 命令行参数解析（必须在所有 init 之前）
	dbFlag := flag.String("db", "/tmp/queue.db", "SQLite DB 文件路径（默认: /tmp/queue.db）")
	cacheBackendFlag := flag.String("cache-backend", "memory", "缓存后端: memory（默认）或 file")
	flag.Parse()
	dbPath = *dbFlag

	initTimeouts()
	initLogger()
	initDB()
	startDBWriter() // 启动单一 writer goroutine，所有写操作通过 channel 串行化
	startInsertCoalescer() // 启动 INSERT 合并器，批量写入提升吞吐
	startDoneCoalescer()   // 启动 markDone 合并器，批量 UPDATE 减少写次数
	initBatchDB()
	initCronDB()
	startCronScheduler()
	initTagsDB()
	initCacheDB(dbPath, *cacheBackendFlag)

	startStaleJobReaper()
	startHeartbeatReaper()
	startIdleVacuumer()    // 空闲时自动压缩 SQLite
	startWalCheckpointer() // 定期 WAL checkpoint，防止 WAL 过大导致读超时
	startSessionReaper()
	// 内置 GoWorker 已移除 — 任务由外部 WS Worker 处理
	// 启动动态 WsDispatcher（自动感知所有有 WS Worker 连接的队列）
	startDynamicWsDispatcher()

	mux := http.NewServeMux()
	// API 鉴权中间件：读取环境变量 API_KEY，若设置则要求请求携带 X-API-Key header
	// 鉴权已禁用（无需 API_KEY）
	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return h
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
	mux.HandleFunc("/dir", requireLogin(handleDir))
	mux.HandleFunc("/dir/", requireLogin(handleDir))
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			handleLoginSubmit(w, r)
		} else {
			handleLoginPage(w, r)
		}
	})
	mux.HandleFunc("/logout", handleLogout)
	mux.HandleFunc("/healthz", handleHealthz)
	mux.HandleFunc("/examples/", handleExamples)
	mux.HandleFunc("/static/", handleStatic)
	mux.HandleFunc("/metrics", handleMetrics)
	mux.HandleFunc("/ws/worker", handleWorkerWS)
	mux.HandleFunc("/api/jobs", cors(auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			handleDispatch(w, r)
		} else {
			handleListJobsWithTags(w, r)
		}
	})))
	// /api/jobs/:id — DELETE to cancel a pending job
	mux.HandleFunc("/api/jobs/", cors(auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete || r.Method == http.MethodPost {
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
	mux.HandleFunc("/api/queues", cors(auth(handleQueueList)))
	mux.HandleFunc("/api/queues/", cors(auth(handleQueuePauseResume)))
	mux.HandleFunc("/api/crons", cors(auth(handleCrons)))
	mux.HandleFunc("/api/crons/", cors(auth(handleCronItem)))
	mux.HandleFunc("/api/tags", cors(auth(handleGetTags)))
	mux.HandleFunc("/api/backend", cors(auth(handleBackendInfo)))    // P3-2: 后端信息
	mux.HandleFunc("/api/autoscale", cors(auth(handleAutoScale)))    // P3-3: 动态扩缩容
	mux.HandleFunc("/api/stats", cors(auth(handleStats)))
	mux.HandleFunc("/api/admin/vacuum", cors(auth(handleAdminVacuum))) // 手动触发 VACUUM
	mux.HandleFunc("/api/db/reset", cors(auth(handleDBReset)))
	mux.HandleFunc("/api/me", requireLogin(handleMe))
	mux.HandleFunc("/api/cache/", cors(auth(cacheRouter)))
	mux.HandleFunc("/api/cache-stats", cors(auth(cacheRouter)))
	mux.HandleFunc("/api/cache-keys", cors(auth(cacheRouter)))
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
