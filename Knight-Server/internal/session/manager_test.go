package session_test

import (
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"knight-server/internal/event"
	"knight-server/internal/kcp2k"

	"knight-server/internal/protocol"
	"knight-server/internal/session"

	knight "knight-server/protogen"
)

// rig 会话层集成测试环境：真实 UDP 上的 kcp2k.Client ↔ gate ↔ Manager。
type rig struct {
	t    *testing.T
	gate *kcp2k.Server
	mgr  *session.Manager
	cli  *kcp2k.Client
	now  uint32

	ready   []readyEvent
	ended   []endEvent
	cliData []*knight.Envelope
	cliDisc bool
}

type readyEvent struct {
	uid    uint64
	connID string
}

type endEvent struct {
	uid    uint64
	connID string
	reason string
}

func newRig(t *testing.T, tokens session.TokenValidator, cfg session.Config) *rig {
	t.Helper()
	if cfg.HeartbeatTimeout == 0 {
		cfg.HeartbeatTimeout = 10 * time.Second
	}
	if cfg.DisconnectDelay == 0 {
		cfg.DisconnectDelay = 5 * time.Millisecond // 测试用短延迟
	}
	if cfg.ProtoVersion == 0 {
		cfg.ProtoVersion = 5
	}
	if cfg.SessionLimit == 0 {
		cfg.SessionLimit = 256
	}

	r := &rig{t: t}
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

	// 生命周期事件经总线观测（处理器与 gate 回调同 goroutine，无并发）
	bus := event.NewBus()
	event.Subscribe(bus, event.SessionReady, func(a event.SessionReadyArgs) {
		r.ready = append(r.ready, readyEvent{a.UID, a.ConnID})
	})
	event.Subscribe(bus, event.SessionEnd, func(a event.SessionEndArgs) {
		r.ended = append(r.ended, endEvent{a.UID, a.ConnID, a.Reason})
	})
	mgr = session.NewManager(cfg, tokens, gate, bus)
	r.mgr = mgr
	return r
}

