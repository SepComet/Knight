#nullable enable
using System;
using System.Collections.Generic;
using SepCore.Definition;

namespace SepCore.Battle;

/// <summary>客户端预测与状态校正参数（默认与服务端 internal/world/room.go 逐项对齐）。</summary>
public sealed class PredictionConfig
{
    public float MoveSpeedTiles { get; init; } = BattleMapCatalog.DefaultPlayerMoveSpeedTiles;
    public float PlayerRadius { get; init; } = BattleCollision.DefaultPlayerRadius;
    public float StepSeconds { get; init; } = InputBatchTracker.InputIntervalSeconds;

    /// <summary>触发回滚重放的位置误差阈值（世界单位/tile，0.02 tile ≈ 0.64 px）。</summary>
    public float ReconcileThreshold { get; init; } = 0.02f;

    /// <summary>触发硬校正（直接瞬移、不平滑）的位置误差阈值（世界单位/tile，1.5 tile = 48 px）。</summary>
    public float SnapThreshold { get; init; } = 1.5f;

    /// <summary>视觉平滑纠偏的指数衰减速率（1/s）。</summary>
    public float VisualErrorDecayRate { get; init; } = 15.0f;
}

/// <summary>单次权威快照对账结果。</summary>
public readonly record struct ReconciliationResult(
    float PositionError,
    bool Reconciled,
    bool HardSnapped,
    int ReplayedInputs);

/// <summary>
/// 本地玩家客户端预测与状态校正器（纯 C#，无 Godot 节点依赖）。
/// 与服务端 30Hz 固定步长与 <see cref="BattleCollision"/> 数学同构：
/// - 预测开启时：本地输入立即推进预测位置并缓存 (seq -> 预测位置)；收到权威快照后按 last_input_seq 对账，
///   误差超阈值时从权威位置回滚并重放未确认输入，大误差硬校正，中小误差通过视觉偏移平滑衰减；
/// - 预测关闭时：退化为直接跟随服务端 15Hz 权威快照位置，便于弱网对比演示。
/// </summary>
public sealed class LocalPlayerPredictor
{
    private readonly record struct PredictedEntry(uint Seq, BattleVec2 Position);

    private readonly PredictionConfig _config;
    private readonly List<PredictedEntry> _history = new();
    private bool _enabled = true;
    private BattleVec2 _basePosition;
    private uint _baseSeq;

    public PredictionConfig Config => _config;
    public bool HasState { get; private set; }
    public BattleVec2 PredictedPosition { get; private set; }
    public BattleVec2 VisualErrorOffset { get; private set; }
    public BattleVec2 RenderPosition => new(PredictedPosition.X + VisualErrorOffset.X, PredictedPosition.Y + VisualErrorOffset.Y);
    public BattleVec2 LastAuthoritativePosition { get; private set; }
    public float FacingDegrees { get; private set; }
    public bool IsMoving { get; private set; }
    public bool Alive { get; private set; } = true;
    public uint LastReconciledSeq { get; private set; }
    public int CachedPredictionCount => _history.Count;

    public bool Enabled
    {
        get => _enabled;
        set
        {
            if (_enabled == value)
                return;
            _enabled = value;
            _history.Clear();
            VisualErrorOffset = new BattleVec2(0f, 0f);
            if (HasState)
            {
                PredictedPosition = LastAuthoritativePosition;
                _basePosition = LastAuthoritativePosition;
                _baseSeq = LastReconciledSeq;
            }
        }
    }

    public LocalPlayerPredictor(PredictionConfig? config = null)
    {
        _config = config ?? new PredictionConfig();
    }

    /// <summary>使用初始权威状态重置预测器（出生/切房时调用）。</summary>
    public void Reset(BattleVec2 position, float facingDegrees = 0f, bool alive = true)
    {
        var safePos = SanitizeVec2(position);
        HasState = true;
        PredictedPosition = safePos;
        LastAuthoritativePosition = safePos;
        _basePosition = safePos;
        _baseSeq = 0;
        VisualErrorOffset = new BattleVec2(0f, 0f);
        FacingDegrees = float.IsFinite(facingDegrees) ? facingDegrees : 0f;
        IsMoving = false;
        Alive = alive;
        LastReconciledSeq = 0;
        _history.Clear();
    }

