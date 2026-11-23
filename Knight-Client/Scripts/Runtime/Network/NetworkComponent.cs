#nullable enable
using Godot;
using Google.Protobuf;
using SepCore.Runtime.Component;
using SepCore.Runtime.Event;

namespace SepCore.Runtime.Network;

/// <summary>
/// 网络组件(全局组件):封装与服务端的 KCP + Protobuf 通信。
/// 真正的逻辑在 <see cref="NetworkClient"/>(纯 C#,可脱离 Godot 单测),本类只做:
/// - autoload 单例挂载与生命周期管理;
/// - 每帧 <see cref="_Process"/> 驱动 NetworkClient(kcp2k 泵/握手超时/心跳);
/// - 把 kcp2k 内部日志接到 Godot 控制台;
/// - 经 <see cref="NetworkEventBridge"/> 把连接事件转发到全局事件总线。
/// 收发与订阅全部转发给 <see cref="Client"/>,事件在主线程同步派发。
/// </summary>
public partial class NetworkComponent : GlobalComponent
{
    public static NetworkComponent? Instance { get; private set; }

    /// <summary>网络客户端核心。由宿主流程调用 Connect(host, port, token) 发起连接。</summary>
    public NetworkClient Client { get; } = new();

    public override void _Ready()
    {
        Instance = this;
        kcp2k.Log.Info = GD.Print;
        kcp2k.Log.Warning = GD.PushWarning;
        kcp2k.Log.Error = GD.PushError;
        // autoload 顺序保证 EventComponent 先于本组件就绪(见 project.godot [autoload])
        NetworkEventBridge.Attach(EventComponent.Instance!.Bus, Client);
    }

    public override void _ExitTree()
    {
        if (Instance == this)
            Instance = null;
        Client.Disconnect("进程退出");
    }

    public override void _Process(double delta)
    {
        Client.Update((float)delta);
    }
}
