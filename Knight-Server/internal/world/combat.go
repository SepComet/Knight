package world

import (
	"math"

	knight "knight-server/protogen"
)

// EntityState.status_flags 位掩码（对齐 M3 跨端契约）。
const (
	StatusFlagInvuln     uint32 = 1 << 0 // 无敌/冲刺帧
	StatusFlagShield     uint32 = 1 << 1 // 护盾减伤（50%）
	StatusFlagAtkBuff    uint32 = 1 << 2 // 攻击增益（+50% 伤害）
	StatusFlagBossPhase2 uint32 = 1 << 3 // Boss 二阶段狂暴
)

// 实体 config_id 常量（按 EntityType 分域，非 0）。
const (
	WeaponConfigRifle   uint32 = 1 // 单发步枪
	WeaponConfigShotgun uint32 = 2 // 散射霰弹枪
	WeaponConfigBurst   uint32 = 3 // 三连发脉冲枪
	WeaponConfigGrenade uint32 = 4 // 范围爆炸榴弹

	ProjConfigRifle   uint32 = 1
	ProjConfigShotgun uint32 = 2
	ProjConfigBurst   uint32 = 3
	ProjConfigGrenade uint32 = 4
	ProjConfigEnemy   uint32 = 11
	ProjConfigBoss    uint32 = 12

	PickupConfigHealth  uint32 = 1 // 回血药水 +35 HP
	PickupConfigAtkBuff uint32 = 2 // 攻击增益 +50% / 15s
	PickupConfigShield  uint32 = 3 // 护盾核心 50%减伤 / 5s

	PickupInteractRadius float32 = 2.2
	PickupHealthAmount   int32   = 35
	PickupAtkBuffSec     float32 = 15.0
	PickupShieldSec      float32 = 5.0
)

// FireMode 武器弹道模式。
type FireMode uint8

const (
	FireSingle FireMode = iota
	FireSpread
	FireBurst
	FireAOE
)

// WeaponConfig 权威武器数值配置。
type WeaponConfig struct {
	ID            uint32
	Mode          FireMode
	CooldownSec   float32
	Damage        int32
	ProjSpeed     float32
	Range         float32
	ProjRadius    float32
	ProjConfigID  uint32
	PelletCount   int     // 散射弹丸数
	SpreadDeg     float32 // 散射总夹角（度）
	BurstCount    int     // 连发弹数
	BurstInterval float32 // 连发间隔（秒）
	AOERadius     float32 // 爆炸范围半径（>0 时命中/终点触发范围伤害）
}

var builtinWeapons = map[uint32]WeaponConfig{
	WeaponConfigRifle: {
		ID:           WeaponConfigRifle,
		Mode:         FireSingle,
		CooldownSec:  0.25,
		Damage:       18,
		ProjSpeed:    14.0,
		Range:        18.0,
		ProjRadius:   0.25,
		ProjConfigID: ProjConfigRifle,
	},
	WeaponConfigShotgun: {
		ID:           WeaponConfigShotgun,
		Mode:         FireSpread,
		CooldownSec:  0.55,
		Damage:       11,
		ProjSpeed:    13.0,
		Range:        12.0,
		ProjRadius:   0.22,
		ProjConfigID: ProjConfigShotgun,
		PelletCount:  3,
		SpreadDeg:    24.0,
	},
	WeaponConfigBurst: {
		ID:            WeaponConfigBurst,
		Mode:          FireBurst,
		CooldownSec:   0.50,
		Damage:        12,
		ProjSpeed:     16.0,
		Range:         18.0,
		ProjRadius:    0.22,
		ProjConfigID:  ProjConfigBurst,
		BurstCount:    3,
		BurstInterval: 0.08,
	},
	WeaponConfigGrenade: {
		ID:           WeaponConfigGrenade,
		Mode:         FireAOE,
		CooldownSec:  0.90,
		Damage:       35,
		ProjSpeed:    9.0,
		Range:        14.0,
		ProjRadius:   0.30,
		ProjConfigID: ProjConfigGrenade,
		AOERadius:    2.5,
	},
}

// GetWeaponConfig 按 weapon_id 查找武器配置，未知时回退默认步枪。
func GetWeaponConfig(id uint32) WeaponConfig {
	if w, ok := builtinWeapons[id]; ok {
		return w
	}
	return builtinWeapons[WeaponConfigRifle]
}

// ProjectileEntity 飞行中的弹道实体。
type ProjectileEntity struct {
	EntityID      uint32
	OwnerID       uint32
	OwnerUID      uint64 // 非 0 表示玩家发射，用于击杀/伤害归因
	FromPlayer    bool
	ConfigID      uint32
	Pos           Vec2
	Vel           Vec2
	Facing        float32
	Damage        int32
	Radius        float32
	RemainingDist float32
	AOERadius     float32
}

