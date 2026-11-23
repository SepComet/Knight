// Package session 实现连接生命周期（Docs/protocol.md §4）：
// 应用层握手（HandshakeReq/Ack）、心跳超时踢出、uid 绑定与错误应答。
// 会话生命周期经事件总线发布（Docs/server-event-pipeline.md）；
// 房间/大厅等业务消息交由 Router 处理，未设置路由时按 ERR_INVALID_REQUEST 应答（不断开）。
package session

import (
	"log"
	"sync"
	"time"

	"knight-server/internal/event"
	"knight-server/internal/kcp2k"
	"knight-server/internal/protocol"

	knight "knight-server/protogen"
)

// TokenValidator 登录令牌校验抽象（HTTP 登录接口落地前的注入点）。
type TokenValidator interface {
	// Validate 返回令牌对应的账号；无效令牌返回 ok=false。
	Validate(token string) (uid uint64, name string, ok bool)
}

// Config 会话层配置。
type Config struct {
	ProtoVersion     uint32        // 握手版本校验值（与 knight.proto 头部一致，当前 5）
	SessionLimit     int           // 同时在线会话上限
	HeartbeatTimeout time.Duration // 无任何 Envelope 的踢出时限（协议 10s）
	// DisconnectDelay 发出错误应答后延迟关闭连接的时长。
	// kcp2k 的 5 连发不可靠 Disconnect 会先于可靠错误消息被客户端处理，
	// 立即断开会让客户端永远收不到 Error；客户端收到 Error 会自行断开，
	// 此延迟只是服务端兜底清理。0 = 默认 500ms。
	DisconnectDelay time.Duration
}

// Router 业务消息路由：握手完成后的业务消息（大厅/游戏）交由此处理。
// 实现方对不认识的消息自行应答错误。设置见 SetRouter。
type Router interface {
	Route(uid uint64, name string, connID string, env *knight.Envelope)
}

type sessState uint8

const (
	awaitingHandshake sessState = iota
	established
)

type sess struct {
	connID     string
	uid        uint64
	name       string
	state      sessState
	endReason  string // SessionEnd 的 Reason；创建时为 disconnect，拒绝/超时路径覆写
	lastPacket time.Time
}

// Manager 会话管理器。生命周期事件在 gate goroutine 上发布；
// CheckTimeouts 可由任意 goroutine 周期调用——内部用互斥锁保护会话表。
type Manager struct {
	cfg    Config
	tokens TokenValidator
	gate   *kcp2k.Server
	bus    *event.Bus

	mu     sync.Mutex
	router Router

	sessions map[string]*sess  // connID → 会话
	byUID    map[uint64]string // uid → connID（同 uid 在线判定）
}

// NewManager 构造并挂接到 kcp2k gate 的回调。bus 不可为空：
// 会话生命周期事件（SessionReady/SessionEnd）经其发布，供大厅等消费方订阅。
func NewManager(cfg Config, tokens TokenValidator, gate *kcp2k.Server, bus *event.Bus) *Manager {
	m := &Manager{
		cfg:      cfg,
		tokens:   tokens,
		gate:     gate,
		bus:      bus,
		sessions: make(map[string]*sess),
		byUID:    make(map[uint64]string),
	}
	return m
}

// ---------------------------------------------------------------- kcp2k 回调（gate goroutine）

// OnKcpConnect bare 握手完成：登记待握手会话。
func (m *Manager) OnKcpConnect(connID string) {
	m.mu.Lock()
	m.sessions[connID] = &sess{connID: connID, state: awaitingHandshake, endReason: event.SessionEndDisconnect, lastPacket: time.Now()}
	m.mu.Unlock()
}

// OnKcpData 收到应用层消息（Envelope 字节）。
func (m *Manager) OnKcpData(connID string, data []byte, _ kcp2k.Channel) {
	env, err := protocol.Decode(data)
	if err != nil {
		// 协议违规：应答错误并断开
		m.reject(connID, knight.ErrorCode_ERR_INVALID_REQUEST, err.Error(), event.SessionEndRejected)
		return
	}
	m.handleEnvelope(connID, env)
}

// OnKcpDisconnect 连接断开：清理会话与 uid 索引，发布会话结束事件。
func (m *Manager) OnKcpDisconnect(connID string) {
	m.mu.Lock()
	s, ok := m.sessions[connID]
	if ok {
		delete(m.sessions, connID)
		if s.uid != 0 && m.byUID[s.uid] == connID {
			delete(m.byUID, s.uid)
		}
	}
	m.mu.Unlock()

	if ok {
		// 生命周期事件的唯一发布点：超时踢出与拒绝关闭均经 gate.Disconnect 汇入此处
		event.Publish(m.bus, event.SessionEnd, event.SessionEndArgs{
			UID:    s.uid,
			ConnID: s.connID,
			Reason: s.endReason,
		})
	}
}

// ---------------------------------------------------------------- 会话逻辑

func (m *Manager) handleEnvelope(connID string, env *knight.Envelope) {
	m.mu.Lock()
	s, ok := m.sessions[connID]
	if ok {
		s.lastPacket = time.Now()
	}
	m.mu.Unlock()
	if !ok {
		return // 断开竞态，静默丢弃
	}

	if s.state == awaitingHandshake {
		m.handleHandshake(s, env)
		return
	}
	m.handleMessage(s, env)
}

