using Godot;

namespace SepCore.Gameplay.Player;

/// <summary>Local aim selection; targets opt in through the attackable group and leave it on death.</summary>
public static class PlayerAim
{
    public static Node2D NearestTarget(SceneTree tree, Vector2 origin)
    {
        Node2D nearest = null;
        var best = float.PositiveInfinity;
        foreach (var node in tree.GetNodesInGroup("attackable"))
        {
            if (node is not Node2D target || !target.IsVisibleInTree() || target.IsQueuedForDeletion()) continue;
            var distance = origin.DistanceSquaredTo(target.GlobalPosition);
            if (distance >= best) continue;
            nearest = target;
            best = distance;
        }
        return nearest;
    }
}
