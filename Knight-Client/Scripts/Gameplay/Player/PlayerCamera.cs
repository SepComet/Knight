using Godot;

namespace SepCore.Gameplay.Player;

/// <summary>Local view only. Limits are supplied by the room, in Godot pixel coordinates.</summary>
public partial class PlayerCamera : Camera2D
{
    [Export] public Node2D Target { get; set; }
    public void SetRoomBounds(Rect2 bounds)
    {
        LimitLeft = (int)bounds.Position.X;
        LimitTop = (int)bounds.Position.Y;
        LimitRight = (int)bounds.End.X;
        LimitBottom = (int)bounds.End.Y;
    }

    public override void _Process(double delta)
    {
        if (IsInstanceValid(Target)) GlobalPosition = Target.GlobalPosition;
    }
}
