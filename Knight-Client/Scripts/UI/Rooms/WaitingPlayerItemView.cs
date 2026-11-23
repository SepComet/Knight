#nullable enable
using Godot;
namespace SepCore.UI.Rooms;
public partial class WaitingPlayerItemView : PanelContainer
{
    public void Render(WaitingPlayer player)
    {
        GetNode<Label>("%PlayerNameLabel").Text = player.Name;
        var role = GetNode<Label>("%RoleLabel");
        role.Text = player.Role; role.Visible = !string.IsNullOrWhiteSpace(player.Role);
        GetNode<Label>("%ReadyStatusLabel").Text = player.IsReady ? "✓" : "—";
    }
}
