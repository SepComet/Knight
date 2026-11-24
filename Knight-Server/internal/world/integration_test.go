package world_test

import (
	"context"
	"math"
	"sync"
	"testing"
	"time"

	"knight-server/internal/event"
	"knight-server/internal/kcp2k"
	"knight-server/internal/lobby"
	"knight-server/internal/protocol"
	"knight-server/internal/session"
	"knight-server/internal/tick"
	"knight-server/internal/world"

	knight "knight-server/protogen"
)

type mapValidator map[string]uint64

func (m mapValidator) Validate(token string) (uint64, string, bool) {
	uid, ok := m[token]
	if !ok {
		return 0, "", false
	}
	return uid, token, true
}

// battleRig 全栈联机测试环境：kcp2k + session + lobby + world + tick 真实 UDP。
type battleRig struct {
	t       *testing.T
	gate    *kcp2k.Server
	runner  *tick.Runner
	bus     *event.Bus
	lobby   *lobby.Manager
	world   *world.Manager
	now     uint32
	clients []*kcp2k.Client
	inbox   [][]*knight.Envelope
}

func newBattleRig(t *testing.T) *battleRig {
	t.Helper()
	r := &battleRig{t: t}
	var mgr *session.Manager
	gate, err := kcp2k.Listen("127.0.0.1:0", kcp2k.DefaultConfig(), kcp2k.ServerCallbacks{
		OnConnect:    func(id string) { mgr.OnKcpConnect(id) },
		OnData:       func(id string, data []byte, _ kcp2k.Channel) { mgr.OnKcpData(id, data, kcp2k.ChannelReliable) },
		OnDisconnect: func(id string) { mgr.OnKcpDisconnect(id) },
	})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(gate.Stop)
	r.gate = gate

	bus := event.NewBus()
	r.bus = bus
	runner, err := tick.NewRunner(tick.Config{Rate: 30})
	if err != nil {
		t.Fatalf("tick runner: %v", err)
	}
	t.Cleanup(runner.Stop)
	r.runner = runner

	mgr = session.NewManager(
		session.Config{ProtoVersion: 5, SessionLimit: 64, HeartbeatTimeout: 10 * time.Second, DisconnectDelay: 5 * time.Millisecond},
		mapValidator{"tok-a": 1, "tok-b": 2},
		gate,
		bus,
	)
	lby := lobby.NewManager(lobby.Config{MaxPlayers: 4, MinPlayers: 2, MaxRooms: 8}, gate, mgr, bus)
	wld := world.NewManager(world.Config{TickRate: 30}, runner, mgr, mgr, bus)
	r.lobby = lby
	r.world = wld

	mgr.SetRouter(session.NewDomainRouter(gate,
		session.DomainRoute{Min: 32, Max: 63, Router: lby},
		session.DomainRoute{Min: 64, Max: 127, Router: wld},
	))
	return r
}

func (r *battleRig) dial(token string) int {
	r.t.Helper()
	i := len(r.clients)
	r.inbox = append(r.inbox, nil)

	cli, err := kcp2k.Dial(kcp2k.DefaultConfig(), r.gate.LocalAddr().String(), kcp2k.ClientCallbacks{
		OnData: func(message []byte, _ kcp2k.Channel) {
			env, derr := protocol.Decode(message)
			if derr != nil {
				r.t.Fatalf("client %d decode: %v", i, derr)
			}
			r.inbox[i] = append(r.inbox[i], env)
		},
	})
	if err != nil {
		r.t.Fatalf("dial: %v", err)
	}
	r.t.Cleanup(cli.Disconnect)
	r.clients = append(r.clients, cli)

	r.pumpUntil(func() bool { return cli.State() == kcp2k.PeerAuthenticated }, "kcp2k handshake")
	r.send(i, &knight.Envelope{Payload: &knight.Envelope_HandshakeReq{
		HandshakeReq: &knight.HandshakeReq{ProtoVersion: 5, Token: token},
	}})
	r.pumpUntil(func() bool {
		return len(r.inbox[i]) > 0 && r.inbox[i][0].GetHandshakeAck() != nil
	}, "session handshake")
	r.inbox[i] = nil
	return i
}

