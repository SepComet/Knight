#nullable enable
using System;
using System.Collections.Generic;
using Godot;
using SepCore.Definition;
using SepCore.Gameplay.Player;
using SepCore.UI.Battle;

namespace SepCore.Battle;

/// <summary>
/// 战斗房间地图加载状态机（纯 C#，可脱离 Godot 单元测试）：
/// 跳过相同 mapId 的重复重建，并在切换地图时按顺序清理旧墙体与重建新墙体。
/// </summary>
public sealed class BattleMapLoader
{
    private readonly List<Rect2> _wallRects = new();

    public uint CurrentMapId { get; private set; }
    public BattleMapDefinition CurrentMap { get; private set; } = BattleMapCatalog.Map1;
    public IReadOnlyList<Rect2> WallRects => _wallRects;

    public bool TryLoad(uint mapId, Action clearWallNodes, Action<Rect2> addWallNode)
    {
        ArgumentNullException.ThrowIfNull(clearWallNodes);
        ArgumentNullException.ThrowIfNull(addWallNode);

        if (mapId == 0 || CurrentMapId == mapId)
            return false;

        CurrentMapId = mapId;
        CurrentMap = BattleMapCatalog.Get(mapId);
        _wallRects.Clear();
        clearWallNodes();

        foreach (var wall in CurrentMap.Walls)
        {
            var rect = CurrentMap.WallToGodotRect(wall);
            _wallRects.Add(rect);
            addWallNode(rect);
        }

        return true;
    }
}

/// <summary>
/// 联机战斗房间场景根（挂载于 res://Scenes/Main.tscn）。
/// 复用 Player.tscn / PlayerMotor / LocalPlayerInput / PlayerCamera / GameHudController，
/// 消费 BattleSessionClient 的房间切换、快照、伤害/技能/道具/出口事件并上报 30Hz 玩家输入，
/// 同时驱动本地玩家客户端预测校正（LocalPlayerPredictor）与远端实体快照插值（RemoteEntityInterpolator）。
/// </summary>
public partial class BattleWorld : Node2D
{
    private const string PlayerPrefabPath = "res://Prefabs/Entity/Player.tscn";
    private const float NetDebugRefreshIntervalSeconds = 0.1f;
    private const float PickupInteractRadiusTiles = 2.0f;

    private Node2D _wallsRoot = null!;
    private Node2D _entitiesRoot = null!;
    private PlayerCamera _camera = null!;
    private PackedScene _playerScene = null!;
    private BattleVfxAndAudio _vfx = null!;

    private BattleSessionClient? _session;
    private GameHudController? _hud;
    private TouchControlsView? _touch;
    private IReadOnlyDictionary<ulong, string>? _memberNames;

    private readonly BattleMapLoader _mapLoader = new();
    private BattleMapDefinition CurrentMap => _mapLoader.CurrentMap;
    private readonly Dictionary<uint, IBattleEntityView> _entityViews = new();
    private readonly LocalPlayerPredictor _localPredictor = new();
    private readonly Dictionary<uint, RemoteEntityInterpolator> _remoteInterpolators = new();
    private readonly SyncMetricsTracker _syncMetrics = new();

    private uint _localPlayerEntityId;
    private uint _activeBossEntityId;
    private LocalPlayerInput? _localInput;
    private bool _interpolationEnabled = true;
    private double _syncClockSeconds;
    private float _netDebugRefreshAccumulator;
    private float _doorPulseTime;

    public BattleSessionClient? ActiveSession => _session;
    public uint LocalPlayerEntityId => _localPlayerEntityId;
    public LocalPlayerInput? LocalInput => _localInput;
    public BattleMapDefinition ActiveMap => CurrentMap;
    public IReadOnlyDictionary<uint, IBattleEntityView> EntityViews => _entityViews;
    public bool DoorOpen => _session?.DoorOpen ?? false;

    public bool PredictionEnabled
    {
        get => _localPredictor.Enabled;
        set => SetPredictionEnabled(value);
    }

    public bool InterpolationEnabled
    {
        get => _interpolationEnabled;
        set => SetInterpolationEnabled(value);
    }

    public bool HudDebugExpanded => _hud?.NetDebugModel.Expanded ?? false;

