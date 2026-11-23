using Godot;

namespace SepCore.Gameplay.Player;

/// <summary>Input intent only. Short interaction presses survive until the next simulation step.</summary>
public sealed class PlayerInputBuffer
{
    public Vector2 Movement { get; set; }
    public bool AttackHeld { get; set; }
    private bool _interact;

    public void RequestInteraction() => _interact = true;

    public PlayerCommand Consume(float aimRadians, bool canAttack = true)
    {
        var movement = Movement.LengthSquared() > 0.04f ? Movement.Normalized() : Vector2.Zero;
        var command = new PlayerCommand(movement, aimRadians, AttackHeld && canAttack, _interact);
        _interact = false;
        return command;
    }

    public void Reset()
    {
        Movement = Vector2.Zero;
        AttackHeld = false;
        _interact = false;
    }
}
