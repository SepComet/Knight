package kcp2k

import (
	"bytes"
	"fmt"
	"testing"
	"time"
)

// fixture 真实 UDP 套接字上的 client↔server 测试环境，虚拟时钟手动泵驱动。
type fixture struct {
	t   *testing.T
	srv *Server
	cli *Client
	now uint32

	srvConnected    []string
	srvDisconnected []string
	srvData         []dataEvent
	srvErrors       []string

	cliConnected    bool
	cliDisconnected bool
	cliData         []dataEvent
	cliErrors       []string
}

type dataEvent struct {
	data    []byte
	channel Channel
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	cfg := DefaultConfig()
	cfg.FastResend = 2 // 游戏协议参数

	f := &fixture{t: t}
	srv, err := Listen("127.0.0.1:0", cfg, ServerCallbacks{
		OnConnect: func(id string) { f.srvConnected = append(f.srvConnected, id) },
		OnData: func(id string, message []byte, channel Channel) {
			f.srvData = append(f.srvData, dataEvent{append([]byte(nil), message...), channel})
		},
		OnDisconnect: func(id string) { f.srvDisconnected = append(f.srvDisconnected, id) },
		OnError: func(id string, code ErrorCode, msg string) {
			f.srvErrors = append(f.srvErrors, fmt.Sprintf("%s:%s", code, msg))
		},
	})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(srv.Stop)
	f.srv = srv

	cli, err := Dial(cfg, srv.LocalAddr().String(), ClientCallbacks{
		OnConnected: func() { f.cliConnected = true },
		OnData: func(message []byte, channel Channel) {
			f.cliData = append(f.cliData, dataEvent{append([]byte(nil), message...), channel})
		},
		OnDisconnected: func() { f.cliDisconnected = true },
		OnError:        func(code ErrorCode, msg string) { f.cliErrors = append(f.cliErrors, fmt.Sprintf("%s:%s", code, msg)) },
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { cli.Disconnect() })
	f.cli = cli
	return f
}

// pump 每步推进 10ms：客户端入/出各 tick 一次，服务端 Step 一次。
func (f *fixture) pump(steps int) {
	for i := 0; i < steps; i++ {
		f.now += 10
		f.cli.TickIncoming(f.now)
		f.cli.TickOutgoing(f.now)
		f.srv.Step(f.now)
	}
}

// pumpUntil 重复泵直到条件满足或超时。
func (f *fixture) pumpUntil(cond func() bool, desc string) {
	f.t.Helper()
	for i := 0; i < 2000; i++ {
		if cond() {
			return
		}
		f.pump(1)
	}
	f.t.Fatalf("timeout waiting for %s", desc)
}

func TestInteropHandshake(t *testing.T) {
	f := newFixture(t)
	f.pumpUntil(func() bool { return f.cliConnected && len(f.srvConnected) == 1 }, "handshake")

	if len(f.srvConnected) != 1 {
		t.Fatalf("server connect events = %d", len(f.srvConnected))
	}
	if f.srvConnected[0] != f.srv.conns[f.srvConnected[0]].id {
		t.Fatal("conn id mismatch")
	}
	if f.cli.peer.cookie == 0 {
		t.Fatal("client did not adopt server cookie")
	}
}

func TestInteropReliableBothDirections(t *testing.T) {
	f := newFixture(t)
	f.pumpUntil(func() bool { return f.cliConnected }, "handshake")

	// 客户端 → 服务端（小消息）
	f.cli.Send([]byte("ping-from-client"), ChannelReliable)
	f.pumpUntil(func() bool { return len(f.srvData) > 0 }, "client data")
	if got := string(f.srvData[0].data); got != "ping-from-client" || f.srvData[0].channel != ChannelReliable {
		t.Fatalf("server got %q ch=%v", got, f.srvData[0].channel)
	}

	// 服务端 → 客户端
	srvID := f.srvConnected[0]
	f.srv.Send(srvID, []byte("pong-from-server"), ChannelReliable)
	f.pumpUntil(func() bool { return len(f.cliData) > 0 }, "server data")
	if got := string(f.cliData[0].data); got != "pong-from-server" {
		t.Fatalf("client got %q", got)
	}
}

func TestInteropLargeReliableMessage(t *testing.T) {
	f := newFixture(t)
	f.pumpUntil(func() bool { return f.cliConnected }, "handshake")

	// 接近可靠通道上限的大消息（127 分片，约 148KB），验证分片/重组全链路
	msg := make([]byte, ReliableMaxMessageSize(1200, 128))
	for i := range msg {
		msg[i] = byte(i * 7 % 256)
	}
	f.cli.Send(msg, ChannelReliable)
	f.pumpUntil(func() bool { return len(f.srvData) > 0 }, "large message")

	got := f.srvData[0].data
	if len(got) != len(msg) {
		t.Fatalf("server got %d bytes, want %d", len(got), len(msg))
	}
	if !bytes.Equal(got, msg) {
		t.Fatal("large message payload mismatch")
	}
}

func TestInteropUnreliable(t *testing.T) {
	f := newFixture(t)
	f.pumpUntil(func() bool { return f.cliConnected }, "handshake")

	f.cli.Send([]byte("unreliable-payload"), ChannelUnreliable)
	f.pumpUntil(func() bool { return len(f.srvData) > 0 }, "unreliable data")
	if f.srvData[0].channel != ChannelUnreliable || string(f.srvData[0].data) != "unreliable-payload" {
		t.Fatalf("unexpected unreliable event: %+v", f.srvData[0])
	}
}

func TestInteropClientDisconnectNotifiesServer(t *testing.T) {
	f := newFixture(t)
	f.pumpUntil(func() bool { return f.cliConnected }, "handshake")

	f.cli.Disconnect()
	f.pumpUntil(func() bool { return len(f.srvDisconnected) == 1 }, "server disconnect event")

	if f.srv.ConnCount() != 0 {
		t.Fatalf("conn count after disconnect = %d, want 0", f.srv.ConnCount())
	}
}

func TestInteropServerDisconnectNotifiesClient(t *testing.T) {
	f := newFixture(t)
	f.pumpUntil(func() bool { return f.cliConnected }, "handshake")

	f.srv.Disconnect(f.srvConnected[0])
	f.pumpUntil(func() bool { return f.cliDisconnected }, "client disconnect event")
}

func TestInteropServerTimeoutKicksSilentClient(t *testing.T) {
	f := newFixture(t)
	f.pumpUntil(func() bool { return f.cliConnected }, "handshake")

	// 客户端停摆（不再收发），仅泵服务端；10s 无入包应被踢出
	start := f.now
	for i := 0; i < 1200 && len(f.srvDisconnected) == 0; i++ {
		f.now += 10
		f.srv.Step(f.now)
	}
	if len(f.srvDisconnected) != 1 {
		t.Fatal("silent client was not kicked")
	}
	elapsed := f.now - start
	// 严格时刻取决于最后一个入包（握手期间）相对 start 的偏移，断言量级即可
	if elapsed < 9000 || elapsed > 11000 {
		t.Fatalf("kicked after %d ms, want ~10000", elapsed)
	}
	// 传输层超时错误应先于断开被上报
	if len(f.srvErrors) == 0 || f.srvErrors[0][:7] != "Timeout" {
		t.Fatalf("expected Timeout error, got %v", f.srvErrors)
	}
}

func TestInteropCookieEnforcement(t *testing.T) {
	f := newFixture(t)
	f.pumpUntil(func() bool { return f.cliConnected }, "handshake")

	// 用客户端自己的套接字发一个 cookie 不匹配的包：应被服务端丢弃
	bad := []byte{byte(ChannelReliable), 0xAA, 0xBB, 0xCC, 0xDD, 0xFF}
	if _, err := f.cli.conn.WriteToUDP(bad, f.cli.remote); err != nil {
		t.Fatalf("write: %v", err)
	}
	f.pump(5)

	if len(f.srvData) != 0 {
		t.Fatalf("server accepted packet with wrong cookie: %+v", f.srvData)
	}
	// 连接仍存活
	if f.srv.ConnCount() != 1 {
		t.Fatalf("conn count = %d, want 1", f.srv.ConnCount())
	}
}

func TestInteropPingPongRTT(t *testing.T) {
	f := newFixture(t)
	f.pumpUntil(func() bool { return f.cliConnected }, "handshake")

	// kcp2k 每秒互发 Ping/Pong；首个 Pong 可能被对端限频挡回，泵到测得 RTT 为止。
	// 虚拟时钟每步推进 10ms，而真实 UDP 环回由独立 readLoop goroutine 异步收包；
	// 若无短暂让出，高速 CPU（如 macOS）会在内核投递单次 Ping/Pong 期间空转上百步，
	// 使虚拟时间虚增数秒。每步短暂休眠以确保 UDP 环回收发跟上虚拟时钟。
	for i := 0; i < 2000 && f.cli.RTT() == 0; i++ {
		f.pump(1)
		time.Sleep(time.Millisecond)
	}
	if f.cli.RTT() == 0 {
		t.Fatal("timeout waiting for client RTT measurement")
	}

	// 环回 + 虚拟时钟下 RTT 通常为几十 ms；KCP 重传会刷新段 ts 导致偶发偏大，
	// 只断言量级合理（远小于 10s 超时）
	if rtt := f.cli.RTT(); rtt > 600 {
		t.Fatalf("rtt = %d ms, unexpectedly large", rtt)
	}
}

func TestInteropSecondHelloAfterAuthDisconnects(t *testing.T) {
	f := newFixture(t)
	f.pumpUntil(func() bool { return f.cliConnected }, "handshake")

	// 认证后再发一条 Hello（KCP 可靠通道，需经 kcp.Send）
	f.cli.peer.sendReliable(HeaderHello, nil)
	f.pumpUntil(func() bool { return len(f.srvDisconnected) == 1 }, "server kicked second hello")
}
