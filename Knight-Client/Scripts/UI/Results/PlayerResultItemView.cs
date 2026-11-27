#nullable enable
using System;
using Godot;

namespace SepCore.UI.Results;

public partial class PlayerResultItemView : PanelContainer
{
    public void Render(PlayerResult player)
    {
        var statusTag = player.Alive ? "[存活]" : "[阵亡]";
        GetNode<Label>("%PlayerNameLabel").Text = $"{player.Name} {statusTag}";
        GetNode<Label>("%WeaponsLabel").Text = $"击杀：{player.Kills} · {player.Weapons}";
        GetNode<Label>("%DamageLabel").Text = $"输出：{Math.Max(0, player.TotalDamage):N0} / 承伤：{Math.Max(0, player.DamageTaken):N0}";
    }
}
