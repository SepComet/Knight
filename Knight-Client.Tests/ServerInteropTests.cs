#nullable enable
using System;
using System.Collections.Generic;
using System.Linq;
using System.Net.Http;
using System.Text;
using System.Threading;
using SepCore.Definition;
using SepCore.Runtime.Network;
using Xunit;

namespace SepCore.Tests;

// Go 服务端互通测试（真实跨语言线上验证，对应 Docs/protocol.md §2/§3/§4/§5）。
//
// 前置：一个运行中的 knight-server（默认配置即可，token 走真实登录）：
//   cd Knight-Server && docker compose up -d   （或 go run ./cmd/knight-server）
// 设置 KNIGHT_INTEROP_TARGET（如 "127.0.0.1:37777"）后测试才会执行；
// HTTP 基址默认取同主机 37780，可用 KNIGHT_INTEROP_HTTP_TARGET 覆盖。
public class ServerInteropTests
{
    static ServerInteropTests()
    {
        // 静音 kcp2k 内部日志
        kcp2k.Log.Info = _ => { };
        kcp2k.Log.Warning = _ => { };
        kcp2k.Log.Error = _ => { };
    }

    private static string? Target => Environment.GetEnvironmentVariable("KNIGHT_INTEROP_TARGET");

    /// <summary>环境门控：未设置 KNIGHT_INTEROP_TARGET 时跳过。</summary>
    private sealed class InteropFactAttribute : FactAttribute
    {
        public InteropFactAttribute()
        {
            if (Target == null)
                Skip = "未设置 KNIGHT_INTEROP_TARGET，跳过 Go 服务端互通测试";
        }
    }

    private static (string host, ushort port) SplitTarget()
    {
        var sep = Target!.LastIndexOf(':');
        Assert.True(sep > 0, $"KNIGHT_INTEROP_TARGET 应为 host:port，实际 {Target}");
        return (Target[..sep], ushort.Parse(Target[(sep + 1)..]));
    }

    /// <summary>HTTP 基址：KNIGHT_INTEROP_HTTP_TARGET 优先，缺省用 KCP 目标的同主机 37780。</summary>
    private static string HttpBase()
    {
        var httpTarget = Environment.GetEnvironmentVariable("KNIGHT_INTEROP_HTTP_TARGET");
        if (!string.IsNullOrEmpty(httpTarget))
            return $"http://{httpTarget}";
        var (host, _) = SplitTarget();
        return $"http://{host}:37780";
    }

    /// <summary>泵驱动直到条件满足或超时（真实时钟，服务端在真实时间运行）。</summary>
    private static bool PumpUntil(Func<bool> condition, TimeSpan timeout, params NetworkClient[] clients)
    {
        var deadline = DateTime.UtcNow + timeout;
        while (DateTime.UtcNow < deadline)
        {
            foreach (var c in clients)
                c.Update(0.016f);
            if (condition())
                return true;
            Thread.Sleep(10);
        }
        return condition();
    }

    /// <summary>纯泵指定时长（存活性观察用）。</summary>
    private static void Pump(TimeSpan duration, params NetworkClient[] clients)
    {
        var deadline = DateTime.UtcNow + duration;
        while (DateTime.UtcNow < deadline)
        {
            foreach (var c in clients)
                c.Update(0.016f);
            Thread.Sleep(10);
        }
    }

    /// <summary>已订阅收件箱的客户端：按类型收集入站消息。</summary>
    private sealed class Inbox : IDisposable
    {
        public NetworkClient Client { get; } = new();
        public Queue<RoomStatePush> Pushes { get; } = new();
        public Queue<GameStart> Starts { get; } = new();
        public Queue<Error> Errors { get; } = new();
        public string? DisconnectReason;

        public Inbox(string host, ushort port, string token)
        {
            Client.Subscribe<RoomStatePush>(p => Pushes.Enqueue(p));
            Client.Subscribe<GameStart>(g => Starts.Enqueue(g));
            Client.Subscribe<Error>(e => Errors.Enqueue(e));
            Client.Disconnected += reason => DisconnectReason = reason;
            Client.Connect(host, port, token);
        }

        public void Dispose() => Client.Disconnect("测试结束");
    }

    // ---------------------------------------------------------------- HTTP 账号

    /// <summary>注册 + 登录，返回 (uid, name, token)。密码错误返回 401 的契约在失败用例中覆盖。</summary>
    private static async System.Threading.Tasks.Task<(ulong uid, string name, string token)> RegisterAndLoginAsync(
        HttpClient http, string name)
    {
        var body = new StringContent(
            System.Text.Json.JsonSerializer.Serialize(new { name, password = "interop-pass-123" }),
            Encoding.UTF8, "application/json");
        using var reg = await http.PostAsync("/api/v1/register", body);
        Assert.Equal(System.Net.HttpStatusCode.OK, reg.StatusCode);
        using var login = await http.PostAsync("/api/v1/login", body);
        Assert.Equal(System.Net.HttpStatusCode.OK, login.StatusCode);
        var json = await login.Content.ReadAsStringAsync();
        using var doc = System.Text.Json.JsonDocument.Parse(json);
        var root = doc.RootElement;
        return (root.GetProperty("uid").GetUInt64(),
                root.GetProperty("name").GetString()!,
                root.GetProperty("token").GetString()!);
    }

