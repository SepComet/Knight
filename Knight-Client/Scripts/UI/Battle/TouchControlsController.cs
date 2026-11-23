#nullable enable
using System;
using Godot;
using SepCore.Runtime.UI;

namespace SepCore.UI.Battle;

public sealed class TouchControlsController : UIController<TouchControlsView>
{
    public TouchInputState Model { get; private set; }
    public event Action<Vector2>? MovementChanged;
    public event Action<bool>? AttackChanged;
    public event Action? SwitchWeaponRequested;
    public event Action? SkillRequested;
    public event Action? InteractRequested;

    public override void OnOpenRequest(object? userData)
    {
        if (View is { } existing)
        {
            existing.OnOpen(userData);
            UI.BringToTop(existing);
            return;
        }
        var view = (TouchControlsView)UI.CreateView(FormId, userData);
        view.MovementChanged += movement =>
        {
            Model = Model with { Movement = movement };
            MovementChanged?.Invoke(movement);
        };
        view.AttackChanged += held =>
        {
            Model = Model with { AttackHeld = held };
            AttackChanged?.Invoke(held);
        };
        view.SwitchWeaponRequested += () => SwitchWeaponRequested?.Invoke();
        view.SkillRequested += () => SkillRequested?.Invoke();
        view.InteractRequested += () => InteractRequested?.Invoke();
    }
}

