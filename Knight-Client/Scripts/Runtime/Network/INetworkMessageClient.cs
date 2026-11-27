#nullable enable
using System;
using Google.Protobuf;

namespace SepCore.Runtime.Network;

public interface INetworkMessageClient
{
    NetworkState State { get; }
    ulong Uid { get; }
    event Action<string>? Disconnected;
    void Send<T>(T message) where T : IMessage;
    IDisposable Subscribe<T>(Action<T> handler) where T : IMessage;
    void Disconnect(string reason = "客户端主动断开");
    NetworkTransportStats GetTransportStats() => default;
}

/// <summary>Session establishment used by account flow; message-only consumers use the base interface.</summary>
public interface INetworkSessionClient : INetworkMessageClient
{
    string Name { get; }
    event Action? Connected;
    void Connect(string host, ushort port, string token);
}
