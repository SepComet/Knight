#nullable enable
using System;
using System.Linq;
using System.Collections.Generic;
using System.Text;
using SepCore.Definition;
using SepCore.Runtime.Network;
using ProtocolError = SepCore.Definition.Error;

namespace SepCore.UI.Rooms;

/// <summary>Existing room protocol only. A request succeeds only after an authoritative room push.</summary>
public sealed class RoomSessionClient : IDisposable
{
    private readonly INetworkMessageClient _network;
    private readonly IDisposable _stateSubscription, _errorSubscription, _listSubscription;
    private RoomPasswordMode? _pending;
    private uint _target;
    private float _elapsed;
    public bool IsBusy => _pending.HasValue;
    public bool IsListing { get; private set; }
    public RoomStatePush? Room { get; private set; }
    public event Action? StateChanged;
    public event Action<RoomStatePush>? RoomChanged;
    public event Action? RoomLeft;
    public event Action<string>? Failed;
    public event Action<IReadOnlyList<RoomSummary>>? RoomsListed;

    public RoomSessionClient(INetworkMessageClient network)
    {
        _network = network;
        _stateSubscription = network.Subscribe<RoomStatePush>(OnRoom);
        _errorSubscription = network.Subscribe<ProtocolError>(OnError);
        _listSubscription = network.Subscribe<GetRoomListResp>(OnList);
        network.Disconnected += OnDisconnected;
    }

    public void Request(RoomIntent intent)
    {
        if (IsBusy || IsListing) return;
        if (_network.State != NetworkState.Connected) { Failed?.Invoke("尚未连接服务器，请先完成登录和连接"); return; }
        if (Room != null) { Failed?.Invoke("请先离开当前房间"); return; }
        if (Encoding.UTF8.GetByteCount(intent.Password) > 32) { Failed?.Invoke("密码超过服务端 32 字节限制"); return; }
        uint target = 0;
        if (intent.Mode == RoomPasswordMode.Join && (!uint.TryParse(intent.RoomId, out target) || target == 0))
        { Failed?.Invoke("房间号无效"); return; }
        _pending = intent.Mode;
        _target = target;
        _elapsed = 0;
        StateChanged?.Invoke();
        try
        {
            if (intent.Mode == RoomPasswordMode.Create)
                _network.Send(new CreateRoomReq { Password = intent.Password });
            else
                _network.Send(new JoinRoomReq { RoomId = target, Password = intent.Password });
        }
        catch (InvalidOperationException) { Fail("连接状态已变化，请重试"); }
    }

    public void RequestList()
    {
        if (IsBusy || IsListing || Room != null) return;
        if (_network.State != NetworkState.Connected)
        {
            RoomsListed?.Invoke(Array.Empty<RoomSummary>());
            Failed?.Invoke("尚未连接服务器，请先完成登录和连接");
            return;
        }
        IsListing = true;
        _elapsed = 0;
        StateChanged?.Invoke();
        try { _network.Send(new GetRoomListReq()); }
        catch (InvalidOperationException) { Fail("连接状态已变化，请重试"); }
    }

    private void OnList(GetRoomListResp response)
    {
        if (!IsListing) return;
        var rooms = response.Rooms.Where(room => room.RoomId > 0 && room.State != RoomSessionState.RoomClosed)
            .GroupBy(room => room.RoomId).Select(group => group.Last()).OrderBy(room => room.RoomId)
            .Select(room => new RoomSummary(room.RoomId.ToString(), room.RoomId.ToString(),
                (int)Math.Min(room.PlayerCount, int.MaxValue), 4, room.State switch
                {
                    RoomSessionState.RoomWaiting => "等待中",
                    RoomSessionState.RoomPreparing => "准备中",
                    RoomSessionState.RoomRunning => "游戏中",
                    RoomSessionState.RoomVictory => "已通关",
                    RoomSessionState.RoomDefeat => "已结束",
                    _ => "不可加入"
                }, room.HasPassword, room.State == RoomSessionState.RoomWaiting)).ToArray();
        IsListing = false;
        RoomsListed?.Invoke(Array.AsReadOnly(rooms));
        StateChanged?.Invoke();
    }

    public void Update(float delta)
    {
        if (!IsBusy && !IsListing) return;
        _elapsed += delta;
        if (_elapsed < 10) return;
        // Protocol has no request IDs: disconnect before retry to exclude late acknowledgements.
        _network.Disconnect("房间请求超时，请重新连接");
        if (IsBusy || IsListing) Fail("房间请求超时，请重新连接");
    }

    public void Leave()
    {
        if (IsBusy) return;
        if (Room != null && _network.State == NetworkState.Connected)
            _network.Send(new LeaveRoom());
        Room = null;
        RoomLeft?.Invoke();
    }

    private void OnRoom(RoomStatePush push)
    {
        var containsLocal = push.Members.Any(member => member.Uid == _network.Uid);
        if (!containsLocal || push.State == RoomSessionState.RoomClosed)
        {
            if (Room?.RoomId == push.RoomId) { Room = null; RoomLeft?.Invoke(); }
            return;
        }
        if (IsBusy)
        {
            if (_pending == RoomPasswordMode.Join && push.RoomId != _target) return;
            if (_pending == RoomPasswordMode.Create && push.OwnerUid != _network.Uid) return;
            _pending = null;
            StateChanged?.Invoke();
        }
        else if (Room?.RoomId != push.RoomId) return;
        Room = push.Clone();
        RoomChanged?.Invoke(Room.Clone());
    }

    private void OnError(ProtocolError error)
    {
        if (!IsBusy && !IsListing) return;
        Fail(error.Code switch
        {
            ErrorCode.ErrRoomNotFound => "房间不存在",
            ErrorCode.ErrRoomFull => "房间已满",
            ErrorCode.ErrGameAlreadyStarted => "房间已开局",
            ErrorCode.ErrRoomPasswordMismatch => "房间密码不正确",
            _ => "房间请求被服务器拒绝"
        });
    }

    private void Fail(string message)
    {
        _pending = null;
        IsListing = false;
        StateChanged?.Invoke();
        Failed?.Invoke(message);
    }

    private void OnDisconnected(string reason)
    {
        var hadRoom = Room != null;
        var wasBusy = IsBusy || IsListing;
        Room = null;
        _pending = null;
        IsListing = false;
        RoomsListed?.Invoke(Array.Empty<RoomSummary>());
        StateChanged?.Invoke();
        if (hadRoom) RoomLeft?.Invoke();
        if (hadRoom || wasBusy) Failed?.Invoke("连接已断开：" + reason);
    }

    public void Dispose()
    {
        _stateSubscription.Dispose();
        _errorSubscription.Dispose();
        _listSubscription.Dispose();
        _network.Disconnected -= OnDisconnected;
    }
}
