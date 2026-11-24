package world

import (
	"log"
	"math"
	"sync"
	"time"

	"knight-server/internal/event"
	"knight-server/internal/kcp2k"
	"knight-server/internal/protocol"
	"knight-server/internal/tick"

	knight "knight-server/protogen"
)

// Config 战斗世界配置（零值字段回落为协议默认值）。
type Config struct {
	TickRate          int           // 模拟频率（默认 30Hz）
	SnapshotRate      int           // 快照频率（默认 15Hz）
	RoomLoadedTimeout time.Duration // RoomLoaded 超时（默认 3s）
	MoveSpeed         float32       // 玩家移动速度（世界单位/秒，默认 3.0）
	PlayerRadius      float32       // 玩家碰撞半径（世界单位，默认 5/32）
	PlayerHP          int32         // 玩家初始生命值（默认 100）
}

func (c Config) withDefaults() Config {
	if c.TickRate <= 0 {
		c.TickRate = DefaultTickRate
	}
	if c.SnapshotRate <= 0 {
		c.SnapshotRate = DefaultSnapshotRate
	}
	if c.RoomLoadedTimeout <= 0 {
		c.RoomLoadedTimeout = DefaultRoomLoadedTimeout
	}
	if c.MoveSpeed <= 0 {
		c.MoveSpeed = DefaultMoveSpeed
	}
	if c.PlayerRadius <= 0 {
		c.PlayerRadius = DefaultPlayerRadius
	}
	if c.PlayerHP <= 0 {
		c.PlayerHP = DefaultPlayerHP
	}
	return c
}

// Manager 战斗世界管理器：
//   - 实现 session.Router（负责消息域 64~127）
//   - 订阅大厅 GameStarted / MemberLeft 事件，管理活跃对局 Room 的生命周期与 tick.Runner 挂载。
type Manager struct {
	cfg                Config
	snapshotEveryTicks int
	loadedTimeoutTicks int
	runner             *tick.Runner
	uidSender          UIDSender
	rejecter           Rejecter
	bus                *event.Bus

	mu     sync.RWMutex
	rooms  map[uint32]*Room
	roomOf map[uint64]*Room
}

// NewManager 构造战斗世界管理器并订阅大厅生命周期事件。
func NewManager(cfg Config, runner *tick.Runner, uidSender UIDSender, rejecter Rejecter, bus *event.Bus) *Manager {
	cfg = cfg.withDefaults()
	every := cfg.TickRate / cfg.SnapshotRate
	if every < 1 {
		every = 1
	}
	timeoutTicks := int(math.Round(cfg.RoomLoadedTimeout.Seconds() * float64(cfg.TickRate)))
	if timeoutTicks < 1 {
		timeoutTicks = 1
	}
	m := &Manager{
		cfg:                cfg,
		snapshotEveryTicks: every,
		loadedTimeoutTicks: timeoutTicks,
		runner:             runner,
		uidSender:          uidSender,
		rejecter:           rejecter,
		bus:                bus,
		rooms:              make(map[uint32]*Room),
		roomOf:             make(map[uint64]*Room),
	}
	event.Subscribe(bus, event.GameStarted, m.onGameStarted)
	event.Subscribe(bus, event.MemberLeft, m.onMemberLeft)
	return m
}

// RoomCount 当前活跃战斗房间数（诊断/测试用）。
func (m *Manager) RoomCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.rooms)
}

// Route 实现 session.Router：分发游戏域消息（64~127）。
func (m *Manager) Route(uid uint64, _ string, connID string, env *knight.Envelope) {
	switch env.Payload.(type) {
	case *knight.Envelope_RoomLoaded:
		room := m.roomForUID(uid)
		if room == nil {
			return
		}
		mapID := env.GetRoomLoaded().GetMapId()
		m.runner.Post(func() {
			room.handleRoomLoaded(uid, mapID)
		})
	case *knight.Envelope_InputBatch:
		batch := env.GetInputBatch()
		if err := validateInputBatch(batch); err != nil {
			if m.rejecter != nil {
				m.rejecter.Reject(connID, knight.ErrorCode_ERR_INVALID_REQUEST, err.Error())
			}
			return
		}
		room := m.roomForUID(uid)
		if room == nil {
			// 非对局状态收到的输入直接丢弃（protocol.md §5.5）
			return
		}
		m.runner.Post(func() {
			room.enqueueInputBatch(uid, batch)
		})
	default:
		if data, err := protocol.EncodeError(knight.ErrorCode_ERR_INVALID_REQUEST, "unsupported game message"); err == nil {
			m.uidSender.SendToUID(uid, data, kcp2k.ChannelReliable)
		}
	}
}

// onGameStarted 响应大厅开局：创建战斗房间、立即下发首房间 RoomTransition 并注册到 tick.Runner。
func (m *Manager) onGameStarted(args event.GameStartedArgs) {
	room, spawns := newRoom(roomConfig{
		roomID:             args.RoomID,
		mapIDs:             args.MapIDs,
		playerUIDs:         args.PlayerUIDs,
		loadedTimeoutTicks: m.loadedTimeoutTicks,
		snapshotEveryTicks: m.snapshotEveryTicks,
		moveSpeed:          m.cfg.MoveSpeed,
		playerRadius:       m.cfg.PlayerRadius,
		playerHP:           m.cfg.PlayerHP,
		uidSender:          m.uidSender,
	})

	m.mu.Lock()
	m.rooms[args.RoomID] = room
	for _, uid := range args.PlayerUIDs {
		m.roomOf[uid] = room
	}
	m.mu.Unlock()

	// 紧随 GameStart 下发首房间 RoomTransition（同一可靠通道保序，protocol.md §5.3）
	if data, err := protocol.Encode(&knight.Envelope{Payload: &knight.Envelope_RoomTransition{
		RoomTransition: &knight.RoomTransition{
			MapId:  room.mapDef.ID,
			Spawns: spawns,
		},
	}}); err == nil {
		for _, uid := range args.PlayerUIDs {
			m.uidSender.SendToUID(uid, data, kcp2k.ChannelReliable)
		}
	} else {
		log.Printf("[world] encode room transition failed: %v", err)
	}

	m.runner.Add(room)
}

// onMemberLeft 响应玩家断线或退房：从战斗房间移除实体并广播 PlayerLeft，空房则注销。
func (m *Manager) onMemberLeft(args event.MemberLeftArgs) {
	m.mu.Lock()
	room, ok := m.roomOf[args.UID]
	if ok {
		delete(m.roomOf, args.UID)
	}
	m.mu.Unlock()
	if !ok {
		return
	}

	roomID := room.id
	uid := args.UID
	reason := args.Reason
	m.runner.Post(func() {
		if empty := room.removePlayer(uid, reason); empty {
			m.runner.RemoveNow(room)
			var removed bool
			m.mu.Lock()
			if m.rooms[roomID] == room {
				delete(m.rooms, roomID)
				removed = true
			}
			m.mu.Unlock()
			if removed {
				event.Publish(m.bus, event.GameEnded, event.GameEndedArgs{
					RoomID: roomID,
					Reason: event.GameEndEmpty,
				})
			}
		}
	})
}

func (m *Manager) roomForUID(uid uint64) *Room {
	m.mu.RLock()
	room := m.roomOf[uid]
	m.mu.RUnlock()
	return room
}