// PickupEntity 地面可拾取道具实体。
type PickupEntity struct {
	EntityID uint32
	ConfigID uint32
	Pos      Vec2
}

func (p *ProjectileEntity) toProto() *knight.EntityState {
	return &knight.EntityState{
		EntityId: p.EntityID,
		Type:     knight.EntityType_ENTITY_PROJECTILE,
		PosX:     p.Pos.X,
		PosY:     p.Pos.Y,
		Facing:   p.Facing,
		State:    knight.EntityAnimState_ANIM_MOVING,
		Hp:       1,
		MaxHp:    1,
		ConfigId: p.ConfigID,
	}
}

func (pk *PickupEntity) toProto() *knight.EntityState {
	return &knight.EntityState{
		EntityId: pk.EntityID,
		Type:     knight.EntityType_ENTITY_PICKUP,
		PosX:     pk.Pos.X,
		PosY:     pk.Pos.Y,
		Facing:   0,
		State:    knight.EntityAnimState_ANIM_IDLE,
		Hp:       1,
		MaxHp:    1,
		ConfigId: pk.ConfigID,
	}
}

// tryFireWeapon 玩家按住 BTN_ATTACK 时校验冷却并按武器模式发射弹道。
func (r *Room) tryFireWeapon(p *PlayerEntity) {
	if !p.Alive || p.WeaponCD > 0 {
		return
	}
	w := GetWeaponConfig(p.WeaponID)
	p.WeaponCD = w.CooldownSec
	p.AnimState = knight.EntityAnimState_ANIM_ATTACKING
	dmg := w.Damage
	if p.AtkBuffTimer > 0 {
		dmg = dmg * 3 / 2
	}

	switch w.Mode {
	case FireSpread:
		count := w.PelletCount
		if count < 1 {
			count = 1
		}
		var spawned []*knight.EntityState
		for i := 0; i < count; i++ {
			offset := float32(0)
			if count > 1 {
				offset = -w.SpreadDeg/2 + w.SpreadDeg*float32(i)/float32(count-1)
			}
			proj := r.createProjectile(p.EntityID, p.UID, true, w.ProjConfigID, p.Pos, p.Facing+offset, w.ProjSpeed, w.Range, w.ProjRadius, dmg, 0)
			spawned = append(spawned, proj.toProto())
		}
		r.broadcastEntitySpawn(spawned)

	case FireBurst:
		// 立即发射第 1 发，剩余连发入队按间隔发射
		proj := r.createProjectile(p.EntityID, p.UID, true, w.ProjConfigID, p.Pos, p.Facing, w.ProjSpeed, w.Range, w.ProjRadius, dmg, 0)
		r.broadcastEntitySpawn([]*knight.EntityState{proj.toProto()})
		if w.BurstCount > 1 {
			p.BurstRemaining = w.BurstCount - 1
			p.BurstTimer = w.BurstInterval
		}

	case FireAOE:
		proj := r.createProjectile(p.EntityID, p.UID, true, w.ProjConfigID, p.Pos, p.Facing, w.ProjSpeed, w.Range, w.ProjRadius, dmg, w.AOERadius)
		r.broadcastEntitySpawn([]*knight.EntityState{proj.toProto()})

	default: // FireSingle
		proj := r.createProjectile(p.EntityID, p.UID, true, w.ProjConfigID, p.Pos, p.Facing, w.ProjSpeed, w.Range, w.ProjRadius, dmg, 0)
		r.broadcastEntitySpawn([]*knight.EntityState{proj.toProto()})
	}
}

func (r *Room) tickPlayerBurst(p *PlayerEntity, dtSec float32) {
	if !p.Alive || p.BurstRemaining <= 0 {
		p.BurstRemaining = 0
		return
	}
	p.BurstTimer -= dtSec
	if p.BurstTimer > 0 {
		return
	}
	w := GetWeaponConfig(p.WeaponID)
	dmg := w.Damage
	if p.AtkBuffTimer > 0 {
		dmg = dmg * 3 / 2
	}
	proj := r.createProjectile(p.EntityID, p.UID, true, w.ProjConfigID, p.Pos, p.Facing, w.ProjSpeed, w.Range, w.ProjRadius, dmg, w.AOERadius)
	r.broadcastEntitySpawn([]*knight.EntityState{proj.toProto()})
	p.BurstRemaining--
	p.BurstTimer = w.BurstInterval
}

