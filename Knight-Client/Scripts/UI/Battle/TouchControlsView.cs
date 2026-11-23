#nullable enable
using System;
using System.Collections.Generic;
using Godot;
using SepCore.Runtime.UI;

namespace SepCore.UI.Battle;

public partial class TouchControlsView : UIForm
{
    public event Action<Vector2>? MovementChanged;
    public event Action<bool>? AttackChanged;
    public event Action? SwitchWeaponRequested;
    public event Action? SkillRequested;
    public event Action? InteractRequested;

    private Window? _window;
    private Control _joystick = null!;
    private Button _knob = null!;
    private Button _attack = null!;
    private Button _switch = null!;
    private Button _skill = null!;
    private Button _interact = null!;
    private readonly Dictionary<int, int> _touchActions = new();
    private int? _moveTouch;
    private bool _moveMouse;
    private bool _attackMouse;
    private bool _attackHeld;
    private bool _active = true;
    private Vector2 _movement;
    private Label _primaryHint = null!;

    public void SetPrimaryInteraction(bool interaction)
    {
        _primaryHint.Text = interaction ? "交互" : "攻击";
        _attack.Modulate = interaction ? new Color("80e2b3") : Colors.White;
    }

    public override void _Ready()
    {
        _joystick = GetNode<Control>("%MoveJoystick");
        _knob = GetNode<Button>("%JoystickKnob");
        _attack = GetNode<Button>("%AttackButton");
        _switch = GetNode<Button>("%SwitchWeaponButton");
        _skill = GetNode<Button>("%SkillButton");
        _interact = GetNode<Button>("%InteractButton");
        _interact.Pressed += OnInteract;
        _interact.Hide();
        _primaryHint = new Label {
            MouseFilter = MouseFilterEnum.Ignore,
            HorizontalAlignment = HorizontalAlignment.Center,
            VerticalAlignment = VerticalAlignment.Bottom
        };
        _attack.AddChild(_primaryHint);
        _primaryHint.SetAnchorsAndOffsetsPreset(LayoutPreset.FullRect);
        SetPrimaryInteraction(false);
        _knob.MouseFilter = MouseFilterEnum.Ignore;
        _attack.ButtonDown += OnAttackDown;
        _attack.ButtonUp += OnAttackUp;
        _switch.Pressed += OnSwitch;
        _skill.Pressed += OnSkill;
        VisibilityChanged += OnVisibilityChanged;
        _window = GetWindow();
        _window.FocusExited += ResetInput;
        CenterKnob();
    }

    public override void OnOpen(object? userData)
    {
        ResetInput();
        SetActive(true);
    }

    public override void OnClose()
    {
        ResetInput();
        SetActive(false);
    }

    public override void OnPause()
    {
        ResetInput();
        SetActive(false);
    }

    public override void OnResume() => SetActive(true);

    public override void _ExitTree()
    {
        if (_window != null)
        {
            _window.FocusExited -= ResetInput;
        }
        ResetInput();
    }

    public override void _Notification(int what)
    {
        if (what == NotificationApplicationFocusOut || what == NotificationDisabled)
        {
            ResetInput();
        }
    }

    private void SetActive(bool active)
    {
        _active = active;
        _attack.Disabled = _switch.Disabled = _skill.Disabled = _interact.Disabled = !active;
    }

    private void OnVisibilityChanged()
    {
        if (!IsVisibleInTree())
        {
            ResetInput();
        }
    }

    private void OnAttackDown()
    {
        if (_active)
        {
            _attackMouse = true;
            RefreshAttack();
        }
    }

    private void OnAttackUp()
    {
        _attackMouse = false;
        RefreshAttack();
    }

    private void OnSwitch()
    {
        if (_active) SwitchWeaponRequested?.Invoke();
    }

    private void OnInteract() { if (_active) InteractRequested?.Invoke(); }

    private void OnSkill()
    {
        if (_active) SkillRequested?.Invoke();
    }

    public override void _Input(InputEvent input)
    {
        if (!_active || !IsVisibleInTree()) return;
        if (input is InputEventScreenTouch touch)
        {
            if (!touch.Pressed || touch.Canceled)
            {
                if (_moveTouch == touch.Index)
                {
                    _moveTouch = null;
                    SetMovement(Vector2.Zero);
                    Consume();
                }

                if (_touchActions.Remove(touch.Index))
                {
                    RefreshAttack();
                    Consume();
                }

                return;
            }

            if (_moveTouch == null && !_moveMouse && Contains(_joystick, touch.Position))
            {
                _moveTouch = touch.Index;
                UpdateMovement(touch.Position);
                Consume();
                return;
            }

            var action = Contains(_attack, touch.Position) ? 0 :
                Contains(_switch, touch.Position) ? 1 :
                Contains(_skill, touch.Position) ? 2 : -1;
            if (action >= 0)
            {
                _touchActions[touch.Index] = action;
                if (action == 0) RefreshAttack();
                else if (action == 1) OnSwitch();
                else if (action == 2) OnSkill();
                else OnInteract();
                Consume();
            }
        }
        else if (input is InputEventScreenDrag drag && _moveTouch == drag.Index)
        {
            UpdateMovement(drag.Position);
            Consume();
        }
        else if (input is InputEventMouseButton mouse && mouse.ButtonIndex == MouseButton.Left)
        {
            if (mouse.Pressed && _moveTouch == null && Contains(_joystick, mouse.Position))
            {
                _moveMouse = true;
                UpdateMovement(mouse.Position);
                Consume();
            }
            else if (!mouse.Pressed && _moveMouse)
            {
                _moveMouse = false;
                SetMovement(Vector2.Zero);
                Consume();
            }
        }
        else if (input is InputEventMouseMotion motion && _moveMouse)
        {
            UpdateMovement(motion.Position);
            Consume();
        }
    }

    private static Vector2 Local(Control control, Vector2 viewportPosition)
        => control.GetGlobalTransformWithCanvas().AffineInverse() * viewportPosition;

    private static bool Contains(Control control, Vector2 position)
        => new Rect2(Vector2.Zero, control.Size).HasPoint(Local(control, position));

    private void Consume() => GetViewport().SetInputAsHandled();

    private float Radius => Math.Max(1,
        (Math.Min(_joystick.Size.X, _joystick.Size.Y) - Math.Max(_knob.Size.X, _knob.Size.Y)) / 2);

    private void UpdateMovement(Vector2 position)
        => SetMovement(((Local(_joystick, position) - _joystick.Size / 2) / Radius).LimitLength());

    private void CenterKnob() => _knob.Position = (_joystick.Size - _knob.Size) / 2 + _movement * Radius;

    private void SetMovement(Vector2 value)
    {
        var changed = !_movement.IsEqualApprox(value);
        _movement = value;
        CenterKnob();
        if (changed) MovementChanged?.Invoke(value);
    }

    private void RefreshAttack()
    {
        var held = _attackMouse || _touchActions.ContainsValue(0);
        if (held == _attackHeld) return;
        _attackHeld = held;
        AttackChanged?.Invoke(held);
    }

    public void ResetInput()
    {
        _moveTouch = null;
        _moveMouse = _attackMouse = false;
        _touchActions.Clear();
        if (_knob != null) SetMovement(Vector2.Zero);
        RefreshAttack();
    }
}

