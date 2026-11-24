package world

import (
	"errors"
	"fmt"
	"log"
	"math"
	"time"

	"knight-server/internal/event"
	"knight-server/internal/kcp2k"
	"knight-server/internal/protocol"

	knight "knight-server/protogen"
)

// 权威模拟与协议常量（对齐 Docs/protocol.md §5.5 / §8 / §9）。
const (
	MaxInputBatchSize        = 120
	DefaultTickRate          = 30
	DefaultSnapshotRate      = 15
	DefaultRoomLoadedTimeout = 3 * time.Second

	DefaultMoveSpeed      float32 = 3.0       // 世界单位/秒（32px/tile 下为 96px/s）
	DefaultPlayerRadius   float32 = 5.0 / 32.0 // 0.15625 世界单位（匹配 Player.tscn CircleShape2D radius=5）
	DefaultPlayerHP       int32   = 100
	DefaultPlayerConfigID uint32  = 1

	maxInputsPerTick = 4 // 允许吸收短暂网络抖动突发，同时防止单 tick 加速作弊
	maxInputBudget   = maxInputsPerTick // 令牌桶上限：每 tick 补 1，最多攒 4 个额度吸收抖动
)

// UIDSender 按 uid 定向发送（session.Manager 满足）。
type UIDSender interface {
	SendToUID(uid uint64, data []byte, channel kcp2k.Channel) bool
}

// Rejecter 协议违规拒绝并断开连接（session.Manager 满足）。
type Rejecter interface {
	Reject(connID string, code knight.ErrorCode, message string)
}

type roomPhase uint8

const (
	phaseLoading roomPhase = iota
	phaseActive
	phaseClosed
)

// PlayerEntity 战斗房间内的玩家运行时实体。
type PlayerEntity struct {
	EntityID      uint32
	UID           uint64
	ConfigID      uint32
	Pos           Vec2
	Facing        float32
	AnimState     knight.EntityAnimState
	HP            int32
	Alive         bool
	Loaded        bool
	LastInputSeq  uint32
	InputBudget   int
	PendingInputs []*knight.InputCmd
}

// Room 单局战斗房间实例，实现 tick.Tickable。
// 构造后交给 tick.Runner 驱动，内部状态仅在模拟 goroutine 上读写。
type Room struct {
	id         uint32
	mapIDs     []uint32
	currentIdx int
	mapDef     MapDef

	phase              roomPhase
	loadingTicks       int
	loadedTimeoutTicks int

	tickNum             uint32
	snapshotEveryTicks  int
	snapshotTickCounter int

	moveSpeed    float32
	playerRadius float32

	nextEntityID uint32
	players      []*PlayerEntity
	byUID        map[uint64]*PlayerEntity

	uidSender UIDSender
}

type roomConfig struct {
	roomID             uint32
	mapIDs             []uint32
	playerUIDs         []uint64
	loadedTimeoutTicks int
	snapshotEveryTicks int
	moveSpeed          float32
	playerRadius       float32
	playerHP           int32
	uidSender          UIDSender
}

func newRoom(cfg roomConfig) (*Room, []*knight.SpawnAssignment) {
	mapID := Map1ID
	if len(cfg.mapIDs) > 0 {
		mapID = cfg.mapIDs[0]
	}
	mapDef, ok := GetMap(mapID)
	if !ok {
		mapDef = map1Def
	}

	r := &Room{
		id:                 cfg.roomID,
		mapIDs:             append([]uint32(nil), cfg.mapIDs...),
		currentIdx:         0,
		mapDef:             mapDef,
		phase:              phaseLoading,
		loadedTimeoutTicks: cfg.loadedTimeoutTicks,
		snapshotEveryTicks: cfg.snapshotEveryTicks,
		moveSpeed:          cfg.moveSpeed,
		playerRadius:       cfg.playerRadius,
		nextEntityID:       1,
		players:            make([]*PlayerEntity, 0, len(cfg.playerUIDs)),
		byUID:              make(map[uint64]*PlayerEntity, len(cfg.playerUIDs)),
		uidSender:          cfg.uidSender,
	}

	spawns := make([]*knight.SpawnAssignment, 0, len(cfg.playerUIDs))
	for i, uid := range cfg.playerUIDs {
		spawn := mapDef.Spawns[i%len(mapDef.Spawns)]
		p := &PlayerEntity{
			EntityID:  r.nextEntityID,
			UID:       uid,
			ConfigID:  DefaultPlayerConfigID,
			Pos:       spawn,
			Facing:    0,
			AnimState: knight.EntityAnimState_ANIM_IDLE,
			HP:        cfg.playerHP,
			Alive:     true,
		}
		r.nextEntityID++
		r.players = append(r.players, p)
		r.byUID[uid] = p
		spawns = append(spawns, &knight.SpawnAssignment{
			Uid:  uid,
			PosX: spawn.X,
			PosY: spawn.Y,
		})
	}
	return r, spawns
}

