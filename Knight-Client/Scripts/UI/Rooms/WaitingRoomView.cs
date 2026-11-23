#nullable enable
using System;
using Godot;
using SepCore.Runtime.UI;
namespace SepCore.UI.Rooms;
public partial class WaitingRoomView : UIForm
{
    public event Action? LeaveRequested;
    public event Action<bool>? ReadyRequested;
    public event Action? StartRequested;
    public override void _Ready()
    {
        GetNode<Button>("%LeaveRoomButton").Pressed += () => LeaveRequested?.Invoke();
        GetNode<Button>("%ReadyButton").Pressed += () => ReadyRequested?.Invoke(true);
        GetNode<Button>("%CancelReadyButton").Pressed += () => ReadyRequested?.Invoke(false);
        GetNode<Button>("%StartGameButton").Pressed += () => StartRequested?.Invoke();
    }
    public override void OnOpen(object? userData) { }
    public void Render(WaitingRoomModel model)
    {
        ClearPlayers();
        var parent = GetNode<Container>("%Players");
        var template = GetNode<WaitingPlayerItemView>("%PlayerTemplate");
        foreach (var player in model.Players)
        {
            var row = (WaitingPlayerItemView)template.Duplicate((int)(Node.DuplicateFlags.Groups | Node.DuplicateFlags.Scripts | Node.DuplicateFlags.UseInstantiation));
            row.UniqueNameInOwner = false;
            parent.AddChild(row); row.Render(player); row.Show();
        }
        GetNode<Label>("%RoomCodeLabel").Text = string.IsNullOrEmpty(model.RoomCode) ? "—" : model.RoomCode;
        var hasLocal = false;
        foreach (var player in model.Players) hasLocal |= player.Id == model.LocalPlayerId;
        GetNode<Button>("%ReadyButton").Visible = !model.LocalReady;
        GetNode<Button>("%ReadyButton").Disabled = !hasLocal;
        GetNode<Button>("%CancelReadyButton").Visible = model.LocalReady;
        var start = GetNode<Button>("%StartGameButton");
        start.Visible = model.IsHost; start.Disabled = !model.CanStart;
    }
    private void ClearPlayers()
    {
        var parent = GetNode<Container>("%Players");
        var template = GetNode<WaitingPlayerItemView>("%PlayerTemplate");
        foreach (var child in parent.GetChildren()) if (child != template) { parent.RemoveChild(child); child.QueueFree(); }
    }
    public override void OnClose() { ClearPlayers(); LeaveRequested = null; ReadyRequested = null; StartRequested = null; }
    public override void OnPause() => ProcessMode = ProcessModeEnum.Disabled;
    public override void OnResume() => ProcessMode = ProcessModeEnum.Inherit;
}
