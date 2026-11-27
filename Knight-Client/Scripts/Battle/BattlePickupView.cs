#nullable enable
using System;
using Godot;

namespace SepCore.Battle;

/// <summary>
/// 场景道具实体视图（实现 <see cref="IBattleEntityView"/>）：
/// - config_id = 1: 回血药水 (+35HP)
/// - config_id = 2: 攻击增益 (+50%伤害/15s)
/// - config_id = 3: 护盾核心 (减伤50%/5s)
/// </summary>
public partial class BattlePickupView : Node2D, IBattleEntityView
{
    public Node2D Node => this;
    public uint ConfigId { get; private set; }

    private Sprite2D? _sprite;
    private Label? _label;
    private Color _ringColor = new("2ecc71");
    private float _bobTime;

    public void Configure(uint configId)
    {
        ConfigId = configId;
        var (title, color, texturePath) = configId switch
        {
            1 => ("回血药水 [E]", new Color("2ecc71"), "res://Textures/Ninja Adventure - Asset Pack/Items/Potion/LifePot.png"),
            2 => ("攻击增益 [E]", new Color("f39c12"), "res://Textures/Ninja Adventure - Asset Pack/Items/Potion/MilkPot.png"),
            3 => ("护盾核心 [E]", new Color("38bdf8"), "res://Textures/Ninja Adventure - Asset Pack/Items/Potion/WaterPot.png"),
            _ => ("道具 [E]", new Color("a3e635"), "res://Textures/Ninja Adventure - Asset Pack/Items/Potion/Heart.png"),
        };
        _ringColor = color;

        if (_sprite == null)
        {
            _sprite = new Sprite2D { TextureFilter = TextureFilterEnum.Nearest, Scale = new Vector2(1.4f, 1.4f) };
            AddChild(_sprite);
        }

        if (ResourceLoader.Exists(texturePath))
        {
            _sprite.Texture = GD.Load<Texture2D>(texturePath);
            _sprite.Visible = true;
        }
        else
        {
            _sprite.Visible = false;
        }

        if (_label == null)
        {
            _label = new Label
            {
                Position = new Vector2(-46, -28),
                Size = new Vector2(92, 16),
                HorizontalAlignment = HorizontalAlignment.Center,
                MouseFilter = Control.MouseFilterEnum.Ignore,
            };
            _label.AddThemeFontSizeOverride("font_size", 10);
            AddChild(_label);
        }

        _label.Text = title;
        _label.Modulate = color.Lightened(0.25f);
        QueueRedraw();
    }

    public void ApplyWorldState(Vector2 godotPosition, float facingRadians, bool moving, bool alive)
    {
        if (godotPosition.IsFinite())
            GlobalPosition = godotPosition;
        Visible = alive;
    }

    public override void _Process(double delta)
    {
        _bobTime += (float)Math.Clamp(delta, 0.0, 0.1) * 3.5f;
        if (_sprite != null)
            _sprite.Position = new Vector2(0, MathF.Sin(_bobTime) * 3f);
        QueueRedraw();
    }

    public override void _Draw()
    {
        var pulse = 1.0f + 0.12f * MathF.Sin(_bobTime);
        DrawCircle(new Vector2(0, 6f), 11f * pulse, new Color(_ringColor.R, _ringColor.G, _ringColor.B, 0.22f));
        DrawArc(new Vector2(0, 6f), 11f * pulse, 0f, Mathf.Tau, 20, _ringColor, 1.5f);
        if (_sprite == null || !_sprite.Visible)
            DrawCircle(Vector2.Zero, 7f, _ringColor);
    }
}
