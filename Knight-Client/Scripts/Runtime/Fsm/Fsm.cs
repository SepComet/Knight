#nullable enable
using System;
using System.Collections.Generic;

namespace SepCore.Runtime.Fsm;

/// <summary>
/// 有限状态机,泛型绑定所有者。同一时刻只有一个激活状态;
/// 切换时先退出当前状态,再进入新状态,<see cref="StateTime"/> 归零。
/// 线程不安全,仅在主线程使用。
/// <para>生命周期:<see cref="Create"/> → <see cref="Start{TState}"/> →
/// 每帧 <see cref="Update"/> → <see cref="Shutdown"/>。Shutdown 后可重新 Start。</para>
/// <para>典型驱动方式:在 Owner 的 _PhysicsProcess 中调用 <see cref="Update"/>,
/// 或配合流程驱动器逐帧推进。</para>
/// </summary>
public sealed class Fsm<TOwner> where TOwner : class
{
    private readonly Dictionary<Type, IFsmState<TOwner>> _states;
    private readonly TOwner _owner;
    private IFsmState<TOwner>? _current;
    // OnEnter/OnExit 执行期间为 true,用于禁止期间的状态切换,避免重入
    private bool _transitioning;

    private Fsm(TOwner owner, IEnumerable<IFsmState<TOwner>> states)
    {
        _owner = owner;
        _states = new Dictionary<Type, IFsmState<TOwner>>();
        foreach (var state in states)
        {
            if (state == null)
                throw new ArgumentException("状态集合中包含 null", nameof(states));
            var type = state.GetType();
            if (!_states.TryAdd(type, state))
                throw new ArgumentException($"状态类型重复注册:{type.Name}", nameof(states));
        }
    }

    /// <summary>创建状态机。状态类型不可重复。</summary>
    public static Fsm<TOwner> Create(TOwner owner, params IFsmState<TOwner>[] states)
    {
        ArgumentNullException.ThrowIfNull(owner);
        ArgumentNullException.ThrowIfNull(states);
        return new Fsm<TOwner>(owner, states);
    }

    public TOwner Owner => _owner;

    /// <summary>当前状态;未启动或已 Shutdown 时为 null。</summary>
    public IFsmState<TOwner>? CurrentState => _current;

    /// <summary>当前状态类型;未启动或已 Shutdown 时为 null。</summary>
    public Type? CurrentStateType => _current?.GetType();

    /// <summary>当前状态已持续的时间(秒),切换状态或 Shutdown 时归零。</summary>
    public float StateTime { get; private set; }

    /// <summary>是否注册了指定状态。</summary>
    public bool HasState<TState>() where TState : class, IFsmState<TOwner>
        => _states.ContainsKey(typeof(TState));

    /// <summary>获取注册的状态实例,未注册则抛出异常。</summary>
    public TState GetState<TState>() where TState : class, IFsmState<TOwner>
    {
        if (_states.TryGetValue(typeof(TState), out var state))
            return (TState)state;
        throw new ArgumentException($"状态机未注册状态:{typeof(TState).Name}");
    }

    /// <summary>启动状态机并进入初始状态。已启动时再次调用会抛出异常。</summary>
    public void Start<TState>() where TState : class, IFsmState<TOwner>
        => Start(typeof(TState));

    /// <summary>按运行时类型启动状态机(初始状态编译期未知时使用)。已启动时再次调用会抛出异常。</summary>
    public void Start(Type stateType)
    {
        ArgumentNullException.ThrowIfNull(stateType);
        if (_current != null)
            throw new InvalidOperationException("状态机已启动,不能重复 Start");
        if (!_states.TryGetValue(stateType, out var state))
            throw new ArgumentException($"状态机未注册状态:{stateType.Name}", nameof(stateType));

        _current = state;
        StateTime = 0f;
        _transitioning = true;
        try
        {
            state.OnEnter(this);
        }
        finally
        {
            _transitioning = false;
        }
    }

    /// <summary>
    /// 切换到目标状态:先退出当前状态,再进入新状态。
    /// 允许切换到当前状态本身(相当于重置该状态)。
    /// 禁止在 OnEnter/OnExit 内调用。
    /// </summary>
    public void ChangeState<TState>() where TState : class, IFsmState<TOwner>
        => ChangeState(typeof(TState));

    /// <inheritdoc cref="ChangeState{TState}"/>
    public void ChangeState(Type stateType)
    {
        ArgumentNullException.ThrowIfNull(stateType);
        if (_transitioning)
            throw new InvalidOperationException("正在切换状态中,禁止在 OnEnter/OnExit 内调用 ChangeState/Shutdown");
        if (_current == null)
            throw new InvalidOperationException("状态机尚未 Start,不能切换状态");
        if (!_states.TryGetValue(stateType, out var next))
            throw new ArgumentException($"状态机未注册状态:{stateType.Name}", nameof(stateType));

        var prev = _current;
        _transitioning = true;
        try
        {
            prev.OnExit(this);
            _current = next;
            StateTime = 0f;
            next.OnEnter(this);
        }
        finally
        {
            _transitioning = false;
        }
    }

    /// <summary>每帧驱动当前状态;未启动时为空操作。</summary>
    public void Update(float delta)
    {
        if (_current == null)
            return;
        StateTime += delta;
        _current.OnUpdate(this, delta);
    }

    /// <summary>关闭状态机:退出当前状态。禁止在 OnEnter/OnExit 内调用。之后可重新 Start。</summary>
    public void Shutdown()
    {
        if (_transitioning)
            throw new InvalidOperationException("正在切换状态中,禁止在 OnEnter/OnExit 内调用 ChangeState/Shutdown");
        if (_current == null)
            return;

        var prev = _current;
        _current = null;
        StateTime = 0f;
        prev.OnExit(this);
    }
}
