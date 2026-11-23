#nullable enable
using System;
using SepCore.UI.Common;

namespace SepCore.UI.Auth;

public sealed class LoginController : ScreenController<LoginView>
{
    public LoginModel Model { get; } = new();
    public AuthMode Mode => Model.Mode;
    public event Action<AuthSubmission>? SubmissionRequested;
    public event Action? Closed;

    public void SetBusy(bool busy)
    {
        Model.SetBusy(busy);
        View?.SetBusy(busy);
    }

    public void SetRequired(bool required)
    {
        Model.SetRequired(required);
        View?.SetRequired(required);
    }

    public void SetMode(AuthMode mode)
    {
        if (Model.IsBusy) return;
        Model.SwitchMode(mode);
        View?.Render(mode);
    }

    public void ShowMessage(string message) => View?.ShowMessage(message);

    public override void OnCloseRequest()
    {
        if (Model.Required || View == null) return;
        base.OnCloseRequest();
        Model.SetBusy(false);
        Closed?.Invoke();
    }

    protected override void Wire(LoginView view)
    {
        view.CloseRequested += OnCloseRequest;
        view.ModeRequested += mode =>
        {
            if (Model.IsBusy) return;
            Model.SwitchMode(mode);
            view.Render(mode);
        };
        view.SubmitRequested += (account, password, confirmation) =>
        {
            if (Model.IsBusy) return;
            var valid = Model.Validate(account, password, confirmation);
            view.ShowMessage(Model.Message);
            if (!valid) return;
            view.ClearPasswords();
            SubmissionRequested?.Invoke(new AuthSubmission(Mode, account, password));
        };
    }

    protected override void Render(LoginView view, object? userData)
    {
        if (Model.IsBusy)
        {
            view.SetBusy(true);
            view.SetRequired(Model.Required);
            return;
        }
        Model.SwitchMode(userData is AuthMode mode ? mode : AuthMode.Login);
        view.Render(Mode);
        view.SetBusy(Model.IsBusy);
        view.SetRequired(Model.Required);
    }
}
