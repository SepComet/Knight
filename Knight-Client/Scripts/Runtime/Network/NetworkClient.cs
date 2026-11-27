#nullable enable
using System;
using System.Collections.Generic;
using Google.Protobuf;
using SepCore.Definition;
using kcp2k;

namespace SepCore.Runtime.Network;

/// <summary>连接状态:Disconnected → Connecting(kcp2k 通道建立+应用握手) → Connected。</summary>
public enum NetworkState
{
    Disconnected,
    /// <summary>kcp2k 通道已建立,正在等待服务端 HandshakeAck。</summary>
    Connecting,
    /// <summary>HandshakeAck 收到,uid 已绑定到本连接。</summary>
    Connected,
}

/// <summary>NetworkClient 可调参数;KCP 链路参数由协议固定,不在此暴露(见 Docs/protocol.md §2)。</summary>
public sealed class NetworkClientConfig
{
    /// <summary>等待 HandshakeAck 的超时(秒);超时判定断线。对齐服务端 10s 心跳超时窗口。</summary>
    public float HandshakeTimeoutSeconds { get; init; } = 10f;

    /// <summary>心跳间隔(秒)。协议要求无条件每 3s 发送 Heartbeat(见 Docs/protocol.md §4.2)。</summary>
    public float HeartbeatIntervalSeconds { get; init; } = 3f;
}

/// <summary>
/// 网络客户端核心(纯 C#,不依赖 Godot,可单元测试):
/// - 封装 kcp2k KcpClient(UDP + KCP 消息模式,一个包一条 Envelope);
/// - 应用层握手(HandshakeReq/Ack)与连接状态机;
/// - 无条件 3s 心跳;
/// - Envelope 编解码与 msg_id/payload 一致性校验;
/// - 按 MsgId 的类型化消息订阅,入站消息在调用 Update 的线程上同步派发。
/// 使用方式:宿主每帧调用 <see cref="Update"/>(NetworkComponent 里是 _Process)。
/// </summary>
public sealed class NetworkClient : INetworkConnectionEvents, INetworkSessionClient
{
    /// <summary>当前协议版本,与 Knight-Proto/knight.proto 中 HandshakeReq 注释的 PROTO_VERSION 保持同步(文件头规则 2)。</summary>
    public const uint ProtoVersion = 5;

    public NetworkState State { get; private set; } = NetworkState.Disconnected;

    /// <summary>HandshakeAck 返回的账号 ID;未握手成功时无意义。</summary>
    public ulong Uid { get; private set; }

    /// <summary>HandshakeAck 返回的角色名;未握手成功时无意义。</summary>
    public string Name { get; private set; } = "";

    /// <summary>应用层握手完成(可开始收发业务消息)。</summary>
    public event Action? Connected;

    /// <summary>连接断开(主动断开/超时/协议违规/服务端拒绝),参数为人类可读原因。MVP 不自动重连。</summary>
    public event Action<string>? Disconnected;

    private readonly NetworkClientConfig _config;
    private readonly Dictionary<MsgId, List<Action<Envelope>>> _handlers = new();

    private KcpClient? _client;
    private string _token = "";
    private string? _lastError;
    private string? _disconnectReason;
    private float _handshakeElapsed;
    private float _heartbeatElapsed;
    private double _elapsedSeconds;
    private readonly KcpLossRateEstimator _lossEstimator = new();

    public NetworkClient(NetworkClientConfig? config = null)
    {
        _config = config ?? new NetworkClientConfig();
    }

    // ---------------------------------------------------------------- 连接生命周期