    public override void _Ready()
    {
        RenderingServer.SetDefaultClearColor(new Color("101b25"));
        _wallsRoot = GetNode<Node2D>("%WallsRoot");
        _entitiesRoot = GetNode<Node2D>("%EntitiesRoot");
        _camera = GetNode<PlayerCamera>("%PlayerCamera");
        _playerScene = GD.Load<PackedScene>(PlayerPrefabPath);
        _vfx = new BattleVfxAndAudio { Name = "BattleVfxAndAudio" };
        AddChild(_vfx);
    }

    private const double MaxSyncDeltaSeconds = 0.05;

    public override void _Process(double delta)
    {
        var clampedDelta = double.IsFinite(delta) && delta > 0.0
            ? Math.Min(delta, MaxSyncDeltaSeconds)
            : 0.0;
        if (clampedDelta > 0.0)
            _syncClockSeconds += clampedDelta;

        if (DoorOpen)
        {
            _doorPulseTime += (float)clampedDelta * 3.5f;
            QueueRedraw();
        }

        if (_localPlayerEntityId != 0
            && _localPredictor.HasState
            && _entityViews.TryGetValue(_localPlayerEntityId, out var localView)
            && IsInstanceValid(localView.Node))
        {
            _localPredictor.DecayVisualError((float)clampedDelta);
            ApplyWorldStateToView(
                localView,
                _localPredictor.RenderPosition,
                _localPredictor.FacingDegrees,
                _localPredictor.IsMoving,
                _localPredictor.Alive);

            UpdateLocalInteractionHint(_localPredictor.RenderPosition);
        }

        if (_interpolationEnabled && _remoteInterpolators.Count > 0)
        {
            foreach (var (entityId, interpolator) in _remoteInterpolators)
            {
                if (_entityViews.TryGetValue(entityId, out var remoteView)
                    && IsInstanceValid(remoteView.Node)
                    && interpolator.TrySample(_syncClockSeconds, out var sampled))
                {
                    ApplyWorldStateToView(
                        remoteView,
                        sampled.Position,
                        sampled.FacingDegrees,
                        sampled.Moving,
                        sampled.Alive);
                }
            }
        }

        if (_hud != null)
        {
            _netDebugRefreshAccumulator += (float)delta;
            if (_netDebugRefreshAccumulator >= NetDebugRefreshIntervalSeconds)
            {
                _netDebugRefreshAccumulator = 0f;
                PushNetDebugToHud();
            }
        }
    }

    public void BindSession(
        BattleSessionClient session,
        IReadOnlyDictionary<ulong, string> memberNames,
        GameHudController? hud,
        TouchControlsView? touch)
    {
        UnbindSession();
        _session = session;
        _memberNames = memberNames;
        _hud = hud;
        _touch = touch;

        _session.RoomTransitionReceived += OnRoomTransition;
        _session.EntitySpawned += OnEntitySpawned;
        _session.EntityUpdated += OnEntityUpdated;
        _session.EntityDespawned += OnEntityDespawned;
        _session.DamageEventReceived += OnDamageEvent;
        _session.DoorStateChanged += OnDoorStateChanged;
        _session.ItemPickedReceived += OnItemPicked;
        _session.SkillEventReceived += OnSkillEvent;
        _session.PlayerLeftReceived += OnPlayerLeft;

        if (_hud != null)
        {
            _hud.PredictionToggled += OnHudPredictionToggled;
            _hud.InterpolationToggled += OnHudInterpolationToggled;
            if (OS.HasFeature("mobile"))
                _hud.SetNetDebugExpanded(false);
            PushNetDebugToHud();
        }
    }

    public void UnbindSession()
    {
        if (_session != null)
        {
            _session.RoomTransitionReceived -= OnRoomTransition;
            _session.EntitySpawned -= OnEntitySpawned;
            _session.EntityUpdated -= OnEntityUpdated;
            _session.EntityDespawned -= OnEntityDespawned;
            _session.DamageEventReceived -= OnDamageEvent;
            _session.DoorStateChanged -= OnDoorStateChanged;
            _session.ItemPickedReceived -= OnItemPicked;
            _session.SkillEventReceived -= OnSkillEvent;
            _session.PlayerLeftReceived -= OnPlayerLeft;
            _session = null;
        }

        if (_hud != null)
        {
            _hud.PredictionToggled -= OnHudPredictionToggled;
            _hud.InterpolationToggled -= OnHudInterpolationToggled;
        }

        ClearEntities();
        _syncMetrics.Reset();
        _hud = null;
        _touch = null;
        _memberNames = null;
    }

    public void SetInputEnabled(bool enabled)
    {
        _localInput?.SetEnabled(enabled);
    }

