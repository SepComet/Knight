#nullable enable
using Godot;
namespace SepCore.UI.Results;
public partial class PlayerResultItemView : PanelContainer
{
    public void Render(PlayerResult player)
    {
        GetNode<Label>("%PlayerNameLabel").Text = player.Name;
        GetNode<Label>("%WeaponsLabel").Text = player.Weapons;
        GetNode<Label>("%DamageLabel").Text = System.Math.Max(0, player.TotalDamage).ToString("N0");
    }
}
