#nullable enable
using System;
using System.Collections.Generic;
using Godot;

namespace SepCore.Battle;

/// <summary>服务端世界单位下的矩形墙体 AABB（1 单位 = 1 tile，+X 右，+Y 上）。</summary>
public readonly record struct BattleWallAabb(float MinX, float MinY, float MaxX, float MaxY);

/// <summary>单张战斗房间的几何与坐标换算定义（与服务端 internal/world 保持逐坐标一致）。</summary>
public sealed class BattleMapDefinition
{
    public uint MapId { get; }
    public float WidthTiles { get; }
    public float HeightTiles { get; }
    public float TileSizePixels { get; }
    public IReadOnlyList<BattleWallAabb> Walls { get; }

    public BattleMapDefinition(
        uint mapId,
        float widthTiles,
        float heightTiles,
        float tileSizePixels,
        IReadOnlyList<BattleWallAabb> walls)
    {
        MapId = mapId;
        WidthTiles = widthTiles;
        HeightTiles = heightTiles;
        TileSizePixels = tileSizePixels;
        Walls = walls;
    }

    public Rect2 RoomBoundsGodot => new(0, 0, WidthTiles * TileSizePixels, HeightTiles * TileSizePixels);

    /// <summary>世界坐标（+Y 上）→ Godot 像素坐标（+Y 下）。</summary>
    public Vector2 WorldToGodot(float worldX, float worldY)
        => new(worldX * TileSizePixels, (HeightTiles - worldY) * TileSizePixels);

    /// <summary>Godot 像素坐标（+Y 下）→ 世界坐标（+Y 上）。</summary>
    public (float X, float Y) GodotToWorld(Vector2 godotPos)
        => (godotPos.X / TileSizePixels, HeightTiles - godotPos.Y / TileSizePixels);

    /// <summary>世界 AABB [minX, minY, maxX, maxY] → Godot Rect2。</summary>
    public Rect2 WallToGodotRect(BattleWallAabb wall)
    {
        var x = wall.MinX * TileSizePixels;
        var y = (HeightTiles - wall.MaxY) * TileSizePixels;
        var w = (wall.MaxX - wall.MinX) * TileSizePixels;
        var h = (wall.MaxY - wall.MinY) * TileSizePixels;
        return new Rect2(x, y, w, h);
    }

    /// <summary>Godot 移动向量（+Y 下）→ 协议世界移动向量（+Y 上，模长钳制 ≤ 1）。</summary>
    public static (float MoveX, float MoveY) GodotMoveToWorld(Vector2 godotMove)
    {
        var x = float.IsFinite(godotMove.X) ? godotMove.X : 0f;
        var y = float.IsFinite(godotMove.Y) ? -godotMove.Y : 0f;
        var lenSq = x * x + y * y;
        if (lenSq > 1f)
        {
            var inv = 1f / MathF.Sqrt(lenSq);
            x *= inv;
            y *= inv;
        }
        return (x, y);
    }

    /// <summary>Godot 瞄准弧度（0=+X, +90°=+Y下）→ 协议世界角度（度，0°=+X, +90°=+Y上，逆时针）。</summary>
    public static float GodotAimRadiansToWorldDegrees(float godotRadians)
        => float.IsFinite(godotRadians) ? -godotRadians * (180f / MathF.PI) : 0f;

    /// <summary>协议世界角度（度，0°=+X, +90°=+Y上，逆时针）→ Godot 弧度（0=+X, +90°=+Y下）。</summary>
    public static float WorldDegreesToGodotRadians(float worldDegrees)
        => float.IsFinite(worldDegrees) ? -worldDegrees * (MathF.PI / 180f) : 0f;
}

/// <summary>内置战斗地图几何表（Map 1 对齐服务端 internal/world 权威 AABB 契约）。</summary>
public static class BattleMapCatalog
{
    public const float DefaultTileSizePixels = 32f;
    public const float DefaultPlayerMoveSpeedTiles = 3.0f;
    public const int DefaultPlayerMaxHp = 100;

    /// <summary>Map 1（30x20 tiles = 960x640 px，1-tile 四周外墙 + 3 面室内掩体墙）。</summary>
    public static BattleMapDefinition Map1 { get; } = new(
        mapId: 1,
        widthTiles: 30f,
        heightTiles: 20f,
        tileSizePixels: DefaultTileSizePixels,
        walls: Array.AsReadOnly(new[]
        {
            // 四周边界墙（可玩区域 [1, 29] x [1, 19]）
            new BattleWallAabb(0f, 0f, 30f, 1f),    // Bottom -> Godot (0, 608, 960, 32)
            new BattleWallAabb(0f, 19f, 30f, 20f),  // Top    -> Godot (0, 0, 960, 32)
            new BattleWallAabb(0f, 0f, 1f, 20f),    // Left   -> Godot (0, 0, 32, 640)
            new BattleWallAabb(29f, 0f, 30f, 20f),  // Right  -> Godot (928, 0, 32, 640)
            // 室内掩体墙
            new BattleWallAabb(10f, 9f, 11f, 15f),  // Wall 1 -> Godot (320, 160, 32, 192)
            new BattleWallAabb(15f, 8f, 20f, 9f),   // Wall 2 -> Godot (480, 352, 160, 32)
            new BattleWallAabb(23f, 11f, 24f, 15f), // Wall 3 -> Godot (736, 160, 32, 128)
        }));

    public static BattleMapDefinition Get(uint mapId) => mapId switch
    {
        1 => Map1,
        _ => Map1,
    };
}
