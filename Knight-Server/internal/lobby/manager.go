// Package lobby 实现房间大厅（Docs/protocol.md §5.2，消息域 32~63）：
// 创建/按号加入/退出/准备/房主开局。成员任何变化全量广播 RoomStatePush。
// 房间状态变化经事件总线发布（Docs/server-event-pipeline.md），供战绩/日志等
// 内部消费方订阅；客户端状态同步仍走 RoomStatePush 全量广播，两条管线互不干扰。
// 房间校验规则（协议契约）：
//   - 单房最多 MaxPlayers 人；开局后禁止加入
//   - 房主才有开始权限，人数 ≥ MinPlayers 才能开局
//   - 房主退出 → 最早加入者继任；房间空 → 销毁
//   - 对局中（RUNNING）成员离开：本阶段按退房广播处理，PlayerLeft 语义随游戏轮次落地
package lobby

import (
	"log"
	"sort"
	"sync"

	"knight-server/internal/event"
	"knight-server/internal/kcp2k"
	"knight-server/internal/protocol"

	knight "knight-server/protogen"
)

// Config 大厅配置。
type Config struct {
	MaxPlayers int // 单房人数上限（协议 4）
	MinPlayers int // 开局最少人数（协议默认 2，开发期可配 1）
	MaxRooms   int // 同时存在房间数上限
}

// maxRoomPasswordLen 房间密码长度上限（knight.proto CreateRoomReq 注释）。
const maxRoomPasswordLen = 32

// Member 房间成员。ConnID 由 session 层管理，广播按 uid 定向发送（UIDSender）。
type Member struct {
	UID   uint64
	Name  string
	Ready bool
}

// Room 游戏房间（大厅阶段）。
type Room struct {
	ID       uint32
	State    knight.RoomSessionState // MVP 只用 WAITING / RUNNING / CLOSED
	password string                  // 可选房间密码；空 = 公开房间。不进广播，成员列表不泄露
	members  []*Member               // 按加入顺序；members[0] 为当前房主
}

// owner 当前房主。
func (r *Room) owner() *Member { return r.members[0] }

// find 按 uid 找成员。
func (r *Room) find(uid uint64) *Member {
	for _, m := range r.members {
		if m.UID == uid {
			return m
		}
	}
	return nil
}

// remove 移除成员；若房主离开则由最早加入者继任。返回被移除者。
func (r *Room) remove(uid uint64) *Member {
	for i, m := range r.members {
		if m.UID == uid {
			r.members = append(r.members[:i], r.members[i+1:]...)
			return m
		}
	}
	return nil
}

// full 是否满员。
func (r *Room) full(max int) bool { return len(r.members) >= max }

// pushRoomState 组 RoomStatePush 消息（全量快照式）。
func pushRoomState(r *Room) *knight.Envelope {
	members := make([]*knight.RoomMember, 0, len(r.members))
	for _, m := range r.members {
		members = append(members, &knight.RoomMember{Uid: m.UID, Name: m.Name, Ready: m.Ready})
	}
	return &knight.Envelope{Payload: &knight.Envelope_RoomStatePush{RoomStatePush: &knight.RoomStatePush{
		RoomId:   r.ID,
		State:    r.State,
		OwnerUid: r.owner().UID,
		Members:  members,
	}}}
}

// Sender 发送抽象（kcp2k.Server 满足）；用于连接域的请求应答（replyError）。
type Sender interface {
	Send(connID string, data []byte, channel kcp2k.Channel)
}

// UIDSender 按 uid 定向发送（session.Manager 满足）；用于用户域广播。
// 接口定义在使用方（Go 惯例），实现方不感知本包。
type UIDSender interface {
	SendToUID(uid uint64, data []byte, channel kcp2k.Channel) bool
}

// Manager 大厅管理器。Route 由 session 层调用（gate goroutine），
// SessionEnd 事件触发断线退房——命令与事件处理器同在 gate goroutine 上串行执行，
// 房间表无并发写；互斥锁仅为诊断读接口（RoomCount）兜底。
type Manager struct {
	cfg       Config
	gate      Sender    // 连接域：请求应答按发起连接定向
	uidSender UIDSender // 用户域：广播按 uid 定向
	bus       *event.Bus

	mu         sync.Mutex
	rooms      map[uint32]*Room
	roomOf     map[uint64]uint32 // uid → 房间号
	nextRoomID uint32
}

