#nullable enable
using System;
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

public readonly record struct TouchInputState(Vector2 Movement, bool AttackHeld);