    public void PlayResultSfx(bool victory)
    {
        _vfx?.PlayResultSfx(victory);
    }

    public void SetPredictionEnabled(bool enabled)
    {
        _localPredictor.Enabled = enabled;
        if (enabled && _localPredictor.HasState && _session != null)
            _localPredictor.RebuildFromUnacked(_session.PendingInputs, CurrentMap);

        if (_localPlayerEntityId != 0
            && _localPredictor.HasState
            && _entityViews.TryGetValue(_localPlayerEntityId, out var localView)
            && IsInstanceValid(localView.Node))
        {
            ApplyWorldStateToView(
                localView,
                _localPredictor.RenderPosition,
                _localPredictor.FacingDegrees,
                _localPredictor.IsMoving,
                _localPredictor.Alive);
        }

        PushNetDebugToHud();
    }

    /// <summary>
    /// 仅用于调试/E2E 验证：偏移本地预测位置（不改变服务端权威位置），
    /// 以便真实联机链路触发回滚重放与平滑收敛。
    /// </summary>
    public bool InjectPredictionOffset(float dx, float dy)
    {
        if (!_localPredictor.InjectPredictionOffset(dx, dy))
            return false;

        if (_localPlayerEntityId != 0
            && _entityViews.TryGetValue(_localPlayerEntityId, out var localView)
            && IsInstanceValid(localView.Node))
        {
            ApplyWorldStateToView(
                localView,
                _localPredictor.RenderPosition,
                _localPredictor.FacingDegrees,
                _localPredictor.IsMoving,
                _localPredictor.Alive);
        }

        return true;
    }

    public void SetInterpolationEnabled(bool enabled)
    {
        _interpolationEnabled = enabled;
        foreach (var (entityId, interpolator) in _remoteInterpolators)
        {
            interpolator.Enabled = enabled;
            if (!enabled
                && _entityViews.TryGetValue(entityId, out var remoteView)
                && IsInstanceValid(remoteView.Node)
                && interpolator.TrySample(_syncClockSeconds, out var latest))
            {
                ApplyWorldStateToView(
                    remoteView,
                    latest.Position,
                    latest.FacingDegrees,
                    latest.Moving,
                    latest.Alive);
            }
        }

        PushNetDebugToHud();
    }

    public void SetHudDebugExpanded(bool expanded)
    {
        _hud?.SetNetDebugExpanded(expanded);
        PushNetDebugToHud();
    }

    public bool ToggleHudDebugExpanded()
    {
        var expanded = _hud?.ToggleNetDebugExpanded() ?? false;
        PushNetDebugToHud();
        return expanded;
    }

    public NetSyncDebugModel GetNetSyncSnapshot()
    {
        _syncMetrics.Prune(_syncClockSeconds);
        var transport = _session?.GetTransportStats() ?? default;
        var fps = (float)Engine.GetFramesPerSecond();
        return new NetSyncDebugModel(
            Fps: fps,
            RttMs: transport.RttMs,
            LossRate: transport.LossRate,
            ReconciliationsPerSecond: _syncMetrics.ReconciliationsPerSecond,
            AveragePositionError: _syncMetrics.AveragePositionError,
            PredictionEnabled: PredictionEnabled,
            InterpolationEnabled: InterpolationEnabled,
            Expanded: HudDebugExpanded);
    }

    private void PushNetDebugToHud()
    {
        _hud?.SetNetDebugModel(GetNetSyncSnapshot());
    }

    private void OnHudPredictionToggled(bool enabled) => SetPredictionEnabled(enabled);
    private void OnHudInterpolationToggled(bool enabled) => SetInterpolationEnabled(enabled);

    private void LoadMap(uint mapId)
    {
        if (!_mapLoader.TryLoad(mapId, ClearWallNodes, AddWallNode))
            return;

        _camera.SetRoomBounds(CurrentMap.RoomBoundsGodot);
        QueueRedraw();
    }

    private void ClearWallNodes()
    {
        foreach (var child in _wallsRoot.GetChildren())
        {
            _wallsRoot.RemoveChild(child);
            child.QueueFree();
        }
    }

    private void AddWallNode(Rect2 rect)
    {
        var body = new StaticBody2D { Position = rect.GetCenter() };
        body.AddChild(new CollisionShape2D { Shape = new RectangleShape2D { Size = rect.Size } });
        _wallsRoot.AddChild(body);
    }

