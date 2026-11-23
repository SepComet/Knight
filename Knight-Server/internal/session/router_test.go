package session_test

import (
	"testing"

	"knight-server/internal/kcp2k"
	"knight-server/internal/protocol"
	"knight-server/internal/session"

	knight "knight-server/protogen"
)

// fakeSender 记录发送（无需真实 UDP）。
type fakeSender struct {
	sent []sentMsg
}

type sentMsg struct {
	connID  string
	data    []byte
	channel kcp2k.Channel
}

func (s *fakeSender) Send(connID string, data []byte, ch kcp2k.Channel) {
	s.sent = append(s.sent, sentMsg{connID, data, ch})
}

// recRouter 记录被路由的消息。
type recRouter struct {
	routed []*knight.Envelope
	uid    uint64
	name   string
	connID string
}

func (r *recRouter) Route(uid uint64, name, connID string, env *knight.Envelope) {
	r.routed = append(r.routed, env)
	r.uid, r.name, r.connID = uid, name, connID
}

func TestDomainRouterDispatchesByRange(t *testing.T) {
	sender := &fakeSender{}
	lobbyRouter := &recRouter{}
	gameRouter := &recRouter{}
	router := session.NewDomainRouter(sender,
		session.DomainRoute{Min: 32, Max: 63, Router: lobbyRouter},
		session.DomainRoute{Min: 64, Max: 127, Router: gameRouter},
	)

	// 大厅域消息 → lobby Router，上下文原样透传
	env := &knight.Envelope{MsgId: knight.MsgId_MSG_CREATE_ROOM_REQ}
	router.Route(7, "alice", "conn-1", env)
	if len(lobbyRouter.routed) != 1 || lobbyRouter.routed[0] != env {
		t.Fatalf("lobby router = %+v", lobbyRouter)
	}
	if lobbyRouter.uid != 7 || lobbyRouter.name != "alice" || lobbyRouter.connID != "conn-1" {
		t.Fatalf("context = uid:%d name:%q conn:%q", lobbyRouter.uid, lobbyRouter.name, lobbyRouter.connID)
	}

	// 游戏域消息 → game Router
	env2 := &knight.Envelope{MsgId: knight.MsgId_MSG_INPUT_BATCH}
	router.Route(7, "alice", "conn-1", env2)
	if len(gameRouter.routed) != 1 || gameRouter.routed[0] != env2 {
		t.Fatalf("game router = %+v", gameRouter)
	}
	if len(lobbyRouter.routed) != 1 || len(sender.sent) != 0 {
		t.Fatalf("不应串域或产生兜底应答: lobby=%d sent=%d", len(lobbyRouter.routed), len(sender.sent))
	}
}

func TestDomainRouterFallbackForUnknownDomain(t *testing.T) {
	sender := &fakeSender{}
	lobbyRouter := &recRouter{}
	router := session.NewDomainRouter(sender,
		session.DomainRoute{Min: 32, Max: 63, Router: lobbyRouter},
	)

	// 未注册域段（64~127 游戏域尚未落地）：不路由，按 ERR_INVALID_REQUEST 应答不断开
	env := &knight.Envelope{MsgId: knight.MsgId_MSG_INPUT_BATCH}
	router.Route(7, "alice", "conn-1", env)
	if len(lobbyRouter.routed) != 0 {
		t.Fatalf("未注册域段不应路由: %+v", lobbyRouter.routed)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("sent = %d, want 1", len(sender.sent))
	}
	if sender.sent[0].connID != "conn-1" || sender.sent[0].channel != kcp2k.ChannelReliable {
		t.Fatalf("sent meta = %+v", sender.sent[0])
	}
	decoded, err := protocol.Decode(sender.sent[0].data)
	if err != nil {
		t.Fatalf("decode fallback error: %v", err)
	}
	e := decoded.GetError()
	if e == nil || e.Code != knight.ErrorCode_ERR_INVALID_REQUEST || e.Message != "not implemented yet" {
		t.Fatalf("fallback error = %+v", e)
	}
}
