#nullable enable
using System;
using Godot;

namespace SepCore.UI.Common;

public partial class MainMenuView : ScreenView
{
    public event Action? StartRequested, SettingsRequested, QuitRequested, AccountRequested;
    public override void _Ready()
    {
        GetNode<Button>("%AccountButton").Pressed += () => AccountRequested?.Invoke();
        GetNode<Button>("%StartButton").Pressed += () => StartRequested?.Invoke();
        GetNode<Button>("%SettingsButton").Pressed += () => SettingsRequested?.Invoke();
        GetNode<Button>("%QuitButton").Pressed += () => QuitRequested?.Invoke();
    }
    public void RenderAccount(bool authenticated, string name, bool busy)
    {
        GetNode<Label>("%AccountLabel").Text = busy ? "登录中…" : authenticated ? $"{name} · 退出登录" : "登录";
        var account = GetNode<Button>("%AccountButton");
        account.Disabled = busy;
        account.TooltipText = authenticated ? "退出登录" : "登录 / 注册";
        GetNode<Button>("%StartButton").Disabled = busy || !authenticated;
    }
}
