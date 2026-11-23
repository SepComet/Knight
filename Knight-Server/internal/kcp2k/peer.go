package kcp2k

// Peer 对应 kcp2k highlevel/KcpPeer.cs：双角色（客户端/服务端共用）的连接状态机，
// 覆盖 bare 握手（Hello）、保活（Ping/Pong）、超时/死链/拥塞断开与双通道收发。
//
// 时间由持有方注入（now，单调毫秒，通常自进程/连接建立起累计）：
// 与 C# 版每 peer 一块 Stopwatch 等价——所有比较均为相对差值，不依赖绝对时钟。

// PeerState 对应 kcp2k KcpState：创建即 Connected；收到可靠 Hello 后 Authenticated。
type PeerState uint8

const (
	PeerConnected     PeerState = 0
	PeerAuthenticated PeerState = 1
	PeerDisconnected  PeerState = 2
)

// ErrorCode 对应 kcp2k ErrorCode（传输层错误，非游戏协议错误码）。
type ErrorCode uint8

const (
	ErrDnsResolve       ErrorCode = 0
	ErrTimeout          ErrorCode = 1
	ErrCongestion       ErrorCode = 2
	ErrInvalidReceive   ErrorCode = 3
	ErrInvalidSend      ErrorCode = 4
	ErrConnectionClosed ErrorCode = 5
	ErrUnexpected       ErrorCode = 6
)

func (c ErrorCode) String() string {
	switch c {
	case ErrDnsResolve:
		return "DnsResolve"
	case ErrTimeout:
		return "Timeout"
	case ErrCongestion:
		return "Congestion"
	case ErrInvalidReceive:
		return "InvalidReceive"
	case ErrInvalidSend:
		return "InvalidSend"
	case ErrConnectionClosed:
		return "ConnectionClosed"
	default:
		return "Unexpected"
	}
}

// Peer 协议常量（KcpPeer.cs）。
const (
	PingInterval             = 1000 // 保活间隔（毫秒）
	DefaultTimeout           = 10000
	QueueDisconnectThreshold = 10000
	disconnectRapidFire      = 5 // 主动断开时连发的不可靠 Disconnect 数（glenn fiedler）
)

// PeerCallbacks peer 层回调。全部由持有方提供；RawSend 输出完整 UDP 数据报。
type PeerCallbacks struct {
	OnAuthenticated func()
	OnData          func(message []byte, channel Channel)
	OnDisconnected  func()
	OnError         func(code ErrorCode, message string)
	RawSend         func(data []byte)
}

// Peer 连接状态机。
type Peer struct {
	kcp     *Kcp
	cookie  uint32
	state   PeerState
	timeout int

	lastReceiveTime uint32
	lastPingTime    uint32
	lastPongTime    uint32
	rttMilliseconds uint32

	reliableMax   int
	unreliableMax int

	kcpMessageBuffer []byte
	kcpSendBuffer    []byte
	rawSendBuffer    []byte

	cb PeerCallbacks
}

// NewPeer 创建 peer。cookie：服务端为每连接生成的防伪装值；客户端初始为 0（认证后采纳服务端的）。
func NewPeer(cfg Config, cookie uint32, cb PeerCallbacks) *Peer {
	p := &Peer{cookie: cookie, cb: cb}
	p.reset(cfg)
	return p
}

// reset 对应 KcpPeer.Reset：每次连接建立时重建全新 KCP 状态。
func (p *Peer) reset(cfg Config) {
	p.state = PeerConnected
	p.lastReceiveTime = 0
	p.lastPingTime = 0

	p.kcp = NewKcp(0, p.rawSendReliable)
	// C# 调用形式：SetNoDelay(NoDelay?1:0, Interval, FastResend, !CongestionWindow)
	nodelay := uint32(0)
	if cfg.NoDelay {
		nodelay = 1
	}
	p.kcp.SetNoDelay(nodelay, cfg.Interval, cfg.FastResend, !cfg.CongestionWindow)
	p.kcp.SetWindowSize(cfg.SendWindowSize, cfg.ReceiveWindowSize)

	// KCP 层 MTU 缩小 5 字节，为 channel+cookie 头腾位
	if err := p.kcp.SetMtu(uint32(cfg.Mtu - MetadataSizeReliable)); err != nil {
		panic("kcp2k: invalid mtu config: " + err.Error())
	}
	p.kcp.deadLink = cfg.MaxRetransmits
	p.timeout = cfg.Timeout

	p.reliableMax = ReliableMaxMessageSize(cfg.Mtu, cfg.ReceiveWindowSize)
	p.unreliableMax = UnreliableMaxMessageSize(cfg.Mtu)
	p.kcpMessageBuffer = make([]byte, 1+p.reliableMax)
	p.kcpSendBuffer = make([]byte, 1+p.reliableMax)
	p.rawSendBuffer = make([]byte, cfg.Mtu)
}

// RTT 最近一次往返延迟（毫秒），Pong 计算得出。
func (p *Peer) RTT() uint32 { return p.rttMilliseconds }