    [InteropFact]
    public async System.Threading.Tasks.Task Connects_WithRealLoginToken_AndStaysConnected()
    {
        var (host, port) = SplitTarget();
        using var http = new HttpClient { BaseAddress = new Uri(HttpBase()) };
        var (uid, name, token) = await RegisterAndLoginAsync(http, $"interop-conn-{Environment.TickCount64}");

        var client = new NetworkClient();
        var connected = new ManualResetEventSlim(false);
        string? disconnectReason = null;
        client.Connected += () => connected.Set();
        client.Disconnected += reason => disconnectReason = reason;

        client.Connect(host, port, token);
        try
        {
            // 握手：真实 token 校验 → HandshakeAck{uid, name}
            Assert.True(PumpUntil(() => connected.IsSet, TimeSpan.FromSeconds(10), client),
                $"10s 内未完成握手，disconnectReason={disconnectReason}");
            Assert.Equal(uid, client.Uid);
            Assert.Equal(name, client.Name);

            // 心跳存活性：持续 > 2 个心跳周期（3s/个）连接不断开
            // （服务端 10s 无 Envelope 踢出；本测试覆盖 3s 心跳 + kcp2k 1s Ping 的跨语言保活）
            Pump(TimeSpan.FromSeconds(7), client);
            Assert.Equal(NetworkState.Connected, client.State);
            Assert.Null(disconnectReason);
        }
        finally
        {
            client.Disconnect("测试结束");
        }
    }

    [InteropFact]
    public async System.Threading.Tasks.Task FullLobbyFlow_Register_Login_Create_Join_Ready_Start()
    {
        var (host, port) = SplitTarget();
        using var http = new HttpClient { BaseAddress = new Uri(HttpBase()) };
        var stamp = Environment.TickCount64;

        // 1. HTTP 注册 + 登录两个账号，拿真实 token
        var (uidA, nameA, tokenA) = await RegisterAndLoginAsync(http, $"interop-a-{stamp}");
        var (uidB, _, tokenB) = await RegisterAndLoginAsync(http, $"interop-b-{stamp}");
        Assert.NotEqual(uidA, uidB);

        // 2. 双客户端用真实 token 完成 KCP 握手
        using var a = new Inbox(host, port, tokenA);
        using var b = new Inbox(host, port, tokenB);
        Assert.True(PumpUntil(() => a.Client.State == NetworkState.Connected
                            && b.Client.State == NetworkState.Connected,
            TimeSpan.FromSeconds(10), a.Client, b.Client),
            $"握手超时：a={a.Client.State}({a.DisconnectReason}) b={b.Client.State}({b.DisconnectReason})");
        Assert.Equal(uidA, a.Client.Uid);
        Assert.Equal(nameA, a.Client.Name);
        Assert.Equal(uidB, b.Client.Uid);

        // 3. A 建房 → RoomStatePush（含服务端生成的房间号）
        a.Client.Send(new CreateRoomReq());
        Assert.True(PumpUntil(() => a.Pushes.Count > 0, TimeSpan.FromSeconds(5), a.Client, b.Client),
            "未收到建房 RoomStatePush");
        var created = a.Pushes.Dequeue();
        Assert.Equal(uidA, created.OwnerUid);
        Assert.Equal(RoomSessionState.RoomWaiting, created.State);
        Assert.NotEqual(0U, created.RoomId);

        // 4. B 按房号加入 → 双方收到 2 人成员表
        b.Client.Send(new JoinRoomReq { RoomId = created.RoomId });
        Assert.True(PumpUntil(() => a.Pushes.Count > 0 && b.Pushes.Count > 0
                            && a.Pushes.Peek().Members.Count == 2 && b.Pushes.Peek().Members.Count == 2,
            TimeSpan.FromSeconds(5), a.Client, b.Client),
            "加入后未收到 2 人成员表");
        a.Pushes.Dequeue();
        b.Pushes.Dequeue();

        // 5. 双准备 → 任一推送全员就绪即达成（每次 SetReady 都会触发一次全量广播）
        a.Client.Send(new SetReady { Ready = true });
        b.Client.Send(new SetReady { Ready = true });
        Assert.True(PumpUntil(() =>
            a.Pushes.Any(p => p.Members.All(m => m.Ready)) &&
            b.Pushes.Any(p => p.Members.All(m => m.Ready)),
            TimeSpan.FromSeconds(5), a.Client, b.Client),
            "准备状态未同步");
        a.Pushes.Clear();
        b.Pushes.Clear();

        // 6. 房主开局 → 双方收 GameStart{[1,2,3]} + RoomStatePush{RUNNING}
        a.Client.Send(new StartGame());
        Assert.True(PumpUntil(() => a.Starts.Count > 0 && b.Starts.Count > 0,
            TimeSpan.FromSeconds(5), a.Client, b.Client),
            "未收到 GameStart");
        Assert.Equal(new[] { 1U, 2U, 3U }, a.Starts.Dequeue().MapIds);
        Assert.Equal(new[] { 1U, 2U, 3U }, b.Starts.Dequeue().MapIds);
        Assert.True(PumpUntil(() => a.Pushes.Count > 0 && b.Pushes.Count > 0
                            && a.Pushes.Peek().State == RoomSessionState.RoomRunning,
            TimeSpan.FromSeconds(5), a.Client, b.Client),
            "未收到 RUNNING 广播");

        // 全程无错误应答
        Assert.Empty(a.Errors);
        Assert.Empty(b.Errors);
    }
}
