package store

import (
	"context"
	"time"
)

// CharacterRow 玩家持有角色（行存在 = 已持有；未解锁不建行）。
type CharacterRow struct {
	CharacterID uint32    // Luban 角色配置 ID
	Level       uint32
	Exp         uint32
	AcquiredAt  time.Time
}

// GrantCharacter 解锁角色（幂等：已持有则直接成功）。返回是否为本次新解锁。
func (s *Store) GrantCharacter(ctx context.Context, uid uint64, characterID uint32) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT IGNORE INTO characters (uid, character_id) VALUES (?, ?)`, uid, characterID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ListCharacters 持有角色列表，按获得时间正序。
func (s *Store) ListCharacters(ctx context.Context, uid uint64) ([]CharacterRow, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT character_id, level, exp, acquired_at
FROM characters
WHERE uid = ?
ORDER BY acquired_at, character_id`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []CharacterRow
	for rows.Next() {
		var r CharacterRow
		if err := rows.Scan(&r.CharacterID, &r.Level, &r.Exp, &r.AcquiredAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
