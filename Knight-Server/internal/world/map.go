// Package world 实现服务端权威战斗世界与地牢房间模拟（Docs/protocol.md §5.4~§6.2，消息域 64~127）。
//
// 并发模型（对齐 internal/tick）：
//   - Manager 在 gate goroutine 上接收消息（Route）与大厅事件（GameStarted/MemberLeft）；
//   - 每个对局 Room 作为 tick.Tickable 注册到 tick.Runner，其内部状态仅在模拟 goroutine 上读写，无锁；
//   - 跨 goroutine 投递（RoomLoaded / InputBatch / 玩家离场）统一经 runner.Post 在 tick 边界串行执行。
package world

import "math/rand/v2"

// Vec2 二维世界坐标（单位 = 地面 tile；+X 向右，+Y 向上，见 Docs/protocol.md §8）。
type Vec2 struct {
	X float32
	Y float32
}

// Rect 轴对齐包围盒（AABB），用于房间边界、墙体碰撞与出口触发区。
type Rect struct {
	MinX float32
	MinY float32
	MaxX float32
	MaxY float32
}

// RoomRole 预制房间模板角色分类（程序化地牢池模式）。
type RoomRole uint8

const (
	RoleStart  RoomRole = iota // 出生/前厅房
	RoleNormal                 // 普通战斗房
	RoleSupply                 // 补给宝箱房（0 怪，进房门即开）
	RoleElite                  // 精英战斗房
	RoleBoss                   // Boss 房
)

// EnemySpawnSpec 房间内怪物/Boss 出生配置。
type EnemySpawnSpec struct {
	Pos      Vec2
	ConfigID uint32
	IsBoss   bool
}

// PickupSpawnSpec 房间内初始道具配置。
type PickupSpawnSpec struct {
	Pos      Vec2
	ConfigID uint32
}

// MapDef 单个地牢房间的几何、出生点、怪物/道具与出口区定义（世界单位 = tile）。
type MapDef struct {
	ID           uint32
	Name         string
	Role         RoomRole
	Width        float32
	Height       float32
	Walls        []Rect // 外围边界墙 + 内部障碍墙
	Spawns       []Vec2 // 玩家出生点列表（按加入顺序分配）
	ExitZone     Rect   // 出口触发区域（右侧中央门）
	EnemySpawns  []EnemySpawnSpec
	PickupSpawns []PickupSpawnSpec
}

// Map1ID 首张战斗房间的 map_id；BossMapID Boss 房 map_id。
const (
	Map1ID    uint32 = 1
	BossMapID uint32 = 9
)

var (
	// 统一 30×20 外围 1 tile 厚边界墙，围出 [1, 29] × [1, 19] 可行走区域。
	outerBoundaryWalls = []Rect{
		{MinX: 0, MinY: 0, MaxX: 30, MaxY: 1},   // 底墙
		{MinX: 0, MinY: 19, MaxX: 30, MaxY: 20}, // 顶墙
		{MinX: 0, MinY: 0, MaxX: 1, MaxY: 20},   // 左墙
		{MinX: 29, MinY: 0, MaxX: 30, MaxY: 20}, // 右墙
	}

	// 统一出口区域（右侧中央门，所有 9 张预制房保持同构）。
	defaultExitZone = Rect{MinX: 26.5, MinY: 8.5, MaxX: 29.0, MaxY: 11.5}

	// 通用左侧安全出生点。
	defaultSpawns = []Vec2{
		{X: 3, Y: 11.5},
		{X: 3, Y: 8.5},
		{X: 5, Y: 11.5},
		{X: 5, Y: 8.5},
	}
)

func withOuterWalls(inner ...Rect) []Rect {
	walls := make([]Rect, 0, len(outerBoundaryWalls)+len(inner))
	walls = append(walls, outerBoundaryWalls...)
	walls = append(walls, inner...)
	return walls
}

// map1Def Map 1（出生前厅，保留 M1/M2 原障碍与出生点几何完全兼容）。
var map1Def = MapDef{
	ID:     1,
	Name:   "Entry Hall",
	Role:   RoleStart,
	Width:  30,
	Height: 20,
	Walls: withOuterWalls(
		Rect{MinX: 10, MinY: 9, MaxX: 11, MaxY: 15},  // 左中竖墙
		Rect{MinX: 15, MinY: 8, MaxX: 20, MaxY: 9},   // 中下横墙
		Rect{MinX: 23, MinY: 11, MaxX: 24, MaxY: 15}, // 右上竖墙
	),
	Spawns: []Vec2{
		{X: 4, Y: 12},
		{X: 4, Y: 8},
		{X: 6, Y: 12},
		{X: 6, Y: 8},
	},
	ExitZone: defaultExitZone,
	EnemySpawns: []EnemySpawnSpec{
		{Pos: Vec2{X: 18, Y: 14}, ConfigID: EnemyConfigMelee},
		{Pos: Vec2{X: 21, Y: 6}, ConfigID: EnemyConfigMelee},
	},
}

