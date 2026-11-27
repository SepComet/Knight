#nullable enable
using System;
using System.Collections.Generic;
using Google.Protobuf;
using SepCore.Battle;
using SepCore.Definition;
using SepCore.Runtime.Network;
using Xunit;

namespace SepCore.Tests;

public sealed class NetSyncTests
{
    private static BattleMapDefinition Map1 => BattleMapCatalog.Map1;

    // ---------------------------------------------------------------- LocalPlayerPredictor

    [Fact]
    public void Predictor_ZeroError_MatchesServerStepWithoutReconciliation()
    {
        var predictor = new LocalPlayerPredictor();
        var tracker = new InputBatchTracker();
        var spawn = new BattleVec2(4f, 12f);
        predictor.Reset(spawn, facingDegrees: 0f, alive: true);

        // Simulate 5 local inputs at 30Hz moving +X
        var serverPos = spawn;
        for (var i = 0; i < 5; i++)
        {
            var cmd = tracker.Record(1f, 0f, 0f, 0u);
            predictor.ApplyLocalInput(cmd, Map1);
            if (i < 3)
            {
                serverPos = LocalPlayerPredictor.StepMove(
                    serverPos, cmd.MoveX, cmd.MoveY,
                    predictor.Config.MoveSpeedTiles,
                    predictor.Config.StepSeconds,
                    predictor.Config.PlayerRadius,
                    Map1, out _);
            }
        }

        // Server acknowledges seq=3 while seq 4, 5 are still in-flight
        tracker.Acknowledge(3u);
        var result = predictor.Reconcile(
            serverPos,
            authFacingDegrees: 0f,
            authMoving: true,
            authAlive: true,
            lastInputSeq: 3u,
            unackedInputs: tracker.PendingInputs,
            map: Map1);

        Assert.False(result.Reconciled);
        Assert.False(result.HardSnapped);
        Assert.True(result.PositionError < 1e-4f);
        Assert.Equal(4f + 5f * 3f * (1f / 30f), predictor.PredictedPosition.X, 4);
        Assert.Equal(0f, predictor.VisualErrorOffset.X, 5);
        Assert.Equal(0f, predictor.VisualErrorOffset.Y, 5);
    }

    [Fact]
    public void Predictor_ModerateDiscrepancy_RollsBackReplaysAndSmoothsVisualOffset()
    {
        var predictor = new LocalPlayerPredictor();
        var tracker = new InputBatchTracker();
        var spawn = new BattleVec2(4f, 12f);
        predictor.Reset(spawn, facingDegrees: 0f, alive: true);

        for (var i = 0; i < 4; i++)
        {
            var cmd = tracker.Record(1f, 0f, 15f, 0u);
            predictor.ApplyLocalInput(cmd, Map1);
        }

        var predictedBefore = predictor.RenderPosition;

        // Server says at seq=2 player was actually pushed back by 0.25 tiles (between ReconcileThreshold and SnapThreshold)
        tracker.Acknowledge(2u);
        var authAtSeq2 = new BattleVec2(4f + 2f * 0.1f - 0.25f, 12f);

        var result = predictor.Reconcile(
            authAtSeq2,
            authFacingDegrees: 15f,
            authMoving: true,
            authAlive: true,
            lastInputSeq: 2u,
            unackedInputs: tracker.PendingInputs,
            map: Map1);

        Assert.True(result.Reconciled);
        Assert.False(result.HardSnapped);
        Assert.Equal(2, result.ReplayedInputs);
        Assert.InRange(result.PositionError, 0.24f, 0.26f);

        // PredictedPosition should be authAtSeq2 + 2 replayed steps (+0.2 tiles)
        Assert.Equal(authAtSeq2.X + 0.2f, predictor.PredictedPosition.X, 4);
        // Immediately after soft reconcile, RenderPosition preserves continuity with prevRenderPos
        Assert.Equal(predictedBefore.X, predictor.RenderPosition.X, 4);
        Assert.True(MathF.Abs(predictor.VisualErrorOffset.X) > 0.2f);

        // DecayVisualError smoothly shrinks visual error toward zero
        var initialOffset = MathF.Abs(predictor.VisualErrorOffset.X);
        predictor.DecayVisualError(0.05f);
        var afterOneDecay = MathF.Abs(predictor.VisualErrorOffset.X);
        Assert.True(afterOneDecay < initialOffset);

        for (var i = 0; i < 40; i++)
            predictor.DecayVisualError(0.05f);
        Assert.Equal(0f, predictor.VisualErrorOffset.X, 5);
        Assert.Equal(0f, predictor.VisualErrorOffset.Y, 5);
    }

