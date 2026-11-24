package world

import (
	"math"
	"sync"
	"testing"

	"knight-server/internal/event"
	"knight-server/internal/kcp2k"
	"knight-server/internal/protocol"
	"knight-server/internal/tick"

	knight "knight-server/protogen"
)

type fakeSender struct {
	mu    sync.Mutex
	inbox map[uint64][]*knight.Envelope
}

func newFakeSender() *fakeSender {
	return &fakeSender{inbox: make(map[uint64][]*knight.Envelope)}
}

func (s *fakeSender) SendToUID(uid uint64, data []byte, _ kcp2k.Channel) bool {
	env, err := protocol.Decode(data)
	if err != nil {
		panic(err)
	}
	s.mu.Lock()
	s.inbox[uid] = append(s.inbox[uid], env)
	s.mu.Unlock()
	return true
}

func (s *fakeSender) drain(uid uint64) []*knight.Envelope {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.inbox[uid]
	s.inbox[uid] = nil
	return out
}

func (s *fakeSender) snapshots(uid uint64) []*knight.Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*knight.Snapshot
	for _, env := range s.inbox[uid] {
		if snap := env.GetSnapshot(); snap != nil {
			out = append(out, snap)
		}
	}
	return out
}

type rejectCall struct {
	connID  string
	code    knight.ErrorCode
	message string
}

type fakeRejecter struct {
	mu    sync.Mutex
	calls []rejectCall
}

func (r *fakeRejecter) Reject(connID string, code knight.ErrorCode, message string) {
	r.mu.Lock()
	r.calls = append(r.calls, rejectCall{connID: connID, code: code, message: message})
	r.mu.Unlock()
}

func newWorldFixture(t *testing.T) (*Manager, *tick.Runner, *event.Bus, *fakeSender, *fakeRejecter) {
	t.Helper()
	runner, err := tick.NewRunner(tick.Config{Rate: 30})
	if err != nil {
		t.Fatalf("new runner: %v", err)
	}
	bus := event.NewBus()
	sender := newFakeSender()
	rej := &fakeRejecter{}
	mgr := NewManager(Config{TickRate: 30}, runner, sender, rej, bus)
	return mgr, runner, bus, sender, rej
}

func findEntityByUID(snap *knight.Snapshot, uid uint64) *knight.EntityState {
	for _, e := range snap.Entities {
		if e.PlayerUid == uid {
			return e
		}
	}
	return nil
}