var builtinMaps = map[uint32]MapDef{
	1: map1Def,
	2: {
		ID:       2,
		Name:     "Four Pillars Temple",
		Role:     RoleNormal,
		Width:    30,
		Height:   20,
		Walls: withOuterWalls(
			Rect{MinX: 9, MinY: 13, MaxX: 11, MaxY: 15},
			Rect{MinX: 9, MinY: 5, MaxX: 11, MaxY: 7},
			Rect{MinX: 19, MinY: 13, MaxX: 21, MaxY: 15},
			Rect{MinX: 19, MinY: 5, MaxX: 21, MaxY: 7},
		),
		Spawns:   defaultSpawns,
		ExitZone: defaultExitZone,
		EnemySpawns: []EnemySpawnSpec{
			{Pos: Vec2{X: 15, Y: 14}, ConfigID: EnemyConfigMelee},
			{Pos: Vec2{X: 15, Y: 6}, ConfigID: EnemyConfigMelee},
			{Pos: Vec2{X: 23, Y: 10}, ConfigID: EnemyConfigRanged},
		},
	},
	3: {
		ID:       3,
		Name:     "Cross Corridor",
		Role:     RoleNormal,
		Width:    30,
		Height:   20,
		Walls: withOuterWalls(
			Rect{MinX: 14, MinY: 12, MaxX: 16, MaxY: 17},
			Rect{MinX: 14, MinY: 3, MaxX: 16, MaxY: 8},
			Rect{MinX: 9, MinY: 9, MaxX: 13, MaxY: 11},
			Rect{MinX: 17, MinY: 9, MaxX: 21, MaxY: 11},
		),
		Spawns:   defaultSpawns,
		ExitZone: defaultExitZone,
		EnemySpawns: []EnemySpawnSpec{
			{Pos: Vec2{X: 19, Y: 15}, ConfigID: EnemyConfigMelee},
			{Pos: Vec2{X: 19, Y: 5}, ConfigID: EnemyConfigRanged},
			{Pos: Vec2{X: 24, Y: 10}, ConfigID: EnemyConfigMelee},
		},
	},
	4: {
		ID:       4,
		Name:     "Twin Wings",
		Role:     RoleNormal,
		Width:    30,
		Height:   20,
		Walls: withOuterWalls(
			Rect{MinX: 8, MinY: 13, MaxX: 14, MaxY: 14},
			Rect{MinX: 16, MinY: 6, MaxX: 22, MaxY: 7},
			Rect{MinX: 12, MinY: 9, MaxX: 18, MaxY: 10},
		),
		Spawns:   defaultSpawns,
		ExitZone: defaultExitZone,
		EnemySpawns: []EnemySpawnSpec{
			{Pos: Vec2{X: 17, Y: 15}, ConfigID: EnemyConfigRanged},
			{Pos: Vec2{X: 22, Y: 12}, ConfigID: EnemyConfigMelee},
			{Pos: Vec2{X: 22, Y: 4}, ConfigID: EnemyConfigRanged},
		},
	},
	5: {
		ID:       5,
		Name:     "U-Fortress",
		Role:     RoleNormal,
		Width:    30,
		Height:   20,
		Walls: withOuterWalls(
			Rect{MinX: 11, MinY: 6, MaxX: 12, MaxY: 14},
			Rect{MinX: 12, MinY: 13, MaxX: 19, MaxY: 14},
			Rect{MinX: 12, MinY: 6, MaxX: 19, MaxY: 7},
		),
		Spawns:   defaultSpawns,
		ExitZone: defaultExitZone,
		EnemySpawns: []EnemySpawnSpec{
			{Pos: Vec2{X: 16, Y: 10}, ConfigID: EnemyConfigMelee},
			{Pos: Vec2{X: 22, Y: 15}, ConfigID: EnemyConfigRanged},
			{Pos: Vec2{X: 22, Y: 5}, ConfigID: EnemyConfigMelee},
		},
	},
	6: {
		ID:       6,
		Name:     "Fountain Shrine",
		Role:     RoleSupply,
		Width:    30,
		Height:   20,
		Walls: withOuterWalls(
			Rect{MinX: 11, MinY: 13, MaxX: 13, MaxY: 15},
			Rect{MinX: 17, MinY: 13, MaxX: 19, MaxY: 15},
			Rect{MinX: 11, MinY: 5, MaxX: 13, MaxY: 7},
			Rect{MinX: 17, MinY: 5, MaxX: 19, MaxY: 7},
		),
		Spawns:   defaultSpawns,
		ExitZone: defaultExitZone,
		PickupSpawns: []PickupSpawnSpec{
			{Pos: Vec2{X: 15, Y: 11.5}, ConfigID: PickupConfigHealth},
			{Pos: Vec2{X: 15, Y: 8.5}, ConfigID: PickupConfigShield},
		},
	},
	7: {
		ID:       7,
		Name:     "Armory Vault",
		Role:     RoleSupply,
		Width:    30,
		Height:   20,
		Walls: withOuterWalls(
			Rect{MinX: 9, MinY: 14, MaxX: 21, MaxY: 15},
			Rect{MinX: 9, MinY: 5, MaxX: 21, MaxY: 6},
		),
		Spawns:   defaultSpawns,
		ExitZone: defaultExitZone,
		PickupSpawns: []PickupSpawnSpec{
			{Pos: Vec2{X: 14, Y: 10}, ConfigID: PickupConfigAtkBuff},
			{Pos: Vec2{X: 17, Y: 10}, ConfigID: PickupConfigHealth},
		},
	},
	8: {
		ID:       8,
		Name:     "Gladiator Altar",
		Role:     RoleElite,
		Width:    30,
		Height:   20,
		Walls: withOuterWalls(
			Rect{MinX: 8, MinY: 14, MaxX: 10, MaxY: 16},
			Rect{MinX: 20, MinY: 14, MaxX: 22, MaxY: 16},
			Rect{MinX: 8, MinY: 4, MaxX: 10, MaxY: 6},
			Rect{MinX: 20, MinY: 4, MaxX: 22, MaxY: 6},
			Rect{MinX: 14, MinY: 9, MaxX: 16, MaxY: 11},
		),
		Spawns:   defaultSpawns,
		ExitZone: defaultExitZone,
		EnemySpawns: []EnemySpawnSpec{
			{Pos: Vec2{X: 18, Y: 13}, ConfigID: EnemyConfigElite},
			{Pos: Vec2{X: 18, Y: 7}, ConfigID: EnemyConfigMelee},
			{Pos: Vec2{X: 24, Y: 14}, ConfigID: EnemyConfigRanged},
			{Pos: Vec2{X: 24, Y: 6}, ConfigID: EnemyConfigRanged},
		},
	},
	9: {
		ID:       9,
		Name:     "Lava Throne",
		Role:     RoleBoss,
		Width:    30,
		Height:   20,
		Walls: withOuterWalls(
			Rect{MinX: 10, MinY: 13, MaxX: 12, MaxY: 16},
			Rect{MinX: 10, MinY: 4, MaxX: 12, MaxY: 7},
			Rect{MinX: 20, MinY: 13, MaxX: 22, MaxY: 16},
			Rect{MinX: 20, MinY: 4, MaxX: 22, MaxY: 7},
		),
		Spawns:   defaultSpawns,
		ExitZone: defaultExitZone,
		EnemySpawns: []EnemySpawnSpec{
			{Pos: Vec2{X: 22, Y: 10}, ConfigID: BossConfigLord, IsBoss: true},
		},
	},
}

// GetMap 按 map_id 查找内置地图定义；未命中时返回 false。
func GetMap(id uint32) (MapDef, bool) {
	m, ok := builtinMaps[id]
	return m, ok
}

// AllMapIDs 返回内置全部预制房间模板 ID（升序）。
func AllMapIDs() []uint32 {
	return []uint32{1, 2, 3, 4, 5, 6, 7, 8, 9}
}

// GenerateDungeon 基于随机种子从 9 张预制房间模板池中生成 4 房地牢序列：
// [1(出生房), rand(2..5 普通战斗房), rand(6..8 补给/精英房), 9(Boss 房)]。
// 保证同局模板不重复、必达 Boss 房、同 Seed 100% 确定性复现。
func GenerateDungeon(seed uint64) []uint32 {
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	normalPool := []uint32{2, 3, 4, 5}
	specialPool := []uint32{6, 7, 8}

	normalID := normalPool[rng.IntN(len(normalPool))]
	specialID := specialPool[rng.IntN(len(specialPool))]
	return []uint32{Map1ID, normalID, specialID, BossMapID}
}
