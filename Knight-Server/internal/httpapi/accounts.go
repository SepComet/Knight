package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// 账号业务错误（由 Accounts 实现返回，HTTP 层映射状态码）。
var (
	ErrNameTaken       = errors.New("name already taken")
	ErrBadCredentials  = errors.New("bad credentials")
	ErrAccountNotFound = errors.New("account not found")
)

// AccountUser 用户基本信息（GET /users/{uid} 响应体）。
// uid 为雪花 ID（超 2^53）：JSON 里序列化为字符串，避免 JS 端静默丢精度。
type AccountUser struct {
	UID       uint64    `json:"uid,string"`
	Name      string    `json:"name"`
	Coins     uint64    `json:"coins"` // 蓝币
	CreatedAt time.Time `json:"created_at"`
}

// MatchEntry 历史战绩列表项（GET /users/{uid}/matches 响应体，
// 字段名对齐 Docs/protocol.md §3 契约）。
type MatchEntry struct {
	MatchID     uint64    `json:"match_id,string"` // 雪花 ID：字符串化
	Outcome     uint32    `json:"outcome"`
	DurationSec uint32    `json:"duration_sec"`
	Seed        uint64    `json:"seed,string"` // 本局随机种子：字符串化防 JS 丢精度
	CharacterID uint32    `json:"character_id"` // 对局时使用的角色 config_id
	WeaponID    uint32    `json:"weapon_id"`    // 对局时装备的武器 config_id
	Kills       uint32    `json:"kills"`
	DamageDealt int32     `json:"damage_dealt"`
	DamageTaken int32     `json:"damage_taken"`
	Alive       bool      `json:"alive"`
	FinishedAt  time.Time `json:"finished_at"`
}

// Accounts 账号业务抽象（生产实现见 internal/account，测试用 fake）。
type Accounts interface {
	Register(ctx context.Context, name, password string) (uint64, error)
	Login(ctx context.Context, name, password string) (uid uint64, displayName string, token string, err error)
	User(ctx context.Context, uid uint64) (AccountUser, error)
	Matches(ctx context.Context, uid uint64) ([]MatchEntry, error)
}

// TokenLookup Bearer 令牌校验抽象（实现见 internal/auth）。
type TokenLookup interface {
	Lookup(token string) (uid uint64, name string, ok bool)
}

// 请求体大小上限，防滥用
const maxBodyBytes = 4 << 10

// registerReq / loginReq 请求体。
type authReq struct {
	Name     string `json:"name"`
	Password string `json:"password"`
}

// handleRegister POST /api/v1/register {name,password} → 200 {uid} / 409 / 400
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeAuthReq(w, r)
	if !ok {
		return
	}
	uid, err := s.accounts.Register(r.Context(), req.Name, req.Password)
	if err != nil {
		switch {
		case errors.Is(err, ErrNameTaken):
			slog.Info("register rejected", "name", req.Name, "reason", "name taken")
			writeError(w, http.StatusConflict, "name already taken")
		default:
			slog.Error("register failed", "name", req.Name, "err", err)
			writeError(w, http.StatusInternalServerError, "internal error")
		}
		return
	}
	slog.Info("register ok", "uid", uid, "name", req.Name)
	writeJSON(w, http.StatusOK, uidResponse{UID: uid})
}

// uidResponse 注册响应体（uid 字符串化，同 AccountUser 注释）。
type uidResponse struct {
	UID uint64 `json:"uid,string"`
}

// handleLogin POST /api/v1/login {name,password} → 200 {uid,name,token} / 401 / 400
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeAuthReq(w, r)
	if !ok {
		return
	}
	uid, name, token, err := s.accounts.Login(r.Context(), req.Name, req.Password)
	if err != nil {
		switch {
		case errors.Is(err, ErrBadCredentials):
			slog.Info("login rejected", "name", req.Name, "reason", "bad credentials")
			writeError(w, http.StatusUnauthorized, "invalid credentials")
		default:
			slog.Error("login failed", "name", req.Name, "err", err)
			writeError(w, http.StatusInternalServerError, "internal error")
		}
		return
	}
	slog.Info("login ok", "uid", uid, "name", name)
	writeJSON(w, http.StatusOK, loginResponse{UID: uid, Name: name, Token: token})
}

