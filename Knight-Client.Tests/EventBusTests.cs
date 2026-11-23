#nullable enable
using System;
using System.Collections.Generic;
using SepCore.Runtime.Event;
using Xunit;

namespace SepCore.Tests;

public class EventBusTests
{
    private sealed class DeathArgs
    {
        public string Victim { get; init; } = "";
    }

    // ---------------------------------------------------------------- 订阅与派发

    [Fact]
    public void Subscribe_Publish_DeliversPayloadToSubscriber()
    {
        var bus = new EventBus();
        DeathArgs? received = null;
        bus.Subscribe(GameEventType.PlayerDied, (DeathArgs args) => received = args);

        var payload = new DeathArgs { Victim = "slime" };
        bus.Publish(GameEventType.PlayerDied, payload);

        Assert.Same(payload, received);
    }

    [Fact]
    public void Subscribe_NoArgSignal_PublishNotifies()
    {
        var bus = new EventBus();
        var called = 0;
        bus.Subscribe(GameEventType.NetConnected, () => called++);

        bus.Publish(GameEventType.NetConnected);

        Assert.Equal(1, called);
    }

    [Fact]
    public void Publish_NoSubscribers_IsNoOp()
    {
        var bus = new EventBus();

        bus.Publish(GameEventType.NetConnected);
        bus.Publish(GameEventType.PlayerDied, new DeathArgs());
    }

    [Fact]
    public void Subscribe_MultipleSubscribers_DispatchInSubscriptionOrder()
    {
        var bus = new EventBus();
        var log = new List<string>();

        bus.Subscribe(GameEventType.NetDisconnected, (string reason) => log.Add($"a:{reason}"));
        bus.Subscribe(GameEventType.NetDisconnected, (string reason) => log.Add($"b:{reason}"));

        bus.Publish(GameEventType.NetDisconnected, "超时");

        Assert.Equal(new[] { "a:超时", "b:超时" }, log);
    }

    // ---------------------------------------------------------------- 退订

    [Fact]
    public void Unsubscribe_StopsReceiving()
    {
        var bus = new EventBus();
        var log = new List<string>();

        void OnPlayerDied(DeathArgs args) => log.Add(args.Victim);

        bus.Subscribe<DeathArgs>(GameEventType.PlayerDied, OnPlayerDied); // 方法组订阅需显式泛型实参
        bus.Publish(GameEventType.PlayerDied, new DeathArgs());
        bus.Unsubscribe<DeathArgs>(GameEventType.PlayerDied, OnPlayerDied);
        bus.Publish(GameEventType.PlayerDied, new DeathArgs());

        Assert.Single(log);
    }

    [Fact]
    public void Unsubscribe_DuplicateSubscription_RemovesAllOccurrences()
    {
        var bus = new EventBus();
        var called = 0;

        void OnNetConnected() => called++;

        bus.Subscribe(GameEventType.NetConnected, OnNetConnected);
        bus.Subscribe(GameEventType.NetConnected, OnNetConnected);
        bus.Publish(GameEventType.NetConnected);
        Assert.Equal(2, called);

        bus.Unsubscribe(GameEventType.NetConnected, OnNetConnected); // 一次退订移除全部重复项
        bus.Publish(GameEventType.NetConnected);
        Assert.Equal(2, called);
    }

    [Fact]
    public void Unsubscribe_UnknownHandler_IsNoOp()
    {
        var bus = new EventBus();

        bus.Unsubscribe(GameEventType.PlayerDied, (DeathArgs _) => { });
        bus.Publish(GameEventType.PlayerDied, new DeathArgs()); // 不抛异常即通过
    }

    [Fact]
    public void Unsubscribe_InsideDispatch_StillReceivesCurrent_SkipsNext()
    {
        var bus = new EventBus();
        var log = new List<string>();

        void OnPlayerDied(DeathArgs args)
        {
            log.Add("hit");
            bus.Unsubscribe<DeathArgs>(GameEventType.PlayerDied, OnPlayerDied); // 派发中退订自己
        }

        bus.Subscribe<DeathArgs>(GameEventType.PlayerDied, OnPlayerDied);

        bus.Publish(GameEventType.PlayerDied, new DeathArgs()); // 快照语义:本次仍送达
        Assert.Single(log);

        bus.Publish(GameEventType.PlayerDied, new DeathArgs()); // 之后不再送达
        Assert.Single(log);
    }

    [Fact]
    public void Subscribe_InsideDispatch_ReceivedFromNextEvent()
    {
        var bus = new EventBus();
        var log = new List<string>();

        void OnPlayerDied(DeathArgs args)
        {
            log.Add("first");
            bus.Subscribe(GameEventType.RoomSwitched, () => log.Add("late")); // 派发中订阅
        }

        bus.Subscribe<DeathArgs>(GameEventType.PlayerDied, OnPlayerDied);

        bus.Publish(GameEventType.PlayerDied, new DeathArgs());
        Assert.Equal(new[] { "first" }, log); // 本次派发不送达新订阅者

        bus.Publish(GameEventType.RoomSwitched);
        Assert.Equal(new[] { "first", "late" }, log); // 下一次事件开始生效
    }

    // ---------------------------------------------------------------- 嵌套派发

    [Fact]
    public void Publish_InsideDispatch_DeliversNestedEvent()
    {
        var bus = new EventBus();
        var log = new List<string>();

        void OnNetDisconnected(string reason) => log.Add($"net:{reason}");

        void OnPlayerDied(DeathArgs args)
        {
            log.Add("died");
            bus.Publish(GameEventType.NetDisconnected, "服务器关闭"); // 回调内嵌套发布
        }

        bus.Subscribe<string>(GameEventType.NetDisconnected, OnNetDisconnected);
        bus.Subscribe<DeathArgs>(GameEventType.PlayerDied, OnPlayerDied);

        bus.Publish(GameEventType.PlayerDied, new DeathArgs());

        Assert.Equal(new[] { "died", "net:服务器关闭" }, log);
    }

    // ---------------------------------------------------------------- 契约校验

    [Fact]
    public void Publish_PayloadMismatch_Throws()
    {
        var bus = new EventBus();
        bus.Subscribe(GameEventType.PlayerDied, (DeathArgs _) => { });

        Assert.Throws<InvalidOperationException>(
            () => bus.Publish(GameEventType.PlayerDied, "类型错误的 payload"));
    }

    [Fact]
    public void Publish_NoArg_SignatureMismatch_Throws()
    {
        var bus = new EventBus();
        bus.Subscribe(GameEventType.NetConnected, (int _) => { });

        Assert.Throws<InvalidOperationException>(() => bus.Publish(GameEventType.NetConnected));
    }

    [Fact]
    public void Subscribe_NullHandler_Throws()
    {
        var bus = new EventBus();

        Assert.Throws<ArgumentNullException>(() => bus.Subscribe(GameEventType.NetConnected, (Action)null!));
        Assert.Throws<ArgumentNullException>(() => bus.Subscribe(GameEventType.PlayerDied, (Action<DeathArgs>)null!));
    }

    [Fact]
    public void Unsubscribe_NullHandler_Throws()
    {
        var bus = new EventBus();

        Assert.Throws<ArgumentNullException>(() => bus.Unsubscribe(GameEventType.NetConnected, (Action)null!));
        Assert.Throws<ArgumentNullException>(() => bus.Unsubscribe(GameEventType.PlayerDied, (Action<DeathArgs>)null!));
    }
}
