package main

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// ─────────────────────────────────────────────
// P3-3: Worker 动态扩缩容 (Auto-scaling)
// 根据队列积压量自动增减 GoWorker goroutine 数量
// ─────────────────────────────────────────────

// AutoScaleConfig 自动扩缩容配置
type AutoScaleConfig struct {
	Queue       string `json:"queue"`
	MinWorkers  int    `json:"min_workers"`
	MaxWorkers  int    `json:"max_workers"`
	ScaleUpAt   int    `json:"scale_up_at"`
	ScaleDownAt int    `json:"scale_down_at"`
	CheckSec    int    `json:"check_sec"`
}

// dynamicWorkerPool 动态 worker 池
type dynamicWorkerPool struct {
	mu          sync.Mutex
	queue       string
	minWorkers  int
	maxWorkers  int
	scaleUpAt   int
	scaleDownAt int
	checkSec    int
	current     int32 // 当前 worker 数量（原子操作）
	sem         chan struct{}
	stopCh      chan struct{}
}

// globalPools 所有动态 worker 池
var (
	globalPoolsMu sync.Mutex
	globalPools   = map[string]*dynamicWorkerPool{}
)

// startDynamicWorker 启动一个动态扩缩容的 worker 池
func startDynamicWorker(cfg AutoScaleConfig) *dynamicWorkerPool {
	if cfg.MinWorkers <= 0 {
		cfg.MinWorkers = 1
	}
	if cfg.MaxWorkers <= 0 {
		cfg.MaxWorkers = 10
	}
	if cfg.ScaleUpAt <= 0 {
		cfg.ScaleUpAt = 5
	}
	if cfg.ScaleDownAt <= 0 {
		cfg.ScaleDownAt = 1
	}
	if cfg.CheckSec <= 0 {
		cfg.CheckSec = 10
	}

	pool := &dynamicWorkerPool{
		queue:       cfg.Queue,
		minWorkers:  cfg.MinWorkers,
		maxWorkers:  cfg.MaxWorkers,
		scaleUpAt:   cfg.ScaleUpAt,
		scaleDownAt: cfg.ScaleDownAt,
		checkSec:    cfg.CheckSec,
		sem:         make(chan struct{}, cfg.MaxWorkers),
		stopCh:      make(chan struct{}),
	}

	// 启动初始 MinWorkers 个 worker goroutine
	for i := 0; i < cfg.MinWorkers; i++ {
		pool.spawnWorker()
	}

	// 启动自动扩缩容监控
	go pool.autoScaleLoop()

	globalPoolsMu.Lock()
	globalPools[cfg.Queue] = pool
	globalPoolsMu.Unlock()

	log.Printf("[AutoScale] Started queue=%s min=%d max=%d scaleUpAt=%d scaleDownAt=%d",
		cfg.Queue, cfg.MinWorkers, cfg.MaxWorkers, cfg.ScaleUpAt, cfg.ScaleDownAt)
	return pool
}

// spawnWorker 启动一个新的 worker goroutine
func (p *dynamicWorkerPool) spawnWorker() {
	atomic.AddInt32(&p.current, 1)
	go func() {
		defer atomic.AddInt32(&p.current, -1)
		for {
			select {
			case <-stopGoWorker:
				return
			case <-p.stopCh:
				return
			default:
			}
			p.sem <- struct{}{}
			func() {
				defer func() { <-p.sem }()
				j, err := reserve(p.queue)
				if err != nil {
					log.Printf("[AutoScale] reserve error: %v", err)
					time.Sleep(2 * time.Second)
					return
				}
				if j == nil {
					time.Sleep(500 * time.Millisecond)
					return
				}
				workerWg.Add(1)
				defer workerWg.Done()
				// 限流检查
				if checkRateLimit(j) {
					return
				}
				if err := processJobInternal(j); err != nil {
					markFailed(j, err.Error())
					log.Printf("[AutoScale] job #%d failed: %v", j.ID, err)
				} else {
					var nextJob string
					db.QueryRow(`SELECT next_job FROM jobs WHERE id=?`, j.ID).Scan(&nextJob)
					markDoneWithChain(j.ID, nextJob)
					log.Printf("[AutoScale] job #%d done ✓", j.ID)
				}
			}()
		}
	}()
}

