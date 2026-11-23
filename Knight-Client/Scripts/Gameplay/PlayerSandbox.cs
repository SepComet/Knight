using System;
using System.Collections.Generic;
using System.Linq;
using Godot;
using SepCore.Gameplay.Player;
using SepCore.UI.Battle;

namespace SepCore.Gameplay;

/// <summary>Local test-room composition. All effects here are demonstrations, not combat rules.</summary>
public partial class PlayerSandbox : Node2D
{
    public static readonly Vector2 StationPosition = new(180, 360);
    public const float StationRadius = 50;
    private PlayerMotor _player;
    private LocalPlayerInput _input;
    private TouchControlsView _touch;
    private Label _status, _feedback, _mode;
    private CheckButton _allowInteraction;
    private int _interactions, _shots, _weapon;
    private float _shotCooldown, _pulse, _skillCooldown, _feedbackTime;
    private bool _stationLit;
    private readonly List<(Vector2 Start, Vector2 End, float Life)> _tracers = new();
    private readonly Rect2[] _walls = {
        new(0, 0, 960, 20), new(0, 620, 960, 20), new(0, 0, 20, 640), new(940, 0, 20, 640),
        new(340, 180, 32, 180), new(520, 360, 150, 32), new(750, 160, 32, 130)
    };

    public override void _Ready()
    {
        RenderingServer.SetDefaultClearColor(new Color("101b25"));
        foreach (var rect in _walls)
        {
            var wall = new StaticBody2D { Position = rect.GetCenter() };
            wall.AddChild(new CollisionShape2D { Shape = new RectangleShape2D { Size = rect.Size } });
            AddChild(wall);
        }
        foreach (var position in new[] { new Vector2(270, 180), new Vector2(580, 220), new Vector2(800, 460) })
        {
            var target = new Node2D { Position = position };
            AddChild(target);
            target.AddToGroup("attackable");
            AddWorldLabel("练习靶", position + new Vector2(-20, 20));
        }
        AddWorldLabel("碰撞 / 滑墙", new(328, 380));
        AddWorldLabel("交互测试台", StationPosition + new Vector2(-40, 32));
        AddWorldLabel("靠近并开启允许交互", StationPosition + new Vector2(-64, 54));
        _player = GD.Load<PackedScene>("res://Prefabs/Entity/Player.tscn").Instantiate<PlayerMotor>();
        _player.Position = new Vector2(180, 250);
        AddChild(_player);
        _input = _player.GetNode<LocalPlayerInput>("%LocalInput");
        _input.CommandSampled += OnCommand;
        _input.SwitchWeaponRequested += SwitchWeapon;
        _input.SkillRequested += UseSkill;
        var camera = new PlayerCamera { Target = _player, Zoom = new Vector2(1.5f, 1.5f), Position = _player.Position };
        AddChild(camera);
        camera.SetRoomBounds(new Rect2(0, 0, 960, 640));
        BuildHud();
        if (OS.GetCmdlineUserArgs().Contains("--sandbox-capture")) Capture();
    }