    private void OnRoomTransition(RoomTransition transition)
    {
        ClearEntities();
        _syncMetrics.Reset();
        LoadMap(transition.MapId);
        QueueRedraw();

        // 镜头先对准本地玩家出生点，避免首帧快照到达前视野停在原点
        if (_session != null && _session.Spawns.TryGetValue(_session.LocalUid, out var localSpawn))
            _camera.GlobalPosition = CurrentMap.WorldToGodot(localSpawn.PosX, localSpawn.PosY);

        UpdateHud(BattleMapCatalog.DefaultPlayerMaxHp, BattleMapCatalog.DefaultPlayerMaxHp, (int)transition.RoomIndex + 1);
        _hud?.SetBossModel(BossHudModel.Hidden);
        PushDungeonProgressToHud();

        if (_session != null)
        {
            _session.ConfirmRoomLoaded(transition.MapId);
            GD.Print($"[BattleWorld] 房间 {transition.MapId} (idx={transition.RoomIndex}/{transition.TotalRooms}) 加载完成，已发送 RoomLoaded");
        }
    }

    private void OnEntitySpawned(EntityState entity)
    {
        if (_entityViews.ContainsKey(entity.EntityId))
        {
            OnEntityUpdated(entity);
            return;
        }

        var view = CreateEntityView(entity, out var isLocal);
        if (view == null)
            return;

        if (ShouldInterpolateEntity(entity.Type, isLocal))
        {
            var interpolator = new RemoteEntityInterpolator { Enabled = _interpolationEnabled };
            interpolator.PushSample(ToSnapshotSample(entity, _syncClockSeconds));
            _remoteInterpolators[entity.EntityId] = interpolator;
        }

        ApplyStateToView(view, entity);
        view.Node.ResetPhysicsInterpolation();

        _entityViews[entity.EntityId] = view;
        if (isLocal)
        {
            var maxHp = entity.MaxHp > 0 ? entity.MaxHp : BattleMapCatalog.DefaultPlayerMaxHp;
            UpdateHud(entity.Hp, maxHp, (int)(_session?.CurrentRoomIndex ?? 0u) + 1);
            UpdateSkillHud(entity);
        }
        else if (entity.Type == EntityType.EntityBoss)
        {
            _activeBossEntityId = entity.EntityId;
            UpdateBossHud(entity);
        }
        else if (entity.Type == EntityType.EntityProjectile && entity.ConfigId <= 4)
        {
            _vfx.PlayShootSfx();
        }
    }

    private IBattleEntityView? CreateEntityView(EntityState entity, out bool isLocal)
    {
        isLocal = false;
        return entity.Type switch
        {
            EntityType.EntityPlayer => CreatePlayerEntityView(entity, out isLocal),
            EntityType.EntityEnemy => CreateEnemyEntityView(entity, isBoss: false),
            EntityType.EntityBoss => CreateEnemyEntityView(entity, isBoss: true),
            EntityType.EntityProjectile => CreateProjectileEntityView(entity),
            EntityType.EntityPickup => CreatePickupEntityView(entity),
            _ => null,
        };
    }

    private IBattleEntityView CreatePlayerEntityView(EntityState entity, out bool isLocal)
    {
        isLocal = _session != null && entity.PlayerUid != 0 && entity.PlayerUid == _session.LocalUid;
        var motor = _playerScene.Instantiate<PlayerMotor>();
        motor.Name = $"Player_{entity.EntityId}";
        motor.AuthoritativeDrive = true;
        motor.MoveSpeed = BattleMapCatalog.DefaultPlayerMoveSpeedTiles * CurrentMap.TileSizePixels;

        var godotPos = CurrentMap.WorldToGodot(entity.PosX, entity.PosY);
        motor.GlobalPosition = godotPos;
        _entitiesRoot.AddChild(motor);

        var alive = entity.State != EntityAnimState.AnimDead && entity.Hp > 0;
        var localInput = motor.GetNode<LocalPlayerInput>("%LocalInput");
        if (isLocal)
        {
            _localPlayerEntityId = entity.EntityId;
            _localInput = localInput;
            _localPredictor.Reset(new BattleVec2(entity.PosX, entity.PosY), entity.Facing, alive);
            localInput.TouchMode = _touch != null;
            if (_touch != null)
                localInput.BindTouch(_touch);
            localInput.SetEnabled(alive);
            localInput.CommandSampled += OnLocalCommandSampled;
            _camera.Target = motor;
            _camera.GlobalPosition = godotPos;
        }
        else
        {
            // 远端玩家不采集本机输入，也不运行本地 30Hz 模拟
            localInput.SetEnabled(false);
            localInput.SetProcess(false);
            localInput.SetProcessUnhandledInput(false);
            motor.InputSource = null;
            motor.SetPhysicsProcess(false);
            motor.Modulate = new Color("b8e3ff");
        }

        AttachNameTag(motor, entity.PlayerUid, isLocal);
        return motor;
    }

