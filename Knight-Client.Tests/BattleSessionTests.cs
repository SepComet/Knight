#nullable enable
using System;
using System.Collections.Generic;
using Godot;
using Google.Protobuf;
using SepCore.Battle;
using SepCore.Definition;
using SepCore.Runtime.Network;
using Xunit;

namespace SepCore.Tests;

public class BattleSessionTests
{
    private sealed class FakeNetwork : INetworkMessageClient
    {
        public NetworkState State { get; set; } = NetworkState.Connected;
        public ulong Uid { get; set; } = 1001;
        public event Action<string>? Disconnected;
        public readonly List<IMessage> Sent = new();
        private readonly Dictionary<Type, List<Delegate>> _handlers = new();

        public void Send<T>(T message) where T : IMessage => Sent.Add(message);

        public IDisposable Subscribe<T>(Action<T> handler) where T : IMessage
        {
            var type = typeof(T);
            if (!_handlers.TryGetValue(type, out var list))
            {
                list = new List<Delegate>();
                _handlers[type] = list;
            }
            list.Add(handler);
            return new Cleanup(() => list.Remove(handler));
        }

        public void Push<T>(T message) where T : IMessage
        {
            if (!_handlers.TryGetValue(typeof(T), out var list)) return;
            foreach (var d in list.ToArray())
                ((Action<T>)d)(message);
        }

        public void Disconnect(string reason = "test")
        {
            State = NetworkState.Disconnected;
            Disconnected?.Invoke(reason);
        }

        private sealed class Cleanup(Action action) : IDisposable
        {
            public void Dispose() => action();
        }
    }

    // ---------------------------------------------------------------- InputBatchTracker

    [Fact]
    public void InputBatchTracker_AssignsMonotonicSeq_AcksAndCapsAt120()
    {
        var tracker = new InputBatchTracker();
        Assert.Null(tracker.BuildBatch());

        var c1 = tracker.Record(1f, 0f, 0f, (uint)InputButtons.BtnAttack);
        var c2 = tracker.Record(3f, 4f, 90f, 0u); // magnitude 5 -> clamped to (0.6, 0.8)
        Assert.Equal(1u, c1.Seq);
        Assert.Equal(2u, c2.Seq);
        Assert.Equal(0.6f, c2.MoveX, 3);
        Assert.Equal(0.8f, c2.MoveY, 3);

        var batch1 = tracker.BuildBatch();
        Assert.NotNull(batch1);
        Assert.Equal(1u, batch1!.FirstSeq);
        Assert.Equal(2, batch1.Inputs.Count);

        tracker.Acknowledge(1u);
        var batch2 = tracker.BuildBatch();
        Assert.NotNull(batch2);
        Assert.Equal(2u, batch2!.FirstSeq);
        Assert.Single(batch2.Inputs);
        Assert.Equal(2u, batch2.Inputs[0].Seq);

        tracker.Acknowledge(2u);
        Assert.Null(tracker.BuildBatch());

        // Cap at 120 entries even if server never acks
        for (var i = 0; i < 150; i++)
            tracker.Record(0f, 1f, 90f, 0u);

        var capped = tracker.BuildBatch();
        Assert.NotNull(capped);
        Assert.Equal(InputBatchTracker.MaxBatchSize, capped!.Inputs.Count);
        Assert.Equal(capped.Inputs[0].Seq, capped.FirstSeq);
        Assert.Equal(152u, capped.Inputs[^1].Seq);
    }

    // ---------------------------------------------------------------- BattleMapCatalog

