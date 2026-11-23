package event

// ---------------------------------------------------------------- 事件契约（v1）
//
// 服务端事件清单与 payload 契约，角色对齐客户端 GameEventType.cs：
// 每个事件集中声明 ID 与 payload 结构体，构成订阅/发布双方的契约。
// 游戏域（消息域 64~127）事件随对局逻辑落地在此追加。

// ---- 会话域（session.Manager 发布）----

var (
	// SessionReady 握手成功、uid 绑定完成（HandshakeAck 发出后）。
	SessionReady = Declare[SessionReadyArgs]("session.ready")
	// SessionEnd 会话结束。唯一发布点为连接清理（OnKcpDisconnect）：
	// 超时踢出与拒绝后延迟断开均汇入同一路径，恰好发布一次。
	SessionEnd = Declare[SessionEndArgs]("session.end")
)

// SessionEnd 的 Reason 取值。
const (
	SessionEndDisconnect = "disconnect" // 对端断开或传输层判死
	SessionEndTimeout    = "timeout"    // 应用层心跳超时踢出
	SessionEndRejected   = "rejected"   // 握手/协议违规拒绝后关闭
)

// SessionReadyArgs 是 SessionReady 的 payload。
type SessionReadyArgs struct {
	UID    uint64
	Name   string
	ConnID string
}

// SessionEndArgs 是 SessionEnd 的 payload。UID 为 0 表示未完成握手。
type SessionEndArgs struct {
	UID    uint64
	ConnID string
	Reason string // SessionEndReason 常量之一
}

// ---- 大厅域（lobby.Manager 发布）----

var (
	RoomCreated   = Declare[RoomCreatedArgs]("lobby.room_created")
	RoomDestroyed = Declare[RoomDestroyedArgs]("lobby.room_destroyed")
	MemberJoined  = Declare[MemberJoinedArgs]("lobby.member_joined")
	MemberLeft    = Declare[MemberLeftArgs]("lobby.member_left")
	OwnerChanged  = Declare[OwnerChangedArgs]("lobby.owner_changed")
	GameStarted   = Declare[GameStartedArgs]("lobby.game_started")
)

// MemberLeft 的 Reason 取值。
const (
	LeaveActive     = "leave"      // 主动退房（LeaveRoom）
	LeaveDisconnect = "disconnect" // 会话结束触发退房
)

// RoomCreatedArgs 是 RoomCreated 的 payload。
type RoomCreatedArgs struct {
	RoomID   uint32
	OwnerUID uint64
}

// RoomDestroyedArgs 是 RoomDestroyed 的 payload。
type RoomDestroyedArgs struct {
	RoomID uint32
}

// MemberJoinedArgs 是 MemberJoined 的 payload。
type MemberJoinedArgs struct {
	RoomID uint32
	UID    uint64
	Name   string
}

// MemberLeftArgs 是 MemberLeft 的 payload。
type MemberLeftArgs struct {
	RoomID uint32
	UID    uint64
	Reason string // LeaveReason 常量之一
}

// OwnerChangedArgs 是 OwnerChanged 的 payload。
type OwnerChangedArgs struct {
	RoomID  uint32
	FromUID uint64
	ToUID   uint64
}

// GameStartedArgs 是 GameStarted 的 payload（后续战绩记录的订阅落点）。
type GameStartedArgs struct {
	RoomID     uint32
	MapIDs     []uint32
	PlayerUIDs []uint64
}
