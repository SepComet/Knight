#nullable enable
using System;
using SepCore.Runtime.Fsm;
using SepCore.Runtime.UI;
using SepCore.UI;

namespace SepCore.Procedure;

/// <summary>
/// 大厅常驻流程:进入时装配控制器并打开主菜单，退出时清理界面。
/// 大厅内部的主菜单/房间选择/等待房间导航由 UiApplication 连接 MVC 控制器。
/// 业务切场景:写 Context.NextScene / NextProcedure → fsm.ChangeState&lt;ProcedureChangeScene&gt;。
/// </summary>
public sealed class ProcedureLobby : ProcedureBase
{
    public override void OnEnter(Fsm<ProcedureComponent> fsm)
    {
        var ui = UIComponent.Instance
            ?? throw new InvalidOperationException("UIComponent 未就绪,无法进入大厅流程");
        var app = UiApplication.Ensure(ui);
        app.QuitRequested += OnQuitRequested;
        app.EnterLobby();
        Godot.GD.Print("[Procedure] 进入大厅流程");
    }

    public override void OnExit(Fsm<ProcedureComponent> fsm)
    {
        var ui = UIComponent.Instance;
        var app = ui?.GetNodeOrNull<UiApplication>("UiApplication");
        if (app != null)
        {
            app.QuitRequested -= OnQuitRequested;
            app.LeaveLobby();
        }
        ui?.CloseAll();
    }

    private static void OnQuitRequested()
        => ProcedureComponent.Instance!.GetTree().Quit();
}