// Tick 实现 tick.Tickable：按固定步长推进房间状态（加载超时 / 30Hz 移动模拟 / 15Hz 快照单播）。
func (r *Room) Tick(dt time.Duration) {
	switch r.phase {
	case phaseClosed:
		return
	case phaseLoading:
		r.loadingTicks++
		if r.loadingTicks >= r.loadedTimeoutTicks {
			log.Printf("[world] room %d map %d load timeout (%d ticks), activating", r.id, r.mapDef.ID, r.loadedTimeoutTicks)
			r.activate()
		}
		return
	case phaseActive:
		r.tickActive(dt)
	}
}

func (r *Room) tickActive(dt time.Duration) {
	r.tickNum++
	stepSec := float32(dt.Seconds())

	for _, p := range r.players {
		if !p.Alive {
			p.AnimState = knight.EntityAnimState_ANIM_DEAD
			continue
		}
		// 令牌桶补充：每 30Hz tick 补充 1 个输入额度，上限 maxInputBudget 以吸收网络抖动，
		// 长期平均速率严格锁定为 1 输入/tick，防止高频输入加速作弊。
		if p.InputBudget < maxInputBudget {
			p.InputBudget++
		}
		if len(p.PendingInputs) == 0 {
			p.AnimState = knight.EntityAnimState_ANIM_IDLE
			continue
		}

		count := len(p.PendingInputs)
		if count > p.InputBudget {
			count = p.InputBudget
		}
		if count > maxInputsPerTick {
			count = maxInputsPerTick
		}
		if count == 0 {
			continue
		}
		for i := 0; i < count; i++ {
			r.applyInput(p, p.PendingInputs[i], stepSec)
		}
		p.InputBudget -= count

		// 将剩余输入前移并清零尾部指针，避免滞留 *knight.InputCmd 指针与底层数组持续扩容。
		rem := copy(p.PendingInputs, p.PendingInputs[count:])
		clear(p.PendingInputs[rem:])
		p.PendingInputs = p.PendingInputs[:rem]
	}

	r.snapshotTickCounter++
	if r.snapshotTickCounter >= r.snapshotEveryTicks {
		r.snapshotTickCounter = 0
		r.broadcastSnapshots()
	}
}

func (r *Room) applyInput(p *PlayerEntity, cmd *knight.InputCmd, stepSec float32) {
	mx, my := sanitizeMove(cmd.MoveX, cmd.MoveY)

	if aim := float64(cmd.AimAngle); !math.IsNaN(aim) && !math.IsInf(aim, 0) {
		p.Facing = cmd.AimAngle
	}

	if mx != 0 || my != 0 {
		next := Vec2{
			X: p.Pos.X + mx*r.moveSpeed*stepSec,
			Y: p.Pos.Y + my*r.moveSpeed*stepSec,
		}
		p.Pos = resolveCollisions(next, r.playerRadius, r.mapDef)
		p.AnimState = knight.EntityAnimState_ANIM_MOVING
	} else {
		p.AnimState = knight.EntityAnimState_ANIM_IDLE
	}

	p.LastInputSeq = cmd.Seq
}

// sanitizeMove 过滤 NaN/Inf 并将模长 > 1 的移动向量钳制到单位圆内（protocol.md §5.4）。
func sanitizeMove(x, y float32) (float32, float32) {
	fx, fy := float64(x), float64(y)
	if math.IsNaN(fx) || math.IsInf(fx, 0) {
		fx = 0
	}
	if math.IsNaN(fy) || math.IsInf(fy, 0) {
		fy = 0
	}
	mag := math.Hypot(fx, fy)
	if mag < 1e-6 {
		return 0, 0
	}
	if mag > 1.0 {
		fx /= mag
		fy /= mag
	}
	return float32(fx), float32(fy)
}

// handleRoomLoaded 处理客户端加载确认（protocol.md §6.2）：
// 仅接受当前加载阶段、当前待加载 map_id、在场玩家的首次确认。
func (r *Room) handleRoomLoaded(uid uint64, mapID uint32) {
	if r.phase != phaseLoading || mapID != r.mapDef.ID {
		return
	}
	p, ok := r.byUID[uid]
	if !ok || p.Loaded {
		return
	}
	p.Loaded = true
	if r.allLoaded() {
		r.activate()
	}
}

func (r *Room) allLoaded() bool {
	if len(r.players) == 0 {
		return false
	}
	for _, p := range r.players {
		if !p.Loaded {
			return false
		}
	}
	return true
}

// activate 从加载阶段切换到活跃模拟，立即下发首帧全量快照（protocol.md §6.2）。
func (r *Room) activate() {
	if r.phase != phaseLoading {
		return
	}
	r.phase = phaseActive
	r.snapshotTickCounter = 0
	log.Printf("[world] room %d map %d active (%d players)", r.id, r.mapDef.ID, len(r.players))
	r.broadcastSnapshots()
}

// enqueueInputBatch 将已通过协议校验的 InputBatch 中尚未处理的新输入入队。
func (r *Room) enqueueInputBatch(uid uint64, batch *knight.InputBatch) {
	if r.phase != phaseActive {
		return
	}
	p, ok := r.byUID[uid]
	if !ok || !p.Alive {
		return
	}
	cutoff := p.LastInputSeq
	if n := len(p.PendingInputs); n > 0 {
		cutoff = p.PendingInputs[n-1].Seq
	}
	for _, cmd := range batch.Inputs {
		if cmd.Seq <= cutoff {
			continue
		}
		if len(p.PendingInputs) >= MaxInputBatchSize {
			break
		}
		p.PendingInputs = append(p.PendingInputs, cmd)
		cutoff = cmd.Seq
	}
}

