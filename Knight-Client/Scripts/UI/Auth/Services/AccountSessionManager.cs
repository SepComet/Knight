#nullable enable
using System;
using System.Threading;
using System.Threading.Tasks;
using SepCore.Runtime.Network;

namespace SepCore.UI.Auth.Services;

public enum AccountStatus { SignedOut, Authenticating, SignedIn }

/// <summary>HTTP credentials exchange, cached-token resume, and authoritative KCP login.</summary>
public sealed class AccountSessionManager : IDisposable
{
    private readonly AuthApi _api;
    private readonly ILoginStore _store;
    private readonly INetworkSessionClient _network;
    private readonly string _host, _scope;
    private readonly ushort _port;
    private CancellationTokenSource? _operation;
    private int _revision;
    private bool _disposed;
    public AccountStatus Status { get; private set; }
    public AuthSession? Session { get; private set; }
    public string Message { get; private set; } = "";
    public bool IsBusy => Status == AccountStatus.Authenticating;
    public bool IsAuthenticated => Status == AccountStatus.SignedIn && _network.State == NetworkState.Connected;
    public event Action? Changed;
    public event Action? LoginRequired;
    public event Action? RegistrationCompleted;
    public event Action<string>? PersistenceWarning;

    public AccountSessionManager(AuthApi api, ILoginStore store, INetworkSessionClient network,
        string host, ushort port, string endpointScope)
    {
        _api = api; _store = store; _network = network;
        _host = host; _port = port; _scope = endpointScope;
        network.Disconnected += OnDisconnected;
    }

    public async Task InitializeAsync()
    {
        if (_disposed || IsBusy || IsAuthenticated) return;
        CachedLogin? cached = null;
        try { cached = _store.Load(); }
        catch (Exception e) when (IsStorageError(e)) { PersistenceWarning?.Invoke("无法读取本地登录信息"); }
        if (cached == null || cached.EndpointScope != _scope)
        {
            Message = "";
            LoginRequired?.Invoke();
            return;
        }
        await AuthenticateAsync(_ => Task.FromResult(new AuthSession(cached.Uid, cached.Name, cached.Token)), true);
    }

    public Task SubmitAsync(AuthSubmission submission)
        => AuthenticateAsync(async token =>
        {
            if (submission.Mode == AuthMode.Register)
            {
                await _api.RegisterAsync(submission.Account, submission.Password, token);
                token.ThrowIfCancellationRequested();
                RegistrationCompleted?.Invoke();
            }
            return await _api.LoginAsync(submission.Account, submission.Password, token);
        }, false);

    private async Task AuthenticateAsync(Func<CancellationToken, Task<AuthSession>> exchange, bool automatic)
    {
        if (_disposed || IsBusy || IsAuthenticated) return;
        var revision = ++_revision;
        using var operation = new CancellationTokenSource();
        _operation = operation;
        var token = operation.Token;
        Status = AccountStatus.Authenticating;
        Message = "";
        Changed?.Invoke();
        try
        {
            var session = await exchange(token);
            token.ThrowIfCancellationRequested();
            if (!IsCurrent(revision)) return;
            session = await HandshakeAsync(session, token);
            token.ThrowIfCancellationRequested();
            if (!IsCurrent(revision)) return;
            try { _store.Save(new CachedLogin(session.Uid, session.Name, session.Token, _scope)); }
            catch (Exception e) when (IsStorageError(e)) { PersistenceWarning?.Invoke("登录成功，但无法保存自动登录信息"); }
            Session = session;
            Status = AccountStatus.SignedIn;
            _operation = null;
            Changed?.Invoke();
        }
        catch (Exception e)
        {
            if (!IsCurrent(revision)) return;
            // Remain busy while Disconnect invokes global connection listeners.
            _network.Disconnect("登录未完成");
            if (automatic && (e.Message.Contains("ErrInvalidToken", StringComparison.Ordinal) ||
                              e.Message.Contains("会话身份不一致", StringComparison.Ordinal)))
            {
                try { _store.Clear(); }
                catch (Exception storageError) when (IsStorageError(storageError))
                { PersistenceWarning?.Invoke("无法清理失效的本地登录信息"); }
            }
            Session = null;
            Status = AccountStatus.SignedOut;
            _operation = null;
            Message = e switch
            {
                AuthApiException => e.Message,
                TimeoutException => "登录连接超时，请重试",
                OperationCanceledException => "登录请求已取消或超时，请重试",
                AccountConnectionException => FriendlyConnectionError(e.Message),
                _ => "暂时无法连接登录服务，请稍后重试"
            };
            Changed?.Invoke();
            LoginRequired?.Invoke();
        }
        finally { if (ReferenceEquals(_operation, operation)) _operation = null; }
    }

