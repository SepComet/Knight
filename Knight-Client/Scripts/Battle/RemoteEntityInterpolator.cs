#nullable enable
using System;
using System.Collections.Generic;

namespace SepCore.Battle;

/// <summary>远端实体快照样本（世界坐标系 + 接收时间戳）。</summary>
public readonly record struct EntitySnapshotSample(
    double TimestampSeconds,
    BattleVec2 Position,
    float FacingDegrees,
    bool Moving,
    bool Alive);

/// <summary>远端实体插值输出状态。</summary>
public readonly record struct InterpolatedEntityState(
    BattleVec2 Position,
    float FacingDegrees,
    bool Moving,
    bool Alive);

/// <summary>
/// 远端实体快照插值器（纯 C#，无 Godot 节点依赖，可复用于远端玩家及 M3 怪物/Boss）。
/// 维护按时间戳递增的快照缓冲队列，默认采用 100ms 插值延迟对位置线性插值、对朝向做最短路径角度插值；
/// 严格遵循 Docs/protocol.md §7.2，默认不外推（缓冲不足时吸附最新权威快照，不穿墙、不产生 NaN；
/// 构造参数保留 <see cref="MaxExtrapolationSeconds"/> 供未来显式开启）；
/// 关闭插值开关时退化为直接返回最新权威快照，便于弱网与 15Hz 跳跃对比。
/// </summary>
public sealed class RemoteEntityInterpolator
{
    public const double DefaultInterpolationDelaySeconds = 0.10;
    public const double DefaultMaxExtrapolationSeconds = 0.0;
    public const int DefaultMaxBufferSize = 32;

    private readonly List<EntitySnapshotSample> _buffer = new();

    public double InterpolationDelaySeconds { get; set; }
    public double MaxExtrapolationSeconds { get; set; }
    public int MaxBufferSize { get; }
    public bool Enabled { get; set; } = true;
    public int BufferedCount => _buffer.Count;

    public RemoteEntityInterpolator(
        double interpolationDelaySeconds = DefaultInterpolationDelaySeconds,
        double maxExtrapolationSeconds = DefaultMaxExtrapolationSeconds,
        int maxBufferSize = DefaultMaxBufferSize)
    {
        InterpolationDelaySeconds = double.IsFinite(interpolationDelaySeconds) && interpolationDelaySeconds >= 0.0
            ? interpolationDelaySeconds
            : DefaultInterpolationDelaySeconds;
        MaxExtrapolationSeconds = double.IsFinite(maxExtrapolationSeconds) && maxExtrapolationSeconds >= 0.0
            ? maxExtrapolationSeconds
            : DefaultMaxExtrapolationSeconds;
        MaxBufferSize = Math.Max(2, maxBufferSize);
    }

    /// <summary>压入一条远端权威快照样本；乱序旧样本被忽略，同时间戳样本覆盖尾部。</summary>
    public void PushSample(EntitySnapshotSample sample)
    {
        if (!double.IsFinite(sample.TimestampSeconds))
            return;

        var sanitized = new EntitySnapshotSample(
            sample.TimestampSeconds,
            new BattleVec2(
                float.IsFinite(sample.Position.X) ? sample.Position.X : 0f,
                float.IsFinite(sample.Position.Y) ? sample.Position.Y : 0f),
            float.IsFinite(sample.FacingDegrees) ? sample.FacingDegrees : 0f,
            sample.Moving,
            sample.Alive);

        if (_buffer.Count > 0)
        {
            var last = _buffer[^1];
            if (sanitized.TimestampSeconds < last.TimestampSeconds)
                return;
            if (Math.Abs(sanitized.TimestampSeconds - last.TimestampSeconds) < 1e-9)
            {
                _buffer[^1] = sanitized;
                return;
            }
        }

        _buffer.Add(sanitized);
        if (_buffer.Count > MaxBufferSize)
            _buffer.RemoveRange(0, _buffer.Count - MaxBufferSize);
    }

