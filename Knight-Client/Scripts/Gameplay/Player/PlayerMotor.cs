#nullable enable
using System;
using Godot;

namespace SepCore.Gameplay.Player;

/// <summary>Local fixed-step movement. Receives commands; does not read devices or animate.</summary>
public partial class PlayerMotor : CharacterBody2D
{
    [Export(PropertyHint.Range, "1,1000,1")] public float MoveSpeed { get; set; } = 96;
    [Export(PropertyHint.Range, "0.001,0.1,0.001")] public float FixedStep { get; set; } = 1f / 30;

    public Func<PlayerCommand>? InputSource { get; set; }
    public PlayerCommand Command { get; set; }
    public Vector2 LastMovement { get; private set; }
    public float FacingRadians { get; private set; }
    private double _accumulator;
    private bool _alive = true;
    public bool Alive
    {
        get => _alive;
        set
        {
            _alive = value;
            if (!value)
            {
                Command = default;
                LastMovement = Velocity = Vector2.Zero;
                _accumulator = 0;
            }
        }
    }

    public override void _Ready() => MotionMode = MotionModeEnum.Floating;

    public override void _PhysicsProcess(double delta)
    {
        if (!Alive) return;
        var step = float.IsFinite(FixedStep) ? Math.Clamp(FixedStep, 0.001f, 0.1f) : 1f / 30;
        _accumulator += delta;
        // Exported steps are floats; tolerate conversion error against double physics deltas.
        while (_accumulator + step * 1e-5 >= step)
        {
            Command = InputSource?.Invoke() ?? Command;
            Simulate(Command, step);
            _accumulator -= step;
        }
    }

    /// <summary>Applies one command in Godot pixel coordinates; later replay can call the same path.</summary>
    public void Simulate(PlayerCommand command, float seconds)
    {
        if (!Alive || !float.IsFinite(seconds) || seconds <= 0) return;
        var movement = command.Movement.IsFinite() ? command.Movement : Vector2.Zero;
        movement = movement.IsZeroApprox() ? Vector2.Zero : movement.Normalized();
        if (float.IsFinite(command.AimRadians)) FacingRadians = command.AimRadians;
        var start = Position;
        var speed = float.IsFinite(MoveSpeed) ? Math.Max(0, MoveSpeed) : 0;
        var remaining = movement * speed * seconds;
        for (var attempt = 0; attempt < 3 && !remaining.IsZeroApprox(); attempt++)
        {
            var collision = MoveAndCollide(remaining);
            if (collision == null) break;
            remaining = collision.GetRemainder().Slide(collision.GetNormal());
        }
        LastMovement = Position - start;
        Velocity = LastMovement / seconds;
    }

    public void Teleport(Vector2 position)
    {
        GlobalPosition = position;
        Command = default;
        LastMovement = Velocity = Vector2.Zero;
        _accumulator = 0;
        ResetPhysicsInterpolation();
    }
}
