#nullable enable
using System;
using System.Collections.Generic;
using SepCore.Definition;
using SepCore.Runtime.Network;

namespace SepCore.Battle;

/// <summary>
/// 战斗网络会话核心（纯 C#，可脱离 Godot 单元测试）。
/// 负责：
/// - 消费 RoomTransition (68) 并发送 RoomLoaded (69)；
/// - 30Hz 采样打包并发送 InputBatch (97)；
/// - 接收 Snapshot (96) / EntitySpawn (64) / EntityDespawn (65) / DamageEvent (66) /
///   DoorState (67) / ItemPicked (70) / PlayerLeft (71) / GameResult (72) / SkillEvent (73)；
/// - 维护权威实体表、地牢房间进度、出口门状态与结算状态。
/// </summary>
public sealed class BattleSessionClient : IDisposable
{
    private readonly INetworkMessageClient _network;
    private readonly List<IDisposable> _subscriptions = new();
    private readonly Dictionary<uint, EntityState> _entities = new();
    private readonly Dictionary<ulong, SpawnAssignment> _spawns = new();
    private readonly InputBatchTracker _inputs = new();
    private bool _disposed;
    private uint _confirmedMapId;

    public ulong LocalUid => _network.Uid;
    public ulong DungeonSeed { get; private set; }
    public uint CurrentMapId { get; private set; }
    public uint CurrentRoomIndex { get; private set; }
    public uint TotalRooms { get; private set; }
    public bool DoorOpen { get; private set; }
    public IReadOnlyList<uint> MapSequence { get; private set; } = Array.Empty<uint>();
    public bool IsRoomLoaded => _confirmedMapId != 0 && _confirmedMapId == CurrentMapId;
    public uint LastSnapshotTick { get; private set; }
    public uint NextInputSeq => _inputs.NextSeq;
    public uint LastAckedInputSeq => _inputs.LastAckedSeq;
    public int PendingInputCount => _inputs.PendingCount;
    public IReadOnlyList<InputCmd> PendingInputs => _inputs.PendingInputs;
    public IReadOnlyDictionary<uint, EntityState> Entities => _entities;
    public IReadOnlyDictionary<ulong, SpawnAssignment> Spawns => _spawns;
    public GameResult? LastGameResult { get; private set; }
    public bool IsMatchFinished => LastGameResult != null;
    public NetworkTransportStats GetTransportStats() => _network.GetTransportStats();

    public event Action<RoomTransition>? RoomTransitionReceived;
    public event Action<EntityState>? EntitySpawned;
    public event Action<EntityState>? EntityUpdated;
    public event Action<uint, ulong>? EntityDespawned;
    public event Action<DamageEvent>? DamageEventReceived;
    public event Action<DoorStateValue>? DoorStateChanged;
    public event Action<ItemPicked>? ItemPickedReceived;
    public event Action<SkillEvent>? SkillEventReceived;
    public event Action<ulong, LeftReason>? PlayerLeftReceived;
    public event Action<Snapshot>? SnapshotReceived;
    public event Action<GameResult>? GameResultReceived;
    public event Action<string>? SessionEnded;

    public BattleSessionClient(INetworkMessageClient network)
    {
        _network = network ?? throw new ArgumentNullException(nameof(network));
        _subscriptions.Add(_network.Subscribe<RoomTransition>(OnRoomTransition));
        _subscriptions.Add(_network.Subscribe<Snapshot>(OnSnapshot));
        _subscriptions.Add(_network.Subscribe<EntitySpawn>(OnEntitySpawn));
        _subscriptions.Add(_network.Subscribe<EntityDespawn>(OnEntityDespawn));
        _subscriptions.Add(_network.Subscribe<DamageEvent>(OnDamageEvent));
        _subscriptions.Add(_network.Subscribe<DoorState>(OnDoorState));
        _subscriptions.Add(_network.Subscribe<ItemPicked>(OnItemPicked));
        _subscriptions.Add(_network.Subscribe<SkillEvent>(OnSkillEvent));
        _subscriptions.Add(_network.Subscribe<PlayerLeft>(OnPlayerLeft));
        _subscriptions.Add(_network.Subscribe<GameResult>(OnGameResult));
        _subscriptions.Add(_network.Subscribe<RoomStatePush>(OnRoomStatePush));
        _network.Disconnected += OnDisconnected;
    }

