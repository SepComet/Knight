package kcp2k

import (
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Server 对应 kcp2k KcpServer.cs + KcpServerConnection.cs：
// 单 UDP 套接字、按远端地址（ip:port）分 peer、单 goroutine 主循环驱动。
//
// 并发模型：独立读 goroutine 把数据报投入 channel；主循环（Run 或测试的 Step）
// 独占消费 channel 与全部 peer 状态（无锁）；其他 goroutine 经 Send/Disconnect
// 提交 ops 串行执行。回调全部在主循环 goroutine 上同步触发，
// OnData 的 message 切片指向内部复用缓冲——仅在回调期间有效，需持有请拷贝。

// ServerCallbacks 服务端回调。
type ServerCallbacks struct {
	// OnConnect 客户端完成 bare 握手（Hello 换 Hello）后触发，此后才允许 Send。
	OnConnect func(connID string)
	// OnData 应用层消息。message 仅回调期间有效。
	OnData func(connID string, message []byte, channel Channel)
	// OnDisconnect 断开（超时/死链/拥塞/对端主动断开）后触发。
	OnDisconnect func(connID string)
	// OnError 传输层错误（随后通常会触发 OnDisconnect）。
	OnError func(connID string, code ErrorCode, message string)
}

type serverConn struct {
	id     string
	remote *net.UDPAddr
	peer   *Peer
	// timeBase：连接创建时的服务端时钟。peer 内部使用连接本地毫秒
	//（等价于 C# 每 peer 一块独立 Stopwatch），否则服务端运行一段时间后
	// 新接入的连接会因 now-lastReceiveTime 超限被立即误判超时。
	timeBase uint32
}

// local 将服务端时钟换算为连接本地毫秒。
func (c *serverConn) local(now uint32) uint32 { return now - c.timeBase }

type udpPacket struct {
	data []byte
	addr *net.UDPAddr
}

// Server kcp2k 服务端。
type Server struct {
	conn     *net.UDPConn
	cfg      Config
	cb       ServerCallbacks
	conns    map[string]*serverConn
	toRemove []string
	packets  chan udpPacket
	ops      chan func(now uint32)
	start    time.Time

	stopCh    chan struct{}
	stopOnce  sync.Once
	readDone  chan struct{}
	mu        sync.Mutex
	runDone   chan struct{}
	connCount atomic.Int32
	netem     *NetEm
}

// Listen 绑定 UDP 地址并启动读 goroutine（不开始主循环；调用 Run 或手动 Step）。
func Listen(address string, cfg Config, cb ServerCallbacks) (*Server, error) {
	addr, err := net.ResolveUDPAddr("udp", address)
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, err
	}
	s := &Server{
		conn:     conn,
		cfg:      cfg,
		cb:       cb,
		conns:    make(map[string]*serverConn),
		packets:  make(chan udpPacket, 1024),
		ops:      make(chan func(now uint32), 1024),
		start:    time.Now(),
		stopCh:   make(chan struct{}),
		readDone: make(chan struct{}),
		netem:    NewNetEm(),
	}
	go func() {
		defer close(s.readDone)
		s.readLoop()
	}()
	return s, nil
}

// NetEm 返回服务端内置的运行时弱网注入器（默认全 0 透传）。
func (s *Server) NetEm() *NetEm { return s.netem }

// readLoop 阻塞读 UDP 并投递副本到 packets channel（conn 关闭后退出）。
func (s *Server) readLoop() {
	buf := make([]byte, s.cfg.Mtu)
	for {
		n, addr, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			return // 套接字已关闭
		}
		pkt := udpPacket{data: append([]byte(nil), buf[:n]...), addr: addr}
		s.netem.HandleInbound(func() {
			select {
			case s.packets <- pkt:
			case <-s.stopCh:
			default:
				log.Printf("[kcp2k] packet queue overflow, dropping from %s", addr)
			}
		})
	}
}

// LocalAddr 返回监听地址（测试绑定随机端口用）。
func (s *Server) LocalAddr() net.Addr { return s.conn.LocalAddr() }

