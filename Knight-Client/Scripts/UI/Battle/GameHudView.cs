#nullable enable
using Godot;
using SepCore.Runtime.UI;

namespace SepCore.UI.Battle;

public partial class GameHudView : UIForm
{
    public override void OnOpen(object? userData) => Render(userData as GameHudModel ?? GameHudModel.Empty);
    public override void OnClose() { }

    public void Render(GameHudModel model)
    {
        RenderResource("Health", model.Health);
        RenderResource("Mana", model.Mana);
        RenderResource("Shield", model.Shield);
        GetNode<Label>("%StageLabel").Text = model.StageText;
    }

    private void RenderResource(string name, ResourceValue resource)
    {
        var bar = GetNode<ProgressBar>("%" + name + "Bar");
        // Godot requires a nonzero range; the display still reports the real zero maximum.
        bar.MaxValue = resource.SafeMaximum > 0 ? resource.SafeMaximum : 1;
        bar.Value = resource.SafeCurrent;
        GetNode<Label>("%" + name + "Text").Text = $"{resource.SafeCurrent:0.##} / {resource.SafeMaximum:0.##}";
    }
}