    /// <summary>清空状态（离开战斗或本地实体销毁时调用）。</summary>
    public void Clear()
    {
        HasState = false;
        PredictedPosition = default;
        LastAuthoritativePosition = default;
        _basePosition = default;
        _baseSeq = 0;
        VisualErrorOffset = default;
        FacingDegrees = 0f;
        IsMoving = false;
        Alive = true;
        LastReconciledSeq = 0;
        _history.Clear();
    }

    /// <summary>
    /// 基于最新权威位置重放当前在途未确认输入并重建历史预测缓冲（用于预测开关 OFF→ON 切换）。
    /// </summary>
    public void RebuildFromUnacked(IReadOnlyList<InputCmd> unacked, BattleMapDefinition map)
    {
        ArgumentNullException.ThrowIfNull(unacked);
        ArgumentNullException.ThrowIfNull(map);

        if (!HasState || !Alive || !_enabled)
            return;

        _history.Clear();
        VisualErrorOffset = new BattleVec2(0f, 0f);
        var pos = LastAuthoritativePosition;
        var facing = FacingDegrees;
        var moving = IsMoving;
        _basePosition = pos;
        _baseSeq = LastReconciledSeq;

        for (var i = 0; i < unacked.Count; i++)
        {
            var cmd = unacked[i];
            if (cmd.Seq <= LastReconciledSeq)
                continue;

            if (float.IsFinite(cmd.AimAngle))
                facing = cmd.AimAngle;

            pos = StepMove(
                pos,
                cmd.MoveX,
                cmd.MoveY,
                _config.MoveSpeedTiles,
                _config.StepSeconds,
                _config.PlayerRadius,
                map,
                out moving);

            _history.Add(new PredictedEntry(cmd.Seq, pos));
        }

        if (_history.Count > InputBatchTracker.MaxBatchSize)
        {
            var drop = _history.Count - InputBatchTracker.MaxBatchSize;
            _basePosition = _history[drop - 1].Position;
            _baseSeq = _history[drop - 1].Seq;
            _history.RemoveRange(0, drop);
        }

        PredictedPosition = pos;
        FacingDegrees = facing;
        IsMoving = moving;
    }

    /// <summary>
    /// 仅用于调试/E2E 验证：人为偏移本地预测位置与历史预测缓冲，不改变服务端权威位置，
    /// 使下一帧权威快照能触发真实的回滚重放与平滑收敛。
    /// </summary>
    public bool InjectPredictionOffset(float dx, float dy)
    {
        if (!HasState || !Alive || !_enabled)
            return false;
        if (!float.IsFinite(dx) || !float.IsFinite(dy))
            return false;

        PredictedPosition = new BattleVec2(PredictedPosition.X + dx, PredictedPosition.Y + dy);
        _basePosition = new BattleVec2(_basePosition.X + dx, _basePosition.Y + dy);
        for (var i = 0; i < _history.Count; i++)
        {
            var entry = _history[i];
            _history[i] = new PredictedEntry(entry.Seq, new BattleVec2(entry.Position.X + dx, entry.Position.Y + dy));
        }

        return true;
    }

    /// <summary>
    /// 与服务端 <c>world/room.go</c> <c>sanitizeMove</c> 逐行数学等价的移动向量清洗。
    /// </summary>
    public static (float X, float Y) SanitizeMove(float x, float y)
    {
        double fx = float.IsFinite(x) ? x : 0.0;
        double fy = float.IsFinite(y) ? y : 0.0;
        var mag = Math.Sqrt(fx * fx + fy * fy);
        if (mag < 1e-6)
            return (0f, 0f);
        if (mag > 1.0)
        {
            fx /= mag;
            fy /= mag;
        }
        return ((float)fx, (float)fy);
    }

    /// <summary>
    /// 单步权威同构移动与碰撞解算（与服务端 <c>world/room.go</c> <c>applyInput</c> 完全等价）。
    /// </summary>
    public static BattleVec2 StepMove(
        BattleVec2 pos,
        float moveX,
        float moveY,
        float moveSpeedTiles,
        float stepSeconds,
        float playerRadius,
        BattleMapDefinition map,
        out bool moved)
    {
        ArgumentNullException.ThrowIfNull(map);
        var (mx, my) = SanitizeMove(moveX, moveY);
        if (mx == 0f && my == 0f)
        {
            moved = false;
            return pos;
        }

        var next = new BattleVec2(
            pos.X + mx * moveSpeedTiles * stepSeconds,
            pos.Y + my * moveSpeedTiles * stepSeconds);
        moved = true;
        return BattleCollision.ResolveCollisions(next, playerRadius, map);
    }

