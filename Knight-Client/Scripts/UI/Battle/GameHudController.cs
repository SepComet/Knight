#nullable enable
using SepCore.Runtime.UI;

namespace SepCore.UI.Battle;

public sealed class GameHudController : UIController<GameHudView>
{
    public GameHudModel Model { get; private set; } = GameHudModel.Empty;
    public override void OnOpenRequest(object? userData)
    {
        if (userData is GameHudModel model) Model = model;
        if (View is { } existing)
        {
            existing.Render(Model);
            UI.BringToTop(existing);
        }
        else UI.CreateView(FormId, Model);
    }

    public void SetModel(GameHudModel model)
    {
        Model = model;
        View?.Render(Model);
    }
}
