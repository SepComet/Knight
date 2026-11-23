#nullable enable
using System;
using System.Globalization;
using System.IO;
using System.Net;
using System.Net.Http;
using System.Text;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;

namespace SepCore.UI.Auth.Services;

public sealed record AuthSession(ulong Uid, string Name, string Token);

public sealed class AuthApiException : Exception
{
    public HttpStatusCode? StatusCode { get; }
    public AuthApiException(string message, HttpStatusCode? statusCode = null) : base(message)
        => StatusCode = statusCode;
}

/// <summary>Existing account HTTP contract. Does not own HttpClient or persist credentials.</summary>
public sealed class AuthApi
{
    private readonly HttpClient _client;
    private readonly Uri _baseUri;
    private const int MaxResponseBytes = 64 * 1024;

    public AuthApi(HttpClient client, Uri baseUri)
    {
        if (!baseUri.IsAbsoluteUri || (baseUri.Scheme != "http" && baseUri.Scheme != "https"))
            throw new ArgumentException("HTTP API address must be an absolute HTTP(S) URL.", nameof(baseUri));
        _client = client;
        _baseUri = baseUri;
    }

    public async Task RegisterAsync(string account, string password, CancellationToken cancellationToken = default)
    {
        using var response = await PostAsync("register", account, password, cancellationToken);
        ReadUid(response.RootElement);
    }

    public async Task<AuthSession> LoginAsync(string account, string password, CancellationToken cancellationToken = default)
    {
        using var response = await PostAsync("login", account, password, cancellationToken);
        var root = response.RootElement;
        var uid = ReadUid(root);
        if (!TryString(root, "name", out var name) || !TryString(root, "token", out var token))
            throw new AuthApiException("服务器登录响应不完整，请重试");
        return new AuthSession(uid, name, token);
    }

    private async Task<JsonDocument> PostAsync(string operation, string account, string password, CancellationToken token)
    {
        using var request = new HttpRequestMessage(HttpMethod.Post, new Uri(_baseUri, "/api/v1/" + operation));
        request.Content = new StringContent(JsonSerializer.Serialize(new { name = account, password }), Encoding.UTF8, "application/json");
        using var response = await _client.SendAsync(request, HttpCompletionOption.ResponseHeadersRead, token);
        if (response.StatusCode != HttpStatusCode.OK)
            throw new AuthApiException(response.StatusCode switch
            {
                HttpStatusCode.Unauthorized => "账号或密码不正确",
                HttpStatusCode.Conflict => "账号已存在，请更换账号或直接登录",
                HttpStatusCode.BadRequest => "账号或密码不符合要求",
                HttpStatusCode.TooManyRequests => "操作过于频繁，请稍后重试",
                _ => "账号服务暂不可用，请稍后重试"
            }, response.StatusCode);
        await using var stream = await response.Content.ReadAsStreamAsync(token);
        using var body = new MemoryStream();
        var buffer = new byte[4096];
        int count;
        while ((count = await stream.ReadAsync(buffer.AsMemory(), token)) > 0)
        {
            if (body.Length + count > MaxResponseBytes)
                throw new AuthApiException("服务器登录响应异常，请重试");
            body.Write(buffer, 0, count);
        }
        try { return JsonDocument.Parse(body.ToArray()); }
        catch (JsonException) { throw new AuthApiException("服务器登录响应格式错误，请重试"); }
    }

    private static ulong ReadUid(JsonElement root)
    {
        if (!TryString(root, "uid", out var value) ||
            !ulong.TryParse(value, NumberStyles.None, CultureInfo.InvariantCulture, out var uid) || uid == 0)
            throw new AuthApiException("服务器登录响应缺少有效账号标识，请重试");
        return uid;
    }

    private static bool TryString(JsonElement root, string property, out string value)
    {
        value = "";
        if (root.ValueKind != JsonValueKind.Object || !root.TryGetProperty(property, out var element) || element.ValueKind != JsonValueKind.String)
            return false;
        value = element.GetString() ?? "";
        return !string.IsNullOrWhiteSpace(value);
    }
}
