package main

import (
	"embed"
	"net/http"
)

// ─────────────────────────────────────────────────────────────────────────────
// /examples/ — 静态示例文件服务
//
// 通过 //go:embed 将 examples/ 目录嵌入二进制，
// 通过 /examples/ 路径提供访问。
// ─────────────────────────────────────────────────────────────────────────────

//go:embed examples
var examplesFS embed.FS

// handleExamples 提供 /examples/ 下的静态文件
func handleExamples(w http.ResponseWriter, r *http.Request) {
	http.FileServer(http.FS(examplesFS)).ServeHTTP(w, r)
}
