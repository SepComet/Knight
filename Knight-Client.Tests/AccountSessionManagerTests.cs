#nullable enable
using System;
using System.Collections.Generic;
using System.Net;
using System.Net.Http;
using System.Threading;
using System.Threading.Tasks;
using Google.Protobuf;
using SepCore.Runtime.Network;
using SepCore.UI.Auth;
using SepCore.UI.Auth.Services;
using Xunit;

namespace SepCore.Tests;

public class AccountSessionManagerTests
{
    private sealed class Store : ILoginStore
    {
        public CachedLogin? Value;
        public int Writes, Clears;
        public CachedLogin? Load() => Value;
        public void Save(CachedLogin value) { Value = value; Writes++; }
        public void Clear() { Value = null; Clears++; }
    }
    private sealed class Connection : INetworkSessionClient
    {
        public NetworkState State { get; private set; }
        public ulong Uid => 42;
        public string Name => "ServerName";
        public event Action? Connected;
        public event Action<string>? Disconnected;
        public bool Reject;
        public int Attempts;
        public void Connect(string host, ushort port, string token)
        {
            Attempts++;
            State = NetworkState.Connecting;
            if (Reject) Disconnect("握手被拒绝:ErrInvalidToken");
            else { State = NetworkState.Connected; Connected?.Invoke(); }
        }
        public void Disconnect(string reason = "test")
        {
            if (State == NetworkState.Disconnected) return;
            State = NetworkState.Disconnected; Disconnected?.Invoke(reason);
        }
        public void Send<T>(T message) where T : IMessage { }
        public IDisposable Subscribe<T>(Action<T> handler) where T : IMessage => new Empty();
        private sealed class Empty : IDisposable { public void Dispose() { } }
    }
    private sealed class Handler(Func<HttpRequestMessage, Task<HttpResponseMessage>> send) : HttpMessageHandler
    {
        protected override Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, CancellationToken token) => send(request);
    }
    private static HttpResponseMessage LoginResponse() => new(HttpStatusCode.OK)
    { Content = new StringContent("{\"uid\":\"42\",\"name\":\"Local\",\"token\":\"session-token\"}") };
    private static AccountSessionManager Manager(HttpClient http, Store store, Connection network)
        => new(new AuthApi(http, new Uri("http://unit.test/")), store, network, "unit.test", 37777, "endpoint");

    [Fact]
    public async Task MissingOrDifferentEndpointCachePromptsWithoutSendingCredentials()
    {
        using var http = new HttpClient(new Handler(_ => throw new Exception("Unexpected HTTP request")));
        var store = new Store { Value = new CachedLogin(42, "Local", "token", "different") };
        var network = new Connection();
        using var manager = Manager(http, store, network);
        var prompts = 0; manager.LoginRequired += () => prompts++;
        await manager.InitializeAsync();
        Assert.Equal(1, prompts); Assert.Equal(0, network.Attempts);
        Assert.False(manager.IsAuthenticated);
    }

    [Fact]
    public async Task CachedTokenAuthenticatesWithoutPasswordAndLogoutClearsIt()
    {
        using var http = new HttpClient(new Handler(_ => throw new Exception("Auto login must use the token")));
        var store = new Store { Value = new CachedLogin(42, "OldName", "token", "endpoint") };
        var network = new Connection();
        using var manager = Manager(http, store, network);
        await manager.InitializeAsync();
        Assert.True(manager.IsAuthenticated);
        Assert.Equal("ServerName", manager.Session!.Name);
        Assert.Equal(1, store.Writes);
        Assert.True(manager.Logout());
        Assert.Null(store.Value); Assert.False(manager.IsAuthenticated);
        Assert.Equal(NetworkState.Disconnected, network.State);
    }

    [Fact]
    public async Task RejectedCachedTokenIsRemovedAndLoginPrompted()
    {
        using var http = new HttpClient(new Handler(_ => throw new Exception("Unexpected HTTP")));
        var store = new Store { Value = new CachedLogin(42, "Local", "old-token", "endpoint") };
        var network = new Connection { Reject = true };
        using var manager = Manager(http, store, network);
        var prompts = 0; manager.LoginRequired += () => prompts++;
        await manager.InitializeAsync();
        Assert.Null(store.Value); Assert.Equal(1, prompts);
        Assert.Contains("失效", manager.Message); Assert.False(manager.IsBusy);
    }

    [Fact]
    public async Task RegistrationContinuesToHttpLoginAndHandshakeBeforeSaving()
    {
        var requests = new List<string>();
        using var http = new HttpClient(new Handler(request =>
        {
            requests.Add(request.RequestUri!.AbsolutePath);
            return Task.FromResult(requests.Count == 1
                ? new HttpResponseMessage(HttpStatusCode.OK) { Content = new StringContent("{\"uid\":\"42\"}") }
                : LoginResponse());
        }));
        var store = new Store(); var network = new Connection();
        using var manager = Manager(http, store, network);
        await manager.SubmitAsync(new AuthSubmission(AuthMode.Register, "Local", "password"));
        Assert.Equal(new[] { "/api/v1/register", "/api/v1/login" }, requests);
        Assert.True(manager.IsAuthenticated); Assert.Equal(1, network.Attempts);
        Assert.Equal("session-token", store.Value!.Token);
    }

    [Fact]
    public async Task CancelledAttemptCannotCommitLateHttpResponse()
    {
        var pending = new TaskCompletionSource<HttpResponseMessage>(TaskCreationOptions.RunContinuationsAsynchronously);
        using var http = new HttpClient(new Handler(_ => pending.Task));
        var store = new Store(); var network = new Connection();
        using var manager = Manager(http, store, network);
        var task = manager.SubmitAsync(new AuthSubmission(AuthMode.Login, "Local", "password"));
        Assert.True(manager.IsBusy);
        manager.Cancel();
        pending.SetResult(LoginResponse());
        await task;
        Assert.Equal(0, network.Attempts); Assert.Null(store.Value);
        Assert.Equal(AccountStatus.SignedOut, manager.Status);
    }
}
