package world

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"knight-server/internal/event"
	"knight-server/internal/tick"

	knight "knight-server/protogen"
)

type fakeMatchRecorder struct {
	mu      sync.Mutex
	records []recordedMatch
	nextID  uint64
}

type recordedMatch struct {
	matchID     uint64
	outcome     knight.GameOutcome
	durationSec uint32
	seed        uint64
	players     []MatchPlayerStat
}

func (f *fakeMatchRecorder) RecordMatch(_ context.Context, outcome knight.GameOutcome, durationSec uint32, seed uint64, players []MatchPlayerStat) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	id := 1000 + f.nextID
	f.records = append(f.records, recordedMatch{
		matchID:     id,
		outcome:     outcome,
		durationSec: durationSec,
		seed:        seed,
		players:     append([]MatchPlayerStat(nil), players...),
	})
	return id, nil
}

func (f *fakeMatchRecorder) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.records)
}

func (f *fakeMatchRecorder) first() recordedMatch {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.records[0]
}

func TestDungeonGeneratorAndPrefabPool(t *testing.T) {
	ids := AllMapIDs()
	if len(ids) != 9 {
		t.Fatalf("expected 9 prefab rooms, got %d", len(ids))
	}
	for _, id := range ids {
		m, ok := GetMap(id)
		if !ok {
			t.Fatalf("map %d missing", id)
		}
		if m.Width != 30 || m.Height != 20 {
			t.Fatalf("map %d unexpected size %.0fx%.0f", id, m.Width, m.Height)
		}
		if len(m.Spawns) < 4 {
			t.Fatalf("map %d needs at least 4 spawns", id)
		}
		for i, sp := range m.Spawns {
			if circleHitsAnyWall(sp, DefaultPlayerRadius, m) {
				t.Fatalf("map %d spawn[%d] %+v overlaps wall", id, i, sp)
			}
		}
	}

	// 同 Seed 确定性复现
	seqA := GenerateDungeon(123456)
	seqB := GenerateDungeon(123456)
	if len(seqA) != 4 || fmt.Sprint(seqA) != fmt.Sprint(seqB) {
		t.Fatalf("non-deterministic dungeon: %v vs %v", seqA, seqB)
	}
	if seqA[0] != Map1ID || seqA[len(seqA)-1] != BossMapID {
		t.Fatalf("dungeon must start at %d and end at %d, got %v", Map1ID, BossMapID, seqA)
	}

	// 不同 Seed 组合不同且单局模板不重复
	seen := make(map[string]struct{})
	for s := uint64(1); s <= 50; s++ {
		seq := GenerateDungeon(s * 9973)
		if len(seq) != 4 {
			t.Fatalf("seed %d: len=%d", s, len(seq))
		}
		uniq := make(map[uint32]struct{}, len(seq))
		for _, id := range seq {
			if _, dup := uniq[id]; dup {
				t.Fatalf("seed %d produced duplicate map_id %d in %v", s, id, seq)
			}
			uniq[id] = struct{}{}
		}
		seen[fmt.Sprint(seq)] = struct{}{}
	}
	if len(seen) < 4 {
		t.Fatalf("expected multiple distinct dungeon combinations across seeds, got %d", len(seen))
	}
}

