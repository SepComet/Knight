package world

import (
	"math"

	knight "knight-server/protogen"
)

// 怪物与 Boss config_id 常量。
const (
	EnemyConfigMelee  uint32 = 1 // 近战小怪
	EnemyConfigRanged uint32 = 2 // 远程小怪
	EnemyConfigElite  uint32 = 3 // 精英近战卫士
	BossConfigLord    uint32 = 1 // 地牢领主 Boss
)

// EnemyAIState 有限状态机状态。
type EnemyAIState uint8

const (
	AIStateIdle EnemyAIState = iota
	AIStateChase
	AIStateKeepDistance
	AIStateAttack
	AIStateBossCharge
)

// EnemyConfig 怪物/Boss 基础数值配置。
type EnemyConfig struct {
	ConfigID     uint32
	IsBoss       bool
	MaxHP        int32
	Radius       float32
	MoveSpeed    float32
	Damage       int32
	AttackCD     float32
	AttackRange  float32
	ProjSpeed    float32
	ProjRange    float32
	ProjConfigID uint32
}

var builtinEnemies = map[uint32]EnemyConfig{
	EnemyConfigMelee: {
		ConfigID:    EnemyConfigMelee,
		MaxHP:       60,
		Radius:      0.45,
		MoveSpeed:   2.2,
		Damage:      12,
		AttackCD:    1.0,
		AttackRange: 1.15,
	},
	EnemyConfigRanged: {
		ConfigID:     EnemyConfigRanged,
		MaxHP:        50,
		Radius:       0.45,
		MoveSpeed:    1.6,
		Damage:       10,
		AttackCD:     1.6,
		AttackRange:  12.0,
		ProjSpeed:    9.5,
		ProjRange:    16.0,
		ProjConfigID: ProjConfigEnemy,
	},
	EnemyConfigElite: {
		ConfigID:    EnemyConfigElite,
		MaxHP:       110,
		Radius:      0.55,
		MoveSpeed:   2.5,
		Damage:      18,
		AttackCD:    0.9,
		AttackRange: 1.35,
	},
}

var bossLordConfig = EnemyConfig{
	ConfigID:     BossConfigLord,
	IsBoss:       true,
	MaxHP:        450,
	Radius:       1.0,
	MoveSpeed:    1.8,
	Damage:       20,
	AttackCD:     1.8,
	AttackRange:  14.0,
	ProjSpeed:    8.5,
	ProjRange:    20.0,
	ProjConfigID: ProjConfigBoss,
}

func getEnemyConfig(cfgID uint32, isBoss bool) EnemyConfig {
	if isBoss {
		return bossLordConfig
	}
	if c, ok := builtinEnemies[cfgID]; ok {
		return c
	}
	return builtinEnemies[EnemyConfigMelee]
}

// EnemyEntity 房间内运行时的怪物或 Boss 实体。
type EnemyEntity struct {
	EntityID     uint32
	ConfigID     uint32
	IsBoss       bool
	Pos          Vec2
	Facing       float32
	AnimState    knight.EntityAnimState
	HP           int32
	MaxHP        int32
	Radius       float32
	MoveSpeed    float32
	Damage       int32
	AttackCD     float32
	AttackTimer  float32
	DamagedTimer float32
	Alive        bool

	// FSM 与 Boss 阶段状态
	AIState      EnemyAIState
	Phase2       bool
	PatternIndex int
	ChargeDir    Vec2
	ChargeTimer  float32
}

func (e *EnemyEntity) entityType() knight.EntityType {
	if e.IsBoss {
		return knight.EntityType_ENTITY_BOSS
	}
	return knight.EntityType_ENTITY_ENEMY
}

func (e *EnemyEntity) statusFlags() uint32 {
	if e.IsBoss && e.Phase2 {
		return StatusFlagBossPhase2
	}
	return 0
}

func (e *EnemyEntity) toProto() *knight.EntityState {
	state := e.AnimState
	if !e.Alive {
		state = knight.EntityAnimState_ANIM_DEAD
	} else if e.DamagedTimer > 0 {
		state = knight.EntityAnimState_ANIM_DAMAGED
	}
	return &knight.EntityState{
		EntityId:    e.EntityID,
		Type:        e.entityType(),
		PosX:        e.Pos.X,
		PosY:        e.Pos.Y,
		Facing:      e.Facing,
		State:       state,
		Hp:          e.HP,
		MaxHp:       e.MaxHP,
		ConfigId:    e.ConfigID,
		StatusFlags: e.statusFlags(),
	}
}

