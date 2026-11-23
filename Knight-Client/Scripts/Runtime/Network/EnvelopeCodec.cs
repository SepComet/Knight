#nullable enable
using System;
using Google.Protobuf;
using SepCore.Definition;

namespace SepCore.Runtime.Network;

/// <summary>
/// Envelope 封包编解码:负责 MsgId ↔ oneof payload 的映射与一致性校验。
/// 每个 KCP 包 = 恰好一条 Envelope（KCP 消息模式，无长度前缀，见 Docs/protocol.md §2）。
/// 协议契约：msg_id 必须与 payload 分支一致，不一致按协议违规处理。
/// </summary>
/// <remarks>
/// 新增消息时需同步更新本文件的 4 个 switch（Encode / ExpectedMsgId / Extract / MsgIdFor），
/// NetworkClientCodecTests 的全量覆盖测试会兜底发现遗漏。
/// </remarks>
public static class EnvelopeCodec
{
    /// <summary>编码为单个 KCP 包载荷（Envelope 序列化字节）。</summary>
    /// <exception cref="ArgumentException">MsgId 未定义或不匹配 message 类型时抛出。</exception>
    public static byte[] Encode(MsgId msgId, IMessage message)
    {
        var envelope = new Envelope { MsgId = msgId };
        switch (msgId)
        {
            // 1~31 连接与会话
            case MsgId.MsgHandshakeReq: envelope.HandshakeReq = As<HandshakeReq>(message); break;
            case MsgId.MsgHandshakeAck: envelope.HandshakeAck = As<HandshakeAck>(message); break;
            case MsgId.MsgHeartbeat: envelope.Heartbeat = As<Heartbeat>(message); break;
            case MsgId.MsgError: envelope.Error = As<Error>(message); break;
            // 32~63 大厅与房间
            case MsgId.MsgCreateRoomReq: envelope.CreateRoomReq = As<CreateRoomReq>(message); break;
            case MsgId.MsgJoinRoomReq: envelope.JoinRoomReq = As<JoinRoomReq>(message); break;
            case MsgId.MsgLeaveRoom: envelope.LeaveRoom = As<LeaveRoom>(message); break;
            case MsgId.MsgSetReady: envelope.SetReady = As<SetReady>(message); break;
            case MsgId.MsgStartGame: envelope.StartGame = As<StartGame>(message); break;
            case MsgId.MsgRoomStatePush: envelope.RoomStatePush = As<RoomStatePush>(message); break;
            case MsgId.MsgGameStart: envelope.GameStart = As<GameStart>(message); break;
            case MsgId.MsgGetRoomListReq: envelope.GetRoomListReq = As<GetRoomListReq>(message); break;
            case MsgId.MsgGetRoomListResp: envelope.GetRoomListResp = As<GetRoomListResp>(message); break;
            // 64~95 游戏内可靠事件
            case MsgId.MsgEntitySpawn: envelope.EntitySpawn = As<EntitySpawn>(message); break;
            case MsgId.MsgEntityDespawn: envelope.EntityDespawn = As<EntityDespawn>(message); break;
            case MsgId.MsgDamageEvent: envelope.DamageEvent = As<DamageEvent>(message); break;
            case MsgId.MsgDoorState: envelope.DoorState = As<DoorState>(message); break;
            case MsgId.MsgRoomTransition: envelope.RoomTransition = As<RoomTransition>(message); break;
            case MsgId.MsgRoomLoaded: envelope.RoomLoaded = As<RoomLoaded>(message); break;
            case MsgId.MsgItemPicked: envelope.ItemPicked = As<ItemPicked>(message); break;
            case MsgId.MsgPlayerLeft: envelope.PlayerLeft = As<PlayerLeft>(message); break;
            case MsgId.MsgGameResult: envelope.GameResult = As<GameResult>(message); break;
            // 96~127 高频通道
            case MsgId.MsgSnapshot: envelope.Snapshot = As<Snapshot>(message); break;
            case MsgId.MsgInputBatch: envelope.InputBatch = As<InputBatch>(message); break;
            default:
                throw new ArgumentException($"未定义的 MsgId: {msgId}", nameof(msgId));
        }

        return envelope.ToByteArray();
    }

