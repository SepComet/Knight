#nullable enable
using SepCore.Definition;

namespace SepCore.Runtime.UI;

/// <summary>
/// 界面控制器基类(MVC 的 Controller):一个 UIFormType 一个实例,由启动流水线注册。
/// 收到打开请求后自行决定走刷新还是重开 —— UIComponent 只提供机制原语,不做策略。
/// </summary>
public abstract class UIController
{
    /// <summary>所属组件,注册时注入。</summary>
    public UIComponent UI { get; private set; } = null!;

    public UIFormType FormId { get; private set; }

    /// <summary>界面配置(注册时从数据表查得)。</summary>
    public UIFormConfig Config { get; private set; } = null!;

    internal void Attach(UIComponent ui, UIFormType formId, UIFormConfig config)
    {
        UI = ui;
        FormId = formId;
        Config = config;
    }

    /// <summary>打开请求的落点:子类在此决定刷新已有视图还是 CreateView 重开。</summary>
    public abstract void OnOpenRequest(object? userData);

    /// <summary>关闭请求的默认实现:销毁当前视图(未打开则空操作)。可覆写(如先确认再关)。</summary>
    public virtual void OnCloseRequest()
    {
        var view = UI.FindView(FormId);
        if (view != null)
            UI.DestroyView(view);
    }
}

/// <summary>带类型化视图引用的控制器便捷基类(适合单实例界面)。</summary>
public abstract class UIController<TView> : UIController where TView : UIForm
{
    /// <summary>当前视图实例;未打开时为 null。多实例界面请改用 UI.FindView 后自行筛选。</summary>
    protected TView? View => UI.FindView(FormId) as TView;
}
