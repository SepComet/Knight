#nullable enable
using System;

namespace SepCore.UI.Auth.Services;

/// <summary>Stable per-instance login storage for local multiplayer testing.</summary>
public static class LoginCachePath
{
    public static string Resolve(string[] arguments)
    {
        string? instance = null;
        for (var i = 0; i < arguments.Length; i++)
        {
            if (arguments[i].StartsWith("--instance-id=", StringComparison.Ordinal))
                instance = arguments[i]["--instance-id=".Length..];
            else if (arguments[i] == "--instance-id")
                instance = ++i < arguments.Length ? arguments[i] : "";
        }

        if (instance == null) return "user://login-session.json";
        if (instance.Length is < 1 or > 64 || !IsValid(instance))
            throw new ArgumentException("--instance-id 必须为 1 至 64 位英文字母、数字、下划线或连字符");

        // Windows filenames are case insensitive; use the same identity on every platform.
        return $"user://login-sessions/{instance.ToLowerInvariant()}.json";
    }

    private static bool IsValid(string value)
    {
        foreach (var c in value)
            if (!(c is >= 'a' and <= 'z' or >= 'A' and <= 'Z' or >= '0' and <= '9' or '_' or '-'))
                return false;
        return true;
    }
}
