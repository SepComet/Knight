package world

import (
	"errors"
	"fmt"
	"log"
	"math"
	"math/rand/v2"
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

	DefaultMoveSpeed      float32 = 3.0        // 世界单位/秒（32px/tile 下为 96px/s）
	DefaultPlayerRadius   float32 = 5.0 / 32.0 // 0.15625 世界单位（匹配 Player.tscn CircleShape2D radius=5）
	DefaultPlayerHP       int32   = 100
	DefaultPlayerConfigID uint32  = 1
	DefaultPlayerWeaponID uint32  = WeaponConfigRifle
	DefaultPlayerSkillID  uint32  = SkillConfigDashSlash

	maxInputsPerTick = 4                // 允许吸收短暂网络抖动突发，同时防止单 tick 加速作弊
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

// MatchPlayerStat 对局结算时单个玩家的统计与配置快照。
type MatchPlayerStat struct {
	UID         uint64
	CharacterID uint32
	WeaponID    uint32
	Kills       uint32
	DamageDealt int32
	DamageTaken int32
	Alive       bool
}

// MatchFinishReport 对局结束报告，交给 Manager 持久化并下发 GameResult。
type MatchFinishReport struct {
	RoomID       uint32
	Outcome      knight.GameOutcome
	DurationSec  uint32
	Seed         uint64
	Players      []MatchPlayerStat
	RecipientUID []uint64 // 当前仍在房间内需要接收 GameResult 的玩家
}

// PlayerEntity 战斗房间内的玩家运行时实体。
type PlayerEntity struct {
	EntityID      uint32
	UID           uint64
	ConfigID      uint32
	WeaponID      uint32
	SkillID       uint32
	Pos           Vec2
	Facing        float32
	AnimState     knight.EntityAnimState
	HP            int32
	MaxHP         int32
	Alive         bool
	Loaded        bool
	LastInputSeq  uint32
	InputBudget   int
	PendingInputs []*knight.InputCmd
	PrevButtons   uint32

	// 武器与连发冷却
	WeaponCD       float32
	BurstRemaining int
	BurstTimer     float32

	// 主动技能冷却
	SkillCD      float32
	SkillCDTotal float32

	// 增益/状态计时器
	InvulnTimer  float32
	ShieldTimer  float32
	AtkBuffTimer float32
	DamagedTimer float32

	// 结算统计
	Kills       uint32
	DamageDealt int32
	DamageTaken int32
}

func (p *PlayerEntity) statusFlags() uint32 {
	var flags uint32
	if p.InvulnTimer > 0 {
		flags |= StatusFlagInvuln
	}
	if p.ShieldTimer > 0 {
		flags |= StatusFlagShield
	}
	if p.AtkBuffTimer > 0 {
		flags |= StatusFlagAtkBuff
	}
	return flags
}

func (p *PlayerEntity) toProto() *knight.EntityState {
	state := p.AnimState
	if !p.Alive {
		state = knight.EntityAnimState_ANIM_DEAD
	} else if p.DamagedTimer > 0 {
		state = knight.EntityAnimState_ANIM_DAMAGED
	}
	cdRem := p.SkillCD
	if cdRem < 0 {
		cdRem = 0
	}
	return &knight.EntityState{
		EntityId:         p.EntityID,
		Type:             knight.EntityType_ENTITY_PLAYER,
		PosX:             p.Pos.X,
		PosY:             p.Pos.Y,
		Facing:           p.Facing,
		State:            state,
		Hp:               p.HP,
		MaxHp:            p.MaxHP,
		PlayerUid:        p.UID,
		ConfigId:         p.ConfigID,
		SkillCdRemaining: cdRem,
		SkillCdTotal:     p.SkillCDTotal,
		StatusFlags:      p.statusFlags(),
	}
}

// Room 单局战斗房间实例，实现 tick.Tickable。
// 构造后交给 tick.Runner 驱动，内部状态仅在模拟 goroutine 上读写。
type Room struct {
	id         uint32
	seed       uint64
	mapIDs     []uint32
	currentIdx int
	mapDef     MapDef

	startedAt time.Time
	rng       *rand.Rand

	phase              roomPhase
	loadingTicks       int
	loadedTimeoutTicks int
	doorOpen           bool
	roomHadBoss        bool

	tickNum             uint32
	snapshotEveryTicks  int
	snapshotTickCounter int

	moveSpeed    float32
	playerRadius float32

	nextEntityID uint32
	players      []*PlayerEntity
	byUID        map[uint64]*PlayerEntity
	leftPlayers  []MatchPlayerStat

	enemies     []*EnemyEntity
	projectiles []*ProjectileEntity
	pickups     []*PickupEntity

	uidSender     UIDSender
	onMatchFinish func(report MatchFinishReport)
}

type roomConfig struct {
	roomID             uint32
	seed               uint64
	mapIDs             []uint32
	playerUIDs         []uint64
	loadedTimeoutTicks int
	snapshotEveryTicks int
	moveSpeed          float32
	playerRadius       float32
	playerHP           int32
	uidSender          UIDSender
	onMatchFinish      func(report MatchFinishReport)
}

func newRoom(cfg roomConfig) (*Room, []*knight.SpawnAssignment) {
	mapIDs := append([]uint32(nil), cfg.mapIDs...)
	if len(mapIDs) == 0 {
		mapIDs = []uint32{Map1ID}
	}
	mapDef, ok := GetMap(mapIDs[0])
	if !ok {
		mapDef = map1Def
	}
	seed := cfg.seed
	if seed == 0 {
		seed = uint64(time.Now().UnixNano())
	}

	r := &Room{
		id:                 cfg.roomID,
		seed:               seed,
		mapIDs:             mapIDs,
		currentIdx:         0,
		mapDef:             mapDef,
		startedAt:          time.Now(),
		rng:                rand.New(rand.NewPCG(seed, seed^0xa5a5a5a5a5a5a5a5)),
		phase:              phaseLoading,
		loadedTimeoutTicks: cfg.loadedTimeoutTicks,
		snapshotEveryTicks: cfg.snapshotEveryTicks,
		moveSpeed:          cfg.moveSpeed,
		playerRadius:       cfg.playerRadius,
		nextEntityID:       1,
		players:            make([]*PlayerEntity, 0, len(cfg.playerUIDs)),
		byUID:              make(map[uint64]*PlayerEntity, len(cfg.playerUIDs)),
		uidSender:          cfg.uidSender,
		onMatchFinish:      cfg.onMatchFinish,
	}

	skillCD := GetSkillStrategy(DefaultPlayerSkillID).Cooldown()
	spawns := make([]*knight.SpawnAssignment, 0, len(cfg.playerUIDs))
	for i, uid := range cfg.playerUIDs {
		spawn := mapDef.Spawns[i%len(mapDef.Spawns)]
		p := &PlayerEntity{
			EntityID:     r.allocEntityID(),
			UID:          uid,
			ConfigID:     DefaultPlayerConfigID,
			WeaponID:     DefaultPlayerWeaponID,
			SkillID:      DefaultPlayerSkillID,
			Pos:          spawn,
			Facing:       0,
			AnimState:    knight.EntityAnimState_ANIM_IDLE,
			HP:           cfg.playerHP,
			MaxHP:        cfg.playerHP,
			Alive:        true,
			SkillCDTotal: skillCD,
		}
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

func (r *Room) allocEntityID() uint32 {
	id := r.nextEntityID
	r.nextEntityID++
	return id
}

// Tick 实现 tick.Tickable：按固定步长推进房间状态（加载超时 / 30Hz 战斗模拟 / 15Hz 快照单播）。
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

	// 1. 玩家计时器更新与输入消费
	for _, p := range r.players {
		if !p.Alive {
			p.AnimState = knight.EntityAnimState_ANIM_DEAD
			continue
		}
		tickTimer(&p.WeaponCD, stepSec)
		tickTimer(&p.SkillCD, stepSec)
		tickTimer(&p.InvulnTimer, stepSec)
		tickTimer(&p.ShieldTimer, stepSec)
		tickTimer(&p.AtkBuffTimer, stepSec)
		tickTimer(&p.DamagedTimer, stepSec)

		r.tickPlayerBurst(p, stepSec)

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

	// 2. 弹道推进与命中伤害判定
	r.tickProjectiles(stepSec)

	// 3. 怪物与 Boss AI
	r.tickEnemies(stepSec)

	// 4. 胜负判定
	if r.roomHadBoss && !r.hasAliveBoss() {
		r.finishMatch(knight.GameOutcome_OUTCOME_VICTORY)
		return
	}
	if r.alivePlayerCount() == 0 {
		r.finishMatch(knight.GameOutcome_OUTCOME_DEFEAT)
		return
	}

	// 5. 清怪解锁出口
	if !r.doorOpen && !r.roomHadBoss && r.aliveEnemyCount() == 0 {
		r.doorOpen = true
		r.broadcastDoorState(knight.DoorStateValue_DOOR_OPEN)
	}

	// 6. 全员存活玩家进入出口 → 切房
	if r.doorOpen && r.currentIdx+1 < len(r.mapIDs) && r.allAlivePlayersInExit() {
		r.transitionToNextRoom()
		return
	}

	// 7. 15Hz 快照单播
	r.snapshotTickCounter++
	if r.snapshotTickCounter >= r.snapshotEveryTicks {
		r.snapshotTickCounter = 0
		r.broadcastSnapshots()
	}
}

func tickTimer(t *float32, dt float32) {
	if *t > 0 {
		*t -= dt
		if *t < 0 {
			*t = 0
		}
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

	// 按钮解析：BTN_ATTACK(bit0) 持续；BTN_INTERACT(bit1) 边沿；BTN_SKILL(bit2) 冷却就绪触发
	btns := cmd.Buttons
	if btns&uint32(knight.InputButtons_BTN_SKILL) != 0 {
		r.triggerPlayerSkill(p, cmd)
	}
	if btns&uint32(knight.InputButtons_BTN_ATTACK) != 0 {
		r.tryFireWeapon(p)
	}
	if (btns&uint32(knight.InputButtons_BTN_INTERACT) != 0) && (p.PrevButtons&uint32(knight.InputButtons_BTN_INTERACT) == 0) {
		r.tryPickupInteract(p)
	}
	p.PrevButtons = btns
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

// activate 从加载阶段切换到活跃模拟：生成房间怪物与道具，广播 DoorState 与首帧全量快照。
func (r *Room) activate() {
	if r.phase != phaseLoading {
		return
	}
	r.phase = phaseActive
	r.loadingTicks = 0
	r.snapshotTickCounter = 0

	r.spawnRoomContents()
	log.Printf("[world] room %d map %d (%s) active (%d players, %d enemies, %d pickups)",
		r.id, r.mapDef.ID, r.mapDef.Name, len(r.players), len(r.enemies), len(r.pickups))
	r.broadcastSnapshots()
}

func (r *Room) spawnRoomContents() {
	r.enemies = r.enemies[:0]
	r.projectiles = r.projectiles[:0]
	r.pickups = r.pickups[:0]
	r.roomHadBoss = false

	var spawned []*knight.EntityState
	for _, spec := range r.mapDef.EnemySpawns {
		e := r.spawnEnemy(spec)
		if e.IsBoss {
			r.roomHadBoss = true
		}
		spawned = append(spawned, e.toProto())
	}
	for _, spec := range r.mapDef.PickupSpawns {
		pk := &PickupEntity{
			EntityID: r.allocEntityID(),
			ConfigID: spec.ConfigID,
			Pos:      resolveCollisions(spec.Pos, 0.35, r.mapDef),
		}
		r.pickups = append(r.pickups, pk)
		spawned = append(spawned, pk.toProto())
	}

	if len(spawned) > 0 {
		r.broadcastEntitySpawn(spawned)
	}

	if len(r.enemies) == 0 {
		r.doorOpen = true
		r.broadcastDoorState(knight.DoorStateValue_DOOR_OPEN)
	} else {
		r.doorOpen = false
		r.broadcastDoorState(knight.DoorStateValue_DOOR_LOCKED)
	}
}

func (r *Room) transitionToNextRoom() {
	// 清理旧房间所有非玩家实体
	var cleanupIDs []uint32
	for _, e := range r.enemies {
		cleanupIDs = append(cleanupIDs, e.EntityID)
	}
	for _, proj := range r.projectiles {
		cleanupIDs = append(cleanupIDs, proj.EntityID)
	}
	for _, pk := range r.pickups {
		cleanupIDs = append(cleanupIDs, pk.EntityID)
	}
	if len(cleanupIDs) > 0 {
		r.broadcastEntityDespawn(cleanupIDs, knight.DespawnReason_DESPAWN_ROOM_CLEANUP)
	}
	r.enemies = nil
	r.projectiles = nil
	r.pickups = nil

	r.currentIdx++
	nextID := r.mapIDs[r.currentIdx]
	nextMap, ok := GetMap(nextID)
	if !ok {
		nextMap = map1Def
	}
	r.mapDef = nextMap
	r.phase = phaseLoading
	r.loadingTicks = 0
	r.doorOpen = false
	r.roomHadBoss = false

	spawns := make([]*knight.SpawnAssignment, 0, len(r.players))
	for i, p := range r.players {
		sp := nextMap.Spawns[i%len(nextMap.Spawns)]
		p.Pos = sp
		p.Loaded = false
		clear(p.PendingInputs)
		p.PendingInputs = p.PendingInputs[:0]
		p.BurstRemaining = 0
		spawns = append(spawns, &knight.SpawnAssignment{
			Uid:  p.UID,
			PosX: sp.X,
			PosY: sp.Y,
		})
	}

	log.Printf("[world] room %d transitioning to idx=%d/%d map=%d (%s)",
		r.id, r.currentIdx+1, len(r.mapIDs), nextMap.ID, nextMap.Name)
	r.broadcastReliable(&knight.Envelope{Payload: &knight.Envelope_RoomTransition{
		RoomTransition: &knight.RoomTransition{
			MapId:      nextMap.ID,
			Spawns:     spawns,
			RoomIndex:  uint32(r.currentIdx),
			TotalRooms: uint32(len(r.mapIDs)),
		},
	}})
}

func (r *Room) aliveEnemyCount() int {
	n := 0
	for _, e := range r.enemies {
		if e.Alive {
			n++
		}
	}
	return n
}

func (r *Room) hasAliveBoss() bool {
	for _, e := range r.enemies {
		if e.Alive && e.IsBoss {
			return true
		}
	}
	return false
}

func (r *Room) alivePlayerCount() int {
	n := 0
	for _, p := range r.players {
		if p.Alive {
			n++
		}
	}
	return n
}

func (r *Room) allAlivePlayersInExit() bool {
	aliveCount := 0
	for _, p := range r.players {
		if !p.Alive {
			continue
		}
		aliveCount++
		if !pointInRect(p.Pos, r.mapDef.ExitZone) {
			return false
		}
	}
	return aliveCount > 0
}

func (r *Room) finishMatch(outcome knight.GameOutcome) {
	if r.phase == phaseClosed {
		return
	}
	r.phase = phaseClosed

	dur := uint32(math.Round(time.Since(r.startedAt).Seconds()))
	if dur == 0 {
		dur = 1
	}

	stats := make([]MatchPlayerStat, 0, len(r.players)+len(r.leftPlayers))
	recipients := make([]uint64, 0, len(r.players))
	for _, p := range r.players {
		recipients = append(recipients, p.UID)
		stats = append(stats, MatchPlayerStat{
			UID:         p.UID,
			CharacterID: p.ConfigID,
			WeaponID:    p.WeaponID,
			Kills:       p.Kills,
			DamageDealt: p.DamageDealt,
			DamageTaken: p.DamageTaken,
			Alive:       p.Alive,
		})
	}
	stats = append(stats, r.leftPlayers...)

	report := MatchFinishReport{
		RoomID:       r.id,
		Outcome:      outcome,
		DurationSec:  dur,
		Seed:         r.seed,
		Players:      stats,
		RecipientUID: recipients,
	}

	if r.onMatchFinish != nil {
		r.onMatchFinish(report)
	}
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
	// 记录断线玩家统计（结算中 alive=false，protocol.md §5.6）
	r.leftPlayers = append(r.leftPlayers, MatchPlayerStat{
		UID:         p.UID,
		CharacterID: p.ConfigID,
		WeaponID:    p.WeaponID,
		Kills:       p.Kills,
		DamageDealt: p.DamageDealt,
		DamageTaken: p.DamageTaken,
		Alive:       false,
	})

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

	if r.phase == phaseActive && r.alivePlayerCount() == 0 {
		r.finishMatch(knight.GameOutcome_OUTCOME_DEFEAT)
		return false
	}
	if r.phase == phaseLoading && r.allLoaded() {
		r.activate()
	}
	return false
}

// leftReasonProto 将事件层离场原因映射为协议 LeftReason。
func leftReasonProto(reason string) knight.LeftReason {
	switch reason {
	case event.LeaveActive, event.LeaveDisconnect:
		return knight.LeftReason_LEFT_DISCONNECT
	default:
		return knight.LeftReason_LEFT_DISCONNECT
	}
}

// broadcastSnapshots 按客户端单播全量快照（各自携带自己的 last_input_seq）。
// 已阵亡玩家（!p.Alive）在死亡时已广播 EntityDespawn，不再写入快照实体列表，
// 避免客户端兜底重建死亡实体（阵亡玩家本身仍继续接收快照观战队友）。
func (r *Room) broadcastSnapshots() {
	total := len(r.players) + len(r.enemies) + len(r.projectiles) + len(r.pickups)
	entities := make([]*knight.EntityState, 0, total)
	for _, p := range r.players {
		if !p.Alive {
			continue
		}
		entities = append(entities, p.toProto())
	}
	for _, e := range r.enemies {
		if e.Alive {
			entities = append(entities, e.toProto())
		}
	}
	for _, proj := range r.projectiles {
		entities = append(entities, proj.toProto())
	}
	for _, pk := range r.pickups {
		entities = append(entities, pk.toProto())
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

func (r *Room) broadcastEntitySpawn(entities []*knight.EntityState) {
	if len(entities) == 0 {
		return
	}
	r.broadcastReliable(&knight.Envelope{Payload: &knight.Envelope_EntitySpawn{
		EntitySpawn: &knight.EntitySpawn{Entities: entities},
	}})
}

func (r *Room) broadcastEntityDespawn(ids []uint32, reason knight.DespawnReason) {
	if len(ids) == 0 {
		return
	}
	r.broadcastReliable(&knight.Envelope{Payload: &knight.Envelope_EntityDespawn{
		EntityDespawn: &knight.EntityDespawn{
			EntityIds: ids,
			Reason:    reason,
		},
	}})
}

func (r *Room) broadcastDoorState(val knight.DoorStateValue) {
	r.broadcastReliable(&knight.Envelope{Payload: &knight.Envelope_DoorState{
		DoorState: &knight.DoorState{State: val},
	}})
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