    /// <summary>
    /// 按当前本地单调时钟采样远端实体渲染状态。
    /// 当 <see cref="Enabled"/> 为 false 时直接返回最新权威快照。
    /// </summary>
    public bool TrySample(double nowSeconds, out InterpolatedEntityState state)
    {
        state = default;
        if (_buffer.Count == 0 || !double.IsFinite(nowSeconds))
            return false;

        var latest = _buffer[^1];
        if (!Enabled || _buffer.Count == 1)
        {
            state = ToState(latest);
            return true;
        }

        var renderTime = nowSeconds - InterpolationDelaySeconds;

        // 清理已完全过期的旧样本，仅保留紧邻 renderTime 左侧的一个样本用于插值
        var pruneCount = 0;
        while (pruneCount + 2 < _buffer.Count && _buffer[pruneCount + 1].TimestampSeconds <= renderTime)
            pruneCount++;
        if (pruneCount > 0)
            _buffer.RemoveRange(0, pruneCount);

        // renderTime 落在缓冲最早样本之前：保持最早样本
        if (renderTime <= _buffer[0].TimestampSeconds)
        {
            state = ToState(_buffer[0]);
            return true;
        }

        // renderTime 落在某两个相邻样本之间：线性插值位置 + 最短路径插值朝向
        for (var i = 0; i < _buffer.Count - 1; i++)
        {
            var a = _buffer[i];
            var b = _buffer[i + 1];
            if (renderTime <= b.TimestampSeconds)
            {
                var span = b.TimestampSeconds - a.TimestampSeconds;
                var t = span > 1e-9 ? (float)Math.Clamp((renderTime - a.TimestampSeconds) / span, 0.0, 1.0) : 1f;
                var pos = new BattleVec2(
                    a.Position.X + (b.Position.X - a.Position.X) * t,
                    a.Position.Y + (b.Position.Y - a.Position.Y) * t);
                var facing = LerpAngleDegrees(a.FacingDegrees, b.FacingDegrees, t);
                var moving = a.Moving || b.Moving;
                state = new InterpolatedEntityState(pos, facing, moving, b.Alive);
                return true;
            }
        }

        // renderTime 超过最新样本（缓冲不足）：受限小幅外推或保持最新状态
        var prev = _buffer[^2];
        var over = renderTime - latest.TimestampSeconds;
        var dt = latest.TimestampSeconds - prev.TimestampSeconds;
        if (latest.Alive && MaxExtrapolationSeconds > 0.0 && over > 0.0 && dt > 1e-4)
        {
            var clampedOver = Math.Min(over, MaxExtrapolationSeconds);
            var vx = (latest.Position.X - prev.Position.X) / dt;
            var vy = (latest.Position.Y - prev.Position.Y) / dt;
            var pos = new BattleVec2(
                latest.Position.X + (float)(vx * clampedOver),
                latest.Position.Y + (float)(vy * clampedOver));
            state = new InterpolatedEntityState(pos, latest.FacingDegrees, latest.Moving, latest.Alive);
            return true;
        }

        state = ToState(latest);
        return true;
    }

    /// <summary>角度最短路径线性插值（度），正确处理 0°/360° 与 -180°/+180° 跨越。</summary>
    public static float LerpAngleDegrees(float fromDeg, float toDeg, float t)
    {
        if (!float.IsFinite(fromDeg)) fromDeg = 0f;
        if (!float.IsFinite(toDeg)) toDeg = fromDeg;
        var clampedT = Math.Clamp(float.IsFinite(t) ? t : 0f, 0f, 1f);
        var delta = ((toDeg - fromDeg) % 360f + 540f) % 360f - 180f;
        return fromDeg + delta * clampedT;
    }

    public void Clear() => _buffer.Clear();

    private static InterpolatedEntityState ToState(EntitySnapshotSample sample)
        => new(sample.Position, sample.FacingDegrees, sample.Moving, sample.Alive);
}
