#nullable enable
using SepCore.Runtime.Fsm;

namespace SepCore.Procedure;

/// <summary>
/// 加载流程:游戏启动时的资源准备与初始化(一次性流程,方向无关的日常切场景不经此流程)。
/// MVP 暂无预热内容,直接转场景切换;后续 shader 预编译、资源预载在此扩展。
/// </summary>
public sealed class ProcedureLoading : ProcedureBase
{
    public override void OnUpdate(Fsm<ProcedureComponent> fsm, float delta)
        => fsm.ChangeState<ProcedureChangeScene>();
}
