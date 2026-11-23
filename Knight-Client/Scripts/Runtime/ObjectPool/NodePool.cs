#nullable enable
using System;
using Godot;

namespace SepCore.Runtime.ObjectPool;

/// <summary>
/// Godot 节点对象池:按 <see cref="PackedScene"/> 复用 <see cref="Node"/> 实例,
/// 常用于子弹、特效、敌人等高频生灭对象。
/// <para>取出时若传入 parent 则挂到其下;归还时先从场景树摘除,再进入池中闲置。
/// 闲置节点游离于场景树之外,不会参与渲染与 _Process。</para>
/// </summary>
public sealed class NodePool
{
    private readonly ObjectPool<Node> _pool;

    public NodePool(PackedScene scene, int initialCapacity = 0, int maxCapacity = 100)
    {
        ArgumentNullException.ThrowIfNull(scene);
        _pool = new ObjectPool<Node>(() => scene.Instantiate(), initialCapacity, maxCapacity);
    }

    /// <summary>池中闲置节点数量。</summary>
    public int CountAvailable => _pool.CountAvailable;

    /// <summary>已借出、尚未归还的节点数量。</summary>
    public int CountActive => _pool.CountActive;

    /// <summary>取出节点;parent 不为空时挂到其下。</summary>
    public Node Get(Node? parent = null)
    {
        var node = _pool.Get();
        parent?.AddChild(node);
        return node;
    }

    /// <summary>取出节点并按类型返回,类型不符会抛出异常。</summary>
    public T Get<T>(Node? parent = null) where T : Node
        => (T)Get(parent);

    /// <summary>归还节点:先从场景树摘除,再进入池中闲置。重复归还或归还异池节点会抛出异常。</summary>
    public void Return(Node node)
    {
        ArgumentNullException.ThrowIfNull(node);
        node.GetParent()?.RemoveChild(node);
        _pool.Return(node);
    }

    /// <summary>
    /// 清空池中闲置节点。闲置节点不会自动 <see cref="Node.QueueFree"/>,
    /// 需要释放资源时在 onClear 回调中自行调用。
    /// </summary>
    public void Clear(Action<Node>? onClear = null)
        => _pool.Clear(onClear);
}
