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
/// 消费 BattleSessionClient 的房间切换与快照事件并上报 30Hz 玩家输入。
/// </summary>
public partial class BattleWorld : Node2D
{
    private const string PlayerPrefabPath = "res://Prefabs/Entity/Player.tscn";

    private Node2D _wallsRoot = null!;
    private Node2D _entitiesRoot = null!;
    private PlayerCamera _camera = null!;
    private PackedScene _playerScene = null!;

    private BattleSessionClient? _session;
    private GameHudController? _hud;
    private TouchControlsView? _touch;
    private IReadOnlyDictionary<ulong, string>? _memberNames;

    private readonly BattleMapLoader _mapLoader = new();
    private BattleMapDefinition CurrentMap => _mapLoader.CurrentMap;
    private readonly Dictionary<uint, PlayerMotor> _playerNodes = new();
    private uint _localPlayerEntityId;
    private LocalPlayerInput? _localInput;

    public BattleSessionClient? ActiveSession => _session;
    public uint LocalPlayerEntityId => _localPlayerEntityId;
    public LocalPlayerInput? LocalInput => _localInput;
    public BattleMapDefinition ActiveMap => CurrentMap;
    public IReadOnlyDictionary<uint, PlayerMotor> PlayerNodes => _playerNodes;

    public override void _Ready()
    {
        RenderingServer.SetDefaultClearColor(new Color("101b25"));
        _wallsRoot = GetNode<Node2D>("%WallsRoot");
        _entitiesRoot = GetNode<Node2D>("%EntitiesRoot");
        _camera = GetNode<PlayerCamera>("%PlayerCamera");
        _playerScene = GD.Load<PackedScene>(PlayerPrefabPath);
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
        _session.PlayerLeftReceived += OnPlayerLeft;
    }

    public void UnbindSession()
    {
        if (_session != null)
        {
            _session.RoomTransitionReceived -= OnRoomTransition;
            _session.EntitySpawned -= OnEntitySpawned;
            _session.EntityUpdated -= OnEntityUpdated;
            _session.EntityDespawned -= OnEntityDespawned;
            _session.PlayerLeftReceived -= OnPlayerLeft;
            _session = null;
        }

        ClearEntities();
        _hud = null;
        _touch = null;
        _memberNames = null;
    }

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
        LoadMap(transition.MapId);

        // 镜头先对准本地玩家出生点，避免首帧快照到达前视野停在原点
        if (_session != null && _session.Spawns.TryGetValue(_session.LocalUid, out var localSpawn))
            _camera.GlobalPosition = CurrentMap.WorldToGodot(localSpawn.PosX, localSpawn.PosY);

        UpdateHud(BattleMapCatalog.DefaultPlayerMaxHp, (int)transition.MapId);