func TestCombatWeaponsAndKillStats(t *testing.T) {
	sender := newFakeSender()
	room, _ := newRoom(roomConfig{
		roomID:             1,
		seed:               42,
		mapIDs:             []uint32{6}, // 补给房默认 0 怪，方便精确摆放测试靶子
		playerUIDs:         []uint64{101},
		loadedTimeoutTicks: 90,
		snapshotEveryTicks: 2,
		moveSpeed:          DefaultMoveSpeed,
		playerRadius:       DefaultPlayerRadius,
		playerHP:           DefaultPlayerHP,
		uidSender:          sender,
	})
	room.handleRoomLoaded(101, 6)
	sender.drain(101)

	p := room.byUID[101]
	p.Pos = Vec2{X: 10, Y: 10}
	p.Facing = 0

	// 1. 步枪单发 + 冷却校验
	e := room.spawnEnemy(EnemySpawnSpec{Pos: Vec2{X: 14, Y: 10}, ConfigID: EnemyConfigMelee})
	room.applyInput(p, &knight.InputCmd{Seq: 1, AimAngle: 0, Buttons: uint32(knight.InputButtons_BTN_ATTACK)}, 1.0/30.0)
	if len(room.projectiles) != 1 {
		t.Fatalf("rifle should spawn 1 projectile, got %d", len(room.projectiles))
	}
	// 冷却未结束，再次开火应被静默忽略
	room.applyInput(p, &knight.InputCmd{Seq: 2, AimAngle: 0, Buttons: uint32(knight.InputButtons_BTN_ATTACK)}, 1.0/30.0)
	if len(room.projectiles) != 1 {
		t.Fatalf("weapon cooldown violated: projectiles=%d", len(room.projectiles))
	}

	// 推进 tick 让子弹命中并验证 DamageEvent
	for i := 0; i < 20; i++ {
		room.Tick(time.Second / 30)
	}
	if e.HP >= e.MaxHP || p.DamageDealt <= 0 {
		t.Fatalf("expected enemy damaged: hp=%d/%d dealt=%d", e.HP, e.MaxHP, p.DamageDealt)
	}

	// 2. 霰弹枪 3 发散射
	p.WeaponID = WeaponConfigShotgun
	p.WeaponCD = 0
	room.projectiles = nil
	room.tryFireWeapon(p)
	if len(room.projectiles) != 3 {
		t.Fatalf("shotgun should spawn 3 pellets, got %d", len(room.projectiles))
	}

	// 3. 连发脉冲枪（首帧 1 发 + 后续 2 发）
	p.WeaponID = WeaponConfigBurst
	p.WeaponCD = 0
	room.projectiles = nil
	room.tryFireWeapon(p)
	if len(room.projectiles) != 1 || p.BurstRemaining != 2 {
		t.Fatalf("burst initial state wrong: proj=%d rem=%d", len(room.projectiles), p.BurstRemaining)
	}
	for i := 0; i < 10; i++ {
		room.tickPlayerBurst(p, 1.0/30.0)
	}
	if len(room.projectiles) != 3 || p.BurstRemaining != 0 {
		t.Fatalf("burst should fire 3 total rounds, got proj=%d rem=%d", len(room.projectiles), p.BurstRemaining)
	}

	// 4. 榴弹 AOE 击杀两只相邻小怪
	p.WeaponID = WeaponConfigGrenade
	p.WeaponCD = 0
	room.projectiles = nil
	room.enemies = nil
	e1 := room.spawnEnemy(EnemySpawnSpec{Pos: Vec2{X: 13, Y: 10}, ConfigID: EnemyConfigMelee})
	e2 := room.spawnEnemy(EnemySpawnSpec{Pos: Vec2{X: 13.5, Y: 11.2}, ConfigID: EnemyConfigMelee})
	e1.HP = 20
	e2.HP = 20
	room.tryFireWeapon(p)
	for i := 0; i < 25; i++ {
		room.Tick(time.Second / 30)
	}
	if e1.Alive || e2.Alive || p.Kills != 2 {
		t.Fatalf("grenade AOE should kill both enemies: e1=%v e2=%v kills=%d", e1.Alive, e2.Alive, p.Kills)
	}
}

