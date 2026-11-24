#nullable enable
using System;
using Godot;

namespace SepCore.Gameplay.Player;

/// <summary>Local fixed-step movement. Receives commands; does not read devices or animate.</summary>
public partial class PlayerMotor : CharacterBody2D
{
    [Export(PropertyHint.Range, "1,1000,1")] public float MoveSpeed { get; set; } = 96;
    [Export(PropertyHint.Range, "0.001,0.1,0.001")] public float FixedStep { get; set; } = 1f / 30;

    /// <summary>
    /// 联机权威驱动模式：为 true 时 _PhysicsProcess 仍按 30Hz 采样 InputSource，
    /// 但位置与朝向由服务端快照经 <see cref="ApplyAuthoritativeState"/> 驱动，不执行本地位移。
    /// </summary>
    public bool AuthoritativeDrive { get; set; }

    public Func<PlayerCommand>? InputSource { get; set; }
    public PlayerCommand Command { get; set; }
    public Vector2 LastMovement { get; private set; }
    public bool IsMoving { get; private set; }
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
                IsMoving = false;
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
            if (!AuthoritativeDrive)
                Simulate(Command, step);
            _accumulator -= step;
        }
    }

    /// <summary>
    /// 清洗移动输入向量：过滤 NaN/Inf，模长小于 1e-6 归零，仅在模长大于 1 时归一化（保留 0 &lt; len &lt;= 1 的模拟摇杆幅度，
    /// 与服务端 world/room.go sanitizeMove 逐项等价）。
    /// </summary>
    public static Vector2 SanitizeMoveInput(Vector2 raw)
    {
        var fx = float.IsFinite(raw.X) ? (double)raw.X : 0.0;
        var fy = float.IsFinite(raw.Y) ? (double)raw.Y : 0.0;
        var mag = Math.Sqrt(fx * fx + fy * fy);
        if (mag < 1e-6)
            return Vector2.Zero;
        if (mag > 1.0)
        {
            fx /= mag;
            fy /= mag;
        }
        return new Vector2((float)fx, (float)fy);
    }

    /// <summary>Applies one command in Godot pixel coordinates; later replay can call the same path.</summary>
    public void Simulate(PlayerCommand command, float seconds)
    {
        if (!Alive || !float.IsFinite(seconds) || seconds <= 0) return;
        var movement = SanitizeMoveInput(command.Movement);
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
        IsMoving = !LastMovement.IsZeroApprox();
    }

    public void Teleport(Vector2 position)
    {
        GlobalPosition = position;
        Command = default;
        LastMovement = Velocity = Vector2.Zero;
        IsMoving = false;
        _accumulator = 0;
        ResetPhysicsInterpolation();
    }

    /// <summary>应用服务端权威快照状态（位置、朝向、移动动画与存活状态）。</summary>
    public void ApplyAuthoritativeState(Vector2 position, float facingRadians, bool moving, bool alive)
    {
        if (position.IsFinite())
            GlobalPosition = position;
        if (float.IsFinite(facingRadians))
            FacingRadians = facingRadians;
        Alive = alive;
        IsMoving = alive && moving;
    }
}
