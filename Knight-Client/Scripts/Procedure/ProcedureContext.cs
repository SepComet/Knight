#nullable enable
using System;
using SepCore.Definition;

namespace SepCore.Procedure;

/// <summary>
/// 流程上下文(黑板):跨流程传递的数据,挂在 <see cref="ProcedureComponent"/> 上。
/// 发起切场景的流程负责写入 <see cref="NextScene"/> 与 <see cref="NextProcedure"/>,
/// 由 ProcedureChangeScene 消费后清空。
/// </summary>
public sealed class ProcedureContext
{
    /// <summary>下一个要进入的场景;未指定时为 null。</summary>
    public SceneType? NextScene { get; set; }

    /// <summary>场景切换完成后进入的流程类型;未指定时为 null。</summary>
    public Type? NextProcedure { get; set; }

    internal void Reset()
    {
        NextScene = null;
        NextProcedure = null;
    }
}
