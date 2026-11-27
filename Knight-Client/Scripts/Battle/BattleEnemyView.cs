#nullable enable
using System;
using Godot;

namespace SepCore.Battle;

/// <summary>
/// 小怪与 Boss 实体视图（实现 <see cref="IBattleEntityView"/>）。
/// - 存活时加入 "attackable" 组供触屏自动瞄准（<see cref="SepCore.Gameplay.Player.PlayerAim"/>）；
/// - 渲染精灵/几何剪影、头顶血条、受击闪白与 Boss 二阶段狂暴光环。
/// </summary>
public partial class BattleEnemyView : Node2D, IBattleEntityView
{
    public const uint StatusBossPhase2 = 8u;

    public Node2D Node => this;
    public bool IsBoss { get; private set; }
    public uint ConfigId { get; private set; }
    public int CurrentHp { get; private set; }
    public int MaxHp { get; private set; } = 1;
    public bool IsPhase2 { get; private set; }
    public bool Alive { get; private set; } = true;

    private Sprite2D? _sprite;
    private float _hitFlashTimer;
    private float _phasePulseTime;
    private float _facingRadians;
    private bool _moving;
    private float _radiusPixels = 14f;
    private Color _baseColor = new("e05a47");

    public void Configure(bool isBoss, uint configId, int hp, int maxHp)
    {
        IsBoss = isBoss;
        ConfigId = configId;
        MaxHp = Math.Max(1, maxHp > 0 ? maxHp : Math.Max(1, hp));
        CurrentHp = Math.Clamp(hp, 0, MaxHp);
        Alive = CurrentHp > 0;

        (_radiusPixels, _baseColor, var texturePath, var hFrames, var vFrames, var spriteScale) = ResolveVisualSpec(isBoss, configId);

        if (_sprite == null)
        {
            _sprite = new Sprite2D { TextureFilter = TextureFilterEnum.Nearest };
            AddChild(_sprite);
        }

        var tex = !string.IsNullOrEmpty(texturePath) && ResourceLoader.Exists(texturePath)
            ? GD.Load<Texture2D>(texturePath)
            : null;
        if (tex != null)
        {
            _sprite.Texture = tex;
            _sprite.Hframes = hFrames;
            _sprite.Vframes = vFrames;
            _sprite.Frame = 0;
            _sprite.Scale = new Vector2(spriteScale, spriteScale);
            _sprite.Visible = true;
        }
        else
        {
            _sprite.Texture = null;
            _sprite.Visible = false;
        }

        SyncAttackableGroup();
        QueueRedraw();
    }

    public void UpdateCombatState(int hp, int maxHp, uint statusFlags)
    {
        if (maxHp > 0)
            MaxHp = maxHp;
        CurrentHp = Math.Clamp(hp, 0, MaxHp);
        IsPhase2 = IsBoss && (statusFlags & StatusBossPhase2) != 0;
        if (CurrentHp <= 0)
            Alive = false;

        SyncAttackableGroup();
        QueueRedraw();
    }

    public void TriggerHitFlash()
    {
        _hitFlashTimer = 0.14f;
        QueueRedraw();
    }

    public void ApplyWorldState(Vector2 godotPosition, float facingRadians, bool moving, bool alive)
    {
        if (godotPosition.IsFinite())
            GlobalPosition = godotPosition;
        if (float.IsFinite(facingRadians))
            _facingRadians = facingRadians;
        _moving = alive && moving;
        Alive = alive;
        SyncAttackableGroup();
    }

    public override void _Process(double delta)
    {
        var dt = (float)Math.Clamp(delta, 0.0, 0.1);
        var needsRedraw = false;

        if (_hitFlashTimer > 0f)
        {
            _hitFlashTimer = Math.Max(0f, _hitFlashTimer - dt);
            needsRedraw = true;
        }

        if (IsPhase2 && Alive)
        {
            _phasePulseTime += dt * 4.5f;
            needsRedraw = true;
        }

        if (_sprite != null && _sprite.Visible)
        {
            var facingX = MathF.Cos(_facingRadians);
            if (MathF.Abs(facingX) > 0.1f)
                _sprite.FlipH = facingX < 0f;

            if (_hitFlashTimer > 0f)
                _sprite.Modulate = new Color(2.2f, 2.2f, 2.2f, 1f);
            else if (IsPhase2)
                _sprite.Modulate = new Color(1.15f, 0.52f, 0.62f, 1f);
            else if (ConfigId == 3 && !IsBoss)
                _sprite.Modulate = new Color(1.15f, 0.85f, 0.45f, 1f);
            else
                _sprite.Modulate = Colors.White;

            if (_moving && _sprite.Hframes * _sprite.Vframes > 1)
                _sprite.Frame = (int)(Time.GetTicksMsec() / 160UL) % Math.Max(1, _sprite.Hframes);
        }

        if (needsRedraw)
            QueueRedraw();
    }