    [Fact]
    public void Predictor_LargeDiscrepancy_HardSnapsWithoutVisualOffset()
    {
        var predictor = new LocalPlayerPredictor();
        var tracker = new InputBatchTracker();
        predictor.Reset(new BattleVec2(4f, 12f));

        var c1 = tracker.Record(1f, 0f, 0f, 0u);
        var c2 = tracker.Record(1f, 0f, 0f, 0u);
        predictor.ApplyLocalInput(c1, Map1);
        predictor.ApplyLocalInput(c2, Map1);

        tracker.Acknowledge(1u);
        // Server teleports player 5 tiles away (> SnapThreshold = 1.5)
        var teleportPos = new BattleVec2(14f, 12f);
        var result = predictor.Reconcile(
            teleportPos, 0f, true, true, 1u, tracker.PendingInputs, Map1);

        Assert.True(result.Reconciled);
        Assert.True(result.HardSnapped);
        Assert.Equal(1, result.ReplayedInputs);
        Assert.Equal(0f, predictor.VisualErrorOffset.X, 5);
        Assert.Equal(0f, predictor.VisualErrorOffset.Y, 5);
        Assert.Equal(teleportPos.X + 0.1f, predictor.PredictedPosition.X, 4);
        Assert.Equal(predictor.PredictedPosition, predictor.RenderPosition);
    }

    [Fact]
    public void Predictor_Disabled_DegradesToPureAuthoritativeSnapshots()
    {
        var predictor = new LocalPlayerPredictor { Enabled = false };
        var tracker = new InputBatchTracker();
        var spawn = new BattleVec2(4f, 12f);
        predictor.Reset(spawn);

        var c1 = tracker.Record(1f, 0f, 45f, 0u);
        predictor.ApplyLocalInput(c1, Map1);

        // Local input does not move player while prediction is off
        Assert.Equal(spawn, predictor.PredictedPosition);
        Assert.Equal(spawn, predictor.RenderPosition);

        // Authoritative snapshot snaps position directly
        tracker.Acknowledge(1u);
        var authPos = new BattleVec2(4.1f, 12f);
        var result = predictor.Reconcile(authPos, 45f, true, true, 1u, tracker.PendingInputs, Map1);

        Assert.False(result.Reconciled);
        Assert.Equal(authPos, predictor.PredictedPosition);
        Assert.Equal(authPos, predictor.RenderPosition);
        Assert.Equal(45f, predictor.FacingDegrees, 3);
    }

    [Fact]
    public void Predictor_WallCollision_MatchesBattleCollisionAndSlides()
    {
        var predictor = new LocalPlayerPredictor();
        var tracker = new InputBatchTracker();
        // Start just left of Wall 1 [10, 9, 11, 15]
        predictor.Reset(new BattleVec2(9.7f, 12f));

        // Walk diagonally (+X, +Y) into Wall 1 for 10 ticks
        for (var i = 0; i < 10; i++)
        {
            var cmd = tracker.Record(1f, 1f, 45f, 0u);
            predictor.ApplyLocalInput(cmd, Map1);
        }

        // Must not penetrate Wall 1 (MinX = 10, player radius = 5/32 = 0.15625)
        var maxAllowedX = 10f - BattleCollision.DefaultPlayerRadius;
        Assert.True(predictor.PredictedPosition.X <= maxAllowedX + 1e-4f);
        // Should have slid upward along +Y
        Assert.True(predictor.PredictedPosition.Y > 12.4f);
    }

    // ---------------------------------------------------------------- RemoteEntityInterpolator

    [Fact]
    public void Interpolator_NormalInterpolation_SmoothlyLerpsPositionAndShortestAngle()
    {
        var interp = new RemoteEntityInterpolator(interpolationDelaySeconds: 0.10);
        interp.PushSample(new EntitySnapshotSample(1.00, new BattleVec2(2f, 4f), 170f, true, true));
        interp.PushSample(new EntitySnapshotSample(1.10, new BattleVec2(4f, 8f), -170f, true, true));

        // At now = 1.15, renderTime = 1.05 (exact midpoint t = 0.5)
        Assert.True(interp.TrySample(1.15, out var mid));
        Assert.Equal(3f, mid.Position.X, 4);
        Assert.Equal(6f, mid.Position.Y, 4);
        // Shortest path from 170 deg to -170 deg (+20 deg wrap) -> midpoint is 180 deg
        Assert.Equal(180f, mid.FacingDegrees, 2);
        Assert.True(mid.Moving);
        Assert.True(mid.Alive);
    }

