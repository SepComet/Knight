using System;
using Google.Protobuf;
using SepCore.Definition;
using SepCore.Runtime.Network;
using Xunit;

namespace SepCore.Tests;

// Envelope 编解码与 msg_id/payload 一致性校验(Docs/protocol.md §2、§11.3)
public class EnvelopeCodecTests
{
    [Fact]
    public void EncodeDecode_RoundTripsHandshakeReq()
    {
        var bytes = EnvelopeCodec.Encode(MsgId.MsgHandshakeReq, new HandshakeReq
        {
            ProtoVersion = NetworkClient.ProtoVersion,
            Token = "token-1",
        });

        var envelope = EnvelopeCodec.Decode(bytes);

        Assert.Equal(MsgId.MsgHandshakeReq, envelope.MsgId);
        var req = Assert.IsType<HandshakeReq>(EnvelopeCodec.Extract(envelope));
        Assert.Equal((uint)NetworkClient.ProtoVersion, req.ProtoVersion);
        Assert.Equal("token-1", req.Token);
    }

    [Fact]
    public void EncodeDecode_RoundTripsSnapshotWithFields()
    {
        var snapshot = new Snapshot
        {
            Tick = 120,
            LastInputSeq = 7,
            Entities = { new EntityState { EntityId = 1, PosX = 1.5f, PosY = 2.5f } },
        };

        var envelope = EnvelopeCodec.Decode(EnvelopeCodec.Encode(MsgId.MsgSnapshot, snapshot));

        Assert.Equal(MsgId.MsgSnapshot, envelope.MsgId);
        var parsed = Assert.IsType<Snapshot>(EnvelopeCodec.Extract(envelope));
        Assert.Equal((uint)120, parsed.Tick);
        Assert.Equal((uint)7, parsed.LastInputSeq);
        Assert.Single(parsed.Entities);
        Assert.Equal((uint)1, parsed.Entities[0].EntityId);
    }

    [Fact]
    public void EncodeDecode_RoundTripsEmptyHeartbeat()
    {
        var envelope = EnvelopeCodec.Decode(EnvelopeCodec.Encode(MsgId.MsgHeartbeat, new Heartbeat()));

        Assert.Equal(MsgId.MsgHeartbeat, envelope.MsgId);
        Assert.IsType<Heartbeat>(EnvelopeCodec.Extract(envelope));
    }

    [Fact]
    public void Decode_Throws_WhenMsgIdMismatchPayload()
    {
        // msg_id 与 payload 分支不一致:按协议违规处理
        var envelope = new Envelope
        {
            MsgId = MsgId.MsgHeartbeat,
            HandshakeReq = new HandshakeReq { Token = "spoof" },
        };

        var ex = Assert.Throws<EnvelopeViolationException>(() => EnvelopeCodec.Decode(envelope.ToByteArray()));
        Assert.Contains("不一致", ex.Message);
    }

    [Fact]
    public void Decode_Throws_WhenPayloadMissing()
    {
        var envelope = new Envelope { MsgId = MsgId.MsgHeartbeat };

        Assert.Throws<EnvelopeViolationException>(() => EnvelopeCodec.Decode(envelope.ToByteArray()));
    }

    [Fact]
    public void Decode_Throws_OnGarbageBytes()
    {
        Assert.Throws<EnvelopeViolationException>(() => EnvelopeCodec.Decode([0xFF, 0xFF, 0xFF, 0x01]));
    }

    [Fact]
    public void Encode_Throws_WhenMessageTypeIdMismatch()
    {
        Assert.Throws<ArgumentException>(
            () => EnvelopeCodec.Encode(MsgId.MsgHeartbeat, new HandshakeReq()));
    }

    [Fact]
    public void Encode_Throws_OnUndefinedMsgId()
    {
        Assert.Throws<ArgumentException>(
            () => EnvelopeCodec.Encode(MsgId.MsgNone, new Heartbeat()));
    }

    [Fact]
    public void MsgIdFor_ResolvesAllProtocolMessages()
    {
        Assert.Equal(MsgId.MsgHandshakeReq, EnvelopeCodec.MsgIdFor<HandshakeReq>());
        Assert.Equal(MsgId.MsgHandshakeAck, EnvelopeCodec.MsgIdFor<HandshakeAck>());
        Assert.Equal(MsgId.MsgHeartbeat, EnvelopeCodec.MsgIdFor<Heartbeat>());
        Assert.Equal(MsgId.MsgError, EnvelopeCodec.MsgIdFor<Error>());
        Assert.Equal(MsgId.MsgCreateRoomReq, EnvelopeCodec.MsgIdFor<CreateRoomReq>());
        Assert.Equal(MsgId.MsgJoinRoomReq, EnvelopeCodec.MsgIdFor<JoinRoomReq>());
        Assert.Equal(MsgId.MsgLeaveRoom, EnvelopeCodec.MsgIdFor<LeaveRoom>());
        Assert.Equal(MsgId.MsgSetReady, EnvelopeCodec.MsgIdFor<SetReady>());
        Assert.Equal(MsgId.MsgStartGame, EnvelopeCodec.MsgIdFor<StartGame>());
        Assert.Equal(MsgId.MsgRoomStatePush, EnvelopeCodec.MsgIdFor<RoomStatePush>());
        Assert.Equal(MsgId.MsgGameStart, EnvelopeCodec.MsgIdFor<GameStart>());
        Assert.Equal(MsgId.MsgEntitySpawn, EnvelopeCodec.MsgIdFor<EntitySpawn>());
        Assert.Equal(MsgId.MsgEntityDespawn, EnvelopeCodec.MsgIdFor<EntityDespawn>());
        Assert.Equal(MsgId.MsgDamageEvent, EnvelopeCodec.MsgIdFor<DamageEvent>());
        Assert.Equal(MsgId.MsgDoorState, EnvelopeCodec.MsgIdFor<DoorState>());
        Assert.Equal(MsgId.MsgRoomTransition, EnvelopeCodec.MsgIdFor<RoomTransition>());
        Assert.Equal(MsgId.MsgRoomLoaded, EnvelopeCodec.MsgIdFor<RoomLoaded>());
        Assert.Equal(MsgId.MsgItemPicked, EnvelopeCodec.MsgIdFor<ItemPicked>());
        Assert.Equal(MsgId.MsgPlayerLeft, EnvelopeCodec.MsgIdFor<PlayerLeft>());
        Assert.Equal(MsgId.MsgGameResult, EnvelopeCodec.MsgIdFor<GameResult>());
        Assert.Equal(MsgId.MsgSnapshot, EnvelopeCodec.MsgIdFor<Snapshot>());
        Assert.Equal(MsgId.MsgInputBatch, EnvelopeCodec.MsgIdFor<InputBatch>());
    }

