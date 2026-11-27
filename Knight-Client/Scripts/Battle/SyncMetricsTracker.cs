#nullable enable
using System;
using System.Collections.Generic;

namespace SepCore.Battle;

/// <summary>
/// 客户端预测与状态校正的滑动窗口统计器（纯 C#，无 Godot 依赖）。
/// 实时计算最近窗口内的：
/// - 预测校正次数/秒（ReconciliationsPerSecond）
/// - 平均位置误差（AveragePositionError，单位：世界单位/tile）
/// </summary>
public sealed class SyncMetricsTracker
{
    private readonly record struct SampleEntry(double TimeSeconds, float PositionError, bool Reconciled);

    private readonly double _windowSeconds;
    private readonly Queue<SampleEntry> _samples = new();
    private double _errorSum;
    private int _reconcileCountInWindow;

    public double WindowSeconds => _windowSeconds;
    public int SampleCount => _samples.Count;
    public long TotalReconciliations { get; private set; }
    public float LastPositionError { get; private set; }

    /// <summary>滑动窗口内的预测校正频率（次/秒）。</summary>
    public float ReconciliationsPerSecond => (float)(_reconcileCountInWindow / _windowSeconds);

    /// <summary>滑动窗口内的平均位置误差（世界单位 / tile）。</summary>
    public float AveragePositionError => _samples.Count > 0 ? (float)(_errorSum / _samples.Count) : 0f;

    public SyncMetricsTracker(double windowSeconds = 1.0)
    {
        if (!double.IsFinite(windowSeconds) || windowSeconds <= 0.0)
            throw new ArgumentOutOfRangeException(nameof(windowSeconds), "Window must be positive and finite.");
        _windowSeconds = windowSeconds;
    }

    public void RecordSample(double nowSeconds, float positionError, bool reconciled)
    {
        if (!double.IsFinite(nowSeconds))
            return;

        var safeError = float.IsFinite(positionError) && positionError >= 0f ? positionError : 0f;
        LastPositionError = safeError;

        _samples.Enqueue(new SampleEntry(nowSeconds, safeError, reconciled));
        _errorSum += safeError;
        if (reconciled)
        {
            _reconcileCountInWindow++;
            TotalReconciliations++;
        }

        Prune(nowSeconds);
    }

    public void Prune(double nowSeconds)
    {
        if (!double.IsFinite(nowSeconds))
            return;

        var cutoff = nowSeconds - _windowSeconds;
        while (_samples.Count > 0 && _samples.Peek().TimeSeconds < cutoff)
        {
            var removed = _samples.Dequeue();
            _errorSum -= removed.PositionError;
            if (removed.Reconciled)
                _reconcileCountInWindow--;
        }

        if (_samples.Count == 0)
        {
            _errorSum = 0.0;
            _reconcileCountInWindow = 0;
        }
        else if (_errorSum < 0.0)
        {
            _errorSum = 0.0;
        }
    }

    public void Reset()
    {
        _samples.Clear();
        _errorSum = 0.0;
        _reconcileCountInWindow = 0;
        TotalReconciliations = 0;
        LastPositionError = 0f;
    }
}
