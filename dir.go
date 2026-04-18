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
// /dir — 目录浏览、文本预览 & 文件下载
//
// URL 设计：
//   GET /dir                        → goapp 所在目录（默认根）
//   GET /dir?path=/                 → Linux 根目录
//   GET /dir?path=/etc              → 任意绝对路径
//   GET /dir?path=..                → 上级目录（相对路径转绝对）
//   GET /dir?path=/foo/bar.txt      → 文本文件：弹窗预览（前端 fetch）
//   GET /dir?path=/foo/bar.txt&raw=1 → 返回纯文本内容（供 fetch 读取）
//   GET /dir?path=/foo/bar.bin      → 二进制文件：直接下载
//
// 安全：requireLogin 保护；filepath.Clean 规范化路径
// ─────────────────────────────────────────────────────────────────────────────

// dirDefaultRoot 返回 goapp 程序所在目录
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

// isTextFile 根据扩展名判断是否为文本类文件（弹窗预览）
func isTextFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".go", ".js", ".ts", ".jsx", ".tsx",
		".py", ".rb", ".php", ".java", ".c", ".cpp", ".h", ".hpp", ".cs", ".rs", ".swift",
		".sh", ".bash", ".zsh", ".fish",
		".txt", ".md", ".markdown", ".rst",
		".json", ".yaml", ".yml", ".toml", ".ini", ".cfg", ".conf", ".env",
		".xml", ".html", ".htm", ".css", ".scss", ".less",
		".sql", ".graphql",
		".log", ".csv",
		".dockerfile", ".makefile", ".gitignore", ".gitattributes",
		".mod", ".sum", ".lock":
		return true
	}
	// 无扩展名的常见文本文件
	base := strings.ToLower(filepath.Base(name))
	switch base {
	case "dockerfile", "makefile", "readme", "license", "changelog",
		"gemfile", "rakefile", "procfile":
		return true
	}
	return false
}

// handleDir 处理 /dir 请求
func handleDir(w http.ResponseWriter, r *http.Request) {
	targetPath := r.URL.Query().Get("path")
	if targetPath == "" {
		targetPath = dirDefaultRoot()
	} else if !filepath.IsAbs(targetPath) {
		targetPath = filepath.Join(dirDefaultRoot(), targetPath)
	}
	targetPath = filepath.Clean(targetPath)

	fi, err := os.Stat(targetPath)
	if err != nil {
		http.Error(w, "404 Not Found: "+err.Error(), http.StatusNotFound)
		return
	}

	if fi.IsDir() {
		renderDirListing(w, r, targetPath)
		return
	}

	// 文件处理
	raw := r.URL.Query().Get("raw") == "1"
	if raw {
		// raw=1：返回纯文本内容（供前端 fetch 读取后弹窗展示）
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		http.ServeFile(w, r, targetPath)
	} else {
		// 普通访问：触发下载
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

	breadcrumb := buildAbsBreadcrumb(absPath)

	var rows strings.Builder

	// 上级目录 ..（已在根 / 时不显示）
	parent := filepath.Dir(absPath)
	if parent != absPath {
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
		filePath := filepath.Join(absPath, name)
		info, _ := e.Info()
		size := ""
		modTime := ""
		if info != nil {
			size = formatFileSize(info.Size())
			modTime = info.ModTime().Format("2006-01-02 15:04:05")
		}

		isText := isTextFile(name)
		rawURL := "/dir?path=" + urlEncodePath(filePath) + "&raw=1"
		dlURL := "/dir?path=" + urlEncodePath(filePath)

		var linkHTML string
		if isText {
			// 文本文件：点击触发 JS 弹窗预览
			linkHTML = fmt.Sprintf(
				`<a href="#" class="file-link text-preview" data-raw="%s" data-name="%s">%s</a>`,
				html.EscapeString(rawURL),
				html.EscapeString(name),
				html.EscapeString(name),
			)
		} else {
			// 二进制文件：直接下载
			linkHTML = fmt.Sprintf(
				`<a href="%s" class="file-link" download>%s</a>`,
				html.EscapeString(dlURL),
				html.EscapeString(name),
			)
		}

		// 下载按钮（文本文件也提供下载入口）
		dlBtn := fmt.Sprintf(
			`<a href="%s" class="dl-btn" download title="下载">⬇</a>`,
			html.EscapeString(dlURL),
		)

		rows.WriteString(fmt.Sprintf(`
		<tr class="file-row">
			<td class="icon-cell">%s</td>
			<td class="name-cell">%s %s</td>
			<td class="meta size">%s</td>
			<td class="meta">%s</td>
		</tr>`,
			fileIcon(name),
			linkHTML,
			dlBtn,
			html.EscapeString(size),
			html.EscapeString(modTime),
		))
	}

	summary := fmt.Sprintf("%d 个目录，%d 个文件", len(dirs), len(files))

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, dirHTML,
		html.EscapeString(absPath),
		breadcrumb,
		summary,
		rows.String(),
	)
}