func TestRoomTransitionAndAllRoomLoaded(t *testing.T) {
	mgr, runner, bus, sender, _ := newWorldFixture(t)

	event.Publish(bus, event.GameStarted, event.GameStartedArgs{
		RoomID:     10,
		MapIDs:     []uint32{1, 2, 3},
		PlayerUIDs: []uint64{101, 102},
	})
	if mgr.RoomCount() != 1 {
		t.Fatalf("RoomCount = %d, want 1", mgr.RoomCount())
	}

	// 双方均应立即收到 RoomTransition(map_id=1) 且包含两人出生点
	for _, uid := range []uint64{101, 102} {
		msgs := sender.drain(uid)
		if len(msgs) != 1 || msgs[0].GetRoomTransition() == nil {
			t.Fatalf("uid=%d expected 1 RoomTransition, got %d msgs", uid, len(msgs))
		}
		rt := msgs[0].GetRoomTransition()
		if rt.MapId != 1 || len(rt.Spawns) != 2 {
			t.Fatalf("unexpected RoomTransition: %+v", rt)
		}
		if rt.Spawns[0].Uid != 101 || rt.Spawns[0].PosX != 4 || rt.Spawns[0].PosY != 12 {
			t.Fatalf("spawn[0] = %+v", rt.Spawns[0])
		}
		if rt.Spawns[1].Uid != 102 || rt.Spawns[1].PosX != 4 || rt.Spawns[1].PosY != 8 {
			t.Fatalf("spawn[1] = %+v", rt.Spawns[1])
		}
	}

	// 未收齐 RoomLoaded 前步进不产生快照
	runner.Step(10)
	if len(sender.snapshots(101)) != 0 || len(sender.snapshots(102)) != 0 {
		t.Fatal("snapshots emitted while still loading")
	}

	// 错误 map_id 的 RoomLoaded 应被忽略
	mgr.Route(101, "a", "c1", &knight.Envelope{
		MsgId:   knight.MsgId_MSG_ROOM_LOADED,
		Payload: &knight.Envelope_RoomLoaded{RoomLoaded: &knight.RoomLoaded{MapId: 99}},
	})
	// 正确确认（仅 101）+ 重复确认
	mgr.Route(101, "a", "c1", &knight.Envelope{
		MsgId:   knight.MsgId_MSG_ROOM_LOADED,
		Payload: &knight.Envelope_RoomLoaded{RoomLoaded: &knight.RoomLoaded{MapId: 1}},
	})
	mgr.Route(101, "a", "c1", &knight.Envelope{
		MsgId:   knight.MsgId_MSG_ROOM_LOADED,
		Payload: &knight.Envelope_RoomLoaded{RoomLoaded: &knight.RoomLoaded{MapId: 1}},
	})
	runner.Step(1)
	if len(sender.snapshots(101)) != 0 {
		t.Fatal("room activated before all players loaded")
	}

	// 102 确认后立即激活并下发首帧全量快照
	mgr.Route(102, "b", "c2", &knight.Envelope{
		MsgId:   knight.MsgId_MSG_ROOM_LOADED,
		Payload: &knight.Envelope_RoomLoaded{RoomLoaded: &knight.RoomLoaded{MapId: 1}},
	})
	runner.Step(1)

	for _, uid := range []uint64{101, 102} {
		snaps := sender.snapshots(uid)
		if len(snaps) != 1 {
			t.Fatalf("uid=%d snapshots = %d, want 1", uid, len(snaps))
		}
		if len(snaps[0].Entities) != 2 {
			t.Fatalf("entities = %d, want 2", len(snaps[0].Entities))
		}
		e1 := findEntityByUID(snaps[0], 101)
		if e1 == nil || e1.Type != knight.EntityType_ENTITY_PLAYER || e1.ConfigId != 1 || e1.Hp != DefaultPlayerHP {
			t.Fatalf("unexpected entity 101: %+v", e1)
		}
	}
}

func TestRoomLoadedTimeoutForcesActivation(t *testing.T) {
	mgr, runner, bus, sender, _ := newWorldFixture(t)

	event.Publish(bus, event.GameStarted, event.GameStartedArgs{
		RoomID:     11,
		MapIDs:     []uint32{1, 2, 3},
		PlayerUIDs: []uint64{101, 102},
	})
	sender.drain(101)
	sender.drain(102)

	// 仅 101 确认，102 不确认
	mgr.Route(101, "a", "c1", &knight.Envelope{
		MsgId:   knight.MsgId_MSG_ROOM_LOADED,
		Payload: &knight.Envelope_RoomLoaded{RoomLoaded: &knight.RoomLoaded{MapId: 1}},
	})

	// 30Hz 下 89 tick < 3s（90 tick = 3s）
	runner.Step(89)
	if len(sender.snapshots(101)) != 0 {
		t.Fatalf("activated too early at 89 ticks")
	}

	// 第 90 tick 触发 3s 超时强制激活
	runner.Step(1)
	if len(sender.snapshots(101)) != 1 || len(sender.snapshots(102)) != 1 {
		t.Fatalf("expected timeout activation snapshots, got p1=%d p2=%d",
			len(sender.snapshots(101)), len(sender.snapshots(102)))
	}
}

