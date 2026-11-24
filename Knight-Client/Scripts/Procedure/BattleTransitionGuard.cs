#nullable enable
using System;
using SepCore.Definition;
using SepCore.Runtime.Network;

namespace SepCore.Procedure;

/// <summary>
/// 切场景过渡期守卫（纯 C#，可脱离 Godot 单元测试）：
/// 在 <see cref="ProcedureLobby"/> 发起 <see cref="ProcedureChangeScene"/> 到
/// <see cref="ProcedureBattle.OnEnter"/> 之间的过渡帧内，缓存提前到达的
/// <see cref="RoomTransition"/>，并捕获过渡期内发生的 <c>ROOM_CLOSED</c> 或网络断线，
/// 防止进入空战斗场景后永久卡死。
/// </summary>
public sealed class BattleTransitionGuard : IDisposable
{
    private readonly INetworkMessageClient _network;
    private readonly ProcedureContext _context;
    private readonly IDisposable _transitionSub;
    private readonly IDisposable _roomStateSub;
    private bool _disposed;

    /// <summary>战斗流程激活期间为 true，此时事件由 BattleSessionClient 直接消费。</summary>
    public bool IsBattleActive { get; set; }

    public BattleTransitionGuard(INetworkMessageClient network, ProcedureContext context)
    {
        _network = network ?? throw new ArgumentNullException(nameof(network));
        _context = context ?? throw new ArgumentNullException(nameof(context));
        _transitionSub = _network.Subscribe<RoomTransition>(OnEarlyRoomTransition);
        _roomStateSub = _network.Subscribe<RoomStatePush>(OnEarlyRoomStatePush);
        _network.Disconnected += OnEarlyDisconnected;
    }

    /// <summary>
    /// 在进入战斗流程时校验连接状态与过渡期缓冲标志。
    /// 若会话已失效，清理全部战斗过渡缓存并返回 true 及原因；否则返回 false。
    /// </summary>
    public bool TryConsumeAbortReason(out string reason)
    {
        if (_network.State != NetworkState.Connected)
        {
            reason = !string.IsNullOrWhiteSpace(_context.PendingTransitionDisconnectReason)
                ? $"连接已断开：{_context.PendingTransitionDisconnectReason}"
                : "连接已断开";
            _context.ClearBattleTransitionState();
            return true;
        }

        if (_context.PendingRoomClosed)
        {
            reason = "房间已关闭";
            _context.ClearBattleTransitionState();
            return true;
        }

        if (!string.IsNullOrWhiteSpace(_context.PendingTransitionDisconnectReason))
        {
            reason = $"连接已断开：{_context.PendingTransitionDisconnectReason}";
            _context.ClearBattleTransitionState();
            return true;
        }

        _context.PendingRoomClosed = false;
        _context.PendingTransitionDisconnectReason = null;
        reason = string.Empty;
        return false;
    }

    private void OnEarlyRoomTransition(RoomTransition transition)
    {
        if (_disposed || IsBattleActive)
            return;
        _context.PendingRoomTransition = transition.Clone();
    }

    private void OnEarlyRoomStatePush(RoomStatePush push)
    {
        if (_disposed || IsBattleActive)
            return;
        if (_context.PendingGameStart != null && push.State == RoomSessionState.RoomClosed)
            _context.PendingRoomClosed = true;
    }

    private void OnEarlyDisconnected(string reason)
    {
        if (_disposed || IsBattleActive)
            return;
        if (_context.PendingGameStart != null)
            _context.PendingTransitionDisconnectReason = string.IsNullOrWhiteSpace(reason) ? "网络已断开" : reason;
    }

    public void Dispose()
    {
        if (_disposed)
            return;
        _disposed = true;
        _transitionSub.Dispose();
        _roomStateSub.Dispose();
        _network.Disconnected -= OnEarlyDisconnected;
    }
}