// NewManager 构造并订阅 SessionEnd（断线/超时 → 在房间内按退房处理）。
// 订阅随进程常驻（对齐客户端 NetworkEventBridge 约定），不退订。
func NewManager(cfg Config, gate Sender, uidSender UIDSender, bus *event.Bus) *Manager {
	m := &Manager{
		cfg:        cfg,
		gate:       gate,
		uidSender:  uidSender,
		bus:        bus,
		rooms:      make(map[uint32]*Room),
		roomOf:     make(map[uint64]uint32),
		nextRoomID: 1,
	}
	event.Subscribe(bus, event.SessionEnd, func(args event.SessionEndArgs) {
		m.onSessionEnd(args.UID)
	})
	return m
}

// RoomCount 当前房间数（诊断用）。
func (m *Manager) RoomCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.rooms)
}

// ---------------------------------------------------------------- 消息入口

// Route 实现 session.Router：分发大厅消息，未知消息按 ERR_INVALID_REQUEST 应答。
// 各 handler 在持锁状态下收集待发布事件（返回闭包），Route 在锁外统一发布——
// 事件处理器同步执行，持锁发布会让回调大厅方法的处理器死锁。
func (m *Manager) Route(uid uint64, name string, connID string, env *knight.Envelope) {
	var pending []func()
	m.mu.Lock()
	switch env.Payload.(type) {
	case *knight.Envelope_CreateRoomReq:
		pending = m.handleCreate(uid, name, connID, env.GetCreateRoomReq())
	case *knight.Envelope_JoinRoomReq:
		pending = m.handleJoin(uid, name, connID, env.GetJoinRoomReq())
	case *knight.Envelope_LeaveRoom:
		pending = m.handleLeave(uid)
	case *knight.Envelope_SetReady:
		pending = m.handleSetReady(uid, connID, env.GetSetReady())
	case *knight.Envelope_StartGame:
		pending = m.handleStart(uid, connID)
	case *knight.Envelope_GetRoomListReq:
		pending = m.handleGetRoomList(connID)
	default:
		// 64~127 游戏内消息：下一轮实现
		m.replyError(connID, knight.ErrorCode_ERR_INVALID_REQUEST, "not implemented yet")
	}
	m.mu.Unlock()
	for _, p := range pending {
		p()
	}
}

// onSessionEnd 会话结束（断线/超时）：在房间内按退房处理并广播。
func (m *Manager) onSessionEnd(uid uint64) {
	m.mu.Lock()
	pending := m.leave(uid, event.LeaveDisconnect)
	m.mu.Unlock()
	for _, p := range pending {
		p()
	}
}

// ---------------------------------------------------------------- 房间操作

func (m *Manager) handleCreate(uid uint64, name, connID string, req *knight.CreateRoomReq) []func() {
	if _, inRoom := m.roomOf[uid]; inRoom {
		m.replyError(connID, knight.ErrorCode_ERR_INVALID_REQUEST, "already in a room")
		return nil
	}
	if len(m.rooms) >= m.cfg.MaxRooms {
		m.replyError(connID, knight.ErrorCode_ERR_SERVER_FULL, "too many rooms")
		return nil
	}
	// 密码可选：空 = 公开房间；非空最长 32 字符
	if len(req.GetPassword()) > maxRoomPasswordLen {
		m.replyError(connID, knight.ErrorCode_ERR_INVALID_REQUEST, "password too long")
		return nil
	}

	room := &Room{ID: m.nextRoomID, State: knight.RoomSessionState_ROOM_WAITING, password: req.GetPassword()}
	m.nextRoomID++
	room.members = append(room.members, &Member{UID: uid, Name: name})
	m.rooms[room.ID] = room
	m.roomOf[uid] = room.ID

	log.Printf("[lobby] room %d created by uid=%d (password=%t)", room.ID, uid, room.password != "")
	m.broadcast(room)
	roomID := room.ID
	return []func(){func() {
		event.Publish(m.bus, event.RoomCreated, event.RoomCreatedArgs{RoomID: roomID, OwnerUID: uid})
	}}
}

