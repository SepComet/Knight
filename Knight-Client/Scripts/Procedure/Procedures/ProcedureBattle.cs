#nullable enable
using System;
using System.Collections.Generic;
using Godot;
using SepCore.Battle;
using SepCore.Definition;
using SepCore.Runtime.Fsm;
using SepCore.Runtime.Network;
using SepCore.Runtime.UI;
using SepCore.UI;
using SepCore.UI.Battle;
using SepCore.UI.Results;
using UiPlayerResult = SepCore.UI.Results.PlayerResult;

namespace SepCore.Procedure;

/// <summary>
/// 联机战斗流程：
/// - OnInit 阶段即订阅 RoomTransition，防止 GameStart → ProcedureChangeScene 切场景期间丢失首房通知；
/// - OnEnter 装配 BattleSessionClient、BattleWorld、GameHudView（及移动端 TouchControlsView）；
/// - 收到 GameResult (72) 时弹出 GameResultView 展示胜负/耗时/全员战绩，由玩家点击返回大厅/房间；
/// - 房间关闭或断线时经 ProcedureChangeScene 返回 ProcedureLobby。
/// </summary>
public sealed class ProcedureBattle : ProcedureBase
{
    private Fsm<ProcedureComponent>? _fsm;
    private BattleTransitionGuard? _transitionGuard;
    private BattleSessionClient? _session;
    private BattleWorld? _world;
    private UiApplication? _uiApp;
    private IReadOnlyDictionary<ulong, string>? _memberNames;
    private bool _active;
    private bool _entering;
    private bool _resultShown;
    private string? _pendingExitReason;

    public bool IsResultShown => _resultShown;

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
        _resultShown = false;

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
            _uiApp = app;
            _memberNames = new Dictionary<ulong, string>(ctx.RoomMemberNames);

            ui.CloseAll();
            ui.Open(UIFormType.RoundHUDForm, GameHudModel.Empty);

            TouchControlsView? touchView = null;
            if (OS.HasFeature("mobile"))
            {
                ui.Open(UIFormType.JoystickForm);
                touchView = ui.FindView(UIFormType.JoystickForm) as TouchControlsView;
            }

            _session = new BattleSessionClient(network.Client);
            _session.GameResultReceived += OnGameResultReceived;
            _session.SessionEnded += OnSessionEnded;

            app.Results.ReturnMenuRequested += OnReturnMenuFromResult;
            app.Results.ReturnRoomRequested += OnReturnRoomFromResult;

            var pendingStart = ctx.PendingGameStart;
            var pendingTransition = ctx.PendingRoomTransition;
            ctx.ClearBattleTransitionState();

            world.BindSession(_session, _memberNames, app.Hud, touchView);
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
        _resultShown = false;
        _pendingExitReason = null;
        _fsm = null;
        if (_transitionGuard != null)
            _transitionGuard.IsBattleActive = false;

        if (_uiApp != null)
        {
            _uiApp.Results.ReturnMenuRequested -= OnReturnMenuFromResult;
            _uiApp.Results.ReturnRoomRequested -= OnReturnRoomFromResult;
            _uiApp = null;
        }

        if (_world != null)
        {
            _world.UnbindSession();
            _world = null;
        }

        if (_session != null)
        {
            _session.GameResultReceived -= OnGameResultReceived;
            _session.SessionEnded -= OnSessionEnded;
            _session.Dispose();
            _session = null;
        }

        _memberNames = null;
        fsm.Owner.Context.ClearBattleTransitionState();
        UIComponent.Instance?.CloseAll();
    }

    private void OnGameResultReceived(GameResult result)
    {
        if (!_active || _uiApp == null)
            return;

        _resultShown = true;
        _world?.SetInputEnabled(false);
        var isVictory = result.Outcome == GameOutcome.OutcomeVictory;
        _world?.PlayResultSfx(isVictory);

        var rows = new List<UiPlayerResult>(result.Results.Count);
        foreach (var pr in result.Results)
        {
            var name = _memberNames != null && _memberNames.TryGetValue(pr.Uid, out var resolved) && !string.IsNullOrWhiteSpace(resolved)
                ? resolved
                : $"玩家 #{pr.Uid % 10000}";
            rows.Add(new UiPlayerResult(
                Id: pr.Uid.ToString(),
                Name: name,
                Weapons: "骑士步枪",
                TotalDamage: pr.DamageDealt,
                Kills: pr.Kills,
                DamageTaken: pr.DamageTaken,
                Alive: pr.Alive));
        }

        _uiApp.Results.SetMatchResult(result.MatchId, isVictory, result.DurationSec, rows);
        UIComponent.Instance?.Open(UIFormType.RoundSettlementForm);
        GD.Print($"[Procedure] 收到结算 GameResult (match_id={result.MatchId}, outcome={result.Outcome}, duration={result.DurationSec}s)");
    }

    private void OnReturnMenuFromResult()
    {
        if (!_active || _fsm == null)
            return;
        _uiApp?.LeaveRoomSession();
        ReturnToLobby(_fsm, "结算页返回大厅");
    }

    private void OnReturnRoomFromResult()
    {
        if (!_active || _fsm == null)
            return;
        _uiApp?.LeaveRoomSession();
        ReturnToLobby(_fsm, "结算页返回房间");
    }

    private void OnSessionEnded(string reason)
    {
        if (!_active || _fsm == null)
            return;

        if (_resultShown)
        {
            GD.Print($"[Procedure] 结算页展示中，忽略被动结束事件：{reason}");
            return;
        }

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
