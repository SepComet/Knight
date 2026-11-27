package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
)

// TestOpenAgainstMySQL 集成测试：需真实 MySQL。
// 默认跳过；设置 KNIGHT_TEST_MYSQL_DSN 后执行，
// 例：KNIGHT_TEST_MYSQL_DSN="knight:knight@tcp(127.0.0.1:3306)/knight?parseTime=true" go test ./internal/store/
func TestOpenAgainstMySQL(t *testing.T) {
	dsn := os.Getenv("KNIGHT_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("KNIGHT_TEST_MYSQL_DSN not set; skip mysql integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	st, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	if err := st.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}

	// 验证迁移幂等且四张基线表存在
	if err := st.migrate(ctx); err != nil {
		t.Fatalf("re-migrate: %v", err)
	}
	for _, table := range []string{"users", "characters", "matches", "match_players"} {
		var cnt int
		err := st.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?`,
			table).Scan(&cnt)
		if err != nil {
			t.Fatalf("query information_schema: %v", err)
		}
		if cnt != 1 {
			t.Fatalf("table %s missing", table)
		}
	}

	testUsersAndCoins(ctx, t, st)
	testCharacters(ctx, t, st)
	testMatches(ctx, t, st)
}

// newTestUser 建唯一名测试账号。
func newTestUser(ctx context.Context, t *testing.T, st *Store) (uint64, string) {
	t.Helper()
	// 唯一后缀：集成测试可重复执行；uid 模拟应用层雪花分配
	name := fmt.Sprintf("integration-user-%d", time.Now().UnixNano())
	uid := uint64(time.Now().UnixNano()) // 数值大且唯一，仅测试用
	if err := st.CreateUser(ctx, uid, name, "hash-abc"); err != nil {
		t.Fatalf("create user: %v", err)
	}
	return uid, name
}

// testUsersAndCoins 注册/查询 + 蓝币加减全流程。
func testUsersAndCoins(ctx context.Context, t *testing.T, st *Store) {
	t.Helper()
	uid, name := newTestUser(ctx, t, st)

	// 重名 → ErrNameTaken
	if err := st.CreateUser(ctx, uid+1, name, "hash-xyz"); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("duplicate name err = %v, want ErrNameTaken", err)
	}

	// 按名查（登录路径）
	u, hash, err := st.GetUserByName(ctx, name)
	if err != nil || u.UID != uid || hash != "hash-abc" || u.Coins != 0 {
		t.Fatalf("get by name = %+v %q %v", u, hash, err)
	}

	// 按 ID 查
	u2, err := st.GetUserByID(ctx, uid)
	if err != nil || u2.Name != name || u2.CreatedAt.IsZero() || u2.Coins != 0 {
		t.Fatalf("get by id = %+v %v", u2, err)
	}

	// 不存在
	if _, _, err := st.GetUserByName(ctx, "nobody"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing user err = %v", err)
	}
	if _, err := st.GetUserByID(ctx, uid+999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing user by id err = %v", err)
	}

	// 蓝币：入账 → 查 → 扣减 → 超额扣减
	if err := st.AddCoins(ctx, uid, 100); err != nil {
		t.Fatalf("add coins: %v", err)
	}
	if u3, _ := st.GetUserByID(ctx, uid); u3.Coins != 100 {
		t.Fatalf("coins after add = %d, want 100", u3.Coins)
	}
	if err := st.SpendCoins(ctx, uid, 30); err != nil {
		t.Fatalf("spend coins: %v", err)
	}
	if u4, _ := st.GetUserByID(ctx, uid); u4.Coins != 70 {
		t.Fatalf("coins after spend = %d, want 70", u4.Coins)
	}
	if err := st.SpendCoins(ctx, uid, 999); !errors.Is(err, ErrInsufficientCoins) {
		t.Fatalf("overspend err = %v, want ErrInsufficientCoins", err)
	}
	if err := st.SpendCoins(ctx, uid+999, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("spend missing user err = %v, want ErrNotFound", err)
	}
	if err := st.AddCoins(ctx, uid+999, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("add missing user err = %v, want ErrNotFound", err)
	}
}

// testCharacters 角色解锁/查询全流程。
func testCharacters(ctx context.Context, t *testing.T, st *Store) {
	t.Helper()
	uid, _ := newTestUser(ctx, t, st)

	newly, err := st.GrantCharacter(ctx, uid, 1)
	if err != nil || !newly {
		t.Fatalf("grant = %v, %v; want newly granted", newly, err)
	}
	// 重复解锁幂等
	newly, err = st.GrantCharacter(ctx, uid, 1)
	if err != nil || newly {
		t.Fatalf("re-grant = %v, %v; want idempotent false", newly, err)
	}
	if _, err := st.GrantCharacter(ctx, uid, 2); err != nil {
		t.Fatalf("grant 2: %v", err)
	}

	rows, err := st.ListCharacters(ctx, uid)
	if err != nil || len(rows) != 2 {
		t.Fatalf("list = %v, %v", rows, err)
	}
	if rows[0].CharacterID != 1 || rows[1].CharacterID != 2 {
		t.Fatalf("order wrong: %+v", rows)
	}
	if rows[0].Level != 1 || rows[0].Exp != 0 || rows[0].AcquiredAt.IsZero() {
		t.Fatalf("defaults wrong: %+v", rows[0])
	}

	// 无持有 → 空列表非错误
	if rows, err := st.ListCharacters(ctx, uid+999); err != nil || len(rows) != 0 {
		t.Fatalf("empty list = %v, %v", rows, err)
	}
}

// testMatches 对局写入/查询全流程（含角色/武器快照与外键约束）。
func testMatches(ctx context.Context, t *testing.T, st *Store) {
	t.Helper()
	uidA, _ := newTestUser(ctx, t, st)
	uidB, _ := newTestUser(ctx, t, st)

	matchID := uint64(time.Now().UnixNano())
	const testSeed uint64 = 9876543210123
	err := st.InsertMatchWithSeed(ctx, matchID, 1, 180, testSeed, []MatchPlayerRow{
		{UID: uidA, CharacterID: 1, WeaponID: 7, Kills: 5, DamageDealt: 800, DamageTaken: 200, Alive: true},
		{UID: uidB, CharacterID: 1, WeaponID: 7, Kills: 2, DamageDealt: 300, DamageTaken: 500, Alive: false},
	})
	if err != nil {
		t.Fatalf("insert match: %v", err)
	}

	// 查询战绩
	rows, err := st.ListMatchesByUID(ctx, uidA)
	if err != nil || len(rows) != 1 {
		t.Fatalf("list matches = %v, %v", rows, err)
	}
	r := rows[0]
	if r.MatchID != matchID || r.Outcome != 1 || r.DurationSec != 180 || r.Seed != testSeed ||
		r.CharacterID != 1 || r.WeaponID != 7 ||
		r.Kills != 5 || r.DamageDealt != 800 || r.DamageTaken != 200 || !r.Alive || r.FinishedAt.IsZero() {
		t.Fatalf("match row = %+v", r)
	}

	// uidB 同局可见
	if rows, err := st.ListMatchesByUID(ctx, uidB); err != nil || len(rows) != 1 {
		t.Fatalf("list matches B = %v, %v", rows, err)
	}

	// 不存在的 uid 违反外键 → 写入失败
	err = st.InsertMatch(ctx, matchID+1, 1, 60, []MatchPlayerRow{
		{UID: uidA + 999999, CharacterID: 1, WeaponID: 7},
	})
	if err == nil {
		t.Fatal("insert with unknown uid should fail (FK)")
	}

	// 无战绩玩家 → 空列表非错误
	if rows, err := st.ListMatchesByUID(ctx, uidA+999); err != nil || len(rows) != 0 {
		t.Fatalf("empty matches = %v, %v", rows, err)
	}
}
