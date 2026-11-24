#nullable enable
using System;
using System.Collections.Generic;
using Godot;
using Google.Protobuf;
using SepCore.Battle;
using SepCore.Definition;
using SepCore.Gameplay.Player;
using SepCore.Procedure;
using SepCore.Runtime.Network;
using Xunit;

namespace SepCore.Tests;

public class BattleP1FixTests
{
    private sealed class FakeNetwork : INetworkMessageClient
    {
        public NetworkState State { get; set; } = NetworkState.Connected;
        public ulong Uid { get; set; } = 1001;
        public event Action<string>? Disconnected;
        private readonly Dictionary<Type, List<Delegate>> _handlers = new();

        public void Send<T>(T message) where T : IMessage { }

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

    // ---------------------------------------------------------------- P1-6: BattleTransitionGuard

    [Fact]
    public void BattleTransitionGuard_RoomClosedDuringTransition_AbortsAndClearsContext()
    {
        var net = new FakeNetwork();
        var ctx = new ProcedureContext();
        using var guard = new BattleTransitionGuard(net, ctx);

        ctx.PendingGameStart = new GameStart { MapIds = { 1u } };
        net.Push(new RoomTransition { MapId = 1u });
        net.Push(new RoomStatePush { RoomId = 42u, State = RoomSessionState.RoomClosed });

        Assert.True(ctx.PendingRoomClosed);
        Assert.NotNull(ctx.PendingRoomTransition);

        Assert.True(guard.TryConsumeAbortReason(out var reason));
        Assert.Equal("房间已关闭", reason);
        Assert.Null(ctx.PendingGameStart);
        Assert.Null(ctx.PendingRoomTransition);
        Assert.False(ctx.PendingRoomClosed);
        Assert.Null(ctx.PendingTransitionDisconnectReason);
    }

    [Fact]
    public void BattleTransitionGuard_DisconnectDuringTransition_AbortsAndClearsContext()
    {
        var net = new FakeNetwork();
        var ctx = new ProcedureContext();
        using var guard = new BattleTransitionGuard(net, ctx);

        ctx.PendingGameStart = new GameStart { MapIds = { 1u } };
        net.Push(new RoomTransition { MapId = 1u });
        net.Disconnect("heartbeat timeout");

        Assert.True(guard.TryConsumeAbortReason(out var reason));
        Assert.Contains("heartbeat timeout", reason);
        Assert.Null(ctx.PendingGameStart);
        Assert.Null(ctx.PendingRoomTransition);
        Assert.False(ctx.PendingRoomClosed);
        Assert.Null(ctx.PendingTransitionDisconnectReason);
    }

    [Fact]
    public void BattleTransitionGuard_DisconnectedStateWithoutTransitionEvent_Aborts()
    {
        var net = new FakeNetwork { State = NetworkState.Disconnected };
        var ctx = new ProcedureContext { PendingGameStart = new GameStart { MapIds = { 1u } } };
        using var guard = new BattleTransitionGuard(net, ctx);

        Assert.True(guard.TryConsumeAbortReason(out var reason));
        Assert.Equal("连接已断开", reason);
        Assert.Null(ctx.PendingGameStart);
    }

    [Fact]
    public void BattleTransitionGuard_LobbyRoomClosedBeforeGameStart_DoesNotAbortNextBattle()
    {
        var net = new FakeNetwork();
        var ctx = new ProcedureContext();
        using var guard = new BattleTransitionGuard(net, ctx);

        // ROOM_CLOSED while still in lobby (PendingGameStart == null) must not poison a later match
        net.Push(new RoomStatePush { RoomId = 1u, State = RoomSessionState.RoomClosed });
        Assert.False(ctx.PendingRoomClosed);

        ctx.PendingGameStart = new GameStart { MapIds = { 1u } };
        net.Push(new RoomTransition { MapId = 1u });

        Assert.False(guard.TryConsumeAbortReason(out var reason));
        Assert.Equal(string.Empty, reason);
        Assert.NotNull(ctx.PendingGameStart);
        Assert.NotNull(ctx.PendingRoomTransition);
    }

    [Fact]
    public void BattleTransitionGuard_IgnoresEarlyEventsWhileBattleActive()
    {
        var net = new FakeNetwork();
        var ctx = new ProcedureContext();
        using var guard = new BattleTransitionGuard(net, ctx) { IsBattleActive = true };

        ctx.PendingGameStart = new GameStart { MapIds = { 1u } };
        net.Push(new RoomTransition { MapId = 2u });
        net.Push(new RoomStatePush { RoomId = 1u, State = RoomSessionState.RoomClosed });

        Assert.Null(ctx.PendingRoomTransition);
        Assert.False(ctx.PendingRoomClosed);
    }

    // ---------------------------------------------------------------- P1-7: Analog Move Sanitize

