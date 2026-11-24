#nullable enable
using Godot;

namespace SepCore.Gameplay.Player;

/// <summary>Renders Ninja Adventure direction columns and walk animation rows.</summary>
public partial class PlayerPresentation : Node2D
{
    [Export] public PlayerMotor Motor { get; set; } = null!;
    [Export] public Texture2D IdleTexture { get; set; } = null!;
    [Export] public Texture2D WalkTexture { get; set; } = null!;
    [Export] public Texture2D DeadTexture { get; set; } = null!;
    [Export(PropertyHint.Range, "1,30,1")] public float WalkFramesPerSecond { get; set; } = 8;
    private Sprite2D _sprite = null!;
    private double _walkTime;

    public override void _Ready() => _sprite = GetNode<Sprite2D>("%BodySprite");

    public override void _Process(double delta)
    {
        if (!Motor.Alive)
        {
            _sprite.Texture = DeadTexture;
            _sprite.Hframes = _sprite.Vframes = 1;
            _sprite.Frame = 0;
            _walkTime = 0;
            return;
        }
        var walking = Motor.IsMoving;
        _walkTime = walking ? _walkTime + delta : 0;
        var facing = Vector2.FromAngle(Motor.FacingRadians);
        var direction = Mathf.Abs(facing.X) > Mathf.Abs(facing.Y)
            ? (facing.X < 0 ? 2 : 3) : (facing.Y < 0 ? 1 : 0);
        _sprite.Texture = walking ? WalkTexture : IdleTexture;
        _sprite.Hframes = 4;
        _sprite.Vframes = walking ? 4 : 1;
        _sprite.Frame = direction + (walking ? (int)(_walkTime * WalkFramesPerSecond) % 4 * 4 : 0);
    }
}