func (m *Manager) handleJoin(uid uint64, name, connID string, req *knight.JoinRoomReq) []func() {
	if _, inRoom := m.roomOf[uid]; inRoom {
		m.replyError(connID, knight.ErrorCode_ERR_INVALID_REQUEST, "already in a room")
		return nil
	}
	room, ok := m.rooms[req.RoomId]
	if !ok {
		m.replyError(connID, knight.ErrorCode_ERR_ROOM_NOT_FOUND, "room not found")
		return nil
	}
	// 加密房间必须密码匹配；公开房间忽略入参密码
	if room.password != "" && req.GetPassword() != room.password {
		m.replyError(connID, knight.ErrorCode_ERR_ROOM_PASSWORD_MISMATCH, "room password mismatch")
		return nil
	}
	if room.State != knight.RoomSessionState_ROOM_WAITING {
		m.replyError(connID, knight.ErrorCode_ERR_GAME_ALREADY_STARTED, "game already started")
		return nil
	}
	if room.full(m.cfg.MaxPlayers) {
		m.replyError(connID, knight.ErrorCode_ERR_ROOM_FULL, "room full")
		return nil
	}

	room.members = append(room.members, &Member{UID: uid, Name: name})
	m.roomOf[uid] = room.ID

	log.Printf("[lobby] uid=%d joined room %d (%d/%d)", uid, room.ID, len(room.members), m.cfg.MaxPlayers)
	m.broadcast(room)
	roomID := room.ID
	return []func(){func() {
		event.Publish(m.bus, event.MemberJoined, event.MemberJoinedArgs{RoomID: roomID, UID: uid, Name: name})
	}}
}

func (m *Manager) handleLeave(uid uint64) []func() {
	// 在大厅时退出：按协议忽略（不回错）
	return m.leave(uid, event.LeaveActive)
}

// leave 执行退房；返回待发布事件（调用方须在锁外执行）。
// 房主离开由最早加入者继任；房间空则销毁。
func (m *Manager) leave(uid uint64, reason string) []func() {
	roomID, ok := m.roomOf[uid]
	if !ok {
		return nil
	}
	room := m.rooms[roomID]
	wasOwner := room.owner().UID == uid // 房主恒为 members[0]，先于 remove 判断
	room.remove(uid)
	delete(m.roomOf, uid)

	var pending []func()
	pending = append(pending, func() {
		event.Publish(m.bus, event.MemberLeft, event.MemberLeftArgs{RoomID: roomID, UID: uid, Reason: reason})
	})
	if len(room.members) == 0 {
		// 无人则销毁
		delete(m.rooms, roomID)
		log.Printf("[lobby] room %d destroyed (empty)", roomID)
		pending = append(pending, func() {
			event.Publish(m.bus, event.RoomDestroyed, event.RoomDestroyedArgs{RoomID: roomID})
		})
		return pending
	}
	log.Printf("[lobby] uid=%d left room %d, owner now uid=%d", uid, roomID, room.owner().UID)
	if wasOwner {
		// 房主离开 → 最早加入者继任（remove 后 members[0] 即新任房主）
		toUID := room.owner().UID
		pending = append(pending, func() {
			event.Publish(m.bus, event.OwnerChanged, event.OwnerChangedArgs{RoomID: roomID, FromUID: uid, ToUID: toUID})
		})
	}
	m.broadcast(room)
	return pending
}

func (m *Manager) handleSetReady(uid uint64, connID string, req *knight.SetReady) []func() {
	roomID, ok := m.roomOf[uid]
	if !ok {
		m.replyError(connID, knight.ErrorCode_ERR_INVALID_REQUEST, "not in a room")
		return nil
	}
	room := m.rooms[roomID]
	if room.State != knight.RoomSessionState_ROOM_WAITING {
		m.replyError(connID, knight.ErrorCode_ERR_INVALID_REQUEST, "game already started")
		return nil
	}
	room.find(uid).Ready = req.Ready
	m.broadcast(room)
	return nil // 准备状态无独立事件，RoomStatePush 已覆盖
}

