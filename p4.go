package main

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
)

// =============================================================================
// P4: 任务标签 (Job Tags)
// jobs 表新增 tags 列（逗号分隔字符串），支持按 tag 过滤查询
// API:
//   POST /api/jobs        → 支持 tags 字段（字符串数组）
//   GET  /api/jobs?tag=xx → 按 tag 过滤
//   GET  /api/tags        → 列出所有已使用的 tag
// =============================================================================

// initTagsDB 为 jobs 表添加 tags 列（迁移）
func initTagsDB() {
	migrations := []string{
		`ALTER TABLE jobs ADD COLUMN tags TEXT NOT NULL DEFAULT ''`,
	}
	for _, m := range migrations {
		if _, err := db.Exec(m); err != nil {
			if !strings.Contains(err.Error(), "duplicate column") {
				log.Printf("[Tags] migration warning: %v", err)
			}
		}
	}
	log.Println("[Tags] DB initialized ✓")
}

// tagsToString 将 []string 转为逗号分隔字符串
func tagsToString(tags []string) string {
	var filtered []string
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t != "" {
			filtered = append(filtered, t)
		}
	}
	return strings.Join(filtered, ",")
}

// stringToTags 将逗号分隔字符串转为 []string
func stringToTags(s string) []string {
	if s == "" {
		return []string{}
	}
	parts := strings.Split(s, ",")
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

// JobWithTags Job 结构体扩展，包含 tags 字段（用于 API 响应）
type JobWithTags struct {
	ID          int64    `json:"id"`
	Queue       string   `json:"queue"`
	Payload     string   `json:"payload"`
	Attempts    int      `json:"attempts"`
	Status      string   `json:"status"`
	Priority    int      `json:"priority"`
	Tags        []string `json:"tags"`
	AvailableAt int64    `json:"available_at"`
	StartedAt   int64    `json:"started_at"`
	FinishedAt  int64    `json:"finished_at"`
	CreatedAt   int64    `json:"created_at"`
	UpdatedAt   int64    `json:"updated_at"`
}

// handleListJobsWithTags GET /api/jobs — 支持 ?tag=xx 过滤（替换原 handleListJobs）
func handleListJobsWithTags(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	queue   := q.Get("queue")
	status  := q.Get("status")
	tag     := q.Get("tag")
	perPage := 20
	if q.Get("per_page") != "" {
		if n, err := strconv.Atoi(q.Get("per_page")); err == nil && n > 0 && n <= 200 {
			perPage = n
		}
	}
	page := 1
	if q.Get("page") != "" {
		if n, err := strconv.Atoi(q.Get("page")); err == nil && n > 0 {
			page = n
		}
	}
	offset := (page - 1) * perPage

	// WHERE 条件（共用）
	where := " WHERE 1=1"
	args  := []interface{}{}
	if queue != "" {
		where += " AND queue=?"
		args = append(args, queue)
	}
	if status != "" {
		where += " AND status=?"
		args = append(args, status)
	}
	if tag != "" {
		where += " AND (tags=? OR tags LIKE ? OR tags LIKE ? OR tags LIKE ?)"
		args = append(args, tag, tag+",%", "%,"+tag, "%,"+tag+",%")
	}

	// 查询总数
	var total int
	countArgs := make([]interface{}, len(args))
	copy(countArgs, args)
	if err := db.QueryRow("SELECT COUNT(*) FROM jobs"+where, countArgs...).Scan(&total); err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}

	// 查询当前页数据
	dataQuery := `SELECT id,queue,payload,attempts,status,priority,tags,available_at,started_at,finished_at,created_at,updated_at FROM jobs` +
		where + fmt.Sprintf(" ORDER BY id DESC LIMIT %d OFFSET %d", perPage, offset)
	rows, err := db.Query(dataQuery, args...)
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	defer rows.Close()

	jobs := []JobWithTags{}
	for rows.Next() {
		var j JobWithTags
		var tagsStr string
		rows.Scan(&j.ID, &j.Queue, &j.Payload, &j.Attempts, &j.Status, &j.Priority,
			&tagsStr, &j.AvailableAt, &j.StartedAt, &j.FinishedAt, &j.CreatedAt, &j.UpdatedAt)
		j.Tags = stringToTags(tagsStr)
		jobs = append(jobs, j)
	}

	type PagedJobs struct {
		Jobs    []JobWithTags `json:"jobs"`
		Total   int           `json:"total"`
		Page    int           `json:"page"`
		PerPage int           `json:"per_page"`
		Pages   int           `json:"pages"`
	}
	pages := (total + perPage - 1) / perPage
	if pages == 0 {
		pages = 1
	}
	jsonResp(w, 200, PagedJobs{
		Jobs:    jobs,
		Total:   total,
		Page:    page,
		PerPage: perPage,
		Pages:   pages,
	})
}

// handleGetTags GET /api/tags — 列出所有已使用的 tag（去重排序）
func handleGetTags(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	rows, err := db.Query(`SELECT DISTINCT tags FROM jobs WHERE tags != '' ORDER BY tags`)
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	defer rows.Close()

	tagSet := map[string]bool{}
	for rows.Next() {
		var tagsStr string
		rows.Scan(&tagsStr)
		for _, t := range stringToTags(tagsStr) {
			tagSet[t] = true
		}
	}

	tags := []string{}
	for t := range tagSet {
		tags = append(tags, t)
	}
	// 简单排序
	for i := 0; i < len(tags); i++ {
		for j := i + 1; j < len(tags); j++ {
			if tags[i] > tags[j] {
				tags[i], tags[j] = tags[j], tags[i]
			}
		}
	}
	jsonResp(w, 200, map[string]interface{}{"tags": tags})
}

// dispatchJobWithTags 投递任务时写入 tags（供 handleDispatch 调用）
func dispatchJobWithTags(jobID int64, tags []string) {
	if len(tags) == 0 {
		return
	}
	tagsStr := tagsToString(tags)
	if tagsStr == "" {
		return
	}
	dbExec(`UPDATE jobs SET tags=? WHERE id=?`, tagsStr, jobID) //nolint
}

