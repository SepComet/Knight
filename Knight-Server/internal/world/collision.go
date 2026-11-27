package world

import "math"

// resolveCollisions 将圆心为 pos、半径为 radius 的实体约束在房间范围内且不穿透任何墙体 AABB。
// 迭代解算多墙拐角接触，沿最短穿透法线推出，天然支持贴墙滑动。
func resolveCollisions(pos Vec2, radius float32, m MapDef) Vec2 {
	pos = clampToRoomBounds(pos, radius, m)
	const iterations = 3
	for i := 0; i < iterations; i++ {
		moved := false
		for _, wall := range m.Walls {
			next, hit := resolveCircleAABB(pos, radius, wall)
			if hit {
				pos = next
				moved = true
			}
		}
		if !moved {
			break
		}
	}
	return clampToRoomBounds(pos, radius, m)
}

// circleHitsAnyWall 检测圆是否与房间边界外或任一墙体 AABB 相交（子弹碰墙判定）。
func circleHitsAnyWall(pos Vec2, radius float32, m MapDef) bool {
	if pos.X-radius < 0 || pos.X+radius > m.Width || pos.Y-radius < 0 || pos.Y+radius > m.Height {
		return true
	}
	for _, wall := range m.Walls {
		if circleOverlapsAABB(pos, radius, wall) {
			return true
		}
	}
	return false
}

func circleOverlapsAABB(pos Vec2, radius float32, r Rect) bool {
	cx := clampF32(pos.X, r.MinX, r.MaxX)
	cy := clampF32(pos.Y, r.MinY, r.MaxY)
	dx := float64(pos.X - cx)
	dy := float64(pos.Y - cy)
	return dx*dx+dy*dy < float64(radius)*float64(radius)
}

func circlesOverlap(a Vec2, rA float32, b Vec2, rB float32) bool {
	dx := float64(a.X - b.X)
	dy := float64(a.Y - b.Y)
	sum := float64(rA + rB)
	return dx*dx+dy*dy <= sum*sum
}

func pointInRect(p Vec2, r Rect) bool {
	return p.X >= r.MinX && p.X <= r.MaxX && p.Y >= r.MinY && p.Y <= r.MaxY
}

func distSq(a, b Vec2) float32 {
	dx := a.X - b.X
	dy := a.Y - b.Y
	return dx*dx + dy*dy
}

func angleDeg(from, to Vec2) float32 {
	return float32(math.Atan2(float64(to.Y-from.Y), float64(to.X-from.X)) * 180.0 / math.Pi)
}

func degToDir(deg float32) Vec2 {
	rad := float64(deg) * math.Pi / 180.0
	return Vec2{X: float32(math.Cos(rad)), Y: float32(math.Sin(rad))}
}

func clampToRoomBounds(pos Vec2, radius float32, m MapDef) Vec2 {
	minX, maxX := radius, m.Width-radius
	minY, maxY := radius, m.Height-radius
	if minX <= maxX {
		if pos.X < minX {
			pos.X = minX
		} else if pos.X > maxX {
			pos.X = maxX
		}
	}
	if minY <= maxY {
		if pos.Y < minY {
			pos.Y = minY
		} else if pos.Y > maxY {
			pos.Y = maxY
		}
	}
	return pos
}

// resolveCircleAABB 检测并解算圆形与 AABB 的重叠；未重叠返回 (pos, false)。
func resolveCircleAABB(pos Vec2, radius float32, r Rect) (Vec2, bool) {
	cx := clampF32(pos.X, r.MinX, r.MaxX)
	cy := clampF32(pos.Y, r.MinY, r.MaxY)

	dx := float64(pos.X - cx)
	dy := float64(pos.Y - cy)
	distSqVal := dx*dx + dy*dy
	rad := float64(radius)

	if distSqVal >= rad*rad {
		return pos, false
	}

	if distSqVal > 1e-12 {
		dist := math.Sqrt(distSqVal)
		push := rad - dist
		return Vec2{
			X: pos.X + float32(dx/dist*push),
			Y: pos.Y + float32(dy/dist*push),
		}, true
	}

	// 圆心落入 AABB 内部：沿穿透最浅的边推出
	penLeft := pos.X - r.MinX
	penRight := r.MaxX - pos.X
	penBottom := pos.Y - r.MinY
	penTop := r.MaxY - pos.Y

	minPen := penLeft
	out := Vec2{X: r.MinX - radius, Y: pos.Y}
	if penRight < minPen {
		minPen = penRight
		out = Vec2{X: r.MaxX + radius, Y: pos.Y}
	}
	if penBottom < minPen {
		minPen = penBottom
		out = Vec2{X: pos.X, Y: r.MinY - radius}
	}
	if penTop < minPen {
		out = Vec2{X: pos.X, Y: r.MaxY + radius}
	}
	return out, true
}

func clampF32(v, lo, hi float32) float32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