func (r *battleRig) pump(steps int) {
	for i := 0; i < steps; i++ {
		r.now += 10
		for _, c := range r.clients {
			c.TickIncoming(r.now)
			c.TickOutgoing(r.now)
		}
		r.gate.Step(r.now)
		time.Sleep(200 * time.Microsecond)
	}
}

func (r *battleRig) pumpUntil(cond func() bool, desc string) {
	r.t.Helper()
	for i := 0; i < 2000; i++ {
		if cond() {
			return
		}
		r.pump(1)
	}
	r.t.Fatalf("timeout waiting for %s", desc)
}

func (r *battleRig) send(i int, env *knight.Envelope) {
	r.t.Helper()
	data, err := protocol.Encode(env)
	if err != nil {
		r.t.Fatal(err)
	}
	r.clients[i].Send(data, kcp2k.ChannelReliable)
}

func (r *battleRig) hasRoomTransition(i int) bool {
	for _, env := range r.inbox[i] {
		if env.GetRoomTransition() != nil {
			return true
		}
	}
	return false
}

func (r *battleRig) latestSnapshot(i int) *knight.Snapshot {
	var last *knight.Snapshot
	for _, env := range r.inbox[i] {
		if s := env.GetSnapshot(); s != nil {
			last = s
		}
	}
	return last
}

func TestFullStackM1StartLoadMoveDisconnect(t *testing.T) {
	r := newBattleRig(t)
	a := r.dial("tok-a")
	b := r.dial("tok-b")

	// 1. 建房 → 进房 → 准备 → 开局
	r.send(a, &knight.Envelope{Payload: &knight.Envelope_CreateRoomReq{CreateRoomReq: &knight.CreateRoomReq{}}})
	r.pumpUntil(func() bool { return len(r.inbox[a]) > 0 && r.inbox[a][0].GetRoomStatePush() != nil }, "room created")
	roomID := r.inbox[a][0].GetRoomStatePush().RoomId
	r.inbox[a] = nil

	r.send(b, &knight.Envelope{Payload: &knight.Envelope_JoinRoomReq{JoinRoomReq: &knight.JoinRoomReq{RoomId: roomID}}})
	r.pumpUntil(func() bool { return len(r.inbox[b]) > 0 }, "b joined")
	r.inbox[a], r.inbox[b] = nil, nil

	r.send(a, &knight.Envelope{Payload: &knight.Envelope_StartGame{StartGame: &knight.StartGame{}}})
	r.pumpUntil(func() bool { return r.hasRoomTransition(a) && r.hasRoomTransition(b) }, "room transition")
	r.inbox[a], r.inbox[b] = nil, nil

	// 2. 双方确认 RoomLoaded(map_id=1) → 激活并收到首帧快照
	r.send(a, &knight.Envelope{Payload: &knight.Envelope_RoomLoaded{RoomLoaded: &knight.RoomLoaded{MapId: 1}}})
	r.send(b, &knight.Envelope{Payload: &knight.Envelope_RoomLoaded{RoomLoaded: &knight.RoomLoaded{MapId: 1}}})
	r.pump(10)
	r.runner.Step(1)
	r.pumpUntil(func() bool { return r.latestSnapshot(a) != nil && r.latestSnapshot(b) != nil }, "initial snapshot")
	r.inbox[a], r.inbox[b] = nil, nil

	// 3. 玩家 A 发送移动输入 → 步进 2 tick（15Hz 快照）→ 双方收到更新位置
	r.send(a, &knight.Envelope{Payload: &knight.Envelope_InputBatch{InputBatch: &knight.InputBatch{
		FirstSeq: 1,
		Inputs: []*knight.InputCmd{
			{Seq: 1, MoveX: 1, MoveY: 0, AimAngle: 0},
		},
	}}})
	r.pump(10)
	r.runner.Step(2)
	r.pumpUntil(func() bool { return r.latestSnapshot(a) != nil && r.latestSnapshot(b) != nil }, "movement snapshot")

	snapA := r.latestSnapshot(a)
	snapB := r.latestSnapshot(b)
	if snapA.LastInputSeq != 1 || snapB.LastInputSeq != 0 {
		t.Fatalf("last_input_seq mismatch: A=%d B=%d", snapA.LastInputSeq, snapB.LastInputSeq)
	}
	var posAX float32
	for _, e := range snapB.Entities {
		if e.PlayerUid == 1 {
			posAX = e.PosX
		}
	}
	if math.Abs(float64(posAX-4.1)) > 1e-4 {
		t.Fatalf("B observed A at x=%f, want 4.1", posAX)
	}

	// 4. 玩家 B 断线 → 玩家 A 收到 PlayerLeft(uid=2)
	r.inbox[a] = nil
	r.clients[b].Disconnect()
	r.pump(15)
	r.runner.Step(1)
	r.pumpUntil(func() bool {
		for _, env := range r.inbox[a] {
			if pl := env.GetPlayerLeft(); pl != nil && pl.Uid == 2 {
				return true
			}
		}
		return false
	}, "player left")
}