    [Fact]
    public void MsgIdFor_Throws_ForNonProtocolType()
    {
        Assert.Throws<ArgumentException>(() => EnvelopeCodec.MsgIdFor(typeof(string)));
    }

    [Fact]
    public void ExpectedMsgId_MatchesMsgIdFor_ForEveryPayloadCase()
    {
        // 全量一致性:每个 oneof 分支反查的 MsgId 必须与正向映射互逆(防四张 switch 表漂移)
        foreach (var payloadCase in Enum.GetValues<Envelope.PayloadOneofCase>())
        {
            if (payloadCase == Envelope.PayloadOneofCase.None)
            {
                Assert.Equal(MsgId.MsgNone, EnvelopeCodec.ExpectedMsgId(payloadCase));
                continue;
            }

            var msgId = EnvelopeCodec.ExpectedMsgId(payloadCase);
            Assert.NotEqual(MsgId.MsgNone, msgId);

            // 反查结果再经正查验证:构造一个空 Envelope 编解码往返,MsgIdFor(Type) 需与之互逆
            var envelope = new Envelope { MsgId = msgId };
            SetPayload(envelope, payloadCase);
            Assert.Equal(msgId, EnvelopeCodec.Decode(envelope.ToByteArray()).MsgId);
        }
    }

    private static void SetPayload(Envelope envelope, Envelope.PayloadOneofCase payloadCase)
    {
        // 每个 case 写入一个空消息,验证 Encode/Decode/ExpectedMsgId 三表互逆
        switch (payloadCase)
        {
            case Envelope.PayloadOneofCase.HandshakeReq: envelope.HandshakeReq = new HandshakeReq(); break;
            case Envelope.PayloadOneofCase.HandshakeAck: envelope.HandshakeAck = new HandshakeAck(); break;
            case Envelope.PayloadOneofCase.Heartbeat: envelope.Heartbeat = new Heartbeat(); break;
            case Envelope.PayloadOneofCase.Error: envelope.Error = new Error(); break;
            case Envelope.PayloadOneofCase.CreateRoomReq: envelope.CreateRoomReq = new CreateRoomReq(); break;
            case Envelope.PayloadOneofCase.JoinRoomReq: envelope.JoinRoomReq = new JoinRoomReq(); break;
            case Envelope.PayloadOneofCase.LeaveRoom: envelope.LeaveRoom = new LeaveRoom(); break;
            case Envelope.PayloadOneofCase.SetReady: envelope.SetReady = new SetReady(); break;
            case Envelope.PayloadOneofCase.StartGame: envelope.StartGame = new StartGame(); break;
            case Envelope.PayloadOneofCase.RoomStatePush: envelope.RoomStatePush = new RoomStatePush(); break;
            case Envelope.PayloadOneofCase.GameStart: envelope.GameStart = new GameStart(); break;
            case Envelope.PayloadOneofCase.GetRoomListReq: envelope.GetRoomListReq = new GetRoomListReq(); break;
            case Envelope.PayloadOneofCase.GetRoomListResp: envelope.GetRoomListResp = new GetRoomListResp(); break;
            case Envelope.PayloadOneofCase.EntitySpawn: envelope.EntitySpawn = new EntitySpawn(); break;
            case Envelope.PayloadOneofCase.EntityDespawn: envelope.EntityDespawn = new EntityDespawn(); break;
            case Envelope.PayloadOneofCase.DamageEvent: envelope.DamageEvent = new DamageEvent(); break;
            case Envelope.PayloadOneofCase.DoorState: envelope.DoorState = new DoorState(); break;
            case Envelope.PayloadOneofCase.RoomTransition: envelope.RoomTransition = new RoomTransition(); break;
            case Envelope.PayloadOneofCase.RoomLoaded: envelope.RoomLoaded = new RoomLoaded(); break;
            case Envelope.PayloadOneofCase.ItemPicked: envelope.ItemPicked = new ItemPicked(); break;
            case Envelope.PayloadOneofCase.PlayerLeft: envelope.PlayerLeft = new PlayerLeft(); break;
            case Envelope.PayloadOneofCase.GameResult: envelope.GameResult = new GameResult(); break;
            case Envelope.PayloadOneofCase.Snapshot: envelope.Snapshot = new Snapshot(); break;
            case Envelope.PayloadOneofCase.InputBatch: envelope.InputBatch = new InputBatch(); break;
        }
    }
}
