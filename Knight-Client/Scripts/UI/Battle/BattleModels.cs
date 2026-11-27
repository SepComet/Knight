#nullable enable
using System;
using System.Collections.Generic;
using System.Text;
using Godot;

namespace SepCore.UI.Battle;

public readonly record struct ResourceValue(double Current, double Maximum)
{
    public double SafeMaximum => double.IsFinite(Maximum) ? Math.Max(0, Maximum) : 0;
    public double SafeCurrent => double.IsFinite(Current) ? Math.Clamp(Current, 0, SafeMaximum) : 0;
}

public sealed record GameHudModel(ResourceValue Health, ResourceValue Mana, ResourceValue Shield,
    int MajorStage = 1, int MinorStage = 1)
{
    public static GameHudModel Empty { get; } = new(new(0, 0), new(0, 0), new(0, 0));
    public string StageText => $"{Math.Clamp(MajorStage, 1, 3)}-{Math.Clamp(MinorStage, 1, 5)}";
}

/// <summary>主动技能冷却与增益状态显示模型。</summary>
public readonly record struct SkillHudModel(
    string SkillName,
    float CooldownRemaining,
    float CooldownTotal,
    uint StatusFlags)
{
    public static SkillHudModel Default { get; } = new("冲刺斩 [Space]", 0f, 5f, 0u);
    public bool IsReady => !float.IsFinite(CooldownRemaining) || CooldownRemaining <= 0.05f;
    public string StatusText
    {
        get
        {
            if (IsReady)
                return "就绪";
            return $"{Math.Max(0f, CooldownRemaining):0.0}s";
        }
    }
}

/// <summary>Boss 顶部血条与二阶段状态显示模型。</summary>
public readonly record struct BossHudModel(
    bool Visible,
    string BossName,
    int CurrentHp,
    int MaxHp,
    bool IsPhase2)
{
    public static BossHudModel Hidden { get; } = new(false, "地牢领主", 0, 1, false);
    public string DisplayTitle => IsPhase2 ? $"【狂暴二阶段】{BossName}" : BossName;
}

/// <summary>地牢房间进度、出口门状态与简易小地图模型。</summary>
public sealed record DungeonProgressModel(
    int RoomIndex,
    int TotalRooms,
    uint CurrentMapId,
    string RoomName,
    bool DoorOpen,
    IReadOnlyList<uint> MapSequence)
{
    public static DungeonProgressModel Empty { get; } = new(0, 0, 0, "", false, Array.Empty<uint>());

    public string ProgressText => TotalRooms > 0
        ? $"房间 {Math.Clamp(RoomIndex + 1, 1, TotalRooms)}/{TotalRooms} · {RoomName}"
        : RoomName;

    public string DoorText => DoorOpen ? "出口已开启 →" : "出口锁定（清怪解锁）";

    public string MinimapText
    {
        get
        {
            var count = Math.Max(TotalRooms, MapSequence.Count);
            if (count <= 0)
                return "[1]";

            var sb = new StringBuilder();
            for (var i = 0; i < count; i++)
            {
                if (i > 0) sb.Append("─");
                var mapId = i < MapSequence.Count ? MapSequence[i] : 0u;
                var label = mapId == 9 ? "B" : (i + 1).ToString();
                if (i < RoomIndex)
                    sb.Append($"[{label}✓]");
                else if (i == RoomIndex)
                    sb.Append($"[{label}★]");
                else
                    sb.Append($"[{label}]");
            }
            return sb.ToString();
        }
    }
}

public readonly record struct TouchInputState(Vector2 Movement, bool AttackHeld);

/// <summary>战斗 HUD 网络同步调试悬浮窗数据模型。</summary>
public readonly record struct NetSyncDebugModel(
    float Fps,
    float RttMs,
    float LossRate,
    float ReconciliationsPerSecond,
    float AveragePositionError,
    bool PredictionEnabled,
    bool InterpolationEnabled,
    bool Expanded)
{
    public static NetSyncDebugModel Default { get; } = new(0f, 0f, 0f, 0f, 0f, true, true, true);
}
