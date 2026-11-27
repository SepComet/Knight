#nullable enable
using System;
using System.Collections.Generic;
using System.Net;
using System.Net.Http;
using HttpClient = System.Net.Http.HttpClient;
using System.Text;
using System.Threading;
using System.Threading.Tasks;
using Godot;
using Google.Protobuf;
using SepCore.Battle;
using SepCore.Definition;
using SepCore.Gameplay.Player;
using SepCore.Runtime.Network;
using SepCore.UI.Battle;
using SepCore.UI.Results;
using Xunit;

namespace SepCore.Tests;

public sealed class BattleM3Tests
{
    [Fact]
    public void BattleMapCatalogHasNineIsomorphicRoomTemplates()
    {
        Assert.Equal(9, BattleMapCatalog.AllMaps.Count);
        var ids = new HashSet<uint>();
        foreach (var map in BattleMapCatalog.AllMaps)
        {
            Assert.True(ids.Add(map.MapId), $"Duplicate map id {map.MapId}");
            Assert.Equal(30f, map.WidthTiles);
            Assert.Equal(20f, map.HeightTiles);
            Assert.True(map.Walls.Count >= 6, $"Map {map.MapId} should have 4 boundary walls + obstacles");
            Assert.Equal(BattleMapCatalog.DefaultExitZone, map.ExitZone);
            Assert.True(map.ExitZone.ContainsPoint(27.5f, 10f));
            Assert.False(map.ExitZone.ContainsPoint(10f, 10f));
            Assert.Same(map, BattleMapCatalog.Get(map.MapId));
        }

        Assert.Equal(BattleRoomKind.Start, BattleMapCatalog.Map1.Kind);
        Assert.Equal(BattleRoomKind.Supply, BattleMapCatalog.Map6.Kind);
        Assert.Equal(BattleRoomKind.Supply, BattleMapCatalog.Map7.Kind);
        Assert.Equal(BattleRoomKind.Elite, BattleMapCatalog.Map8.Kind);
        Assert.Equal(BattleRoomKind.Boss, BattleMapCatalog.Map9.Kind);
    }

    [Fact]
    public void ProtoVersionIsSixAndSkillEventRoundTrips()
    {
        Assert.Equal(6u, NetworkClient.ProtoVersion);
        Assert.Equal(MsgId.MsgSkillEvent, EnvelopeCodec.MsgIdFor<SkillEvent>());

        var original = new SkillEvent
        {
            CasterId = 42,
            SkillId = 1,
            PosX = 12.5f,
            PosY = 9.25f,
            DirAngle = 45f,
            CooldownSec = 5.0f,
        };

        var bytes = EnvelopeCodec.Encode(MsgId.MsgSkillEvent, original);
        var decoded = EnvelopeCodec.Decode(bytes);
        var extracted = Assert.IsType<SkillEvent>(EnvelopeCodec.Extract(decoded));
        Assert.Equal(42u, extracted.CasterId);
        Assert.Equal(1u, extracted.SkillId);
        Assert.Equal(5.0f, extracted.CooldownSec);
    }

    [Fact]
    public void PlayerInputAndSyntheticOverrideTriggerSkillEdgeOnce()
    {
        var buffer = new PlayerInputBuffer();
        buffer.RequestSkill();
        var first = buffer.Consume(0f);
        var second = buffer.Consume(0f);
        Assert.True(first.SkillPressed);
        Assert.False(second.SkillPressed);

        var synthetic = new SyntheticInputOverride();
        synthetic.Set(Vector2.Right, aimRadians: 0.5f, attackHeld: true, interact: false, durationSeconds: 1.0, skill: true);
        Assert.True(synthetic.TrySample(0f, out var s1));
        Assert.True(s1.SkillPressed);
        Assert.True(s1.AttackHeld);
        Assert.True(synthetic.TrySample(0f, out var s2));
        Assert.False(s2.SkillPressed);
        Assert.True(s2.AttackHeld);
    }

