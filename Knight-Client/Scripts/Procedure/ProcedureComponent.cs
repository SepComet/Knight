#nullable enable
using System;
using Godot;
using SepCore.Runtime.Component;
using SepCore.Runtime.Fsm;

namespace SepCore.Procedure;

/// <summary>
/// 全局流程组件(autoload 常驻):游戏流程 = 各子流程之间的流转,本组件持有一台
/// 流程状态机并逐帧驱动。场景切换不影响流程机(autoload 不随场景销毁)。
/// 用法:<see cref="Run"/> 注册全部流程(第一个为初始流程)并启动;
/// 流程内经 <see cref="Context"/> 传递跨流程数据、fsm.ChangeState 推进流转;
/// 任意处监听 <see cref="ProcedureChanged"/> 感知当前流程。
/// </summary>
public partial class ProcedureComponent : GlobalComponent
{
    public static ProcedureComponent? Instance { get; private set; }

    private Fsm<ProcedureComponent>? _fsm;
    private ProcedureBase[]? _procedures;

    /// <summary>流程上下文(黑板);每次 <see cref="Run"/> 重置。</summary>
    public ProcedureContext Context { get; } = new();

    /// <summary>当前流程的类型;未运行或已关停时为 null。</summary>
    public Type? CurrentProcedureType => _fsm?.CurrentStateType;

    /// <summary>当前流程已持续秒数;未运行时为 0。</summary>
    public float StateTime => _fsm?.StateTime ?? 0f;

    /// <summary>当前流程切换时触发(null 表示流程机已关停)。</summary>
    public event Action<Type?>? ProcedureChanged;

    public override void _Ready()
    {
        Instance = this;
    }

    public override void _ExitTree()
    {
        if (Instance == this)
            Instance = null;
    }

    /// <summary>
    /// 注册全部流程并启动(第一个为初始流程):逐个 OnInit → Start。
    /// 重复调用会先关停上一条(逆序 OnDestroy)。
    /// </summary>
    public void Run(params ProcedureBase[] procedures)
    {
        ArgumentNullException.ThrowIfNull(procedures);
        if (procedures.Length == 0)
            throw new ArgumentException("流程集合至少需要一个流程", nameof(procedures));

        Shutdown();
        _procedures = procedures;
        _fsm = Fsm<ProcedureComponent>.Create(this, procedures);
        foreach (var procedure in procedures)
            procedure.OnInit(this);
        _fsm.Start(procedures[0].GetType());
        ProcedureChanged?.Invoke(_fsm.CurrentStateType);
    }

    /// <summary>
    /// 关停流程机:退出当前流程,然后按注册逆序对全部流程调用 OnDestroy。
    /// </summary>
    public void Shutdown()
    {
        if (_fsm == null)
            return;

        _fsm.Shutdown();
        _fsm = null;
        if (_procedures != null)
        {
            for (var i = _procedures.Length - 1; i >= 0; i--)
                _procedures[i].OnDestroy(this);
            _procedures = null;
        }
        Context.Reset();
        ProcedureChanged?.Invoke(null);
    }

    public override void _Process(double delta)
    {
        if (_fsm == null)
            return;

        var prev = _fsm.CurrentStateType;
        _fsm.Update((float)delta);
        if (!ReferenceEquals(prev, _fsm.CurrentStateType))
            ProcedureChanged?.Invoke(_fsm.CurrentStateType);
    }
}