// TestRunningRoomLeaveNoRoomStatePushAndRecreateRoom 验证 P1-4：
// 1. ROOM_RUNNING 期间成员主动 LeaveRoom 时，lobby 不再向剩余玩家广播 RoomStatePush（仅由 world 发 PlayerLeft）；
// 2. 离开的成员可立即重新建房；
// 3. 当 world 发布 event.GameEnded 关闭旧房间时，lobby 清理 rooms/roomOf 并向在场玩家推送 ROOM_CLOSED，
//    之后该玩家也可立即重新建房。
func TestRunningRoomLeaveNoRoomStatePushAndRecreateRoom(t *testing.T) {
	r := newBattleRig(t)
	a := r.dial("tok-a")
	b := r.dial("tok-b")

	// 建房 → 加入 → 开局
	r.send(a, &knight.Envelope{Payload: &knight.Envelope_CreateRoomReq{CreateRoomReq: &knight.CreateRoomReq{}}})
	r.pumpUntil(func() bool { return len(r.inbox[a]) > 0 && r.inbox[a][0].GetRoomStatePush() != nil }, "room created")
	roomID := r.inbox[a][0].GetRoomStatePush().RoomId
	r.inbox[a] = nil

	r.send(b, &knight.Envelope{Payload: &knight.Envelope_JoinRoomReq{JoinRoomReq: &knight.JoinRoomReq{RoomId: roomID}}})
	r.pumpUntil(func() bool { return len(r.inbox[b]) > 0 }, "b joined")
	r.inbox[a], r.inbox[b] = nil, nil

	r.send(a, &knight.Envelope{Payload: &knight.Envelope_StartGame{StartGame: &knight.StartGame{}}})
	r.pumpUntil(func() bool { return r.hasRoomTransition(a) && r.hasRoomTransition(b) }, "room transition")
	r.inbox[a], r.inbox[b] = nil, nil

	// 双方加载完成进入活跃对局
	r.send(a, &knight.Envelope{Payload: &knight.Envelope_RoomLoaded{RoomLoaded: &knight.RoomLoaded{MapId: 1}}})
	r.send(b, &knight.Envelope{Payload: &knight.Envelope_RoomLoaded{RoomLoaded: &knight.RoomLoaded{MapId: 1}}})
	r.pump(10)
	r.runner.Step(1)
	r.pumpUntil(func() bool { return r.latestSnapshot(a) != nil && r.latestSnapshot(b) != nil }, "initial snapshot")
	r.inbox[a], r.inbox[b] = nil, nil

	// 1. B 在 ROOM_RUNNING 期间主动 LeaveRoom
	r.send(b, &knight.Envelope{Payload: &knight.Envelope_LeaveRoom{LeaveRoom: &knight.LeaveRoom{}}})
	r.pump(10)
	r.runner.Step(1)
	r.pumpUntil(func() bool {
		for _, env := range r.inbox[a] {
			if pl := env.GetPlayerLeft(); pl != nil && pl.Uid == 2 {
				return true
			}
		}
		return false
	}, "A receives PlayerLeft")

	// A 不得收到任何 RoomStatePush（protocol.md §5.6）
	for _, env := range r.inbox[a] {
		if push := env.GetRoomStatePush(); push != nil {
			t.Fatalf("unexpected RoomStatePush to A during ROOM_RUNNING leave: %+v", push)
		}
	}
	r.inbox[a], r.inbox[b] = nil, nil

	// 2. B 立刻重新建房成功
	r.send(b, &knight.Envelope{Payload: &knight.Envelope_CreateRoomReq{CreateRoomReq: &knight.CreateRoomReq{}}})
	r.pumpUntil(func() bool {
		for _, env := range r.inbox[b] {
			if push := env.GetRoomStatePush(); push != nil && push.State == knight.RoomSessionState_ROOM_WAITING {
				return true
			}
		}
		return false
	}, "B recreates room")

	// 3. 模拟 M3 对局结算关闭旧房间（world 发布 event.GameEnded）
	event.Publish(r.bus, event.GameEnded, event.GameEndedArgs{
		RoomID: roomID,
		Reason: event.GameEndVictory,
	})
	r.pumpUntil(func() bool {
		for _, env := range r.inbox[a] {
			if push := env.GetRoomStatePush(); push != nil && push.RoomId == roomID && push.State == knight.RoomSessionState_ROOM_CLOSED {
				return true
			}
		}
		return false
	}, "A receives ROOM_CLOSED on GameEnded")
	r.inbox[a] = nil

	// A 解除旧房绑定后可立刻重新建房
	r.send(a, &knight.Envelope{Payload: &knight.Envelope_CreateRoomReq{CreateRoomReq: &knight.CreateRoomReq{}}})
	r.pumpUntil(func() bool {
		for _, env := range r.inbox[a] {
			if push := env.GetRoomStatePush(); push != nil && push.State == knight.RoomSessionState_ROOM_WAITING && push.RoomId != roomID {
				return true
			}
		}
		return false
	}, "A recreates room after GameEnded")
}