    /// <summary>
    /// 发起连接并发起应用层握手。KCP 初始参数按协议固定:
    /// nodelay=1, interval=10, resend=2, nc=1(关闭拥塞控制);断线判定超时 10s。
    /// </summary>
    public void Connect(string host, ushort port, string token)
    {
        if (State != NetworkState.Disconnected)
            throw new InvalidOperationException($"当前状态 {State} 不允许发起连接");

        _token = token;
        _lastError = null;
        _disconnectReason = null;
        _handshakeElapsed = 0f;
        _heartbeatElapsed = 0f;
        _elapsedSeconds = 0.0;
        _lossEstimator.Reset();
        State = NetworkState.Connecting;

        _client = new KcpClient(
            OnConnected: OnKcpConnected,
            OnData: OnKcpData,
            OnDisconnected: OnKcpDisconnected,
            OnError: (error, message) =>
            {
                // 仅记录最近的错误作为断线原因;kcp2k 的超时/死链错误随后会触发 OnDisconnected。
                _lastError = $"{error}:{message}";
            },
            config: new KcpConfig(
                NoDelay: true,            // nodelay=1
                Interval: 10,             // interval=10
                FastResend: 2,            // resend=2
                CongestionWindow: false,  // nc=1(关闭拥塞控制)
                Timeout: 10_000));        // 10s 无入包判定断线(协议 §4.2)

        // DNS 解析失败等错误会经 OnError/OnDisconnected 回调同步抵达,状态机自动回 Disconnected。
        _client.Connect(host, port);
    }

    /// <summary>主动断开。幂等:已断开时无事发生。reason 作为最终断线原因上报。</summary>
    public void Disconnect(string reason = "客户端主动断开")
    {
        var client = _client;
        if (State == NetworkState.Disconnected || client == null)
            return;

        _disconnectReason ??= reason;
        // kcp2k 的 Disconnect 会同步触发 OnKcpDisconnected → Teardown(置 Disconnected 并发事件)。
        client.Disconnect();
        if (State != NetworkState.Disconnected)
            Teardown();
    }

    // ---------------------------------------------------------------- 收发与订阅

    /// <summary>发送业务消息;内部包 Envelope 并校验 MsgId/类型一致。仅 Connected 状态可发。</summary>
    /// <exception cref="InvalidOperationException">未处于 Connected 状态。</exception>
    public void Send<T>(T message) where T : IMessage
    {
        if (State != NetworkState.Connected)
            throw new InvalidOperationException($"当前状态 {State} 不允许发送消息");
        SendEnvelope(EnvelopeCodec.MsgIdFor<T>(), message);
    }

    /// <summary>订阅消息，释放返回的句柄可退订。入站消息在 Update 调用线程上同步派发。</summary>
    public IDisposable Subscribe<T>(Action<T> handler) where T : IMessage
    {
        var msgId = EnvelopeCodec.MsgIdFor<T>();
        if (!_handlers.TryGetValue(msgId, out var list))
            _handlers[msgId] = list = new List<Action<Envelope>>();
        Action<Envelope> callback = envelope => handler((T)EnvelopeCodec.Extract(envelope)!);
        list.Add(callback);
        return new Subscription(() => list.Remove(callback));
    }

    private sealed class Subscription(Action remove) : IDisposable
    {
        private Action? _remove = remove;
        public void Dispose() { _remove?.Invoke(); _remove = null; }
    }

    /// <summary>每帧驱动:kcp2k 泵 + 握手超时 + 无条件心跳。仅在非 Disconnected 状态有实际工作。</summary>
    public void Update(float delta)
    {
        if (State == NetworkState.Disconnected)
            return;

        _client?.Tick();

        if (float.IsFinite(delta) && delta > 0f)
            _elapsedSeconds += delta;

        if (_client != null && State == NetworkState.Connected)
            _lossEstimator.Sample(_elapsedSeconds, _client.TotalSegmentsSent, _client.TotalRetransmits);

        if (State == NetworkState.Connecting)
        {
            _handshakeElapsed += delta;
            if (_handshakeElapsed >= _config.HandshakeTimeoutSeconds)
                Disconnect("握手超时");
        }
        else if (State == NetworkState.Connected)
        {
            _heartbeatElapsed += delta;
            if (_heartbeatElapsed >= _config.HeartbeatIntervalSeconds)
            {
                _heartbeatElapsed -= _config.HeartbeatIntervalSeconds; // 保留余量,避免长期漂移
                SendEnvelope(MsgId.MsgHeartbeat, new Heartbeat());
            }
        }
    }

