// Package protocol 封装 Envelope 的编解码与契约校验，
// 与客户端 Knight-Client/Scripts/Runtime/Network/EnvelopeCodec.cs 语义对称：
// msg_id 必须与 oneof payload 分支一致，不一致按协议违规处理。
package protocol

import (
	"errors"
	"fmt"

	"google.golang.org/protobuf/proto"

	knight "knight-server/protogen"
)

// Decode 解析单个 KCP 包载荷（Envelope 字节）并校验 msg_id / payload 一致性。
func Decode(data []byte) (*knight.Envelope, error) {
	env := &knight.Envelope{}
	if err := proto.Unmarshal(data, env); err != nil {
		return nil, fmt.Errorf("envelope 解析失败: %w", err)
	}
	if env.GetPayload() == nil {
		return nil, errors.New("envelope 缺少 payload")
	}
	expected, ok := expectedMsgID(env)
	if !ok {
		return nil, fmt.Errorf("envelope 未知 payload 分支: %T", env.Payload)
	}
	if env.MsgId != expected {
		return nil, fmt.Errorf("msg_id 与 payload 不一致: msg_id=%v payload=%T 期望 msg_id=%v",
			env.MsgId, env.Payload, expected)
	}
	return env, nil
}

// Encode 填充 msg_id（按 payload 分支推导）并序列化。payload 为空返回错误。
func Encode(env *knight.Envelope) ([]byte, error) {
	if env.GetPayload() == nil {
		return nil, errors.New("envelope 缺少 payload")
	}
	expected, ok := expectedMsgID(env)
	if !ok {
		return nil, fmt.Errorf("未定义的 payload 分支: %T", env.Payload)
	}
	env.MsgId = expected
	return proto.Marshal(env)
}

// EncodeError 构造 S→C 错误消息字节。
func EncodeError(code knight.ErrorCode, message string) ([]byte, error) {
	return Encode(&knight.Envelope{Payload: &knight.Envelope_Error{
		Error: &knight.Error{Code: code, Message: message},
	}})
}

// EncodeAck 构造握手应答字节。
func EncodeAck(ack *knight.HandshakeAck) ([]byte, error) {
	return Encode(&knight.Envelope{Payload: &knight.Envelope_HandshakeAck{HandshakeAck: ack}})
}

// expectedMsgID 按 payload 分支反查应有的 MsgId（对应客户端 ExpectedMsgId）。
// 参数用 *knight.Envelope：oneof 持有接口类型未导出，通过对字段值做类型开关判别。
func expectedMsgID(env *knight.Envelope) (knight.MsgId, bool) {
	switch env.Payload.(type) {
	// 1~31 连接与会话
	case *knight.Envelope_HandshakeReq:
		return knight.MsgId_MSG_HANDSHAKE_REQ, true
	case *knight.Envelope_HandshakeAck:
		return knight.MsgId_MSG_HANDSHAKE_ACK, true
	case *knight.Envelope_Heartbeat:
		return knight.MsgId_MSG_HEARTBEAT, true
	case *knight.Envelope_Error:
		return knight.MsgId_MSG_ERROR, true
	// 32~63 大厅与房间
	case *knight.Envelope_CreateRoomReq:
		return knight.MsgId_MSG_CREATE_ROOM_REQ, true
	case *knight.Envelope_JoinRoomReq:
		return knight.MsgId_MSG_JOIN_ROOM_REQ, true
	case *knight.Envelope_LeaveRoom:
		return knight.MsgId_MSG_LEAVE_ROOM, true
	case *knight.Envelope_SetReady:
		return knight.MsgId_MSG_SET_READY, true
	case *knight.Envelope_StartGame:
		return knight.MsgId_MSG_START_GAME, true
	case *knight.Envelope_RoomStatePush:
		return knight.MsgId_MSG_ROOM_STATE_PUSH, true
	case *knight.Envelope_GameStart:
		return knight.MsgId_MSG_GAME_START, true
	case *knight.Envelope_GetRoomListReq:
		return knight.MsgId_MSG_GET_ROOM_LIST_REQ, true
	case *knight.Envelope_GetRoomListResp:
		return knight.MsgId_MSG_GET_ROOM_LIST_RESP, true
	// 64~95 游戏内可靠事件
	case *knight.Envelope_EntitySpawn:
		return knight.MsgId_MSG_ENTITY_SPAWN, true
	case *knight.Envelope_EntityDespawn:
		return knight.MsgId_MSG_ENTITY_DESPAWN, true
	case *knight.Envelope_DamageEvent:
		return knight.MsgId_MSG_DAMAGE_EVENT, true
	case *knight.Envelope_DoorState:
		return knight.MsgId_MSG_DOOR_STATE, true
	case *knight.Envelope_RoomTransition:
		return knight.MsgId_MSG_ROOM_TRANSITION, true
	case *knight.Envelope_RoomLoaded:
		return knight.MsgId_MSG_ROOM_LOADED, true
	case *knight.Envelope_ItemPicked:
		return knight.MsgId_MSG_ITEM_PICKED, true
	case *knight.Envelope_PlayerLeft:
		return knight.MsgId_MSG_PLAYER_LEFT, true
	case *knight.Envelope_GameResult:
		return knight.MsgId_MSG_GAME_RESULT, true
	case *knight.Envelope_SkillEvent:
		return knight.MsgId_MSG_SKILL_EVENT, true
	// 96~127 高频通道
	case *knight.Envelope_Snapshot:
		return knight.MsgId_MSG_SNAPSHOT, true
	case *knight.Envelope_InputBatch:
		return knight.MsgId_MSG_INPUT_BATCH, true
	default:
		return knight.MsgId_MSG_NONE, false
	}
}