    [Fact]
    public void HudAndResultModelsFormatDungeonMinimapSkillBossAndSummary()
    {
        var progress = new DungeonProgressModel(1, 4, 3, "十字走廊", true, new uint[] { 1, 3, 6, 9 });
        Assert.Equal("房间 2/4 · 十字走廊", progress.ProgressText);
        Assert.Equal("出口已开启 →", progress.DoorText);
        Assert.Equal("[1✓]─[2★]─[3]─[B]", progress.MinimapText);

        var skillReady = new SkillHudModel("冲刺斩 [Space]", 0f, 5f, 0u);
        Assert.True(skillReady.IsReady);
        Assert.Equal("就绪", skillReady.StatusText);

        var skillCd = new SkillHudModel("冲刺斩 [Space]", 2.4f, 5f, 4u);
        Assert.False(skillCd.IsReady);
        Assert.Equal("2.4s", skillCd.StatusText);

        var bossP2 = new BossHudModel(true, "地牢领主", 200, 450, true);
        Assert.Contains("二阶段", bossP2.DisplayTitle);

        var result = new GameResultModel();
        result.SetMatchSummary(9001, isVictory: true, durationSec: 125);
        Assert.Contains("胜利", result.TitleText);
        Assert.Contains("9001", result.SummaryText);
        Assert.Contains("02:05", result.SummaryText);

        var defeat = new GameResultModel();
        defeat.SetMatchSummary(0, isVictory: false, durationSec: 0);
        Assert.False(defeat.IsVictory);
        Assert.Contains("失败", defeat.TitleText);
        Assert.Equal("耗时 00:00", defeat.SummaryText);
    }

    [Fact]
    public void BattleSessionTracksM3EventsAndSuppressesRoomClosedAfterGameResult()
    {
        var fakeNet = new FakeNetworkClient { Uid = 10001 };
        using var session = new BattleSessionClient(fakeNet);

        DamageEvent? gotDamage = null;
        DoorStateValue? gotDoor = null;
        ItemPicked? gotItem = null;
        SkillEvent? gotSkill = null;
        GameResult? gotResult = null;
        string? endedReason = null;

        session.DamageEventReceived += e => gotDamage = e;
        session.DoorStateChanged += d => gotDoor = d;
        session.ItemPickedReceived += i => gotItem = i;
        session.SkillEventReceived += s => gotSkill = s;
        session.GameResultReceived += r => gotResult = r;
        session.SessionEnded += reason => endedReason = reason;

        var start = new GameStart { Seed = 123456789UL };
        start.MapIds.AddRange(new uint[] { 1, 2, 6, 9 });
        var firstRoom = new RoomTransition { MapId = 1, RoomIndex = 0, TotalRooms = 4 };
        session.Start(start, firstRoom);

        Assert.Equal(123456789UL, session.DungeonSeed);
        Assert.Equal(0u, session.CurrentRoomIndex);
        Assert.Equal(4u, session.TotalRooms);
        Assert.False(session.DoorOpen);
        Assert.True(session.ConfirmRoomLoaded(1));

        fakeNet.Deliver(new DamageEvent { AttackerId = 1, TargetId = 2, Damage = 20, NewHp = 40 });
        fakeNet.Deliver(new DoorState { State = DoorStateValue.DoorOpen });
        fakeNet.Deliver(new ItemPicked { ItemEntityId = 9, Uid = 10001, HealedHp = 35, ItemConfigId = 1 });
        fakeNet.Deliver(new SkillEvent { CasterId = 1, SkillId = 1, CooldownSec = 5f });

        Assert.Equal(20, gotDamage?.Damage);
        Assert.Equal(DoorStateValue.DoorOpen, gotDoor);
        Assert.True(session.DoorOpen);
        Assert.Equal(1u, gotItem?.ItemConfigId);
        Assert.Equal(1u, gotSkill?.SkillId);

        fakeNet.Deliver(new GameResult { MatchId = 777, Outcome = GameOutcome.OutcomeVictory, DurationSec = 95 });
        Assert.True(session.IsMatchFinished);
        Assert.Equal(777UL, gotResult?.MatchId);
        Assert.Null(session.SampleAndSendInput(1f, 0f, 0f, (uint)InputButtons.BtnSkill));

        // 结算后收到 RoomClosed 不应强行触发被动 SessionEnded 覆盖结算页
        fakeNet.Deliver(new RoomStatePush { RoomId = 1, State = RoomSessionState.RoomClosed });
        Assert.Null(endedReason);
    }

    [Fact]
    public async Task MatchHistoryApiSendsBearerTokenAndParsesJsonEntries()
    {
        string? capturedPath = null;
        string? capturedAuth = null;
        var json = """
        [
          {
            "match_id": "99887766",
            "outcome": 1,
            "duration_sec": 142,
            "seed": "424242",
            "character_id": 1,
            "weapon_id": 1,
            "kills": 12,
            "damage_dealt": 950,
            "damage_taken": 60,
            "alive": true,
            "finished_at": "2026-11-27T12:00:00Z"
          }
        ]
        """;

        using var http = new HttpClient(new StubHandler((req, _) =>
        {
            capturedPath = req.RequestUri?.AbsolutePath;
            capturedAuth = req.Headers.Authorization?.ToString();
            return Task.FromResult(new HttpResponseMessage(HttpStatusCode.OK)
            {
                Content = new StringContent(json, Encoding.UTF8, "application/json"),
            });
        }));

        var api = new MatchHistoryApi(http, new Uri("http://127.0.0.1:37780/"));
        var entries = await api.FetchAsync(10001UL, "tok-abc");

        Assert.Equal("/api/v1/users/10001/matches", capturedPath);
        Assert.Equal("Bearer tok-abc", capturedAuth);
        var entry = Assert.Single(entries);
        Assert.Equal("99887766", entry.MatchId);
        Assert.True(entry.IsVictory);
        Assert.Equal("02:22", entry.FormattedDuration);
        Assert.Equal(12, entry.Kills);
        Assert.Equal(950, entry.DamageDealt);
        Assert.Equal(60, entry.DamageTaken);
        Assert.True(entry.Alive);
    }

