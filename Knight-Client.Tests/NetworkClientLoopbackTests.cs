#nullable enable
using System;
using System.Collections.Generic;
using System.Linq;
using System.Net;
using System.Net.Sockets;
using System.Threading;
using Google.Protobuf;
using SepCore.Definition;
using SepCore.Runtime.Network;
using kcp2k;
using Xunit;

namespace SepCore.Tests;

// NetworkClient 端到端回路测试:kcp2k KcpServer 假服务端 + 真实 UDP loopback,
// 覆盖 握手 / 心跳 / 双向收发 / 断线(Docs/protocol.md §4)。全部在单线程内泵驱动。
public class NetworkClientLoopbackTests
{
    [Fact]
    public void RoomRequestsUseRealKcpAndWaitForRoomPush()
    {
        using var server = new FakeServer();
        var client = Connect(server);
        Assert.True(PumpUntil(client, server, () => client.State == NetworkState.Connected));
        client.Subscribe<RoomStatePush>(_ => throw new Exception("Disposed subscription invoked")).Dispose();
        using var rooms = new SepCore.UI.Rooms.RoomSessionClient(client);
        System.Collections.Generic.IReadOnlyList<SepCore.UI.Rooms.RoomSummary>? directory = null;
        rooms.RoomsListed += value => directory = value;
        rooms.RequestList();
        Assert.True(PumpUntil(client, server, () => server.Received.Any(e => e.GetRoomListReq != null)));
        server.Send(server.ClientId, new GetRoomListResp
        {
            Rooms = { new SepCore.Definition.RoomSummary { RoomId = 11, HasPassword = true, PlayerCount = 1 } }
        });
        Assert.True(PumpUntil(client, server, () => directory?.Count == 1));
        Assert.Equal("11", directory![0].Id);
        Assert.True(directory[0].HasPassword);
        rooms.Request(new SepCore.UI.Rooms.RoomIntent(SepCore.UI.Rooms.RoomPasswordMode.Create, null, "room-secret"));
        Assert.True(PumpUntil(client, server, () => server.Received.Any(e => e.CreateRoomReq != null)));
        Assert.Equal("room-secret", server.Received.First(e => e.CreateRoomReq != null).CreateRoomReq.Password);
        Assert.True(rooms.IsBusy);
        var state = new RoomStatePush { RoomId = 10, OwnerUid = FakeServer.AckUid };
        state.Members.Add(new RoomMember { Uid = FakeServer.AckUid, Name = "tester" });
        server.Send(server.ClientId, state);
        Assert.True(PumpUntil(client, server, () => rooms.Room?.RoomId == 10));
        rooms.Leave();
        Assert.True(PumpUntil(client, server, () => server.Received.Any(e => e.LeaveRoom != null)));
        rooms.Request(new SepCore.UI.Rooms.RoomIntent(SepCore.UI.Rooms.RoomPasswordMode.Join, "11", "join-secret"));
        Assert.True(PumpUntil(client, server, () => server.Received.Any(e => e.JoinRoomReq != null)));
        var request = server.Received.First(e => e.JoinRoomReq != null).JoinRoomReq;
        Assert.Equal(11u, request.RoomId);
        Assert.Equal("join-secret", request.Password);
        server.Send(server.ClientId, new SepCore.Definition.Error { Code = SepCore.Definition.ErrorCode.ErrRoomFull });
        Assert.True(PumpUntil(client, server, () => !rooms.IsBusy));
        Assert.Null(rooms.Room);
        client.Disconnect();
    }

    static NetworkClientLoopbackTests()
    {
        // 测试里静音 kcp2k 内部日志,避免握手 debug 输出污染测试结果
        kcp2k.Log.Info = _ => { };
        kcp2k.Log.Warning = _ => { };
        kcp2k.Log.Error = _ => { };
    }

    // ---------------------------------------------------------------- 假服务端

    /// <summary>kcp2k 服务端:回 HandshakeAck,记录全部收包,供测试注入下发消息。</summary>
    private sealed class FakeServer : IDisposable
    {
        public const ulong AckUid = 42;
        public const string AckName = "tester";

        public KcpServer Server { get; }
        public ushort Port { get; }
        public List<Envelope> Received { get; } = new();
        public List<int> DisconnectedIds { get; } = new();

        public FakeServer()
        {
            Server = new KcpServer(
                OnConnected: _ => { },
                OnData: HandleData,
                OnDisconnected: id => DisconnectedIds.Add(id),
                OnError: (_, _, _) => { },
                config: new KcpConfig());
            Server.Start(0); // 0 = 系统分配临时端口
            Port = (ushort)((IPEndPoint)Server.LocalEndPoint!).Port;
        }

        /// <summary>首个(唯一)客户端连接 ID;未连接时抛出。</summary>
        public int ClientId
        {
            get
            {
                foreach (var id in Server.connections.Keys)
                    return id;
                throw new InvalidOperationException("测试服务端还没有客户端连接");
            }
        }

        private void HandleData(int connectionId, ArraySegment<byte> segment, KcpChannel channel)
        {
            var envelope = EnvelopeCodec.Decode(segment.AsSpan());
            Received.Add(envelope);

            if (envelope.PayloadCase == Envelope.PayloadOneofCase.HandshakeReq)
                Send(connectionId, new HandshakeAck { Uid = AckUid, Name = AckName });
        }

        public void Send<T>(int connectionId, T message) where T : IMessage
        {
            Server.Send(connectionId,
                new ArraySegment<byte>(EnvelopeCodec.Encode(EnvelopeCodec.MsgIdFor<T>(), message)),
                KcpChannel.Reliable);
        }

        public void Dispose() => Server.Stop();
    }