func TestInputBatchMovementAndUnicastSnapshot15Hz(t *testing.T) {
	mgr, runner, bus, sender, _ := newWorldFixture(t)

	event.Publish(bus, event.GameStarted, event.GameStartedArgs{
		RoomID:     12,
		MapIDs:     []uint32{1, 2, 3},
		PlayerUIDs: []uint64{101, 102},
	})
	mgr.Route(101, "a", "c1", &knight.Envelope{
		MsgId:   knight.MsgId_MSG_ROOM_LOADED,
		Payload: &knight.Envelope_RoomLoaded{RoomLoaded: &knight.RoomLoaded{MapId: 1}},
	})
	mgr.Route(102, "b", "c2", &knight.Envelope{
		MsgId:   knight.MsgId_MSG_ROOM_LOADED,
		Payload: &knight.Envelope_RoomLoaded{RoomLoaded: &knight.RoomLoaded{MapId: 1}},
	})
	runner.Step(1) // 激活并推首帧
	sender.drain(101)
	sender.drain(102)

	// 玩家 101 发 2 条向右输入，玩家 102 发 1 条向上输入
	mgr.Route(101, "a", "c1", &knight.Envelope{
		MsgId: knight.MsgId_MSG_INPUT_BATCH,
		Payload: &knight.Envelope_InputBatch{InputBatch: &knight.InputBatch{
			FirstSeq: 1,
			Inputs: []*knight.InputCmd{
				{Seq: 1, MoveX: 1, MoveY: 0, AimAngle: 45},
				{Seq: 2, MoveX: 1, MoveY: 0, AimAngle: 45},
			},
		}},
	})
	mgr.Route(102, "b", "c2", &knight.Envelope{
		MsgId: knight.MsgId_MSG_INPUT_BATCH,
		Payload: &knight.Envelope_InputBatch{InputBatch: &knight.InputBatch{
			FirstSeq: 1,
			Inputs: []*knight.InputCmd{
				{Seq: 1, MoveX: 0, MoveY: 1, AimAngle: 90},
			},
		}},
	})

	// 激活后第 1 个活跃 tick（激活已算 tick 1？激活发生在 ops 中，之后同一次 Step 跑了 tick 1！）
	// 15Hz = 每 2 个 tick 广播一次，因此步进 2 tick 必然收到恰好 1 帧快照
	runner.Step(2)
	s1 := sender.snapshots(101)
	s2 := sender.snapshots(102)
	if len(s1) != 1 || len(s2) != 1 {
		t.Fatalf("expected 1 snapshot after 2 ticks (15Hz), got p1=%d p2=%d", len(s1), len(s2))
	}

	// 单播各自携带不同的 last_input_seq
	if s1[0].LastInputSeq != 2 {
		t.Fatalf("p1 last_input_seq = %d, want 2", s1[0].LastInputSeq)
	}
	if s2[0].LastInputSeq != 1 {
		t.Fatalf("p2 last_input_seq = %d, want 1", s2[0].LastInputSeq)
	}

	// 位置推进校验：101 从 (4,12) 走 2 步 × 0.1 = 4.2；102 从 (4,8) 走 1 步 × 0.1 = 8.1
	e1 := findEntityByUID(s1[0], 101)
	e2 := findEntityByUID(s1[0], 102)
	if math.Abs(float64(e1.PosX-4.2)) > 1e-4 || math.Abs(float64(e1.PosY-12.0)) > 1e-4 {
		t.Fatalf("p1 pos = (%f, %f), want (4.2, 12.0)", e1.PosX, e1.PosY)
	}
	if e1.Facing != 45 {
		t.Fatalf("p1 facing = %f, want 45", e1.Facing)
	}
	if math.Abs(float64(e2.PosX-4.0)) > 1e-4 || math.Abs(float64(e2.PosY-8.1)) > 1e-4 {
		t.Fatalf("p2 pos = (%f, %f), want (4.0, 8.1)", e2.PosX, e2.PosY)
	}

	// 重发包含已确认 seq=1,2 的批次（全量未确认重发语义）：只应执行新 seq=3，不能重复位移
	sender.drain(101)
	mgr.Route(101, "a", "c1", &knight.Envelope{
		MsgId: knight.MsgId_MSG_INPUT_BATCH,
		Payload: &knight.Envelope_InputBatch{InputBatch: &knight.InputBatch{
			FirstSeq: 1,
			Inputs: []*knight.InputCmd{
				{Seq: 1, MoveX: 1, MoveY: 0, AimAngle: 45},
				{Seq: 2, MoveX: 1, MoveY: 0, AimAngle: 45},
				{Seq: 3, MoveX: 1, MoveY: 0, AimAngle: 45},
			},
		}},
	})
	runner.Step(2)
	s1After := sender.snapshots(101)
	if len(s1After) != 1 || s1After[0].LastInputSeq != 3 {
		t.Fatalf("expected last_input_seq=3, got %+v", s1After)
	}
	e1After := findEntityByUID(s1After[0], 101)
	if math.Abs(float64(e1After.PosX-4.3)) > 1e-4 {
		t.Fatalf("duplicate inputs applied: posX = %f, want 4.3", e1After.PosX)
	}
}

