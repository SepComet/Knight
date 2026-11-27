#nullable enable
using System;
using System.Collections.Generic;
using Godot;

namespace SepCore.Battle;

/// <summary>服务端世界单位下的矩形区域 AABB（1 单位 = 1 tile，+X 右，+Y 上）。</summary>
public readonly record struct BattleWallAabb(float MinX, float MinY, float MaxX, float MaxY)
{
    public bool ContainsPoint(float x, float y)
        => x >= MinX && x <= MaxX && y >= MinY && y <= MaxY;
}

/// <summary>房间风格类型（用于小地图标识与视觉配色）。</summary>
public enum BattleRoomKind
{
    Start,
    Normal,
    Supply,
    Elite,
    Boss,
}

/// <summary>房间视觉配色主题（用于区分不同预制房间模板的视觉风格）。</summary>
public readonly record struct BattleRoomPalette(
    string FloorHex,
    string GridHex,
    string WallHex,
    string WallTopHex,
    string WallShadowHex);

/// <summary>单张战斗房间的几何与坐标换算定义（与服务端 internal/world 保持逐坐标一致）。</summary>
public sealed class BattleMapDefinition
{
    public uint MapId { get; }
    public string Name { get; }
    public BattleRoomKind Kind { get; }
    public float WidthTiles { get; }
    public float HeightTiles { get; }
    public float TileSizePixels { get; }
    public BattleWallAabb ExitZone { get; }
    public BattleRoomPalette Palette { get; }
    public IReadOnlyList<BattleWallAabb> Walls { get; }

    public BattleMapDefinition(
        uint mapId,
        float widthTiles,
        float heightTiles,
        float tileSizePixels,
        IReadOnlyList<BattleWallAabb> walls,
        string name = "",
        BattleRoomKind kind = BattleRoomKind.Normal,
        BattleWallAabb? exitZone = null,
        BattleRoomPalette? palette = null)
    {
        MapId = mapId;
        Name = string.IsNullOrWhiteSpace(name) ? $"房间 #{mapId}" : name;
        Kind = kind;
        WidthTiles = widthTiles;
        HeightTiles = heightTiles;
        TileSizePixels = tileSizePixels;
        ExitZone = exitZone ?? BattleMapCatalog.DefaultExitZone;
        Palette = palette ?? BattleMapCatalog.DefaultPalette;
        Walls = walls;
    }

    public Rect2 RoomBoundsGodot => new(0, 0, WidthTiles * TileSizePixels, HeightTiles * TileSizePixels);
    public Rect2 ExitZoneGodot => WallToGodotRect(ExitZone);

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

/// <summary>内置战斗地图几何表（9 张预制房间模板，与服务端 internal/world 100% 同构）。</summary>
public static class BattleMapCatalog
{
    public const float DefaultTileSizePixels = 32f;
    public const float DefaultRoomWidthTiles = 30f;
    public const float DefaultRoomHeightTiles = 20f;
    public const float DefaultPlayerMoveSpeedTiles = 3.0f;
    public const int DefaultPlayerMaxHp = 100;

    /// <summary>统一出口区域（右侧中央门，与服务端 ExitZone 一致）。</summary>
    public static BattleWallAabb DefaultExitZone { get; } = new(26.5f, 8.5f, 29.0f, 11.5f);

    public static BattleRoomPalette DefaultPalette { get; } = new(
        FloorHex: "203440",
        GridHex: "293f4a",
        WallHex: "526d77",
        WallTopHex: "91a8ac",
        WallShadowHex: "142630");

    private static readonly BattleWallAabb[] OuterBoundaryWalls =
    {
        new(0f, 0f, 30f, 1f),    // Bottom -> Godot (0, 608, 960, 32)
        new(0f, 19f, 30f, 20f),  // Top    -> Godot (0, 0, 960, 32)
        new(0f, 0f, 1f, 20f),    // Left   -> Godot (0, 0, 32, 640)
        new(29f, 0f, 30f, 20f),  // Right  -> Godot (928, 0, 32, 640)
    };