func (r *Room) createProjectile(ownerID uint32, ownerUID uint64, fromPlayer bool, cfgID uint32, origin Vec2, angle float32, speed, maxDist, radius float32, damage int32, aoeRadius float32) *ProjectileEntity {
	dir := degToDir(angle)
	spawnPos := Vec2{
		X: origin.X + dir.X*(radius+0.25),
		Y: origin.Y + dir.Y*(radius+0.25),
	}
	proj := &ProjectileEntity{
		EntityID:      r.allocEntityID(),
		OwnerID:       ownerID,
		OwnerUID:      ownerUID,
		FromPlayer:    fromPlayer,
		ConfigID:      cfgID,
		Pos:           spawnPos,
		Vel:           Vec2{X: dir.X * speed, Y: dir.Y * speed},
		Facing:        angle,
		Damage:        damage,
		Radius:        radius,
		RemainingDist: maxDist,
		AOERadius:     aoeRadius,
	}
	r.projectiles = append(r.projectiles, proj)
	return proj
}

func (r *Room) tickProjectiles(dtSec float32) {
	if len(r.projectiles) == 0 {
		return
	}
	var despawnLifetime []uint32
	var despawnKilled []uint32

	kept := r.projectiles[:0]
	for _, proj := range r.projectiles {
		stepX := proj.Vel.X * dtSec
		stepY := proj.Vel.Y * dtSec
		proj.Pos.X += stepX
		proj.Pos.Y += stepY
		proj.RemainingDist -= float32(hypot(stepX, stepY))

		// 1. 碰墙检测
		if circleHitsAnyWall(proj.Pos, proj.Radius, r.mapDef) {
			if proj.AOERadius > 0 {
				r.triggerAOE(proj.OwnerID, proj.OwnerUID, proj.FromPlayer, proj.Pos, proj.AOERadius, proj.Damage)
			}
			despawnLifetime = append(despawnLifetime, proj.EntityID)
			continue
		}

		// 2. 命中实体检测
		hit := false
		if proj.FromPlayer {
			for _, e := range r.enemies {
				if !e.Alive {
					continue
				}
				if circlesOverlap(proj.Pos, proj.Radius, e.Pos, e.Radius) {
					hit = true
					if proj.AOERadius > 0 {
						r.triggerAOE(proj.OwnerID, proj.OwnerUID, true, proj.Pos, proj.AOERadius, proj.Damage)
					} else {
						r.damageEnemy(proj.OwnerID, proj.OwnerUID, e, proj.Damage, proj.Pos)
					}
					break
				}
			}
		} else {
			for _, p := range r.players {
				if !p.Alive || p.InvulnTimer > 0 {
					continue
				}
				if circlesOverlap(proj.Pos, proj.Radius, p.Pos, r.playerRadius) {
					hit = true
					r.damagePlayer(proj.OwnerID, p, proj.Damage, proj.Pos)
					break
				}
			}
		}

		if hit {
			despawnKilled = append(despawnKilled, proj.EntityID)
			continue
		}

		// 3. 射程到期
		if proj.RemainingDist <= 0 {
			if proj.AOERadius > 0 {
				r.triggerAOE(proj.OwnerID, proj.OwnerUID, proj.FromPlayer, proj.Pos, proj.AOERadius, proj.Damage)
			}
			despawnLifetime = append(despawnLifetime, proj.EntityID)
			continue
		}

		kept = append(kept, proj)
	}
	clear(r.projectiles[len(kept):])
	r.projectiles = kept

	if len(despawnKilled) > 0 {
		r.broadcastEntityDespawn(despawnKilled, knight.DespawnReason_DESPAWN_KILLED)
	}
	if len(despawnLifetime) > 0 {
		r.broadcastEntityDespawn(despawnLifetime, knight.DespawnReason_DESPAWN_LIFETIME_END)
	}
}

func (r *Room) triggerAOE(attackerID uint32, attackerUID uint64, fromPlayer bool, center Vec2, radius float32, damage int32) {
	if fromPlayer {
		for _, e := range r.enemies {
			if !e.Alive {
				continue
			}
			if circlesOverlap(center, radius, e.Pos, e.Radius) {
				r.damageEnemy(attackerID, attackerUID, e, damage, e.Pos)
			}
		}
		return
	}
	for _, p := range r.players {
		if !p.Alive || p.InvulnTimer > 0 {
			continue
		}
		if circlesOverlap(center, radius, p.Pos, r.playerRadius) {
			r.damagePlayer(attackerID, p, damage, p.Pos)
		}
	}
}

