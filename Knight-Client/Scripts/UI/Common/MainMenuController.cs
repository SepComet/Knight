#nullable enable
using System;

namespace SepCore.UI.Common;

public sealed class MainMenuController : ScreenController<MainMenuView>
{
    public event Action? StartRequested, SettingsRequested, QuitRequested, AccountRequested;
    private bool _authenticated, _busy;
    private string _name = "";

    public void SetAccount(bool authenticated, string name, bool busy)
    {
        _authenticated = authenticated;
        _name = name;
        _busy = busy;
        View?.RenderAccount(authenticated, name, busy);
    }

    protected override void Render(MainMenuView view, object? userData)
        => view.RenderAccount(_authenticated, _name, _busy);

    protected override void Wire(MainMenuView view)
    {
        view.StartRequested += () => { if (_authenticated && !_busy) StartRequested?.Invoke(); };
        view.AccountRequested += () => { if (!_busy) AccountRequested?.Invoke(); };
        view.SettingsRequested += () => SettingsRequested?.Invoke();
        view.QuitRequested += () => QuitRequested?.Invoke();
    }
}