func TestInputValidationAndClamping(t *testing.T) {
	mgr, runner, bus, sender, rej := newWorldFixture(t)

	// 非对局状态发合法输入：静默丢弃，不拒绝
	mgr.Route(999, "lobby-user", "c999", &knight.Envelope{
		MsgId: knight.MsgId_MSG_INPUT_BATCH,
		Payload: &knight.Envelope_InputBatch{InputBatch: &knight.InputBatch{
			FirstSeq: 1,
			Inputs:   []*knight.InputCmd{{Seq: 1, MoveX: 1}},
		}},
	})
	if len(rej.calls) != 0 {
		t.Fatalf("unexpected reject for lobby input: %+v", rej.calls)
	}

	// 超过 120 条 → ERR_INVALID_REQUEST + Reject
	tooMany := make([]*knight.InputCmd, MaxInputBatchSize+1)
	for i := range tooMany {
		tooMany[i] = &knight.InputCmd{Seq: uint32(i + 1)}
	}
	mgr.Route(101, "a", "c1", &knight.Envelope{
		MsgId:   knight.MsgId_MSG_INPUT_BATCH,
		Payload: &knight.Envelope_InputBatch{InputBatch: &knight.InputBatch{FirstSeq: 1, Inputs: tooMany}},
	})

	// first_seq 不匹配 → Reject
	mgr.Route(101, "a", "c1", &knight.Envelope{
		MsgId: knight.MsgId_MSG_INPUT_BATCH,
		Payload: &knight.Envelope_InputBatch{InputBatch: &knight.InputBatch{
			FirstSeq: 99,
			Inputs:   []*knight.InputCmd{{Seq: 1}},
		}},
	})

	// 非严格递增 seq → Reject
	mgr.Route(101, "a", "c1", &knight.Envelope{
		MsgId: knight.MsgId_MSG_INPUT_BATCH,
		Payload: &knight.Envelope_InputBatch{InputBatch: &knight.InputBatch{
			FirstSeq: 2,
			Inputs:   []*knight.InputCmd{{Seq: 2}, {Seq: 2}},
		}},
	})

	if len(rej.calls) != 3 {
		t.Fatalf("reject calls = %d, want 3: %+v", len(rej.calls), rej.calls)
	}
	for i, c := range rej.calls {
		if c.code != knight.ErrorCode_ERR_INVALID_REQUEST {
			t.Fatalf("reject[%d].code = %v, want ERR_INVALID_REQUEST", i, c.code)
		}
	}

	// 移动向量超模长钳制与 NaN 保护
	event.Publish(bus, event.GameStarted, event.GameStartedArgs{
		RoomID:     13,
		MapIDs:     []uint32{1},
		PlayerUIDs: []uint64{101},
	})
	mgr.Route(101, "a", "c1", &knight.Envelope{
		MsgId:   knight.MsgId_MSG_ROOM_LOADED,
		Payload: &knight.Envelope_RoomLoaded{RoomLoaded: &knight.RoomLoaded{MapId: 1}},
	})
	runner.Step(1)
	sender.drain(101)

	mgr.Route(101, "a", "c1", &knight.Envelope{
		MsgId: knight.MsgId_MSG_INPUT_BATCH,
		Payload: &knight.Envelope_InputBatch{InputBatch: &knight.InputBatch{
			FirstSeq: 1,
			Inputs: []*knight.InputCmd{
				{Seq: 1, MoveX: 10, MoveY: 10},
				{Seq: 2, MoveX: float32(math.NaN()), MoveY: float32(math.Inf(1))},
			},
		}},
	})
	runner.Step(2)
	snaps := sender.snapshots(101)
	e := findEntityByUID(snaps[0], 101)
	// 钳制到单位向量 (1/sqrt(2), 1/sqrt(2))，步长 0.1
	wantDelta := float32(0.1 / math.Sqrt2)
	if math.Abs(float64(e.PosX-(4.0+wantDelta))) > 1e-4 || math.Abs(float64(e.PosY-(12.0+wantDelta))) > 1e-4 {
		t.Fatalf("clamped pos = (%f, %f), want (%f, %f)", e.PosX, e.PosY, 4.0+wantDelta, 12.0+wantDelta)
	}
}