    /// <summary>本地 30Hz 采样输入后立即推进预测位置（预测关闭时仅忽略本地推进，等待服务端快照）。</summary>
    public void ApplyLocalInput(InputCmd cmd, BattleMapDefinition map)
    {
        ArgumentNullException.ThrowIfNull(cmd);
        ArgumentNullException.ThrowIfNull(map);

        if (!HasState || !Alive)
            return;

        if (!_enabled)
            return;

        if (float.IsFinite(cmd.AimAngle))
            FacingDegrees = cmd.AimAngle;

        PredictedPosition = StepMove(
            PredictedPosition,
            cmd.MoveX,
            cmd.MoveY,
            _config.MoveSpeedTiles,
            _config.StepSeconds,
            _config.PlayerRadius,
            map,
            out var moved);
        IsMoving = moved;

        _history.Add(new PredictedEntry(cmd.Seq, PredictedPosition));
        if (_history.Count > InputBatchTracker.MaxBatchSize)
        {
            var drop = _history.Count - InputBatchTracker.MaxBatchSize;
            _basePosition = _history[drop - 1].Position;
            _baseSeq = _history[drop - 1].Seq;
            _history.RemoveRange(0, drop);
        }
    }

    /// <summary>
    /// 收到服务端权威快照后执行对账与回滚重放。
    /// 调用方应先对 <see cref="InputBatchTracker"/> 执行 Acknowledge，使 <paramref name="unackedInputs"/> 仅含未确认输入。
    /// </summary>
    public ReconciliationResult Reconcile(
        BattleVec2 authPos,
        float authFacingDegrees,
        bool authMoving,
        bool authAlive,
        uint lastInputSeq,
        IReadOnlyList<InputCmd> unackedInputs,
        BattleMapDefinition map)
    {
        ArgumentNullException.ThrowIfNull(unackedInputs);
        ArgumentNullException.ThrowIfNull(map);

        // 忽略乱序/过期快照对账（含 120 条截断后迟到的 lastInputSeq < _baseSeq），防止基准错位触发虚假回滚
        if (HasState && lastInputSeq < LastReconciledSeq)
            return new ReconciliationResult(0f, false, false, 0);
        if (HasState && _enabled && Alive && authAlive && lastInputSeq < _baseSeq)
            return new ReconciliationResult(0f, false, false, 0);

        var safeAuthPos = SanitizeVec2(authPos);
        var safeAuthFacing = float.IsFinite(authFacingDegrees) ? authFacingDegrees : FacingDegrees;
        var wasAlive = Alive;

        LastAuthoritativePosition = safeAuthPos;
        LastReconciledSeq = lastInputSeq;
        Alive = authAlive;

        if (!HasState || !authAlive || !wasAlive)
        {
            HasState = true;
            PredictedPosition = safeAuthPos;
            _basePosition = safeAuthPos;
            _baseSeq = lastInputSeq;
            VisualErrorOffset = new BattleVec2(0f, 0f);
            FacingDegrees = safeAuthFacing;
            IsMoving = authAlive && authMoving;
            _history.Clear();
            return new ReconciliationResult(0f, false, false, 0);
        }

        // 预测关闭：直接退化为服务端权威快照位置（15Hz 步进），不做本地回滚重放与平滑
        if (!_enabled)
        {
            PredictedPosition = safeAuthPos;
            _basePosition = safeAuthPos;
            _baseSeq = lastInputSeq;
            VisualErrorOffset = new BattleVec2(0f, 0f);
            FacingDegrees = safeAuthFacing;
            IsMoving = authMoving;
            _history.Clear();
            return new ReconciliationResult(0f, false, false, 0);
        }

        var prevRenderPos = RenderPosition;

        // 查找 lastInputSeq 对应的历史预测位置，并清理所有已确认历史
        var foundPredicted = TryConsumePredictedAtSeq(lastInputSeq, out var predictedAtAck);
        var comparePos = foundPredicted ? predictedAtAck : _basePosition;
        var posError = Distance(safeAuthPos, comparePos);

        // 无未确认输入时若历史缺失，也需比对当前预测位置
        if (!foundPredicted && unackedInputs.Count == 0)
            posError = Distance(safeAuthPos, PredictedPosition);

        if (posError <= _config.ReconcileThreshold)
        {
            _basePosition = safeAuthPos;
            _baseSeq = lastInputSeq;
            if (unackedInputs.Count == 0)
            {
                FacingDegrees = safeAuthFacing;
                IsMoving = authMoving;
            }
            return new ReconciliationResult(posError, false, false, 0);
        }

        // 误差超阈值：从权威位置回滚并重放所有未确认输入
        var replayedPos = safeAuthPos;
        var replayedFacing = safeAuthFacing;
        var replayedMoving = authMoving;
        _history.Clear();
        _basePosition = safeAuthPos;
        _baseSeq = lastInputSeq;

        for (var i = 0; i < unackedInputs.Count; i++)
        {
            var cmd = unackedInputs[i];
            if (cmd.Seq <= lastInputSeq)
                continue;

            if (float.IsFinite(cmd.AimAngle))
                replayedFacing = cmd.AimAngle;

            replayedPos = StepMove(
                replayedPos,
                cmd.MoveX,
                cmd.MoveY,
                _config.MoveSpeedTiles,
                _config.StepSeconds,
                _config.PlayerRadius,
                map,
                out replayedMoving);

            _history.Add(new PredictedEntry(cmd.Seq, replayedPos));
        }

        if (_history.Count > InputBatchTracker.MaxBatchSize)
        {
            var drop = _history.Count - InputBatchTracker.MaxBatchSize;
            _basePosition = _history[drop - 1].Position;
            _baseSeq = _history[drop - 1].Seq;
            _history.RemoveRange(0, drop);
        }

        PredictedPosition = replayedPos;
        FacingDegrees = replayedFacing;
        IsMoving = replayedMoving;

        var hardSnapped = posError >= _config.SnapThreshold;
        if (hardSnapped)
        {
            VisualErrorOffset = new BattleVec2(0f, 0f);
        }
        else
        {
            var offsetX = prevRenderPos.X - replayedPos.X;
            var offsetY = prevRenderPos.Y - replayedPos.Y;
            var offsetLen = MathF.Sqrt(offsetX * offsetX + offsetY * offsetY);
            if (offsetLen > _config.SnapThreshold)
            {
                VisualErrorOffset = new BattleVec2(0f, 0f);
                hardSnapped = true;
            }
            else
            {
                VisualErrorOffset = new BattleVec2(offsetX, offsetY);
            }
        }

        return new ReconciliationResult(posError, true, hardSnapped, _history.Count);
    }

