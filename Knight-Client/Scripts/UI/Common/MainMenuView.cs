#nullable enable
using System;
using System.Collections.Generic;
using Godot;
using SepCore.UI.Results;

namespace SepCore.UI.Common;

public partial class MainMenuView : ScreenView
{
    public event Action? StartRequested, SettingsRequested, QuitRequested, AccountRequested;
    public event Action? HistoryRequested, HistoryRefreshRequested, HistoryCloseRequested;

    public override void _Ready()
    {
        GetNode<Button>("%AccountButton").Pressed += () => AccountRequested?.Invoke();
        GetNode<Button>("%StartButton").Pressed += () => StartRequested?.Invoke();
        GetNode<Button>("%SettingsButton").Pressed += () => SettingsRequested?.Invoke();
        GetNode<Button>("%QuitButton").Pressed += () => QuitRequested?.Invoke();

        var historyBtn = GetNodeOrNull<Button>("%HistoryButton");
        if (historyBtn != null)
            historyBtn.Pressed += () => HistoryRequested?.Invoke();

        var refreshBtn = GetNodeOrNull<Button>("%MatchHistoryRefreshButton");
        if (refreshBtn != null)
            refreshBtn.Pressed += () => HistoryRefreshRequested?.Invoke();

        var closeBtn = GetNodeOrNull<Button>("%MatchHistoryCloseButton");
        if (closeBtn != null)
            closeBtn.Pressed += () => HistoryCloseRequested?.Invoke();
    }

    public void RenderAccount(bool authenticated, string name, bool busy)
    {
        GetNode<Label>("%AccountLabel").Text = busy ? "登录中…" : authenticated ? $"{name} · 退出登录" : "登录";
        var account = GetNode<Button>("%AccountButton");
        account.Disabled = busy;
        account.TooltipText = authenticated ? "退出登录" : "登录 / 注册";
        GetNode<Button>("%StartButton").Disabled = busy || !authenticated;
        var historyBtn = GetNodeOrNull<Button>("%HistoryButton");
        if (historyBtn != null)
            historyBtn.Disabled = busy || !authenticated;
    }

    public void RenderMatchHistory(bool visible, bool loading, string statusMessage, IReadOnlyList<MatchHistoryEntry> entries)
    {
        var panel = GetNodeOrNull<Control>("%MatchHistoryPanel");
        if (panel == null)
            return;

        panel.Visible = visible;
        if (!visible)
            return;

        var statusLabel = GetNodeOrNull<Label>("%MatchHistoryStatusLabel");
        if (statusLabel != null)
        {
            if (loading)
                statusLabel.Text = "正在加载历史战绩…";
            else if (!string.IsNullOrWhiteSpace(statusMessage))
                statusLabel.Text = statusMessage;
            else if (entries.Count == 0)
                statusLabel.Text = "暂无历史战绩记录";
            else
                statusLabel.Text = $"共 {entries.Count} 条历史战绩";
        }

        var refreshBtn = GetNodeOrNull<Button>("%MatchHistoryRefreshButton");
        if (refreshBtn != null)
            refreshBtn.Disabled = loading;

        var list = GetNodeOrNull<VBoxContainer>("%MatchHistoryList");
        if (list == null)
            return;

        foreach (var child in list.GetChildren())
        {
            list.RemoveChild(child);
            child.QueueFree();
        }

        foreach (var entry in entries)
        {
            var row = new PanelContainer { CustomMinimumSize = new Vector2(0, 44) };
            var label = new Label
            {
                Text = $"#{entry.MatchId} [{entry.OutcomeText}] 耗时 {entry.FormattedDuration} · 击杀 {entry.Kills} · 输出 {entry.DamageDealt} · 承伤 {entry.DamageTaken} · {(entry.Alive ? "存活" : "阵亡")}",
                VerticalAlignment = VerticalAlignment.Center,
            };
            label.AddThemeFontSizeOverride("font_size", 14);
            label.Modulate = entry.IsVictory ? new Color("86efac") : new Color("fca5a5");
            row.AddChild(label);
            list.AddChild(row);
        }
    }
}
