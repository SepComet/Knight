#nullable enable
using System;
using SepCore.Definition;
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
    private Fsm<ProcedureComponent>? _fsm;

    public override void OnEnter(Fsm<ProcedureComponent> fsm)
    {
        _fsm = fsm;
        var ui = UIComponent.Instance
            ?? throw new InvalidOperationException("UIComponent 未就绪,无法进入大厅流程");
        var app = UiApplication.Ensure(ui);
        app.QuitRequested += OnQuitRequested;
        app.GameStarted += OnGameStarted;
        app.EnterLobby();
        Godot.GD.Print("[Procedure] 进入大厅流程");
    }

    public override void OnExit(Fsm<ProcedureComponent> fsm)
    {
        _fsm = null;
        var ui = UIComponent.Instance;
        var app = ui?.GetNodeOrNull<UiApplication>("UiApplication");
        if (app != null)
        {
            app.QuitRequested -= OnQuitRequested;
            app.GameStarted -= OnGameStarted;
            app.LeaveLobby();
        }
        ui?.CloseAll();
    }

    private void OnGameStarted(GameStart gameStart)
    {
        if (_fsm == null)
            return;

        var ctx = _fsm.Owner.Context;
        ctx.PendingRoomClosed = false;
        ctx.PendingTransitionDisconnectReason = null;
        ctx.PendingGameStart = gameStart.Clone();
        ctx.RoomMemberNames.Clear();

        var app = UIComponent.Instance?.GetNodeOrNull<UiApplication>("UiApplication");
        if (app != null)
        {
            foreach (var kv in app.GetRoomMemberNames())
                ctx.RoomMemberNames[kv.Key] = kv.Value;
        }

        ctx.NextScene = SceneType.Main;
        ctx.NextProcedure = typeof(ProcedureBattle);
        _fsm.ChangeState<ProcedureChangeScene>();
    }

    private static void OnQuitRequested()
        => ProcedureComponent.Instance!.GetTree().Quit();
}