func TestWallAABBCollisionAndSliding(t *testing.T) {
	m, ok := GetMap(Map1ID)
	if !ok {
		t.Fatal("map 1 missing")
	}
	rad := DefaultPlayerRadius

	// 1. 冲向左侧外墙 [0,0,1,20]：应停在 x = 1 + rad
	pos := Vec2{X: 1.5, Y: 10.0}
	for i := 0; i < 20; i++ {
		pos = resolveCollisions(Vec2{X: pos.X - 0.1, Y: pos.Y}, rad, m)
	}
	if math.Abs(float64(pos.X-(1.0+rad))) > 1e-4 {
		t.Fatalf("left perimeter clamp failed: x=%f, want %f", pos.X, 1.0+rad)
	}

	// 2. 冲向内部障碍墙 Wall 1 [10, 9, 11, 15]：应停在 x = 10 - rad
	pos = Vec2{X: 9.5, Y: 12.0}
	for i := 0; i < 20; i++ {
		pos = resolveCollisions(Vec2{X: pos.X + 0.1, Y: pos.Y}, rad, m)
	}
	if math.Abs(float64(pos.X-(10.0-rad))) > 1e-4 {
		t.Fatalf("interior wall collision failed: x=%f, want %f", pos.X, 10.0-rad)
	}

	// 3. 斜向撞墙（贴墙滑动）：X 阻挡在 10 - rad，Y 正常向上推进
	startY := pos.Y
	for i := 0; i < 10; i++ {
		pos = resolveCollisions(Vec2{X: pos.X + 0.07, Y: pos.Y + 0.07}, rad, m)
	}
	if math.Abs(float64(pos.X-(10.0-rad))) > 1e-4 {
		t.Fatalf("wall slide X drifted: x=%f, want %f", pos.X, 10.0-rad)
	}
	if pos.Y <= startY+0.6 {
		t.Fatalf("wall slide Y blocked: startY=%f, endY=%f", startY, pos.Y)
	}
}