    // ---------------------------------------------------------------- 泵驱动

    private static void PumpOnce(NetworkClient client, FakeServer? server)
    {
        client.Update(0.033f);
        server?.Server.Tick();
        Thread.Sleep(3);
    }

    private static bool PumpUntil(NetworkClient client, FakeServer? server, Func<bool> done, double timeoutSeconds = 5)
    {
        var deadline = DateTime.UtcNow.AddSeconds(timeoutSeconds);
        while (DateTime.UtcNow < deadline)
        {
            PumpOnce(client, server);
            if (done())
                return true;
        }
        return false;
    }

    private static NetworkClient Connect(FakeServer server, NetworkClientConfig? config = null)
    {
        var client = new NetworkClient(config ?? new NetworkClientConfig());
        client.Connect("127.0.0.1", server.Port, "token-abc");
        return client;
    }

    // ---------------------------------------------------------------- 测试

    [Fact]
    public void Connect_CompletesHandshake_AndCarriesIdentity()
    {
        using var server = new FakeServer();
        var connected = 0;
        var client = Connect(server);
        client.Connected += () => connected++;

        Assert.True(PumpUntil(client, server, () => client.State == NetworkState.Connected), "应在超时前完成握手");
        Assert.Equal(1, connected);
        Assert.Equal(FakeServer.AckUid, client.Uid);
        Assert.Equal(FakeServer.AckName, client.Name);

        // 服务端收到且只收到一条 HandshakeReq(心跳间隔 3s 还未到)
        Assert.Contains(server.Received, e => e.PayloadCase == Envelope.PayloadOneofCase.HandshakeReq);
        Assert.Single(server.Received);
    }

    [Fact]
    public void SendReceive_RoundTripsBusinessMessages()
    {
        using var server = new FakeServer();
        var client = Connect(server);
        Assert.True(PumpUntil(client, server, () => client.State == NetworkState.Connected), "应在超时前完成握手");

        Snapshot? received = null;
        client.Subscribe<Snapshot>(s => received = s);

        // S→C:服务端下发 Snapshot,客户端类型化订阅收到
        server.Send(server.ClientId, new Snapshot { LastInputSeq = 9 });
        Assert.True(PumpUntil(client, server, () => received != null), "客户端应收到 Snapshot");
        Assert.Equal((uint)9, received!.LastInputSeq);

        // C→S:客户端发送业务请求,服务端按 MsgId 收到
        client.Send(new JoinRoomReq { RoomId = 100 });
        Assert.True(PumpUntil(client, server,
                () => server.Received.Any(e => e.PayloadCase == Envelope.PayloadOneofCase.JoinRoomReq)),
            "服务端应收到 JoinRoomReq");
        var joinReq = (JoinRoomReq)EnvelopeCodec.Extract(
            server.Received.First(e => e.PayloadCase == Envelope.PayloadOneofCase.JoinRoomReq))!;
        Assert.Equal(100UL, joinReq.RoomId);
    }

    [Fact]
    public void Heartbeat_IsSentUnconditionally_EveryInterval()
    {
        using var server = new FakeServer();
        var client = Connect(server, new NetworkClientConfig { HeartbeatIntervalSeconds = 0.1f });
        Assert.True(PumpUntil(client, server, () => client.State == NetworkState.Connected), "应在超时前完成握手");

        Assert.True(PumpUntil(client, server,
                () => server.Received.Count(e => e.PayloadCase == Envelope.PayloadOneofCase.Heartbeat) >= 2,
                timeoutSeconds: 2),
            "按缩短的心跳间隔应持续收到 Heartbeat");
    }

    [Fact]
    public void Connect_ToDeadAddress_TimesOutHandshakeAndReportsReason()
    {
        // 黑洞 socket:只绑定不读取、不应答 → kcp2k 通道永远无法建立 → 命中客户端握手超时
        using var blackhole = new Socket(AddressFamily.InterNetwork, SocketType.Dgram, ProtocolType.Udp);
        blackhole.Bind(new IPEndPoint(IPAddress.Loopback, 0));
        var port = (ushort)((IPEndPoint)blackhole.LocalEndPoint!).Port;

        var client = new NetworkClient(new NetworkClientConfig { HandshakeTimeoutSeconds = 0.2f });
        var reasons = new List<string>();
        client.Disconnected += r => reasons.Add(r);
        client.Connect("127.0.0.1", port, "token");

        Assert.True(PumpUntil(client, null, () => client.State == NetworkState.Disconnected, timeoutSeconds: 2),
            "应在握手超时后断开");
        Assert.Equal(NetworkState.Disconnected, client.State);
        Assert.NotEmpty(reasons);
        Assert.StartsWith("握手超时", reasons[0]);
    }

    [Fact]
    public void Disconnect_IsIdempotent_AndReportsExplicitReason()
    {
        using var server = new FakeServer();
        var client = Connect(server);
        Assert.True(PumpUntil(client, server, () => client.State == NetworkState.Connected), "应在超时前完成握手");

        var reasons = new List<string>();
        client.Disconnected += r => reasons.Add(r);
        client.Disconnect("测试主动断开");
        client.Disconnect("测试主动断开"); // 幂等:重复调用不应再触发事件

        Assert.Equal(NetworkState.Disconnected, client.State);
        Assert.Single(reasons);
        Assert.Equal("测试主动断开", reasons[0]);

        // 服务端应感知到断开
        Assert.True(PumpUntil(client, server, () => server.DisconnectedIds.Count > 0), "服务端应感知断开");
    }

    [Fact]
    public void Send_Throws_WhenNotConnected()
    {
        var client = new NetworkClient();
        Assert.Throws<InvalidOperationException>(() => client.Send(new Heartbeat()));
    }
}
