package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"
)

// ─────────────────────────────────────────────
// Session Store（内存，生产可换 Redis）
// ─────────────────────────────────────────────

const (
	sessionCookieName = "gq_session"
	sessionTTL        = 8 * time.Hour   // session 有效期
	sessionMaxAge     = 8 * 60 * 60     // cookie MaxAge（秒）
)

type session struct {
	username  string
	createdAt time.Time
	expiresAt time.Time
}

var sessionStore = struct {
	mu   sync.RWMutex
	data map[string]*session
}{data: map[string]*session{}}

// newSession 创建新 session，返回 token
func newSession(username string) string {
	b := make([]byte, 32)
	rand.Read(b)
	token := hex.EncodeToString(b)
	now := time.Now()
	sessionStore.mu.Lock()
	sessionStore.data[token] = &session{
		username:  username,
		createdAt: now,
		expiresAt: now.Add(sessionTTL),
	}
	sessionStore.mu.Unlock()
	return token
}

// getSession 验证 token，返回 session（nil 表示无效/过期）
func getSession(token string) *session {
	if token == "" {
		return nil
	}
	sessionStore.mu.RLock()
	s, ok := sessionStore.data[token]
	sessionStore.mu.RUnlock()
	if !ok || time.Now().After(s.expiresAt) {
		return nil
	}
	return s
}

// deleteSession 删除 session（登出）
func deleteSession(token string) {
	sessionStore.mu.Lock()
	delete(sessionStore.data, token)
	sessionStore.mu.Unlock()
}

// startSessionReaper 定期清理过期 session
func startSessionReaper() {
	go func() {
		for range time.Tick(30 * time.Minute) {
			now := time.Now()
			sessionStore.mu.Lock()
			for token, s := range sessionStore.data {
				if now.After(s.expiresAt) {
					delete(sessionStore.data, token)
				}
			}
			sessionStore.mu.Unlock()
		}
	}()
}

// ─────────────────────────────────────────────
// 凭据验证
// ─────────────────────────────────────────────

// dashboardCredentials 从环境变量读取 dashboard 登录凭据
// DASHBOARD_USER（默认 admin）
// DASHBOARD_PASS（默认 admin，生产环境请务必修改）
func dashboardCredentials() (string, string) {
	user := os.Getenv("DASHBOARD_USER")
	if user == "" {
		user = "admin"
	}
	pass := os.Getenv("DASHBOARD_PASS")
	if pass == "" {
		pass = "admin"
	}
	return user, pass
}

// hashPass 对密码做 SHA-256（防止明文比较时的时序攻击）
func hashPass(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// checkCredentials 验证用户名/密码
func checkCredentials(user, pass string) bool {
	wantUser, wantPass := dashboardCredentials()
	// 使用 hash 比较，避免短路时序攻击
	return hashPass(user) == hashPass(wantUser) &&
		hashPass(pass) == hashPass(wantPass)
}

// ─────────────────────────────────────────────
// 中间件：requireLogin
// ─────────────────────────────────────────────

// requireLogin 检查 session cookie，未登录则重定向到 /login
func requireLogin(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || getSession(cookie.Value) == nil {
			http.Redirect(w, r, "/login?next="+r.URL.Path, http.StatusFound)
			return
		}
		h(w, r)
	}
}

// ─────────────────────────────────────────────
// 登录页 HTML
// ─────────────────────────────────────────────

