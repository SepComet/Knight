#nullable enable
using System;
using Godot;
namespace SepCore.UI.Rooms;
public partial class RoomListItemView : Button
{
    private string _id = "";
    public event Action<string>? Selected;
    public override void _Ready() => Pressed += OnSelected;
    private void OnSelected() { if (!Disabled && _id.Length > 0) Selected?.Invoke(_id); }
    public void Render(RoomSummary room)
    {
        _id = room.Id;
        Disabled = !RoomBrowserModel.CanJoin(room);
        SetPressedNoSignal(false);
        GetNode<Label>("%RoomCodeLabel").Text = room.Number;
        GetNode<Label>("%PlayerCountLabel").Text = $"{room.PlayerCount}/{room.Capacity}";
        GetNode<Label>("%StatusLabel").Text = room.Status;
        GetNode<Label>("%PasswordLabel").Text = room.HasPassword ? "有" : "无";
    }
    public void SetSelected(bool selected) => SetPressedNoSignal(!Disabled && selected);
    public override void _ExitTree() { Pressed -= OnSelected; Selected = null; }
}
