#nullable enable
using System;
using Godot;

namespace SepCore.UI.Rooms;

/// <summary>Embedded dialog view; its owning screen controller manages the mode.</summary>
public partial class RoomPasswordView : Control
{
    public event Action<string>? SubmitRequested;
    public event Action? CancelRequested;
    private LineEdit _password = null!;
    private Control? _previousFocus;

    public override void _Ready()
    {
        _password = GetNode<LineEdit>("%PasswordInput");
        GetNode<Button>("%ConfirmButton").Pressed += () => SubmitRequested?.Invoke(_password.Text);
        _password.TextSubmitted += _ => SubmitRequested?.Invoke(_password.Text);
        GetNode<Button>("%CloseButton").Pressed += () => CancelRequested?.Invoke();
        GetNode<Button>("%CancelButton").Pressed += () => CancelRequested?.Invoke();
        Control[] focusOrder = { _password, GetNode<Button>("%ConfirmButton"),
            GetNode<Button>("%CancelButton"), GetNode<Button>("%CloseButton") };
        for (var i = 0; i < focusOrder.Length; i++)
        {
            var current = focusOrder[i];
            var next = current.GetPathTo(focusOrder[(i + 1) % focusOrder.Length]);
            var previous = current.GetPathTo(focusOrder[(i + focusOrder.Length - 1) % focusOrder.Length]);
            current.FocusNext = current.FocusNeighborBottom = current.FocusNeighborRight = next;
            current.FocusPrevious = current.FocusNeighborTop = current.FocusNeighborLeft = previous;
        }
    }

    public void Open(RoomPasswordMode mode)
    {
        if (!Visible) _previousFocus = GetViewport().GuiGetFocusOwner();
        _password.Clear();
        GetNode<Label>("%TitleLabel").Text = mode == RoomPasswordMode.Create ? "设置密码" : "输入密码";
        _password.PlaceholderText = mode == RoomPasswordMode.Create ? "密码（可选）" : "密码";
        GetNode<Label>("%ConfirmLabel").Text = mode == RoomPasswordMode.Create ? "创建" : "加入";
        ShowMessage("");
        Show();
        _password.GrabFocus();
    }

    public void Close()
    {
        _password.Clear();
        ShowMessage("");
        Hide();
        if (GodotObject.IsInstanceValid(_previousFocus) && _previousFocus!.IsInsideTree() &&
            !_previousFocus.IsQueuedForDeletion() && _previousFocus.IsVisibleInTree())
            _previousFocus.GrabFocus();
        _previousFocus = null;
    }

    public void ShowMessage(string message)
    {
        var label = GetNode<Label>("%MessageLabel");
        label.Text = message;
        label.Visible = message.Length > 0;
    }

    public override void _UnhandledKeyInput(InputEvent @event)
    {
        if (IsVisibleInTree() && @event.IsActionPressed("ui_cancel"))
        {
            CancelRequested?.Invoke();
            GetViewport().SetInputAsHandled();
        }
    }
}