    [Fact]
    public void Interpolator_OutOfOrderAndExpiredSamples_IgnoredAndPruned()
    {
        var interp = new RemoteEntityInterpolator(interpolationDelaySeconds: 0.10);
        interp.PushSample(new EntitySnapshotSample(1.00, new BattleVec2(1f, 0f), 0f, true, true));
        interp.PushSample(new EntitySnapshotSample(1.10, new BattleVec2(2f, 0f), 0f, true, true));
        // Out-of-order older sample should be ignored
        interp.PushSample(new EntitySnapshotSample(1.05, new BattleVec2(99f, 99f), 0f, true, true));
        interp.PushSample(new EntitySnapshotSample(1.20, new BattleVec2(3f, 0f), 0f, true, true));
        interp.PushSample(new EntitySnapshotSample(1.30, new BattleVec2(4f, 0f), 0f, true, true));

        Assert.Equal(4, interp.BufferedCount);

        // At now = 1.35, renderTime = 1.25 -> samples at 1.00 and 1.10 are expired and pruned
        Assert.True(interp.TrySample(1.35, out var state));
        Assert.Equal(3.5f, state.Position.X, 4);
        Assert.Equal(2, interp.BufferedCount);
    }

    [Fact]
    public void Interpolator_Disabled_ReturnsLatestSnapshotDirectly()
    {
        var interp = new RemoteEntityInterpolator(interpolationDelaySeconds: 0.10) { Enabled = false };
        interp.PushSample(new EntitySnapshotSample(1.00, new BattleVec2(1f, 1f), 0f, true, true));
        interp.PushSample(new EntitySnapshotSample(1.10, new BattleVec2(5f, 7f), 90f, false, true));

        Assert.True(interp.TrySample(1.12, out var state));
        Assert.Equal(5f, state.Position.X, 4);
        Assert.Equal(7f, state.Position.Y, 4);
        Assert.Equal(90f, state.FacingDegrees, 3);
        Assert.False(state.Moving);
    }

    [Fact]
    public void Interpolator_BufferUnderrun_ClampsExtrapolationAndNeverNaN()
    {
        var interp = new RemoteEntityInterpolator(
            interpolationDelaySeconds: 0.10,
            maxExtrapolationSeconds: 0.05);
        interp.PushSample(new EntitySnapshotSample(1.00, new BattleVec2(0f, 0f), 0f, true, true));
        interp.PushSample(new EntitySnapshotSample(1.10, new BattleVec2(1f, 0f), 0f, true, true)); // vx = 10 tiles/s

        // At now = 2.00, renderTime = 1.90 (0.80s past latest -> clamped to 0.05s => +0.5 tiles)
        Assert.True(interp.TrySample(2.00, out var state));
        Assert.True(float.IsFinite(state.Position.X));
        Assert.True(float.IsFinite(state.Position.Y));
        Assert.Equal(1.5f, state.Position.X, 4);
    }

    [Theory]
    [InlineData(350f, 10f, 0.5f, 360f)]
    [InlineData(10f, 350f, 0.5f, 0f)]
    [InlineData(-170f, 170f, 0.5f, -180f)]
    [InlineData(0f, 90f, 0.25f, 22.5f)]
    public void Interpolator_LerpAngleDegrees_AlwaysTakesShortestArc(float from, float to, float t, float expected)
    {
        var actual = RemoteEntityInterpolator.LerpAngleDegrees(from, to, t);
        Assert.Equal(expected, actual, 2);
    }

    // ---------------------------------------------------------------- SyncMetricsTracker