    private IBattleEntityView CreateEnemyEntityView(EntityState entity, bool isBoss)
    {
        var view = new BattleEnemyView { Name = $"{(isBoss ? "Boss" : "Enemy")}_{entity.EntityId}" };
        view.GlobalPosition = CurrentMap.WorldToGodot(entity.PosX, entity.PosY);
        _entitiesRoot.AddChild(view);
        view.Configure(isBoss, entity.ConfigId, entity.Hp, entity.MaxHp);
        view.UpdateCombatState(entity.Hp, entity.MaxHp, entity.StatusFlags);
        return view;
    }

    private IBattleEntityView CreateProjectileEntityView(EntityState entity)
    {
        var view = new BattleProjectileView { Name = $"Proj_{entity.EntityId}" };
        view.GlobalPosition = CurrentMap.WorldToGodot(entity.PosX, entity.PosY);
        _entitiesRoot.AddChild(view);
        view.Configure(entity.ConfigId);
        return view;
    }

    private IBattleEntityView CreatePickupEntityView(EntityState entity)
    {
        var view = new BattlePickupView { Name = $"Pickup_{entity.EntityId}" };
        view.GlobalPosition = CurrentMap.WorldToGodot(entity.PosX, entity.PosY);
        _entitiesRoot.AddChild(view);
        view.Configure(entity.ConfigId);
        return view;
    }

    private static bool ShouldInterpolateEntity(EntityType entityType, bool isLocal)
    {
        if (isLocal)
            return false;

        return entityType switch
        {
            EntityType.EntityPlayer => true,
            EntityType.EntityEnemy => true,
            EntityType.EntityBoss => true,
            EntityType.EntityProjectile => true,
            _ => false,
        };
    }

    private void OnEntityUpdated(EntityState entity)
    {
        if (!_entityViews.TryGetValue(entity.EntityId, out var view))
            return;

        if (view is BattleEnemyView enemyView)
            enemyView.UpdateCombatState(entity.Hp, entity.MaxHp, entity.StatusFlags);

        if (entity.Type == EntityType.EntityBoss)
        {
            _activeBossEntityId = entity.EntityId;
            UpdateBossHud(entity);
        }

        if (entity.EntityId == _localPlayerEntityId)
        {
            var alive = entity.State != EntityAnimState.AnimDead && entity.Hp > 0;
            var moving = entity.State == EntityAnimState.AnimMoving;
            var result = _localPredictor.Reconcile(
                new BattleVec2(entity.PosX, entity.PosY),
                entity.Facing,
                moving,
                alive,
                _session?.LastAckedInputSeq ?? 0u,
                _session?.PendingInputs ?? Array.Empty<InputCmd>(),
                CurrentMap);

            _syncMetrics.RecordSample(_syncClockSeconds, result.PositionError, result.Reconciled);

            ApplyWorldStateToView(
                view,
                _localPredictor.RenderPosition,
                _localPredictor.FacingDegrees,
                _localPredictor.IsMoving,
                _localPredictor.Alive);

            if (view.Node is PlayerMotor localMotor)
            {
                localMotor.Modulate = (entity.StatusFlags & 1u) != 0
                    ? new Color("7dd3fc")
                    : ((entity.StatusFlags & 4u) != 0 ? new Color("fde047") : Colors.White);
            }

            var maxHp = entity.MaxHp > 0 ? entity.MaxHp : BattleMapCatalog.DefaultPlayerMaxHp;
            UpdateHud(entity.Hp, maxHp, (int)(_session?.CurrentRoomIndex ?? 0u) + 1);
            UpdateSkillHud(entity);
            return;
        }

        if (!ShouldInterpolateEntity(entity.Type, isLocal: false))
        {
            ApplyStateToView(view, entity);
            return;
        }

        if (!_remoteInterpolators.TryGetValue(entity.EntityId, out var interpolator))
        {
            interpolator = new RemoteEntityInterpolator { Enabled = _interpolationEnabled };
            _remoteInterpolators[entity.EntityId] = interpolator;
        }

        interpolator.PushSample(ToSnapshotSample(entity, _syncClockSeconds));
        if (!_interpolationEnabled)
            ApplyStateToView(view, entity);
    }