    /// <summary>启动战斗会话，并立刻回放切场景期间缓存的 GameStart / RoomTransition。</summary>
    public void Start(GameStart? gameStart, RoomTransition? pendingTransition)
    {
        _inputs.Reset();
        LastGameResult = null;
        DoorOpen = false;
        if (gameStart != null)
        {
            DungeonSeed = gameStart.Seed;
            var seq = new uint[gameStart.MapIds.Count];
            for (var i = 0; i < gameStart.MapIds.Count; i++)
                seq[i] = gameStart.MapIds[i];
            MapSequence = Array.AsReadOnly(seq);
            TotalRooms = (uint)seq.Length;
        }

        if (pendingTransition != null)
            OnRoomTransition(pendingTransition);
    }

    /// <summary>客户端本地加载完指定 mapId 后调用，发送 RoomLoaded (69)（同一房间只确认一次）。</summary>
    public bool ConfirmRoomLoaded(uint mapId)
    {
        if (_disposed || mapId == 0 || mapId != CurrentMapId || _confirmedMapId == mapId)
            return false;
        if (_network.State != NetworkState.Connected)
            return false;

        _confirmedMapId = mapId;
        _network.Send(new RoomLoaded { MapId = mapId });
        return true;
    }

    /// <summary>在 30Hz 固定步长上记录一条输入并发送包含全部未确认输入的 InputBatch (97)。</summary>
    public InputCmd? SampleAndSendInput(float worldMoveX, float worldMoveY, float worldAimDeg, uint buttons)
    {
        if (_disposed || !IsRoomLoaded || IsMatchFinished || _network.State != NetworkState.Connected)
            return null;

        var cmd = _inputs.Record(worldMoveX, worldMoveY, worldAimDeg, buttons);
        var batch = _inputs.BuildBatch();
        if (batch != null)
            _network.Send(batch);
        return cmd;
    }

    private void OnRoomTransition(RoomTransition transition)
    {
        if (_disposed) return;

        // 清理旧房间实体
        if (_entities.Count > 0)
        {
            var oldIds = new List<(uint Id, ulong Uid)>(_entities.Count);
            foreach (var kv in _entities)
                oldIds.Add((kv.Key, kv.Value.PlayerUid));
            _entities.Clear();
            foreach (var (id, uid) in oldIds)
                EntityDespawned?.Invoke(id, uid);
        }

        CurrentMapId = transition.MapId;
        CurrentRoomIndex = transition.RoomIndex;
        if (transition.TotalRooms > 0)
            TotalRooms = transition.TotalRooms;
        else if (TotalRooms == 0 && MapSequence.Count > 0)
            TotalRooms = (uint)MapSequence.Count;

        DoorOpen = false;
        _confirmedMapId = 0;
        LastSnapshotTick = 0;
        _inputs.ClearPending();
        _spawns.Clear();
        foreach (var spawn in transition.Spawns)
            _spawns[spawn.Uid] = spawn.Clone();

        RoomTransitionReceived?.Invoke(transition.Clone());
    }