// removePlayer 对局中移除断线/离开玩家：向剩余玩家广播 EntityDespawn + PlayerLeft。
// 返回 true 表示房间已空并关闭。
func (r *Room) removePlayer(uid uint64, reason string) bool {
	p, ok := r.byUID[uid]
	if !ok {
		return len(r.players) == 0
	}
	clear(p.PendingInputs)
	p.PendingInputs = nil
	delete(r.byUID, uid)
	for i, cur := range r.players {
		if cur.UID == uid {
			r.players = append(r.players[:i], r.players[i+1:]...)
			break
		}
	}

	log.Printf("[world] room %d player uid=%d left (reason=%s, remaining=%d)", r.id, uid, reason, len(r.players))
	if len(r.players) == 0 {
		r.phase = phaseClosed
		log.Printf("[world] room %d closed (empty)", r.id)
		return true
	}

	r.broadcastReliable(&knight.Envelope{Payload: &knight.Envelope_EntityDespawn{
		EntityDespawn: &knight.EntityDespawn{
			EntityIds: []uint32{p.EntityID},
			Reason:    knight.DespawnReason_DESPAWN_KILLED,
		},
	}})
	r.broadcastReliable(&knight.Envelope{Payload: &knight.Envelope_PlayerLeft{
		PlayerLeft: &knight.PlayerLeft{
			Uid:    uid,
			Reason: leftReasonProto(reason),
		},
	}})

	if r.phase == phaseLoading && r.allLoaded() {
		r.activate()
	}
	return false
}

// leftReasonProto 将事件层离场原因映射为协议 LeftReason。
// knight.proto v5 仅定义 LEFT_DISCONNECT=0，主动退房（LeaveActive）与断线（LeaveDisconnect）
// 当前统一映射为 LEFT_DISCONNECT；待后续协议升级补充主动离场枚举时在此分流。
func leftReasonProto(reason string) knight.LeftReason {
	switch reason {
	case event.LeaveActive, event.LeaveDisconnect:
		return knight.LeftReason_LEFT_DISCONNECT
	default:
		return knight.LeftReason_LEFT_DISCONNECT
	}
}

// broadcastSnapshots 按客户端单播全量快照（各自携带自己的 last_input_seq）。
func (r *Room) broadcastSnapshots() {
	entities := make([]*knight.EntityState, 0, len(r.players))
	for _, p := range r.players {
		entities = append(entities, &knight.EntityState{
			EntityId:  p.EntityID,
			Type:      knight.EntityType_ENTITY_PLAYER,
			PosX:      p.Pos.X,
			PosY:      p.Pos.Y,
			Facing:    p.Facing,
			State:     p.AnimState,
			Hp:        p.HP,
			PlayerUid: p.UID,
			ConfigId:  p.ConfigID,
		})
	}

	for _, p := range r.players {
		data, err := protocol.Encode(&knight.Envelope{Payload: &knight.Envelope_Snapshot{
			Snapshot: &knight.Snapshot{
				Tick:         r.tickNum,
				LastInputSeq: p.LastInputSeq,
				BaselineSeq:  0,
				Entities:     entities,
			},
		}})
		if err != nil {
			log.Printf("[world] encode snapshot failed: %v", err)
			return
		}
		r.uidSender.SendToUID(p.UID, data, kcp2k.ChannelReliable)
	}
}

func (r *Room) broadcastReliable(env *knight.Envelope) {
	data, err := protocol.Encode(env)
	if err != nil {
		log.Printf("[world] encode event failed: %v", err)
		return
	}
	for _, p := range r.players {
		r.uidSender.SendToUID(p.UID, data, kcp2k.ChannelReliable)
	}
}

// validateInputBatch 校验 InputBatch 协议契约（knight.proto / protocol.md §5.5）：
// 最多 120 条、seq 从 1 起严格单调递增、first_seq == inputs[0].seq。
func validateInputBatch(batch *knight.InputBatch) error {
	if batch == nil {
		return errors.New("nil input batch")
	}
	if len(batch.Inputs) > MaxInputBatchSize {
		return fmt.Errorf("input batch too large: %d > %d", len(batch.Inputs), MaxInputBatchSize)
	}
	if len(batch.Inputs) == 0 {
		return nil
	}
	first := batch.Inputs[0]
	if first == nil || first.Seq == 0 {
		return errors.New("input seq must be >= 1")
	}
	if batch.FirstSeq != first.Seq {
		return fmt.Errorf("first_seq %d != inputs[0].seq %d", batch.FirstSeq, first.Seq)
	}
	for i := 1; i < len(batch.Inputs); i++ {
		cur := batch.Inputs[i]
		if cur == nil || cur.Seq <= batch.Inputs[i-1].Seq {
			return errors.New("input seq must be strictly increasing")
		}
	}
	return nil
}