// State 当前状态。
func (p *Peer) State() PeerState { return p.state }

// Cookie 当前防伪装 cookie（客户端认证后为服务端分配值）。
func (p *Peer) Cookie() uint32 { return p.cookie }

// ---------------------------------------------------------------- 原始收发

// rawSendReliable 是 kcp 的 output 回调：为每个 KCP 数据报加 channel+cookie 头。
func (p *Peer) rawSendReliable(data []byte, size int) {
	p.rawSendBuffer[0] = byte(ChannelReliable)
	encode32u(p.rawSendBuffer, 1, p.cookie)
	copy(p.rawSendBuffer[5:], data[:size])
	p.cb.RawSend(p.rawSendBuffer[:5+size])
}

// RawInputReliable 将可靠通道载荷（KCP 段流）喂给状态机。channel+cookie 头已由持有方剥离。
func (p *Peer) RawInputReliable(message []byte) {
	if n := p.kcp.Input(message, 0, len(message)); n != 0 {
		p.cb.OnError(ErrInvalidReceive, "kcp input failed")
	}
}

// RawInputUnreliable 处理不可靠通道载荷（[1B 头][内容]），仅认证后接受 Data。
func (p *Peer) RawInputUnreliable(message []byte, now uint32) {
	if len(message) < 1 {
		return
	}
	header := message[0]
	content := message[1:]

	switch header {
	case HeaderUnreliableData:
		if p.state == PeerAuthenticated {
			p.cb.OnData(content, ChannelUnreliable)
			// 可靠与不可靠消息都算活性
			p.lastReceiveTime = now
		}
		// 未认证时忽略（网络噪音或竞态）
	case HeaderUnreliableDisconnect:
		p.Disconnect()
	default:
		p.cb.OnError(ErrInvalidReceive, "invalid unreliable header")
		p.Disconnect()
	}
}

// ---------------------------------------------------------------- 保活/超时/拥塞

func (p *Peer) handleTimeout(now uint32) {
	if timeDiff(now, p.lastReceiveTime) >= p.timeout {
		p.cb.OnError(ErrTimeout, "connection timed out")
		p.Disconnect()
	}
}

func (p *Peer) handleDeadLink() {
	if p.kcp.state == -1 {
		p.cb.OnError(ErrTimeout, "dead_link detected")
		p.Disconnect()
	}
}

func (p *Peer) handlePing(now uint32) {
	if timeDiff(now, p.lastPingTime) >= PingInterval {
		p.sendPing(now)
		p.lastPingTime = now
	}
}

func (p *Peer) handleChoked() {
	if p.kcp.QueueTotal() >= QueueDisconnectThreshold {
		p.cb.OnError(ErrCongestion, "queue overflow")
		p.kcp.ClearSendQueue()
		p.Disconnect()
	}
}

// ---------------------------------------------------------------- 驱动

// TickIncoming 处理收到的消息并驱动超时/保活状态机（对应 TickIncoming）。
func (p *Peer) TickIncoming(now uint32) {
	switch p.state {
	case PeerConnected:
		p.tickIncomingConnected(now)
	case PeerAuthenticated:
		p.tickIncomingAuthenticated(now)
	case PeerDisconnected:
		// do nothing
	}
}

func (p *Peer) tickIncomingConnected(now uint32) {
	p.handleTimeout(now)
	p.handleDeadLink()
	p.handlePing(now)
	p.handleChoked()

	// 注意：与 C# 一致，认证前每 tick 只处理一条可靠消息（if 而非 while）
	if header, message, ok := p.receiveNextReliable(now); ok {
		switch header {
		case HeaderHello:
			// 对端说的是我们的协议
			p.state = PeerAuthenticated
			if p.cb.OnAuthenticated != nil {
				p.cb.OnAuthenticated()
			}
		case HeaderPing, HeaderPong:
			_ = message // 保活；认证前不回 Pong
		case HeaderData:
			// 认证前不允许任何数据
			p.cb.OnError(ErrInvalidReceive, "received data before authentication")
			p.Disconnect()
		}
	}
}

func (p *Peer) tickIncomingAuthenticated(now uint32) {
	p.handleTimeout(now)
	p.handleDeadLink()
	p.handlePing(now)
	p.handleChoked()

	for {
		header, message, ok := p.receiveNextReliable(now)
		if !ok {
			break
		}
		switch header {
		case HeaderHello:
			// 认证后再收 Hello 非法
			p.Disconnect()
		case HeaderData:
			if len(message) > 0 {
				p.cb.OnData(message, ChannelReliable)
			} else {
				p.cb.OnError(ErrInvalidReceive, "received empty data message")
				p.Disconnect()
			}
		case HeaderPing:
			// Ping 带发送方时间戳，原样回 Pong（限频防放大）
			if len(message) == 4 && timeDiff(now, p.lastPongTime) >= PingInterval {
				ts, _ := decode32u(message, 0)
				p.sendPong(ts)
				p.lastPongTime = now
			}
		case HeaderPong:
			if len(message) == 4 {
				ts, _ := decode32u(message, 0)
				if timeDiff(now, ts) >= 0 {
					p.rttMilliseconds = now - ts
				}
			}
		}
	}
}

