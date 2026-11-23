#nullable enable
using System;
using System.Collections.Generic;
using Godot;
using SepCore.UI.Common;

namespace SepCore.UI.Auth;

public partial class LoginView : ScreenView
{
    public event Action? CloseRequested;
    public event Action<AuthMode>? ModeRequested;
    public event Action<string, string, string>? SubmitRequested;
    private AuthMode _mode;
    private bool _busy, _required;
    private LineEdit _loginAccount = null!, _loginPassword = null!;
    private LineEdit _registerAccount = null!, _registerPassword = null!, _confirmation = null!;

    public override void _Ready()
    {
        _loginAccount = GetNode<LineEdit>("%LoginAccountInput");
        _loginPassword = GetNode<LineEdit>("%LoginPasswordInput");
        _registerAccount = GetNode<LineEdit>("%RegisterAccountInput");
        _registerPassword = GetNode<LineEdit>("%RegisterPasswordInput");
        _confirmation = GetNode<LineEdit>("%ConfirmPasswordInput");
        GetNode<Button>("%CloseButton").Pressed += () => { if (!_required) CloseRequested?.Invoke(); };
        GetNode<Button>("%SwitchToRegisterButton").Pressed += () => { if (!_busy) ModeRequested?.Invoke(AuthMode.Register); };
        GetNode<Button>("%SwitchToLoginButton").Pressed += () => { if (!_busy) ModeRequested?.Invoke(AuthMode.Login); };
        GetNode<Button>("%LoginButton").Pressed += Submit;
        GetNode<Button>("%RegisterButton").Pressed += Submit;
        _loginPassword.TextSubmitted += _ => Submit();
        _confirmation.TextSubmitted += _ => Submit();
    }

    private void Submit()
    {
        if (_busy) return;
        if (_mode == AuthMode.Login)
            SubmitRequested?.Invoke(_loginAccount.Text.Trim(), _loginPassword.Text, "");
        else
            SubmitRequested?.Invoke(_registerAccount.Text.Trim(), _registerPassword.Text, _confirmation.Text);
    }

    public void Render(AuthMode mode)
    {
        ClearPasswords();
        _mode = mode;
        GetNode<Control>("%LoginFields").Visible = mode == AuthMode.Login;
        GetNode<Control>("%RegisterFields").Visible = mode == AuthMode.Register;
        GetNode<Label>("%TitleLabel").Text = mode == AuthMode.Login ? "登录" : "注册";
        ShowMessage("");
        UpdateFocusCycle();
        if (!_busy) (mode == AuthMode.Login ? _loginAccount : _registerAccount).GrabFocus();
    }

    public void ShowMessage(string message)
    {
        var label = GetNode<Label>("%MessageLabel");
        label.Text = message;
        label.Visible = message.Length > 0;
    }

    public void ClearPasswords()
    {
        _loginPassword.Clear();
        _registerPassword.Clear();
        _confirmation.Clear();
    }

    public void SetBusy(bool busy)
    {
        _busy = busy;
        foreach (var name in new[] { "LoginButton", "RegisterButton", "SwitchToRegisterButton", "SwitchToLoginButton" })
            GetNode<Button>("%" + name).Disabled = busy;
        foreach (var field in new[] { _loginAccount, _loginPassword, _registerAccount, _registerPassword, _confirmation })
            field.Editable = !busy;
        UpdateFocusCycle();
    }

    public void SetRequired(bool required)
    {
        _required = required;
        GetNode<Button>("%CloseButton").Disabled = required;
        UpdateFocusCycle();
    }

    private void UpdateFocusCycle()
    {
        var controls = new List<Control>();
        if (!_busy)
        {
            if (_mode == AuthMode.Login)
                controls.AddRange(new Control[] { _loginAccount, _loginPassword, GetNode<Button>("%LoginButton"), GetNode<Button>("%SwitchToRegisterButton") });
            else
                controls.AddRange(new Control[] { _registerAccount, _registerPassword, _confirmation, GetNode<Button>("%RegisterButton"), GetNode<Button>("%SwitchToLoginButton") });
        }
        if (!_required) controls.Add(GetNode<Button>("%CloseButton"));
        for (var i = 0; i < controls.Count; i++)
        {
            var current = controls[i];
            var next = current.GetPathTo(controls[(i + 1) % controls.Count]);
            var previous = current.GetPathTo(controls[(i + controls.Count - 1) % controls.Count]);
            current.FocusNext = current.FocusNeighborBottom = current.FocusNeighborRight = next;
            current.FocusPrevious = current.FocusNeighborTop = current.FocusNeighborLeft = previous;
        }
    }

    public override void _UnhandledKeyInput(InputEvent @event)
    {
        if (!_required && IsVisibleInTree() && @event.IsActionPressed("ui_cancel"))
        {
            CloseRequested?.Invoke();
            GetViewport().SetInputAsHandled();
        }
    }

    public override void OnClose() => ClearPasswords();
}
