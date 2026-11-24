#nullable enable
using System;
using System.Collections.Generic;

namespace SepCore.Battle;

/// <summary>纯 C# 服务端世界坐标系下的二维向量（1 单位 = 1 tile，+X 右，+Y 上）。</summary>
public readonly record struct BattleVec2(float X, float Y);

/// <summary>
/// 纯 C# 圆形与 AABB 碰撞解算（与服务端 <c>Knight-Server/internal/world/collision.go</c> 逐行数学等价）。
/// 供 M2 客户端本地预测与校正重放使用，不依赖 Godot 物理引擎。
/// </summary>
public static class BattleCollision
{
    /// <summary>玩家碰撞半径（世界单位，匹配服务端 DefaultPlayerRadius = 5 / 32 = 0.15625）。</summary>
    public const float DefaultPlayerRadius = 5.0f / 32.0f;

    private const int MaxIterations = 3;

    /// <summary>
    /// 将圆心为 <paramref name="pos"/>、半径为 <paramref name="radius"/> 的圆形约束在房间范围内且不穿透任何墙体 AABB。
    /// 迭代解算多墙拐角接触，沿最短穿透法线推出，天然支持贴墙滑动。
    /// </summary>
    public static BattleVec2 ResolveCollisions(BattleVec2 pos, float radius, BattleMapDefinition map)
    {
        ArgumentNullException.ThrowIfNull(map);
        return ResolveCollisions(pos, radius, map.WidthTiles, map.HeightTiles, map.Walls);
    }

    /// <inheritdoc cref="ResolveCollisions(BattleVec2, float, BattleMapDefinition)"/>
    public static BattleVec2 ResolveCollisions(
        BattleVec2 pos,
        float radius,
        float roomWidth,
        float roomHeight,
        IReadOnlyList<BattleWallAabb> walls)
    {
        ArgumentNullException.ThrowIfNull(walls);
        pos = ClampToRoomBounds(pos, radius, roomWidth, roomHeight);
        for (var i = 0; i < MaxIterations; i++)
        {
            var moved = false;
            for (var w = 0; w < walls.Count; w++)
            {
                var (next, hit) = ResolveCircleAabb(pos, radius, walls[w]);
                if (hit)
                {
                    pos = next;
                    moved = true;
                }
            }
            if (!moved)
                break;
        }
        return ClampToRoomBounds(pos, radius, roomWidth, roomHeight);
    }

    /// <summary>将圆心约束在房间边界内 [radius, width - radius] x [radius, height - radius]。</summary>
    public static BattleVec2 ClampToRoomBounds(BattleVec2 pos, float radius, float roomWidth, float roomHeight)
    {
        var minX = radius;
        var maxX = roomWidth - radius;
        var minY = radius;
        var maxY = roomHeight - radius;
        var x = pos.X;
        var y = pos.Y;

        if (minX <= maxX)
        {
            if (x < minX)
                x = minX;
            else if (x > maxX)
                x = maxX;
        }

        if (minY <= maxY)
        {
            if (y < minY)
                y = minY;
            else if (y > maxY)
                y = maxY;
        }

        return new BattleVec2(x, y);
    }

    /// <summary>检测并解算圆形与 AABB 的重叠；未重叠返回 (pos, false)。</summary>
    public static (BattleVec2 Position, bool Hit) ResolveCircleAabb(BattleVec2 pos, float radius, BattleWallAabb wall)
    {
        var cx = ClampF32(pos.X, wall.MinX, wall.MaxX);
        var cy = ClampF32(pos.Y, wall.MinY, wall.MaxY);

        double dx = pos.X - cx;
        double dy = pos.Y - cy;
        double distSq = dx * dx + dy * dy;
        double rad = radius;

        if (distSq >= rad * rad)
            return (pos, false);

        if (distSq > 1e-12)
        {
            double dist = Math.Sqrt(distSq);
            double push = rad - dist;
            return (
                new BattleVec2(
                    pos.X + (float)(dx / dist * push),
                    pos.Y + (float)(dy / dist * push)),
                true);
        }

        // 圆心落入 AABB 内部：沿穿透最浅的边推出（顺序与服务端 collision.go 完全一致）
        float penLeft = pos.X - wall.MinX;
        float penRight = wall.MaxX - pos.X;
        float penBottom = pos.Y - wall.MinY;
        float penTop = wall.MaxY - pos.Y;

        float minPen = penLeft;
        var resolved = new BattleVec2(wall.MinX - radius, pos.Y);
        if (penRight < minPen)
        {
            minPen = penRight;
            resolved = new BattleVec2(wall.MaxX + radius, pos.Y);
        }
        if (penBottom < minPen)
        {
            minPen = penBottom;
            resolved = new BattleVec2(pos.X, wall.MinY - radius);
        }
        if (penTop < minPen)
        {
            resolved = new BattleVec2(pos.X, wall.MaxY + radius);
        }
        return (resolved, true);
    }

    private static float ClampF32(float v, float lo, float hi)
    {
        if (v < lo)
            return lo;
        if (v > hi)
            return hi;
        return v;
    }
}
