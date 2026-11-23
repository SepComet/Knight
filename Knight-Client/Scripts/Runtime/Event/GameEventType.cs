#nullable enable

namespace SepCore.Runtime.Event;

/// <summary>
/// 全局事件类型。每个枚举值用 XML 注释声明对应的 payload 类型,构成订阅/发布双方的契约:
/// - 无 payload 的事件:Publish(type) 发布,Subscribe(type, Action) 订阅;
/// - 带 payload 的事件:Publish(type, payload) 发布,Subscribe(type, Action&lt;TPayload&gt;) 订阅。
/// payload 类型定义在使用方所属业务模块;枚举值与 payload 不匹配时在派发时抛异常。
/// </summary>
public enum GameEventType
{
    // ---- 网络(NetworkComponent 桥接 NetworkClient 的连接事件) ----

    /// <summary>连接建立(应用层握手完成)。payload:无。</summary>
    NetConnected,

    /// <summary>连接断开。payload:<see cref="SepCore.Runtime.Network.NetDisconnectedArgs"/>(断线原因,人类可读)。</summary>
    NetDisconnected,

    // ---- 战斗 ----

    /// <summary>角色死亡。payload:待定(死亡角色引用,战斗模块落地时补齐)。</summary>
    PlayerDied,

    /// <summary>房间切换。payload:待定(目标房间,房间系统落地时补齐)。</summary>
    RoomSwitched,
}