// Run 阻塞式主循环：每步 = ops + 收包 + 全体 peer 进出各 tick 一次，节奏 = KCP interval。
func (s *Server) Run() {
	s.mu.Lock()
	select {
	case <-s.stopCh:
		s.mu.Unlock()
		return
	default:
	}
	done := make(chan struct{})
	s.runDone = done
	s.mu.Unlock()

	defer func() {
		s.conns = make(map[string]*serverConn)
		s.connCount.Store(0)
		close(done)
	}()

	tickInterval := time.Duration(s.cfg.Interval) * time.Millisecond
	if tickInterval < 10*time.Millisecond {
		tickInterval = 10 * time.Millisecond
	}
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		default:
		}
		now := s.now()
		s.runOps(now)
		s.drainPackets(now)
		s.tickAll(now)

		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
		}
	}
}

// Step 手动驱动一步（测试/诊断用）：非阻塞收包 + ops + 全体 peer tick。
func (s *Server) Step(now uint32) {
	s.runOps(now)
	s.drainPackets(now)
	s.tickAll(now)
}

// Stop 停止并关闭套接字，并等待 Run（若已启动）、读 goroutine 与弱网调度器完全退出（幂等）。
func (s *Server) Stop() {
	s.stopOnce.Do(func() {
		close(s.stopCh)
		_ = s.conn.Close()
	})
	s.Wait()
	s.netem.Close()
}

// Wait 等待 Run（若已启动）与读 goroutine 退出。
func (s *Server) Wait() {
	s.mu.Lock()
	done := s.runDone
	s.mu.Unlock()
	if done != nil {
		<-done
	}
	<-s.readDone
}

// now 单调毫秒（自 Server 创建起，uint32 约 49 天回绕，timeDiff 保证回绕安全）。
func (s *Server) now() uint32 {
	return uint32(time.Since(s.start).Milliseconds())
}

// Send 向指定连接发送应用层消息（跨 goroutine 安全，经 ops 串行化）。
// 未完成握手的连接会被静默丢弃——对端认证前收到 Data 会按协议断开。
// 服务已停止或 ops 队列满载时不阻塞调用方（避免拖垮 tick.Runner 或泄漏停机后的 goroutine）。
func (s *Server) Send(connID string, data []byte, channel Channel) {
	select {
	case <-s.stopCh:
		return
	default:
	}
	buf := append([]byte(nil), data...)
	op := func(_ uint32) {
		c, ok := s.conns[connID]
		if !ok {
			return
		}
		if c.peer.State() != PeerAuthenticated {
			log.Printf("[kcp2k] send before handshake dropped: %s", connID)
			return
		}
		c.peer.SendData(buf, channel)
	}
	select {
	case s.ops <- op:
	case <-s.stopCh:
	default:
		log.Printf("[kcp2k] ops queue overflow, dropping send to %s", connID)
	}
}

// Disconnect 断开指定连接（跨 goroutine 安全，经 ops 串行化）。
// 先刷出已排队的可靠消息再断开，保证"错误应答后关闭"场景的错误可达。
// 服务已停止或 ops 队列满载时不阻塞调用方。
func (s *Server) Disconnect(connID string) {
	select {
	case <-s.stopCh:
		return
	default:
	}
	op := func(now uint32) {
		if c, ok := s.conns[connID]; ok {
			c.peer.FlushThenDisconnect(c.local(now))
		}
	}
	select {
	case s.ops <- op:
	case <-s.stopCh:
	default:
		log.Printf("[kcp2k] ops queue overflow, dropping disconnect for %s", connID)
	}
}

// ConnCount 当前连接数（诊断用，并发安全）。
func (s *Server) ConnCount() int { return int(s.connCount.Load()) }

// drainPackets 非阻塞收取已到达的数据报并逐包处理。
func (s *Server) drainPackets(now uint32) {
	for {
		select {
		case pkt := <-s.packets:
			s.processMessage(pkt.data, pkt.addr, now)
		default:
			return
		}
	}
}

