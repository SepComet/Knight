#nullable enable
using System;
using Godot;

namespace SepCore.Procedure;

/// <summary>
/// Splash 场景根:启动全局流程机并展示当前流程。
/// 流程机是 autoload 常驻的,首个场景切换(进入大厅)后本场景即被替换;
/// 启动阶段(Launch/Loading)结束后先自行隐藏,给场景切换留出干净的过场。
/// </summary>
public partial class SplashRoot : Control
{
    private Label? _status;

    public override void _Ready()
    {
        _status = GetNode<Label>("%Status");

        var flow = ProcedureComponent.Instance
            ?? throw new InvalidOperationException("ProcedureComponent 未注册为 autoload,无法启动流程");

        flow.ProcedureChanged += OnProcedureChanged;
        flow.Run(new ProcedureLaunch(), new ProcedureLoading(), new ProcedureChangeScene(), new ProcedureLobby());
    }

    public override void _ExitTree()
    {
        if (ProcedureComponent.Instance != null)
            ProcedureComponent.Instance.ProcedureChanged -= OnProcedureChanged;
    }

    private void OnProcedureChanged(Type? procedure)
    {
        // 启动阶段(Launch/Loading)结束即隐藏 splash
        if (procedure != typeof(ProcedureLaunch) && procedure != typeof(ProcedureLoading))
            Hide();

        if (_status != null)
            _status.Text = procedure == null ? "(空闲)" : procedure.Name;
    }
}