    private void BuildHud()
    {
        var canvas = new CanvasLayer();
        AddChild(canvas);
        _touch = GD.Load<PackedScene>("res://Prefabs/UI/TouchControls.tscn").Instantiate<TouchControlsView>();
        canvas.AddChild(_touch);
        _touch.OnOpen(null);
        _touch.GetNode<Button>("%InteractButton").Hide();
        _input.BindTouch(_touch);
        _input.TouchMode = OS.HasFeature("mobile") || OS.GetCmdlineUserArgs().Contains("--sandbox-touch");
        _touch.Visible = _input.TouchMode;
        var panel = new PanelContainer { Position = new Vector2(20, 18), MouseFilter = Control.MouseFilterEnum.Ignore };
        var style = new StyleBoxFlat { BgColor = new Color("102433ed"), CornerRadiusTopLeft = 12,
            CornerRadiusTopRight = 12, CornerRadiusBottomLeft = 12, CornerRadiusBottomRight = 12,
            ContentMarginLeft = 18, ContentMarginRight = 18, ContentMarginTop = 12, ContentMarginBottom = 12 };
        panel.AddThemeStyleboxOverride("panel", style);
        canvas.AddChild(panel);
        var box = new VBoxContainer { MouseFilter = Control.MouseFilterEnum.Ignore };
        panel.AddChild(box);
        var title = new Label { Text = "玩家控制器 · 本地训练场", MouseFilter = Control.MouseFilterEnum.Ignore };
        title.AddThemeFontSizeOverride("font_size", 23);
        title.Modulate = new Color("8ee3ce");
        box.AddChild(title);
        _status = new Label { MouseFilter = Control.MouseFilterEnum.Ignore };
        box.AddChild(_status);
        _mode = new Label { MouseFilter = Control.MouseFilterEnum.Ignore };
        _mode.Modulate = new Color("f5d38b");
        box.AddChild(_mode);
        var tools = new VBoxContainer { AnchorLeft = 1, AnchorRight = 1, OffsetLeft = -232, OffsetRight = -20, OffsetTop = 18 };
        canvas.AddChild(tools);
        Button AddButton(string text, Action action)
        {
            var button = new Button { Text = text, CustomMinimumSize = new Vector2(212, 36) };
            button.Pressed += action;
            tools.AddChild(button);
            return button;
        }
        AddButton("切换键鼠 / 触屏", () => {
            _input.SetEnabled(false);
            _input.TouchMode = !_input.TouchMode;
            _touch.Visible = _input.TouchMode;
            _input.SetEnabled(true);
            Notify(_input.TouchMode ? "触屏模式：拖动左摇杆，主按钮自动瞄准" : "键鼠模式：WASD + 鼠标左键");
        });
        _allowInteraction = new CheckButton { Text = "允许交互（测试条件）", CustomMinimumSize = new Vector2(212, 36) };
        tools.AddChild(_allowInteraction);
        AddButton("切换武器  [Q]", SwitchWeapon);
        AddButton("使用技能  [Space]", UseSkill);
        AddButton("切换存活 / 死亡", () => { _player.Alive = !_player.Alive; _input.SetEnabled(_player.Alive); Notify(_player.Alive ? "已恢复操作" : "死亡：移动与操作已禁用"); });
        AddButton("重置位置", () => { _input.SetEnabled(false); _player.Alive = true; _player.Teleport(new Vector2(180, 250)); _input.SetEnabled(true); Notify("已返回出生点"); });
        _feedback = new Label { AnchorTop = 1, AnchorBottom = 1, OffsetLeft = 24, OffsetTop = -46, OffsetBottom = -12,
            MouseFilter = Control.MouseFilterEnum.Ignore, Text = "WASD 移动 · 鼠标瞄准 / 左键主操作 · Q 切武器 · Space 技能" };
        _feedback.Modulate = new Color("a9c5d5");
        canvas.AddChild(_feedback);
    }

    private void AddWorldLabel(string text, Vector2 position)
    {
        var label = new Label { Text = text, Position = position, MouseFilter = Control.MouseFilterEnum.Ignore };
        label.AddThemeFontSizeOverride("font_size", 11);
        label.Modulate = new Color("a8c7d0");
        AddChild(label);
    }

    private void OnCommand(PlayerCommand command)
    {
        if (command.AttackHeld && _shotCooldown <= 0)
        {
            _shots++;
            _shotCooldown = _weapon == 0 ? 0.15f : 0.45f;
            var count = _weapon == 0 ? 1 : 5;
            for (var i = 0; i < count; i++)
            {
                var direction = Vector2.FromAngle(command.AimRadians + (i - (count - 1) / 2f) * 0.09f);
                _tracers.Add((_player.Position + direction * 10, _player.Position + direction * 105, 0.13f));
            }
        }
        if (command.InteractPressed && _input.InteractionAvailable)
        {
            _interactions++;
            _stationLit = !_stationLit;
            Notify($"测试台已{(_stationLit ? "点亮" : "关闭")} · 交互 {_interactions} 次");
        }
    }

    private void SwitchWeapon()
    {
        if (!_player.Alive) return;
        _weapon = 1 - _weapon;
        Notify($"已切换为{(_weapon == 0 ? "步枪（连续示意线）" : "散弹（扇形示意线）")}");
    }

    private void UseSkill()
    {
        if (!_player.Alive) return;
        if (_skillCooldown > 0) { Notify($"技能冷却中：{_skillCooldown:0.0}s"); return; }
        _skillCooldown = 3;
        _pulse = 0.65f;
        Notify("技能已触发：扩散光环（测试表现）");
    }

    private void Notify(string text) { _feedback.Text = text; _feedbackTime = 3; }

