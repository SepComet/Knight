#nullable enable
namespace SepCore.UI.Auth;

public enum AuthMode { Login, Register }
public sealed record AuthSubmission(AuthMode Mode, string Account, string Password);

public sealed class LoginModel
{
    public AuthMode Mode { get; private set; }
    public bool IsBusy { get; private set; }
    public bool Required { get; private set; }
    public void SetBusy(bool busy) => IsBusy = busy;
    public void SetRequired(bool required) => Required = required;
    public string Message { get; private set; } = "";
    public void SwitchMode(AuthMode mode) { Mode = mode; Message = ""; }
    public bool Validate(string account, string password, string confirmation)
    {
        Message = AuthValidation.Validate(Mode, account, password, confirmation);
        return Message.Length == 0;
    }
}

/// <summary>Transient form rules only. Credentials are never persisted by the UI.</summary>
public static class AuthValidation
{
    public static string Validate(AuthMode mode, string account, string password, string confirmation)
    {
        if (string.IsNullOrWhiteSpace(account)) return "请输入账号";
        if (string.IsNullOrWhiteSpace(password)) return "请输入密码";
        if (mode == AuthMode.Register && password != confirmation) return "两次密码不一致";
        return "";
    }
}
