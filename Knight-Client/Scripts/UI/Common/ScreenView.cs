#nullable enable
using Godot;
using SepCore.Runtime.UI;

namespace SepCore.UI.Common;

public abstract partial class ScreenView : UIForm
{
    public override void OnOpen(object? userData) { }
    public override void OnClose() { }
    public override void OnPause() => ProcessMode = ProcessModeEnum.Disabled;
    public override void OnResume() => ProcessMode = ProcessModeEnum.Inherit;
}