func (r *Room) spawnEnemy(spec EnemySpawnSpec) *EnemyEntity {
	cfg := getEnemyConfig(spec.ConfigID, spec.IsBoss)
	pos := resolveCollisions(spec.Pos, cfg.Radius, r.mapDef)
	e := &EnemyEntity{
		EntityID:    r.allocEntityID(),
		ConfigID:    cfg.ConfigID,
		IsBoss:      cfg.IsBoss,
		Pos:         pos,
		Facing:      180,
		AnimState:   knight.EntityAnimState_ANIM_IDLE,
		HP:          cfg.MaxHP,
		MaxHP:       cfg.MaxHP,
		Radius:      cfg.Radius,
		MoveSpeed:   cfg.MoveSpeed,
		Damage:      cfg.Damage,
		AttackCD:    cfg.AttackCD,
		AttackTimer: 0.6, // 进房短暂前摇，避免瞬秒
		Alive:       true,
		AIState:     AIStateChase,
	}
	r.enemies = append(r.enemies, e)
	return e
}

func (r *Room) tickEnemies(dtSec float32) {
	if len(r.enemies) == 0 {
		return
	}
	kept := r.enemies[:0]
	for _, e := range r.enemies {
		if !e.Alive {
			continue
		}
		if e.DamagedTimer > 0 {
			e.DamagedTimer -= dtSec
		}
		if e.AttackTimer > 0 {
			e.AttackTimer -= dtSec
		}

		target := r.nearestAlivePlayer(e.Pos)
		if target == nil {
			e.AIState = AIStateIdle
			e.AnimState = knight.EntityAnimState_ANIM_IDLE
			kept = append(kept, e)
			continue
		}

		if e.IsBoss {
			r.tickBossAI(e, target, dtSec)
		} else if e.ConfigID == EnemyConfigRanged {
			r.tickRangedAI(e, target, dtSec)
		} else {
			r.tickMeleeAI(e, target, dtSec)
		}
		kept = append(kept, e)
	}
	clear(r.enemies[len(kept):])
	r.enemies = kept
}

func (r *Room) nearestAlivePlayer(from Vec2) *PlayerEntity {
	var best *PlayerEntity
	bestD2 := float32(math.MaxFloat32)
	for _, p := range r.players {
		if !p.Alive {
			continue
		}
		d2 := distSq(from, p.Pos)
		if d2 < bestD2 {
			bestD2 = d2
			best = p
		}
	}
	return best
}

func (r *Room) moveEnemyToward(e *EnemyEntity, dir Vec2, speed, dtSec float32) {
	next := Vec2{
		X: e.Pos.X + dir.X*speed*dtSec,
		Y: e.Pos.Y + dir.Y*speed*dtSec,
	}
	e.Pos = resolveCollisions(next, e.Radius, r.mapDef)
	e.AnimState = knight.EntityAnimState_ANIM_MOVING
}

func (r *Room) tickMeleeAI(e *EnemyEntity, target *PlayerEntity, dtSec float32) {
	cfg := getEnemyConfig(e.ConfigID, false)
	e.Facing = angleDeg(e.Pos, target.Pos)
	d := float32(math.Sqrt(float64(distSq(e.Pos, target.Pos))))

	if d <= cfg.AttackRange {
		e.AIState = AIStateAttack
		if e.AttackTimer <= 0 {
			e.AttackTimer = e.AttackCD
			e.AnimState = knight.EntityAnimState_ANIM_ATTACKING
			r.damagePlayer(e.EntityID, target, e.Damage, target.Pos)
		} else {
			e.AnimState = knight.EntityAnimState_ANIM_IDLE
		}
		return
	}

	e.AIState = AIStateChase
	dir := Vec2{X: (target.Pos.X - e.Pos.X) / d, Y: (target.Pos.Y - e.Pos.Y) / d}
	r.moveEnemyToward(e, dir, e.MoveSpeed, dtSec)
}

func (r *Room) tickRangedAI(e *EnemyEntity, target *PlayerEntity, dtSec float32) {
	cfg := getEnemyConfig(e.ConfigID, false)
	e.Facing = angleDeg(e.Pos, target.Pos)
	d := float32(math.Sqrt(float64(distSq(e.Pos, target.Pos))))

	const preferredMin float32 = 4.5
	const preferredMax float32 = 9.5

	if d > preferredMax {
		e.AIState = AIStateChase
		dir := Vec2{X: (target.Pos.X - e.Pos.X) / d, Y: (target.Pos.Y - e.Pos.Y) / d}
		r.moveEnemyToward(e, dir, e.MoveSpeed, dtSec)
	} else if d < preferredMin && d > 0.01 {
		e.AIState = AIStateKeepDistance
		dir := Vec2{X: (e.Pos.X - target.Pos.X) / d, Y: (e.Pos.Y - target.Pos.Y) / d}
		r.moveEnemyToward(e, dir, e.MoveSpeed*0.85, dtSec)
	} else {
		e.AnimState = knight.EntityAnimState_ANIM_IDLE
	}

	if d <= cfg.AttackRange && e.AttackTimer <= 0 {
		e.AIState = AIStateAttack
		e.AttackTimer = e.AttackCD
		e.AnimState = knight.EntityAnimState_ANIM_ATTACKING
		proj := r.createProjectile(e.EntityID, 0, false, cfg.ProjConfigID, e.Pos, e.Facing, cfg.ProjSpeed, cfg.ProjRange, 0.25, e.Damage, 0)
		r.broadcastEntitySpawn([]*knight.EntityState{proj.toProto()})
	}
}

