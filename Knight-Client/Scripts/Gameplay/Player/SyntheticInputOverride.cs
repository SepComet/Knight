#nullable enable
using System;
using Godot;

namespace SepCore.Gameplay.Player;

/// <summary>
/// Programmatic input override state for automated testing / DevAgent control.
/// Pure C# (no Godot SceneTree calls) so expiration and sampling logic can be unit-tested directly.
/// </summary>
public sealed class SyntheticInputOverride
{
    private bool _pendingInteract;
    private bool _pendingSkill;

    public bool IsActive { get; private set; }
    public Vector2 Movement { get; private set; }
    public float? AimRadians { get; private set; }
    public bool AttackHeld { get; private set; }
    public double? RemainingSeconds { get; private set; }

    /// <summary>
    /// Activates or updates the synthetic input override.
    /// When <paramref name="durationSeconds"/> is positive and finite, the override expires automatically
    /// after that duration; otherwise it remains active until <see cref="Clear"/> is called.
    /// </summary>
    public void Set(
        Vector2 movement,
        float? aimRadians = null,
        bool attackHeld = false,
        bool interact = false,
        double? durationSeconds = null,
        bool skill = false)
    {
        Movement = PlayerMotor.SanitizeMoveInput(movement);
        AimRadians = aimRadians.HasValue && float.IsFinite(aimRadians.Value) ? aimRadians.Value : null;
        AttackHeld = attackHeld;
        _pendingInteract = interact;
        _pendingSkill = skill;
        RemainingSeconds = durationSeconds.HasValue && double.IsFinite(durationSeconds.Value) && durationSeconds.Value > 0
            ? durationSeconds.Value
            : null;
        IsActive = true;
    }

    /// <summary>Clears any active synthetic input override.</summary>
    public void Clear()
    {
        IsActive = false;
        Movement = Vector2.Zero;
        AimRadians = null;
        AttackHeld = false;
        _pendingInteract = false;
        _pendingSkill = false;
        RemainingSeconds = null;
    }

    /// <summary>Advances the optional expiration timer by <paramref name="deltaSeconds"/>.</summary>
    public void Advance(double deltaSeconds)
    {
        if (!IsActive || !RemainingSeconds.HasValue)
            return;
        if (!double.IsFinite(deltaSeconds) || deltaSeconds <= 0)
            return;

        var remaining = RemainingSeconds.Value - deltaSeconds;
        if (remaining <= 0)
            Clear();
        else
            RemainingSeconds = remaining;
    }

    /// <summary>
    /// Samples the active synthetic override as a <see cref="PlayerCommand"/>.
    /// Returns false when inactive. Interaction and skill are edge-triggered and consumed on the first sample.
    /// </summary>
    public bool TrySample(float fallbackAimRadians, out PlayerCommand command)
    {
        if (!IsActive)
        {
            command = default;
            return false;
        }

        var aim = AimRadians ?? (float.IsFinite(fallbackAimRadians) ? fallbackAimRadians : 0f);
        command = new PlayerCommand(Movement, aim, AttackHeld, _pendingInteract, _pendingSkill);
        _pendingInteract = false;
        _pendingSkill = false;
        return true;
    }
}
