package main

import (
	"fmt"
	"html"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ─────────────────────────────────────────────────────────────────────────────
// /dir — 目录浏览 & 文件下载
//
// URL 设计：
//   GET /dir              → 显示 goapp 所在目录（默认根）
//   GET /dir?path=/       → 访问 Linux 根目录
//   GET /dir?path=/etc    → 访问任意绝对路径
//   GET /dir?path=..      → 相对于当前目录的上级（会被转为绝对路径）
//   GET /dir?path=/foo/bar.txt → 下载文件
//
// 安全：requireLogin 保护；路径用 filepath.Clean 规范化；无路径白名单限制
// ─────────────────────────────────────────────────────────────────────────────

// dirDefaultRoot 返回 goapp 程序所在目录（默认起始目录）
func dirDefaultRoot() string {
	exe, err := os.Executable()
	if err != nil {
		wd, _ := os.Getwd()
		return wd
	}
	real, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return filepath.Dir(exe)
	}
	return filepath.Dir(real)
}

// handleDir 处理 /dir 请求
func handleDir(w http.ResponseWriter, r *http.Request) {
	// 解析目标路径
	targetPath := r.URL.Query().Get("path")
	if targetPath == "" {
		// 默认：goapp 所在目录
		targetPath = dirDefaultRoot()
	} else if !filepath.IsAbs(targetPath) {
		// 相对路径：相对于 goapp 根目录解析
		targetPath = filepath.Join(dirDefaultRoot(), targetPath)
	}
	// 规范化（处理 .. / . / 多余斜杠）
	targetPath = filepath.Clean(targetPath)

	fi, err := os.Stat(targetPath)
	if err != nil {
		http.Error(w, "404 Not Found: "+err.Error(), http.StatusNotFound)
		return
	}

	if fi.IsDir() {
		renderDirListing(w, r, targetPath)
	} else {
		// 文件：触发下载
		w.Header().Set("Content-Disposition",
			fmt.Sprintf(`attachment; filename="%s"`, filepath.Base(targetPath)))
		http.ServeFile(w, r, targetPath)
	}
}

// renderDirListing 渲染目录列表 HTML 页面
func renderDirListing(w http.ResponseWriter, r *http.Request, absPath string) {
	entries, err := os.ReadDir(absPath)
	if err != nil {
		http.Error(w, "500 Internal Server Error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// 分离目录和文件，分别排序
	var dirs, files []os.DirEntry
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e)
		} else {
			files = append(files, e)
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Name() < dirs[j].Name() })
	sort.Slice(files, func(i, j int) bool { return files[i].Name() < files[j].Name() })

	// 构建面包屑导航（基于绝对路径，每段可点击）
	breadcrumb := buildAbsBreadcrumb(absPath)

	// 构建表格行 HTML
	var rows strings.Builder

	// 上级目录（.. 条目，始终显示，除非已在文件系统根 /）
	parent := filepath.Dir(absPath)
	if parent != absPath { // filepath.Dir("/") == "/"，相等时说明已在根
		parentURL := "/dir?path=" + urlEncodePath(parent)
		rows.WriteString(fmt.Sprintf(`
		<tr class="dir-row">
			<td class="icon-cell">📁</td>
			<td><a href="%s" class="dir-link">..</a></td>
			<td class="meta">—</td>
			<td class="meta">—</td>
		</tr>`, html.EscapeString(parentURL)))
	}

	// 目录行
	for _, e := range dirs {
		name := e.Name()
		href := "/dir?path=" + urlEncodePath(filepath.Join(absPath, name))
		info, _ := e.Info()
		modTime := ""
		if info != nil {
			modTime = info.ModTime().Format("2006-01-02 15:04:05")
		}
		rows.WriteString(fmt.Sprintf(`
		<tr class="dir-row">
			<td class="icon-cell">📁</td>
			<td><a href="%s" class="dir-link">%s/</a></td>
			<td class="meta">—</td>
			<td class="meta">%s</td>
		</tr>`,
			html.EscapeString(href),
			html.EscapeString(name),
			html.EscapeString(modTime),
		))
	}

	// 文件行
	for _, e := range files {
		name := e.Name()
		href := "/dir?path=" + urlEncodePath(filepath.Join(absPath, name))
		info, _ := e.Info()
		size := ""
		modTime := ""
		if info != nil {
			size = formatFileSize(info.Size())
			modTime = info.ModTime().Format("2006-01-02 15:04:05")
		}
		rows.WriteString(fmt.Sprintf(`
		<tr class="file-row">
			<td class="icon-cell">%s</td>
			<td><a href="%s" class="file-link" download>%s</a></td>
			<td class="meta size">%s</td>
			<td class="meta">%s</td>
		</tr>`,
			fileIcon(name),
			html.EscapeString(href),
			html.EscapeString(name),
			html.EscapeString(size),
			html.EscapeString(modTime),
		))
	}

	summary := fmt.Sprintf("%d 个目录，%d 个文件", len(dirs), len(files))

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, dirHTML,
		html.EscapeString(absPath), // 页面标题
		breadcrumb,                 // 面包屑 HTML
		html.EscapeString(absPath), // 路径栏
		summary,                    // 统计
		rows.String(),              // 表格行
	)
}

