// Package httpapi 提供业务 HTTP 接口：
//   - GET  /healthz                  服务与数据库连通性探测
//   - POST /api/v1/register          注册（protocol.md §3）
//   - POST /api/v1/login             登录换 token
//   - GET  /api/v1/users/{uid}       用户信息（Bearer）
//   - GET  /api/v1/users/{uid}/matches  历史战绩（Bearer）
//
// 错误统一 4xx/5xx + {"error": "<描述>"}。
package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"
)

// Pinger 数据库连通性探测抽象，解耦 store 实现便于测试。
type Pinger interface {
	Ping(ctx context.Context) error
}

// Server HTTP 接口服务。
type Server struct {
	db       Pinger
	accounts Accounts
	tokens   TokenLookup
}

// NewServer 构造 HTTP 接口服务。
// db 可为 nil（healthz 只报服务状态）；accounts/tokens 为 nil 时账号路由返回 503。
func NewServer(db Pinger, accounts Accounts, tokens TokenLookup) *Server {
	return &Server{db: db, accounts: accounts, tokens: tokens}
}

// Handler 返回全部路由（含访问日志中间件）。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("POST /api/v1/register", s.guardAccounts(s.handleRegister))
	mux.HandleFunc("POST /api/v1/login", s.guardAccounts(s.handleLogin))
	mux.HandleFunc("GET /api/v1/users/{uid}", s.guardAccounts(s.handleUser))
	mux.HandleFunc("GET /api/v1/users/{uid}/matches", s.guardAccounts(s.handleMatches))
	return s.logRequests(mux)
}

// logRequests 访问日志中间件：每个请求一行（方法/路径/状态码/耗时/来源）。
// 级别按状态码分档：2xx/3xx=Info、4xx=Warn、5xx=Error；
// 顺带兜底 handler panic（记录堆栈并返回 500），避免 net/http 默认静默断连。
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("http handler panic",
					"method", r.Method, "path", r.URL.Path, "panic", rec,
					"stack", string(debug.Stack()))
				writeError(sw, http.StatusInternalServerError, "internal error")
			}
			level := slog.LevelInfo
			switch {
			case sw.status >= 500:
				level = slog.LevelError
			case sw.status >= 400:
				level = slog.LevelWarn
			}
			slog.Log(context.Background(), level, "http request",
				"method", r.Method, "path", r.URL.Path, "status", sw.status,
				"duration_ms", time.Since(start).Milliseconds(), "remote", r.RemoteAddr)
		}()
		next.ServeHTTP(sw, r)
	})
}

// statusWriter 捕获响应状态码与响应字节数的包装器。
type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

// guardAccounts 账号路由的前置：未装配业务实现时返回 503。
func (s *Server) guardAccounts(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.accounts == nil || s.tokens == nil {
			writeError(w, http.StatusServiceUnavailable, "accounts not available")
			return
		}
		next(w, r)
	}
}

// healthzResponse 健康检查响应体。
type healthzResponse struct {
	Status string `json:"status"` // ok / degraded
	DB     string `json:"db"`     // up / down / disabled
	Error  string `json:"error,omitempty"`
}

// handleHealthz GET /healthz —— 服务存活 + MySQL 连通性。
// 数据库不可达返回 503，便于 compose/编排做健康检查。
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	resp := healthzResponse{Status: "ok", DB: "disabled"}
	if s.db != nil {
		resp.DB = "up"
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := s.db.Ping(ctx); err != nil {
			resp.Status = "degraded"
			resp.DB = "down"
			resp.Error = err.Error()
			writeJSON(w, http.StatusServiceUnavailable, resp)
			return
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
