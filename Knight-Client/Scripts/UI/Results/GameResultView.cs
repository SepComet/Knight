#nullable enable
using System;
using Godot;
using SepCore.Runtime.UI;
namespace SepCore.UI.Results;
public partial class GameResultView : UIForm
{
    public event Action? ReturnRoomRequested;
    public event Action? ReturnMenuRequested;
    public override void _Ready()
    {
        GetNode<Button>("%ReturnRoomButton").Pressed += () => ReturnRoomRequested?.Invoke();
        GetNode<Button>("%ReturnMenuButton").Pressed += () => ReturnMenuRequested?.Invoke();
    }
    public override void OnOpen(object? userData) { }
    public void Render(GameResultModel model)
    {
        ClearResults();
        var parent = GetNode<Container>("%PlayerResults");
        var template = GetNode<PlayerResultItemView>("%PlayerResultTemplate");
        foreach (var player in model.Players)
        {
            var row = (PlayerResultItemView)template.Duplicate((int)(Node.DuplicateFlags.Groups | Node.DuplicateFlags.Scripts | Node.DuplicateFlags.UseInstantiation));
            row.UniqueNameInOwner = false;
            parent.AddChild(row); row.Render(player); row.Show();
        }
    }
    private void ClearResults()
    {
        var parent = GetNode<Container>("%PlayerResults");
        var template = GetNode<PlayerResultItemView>("%PlayerResultTemplate");
        foreach (var child in parent.GetChildren()) if (child != template) { parent.RemoveChild(child); child.QueueFree(); }
    }
    public override void OnClose() { ClearResults(); ReturnRoomRequested = null; ReturnMenuRequested = null; }
    public override void OnPause() => ProcessMode = ProcessModeEnum.Disabled;
    public override void OnResume() => ProcessMode = ProcessModeEnum.Inherit;
}
