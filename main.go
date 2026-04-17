package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
)

// ─────────────────────────────────────────────
// DB
// ─────────────────────────────────────────────

var db *sql.DB

func initDB() {
	var err error
	db, err = sql.Open("sqlite", "./queue.db?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		log.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	schema := `
	CREATE TABLE IF NOT EXISTS jobs (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		queue        TEXT    NOT NULL DEFAULT 'default',
		payload      TEXT    NOT NULL,
		attempts     INTEGER NOT NULL DEFAULT 0,
		status       TEXT    NOT NULL DEFAULT 'pending',
		available_at INTEGER NOT NULL DEFAULT 0,
		created_at   INTEGER NOT NULL,
		updated_at   INTEGER NOT NULL
	);
	CREATE TABLE IF NOT EXISTS failed_jobs (
		id        INTEGER PRIMARY KEY AUTOINCREMENT,
		queue     TEXT NOT NULL,
		payload   TEXT NOT NULL,
		exception TEXT NOT NULL,
		failed_at INTEGER NOT NULL
	);`
	if _, err = db.Exec(schema); err != nil {
		log.Fatal(err)
	}
	log.Println("[DB] SQLite (pure Go / modernc.org/sqlite) initialized ✓")
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
	AvailableAt int64  `json:"available_at"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

type Payload struct {
	JobType string          `json:"job_type"`
	Data    json.RawMessage `json:"data"`
}

func (j *Job) JobType() string {
	var p Payload
	json.Unmarshal([]byte(j.Payload), &p)
	return p.JobType
}

func dispatchJob(queue string, jobType string, data interface{}, delaySeconds int64) (int64, error) {
	dataBytes, _ := json.Marshal(data)
	p := Payload{JobType: jobType, Data: dataBytes}
	payloadBytes, _ := json.Marshal(p)
	now := time.Now().Unix()
	res, err := db.Exec(
		`INSERT INTO jobs (queue, payload, status, available_at, created_at, updated_at)
		 VALUES (?, ?, 'pending', ?, ?, ?)`,
		queue, string(payloadBytes), now+delaySeconds, now, now,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func reserve(queue string) (*Job, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	row := tx.QueryRow(
		`SELECT id, queue, payload, attempts, status, available_at, created_at, updated_at
		 FROM jobs WHERE queue=? AND status='pending' AND available_at<=?
		 ORDER BY id ASC LIMIT 1`,
		queue, now,
	)
	var j Job
	if err := row.Scan(&j.ID, &j.Queue, &j.Payload, &j.Attempts, &j.Status,
		&j.AvailableAt, &j.CreatedAt, &j.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if _, err = tx.Exec(
		`UPDATE jobs SET status='running', attempts=attempts+1, updated_at=? WHERE id=?`,
		now, j.ID,
	); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	j.Status = "running"
	j.Attempts++
	return &j, nil
}

func markDone(id int64) {
	db.Exec(`UPDATE jobs SET status='done', updated_at=? WHERE id=?`, time.Now().Unix(), id)
}

func markFailed(j *Job, reason string) {
	now := time.Now().Unix()
	db.Exec(`UPDATE jobs SET status='failed', updated_at=? WHERE id=?`, now, j.ID)
	db.Exec(`INSERT INTO failed_jobs (queue, payload, exception, failed_at) VALUES (?,?,?,?)`,
		j.Queue, j.Payload, reason, now)
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
	id     string
	queue  string
	send   chan []byte
	result chan WsResultMessage
	done   chan struct{}
}

type WorkerHub struct {
	mu      sync.Mutex
	workers map[string]*WsWorker
}

var hub = &WorkerHub{workers: make(map[string]*WsWorker)}

func (h *WorkerHub) register(w *WsWorker) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.workers[w.id] = w
	log.Printf("[Hub] Worker registered: id=%s queue=%s total=%d", w.id, w.queue, len(h.workers))
}

func (h *WorkerHub) unregister(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.workers, id)
	log.Printf("[Hub] Worker unregistered: id=%s total=%d", id, len(h.workers))
}

func (h *WorkerHub) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.workers)
}

func (h *WorkerHub) dispatchToWs(j *Job) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, w := range h.workers {
		if w.queue == j.Queue || w.queue == "" {
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
				return true
			default:
			}
		}
	}
	return false
}

