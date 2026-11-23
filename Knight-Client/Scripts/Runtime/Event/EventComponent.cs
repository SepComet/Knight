#nullable enable
using SepCore.Runtime.Component;

namespace SepCore.Runtime.Event;

/// <summary>
/// 事件组件(全局组件):全局事件总线 <see cref="EventBus"/> 的 autoload 挂载点。
/// 总线随进程常驻,订阅者自行管理退订;派发为同步调用,本组件无每帧驱动。
/// autoload 顺序在 NetworkComponent 之前,后续网络连接事件的桥接依赖其先就绪。
/// </summary>
public partial class EventComponent : GlobalComponent
{
    public static EventComponent? Instance { get; private set; }

    /// <summary>全局事件总线。订阅、退订、发布全部经由 <see cref="Bus"/> 操作。</summary>
    public EventBus Bus { get; } = new();

    public override void _Ready()
    {
        Instance = this;
    }

    public override void _ExitTree()
    {
        if (Instance == this)
            Instance = null;
    }
}
