#nullable enable
using System;
using System.Collections.Generic;
using Godot;
using SepCore.UI.Common;

namespace SepCore.UI.Rooms;

public partial class RoomSelectionView : ScreenView
{
    public event Action? CreateRequested, JoinRequested, BackRequested, RefreshRequested;
    public event Action<string>? RoomSelected;
    public RoomPasswordView PasswordDialog { get; private set; } = null!;
    private VBoxContainer _list = null!;
    private RoomListItemView _template = null!;
    private readonly List<RoomListItemView> _items = new();

    public override void _Ready()
    {
        PasswordDialog = GetNode<RoomPasswordView>("%RoomPasswordDialog");
        _list = GetNode<VBoxContainer>("%RoomList");
        _template = GetNode<RoomListItemView>("%RoomItemTemplate");
        GetNode<Button>("%CreateRoomButton").Pressed += () => CreateRequested?.Invoke();
        GetNode<Button>("%JoinRoomButton").Pressed += () => JoinRequested?.Invoke();
        GetNode<Button>("%BackButton").Pressed += () => BackRequested?.Invoke();
        GetNode<Button>("%RefreshButton").Pressed += () => RefreshRequested?.Invoke();
    }

    public void Render(RoomBrowserModel model)
    {
        ClearItems();
        foreach (var room in model.Rooms)
        {
            var item = (RoomListItemView)_template.Duplicate((int)(Node.DuplicateFlags.Groups |
                Node.DuplicateFlags.Scripts | Node.DuplicateFlags.UseInstantiation));
            item.UniqueNameInOwner = false;
            _list.AddChild(item);
            item.Render(room);
            item.SetSelected(room.Id == model.SelectedId);
            item.Selected += id => RoomSelected?.Invoke(id);
            item.Show();
            _items.Add(item);
        }
        GetNode<Button>("%JoinRoomButton").Disabled = model.Selected == null;
        ShowMessage(model.Rooms.Count == 0 ? "暂无房间" : "");
    }

    public void RenderSelection(RoomBrowserModel model)
    {
        for (var i = 0; i < _items.Count; i++)
            _items[i].SetSelected(model.Rooms[i].Id == model.SelectedId);
        GetNode<Button>("%JoinRoomButton").Disabled = model.Selected == null;
    }

    public void ShowMessage(string message)
    {
        var label = GetNode<Label>("%ListMessageLabel");
        label.Text = message;
        label.Visible = message.Length > 0;
    }

    public void SetBusy(bool busy, bool canJoin)
    {
        GetNode<Button>("%CreateRoomButton").Disabled = busy;
        GetNode<Button>("%JoinRoomButton").Disabled = busy || !canJoin;
        GetNode<Button>("%BackButton").Disabled = busy;
        GetNode<Button>("%RefreshButton").Disabled = busy;
    }

    private void ClearItems()
    {
        foreach (var item in _items)
        {
            _list.RemoveChild(item);
            item.QueueFree();
        }
        _items.Clear();
    }

    public override void OnClose()
    {
        PasswordDialog.Close();
        ClearItems();
    }

    public override void OnPause()
    {
        PasswordDialog.Close();
        base.OnPause();
    }
}