    [Fact]
    public void SyncMetricsTracker_ComputesWindowedRateAndAverageError_AndPrunesOldSamples()
    {
        var metrics = new SyncMetricsTracker(windowSeconds: 1.0);

        metrics.RecordSample(1.0, 0.10f, reconciled: true);
        metrics.RecordSample(1.2, 0.20f, reconciled: false);
        metrics.RecordSample(1.5, 0.30f, reconciled: true);

        Assert.Equal(3, metrics.SampleCount);
        Assert.Equal(2f, metrics.ReconciliationsPerSecond, 3);
        Assert.Equal(0.20f, metrics.AveragePositionError, 3);
        Assert.Equal(2, metrics.TotalReconciliations);

        // Advance time to 2.3 -> samples at 1.0 and 1.2 expire, only 1.5 remains
        metrics.Prune(2.3);
        Assert.Equal(1, metrics.SampleCount);
        Assert.Equal(1f, metrics.ReconciliationsPerSecond, 3);
        Assert.Equal(0.30f, metrics.AveragePositionError, 3);

        // Advance past all samples
        metrics.Prune(4.0);
        Assert.Equal(0, metrics.SampleCount);
        Assert.Equal(0f, metrics.ReconciliationsPerSecond, 3);
        Assert.Equal(0f, metrics.AveragePositionError, 3);
    }

    // ---------------------------------------------------------------- KcpLossRateEstimator

    [Fact]
    public void KcpLossRateEstimator_ComputesWindowedRetransmitRatio_AndRecoversAfterLossStops()
    {
        var estimator = new KcpLossRateEstimator(windowSeconds: 5.0);

        Assert.Equal(0f, estimator.Sample(0.0, segmentsSent: 100, retransmits: 0), 4);
        // 90 new segments + 10 retransmits => 10 / 100 = 0.10
        Assert.Equal(0.10f, estimator.Sample(2.0, segmentsSent: 190, retransmits: 10), 4);

        // After 5s window passes with 100 new segments and 0 new retransmits => drops back to 0
        Assert.Equal(0f, estimator.Sample(8.0, segmentsSent: 290, retransmits: 10), 4);

        // Counter reset (e.g. reconnect) does not underflow or throw
        Assert.Equal(0f, estimator.Sample(9.0, segmentsSent: 5, retransmits: 0), 4);
    }

    // ---------------------------------------------------------------- P1 Regression Tests

    [Fact]
    public void Predictor_OffToOnToggle_RebuildFromUnackedPreventsSpuriousRollback()
    {
        var predictor = new LocalPlayerPredictor { Enabled = false };
        var tracker = new InputBatchTracker();
        var spawn = new BattleVec2(4f, 12f);
        predictor.Reset(spawn, facingDegrees: 0f, alive: true);

        // Prediction OFF: 3 in-flight inputs sent while server has not acked them yet
        var serverPos = spawn;
        for (var i = 0; i < 3; i++)
        {
            var cmd = tracker.Record(1f, 0f, 0f, 0u);
            predictor.ApplyLocalInput(cmd, Map1);
            serverPos = LocalPlayerPredictor.StepMove(
                serverPos, cmd.MoveX, cmd.MoveY,
                predictor.Config.MoveSpeedTiles,
                predictor.Config.StepSeconds,
                predictor.Config.PlayerRadius,
                Map1, out _);
        }

        // Prediction switched back ON -> immediately rebuild from unacked inputs
        predictor.Enabled = true;
        predictor.RebuildFromUnacked(tracker.PendingInputs, Map1);

        Assert.Equal(3, predictor.CachedPredictionCount);
        Assert.Equal(serverPos.X, predictor.PredictedPosition.X, 4);

        // Next input seq=4 arrives locally, then server acknowledges seq=3 at exact serverPos
        var cmd4 = tracker.Record(1f, 0f, 0f, 0u);
        predictor.ApplyLocalInput(cmd4, Map1);

        tracker.Acknowledge(3u);
        var result = predictor.Reconcile(
            serverPos,
            authFacingDegrees: 0f,
            authMoving: true,
            authAlive: true,
            lastInputSeq: 3u,
            unackedInputs: tracker.PendingInputs,
            map: Map1);

        Assert.False(result.Reconciled);
        Assert.True(result.PositionError < 1e-4f);
        Assert.Equal(4f + 4f * 0.1f, predictor.PredictedPosition.X, 4);
    }

