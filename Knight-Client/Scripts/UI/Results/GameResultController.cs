#nullable enable
using System;
using System.Collections.Generic;
using SepCore.Runtime.UI;

namespace SepCore.UI.Results;

public sealed class GameResultController : UIController<GameResultView>
{
    public GameResultModel Model { get; } = new();
    public event Action? ReturnRoomRequested;
    public event Action? ReturnMenuRequested;

    public void SetResults(IEnumerable<PlayerResult> players)
    {
        Model.SetResults(players);
        View?.Render(Model);
    }

    public void SetMatchResult(ulong matchId, bool isVictory, uint durationSec, IEnumerable<PlayerResult> players)
    {
        Model.SetMatchSummary(matchId, isVictory, durationSec);
        Model.SetResults(players);
        View?.Render(Model);
    }

    public override void OnOpenRequest(object? userData)
    {
        var view = View;
        if (view == null)
        {
            view = (GameResultView)UI.CreateView(FormId);
            view.ReturnRoomRequested += () => ReturnRoomRequested?.Invoke();
            view.ReturnMenuRequested += () => ReturnMenuRequested?.Invoke();
        }
        else
        {
            UI.BringToTop(view);
        }
        view.Render(Model);
    }
}