    /// <summary>解析单个 KCP 包并校验 msg_id / payload 一致性。</summary>
    /// <exception cref="EnvelopeViolationException">解析失败、空载荷或两者不一致（协议违规）时抛出。</exception>
    public static Envelope Decode(ReadOnlySpan<byte> data)
    {
        Envelope envelope;
        try
        {
            envelope = Envelope.Parser.ParseFrom(data);
        }
        catch (InvalidProtocolBufferException e)
        {
            throw new EnvelopeViolationException($"Envelope 解析失败:{e.Message}", e);
        }

        if (envelope.PayloadCase == Envelope.PayloadOneofCase.None)
            throw new EnvelopeViolationException("Envelope 缺少 payload");

        var expected = ExpectedMsgId(envelope.PayloadCase);
        if (envelope.MsgId != expected)
            throw new EnvelopeViolationException(
                $"msg_id 与 payload 不一致:msg_id={envelope.MsgId} payload={envelope.PayloadCase} 期望 msg_id={expected}");

        return envelope;
    }

    /// <summary>取出 Envelope 的 oneof 载荷（派发用）；空载荷返回 null。</summary>
    public static IMessage? Extract(Envelope envelope)
    {
        return envelope.PayloadCase switch
        {
            Envelope.PayloadOneofCase.HandshakeReq => envelope.HandshakeReq,
            Envelope.PayloadOneofCase.HandshakeAck => envelope.HandshakeAck,
            Envelope.PayloadOneofCase.Heartbeat => envelope.Heartbeat,
            Envelope.PayloadOneofCase.Error => envelope.Error,
            Envelope.PayloadOneofCase.CreateRoomReq => envelope.CreateRoomReq,
            Envelope.PayloadOneofCase.JoinRoomReq => envelope.JoinRoomReq,
            Envelope.PayloadOneofCase.LeaveRoom => envelope.LeaveRoom,
            Envelope.PayloadOneofCase.SetReady => envelope.SetReady,
            Envelope.PayloadOneofCase.StartGame => envelope.StartGame,
            Envelope.PayloadOneofCase.RoomStatePush => envelope.RoomStatePush,
            Envelope.PayloadOneofCase.GameStart => envelope.GameStart,
            Envelope.PayloadOneofCase.GetRoomListReq => envelope.GetRoomListReq,
            Envelope.PayloadOneofCase.GetRoomListResp => envelope.GetRoomListResp,
            Envelope.PayloadOneofCase.EntitySpawn => envelope.EntitySpawn,
            Envelope.PayloadOneofCase.EntityDespawn => envelope.EntityDespawn,
            Envelope.PayloadOneofCase.DamageEvent => envelope.DamageEvent,
            Envelope.PayloadOneofCase.DoorState => envelope.DoorState,
            Envelope.PayloadOneofCase.RoomTransition => envelope.RoomTransition,
            Envelope.PayloadOneofCase.RoomLoaded => envelope.RoomLoaded,
            Envelope.PayloadOneofCase.ItemPicked => envelope.ItemPicked,
            Envelope.PayloadOneofCase.PlayerLeft => envelope.PlayerLeft,
            Envelope.PayloadOneofCase.GameResult => envelope.GameResult,
            Envelope.PayloadOneofCase.Snapshot => envelope.Snapshot,
            Envelope.PayloadOneofCase.InputBatch => envelope.InputBatch,
            _ => null,
        };
    }

    /// <summary>按 payload 分支反查应有的 MsgId；空载荷返回 MSG_NONE。</summary>
    public static MsgId ExpectedMsgId(Envelope.PayloadOneofCase payloadCase)
    {
        return payloadCase switch
        {
            Envelope.PayloadOneofCase.HandshakeReq => MsgId.MsgHandshakeReq,
            Envelope.PayloadOneofCase.HandshakeAck => MsgId.MsgHandshakeAck,
            Envelope.PayloadOneofCase.Heartbeat => MsgId.MsgHeartbeat,
            Envelope.PayloadOneofCase.Error => MsgId.MsgError,
            Envelope.PayloadOneofCase.CreateRoomReq => MsgId.MsgCreateRoomReq,
            Envelope.PayloadOneofCase.JoinRoomReq => MsgId.MsgJoinRoomReq,
            Envelope.PayloadOneofCase.LeaveRoom => MsgId.MsgLeaveRoom,
            Envelope.PayloadOneofCase.SetReady => MsgId.MsgSetReady,
            Envelope.PayloadOneofCase.StartGame => MsgId.MsgStartGame,
            Envelope.PayloadOneofCase.RoomStatePush => MsgId.MsgRoomStatePush,
            Envelope.PayloadOneofCase.GameStart => MsgId.MsgGameStart,
            Envelope.PayloadOneofCase.GetRoomListReq => MsgId.MsgGetRoomListReq,
            Envelope.PayloadOneofCase.GetRoomListResp => MsgId.MsgGetRoomListResp,
            Envelope.PayloadOneofCase.EntitySpawn => MsgId.MsgEntitySpawn,
            Envelope.PayloadOneofCase.EntityDespawn => MsgId.MsgEntityDespawn,
            Envelope.PayloadOneofCase.DamageEvent => MsgId.MsgDamageEvent,
            Envelope.PayloadOneofCase.DoorState => MsgId.MsgDoorState,
            Envelope.PayloadOneofCase.RoomTransition => MsgId.MsgRoomTransition,
            Envelope.PayloadOneofCase.RoomLoaded => MsgId.MsgRoomLoaded,
            Envelope.PayloadOneofCase.ItemPicked => MsgId.MsgItemPicked,
            Envelope.PayloadOneofCase.PlayerLeft => MsgId.MsgPlayerLeft,
            Envelope.PayloadOneofCase.GameResult => MsgId.MsgGameResult,
            Envelope.PayloadOneofCase.Snapshot => MsgId.MsgSnapshot,
            Envelope.PayloadOneofCase.InputBatch => MsgId.MsgInputBatch,
            _ => MsgId.MsgNone,
        };
    }

