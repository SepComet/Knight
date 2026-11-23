#nullable enable
using System;
using System.Collections.Generic;

namespace SepCore.Runtime.Event;

/// <summary>
/// 事件总线(纯 C#,不依赖 Godot,可单元测试):
/// 以 <see cref="GameEventType"/> 为键的订阅/退订/同步派发全局事件管线。
/// <para>语义约定:</para>
/// <para>- 回调原样存储、派发时才按目标签名转型:保证可退订;
/// 枚举值与 payload 类型不匹配时派发抛异常(契约见 GameEventType 注释),未触发的回调被跳过;</para>
/// <para>- 派发前对回调列表做快照:派发中退订(含自己)不会破坏本次派发,被退订者仍会收到本次事件;
/// 派发中新增的订阅从下一次事件开始生效;</para>
/// <para>- 订阅顺序即派发顺序,不做优先级;重复订阅同一回调会收到多次,退订一次移除全部重复项;</para>
/// <para>- 退订不存在的回调为幂等空操作;无人订阅的 Publish 为空操作。</para>
/// 线程不安全,仅在主线程使用。
/// </summary>
public sealed class EventBus
{
    private readonly Dictionary<GameEventType, List<Delegate>> _handlers = new();

    // ---------------------------------------------------------------- 订阅

    /// <summary>
    /// 订阅带 payload 的事件。payload 类型由回调的参数类型决定:
    /// lambda 需显式标注参数类型(隐式参数 lambda 与方法组均无法推断 TArg);
    /// 方法组订阅请显式指定泛型实参,如 Subscribe&lt;TPayload&gt;(type, Handler)。重复订阅会收到多次。
    /// </summary>
    public void Subscribe<TArg>(GameEventType type, Action<TArg> handler)
        => Add(type, handler);

    /// <summary>订阅无 payload 的事件(信号类)。重复订阅会收到多次。</summary>
    public void Subscribe(GameEventType type, Action handler)
        => Add(type, handler);

    // ---------------------------------------------------------------- 退订

    /// <summary>
    /// 退订带 payload 的事件;TArg 需与订阅时一致(方法组请显式指定泛型实参)。
    /// 移除该回调的全部重复项;回调不存在时为幂等空操作。
    /// </summary>
    public void Unsubscribe<TArg>(GameEventType type, Action<TArg> handler)
        => RemoveAll(type, handler);

    /// <summary>退订无 payload 的事件;移除该回调的全部重复项。回调不存在时为幂等空操作。</summary>
    public void Unsubscribe(GameEventType type, Action handler)
        => RemoveAll(type, handler);

    // ---------------------------------------------------------------- 发布

    /// <summary>发布带 payload 的事件,同步逐个调用订阅者(订阅顺序)。无人订阅时为空操作。</summary>
    public void Publish<TArg>(GameEventType type, TArg arg)
    {
        var handlers = Snapshot(type);
        if (handlers == null)
            return;

        foreach (var handler in handlers)
        {
            if (handler is Action<TArg> typed)
                typed(arg);
            else
                throw PayloadMismatch(type, handler, $"Action<{typeof(TArg).Name}>");
        }
    }

    /// <summary>发布无 payload 的事件(信号类),同步逐个调用订阅者(订阅顺序)。无人订阅时为空操作。</summary>
    public void Publish(GameEventType type)
    {
        var handlers = Snapshot(type);
        if (handlers == null)
            return;

        foreach (var handler in handlers)
        {
            if (handler is Action typed)
                typed();
            else
                throw PayloadMismatch(type, handler, "Action");
        }
    }

    // ---------------------------------------------------------------- 内部实现

    private void Add(GameEventType type, Delegate handler)
    {
        ArgumentNullException.ThrowIfNull(handler);
        if (!_handlers.TryGetValue(type, out var list))
            _handlers[type] = list = new List<Delegate>();
        list.Add(handler);
    }

    private void RemoveAll(GameEventType type, Delegate handler)
    {
        ArgumentNullException.ThrowIfNull(handler);
        if (!_handlers.TryGetValue(type, out var list))
            return;

        list.RemoveAll(candidate => candidate.Equals(handler));
        if (list.Count == 0)
            _handlers.Remove(type); // 及时清理空列表,避免长期运行下字典膨胀
    }

    /// <summary>派发前快照回调列表,保证派发中退订/订阅不影响本次枚举;无订阅者时返回 null。</summary>
    private Delegate[]? Snapshot(GameEventType type)
        => _handlers.TryGetValue(type, out var list) && list.Count > 0 ? list.ToArray() : null;

    private static InvalidOperationException PayloadMismatch(GameEventType type, Delegate handler, string expected)
        => new($"事件 {type} 派发时回调签名不匹配:期望 {expected},实际为 {handler.GetType().Name}" +
               "(枚举值与 payload 的契约见 GameEventType 注释)");
}
