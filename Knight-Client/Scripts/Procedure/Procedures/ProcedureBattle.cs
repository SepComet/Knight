#nullable enable
using System;
using Godot;
using SepCore.Battle;
using SepCore.Definition;
using SepCore.Runtime.Fsm;
using SepCore.Runtime.Network;
using SepCore.Runtime.UI;
using SepCore.UI;
using SepCore.UI.Battle;

namespace SepCore.Procedure;

/// <summary>
/// 联机战斗流程：
/// - OnInit 阶段即订阅 RoomTransition，防止 GameStart → ProcedureChangeScene 切场景期间丢失首房通知；
/// - OnEnter 装配 BattleSessionClient、BattleWorld、GameHudView（及移动端 TouchControlsView）；
/// - 房间关闭或断线时经 ProcedureChangeScene 返回 ProcedureLobby。
/// </summary>
public sealed class ProcedureBattle : ProcedureBase
{
    private Fsm<ProcedureComponent>? _fsm;
    private BattleTransitionGuard? _transitionGuard;
    private BattleSessionClient? _session;
    private BattleWorld? _world;
    private bool _active;
    private bool _entering;
    private string? _pendingExitReason;

    public override void OnInit(ProcedureComponent owner)
    {
        if (NetworkComponent.Instance != null)
            _transitionGuard = new BattleTransitionGuard(NetworkComponent.Instance.Client, owner.Context);
    }

    public override void OnDestroy(ProcedureComponent owner)
    {
        _transitionGuard?.Dispose();
        _transitionGuard = null;
    }

    public override void OnEnter(Fsm<ProcedureComponent> fsm)
    {
        _fsm = fsm;
        _pendingExitReason = null;

        var network = NetworkComponent.Instance
            ?? throw new InvalidOperationException("NetworkComponent 未就绪，无法进入战斗流程");

        var ctx = fsm.Owner.Context;
        if (_transitionGuard != null && _transitionGuard.TryConsumeAbortReason(out var abortReason))
        {
            GD.Print($"[Procedure] 战斗切场景期间会话已失效，返回大厅：{abortReason}");
            _pendingExitReason = abortReason;
            return;
        }
        if (_transitionGuard == null && network.Client.State != NetworkState.Connected)
        {
            ctx.ClearBattleTransitionState();
            GD.Print("[Procedure] 进入战斗时连接已断开，返回大厅");
            _pendingExitReason = "连接已断开";
            return;
        }

        _entering = true;
        _active = true;
        if (_transitionGuard != null)
            _transitionGuard.IsBattleActive = true;

        try
        {
            var ui = UIComponent.Instance
                ?? throw new InvalidOperationException("UIComponent 未就绪，无法进入战斗流程");
            var world = fsm.Owner.GetTree().CurrentScene as BattleWorld
                ?? throw new InvalidOperationException("当前场景不是 BattleWorld（res://Scenes/Main.tscn）");

            _world = world;
            var app = UiApplication.Ensure(ui);
            ui.CloseAll();
            ui.Open(UIFormType.RoundHUDForm, GameHudModel.Empty);

            TouchControlsView? touchView = null;
            if (OS.HasFeature("mobile"))
            {
                ui.Open(UIFormType.JoystickForm);
                touchView = ui.FindView(UIFormType.JoystickForm) as TouchControlsView;
            }

            _session = new BattleSessionClient(network.Client);
            _session.SessionEnded += OnSessionEnded;

            var pendingStart = ctx.PendingGameStart;
            var pendingTransition = ctx.PendingRoomTransition;
            ctx.ClearBattleTransitionState();

            world.BindSession(_session, ctx.RoomMemberNames, app.Hud, touchView);
            _session.Start(pendingStart, pendingTransition);

            GD.Print("[Procedure] 进入战斗流程");
        }
        finally
        {
            _entering = false;
        }
    }

    public override void OnUpdate(Fsm<ProcedureComponent> fsm, float delta)
    {
        if (_pendingExitReason != null)
        {
            var reason = _pendingExitReason;
            _pendingExitReason = null;
            ReturnToLobby(fsm, reason);
        }
    }

    public override void OnExit(Fsm<ProcedureComponent> fsm)
    {
        _active = false;
        _entering = false;
        _pendingExitReason = null;
        _fsm = null;
        if (_transitionGuard != null)
            _transitionGuard.IsBattleActive = false;

        if (_world != null)
        {
            _world.UnbindSession();
            _world = null;
        }

        if (_session != null)
        {
            _session.SessionEnded -= OnSessionEnded;
            _session.Dispose();
            _session = null;
        }

        fsm.Owner.Context.ClearBattleTransitionState();
        UIComponent.Instance?.CloseAll();
    }

    private void OnSessionEnded(string reason)
    {
        if (!_active || _fsm == null)
            return;

        if (_entering)
        {
            _pendingExitReason = reason;
            return;
        }

        ReturnToLobby(_fsm, reason);
    }

    private static void ReturnToLobby(Fsm<ProcedureComponent> fsm, string reason)
    {
        GD.Print($"[Procedure] 战斗结束，返回大厅：{reason}");
        var ctx = fsm.Owner.Context;
        ctx.ClearBattleTransitionState();
        ctx.NextScene = SceneType.Menu;
        ctx.NextProcedure = typeof(ProcedureLobby);
        fsm.ChangeState<ProcedureChangeScene>();
    }
}
