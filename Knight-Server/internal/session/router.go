package session

import (
	"knight-server/internal/kcp2k"
	"knight-server/internal/protocol"

	knight "knight-server/protogen"
)

// Sender 发送抽象（kcp2k.Server 满足）；DomainRouter 仅用于未知域段的兜底错误应答。
// 业务层的同形接口见 lobby.Sender（连接域应答）与 lobby.UIDSender（用户域广播）。
type Sender interface {
	Send(connID string, data []byte, channel kcp2k.Channel)
}

// DomainRoute 一个消息域段到业务 Router 的映射。
// 域段对应 knight.proto MsgId 分配表 / protocol.md §5.1：
// 1~31 连接会话（session 层自处理，不注册）、32~63 大厅、64~95 游戏事件、96~127 高频。
type DomainRoute struct {
	Min, Max uint32 // 消息号域段（含端点）
	Router   Router
}

// NewDomainRouter 按 Envelope.MsgId 的域段把业务消息分发给对应 Router；
// 无匹配域段时按 ERR_INVALID_REQUEST "not implemented yet" 应答（不断开，
// 对齐 Manager 无路由时的行为）。域内未知消息仍由业务 Router 的 default
// 分支兜底——两层兜底各管各的：域外归 DomainRouter，域内归业务。
func NewDomainRouter(sender Sender, routes ...DomainRoute) Router {
	return &domainRouter{sender: sender, routes: routes}
}

type domainRouter struct {
	sender Sender
	routes []DomainRoute // 域段条目个位数，线性匹配即可
}

func (d *domainRouter) Route(uid uint64, name string, connID string, env *knight.Envelope) {
	id := uint32(env.MsgId)
	for _, r := range d.routes {
		if id >= r.Min && id <= r.Max {
			r.Router.Route(uid, name, connID, env)
			return
		}
	}
	if data, err := protocol.EncodeError(knight.ErrorCode_ERR_INVALID_REQUEST, "not implemented yet"); err == nil {
		d.sender.Send(connID, data, kcp2k.ChannelReliable)
	}
}
