namespace SepCore.Runtime.Fsm;

/// <summary>
/// 有限状态机的状态接口。状态类建议保持无状态,数据挂在 Owner 或 Fsm 上。
/// <para>约束:<see cref="OnEnter"/>/<see cref="OnExit"/> 内禁止调用
/// <see cref="Fsm{TOwner}.ChangeState"/> 与 <see cref="Fsm{TOwner}.Shutdown"/>(会抛异常);
/// 切换状态请在 <see cref="OnUpdate"/> 或状态机外部触发。</para>
/// </summary>
public interface IFsmState<TOwner> where TOwner : class
{
    /// <summary>进入状态时调用,此时 <see cref="Fsm{TOwner}.StateTime"/> 已归零。</summary>
    void OnEnter(Fsm<TOwner> fsm);

    /// <summary>每帧驱动,由 <see cref="Fsm{TOwner}.Update"/> 调用。delta 为秒。</summary>
    void OnUpdate(Fsm<TOwner> fsm, float delta);

    /// <summary>退出状态时调用(含 <see cref="Fsm{TOwner}.Shutdown"/> 触发的退出)。</summary>
    void OnExit(Fsm<TOwner> fsm);
}