    [Fact]
    public void Predictor_After120HistoryTruncation_StaleLastInputSeqIsIgnoredWithoutFalseRollback()
    {
        var predictor = new LocalPlayerPredictor();
        var tracker = new InputBatchTracker();
        var spawn = new BattleVec2(2f, 12f);
        predictor.Reset(spawn, facingDegrees: 0f, alive: true);

        // Send 130 inputs without any server ack -> history truncates seq 1..10 (_baseSeq = 10)
        var posAtSeq5 = spawn;
        for (var i = 1; i <= 130; i++)
        {
            var cmd = tracker.Record(1f, 0f, 0f, 0u);
            predictor.ApplyLocalInput(cmd, Map1);
            if (i <= 5)
            {
                posAtSeq5 = LocalPlayerPredictor.StepMove(
                    posAtSeq5, cmd.MoveX, cmd.MoveY,
                    predictor.Config.MoveSpeedTiles,
                    predictor.Config.StepSeconds,
                    predictor.Config.PlayerRadius,
                    Map1, out _);
            }
        }

        Assert.Equal(InputBatchTracker.MaxBatchSize, predictor.CachedPredictionCount);
        var predictedBefore = predictor.PredictedPosition;

        // Delayed snapshot with lastInputSeq=5 (< _baseSeq=10) arrives -> must be ignored
        var result = predictor.Reconcile(
            posAtSeq5,
            authFacingDegrees: 0f,
            authMoving: true,
            authAlive: true,
            lastInputSeq: 5u,
            unackedInputs: tracker.PendingInputs,
            map: Map1);

        Assert.False(result.Reconciled);
        Assert.Equal(0f, result.PositionError, 5);
        Assert.Equal(predictedBefore, predictor.PredictedPosition);
        Assert.Equal(InputBatchTracker.MaxBatchSize, predictor.CachedPredictionCount);
    }

    [Fact]
    public void BattleSessionClient_OnRoomTransition_ClearsPendingInputsAndPreservesMonotonicNextSeq()
    {
        var net = new StubNetwork();
        using var session = new BattleSessionClient(net);
        session.Start(
            new GameStart { MapIds = { 1u, 2u } },
            new RoomTransition { MapId = 1u });
        Assert.True(session.ConfirmRoomLoaded(1u));

        // Sample 3 unacked inputs in room 1 (seq 1, 2, 3)
        Assert.NotNull(session.SampleAndSendInput(1f, 0f, 0f, 0u));
        Assert.NotNull(session.SampleAndSendInput(1f, 0f, 0f, 0u));
        Assert.NotNull(session.SampleAndSendInput(1f, 0f, 0f, 0u));
        Assert.Equal(3, session.PendingInputCount);
        Assert.Equal(4u, session.NextInputSeq);

        // Transition to room 2 -> pending inputs cleared, NextInputSeq stays 4
        net.Push(new RoomTransition { MapId = 2u });
        Assert.Equal(0, session.PendingInputCount);
        Assert.Empty(session.PendingInputs);
        Assert.Equal(4u, session.NextInputSeq);

        Assert.True(session.ConfirmRoomLoaded(2u));
        var nextCmd = session.SampleAndSendInput(0f, 1f, 90f, 0u);
        Assert.NotNull(nextCmd);
        Assert.Equal(4u, nextCmd!.Seq);
        Assert.Equal(1, session.PendingInputCount);
        Assert.Equal(5u, session.NextInputSeq);
    }

    [Fact]
    public void Interpolator_DefaultConfig_BufferUnderrun_SnapsToLatestWithoutExtrapolation()
    {
        var interp = new RemoteEntityInterpolator();
        Assert.Equal(0.0, interp.MaxExtrapolationSeconds);

        interp.PushSample(new EntitySnapshotSample(1.00, new BattleVec2(0f, 0f), 0f, true, true));
        interp.PushSample(new EntitySnapshotSample(1.10, new BattleVec2(1f, 0f), 0f, true, true));

        // At now = 2.00, renderTime = 1.90 (far past latest=1.10) -> must snap to latest (1, 0), never extrapolate
        Assert.True(interp.TrySample(2.00, out var state));
        Assert.Equal(1f, state.Position.X, 4);
        Assert.Equal(0f, state.Position.Y, 4);
    }

    [Fact]
    public void KcpLossRateEstimator_HighFrequencyAndSameTimestamp_DoesNotGrowQueueUnboundedly()
    {
        var estimator = new KcpLossRateEstimator(windowSeconds: 5.0, minSampleIntervalSeconds: 0.1);

        // 100 calls at the exact same timestamp -> stays at 1 sample
        for (var i = 0; i < 100; i++)
            estimator.Sample(1.0, segmentsSent: (uint)(10 + i), retransmits: 0);
        Assert.Equal(1, estimator.SampleCount);

        // 60Hz sampling over 5 seconds (300 calls) -> throttled to <= ~55 samples
        for (var frame = 1; frame <= 300; frame++)
        {
            var t = 1.0 + frame / 60.0;
            estimator.Sample(t, segmentsSent: (uint)(110 + frame * 2), retransmits: (uint)(frame / 10));
        }

        Assert.InRange(estimator.SampleCount, 2, 60);
        Assert.InRange(estimator.CurrentLossRate, 0.01f, 0.20f);
    }

