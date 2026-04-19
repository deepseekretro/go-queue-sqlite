package main

import (
	"embed"
	"net/http"
)

// ─────────────────────────────────────────────────────────────────────────────
// /static/ — 嵌入静态文件服务
// ─────────────────────────────────────────────────────────────────────────────

//go:embed static
var staticFS embed.FS

// handleStatic 提供 /static/ 下的静态文件
func handleStatic(w http.ResponseWriter, r *http.Request) {
	http.FileServer(http.FS(staticFS)).ServeHTTP(w, r)
}