    public override void _Process(double delta)
    {
        if (_player == null) return;
        var dt = (float)delta;
        _shotCooldown -= dt;
        _skillCooldown = Mathf.Max(0, _skillCooldown - dt);
        _pulse = Mathf.Max(0, _pulse - dt);
        _feedbackTime -= dt;
        for (var i = _tracers.Count - 1; i >= 0; i--)
        {
            var shot = _tracers[i];
            if (shot.Life <= dt) _tracers.RemoveAt(i);
            else _tracers[i] = (shot.Start, shot.End, shot.Life - dt);
        }
        _input.InteractionAvailable = _player.Alive && _allowInteraction.ButtonPressed && _player.Position.DistanceTo(StationPosition) <= StationRadius;
        _touch.SetPrimaryInteraction(_input.IsInteractionMode);
        _status.Text = $"{(_input.TouchMode ? "触屏" : "键鼠")} · {(_player.Alive ? "存活" : "死亡")} · {(_weapon == 0 ? "步枪" : "散弹")} · 开火 {_shots} · 交互 {_interactions}\n" +
            $"技能 {(_skillCooldown > 0 ? $"{_skillCooldown:0.0}s" : "就绪")}  |  左键按住攻击；按下交互一次";
        _mode.Text = _input.IsInteractionMode ? "主按钮：交互 · 点击操作测试台" : "主按钮：攻击 · 仅允许交互且靠近测试台时切换";
        if (_feedbackTime <= 0) _feedback.Text = "WASD 移动 · 鼠标瞄准 / 左键主操作 · Q 切武器 · Space 技能";
        QueueRedraw();
    }

    public override void _Draw()
    {
        DrawRect(new Rect2(0, 0, 960, 640), new Color("203440"));
        for (var x = 0; x < 960; x += 32) DrawLine(new(x, 0), new(x, 640), new Color("293f4a"));
        for (var y = 0; y < 640; y += 32) DrawLine(new(0, y), new(960, y), new Color("293f4a"));
        foreach (var wall in _walls)
        {
            DrawRect(new Rect2(wall.Position + new Vector2(4, 6), wall.Size), new Color("142630"));
            DrawRect(wall, new Color("526d77"));
            DrawLine(wall.Position, new Vector2(wall.End.X, wall.Position.Y), new Color("91a8ac"), 3);
        }
        DrawCircle(StationPosition, StationRadius, new Color(0.25f, 0.7f, 0.65f, 0.12f));
        DrawArc(StationPosition, StationRadius, 0, Mathf.Tau, 64, new Color("669e9a"), 1);
        DrawRect(new Rect2(StationPosition - new Vector2(15, 14), new Vector2(30, 28)), new Color("496a73"));
        DrawRect(new Rect2(StationPosition - new Vector2(10, 9), new Vector2(20, 14)), _stationLit ? new Color("a3f0a5") : new Color("3d5664"));
        foreach (var node in GetTree().GetNodesInGroup("attackable"))
            if (node is Node2D target)
            {
                DrawCircle(target.Position, 14, new Color("b26254"));
                DrawCircle(target.Position, 9, new Color("efac76"));
                DrawCircle(target.Position, 4, new Color("724d48"));
            }
        if (_player == null) return;
        DrawLine(_player.Position, _player.Position + Vector2.FromAngle(_player.FacingRadians) * 28, new Color("8ad7c2"), 1);
        foreach (var shot in _tracers) DrawLine(shot.Start, shot.End, new Color(1, 0.85f, 0.43f, shot.Life / 0.13f), 2);
        if (_pulse > 0) DrawArc(_player.Position, 12 + (1 - _pulse / 0.65f) * 85, 0, Mathf.Tau, 64, new Color(0.48f, 0.9f, 1, _pulse / 0.65f), 3);
    }

    private async void Capture()
    {
        for (var i = 0; i < 8; i++) await ToSignal(GetTree(), SceneTree.SignalName.ProcessFrame);
        await ToSignal(RenderingServer.Singleton, RenderingServer.SignalName.FramePostDraw);
        var filename = _input.TouchMode ? "player-sandbox-touch.png" : "player-sandbox.png";
        var path = ProjectSettings.GlobalizePath($"res://../.scratch/{filename}");
        GetViewport().GetTexture().GetImage().SavePng(path);
        GD.Print($"[PlayerSandbox] capture: {path}");
        GetTree().Quit();
    }
}