// runOps 执行其他 goroutine 提交的发送/断开操作（以当前步时钟驱动）。
func (s *Server) runOps(now uint32) {
	for {
		select {
		case op := <-s.ops:
			op(now)
		default:
			return
		}
	}
}

func (s *Server) tickAll(now uint32) {
	for _, c := range s.conns {
		c.peer.TickIncoming(c.local(now))
	}
	if len(s.toRemove) > 0 {
		for _, id := range s.toRemove {
			delete(s.conns, id)
		}
		s.toRemove = s.toRemove[:0]
		s.connCount.Store(int32(len(s.conns)))
	}
	for _, c := range s.conns {
		c.peer.TickOutgoing(c.local(now))
	}
}

// processMessage 对应 KcpServer.ProcessMessage：
// 未知地址创建临时连接，仅当首条消息是 Hello（触发 OnAuthenticated）才入表。
func (s *Server) processMessage(segment []byte, addr *net.UDPAddr, now uint32) {
	id := addr.String()
	if c, ok := s.conns[id]; ok {
		c.rawInput(segment, now)
		return
	}

	// 未知地址：创建连接但不入表。首条消息不是 Hello 的话连接直接被丢弃
	//（网络噪音 / 已断开客户端的残留重发包）。
	c := s.newServerConn(id, addr, now)
	c.rawInput(segment, now)
	c.peer.TickIncoming(c.local(now))
}

// newServerConn 对应 KcpServer.CreateConnection：生成防伪装 cookie 并装配回调。
func (s *Server) newServerConn(id string, remote *net.UDPAddr, now uint32) *serverConn {
	c := &serverConn{id: id, remote: remote, timeBase: now}
	c.peer = NewPeer(s.cfg, GenerateCookie(), PeerCallbacks{
		OnAuthenticated: func() {
			// 收到客户端 Hello：立即回 Hello（客户端由此获知 cookie），然后入表
			c.peer.SendHello()
			s.conns[c.id] = c
			s.connCount.Store(int32(len(s.conns)))
			if s.cb.OnConnect != nil {
				s.cb.OnConnect(c.id)
			}
		},
		OnData: func(message []byte, channel Channel) {
			if s.cb.OnData != nil {
				s.cb.OnData(c.id, message, channel)
			}
		},
		OnDisconnected: func() {
			s.toRemove = append(s.toRemove, c.id)
			if s.cb.OnDisconnect != nil {
				s.cb.OnDisconnect(c.id)
			}
		},
		OnError: func(code ErrorCode, message string) {
			if s.cb.OnError != nil {
				s.cb.OnError(c.id, code, message)
			}
		},
		RawSend: func(data []byte) {
			s.netem.HandleOutbound(data, func(payload []byte) {
				select {
				case <-s.stopCh:
					return
				default:
				}
				if _, err := s.conn.WriteToUDP(payload, c.remote); err != nil {
					select {
					case <-s.stopCh:
					default:
						log.Printf("[kcp2k] sendto %s failed: %v", c.id, err)
					}
				}
			})
		},
	})
	return c
}

// rawInput 对应 KcpServerConnection.RawInput：解析 channel+cookie 并分发。
// 仅认证后强制校验 cookie——客户端 Hello 重传（cookie=0）属正常现象，不校验。
func (c *serverConn) rawInput(segment []byte, now uint32) {
	if len(segment) <= 5 {
		return
	}
	channel := segment[0]
	messageCookie, _ := decode32u(segment, 1)

	if c.peer.State() == PeerAuthenticated && messageCookie != c.peer.Cookie() {
		// 可能是攻击，也可能是 Hello 重传——丢弃即可
		return
	}

	local := c.local(now)
	payload := segment[5:]
	switch Channel(channel) {
	case ChannelReliable:
		c.peer.RawInputReliable(payload)
	case ChannelUnreliable:
		c.peer.RawInputUnreliable(payload, local)
	default:
		// 非法 channel = 网络噪音，忽略
	}
}
