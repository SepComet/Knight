#nullable enable
using System;
using System.Collections.Generic;

namespace SepCore.Runtime.Network;

/// <summary>
/// 底层 KCP 传输层实时统计快照：
/// - <see cref="RttMs"/>：来自 KCP SRTT（若尚未产生 ACK 样本则回退至 Ping/Pong RTT），单位 ms；
/// - <see cref="LossRate"/>：基于滑动窗口内 KCP 上行（C→S）段重传率（重传段 / (新发段 + 重传段)，含快速重传，不含下行丢包），范围 [0, 1]。
/// </summary>
public readonly record struct NetworkTransportStats(
    float RttMs,
    float LossRate,
    uint SegmentsSent,
    uint Retransmits);

/// <summary>
/// 基于滑动窗口的 KCP 上行重传率估算器（纯 C#，可单元测试）。
/// 相比全程累计比值，滑动窗口在运行时开关弱网注入器时能迅速反映真实变化；
/// 内置最小采样间隔节流，高频或同时间戳调用原位更新尾部样本而非重复入队。
/// </summary>
public sealed class KcpLossRateEstimator
{
    public const double DefaultMinSampleIntervalSeconds = 0.1;

    private readonly record struct CounterSample(double TimeSeconds, uint SegmentsSent, uint Retransmits);

    private readonly double _windowSeconds;
    private readonly double _minSampleIntervalSeconds;
    private readonly List<CounterSample> _samples = new();
    private uint _lastSent;
    private uint _lastRetransmits;
    private bool _hasLast;

    public float CurrentLossRate { get; private set; }
    public int SampleCount => _samples.Count;

    public KcpLossRateEstimator(
        double windowSeconds = 5.0,
        double minSampleIntervalSeconds = DefaultMinSampleIntervalSeconds)
    {
        if (!double.IsFinite(windowSeconds) || windowSeconds <= 0)
            throw new ArgumentOutOfRangeException(nameof(windowSeconds), "Window must be positive and finite.");
        if (!double.IsFinite(minSampleIntervalSeconds) || minSampleIntervalSeconds < 0)
            throw new ArgumentOutOfRangeException(nameof(minSampleIntervalSeconds), "Min sample interval must be non-negative and finite.");
        _windowSeconds = windowSeconds;
        _minSampleIntervalSeconds = minSampleIntervalSeconds;
    }

    public float Sample(double nowSeconds, uint segmentsSent, uint retransmits)
    {
        if (!double.IsFinite(nowSeconds))
            return CurrentLossRate;

        // 计数器回退（例如重连重置了 Kcp 实例）时清空历史样本重新统计
        if (_hasLast && (segmentsSent < _lastSent || retransmits < _lastRetransmits))
        {
            _samples.Clear();
            CurrentLossRate = 0f;
        }

        _lastSent = segmentsSent;
        _lastRetransmits = retransmits;
        _hasLast = true;

        if (_samples.Count == 0)
        {
            _samples.Add(new CounterSample(nowSeconds, segmentsSent, retransmits));
        }
        else
        {
            var last = _samples[^1];
            if (nowSeconds <= last.TimeSeconds)
            {
                _samples[^1] = new CounterSample(last.TimeSeconds, segmentsSent, retransmits);
            }
            else if (_samples.Count >= 2 && (nowSeconds - _samples[^2].TimeSeconds) < _minSampleIntervalSeconds)
            {
                _samples[^1] = new CounterSample(nowSeconds, segmentsSent, retransmits);
            }
            else
            {
                _samples.Add(new CounterSample(nowSeconds, segmentsSent, retransmits));
            }
        }

        var effectiveNow = _samples[^1].TimeSeconds;
        var cutoff = effectiveNow - _windowSeconds;
        var pruneCount = 0;
        while (pruneCount + 1 < _samples.Count && _samples[pruneCount].TimeSeconds < cutoff)
            pruneCount++;
        if (pruneCount > 0)
            _samples.RemoveRange(0, pruneCount);

        var oldest = _samples[0];
        var deltaSent = segmentsSent >= oldest.SegmentsSent ? segmentsSent - oldest.SegmentsSent : 0u;
        var deltaRetrans = retransmits >= oldest.Retransmits ? retransmits - oldest.Retransmits : 0u;
        var total = (double)deltaSent + deltaRetrans;

        CurrentLossRate = total > 0.0
            ? (float)Math.Clamp(deltaRetrans / total, 0.0, 1.0)
            : 0f;

        return CurrentLossRate;
    }

    public void Reset()
    {
        _samples.Clear();
        _lastSent = 0;
        _lastRetransmits = 0;
        _hasLast = false;
        CurrentLossRate = 0f;
    }
}