func loginPageHTML(errMsg string) string {
	errHTML := ""
	if errMsg != "" {
		errHTML = fmt.Sprintf(`<div class="error">%s</div>`, errMsg)
	}
	return `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>登录 — Go Queue Dashboard</title>
<style>
  *, *::before, *::after { box-sizing: border-box; margin: 0; padding: 0; }
  body {
    font-family: 'Segoe UI', system-ui, sans-serif;
    background: #0f172a;
    color: #e2e8f0;
    min-height: 100vh;
    display: flex;
    align-items: center;
    justify-content: center;
  }
  .card {
    background: #1e293b;
    border: 1px solid #334155;
    border-radius: 16px;
    padding: 48px 40px 40px;
    width: 100%;
    max-width: 400px;
    box-shadow: 0 25px 50px rgba(0,0,0,.5);
  }
  .logo {
    display: flex;
    align-items: center;
    gap: 12px;
    margin-bottom: 32px;
    justify-content: center;
  }
  .logo-icon {
    width: 44px; height: 44px;
    background: linear-gradient(135deg, #3b82f6, #1d4ed8);
    border-radius: 10px;
    display: flex; align-items: center; justify-content: center;
    font-size: 22px;
  }
  .logo h1 { font-size: 1.4rem; color: #60a5fa; font-weight: 700; }
  .logo p  { font-size: 0.75rem; color: #64748b; margin-top: 2px; }
  label {
    display: block;
    font-size: 0.8rem;
    color: #94a3b8;
    margin-bottom: 6px;
    font-weight: 500;
    letter-spacing: .03em;
  }
  input[type=text], input[type=password] {
    width: 100%;
    padding: 10px 14px;
    background: #0f172a;
    border: 1px solid #334155;
    border-radius: 8px;
    color: #e2e8f0;
    font-size: 0.95rem;
    outline: none;
    transition: border-color .2s;
    margin-bottom: 18px;
  }
  input:focus { border-color: #3b82f6; }
  button[type=submit] {
    width: 100%;
    padding: 11px;
    background: linear-gradient(135deg, #3b82f6, #1d4ed8);
    border: none;
    border-radius: 8px;
    color: #fff;
    font-size: 1rem;
    font-weight: 600;
    cursor: pointer;
    transition: opacity .2s, transform .1s;
    margin-top: 4px;
  }
  button[type=submit]:hover  { opacity: .9; }
  button[type=submit]:active { transform: scale(.98); }
  .error {
    background: rgba(239,68,68,.15);
    border: 1px solid rgba(239,68,68,.4);
    color: #fca5a5;
    border-radius: 8px;
    padding: 10px 14px;
    font-size: 0.85rem;
    margin-bottom: 20px;
    text-align: center;
  }
  .hint {
    text-align: center;
    font-size: 0.75rem;
    color: #475569;
    margin-top: 20px;
  }
</style>
</head>
<body>
<div class="card">
  <div class="logo">
    <div class="logo-icon">⚡</div>
    <div>
      <h1>Go Queue</h1>
      <p>Dashboard Login</p>
    </div>
  </div>
  ` + errHTML + `
  <form method="POST" action="/login">
    <label for="username">用户名</label>
    <input type="text" id="username" name="username" placeholder="admin" autocomplete="username" autofocus required>
    <label for="password">密码</label>
    <input type="password" id="password" name="password" placeholder="••••••••" autocomplete="current-password" required>
    <button type="submit">登 录</button>
  </form>
  <p class="hint">默认凭据可通过环境变量 DASHBOARD_USER / DASHBOARD_PASS 修改</p>
</div>
</body>
</html>`
}

// ─────────────────────────────────────────────
// HTTP Handlers
// ─────────────────────────────────────────────

// handleLoginPage GET /login — 显示登录表单
func handleLoginPage(w http.ResponseWriter, r *http.Request) {
	// 已登录则直接跳转 dashboard
	if cookie, err := r.Cookie(sessionCookieName); err == nil && getSession(cookie.Value) != nil {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, loginPageHTML(""))
}

// handleLoginSubmit POST /login — 处理登录表单提交
func handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	username := r.FormValue("username")
	password := r.FormValue("password")

	if !checkCredentials(username, password) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, loginPageHTML("用户名或密码错误，请重试"))
		return
	}

	// 创建 session
	token := newSession(username)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   sessionMaxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	// 跳转到 next 参数指定的页面，默认 /
	next := r.URL.Query().Get("next")
	if next == "" || next == "/login" {
		next = "/"
	}
	http.Redirect(w, r, next, http.StatusFound)
}

// handleLogout GET /logout — 登出
func handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		deleteSession(cookie.Value)
	}
	// 清除 cookie
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})
	http.Redirect(w, r, "/login", http.StatusFound)
}