    private void OnSnapshot(Snapshot snapshot)
    {
        if (_disposed) return;

        // 忽略乱序旧快照（仅当已收到过当前房间的快照时校验）
        if (LastSnapshotTick > 0 && snapshot.Tick < LastSnapshotTick)
            return;

        LastSnapshotTick = snapshot.Tick;
        _inputs.Acknowledge(snapshot.LastInputSeq);

        var seen = new HashSet<uint>(snapshot.Entities.Count);
        foreach (var entity in snapshot.Entities)
        {
            if (entity.EntityId == 0)
                continue;

            seen.Add(entity.EntityId);
            var clone = entity.Clone();
            if (_entities.ContainsKey(entity.EntityId))
            {
                _entities[entity.EntityId] = clone;
                EntityUpdated?.Invoke(clone);
            }
            else
            {
                // 兜底生成（Docs/protocol.md §7.3）
                _entities[entity.EntityId] = clone;
                EntitySpawned?.Invoke(clone);
            }
        }

        // 快照中消失的实体一律移除
        List<(uint Id, ulong Uid)>? removed = null;
        foreach (var kv in _entities)
        {
            if (!seen.Contains(kv.Key))
            {
                removed ??= new List<(uint, ulong)>();
                removed.Add((kv.Key, kv.Value.PlayerUid));
            }
        }

        if (removed != null)
        {
            foreach (var (id, uid) in removed)
            {
                _entities.Remove(id);
                EntityDespawned?.Invoke(id, uid);
            }
        }

        SnapshotReceived?.Invoke(snapshot);
    }

    private void OnEntitySpawn(EntitySpawn spawn)
    {
        if (_disposed) return;

        foreach (var entity in spawn.Entities)
        {
            if (entity.EntityId == 0)
                continue;

            var clone = entity.Clone();
            if (_entities.ContainsKey(entity.EntityId))
            {
                _entities[entity.EntityId] = clone;
                EntityUpdated?.Invoke(clone);
            }
            else
            {
                _entities[entity.EntityId] = clone;
                EntitySpawned?.Invoke(clone);
            }
        }
    }

    private void OnEntityDespawn(EntityDespawn despawn)
    {
        if (_disposed) return;

        foreach (var id in despawn.EntityIds)
        {
            if (_entities.Remove(id, out var existing))
                EntityDespawned?.Invoke(id, existing.PlayerUid);
        }
    }

    private void OnDamageEvent(DamageEvent damage)
    {
        if (_disposed) return;
        DamageEventReceived?.Invoke(damage.Clone());
    }

    private void OnDoorState(DoorState door)
    {
        if (_disposed) return;
        DoorOpen = door.State == DoorStateValue.DoorOpen;
        DoorStateChanged?.Invoke(door.State);
    }

    private void OnItemPicked(ItemPicked picked)
    {
        if (_disposed) return;
        ItemPickedReceived?.Invoke(picked.Clone());
    }

    private void OnSkillEvent(SkillEvent skill)
    {
        if (_disposed) return;
        SkillEventReceived?.Invoke(skill.Clone());
    }

    private void OnPlayerLeft(PlayerLeft left)
    {
        if (_disposed) return;

        _spawns.Remove(left.Uid);

        List<uint>? toRemove = null;
        foreach (var kv in _entities)
        {
            if (kv.Value.Type == EntityType.EntityPlayer && kv.Value.PlayerUid == left.Uid)
            {
                toRemove ??= new List<uint>();
                toRemove.Add(kv.Key);
            }
        }

        if (toRemove != null)
        {
            foreach (var id in toRemove)
            {
                _entities.Remove(id);
                EntityDespawned?.Invoke(id, left.Uid);
            }
        }

        PlayerLeftReceived?.Invoke(left.Uid, left.Reason);
    }

    private void OnGameResult(GameResult result)
    {
        if (_disposed) return;
        var clone = result.Clone();
        LastGameResult = clone;
        GameResultReceived?.Invoke(clone);
    }

    private void OnRoomStatePush(RoomStatePush push)
    {
        if (_disposed) return;
        if (push.State == RoomSessionState.RoomClosed && !IsMatchFinished)
            SessionEnded?.Invoke("房间已关闭");
    }

    private void OnDisconnected(string reason)
    {
        if (_disposed) return;
        SessionEnded?.Invoke("连接已断开：" + reason);
    }

    public void Dispose()
    {
        if (_disposed) return;
        _disposed = true;
        foreach (var sub in _subscriptions)
            sub.Dispose();
        _subscriptions.Clear();
        _network.Disconnected -= OnDisconnected;
    }
}