func (r *Room) damageEnemy(attackerID uint32, attackerUID uint64, e *EnemyEntity, rawDamage int32, hitPos Vec2) {
	if !e.Alive || rawDamage <= 0 {
		return
	}
	actual := rawDamage
	if actual > e.HP {
		actual = e.HP
	}
	e.HP -= actual
	e.DamagedTimer = 0.15

	if attackerUID != 0 {
		if p, ok := r.byUID[attackerUID]; ok {
			p.DamageDealt += actual
		}
	}

	r.broadcastReliable(&knight.Envelope{Payload: &knight.Envelope_DamageEvent{
		DamageEvent: &knight.DamageEvent{
			AttackerId: attackerID,
			TargetId:   e.EntityID,
			PosX:       hitPos.X,
			PosY:       hitPos.Y,
			NewHp:      e.HP,
			Damage:     actual,
		},
	}})

	if e.IsBoss && !e.Phase2 && e.HP > 0 && e.HP*2 <= e.MaxHP {
		e.Phase2 = true
	}

	if e.HP <= 0 {
		e.HP = 0
		e.Alive = false
		if attackerUID != 0 {
			if p, ok := r.byUID[attackerUID]; ok {
				p.Kills++
			}
		}
		r.broadcastEntityDespawn([]uint32{e.EntityID}, knight.DespawnReason_DESPAWN_KILLED)
		if !e.IsBoss {
			r.maybeDropPickup(e.Pos)
		}
	}
}

func (r *Room) damagePlayer(attackerID uint32, p *PlayerEntity, rawDamage int32, hitPos Vec2) {
	if !p.Alive || p.InvulnTimer > 0 || rawDamage <= 0 {
		return
	}
	dmg := rawDamage
	if p.ShieldTimer > 0 {
		dmg = (dmg + 1) / 2 // 50% 减伤（向上取整，至少 1）
	}
	actual := dmg
	if actual > p.HP {
		actual = p.HP
	}
	p.HP -= actual
	p.DamageTaken += actual
	p.DamagedTimer = 0.2

	r.broadcastReliable(&knight.Envelope{Payload: &knight.Envelope_DamageEvent{
		DamageEvent: &knight.DamageEvent{
			AttackerId: attackerID,
			TargetId:   p.EntityID,
			PosX:       hitPos.X,
			PosY:       hitPos.Y,
			NewHp:      p.HP,
			Damage:     actual,
		},
	}})

	if p.HP <= 0 {
		p.HP = 0
		p.Alive = false
		p.AnimState = knight.EntityAnimState_ANIM_DEAD
		p.BurstRemaining = 0
		r.broadcastEntityDespawn([]uint32{p.EntityID}, knight.DespawnReason_DESPAWN_KILLED)
	}
}

func (r *Room) maybeDropPickup(pos Vec2) {
	if r.rng == nil {
		return
	}
	dropChance := 0.40
	if r.mapDef.Role == RoleElite {
		dropChance = 0.75
	}
	if r.rng.Float64() > dropChance {
		return
	}
	cfgID := PickupConfigHealth
	roll := r.rng.IntN(10)
	if roll >= 8 {
		cfgID = PickupConfigAtkBuff
	} else if roll >= 6 {
		cfgID = PickupConfigShield
	}
	pk := &PickupEntity{
		EntityID: r.allocEntityID(),
		ConfigID: cfgID,
		Pos:      resolveCollisions(pos, 0.35, r.mapDef),
	}
	r.pickups = append(r.pickups, pk)
	r.broadcastEntitySpawn([]*knight.EntityState{pk.toProto()})
}

// tryPickupInteract 玩家触发 BTN_INTERACT 边沿时，拾取半径内最近的合法道具。
func (r *Room) tryPickupInteract(p *PlayerEntity) {
	if !p.Alive || len(r.pickups) == 0 {
		return
	}
	bestIdx := -1
	bestDistSq := PickupInteractRadius * PickupInteractRadius
	for i, pk := range r.pickups {
		d2 := distSq(p.Pos, pk.Pos)
		if d2 <= bestDistSq {
			bestDistSq = d2
			bestIdx = i
		}
	}
	if bestIdx < 0 {
		return
	}
	pk := r.pickups[bestIdx]
	r.pickups = append(r.pickups[:bestIdx], r.pickups[bestIdx+1:]...)

	var healed int32
	switch pk.ConfigID {
	case PickupConfigHealth:
		before := p.HP
		p.HP += PickupHealthAmount
		if p.HP > p.MaxHP {
			p.HP = p.MaxHP
		}
		healed = p.HP - before
	case PickupConfigAtkBuff:
		p.AtkBuffTimer = PickupAtkBuffSec
	case PickupConfigShield:
		p.ShieldTimer = PickupShieldSec
	}

	r.broadcastReliable(&knight.Envelope{Payload: &knight.Envelope_ItemPicked{
		ItemPicked: &knight.ItemPicked{
			ItemEntityId: pk.EntityID,
			Uid:          p.UID,
			HealedHp:     healed,
			ItemConfigId: pk.ConfigID,
		},
	}})
	r.broadcastEntityDespawn([]uint32{pk.EntityID}, knight.DespawnReason_DESPAWN_CONSUMED)
}

func hypot(x, y float32) float64 {
	return math.Hypot(float64(x), float64(y))
}
