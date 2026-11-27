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

    public override void OnOpen(object? userData)
    {
        WireNetDebugControls();
        Render(userData as GameHudModel ?? GameHudModel.Empty);
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

    public void RenderNetDebug(NetSyncDebugModel model)
    {
        WireNetDebugControls();
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

    private void WireNetDebugControls()
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