    private void OnEntityDespawned(uint entityId, ulong playerUid)
    {
        _remoteInterpolators.Remove(entityId);
        if (entityId == _activeBossEntityId)
        {
            _activeBossEntityId = 0;
            _hud?.SetBossModel(BossHudModel.Hidden);
        }

        if (!_entityViews.Remove(entityId, out var view))
            return;

        if (entityId == _localPlayerEntityId)
        {
            if (_localInput != null)
            {
                _localInput.CommandSampled -= OnLocalCommandSampled;
                _localInput = null;
            }
            _localPlayerEntityId = 0;
            _localPredictor.Clear();
            if (IsInstanceValid(_camera) && _camera.Target == view.Node)
                _camera.Target = null;
        }

        if (IsInstanceValid(view.Node))
            view.Node.QueueFree();
    }

    private void OnDamageEvent(DamageEvent damage)
    {
        var godotPos = CurrentMap.WorldToGodot(damage.PosX, damage.PosY);
        var isLocalTarget = damage.TargetId == _localPlayerEntityId;
        _vfx.SpawnDamagePopup(godotPos, damage.Damage, isLocalTarget);

        if (isLocalTarget && damage.NewHp <= 0)
        {
            var maxHp = BattleMapCatalog.DefaultPlayerMaxHp;
            if (_session != null && _session.Entities.TryGetValue(_localPlayerEntityId, out var localEnt) && localEnt.MaxHp > 0)
                maxHp = localEnt.MaxHp;
            UpdateHud(0, maxHp, (int)(_session?.CurrentRoomIndex ?? 0u) + 1);
            _localInput?.SetEnabled(false);
        }

        if (_entityViews.TryGetValue(damage.TargetId, out var targetView) && targetView is BattleEnemyView enemyView)
            enemyView.TriggerHitFlash();
    }

    private void OnDoorStateChanged(DoorStateValue state)
    {
        PushDungeonProgressToHud();
        QueueRedraw();
        if (state == DoorStateValue.DoorOpen)
            _vfx.PlayDoorOpenSfx();
    }

    private void OnItemPicked(ItemPicked picked)
    {
        Vector2 pos = _camera.GlobalPosition;
        if (_session != null)
        {
            foreach (var entity in _session.Entities.Values)
            {
                if (entity.Type == EntityType.EntityPlayer && entity.PlayerUid == picked.Uid)
                {
                    pos = CurrentMap.WorldToGodot(entity.PosX, entity.PosY);
                    break;
                }
            }
        }
        _vfx.SpawnHealOrBuffPopup(pos, picked.HealedHp, picked.ItemConfigId);
    }

    private void OnSkillEvent(SkillEvent skill)
    {
        var pos = CurrentMap.WorldToGodot(skill.PosX, skill.PosY);
        var rad = BattleMapDefinition.WorldDegreesToGodotRadians(skill.DirAngle);
        _vfx.SpawnSkillEffect(pos, rad);
    }

    private void OnPlayerLeft(ulong uid, LeftReason reason)
    {
        GD.Print($"[BattleWorld] 玩家 {uid} 离开对局 ({reason})");
    }

    private void OnLocalCommandSampled(PlayerCommand command)
    {
        if (_session == null || !_session.IsRoomLoaded || _session.IsMatchFinished)
            return;

        var (moveX, moveY) = BattleMapDefinition.GodotMoveToWorld(command.Movement);
        var aimDeg = BattleMapDefinition.GodotAimRadiansToWorldDegrees(command.AimRadians);
        uint buttons = 0;
        if (command.AttackHeld)
            buttons |= (uint)InputButtons.BtnAttack;
        if (command.InteractPressed)
            buttons |= (uint)InputButtons.BtnInteract;
        if (command.SkillPressed)
            buttons |= (uint)InputButtons.BtnSkill;

        var cmd = _session.SampleAndSendInput(moveX, moveY, aimDeg, buttons);
        if (cmd != null && _localPlayerEntityId != 0 && _localPredictor.HasState)
        {
            _localPredictor.ApplyLocalInput(cmd, CurrentMap);
            if (_localPredictor.Enabled
                && _entityViews.TryGetValue(_localPlayerEntityId, out var localView)
                && IsInstanceValid(localView.Node))
            {
                ApplyWorldStateToView(
                    localView,
                    _localPredictor.RenderPosition,
                    _localPredictor.FacingDegrees,
                    _localPredictor.IsMoving,
                    _localPredictor.Alive);
            }
        }
    }

