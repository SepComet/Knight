#nullable enable
using System;
using Godot;
using SepCore.UI.Battle;

namespace SepCore.Gameplay.Player;

/// <summary>Connects input sources to a local motor. Does not move nodes or implement combat.</summary>
public partial class LocalPlayerInput : Node
{
    [Export] public PlayerMotor Motor { get; set; } = null!;
    [Export] public bool TouchMode { get; set; }
    public event Action<PlayerCommand>? CommandSampled;
    public event Action? SwitchWeaponRequested;
    public event Action? SkillRequested;
    public bool InteractionAvailable { get; set; }
    public bool IsInteractionMode => _primary.IsInteractionMode(InteractionAvailable);
    public bool HasSyntheticInput => _synthetic.IsActive;
    private readonly PrimaryActionState _primary = new();
    private readonly PlayerInputBuffer _input = new();
    private readonly SyntheticInputOverride _synthetic = new();
    private TouchControlsView? _touch;
    private bool _focused = true;
    private bool _enabled = true;
    private bool _wasAlive = true;

    public override void _Ready() => Motor.InputSource = Sample;

    public void SetSyntheticInput(
        Vector2 movement,
        float? aimRadians = null,
        bool attackHeld = false,
        bool interact = false,
        double? durationSeconds = null)
        => _synthetic.Set(movement, aimRadians, attackHeld, interact, durationSeconds);

    public void ClearSyntheticInput() => _synthetic.Clear();

    public void BindTouch(TouchControlsView view)
    {
        UnbindTouch();
        _touch = view;
        view.MovementChanged += OnMovement;
        view.AttackChanged += OnAttack;
        view.SwitchWeaponRequested += OnSwitchWeapon;
        view.SkillRequested += OnSkill;
    }

    public void SetEnabled(bool enabled)
    {
        _enabled = enabled;
        _input.Reset();
        _primary.Reset();
        if (!enabled) _synthetic.Clear();
        _touch?.ResetInput();
    }

    public override void _UnhandledInput(InputEvent input)
    {
        if (!_enabled || !_focused || !Motor.Alive || TouchMode || input.IsEcho()) return;
        if (input.IsActionPressed("player_attack")) _primary.SetHeld(true, InteractionAvailable);
        if (input.IsActionReleased("player_attack")) _primary.SetHeld(false, InteractionAvailable);
        if (input.IsActionPressed("player_switch_weapon")) OnSwitchWeapon();
        if (input.IsActionPressed("player_skill")) OnSkill();
    }

    public override void _Process(double delta)
    {
        _synthetic.Advance(delta);
        if (_wasAlive && !Motor.Alive)
        {
            _input.Reset();
            _primary.Reset();
            _synthetic.Clear();
            _touch?.ResetInput();
        }
        _wasAlive = Motor.Alive;
        _touch?.SetPrimaryInteraction(IsInteractionMode);
    }

    private PlayerCommand Sample()
    {
        if (!_enabled || !Motor.Alive)
        {
            _input.Reset();
            _synthetic.Clear();
            return new(Vector2.Zero, Motor.FacingRadians, false, false);
        }
        if (_synthetic.TrySample(Motor.FacingRadians, out var syntheticCommand))
        {
            CommandSampled?.Invoke(syntheticCommand);
            return syntheticCommand;
        }
        if (!_focused) { _input.Reset(); return new(Vector2.Zero, Motor.FacingRadians, false, false); }
        var aim = Motor.FacingRadians;
        var canAttack = true;
        if (TouchMode)
        {
            var target = PlayerAim.NearestTarget(GetTree(), Motor.GlobalPosition);
            canAttack = target != null;
            if (target != null) aim = (target.GlobalPosition - Motor.GlobalPosition).Angle();
        }
        else
        {
            _input.Movement = Input.GetVector("player_left", "player_right", "player_up", "player_down");
            // Only unhandled presses begin an action. Always observe release, even over GUI.
            if (!Input.IsActionPressed("player_attack")) _primary.SetHeld(false, InteractionAvailable);
            var offset = Motor.GetGlobalMousePosition() - Motor.GlobalPosition;
            if (!offset.IsZeroApprox()) aim = offset.Angle();
        }
        _input.AttackHeld = _primary.AttackHeld;
        if (_primary.ConsumeInteraction()) _input.RequestInteraction();
        var command = _input.Consume(aim, canAttack);
        CommandSampled?.Invoke(command);
        return command;
    }

    public override void _Notification(int what)
    {
        if (what == NotificationApplicationFocusOut) { _focused = false; _input.Reset(); _primary.Reset(); }
        if (what == NotificationApplicationFocusIn) _focused = true;
    }

    private void OnMovement(Vector2 movement) => _input.Movement = movement;
    private void OnAttack(bool held)
    {
        if (_enabled && _focused && Motor.Alive) _primary.SetHeld(held, InteractionAvailable);
    }
    private void OnSwitchWeapon() { if (_enabled && _focused && Motor.Alive) SwitchWeaponRequested?.Invoke(); }
    private void OnSkill() { if (_enabled && _focused && Motor.Alive) SkillRequested?.Invoke(); }
    private void UnbindTouch()
    {
        if (_touch == null) return;
        _touch.MovementChanged -= OnMovement;
        _touch.AttackChanged -= OnAttack;
        _touch.SwitchWeaponRequested -= OnSwitchWeapon;
        _touch.SkillRequested -= OnSkill;
        _touch = null;
        _input.Reset();
        _primary.Reset();
    }

    public override void _ExitTree()
    {
        UnbindTouch();
        if (GodotObject.IsInstanceValid(Motor)) Motor.InputSource = null;
    }
}
