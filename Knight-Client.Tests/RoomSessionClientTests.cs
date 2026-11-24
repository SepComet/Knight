#nullable enable
using System;
using System.Collections.Generic;
using Google.Protobuf;
using SepCore.Definition;
using SepCore.Runtime.Network;
using SepCore.UI.Rooms;
using Xunit;

namespace SepCore.Tests;

public class RoomSessionClientTests
{
    [Fact]
    public void RoomListMapsServerStateAndSerializesRequests()
    {
        var connection = new Connection();
        using var client = new RoomSessionClient(connection);
        IReadOnlyList<SepCore.UI.Rooms.RoomSummary>? snapshot = null;
        client.RoomsListed += rooms => snapshot = rooms;
        client.RequestList();
        client.RequestList();
        client.Request(new RoomIntent(RoomPasswordMode.Join, "1", ""));
        Assert.IsType<GetRoomListReq>(Assert.Single(connection.Sent));
        Assert.True(client.IsListing);
        var response = new GetRoomListResp();
        response.Rooms.Add(new SepCore.Definition.RoomSummary { RoomId = 3, PlayerCount = 4 });
        response.Rooms.Add(new SepCore.Definition.RoomSummary { RoomId = 1, PlayerCount = 1, HasPassword = true });
        response.Rooms.Add(new SepCore.Definition.RoomSummary { RoomId = 2, State = RoomSessionState.RoomRunning });
        response.Rooms.Add(new SepCore.Definition.RoomSummary { RoomId = 4, State = RoomSessionState.RoomClosed });
        connection.Push(response);
        Assert.False(client.IsListing);
        Assert.Equal(3, snapshot!.Count);
        Assert.Equal("1", snapshot[0].Id);
        Assert.True(snapshot[0].HasPassword);
        Assert.True(RoomBrowserModel.CanJoin(snapshot[0]));
        Assert.False(RoomBrowserModel.CanJoin(snapshot[1]));
        Assert.False(RoomBrowserModel.CanJoin(snapshot[2]));
        var model = new RoomBrowserModel();
        model.ReplaceRooms(snapshot);
        model.Select("1");
        client.Request(new RoomIntent(RoomPasswordMode.Join, model.Selected!.Id, "secret"));
        Assert.Equal(1u, Assert.IsType<JoinRoomReq>(connection.Sent[1]).RoomId);
    }

    [Fact]
    public void EmptyListErrorAndDisconnectClearLoadingAndOldData()
    {
        var connection = new Connection();
        using var client = new RoomSessionClient(connection);
        IReadOnlyList<SepCore.UI.Rooms.RoomSummary>? snapshot = null;
        client.RoomsListed += rooms => snapshot = rooms;
        client.RequestList();
        connection.Push(new GetRoomListResp());
        Assert.NotNull(snapshot); Assert.Empty(snapshot); Assert.False(client.IsListing);
        client.RequestList();
        connection.Push(new SepCore.Definition.Error { Code = ErrorCode.ErrInvalidRequest });
        Assert.False(client.IsListing);
        client.RequestList();
        client.Update(10);
        Assert.False(client.IsListing);
        Assert.Equal(NetworkState.Disconnected, connection.State);
        Assert.Empty(snapshot!);
        connection.Push(new GetRoomListResp { Rooms = { new SepCore.Definition.RoomSummary { RoomId = 99 } } });
        Assert.Empty(snapshot!);
    }

    private sealed class Connection : INetworkMessageClient
    {
        public NetworkState State { get; set; } = NetworkState.Connected;
        public ulong Uid => 42;
        public event Action<string>? Disconnected;
        public readonly List<IMessage> Sent = new();
        private readonly Dictionary<Type, Delegate> _handlers = new();
        public void Send<T>(T message) where T : IMessage => Sent.Add(message);
        public IDisposable Subscribe<T>(Action<T> handler) where T : IMessage
        { _handlers[typeof(T)] = handler; return new Cleanup(() => _handlers.Remove(typeof(T))); }
        public void Push<T>(T message) where T : IMessage
        { if (_handlers.TryGetValue(typeof(T), out var handler)) ((Action<T>)handler)(message); }
        public void Disconnect(string reason = "test") { State = NetworkState.Disconnected; Disconnected?.Invoke(reason); }
        private sealed class Cleanup(Action cleanup) : IDisposable { public void Dispose() => cleanup(); }
    }

