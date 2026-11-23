#nullable enable
using Godot;
using SepCore.Definition;

namespace SepCore.Runtime.UI;

/// <summary>
/// 界面视图基类(MVC 的 View):界面场景根节点必须继承本类。
/// 只负责显示与输入转发,业务判断在 Controller;生命周期回调由 UIComponent 触发。
/// </summary>
public abstract partial class UIForm : Control
{
    /// <summary>所属界面配置,实例化后由 UIComponent 注入。</summary>
    public UIFormConfig Config { get; internal set; } = null!;

    /// <summary>实例入树后调用;控制器走"刷新"路径时会再次调用(视为数据刷新)。</summary>
    public abstract void OnOpen(object? userData);

    /// <summary>销毁前调用。</summary>
    public abstract void OnClose();

    private bool _stackPaused;

    /// <summary>覆盖暂停由 UIComponent 按组栈序触发,自带去重(状态不变不重复回调)。</summary>
    internal void NotifyStackPause(bool paused)
    {
        if (_stackPaused == paused)
        {
            return;
        }
        _stackPaused = paused;
        
        if (paused)
        {
            OnPause();
        }
        else
        {
            OnResume();
        }
    }

    /// <summary>被同组更高层的界面覆盖(PauseCoveredUIForm)。</summary>
    public virtual void OnPause() { }

    /// <summary>覆盖者关闭,重新可见。</summary>
    public virtual void OnResume() { }
}
