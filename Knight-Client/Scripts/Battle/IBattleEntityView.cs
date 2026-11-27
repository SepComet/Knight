#nullable enable
using Godot;

namespace SepCore.Battle;

/// <summary>
/// 战斗实体视图契约（供 <see cref="BattleWorld"/> 统一驱动玩家、怪物、Boss、投射物等 Godot 节点）。
/// </summary>
public interface IBattleEntityView
{
    Node2D Node { get; }

    /// <summary>应用世界状态（Godot 像素坐标、朝向弧度、移动动画态与存活态）。</summary>
    void ApplyWorldState(Vector2 godotPosition, float facingRadians, bool moving, bool alive);
}
