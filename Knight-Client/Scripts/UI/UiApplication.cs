#nullable enable
using System;
using System.Collections.Generic;
using System.Linq;
using HttpClient = System.Net.Http.HttpClient;
using Godot;
using SepCore.Definition;
using SepCore.Runtime.UI;
using SepCore.Runtime.Network;
using SepCore.UI.Auth;
using SepCore.UI.Auth.Services;
using SepCore.UI.Battle;
using SepCore.UI.Common;
using SepCore.UI.Rooms;
using SepCore.UI.Results;

namespace SepCore.UI;

/// <summary>Composition root for UI controllers, navigation, and account/room services.</summary>
public partial class UiApplication : Node
{
    public UiApplication() { }
    public UiApplication(AccountSessionManager accounts) => Accounts = accounts;
    /// <summary>Controllers are registered once and persist across procedure re-entry.</summary>
    public static UiApplication Ensure(UIComponent ui)
    {
        var app = ui.GetNodeOrNull<UiApplication>("UiApplication");
        if (app != null) return app;
        app = new UiApplication { Name = "UiApplication" };
        ui.AddChild(app);
        return app;
    }

    public MainMenuController MainMenu { get; } = new();
    public LoginController Login { get; } = new();
    public RoomSelectionController Rooms { get; } = new();
    public WaitingRoomController Waiting { get; } = new();
    public GameHudController Hud { get; } = new();
    public TouchControlsController Controls { get; } = new();
    public GameResultController Results { get; } = new();
    public AccountSessionManager Accounts { get; private set; } = null!;
    public RoomStatePush? CurrentRoom => _roomSession?.Room;

    public event Action? SettingsRequested, QuitRequested;
    public event Action<GameStart>? GameStarted;
    private UIComponent _ui = null!;
    private RoomSessionClient _roomSession = null!;
    private bool _lobbyActive;
    private HttpClient? _authHttp;
    private bool _registeredDuringAttempt;

    public override void _Ready()
    {
        _ui = GetParent<UIComponent>();
        _ui.Register(UIFormType.LobbyForm, MainMenu);
        _ui.Register(UIFormType.LoginForm, Login);
        _ui.Register(UIFormType.RoomSelection, Rooms);
        _ui.Register(UIFormType.WaitingRoom, Waiting);
        _ui.Register(UIFormType.RoundHUDForm, Hud);
        _ui.Register(UIFormType.JoystickForm, Controls);
        _ui.Register(UIFormType.RoundSettlementForm, Results);
        if (Accounts == null) CreateAccountManager();
        Accounts!.Changed += OnAccountChanged;
        Accounts.LoginRequired += OnLoginRequired;
        Accounts.RegistrationCompleted += OnRegistered;
        Accounts.PersistenceWarning += OnPersistenceWarning;
        MainMenu.AccountRequested += OnAccountRequested;
        Login.SubmissionRequested += OnLoginSubmitted;
        Login.Closed += Accounts.Cancel;
        OnAccountChanged();
        MainMenu.StartRequested += ShowRooms;
        MainMenu.SettingsRequested += () => SettingsRequested?.Invoke();
        MainMenu.QuitRequested += () => QuitRequested?.Invoke();
        Rooms.BackRequested += ShowMainMenu;
        _roomSession = new RoomSessionClient(NetworkComponent.Instance!.Client);
        Rooms.RoomActionRequested += OnRoomRequest;
        _roomSession.StateChanged += () =>
        {
            Rooms.SetBusy(_roomSession.IsBusy || _roomSession.IsListing);
            if (_roomSession.IsListing) Rooms.ShowMessage("正在获取房间列表…");
        };
        _roomSession.RoomsListed += rooms => Rooms.SetRooms(rooms);
        Rooms.RefreshRequested += _roomSession.RequestList;
        NetworkComponent.Instance.Client.Connected += OnConnected;
        _roomSession.Failed += message => { if (_lobbyActive) Rooms.ShowMessage(message); };
        _roomSession.RoomChanged += OnRoomChanged;
        _roomSession.RoomLeft += OnRoomLeft;
        _roomSession.GameStarted += OnGameStarted;
        Waiting.LeaveRequested += _roomSession.Leave;
        Waiting.ReadyChanged += _roomSession.SetReady;
        Waiting.StartRequested += _roomSession.StartGame;
        Results.ReturnRoomRequested += ShowWaitingRoom;
        Results.ReturnMenuRequested += ShowMainMenu;
        _ui.FormOpened += OnFormOpened;
    }

    public async void EnterLobby()
    {
        _lobbyActive = true;
        if (_roomSession.Room is { } room) OnRoomChanged(room);
        else ShowMainMenu();
        await Accounts.InitializeAsync();
    }
    public void LeaveLobby()
    {
        _lobbyActive = false;
        Accounts.Cancel();
        if (_roomSession.IsBusy || _roomSession.IsListing)
            NetworkComponent.Instance!.Client.Disconnect("退出大厅，取消房间请求");
    }

    public override void _Process(double delta) => _roomSession.Update((float)delta);

    private void OnRoomRequest(RoomIntent intent)
    {
        if (_lobbyActive && Accounts.IsAuthenticated) _roomSession.Request(intent);
    }