    [Fact]
    public void Predictor_DeathAndReviveTransition_HaltsPredictionAndResetsCleanly()
    {
        var predictor = new LocalPlayerPredictor();
        var tracker = new InputBatchTracker();
        var metrics = new SyncMetricsTracker(windowSeconds: 1.0);
        var spawn = new BattleVec2(4f, 12f);
        predictor.Reset(spawn, facingDegrees: 0f, alive: true);

        var c1 = tracker.Record(1f, 0f, 0f, 0u);
        predictor.ApplyLocalInput(c1, Map1);
        Assert.True(predictor.PredictedPosition.X > spawn.X);

        // Authoritative death snapshot at seq=1
        tracker.Acknowledge(1u);
        var deathPos = new BattleVec2(4.1f, 12f);
        var deathResult = predictor.Reconcile(deathPos, 0f, false, authAlive: false, 1u, tracker.PendingInputs, Map1);
        metrics.RecordSample(1.0, deathResult.PositionError, deathResult.Reconciled);

        Assert.False(predictor.Alive);
        Assert.False(predictor.IsMoving);
        Assert.False(deathResult.Reconciled);
        Assert.Equal(0f, deathResult.PositionError, 5);
        Assert.Equal(0, predictor.CachedPredictionCount);
        Assert.Equal(0f, metrics.ReconciliationsPerSecond, 5);

        // Inputs while dead are ignored
        var c2 = tracker.Record(1f, 0f, 0f, 0u);
        predictor.ApplyLocalInput(c2, Map1);
        Assert.Equal(deathPos, predictor.PredictedPosition);
        Assert.Equal(0, predictor.CachedPredictionCount);

        // Authoritative revive at a different respawn point -> cleanly snaps with zero spurious error
        tracker.Acknowledge(2u);
        var revivePos = new BattleVec2(8f, 10f);
        var reviveResult = predictor.Reconcile(revivePos, 90f, false, authAlive: true, 2u, tracker.PendingInputs, Map1);
        metrics.RecordSample(1.2, reviveResult.PositionError, reviveResult.Reconciled);

        Assert.True(predictor.Alive);
        Assert.False(reviveResult.Reconciled);
        Assert.Equal(0f, reviveResult.PositionError, 5);
        Assert.Equal(revivePos, predictor.PredictedPosition);
        Assert.Equal(0f, metrics.ReconciliationsPerSecond, 5);
        Assert.Equal(0f, metrics.AveragePositionError, 5);

        // Normal prediction resumes after revive
        var c3 = tracker.Record(0f, 1f, 90f, 0u);
        predictor.ApplyLocalInput(c3, Map1);
        Assert.True(predictor.PredictedPosition.Y > revivePos.Y);
        Assert.Equal(1, predictor.CachedPredictionCount);
    }

    [Fact]
    public void Predictor_InjectPredictionOffset_TriggersReconciliationAndSmoothRecovery()
    {
        var predictor = new LocalPlayerPredictor();
        var tracker = new InputBatchTracker();
        var spawn = new BattleVec2(4f, 12f);
        predictor.Reset(spawn, facingDegrees: 0f, alive: true);

        Assert.True(predictor.InjectPredictionOffset(0.5f, 0f));
        Assert.Equal(4.5f, predictor.PredictedPosition.X, 4);

        var result = predictor.Reconcile(spawn, 0f, false, true, 0u, tracker.PendingInputs, Map1);
        Assert.True(result.Reconciled);
        Assert.False(result.HardSnapped);
        Assert.InRange(result.PositionError, 0.49f, 0.51f);
        Assert.Equal(spawn.X, predictor.PredictedPosition.X, 4);
        Assert.True(MathF.Abs(predictor.VisualErrorOffset.X) > 0.4f);
    }

    private sealed class StubNetwork : INetworkMessageClient
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
                _handlers[type] = list = new List<Delegate>();
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
}
