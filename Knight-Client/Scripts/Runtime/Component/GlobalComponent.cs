#nullable enable
using Godot;

namespace SepCore.Runtime.Component;

/// <summary>
/// 全局组件基类:作为 autoload 常驻进程的框架组件,仅限定类型语义。
/// 单例访问(如静态 Instance)与具体逻辑由各子类自行实现,本基类不做其他约束。
/// </summary>
public abstract partial class GlobalComponent : Node
{
}
