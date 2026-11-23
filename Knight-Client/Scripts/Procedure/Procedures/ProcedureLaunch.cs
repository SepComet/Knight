#nullable enable
using System;
using SepCore.Definition;
using SepCore.Runtime.Fsm;
using SepCore.Runtime.Luban;

namespace SepCore.Procedure;

/// <summary>
/// 启动流程:引擎级组件均以 autoload 就绪,此处完成数据装配。
/// 完成后指定首次目标(大厅)并转入加载流程。
/// </summary>
public sealed class ProcedureLaunch : ProcedureBase
{
    public override void OnEnter(Fsm<ProcedureComponent> fsm)
    {
        var luban = LubanComponent.Instance
            ?? throw new InvalidOperationException("LubanComponent 未就绪,无法启动流程");
        if (!luban.IsLoaded)
            luban.LoadTables(); // 失败直接抛:数据装配属严重问题,不允许静默
    }

    public override void OnUpdate(Fsm<ProcedureComponent> fsm, float delta)
    {
        // 首次目标固定为大厅;谁切到 ProcedureChangeScene 谁负责指定目标场景与目标流程
        var flow = fsm.Owner;
        flow.Context.NextScene = SceneType.Menu;
        flow.Context.NextProcedure = typeof(ProcedureLobby);
        fsm.ChangeState<ProcedureLoading>();
    }
}