// WS dispatcher: polls DB and sends to connected WS workers
func startWsDispatcher(queue string) {
	go func() {
		for {
			if hub.count() == 0 {
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
		send:   make(chan []byte, 4),
		result: make(chan WsResultMessage, 4),
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

	for {
		select {
		case <-worker.done:
			log.Printf("[WS] Worker %s disconnected", workerID)
			return
		case jobMsg := <-worker.send:
			if err := conn.WriteMessage(1, jobMsg); err != nil {
				log.Printf("[WS] write error: %v", err)
				return
			}
			log.Printf("[WS] Job sent to worker %s", workerID)
			select {
			case result := <-worker.result:
				if result.Success {
					markDone(result.JobID)
					log.Printf("[WS] Job #%d done by worker %s: %s", result.JobID, workerID, result.Log)
					ack, _ := json.Marshal(WsControl{Type: "ack", Message: fmt.Sprintf("job #%d done", result.JobID)})
					conn.WriteMessage(1, ack)
				} else {
					row := db.QueryRow(`SELECT id,queue,payload,attempts,status,available_at,created_at,updated_at FROM jobs WHERE id=?`, result.JobID)
					var j Job
					row.Scan(&j.ID, &j.Queue, &j.Payload, &j.Attempts, &j.Status, &j.AvailableAt, &j.CreatedAt, &j.UpdatedAt)
					markFailed(&j, result.Error)
					log.Printf("[WS] Job #%d failed by worker %s: %s", result.JobID, workerID, result.Error)
				}
			case <-time.After(30 * time.Second):
				log.Printf("[WS] Worker %s timed out", workerID)
				return
			case <-worker.done:
				return
			}
		}
	}
}

// ─────────────────────────────────────────────
// Fallback Go Worker
// ─────────────────────────────────────────────

func processJobInternal(j *Job) error {
	var p Payload
	if err := json.Unmarshal([]byte(j.Payload), &p); err != nil {
		return fmt.Errorf("invalid payload: %w", err)
	}
	log.Printf("[GoWorker] Processing job #%d type=%s queue=%s attempt=%d",
		j.ID, p.JobType, j.Queue, j.Attempts)
	switch p.JobType {
	case "send_email":
		var d struct {
			To      string `json:"to"`
			Subject string `json:"subject"`
		}
		json.Unmarshal(p.Data, &d)
		time.Sleep(1 * time.Second)
		log.Printf("[GoWorker] ✉  Email sent to %s: %s", d.To, d.Subject)
	case "generate_report":
		var d struct{ Name string `json:"name"` }
		json.Unmarshal(p.Data, &d)
		time.Sleep(2 * time.Second)
		log.Printf("[GoWorker] 📊 Report generated: %s", d.Name)
	case "resize_image":
		var d struct{ URL string `json:"url"` }
		json.Unmarshal(p.Data, &d)
		time.Sleep(1500 * time.Millisecond)
		log.Printf("[GoWorker] 🖼  Image resized: %s", d.URL)
	case "fail_job":
		return fmt.Errorf("intentional failure for testing")
	default:
		return fmt.Errorf("unknown job type: %s", p.JobType)
	}
	return nil
}

func startGoWorker(queue string, concurrency int) {
	sem := make(chan struct{}, concurrency)
	go func() {
		for {
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
				if err := processJobInternal(j); err != nil {
					markFailed(j, err.Error())
					log.Printf("[GoWorker] job #%d failed: %v", j.ID, err)
				} else {
					markDone(j.ID)
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

func handleDispatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var req struct {
		Queue   string          `json:"queue"`
		JobType string          `json:"job_type"`
		Data    json.RawMessage `json:"data"`
		Delay   int64           `json:"delay"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResp(w, 400, map[string]string{"error": err.Error()})
		return
	}
	if req.Queue == "" {
		req.Queue = "default"
	}
	id, err := dispatchJob(req.Queue, req.JobType, req.Data, req.Delay)
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	jsonResp(w, 201, map[string]interface{}{"job_id": id, "queue": req.Queue, "status": "pending"})
}

func handleListJobs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	queue, status, limit := q.Get("queue"), q.Get("status"), "50"
	if q.Get("limit") != "" {
		limit = q.Get("limit")
	}
	query := `SELECT id,queue,payload,attempts,status,available_at,created_at,updated_at FROM jobs WHERE 1=1`
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
		rows.Scan(&j.ID, &j.Queue, &j.Payload, &j.Attempts, &j.Status,
			&j.AvailableAt, &j.CreatedAt, &j.UpdatedAt)
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

func handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, indexHTML)
}

// ─────────────────────────────────────────────
// main
// ─────────────────────────────────────────────

func main() {
	initDB()

	startGoWorker("default", 2)
	startGoWorker("emails", 1)
	startWsDispatcher("default")
	startWsDispatcher("emails")

	mux := http.NewServeMux()
	cors := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET,POST,DELETE,OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			if r.Method == http.MethodOptions {
				w.WriteHeader(204)
				return
			}
			h(w, r)
		}
	}

	mux.HandleFunc("/", handleIndex)
	mux.HandleFunc("/ws/worker", handleWorkerWS)
	mux.HandleFunc("/api/jobs", cors(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			handleDispatch(w, r)
		} else {
			handleListJobs(w, r)
		}
	}))
	mux.HandleFunc("/api/stats", cors(handleStats))
	mux.HandleFunc("/api/jobs/failed", cors(handleClearFailed))
	mux.HandleFunc("/api/jobs/retry-failed", cors(handleRetryFailed))

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
	log.Println("[HTTP] Shutting down...")
}
