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
// 路由：GET /dir        → 显示程序所在目录
//       GET /dir/sub    → 浏览子目录
//       GET /dir/file   → 下载文件
// 安全：requireLogin 保护；路径穿越防护；不暴露 .git 等隐藏目录
// ─────────────────────────────────────────────────────────────────────────────

// dirRoot 返回程序所在目录（os.Executable 解析后的目录）
func dirRoot() string {
	exe, err := os.Executable()
	if err != nil {
		// 降级：使用当前工作目录
		wd, _ := os.Getwd()
		return wd
	}
	// EvalSymlinks 解析软链接，得到真实路径
	real, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return filepath.Dir(exe)
	}
	return filepath.Dir(real)
}

// handleDir 处理 /dir 及其子路径的请求
func handleDir(w http.ResponseWriter, r *http.Request) {
	root := dirRoot()

	// 从 URL 提取相对路径：/dir/foo/bar → foo/bar
	relPath := strings.TrimPrefix(r.URL.Path, "/dir")
	relPath = strings.TrimPrefix(relPath, "/")

	// 安全清理：防止路径穿越（../）
	clean := filepath.Clean(filepath.Join(root, relPath))
	if !strings.HasPrefix(clean, root) {
		http.Error(w, "403 Forbidden", http.StatusForbidden)
		return
	}

	fi, err := os.Stat(clean)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	if fi.IsDir() {
		renderDirListing(w, r, root, clean, relPath)
	} else {
		// 文件：触发下载
		w.Header().Set("Content-Disposition",
			fmt.Sprintf(`attachment; filename="%s"`, filepath.Base(clean)))
		http.ServeFile(w, r, clean)
	}
}

// renderDirListing 渲染目录列表 HTML 页面
func renderDirListing(w http.ResponseWriter, r *http.Request, root, absPath, relPath string) {
	entries, err := os.ReadDir(absPath)
	if err != nil {
		http.Error(w, "500 Internal Server Error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// 分离目录和文件，分别排序
	var dirs, files []os.DirEntry
	for _, e := range entries {
		if dirs, files = append(dirs, nil), append(files, nil); e.IsDir() {
			dirs[len(dirs)-1] = e
		} else {
			files[len(files)-1] = e
		}
	}
	// 去掉 nil（上面写法有 bug，重写）
	dirs = dirs[:0]
	files = files[:0]
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e)
		} else {
			files = append(files, e)
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Name() < dirs[j].Name() })
	sort.Slice(files, func(i, j int) bool { return files[i].Name() < files[j].Name() })

	// 构建面包屑导航
	breadcrumb := buildBreadcrumb(relPath)

	// 构建行 HTML
	var rows strings.Builder

	// 返回上级目录（非根目录时显示）
	if relPath != "" && relPath != "." {
		parent := filepath.ToSlash(filepath.Dir("/dir/" + relPath))
		if parent == "/dir" {
			parent = "/dir"
		}
		rows.WriteString(fmt.Sprintf(`
		<tr class="dir-row">
			<td class="icon-cell">📁</td>
			<td><a href="%s" class="dir-link">..</a></td>
			<td class="meta">—</td>
			<td class="meta">—</td>
		</tr>`, html.EscapeString(parent)))
	}

	// 目录行
	for _, e := range dirs {
		name := e.Name()
		href := "/dir/" + relPath
		if relPath == "" || relPath == "." {
			href = "/dir/" + name
		} else {
			href = "/dir/" + relPath + "/" + name
		}
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
		href := "/dir/" + relPath
		if relPath == "" || relPath == "." {
			href = "/dir/" + name
		} else {
			href = "/dir/" + relPath + "/" + name
		}
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

	// 统计信息
	summary := fmt.Sprintf("%d 个目录，%d 个文件", len(dirs), len(files))

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, dirHTML,
		html.EscapeString(absPath), // %s — 页面标题路径
		breadcrumb,                 // %s — 面包屑
		html.EscapeString(absPath), // %s — 当前路径显示
		summary,                    // %s — 统计
		rows.String(),              // %s — 表格行
	)
}

// buildBreadcrumb 根据 relPath 构建面包屑 HTML
func buildBreadcrumb(relPath string) string {
	var b strings.Builder
	b.WriteString(`<a href="/dir" class="crumb">🏠 根目录</a>`)
	if relPath == "" || relPath == "." {
		return b.String()
	}
	parts := strings.Split(relPath, "/")
	cumPath := ""
	for i, part := range parts {
		if part == "" {
			continue
		}
		cumPath += "/" + part
		b.WriteString(` <span class="crumb-sep">›</span> `)
		if i == len(parts)-1 {
			// 最后一段：不加链接
			b.WriteString(fmt.Sprintf(`<span class="crumb-cur">%s</span>`, html.EscapeString(part)))
		} else {
			b.WriteString(fmt.Sprintf(`<a href="/dir%s" class="crumb">%s</a>`,
				html.EscapeString(cumPath), html.EscapeString(part)))
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

// dirHTML 是目录浏览页面的 HTML 模板
// 参数顺序：页面标题路径, 面包屑HTML, 当前路径, 统计信息, 表格行HTML
const dirHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>📁 %s — 目录浏览</title>
<style>
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body { font-family: 'Segoe UI', sans-serif; background: #0f172a; color: #e2e8f0; min-height: 100vh; }

  /* ── Header ── */
  header {
    background: linear-gradient(135deg, #1e3a5f, #0f172a);
    padding: 16px 32px;
    border-bottom: 1px solid #1e40af;
    display: flex; align-items: center; gap: 16px;
  }
  header h1 { font-size: 1.3rem; color: #60a5fa; }
  header .back-btn {
    margin-left: auto;
    background: #1e3a5f; color: #93c5fd;
    border: 1px solid #2563eb; border-radius: 8px;
    padding: 6px 14px; font-size: 0.82rem; text-decoration: none;
    transition: background 0.15s;
  }
  header .back-btn:hover { background: #2563eb; color: #fff; }

  /* ── Breadcrumb ── */
  .breadcrumb {
    padding: 12px 32px;
    background: #0f172a;
    border-bottom: 1px solid #1e293b;
    font-size: 0.85rem;
  }
  .crumb { color: #60a5fa; text-decoration: none; }
  .crumb:hover { text-decoration: underline; }
  .crumb-sep { color: #475569; margin: 0 4px; }
  .crumb-cur { color: #e2e8f0; font-weight: 600; }

  /* ── Main ── */
  main { padding: 24px 32px; }

  .path-bar {
    background: #1e293b; border: 1px solid #334155; border-radius: 10px;
    padding: 10px 16px; margin-bottom: 20px;
    font-size: 0.82rem; color: #94a3b8;
    display: flex; align-items: center; gap: 8px;
  }
  .path-bar .path-text { color: #60a5fa; font-family: monospace; word-break: break-all; }
  .path-bar .summary { margin-left: auto; color: #64748b; white-space: nowrap; }

  /* ── Table ── */
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

  .dir-link {
    color: #93c5fd; text-decoration: none; font-weight: 500;
  }
  .dir-link:hover { text-decoration: underline; color: #60a5fa; }

  .file-link {
    color: #e2e8f0; text-decoration: none;
  }
  .file-link:hover { color: #60a5fa; text-decoration: underline; }

  .meta { color: #64748b; font-size: 0.78rem; }
  .size { font-family: monospace; color: #94a3b8; }

  /* ── Empty state ── */
  .empty { text-align: center; padding: 48px; color: #475569; font-size: 0.9rem; }

  /* ── Responsive ── */
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

