package session_test

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"knight-server/internal/event"
	"knight-server/internal/kcp2k"
	"knight-server/internal/lobby"
	"knight-server/internal/protocol"
	"knight-server/internal/session"

	knight "knight-server/protogen"
)

// lobbyRig 多客户端大厅测试环境：kcp2k gate + session manager + lobby router 全栈真实 UDP。
type lobbyRig struct {
	t       *testing.T
	gate    *kcp2k.Server
	mgr     *session.Manager
	lobby   *lobby.Manager
	now     uint32
	clients []*kcp2k.Client
	inbox   [][]*knight.Envelope
	disc    []bool
	events  []string // 大厅域事件流水（handler 与 gate 回调同 goroutine，无并发）
}

func newLobbyRig(t *testing.T, minPlayers int) *lobbyRig {
	t.Helper()
	r := &lobbyRig{t: t}
	var mgr *session.Manager
	var lby *lobby.Manager
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
	event.Subscribe(bus, event.RoomCreated, func(a event.RoomCreatedArgs) {
		r.events = append(r.events, fmt.Sprintf("created:%d:%d", a.RoomID, a.OwnerUID))
	})
	event.Subscribe(bus, event.MemberJoined, func(a event.MemberJoinedArgs) {
		r.events = append(r.events, fmt.Sprintf("joined:%d:%d", a.RoomID, a.UID))
	})
	event.Subscribe(bus, event.MemberLeft, func(a event.MemberLeftArgs) {
		r.events = append(r.events, fmt.Sprintf("left:%d:%d:%s", a.RoomID, a.UID, a.Reason))
	})
	event.Subscribe(bus, event.OwnerChanged, func(a event.OwnerChangedArgs) {
		r.events = append(r.events, fmt.Sprintf("owner:%d:%d->%d", a.RoomID, a.FromUID, a.ToUID))
	})
	event.Subscribe(bus, event.RoomDestroyed, func(a event.RoomDestroyedArgs) {
		r.events = append(r.events, fmt.Sprintf("destroyed:%d", a.RoomID))
	})
	event.Subscribe(bus, event.GameStarted, func(a event.GameStartedArgs) {
		r.events = append(r.events, fmt.Sprintf("started:%d:%v", a.RoomID, a.PlayerUIDs))
	})

	// 会话层先于大厅构造（大厅广播需要 mgr 作 UIDSender）；Router 走域级复合路由全栈验证
	mgr = session.NewManager(
		session.Config{ProtoVersion: 6, SessionLimit: 64, HeartbeatTimeout: 10 * time.Second, DisconnectDelay: 5 * time.Millisecond},
		mapValidator{"tok-a": 1, "tok-b": 2, "tok-c": 3, "tok-d": 4, "tok-e": 5},
		gate,
		bus,
	)
	lby = lobby.NewManager(lobby.Config{MaxPlayers: 4, MinPlayers: minPlayers, MaxRooms: 8}, gate, mgr, bus)
	mgr.SetRouter(session.NewDomainRouter(gate,
		session.DomainRoute{Min: 32, Max: 63, Router: lby},
	))
	r.mgr = mgr
	r.lobby = lby
	return r
}

// dial 新客户端完成完整握手，返回客户端索引。token 形如 "tok-a"。
func (r *lobbyRig) dial(token string, uid uint64) int {
	r.t.Helper()
	i := len(r.clients)
	r.inbox = append(r.inbox, nil)
	r.disc = append(r.disc, false)

	cli, err := kcp2k.Dial(kcp2k.DefaultConfig(), r.gate.LocalAddr().String(), kcp2k.ClientCallbacks{
		OnData: func(message []byte, _ kcp2k.Channel) {
			env, derr := protocol.Decode(message)
			if derr != nil {
				r.t.Fatalf("client %d decode: %v", i, derr)
			}
			r.inbox[i] = append(r.inbox[i], env)
		},
		OnDisconnected: func() { r.disc[i] = true },
	})
	if err != nil {
		r.t.Fatalf("dial: %v", err)
	}
	r.t.Cleanup(cli.Disconnect)
	r.clients = append(r.clients, cli)

	// 泵到 kcp2k 认证 → 发 HandshakeReq → 等 Ack
	r.pumpUntil(func() bool { return cli.State() == kcp2k.PeerAuthenticated }, "kcp2k handshake")
	data, err := protocol.Encode(&knight.Envelope{Payload: &knight.Envelope_HandshakeReq{
		HandshakeReq: &knight.HandshakeReq{ProtoVersion: 6, Token: token},
	}})
	if err != nil {
		r.t.Fatal(err)
	}
	cli.Send(data, kcp2k.ChannelReliable)
	r.pumpUntil(func() bool {
		return len(r.inbox[i]) > 0 && r.inbox[i][0].GetHandshakeAck() != nil
	}, "session handshake")
	r.inbox[i] = nil // 消费握手 Ack
	return i
}