// TickOutgoing 驱动 KCP 发送（Flush）。
func (p *Peer) TickOutgoing(now uint32) {
	if p.state == PeerConnected || p.state == PeerAuthenticated {
		p.kcp.Update(now)
	}
}

// ---------------------------------------------------------------- 接收

// receiveNextReliable 从 KCP 取下一条可靠消息并解析消息头。
func (p *Peer) receiveNextReliable(now uint32) (byte, []byte, bool) {
	msgSize := p.kcp.PeekSize()
	if msgSize <= 0 {
		return 0, nil, false
	}

	// 超过缓冲上限视为攻击
	if msgSize > len(p.kcpMessageBuffer) {
		p.cb.OnError(ErrInvalidReceive, "possible allocation attack")
		p.Disconnect()
		return 0, nil, false
	}

	received := p.kcp.Receive(p.kcpMessageBuffer[:msgSize])
	if received < 0 {
		p.cb.OnError(ErrInvalidReceive, "kcp receive failed")
		p.Disconnect()
		return 0, nil, false
	}

	header := p.kcpMessageBuffer[0]
	if header != HeaderHello && header != HeaderPing && header != HeaderPong && header != HeaderData {
		p.cb.OnError(ErrInvalidReceive, "invalid reliable header")
		p.Disconnect()
		return 0, nil, false
	}

	// 内容不含消息头
	message := p.kcpMessageBuffer[1:received]
	p.lastReceiveTime = now
	return header, message, true
}

// ---------------------------------------------------------------- 发送

// sendReliable 组 [1B 可靠头][内容] 消息交给 KCP 可靠发送。
func (p *Peer) sendReliable(header byte, content []byte) {
	if 1+len(content) > len(p.kcpSendBuffer) {
		p.cb.OnError(ErrInvalidSend, "message larger than reliable max")
		return
	}
	p.kcpSendBuffer[0] = header
	copy(p.kcpSendBuffer[1:], content)
	if n := p.kcp.Send(p.kcpSendBuffer, 0, 1+len(content)); n < 0 {
		p.cb.OnError(ErrInvalidSend, "kcp send failed")
	}
}

// sendUnreliable 直接组完整 UDP 数据报：[channel][cookie][1B 头][内容]。
func (p *Peer) sendUnreliable(header byte, content []byte) {
	if len(content) > p.unreliableMax {
		p.cb.OnError(ErrInvalidSend, "message larger than unreliable max")
		return
	}
	p.rawSendBuffer[0] = byte(ChannelUnreliable)
	encode32u(p.rawSendBuffer, 1, p.cookie)
	p.rawSendBuffer[5] = header
	copy(p.rawSendBuffer[6:], content)
	p.cb.RawSend(p.rawSendBuffer[:6+len(content)])
}

// SendHello 发送 bare 握手（可靠空 Hello）。
func (p *Peer) SendHello() {
	p.sendReliable(HeaderHello, nil)
}

// SendData 发送应用层消息。空消息视为编程错误并断开（与 C# 一致）。
func (p *Peer) SendData(data []byte, channel Channel) {
	if len(data) == 0 {
		p.cb.OnError(ErrInvalidSend, "tried sending empty message")
		p.Disconnect()
		return
	}
	switch channel {
	case ChannelReliable:
		p.sendReliable(HeaderData, data)
	case ChannelUnreliable:
		p.sendUnreliable(HeaderUnreliableData, data)
	}
}

func (p *Peer) sendPing(now uint32) {
	var pingData [4]byte
	encode32u(pingData[:], 0, now)
	p.sendReliable(HeaderPing, pingData[:])
}

func (p *Peer) sendPong(pingTimestamp uint32) {
	var pingData [4]byte
	encode32u(pingData[:], 0, pingTimestamp)
	p.sendReliable(HeaderPong, pingData[:])
}

// Disconnect 断开连接：连发不可靠 Disconnect 后置状态并回调（幂等）。
func (p *Peer) Disconnect() {
	if p.state == PeerDisconnected {
		return
	}
	for i := 0; i < disconnectRapidFire; i++ {
		p.sendUnreliable(HeaderUnreliableDisconnect, nil)
	}
	p.state = PeerDisconnected
	if p.cb.OnDisconnected != nil {
		p.cb.OnDisconnected()
	}
}

// FlushThenDisconnect 先把已排队的可靠消息（如错误应答）刷出再断开。
// 直接 Disconnect 会让 snd_queue 中未 Flush 的消息永远无法送达——
// "应答错误后关闭连接" 的场景必须走这里。
func (p *Peer) FlushThenDisconnect(now uint32) {
	if p.state == PeerDisconnected {
		return
	}
	p.kcp.Update(now)
	p.Disconnect()
}