        if (_session != null)
        {
            _session.ConfirmRoomLoaded(transition.MapId);
            GD.Print($"[BattleWorld] 房间 {transition.MapId} 加载完成，已发送 RoomLoaded");
        }
    }

    private void OnEntitySpawned(EntityState entity)
    {
        if (entity.Type != EntityType.EntityPlayer)
            return;

        if (_playerNodes.ContainsKey(entity.EntityId))
        {
            OnEntityUpdated(entity);
            return;
        }

        var isLocal = _session != null && entity.PlayerUid != 0 && entity.PlayerUid == _session.LocalUid;
        var motor = _playerScene.Instantiate<PlayerMotor>();
        motor.Name = $"Player_{entity.EntityId}";
        motor.AuthoritativeDrive = true;
        motor.MoveSpeed = BattleMapCatalog.DefaultPlayerMoveSpeedTiles * CurrentMap.TileSizePixels;

        var godotPos = CurrentMap.WorldToGodot(entity.PosX, entity.PosY);
        motor.GlobalPosition = godotPos;
        _entitiesRoot.AddChild(motor);

        var localInput = motor.GetNode<LocalPlayerInput>("%LocalInput");
        if (isLocal)
        {
            _localPlayerEntityId = entity.EntityId;
            _localInput = localInput;
            localInput.TouchMode = _touch != null;
            if (_touch != null)
                localInput.BindTouch(_touch);
            localInput.SetEnabled(true);
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
        ApplyStateToMotor(motor, entity);
        motor.ResetPhysicsInterpolation();

        _playerNodes[entity.EntityId] = motor;
        if (isLocal)
            UpdateHud(entity.Hp, (int)Math.Max(1u, _session?.CurrentMapId ?? 1u));
    }

    private void OnEntityUpdated(EntityState entity)
    {
        if (!_playerNodes.TryGetValue(entity.EntityId, out var motor))
            return;

        ApplyStateToMotor(motor, entity);
        if (entity.EntityId == _localPlayerEntityId)
            UpdateHud(entity.Hp, (int)Math.Max(1u, _session?.CurrentMapId ?? 1u));
    }

    private void OnEntityDespawned(uint entityId, ulong playerUid)
    {
        if (!_playerNodes.Remove(entityId, out var motor))
            return;

        if (entityId == _localPlayerEntityId)
        {
            if (_localInput != null)
            {
                _localInput.CommandSampled -= OnLocalCommandSampled;
                _localInput = null;
            }
            _localPlayerEntityId = 0;
            if (IsInstanceValid(_camera) && _camera.Target == motor)
                _camera.Target = null;
        }

        if (IsInstanceValid(motor))
            motor.QueueFree();
    }

    private void OnPlayerLeft(ulong uid, LeftReason reason)
    {
        GD.Print($"[BattleWorld] 玩家 {uid} 离开对局 ({reason})");
    }

    private void OnLocalCommandSampled(PlayerCommand command)
    {
        if (_session == null || !_session.IsRoomLoaded)
            return;

        var (moveX, moveY) = BattleMapDefinition.GodotMoveToWorld(command.Movement);
        var aimDeg = BattleMapDefinition.GodotAimRadiansToWorldDegrees(command.AimRadians);
        uint buttons = 0;
        if (command.AttackHeld)
            buttons |= (uint)InputButtons.BtnAttack;
        if (command.InteractPressed)
            buttons |= (uint)InputButtons.BtnInteract;

        _session.SampleAndSendInput(moveX, moveY, aimDeg, buttons);
    }

    private void ApplyStateToMotor(PlayerMotor motor, EntityState entity)
    {
        var godotPos = CurrentMap.WorldToGodot(entity.PosX, entity.PosY);
        var godotFacing = BattleMapDefinition.WorldDegreesToGodotRadians(entity.Facing);
        var alive = entity.State != EntityAnimState.AnimDead && entity.Hp > 0;
        var moving = entity.State == EntityAnimState.AnimMoving;
        motor.ApplyAuthoritativeState(godotPos, godotFacing, moving, alive);
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

    private void UpdateHud(int hp, int mapId)
    {
        if (_hud == null)
            return;

        var maxHp = BattleMapCatalog.DefaultPlayerMaxHp;
        _hud.SetModel(new GameHudModel(
            Health: new ResourceValue(Math.Clamp(hp, 0, maxHp), maxHp),
            Mana: new ResourceValue(40, 40),
            Shield: new ResourceValue(0, 0),
            MajorStage: 1,
            MinorStage: Math.Clamp(mapId, 1, 5)));
    }

    private void ClearEntities()
    {
        if (_localInput != null)
        {
            _localInput.CommandSampled -= OnLocalCommandSampled;
            _localInput = null;
        }
        _localPlayerEntityId = 0;

        if (IsInstanceValid(_camera))
            _camera.Target = null;

        foreach (var motor in _playerNodes.Values)
        {
            if (IsInstanceValid(motor))
                motor.QueueFree();
        }
        _playerNodes.Clear();
    }

    public override void _Draw()
    {
        var bounds = CurrentMap.RoomBoundsGodot;
        var tile = CurrentMap.TileSizePixels;

        // 地面底色与 1-tile 网格线（与服务端世界单位 1:1 对齐）
        DrawRect(bounds, new Color("203440"));
        for (var x = 0f; x <= bounds.Size.X; x += tile)
            DrawLine(new Vector2(x, 0), new Vector2(x, bounds.Size.Y), new Color("293f4a"));
        for (var y = 0f; y <= bounds.Size.Y; y += tile)
            DrawLine(new Vector2(0, y), new Vector2(bounds.Size.X, y), new Color("293f4a"));

        // 矩形墙体 AABB
        foreach (var wall in _mapLoader.WallRects)
        {
            DrawRect(new Rect2(wall.Position + new Vector2(3, 5), wall.Size), new Color("142630"));
            DrawRect(wall, new Color("526d77"));
            DrawLine(wall.Position, new Vector2(wall.End.X, wall.Position.Y), new Color("91a8ac"), 2);
        }
    }

    public override void _ExitTree()
    {
        UnbindSession();
    }
}
