#nullable enable
using System;
using SepCore.Runtime.Event;
using SepCore.Runtime.Network;
using Xunit;

namespace SepCore.Tests;

public class NetworkEventBridgeTests
{
    private sealed class FakeConnection : INetworkConnectionEvents
    {
        public event Action? Connected;
        public event Action<string>? Disconnected;

        public void RaiseConnected() => Connected?.Invoke();

        public void RaiseDisconnected(string reason) => Disconnected?.Invoke(reason);
    }

    [Fact]
    public void Attach_ClientConnected_PublishesNetConnected()
    {
        var bus = new EventBus();
        var fake = new FakeConnection();
        NetworkEventBridge.Attach(bus, fake);

        var received = 0;
        bus.Subscribe(GameEventType.NetConnected, () => received++);

        fake.RaiseConnected();

        Assert.Equal(1, received);
    }

    [Fact]
    public void Attach_ClientDisconnected_PublishesNetDisconnected_WithReason()
    {
        var bus = new EventBus();
        var fake = new FakeConnection();
        NetworkEventBridge.Attach(bus, fake);

        NetDisconnectedArgs? received = null;
        bus.Subscribe(GameEventType.NetDisconnected, (NetDisconnectedArgs args) => received = args);

        fake.RaiseDisconnected("握手超时");

        Assert.NotNull(received);
        Assert.Equal("握手超时", received!.Reason);
    }

    [Fact]
    public void Attach_NullArgument_Throws()
    {
        var bus = new EventBus();
        var fake = new FakeConnection();

        Assert.Throws<ArgumentNullException>(() => NetworkEventBridge.Attach(null!, fake));
        Assert.Throws<ArgumentNullException>(() => NetworkEventBridge.Attach(bus, null!));
    }
}
