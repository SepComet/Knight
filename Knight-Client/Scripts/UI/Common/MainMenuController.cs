#nullable enable
using System;
using System.Collections.Generic;
using SepCore.UI.Results;

namespace SepCore.UI.Common;

public sealed class MainMenuController : ScreenController<MainMenuView>
{
    public event Action? StartRequested, SettingsRequested, QuitRequested, AccountRequested;
    public event Action? HistoryRequested, HistoryRefreshRequested, HistoryCloseRequested;

    private bool _authenticated, _busy;
    private string _name = "";

    public bool HistoryVisible { get; private set; }
    public bool HistoryLoading { get; private set; }
    public string HistoryMessage { get; private set; } = "";
    public IReadOnlyList<MatchHistoryEntry> MatchHistory { get; private set; } = Array.Empty<MatchHistoryEntry>();

    public void SetAccount(bool authenticated, string name, bool busy)
    {
        _authenticated = authenticated;
        _name = name;
        _busy = busy;
        View?.RenderAccount(authenticated, name, busy);
    }

    public void SetMatchHistory(bool visible, bool loading, string message, IReadOnlyList<MatchHistoryEntry> entries)
    {
        HistoryVisible = visible;
        HistoryLoading = loading;
        HistoryMessage = message;
        MatchHistory = entries;
        View?.RenderMatchHistory(HistoryVisible, HistoryLoading, HistoryMessage, MatchHistory);
    }

    protected override void Render(MainMenuView view, object? userData)
    {
        view.RenderAccount(_authenticated, _name, _busy);
        view.RenderMatchHistory(HistoryVisible, HistoryLoading, HistoryMessage, MatchHistory);
    }

    protected override void Wire(MainMenuView view)
    {
        view.StartRequested += () => { if (_authenticated && !_busy) StartRequested?.Invoke(); };
        view.AccountRequested += () => { if (!_busy) AccountRequested?.Invoke(); };
        view.SettingsRequested += () => SettingsRequested?.Invoke();
        view.QuitRequested += () => QuitRequested?.Invoke();
        view.HistoryRequested += () => { if (_authenticated && !_busy) HistoryRequested?.Invoke(); };
        view.HistoryRefreshRequested += () => { if (_authenticated && !_busy) HistoryRefreshRequested?.Invoke(); };
        view.HistoryCloseRequested += () => HistoryCloseRequested?.Invoke();
    }
}