    // ---------------------------------------------------------------- kcp2k 回调(全部在 Update 调用线程上同步触发)

    /// <summary>kcp2k 通道建立(bare 握手完成)→ 发起应用层握手。</summary>
    private void OnKcpConnected()
    {
        SendEnvelope(MsgId.MsgHandshakeReq, new HandshakeReq
        {
            ProtoVersion = ProtoVersion,
            Token = _token,
        });
    }

    private void OnKcpData(ArraySegment<byte> segment, KcpChannel channel)
    {
        if (State == NetworkState.Disconnected)
            return;

        Envelope envelope;
        try
        {
            envelope = EnvelopeCodec.Decode(segment.AsSpan());
        }
        catch (EnvelopeViolationException e)
        {
            // 协议 §11.3:msg_id 与 payload 不匹配等契约问题按违规处理,直接断开。
            Disconnect($"协议违规:{e.Message}");
            return;
        }

        var payload = EnvelopeCodec.Extract(envelope);
        if (payload == null)
        {
            Disconnect("协议违规:Envelope 缺少 payload");
            return;
        }

        switch (State)
        {
            case NetworkState.Connecting:
                HandleHandshakeResponse(payload);
                return;
            case NetworkState.Connected:
                // 重复的握手应答属于异常但不致命,忽略;其余消息(含服务端 Error)交给订阅者。
                if (payload is HandshakeAck)
                    return;
                Dispatch(envelope);
                return;
            default:
                return;
        }
    }

    private void HandleHandshakeResponse(IMessage payload)
    {
        switch (payload)
        {
            case HandshakeAck ack:
                Uid = ack.Uid;
                Name = ack.Name;
                _heartbeatElapsed = 0f;
                State = NetworkState.Connected;
                Connected?.Invoke();
                return;
            case Error error:
                // 服务端握手校验失败(版本/令牌/重复登录/满员),按协议随后会关闭连接,这里主动断开。
                Disconnect($"握手被拒绝:{error.Code}");
                return;
            default:
                // 协议 §4.1:握手完成前服务端不应发送任何其他消息。
                Disconnect($"握手完成前收到异常消息:{EnvelopeCodec.MsgIdFor(payload.GetType())}");
                return;
        }
    }

    private void OnKcpDisconnected()
    {
        Teardown();
    }

    /// <summary>只读获取当前 KCP 传输层的实时 RTT（ms）与滑动窗口上行重传率（无副作用）。</summary>
    public NetworkTransportStats GetTransportStats()
    {
        var client = _client;
        if (client == null || State != NetworkState.Connected)
            return default;

        var srtt = client.SmoothedRttMs;
        var rttMs = srtt > 0 ? (float)srtt : (float)client.rttInMilliseconds;
        var sent = client.TotalSegmentsSent;
        var retrans = client.TotalRetransmits;
        return new NetworkTransportStats(rttMs, _lossEstimator.CurrentLossRate, sent, retrans);
    }

    /// <summary>统一拆除状态机并上报断线原因:显式断开原因 > kcp2k 错误 > 默认文案。</summary>
    private void Teardown()
    {
        State = NetworkState.Disconnected;
        _client = null;
        _lossEstimator.Reset();
        Disconnected?.Invoke(_disconnectReason ?? _lastError ?? "连接断开");
    }

    private void Dispatch(Envelope envelope)
    {
        if (!_handlers.TryGetValue(envelope.MsgId, out var handlers))
            return;

        foreach (var handler in handlers.ToArray())
            handler(envelope);
    }

    private void SendEnvelope(MsgId msgId, IMessage message)
    {
        var client = _client;
        if (client == null)
            return; // 断线竞态:连接已拆除,静默丢弃

        client.Send(new ArraySegment<byte>(EnvelopeCodec.Encode(msgId, message)), KcpChannel.Reliable);
    }
}