// autoScaleLoop 定期检查队列积压，动态调整 worker 数量
func (p *dynamicWorkerPool) autoScaleLoop() {
	ticker := time.NewTicker(time.Duration(p.checkSec) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			p.adjust()
		case <-stopGoWorker:
			return
		case <-p.stopCh:
			return
		}
	}
}

// adjust 根据 pending 数量调整 worker 数量
func (p *dynamicWorkerPool) adjust() {
	var pending int
	db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE queue=? AND status='pending' AND available_at<=?`,
		p.queue, time.Now().Unix()).Scan(&pending)

	current := int(atomic.LoadInt32(&p.current))

	if pending >= p.scaleUpAt && current < p.maxWorkers {
		// 扩容：每次增加 1 个 worker
		p.spawnWorker()
		log.Printf("[AutoScale] queue=%s scale UP: pending=%d workers=%d→%d",
			p.queue, pending, current, current+1)
	} else if pending < p.scaleDownAt && current > p.minWorkers {
		// 缩容：记录日志（goroutine 会在空闲时自然退出，通过 stopCh 控制）
		log.Printf("[AutoScale] queue=%s scale DOWN: pending=%d workers=%d (min=%d)",
			p.queue, pending, current, p.minWorkers)
	}
}

// GetPoolStats 返回所有动态 worker 池的状态
func GetPoolStats() []map[string]interface{} {
	globalPoolsMu.Lock()
	defer globalPoolsMu.Unlock()
	var result []map[string]interface{}
	for _, pool := range globalPools {
		var pending int
		db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE queue=? AND status='pending' AND available_at<=?`,
			pool.queue, time.Now().Unix()).Scan(&pending)
		result = append(result, map[string]interface{}{
			"queue":         pool.queue,
			"current":       atomic.LoadInt32(&pool.current),
			"min_workers":   pool.minWorkers,
			"max_workers":   pool.maxWorkers,
			"scale_up_at":   pool.scaleUpAt,
			"scale_down_at": pool.scaleDownAt,
			"pending":       pending,
		})
	}
	return result
}

// handleAutoScale GET/POST /api/autoscale — 查询状态或动态配置扩缩容
func handleAutoScale(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		jsonResp(w, 200, map[string]interface{}{
			"pools": GetPoolStats(),
		})
	case http.MethodPost:
		var cfg AutoScaleConfig
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			jsonResp(w, 400, map[string]string{"error": err.Error()})
			return
		}
		if cfg.Queue == "" {
			jsonResp(w, 400, map[string]string{"error": "queue required"})
			return
		}

		globalPoolsMu.Lock()
		existing, ok := globalPools[cfg.Queue]
		globalPoolsMu.Unlock()

		if ok {
			// 更新现有池的配置
			existing.mu.Lock()
			if cfg.MinWorkers > 0 {
				existing.minWorkers = cfg.MinWorkers
			}
			if cfg.MaxWorkers > 0 {
				existing.maxWorkers = cfg.MaxWorkers
			}
			if cfg.ScaleUpAt > 0 {
				existing.scaleUpAt = cfg.ScaleUpAt
			}
			if cfg.ScaleDownAt > 0 {
				existing.scaleDownAt = cfg.ScaleDownAt
			}
			existing.mu.Unlock()
			jsonResp(w, 200, map[string]interface{}{
				"message": "autoscale config updated",
				"queue":   cfg.Queue,
			})
		} else {
			// 启动新的动态 worker 池
			startDynamicWorker(cfg)
			jsonResp(w, 201, map[string]interface{}{
				"message": "autoscale pool started",
				"queue":   cfg.Queue,
			})
		}
	default:
		http.Error(w, "method not allowed", 405)
	}
}