// loginResponse 登录响应体（uid 字符串化）。
type loginResponse struct {
	UID   uint64 `json:"uid,string"`
	Name  string `json:"name"`
	Token string `json:"token"`
}

// handleUser GET /api/v1/users/{uid}（Bearer）→ 200 {uid,name,created_at} / 401 / 404
func (s *Server) handleUser(w http.ResponseWriter, r *http.Request) {
	if !s.bearerOK(w, r) {
		return
	}
	uid, ok := parseUID(w, r)
	if !ok {
		return
	}
	u, err := s.accounts.User(r.Context(), uid)
	if err != nil {
		if errors.Is(err, ErrAccountNotFound) {
			writeError(w, http.StatusNotFound, "user not found")
		} else {
			slog.Error("user query failed", "uid", uid, "err", err)
			writeError(w, http.StatusInternalServerError, "internal error")
		}
		return
	}
	writeJSON(w, http.StatusOK, u)
}

// handleMatches GET /api/v1/users/{uid}/matches（Bearer）→ 200 [...] / 401 / 404
func (s *Server) handleMatches(w http.ResponseWriter, r *http.Request) {
	if !s.bearerOK(w, r) {
		return
	}
	uid, ok := parseUID(w, r)
	if !ok {
		return
	}
	// 账号存在性校验：不存在 → 404；存在而无对局 → 200 []
	if _, err := s.accounts.User(r.Context(), uid); err != nil {
		if errors.Is(err, ErrAccountNotFound) {
			writeError(w, http.StatusNotFound, "user not found")
		} else {
			slog.Error("user query failed", "uid", uid, "err", err)
			writeError(w, http.StatusInternalServerError, "internal error")
		}
		return
	}
	matches, err := s.accounts.Matches(r.Context(), uid)
	if err != nil {
		slog.Error("matches query failed", "uid", uid, "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if matches == nil {
		matches = []MatchEntry{}
	}
	writeJSON(w, http.StatusOK, matches)
}

// ---------------------------------------------------------------- 辅助

func decodeAuthReq(w http.ResponseWriter, r *http.Request) (authReq, bool) {
	var req authReq
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		slog.Warn("invalid auth request body", "path", r.URL.Path, "err", err)
		writeError(w, http.StatusBadRequest, "invalid body")
		return req, false
	}
	req.Name = strings.TrimSpace(req.Name)
	// 名称 1~64 字符；密码 1~72（bcrypt 输入上限）
	if len(req.Name) == 0 || len(req.Name) > 64 || len(req.Password) == 0 || len(req.Password) > 72 {
		writeError(w, http.StatusBadRequest, "invalid name or password length")
		return req, false
	}
	return req, true
}

// bearerOK 校验 Authorization: Bearer <token>；失败时已写 401 响应。
func (s *Server) bearerOK(w http.ResponseWriter, r *http.Request) bool {
	const prefix = "Bearer "
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, prefix) {
		writeError(w, http.StatusUnauthorized, "missing bearer token")
		return false
	}
	if _, _, ok := s.tokens.Lookup(strings.TrimPrefix(auth, prefix)); !ok {
		writeError(w, http.StatusUnauthorized, "invalid token")
		return false
	}
	return true
}

func parseUID(w http.ResponseWriter, r *http.Request) (uint64, bool) {
	uid, err := strconv.ParseUint(r.PathValue("uid"), 10, 64)
	if err != nil || uid == 0 {
		writeError(w, http.StatusNotFound, "user not found")
		return 0, false
	}
	return uid, true
}

func writeError(w http.ResponseWriter, code int, message string) {
	writeJSON(w, code, map[string]string{"error": message})
}
