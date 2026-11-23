using System;
using System.Collections.Generic;
using SepCore.Runtime.ObjectPool;
using Xunit;

namespace SepCore.Tests;

public class ObjectPoolTests
{
    private sealed class PooledItem : IPoolable
    {
        public int SpawnCount;
        public int DespawnCount;

        public void OnSpawn() => SpawnCount++;
        public void OnDespawn() => DespawnCount++;
    }

    private sealed class LoggedItem : IPoolable
    {
        private readonly List<string> _log;

        public LoggedItem(List<string> log) => _log = log;

        public void OnSpawn() => _log.Add("OnSpawn");
        public void OnDespawn() => _log.Add("OnDespawn");
    }

    [Fact]
    public void Get_CreatesViaFactory_AndCountsActive()
    {
        int factoryCalls = 0;
        var pool = new ObjectPool<PooledItem>(() => { factoryCalls++; return new PooledItem(); });

        var item = pool.Get();

        Assert.Equal(1, factoryCalls);
        Assert.Equal(1, pool.CountActive);
        Assert.Equal(0, pool.CountAvailable);
    }

    [Fact]
    public void Get_AfterReturn_ReusesInstance()
    {
        int factoryCalls = 0;
        var pool = new ObjectPool<PooledItem>(() => { factoryCalls++; return new PooledItem(); });

        var first = pool.Get();
        pool.Return(first);
        var second = pool.Get();

        Assert.Same(first, second);
        Assert.Equal(1, factoryCalls);
        Assert.Equal(1, pool.CountActive);
        Assert.Equal(0, pool.CountAvailable);
    }

    [Fact]
    public void Lifecycle_Order_Is_OnSpawn_onGet_onRelease_OnDespawn()
    {
        var log = new List<string>();
        var pool = new ObjectPool<LoggedItem>(
            () => new LoggedItem(log),
            onGet: _ => log.Add("onGet"),
            onRelease: _ => log.Add("onRelease"));

        var item = pool.Get();
        pool.Return(item);

        Assert.Equal(new[] { "OnSpawn", "onGet", "onRelease", "OnDespawn" }, log);
    }

    [Fact]
    public void Return_Twice_Throws()
    {
        var pool = new ObjectPool<PooledItem>(() => new PooledItem());
        var item = pool.Get();
        pool.Return(item);

        Assert.Throws<InvalidOperationException>(() => pool.Return(item));
    }

    [Fact]
    public void Return_ForeignItem_Throws()
    {
        var pool = new ObjectPool<PooledItem>(() => new PooledItem());

        Assert.Throws<InvalidOperationException>(() => pool.Return(new PooledItem()));
    }

    [Fact]
    public void Return_WhenFull_DiscardsItem()
    {
        var pool = new ObjectPool<PooledItem>(() => new PooledItem(), maxCapacity: 1);
        var a = pool.Get();
        var b = pool.Get();

        pool.Return(a);
        pool.Return(b); // 闲置已达上限,b 被丢弃

        Assert.Equal(1, pool.CountAvailable);
        Assert.Equal(0, pool.CountActive);
        Assert.Equal(1, b.DespawnCount); // 丢弃前仍走 OnDespawn

        Assert.Throws<InvalidOperationException>(() => pool.Return(b)); // 已不属于池
    }

    [Fact]
    public void InitialCapacity_PrefillsIdleItems()
    {
        int factoryCalls = 0;
        var pool = new ObjectPool<PooledItem>(() => { factoryCalls++; return new PooledItem(); }, initialCapacity: 3);

        Assert.Equal(3, pool.CountAvailable);
        Assert.Equal(0, pool.CountActive);
        Assert.Equal(3, factoryCalls);

        var item = pool.Get();

        Assert.Equal(3, factoryCalls);   // 取出未触发新建
        Assert.Equal(1, item.SpawnCount); // 预热对象首次取出也走 OnSpawn
    }

    [Fact]
    public void Clear_ReleasesIdleOnly_AndInvokesOnClear()
    {
        var pool = new ObjectPool<PooledItem>(() => new PooledItem());
        var a = pool.Get();
        var b = pool.Get();
        pool.Return(a);

        var cleared = new List<PooledItem>();
        pool.Clear(item => cleared.Add(item));

        Assert.Single(cleared);
        Assert.Same(a, cleared[0]);
        Assert.Equal(0, pool.CountAvailable);
        Assert.Equal(1, pool.CountActive); // 借出的 b 不受影响

        pool.Return(b); // 归还仍正常
        Assert.Equal(1, pool.CountAvailable);
    }

    [Fact]
    public void Constructor_InvalidArguments_Throw()
    {
        Assert.Throws<ArgumentNullException>(() => new ObjectPool<PooledItem>(null!));
        Assert.Throws<ArgumentOutOfRangeException>(
            () => new ObjectPool<PooledItem>(() => new PooledItem(), initialCapacity: -1));
        Assert.Throws<ArgumentOutOfRangeException>(
            () => new ObjectPool<PooledItem>(() => new PooledItem(), maxCapacity: -1));
    }

    [Fact]
    public void Return_Null_Throws()
    {
        var pool = new ObjectPool<PooledItem>(() => new PooledItem());

        Assert.Throws<ArgumentNullException>(() => pool.Return(null!));
    }
}