func (r *Room) tickBossAI(e *EnemyEntity, target *PlayerEntity, dtSec float32) {
	cfg := bossLordConfig
	speed := e.MoveSpeed
	cdScale := float32(1.0)
	if e.Phase2 {
		speed *= 1.3
		cdScale = 0.75
	}

	// 正在冲锋状态
	if e.AIState == AIStateBossCharge {
		e.ChargeTimer -= dtSec
		r.moveEnemyToward(e, e.ChargeDir, speed*2.6, dtSec)
		// 冲锋接触伤害
		for _, p := range r.players {
			if p.Alive && p.InvulnTimer <= 0 && circlesOverlap(e.Pos, e.Radius, p.Pos, r.playerRadius+0.25) {
				r.damagePlayer(e.EntityID, p, e.Damage, p.Pos)
				e.ChargeTimer = 0
				break
			}
		}
		if e.ChargeTimer <= 0 {
			e.AIState = AIStateChase
			e.AttackTimer = cfg.AttackCD * cdScale
		}
		return
	}

	e.Facing = angleDeg(e.Pos, target.Pos)
	d := float32(math.Sqrt(float64(distSq(e.Pos, target.Pos))))

	if d > 5.0 {
		dir := Vec2{X: (target.Pos.X - e.Pos.X) / d, Y: (target.Pos.Y - e.Pos.Y) / d}
		r.moveEnemyToward(e, dir, speed, dtSec)
	} else {
		e.AnimState = knight.EntityAnimState_ANIM_IDLE
	}

	if e.AttackTimer > 0 {
		return
	}

	// 攻击模式轮换：
	// 一阶段：0=3发扇形弹幕, 1=冲锋扑击
	// 二阶段：0=5发扇形弹幕, 1=冲锋扑击, 2=8方向环形弹幕
	modeCount := 2
	if e.Phase2 {
		modeCount = 3
	}
	pattern := e.PatternIndex % modeCount
	e.PatternIndex++

	switch pattern {
	case 0: // 扇形弹幕
		e.AIState = AIStateAttack
		e.AnimState = knight.EntityAnimState_ANIM_ATTACKING
		e.AttackTimer = cfg.AttackCD * cdScale
		count := 3
		spread := float32(30.0)
		if e.Phase2 {
			count = 5
			spread = 50.0
		}
		spawned := make([]*knight.EntityState, 0, count)
		for i := 0; i < count; i++ {
			offset := -spread/2 + spread*float32(i)/float32(count-1)
			proj := r.createProjectile(e.EntityID, 0, false, cfg.ProjConfigID, e.Pos, e.Facing+offset, cfg.ProjSpeed, cfg.ProjRange, 0.30, e.Damage, 0)
			spawned = append(spawned, proj.toProto())
		}
		r.broadcastEntitySpawn(spawned)

	case 1: // 冲锋扑击
		e.AIState = AIStateBossCharge
		e.AnimState = knight.EntityAnimState_ANIM_ATTACKING
		e.ChargeDir = degToDir(e.Facing)
		e.ChargeTimer = 0.65

	case 2: // 二阶段专属：8 方向环形弹幕
		e.AIState = AIStateAttack
		e.AnimState = knight.EntityAnimState_ANIM_ATTACKING
		e.AttackTimer = cfg.AttackCD * cdScale
		const ringCount = 8
		spawned := make([]*knight.EntityState, 0, ringCount)
		for i := 0; i < ringCount; i++ {
			ang := float32(i) * (360.0 / ringCount)
			proj := r.createProjectile(e.EntityID, 0, false, cfg.ProjConfigID, e.Pos, ang, cfg.ProjSpeed*0.9, cfg.ProjRange, 0.30, e.Damage, 0)
			spawned = append(spawned, proj.toProto())
		}
		r.broadcastEntitySpawn(spawned)
	}
}