    private static IReadOnlyList<BattleWallAabb> BuildWalls(params BattleWallAabb[] innerWalls)
    {
        var all = new BattleWallAabb[OuterBoundaryWalls.Length + innerWalls.Length];
        OuterBoundaryWalls.CopyTo(all, 0);
        innerWalls.CopyTo(all, OuterBoundaryWalls.Length);
        return Array.AsReadOnly(all);
    }

    /// <summary>Map 1：出生前厅（兼容 M1/M2）。</summary>
    public static BattleMapDefinition Map1 { get; } = new(
        mapId: 1,
        widthTiles: DefaultRoomWidthTiles,
        heightTiles: DefaultRoomHeightTiles,
        tileSizePixels: DefaultTileSizePixels,
        walls: BuildWalls(
            new BattleWallAabb(10f, 9f, 11f, 15f),
            new BattleWallAabb(15f, 8f, 20f, 9f),
            new BattleWallAabb(23f, 11f, 24f, 15f)),
        name: "出生前厅",
        kind: BattleRoomKind.Start,
        palette: DefaultPalette);

    /// <summary>Map 2：四柱神殿（普通战斗房）。</summary>
    public static BattleMapDefinition Map2 { get; } = new(
        mapId: 2,
        widthTiles: DefaultRoomWidthTiles,
        heightTiles: DefaultRoomHeightTiles,
        tileSizePixels: DefaultTileSizePixels,
        walls: BuildWalls(
            new BattleWallAabb(9f, 13f, 11f, 15f),
            new BattleWallAabb(9f, 5f, 11f, 7f),
            new BattleWallAabb(19f, 13f, 21f, 15f),
            new BattleWallAabb(19f, 5f, 21f, 7f)),
        name: "四柱神殿",
        kind: BattleRoomKind.Normal,
        palette: new BattleRoomPalette("23303d", "2c3c4c", "5b6b7d", "9cb0c4", "141c26"));

    /// <summary>Map 3：十字走廊（普通战斗房）。</summary>
    public static BattleMapDefinition Map3 { get; } = new(
        mapId: 3,
        widthTiles: DefaultRoomWidthTiles,
        heightTiles: DefaultRoomHeightTiles,
        tileSizePixels: DefaultTileSizePixels,
        walls: BuildWalls(
            new BattleWallAabb(14f, 12f, 16f, 17f),
            new BattleWallAabb(14f, 3f, 16f, 8f),
            new BattleWallAabb(9f, 9f, 13f, 11f),
            new BattleWallAabb(17f, 9f, 21f, 11f)),
        name: "十字走廊",
        kind: BattleRoomKind.Normal,
        palette: new BattleRoomPalette("283034", "333d42", "636e72", "a4b0b5", "181e21"));

    /// <summary>Map 4：双翼掩体（普通战斗房）。</summary>
    public static BattleMapDefinition Map4 { get; } = new(
        mapId: 4,
        widthTiles: DefaultRoomWidthTiles,
        heightTiles: DefaultRoomHeightTiles,
        tileSizePixels: DefaultTileSizePixels,
        walls: BuildWalls(
            new BattleWallAabb(8f, 13f, 14f, 14f),
            new BattleWallAabb(16f, 6f, 22f, 7f),
            new BattleWallAabb(12f, 9f, 18f, 10f)),
        name: "双翼掩体",
        kind: BattleRoomKind.Normal,
        palette: new BattleRoomPalette("253430", "2f423d", "527067", "8cb0a5", "16211e"));

    /// <summary>Map 5：U 形堡垒（普通战斗房）。</summary>
    public static BattleMapDefinition Map5 { get; } = new(
        mapId: 5,
        widthTiles: DefaultRoomWidthTiles,
        heightTiles: DefaultRoomHeightTiles,
        tileSizePixels: DefaultTileSizePixels,
        walls: BuildWalls(
            new BattleWallAabb(11f, 6f, 12f, 14f),
            new BattleWallAabb(12f, 13f, 19f, 14f),
            new BattleWallAabb(12f, 6f, 19f, 7f)),
        name: "U 形堡垒",
        kind: BattleRoomKind.Normal,
        palette: new BattleRoomPalette("2e2a36", "3a3545", "6b617d", "a89ec2", "1d1924"));