    private static RoomStatePush Room(uint id = 10) => new()
    {
        RoomId = id, OwnerUid = 42, State = RoomSessionState.RoomWaiting,
        Members = { new RoomMember { Uid = 42, Name = "local" } }
    };

    [Fact]
    public void CreateSendsPasswordAndWaitsForAuthoritativeMembership()
    {
        var connection = new Connection();
        using var client = new RoomSessionClient(connection);
        var rooms = 0; client.RoomChanged += _ => rooms++;
        client.Request(new RoomIntent(RoomPasswordMode.Create, null, "private"));
        Assert.Equal("private", Assert.IsType<CreateRoomReq>(Assert.Single(connection.Sent)).Password);
        Assert.True(client.IsBusy); Assert.Null(client.Room); Assert.Equal(0, rooms);
        client.Request(new RoomIntent(RoomPasswordMode.Create, null, "duplicate"));
        Assert.Single(connection.Sent);
        var unrelated = Room(); unrelated.OwnerUid = 99;
        connection.Push(unrelated); Assert.True(client.IsBusy);
        connection.Push(Room());
        Assert.False(client.IsBusy); Assert.Equal(1, rooms); Assert.Equal(10u, client.Room!.RoomId);
    }

    [Fact]
    public void JoinMatchesRoomAndMapsErrorsBeforeRetry()
    {
        var connection = new Connection();
        using var client = new RoomSessionClient(connection);
        string? error = null; client.Failed += message => error = message;
        client.Request(new RoomIntent(RoomPasswordMode.Join, "10", "secret"));
        var request = Assert.IsType<JoinRoomReq>(Assert.Single(connection.Sent));
        Assert.Equal(10u, request.RoomId); Assert.Equal("secret", request.Password);
        connection.Push(Room(11)); Assert.True(client.IsBusy);
        connection.Push(new SepCore.Definition.Error { Code = ErrorCode.ErrRoomPasswordMismatch });
        Assert.False(client.IsBusy); Assert.Contains("密码", error); Assert.Null(client.Room);
        client.Request(new RoomIntent(RoomPasswordMode.Join, "10", "correct"));
        connection.Push(Room()); Assert.Equal(10u, client.Room!.RoomId);
        client.Leave(); Assert.IsType<LeaveRoom>(connection.Sent[2]); Assert.Null(client.Room);
    }

    [Fact]
    public void DisconnectedInvalidRoomAndOversizedUtf8PasswordNeverSend()
    {
        var connection = new Connection { State = NetworkState.Disconnected };
        using var client = new RoomSessionClient(connection);
        var errors = 0; client.Failed += _ => errors++;
        client.Request(new RoomIntent(RoomPasswordMode.Create, null, ""));
        connection.State = NetworkState.Connected;
        client.Request(new RoomIntent(RoomPasswordMode.Join, "not-a-number", ""));
        client.Request(new RoomIntent(RoomPasswordMode.Create, null, new string('密', 11)));
        Assert.Equal(3, errors); Assert.Empty(connection.Sent); Assert.False(client.IsBusy);
    }

    [Fact]
    public void TimeoutDisconnectsAndDisposedSubscriptionsDoNotDeliver()
    {
        var connection = new Connection();
        var client = new RoomSessionClient(connection);
        client.Request(new RoomIntent(RoomPasswordMode.Join, "10", ""));
        client.Update(10);
        Assert.Equal(NetworkState.Disconnected, connection.State); Assert.False(client.IsBusy);
        connection.Push(Room()); Assert.Null(client.Room);
        client.Dispose(); connection.State = NetworkState.Connected;
        connection.Push(Room()); Assert.Null(client.Room);
    }

    [Fact]
    public void SetReadyStartGameAndGameStartNotificationWorkInWaitingRoom()
    {
        var connection = new Connection();
        using var client = new RoomSessionClient(connection);
        GameStart? started = null;
        client.GameStarted += g => started = g;

        // Join as host (uid 42)
        client.Request(new RoomIntent(RoomPasswordMode.Create, null, ""));
        connection.Push(Room(10));
        connection.Sent.Clear();

        client.SetReady(true);
        var ready = Assert.IsType<SetReady>(Assert.Single(connection.Sent));
        Assert.True(ready.Ready);

        client.StartGame();
        Assert.IsType<StartGame>(connection.Sent[1]);

        connection.Push(new GameStart { MapIds = { 1u, 2u, 3u } });
        Assert.NotNull(started);
        Assert.Equal(new[] { 1u, 2u, 3u }, started!.MapIds);
    }
}
