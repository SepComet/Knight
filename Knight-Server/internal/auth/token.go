// Package auth 提供登录令牌管理与密码哈希。
// token 为内存态（Docs/protocol.md §3：随机 128 位串、24h 有效、进程重启失效，MVP 接受）；
// 密码 bcrypt 加盐哈希，不落明文。
package auth

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// TokenStore 内存态登录令牌表。
type TokenStore struct {
	mu   sync.Mutex
	ttl  time.Duration
	toks map[string]tokenEntry
}

type tokenEntry struct {
	uid       uint64
	name      string
	expiresAt time.Time
}

// NewTokenStore 构造令牌表，ttl 为令牌有效期（如 24h）。
func NewTokenStore(ttl time.Duration) *TokenStore {
	return &TokenStore{ttl: ttl, toks: make(map[string]tokenEntry)}
}

// Create 为账号签发新令牌。
func (s *TokenStore) Create(uid uint64, name string) string {
	var raw [16]byte // 128 位
	if _, err := rand.Read(raw[:]); err != nil {
		panic("auth: crypto/rand failed: " + err.Error())
	}
	token := hex.EncodeToString(raw[:])

	s.mu.Lock()
	s.toks[token] = tokenEntry{uid: uid, name: name, expiresAt: time.Now().Add(s.ttl)}
	s.mu.Unlock()
	return token
}

// Lookup 校验令牌，返回账号信息；无效或过期返回 ok=false（过期条目顺带清除）。
func (s *TokenStore) Lookup(token string) (uid uint64, name string, ok bool) {
	return s.validate(token)
}

// Validate 与 Lookup 等价；满足 session.TokenValidator 接口（KCP 握手用）。
func (s *TokenStore) Validate(token string) (uid uint64, name string, ok bool) {
	return s.validate(token)
}

func (s *TokenStore) validate(token string) (uid uint64, name string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, exist := s.toks[token]
	if !exist {
		return 0, "", false
	}
	if time.Now().After(e.expiresAt) {
		delete(s.toks, token)
		return 0, "", false
	}
	return e.uid, e.name, true
}

// Revoke 吊销令牌（登出/踢出场景，MVP 暂无调用方）。
func (s *TokenStore) Revoke(token string) {
	s.mu.Lock()
	delete(s.toks, token)
	s.mu.Unlock()
}

// Len 当前有效令牌数（诊断用，含未过期条目）。
func (s *TokenStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	now := time.Now()
	for _, e := range s.toks {
		if now.Before(e.expiresAt) {
			n++
		}
	}
	return n
}