// buildAbsBreadcrumb 根据绝对路径构建可点击面包屑
// 例：/home/user/goapp → 🏠 / ›  home ›  user ›  goapp（每段可点击）
func buildAbsBreadcrumb(absPath string) string {
	var b strings.Builder

	// Linux 根目录 /
	rootURL := "/dir?path=/"
	b.WriteString(fmt.Sprintf(`<a href="%s" class="crumb">🖥️ /</a>`, html.EscapeString(rootURL)))

	// 拆分路径各段（去掉开头的空字符串）
	parts := strings.Split(strings.TrimPrefix(absPath, "/"), "/")
	cumPath := ""
	for i, part := range parts {
		if part == "" {
			continue
		}
		cumPath += "/" + part
		b.WriteString(` <span class="crumb-sep">›</span> `)
		if i == len(parts)-1 {
			// 最后一段：当前目录，不加链接（加粗高亮）
			b.WriteString(fmt.Sprintf(`<span class="crumb-cur">%s</span>`, html.EscapeString(part)))
		} else {
			href := "/dir?path=" + urlEncodePath(cumPath)
			b.WriteString(fmt.Sprintf(`<a href="%s" class="crumb">%s</a>`,
				html.EscapeString(href), html.EscapeString(part)))
		}
	}
	return b.String()
}

// urlEncodePath 对路径做 URL 编码（保留 /，对其他特殊字符编码）
func urlEncodePath(p string) string {
	// 手动编码：只编码空格和特殊字符，保留路径分隔符 /
	var b strings.Builder
	for _, c := range p {
		switch {
		case c == '/' || c == '-' || c == '_' || c == '.' ||
			(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9'):
			b.WriteRune(c)
		default:
			b.WriteString(fmt.Sprintf("%%%02X", c))
		}
	}
	return b.String()
}

// formatFileSize 将字节数格式化为人类可读的字符串
func formatFileSize(size int64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
	)
	switch {
	case size >= GB:
		return fmt.Sprintf("%.2f GB", float64(size)/GB)
	case size >= MB:
		return fmt.Sprintf("%.2f MB", float64(size)/MB)
	case size >= KB:
		return fmt.Sprintf("%.1f KB", float64(size)/KB)
	default:
		return fmt.Sprintf("%d B", size)
	}
}