    private async Task<AuthSession> HandshakeAsync(AuthSession session, CancellationToken token)
    {
        if (_network.State != NetworkState.Disconnected) _network.Disconnect("建立新登录会话");
        var completed = new TaskCompletionSource<AuthSession>(TaskCreationOptions.RunContinuationsAsynchronously);
        void Connected()
        {
            if (_network.Uid == 0 || _network.Uid != session.Uid)
                completed.TrySetException(new AccountConnectionException("会话身份不一致"));
            else completed.TrySetResult(session with { Uid = _network.Uid, Name = _network.Name });
        }
        void Disconnected(string reason) => completed.TrySetException(new AccountConnectionException(reason));
        _network.Connected += Connected;
        _network.Disconnected += Disconnected;
        try
        {
            token.ThrowIfCancellationRequested();
            _network.Connect(_host, _port, session.Token);
            return await completed.Task.WaitAsync(TimeSpan.FromSeconds(15), token);
        }
        finally
        {
            _network.Connected -= Connected;
            _network.Disconnected -= Disconnected;
        }
    }

    public void Cancel()
    {
        if (!IsBusy) return;
        ++_revision;
        _operation?.Cancel();
        _operation = null;
        _network.Disconnect("取消登录");
        Session = null;
        Status = AccountStatus.SignedOut;
        Message = "";
        Changed?.Invoke();
    }

    public bool Logout()
    {
        try { _store.Clear(); }
        catch (Exception e) when (IsStorageError(e))
        { Message = "无法清理本地登录信息，请重试"; Changed?.Invoke(); return false; }
        ++_revision;
        _operation?.Cancel();
        _operation = null;
        // Suppress the automatic login prompt from the disconnection callback.
        Status = AccountStatus.Authenticating;
        Session = null;
        _network.Disconnect("退出登录");
        Status = AccountStatus.SignedOut;
        Message = "";
        Changed?.Invoke();
        return true;
    }

    private void OnDisconnected(string reason)
    {
        if (_disposed || IsBusy || Status != AccountStatus.SignedIn) return;
        Session = null;
        Status = AccountStatus.SignedOut;
        Message = "连接已断开，请重新登录";
        Changed?.Invoke();
        LoginRequired?.Invoke();
    }

    private bool IsCurrent(int revision) => !_disposed && revision == _revision;
    private static bool IsStorageError(Exception error) => error is System.IO.IOException or UnauthorizedAccessException;
    private static string FriendlyConnectionError(string reason)
        => reason.Contains("ErrInvalidToken", StringComparison.Ordinal) ? "登录信息已失效，请重新登录" :
           reason.Contains("ErrAlreadyOnline", StringComparison.Ordinal) ? "账号已在线，请稍后重试" :
           reason.Contains("ErrVersionMismatch", StringComparison.Ordinal) ? "客户端与服务器协议版本不一致" :
           "无法建立游戏连接，请重试";

    public void Dispose()
    {
        _disposed = true;
        ++_revision;
        _operation?.Cancel();
        _network.Disconnected -= OnDisconnected;
    }

    private sealed class AccountConnectionException(string message) : Exception(message);
}
