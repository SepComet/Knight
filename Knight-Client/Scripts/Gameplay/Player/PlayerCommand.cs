using Godot;

namespace SepCore.Gameplay.Player;

/// <summary>One local simulation step's intent, independent of input devices.</summary>
public readonly record struct PlayerCommand(
    Vector2 Movement, float AimRadians, bool AttackHeld, bool InteractPressed);