    [Theory]
    [InlineData(HttpStatusCode.Unauthorized, "登录态已失效")]
    [InlineData(HttpStatusCode.NotFound, "战绩接口不可用")]
    [InlineData(HttpStatusCode.InternalServerError, "获取历史战绩失败")]
    public async Task MatchHistoryApiThrowsDescriptiveExceptionOnHttpError(HttpStatusCode status, string expectedSubstring)
    {
        using var http = new HttpClient(new StubHandler((_, _) =>
            Task.FromResult(new HttpResponseMessage(status))));
        var api = new MatchHistoryApi(http, new Uri("http://127.0.0.1:37780/"));

        var ex = await Assert.ThrowsAsync<MatchHistoryApiException>(() => api.FetchAsync(10001UL, "tok-abc"));
        Assert.Equal(status, ex.StatusCode);
        Assert.Contains(expectedSubstring, ex.Message);
    }

    [Fact]
    public async Task MatchHistoryApiRejectsInvalidJsonAndOversizedResponse()
    {
        using var invalidJsonHttp = new HttpClient(new StubHandler((_, _) =>
            Task.FromResult(new HttpResponseMessage(HttpStatusCode.OK)
            {
                Content = new StringContent("{not-valid-json", Encoding.UTF8, "application/json"),
            })));
        var api1 = new MatchHistoryApi(invalidJsonHttp, new Uri("http://127.0.0.1:37780/"));
        var jsonEx = await Assert.ThrowsAsync<MatchHistoryApiException>(() => api1.FetchAsync(10001UL, "tok-abc"));
        Assert.Contains("格式错误", jsonEx.Message);

        var oversized = new string('a', 300 * 1024);
        using var oversizedHttp = new HttpClient(new StubHandler((_, _) =>
            Task.FromResult(new HttpResponseMessage(HttpStatusCode.OK)
            {
                Content = new StringContent(oversized, Encoding.UTF8, "application/json"),
            })));
        var api2 = new MatchHistoryApi(oversizedHttp, new Uri("http://127.0.0.1:37780/"));
        var sizeEx = await Assert.ThrowsAsync<MatchHistoryApiException>(() => api2.FetchAsync(10001UL, "tok-abc"));
        Assert.Contains("响应过大", sizeEx.Message);
    }

    private sealed class StubHandler(Func<HttpRequestMessage, CancellationToken, Task<HttpResponseMessage>> responder)
        : HttpMessageHandler
    {
        protected override Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, CancellationToken cancellationToken)
            => responder(request, cancellationToken);
    }

    private sealed class FakeNetworkClient : INetworkMessageClient
    {
        private readonly Dictionary<Type, List<Action<IMessage>>> _handlers = new();
        public NetworkState State { get; set; } = NetworkState.Connected;
        public ulong Uid { get; set; } = 1;
        public string Name { get; set; } = "Tester";
        public event Action? Connected;
        public event Action<string>? Disconnected;

        public void Send<T>(T message) where T : IMessage { }
        public void Disconnect(string reason = "") => Disconnected?.Invoke(reason);

        public IDisposable Subscribe<T>(Action<T> handler) where T : IMessage
        {
            if (!_handlers.TryGetValue(typeof(T), out var list))
                _handlers[typeof(T)] = list = new List<Action<IMessage>>();
            Action<IMessage> wrapped = msg => handler((T)msg);
            list.Add(wrapped);
            return new ActionDisposable(() => list.Remove(wrapped));
        }

        public NetworkTransportStats GetTransportStats() => default;

        public void Deliver<T>(T message) where T : IMessage
        {
            if (!_handlers.TryGetValue(typeof(T), out var list))
                return;
            foreach (var h in list.ToArray())
                h(message);
        }

        public void RaiseConnected() => Connected?.Invoke();
        public void RaiseDisconnected(string reason) => Disconnected?.Invoke(reason);

        private sealed class ActionDisposable(Action onDispose) : IDisposable
        {
            private Action? _onDispose = onDispose;
            public void Dispose() { _onDispose?.Invoke(); _onDispose = null; }
        }
    }
}