    /// <summary>Map 6：泉水圣坛（补给宝箱房）。</summary>
    public static BattleMapDefinition Map6 { get; } = new(
        mapId: 6,
        widthTiles: DefaultRoomWidthTiles,
        heightTiles: DefaultRoomHeightTiles,
        tileSizePixels: DefaultTileSizePixels,
        walls: BuildWalls(
            new BattleWallAabb(11f, 13f, 13f, 15f),
            new BattleWallAabb(17f, 13f, 19f, 15f),
            new BattleWallAabb(11f, 5f, 13f, 7f),
            new BattleWallAabb(17f, 5f, 19f, 7f)),
        name: "泉水圣坛",
        kind: BattleRoomKind.Supply,
        palette: new BattleRoomPalette("1c383b", "24474a", "427d7a", "7cc4bf", "112527"));

    /// <summary>Map 7：军械库房（补给宝箱房）。</summary>
    public static BattleMapDefinition Map7 { get; } = new(
        mapId: 7,
        widthTiles: DefaultRoomWidthTiles,
        heightTiles: DefaultRoomHeightTiles,
        tileSizePixels: DefaultTileSizePixels,
        walls: BuildWalls(
            new BattleWallAabb(9f, 14f, 21f, 15f),
            new BattleWallAabb(9f, 5f, 21f, 6f)),
        name: "军械库房",
        kind: BattleRoomKind.Supply,
        palette: new BattleRoomPalette("2b3228", "374033", "6c7559", "a8b391", "1b2119"));

    /// <summary>Map 8：角斗祭坛（精英房）。</summary>
    public static BattleMapDefinition Map8 { get; } = new(
        mapId: 8,
        widthTiles: DefaultRoomWidthTiles,
        heightTiles: DefaultRoomHeightTiles,
        tileSizePixels: DefaultTileSizePixels,
        walls: BuildWalls(
            new BattleWallAabb(8f, 14f, 10f, 16f),
            new BattleWallAabb(20f, 14f, 22f, 16f),
            new BattleWallAabb(8f, 4f, 10f, 6f),
            new BattleWallAabb(20f, 4f, 22f, 6f),
            new BattleWallAabb(14f, 9f, 16f, 11f)),
        name: "角斗祭坛",
        kind: BattleRoomKind.Elite,
        palette: new BattleRoomPalette("382927", "473431", "805952", "bf8c84", "241917"));

    /// <summary>Map 9：熔岩王座（Boss 房）。</summary>
    public static BattleMapDefinition Map9 { get; } = new(
        mapId: 9,
        widthTiles: DefaultRoomWidthTiles,
        heightTiles: DefaultRoomHeightTiles,
        tileSizePixels: DefaultTileSizePixels,
        walls: BuildWalls(
            new BattleWallAabb(10f, 13f, 12f, 16f),
            new BattleWallAabb(10f, 4f, 12f, 7f),
            new BattleWallAabb(20f, 13f, 22f, 16f),
            new BattleWallAabb(20f, 4f, 22f, 7f)),
        name: "熔岩王座",
        kind: BattleRoomKind.Boss,
        palette: new BattleRoomPalette("361e22", "4a282e", "8c423b", "d47566", "211114"));

    public static IReadOnlyList<BattleMapDefinition> AllMaps { get; } = Array.AsReadOnly(new[]
    {
        Map1, Map2, Map3, Map4, Map5, Map6, Map7, Map8, Map9,
    });

    public static BattleMapDefinition Get(uint mapId) => mapId switch
    {
        1 => Map1,
        2 => Map2,
        3 => Map3,
        4 => Map4,
        5 => Map5,
        6 => Map6,
        7 => Map7,
        8 => Map8,
        9 => Map9,
        _ => Map1,
    };
}