// buildAbsBreadcrumb 根据绝对路径构建可点击面包屑
func buildAbsBreadcrumb(absPath string) string {
	var b strings.Builder
	rootURL := "/dir?path=/"
	b.WriteString(fmt.Sprintf(`<a href="%s" class="crumb">🖥️ /</a>`, html.EscapeString(rootURL)))

	parts := strings.Split(strings.TrimPrefix(absPath, "/"), "/")
	cumPath := ""
	for _, part := range parts {
		if part == "" {
			continue
		}
		cumPath += "/" + part
		b.WriteString(` <span class="crumb-sep">›</span> `)
		// 每一段都是可点击链接（包括最后一段/当前目录）
		href := "/dir?path=" + urlEncodePath(cumPath)
		b.WriteString(fmt.Sprintf(`<a href="%s" class="crumb">%s</a>`,
			html.EscapeString(href), html.EscapeString(part)))
	}
	return b.String()
}

// urlEncodePath 对路径做 URL 编码（保留 /，编码其他特殊字符）
func urlEncodePath(p string) string {
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

// formatFileSize 将字节数格式化为人类可读字符串
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

// fileIcon 根据文件扩展名返回 emoji 图标
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
    padding: 10px 32px;
    background: #0f172a;
    border-bottom: 1px solid #1e293b;
    font-size: 0.85rem;
    display: flex; flex-wrap: wrap; align-items: center; gap: 2px;
  }
  .crumb-nav { display: flex; flex-wrap: wrap; align-items: center; gap: 2px; flex: 1; }
  .crumb-summary { margin-left: auto; color: #475569; font-size: 0.78rem; white-space: nowrap; padding-left: 16px; }
  .crumb { color: #60a5fa; text-decoration: none; padding: 2px 6px; border-radius: 4px; }
  .crumb:hover { background: #1e293b; text-decoration: underline; }
  .crumb-sep { color: #475569; margin: 0 1px; }

  main { padding: 24px 32px; }

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
  .name-cell { display: flex; align-items: center; gap: 8px; }

  .dir-link { color: #93c5fd; text-decoration: none; font-weight: 500; }
  .dir-link:hover { text-decoration: underline; color: #60a5fa; }

  .file-link { color: #e2e8f0; text-decoration: none; }
  .file-link:hover { color: #60a5fa; text-decoration: underline; }
  .file-link.text-preview { color: #a5f3fc; cursor: pointer; }
  .file-link.text-preview:hover { color: #22d3ee; text-decoration: underline; }

  /* 下载按钮 */
  .dl-btn {
    color: #475569; text-decoration: none; font-size: 0.8rem;
    padding: 1px 5px; border-radius: 4px; border: 1px solid #334155;
    transition: all 0.15s; white-space: nowrap;
  }
  .dl-btn:hover { background: #1e3a5f; color: #93c5fd; border-color: #2563eb; }

  .meta { color: #64748b; font-size: 0.78rem; }
  .size { font-family: monospace; color: #94a3b8; }

  /* ── 预览弹窗 ── */
  dialog {
    background: #1e293b;
    border: 1px solid #334155;
    border-radius: 14px;
    padding: 0;
    width: min(90vw, 900px);
    max-height: 85vh;
    color: #e2e8f0;
    box-shadow: 0 25px 60px rgba(0,0,0,0.6);
    display: flex;
    flex-direction: column;
    /* 拖拽定位 */
    position: fixed;
    margin: 0;
  }
  dialog::backdrop { background: rgba(0,0,0,0.65); backdrop-filter: blur(3px); }
  .dialog-header { cursor: grab; user-select: none; }
  .dialog-header:active { cursor: grabbing; }
  .dialog-header {
    display: flex; align-items: center; gap: 12px;
    padding: 14px 20px;
    border-bottom: 1px solid #334155;
    background: #0f172a;
    border-radius: 14px 14px 0 0;
    flex-shrink: 0;
  }
  .dialog-title {
    font-size: 0.9rem; color: #93c5fd; font-family: monospace;
    flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  }
  .dialog-dl-btn {
    background: #1e3a5f; color: #93c5fd;
    border: 1px solid #2563eb; border-radius: 7px;
    padding: 5px 12px; font-size: 0.78rem; text-decoration: none;
    white-space: nowrap; transition: background 0.15s;
  }
  .dialog-dl-btn:hover { background: #2563eb; color: #fff; }
  .dialog-close {
    background: none; border: none; color: #64748b;
    font-size: 1.3rem; cursor: pointer; padding: 2px 6px;
    border-radius: 6px; transition: all 0.15s; line-height: 1;
  }
  .dialog-close:hover { background: #334155; color: #e2e8f0; }
  .dialog-body {
    overflow: auto;
    flex: 1;
    padding: 0;
  }
  .dialog-loading {
    padding: 48px; text-align: center; color: #475569; font-size: 0.9rem;
  }
  .dialog-error {
    padding: 24px; color: #f87171; font-size: 0.85rem;
    background: #3b1a1a; margin: 16px; border-radius: 8px;
  }
  pre.file-content {
    margin: 0;
    padding: 20px 24px;
    font-family: 'Cascadia Code', 'Fira Code', 'JetBrains Mono', monospace;
    font-size: 0.82rem;
    line-height: 1.6;
    color: #e2e8f0;
    white-space: pre-wrap;
    word-break: break-all;
    tab-size: 4;
  }
  .line-nums {
    display: grid;
    grid-template-columns: auto 1fr;
    gap: 0;
  }
  .line-num-col {
    padding: 20px 12px 20px 20px;
    text-align: right;
    color: #334155;
    font-family: monospace;
    font-size: 0.82rem;
    line-height: 1.6;
    user-select: none;
    border-right: 1px solid #1e293b;
    background: #0f172a;
    white-space: pre;
  }
  .line-code-col {
    padding: 20px 20px 20px 16px;
    font-family: 'Cascadia Code', 'Fira Code', 'JetBrains Mono', monospace;
    font-size: 0.82rem;
    line-height: 1.6;
    white-space: pre-wrap;
    word-break: break-all;
    tab-size: 4;
  }
  .dialog-footer {
    padding: 8px 20px;
    border-top: 1px solid #1e293b;
    font-size: 0.75rem;
    color: #475569;
    flex-shrink: 0;
    display: flex; gap: 16px;
  }

  @media (max-width: 640px) {
    main { padding: 16px; }
    header { padding: 12px 16px; }
    .breadcrumb { padding: 10px 16px; }
    .dir-table thead th:nth-child(3),
    .dir-table td:nth-child(3) { display: none; }
    dialog { width: 98vw; max-height: 92vh; }
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

<div class="breadcrumb">
    <span class="crumb-nav">%s</span>
    <span class="crumb-summary">%s</span>
  </div>

<main>
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

<!-- 文本预览弹窗 -->
<dialog id="previewDialog">
  <div class="dialog-header">
    <span style="font-size:1.1rem">📄</span>
    <span class="dialog-title" id="dialogTitle">—</span>
    <a href="#" class="dialog-dl-btn" id="dialogDlBtn" download>⬇ 下载</a>
    <button class="dialog-close" id="dialogClose" title="关闭 (Esc)">✕</button>
  </div>
  <div class="dialog-body" id="dialogBody">
    <div class="dialog-loading">加载中…</div>
  </div>
  <div class="dialog-footer" id="dialogFooter"></div>
</dialog>

<script>
(function () {
  const dialog = document.getElementById('previewDialog');
  const dialogTitle = document.getElementById('dialogTitle');
  const dialogBody = document.getElementById('dialogBody');
  const dialogDlBtn = document.getElementById('dialogDlBtn');
  const dialogFooter = document.getElementById('dialogFooter');
  const dialogClose = document.getElementById('dialogClose');

  // 关闭弹窗
  dialogClose.addEventListener('click', () => dialog.close());
  dialog.addEventListener('click', e => { if (e.target === dialog) dialog.close(); });
  document.addEventListener('keydown', e => { if (e.key === 'Escape' && dialog.open) dialog.close(); });

  // ── 拖拽移动弹窗 ──
  // 拖拽 dialog-header 区域来移动整个 dialog
  const dialogHeader = document.querySelector('.dialog-header');
  let isDragging = false, dragOffsetX = 0, dragOffsetY = 0;

  dialogHeader.addEventListener('mousedown', function (e) {
    // 不拦截按钮点击
    if (e.target.closest('button, a')) return;
    isDragging = true;
    const rect = dialog.getBoundingClientRect();
    dragOffsetX = e.clientX - rect.left;
    dragOffsetY = e.clientY - rect.top;
    dialogHeader.style.cursor = 'grabbing';
    e.preventDefault();
  });

  document.addEventListener('mousemove', function (e) {
    if (!isDragging) return;
    let newLeft = e.clientX - dragOffsetX;
    let newTop  = e.clientY - dragOffsetY;
    // 限制在视口内
    const rect = dialog.getBoundingClientRect();
    newLeft = Math.max(0, Math.min(newLeft, window.innerWidth  - rect.width));
    newTop  = Math.max(0, Math.min(newTop,  window.innerHeight - rect.height));
    dialog.style.left = newLeft + 'px';
    dialog.style.top  = newTop  + 'px';
  });

  document.addEventListener('mouseup', function () {
    if (isDragging) {
      isDragging = false;
      dialogHeader.style.cursor = '';
    }
  });

  // 点击文本文件链接
  document.addEventListener('click', async function (e) {
    const link = e.target.closest('.text-preview');
    if (!link) return;
    e.preventDefault();

    const rawURL = link.dataset.raw;
    const fileName = link.dataset.name;
    const dlURL = rawURL.replace('&raw=1', '');

    // 打开弹窗，显示加载状态
    dialogTitle.textContent = fileName;
    dialogDlBtn.href = dlURL;
    dialogDlBtn.download = fileName;
    dialogBody.innerHTML = '<div class="dialog-loading">⏳ 加载中…</div>';
    dialogFooter.textContent = '';
    // 居中显示（position:fixed 后需手动居中）
    dialog.style.left = '';
    dialog.style.top  = '';
    dialog.showModal();
    // 计算居中位置
    const dr = dialog.getBoundingClientRect();
    dialog.style.left = Math.max(0, (window.innerWidth  - dr.width)  / 2) + 'px';
    dialog.style.top  = Math.max(0, (window.innerHeight - dr.height) / 2) + 'px';

    try {
      const resp = await fetch(rawURL);
      if (!resp.ok) throw new Error('HTTP ' + resp.status);
      const text = await resp.text();

      // 行号 + 代码
      const lines = text.split('\n');
      // 末尾空行不计入行号
      const lineCount = lines[lines.length - 1] === '' ? lines.length - 1 : lines.length;

      const numCol = document.createElement('div');
      numCol.className = 'line-num-col';
      numCol.textContent = Array.from({length: lineCount}, (_, i) => i + 1).join('\n');

      const codeCol = document.createElement('div');
      codeCol.className = 'line-code-col';
      codeCol.textContent = text;

      const grid = document.createElement('div');
      grid.className = 'line-nums';
      grid.appendChild(numCol);
      grid.appendChild(codeCol);

      dialogBody.innerHTML = '';
      dialogBody.appendChild(grid);

      // 页脚信息
      const bytes = new TextEncoder().encode(text).length;
      dialogFooter.textContent =
        lineCount + ' 行  ·  ' + formatSize(bytes) + '  ·  ' + fileName;

    } catch (err) {
      dialogBody.innerHTML =
        '<div class="dialog-error">❌ 加载失败：' + escHtml(err.message) + '</div>';
    }
  });

  function formatSize(bytes) {
    if (bytes >= 1024 * 1024) return (bytes / 1024 / 1024).toFixed(2) + ' MB';
    if (bytes >= 1024) return (bytes / 1024).toFixed(1) + ' KB';
    return bytes + ' B';
  }

  function escHtml(s) {
    return s.replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;');
  }
})();
</script>

</body>
</html>
`