    public override void _Draw()
    {
        if (!Alive)
            return;

        // 脚底阴影
        DrawCircle(new Vector2(0, _radiusPixels * 0.55f), _radiusPixels * 0.75f, new Color(0f, 0f, 0f, 0.35f));

        // Boss 二阶段脉冲光环
        if (IsPhase2)
        {
            var pulse = 1.0f + 0.14f * MathF.Sin(_phasePulseTime);
            DrawArc(Vector2.Zero, _radiusPixels * 1.35f * pulse, 0f, Mathf.Tau, 28, new Color("ff3b5c", 0.85f), 2.5f);
        }

        // 无贴图兜底绘制
        if (_sprite == null || !_sprite.Visible)
        {
            var bodyColor = _hitFlashTimer > 0f ? Colors.White : (IsPhase2 ? new Color("ff3b5c") : _baseColor);
            DrawCircle(Vector2.Zero, _radiusPixels, bodyColor);
            DrawArc(Vector2.Zero, _radiusPixels, 0f, Mathf.Tau, 20, new Color("1b1b22"), 1.5f);
        }

        // 头顶小血条（Boss 同时在 HUD 顶部显示大血条）
        var barWidth = IsBoss ? 56f : 30f;
        var barHeight = IsBoss ? 5f : 4f;
        var barTop = -_radiusPixels - (IsBoss ? 18f : 10f);
        var bgRect = new Rect2(-barWidth * 0.5f, barTop, barWidth, barHeight);
        var ratio = Math.Clamp((float)CurrentHp / Math.Max(1, MaxHp), 0f, 1f);
        var fillColor = IsBoss
            ? (IsPhase2 ? new Color("ff2e63") : new Color("e84545"))
            : (ConfigId == 3 ? new Color("f39c12") : new Color("e74c3c"));

        DrawRect(bgRect, new Color(0.08f, 0.09f, 0.12f, 0.85f));
        DrawRect(new Rect2(bgRect.Position, new Vector2(barWidth * ratio, barHeight)), fillColor);
    }

    public override void _ExitTree()
    {
        if (IsInGroup("attackable"))
            RemoveFromGroup("attackable");
    }

    private void SyncAttackableGroup()
    {
        if (!IsInsideTree())
            return;

        if (Alive && CurrentHp > 0)
        {
            if (!IsInGroup("attackable"))
                AddToGroup("attackable");
        }
        else if (IsInGroup("attackable"))
        {
            RemoveFromGroup("attackable");
        }
    }

    public override void _EnterTree()
    {
        SyncAttackableGroup();
    }

    private static (float RadiusPx, Color FallbackColor, string TexturePath, int HFrames, int VFrames, float Scale)
        ResolveVisualSpec(bool isBoss, uint configId)
    {
        if (isBoss)
        {
            return (
                30f,
                new Color("c0392b"),
                "res://Textures/Ninja Adventure - Asset Pack/Actor/Boss/DemonCyclop/Idle.png",
                4, 1, 1.35f);
        }

        return configId switch
        {
            // 1 = 近战怪
            1 => (14f, new Color("4cd137"), "res://Textures/Ninja Adventure - Asset Pack/Actor/Monster/Slime/Slime.png", 4, 4, 1.4f),
            // 2 = 远程怪
            2 => (14f, new Color("9b59b6"), "res://Textures/Ninja Adventure - Asset Pack/Actor/Monster/Skull/SpriteSheet.png", 4, 4, 1.4f),
            // 3 = 精英近战卫士
            3 => (18f, new Color("e67e22"), "res://Textures/Ninja Adventure - Asset Pack/Actor/Monster/Slime/Slime.png", 4, 4, 1.85f),
            _ => (14f, new Color("e05a47"), "res://Textures/Ninja Adventure - Asset Pack/Actor/Monster/Slime/Slime.png", 4, 4, 1.4f),
        };
    }
}
