package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// ─── 类型定义 ─────────────────────────────────────────────────────────────────

type CacheRequest struct {
	Data interface{} `json:"data"`
	TTL  int         `json:"ttl"` // 秒，0 = 默认 3600
}

type CacheResponse struct {
	Success   bool        `json:"success"`
	Data      interface{} `json:"data,omitempty"`
	Cached    bool        `json:"cached,omitempty"`
	Message   string      `json:"message,omitempty"`
	Error     string      `json:"error,omitempty"`
	Key       string      `json:"key,omitempty"`
	TTL       int         `json:"ttl,omitempty"`
	Timestamp int64       `json:"timestamp"`
}

type CacheStatsResponse struct {
	Success   bool       `json:"success"`
	Stats     CacheStats `json:"stats,omitempty"`
	Error     string     `json:"error,omitempty"`
	Timestamp int64      `json:"timestamp"`
}

type CacheStats struct {
	KeyCount   int64  `json:"keyCount"`
	Backend    string `json:"backend"` // "memory" | "file"
	MemoryInfo string `json:"memoryInfo"`
}

// ─── 缓存 DB（独立连接，与主 DB 隔离）────────────────────────────────────────

var cacheDB *sql.DB
var cacheBackend string // "memory" | "file"

// initCacheDB 初始化缓存数据库。
//
//	backend = "memory" → SQLite :memory:（进程内，重启丢失）
//	backend = "file"   → 与主 DB 同目录的 cache.db 文件
func initCacheDB(mainDBPath, backend string) {
	cacheBackend = backend

	var dsn string
	if backend == "file" {
		// 与主 DB 同目录
		dir := mainDBPath
		if idx := strings.LastIndex(mainDBPath, "/"); idx >= 0 {
			dir = mainDBPath[:idx]
		} else {
			dir = "."
		}
		dsn = dir + "/cache.db?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	} else {
		// 内存模式：file::memory:?cache=shared 允许同进程多连接共享同一内存 DB
		dsn = "file::memory:?cache=shared&_pragma=busy_timeout(5000)"
		cacheBackend = "memory"
	}

	var err error
	cacheDB, err = sql.Open("sqlite", dsn)
	if err != nil {
		log.Fatalf("[Cache] 打开缓存 DB 失败: %v", err)
	}
	cacheDB.SetMaxOpenConns(1)
	cacheDB.SetMaxIdleConns(1)

	schema := `
	CREATE TABLE IF NOT EXISTS cache_items (
		key        TEXT    PRIMARY KEY,
		value      TEXT    NOT NULL,
		expires_at INTEGER NOT NULL,
		created_at INTEGER NOT NULL
	);`
	if _, err = cacheDB.Exec(schema); err != nil {
		log.Fatalf("[Cache] 建表失败: %v", err)
	}

	// 启动过期清理 goroutine
	go cacheCleanupLoop()

	log.Printf("[Cache] 初始化成功 backend=%s dsn=%s", cacheBackend, dsn)
}

// cacheCleanupLoop 每分钟删除已过期的缓存项
func cacheCleanupLoop() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		res, err := cacheDB.Exec(`DELETE FROM cache_items WHERE expires_at > 0 AND expires_at < ?`, time.Now().Unix())
		if err != nil {
			log.Printf("[Cache] 清理过期项失败: %v", err)
			continue
		}
		n, _ := res.RowsAffected()
		if n > 0 {
			log.Printf("[Cache] 清理过期项 %d 条", n)
		}
	}
}

// ─── HTTP Handlers ────────────────────────────────────────────────────────────

// GET /api/cache/:key
func handleCacheGet(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/api/cache/")
	if key == "" {
		jsonResp(w, 400, CacheResponse{Success: false, Error: "key is required", Timestamp: time.Now().Unix()})
		return
	}

	var value string
	var expiresAt int64
	err := cacheDB.QueryRow(
		`SELECT value, expires_at FROM cache_items WHERE key = ?`, key,
	).Scan(&value, &expiresAt)

	if err == sql.ErrNoRows {
		jsonResp(w, 200, CacheResponse{Success: true, Cached: false, Timestamp: time.Now().Unix()})
		return
	}
	if err != nil {
		jsonResp(w, 500, CacheResponse{Success: false, Error: err.Error(), Timestamp: time.Now().Unix()})
		return
	}

	// 检查是否过期（expires_at=0 表示永不过期）
	if expiresAt > 0 && time.Now().Unix() > expiresAt {
		cacheDB.Exec(`DELETE FROM cache_items WHERE key = ?`, key)
		jsonResp(w, 200, CacheResponse{Success: true, Cached: false, Timestamp: time.Now().Unix()})
		return
	}

	var data interface{}
	if err2 := json.Unmarshal([]byte(value), &data); err2 != nil {
		data = value // fallback: 原始字符串
	}

	jsonResp(w, 200, CacheResponse{
		Success:   true,
		Data:      data,
		Cached:    true,
		Timestamp: time.Now().Unix(),
	})
}