func TestPlayerDisconnectDuringBattleAndRoomCleanup(t *testing.T) {
	mgr, runner, bus, sender, _ := newWorldFixture(t)

	event.Publish(bus, event.GameStarted, event.GameStartedArgs{
		RoomID:     14,
		MapIDs:     []uint32{1},
		PlayerUIDs: []uint64{101, 102},
	})
	mgr.Route(101, "a", "c1", &knight.Envelope{
		MsgId:   knight.MsgId_MSG_ROOM_LOADED,
		Payload: &knight.Envelope_RoomLoaded{RoomLoaded: &knight.RoomLoaded{MapId: 1}},
	})
	mgr.Route(102, "b", "c2", &knight.Envelope{
		MsgId:   knight.MsgId_MSG_ROOM_LOADED,
		Payload: &knight.Envelope_RoomLoaded{RoomLoaded: &knight.RoomLoaded{MapId: 1}},
	})
	runner.Step(1)
	sender.drain(101)
	sender.drain(102)

	// 102 断线离开
	event.Publish(bus, event.MemberLeft, event.MemberLeftArgs{
		RoomID: 14,
		UID:    102,
		Reason: event.LeaveDisconnect,
	})
	runner.Step(2)

	msgs := sender.drain(101)
	var gotDespawn, gotLeft bool
	var lastSnap *knight.Snapshot
	for _, m := range msgs {
		if d := m.GetEntityDespawn(); d != nil && len(d.EntityIds) == 1 && d.EntityIds[0] == 2 {
			gotDespawn = true
		}
		if pl := m.GetPlayerLeft(); pl != nil && pl.Uid == 102 && pl.Reason == knight.LeftReason_LEFT_DISCONNECT {
			gotLeft = true
		}
		if s := m.GetSnapshot(); s != nil {
			lastSnap = s
		}
	}
	if !gotDespawn || !gotLeft {
		t.Fatalf("missing disconnect events: despawn=%t left=%t", gotDespawn, gotLeft)
	}
	if lastSnap == nil || len(lastSnap.Entities) != 1 || lastSnap.Entities[0].PlayerUid != 101 {
		t.Fatalf("snapshot after disconnect should only contain 101: %+v", lastSnap)
	}

	// 最后一名玩家离开 → 房间清理
	event.Publish(bus, event.MemberLeft, event.MemberLeftArgs{
		RoomID: 14,
		UID:    101,
		Reason: event.LeaveDisconnect,
	})
	runner.Step(1)
	if mgr.RoomCount() != 0 {
		t.Fatalf("RoomCount after all left = %d, want 0", mgr.RoomCount())
	}
}

func TestPlayerDisconnectDuringLoadingActivatesRemaining(t *testing.T) {
	mgr, runner, bus, sender, _ := newWorldFixture(t)

	event.Publish(bus, event.GameStarted, event.GameStartedArgs{
		RoomID:     15,
		MapIDs:     []uint32{1},
		PlayerUIDs: []uint64{101, 102},
	})
	sender.drain(101)
	sender.drain(102)

	// 仅 101 确认加载，102 断线 → 剩余在场玩家全部已确认，应立即激活
	mgr.Route(101, "a", "c1", &knight.Envelope{
		MsgId:   knight.MsgId_MSG_ROOM_LOADED,
		Payload: &knight.Envelope_RoomLoaded{RoomLoaded: &knight.RoomLoaded{MapId: 1}},
	})
	event.Publish(bus, event.MemberLeft, event.MemberLeftArgs{
		RoomID: 15,
		UID:    102,
		Reason: event.LeaveDisconnect,
	})
	runner.Step(1)

	snaps := sender.snapshots(101)
	if len(snaps) != 1 || len(snaps[0].Entities) != 1 {
		t.Fatalf("expected immediate activation snapshot with 1 entity, got %+v", snaps)
	}
}

