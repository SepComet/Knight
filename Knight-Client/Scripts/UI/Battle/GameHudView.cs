#nullable enable
using System;
using Godot;
using SepCore.Runtime.UI;

namespace SepCore.UI.Battle;

public partial class GameHudView : UIForm
{
    public event Action<bool>? PredictionToggled;
    public event Action<bool>? InterpolationToggled;
    public event Action? NetDebugToggleRequested;

    private Button? _netDebugToggleButton;
    private Control? _netDebugBody;
    private Label? _fpsValueLabel;
    private Label? _rttValueLabel;
    private Label? _lossValueLabel;
    private Label? _reconcileValueLabel;
    private Label? _posErrorValueLabel;
    private CheckButton? _predictionToggle;
    private CheckButton? _interpolationToggle;
    private bool _netDebugWired;

    private Control? _bossPanel;
    private Label? _bossNameLabel;
    private ProgressBar? _bossHealthBar;
    private Label? _bossHealthText;

    private Label? _skillNameLabel;
    private ProgressBar? _skillCooldownBar;
    private Label? _skillCooldownText;
    private Label? _buffStatusLabel;

    private Label? _roomProgressLabel;
    private Label? _doorStateLabel;
    private Label? _minimapLabel;

    public override void OnOpen(object? userData)
    {
        WireControls();
        Render(userData as GameHudModel ?? GameHudModel.Empty);
        RenderSkill(SkillHudModel.Default);
        RenderBoss(BossHudModel.Hidden);
        RenderDungeonProgress(DungeonProgressModel.Empty);
        RenderNetDebug(NetSyncDebugModel.Default);
    }

    public override void OnClose()
    {
        UnwireNetDebugControls();
    }

    public override void _UnhandledInput(InputEvent @event)
    {
        if (@event is InputEventKey key && key.Pressed && !key.Echo && key.Keycode == Key.F3)
        {
            NetDebugToggleRequested?.Invoke();
            GetViewport()?.SetInputAsHandled();
        }
    }

    public void Render(GameHudModel model)
    {
        RenderResource("Health", model.Health);
        RenderResource("Mana", model.Mana);
        RenderResource("Shield", model.Shield);
        GetNode<Label>("%StageLabel").Text = model.StageText;
    }

    public void RenderSkill(SkillHudModel model)
    {
        WireControls();
        if (_skillNameLabel != null)
            _skillNameLabel.Text = model.SkillName;

        var total = float.IsFinite(model.CooldownTotal) && model.CooldownTotal > 0f ? model.CooldownTotal : 1f;
        var rem = float.IsFinite(model.CooldownRemaining) ? Math.Clamp(model.CooldownRemaining, 0f, total) : 0f;
        if (_skillCooldownBar != null)
        {
            _skillCooldownBar.MaxValue = total;
            _skillCooldownBar.Value = total - rem;
        }
        if (_skillCooldownText != null)
        {
            _skillCooldownText.Text = model.StatusText;
            _skillCooldownText.Modulate = model.IsReady ? new Color("86efac") : new Color("fde047");
        }
        if (_buffStatusLabel != null)
        {
            var flags = model.StatusFlags;
            var buffText = "";
            if ((flags & 1u) != 0) buffText += "[无敌] ";
            if ((flags & 2u) != 0) buffText += "[护盾] ";
            if ((flags & 4u) != 0) buffText += "[攻击+50%] ";
            _buffStatusLabel.Text = buffText.Trim();
            _buffStatusLabel.Visible = !string.IsNullOrEmpty(buffText);
        }
    }

    public void RenderBoss(BossHudModel model)
    {
        WireControls();
        if (_bossPanel == null)
            return;

        _bossPanel.Visible = model.Visible;
        if (!model.Visible)
            return;

        if (_bossNameLabel != null)
        {
            _bossNameLabel.Text = model.DisplayTitle;
            _bossNameLabel.Modulate = model.IsPhase2 ? new Color("ff5c8a") : new Color("fca5a5");
        }

        var maxHp = Math.Max(1, model.MaxHp);
        var curHp = Math.Clamp(model.CurrentHp, 0, maxHp);
        if (_bossHealthBar != null)
        {
            _bossHealthBar.MaxValue = maxHp;
            _bossHealthBar.Value = curHp;
            _bossHealthBar.Modulate = model.IsPhase2 ? new Color("ff2e63") : Colors.White;
        }
        if (_bossHealthText != null)
            _bossHealthText.Text = $"{curHp} / {maxHp}";
    }

    public void RenderDungeonProgress(DungeonProgressModel model)
    {
        WireControls();
        if (_roomProgressLabel != null)
            _roomProgressLabel.Text = model.ProgressText;
        if (_doorStateLabel != null)
        {
            _doorStateLabel.Text = model.DoorText;
            _doorStateLabel.Modulate = model.DoorOpen ? new Color("4ade80") : new Color("fbbf24");
        }
        if (_minimapLabel != null)
            _minimapLabel.Text = model.MinimapText;
    }

