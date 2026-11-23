#nullable enable
using SepCore.Runtime.UI;

namespace SepCore.UI.Common;

/// <summary>Single-instance screen policy; signals are wired once per created view.</summary>
public abstract class ScreenController<TView> : UIController<TView> where TView : UIForm
{
    public sealed override void OnOpenRequest(object? userData)
    {
        var view = View;
        if (view == null)
        {
            view = (TView)UI.CreateView(FormId);
            Wire(view);
        }
        else
            UI.BringToTop(view);
        Render(view, userData);
    }

    protected abstract void Wire(TView view);
    protected virtual void Render(TView view, object? userData) { }
}