func TestActiveSkillsAndCooldown(t *testing.T) {
	sender := newFakeSender()
	room, _ := newRoom(roomConfig{
		roomID:             2,
		seed:               7,
		mapIDs:             []uint32{6},
		playerUIDs:         []uint64{101, 102},
		loadedTimeoutTicks: 90,
		snapshotEveryTicks: 2,
		moveSpeed:          DefaultMoveSpeed,
		playerRadius:       DefaultPlayerRadius,
		playerHP:           DefaultPlayerHP,
		uidSender:          sender,
	})
	room.handleRoomLoaded(101, 6)
	room.handleRoomLoaded(102, 6)
	sender.drain(101)

	p1 := room.byUID[101]
	p2 := room.byUID[102]
	p1.Pos = Vec2{X: 6, Y: 10}
	p2.Pos = Vec2{X: 8, Y: 10}
	e := room.spawnEnemy(EnemySpawnSpec{Pos: Vec2{X: 9.5, Y: 10}, ConfigID: EnemyConfigMelee})

	// 技能 1：冲刺闪避斩（位移 + 无敌 + AOE + SkillEvent + 冷却）
	room.applyInput(p1, &knight.InputCmd{
		Seq:      1,
		MoveX:    1,
		AimAngle: 0,
		Buttons:  uint32(knight.InputButtons_BTN_SKILL),
	}, 1.0/30.0)
	if p1.Pos.X <= 8.5 {
		t.Fatalf("expected dash forward, pos=%+v", p1.Pos)
	}
	if p1.InvulnTimer <= 0 || p1.SkillCD <= 0 {
		t.Fatalf("expected invuln and cooldown active: invuln=%v cd=%v", p1.InvulnTimer, p1.SkillCD)
	}
	if e.HP >= e.MaxHP {
		t.Fatalf("expected dash slash AOE damage to enemy, hp=%d/%d", e.HP, e.MaxHP)
	}
	// 无敌期间免疫伤害
	hpBefore := p1.HP
	room.damagePlayer(e.EntityID, p1, 30, p1.Pos)
	if p1.HP != hpBefore {
		t.Fatalf("invuln player took damage: %d -> %d", hpBefore, p1.HP)
	}

	gotSkillEvent := false
	for _, m := range sender.drain(101) {
		if se := m.GetSkillEvent(); se != nil && se.CasterId == p1.EntityID && se.SkillId == SkillConfigDashSlash {
			gotSkillEvent = true
		}
	}
	if !gotSkillEvent {
		t.Fatal("expected SkillEvent broadcast")
	}

	// 技能 2：护盾减伤 50%
	p2.SkillID = SkillConfigShield
	room.triggerPlayerSkill(p2, &knight.InputCmd{Seq: 1})
	room.damagePlayer(e.EntityID, p2, 20, p2.Pos)
	if p2.HP != DefaultPlayerHP-10 {
		t.Fatalf("shield should halve 20 dmg to 10, hp=%d", p2.HP)
	}

	// 技能 4：群体治疗
	p1.InvulnTimer = 0
	p1.HP = 50
	p2.HP = 60
	p2.SkillID = SkillConfigHealAura
	p2.SkillCD = 0
	room.triggerPlayerSkill(p2, &knight.InputCmd{Seq: 2})
	if p1.HP != 85 || p2.HP != 95 {
		t.Fatalf("heal aura failed: p1=%d p2=%d", p1.HP, p2.HP)
	}
}

func TestPickupsDoorUnlockAndRoomTransition(t *testing.T) {
	sender := newFakeSender()
	room, _ := newRoom(roomConfig{
		roomID:             3,
		seed:               99,
		mapIDs:             []uint32{1, 6, 9},
		playerUIDs:         []uint64{101},
		loadedTimeoutTicks: 90,
		snapshotEveryTicks: 2,
		moveSpeed:          DefaultMoveSpeed,
		playerRadius:       DefaultPlayerRadius,
		playerHP:           DefaultPlayerHP,
		uidSender:          sender,
	})
	room.handleRoomLoaded(101, 1)
	if room.doorOpen {
		t.Fatal("map 1 has enemies; door should start locked")
	}
	sender.drain(101)

	// 清空 Map 1 全部怪物 → 门解锁
	for _, e := range room.enemies {
		room.damageEnemy(room.byUID[101].EntityID, 101, e, e.HP, e.Pos)
	}
	room.Tick(time.Second / 30)
	if !room.doorOpen {
		t.Fatal("door should open after clearing all enemies")
	}
	gotOpen := false
	for _, m := range sender.drain(101) {
		if ds := m.GetDoorState(); ds != nil && ds.State == knight.DoorStateValue_DOOR_OPEN {
			gotOpen = true
		}
	}
	if !gotOpen {
		t.Fatal("expected DoorState{DOOR_OPEN}")
	}

	// 玩家进入出口区 → 触发 RoomTransition 到 Map 6（补给房）
	room.byUID[101].Pos = Vec2{X: 27.5, Y: 10.0}
	room.Tick(time.Second / 30)
	if room.phase != phaseLoading || room.mapDef.ID != 6 {
		t.Fatalf("expected transition to map 6 loading, phase=%v map=%d", room.phase, room.mapDef.ID)
	}
	gotTransition := false
	for _, m := range sender.drain(101) {
		if rt := m.GetRoomTransition(); rt != nil && rt.MapId == 6 && rt.RoomIndex == 1 && rt.TotalRooms == 3 {
			gotTransition = true
		}
	}
	if !gotTransition {
		t.Fatal("expected RoomTransition(map=6, idx=1, total=3)")
	}

	// 加载 Map 6 补给房：0 怪 → 立即开门且含 2 个道具
	room.handleRoomLoaded(101, 6)
	if !room.doorOpen || len(room.pickups) != 2 {
		t.Fatalf("supply room should open door immediately with 2 pickups: open=%v pickups=%d", room.doorOpen, len(room.pickups))
	}
	sender.drain(101)

	// 拾取回血药水
	p := room.byUID[101]
	p.HP = 70
	p.Pos = room.pickups[0].Pos
	room.applyInput(p, &knight.InputCmd{Seq: 10, Buttons: uint32(knight.InputButtons_BTN_INTERACT)}, 1.0/30.0)
	if p.HP != 100 {
		t.Fatalf("expected healed to 100, got %d", p.HP)
	}
	gotPicked := false
	for _, m := range sender.drain(101) {
		if ip := m.GetItemPicked(); ip != nil && ip.Uid == 101 && ip.HealedHp == 30 && ip.ItemConfigId == PickupConfigHealth {
			gotPicked = true
		}
	}
	if !gotPicked {
		t.Fatal("expected ItemPicked(healed=30, config=1)")
	}
}