    public void RenderNetDebug(NetSyncDebugModel model)
    {
        WireControls();
        if (_netDebugToggleButton == null)
            return;

        _netDebugToggleButton.Text = model.Expanded ? "网络同步 [F3] ▼" : "网络同步 [F3] ▶";
        if (_netDebugBody != null)
            _netDebugBody.Visible = model.Expanded;

        var safeFps = float.IsFinite(model.Fps) && model.Fps >= 0f ? model.Fps : 0f;
        var safeRtt = float.IsFinite(model.RttMs) && model.RttMs >= 0f ? model.RttMs : 0f;
        var safeLoss = float.IsFinite(model.LossRate) ? Math.Clamp(model.LossRate, 0f, 1f) : 0f;
        var safeRecon = float.IsFinite(model.ReconciliationsPerSecond) && model.ReconciliationsPerSecond >= 0f
            ? model.ReconciliationsPerSecond
            : 0f;
        var safeErr = float.IsFinite(model.AveragePositionError) && model.AveragePositionError >= 0f
            ? model.AveragePositionError
            : 0f;

        if (_fpsValueLabel != null)
            _fpsValueLabel.Text = $"{safeFps:0}";
        if (_rttValueLabel != null)
            _rttValueLabel.Text = $"{safeRtt:0} ms";
        if (_lossValueLabel != null)
            _lossValueLabel.Text = $"{safeLoss * 100f:0.0}%";
        if (_reconcileValueLabel != null)
            _reconcileValueLabel.Text = $"{safeRecon:0.0} /s";
        if (_posErrorValueLabel != null)
            _posErrorValueLabel.Text = $"{safeErr:0.000} tile";

        _predictionToggle?.SetPressedNoSignal(model.PredictionEnabled);
        _interpolationToggle?.SetPressedNoSignal(model.InterpolationEnabled);
    }

    private void WireControls()
    {
        if (_netDebugWired)
            return;

        _netDebugToggleButton = GetNodeOrNull<Button>("%NetDebugToggleButton");
        _netDebugBody = GetNodeOrNull<Control>("%NetDebugBody");
        _fpsValueLabel = GetNodeOrNull<Label>("%FpsValueLabel");
        _rttValueLabel = GetNodeOrNull<Label>("%RttValueLabel");
        _lossValueLabel = GetNodeOrNull<Label>("%LossValueLabel");
        _reconcileValueLabel = GetNodeOrNull<Label>("%ReconcileValueLabel");
        _posErrorValueLabel = GetNodeOrNull<Label>("%PosErrorValueLabel");
        _predictionToggle = GetNodeOrNull<CheckButton>("%PredictionToggle");
        _interpolationToggle = GetNodeOrNull<CheckButton>("%InterpolationToggle");

        _bossPanel = GetNodeOrNull<Control>("%BossPanel");
        _bossNameLabel = GetNodeOrNull<Label>("%BossNameLabel");
        _bossHealthBar = GetNodeOrNull<ProgressBar>("%BossHealthBar");
        _bossHealthText = GetNodeOrNull<Label>("%BossHealthText");

        _skillNameLabel = GetNodeOrNull<Label>("%SkillNameLabel");
        _skillCooldownBar = GetNodeOrNull<ProgressBar>("%SkillCooldownBar");
        _skillCooldownText = GetNodeOrNull<Label>("%SkillCooldownText");
        _buffStatusLabel = GetNodeOrNull<Label>("%BuffStatusLabel");

        _roomProgressLabel = GetNodeOrNull<Label>("%RoomProgressLabel");
        _doorStateLabel = GetNodeOrNull<Label>("%DoorStateLabel");
        _minimapLabel = GetNodeOrNull<Label>("%MinimapLabel");

        if (_netDebugToggleButton != null)
            _netDebugToggleButton.Pressed += OnNetDebugTogglePressed;
        if (_predictionToggle != null)
            _predictionToggle.Toggled += OnPredictionToggled;
        if (_interpolationToggle != null)
            _interpolationToggle.Toggled += OnInterpolationToggled;

        _netDebugWired = true;
    }

    private void UnwireNetDebugControls()
    {
        if (!_netDebugWired)
            return;

        if (_netDebugToggleButton != null)
            _netDebugToggleButton.Pressed -= OnNetDebugTogglePressed;
        if (_predictionToggle != null)
            _predictionToggle.Toggled -= OnPredictionToggled;
        if (_interpolationToggle != null)
            _interpolationToggle.Toggled -= OnInterpolationToggled;

        _netDebugWired = false;
    }

    private void OnNetDebugTogglePressed() => NetDebugToggleRequested?.Invoke();
    private void OnPredictionToggled(bool toggledOn) => PredictionToggled?.Invoke(toggledOn);
    private void OnInterpolationToggled(bool toggledOn) => InterpolationToggled?.Invoke(toggledOn);

    private void RenderResource(string name, ResourceValue resource)
    {
        var bar = GetNode<ProgressBar>("%" + name + "Bar");
        // Godot requires a nonzero range; the display still reports the real zero maximum.
        bar.MaxValue = resource.SafeMaximum > 0 ? resource.SafeMaximum : 1;
        bar.Value = resource.SafeCurrent;
        GetNode<Label>("%" + name + "Text").Text = $"{resource.SafeCurrent:0.##} / {resource.SafeMaximum:0.##}";
    }
}
