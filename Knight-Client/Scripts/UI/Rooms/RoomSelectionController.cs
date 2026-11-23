#nullable enable
using System;
using System.Collections.Generic;
using SepCore.UI.Common;

namespace SepCore.UI.Rooms;

public sealed class RoomSelectionController : ScreenController<RoomSelectionView>
{
    public RoomBrowserModel Model { get; } = new();
    public event Action? BackRequested;
    public event Action? RefreshRequested;
    public event Action<RoomIntent>? RoomActionRequested;
    private RoomPasswordMode _mode;
    private string? _pendingRoom;
    private bool _busy;

    public void SetBusy(bool busy)
    {
        _busy = busy;
        View?.SetBusy(busy, Model.Selected != null);
        if (busy) View?.ShowMessage("等待服务器确认…");
    }

    public void ShowMessage(string message) => View?.ShowMessage(message);

    public void SetRooms(IEnumerable<RoomSummary> rooms)
    {
        Model.ReplaceRooms(rooms);
        if (View is not { } view) return;
        if (view.PasswordDialog.Visible && _mode == RoomPasswordMode.Join &&
            (Model.Selected is not { } pending || pending.Id != _pendingRoom || !pending.HasPassword))
        {
            view.PasswordDialog.Close();
            _pendingRoom = null;
        }
        view.Render(Model);
        view.SetBusy(_busy, Model.Selected != null);
    }

    protected override void Wire(RoomSelectionView view)
    {
        view.BackRequested += () => BackRequested?.Invoke();
        view.RefreshRequested += () => { if (!_busy) RefreshRequested?.Invoke(); };
        view.CreateRequested += () =>
        {
            if (_busy) return;
            _mode = RoomPasswordMode.Create;
            _pendingRoom = null;
            view.PasswordDialog.Open(_mode);
        };
        view.RoomSelected += id => { if (!_busy) { Model.Select(id); view.RenderSelection(Model); } };
        view.JoinRequested += () =>
        {
            if (_busy) return;
            if (Model.Selected is not { } room) return;
            if (!room.HasPassword)
            {
                RoomActionRequested?.Invoke(new RoomIntent(RoomPasswordMode.Join, room.Id, ""));
                return;
            }
            _mode = RoomPasswordMode.Join;
            _pendingRoom = room.Id;
            view.PasswordDialog.Open(_mode);
        };
        view.PasswordDialog.CancelRequested += () =>
        {
            view.PasswordDialog.Close();
            _pendingRoom = null;
        };
        view.PasswordDialog.SubmitRequested += password =>
        {
            if (_mode == RoomPasswordMode.Join)
            {
                if (Model.Selected is not { } room || room.Id != _pendingRoom || !room.HasPassword)
                {
                    view.PasswordDialog.Close();
                    view.ShowMessage("房间信息已变化，请重新选择");
                    return;
                }
                if (string.IsNullOrWhiteSpace(password))
                {
                    view.PasswordDialog.ShowMessage("请输入密码");
                    return;
                }
            }
            var intent = new RoomIntent(_mode, _pendingRoom, password);
            view.PasswordDialog.Close();
            _pendingRoom = null;
            RoomActionRequested?.Invoke(intent);
        };
    }

    protected override void Render(RoomSelectionView view, object? userData)
    {
        if (userData is IEnumerable<RoomSummary> rooms) Model.ReplaceRooms(rooms);
        view.PasswordDialog.Close();
        _pendingRoom = null;
        view.Render(Model);
        view.SetBusy(_busy, Model.Selected != null);
    }
}
