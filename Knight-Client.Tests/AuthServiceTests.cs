#nullable enable
using System;
using System.IO;
using System.Net;
using System.Net.Http;
using System.Text;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using SepCore.UI.Auth.Services;
using Xunit;

namespace SepCore.Tests;

public class AuthServiceTests
{
    private sealed class Handler(Func<HttpRequestMessage, CancellationToken, Task<HttpResponseMessage>> handle) : HttpMessageHandler
    {
        protected override Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, CancellationToken token) => handle(request, token);
    }
    private static HttpResponseMessage Response(string body, HttpStatusCode status = HttpStatusCode.OK)
        => new(status) { Content = new StringContent(body, Encoding.UTF8, "application/json") };

    [Fact]
    public async Task LoginUsesContractAndPreservesFullUint64Uid()
    {
        using var client = new HttpClient(new Handler(async (request, token) =>
        {
            Assert.Equal(HttpMethod.Post, request.Method);
            Assert.Equal("/api/v1/login", request.RequestUri!.AbsolutePath);
            using var body = JsonDocument.Parse(await request.Content!.ReadAsStringAsync(token));
            Assert.Equal("玩家", body.RootElement.GetProperty("name").GetString());
            Assert.Equal("secret", body.RootElement.GetProperty("password").GetString());
            return Response("{\"uid\":\"18446744073709551615\",\"name\":\"玩家\",\"token\":\"session-token\"}");
        }));
        var api = new AuthApi(client, new Uri("http://localhost:37780"));
        var session = await api.LoginAsync("玩家", "secret");
        Assert.Equal(ulong.MaxValue, session.Uid);
        Assert.Equal("session-token", session.Token);
    }

    [Fact]
    public async Task RegisterThenLoginUsesSeparateExistingEndpoints()
    {
        var calls = 0;
        using var client = new HttpClient(new Handler((request, _) =>
        {
            calls++;
            Assert.Equal(calls == 1 ? "/api/v1/register" : "/api/v1/login", request.RequestUri!.AbsolutePath);
            return Task.FromResult(calls == 1 ? Response("{\"uid\":\"42\"}") :
                Response("{\"uid\":\"42\",\"name\":\"player\",\"token\":\"token\"}"));
        }));
        var api = new AuthApi(client, new Uri("http://localhost:37780"));
        await api.RegisterAsync("player", "secret");
        Assert.Equal(42ul, (await api.LoginAsync("player", "secret")).Uid);
        Assert.Equal(2, calls);
    }

    [Theory]
    [InlineData("{\"uid\":42,\"name\":\"a\",\"token\":\"t\"}")]
    [InlineData("{\"uid\":\"0\",\"name\":\"a\",\"token\":\"t\"}")]
    [InlineData("{\"uid\":\"42\",\"name\":\"a\"}")]
    [InlineData("not json")]
    [InlineData("[]")]
    public async Task MalformedLoginNeverProducesSession(string json)
    {
        using var client = new HttpClient(new Handler((_, _) => Task.FromResult(Response(json))));
        var api = new AuthApi(client, new Uri("http://localhost"));
        await Assert.ThrowsAsync<AuthApiException>(() => api.LoginAsync("a", "secret"));
    }

    [Theory]
    [InlineData(HttpStatusCode.Unauthorized)]
    [InlineData(HttpStatusCode.Conflict)]
    public async Task HttpErrorsDoNotExposeServerBody(HttpStatusCode status)
    {
        using var client = new HttpClient(new Handler((_, _) => Task.FromResult(Response("sensitive-server-body", status))));
        var api = new AuthApi(client, new Uri("http://localhost"));
        var error = await Assert.ThrowsAsync<AuthApiException>(() => api.LoginAsync("a", "secret"));
        Assert.Equal(status, error.StatusCode);
        Assert.DoesNotContain("sensitive-server-body", error.Message);
        Assert.DoesNotContain("secret", error.Message);
    }

    [Fact]
    public async Task CancellationIsPreserved()
    {
        using var client = new HttpClient(new Handler(async (_, token) =>
        {
            await Task.Delay(Timeout.Infinite, token);
            return Response("{}");
        }));
        using var cancellation = new CancellationTokenSource();
        cancellation.Cancel();
        await Assert.ThrowsAnyAsync<OperationCanceledException>(() =>
            new AuthApi(client, new Uri("http://localhost")).LoginAsync("a", "secret", cancellation.Token));
    }

    [Fact]
    public async Task ExcessiveResponseIsRejected()
    {
        using var client = new HttpClient(new Handler((_, _) => Task.FromResult(Response(new string('x', 65537)))));
        await Assert.ThrowsAsync<AuthApiException>(() => new AuthApi(client, new Uri("http://localhost")).LoginAsync("a", "secret"));
    }

    [Fact]
    public void CacheRoundTripCorruptionAndClear()
    {
        var directory = Path.Combine(Path.GetTempPath(), "knight-auth-tests-" + Guid.NewGuid().ToString("N"));
        var path = Path.Combine(directory, "login.json");
        try
        {
            var store = new JsonLoginStore(path);
            Assert.Null(store.Load());
            var session = new CachedLogin(ulong.MaxValue, "玩家", "session-token", "http://localhost:37780|127.0.0.1:37777");
            store.Save(session);
            Assert.Equal(session, store.Load());
            Assert.DoesNotContain("password", File.ReadAllText(path), StringComparison.OrdinalIgnoreCase);
            Assert.Throws<ArgumentException>(() => store.Save(session with { Token = new string('x', 40000) }));
            Assert.Equal(session, store.Load());
            File.WriteAllText(path, "broken cache");
            Assert.Null(store.Load());
            Assert.False(File.Exists(path));
            store.Save(session);
            store.Clear(); store.Clear();
            Assert.Null(store.Load());
        }
        finally { if (Directory.Exists(directory)) Directory.Delete(directory, true); }
    }
}
