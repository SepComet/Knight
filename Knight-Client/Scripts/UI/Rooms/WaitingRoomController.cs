#nullable enable
using System;
using System.Collections.Generic;
using SepCore.Runtime.UI;
namespace SepCore.UI.Rooms;
public sealed class WaitingRoomController : UIController<WaitingRoomView>
{
    public WaitingRoomModel Model { get; } = new();
    public event Action? LeaveRequested;
    public event Action? StartRequested;
    public event Action<bool>? ReadyChanged;
    public void SetPlayers(string roomCode, string localPlayerId, bool isHost, IEnumerable<WaitingPlayer> players)
    {
        Model.SetPlayers(roomCode, localPlayerId, isHost, players); View?.Render(Model);
    }
    public override void OnOpenRequest(object? userData)
    {
        var view = View;
        if (view == null)
        {
            view = (WaitingRoomView)UI.CreateView(FormId);
            view.LeaveRequested += () => LeaveRequested?.Invoke();
            view.ReadyRequested += ready => { if (Model.SetReady(ready)) { View?.Render(Model); ReadyChanged?.Invoke(ready); } };
            view.StartRequested += () => { if (Model.CanStart) StartRequested?.Invoke(); };
        }
        else UI.BringToTop(view);
        view.Render(Model);
    }
}
