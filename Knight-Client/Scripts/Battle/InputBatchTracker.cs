#nullable enable
using System;
using System.Collections.Generic;
using SepCore.Definition;

namespace SepCore.Battle;

/// <summary>
/// 30Hz 输入采样序号管理与未确认输入缓存（对应 Docs/protocol.md §5.5 与 knight.proto InputBatch）。
/// 每次构建的 InputBatch 携带全部尚未被服务端 last_input_seq 确认的输入，单批严格不超过 120 条。
/// </summary>
public sealed class InputBatchTracker
{
    public const float InputIntervalSeconds = 1f / 30f;
    public const int MaxBatchSize = 120;

    private readonly List<InputCmd> _unacked = new();
    private readonly IReadOnlyList<InputCmd> _pendingView;
    private uint _nextSeq = 1;

    public InputBatchTracker()
    {
        _pendingView = _unacked.AsReadOnly();
    }

    /// <summary>下一条待分配的输入序号（从 1 起单调递增）。</summary>
    public uint NextSeq => _nextSeq;

    /// <summary>已收到的最新服务端确认序号。</summary>
    public uint LastAckedSeq { get; private set; }

    /// <summary>当前待确认输入条数。</summary>
    public int PendingCount => _unacked.Count;

    /// <summary>尚未被服务端确认的输入只读视图（按 seq 升序，供 M2 预测校正重放遍历）。</summary>
    public IReadOnlyList<InputCmd> PendingInputs => _pendingView;

    /// <summary>记录一条新采样输入并分配单调递增 seq。</summary>
    public InputCmd Record(float moveX, float moveY, float aimAngleDeg, uint buttons)
    {
        var mx = float.IsFinite(moveX) ? moveX : 0f;
        var my = float.IsFinite(moveY) ? moveY : 0f;
        var lenSq = mx * mx + my * my;
        if (lenSq > 1f)
        {
            var inv = 1f / MathF.Sqrt(lenSq);
            mx *= inv;
            my *= inv;
        }

        var aim = float.IsFinite(aimAngleDeg) ? aimAngleDeg : 0f;
        var cmd = new InputCmd
        {
            Seq = _nextSeq++,
            MoveX = mx,
            MoveY = my,
            AimAngle = aim,
            Buttons = buttons,
        };

        _unacked.Add(cmd);
        if (_unacked.Count > MaxBatchSize)
            _unacked.RemoveRange(0, _unacked.Count - MaxBatchSize);

        return cmd;
    }

    /// <summary>按服务端快照的 last_input_seq 清理已确认输入。</summary>
    public void Acknowledge(uint lastInputSeq)
    {
        if (lastInputSeq <= LastAckedSeq)
            return;

        LastAckedSeq = lastInputSeq;
        var removeCount = 0;
        while (removeCount < _unacked.Count && _unacked[removeCount].Seq <= lastInputSeq)
            removeCount++;

        if (removeCount > 0)
            _unacked.RemoveRange(0, removeCount);
    }

    /// <summary>打包全部未确认输入为 InputBatch；无待发输入时返回 null。</summary>
    public InputBatch? BuildBatch()
    {
        if (_unacked.Count == 0)
            return null;

        var batch = new InputBatch { FirstSeq = _unacked[0].Seq };
        foreach (var cmd in _unacked)
            batch.Inputs.Add(cmd.Clone());
        return batch;
    }

    /// <summary>清空当前未确认输入缓存，保持输入序号单调递增（切房时调用）。</summary>
    public void ClearPending()
    {
        _unacked.Clear();
    }

    /// <summary>重置输入序号与缓存（新对局开始时调用）。</summary>
    public void Reset()
    {
        _unacked.Clear();
        _nextSeq = 1;
        LastAckedSeq = 0;
    }
}