func (m *Manager) handleStart(uid uint64, connID string) []func() {
	roomID, ok := m.roomOf[uid]
	if !ok {
		m.replyError(connID, knight.ErrorCode_ERR_INVALID_REQUEST, "not in a room")
		return nil
	}
	room := m.rooms[roomID]
	if room.owner().UID != uid {
		m.replyError(connID, knight.ErrorCode_ERR_NOT_ROOM_OWNER, "not room owner")
		return nil
	}
	if room.State != knight.RoomSessionState_ROOM_WAITING {
		m.replyError(connID, knight.ErrorCode_ERR_INVALID_REQUEST, "game already started")
		return nil
	}
	if len(room.members) < m.cfg.MinPlayers {
		m.replyError(connID, knight.ErrorCode_ERR_INVALID_REQUEST, "not enough players")
		return nil
	}

	room.State = knight.RoomSessionState_ROOM_RUNNING

	// 开局通知：MVP 固定地图序列 [1, 2, 3]（knight.proto GameStart 注释）
	mapIDs := []uint32{1, 2, 3}
	if data, err := protocol.Encode(&knight.Envelope{Payload: &knight.Envelope_GameStart{
		GameStart: &knight.GameStart{MapIds: mapIDs},
	}}); err == nil {
		for _, mem := range room.members {
			m.uidSender.SendToUID(mem.UID, data, kcp2k.ChannelReliable)
		}
	}

	log.Printf("[lobby] room %d game started (%d players)", room.ID, len(room.members))
	m.broadcast(room) // RoomStatePush{RUNNING}

	playerUIDs := make([]uint64, 0, len(room.members))
	for _, mem := range room.members {
		playerUIDs = append(playerUIDs, mem.UID)
	}
	startRoomID := room.ID
	return []func(){func() {
		event.Publish(m.bus, event.GameStarted, event.GameStartedArgs{
			RoomID:     startRoomID,
			MapIDs:     mapIDs,
			PlayerUIDs: playerUIDs,
		})
	}}
}

// ---------------------------------------------------------------- 广播/应答

// broadcast 向房间全体成员全量下发 RoomStatePush（协议：任何变化全量广播）。
// 用户域广播按 uid 定向发送，不感知传输层 connID。
func (m *Manager) broadcast(room *Room) {
	data, err := protocol.Encode(pushRoomState(room))
	if err != nil {
		log.Printf("[lobby] encode room state failed: %v", err)
		return
	}
	for _, mem := range room.members {
		m.uidSender.SendToUID(mem.UID, data, kcp2k.ChannelReliable)
	}
}

// replyError 应答错误（不断开；大厅类错误均为业务可恢复错误）。
func (m *Manager) replyError(connID string, code knight.ErrorCode, message string) {
	if data, err := protocol.EncodeError(code, message); err == nil {
		m.gate.Send(connID, data, kcp2k.ChannelReliable)
	}
}

// handleGetRoomList 应答当前房间列表（GetRoomListResp）：
// 全部非 CLOSED 房间的摘要，按房间号升序；不泄露密码本身。
func (m *Manager) handleGetRoomList(connID string) []func() {
	ids := make([]uint32, 0, len(m.rooms))
	for id := range m.rooms {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	rooms := make([]*knight.RoomSummary, 0, len(ids))
	for _, id := range ids {
		r := m.rooms[id]
		if r.State == knight.RoomSessionState_ROOM_CLOSED {
			continue
		}
		rooms = append(rooms, &knight.RoomSummary{
			RoomId:      r.ID,
			HasPassword: r.password != "",
			PlayerCount: uint32(len(r.members)),
			State:       r.State,
		})
	}

	data, err := protocol.Encode(&knight.Envelope{Payload: &knight.Envelope_GetRoomListResp{
		GetRoomListResp: &knight.GetRoomListResp{Rooms: rooms},
	}})
	if err != nil {
		log.Printf("[lobby] encode room list failed: %v", err)
		return nil
	}
	m.gate.Send(connID, data, kcp2k.ChannelReliable)
	return nil
}
