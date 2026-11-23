#nullable enable
using System;
using Godot;
using SepCore.Definition;
using SepCore.Runtime.Fsm;
using SepCore.Runtime.Luban;

namespace SepCore.Procedure;

/// <summary>
/// 切换场景流程:按 <see cref="ProcedureContext.NextScene"/> 查场景配置并切换,
/// 完成后进入 <see cref="ProcedureContext.NextProcedure"/> 指定的流程并清空上下文。
/// 两项均由发起切场景的流程写入,未指定即抛异常(属严重问题)。
/// </summary>
public sealed class ProcedureChangeScene : ProcedureBase
{
    public override void OnEnter(Fsm<ProcedureComponent> fsm)
    {
        var ctx = fsm.Owner.Context;
        var nextScene = ctx.NextScene
            ?? throw new InvalidOperationException("切场景前未指定 Context.NextScene(发起方职责)");
        var nextProcedure = ctx.NextProcedure
            ?? throw new InvalidOperationException("切场景前未指定 Context.NextProcedure(发起方职责)");

        var config = LubanComponent.Instance?.Tables?.TbSceneConfig.GetOrDefault(nextScene)
            ?? throw new InvalidOperationException($"场景配置缺失:{nextScene}");

        var path = $"res://Scenes/{config.AssetName}.tscn";
        var err = fsm.Owner.GetTree().ChangeSceneToFile(path);
        if (err != Godot.Error.Ok)
            throw new InvalidOperationException($"场景切换失败:{path}({err})");
        GD.Print($"[Procedure] 切换场景:{config.AssetName}");
    }

    public override void OnUpdate(Fsm<ProcedureComponent> fsm, float delta)
    {
        var ctx = fsm.Owner.Context;
        var nextProcedure = ctx.NextProcedure!;
        ctx.NextScene = null;
        ctx.NextProcedure = null;
        fsm.ChangeState(nextProcedure);
    }
}