    [Fact]
    public void PlayerMotor_SanitizeMoveInput_PreservesAnalogBelowOne_AndClampsAboveOne()
    {
        // Analog half-stick magnitude (0 < len <= 1) is preserved
        var half = PlayerMotor.SanitizeMoveInput(new Vector2(0.3f, 0.4f));
        Assert.Equal(0.3f, half.X, 5);
        Assert.Equal(0.4f, half.Y, 5);

        var axis = PlayerMotor.SanitizeMoveInput(new Vector2(0.5f, 0f));
        Assert.Equal(0.5f, axis.X, 5);
        Assert.Equal(0f, axis.Y, 5);

        var unit = PlayerMotor.SanitizeMoveInput(new Vector2(0f, -1f));
        Assert.Equal(0f, unit.X, 5);
        Assert.Equal(-1f, unit.Y, 5);

        // Magnitude > 1 is normalized to unit length
        var over = PlayerMotor.SanitizeMoveInput(new Vector2(3f, 4f));
        Assert.Equal(0.6f, over.X, 5);
        Assert.Equal(0.8f, over.Y, 5);

        var diag = PlayerMotor.SanitizeMoveInput(new Vector2(1f, 1f));
        var invSqrt2 = 1f / MathF.Sqrt(2f);
        Assert.Equal(invSqrt2, diag.X, 5);
        Assert.Equal(invSqrt2, diag.Y, 5);

        // Sub-epsilon and non-finite inputs match server sanitizeMove
        Assert.Equal(Vector2.Zero, PlayerMotor.SanitizeMoveInput(new Vector2(1e-7f, 0f)));
        var partialNan = PlayerMotor.SanitizeMoveInput(new Vector2(float.NaN, 0.5f));
        Assert.Equal(0f, partialNan.X, 5);
        Assert.Equal(0.5f, partialNan.Y, 5);
        Assert.Equal(Vector2.Zero, PlayerMotor.SanitizeMoveInput(new Vector2(float.PositiveInfinity, float.NegativeInfinity)));
    }

    // ---------------------------------------------------------------- P1-7: InputBatchTracker.PendingInputs

    [Fact]
    public void InputBatchTracker_PendingInputs_ExposesUnackedQueueInOrder()
    {
        var tracker = new InputBatchTracker();
        Assert.Empty(tracker.PendingInputs);

        tracker.Record(0.5f, 0f, 0f, 0u);
        tracker.Record(0f, 0.5f, 90f, 1u);
        tracker.Record(1f, 0f, 180f, 0u);

        Assert.Equal(3, tracker.PendingInputs.Count);
        Assert.Equal(1u, tracker.PendingInputs[0].Seq);
        Assert.Equal(0.5f, tracker.PendingInputs[0].MoveX, 4);
        Assert.Equal(2u, tracker.PendingInputs[1].Seq);
        Assert.Equal(3u, tracker.PendingInputs[2].Seq);

        tracker.Acknowledge(2u);
        Assert.Single(tracker.PendingInputs);
        Assert.Equal(3u, tracker.PendingInputs[0].Seq);

        tracker.Reset();
        Assert.Empty(tracker.PendingInputs);
    }

    // ---------------------------------------------------------------- P1-7: BattleCollision parity

    [Fact]
    public void BattleCollision_ResolveCircleAabb_NoOverlap_ReturnsUnchanged()
    {
        var wall = new BattleWallAabb(10f, 9f, 11f, 15f);
        var pos = new BattleVec2(8f, 12f);

        var (resolved, hit) = BattleCollision.ResolveCircleAabb(pos, BattleCollision.DefaultPlayerRadius, wall);
        Assert.False(hit);
        Assert.Equal(pos, resolved);
    }

    [Fact]
    public void BattleCollision_ResolveCircleAabb_ExternalEdgeAndCornerOverlap_PushesOutCleanly()
    {
        const float r = BattleCollision.DefaultPlayerRadius; // 0.15625
        var wall = new BattleWallAabb(10f, 9f, 11f, 15f);

        // Edge overlap on left face
        var (edgePos, edgeHit) = BattleCollision.ResolveCircleAabb(new BattleVec2(9.95f, 12f), r, wall);
        Assert.True(edgeHit);
        Assert.Equal(10f - r, edgePos.X, 5);
        Assert.Equal(12f, edgePos.Y, 5);

        // Corner overlap near bottom-left (10, 9)
        var (cornerPos, cornerHit) = BattleCollision.ResolveCircleAabb(new BattleVec2(9.95f, 8.95f), r, wall);
        Assert.True(cornerHit);
        var dx = cornerPos.X - 10f;
        var dy = cornerPos.Y - 9f;
        Assert.Equal(r, MathF.Sqrt(dx * dx + dy * dy), 5);
        Assert.True(cornerPos.X < 10f && cornerPos.Y < 9f);
    }

