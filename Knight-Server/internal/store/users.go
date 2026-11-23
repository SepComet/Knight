package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/go-sql-driver/mysql"
)

// 领域错误（HTTP 层映射为 409/404 等）。
var (
	ErrNameTaken         = errors.New("store: name already taken")
	ErrNotFound          = errors.New("store: not found")
	ErrInsufficientCoins = errors.New("store: insufficient coins")
)

// User 账号信息。
type User struct {
	UID       uint64
	Name      string
	Coins     uint64 // 蓝币
	CreatedAt time.Time
}

// CreateUser 注册账号；uid 由应用层雪花生成，名称唯一冲突返回 ErrNameTaken。
func (s *Store) CreateUser(ctx context.Context, uid uint64, name, passwordHash string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO users (uid, name, password_hash) VALUES (?, ?, ?)`, uid, name, passwordHash)
	if err != nil {
		if isDuplicateEntry(err) {
			return ErrNameTaken
		}
		return err
	}
	return nil
}

// GetUserByName 按名称查账号（登录用）。
func (s *Store) GetUserByName(ctx context.Context, name string) (User, string, error) {
	var u User
	var hash string
	var createdAt time.Time
	err := s.db.QueryRowContext(ctx,
		`SELECT uid, name, coins, password_hash, created_at FROM users WHERE name = ?`, name).
		Scan(&u.UID, &u.Name, &u.Coins, &hash, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, "", ErrNotFound
	}
	if err != nil {
		return User{}, "", err
	}
	u.CreatedAt = createdAt
	return u, hash, nil
}

// GetUserByID 按 ID 查账号。
func (s *Store) GetUserByID(ctx context.Context, uid uint64) (User, error) {
	var u User
	var createdAt time.Time
	err := s.db.QueryRowContext(ctx,
		`SELECT uid, name, coins, created_at FROM users WHERE uid = ?`, uid).
		Scan(&u.UID, &u.Name, &u.Coins, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	u.CreatedAt = createdAt
	return u, nil
}

// AddCoins 蓝币入账（奖励/补偿），amount 为 0 视为无操作。
// 账号不存在返回 ErrNotFound。
func (s *Store) AddCoins(ctx context.Context, uid, amount uint64) error {
	if amount == 0 {
		return nil
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET coins = coins + ? WHERE uid = ?`, amount, uid)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SpendCoins 蓝币扣减：WHERE coins >= ? 保证余额充足才扣（原子，防负数）。
// amount 为 0 视为无操作。余额不足返回 ErrInsufficientCoins，账号不存在返回 ErrNotFound。
func (s *Store) SpendCoins(ctx context.Context, uid, amount uint64) error {
	if amount == 0 {
		return nil
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET coins = coins - ? WHERE uid = ? AND coins >= ?`, amount, uid, amount)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// 区分账号不存在与余额不足（低频路径，多一次点查）
		if _, err := s.GetUserByID(ctx, uid); errors.Is(err, ErrNotFound) {
			return ErrNotFound
		}
		return ErrInsufficientCoins
	}
	return nil
}

// isDuplicateEntry 识别 MySQL 唯一键冲突（error 1062）。
func isDuplicateEntry(err error) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && me.Number == 1062
}
