package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// ─── CLI flags ────────────────────────────────────────────────────────────────

var (
	baseURL     = flag.String("url", "http://localhost:8080", "Go Queue 服务地址")
	concurrency = flag.Int("c", 50, "并发 goroutine 数")
	duration    = flag.Int("d", 30, "压测持续时间（秒）")
	keySpace    = flag.Int("keys", 1000, "Key 空间大小（随机 key 数量）")
	readRatio   = flag.Int("read", 70, "读操作占比 %（其余为写）")
	ttl         = flag.Int("ttl", 60, "写入时的 TTL（秒），0=永不过期")
	apiKey      = flag.String("api-key", "", "X-API-Key（如有鉴权）")
	reportEvery = flag.Int("report", 2, "实时报告间隔（秒）")
	mixDelete   = flag.Bool("delete", false, "是否混入 DELETE 操作（读:写:删 = read%:write%:5%）")
)

// ─── 全局计数器（atomic）─────────────────────────────────────────────────────

var (
	totalReqs    int64 // 总请求数
	totalOK      int64 // HTTP 2xx
	totalErr     int64 // 网络/超时错误
	totalHit     int64 // GET 命中
	totalMiss    int64 // GET 未命中
	totalLatNs   int64 // 累计延迟 ns（用于平均值）
	latCount     int64 // 参与延迟统计的请求数

	// 分桶延迟（用于 P50/P95/P99）
	latMu      sync.Mutex
	latSamples []int64 // 单位 ms，最多保留 100000 个样本
)

const maxSamples = 100_000

// ─── HTTP client（复用连接）──────────────────────────────────────────────────

func newClient() *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			MaxIdleConnsPerHost: *concurrency + 10,
			IdleConnTimeout:     90 * time.Second,
		},
	}
}

// ─── 请求封装 ─────────────────────────────────────────────────────────────────

