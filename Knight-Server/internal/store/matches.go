package store

import (
	"context"
	"time"
)

// MatchPlayerRow 对局中单个玩家的结算明细（对齐 GameResult.PlayerResult，
// 外加对局时角色/武器快照）。
type MatchPlayerRow struct {
	UID         uint64
	CharacterID uint32 // 对局时使用的角色 config_id
	WeaponID    uint32 // 对局时装备的武器 config_id
	Kills       uint32
	DamageDealt int32
	DamageTaken int32
	Alive       bool
}

// MatchRow 一条历史战绩（对齐 protocol.md §3 的 /matches 列表项）。
type MatchRow struct {
	MatchID     uint64
	Outcome     uint32 // knight.GameOutcome：1=Victory 2=Defeat
	DurationSec uint32
	CharacterID uint32
	WeaponID    uint32
	Kills       uint32
	DamageDealt int32
	DamageTaken int32
	Alive       bool
	FinishedAt  time.Time
}

// InsertMatch 写入一局结算（match 主记录 + 全体玩家明细，事务）。
// matchID 由应用层雪花生成后传入；uid 不存在的玩家会因外键约束写入失败。
func (s *Store) InsertMatch(ctx context.Context, matchID uint64, outcome uint32, durationSec uint32, players []MatchPlayerRow) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO matches (match_id, outcome, duration_sec) VALUES (?, ?, ?)`,
		matchID, outcome, durationSec); err != nil {
		return err
	}

	for _, p := range players {
		alive := 0
		if p.Alive {
			alive = 1
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO match_players (match_id, uid, character_id, weapon_id, kills, damage_dealt, damage_taken, alive)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			matchID, p.UID, p.CharacterID, p.WeaponID, p.Kills, p.DamageDealt, p.DamageTaken, alive); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// ListMatchesByUID 按 finished_at 倒序列出玩家全部战绩。
// 账号不存在无法在单查询中区分——调用方先用 GetUserByID 校验。
func (s *Store) ListMatchesByUID(ctx context.Context, uid uint64) ([]MatchRow, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT m.match_id, m.outcome, m.duration_sec,
       p.character_id, p.weapon_id,
       p.kills, p.damage_dealt, p.damage_taken, p.alive, m.finished_at
FROM match_players p
JOIN matches m ON m.match_id = p.match_id
WHERE p.uid = ?
ORDER BY m.finished_at DESC, m.match_id DESC`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []MatchRow
	for rows.Next() {
		var r MatchRow
		var finishedAt time.Time
		var alive int
		if err := rows.Scan(&r.MatchID, &r.Outcome, &r.DurationSec,
			&r.CharacterID, &r.WeaponID,
			&r.Kills, &r.DamageDealt, &r.DamageTaken, &alive, &finishedAt); err != nil {
			return nil, err
		}
		r.Alive = alive != 0
		r.FinishedAt = finishedAt
		out = append(out, r)
	}
	return out, rows.Err()
}
