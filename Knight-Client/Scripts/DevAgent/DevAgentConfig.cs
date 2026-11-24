#nullable enable
using System;
using System.Collections.Generic;
using System.Globalization;

namespace SepCore.DevAgent;

/// <summary>
/// Parsed command-line configuration for <see cref="DevAgentComponent"/>.
/// Pure C# (no Godot dependency) for direct unit testing.
/// </summary>
public sealed class DevAgentConfig
{
    public const int MinPort = 1;
    public const int MaxPort = 65535;

    public int Port { get; }
    public string InstanceId { get; }
    public bool IsEnabled => Port is >= MinPort and <= MaxPort;

    public DevAgentConfig(int port, string instanceId)
    {
        Port = port is >= MinPort and <= MaxPort ? port : 0;
        InstanceId = instanceId ?? "";
    }

    /// <summary>
    /// Parses <c>--dev-agent-port=&lt;port&gt;</c> / <c>--dev-agent-port &lt;port&gt;</c>
    /// and <c>--instance-id=&lt;id&gt;</c> / <c>--instance-id &lt;id&gt;</c> from user command-line arguments.
    /// </summary>
    public static DevAgentConfig Parse(IReadOnlyList<string>? args)
    {
        if (args == null || args.Count == 0)
            return new DevAgentConfig(0, "");

        var port = 0;
        var instanceId = "";

        for (var i = 0; i < args.Count; i++)
        {
            var arg = args[i];
            if (string.IsNullOrEmpty(arg))
                continue;

            if (arg.StartsWith("--dev-agent-port=", StringComparison.Ordinal))
            {
                var raw = arg["--dev-agent-port=".Length..];
                port = ParsePort(raw);
            }
            else if (arg == "--dev-agent-port")
            {
                port = i + 1 < args.Count ? ParsePort(args[++i]) : 0;
            }
            else if (arg.StartsWith("--instance-id=", StringComparison.Ordinal))
            {
                instanceId = arg["--instance-id=".Length..].Trim();
            }
            else if (arg == "--instance-id")
            {
                instanceId = i + 1 < args.Count ? (args[++i] ?? "").Trim() : "";
            }
        }

        return new DevAgentConfig(port, instanceId);
    }

    private static int ParsePort(string? raw)
    {
        if (string.IsNullOrWhiteSpace(raw))
            return 0;

        return int.TryParse(raw.Trim(), NumberStyles.Integer, CultureInfo.InvariantCulture, out var parsed)
            && parsed is >= MinPort and <= MaxPort
                ? parsed
                : 0;
    }
}