    private void OnRoomChanged(RoomStatePush room)
    {
        var uid = NetworkComponent.Instance!.Client.Uid;
        Waiting.SetPlayers(room.RoomId.ToString(), uid.ToString(), room.OwnerUid == uid,
            room.Members.Select(member => new WaitingPlayer(member.Uid.ToString(), member.Name, IsReady: member.Ready)));
        if (_lobbyActive && _ui.FindView(UIFormType.WaitingRoom) == null)
            ShowWaitingRoom();
    }

    private void OnRoomLeft()
    {
        Waiting.SetPlayers("", "", false, Array.Empty<WaitingPlayer>());
        if (_lobbyActive && _ui.FindView(UIFormType.WaitingRoom) != null) ShowRooms();
    }

    private void OnGameStarted(GameStart gameStart)
    {
        if (!_lobbyActive) return;
        GameStarted?.Invoke(gameStart);
    }

    public IReadOnlyDictionary<ulong, string> GetRoomMemberNames()
    {
        var names = new Dictionary<ulong, string>();
        if (_roomSession.Room is { } room)
        {
            foreach (var member in room.Members)
                names[member.Uid] = member.Name;
        }
        return names;
    }

    public void ShowMainMenu() => Navigate(UIFormType.LobbyForm);
    public void ShowRooms()
    {
        if (!Accounts.IsAuthenticated) { OpenLoginDialog(); return; }
        Navigate(UIFormType.RoomSelection);
        _roomSession.RequestList();
    }

    private void OnConnected()
    {
        if (_lobbyActive && Accounts.IsAuthenticated && _ui.FindView(UIFormType.RoomSelection) != null)
            _roomSession.RequestList();
    }
    public void ShowWaitingRoom() => Navigate(UIFormType.WaitingRoom);

    private void CreateAccountManager()
    {
        var baseUri = new Uri(ProjectSettings.GetSetting("network/http_base_url", "http://127.0.0.1:37780/").AsString());
        var host = ProjectSettings.GetSetting("network/kcp_host", "127.0.0.1").AsString();
        var portNumber = ProjectSettings.GetSetting("network/kcp_port", 37777).AsInt32();
        if (portNumber is < 1 or > 65535) throw new InvalidOperationException("KCP 端口配置无效");
        var port = (ushort)portNumber;
        _authHttp = new HttpClient { Timeout = TimeSpan.FromSeconds(10) };
        Accounts = new AccountSessionManager(new AuthApi(_authHttp, baseUri),
            new JsonLoginStore(ProjectSettings.GlobalizePath(LoginCachePath.Resolve(OS.GetCmdlineUserArgs()))),
            NetworkComponent.Instance!.Client, host, port, $"{baseUri.AbsoluteUri}|{host}:{port}");
    }

    private void OnAccountChanged()
    {
        MainMenu.SetAccount(Accounts.IsAuthenticated, Accounts.Session?.Name ?? "", Accounts.IsBusy);
        Login.SetBusy(Accounts.IsBusy);
        if (Accounts.IsAuthenticated)
        {
            Login.SetRequired(false);
            Login.OnCloseRequest();
            if (_lobbyActive && _ui.FindView(UIFormType.RoomSelection) != null)
                _roomSession.RequestList();
        }
    }

    private void OnLoginRequired()
    {
        if (!_lobbyActive) return;
        if (_ui.FindView(UIFormType.LobbyForm) == null) ShowMainMenu();
        OpenLoginDialog();
    }

    private void OpenLoginDialog()
    {
        if (!_lobbyActive) return;
        Login.SetRequired(false);
        _ui.Open(UIFormType.LoginForm, _registeredDuringAttempt ? AuthMode.Login : Login.Mode);
        Login.SetBusy(Accounts.IsBusy);
        Login.ShowMessage(Accounts.Message);
    }

    private async void OnLoginSubmitted(AuthSubmission submission)
    {
        _registeredDuringAttempt = false;
        await Accounts.SubmitAsync(submission);
    }

    private void OnRegistered()
    {
        _registeredDuringAttempt = true;
        Login.ShowMessage("注册成功，正在登录…");
    }

    private void OnAccountRequested()
    {
        if (Accounts.IsAuthenticated)
        {
            if (!Accounts.Logout()) { OnPersistenceWarning(Accounts.Message); return; }
            _registeredDuringAttempt = false;
            ShowMainMenu();
        }
        OpenLoginDialog();
    }

    private static void OnPersistenceWarning(string message) => GD.PushWarning(message);

    private void Navigate(UIFormType id)
    {
        _ui.CloseAll();
        _ui.Open(id);
    }

    private void OnFormOpened(UIForm form)
    {
        // Result is in Overlay, so same-group pause policy cannot stop Input.
        if (form.Config.Id == UIFormType.RoundSettlementForm)
            _ui.CloseGroup("Input");
    }

    public override void _ExitTree()
    {
        Accounts.Changed -= OnAccountChanged;
        Accounts.LoginRequired -= OnLoginRequired;
        Accounts.RegistrationCompleted -= OnRegistered;
        Accounts.PersistenceWarning -= OnPersistenceWarning;
        Accounts.Dispose();
        _authHttp?.Dispose();
        _ui.FormOpened -= OnFormOpened;
        _roomSession.Dispose();
        if (NetworkComponent.Instance != null)
            NetworkComponent.Instance.Client.Connected -= OnConnected;
    }
}
