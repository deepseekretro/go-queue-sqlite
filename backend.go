package main

import (
	"database/sql"
	"log"
	"net/http"
	"time"
)

// ─────────────────────────────────────────────
// P3-2: 多后端支持 (QueueBackend interface)
// 抽象 QueueBackend interface，支持 Redis/MySQL 替换 SQLite
// ─────────────────────────────────────────────

// QueueBackend 队列后端接口
// 实现此接口即可替换底层存储（SQLite / Redis / MySQL / PostgreSQL 等）
type QueueBackend interface {
	// Reserve 从指定队列取出一个 pending 任务并标记为 running
	// 返回 nil, nil 表示队列为空
	Reserve(queue string) (*Job, error)

	// Dispatch 投递一个新任务到队列
	// delaySeconds: 延迟秒数（0 = 立即可用）
	// opts: 可选参数（优先级、去重 key）
	// 返回新任务 ID，0 表示被去重忽略
	Dispatch(queue string, payload string, delaySeconds int64, opts ...DispatchOptions) (int64, error)

	// MarkDone 将任务标记为完成
	MarkDone(id int64) error

	// MarkFailed 将任务标记为失败，触发重试或 DLQ 逻辑
	MarkFailed(j *Job, reason string) error

	// Stats 返回各状态任务数量统计
	Stats() (map[string]int, error)

	// Name 返回后端名称（用于日志和 /healthz）
	Name() string
}

// SQLiteBackend 基于 SQLite 的队列后端（当前默认实现）
type SQLiteBackend struct{}

// Ensure SQLiteBackend implements QueueBackend
var _ QueueBackend = (*SQLiteBackend)(nil)

func (b *SQLiteBackend) Reserve(queue string) (*Job, error) {
	return reserve(queue)
}

func (b *SQLiteBackend) Dispatch(queue string, payload string, delaySeconds int64, opts ...DispatchOptions) (int64, error) {
	return dispatchJobRaw(queue, payload, delaySeconds, opts...)
}

func (b *SQLiteBackend) MarkDone(id int64) error {
	markDone(id)
	return nil
}

func (b *SQLiteBackend) MarkFailed(j *Job, reason string) error {
	handleJobFailure(j, reason)
	return nil
}

func (b *SQLiteBackend) Stats() (map[string]int, error) {
	rows, err := db.Query(`SELECT status, COUNT(*) FROM jobs GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	stats := map[string]int{}
	for rows.Next() {
		var s string
		var c int
		rows.Scan(&s, &c)
		stats[s] = c
	}
	return stats, nil
}

func (b *SQLiteBackend) Name() string {
	return "sqlite"
}

// DefaultBackend 全局默认后端（可在 main 中替换为其他实现）
var DefaultBackend QueueBackend = &SQLiteBackend{}

// ─────────────────────────────────────────────
// 未来可扩展的后端示例（接口文档）
// ─────────────────────────────────────────────

// RedisBackend 示例（未实现，仅展示接口扩展方式）
// type RedisBackend struct {
//     client *redis.Client
//     prefix string
// }
// func (b *RedisBackend) Reserve(queue string) (*Job, error) { ... }
// func (b *RedisBackend) Dispatch(...) (int64, error) { ... }
// func (b *RedisBackend) MarkDone(id int64) error { ... }
// func (b *RedisBackend) MarkFailed(j *Job, reason string) error { ... }
// func (b *RedisBackend) Stats() (map[string]int, error) { ... }
// func (b *RedisBackend) Name() string { return "redis" }

// MySQLBackend 示例（未实现，仅展示接口扩展方式）
// type MySQLBackend struct {
//     db *sql.DB
// }
// func (b *MySQLBackend) Reserve(queue string) (*Job, error) { ... }
// ... 其余方法类似 SQLiteBackend，替换 SQL 方言即可

// handleBackendInfo GET /api/backend — 返回当前后端信息
func handleBackendInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "method not allowed", 405)
		return
	}
	stats, err := DefaultBackend.Stats()
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	jsonResp(w, 200, map[string]interface{}{
		"backend": DefaultBackend.Name(),
		"stats":   stats,
		"supported_backends": []string{"sqlite", "redis (planned)", "mysql (planned)", "postgresql (planned)"},
	})
}

// handleDBReset POST /api/db/reset — 清空所有任务数据，重置数据库到初始状态
func handleDBReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}

	// 通过 writer goroutine 串行化，避免与其他写操作并发
	err := dbTxFunc(func(tx *sql.Tx) error {
		stmts := []string{
			"DELETE FROM jobs",
			"DELETE FROM sqlite_sequence WHERE name='jobs'",
			"DELETE FROM batches",
			"DELETE FROM sqlite_sequence WHERE name='batches'",
			"DELETE FROM job_chains",
			"DELETE FROM sqlite_sequence WHERE name='job_chains'",
		}
		for _, s := range stmts {
			tx.Exec(s) //nolint: 忽略 sqlite_sequence 不存在的错误
		}
		return nil
	})
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": "reset: " + err.Error()})
		return
	}

	// WAL checkpoint：把 WAL 文件合并回主库并截断，防止 WAL 膨胀拖慢后续写入
	db.Exec("PRAGMA wal_checkpoint(TRUNCATE)") //nolint

	log.Printf("[API] Database reset by admin")
	jsonResp(w, 200, map[string]interface{}{
		"message": "Database reset successfully. All jobs, batches and chains have been cleared.",
		"reset_at": time.Now().Unix(),
	})
}