    private void UpdateLocalInteractionHint(BattleVec2 localWorldPos)
    {
        if (_localInput == null || _session == null)
            return;

        var nearPickup = false;
        var maxDistSq = PickupInteractRadiusTiles * PickupInteractRadiusTiles;
        foreach (var entity in _session.Entities.Values)
        {
            if (entity.Type != EntityType.EntityPickup)
                continue;
            var dx = entity.PosX - localWorldPos.X;
            var dy = entity.PosY - localWorldPos.Y;
            if (dx * dx + dy * dy <= maxDistSq)
            {
                nearPickup = true;
                break;
            }
        }
        _localInput.InteractionAvailable = nearPickup;
    }

    private void ApplyStateToView(IBattleEntityView view, EntityState entity)
    {
        var alive = entity.State != EntityAnimState.AnimDead && (entity.Type == EntityType.EntityProjectile || entity.Type == EntityType.EntityPickup || entity.Hp > 0);
        var moving = entity.State == EntityAnimState.AnimMoving;
        ApplyWorldStateToView(view, new BattleVec2(entity.PosX, entity.PosY), entity.Facing, moving, alive);
    }

    private void ApplyWorldStateToView(
        IBattleEntityView view,
        BattleVec2 worldPos,
        float facingDegrees,
        bool moving,
        bool alive)
    {
        var godotPos = CurrentMap.WorldToGodot(worldPos.X, worldPos.Y);
        var godotFacing = BattleMapDefinition.WorldDegreesToGodotRadians(facingDegrees);
        view.ApplyWorldState(godotPos, godotFacing, moving, alive);
    }

    private static EntitySnapshotSample ToSnapshotSample(EntityState entity, double timestampSeconds)
    {
        var alive = entity.State != EntityAnimState.AnimDead && (entity.Type == EntityType.EntityProjectile || entity.Type == EntityType.EntityPickup || entity.Hp > 0);
        var moving = entity.State == EntityAnimState.AnimMoving;
        return new EntitySnapshotSample(
            timestampSeconds,
            new BattleVec2(entity.PosX, entity.PosY),
            entity.Facing,
            moving,
            alive);
    }

    private void AttachNameTag(PlayerMotor motor, ulong uid, bool isLocal)
    {
        var displayName = _memberNames != null && _memberNames.TryGetValue(uid, out var name) && !string.IsNullOrWhiteSpace(name)
            ? name
            : (isLocal ? "我" : $"P{uid % 1000}");
        var label = new Label
        {
            Text = isLocal ? $"{displayName} (你)" : displayName,
            Position = new Vector2(-36, -24),
            Size = new Vector2(72, 16),
            HorizontalAlignment = HorizontalAlignment.Center,
            MouseFilter = Control.MouseFilterEnum.Ignore,
        };
        label.AddThemeFontSizeOverride("font_size", 9);
        label.Modulate = isLocal ? new Color("9bf5c6") : new Color("d6e9f7");
        motor.AddChild(label);
    }

    private void UpdateHud(int hp, int maxHp, int minorStage)
    {
        if (_hud == null)
            return;

        var safeMax = maxHp > 0 ? maxHp : BattleMapCatalog.DefaultPlayerMaxHp;
        _hud.SetModel(new GameHudModel(
            Health: new ResourceValue(Math.Clamp(hp, 0, safeMax), safeMax),
            Mana: new ResourceValue(40, 40),
            Shield: new ResourceValue(0, 0),
            MajorStage: 1,
            MinorStage: Math.Clamp(minorStage, 1, 5)));
    }

    private void UpdateSkillHud(EntityState localPlayer)
    {
        if (_hud == null)
            return;

        var total = localPlayer.SkillCdTotal > 0f ? localPlayer.SkillCdTotal : 5f;
        _hud.SetSkillModel(new SkillHudModel(
            SkillName: "冲刺斩 [Space]",
            CooldownRemaining: localPlayer.SkillCdRemaining,
            CooldownTotal: total,
            StatusFlags: localPlayer.StatusFlags));
    }