// TestInputRateBudgetAntiSpeedHackAndBurstRecovery 验证 P1-1：
// 1. 持续 120Hz 高频输入流不能突破 1x MoveSpeed（3.0 units/s）；
// 2. 短暂网络抖动（静默若干 tick 后突发 4 条输入）仍可单 tick 恢复。
func TestInputRateBudgetAntiSpeedHackAndBurstRecovery(t *testing.T) {
	mgr, runner, bus, sender, _ := newWorldFixture(t)

	event.Publish(bus, event.GameStarted, event.GameStartedArgs{
		RoomID:     20,
		MapIDs:     []uint32{1},
		PlayerUIDs: []uint64{101},
	})
	mgr.Route(101, "a", "c1", &knight.Envelope{
		MsgId:   knight.MsgId_MSG_ROOM_LOADED,
		Payload: &knight.Envelope_RoomLoaded{RoomLoaded: &knight.RoomLoaded{MapId: 1}},
	})
	runner.Step(1) // 激活并走 1 个空 tick（预算=1）
	sender.drain(101)

	// Part A: 模拟 120Hz 加速作弊（每 30Hz tick 注入 4 条向右输入），持续 30 tick（1 秒）。
	// 先把初始积攒的 1 点预算消耗掉，再观测后续 30 tick 的稳态位移。
	var seq uint32
	sendBurst := func(n int) {
		cmds := make([]*knight.InputCmd, n)
		for i := 0; i < n; i++ {
			seq++
			cmds[i] = &knight.InputCmd{Seq: seq, MoveX: 1, MoveY: 0}
		}
		mgr.Route(101, "a", "c1", &knight.Envelope{
			MsgId: knight.MsgId_MSG_INPUT_BATCH,
			Payload: &knight.Envelope_InputBatch{InputBatch: &knight.InputBatch{
				FirstSeq: cmds[0].Seq,
				Inputs:   cmds,
			}},
		})
	}

	// 预热 2 tick 消耗初始预算并获取起始位置（每 2 tick 出一帧快照）
	sendBurst(4)
	runner.Step(2)
	warmSnaps := sender.snapshots(101)
	if len(warmSnaps) == 0 {
		t.Fatal("missing warmup snapshot")
	}
	startX := findEntityByUID(warmSnaps[len(warmSnaps)-1], 101).PosX
	startSeq := warmSnaps[len(warmSnaps)-1].LastInputSeq
	sender.drain(101)

	// 持续 30 tick（1.0 秒），每 tick 注入 4 条输入（120Hz）
	for i := 0; i < 30; i++ {
		sendBurst(4)
		runner.Step(1)
	}
	snaps := sender.snapshots(101)
	if len(snaps) == 0 {
		t.Fatal("missing snapshots during 120Hz stream")
	}
	lastSnap := snaps[len(snaps)-1]
	endX := findEntityByUID(lastSnap, 101).PosX
	deltaX := endX - startX
	consumed := lastSnap.LastInputSeq - startSeq

	// 30 tick 内严格只能消费 30 条输入，位移应等于 1.0x MoveSpeed（3.0 units），绝不能出现 4x（12.0）
	if consumed != 30 {
		t.Fatalf("consumed inputs in 30 ticks = %d, want 30", consumed)
	}
	if math.Abs(float64(deltaX-3.0)) > 1e-3 {
		t.Fatalf("displacement in 1s under 120Hz flood = %f, want 3.0 (1x MoveSpeed)", deltaX)
	}

	// Part B: 抖动恢复测试 — 另建房间，静默 4 tick 后单 tick 突发 4 条输入，应立即全部消费。
	mgr2, runner2, bus2, sender2, _ := newWorldFixture(t)
	event.Publish(bus2, event.GameStarted, event.GameStartedArgs{
		RoomID:     21,
		MapIDs:     []uint32{1},
		PlayerUIDs: []uint64{201},
	})
	mgr2.Route(201, "b", "c2", &knight.Envelope{
		MsgId:   knight.MsgId_MSG_ROOM_LOADED,
		Payload: &knight.Envelope_RoomLoaded{RoomLoaded: &knight.RoomLoaded{MapId: 1}},
	})
	runner2.Step(1) // 激活（budget=1）
	runner2.Step(4) // 静默 4 tick（budget 封顶至 maxInputBudget=4）
	sender2.drain(201)

	// 单 tick 突发 4 条向右输入
	mgr2.Route(201, "b", "c2", &knight.Envelope{
		MsgId: knight.MsgId_MSG_INPUT_BATCH,
		Payload: &knight.Envelope_InputBatch{InputBatch: &knight.InputBatch{
			FirstSeq: 1,
			Inputs: []*knight.InputCmd{
				{Seq: 1, MoveX: 1},
				{Seq: 2, MoveX: 1},
				{Seq: 3, MoveX: 1},
				{Seq: 4, MoveX: 1},
			},
		}},
	})
	runner2.Step(2) // 第 1 tick 应一次性消化全部 4 条突发输入，第 2 tick 触发 15Hz 快照
	burstSnaps := sender2.snapshots(201)
	if len(burstSnaps) == 0 {
		t.Fatal("missing snapshot after burst recovery")
	}
	bs := burstSnaps[len(burstSnaps)-1]
	if bs.LastInputSeq != 4 {
		t.Fatalf("burst recovery last_input_seq = %d, want 4", bs.LastInputSeq)
	}
	be := findEntityByUID(bs, 201)
	if math.Abs(float64(be.PosX-4.4)) > 1e-4 {
		t.Fatalf("burst recovery posX = %f, want 4.4", be.PosX)
	}
}