    /// <summary>每渲染帧衰减视觉纠偏偏移，消除小偏差导致的生硬瞬移。</summary>
    public void DecayVisualError(float deltaSeconds)
    {
        if (!float.IsFinite(deltaSeconds) || deltaSeconds <= 0f)
            return;

        if (VisualErrorOffset.X == 0f && VisualErrorOffset.Y == 0f)
            return;

        var factor = MathF.Exp(-_config.VisualErrorDecayRate * deltaSeconds);
        var nx = VisualErrorOffset.X * factor;
        var ny = VisualErrorOffset.Y * factor;
        if (nx * nx + ny * ny < 1e-8f)
            VisualErrorOffset = new BattleVec2(0f, 0f);
        else
            VisualErrorOffset = new BattleVec2(nx, ny);
    }

    private bool TryConsumePredictedAtSeq(uint seq, out BattleVec2 position)
    {
        position = default;
        if (seq == 0 || _history.Count == 0)
            return false;

        var found = false;
        var removeCount = 0;
        for (var i = 0; i < _history.Count; i++)
        {
            if (_history[i].Seq <= seq)
            {
                removeCount = i + 1;
                if (_history[i].Seq == seq)
                {
                    position = _history[i].Position;
                    found = true;
                }
            }
            else
            {
                break;
            }
        }

        if (removeCount > 0)
        {
            _basePosition = _history[removeCount - 1].Position;
            _baseSeq = _history[removeCount - 1].Seq;
            _history.RemoveRange(0, removeCount);
        }

        return found;
    }

    private static float Distance(BattleVec2 a, BattleVec2 b)
    {
        var dx = a.X - b.X;
        var dy = a.Y - b.Y;
        return MathF.Sqrt(dx * dx + dy * dy);
    }

    private static BattleVec2 SanitizeVec2(BattleVec2 v)
        => new(float.IsFinite(v.X) ? v.X : 0f, float.IsFinite(v.Y) ? v.Y : 0f);
}
