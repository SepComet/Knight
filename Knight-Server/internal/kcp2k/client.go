package kcp2k

import (
	"net"
	"time"
)

// Client 对应 kcp2k KcpClient.cs：客户端角色 peer。
// 生产环境由 Godot 客户端扮演；本类型用于服务端互操作测试与诊断工具。

// ClientCallbacks 客户端回调。
type ClientCallbacks struct {
	OnConnected    func() // bare 握手完成（收到服务端 Hello）
	OnData         func(message []byte, channel Channel)
	OnDisconnected func()
	OnError        func(code ErrorCode, message string)
}

// Client 客户端连接。
type Client struct {
	peer      *Peer
	conn      *net.UDPConn
	remote    *net.UDPAddr
	packets   chan udpPacket
	rawBuffer []byte
	// 时间基准：首个 tick 时的外部时钟，peer 使用连接本地毫秒
	//（等价 C# 每 peer 独立 Stopwatch）。
	base    uint32
	baseSet bool
}

// local 将外部时钟换算为连接本地毫秒。
func (c *Client) local(now uint32) uint32 {
	if !c.baseSet {
		c.base = now
		c.baseSet = true
	}
	return now - c.base
}

// Dial 解析地址、建立 UDP 套接字并立即发送 Hello（对应 KcpClient.Connect）。
func Dial(cfg Config, address string, cb ClientCallbacks) (*Client, error) {
	remote, err := net.ResolveUDPAddr("udp", address)
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp", &net.UDPAddr{})
	if err != nil {
		return nil, err
	}

	c := &Client{
		conn:      conn,
		remote:    remote,
		packets:   make(chan udpPacket, 1024),
		rawBuffer: make([]byte, cfg.Mtu),
	}
	c.peer = NewPeer(cfg, 0, PeerCallbacks{
		OnAuthenticated: func() {
			if cb.OnConnected != nil {
				cb.OnConnected()
			}
		},
		OnData: func(message []byte, channel Channel) {
			if cb.OnData != nil {
				cb.OnData(message, channel)
			}
		},
		OnDisconnected: func() {
			_ = conn.Close()
			if cb.OnDisconnected != nil {
				cb.OnDisconnected()
			}
		},
		OnError: func(code ErrorCode, message string) {
			if cb.OnError != nil {
				cb.OnError(code, message)
			}
		},
		RawSend: func(data []byte) {
			_, _ = conn.WriteToUDP(data, c.remote)
		},
	})

	go c.readLoop()

	// 立即发送 Hello；此时 cookie=0，服务端回包后采纳
	c.peer.SendHello()
	return c, nil
}

// readLoop 阻塞读 UDP 并投递副本（conn 关闭后退出）。
func (c *Client) readLoop() {
	for {
		n, addr, err := c.conn.ReadFromUDP(c.rawBuffer)
		if err != nil {
			return
		}
		pkt := udpPacket{data: append([]byte(nil), c.rawBuffer[:n]...), addr: addr}
		select {
		case c.packets <- pkt:
		default:
			// 测试客户端，溢出直接丢弃
		}
	}
}

// Send 发送应用层消息。未完成 bare 握手时拒绝发送
// （与 C# KcpClient.Send 行为一致：认证前的 Data 带 cookie=0，会被服务端丢弃）。
func (c *Client) Send(data []byte, channel Channel) {
	if c.peer.State() != PeerAuthenticated {
		return
	}
	c.peer.SendData(data, channel)
}

// Disconnect 主动断开。
func (c *Client) Disconnect() {
	c.peer.Disconnect()
}

// RTT 最近往返延迟。
func (c *Client) RTT() uint32 { return c.peer.RTT() }

// State 当前状态。
func (c *Client) State() PeerState { return c.peer.State() }

// TickIncoming 非阻塞收取 UDP 并驱动入站状态机（测试手动泵用）。
func (c *Client) TickIncoming(now uint32) {
	local := c.local(now)
	c.drain()
	c.peer.TickIncoming(local)
}

// TickOutgoing 驱动出站 Flush。
func (c *Client) TickOutgoing(now uint32) {
	c.peer.TickOutgoing(c.local(now))
}

func (c *Client) drain() {
	for {
		select {
		case pkt := <-c.packets:
			c.rawInput(pkt.data, pkt.addr)
		default:
			return
		}
	}
}

// rawInput 对应 KcpClient.RawInput：解析 channel+cookie（首个非零 cookie 被采纳）。
func (c *Client) rawInput(segment []byte, _ *net.UDPAddr) {
	if len(segment) <= 5 {
		return
	}
	channel := segment[0]
	messageCookie, _ := decode32u(segment, 1)

	if messageCookie == 0 {
		// 服务端不应发送 cookie=0 的包；按 C# 行为记录后继续
		c.peer.cb.OnError(ErrUnexpected, "received message with cookie=0")
	}

	switch c.peer.cookie {
	case 0:
		c.peer.cookie = messageCookie // 采纳初始 cookie
	default:
		if c.peer.cookie != messageCookie {
			return // 不匹配丢弃
		}
	}

	payload := segment[5:]
	switch Channel(channel) {
	case ChannelReliable:
		c.peer.RawInputReliable(payload)
	case ChannelUnreliable:
		c.peer.RawInputUnreliable(payload, 0)
	}
}

// time 引用占位：包内其他文件使用 time 包，此处避免误删 import。
var _ = time.Now