// TestPendingInputsZeroLeakAndBoundedCapacity 验证 P1-5：
// 消费输入后底层数组尾部指针全部清零，且多轮循环后容量不会无限膨胀。
func TestPendingInputsZeroLeakAndBoundedCapacity(t *testing.T) {
	mgr, runner, bus, _, _ := newWorldFixture(t)

	event.Publish(bus, event.GameStarted, event.GameStartedArgs{
		RoomID:     22,
		MapIDs:     []uint32{1},
		PlayerUIDs: []uint64{101},
	})
	mgr.Route(101, "a", "c1", &knight.Envelope{
		MsgId:   knight.MsgId_MSG_ROOM_LOADED,
		Payload: &knight.Envelope_RoomLoaded{RoomLoaded: &knight.RoomLoaded{MapId: 1}},
	})
	runner.Step(1)

	var seq uint32
	for cycle := 0; cycle < 200; cycle++ {
		cmds := make([]*knight.InputCmd, 4)
		for i := 0; i < 4; i++ {
			seq++
			cmds[i] = &knight.InputCmd{Seq: seq, MoveX: 1}
		}
		mgr.Route(101, "a", "c1", &knight.Envelope{
			MsgId: knight.MsgId_MSG_INPUT_BATCH,
			Payload: &knight.Envelope_InputBatch{InputBatch: &knight.InputBatch{
				FirstSeq: cmds[0].Seq,
				Inputs:   cmds,
			}},
		})
		runner.Step(4)
	}

	room := mgr.roomForUID(101)
	if room == nil {
		t.Fatal("room missing")
	}
	p := room.byUID[101]
	if len(p.PendingInputs) != 0 {
		t.Fatalf("PendingInputs len = %d, want 0", len(p.PendingInputs))
	}
	if cap(p.PendingInputs) > MaxInputBatchSize {
		t.Fatalf("PendingInputs cap grew unboundedly: %d > %d", cap(p.PendingInputs), MaxInputBatchSize)
	}
	// 检查底层数组尾部全部已清零（无滞留 *knight.InputCmd 指针）
	backing := p.PendingInputs[:cap(p.PendingInputs)]
	for i, elem := range backing {
		if elem != nil {
			t.Fatalf("backing[%d] leaked non-nil InputCmd pointer: %+v", i, elem)
		}
	}
}

// TestWorldPublishesGameEndedWhenRoomEmpties 验证 P1-4：世界房间清空关闭时发布 event.GameEnded。
func TestWorldPublishesGameEndedWhenRoomEmpties(t *testing.T) {
	mgr, runner, bus, _, _ := newWorldFixture(t)

	var ended []event.GameEndedArgs
	event.Subscribe(bus, event.GameEnded, func(args event.GameEndedArgs) {
		ended = append(ended, args)
	})

	event.Publish(bus, event.GameStarted, event.GameStartedArgs{
		RoomID:     23,
		MapIDs:     []uint32{1},
		PlayerUIDs: []uint64{101},
	})
	runner.Step(1)

	event.Publish(bus, event.MemberLeft, event.MemberLeftArgs{
		RoomID: 23,
		UID:    101,
		Reason: event.LeaveActive,
	})
	runner.Step(1)

	if mgr.RoomCount() != 0 {
		t.Fatalf("RoomCount = %d, want 0", mgr.RoomCount())
	}
	if len(ended) != 1 || ended[0].RoomID != 23 || ended[0].Reason != event.GameEndEmpty {
		t.Fatalf("GameEnded events = %+v, want [{RoomID:23 Reason:empty}]", ended)
	}
}