func (r *rig) dial() {
	t := r.t
	cli, err := kcp2k.Dial(kcp2k.DefaultConfig(), r.gate.LocalAddr().String(), kcp2k.ClientCallbacks{
		OnData: func(message []byte, _ kcp2k.Channel) {
			env, err := protocol.Decode(message)
			if err != nil {
				t.Fatalf("client decode: %v", err)
			}
			r.cliData = append(r.cliData, env)
		},
		OnDisconnected: func() { r.cliDisc = true },
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(cli.Disconnect)
	r.cli = cli
}

func (r *rig) pump(steps int) {
	for i := 0; i < steps; i++ {
		r.now += 10
		r.cli.TickIncoming(r.now)
		r.cli.TickOutgoing(r.now)
		r.gate.Step(r.now)
		// 微睡眠：让两侧读 goroutine 有机会把内核缓冲的数据报推入 channel
		time.Sleep(200 * time.Microsecond)
	}
}

func (r *rig) pumpUntil(cond func() bool, desc string) {
	r.t.Helper()
	for i := 0; i < 2000; i++ {
		if cond() {
			return
		}
		r.pump(1)
	}
	r.t.Fatalf("timeout waiting for %s", desc)
}

// sendHandshakeReq 发送握手请求（指定版本与 token）。
// 先泵到 kcp2k bare 握手完成——与真实客户端一致：HandshakeReq 在 kcp2k
// 通道建立后才可发送（NetworkClient.OnKcpConnected）。
func (r *rig) sendHandshakeReq(version uint32, token string) {
	r.pumpUntil(func() bool { return r.cli.State() == kcp2k.PeerAuthenticated }, "kcp2k handshake")
	data, err := protocol.Encode(&knight.Envelope{Payload: &knight.Envelope_HandshakeReq{
		HandshakeReq: &knight.HandshakeReq{ProtoVersion: version, Token: token},
	}})
	if err != nil {
		r.t.Fatal(err)
	}
	r.cli.Send(data, kcp2k.ChannelReliable)
}

// awaitHandshakeAck 泵到收到 HandshakeAck 并消费之（同时隐含 ready 事件已发）。
func (r *rig) awaitHandshakeAck() *knight.HandshakeAck {
	r.t.Helper()
	r.pumpUntil(func() bool { return len(r.cliData) > 0 }, "handshake ack")
	env := r.cliData[0]
	r.cliData = r.cliData[1:]
	ack := env.GetHandshakeAck()
	if ack == nil {
		r.t.Fatalf("expected HandshakeAck, got %T", env.Payload)
	}
	return ack
}

// expectError 取下一条客户端收到的 Error 消息。
func (r *rig) expectError() *knight.Error {
	r.t.Helper()
	r.pumpUntil(func() bool { return len(r.cliData) > 0 }, "error reply")
	env := r.cliData[0]
	r.cliData = r.cliData[1:]
	e := env.GetError()
	if e == nil {
		r.t.Fatalf("expected Error, got %T", env.Payload)
	}
	return e
}

// mapValidator 固定 token→uid 映射的测试校验器。
type mapValidator map[string]uint64

func (m mapValidator) Validate(token string) (uint64, string, bool) {
	uid, ok := m[token]
	if !ok {
		return 0, "", false
	}
	return uid, "user-" + token, true
}

func TestHandshakeSuccess(t *testing.T) {
	r := newRig(t, mapValidator{"tok-a": 1}, session.Config{})
	r.dial()
	r.sendHandshakeReq(5, "tok-a")

	ack := r.awaitHandshakeAck()
	if ack.Uid != 1 || ack.Name != "user-tok-a" {
		t.Fatalf("ack = uid:%d name:%q", ack.Uid, ack.Name)
	}
	if len(r.ready) != 1 || r.ready[0].uid != 1 {
		t.Fatalf("ready events = %+v", r.ready)
	}
	if r.mgr.SessionCount() != 1 {
		t.Fatalf("session count = %d", r.mgr.SessionCount())
	}
}

func TestHandshakeVersionMismatch(t *testing.T) {
	r := newRig(t, mapValidator{"tok-a": 1}, session.Config{})
	r.dial()
	r.sendHandshakeReq(2, "tok-a") // 服务端期望 5（版本不一致）

	e := r.expectError()
	if e.Code != knight.ErrorCode_ERR_VERSION_MISMATCH {
		t.Fatalf("code = %v", e.Code)
	}
	r.pumpUntil(func() bool { return r.cliDisc && len(r.ended) == 1 }, "disconnect")
	if r.ended[0].reason != event.SessionEndRejected {
		t.Fatalf("end reason = %q, want %q", r.ended[0].reason, event.SessionEndRejected)
	}
}

func TestHandshakeInvalidToken(t *testing.T) {
	r := newRig(t, mapValidator{"tok-a": 1}, session.Config{})
	r.dial()
	r.sendHandshakeReq(5, "wrong-token")

	e := r.expectError()
	if e.Code != knight.ErrorCode_ERR_INVALID_TOKEN {
		t.Fatalf("code = %v", e.Code)
	}
	r.pumpUntil(func() bool { return r.cliDisc }, "disconnect")
}

func TestHandshakeAlreadyOnline(t *testing.T) {
	r := newRig(t, mapValidator{"tok-a": 1}, session.Config{})

	// 第一个客户端正常握手
	r.dial()
	r.sendHandshakeReq(5, "tok-a")
	r.awaitHandshakeAck()

	// 第二个客户端同 uid：拒绝新连接，旧会话保留
	r.dial()
	r.sendHandshakeReq(5, "tok-a")
	e := r.expectError()
	if e.Code != knight.ErrorCode_ERR_ALREADY_ONLINE {
		t.Fatalf("code = %v", e.Code)
	}
	r.pumpUntil(func() bool { return r.mgr.SessionCount() == 1 }, "old session kept, new conn closed")
	if len(r.ended) != 1 || r.ended[0].connID == r.ready[0].connID {
		t.Fatalf("ended events should only contain the new conn: %+v vs ready %+v", r.ended, r.ready)
	}
	if r.ended[0].reason != event.SessionEndRejected || r.ended[0].uid != 0 {
		t.Fatalf("rejected conn should end with uid=0 reason=rejected: %+v", r.ended[0])
	}
}

func TestHandshakeServerFull(t *testing.T) {
	r := newRig(t, mapValidator{"tok-a": 1, "tok-b": 2}, session.Config{SessionLimit: 1})

	r.dial()
	r.sendHandshakeReq(5, "tok-a")
	r.awaitHandshakeAck()

	// 第二个客户端（不同 uid）：会话数达上限
	r.dial()
	r.sendHandshakeReq(5, "tok-b")
	e := r.expectError()
	if e.Code != knight.ErrorCode_ERR_SERVER_FULL {
		t.Fatalf("code = %v", e.Code)
	}
}

func TestPreHandshakeMessageRejected(t *testing.T) {
	r := newRig(t, mapValidator{"tok-a": 1}, session.Config{})
	r.dial()

	// 应用层握手前发心跳（kcp2k 通道已建立）：ERR_INVALID_REQUEST + 关闭
	r.pumpUntil(func() bool { return r.cli.State() == kcp2k.PeerAuthenticated }, "kcp2k handshake")
	data, _ := protocol.Encode(&knight.Envelope{Payload: &knight.Envelope_Heartbeat{Heartbeat: &knight.Heartbeat{}}})
	r.cli.Send(data, kcp2k.ChannelReliable)

	e := r.expectError()
	if e.Code != knight.ErrorCode_ERR_INVALID_REQUEST {
		t.Fatalf("code = %v", e.Code)
	}
	r.pumpUntil(func() bool { return r.cliDisc }, "disconnect")
}

func TestBusinessMessageNotImplementedKeepsConnection(t *testing.T) {
	r := newRig(t, mapValidator{"tok-a": 1}, session.Config{})
	r.dial()
	r.sendHandshakeReq(5, "tok-a")
	r.awaitHandshakeAck()
	r.cliData = nil

	// 房间消息本轮未实现：ERR_INVALID_REQUEST 应答但不断开
	data, _ := protocol.Encode(&knight.Envelope{Payload: &knight.Envelope_CreateRoomReq{CreateRoomReq: &knight.CreateRoomReq{}}})
	r.cli.Send(data, kcp2k.ChannelReliable)

	e := r.expectError()
	if e.Code != knight.ErrorCode_ERR_INVALID_REQUEST {
		t.Fatalf("code = %v", e.Code)
	}

	// 连接仍可用：心跳后无新错误
	r.cliData = nil
	hb, _ := protocol.Encode(&knight.Envelope{Payload: &knight.Envelope_Heartbeat{Heartbeat: &knight.Heartbeat{}}})
	r.cli.Send(hb, kcp2k.ChannelReliable)
	r.pump(5)
	if len(r.cliData) != 0 {
		t.Fatalf("heartbeat should not be answered, got %+v", r.cliData)
	}
}

func TestHeartbeatTimeoutKicksSession(t *testing.T) {
	r := newRig(t, mapValidator{"tok-a": 1}, session.Config{HeartbeatTimeout: 100 * time.Millisecond})
	r.dial()
	r.sendHandshakeReq(5, "tok-a")
	r.awaitHandshakeAck()
	r.cliData = nil

	// 真实时钟推进 150ms 后巡检踢出
	time.Sleep(150 * time.Millisecond)
	r.mgr.CheckTimeouts()

	e := r.expectError()
	if e.Code != knight.ErrorCode_ERR_KICKED_TIMEOUT {
		t.Fatalf("code = %v", e.Code)
	}
	r.pumpUntil(func() bool { return r.cliDisc && len(r.ended) == 1 }, "disconnect + session end")
	if r.ended[0].reason != event.SessionEndTimeout {
		t.Fatalf("end reason = %q, want %q", r.ended[0].reason, event.SessionEndTimeout)
	}
	if r.mgr.SessionCount() != 0 {
		t.Fatalf("session count = %d, want 0", r.mgr.SessionCount())
	}
}

func TestSendToUID(t *testing.T) {
	r := newRig(t, mapValidator{"tok-a": 1}, session.Config{})
	r.dial()
	r.sendHandshakeReq(5, "tok-a")
	r.awaitHandshakeAck()
	r.cliData = nil

	// 在线：发送直达该 uid 的会话
	data, err := protocol.Encode(&knight.Envelope{Payload: &knight.Envelope_Heartbeat{Heartbeat: &knight.Heartbeat{}}})
	if err != nil {
		t.Fatal(err)
	}
	if !r.mgr.SendToUID(1, data, kcp2k.ChannelReliable) {
		t.Fatal("在线 uid 应返回 true")
	}
	r.pumpUntil(func() bool { return len(r.cliData) > 0 }, "sent message")
	if got := r.cliData[0]; got.GetHeartbeat() == nil {
		t.Fatalf("expected Heartbeat, got %T", got.Payload)
	}

	// 离线：返回 false，不发送
	r.cliData = nil
	if r.mgr.SendToUID(999, data, kcp2k.ChannelReliable) {
		t.Fatal("离线 uid 应返回 false")
	}
	r.pump(5)
	if len(r.cliData) != 0 {
		t.Fatalf("offline uid should not receive, got %+v", r.cliData)
	}
}

func TestMsgIDPayloadMismatchRejected(t *testing.T) {
	r := newRig(t, mapValidator{"tok-a": 1}, session.Config{})
	r.dial()

	// 直接构造不一致的 Envelope（心跳载荷 + ERROR 的 msg_id）
	r.pumpUntil(func() bool { return r.cli.State() == kcp2k.PeerAuthenticated }, "kcp2k handshake")
	mismatched, _ := proto.Marshal(&knight.Envelope{
		MsgId:   knight.MsgId_MSG_ERROR,
		Payload: &knight.Envelope_Heartbeat{Heartbeat: &knight.Heartbeat{}},
	})
	r.cli.Send(mismatched, kcp2k.ChannelReliable)

	e := r.expectError()
	if e.Code != knight.ErrorCode_ERR_INVALID_REQUEST {
		t.Fatalf("code = %v", e.Code)
	}
	r.pumpUntil(func() bool { return r.cliDisc }, "disconnect")
}