func doGet(client *http.Client, key string) (hit bool, latMs int64, err error) {
	url := fmt.Sprintf("%s/api/cache/%s", *baseURL, key)
	req, _ := http.NewRequest("GET", url, nil)
	if *apiKey != "" {
		req.Header.Set("X-API-Key", *apiKey)
	}
	t0 := time.Now()
	resp, e := client.Do(req)
	latMs = time.Since(t0).Milliseconds()
	if e != nil {
		return false, latMs, e
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return false, latMs, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var result struct {
		Cached bool `json:"cached"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	return result.Cached, latMs, nil
}

func doSet(client *http.Client, key string, value interface{}) (latMs int64, err error) {
	url := fmt.Sprintf("%s/api/cache/%s", *baseURL, key)
	body, _ := json.Marshal(map[string]interface{}{
		"data": value,
		"ttl":  *ttl,
	})
	req, _ := http.NewRequest("POST", url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if *apiKey != "" {
		req.Header.Set("X-API-Key", *apiKey)
	}
	t0 := time.Now()
	resp, e := client.Do(req)
	latMs = time.Since(t0).Milliseconds()
	if e != nil {
		return latMs, e
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return latMs, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return latMs, nil
}

func doDel(client *http.Client, key string) (latMs int64, err error) {
	url := fmt.Sprintf("%s/api/cache/%s", *baseURL, key)
	req, _ := http.NewRequest("DELETE", url, nil)
	if *apiKey != "" {
		req.Header.Set("X-API-Key", *apiKey)
	}
	t0 := time.Now()
	resp, e := client.Do(req)
	latMs = time.Since(t0).Milliseconds()
	if e != nil {
		return latMs, e
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return latMs, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return latMs, nil
}

// ─── 随机 key / value ─────────────────────────────────────────────────────────

func randKey(rng *rand.Rand) string {
	return fmt.Sprintf("bench:key:%d", rng.Intn(*keySpace))
}

func randValue(rng *rand.Rand) interface{} {
	return map[string]interface{}{
		"id":    rng.Int63(),
		"score": rng.Float64() * 100,
		"label": fmt.Sprintf("item-%d", rng.Intn(10000)),
	}
}

// ─── 操作类型 ─────────────────────────────────────────────────────────────────

type opType int

const (
	opGet opType = iota
	opSet
	opDel
)

func pickOp(rng *rand.Rand) opType {
	r := rng.Intn(100)
	delPct := 0
	if *mixDelete {
		delPct = 5
	}
	if r < *readRatio-delPct {
		return opGet
	}
	if *mixDelete && r >= 100-delPct {
		return opDel
	}
	return opSet
}

// ─── Worker goroutine ─────────────────────────────────────────────────────────

func worker(stopCh <-chan struct{}, wg *sync.WaitGroup) {
	defer wg.Done()
	client := newClient()
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	for {
		select {
		case <-stopCh:
			return
		default:
		}

		key := randKey(rng)
		op := pickOp(rng)

		var latMs int64
		var err error
		var hit bool

		switch op {
		case opGet:
			hit, latMs, err = doGet(client, key)
		case opSet:
			latMs, err = doSet(client, key, randValue(rng))
		case opDel:
			latMs, err = doDel(client, key)
		}

		atomic.AddInt64(&totalReqs, 1)
		if err != nil {
			atomic.AddInt64(&totalErr, 1)
		} else {
			atomic.AddInt64(&totalOK, 1)
			if op == opGet {
				if hit {
					atomic.AddInt64(&totalHit, 1)
				} else {
					atomic.AddInt64(&totalMiss, 1)
				}
			}
		}

		// 延迟统计
		atomic.AddInt64(&totalLatNs, latMs*1e6)
		atomic.AddInt64(&latCount, 1)

		latMu.Lock()
		if len(latSamples) < maxSamples {
			latSamples = append(latSamples, latMs)
		}
		latMu.Unlock()
	}
}

// ─── 实时监控 ─────────────────────────────────────────────────────────────────

func monitor(stopCh <-chan struct{}) {
	ticker := time.NewTicker(time.Duration(*reportEvery) * time.Second)
	defer ticker.Stop()

	var lastReqs int64
	startTime := time.Now()

	log.Printf("%-8s  %-10s  %-10s  %-10s  %-10s  %-10s  %-10s",
		"Elapsed", "TPS", "Total", "OK", "Err", "HitRate", "AvgLat")
	log.Printf("%s", "────────  ──────────  ──────────  ──────────  ──────────  ──────────  ──────────")

	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			elapsed := time.Since(startTime)
			cur := atomic.LoadInt64(&totalReqs)
			ok := atomic.LoadInt64(&totalOK)
			errCnt := atomic.LoadInt64(&totalErr)
			hit := atomic.LoadInt64(&totalHit)
			miss := atomic.LoadInt64(&totalMiss)
			lc := atomic.LoadInt64(&latCount)

			delta := cur - lastReqs
			lastReqs = cur
			tps := float64(delta) / float64(*reportEvery)

			var avgLat float64
			if lc > 0 {
				avgLat = float64(atomic.LoadInt64(&totalLatNs)) / float64(lc) / 1e6
			}

			hitRate := 0.0
			if hit+miss > 0 {
				hitRate = float64(hit) / float64(hit+miss) * 100
			}

			log.Printf("%-8s  %-10.1f  %-10d  %-10d  %-10d  %-9.1f%%  %-8.2fms",
				elapsed.Round(time.Second),
				tps,
				cur, ok, errCnt,
				hitRate,
				avgLat,
			)
		}
	}
}

// ─── 百分位计算 ───────────────────────────────────────────────────────────────

func percentile(sorted []int64, pct float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(float64(len(sorted)-1) * pct / 100.0)
	return sorted[idx]
}

// ─── 最终报告 ─────────────────────────────────────────────────────────────────

func printReport(elapsed time.Duration) {
	reqs := atomic.LoadInt64(&totalReqs)
	ok := atomic.LoadInt64(&totalOK)
	errCnt := atomic.LoadInt64(&totalErr)
	hit := atomic.LoadInt64(&totalHit)
	miss := atomic.LoadInt64(&totalMiss)
	lc := atomic.LoadInt64(&latCount)

	tps := 0.0
	if elapsed.Seconds() > 0 {
		tps = float64(reqs) / elapsed.Seconds()
	}

	var avgLat float64
	if lc > 0 {
		avgLat = float64(atomic.LoadInt64(&totalLatNs)) / float64(lc) / 1e6
	}

	// 百分位
	latMu.Lock()
	sorted := make([]int64, len(latSamples))
	copy(sorted, latSamples)
	latMu.Unlock()
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	p50 := percentile(sorted, 50)
	p95 := percentile(sorted, 95)
	p99 := percentile(sorted, 99)
	pMax := int64(0)
	if len(sorted) > 0 {
		pMax = sorted[len(sorted)-1]
	}

	hitRate := 0.0
	if hit+miss > 0 {
		hitRate = float64(hit) / float64(hit+miss) * 100
	}

	errRate := 0.0
	if reqs > 0 {
		errRate = float64(errCnt) / float64(reqs) * 100
	}

	log.Printf("")
	log.Printf("╔══════════════════════════════════════════════════════════╗")
	log.Printf("║              Cache Bench — 压力测试报告                  ║")
	log.Printf("╠══════════════════════════════════════════════════════════╣")
	log.Printf("║  目标地址:   %-44s║", *baseURL)
	log.Printf("║  并发数:     %-44d║", *concurrency)
	log.Printf("║  持续时间:   %-44s║", elapsed.Round(time.Millisecond))
	log.Printf("║  Key 空间:   %-44d║", *keySpace)
	log.Printf("║  读写比:     %d%% GET / %d%% SET%s%-28s║",
		*readRatio, 100-*readRatio,
		func() string {
			if *mixDelete {
				return " / 5% DEL"
			}
			return "        "
		}(),
		"",
	)
	log.Printf("╠══════════════════════════════════════════════════════════╣")
	log.Printf("║  总请求数:   %-44d║", reqs)
	log.Printf("║  成功 (2xx): %-44d║", ok)
	log.Printf("║  错误:       %-35d (%.2f%%) ║", errCnt, errRate)
	log.Printf("╠══════════════════════════════════════════════════════════╣")
	log.Printf("║  TPS:        %-44.2f║", tps)
	log.Printf("║  平均延迟:   %-44s║", fmt.Sprintf("%.2f ms", avgLat))
	log.Printf("║  P50 延迟:   %-44s║", fmt.Sprintf("%d ms", p50))
	log.Printf("║  P95 延迟:   %-44s║", fmt.Sprintf("%d ms", p95))
	log.Printf("║  P99 延迟:   %-44s║", fmt.Sprintf("%d ms", p99))
	log.Printf("║  Max 延迟:   %-44s║", fmt.Sprintf("%d ms", pMax))
	log.Printf("╠══════════════════════════════════════════════════════════╣")
	log.Printf("║  GET 命中:   %-44d║", hit)
	log.Printf("║  GET 未命中: %-44d║", miss)
	log.Printf("║  命中率:     %-44s║", fmt.Sprintf("%.2f%%", hitRate))
	log.Printf("╚══════════════════════════════════════════════════════════╝")
}

// ─── main ─────────────────────────────────────────────────────────────────────

func main() {
	flag.Parse()

	log.SetFlags(log.Ltime | log.Lmicroseconds)

	log.Printf("Cache Bench 启动")
	log.Printf("  目标: %s", *baseURL)
	log.Printf("  并发: %d goroutines", *concurrency)
	log.Printf("  时长: %d 秒", *duration)
	log.Printf("  Key 空间: %d", *keySpace)
	log.Printf("  读写比: %d%% GET / %d%% SET", *readRatio, 100-*readRatio)
	if *mixDelete {
		log.Printf("  混入 DELETE: 是（约 5%%）")
	}
	log.Printf("")

	// 预热：写入一批 key，避免冷启动全部 miss
	log.Printf("预热中（写入 %d 个 key）...", min(*keySpace, 200))
	warmClient := newClient()
	warmRng := rand.New(rand.NewSource(42))
	warmCount := min(*keySpace, 200)
	for i := 0; i < warmCount; i++ {
		key := fmt.Sprintf("bench:key:%d", i)
		doSet(warmClient, key, randValue(warmRng)) //nolint
	}
	log.Printf("预热完成 ✓")
	log.Printf("")

	stopCh := make(chan struct{})
	var wg sync.WaitGroup

	// 启动 worker goroutines
	for i := 0; i < *concurrency; i++ {
		wg.Add(1)
		go worker(stopCh, &wg)
	}

	// 启动实时监控
	monStop := make(chan struct{})
	go monitor(monStop)

	// 等待压测时间到
	startTime := time.Now()
	time.Sleep(time.Duration(*duration) * time.Second)
	elapsed := time.Since(startTime)

	// 停止所有 goroutine
	close(stopCh)
	wg.Wait()
	close(monStop)

	// 打印最终报告
	printReport(elapsed)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
