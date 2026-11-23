#nullable enable
using SepCore.Runtime.Fsm;

namespace SepCore.Procedure;

/// <summary>
/// 流程基类:游戏由一组流程之间的流转构成(参考 GameFramework)。
/// 生命周期:全部流程 <see cref="OnInit"/>(进入首个流程前)→ 当前流程
/// <see cref="OnEnter"/>/每帧 <see cref="OnUpdate"/>/<see cref="OnExit"/> →
/// 关停时全部流程逆序 <see cref="OnDestroy"/>。
/// 流程应保持无状态,跨流程数据放 <see cref="ProcedureComponent.Context"/>。
/// 约定:切换目标流程只能在 <see cref="OnUpdate"/> 或外部事件中调用
/// fsm.ChangeState(OnEnter/OnExit 内禁止,状态机会抛异常)。
/// </summary>
public abstract class ProcedureBase : IFsmState<ProcedureComponent>
{
    /// <summary>全部流程注册完成后、进入首个流程前调用一次。</summary>
    public virtual void OnInit(ProcedureComponent flow) { }

    /// <summary>进入流程。</summary>
    public virtual void OnEnter(Fsm<ProcedureComponent> fsm) { }

    /// <summary>每帧驱动,由 <see cref="ProcedureComponent"/> 调用。delta 为秒。</summary>
    public virtual void OnUpdate(Fsm<ProcedureComponent> fsm, float delta) { }

    /// <summary>退出流程(含切走与关停)。</summary>
    public virtual void OnExit(Fsm<ProcedureComponent> fsm) { }

    /// <summary>流程机关停时调用一次(逆序)。</summary>
    public virtual void OnDestroy(ProcedureComponent flow) { }
}
