#nullable enable
using System;
using System.Collections.Generic;
using Godot;

namespace SepCore.Battle;

/// <summary>
/// 战斗轻量特效与音效管理器：
/// - 飘字（受击扣血 / 回血 / 增益提示）
/// - 技能冲刺斩光环与受击扩散波
/// - 复用 Ninja Adventure 音效资源，静默兼容无音频设备（headless）环境
/// </summary>
public partial class BattleVfxAndAudio : Node2D
{
    private const int AudioPoolSize = 6;
    private readonly List<AudioStreamPlayer> _audioPool = new();
    private readonly Dictionary<string, AudioStream?> _streamCache = new();
    private int _audioIndex;

    private const string SfxHit = "res://Textures/Ninja Adventure - Asset Pack/Audio/Sounds/Hit & Impact/Hit1.wav";
    private const string SfxShoot = "res://Textures/Ninja Adventure - Asset Pack/Audio/Sounds/Whoosh & Slash/Launch.wav";
    private const string SfxSkill = "res://Textures/Ninja Adventure - Asset Pack/Audio/Sounds/Whoosh & Slash/Slash.wav";
    private const string SfxHeal = "res://Textures/Ninja Adventure - Asset Pack/Audio/Sounds/Magic & Skill/Heal.wav";
    private const string SfxBuff = "res://Textures/Ninja Adventure - Asset Pack/Audio/Sounds/Bonus/PowerUp1.wav";
    private const string SfxDoorOpen = "res://Textures/Ninja Adventure - Asset Pack/Audio/Jingles/Secret1.wav";
    private const string SfxVictory = "res://Textures/Ninja Adventure - Asset Pack/Audio/Jingles/Success1.wav";
    private const string SfxDefeat = "res://Textures/Ninja Adventure - Asset Pack/Audio/Jingles/GameOver.wav";

    public override void _Ready()
    {
        ZIndex = 50;
        for (var i = 0; i < AudioPoolSize; i++)
        {
            var player = new AudioStreamPlayer { VolumeDb = -8f };
            AddChild(player);
            _audioPool.Add(player);
        }
    }

    public void SpawnDamagePopup(Vector2 godotPos, int damage, bool isLocalTarget)
    {
        var color = isLocalTarget ? new Color("ff6b6b") : new Color("ffe066");
        SpawnFloatingText(godotPos + new Vector2(0, -14), $"-{Math.Max(0, damage)}", color);
        SpawnRingEffect(godotPos, 18f, color, 0.22f);
        PlaySfx(SfxHit);
    }

    public void SpawnHealOrBuffPopup(Vector2 godotPos, int healedHp, uint itemConfigId)
    {
        if (healedHp > 0)
        {
            SpawnFloatingText(godotPos + new Vector2(0, -18), $"+{healedHp} HP", new Color("4ade80"));
            SpawnRingEffect(godotPos, 24f, new Color("4ade80"), 0.35f);
            PlaySfx(SfxHeal);
            return;
        }

        var (text, color) = itemConfigId switch
        {
            2 => ("攻击 +50%", new Color("fbbf24")),
            3 => ("护盾激活", new Color("38bdf8")),
            _ => ("道具拾取", new Color("a3e635")),
        };
        SpawnFloatingText(godotPos + new Vector2(0, -18), text, color);
        SpawnRingEffect(godotPos, 24f, color, 0.35f);
        PlaySfx(SfxBuff);
    }

    public void SpawnSkillEffect(Vector2 godotPos, float facingRadians)
    {
        SpawnRingEffect(godotPos, 52f, new Color("38bdf8"), 0.30f);
        SpawnFloatingText(godotPos + new Vector2(0, -24), "冲刺斩!", new Color("7dd3fc"));
        PlaySfx(SfxSkill);
    }

    public void PlayShootSfx() => PlaySfx(SfxShoot);
    public void PlayDoorOpenSfx() => PlaySfx(SfxDoorOpen);
    public void PlayResultSfx(bool victory) => PlaySfx(victory ? SfxVictory : SfxDefeat);

    private void SpawnFloatingText(Vector2 pos, string text, Color color)
    {
        var popup = new FloatingTextNode(text, color) { Position = pos };
        AddChild(popup);
    }

    private void SpawnRingEffect(Vector2 pos, float maxRadius, Color color, float duration)
    {
        var ring = new ExpandingRingNode(maxRadius, color, duration) { Position = pos };
        AddChild(ring);
    }

    private void PlaySfx(string resourcePath)
    {
        if (_audioPool.Count == 0 || !ResourceLoader.Exists(resourcePath))
            return;

        if (!_streamCache.TryGetValue(resourcePath, out var stream))
        {
            stream = GD.Load<AudioStream>(resourcePath);
            _streamCache[resourcePath] = stream;
        }

        if (stream == null)
            return;

        var player = _audioPool[_audioIndex];
        _audioIndex = (_audioIndex + 1) % _audioPool.Count;
        player.Stream = stream;
        player.Play();
    }

    private sealed partial class FloatingTextNode : Node2D
    {
        private readonly Label _label;
        private float _elapsed;
        private const float Lifetime = 0.65f;

        public FloatingTextNode(string text, Color color)
        {
            _label = new Label
            {
                Text = text,
                Position = new Vector2(-36, -10),
                Size = new Vector2(72, 20),
                HorizontalAlignment = HorizontalAlignment.Center,
                MouseFilter = Control.MouseFilterEnum.Ignore,
            };
            _label.AddThemeFontSizeOverride("font_size", 13);
            _label.Modulate = color;
            AddChild(_label);
        }

        public override void _Process(double delta)
        {
            var dt = (float)Math.Clamp(delta, 0.0, 0.1);
            _elapsed += dt;
            Position += new Vector2(0, -34f * dt);
            var alpha = Math.Clamp(1f - _elapsed / Lifetime, 0f, 1f);
            Modulate = new Color(1f, 1f, 1f, alpha);
            if (_elapsed >= Lifetime)
                QueueFree();
        }
    }

    private sealed partial class ExpandingRingNode : Node2D
    {
        private readonly float _maxRadius;
        private readonly Color _color;
        private readonly float _duration;
        private float _elapsed;

        public ExpandingRingNode(float maxRadius, Color color, float duration)
        {
            _maxRadius = maxRadius;
            _color = color;
            _duration = Math.Max(0.05f, duration);
        }

        public override void _Process(double delta)
        {
            _elapsed += (float)Math.Clamp(delta, 0.0, 0.1);
            if (_elapsed >= _duration)
            {
                QueueFree();
                return;
            }
            QueueRedraw();
        }

        public override void _Draw()
        {
            var t = Math.Clamp(_elapsed / _duration, 0f, 1f);
            var radius = 6f + (_maxRadius - 6f) * t;
            var alpha = (1f - t) * 0.85f;
            DrawArc(Vector2.Zero, radius, 0f, Mathf.Tau, 24, new Color(_color.R, _color.G, _color.B, alpha), 2.5f);
        }
    }
}