// POST /api/cache/:key   body: {"data": ..., "ttl": 3600}
func handleCacheSet(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/api/cache/")
	if key == "" {
		jsonResp(w, 400, CacheResponse{Success: false, Error: "key is required", Timestamp: time.Now().Unix()})
		return
	}

	var req CacheRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResp(w, 400, CacheResponse{Success: false, Error: err.Error(), Timestamp: time.Now().Unix()})
		return
	}
	if req.TTL == 0 {
		req.TTL = 3600
	}

	valueBytes, err := json.Marshal(req.Data)
	if err != nil {
		jsonResp(w, 500, CacheResponse{Success: false, Error: err.Error(), Timestamp: time.Now().Unix()})
		return
	}

	now := time.Now().Unix()
	var expiresAt int64
	if req.TTL > 0 {
		expiresAt = now + int64(req.TTL)
	}

	_, err = cacheDB.Exec(
		`INSERT INTO cache_items(key, value, expires_at, created_at)
		 VALUES(?, ?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value, expires_at=excluded.expires_at, created_at=excluded.created_at`,
		key, string(valueBytes), expiresAt, now,
	)
	if err != nil {
		jsonResp(w, 500, CacheResponse{Success: false, Error: err.Error(), Timestamp: time.Now().Unix()})
		return
	}

	jsonResp(w, 200, CacheResponse{
		Success:   true,
		Message:   "Cache saved successfully",
		Key:       key,
		TTL:       req.TTL,
		Timestamp: now,
	})
}

// DELETE /api/cache/:key
func handleCacheDelete(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/api/cache/")
	if key == "" {
		jsonResp(w, 400, CacheResponse{Success: false, Error: "key is required", Timestamp: time.Now().Unix()})
		return
	}

	res, err := cacheDB.Exec(`DELETE FROM cache_items WHERE key = ?`, key)
	if err != nil {
		jsonResp(w, 500, CacheResponse{Success: false, Error: err.Error(), Timestamp: time.Now().Unix()})
		return
	}
	n, _ := res.RowsAffected()
	jsonResp(w, 200, CacheResponse{
		Success:   true,
		Key:       key,
		Message:   fmt.Sprintf("deleted %d item(s)", n),
		Timestamp: time.Now().Unix(),
	})
}

// GET /api/cache-stats
func handleCacheStats(w http.ResponseWriter, r *http.Request) {
	var total, active int64
	now := time.Now().Unix()
	cacheDB.QueryRow(`SELECT COUNT(*) FROM cache_items`).Scan(&total)
	cacheDB.QueryRow(`SELECT COUNT(*) FROM cache_items WHERE expires_at = 0 OR expires_at > ?`, now).Scan(&active)

	memInfo := fmt.Sprintf("total=%d active=%d expired=%d backend=%s", total, active, total-active, cacheBackend)
	jsonResp(w, 200, CacheStatsResponse{
		Success: true,
		Stats: CacheStats{
			KeyCount:   active,
			Backend:    cacheBackend,
			MemoryInfo: memInfo,
		},
		Timestamp: time.Now().Unix(),
	})
}

// GET /api/cache-keys  — 列出所有缓存项（供 Dashboard 使用）
type CacheKeyItem struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	ExpiresAt int64  `json:"expires_at"`
	CreatedAt int64  `json:"created_at"`
}

func handleCacheKeys(w http.ResponseWriter, r *http.Request) {
	rows, err := cacheDB.Query(
		`SELECT key, value, expires_at, created_at FROM cache_items ORDER BY created_at DESC LIMIT 500`,
	)
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	defer rows.Close()

	var items []CacheKeyItem
	for rows.Next() {
		var it CacheKeyItem
		rows.Scan(&it.Key, &it.Value, &it.ExpiresAt, &it.CreatedAt)
		items = append(items, it)
	}
	if items == nil {
		items = []CacheKeyItem{}
	}
	jsonResp(w, 200, items)
}

// cacheRouter 分发 /api/cache/ 和 /api/cache-stats 请求
func cacheRouter(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	// GET /api/cache-stats
	if path == "/api/cache-stats" {
		handleCacheStats(w, r)
		return
	}

	// GET /api/cache-keys
	if path == "/api/cache-keys" {
		handleCacheKeys(w, r)
		return
	}

	// /api/cache/:key
	if strings.HasPrefix(path, "/api/cache/") {
		switch r.Method {
		case http.MethodGet:
			handleCacheGet(w, r)
		case http.MethodPost:
			handleCacheSet(w, r)
		case http.MethodDelete:
			handleCacheDelete(w, r)
		case http.MethodOptions:
			w.WriteHeader(204)
		default:
			jsonResp(w, 405, map[string]string{"error": "method not allowed"})
		}
		return
	}

	jsonResp(w, 404, map[string]string{"error": "not found"})
}