func (r *lobbyRig) pump(steps int) {
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

func (r *lobbyRig) pumpUntil(cond func() bool, desc string) {
	r.t.Helper()
	for i := 0; i < 2000; i++ {
		if cond() {
			return
		}
		r.pump(1)
	}
	r.t.Fatalf("timeout waiting for %s", desc)
}

// send 发送业务消息。
func (r *lobbyRig) send(i int, env *knight.Envelope) {
	r.t.Helper()
	data, err := protocol.Encode(env)
	if err != nil {
		r.t.Fatal(err)
	}
	r.clients[i].Send(data, kcp2k.ChannelReliable)
}

// nextRoomState 取客户端收件箱中最新的 RoomStatePush（不消费；观察最新状态用）。
func (r *lobbyRig) nextRoomState(i int) *knight.RoomStatePush {
	r.t.Helper()
	var last *knight.RoomStatePush
	for _, env := range r.inbox[i] {
		if p := env.GetRoomStatePush(); p != nil {
			last = p
		}
	}
	return last
}

// takeNext 取客户端下一条消息（任意类型）。
func (r *lobbyRig) takeNext(i int) *knight.Envelope {
	r.t.Helper()
	r.pumpUntil(func() bool { return len(r.inbox[i]) > 0 }, "message for client")
	env := r.inbox[i][0]
	r.inbox[i] = r.inbox[i][1:]
	return env
}

// clearInbox 丢弃指定客户端的收件箱。
func (r *lobbyRig) clearInbox(i int) { r.inbox[i] = nil }

func TestLobbyCreateJoinReadyStart(t *testing.T) {
	r := newLobbyRig(t, 2)
	a := r.dial("tok-a", 1)
	b := r.dial("tok-b", 2)

	// A 建房：A 收 RoomStatePush（房间号/房主/成员表）
	r.send(a, &knight.Envelope{Payload: &knight.Envelope_CreateRoomReq{CreateRoomReq: &knight.CreateRoomReq{}}})
	r.pumpUntil(func() bool { return r.nextRoomState(a) != nil }, "room state push")
	pushA := r.nextRoomState(a)
	if pushA.RoomId == 0 || pushA.OwnerUid != 1 || pushA.State != knight.RoomSessionState_ROOM_WAITING || len(pushA.Members) != 1 {
		t.Fatalf("pushA = %+v", pushA)
	}

	// B 加入：双方收全量成员表
	r.send(b, &knight.Envelope{Payload: &knight.Envelope_JoinRoomReq{JoinRoomReq: &knight.JoinRoomReq{RoomId: pushA.RoomId}}})
	r.pumpUntil(func() bool {
		return r.nextRoomState(a) != nil && len(r.nextRoomState(a).Members) == 2 &&
			r.nextRoomState(b) != nil && len(r.nextRoomState(b).Members) == 2
	}, "both see 2 members")

	// 双准备
	r.send(a, &knight.Envelope{Payload: &knight.Envelope_SetReady{SetReady: &knight.SetReady{Ready: true}}})
	r.send(b, &knight.Envelope{Payload: &knight.Envelope_SetReady{SetReady: &knight.SetReady{Ready: true}}})
	r.pumpUntil(func() bool {
		pa := r.nextRoomState(a)
		return pa != nil && len(pa.Members) == 2 && pa.Members[0].Ready && pa.Members[1].Ready
	}, "both ready")
	r.clearInbox(a)
	r.clearInbox(b)

	// 房主开局：双方收 GameStart{[1,2,3]} + RoomStatePush{RUNNING}
	r.send(a, &knight.Envelope{Payload: &knight.Envelope_StartGame{StartGame: &knight.StartGame{}}})

	for _, idx := range []int{a, b} {
		env := r.takeNext(idx)
		gs := env.GetGameStart()
		if gs == nil || len(gs.MapIds) != 4 || gs.MapIds[0] != 1 || gs.MapIds[len(gs.MapIds)-1] != 9 || gs.Seed == 0 {
			t.Fatalf("client %d: expected GameStart(4 maps, seed!=0), got %+v", idx, gs)
		}
		env = r.takeNext(idx)
		p := env.GetRoomStatePush()
		if p == nil || p.State != knight.RoomSessionState_ROOM_RUNNING {
			t.Fatalf("client %d: expected RUNNING push, got %T", idx, env.Payload)
		}
	}
}

func TestLobbyJoinErrors(t *testing.T) {
	r := newLobbyRig(t, 2)
	a := r.dial("tok-a", 1)
	r.send(a, &knight.Envelope{Payload: &knight.Envelope_CreateRoomReq{CreateRoomReq: &knight.CreateRoomReq{}}})
	r.pumpUntil(func() bool { return r.nextRoomState(a) != nil }, "room created")
	roomID := r.nextRoomState(a).RoomId

	// 房间不存在
	b := r.dial("tok-b", 2)
	r.send(b, &knight.Envelope{Payload: &knight.Envelope_JoinRoomReq{JoinRoomReq: &knight.JoinRoomReq{RoomId: 999}}})
	if e := r.takeNext(b).GetError(); e == nil || e.Code != knight.ErrorCode_ERR_ROOM_NOT_FOUND {
		t.Fatalf("expected ERR_ROOM_NOT_FOUND, got %v", e)
	}

	// 满员（MaxPlayers=4）：填满 3 人后第 5 人被拒
	c := r.dial("tok-c", 3)
	d := r.dial("tok-d", 4)
	for _, idx := range []int{b, c, d} {
		r.send(idx, &knight.Envelope{Payload: &knight.Envelope_JoinRoomReq{JoinRoomReq: &knight.JoinRoomReq{RoomId: roomID}}})
	}
	r.pumpUntil(func() bool {
		p := r.nextRoomState(a)
		return p != nil && len(p.Members) == 4
	}, "room full with 4")
	for _, idx := range []int{b, c, d} {
		r.clearInbox(idx)
	}
	e5 := r.dial("tok-e", 5)
	r.send(e5, &knight.Envelope{Payload: &knight.Envelope_JoinRoomReq{JoinRoomReq: &knight.JoinRoomReq{RoomId: roomID}}})
	if e := r.takeNext(e5).GetError(); e == nil || e.Code != knight.ErrorCode_ERR_ROOM_FULL {
		t.Fatalf("expected ERR_ROOM_FULL, got %v", e)
	}
}

func TestLobbyJoinAfterStartRejected(t *testing.T) {
	r := newLobbyRig(t, 1) // 开发期配置：1 人可开局
	a := r.dial("tok-a", 1)
	r.send(a, &knight.Envelope{Payload: &knight.Envelope_CreateRoomReq{CreateRoomReq: &knight.CreateRoomReq{}}})
	r.pumpUntil(func() bool { return r.nextRoomState(a) != nil }, "room created")
	roomID := r.nextRoomState(a).RoomId
	r.clearInbox(a)

	r.send(a, &knight.Envelope{Payload: &knight.Envelope_StartGame{StartGame: &knight.StartGame{}}})
	r.pumpUntil(func() bool {
		return r.nextRoomState(a) != nil && r.nextRoomState(a).State == knight.RoomSessionState_ROOM_RUNNING
	}, "running")
	r.clearInbox(a)

	// 已开局后新玩家加入 → ERR_GAME_ALREADY_STARTED
	b := r.dial("tok-b", 2)
	r.send(b, &knight.Envelope{Payload: &knight.Envelope_JoinRoomReq{JoinRoomReq: &knight.JoinRoomReq{RoomId: roomID}}})
	if e := r.takeNext(b).GetError(); e == nil || e.Code != knight.ErrorCode_ERR_GAME_ALREADY_STARTED {
		t.Fatalf("expected ERR_GAME_ALREADY_STARTED, got %v", e)
	}

	// RUNNING 中准备 → ERR_INVALID_REQUEST
	r.send(a, &knight.Envelope{Payload: &knight.Envelope_SetReady{SetReady: &knight.SetReady{Ready: true}}})
	env := r.takeNext(a)
	if e := env.GetError(); e == nil || e.Code != knight.ErrorCode_ERR_INVALID_REQUEST {
		t.Fatalf("expected ERR_INVALID_REQUEST, got %T %v", env.Payload, e)
	}
}

func TestLobbyStartPermissionAndMinPlayers(t *testing.T) {
	r := newLobbyRig(t, 2)
	a := r.dial("tok-a", 1)
	b := r.dial("tok-b", 2)
	r.send(a, &knight.Envelope{Payload: &knight.Envelope_CreateRoomReq{CreateRoomReq: &knight.CreateRoomReq{}}})
	r.pumpUntil(func() bool { return r.nextRoomState(a) != nil }, "room created")
	roomID := r.nextRoomState(a).RoomId
	r.send(b, &knight.Envelope{Payload: &knight.Envelope_JoinRoomReq{JoinRoomReq: &knight.JoinRoomReq{RoomId: roomID}}})
	r.pumpUntil(func() bool { return r.nextRoomState(a) != nil && len(r.nextRoomState(a).Members) == 2 }, "room with 2")
	r.clearInbox(a)
	r.clearInbox(b)

	// 非房主开局 → ERR_NOT_ROOM_OWNER
	r.send(b, &knight.Envelope{Payload: &knight.Envelope_StartGame{StartGame: &knight.StartGame{}}})
	if e := r.takeNext(b).GetError(); e == nil || e.Code != knight.ErrorCode_ERR_NOT_ROOM_OWNER {
		t.Fatalf("expected ERR_NOT_ROOM_OWNER, got %v", e)
	}

	// 人数不足（minPlayers=2，只有房主... 实际 2 人在房；改为先退一人再试）
	r.send(b, &knight.Envelope{Payload: &knight.Envelope_LeaveRoom{LeaveRoom: &knight.LeaveRoom{}}})
	r.pumpUntil(func() bool {
		p := r.nextRoomState(a)
		return p != nil && len(p.Members) == 1
	}, "back to 1 member")
	r.clearInbox(a)

	r.send(a, &knight.Envelope{Payload: &knight.Envelope_StartGame{StartGame: &knight.StartGame{}}})
	if e := r.takeNext(a).GetError(); e == nil || e.Code != knight.ErrorCode_ERR_INVALID_REQUEST {
		t.Fatalf("expected ERR_INVALID_REQUEST (not enough players), got %v", e)
	}
}

func TestLobbyOwnerLeaveSuccession(t *testing.T) {
	r := newLobbyRig(t, 2)
	a := r.dial("tok-a", 1)
	b := r.dial("tok-b", 2)
	c := r.dial("tok-c", 3)

	r.send(a, &knight.Envelope{Payload: &knight.Envelope_CreateRoomReq{CreateRoomReq: &knight.CreateRoomReq{}}})
	r.pumpUntil(func() bool { return r.nextRoomState(a) != nil }, "room created")
	roomID := r.nextRoomState(a).RoomId
	// 分步加入并泵到确认：B/C 走不同 UDP 连接，到达顺序不保证，
	// 而继任规则是"最早加入者"，必须按序确定成员表
	r.send(b, &knight.Envelope{Payload: &knight.Envelope_JoinRoomReq{JoinRoomReq: &knight.JoinRoomReq{RoomId: roomID}}})
	r.pumpUntil(func() bool {
		p := r.nextRoomState(a)
		return p != nil && len(p.Members) == 2
	}, "2 members")
	r.send(c, &knight.Envelope{Payload: &knight.Envelope_JoinRoomReq{JoinRoomReq: &knight.JoinRoomReq{RoomId: roomID}}})
	r.pumpUntil(func() bool {
		p := r.nextRoomState(a)
		return p != nil && len(p.Members) == 3
	}, "3 members")
	for i := 0; i < 3; i++ {
		r.clearInbox(i)
	}

	// 房主退出 → 最早加入者（B）继任
	r.send(a, &knight.Envelope{Payload: &knight.Envelope_LeaveRoom{LeaveRoom: &knight.LeaveRoom{}}})
	r.pumpUntil(func() bool {
		p := r.nextRoomState(b)
		return p != nil && p.OwnerUid == 2 && len(p.Members) == 2
	}, "B becomes owner")
	r.clearInbox(b)

	// 继任者可以开局（满足人数）
	r.send(b, &knight.Envelope{Payload: &knight.Envelope_StartGame{StartGame: &knight.StartGame{}}})
	env := r.takeNext(b)
	if env.GetGameStart() == nil {
		t.Fatalf("successor should be able to start, got %T", env.Payload)
	}
}

func TestLobbyLeaveLastDestroysRoom(t *testing.T) {
	r := newLobbyRig(t, 2)
	a := r.dial("tok-a", 1)
	r.send(a, &knight.Envelope{Payload: &knight.Envelope_CreateRoomReq{CreateRoomReq: &knight.CreateRoomReq{}}})
	r.pumpUntil(func() bool { return r.nextRoomState(a) != nil }, "room created")
	roomID := r.nextRoomState(a).RoomId

	// 房主退出 → 房间销毁
	r.send(a, &knight.Envelope{Payload: &knight.Envelope_LeaveRoom{LeaveRoom: &knight.LeaveRoom{}}})
	r.pump(5)
	if n := r.lobby.RoomCount(); n != 0 {
		t.Fatalf("room count = %d, want 0", n)
	}

	// 再加入 → ERR_ROOM_NOT_FOUND
	b := r.dial("tok-b", 2)
	r.send(b, &knight.Envelope{Payload: &knight.Envelope_JoinRoomReq{JoinRoomReq: &knight.JoinRoomReq{RoomId: roomID}}})
	if e := r.takeNext(b).GetError(); e == nil || e.Code != knight.ErrorCode_ERR_ROOM_NOT_FOUND {
		t.Fatalf("expected ERR_ROOM_NOT_FOUND, got %v", e)
	}
}

func TestLobbyDisconnectCleansRoom(t *testing.T) {
	r := newLobbyRig(t, 2)
	a := r.dial("tok-a", 1)
	b := r.dial("tok-b", 2)
	r.send(a, &knight.Envelope{Payload: &knight.Envelope_CreateRoomReq{CreateRoomReq: &knight.CreateRoomReq{}}})
	r.pumpUntil(func() bool { return r.nextRoomState(a) != nil }, "room created")
	roomID := r.nextRoomState(a).RoomId
	r.send(b, &knight.Envelope{Payload: &knight.Envelope_JoinRoomReq{JoinRoomReq: &knight.JoinRoomReq{RoomId: roomID}}})
	r.pumpUntil(func() bool {
		p := r.nextRoomState(a)
		return p != nil && len(p.Members) == 2
	}, "2 members")
	r.clearInbox(b)

	// 房主断线：B 应收到成员变化的 RoomStatePush 并继任房主
	r.clients[a].Disconnect()
	r.pumpUntil(func() bool {
		p := r.nextRoomState(b)
		return p != nil && p.OwnerUid == 2 && len(p.Members) == 1
	}, "B sees succession after A disconnect")
	if r.lobby.RoomCount() != 1 {
		t.Fatalf("room count = %d, want 1", r.lobby.RoomCount())
	}
	// 断线退房事件 reason=disconnect
	if !slices.Contains(r.events, fmt.Sprintf("left:%d:1:disconnect", roomID)) {
		t.Fatalf("events = %v, want left:%d:1:disconnect", r.events, roomID)
	}
}

// TestLobbyEventSequence 断言大厅域事件的完整序列与 payload（含退订原因与继任次序）。
func TestLobbyEventSequence(t *testing.T) {
	r := newLobbyRig(t, 2)
	a := r.dial("tok-a", 1)
	b := r.dial("tok-b", 2)

	// A 建房 → B 加入 → 房主 A 退房（B 继任）→ B 退房（房间销毁）
	r.send(a, &knight.Envelope{Payload: &knight.Envelope_CreateRoomReq{CreateRoomReq: &knight.CreateRoomReq{}}})
	r.pumpUntil(func() bool { return r.nextRoomState(a) != nil }, "room created")
	roomID := r.nextRoomState(a).RoomId

	r.send(b, &knight.Envelope{Payload: &knight.Envelope_JoinRoomReq{JoinRoomReq: &knight.JoinRoomReq{RoomId: roomID}}})
	r.pumpUntil(func() bool {
		p := r.nextRoomState(a)
		return p != nil && len(p.Members) == 2
	}, "b joined")

	r.send(a, &knight.Envelope{Payload: &knight.Envelope_LeaveRoom{LeaveRoom: &knight.LeaveRoom{}}})
	r.pumpUntil(func() bool {
		p := r.nextRoomState(b)
		return p != nil && p.OwnerUid == 2
	}, "b becomes owner")

	r.send(b, &knight.Envelope{Payload: &knight.Envelope_LeaveRoom{LeaveRoom: &knight.LeaveRoom{}}})
	r.pump(5)
	if r.lobby.RoomCount() != 0 {
		t.Fatalf("room count = %d, want 0", r.lobby.RoomCount())
	}

	want := []string{
		fmt.Sprintf("created:%d:1", roomID),
		fmt.Sprintf("joined:%d:2", roomID),
		fmt.Sprintf("left:%d:1:leave", roomID),
		fmt.Sprintf("owner:%d:1->2", roomID),
		fmt.Sprintf("left:%d:2:leave", roomID),
		fmt.Sprintf("destroyed:%d", roomID),
	}
	if !slices.Equal(r.events, want) {
		t.Fatalf("events = %v\nwant    %v", r.events, want)
	}
}

func TestLobbyRoomPassword(t *testing.T) {
	r := newLobbyRig(t, 2)

	// A 创建加密房间
	a := r.dial("tok-a", 1)
	r.send(a, &knight.Envelope{Payload: &knight.Envelope_CreateRoomReq{
		CreateRoomReq: &knight.CreateRoomReq{Password: "pass-123"}}})
	r.pumpUntil(func() bool { return r.nextRoomState(a) != nil }, "room created")
	roomID := r.nextRoomState(a).RoomId

	// B 无密码加入 → ERR_ROOM_PASSWORD_MISMATCH
	b := r.dial("tok-b", 2)
	r.send(b, &knight.Envelope{Payload: &knight.Envelope_JoinRoomReq{
		JoinRoomReq: &knight.JoinRoomReq{RoomId: roomID}}})
	if e := r.takeNext(b).GetError(); e == nil || e.Code != knight.ErrorCode_ERR_ROOM_PASSWORD_MISMATCH {
		t.Fatalf("expected ERR_ROOM_PASSWORD_MISMATCH (empty password), got %v", e)
	}

	// B 错误密码 → ERR_ROOM_PASSWORD_MISMATCH
	r.send(b, &knight.Envelope{Payload: &knight.Envelope_JoinRoomReq{
		JoinRoomReq: &knight.JoinRoomReq{RoomId: roomID, Password: "wrong"}}})
	if e := r.takeNext(b).GetError(); e == nil || e.Code != knight.ErrorCode_ERR_ROOM_PASSWORD_MISMATCH {
		t.Fatalf("expected ERR_ROOM_PASSWORD_MISMATCH (wrong password), got %v", e)
	}

	// B 正确密码 → 加入成功，双方看到 2 成员
	r.send(b, &knight.Envelope{Payload: &knight.Envelope_JoinRoomReq{
		JoinRoomReq: &knight.JoinRoomReq{RoomId: roomID, Password: "pass-123"}}})
	r.pumpUntil(func() bool {
		return r.nextRoomState(a) != nil && len(r.nextRoomState(a).Members) == 2 &&
			r.nextRoomState(b) != nil && len(r.nextRoomState(b).Members) == 2
	}, "B joined with password")

	// C 创建公开房间（无密码），D 携带任意密码也能加入（password 字段被忽略）
	c := r.dial("tok-c", 3)
	r.send(c, &knight.Envelope{Payload: &knight.Envelope_CreateRoomReq{
		CreateRoomReq: &knight.CreateRoomReq{}}})
	r.pumpUntil(func() bool { return r.nextRoomState(c) != nil }, "public room created")
	publicID := r.nextRoomState(c).RoomId

	d := r.dial("tok-d", 4)
	r.send(d, &knight.Envelope{Payload: &knight.Envelope_JoinRoomReq{
		JoinRoomReq: &knight.JoinRoomReq{RoomId: publicID, Password: "whatever"}}})
	r.pumpUntil(func() bool {
		return r.nextRoomState(c) != nil && len(r.nextRoomState(c).Members) == 2
	}, "D joined public room with arbitrary password")
}

func TestLobbyGetRoomList(t *testing.T) {
	r := newLobbyRig(t, 2)

	// A 建公开房；B 建加密房；C 停在大厅
	a := r.dial("tok-a", 1)
	r.send(a, &knight.Envelope{Payload: &knight.Envelope_CreateRoomReq{
		CreateRoomReq: &knight.CreateRoomReq{}}})
	r.pumpUntil(func() bool { return r.nextRoomState(a) != nil }, "room A created")
	roomA := r.nextRoomState(a).RoomId

	b := r.dial("tok-b", 2)
	r.send(b, &knight.Envelope{Payload: &knight.Envelope_CreateRoomReq{
		CreateRoomReq: &knight.CreateRoomReq{Password: "secret"}}})
	r.pumpUntil(func() bool { return r.nextRoomState(b) != nil }, "room B created")
	roomB := r.nextRoomState(b).RoomId

	c := r.dial("tok-c", 3)
	r.send(c, &knight.Envelope{Payload: &knight.Envelope_GetRoomListReq{
		GetRoomListReq: &knight.GetRoomListReq{}}})
	resp := r.takeNext(c).GetGetRoomListResp()
	if resp == nil {
		t.Fatalf("expected GetRoomListResp, got %T", r.inbox[c][0].Payload)
	}
	if len(resp.Rooms) != 2 {
		t.Fatalf("rooms = %d, want 2", len(resp.Rooms))
	}
	// 房间号升序
	if resp.Rooms[0].RoomId >= resp.Rooms[1].RoomId {
		t.Fatalf("rooms not sorted ascending: %v, %v", resp.Rooms[0], resp.Rooms[1])
	}

	byID := map[uint32]*knight.RoomSummary{
		resp.Rooms[0].RoomId: resp.Rooms[0],
		resp.Rooms[1].RoomId: resp.Rooms[1],
	}
	sa, ok := byID[roomA]
	if !ok {
		t.Fatalf("room %d missing from list", roomA)
	}
	if sa.HasPassword || sa.PlayerCount != 1 || sa.State != knight.RoomSessionState_ROOM_WAITING {
		t.Fatalf("room A summary = %+v", sa)
	}
	sb, ok := byID[roomB]
	if !ok {
		t.Fatalf("room %d missing from list", roomB)
	}
	if !sb.HasPassword || sb.PlayerCount != 1 || sb.State != knight.RoomSessionState_ROOM_WAITING {
		t.Fatalf("room B summary = %+v", sb)
	}
}