    [Fact]
    public void BattleMap1_CoordinatesAndWallsMatchServerContract()
    {
        var map = BattleMapCatalog.Get(1);
        Assert.Equal(new Rect2(0, 0, 960, 640), map.RoomBoundsGodot);
        Assert.Equal(7, map.Walls.Count);

        // Perimeter + 3 interior obstacle walls
        Assert.Equal(new Rect2(0, 608, 960, 32), map.WallToGodotRect(map.Walls[0]));   // Bottom [0,0,30,1]
        Assert.Equal(new Rect2(0, 0, 960, 32), map.WallToGodotRect(map.Walls[1]));     // Top [0,19,30,20]
        Assert.Equal(new Rect2(0, 0, 32, 640), map.WallToGodotRect(map.Walls[2]));     // Left [0,0,1,20]
        Assert.Equal(new Rect2(928, 0, 32, 640), map.WallToGodotRect(map.Walls[3]));   // Right [29,0,30,20]
        Assert.Equal(new Rect2(320, 160, 32, 192), map.WallToGodotRect(map.Walls[4])); // Wall 1 [10,9,11,15]
        Assert.Equal(new Rect2(480, 352, 160, 32), map.WallToGodotRect(map.Walls[5])); // Wall 2 [15,8,20,9]
        Assert.Equal(new Rect2(736, 160, 32, 128), map.WallToGodotRect(map.Walls[6])); // Wall 3 [23,11,24,15]

        // Slot 0 spawn (4.0, 12.0) -> Godot (128, 256)
        var godotSpawn = map.WorldToGodot(4f, 12f);
        Assert.Equal(new Vector2(128f, 256f), godotSpawn);
        var (wx, wy) = map.GodotToWorld(godotSpawn);
        Assert.Equal(4f, wx, 3);
        Assert.Equal(12f, wy, 3);

        // Godot up (0, -1) -> World up (0, +1)
        var (mx, my) = BattleMapDefinition.GodotMoveToWorld(new Vector2(0f, -1f));
        Assert.Equal(0f, mx, 3);
        Assert.Equal(1f, my, 3);

        // Godot up (-pi/2) <-> World up (+90 deg)
        Assert.Equal(90f, BattleMapDefinition.GodotAimRadiansToWorldDegrees(-MathF.PI / 2f), 2);
        Assert.Equal(-MathF.PI / 2f, BattleMapDefinition.WorldDegreesToGodotRadians(90f), 3);
    }

    // ---------------------------------------------------------------- BattleSessionClient

    [Fact]
    public void BattleSession_RoomTransition_SendsRoomLoadedOnce_AndStreamsInputBatches()
    {
        var net = new FakeNetwork { Uid = 1001 };
        using var session = new BattleSessionClient(net);

        RoomTransition? receivedTransition = null;
        session.RoomTransitionReceived += t =>
        {
            receivedTransition = t;
            Assert.True(session.ConfirmRoomLoaded(t.MapId));
            Assert.False(session.ConfirmRoomLoaded(t.MapId)); // duplicate suppressed
        };

        var start = new GameStart { MapIds = { 1u, 2u, 3u } };
        var transition = new RoomTransition
        {
            MapId = 1u,
            Spawns =
            {
                new SpawnAssignment { Uid = 1001, PosX = 4f, PosY = 12f },
                new SpawnAssignment { Uid = 1002, PosX = 4f, PosY = 8f },
            }
        };

        // Before Start/RoomLoaded, input is ignored
        Assert.Null(session.SampleAndSendInput(1f, 0f, 0f, 0u));

        session.Start(start, transition);

        Assert.NotNull(receivedTransition);
        Assert.Equal(1u, session.CurrentMapId);
        Assert.True(session.IsRoomLoaded);
        Assert.Equal(2, session.Spawns.Count);
        var loaded = Assert.IsType<RoomLoaded>(Assert.Single(net.Sent));
        Assert.Equal(1u, loaded.MapId);

        // Sample two inputs -> second batch carries both unacked inputs
        session.SampleAndSendInput(1f, 0f, 0f, 0u);
        session.SampleAndSendInput(0f, 1f, 90f, (uint)InputButtons.BtnAttack);

        var batch1 = Assert.IsType<InputBatch>(net.Sent[1]);
        Assert.Equal(1u, batch1.FirstSeq);
        Assert.Single(batch1.Inputs);

        var batch2 = Assert.IsType<InputBatch>(net.Sent[2]);
        Assert.Equal(1u, batch2.FirstSeq);
        Assert.Equal(2, batch2.Inputs.Count);
        Assert.Equal(2u, batch2.Inputs[1].Seq);

        // Snapshot acks seq 1 -> next batch starts at seq 2
        net.Push(new Snapshot { Tick = 1, LastInputSeq = 1 });
        session.SampleAndSendInput(0f, 0f, 90f, 0u);

        var batch3 = Assert.IsType<InputBatch>(net.Sent[3]);
        Assert.Equal(2u, batch3.FirstSeq);
        Assert.Equal(2, batch3.Inputs.Count);
        Assert.Equal(3u, batch3.Inputs[1].Seq);
    }

