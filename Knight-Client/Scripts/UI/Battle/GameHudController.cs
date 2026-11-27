#nullable enable
using System;
using SepCore.Runtime.UI;

namespace SepCore.UI.Battle;

public sealed class GameHudController : UIController<GameHudView>
{
    private GameHudView? _boundView;

    public GameHudModel Model { get; private set; } = GameHudModel.Empty;
    public SkillHudModel SkillModel { get; private set; } = SkillHudModel.Default;
    public BossHudModel BossModel { get; private set; } = BossHudModel.Hidden;
    public DungeonProgressModel DungeonModel { get; private set; } = DungeonProgressModel.Empty;
    public NetSyncDebugModel NetDebugModel { get; private set; } = NetSyncDebugModel.Default;

    public event Action<bool>? PredictionToggled;
    public event Action<bool>? InterpolationToggled;
    public event Action<bool>? NetDebugExpandedChanged;

    public override void OnOpenRequest(object? userData)
    {
        if (userData is GameHudModel model) Model = model;
        if (View is { } existing)
        {
            BindView(existing);
            existing.Render(Model);
            existing.RenderSkill(SkillModel);
            existing.RenderBoss(BossModel);
            existing.RenderDungeonProgress(DungeonModel);
            existing.RenderNetDebug(NetDebugModel);
            UI.BringToTop(existing);
        }
        else
        {
            UI.CreateView(FormId, Model);
            if (View is { } created)
            {
                BindView(created);
                created.RenderSkill(SkillModel);
                created.RenderBoss(BossModel);
                created.RenderDungeonProgress(DungeonModel);
                created.RenderNetDebug(NetDebugModel);
            }
        }
    }

    public override void OnCloseRequest()
    {
        UnbindView();
        SkillModel = SkillHudModel.Default;
        BossModel = BossHudModel.Hidden;
        DungeonModel = DungeonProgressModel.Empty;
        NetDebugModel = NetSyncDebugModel.Default;
        base.OnCloseRequest();
    }

    public void SetModel(GameHudModel model)
    {
        Model = model;
        View?.Render(Model);
    }

    public void SetSkillModel(SkillHudModel model)
    {
        SkillModel = model;
        View?.RenderSkill(model);
    }

    public void SetBossModel(BossHudModel model)
    {
        BossModel = model;
        View?.RenderBoss(model);
    }

    public void SetDungeonProgress(DungeonProgressModel model)
    {
        DungeonModel = model;
        View?.RenderDungeonProgress(model);
    }

    public void SetNetDebugModel(NetSyncDebugModel model)
    {
        NetDebugModel = model;
        if (View is { } view)
        {
            BindView(view);
            view.RenderNetDebug(model);
        }
    }

    public void SetNetDebugExpanded(bool expanded)
    {
        if (NetDebugModel.Expanded == expanded)
        {
            View?.RenderNetDebug(NetDebugModel);
            return;
        }

        NetDebugModel = NetDebugModel with { Expanded = expanded };
        View?.RenderNetDebug(NetDebugModel);
        NetDebugExpandedChanged?.Invoke(expanded);
    }

    public bool ToggleNetDebugExpanded()
    {
        var next = !NetDebugModel.Expanded;
        SetNetDebugExpanded(next);
        return next;
    }

    private void BindView(GameHudView view)
    {
        if (ReferenceEquals(_boundView, view))
            return;

        UnbindView();
        _boundView = view;
        _boundView.PredictionToggled += OnViewPredictionToggled;
        _boundView.InterpolationToggled += OnViewInterpolationToggled;
        _boundView.NetDebugToggleRequested += OnViewNetDebugToggleRequested;
    }

    private void UnbindView()
    {
        if (_boundView == null)
            return;

        _boundView.PredictionToggled -= OnViewPredictionToggled;
        _boundView.InterpolationToggled -= OnViewInterpolationToggled;
        _boundView.NetDebugToggleRequested -= OnViewNetDebugToggleRequested;
        _boundView = null;
    }

    private void OnViewPredictionToggled(bool enabled)
    {
        NetDebugModel = NetDebugModel with { PredictionEnabled = enabled };
        PredictionToggled?.Invoke(enabled);
    }

    private void OnViewInterpolationToggled(bool enabled)
    {
        NetDebugModel = NetDebugModel with { InterpolationEnabled = enabled };
        InterpolationToggled?.Invoke(enabled);
    }

    private void OnViewNetDebugToggleRequested()
    {
        ToggleNetDebugExpanded();
    }
}
