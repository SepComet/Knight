#nullable enable
using System;
using System.Globalization;
using System.IO;
using System.Text.Json;

namespace SepCore.UI.Auth.Services;

public sealed record CachedLogin(ulong Uid, string Name, string Token, string EndpointScope);

public interface ILoginStore
{
    CachedLogin? Load();
    void Save(CachedLogin session);
    void Clear();
}

/// <summary>Persists only a resumable session token, never an account password.</summary>
public sealed class JsonLoginStore : ILoginStore
{
    private readonly string _path;
    private const int MaxFileBytes = 32 * 1024;
    private sealed record StoredLogin(int Version, string Uid, string Name, string Token, string EndpointScope);

    public JsonLoginStore(string absolutePath)
    {
        if (!Path.IsPathFullyQualified(absolutePath)) throw new ArgumentException("Login cache path must be absolute.", nameof(absolutePath));
        _path = absolutePath;
    }

    public CachedLogin? Load()
    {
        if (!File.Exists(_path)) return null;
        if (new FileInfo(_path).Length > MaxFileBytes) { Clear(); return null; }
        try
        {
            var data = JsonSerializer.Deserialize<StoredLogin>(File.ReadAllText(_path));
            if (data is not null && data.Version == 1 &&
                ulong.TryParse(data.Uid, NumberStyles.None, CultureInfo.InvariantCulture, out var uid) && uid != 0 &&
                !string.IsNullOrWhiteSpace(data.Name) && !string.IsNullOrWhiteSpace(data.Token) && !string.IsNullOrWhiteSpace(data.EndpointScope))
                return new CachedLogin(uid, data.Name, data.Token, data.EndpointScope);
        }
        catch (JsonException) { }
        Clear();
        return null;
    }

    public void Save(CachedLogin session)
    {
        if (session.Uid == 0 || string.IsNullOrWhiteSpace(session.Name) || string.IsNullOrWhiteSpace(session.Token) || string.IsNullOrWhiteSpace(session.EndpointScope))
            throw new ArgumentException("Cannot persist an incomplete login session.", nameof(session));
        var json = JsonSerializer.Serialize(new StoredLogin(1, session.Uid.ToString(CultureInfo.InvariantCulture), session.Name, session.Token, session.EndpointScope));
        if (System.Text.Encoding.UTF8.GetByteCount(json) > MaxFileBytes)
            throw new ArgumentException("Login session exceeds cache size limit.", nameof(session));
        Directory.CreateDirectory(Path.GetDirectoryName(_path)!);
        var temporary = _path + "." + Guid.NewGuid().ToString("N") + ".tmp";
        try
        {
            File.WriteAllText(temporary, json);
            File.Move(temporary, _path, true);
        }
        finally
        {
            try { File.Delete(temporary); }
            catch (IOException) { }
            catch (UnauthorizedAccessException) { }
        }
    }

    public void Clear() => File.Delete(_path);
}