    [Fact]
    public void BattleSession_SnapshotSync_FallbackSpawns_Updates_Despawns_AndHandlesPlayerLeft()
    {
        var net = new FakeNetwork { Uid = 1001 };
        using var session = new BattleSessionClient(net);

        var spawned = new List<EntityState>();
        var updated = new List<EntityState>();
        var despawned = new List<(uint Id, ulong Uid)>();
        var leftUids = new List<ulong>();

        session.EntitySpawned += e => spawned.Add(e);
        session.EntityUpdated += e => updated.Add(e);
        session.EntityDespawned += (id, uid) => despawned.Add((id, uid));
        session.PlayerLeftReceived += (uid, _) => leftUids.Add(uid);

        session.Start(new GameStart { MapIds = { 1u } }, new RoomTransition { MapId = 1u });
        session.ConfirmRoomLoaded(1u);

        // Tick 2: two players appear in snapshot without prior EntitySpawn -> fallback spawn
        net.Push(new Snapshot
        {
            Tick = 2,
            LastInputSeq = 0,
            Entities =
            {
                new EntityState { EntityId = 10, Type = EntityType.EntityPlayer, PlayerUid = 1001, ConfigId = 1, PosX = 4f, PosY = 12f, Hp = 100, State = EntityAnimState.AnimIdle },
                new EntityState { EntityId = 11, Type = EntityType.EntityPlayer, PlayerUid = 1002, ConfigId = 1, PosX = 4f, PosY = 8f, Hp = 100, State = EntityAnimState.AnimMoving },
            }
        });

        Assert.Equal(2, spawned.Count);
        Assert.Empty(updated);
        Assert.Equal(2, session.Entities.Count);

        // Stale tick 1 is ignored
        net.Push(new Snapshot { Tick = 1, Entities = { } });
        Assert.Equal(2, session.Entities.Count);
        Assert.Empty(despawned);

        // Tick 4: player 10 moves
        net.Push(new Snapshot
        {
            Tick = 4,
            LastInputSeq = 0,
            Entities =
            {
                new EntityState { EntityId = 10, Type = EntityType.EntityPlayer, PlayerUid = 1001, ConfigId = 1, PosX = 5f, PosY = 12f, Hp = 100, State = EntityAnimState.AnimMoving },
                new EntityState { EntityId = 11, Type = EntityType.EntityPlayer, PlayerUid = 1002, ConfigId = 1, PosX = 4f, PosY = 8f, Hp = 100, State = EntityAnimState.AnimIdle },
            }
        });
        Assert.Equal(2, updated.Count);
        Assert.Equal(5f, session.Entities[10].PosX);

        // Player 1002 disconnects -> PlayerLeft despawns entity 11 immediately
        net.Push(new PlayerLeft { Uid = 1002, Reason = LeftReason.LeftDisconnect });
        Assert.Single(leftUids);
        Assert.Equal(1002ul, leftUids[0]);
        Assert.Single(despawned);
        Assert.Equal((11u, 1002ul), despawned[0]);
        Assert.Single(session.Entities);

        // Tick 6 with empty entities despawns remaining entity 10
        net.Push(new Snapshot { Tick = 6 });
        Assert.Empty(session.Entities);
        Assert.Equal(2, despawned.Count);
        Assert.Equal((10u, 1001ul), despawned[1]);
    }

    [Fact]
    public void BattleSession_RoomClosedAndDisconnect_TriggerSessionEnded()
    {
        var net = new FakeNetwork { Uid = 1001 };
        using var session = new BattleSessionClient(net);
        var endedReasons = new List<string>();
        session.SessionEnded += r => endedReasons.Add(r);

        net.Push(new RoomStatePush { RoomId = 1, State = RoomSessionState.RoomClosed });
        net.Disconnect("超时");

        Assert.Equal(2, endedReasons.Count);
    }
}
