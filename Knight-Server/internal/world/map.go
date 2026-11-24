// Package world 实现服务端权威战斗世界与地牢房间模拟（Docs/protocol.md §5.4~§6.2，消息域 64~127）。
//
// 并发模型（对齐 internal/tick）：
//   - Manager 在 gate goroutine 上接收消息（Route）与大厅事件（GameStarted/MemberLeft）；
//   - 每个对局 Room 作为 tick.Tickable 注册到 tick.Runner，其内部状态仅在模拟 goroutine 上读写，无锁；
//   - 跨 goroutine 投递（RoomLoaded / InputBatch / 玩家离场）统一经 runner.Post 在 tick 边界串行执行。
package world

// Vec2 二维世界坐标（单位 = 地面 tile；+X 向右，+Y 向上，见 Docs/protocol.md §8）。
type Vec2 struct {
	X float32
	Y float32
}

// Rect 轴对齐包围盒（AABB），用于房间边界与墙体碰撞。
type Rect struct {
	MinX float32
	MinY float32
	MaxX float32
	MaxY float32
}

// MapDef 单个地牢房间的几何与出生点定义（世界单位 = tile）。
type MapDef struct {
	ID     uint32
	Width  float32
	Height float32
	Walls  []Rect // 外围边界墙 + 内部障碍墙
	Spawns []Vec2 // 玩家出生点列表（按加入顺序分配）
}

// Map1ID 首张战斗房间的 map_id。
const Map1ID uint32 = 1

// map1Def Map 1（30×20 世界单位，对应客户端 32px/tile 下的 960×640）。
// 外围 1 tile 厚边界墙围出 [1, 29] × [1, 19] 可行走区域，内含 3 面掩体墙。
var map1Def = MapDef{
	ID:     Map1ID,
	Width:  30,
	Height: 20,
	Walls: []Rect{
		// 外围边界墙（厚 1.0 tile）
		{MinX: 0, MinY: 0, MaxX: 30, MaxY: 1},   // 底墙
		{MinX: 0, MinY: 19, MaxX: 30, MaxY: 20}, // 顶墙
		{MinX: 0, MinY: 0, MaxX: 1, MaxY: 20},   // 左墙
		{MinX: 29, MinY: 0, MaxX: 30, MaxY: 20}, // 右墙
		// 内部障碍墙
		{MinX: 10, MinY: 9, MaxX: 11, MaxY: 15},  // 墙 1（左中竖墙）
		{MinX: 15, MinY: 8, MaxX: 20, MaxY: 9},   // 墙 2（中下横墙）
		{MinX: 23, MinY: 11, MaxX: 24, MaxY: 15}, // 墙 3（右上竖墙）
	},
	Spawns: []Vec2{
		{X: 4, Y: 12},
		{X: 4, Y: 8},
		{X: 6, Y: 12},
		{X: 6, Y: 8},
	},
}

var builtinMaps = map[uint32]MapDef{
	1: map1Def,
	2: cloneMapWithID(map1Def, 2),
	3: cloneMapWithID(map1Def, 3),
}

func cloneMapWithID(base MapDef, id uint32) MapDef {
	cp := base
	cp.ID = id
	return cp
}

// GetMap 按 map_id 查找内置地图定义；未命中时返回 false。
func GetMap(id uint32) (MapDef, bool) {
	m, ok := builtinMaps[id]
	return m, ok
}