    private void UpdateBossHud(EntityState boss)
    {
        if (_hud == null)
            return;

        var maxHp = boss.MaxHp > 0 ? boss.MaxHp : Math.Max(1, boss.Hp);
        var isPhase2 = (boss.StatusFlags & BattleEnemyView.StatusBossPhase2) != 0;
        _hud.SetBossModel(new BossHudModel(
            Visible: boss.Hp > 0 && boss.State != EntityAnimState.AnimDead,
            BossName: "地牢领主",
            CurrentHp: boss.Hp,
            MaxHp: maxHp,
            IsPhase2: isPhase2));
    }

    private void PushDungeonProgressToHud()
    {
        if (_hud == null || _session == null)
            return;

        _hud.SetDungeonProgress(new DungeonProgressModel(
            RoomIndex: (int)_session.CurrentRoomIndex,
            TotalRooms: (int)_session.TotalRooms,
            CurrentMapId: _session.CurrentMapId,
            RoomName: CurrentMap.Name,
            DoorOpen: _session.DoorOpen,
            MapSequence: _session.MapSequence));
    }

    private void ClearEntities()
    {
        if (_localInput != null)
        {
            _localInput.CommandSampled -= OnLocalCommandSampled;
            _localInput = null;
        }
        _localPlayerEntityId = 0;
        _activeBossEntityId = 0;
        _localPredictor.Clear();
        _remoteInterpolators.Clear();

        if (IsInstanceValid(_camera))
            _camera.Target = null;

        foreach (var view in _entityViews.Values)
        {
            if (IsInstanceValid(view.Node))
                view.Node.QueueFree();
        }
        _entityViews.Clear();
    }

    public override void _Draw()
    {
        var bounds = CurrentMap.RoomBoundsGodot;
        var tile = CurrentMap.TileSizePixels;
        var palette = CurrentMap.Palette;

        // 地面底色与 1-tile 网格线（与服务端世界单位 1:1 对齐，各房间模板有专属配色）
        DrawRect(bounds, new Color(palette.FloorHex));
        var gridColor = new Color(palette.GridHex);
        for (var x = 0f; x <= bounds.Size.X; x += tile)
            DrawLine(new Vector2(x, 0), new Vector2(x, bounds.Size.Y), gridColor);
        for (var y = 0f; y <= bounds.Size.Y; y += tile)
            DrawLine(new Vector2(0, y), new Vector2(bounds.Size.X, y), gridColor);

        // 出口区域门可视化（右侧中央 ExitZone：锁定=暗红栅栏，开启=翠绿脉冲传送区）
        var exitRect = CurrentMap.ExitZoneGodot;
        if (DoorOpen)
        {
            var pulse = 0.25f + 0.14f * MathF.Sin(_doorPulseTime);
            DrawRect(exitRect, new Color(0.18f, 0.85f, 0.45f, pulse));
            DrawRect(exitRect, new Color("4ade80"), false, 2.5f);
            // 向右引导箭头
            var center = exitRect.GetCenter();
            DrawLine(center + new Vector2(-14, 0), center + new Vector2(14, 0), new Color("bbf7d0"), 3f);
            DrawLine(center + new Vector2(4, -9), center + new Vector2(14, 0), new Color("bbf7d0"), 3f);
            DrawLine(center + new Vector2(4, 9), center + new Vector2(14, 0), new Color("bbf7d0"), 3f);
        }
        else
        {
            DrawRect(exitRect, new Color(0.75f, 0.22f, 0.22f, 0.20f));
            DrawRect(exitRect, new Color("ef4444", 0.75f), false, 2f);
            for (var barY = exitRect.Position.Y + 12f; barY < exitRect.End.Y; barY += 16f)
            {
                DrawLine(
                    new Vector2(exitRect.Position.X + 4f, barY),
                    new Vector2(exitRect.End.X - 4f, barY),
                    new Color("fca5a5", 0.45f),
                    2f);
            }
        }

        // 矩形墙体 AABB
        var shadowColor = new Color(palette.WallShadowHex);
        var wallColor = new Color(palette.WallHex);
        var topColor = new Color(palette.WallTopHex);
        foreach (var wall in _mapLoader.WallRects)
        {
            DrawRect(new Rect2(wall.Position + new Vector2(3, 5), wall.Size), shadowColor);
            DrawRect(wall, wallColor);
            DrawLine(wall.Position, new Vector2(wall.End.X, wall.Position.Y), topColor, 2);
        }
    }

    public override void _ExitTree()
    {
        UnbindSession();
    }
}
