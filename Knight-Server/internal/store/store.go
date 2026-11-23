// Package store 封装 MySQL 持久化接入。
// 本阶段仅做：连接管理（含启动重试）、连通性 Ping、schema 基线迁移；
// 业务读写（账号/战绩等）随后续业务迭代落地。
package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// Store MySQL 持久化入口。
type Store struct {
	db *sql.DB
}

// Open 建立 MySQL 连接池并执行 schema 基线迁移。
// 启动阶段数据库可能尚未就绪（如 docker compose 内 mysql 健康检查刚通过），
// 因此 Ping 失败时做有界重试。
func Open(ctx context.Context, dsn string) (*Store, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}
	if err := pingWithRetry(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Ping 探测数据库连通性（供健康检查调用）。
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// Close 关闭底层连接池。
func (s *Store) Close() error { return s.db.Close() }

func pingWithRetry(ctx context.Context, db *sql.DB) error {
	const attempts = 10
	const interval = 500 * time.Millisecond
	var lastErr error
	for range attempts {
		if err := db.PingContext(ctx); err == nil {
			return nil
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("mysql ping: %w", ctx.Err())
		case <-time.After(interval):
		}
	}
	return fmt.Errorf("mysql ping after %d attempts: %w", attempts, lastErr)
}

// migrate 建立 schema 基线（幂等）。
// 表结构对齐 Docs/protocol.md §3 的账号与战绩契约：
//   - users：uid 由应用层雪花生成（无 AUTO_INCREMENT），coins = 蓝币
//   - characters：行存在 = 已持有该角色，未解锁不建行
//   - matches / match_players：战绩历史；玩家明细含对局时角色/武器 config_id（快照，不外键）
//
// CREATE IF NOT EXISTS 不会改已有表结构：检测到旧基线（缺关键列）时报可操作错误。
func (s *Store) migrate(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS users (
		    uid           BIGINT UNSIGNED PRIMARY KEY,
		    name          VARCHAR(64)  NOT NULL UNIQUE,
		    password_hash VARCHAR(255) NOT NULL,
		    coins         INT UNSIGNED NOT NULL DEFAULT 0,
		    created_at    TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS characters (
		    uid          BIGINT UNSIGNED NOT NULL,
		    character_id INT UNSIGNED    NOT NULL, -- Luban 角色配置 ID（与 EntityState.config_id 同约定）
		    level        INT UNSIGNED    NOT NULL DEFAULT 1,
		    exp          INT UNSIGNED    NOT NULL DEFAULT 0,
		    acquired_at  TIMESTAMP(3)    NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
		    PRIMARY KEY (uid, character_id),
		    CONSTRAINT fk_characters_user FOREIGN KEY (uid) REFERENCES users(uid) ON DELETE CASCADE
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS matches (
		    match_id     BIGINT UNSIGNED PRIMARY KEY,
		    outcome      TINYINT UNSIGNED NOT NULL, -- GameOutcome 枚举值：1=Victory 2=Defeat
		    duration_sec INT UNSIGNED NOT NULL,
		    finished_at  TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS match_players (
		    match_id      BIGINT UNSIGNED NOT NULL,
		    uid           BIGINT UNSIGNED NOT NULL,
		    character_id  INT UNSIGNED    NOT NULL, -- 对局时使用的角色 config_id（快照）
		    weapon_id     INT UNSIGNED    NOT NULL, -- 对局时装备的武器 config_id（快照）
		    kills         INT UNSIGNED    NOT NULL,
		    damage_dealt  INT             NOT NULL,
		    damage_taken  INT             NOT NULL,
		    alive         TINYINT(1)      NOT NULL, -- 0/1
		    PRIMARY KEY (match_id, uid),
		    INDEX idx_match_players_uid (uid),
		    CONSTRAINT fk_match_players_match FOREIGN KEY (match_id) REFERENCES matches(match_id) ON DELETE CASCADE,
		    CONSTRAINT fk_match_players_user  FOREIGN KEY (uid) REFERENCES users(uid) ON DELETE CASCADE
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	return s.requireSchema(ctx)
}

// requireSchema 校验关键列存在；旧基线数据给出清库指引，避免运行期才暴露。
func (s *Store) requireSchema(ctx context.Context) error {
	type required struct{ table, column string }
	checks := []required{
		{"users", "coins"},          // 旧基线 users 无 coins
		{"users", "uid"},            // 存在性兜底
		{"characters", "character_id"},
		{"matches", "match_id"},
		{"match_players", "character_id"}, // 旧基线无角色/武器列
	}
	for _, c := range checks {
		var cnt int
		err := s.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM information_schema.columns
			 WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?`,
			c.table, c.column).Scan(&cnt)
		if err != nil {
			return err
		}
		if cnt != 1 {
			return fmt.Errorf(
				"migrate: 表 %s 缺少列 %s —— 检测到旧版基线数据，请清库后重启（DROP TABLE users, characters, matches, match_players）",
				c.table, c.column)
		}
	}
	return nil
}