// fileIcon 根据文件扩展名返回对应 emoji 图标
func fileIcon(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".go":
		return "🐹"
	case ".db", ".sqlite", ".sqlite3":
		return "🗄️"
	case ".log":
		return "📋"
	case ".json":
		return "📄"
	case ".sh", ".bash":
		return "⚙️"
	case ".md", ".txt":
		return "📝"
	case ".zip", ".tar", ".gz", ".tgz":
		return "📦"
	case ".png", ".jpg", ".jpeg", ".gif", ".svg":
		return "🖼️"
	case ".pdf":
		return "📕"
	case ".yml", ".yaml", ".toml":
		return "⚙️"
	default:
		return "📄"
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// HTML 模板
// ─────────────────────────────────────────────────────────────────────────────

const dirHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>📁 %s — 目录浏览</title>
<style>
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body { font-family: 'Segoe UI', sans-serif; background: #0f172a; color: #e2e8f0; min-height: 100vh; }

  header {
    background: linear-gradient(135deg, #1e3a5f, #0f172a);
    padding: 16px 32px;
    border-bottom: 1px solid #1e40af;
    display: flex; align-items: center; gap: 16px;
  }
  header h1 { font-size: 1.3rem; color: #60a5fa; }
  header .home-btn {
    background: #1e293b; color: #60a5fa;
    border: 1px solid #334155; border-radius: 8px;
    padding: 6px 14px; font-size: 0.82rem; text-decoration: none;
    transition: background 0.15s;
  }
  header .home-btn:hover { background: #1e3a5f; }
  header .back-btn {
    margin-left: auto;
    background: #1e3a5f; color: #93c5fd;
    border: 1px solid #2563eb; border-radius: 8px;
    padding: 6px 14px; font-size: 0.82rem; text-decoration: none;
    transition: background 0.15s;
  }
  header .back-btn:hover { background: #2563eb; color: #fff; }

  .breadcrumb {
    padding: 12px 32px;
    background: #0f172a;
    border-bottom: 1px solid #1e293b;
    font-size: 0.85rem;
    display: flex; flex-wrap: wrap; align-items: center; gap: 2px;
  }
  .crumb { color: #60a5fa; text-decoration: none; padding: 2px 4px; border-radius: 4px; }
  .crumb:hover { background: #1e293b; text-decoration: underline; }
  .crumb-sep { color: #475569; margin: 0 2px; }
  .crumb-cur { color: #e2e8f0; font-weight: 600; padding: 2px 4px; }

  main { padding: 24px 32px; }

  .path-bar {
    background: #1e293b; border: 1px solid #334155; border-radius: 10px;
    padding: 10px 16px; margin-bottom: 20px;
    font-size: 0.82rem; color: #94a3b8;
    display: flex; align-items: center; gap: 8px;
  }
  .path-bar .path-text { color: #60a5fa; font-family: monospace; word-break: break-all; }
  .path-bar .summary { margin-left: auto; color: #64748b; white-space: nowrap; }

  .dir-table {
    width: 100%%;
    border-collapse: collapse;
    background: #1e293b;
    border: 1px solid #334155;
    border-radius: 12px;
    overflow: hidden;
  }
  .dir-table thead th {
    background: #0f172a;
    padding: 10px 14px;
    text-align: left;
    font-size: 0.78rem;
    color: #64748b;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    border-bottom: 1px solid #334155;
  }
  .dir-table tbody tr {
    border-bottom: 1px solid #1e293b;
    transition: background 0.1s;
  }
  .dir-table tbody tr:last-child { border-bottom: none; }
  .dir-table tbody tr:hover { background: #263348; }
  .dir-table td { padding: 9px 14px; font-size: 0.85rem; vertical-align: middle; }

  .icon-cell { width: 36px; text-align: center; font-size: 1.1rem; }

  .dir-link { color: #93c5fd; text-decoration: none; font-weight: 500; }
  .dir-link:hover { text-decoration: underline; color: #60a5fa; }

  .file-link { color: #e2e8f0; text-decoration: none; }
  .file-link:hover { color: #60a5fa; text-decoration: underline; }

  .meta { color: #64748b; font-size: 0.78rem; }
  .size { font-family: monospace; color: #94a3b8; }

  .empty { text-align: center; padding: 48px; color: #475569; font-size: 0.9rem; }

  @media (max-width: 640px) {
    main { padding: 16px; }
    header { padding: 12px 16px; }
    .breadcrumb { padding: 10px 16px; }
    .dir-table thead th:nth-child(3),
    .dir-table td:nth-child(3) { display: none; }
  }
</style>
</head>
<body>

<header>
  <span style="font-size:1.6rem">📁</span>
  <h1>目录浏览</h1>
  <a href="/dir" class="home-btn">🏠 goapp 根目录</a>
  <a href="/" class="back-btn">← 返回 Dashboard</a>
</header>

<div class="breadcrumb">%s</div>

<main>
  <div class="path-bar">
    <span>📍</span>
    <span class="path-text">%s</span>
    <span class="summary">%s</span>
  </div>

  <table class="dir-table">
    <thead>
      <tr>
        <th></th>
        <th>名称</th>
        <th>大小</th>
        <th>修改时间</th>
      </tr>
    </thead>
    <tbody>
      %s
    </tbody>
  </table>
</main>

</body>
</html>
`
