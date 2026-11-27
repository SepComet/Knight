// Package account 账号业务实现：组合 store（持久化）与 auth（令牌/密码），
// 实现 httpapi.Accounts 接口。
package account

import (
	"context"
	"errors"

	"knight-server/internal/auth"
	"knight-server/internal/httpapi"
	"knight-server/internal/snowflake"
	"knight-server/internal/store"
)

// Service 账号业务服务。
type Service struct {
	store  *store.Store
	tokens *auth.TokenStore
	ids    *snowflake.Node
}

// NewService 构造。ids 为全局雪花 ID 生成器（uid 分配）。
func NewService(st *store.Store, tokens *auth.TokenStore, ids *snowflake.Node) *Service {
	return &Service{store: st, tokens: tokens, ids: ids}
}

// Register 注册账号：bcrypt 加盐哈希后落库；uid 由雪花生成；名称唯一。
func (s *Service) Register(ctx context.Context, name, password string) (uint64, error) {
	uid, err := s.ids.Next()
	if err != nil {
		return 0, err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return 0, err
	}
	if err := s.store.CreateUser(ctx, uid, name, hash); err != nil {
		if errors.Is(err, store.ErrNameTaken) {
			return 0, httpapi.ErrNameTaken
		}
		return 0, err
	}
	return uid, nil
}

// Login 校验凭证并签发令牌。账号不存在与密码错误统一返回
// ErrBadCredentials（不泄露账号是否存在）。
func (s *Service) Login(ctx context.Context, name, password string) (uint64, string, string, error) {
	u, hash, err := s.store.GetUserByName(ctx, name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return 0, "", "", httpapi.ErrBadCredentials
		}
		return 0, "", "", err
	}
	if !auth.VerifyPassword(hash, password) {
		return 0, "", "", httpapi.ErrBadCredentials
	}
	token := s.tokens.Create(u.UID, u.Name)
	return u.UID, u.Name, token, nil
}

// User 用户基本信息。
func (s *Service) User(ctx context.Context, uid uint64) (httpapi.AccountUser, error) {
	u, err := s.store.GetUserByID(ctx, uid)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return httpapi.AccountUser{}, httpapi.ErrAccountNotFound
		}
		return httpapi.AccountUser{}, err
	}
	return httpapi.AccountUser{
		UID:       u.UID,
		Name:      u.Name,
		Coins:     u.Coins,
		CreatedAt: u.CreatedAt,
	}, nil
}

// Matches 历史战绩（按完成时间倒序）。
func (s *Service) Matches(ctx context.Context, uid uint64) ([]httpapi.MatchEntry, error) {
	rows, err := s.store.ListMatchesByUID(ctx, uid)
	if err != nil {
		return nil, err
	}
	out := make([]httpapi.MatchEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, httpapi.MatchEntry{
			MatchID:     r.MatchID,
			Outcome:     r.Outcome,
			DurationSec: r.DurationSec,
			Seed:        r.Seed,
			CharacterID: r.CharacterID,
			WeaponID:    r.WeaponID,
			Kills:       r.Kills,
			DamageDealt: r.DamageDealt,
			DamageTaken: r.DamageTaken,
			Alive:       r.Alive,
			FinishedAt:  r.FinishedAt,
		})
	}
	return out, nil
}
