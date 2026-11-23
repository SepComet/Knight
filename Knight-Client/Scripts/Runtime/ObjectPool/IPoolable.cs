namespace SepCore.Runtime.ObjectPool;

/// <summary>
/// 可入池对象回调接口:<see cref="ObjectPool{T}"/> 在取出/归还对象时自动调用。
/// </summary>
public interface IPoolable
{
    /// <summary>从池中取出(生成)时调用,晚于池内复用判断、早于 onGet 回调。</summary>
    void OnSpawn();

    /// <summary>归还到池(回收)时调用,晚于 onRelease 回调;若池已满对象被丢弃,该回调仍会执行。</summary>
    void OnDespawn();
}
