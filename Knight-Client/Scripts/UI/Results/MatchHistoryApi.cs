#nullable enable
using System;
using System.Collections.Generic;
using System.Globalization;
using System.IO;
using System.Net;
using System.Net.Http;
using System.Net.Http.Headers;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;

namespace SepCore.UI.Results;

/// <summary>单条历史战绩记录（对齐服务端 GET /api/v1/users/{uid}/matches JSON 契约）。</summary>
public sealed record MatchHistoryEntry(
    string MatchId,
    int Outcome,
    int DurationSec,
    string Seed,
    int CharacterId,
    int WeaponId,
    int Kills,
    int DamageDealt,
    int DamageTaken,
    bool Alive,
    string FinishedAt)
{
    public bool IsVictory => Outcome == 1;
    public string OutcomeText => Outcome switch
    {
        1 => "胜利",
        2 => "失败",
        _ => "未知",
    };

    public string FormattedDuration
    {
        get
        {
            var sec = Math.Max(0, DurationSec);
            return $"{sec / 60:00}:{sec % 60:00}";
        }
    }
}

public sealed class MatchHistoryApiException : Exception
{
    public HttpStatusCode? StatusCode { get; }
    public MatchHistoryApiException(string message, HttpStatusCode? statusCode = null) : base(message)
        => StatusCode = statusCode;
}

/// <summary>历史战绩 HTTP 客户端（纯 C#，可脱离 Godot 单元测试）。</summary>
public sealed class MatchHistoryApi
{
    private readonly HttpClient _client;
    private readonly Uri _baseUri;
    private const int MaxResponseBytes = 256 * 1024;

    public MatchHistoryApi(HttpClient client, Uri baseUri)
    {
        if (!baseUri.IsAbsoluteUri || (baseUri.Scheme != "http" && baseUri.Scheme != "https"))
            throw new ArgumentException("HTTP API address must be an absolute HTTP(S) URL.", nameof(baseUri));
        _client = client ?? throw new ArgumentNullException(nameof(client));
        _baseUri = baseUri;
    }

    public async Task<IReadOnlyList<MatchHistoryEntry>> FetchAsync(
        ulong uid,
        string token,
        CancellationToken cancellationToken = default)
    {
        if (uid == 0)
            throw new ArgumentOutOfRangeException(nameof(uid), "UID must be positive.");
        if (string.IsNullOrWhiteSpace(token))
            throw new ArgumentException("Bearer token is required.", nameof(token));

        var endpoint = new Uri(_baseUri, $"/api/v1/users/{uid.ToString(CultureInfo.InvariantCulture)}/matches");
        using var request = new HttpRequestMessage(HttpMethod.Get, endpoint);
        request.Headers.Authorization = new AuthenticationHeaderValue("Bearer", token);

        using var response = await _client.SendAsync(request, HttpCompletionOption.ResponseHeadersRead, cancellationToken);
        if (response.StatusCode != HttpStatusCode.OK)
        {
            throw new MatchHistoryApiException(response.StatusCode switch
            {
                HttpStatusCode.Unauthorized => "登录态已失效，请重新登录",
                HttpStatusCode.Forbidden => "无权查看该账号战绩",
                HttpStatusCode.NotFound => "战绩接口不可用",
                _ => "获取历史战绩失败，请稍后重试",
            }, response.StatusCode);
        }

        await using var stream = await response.Content.ReadAsStreamAsync(cancellationToken);
        using var body = new MemoryStream();
        var buffer = new byte[4096];
        int count;
        while ((count = await stream.ReadAsync(buffer.AsMemory(), cancellationToken)) > 0)
        {
            if (body.Length + count > MaxResponseBytes)
                throw new MatchHistoryApiException("历史战绩响应过大，请重试");
            body.Write(buffer, 0, count);
        }

        try
        {
            using var doc = JsonDocument.Parse(body.ToArray());
            return ParseEntries(doc.RootElement);
        }
        catch (JsonException)
        {
            throw new MatchHistoryApiException("历史战绩数据格式错误");
        }
    }

    public static IReadOnlyList<MatchHistoryEntry> ParseEntries(JsonElement root)
    {
        if (root.ValueKind != JsonValueKind.Array)
            return Array.Empty<MatchHistoryEntry>();

        var list = new List<MatchHistoryEntry>();
        foreach (var item in root.EnumerateArray())
        {
            if (item.ValueKind != JsonValueKind.Object)
                continue;

            list.Add(new MatchHistoryEntry(
                MatchId: ReadStringOrNumber(item, "match_id"),
                Outcome: ReadInt(item, "outcome"),
                DurationSec: ReadInt(item, "duration_sec"),
                Seed: ReadStringOrNumber(item, "seed"),
                CharacterId: ReadInt(item, "character_id"),
                WeaponId: ReadInt(item, "weapon_id"),
                Kills: ReadInt(item, "kills"),
                DamageDealt: ReadInt(item, "damage_dealt"),
                DamageTaken: ReadInt(item, "damage_taken"),
                Alive: ReadBool(item, "alive"),
                FinishedAt: ReadStringOrNumber(item, "finished_at")));
        }

        return Array.AsReadOnly(list.ToArray());
    }

    private static string ReadStringOrNumber(JsonElement obj, string prop)
    {
        if (!obj.TryGetProperty(prop, out var el))
            return "";
        return el.ValueKind switch
        {
            JsonValueKind.String => el.GetString() ?? "",
            JsonValueKind.Number => el.GetRawText(),
            _ => "",
        };
    }

    private static int ReadInt(JsonElement obj, string prop)
    {
        if (!obj.TryGetProperty(prop, out var el))
            return 0;
        if (el.ValueKind == JsonValueKind.Number && el.TryGetInt32(out var val))
            return val;
        if (el.ValueKind == JsonValueKind.String && int.TryParse(el.GetString(), NumberStyles.Integer, CultureInfo.InvariantCulture, out var parsed))
            return parsed;
        return 0;
    }

    private static bool ReadBool(JsonElement obj, string prop)
    {
        if (!obj.TryGetProperty(prop, out var el))
            return false;
        return el.ValueKind == JsonValueKind.True;
    }
}
