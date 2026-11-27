package world

import (
	"math"

	knight "knight-server/protogen"
)

// 主动技能 ID 常量（与 SkillEvent.skill_id 对应）。
const (
	SkillConfigDashSlash uint32 = 1 // 冲刺闪避斩（默认角色技能）
	SkillConfigShield    uint32 = 2 // 圣盾守护
	SkillConfigNovaBurst uint32 = 3 // 范围爆发冲击波
	SkillConfigHealAura  uint32 = 4 // 群体治疗光环
)

// SkillStrategy 可扩展的主动技能策略接口。
type SkillStrategy interface {
	ID() uint32
	Cooldown() float32
	Execute(r *Room, p *PlayerEntity, cmd *knight.InputCmd) (posX, posY, dirAngle float32)
}

var builtinSkills = map[uint32]SkillStrategy{
	SkillConfigDashSlash: skillDashSlash{},
	SkillConfigShield:    skillShield{},
	SkillConfigNovaBurst: skillNovaBurst{},
	SkillConfigHealAura:  skillHealAura{},
}

// GetSkillStrategy 按 skill_id 查找技能策略，未命中时回退默认技能 1。
func GetSkillStrategy(id uint32) SkillStrategy {
	if s, ok := builtinSkills[id]; ok {
		return s
	}
	return builtinSkills[SkillConfigDashSlash]
}

// triggerPlayerSkill 校验冷却并执行玩家主动技能，广播 SkillEvent。
func (r *Room) triggerPlayerSkill(p *PlayerEntity, cmd *knight.InputCmd) {
	if !p.Alive || p.SkillCD > 0 {
		return
	}
	strat := GetSkillStrategy(p.SkillID)
	cd := strat.Cooldown()
	p.SkillCD = cd
	p.SkillCDTotal = cd
	p.AnimState = knight.EntityAnimState_ANIM_ATTACKING

	px, py, dir := strat.Execute(r, p, cmd)
	r.broadcastReliable(&knight.Envelope{Payload: &knight.Envelope_SkillEvent{
		SkillEvent: &knight.SkillEvent{
			CasterId:    p.EntityID,
			SkillId:     strat.ID(),
			PosX:        px,
			PosY:        py,
			DirAngle:    dir,
			CooldownSec: cd,
		},
	}})
}

// skillDashSlash 技能 1：朝移动/瞄准方向冲刺 3.5u + 0.5s 无敌帧 + 落点 2.2u 范围斩击（25 伤）。
type skillDashSlash struct{}

func (skillDashSlash) ID() uint32        { return SkillConfigDashSlash }
func (skillDashSlash) Cooldown() float32 { return 5.0 }

func (skillDashSlash) Execute(r *Room, p *PlayerEntity, cmd *knight.InputCmd) (float32, float32, float32) {
	mx, my := sanitizeMove(cmd.MoveX, cmd.MoveY)
	var dir Vec2
	dirAngle := p.Facing
	if mx != 0 || my != 0 {
		mag := float32(math.Hypot(float64(mx), float64(my)))
		dir = Vec2{X: mx / mag, Y: my / mag}
		dirAngle = float32(math.Atan2(float64(dir.Y), float64(dir.X)) * 180.0 / math.Pi)
	} else {
		dir = degToDir(p.Facing)
	}

	// 分步推进碰撞检测，防止高速冲刺穿透薄墙
	const totalDist float32 = 3.5
	const steps = 7
	stepDist := totalDist / steps
	for i := 0; i < steps; i++ {
		next := Vec2{X: p.Pos.X + dir.X*stepDist, Y: p.Pos.Y + dir.Y*stepDist}
		p.Pos = resolveCollisions(next, r.playerRadius, r.mapDef)
	}

	p.InvulnTimer = 0.5
	dmg := int32(25)
	if p.AtkBuffTimer > 0 {
		dmg = dmg * 3 / 2
	}
	r.triggerAOE(p.EntityID, p.UID, true, p.Pos, 2.2, dmg)
	return p.Pos.X, p.Pos.Y, dirAngle
}

// skillShield 技能 2：圣盾守护（4.0s 50% 减伤）。
type skillShield struct{}

func (skillShield) ID() uint32        { return SkillConfigShield }
func (skillShield) Cooldown() float32 { return 8.0 }

func (skillShield) Execute(_ *Room, p *PlayerEntity, _ *knight.InputCmd) (float32, float32, float32) {
	if p.ShieldTimer < 4.0 {
		p.ShieldTimer = 4.0
	}
	return p.Pos.X, p.Pos.Y, p.Facing
}

// skillNovaBurst 技能 3：以自身为中心 4.0u 范围爆发（40 伤害）。
type skillNovaBurst struct{}

func (skillNovaBurst) ID() uint32        { return SkillConfigNovaBurst }
func (skillNovaBurst) Cooldown() float32 { return 7.0 }

func (skillNovaBurst) Execute(r *Room, p *PlayerEntity, _ *knight.InputCmd) (float32, float32, float32) {
	dmg := int32(40)
	if p.AtkBuffTimer > 0 {
		dmg = dmg * 3 / 2
	}
	r.triggerAOE(p.EntityID, p.UID, true, p.Pos, 4.0, dmg)
	return p.Pos.X, p.Pos.Y, p.Facing
}

// skillHealAura 技能 4：6.0u 范围内所有存活队友恢复 35 HP。
type skillHealAura struct{}

func (skillHealAura) ID() uint32        { return SkillConfigHealAura }
func (skillHealAura) Cooldown() float32 { return 10.0 }

func (skillHealAura) Execute(r *Room, p *PlayerEntity, _ *knight.InputCmd) (float32, float32, float32) {
	const healRadius float32 = 6.0
	const healAmt int32 = 35
	for _, ally := range r.players {
		if !ally.Alive {
			continue
		}
		if distSq(p.Pos, ally.Pos) <= healRadius*healRadius {
			ally.HP += healAmt
			if ally.HP > ally.MaxHP {
				ally.HP = ally.MaxHP
			}
		}
	}
	return p.Pos.X, p.Pos.Y, p.Facing
}
