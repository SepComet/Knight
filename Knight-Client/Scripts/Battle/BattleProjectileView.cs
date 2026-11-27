#nullable enable
using Godot;

namespace SepCore.Battle;

/// <summary>
/// 投射物实体视图（实现 <see cref="IBattleEntityView"/>）。
/// 按服务端 config_id 区分玩家步枪/霰弹/连发/榴弹、远程怪火球与 Boss 暗影弹表现。
/// </summary>
public partial class BattleProjectileView : Node2D, IBattleEntityView
{
    public Node2D Node => this;
    public uint ConfigId { get; private set; }

    private Color _coreColor = new("fff4a3");
    private Color _glowColor = new("ff9f43", 0.45f);
    private float _radius = 4.5f;
    private bool _elongated = true;

    public void Configure(uint configId)
    {
        ConfigId = configId;
        (_radius, _coreColor, _glowColor, _elongated) = configId switch
        {
            // 1 = 玩家步枪弹
            1 => (4.2f, new Color("fff2a6"), new Color("f7b731", 0.48f), true),
            // 2 = 玩家霰弹
            2 => (3.5f, new Color("ffd166"), new Color("ff7f50", 0.48f), false),
            // 3 = 玩家脉冲连发
            3 => (4.0f, new Color("74f9ff"), new Color("00b4d8", 0.52f), true),
            // 4 = 玩家爆炸榴弹
            4 => (6.5f, new Color("ff6b35"), new Color("ffbe0b", 0.55f), false),
            // 11 = 远程怪火球
            11 => (5.5f, new Color("ff595e"), new Color("ff924c", 0.55f), false),
            // 12 = Boss 暗影弹
            12 => (7.5f, new Color("d66bff"), new Color("7b2cbf", 0.6f), false),
            _ => (4.5f, new Color("ffffff"), new Color("74b9ff", 0.45f), false),
        };
        QueueRedraw();
    }

    public void ApplyWorldState(Vector2 godotPosition, float facingRadians, bool moving, bool alive)
    {
        if (godotPosition.IsFinite())
            GlobalPosition = godotPosition;
        if (float.IsFinite(facingRadians))
            Rotation = facingRadians;
        Visible = alive;
    }

    public override void _Draw()
    {
        DrawCircle(Vector2.Zero, _radius * 1.8f, _glowColor);
        if (_elongated)
        {
            var rect = new Rect2(-_radius * 1.6f, -_radius * 0.65f, _radius * 3.2f, _radius * 1.3f);
            DrawRect(rect, _coreColor);
        }
        else
        {
            DrawCircle(Vector2.Zero, _radius, _coreColor);
            DrawCircle(Vector2.Zero, _radius * 0.45f, Colors.White);
        }
    }
}