    /// <summary>消息类型 → MsgId（Subscribe / 泛型 Send 用）。</summary>
    /// <exception cref="ArgumentException">类型不参与协议时抛出。</exception>
    public static MsgId MsgIdFor<T>() where T : IMessage => MsgIdFor(typeof(T));

    /// <summary>非泛型重载,按运行时类型查 MsgId。</summary>
    /// <exception cref="ArgumentException">类型不参与协议时抛出。</exception>
    public static MsgId MsgIdFor(Type type)
    {
        return type switch
        {
            // 1~31 连接与会话
            var t when t == typeof(HandshakeReq) => MsgId.MsgHandshakeReq,
            var t when t == typeof(HandshakeAck) => MsgId.MsgHandshakeAck,
            var t when t == typeof(Heartbeat) => MsgId.MsgHeartbeat,
            var t when t == typeof(Error) => MsgId.MsgError,
            // 32~63 大厅与房间
            var t when t == typeof(CreateRoomReq) => MsgId.MsgCreateRoomReq,
            var t when t == typeof(JoinRoomReq) => MsgId.MsgJoinRoomReq,
            var t when t == typeof(LeaveRoom) => MsgId.MsgLeaveRoom,
            var t when t == typeof(SetReady) => MsgId.MsgSetReady,
            var t when t == typeof(StartGame) => MsgId.MsgStartGame,
            var t when t == typeof(RoomStatePush) => MsgId.MsgRoomStatePush,
            var t when t == typeof(GameStart) => MsgId.MsgGameStart,
            var t when t == typeof(GetRoomListReq) => MsgId.MsgGetRoomListReq,
            var t when t == typeof(GetRoomListResp) => MsgId.MsgGetRoomListResp,
            // 64~95 游戏内可靠事件
            var t when t == typeof(EntitySpawn) => MsgId.MsgEntitySpawn,
            var t when t == typeof(EntityDespawn) => MsgId.MsgEntityDespawn,
            var t when t == typeof(DamageEvent) => MsgId.MsgDamageEvent,
            var t when t == typeof(DoorState) => MsgId.MsgDoorState,
            var t when t == typeof(RoomTransition) => MsgId.MsgRoomTransition,
            var t when t == typeof(RoomLoaded) => MsgId.MsgRoomLoaded,
            var t when t == typeof(ItemPicked) => MsgId.MsgItemPicked,
            var t when t == typeof(PlayerLeft) => MsgId.MsgPlayerLeft,
            var t when t == typeof(GameResult) => MsgId.MsgGameResult,
            // 96~127 高频通道
            var t when t == typeof(Snapshot) => MsgId.MsgSnapshot,
            var t when t == typeof(InputBatch) => MsgId.MsgInputBatch,
            _ => throw new ArgumentException($"类型不参与协议映射:{type.Name}"),
        };
    }

    private static T As<T>(IMessage message) where T : class, IMessage
    {
        if (message is T typed)
            return typed;
        throw new ArgumentException($"MsgId 与消息类型不匹配:期望 {typeof(T).Name} 实际 {message.GetType().Name}");
    }
}

/// <summary>收包方向违反 Envelope 契约（解析失败 / msg_id 与 payload 不一致 / 空载荷）。</summary>
public sealed class EnvelopeViolationException(string message, Exception? inner = null)
    : Exception(message, inner);
