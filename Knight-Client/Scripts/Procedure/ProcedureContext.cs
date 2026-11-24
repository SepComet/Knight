#nullable enable
using System;
using System.Collections.Generic;
using SepCore.Definition;

namespace SepCore.Procedure;

/// <summary>
/// 流程上下文(黑板):跨流程传递的数据,挂在 <see cref="ProcedureComponent"/> 上。
/// 发起切场景的流程负责写入 <see cref="NextScene"/> 与 <see cref="NextProcedure"/>,
/// 由 ProcedureChangeScene 消费后清空。
/// </summary>
public sealed class ProcedureContext
{
    /// <summary>下一个要进入的场景;未指定时为 null。</summary>
    public SceneType? NextScene { get; set; }

    /// <summary>场景切换完成后进入的流程类型;未指定时为 null。</summary>
    public Type? NextProcedure { get; set; }

    /// <summary>开局通知缓存（GameStart → ProcedureBattle 跨场景交接）。</summary>
    public GameStart? PendingGameStart { get; set; }

    /// <summary>提前到达的房间切换通知缓存（防止切场景期间丢失首房 RoomTransition）。</summary>
    public RoomTransition? PendingRoomTransition { get; set; }

    /// <summary>切场景过渡帧期间收到 ROOM_CLOSED 标志（防止进入空战斗场景后假死）。</summary>
    public bool PendingRoomClosed { get; set; }

    /// <summary>切场景过渡帧期间发生断线的说明（非空表示过渡期已断线）。</summary>
    public string? PendingTransitionDisconnectReason { get; set; }

    /// <summary>当前房间成员 UID → 名称映射（供局内显示玩家名）。</summary>
    public Dictionary<ulong, string> RoomMemberNames { get; } = new();

    /// <summary>清空战斗切场景交接缓存（进入失败或退出战斗流程时调用）。</summary>
    public void ClearBattleTransitionState()
    {
        PendingGameStart = null;
        PendingRoomTransition = null;
        PendingRoomClosed = false;
        PendingTransitionDisconnectReason = null;
    }

    internal void Reset()
    {
        NextScene = null;
        NextProcedure = null;
        ClearBattleTransitionState();
        RoomMemberNames.Clear();
    }
}