// TestConcurrentGateAndRunnerGracefulShutdown 验证 P1-3：
// 真实双 goroutine（gate.Run + runner.Run）并发收发、存在挂起 Reject 延迟断开定时器时，
// 按 main.go 顺序优雅停机无死锁、无数据竞争，且停机后 Send/Disconnect 不阻塞。
func TestConcurrentGateAndRunnerGracefulShutdown(t *testing.T) {
	var mgr *session.Manager
	gate, err := kcp2k.Listen("127.0.0.1:0", kcp2k.DefaultConfig(), kcp2k.ServerCallbacks{
		OnConnect:    func(id string) { mgr.OnKcpConnect(id) },
		OnData:       func(id string, data []byte, _ kcp2k.Channel) { mgr.OnKcpData(id, data, kcp2k.ChannelReliable) },
		OnDisconnect: func(id string) { mgr.OnKcpDisconnect(id) },
	})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	bus := event.NewBus()
	runner, err := tick.NewRunner(tick.Config{Rate: 30})
	if err != nil {
		t.Fatalf("tick runner: %v", err)
	}

	mgr = session.NewManager(
		session.Config{
			ProtoVersion:     5,
			SessionLimit:     64,
			HeartbeatTimeout: 10 * time.Second,
			DisconnectDelay:  200 * time.Millisecond, // 刻意留出停机时仍挂起的延迟断开定时器
		},
		mapValidator{"tok-a": 1, "tok-b": 2},
		gate,
		bus,
	)
	lby := lobby.NewManager(lobby.Config{MaxPlayers: 4, MinPlayers: 2, MaxRooms: 8}, gate, mgr, bus)
	wld := world.NewManager(world.Config{TickRate: 30}, runner, mgr, mgr, bus)
	mgr.SetRouter(session.NewDomainRouter(gate,
		session.DomainRoute{Min: 32, Max: 63, Router: lby},
		session.DomainRoute{Min: 64, Max: 127, Router: wld},
	))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	gateDone := make(chan struct{})
	go func() {
		defer close(gateDone)
		gate.Run()
	}()
	runnerDone := make(chan struct{})
	go func() {
		defer close(runnerDone)
		runner.Run(ctx)
	}()

	// 客户端驱动协程：真实 UDP 握手 → 开局 → 持续收发快照与输入
	var mu sync.Mutex
	var gotSnapA bool
	var roomID uint32
	cliStop := make(chan struct{})
	var cliWG sync.WaitGroup

	type liveClient struct {
		sendCh chan []byte
		ready  chan struct{}
	}

	dialLive := func(token string, onEnv func(*knight.Envelope)) *liveClient {
		cli, derr := kcp2k.Dial(kcp2k.DefaultConfig(), gate.LocalAddr().String(), kcp2k.ClientCallbacks{
			OnData: func(message []byte, _ kcp2k.Channel) {
				if env, err := protocol.Decode(message); err == nil {
					onEnv(env)
				}
			},
		})
		if derr != nil {
			t.Fatalf("dial: %v", derr)
		}
		lc := &liveClient{
			sendCh: make(chan []byte, 64),
			ready:  make(chan struct{}),
		}
		cliWG.Add(1)
		go func() {
			defer cliWG.Done()
			start := time.Now()
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			authed := false
			for {
				now := uint32(time.Since(start).Milliseconds())
				cli.TickIncoming(now)
				if !authed && cli.State() == kcp2k.PeerAuthenticated {
					authed = true
					data, _ := protocol.Encode(&knight.Envelope{Payload: &knight.Envelope_HandshakeReq{
						HandshakeReq: &knight.HandshakeReq{ProtoVersion: 5, Token: token},
					}})
					cli.Send(data, kcp2k.ChannelReliable)
					close(lc.ready)
				}
				for draining := true; draining; {
					select {
					case payload := <-lc.sendCh:
						cli.Send(payload, kcp2k.ChannelReliable)
					default:
						draining = false
					}
				}
				cli.TickOutgoing(now)
				select {
				case <-cliStop:
					cli.Disconnect()
					return
				case <-ticker.C:
				}
			}
		}()
		select {
		case <-lc.ready:
		case <-time.After(2 * time.Second):
			t.Fatalf("kcp handshake timeout for %s", token)
		}
		return lc
	}

	ackA, ackB := make(chan struct{}, 1), make(chan struct{}, 1)
	joinedB := make(chan struct{}, 1)
	transA, transB := make(chan struct{}, 1), make(chan struct{}, 1)

	cliA := dialLive("tok-a", func(env *knight.Envelope) {
		switch {
		case env.GetHandshakeAck() != nil:
			select {
			case ackA <- struct{}{}:
			default:
			}
		case env.GetRoomStatePush() != nil:
			mu.Lock()
			if roomID == 0 {
				roomID = env.GetRoomStatePush().RoomId
			}
			mu.Unlock()
		case env.GetRoomTransition() != nil:
			select {
			case transA <- struct{}{}:
			default:
			}
		case env.GetSnapshot() != nil:
			mu.Lock()
			gotSnapA = true
			mu.Unlock()
		}
	})
	cliB := dialLive("tok-b", func(env *knight.Envelope) {
		switch {
		case env.GetHandshakeAck() != nil:
			select {
			case ackB <- struct{}{}:
			default:
			}
		case env.GetRoomStatePush() != nil:
			select {
			case joinedB <- struct{}{}:
			default:
			}
		case env.GetRoomTransition() != nil:
			select {
			case transB <- struct{}{}:
			default:
			}
		}
	})

	sendEnv := func(cli *liveClient, env *knight.Envelope) {
		data, err := protocol.Encode(env)
		if err != nil {
			t.Fatal(err)
		}
		cli.sendCh <- data
	}

	<-ackA
	<-ackB

	sendEnv(cliA, &knight.Envelope{Payload: &knight.Envelope_CreateRoomReq{CreateRoomReq: &knight.CreateRoomReq{}}})
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		rid := roomID
		mu.Unlock()
		if rid != 0 {
			sendEnv(cliB, &knight.Envelope{Payload: &knight.Envelope_JoinRoomReq{JoinRoomReq: &knight.JoinRoomReq{RoomId: rid}}})
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timeout waiting for room create")
		}
		time.Sleep(5 * time.Millisecond)
	}
	<-joinedB

	sendEnv(cliA, &knight.Envelope{Payload: &knight.Envelope_StartGame{StartGame: &knight.StartGame{}}})
	<-transA
	<-transB

	sendEnv(cliA, &knight.Envelope{Payload: &knight.Envelope_RoomLoaded{RoomLoaded: &knight.RoomLoaded{MapId: 1}}})
	sendEnv(cliB, &knight.Envelope{Payload: &knight.Envelope_RoomLoaded{RoomLoaded: &knight.RoomLoaded{MapId: 1}}})

	// 等待至少收到一帧活跃快照，同时发一些移动输入
	deadline = time.Now().Add(2 * time.Second)
	for seq := uint32(1); ; seq++ {
		sendEnv(cliA, &knight.Envelope{Payload: &knight.Envelope_InputBatch{InputBatch: &knight.InputBatch{
			FirstSeq: seq,
			Inputs:   []*knight.InputCmd{{Seq: seq, MoveX: 1}},
		}}})
		mu.Lock()
		ok := gotSnapA
		mu.Unlock()
		if ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timeout waiting for live snapshot")
		}
		time.Sleep(15 * time.Millisecond)
	}

	// 触发一次协议违规 Reject，让 session.Manager 挂起 200ms 延迟断开定时器
	badInputs := make([]*knight.InputCmd, world.MaxInputBatchSize+1)
	for i := range badInputs {
		badInputs[i] = &knight.InputCmd{Seq: uint32(i + 1)}
	}
	sendEnv(cliB, &knight.Envelope{Payload: &knight.Envelope_InputBatch{InputBatch: &knight.InputBatch{
		FirstSeq: 1,
		Inputs:   badInputs,
	}}})
	time.Sleep(30 * time.Millisecond)

	// 按 main.go 顺序优雅停机：先 runner.Stop，再 mgr.Close，再 gate.Stop
	shutdownDone := make(chan struct{})
	go func() {
		cancel()
		runner.Stop()
		mgr.Close()
		gate.Stop()
		close(shutdownDone)
	}()

	select {
	case <-shutdownDone:
	case <-time.After(3 * time.Second):
		t.Fatal("graceful shutdown deadlocked or timed out")
	}

	select {
	case <-runnerDone:
	case <-time.After(time.Second):
		t.Fatal("runner.Run did not exit")
	}
	select {
	case <-gateDone:
	case <-time.After(time.Second):
		t.Fatal("gate.Run did not exit")
	}

	// 停机后调用 Send / Disconnect 不得阻塞（包括连续多次超过 ops 缓冲容量）
	postDone := make(chan struct{})
	go func() {
		for i := 0; i < 2048; i++ {
			gate.Send("nonexistent", []byte("x"), kcp2k.ChannelReliable)
			gate.Disconnect("nonexistent")
		}
		close(postDone)
	}()
	select {
	case <-postDone:
	case <-time.After(2 * time.Second):
		t.Fatal("post-stop Send/Disconnect blocked")
	}

	close(cliStop)
	cliWG.Wait()
}
