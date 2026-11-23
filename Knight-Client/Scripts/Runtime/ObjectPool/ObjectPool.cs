#nullable enable
using System;
using System.Collections.Generic;

namespace SepCore.Runtime.ObjectPool;

/// <summary>
/// 通用 C# 对象池:按需创建、复用归还的对象,减少 GC 压力。
/// 线程不安全,仅在主线程使用。
/// <para>取出(<see cref="Get"/>):池中无闲置对象时调用工厂新建;
/// 随后依次调用 <see cref="IPoolable.OnSpawn"/> 与 <c>onGet</c> 回调。</para>
/// <para>归还(<see cref="Return"/>):依次调用 <c>onRelease</c> 回调与
/// <see cref="IPoolable.OnDespawn"/>;若闲置对象已达 <see cref="MaxCapacity"/>,
/// 对象被直接丢弃,交给 GC 回收。</para>
/// </summary>
public sealed class ObjectPool<T> where T : class
{
    private readonly Func<T> _factory;
    private readonly Action<T>? _onGet;
    private readonly Action<T>? _onRelease;
    private readonly int _maxCapacity;

    // 闲置对象栈(LIFO 复用,缓存更热)+ 其镜像集合,用于 O(1) 判定"重复归还"
    private readonly Stack<T> _items;
    private readonly HashSet<T> _itemSet;
    // 仍属于本池且存活的对象(闲置 + 借出);因池满被丢弃的对象会从中移除
    private readonly HashSet<T> _owned;

    public ObjectPool(Func<T> factory, int initialCapacity = 0, int maxCapacity = int.MaxValue,
        Action<T>? onGet = null, Action<T>? onRelease = null)
    {
        _factory = factory ?? throw new ArgumentNullException(nameof(factory));
        if (initialCapacity < 0)
            throw new ArgumentOutOfRangeException(nameof(initialCapacity), initialCapacity, "初始容量不能为负数");
        if (maxCapacity < 0)
            throw new ArgumentOutOfRangeException(nameof(maxCapacity), maxCapacity, "最大容量不能为负数");

        _maxCapacity = maxCapacity;
        _onGet = onGet;
        _onRelease = onRelease;
        _items = new Stack<T>(initialCapacity);
        _itemSet = new HashSet<T>();
        _owned = new HashSet<T>();

        for (int i = 0; i < initialCapacity; i++)
        {
            var item = CreateItem();
            _items.Push(item);
            _itemSet.Add(item);
            _owned.Add(item);
        }
    }

    /// <summary>池内最多保留的闲置对象数量,归还时超出的对象会被丢弃。</summary>
    public int MaxCapacity => _maxCapacity;

    /// <summary>池中闲置(可复用)的对象数量。</summary>
    public int CountAvailable => _items.Count;

    /// <summary>已借出、尚未归还的对象数量。</summary>
    public int CountActive { get; private set; }

    /// <summary>取出一个对象:优先复用闲置对象,否则调用工厂新建。</summary>
    public T Get()
    {
        T item;
        if (_items.Count > 0)
        {
            item = _items.Pop();
            _itemSet.Remove(item);
        }
        else
        {
            item = CreateItem();
        }

        CountActive++;
        if (item is IPoolable poolable)
            poolable.OnSpawn();
        _onGet?.Invoke(item);
        return item;
    }

    /// <summary>归还对象。重复归还或归还非本池创建的对象会抛出异常(快速暴露使用错误)。</summary>
    public void Return(T item)
    {
        ArgumentNullException.ThrowIfNull(item);
        if (_itemSet.Contains(item))
            throw new InvalidOperationException($"对象重复归还:{typeof(T).Name}");
        if (!_owned.Contains(item))
            throw new InvalidOperationException($"归还的对象不属于本池(或已因池满被丢弃):{typeof(T).Name}");

        _onRelease?.Invoke(item);
        if (item is IPoolable poolable)
            poolable.OnDespawn();
        CountActive--;

        // 闲置已满:丢弃对象,交给 GC
        if (_itemSet.Count >= _maxCapacity)
        {
            _owned.Remove(item);
            return;
        }

        _items.Push(item);
        _itemSet.Add(item);
    }

    /// <summary>
    /// 清空池中的闲置对象(借出中的对象不受影响,后续正常归还)。
    /// <paramref name="onClear"/> 用于在清理时释放资源。
    /// </summary>
    public void Clear(Action<T>? onClear = null)
    {
        while (_items.Count > 0)
        {
            var item = _items.Pop();
            _itemSet.Remove(item);
            _owned.Remove(item);
            onClear?.Invoke(item);
        }
    }

    private T CreateItem()
    {
        var item = _factory();
        if (item == null)
            throw new InvalidOperationException($"对象池工厂返回了 null:{typeof(T).Name}");
        _owned.Add(item);
        return item;
    }
}