func TestBossPhase2VictoryAndMatchPersistence(t *testing.T) {
	bus := event.NewBus()
	sender := newFakeSender()
	rec := &fakeMatchRecorder{}
	runner, err := tick.NewRunner(tick.Config{Rate: 30})
	if err != nil {
		t.Fatalf("new runner: %v", err)
	}
	var endedCh = make(chan event.GameEndedArgs, 1)
	event.Subscribe(bus, event.GameEnded, func(args event.GameEndedArgs) {
		endedCh <- args
	})

	mgr := NewManager(Config{TickRate: 30, SnapshotRate: 15, Recorder: rec}, runner, sender, nil, bus)

	const testSeed uint64 = 777888999
	event.Publish(bus, event.GameStarted, event.GameStartedArgs{
		RoomID:     99,
		MapIDs:     []uint32{BossMapID},
		Seed:       testSeed,
		PlayerUIDs: []uint64{101, 102},
	})
	mgr.Route(101, "a", "c1", &knight.Envelope{MsgId: knight.MsgId_MSG_ROOM_LOADED, Payload: &knight.Envelope_RoomLoaded{RoomLoaded: &knight.RoomLoaded{MapId: BossMapID}}})
	mgr.Route(102, "b", "c2", &knight.Envelope{MsgId: knight.MsgId_MSG_ROOM_LOADED, Payload: &knight.Envelope_RoomLoaded{RoomLoaded: &knight.RoomLoaded{MapId: BossMapID}}})
	runner.Step(1)

	room := mgr.roomForUID(101)
	if room == nil {
		t.Fatal("expected active boss room")
	}

	// 验证 Boss 存在、半血切二阶段、以及击杀触发 Victory + 持久化 + GameResult + GameEnded
	runner.Post(func() {
		if len(room.enemies) != 1 || !room.enemies[0].IsBoss {
			t.Errorf("expected 1 boss in map 9")
			return
		}
		boss := room.enemies[0]
		p1 := room.byUID[101]
		// 打掉超过一半血 → 触发二阶段
		room.damageEnemy(p1.EntityID, 101, boss, boss.MaxHP/2+10, boss.Pos)
		if !boss.Phase2 || boss.statusFlags()&StatusFlagBossPhase2 == 0 {
			t.Errorf("expected boss phase 2 active after dropping below 50%% HP")
			return
		}
		// 让 Boss 推进一次攻击（验证二阶段 AI 不崩溃且可发射弹幕）
		boss.AttackTimer = 0
		boss.PatternIndex = 2 // 8方向环形弹幕
		room.tickBossAI(boss, p1, 1.0/30.0)
		if len(room.projectiles) < 8 {
			t.Errorf("expected boss phase 2 ring burst >= 8 projectiles, got %d", len(room.projectiles))
		}
		// 击杀 Boss
		room.damageEnemy(p1.EntityID, 101, boss, boss.HP, boss.Pos)
	})
	runner.Step(2)

	select {
	case ended := <-endedCh:
		if ended.RoomID != 99 || ended.Reason != event.GameEndVictory {
			t.Fatalf("unexpected GameEnded: %+v", ended)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for GameEnded")
	}

	if mgr.RoomCount() != 0 {
		t.Fatalf("RoomCount after victory = %d, want 0", mgr.RoomCount())
	}
	if rec.count() != 1 {
		t.Fatalf("expected 1 persisted match, got %d", rec.count())
	}
	saved := rec.first()
	if saved.outcome != knight.GameOutcome_OUTCOME_VICTORY || saved.seed != testSeed || len(saved.players) != 2 {
		t.Fatalf("unexpected persisted match: %+v", saved)
	}

	gotResult := false
	for _, m := range sender.drain(101) {
		if gr := m.GetGameResult(); gr != nil {
			gotResult = true
			if gr.MatchId != saved.matchID || gr.Outcome != knight.GameOutcome_OUTCOME_VICTORY || len(gr.Results) != 2 {
				t.Fatalf("unexpected GameResult: %+v", gr)
			}
		}
	}
	if !gotResult {
		t.Fatal("expected GameResult sent to player 101")
	}
}

func TestAllPlayersDeadTriggersDefeat(t *testing.T) {
	bus := event.NewBus()
	sender := newFakeSender()
	rec := &fakeMatchRecorder{}
	runner, err := tick.NewRunner(tick.Config{Rate: 30})
	if err != nil {
		t.Fatalf("new runner: %v", err)
	}
	endedCh := make(chan event.GameEndedArgs, 1)
	event.Subscribe(bus, event.GameEnded, func(args event.GameEndedArgs) {
		endedCh <- args
	})

	mgr := NewManager(Config{TickRate: 30, SnapshotRate: 15, Recorder: rec}, runner, sender, nil, bus)
	event.Publish(bus, event.GameStarted, event.GameStartedArgs{
		RoomID:     100,
		MapIDs:     []uint32{1},
		Seed:       555,
		PlayerUIDs: []uint64{101},
	})
	mgr.Route(101, "a", "c1", &knight.Envelope{MsgId: knight.MsgId_MSG_ROOM_LOADED, Payload: &knight.Envelope_RoomLoaded{RoomLoaded: &knight.RoomLoaded{MapId: 1}}})
	runner.Step(1)

	room := mgr.roomForUID(101)
	runner.Post(func() {
		p := room.byUID[101]
		room.damagePlayer(999, p, p.HP, p.Pos)
	})
	runner.Step(2)

	select {
	case ended := <-endedCh:
		if ended.Reason != event.GameEndDefeat {
			t.Fatalf("expected defeat, got %+v", ended)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for defeat GameEnded")
	}

	if rec.count() != 1 || rec.first().outcome != knight.GameOutcome_OUTCOME_DEFEAT {
		t.Fatalf("expected defeat match persisted")
	}
}

type failingMatchRecorder struct{}

func (failingMatchRecorder) RecordMatch(context.Context, knight.GameOutcome, uint32, uint64, []MatchPlayerStat) (uint64, error) {
	return 0, errors.New("mysql unavailable")
}

// TestDeadPlayerExcludedFromSnapshotAndAliveTeammateAdvances 验证 P0-1：
// 多人局 1 人阵亡后 Snapshot 不再包含已 despawn 的阵亡玩家，且存活队友可正常清怪切房。
func TestDeadPlayerExcludedFromSnapshotAndAliveTeammateAdvances(t *testing.T) {
	sender := newFakeSender()
	room, _ := newRoom(roomConfig{
		roomID:             201,
		seed:               1001,
		mapIDs:             []uint32{1, 6},
		playerUIDs:         []uint64{101, 102},
		loadedTimeoutTicks: 90,
		snapshotEveryTicks: 1,
		moveSpeed:          DefaultMoveSpeed,
		playerRadius:       DefaultPlayerRadius,
		playerHP:           DefaultPlayerHP,
		uidSender:          sender,
	})
	room.handleRoomLoaded(101, 1)
	room.handleRoomLoaded(102, 1)
	sender.drain(101)
	sender.drain(102)

	// 玩家 102 阵亡
	p2 := room.byUID[102]
	room.damagePlayer(999, p2, p2.HP, p2.Pos)
	room.Tick(time.Second / 30)

	// 双方均收到快照，但快照实体列表中不应再出现阵亡玩家 102（防止客户端兜底复活）
	for _, uid := range []uint64{101, 102} {
		snaps := sender.snapshots(uid)
		if len(snaps) == 0 {
			t.Fatalf("uid=%d expected snapshot after teammate death", uid)
		}
		last := snaps[len(snaps)-1]
		if findEntityByUID(last, 101) == nil {
			t.Fatalf("uid=%d snapshot missing alive player 101", uid)
		}
		if findEntityByUID(last, 102) != nil {
			t.Fatalf("uid=%d snapshot must not contain dead player 102", uid)
		}
	}

	// 存活队友 101 清光怪物并走进出口区 → 正常切换至 Map 6（不被已阵亡的 102 阻塞）
	for _, e := range room.enemies {
		room.damageEnemy(room.byUID[101].EntityID, 101, e, e.HP, e.Pos)
	}
	room.Tick(time.Second / 30)
	if !room.doorOpen {
		t.Fatal("door should unlock after alive teammate clears room")
	}
	room.byUID[101].Pos = Vec2{X: 27.5, Y: 10.0}
	room.Tick(time.Second / 30)
	if room.phase != phaseLoading || room.mapDef.ID != 6 {
		t.Fatalf("expected transition to map 6, phase=%v map=%d", room.phase, room.mapDef.ID)
	}
}

// TestMatchRecorderErrorFallsBackToSequenceAndBroadcastsResult 验证 P1-5：
// RecordMatch 返回错误时降级使用 fallbackMatchSeq，GameResult 仍正常下发且 WaitPersist 正常返回。
func TestMatchRecorderErrorFallsBackToSequenceAndBroadcastsResult(t *testing.T) {
	bus := event.NewBus()
	sender := newFakeSender()
	runner, err := tick.NewRunner(tick.Config{Rate: 30})
	if err != nil {
		t.Fatalf("new runner: %v", err)
	}
	endedCh := make(chan event.GameEndedArgs, 1)
	event.Subscribe(bus, event.GameEnded, func(args event.GameEndedArgs) {
		endedCh <- args
	})

	mgr := NewManager(Config{TickRate: 30, SnapshotRate: 15, Recorder: failingMatchRecorder{}}, runner, sender, nil, bus)
	event.Publish(bus, event.GameStarted, event.GameStartedArgs{
		RoomID:     202,
		MapIDs:     []uint32{BossMapID},
		Seed:       4242,
		PlayerUIDs: []uint64{101},
	})
	mgr.Route(101, "a", "c1", &knight.Envelope{MsgId: knight.MsgId_MSG_ROOM_LOADED, Payload: &knight.Envelope_RoomLoaded{RoomLoaded: &knight.RoomLoaded{MapId: BossMapID}}})
	runner.Step(1)

	room := mgr.roomForUID(101)
	runner.Post(func() {
		boss := room.enemies[0]
		room.damageEnemy(room.byUID[101].EntityID, 101, boss, boss.HP, boss.Pos)
	})
	runner.Step(2)

	select {
	case ended := <-endedCh:
		if ended.Reason != event.GameEndVictory {
			t.Fatalf("unexpected GameEnded: %+v", ended)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for GameEnded")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := mgr.WaitPersist(ctx); err != nil {
		t.Fatalf("WaitPersist failed: %v", err)
	}

	var got *knight.GameResult
	for _, m := range sender.drain(101) {
		if gr := m.GetGameResult(); gr != nil {
			got = gr
		}
	}
	if got == nil || got.MatchId == 0 || got.Outcome != knight.GameOutcome_OUTCOME_VICTORY {
		t.Fatalf("expected fallback GameResult with non-zero match_id, got %+v", got)
	}
}

// TestDisconnectedPlayerIncludedAsDeadInResultAndNotSentResult 验证 P1-5：
// 2 人局 1 人中途断线，剩余玩家通关后 leftPlayers 以 alive=false 汇总写入结算，且不向断线玩家发 GameResult。
func TestDisconnectedPlayerIncludedAsDeadInResultAndNotSentResult(t *testing.T) {
	bus := event.NewBus()
	sender := newFakeSender()
	rec := &fakeMatchRecorder{}
	runner, err := tick.NewRunner(tick.Config{Rate: 30})
	if err != nil {
		t.Fatalf("new runner: %v", err)
	}
	endedCh := make(chan event.GameEndedArgs, 1)
	event.Subscribe(bus, event.GameEnded, func(args event.GameEndedArgs) {
		endedCh <- args
	})

	mgr := NewManager(Config{TickRate: 30, SnapshotRate: 15, Recorder: rec}, runner, sender, nil, bus)
	event.Publish(bus, event.GameStarted, event.GameStartedArgs{
		RoomID:     203,
		MapIDs:     []uint32{BossMapID},
		Seed:       8888,
		PlayerUIDs: []uint64{101, 102},
	})
	mgr.Route(101, "a", "c1", &knight.Envelope{MsgId: knight.MsgId_MSG_ROOM_LOADED, Payload: &knight.Envelope_RoomLoaded{RoomLoaded: &knight.RoomLoaded{MapId: BossMapID}}})
	mgr.Route(102, "b", "c2", &knight.Envelope{MsgId: knight.MsgId_MSG_ROOM_LOADED, Payload: &knight.Envelope_RoomLoaded{RoomLoaded: &knight.RoomLoaded{MapId: BossMapID}}})
	runner.Step(1)

	// 102 先造成部分伤害后中途断线
	room := mgr.roomForUID(101)
	runner.Post(func() {
		boss := room.enemies[0]
		room.damageEnemy(room.byUID[102].EntityID, 102, boss, 40, boss.Pos)
	})
	runner.Step(1)

	event.Publish(bus, event.MemberLeft, event.MemberLeftArgs{RoomID: 203, UID: 102, Reason: event.LeaveDisconnect})
	runner.Step(1)
	sender.drain(101)
	sender.drain(102)

	// 101 独自击杀 Boss 通关
	runner.Post(func() {
		boss := room.enemies[0]
		room.damageEnemy(room.byUID[101].EntityID, 101, boss, boss.HP, boss.Pos)
	})
	runner.Step(2)

	select {
	case <-endedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for GameEnded")
	}

	if rec.count() != 1 {
		t.Fatalf("expected 1 persisted match, got %d", rec.count())
	}
	saved := rec.first()
	if len(saved.players) != 2 {
		t.Fatalf("expected 2 players in persisted match (including disconnected), got %+v", saved.players)
	}
	var found101, found102 *MatchPlayerStat
	for i := range saved.players {
		switch saved.players[i].UID {
		case 101:
			found101 = &saved.players[i]
		case 102:
			found102 = &saved.players[i]
		}
	}
	if found101 == nil || !found101.Alive || found102 == nil || found102.Alive || found102.DamageDealt != 40 {
		t.Fatalf("unexpected player stats: p101=%+v p102=%+v", found101, found102)
	}

	// 101 收到 GameResult，已断线的 102 不应收到 GameResult
	got101 := false
	for _, m := range sender.drain(101) {
		if m.GetGameResult() != nil {
			got101 = true
		}
	}
	got102 := false
	for _, m := range sender.drain(102) {
		if m.GetGameResult() != nil {
			got102 = true
		}
	}
	if !got101 || got102 {
		t.Fatalf("GameResult delivery wrong: got101=%v got102=%v", got101, got102)
	}
}

// TestDashSlashDoesNotClipThroughThinWall 验证 P1-5：
// 玩家紧贴 1-tile 内部墙体（Map 1 竖墙 x=[10,11], y=[9,15]）朝墙体释放冲刺斩，不得穿墙。
func TestDashSlashDoesNotClipThroughThinWall(t *testing.T) {
	sender := newFakeSender()
	room, _ := newRoom(roomConfig{
		roomID:             204,
		seed:               321,
		mapIDs:             []uint32{1},
		playerUIDs:         []uint64{101},
		loadedTimeoutTicks: 90,
		snapshotEveryTicks: 2,
		moveSpeed:          DefaultMoveSpeed,
		playerRadius:       DefaultPlayerRadius,
		playerHP:           DefaultPlayerHP,
		uidSender:          sender,
	})
	room.handleRoomLoaded(101, 1)

	p := room.byUID[101]
	// 紧贴左中竖墙（MinX=10, MaxX=11, MinY=9, MaxY=15）左侧，朝 +X 冲刺
	p.Pos = Vec2{X: 9.8, Y: 12.0}
	p.Facing = 0
	room.applyInput(p, &knight.InputCmd{
		Seq:      1,
		MoveX:    1,
		MoveY:    0,
		AimAngle: 0,
		Buttons:  uint32(knight.InputButtons_BTN_SKILL),
	}, 1.0/30.0)

	if p.Pos.X > 10.0-DefaultPlayerRadius+1e-3 {
		t.Fatalf("dash clipped through 1-tile wall: pos=%+v", p.Pos)
	}
	if circleHitsAnyWall(p.Pos, DefaultPlayerRadius, room.mapDef) {
		t.Fatalf("player stuck inside wall after dash: pos=%+v", p.Pos)
	}
}