    [Fact]
    public void BattleCollision_ResolveCircleAabb_CenterInsideAabb_PushesOutShallowestEdge()
    {
        const float r = BattleCollision.DefaultPlayerRadius;
        var wall = new BattleWallAabb(10f, 9f, 11f, 15f);

        // Closest to left edge
        var (left, hitL) = BattleCollision.ResolveCircleAabb(new BattleVec2(10.1f, 12f), r, wall);
        Assert.True(hitL);
        Assert.Equal(10f - r, left.X, 5);
        Assert.Equal(12f, left.Y, 5);

        // Closest to right edge
        var (right, hitR) = BattleCollision.ResolveCircleAabb(new BattleVec2(10.9f, 12f), r, wall);
        Assert.True(hitR);
        Assert.Equal(11f + r, right.X, 5);
        Assert.Equal(12f, right.Y, 5);

        // Closest to bottom edge
        var (bottom, hitB) = BattleCollision.ResolveCircleAabb(new BattleVec2(10.5f, 9.1f), r, wall);
        Assert.True(hitB);
        Assert.Equal(10.5f, bottom.X, 5);
        Assert.Equal(9f - r, bottom.Y, 5);

        // Closest to top edge
        var (top, hitT) = BattleCollision.ResolveCircleAabb(new BattleVec2(10.5f, 14.9f), r, wall);
        Assert.True(hitT);
        Assert.Equal(10.5f, top.X, 5);
        Assert.Equal(15f + r, top.Y, 5);
    }

    [Fact]
    public void BattleCollision_ResolveCollisions_CornerSlidingAndMap1Bounds()
    {
        const float r = BattleCollision.DefaultPlayerRadius;
        var map = BattleMapCatalog.Map1;

        // Corner squeeze against bottom + left perimeter walls [0,0,30,1] & [0,0,1,20]
        var corner = BattleCollision.ResolveCollisions(new BattleVec2(0.95f, 0.95f), r, map);
        Assert.Equal(1f + r, corner.X, 5);
        Assert.Equal(1f + r, corner.Y, 5);

        // Multi-iteration sliding in an L-shaped interior corner
        var lWalls = new[]
        {
            new BattleWallAabb(5f, 0f, 6f, 10f),  // vertical wall
            new BattleWallAabb(0f, 5f, 10f, 6f),  // horizontal wall
        };
        var squeezed = BattleCollision.ResolveCollisions(new BattleVec2(4.95f, 4.95f), r, 20f, 20f, lWalls);
        Assert.Equal(5f - r, squeezed.X, 5);
        Assert.Equal(5f - r, squeezed.Y, 5);

        // Diagonal slide along Map 1 interior Wall 1 [10, 9, 11, 15]
        var slid = BattleCollision.ResolveCollisions(new BattleVec2(9.95f, 12.1f), r, map);
        Assert.Equal(10f - r, slid.X, 5);
        Assert.Equal(12.1f, slid.Y, 5);

        // Approaching Map 1 perimeter from playable area pushes back to inner boundary [1 + r, 19 - r]
        var nearPerimeter = BattleCollision.ResolveCollisions(new BattleVec2(0.9f, 19.1f), r, map);
        Assert.Equal(1f + r, nearPerimeter.X, 5);
        Assert.Equal(19f - r, nearPerimeter.Y, 5);

        // Out-of-bounds coordinates are clamped inside [r, width - r] x [r, height - r]
        var clamped = BattleCollision.ClampToRoomBounds(new BattleVec2(-50f, 100f), r, map.WidthTiles, map.HeightTiles);
        Assert.Equal(r, clamped.X, 5);
        Assert.Equal(map.HeightTiles - r, clamped.Y, 5);
    }

    // ---------------------------------------------------------------- P1-8: BattleMapLoader idempotency

    [Fact]
    public void BattleMapLoader_SkipsDuplicateMapId_AndClearsBeforeRebuildOnNewMap()
    {
        var loader = new BattleMapLoader();
        var events = new List<string>();
        var clearCount = 0;
        var addedWalls = new List<Rect2>();

        // mapId == 0 is ignored
        Assert.False(loader.TryLoad(0u, () => clearCount++, r => addedWalls.Add(r)));
        Assert.Equal(0u, loader.CurrentMapId);
        Assert.Equal(0, clearCount);

        // First LoadMap(1) builds Map 1 once
        Assert.True(loader.TryLoad(
            1u,
            () =>
            {
                clearCount++;
                events.Add("clear");
            },
            r =>
            {
                addedWalls.Add(r);
                events.Add("add");
            }));
        Assert.Equal(1u, loader.CurrentMapId);
        Assert.Equal(1, clearCount);
        Assert.Equal(BattleMapCatalog.Map1.Walls.Count, addedWalls.Count);
        Assert.Equal(BattleMapCatalog.Map1.Walls.Count, loader.WallRects.Count);
        Assert.Equal("clear", events[0]);

        // Duplicate LoadMap(1) is a no-op (no duplicate colliders, no clear/rebuild)
        Assert.False(loader.TryLoad(1u, () => clearCount++, r => addedWalls.Add(r)));
        Assert.Equal(1, clearCount);
        Assert.Equal(BattleMapCatalog.Map1.Walls.Count, addedWalls.Count);

        // Switching to Map 2 clears old walls before adding new walls
        events.Clear();
        Assert.True(loader.TryLoad(
            2u,
            () =>
            {
                clearCount++;
                events.Add("clear");
            },
            r =>
            {
                addedWalls.Add(r);
                events.Add("add");
            }));
        Assert.Equal(2u, loader.CurrentMapId);
        Assert.Equal(2, clearCount);
        Assert.Equal("clear", events[0]);
        Assert.Equal("add", events[1]);
    }
}
