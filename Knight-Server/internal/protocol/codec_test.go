package protocol

import (
	"bytes"
	"testing"

	"google.golang.org/protobuf/proto"

	knight "knight-server/protogen"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	data, err := Encode(&knight.Envelope{Payload: &knight.Envelope_Heartbeat{Heartbeat: &knight.Heartbeat{}}})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	env, err := Decode(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.MsgId != knight.MsgId_MSG_HEARTBEAT {
		t.Fatalf("msg_id = %v", env.MsgId)
	}
	if env.GetHeartbeat() == nil {
		t.Fatal("payload not heartbeat")
	}
}

func TestEncodeFillsMsgID(t *testing.T) {
	data, err := Encode(&knight.Envelope{Payload: &knight.Envelope_JoinRoomReq{
		JoinRoomReq: &knight.JoinRoomReq{RoomId: 42},
	}})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	env, err := Decode(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.MsgId != knight.MsgId_MSG_JOIN_ROOM_REQ || env.GetJoinRoomReq().RoomId != 42 {
		t.Fatalf("unexpected envelope: %v %+v", env.MsgId, env.GetJoinRoomReq())
	}
}

func TestDecodeRejectsMsgIDMismatch(t *testing.T) {
	// Heartbeat 载荷但 msg_id=MSG_ERROR：按协议违规拒绝
	raw, _ := proto.Marshal(&knight.Envelope{
		MsgId:   knight.MsgId_MSG_ERROR,
		Payload: &knight.Envelope_Heartbeat{Heartbeat: &knight.Heartbeat{}},
	})
	if _, err := Decode(raw); err == nil {
		t.Fatal("expected error for msg_id/payload mismatch")
	}
}

func TestDecodeRejectsEmptyPayload(t *testing.T) {
	raw, _ := proto.Marshal(&knight.Envelope{MsgId: knight.MsgId_MSG_HEARTBEAT})
	if _, err := Decode(raw); err == nil {
		t.Fatal("expected error for empty payload")
	}
}

func TestDecodeRejectsGarbage(t *testing.T) {
	if _, err := Decode(bytes.Repeat([]byte{0xFF}, 32)); err == nil {
		t.Fatal("expected error for garbage bytes")
	}
}

func TestEncodeError(t *testing.T) {
	data, err := EncodeError(knight.ErrorCode_ERR_KICKED_TIMEOUT, "heartbeat timeout")
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	env, err := Decode(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	e := env.GetError()
	if e == nil || e.Code != knight.ErrorCode_ERR_KICKED_TIMEOUT || e.Message != "heartbeat timeout" {
		t.Fatalf("unexpected error envelope: %+v", e)
	}
}

func TestEncodeRejectsEmptyPayload(t *testing.T) {
	if _, err := Encode(&knight.Envelope{}); err == nil {
		t.Fatal("expected error for empty payload")
	}
}
