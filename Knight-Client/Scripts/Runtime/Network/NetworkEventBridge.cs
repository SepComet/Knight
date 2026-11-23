#nullable enable
using System;
using SepCore.Runtime.Event;

namespace SepCore.Runtime.Network;

/// <summary>连接生命周期事件源:由 <see cref="NetworkClient"/> 实现,便于桥接器单测时打桩。</summary>
public interface INetworkConnectionEvents
{
    /// <summary>应用层握手完成(可开始收发业务消息)。</summary>
    event Action? Connected;

    /// <summary>连接断开,参数为人类可读原因。</summary>
    event Action<string>? Disconnected;
}

/// <summary>
/// 网络事件桥接器:把 <see cref="NetworkClient"/> 的连接生命周期事件转发为全局事件总线的
/// <see cref="GameEventType.NetConnected"/> / <see cref="GameEventType.NetDisconnected"/>,
/// 业务侧(UI 等)统一从总线感知连接状态,不直接依赖 NetworkClient。
/// 由 <see cref="NetworkComponent"/> 在 _Ready 中装配一次;双方均随进程常驻,不提供退订。
/// </summary>
public static class NetworkEventBridge
{
    /// <summary>装配桥接,仅应在进程启动时调用一次。重复调用会导致事件重复发布。</summary>
    public static void Attach(EventBus bus, INetworkConnectionEvents connection)
    {
        ArgumentNullException.ThrowIfNull(bus);
        ArgumentNullException.ThrowIfNull(connection);

        connection.Connected += () => bus.Publish(GameEventType.NetConnected);
        connection.Disconnected += reason => bus.Publish(GameEventType.NetDisconnected,
            new NetDisconnectedArgs { Reason = reason });
    }
}

/// <summary><see cref="GameEventType.NetDisconnected"/> 的 payload:连接断开原因。</summary>
public sealed class NetDisconnectedArgs
{
    /// <summary>人类可读的断线原因(来自 NetworkClient.Disconnected)。</summary>
    public string Reason { get; init; } = "";
}