// handleHandshake 校验顺序对齐 protocol.md §4.1：
// 版本 → token → 同 uid 在线（拒绝新连接，旧会话保留）→ 会话数上限。
func (m *Manager) handleHandshake(s *sess, env *knight.Envelope) {
	req := env.GetHandshakeReq()
	if req == nil {
		// 握手前收到其他消息：ERR_INVALID_REQUEST + 关闭
		m.reject(s.connID, knight.ErrorCode_ERR_INVALID_REQUEST, "handshake required first", event.SessionEndRejected)
		return
	}

	if req.ProtoVersion != m.cfg.ProtoVersion {
		m.reject(s.connID, knight.ErrorCode_ERR_VERSION_MISMATCH, "proto version mismatch", event.SessionEndRejected)
		return
	}

	uid, name, ok := m.tokens.Validate(req.Token)
	if !ok {
		m.reject(s.connID, knight.ErrorCode_ERR_INVALID_TOKEN, "invalid token", event.SessionEndRejected)
		return
	}

	m.mu.Lock()
	if _, online := m.byUID[uid]; online {
		m.mu.Unlock()
		// 同账号已有活跃会话：拒绝新连接，旧会话保留
		m.reject(s.connID, knight.ErrorCode_ERR_ALREADY_ONLINE, "already online", event.SessionEndRejected)
		return
	}
	if len(m.byUID) >= m.cfg.SessionLimit {
		m.mu.Unlock()
		m.reject(s.connID, knight.ErrorCode_ERR_SERVER_FULL, "server full", event.SessionEndRejected)
		return
	}

	s.uid = uid
	s.name = name
	s.state = established
	m.byUID[uid] = s.connID
	m.mu.Unlock()

	// 握手成功：回 HandshakeAck{uid, name}
	if data, err := protocol.EncodeAck(&knight.HandshakeAck{Uid: uid, Name: name}); err == nil {
		m.gate.Send(s.connID, data, kcp2k.ChannelReliable)
	}

	log.Printf("[session] handshake ok uid=%d name=%q conn=%s", uid, name, s.connID)
	event.Publish(m.bus, event.SessionReady, event.SessionReadyArgs{UID: uid, Name: name, ConnID: s.connID})
}

// handleMessage 已握手会话的消息分发。
// 心跳自行处理；其余业务消息交给 Router（大厅/游戏），未设置路由时
// 按 ERR_INVALID_REQUEST 应答（不断开）。
func (m *Manager) handleMessage(s *sess, env *knight.Envelope) {
	switch env.Payload.(type) {
	case *knight.Envelope_Heartbeat:
		// 心跳：入包已重置计时器，无需应答
		return
	case *knight.Envelope_HandshakeReq:
		// 重复握手：异常但不致命，保持连接
		m.reject(s.connID, knight.ErrorCode_ERR_INVALID_REQUEST, "already handshaked", "")
		return
	default:
		if m.router != nil {
			m.router.Route(s.uid, s.name, s.connID, env)
			return
		}
		log.Printf("[session] no router, dropping message uid=%d msg_id=%v", s.uid, env.MsgId)
		m.reject(s.connID, knight.ErrorCode_ERR_INVALID_REQUEST, "not implemented yet", "")
	}
}

// SetRouter 设置业务消息路由（大厅/游戏层）；须在服务开始处理消息前调用。
func (m *Manager) SetRouter(r Router) {
	m.mu.Lock()
	m.router = r
	m.mu.Unlock()
}

// SendToUID 向 uid 的在线会话发送数据，返回 false = uid 不在线。
// 任意 goroutine 可调：session.mu 仅保护查表，发送经 gate ops 串行执行。
// 消费方（大厅/游戏域广播）经此按 uid 定向发送，不感知传输层 connID。
func (m *Manager) SendToUID(uid uint64, data []byte, channel kcp2k.Channel) bool {
	m.mu.Lock()
	connID, ok := m.byUID[uid]
	m.mu.Unlock()
	if !ok {
		return false
	}
	m.gate.Send(connID, data, channel)
	return true
}

// reject 应答 Error 消息。endReason 为空 = 业务可恢复错误，不断开；
// 非空 = 结束性拒绝：记录会话结束原因并延迟断开连接。
func (m *Manager) reject(connID string, code knight.ErrorCode, message, endReason string) {
	if endReason != "" {
		m.mu.Lock()
		if s, ok := m.sessions[connID]; ok {
			s.endReason = endReason
		}
		m.mu.Unlock()
	}
	if data, err := protocol.EncodeError(code, message); err == nil {
		m.gate.Send(connID, data, kcp2k.ChannelReliable)
	}
	if endReason != "" {
		delay := m.cfg.DisconnectDelay
		if delay == 0 {
			delay = 500 * time.Millisecond
		}
		// 延迟断开：给可靠错误消息留出送达时间（客户端收到 Error 会自行断开）
		time.AfterFunc(delay, func() {
			m.gate.Disconnect(connID)
		})
	}
}

// ---------------------------------------------------------------- 超时

// CheckTimeouts 踢出超过 HeartbeatTimeout 无任何 Envelope 的会话。
// 周期调用（建议 1s）；kcp2k 层自身的 Ping 不计入应用层活性。
func (m *Manager) CheckTimeouts() {
	deadline := time.Now().Add(-m.cfg.HeartbeatTimeout)

	m.mu.Lock()
	var kicked []string
	for connID, s := range m.sessions {
		if s.lastPacket.Before(deadline) {
			kicked = append(kicked, connID)
		}
	}
	m.mu.Unlock()

	for _, connID := range kicked {
		log.Printf("[session] heartbeat timeout, kicking conn=%s", connID)
		m.reject(connID, knight.ErrorCode_ERR_KICKED_TIMEOUT, "heartbeat timeout", event.SessionEndTimeout)
	}
}

// SessionCount 当前会话数（诊断用）。
func (m *Manager) SessionCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sessions)
}
