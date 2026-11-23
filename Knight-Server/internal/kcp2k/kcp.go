package kcp2k

import "errors"

// Kcp 对应 kcp2k kcp/Kcp.cs（KCP 算法核心）。
// 与 skywind3000/kcp 原版 C 的差异点（均保留 kcp2k 行为）：
//   - MTU 默认 1200（非 1400）
//   - Send 分片上限用运行时 rcv_wnd 判定（修复原版 WND_RCV 常量 bug）
//   - fastlimit 限制快速重传触发次数
//   - Input 增加 offset 参数（本移植中由 peer 层直接切好切片，保留参数以对照）
//   - 不支持 ispeek 与 stream 模式
type Kcp struct {
	state int // -1 = dead link

	conv      uint32
	mtu       uint32
	mss       uint32
	sndUna    uint32
	sndNxt    uint32
	rcvNxt    uint32
	ssthresh  uint32
	rxRttval  int
	rxSrtt    int
	rxRto     int
	rxMinRto  int
	sndWnd    uint32
	rcvWnd    uint32
	rmtWnd    uint32
	cwnd      uint32
	probe     uint32
	interval  uint32
	tsFlush   uint32
	xmit      uint32
	nodelay   uint32
	updated   bool
	tsProbe   uint32
	probeWait uint32
	deadLink  uint32
	incr      uint32
	current   uint32

	fastresend int
	fastlimit  int
	nocwnd     bool

	sndQueue []*segment
	rcvQueue []*segment
	sndBuf   []*segment
	rcvBuf   []*segment
	acklist  []ackItem

	buffer []byte

	output func(buffer []byte, size int)
}

type ackItem struct {
	serialNumber uint32
	timestamp    uint32
}

// NewKcp 对应 ikcp_create：conv 双端必须一致；output 为底层 UDP 发送回调。
func NewKcp(conv uint32, output func(buffer []byte, size int)) *Kcp {
	k := &Kcp{
		conv:      conv,
		output:    output,
		sndWnd:    WndSnd,
		rcvWnd:    WndRcv,
		rmtWnd:    WndRcv,
		mtu:       MtuDef,
		rxRto:     RtoDef,
		rxMinRto:  RtoMin,
		interval:  Interval,
		tsFlush:   Interval,
		ssthresh:  ThreshInit,
		fastlimit: FastackLimit,
		deadLink:  Deadlink,
	}
	k.mss = k.mtu - Overhead
	k.buffer = make([]byte, (k.mtu+Overhead)*3)
	return k
}

// WaitSnd 计算待发送数量（对应 WaitSnd 属性）。
func (k *Kcp) WaitSnd() int {
	return len(k.sndBuf) + len(k.sndQueue)
}

// QueueTotal 返回全部内部队列总长（peer 层拥塞断开判定用）。
func (k *Kcp) QueueTotal() int {
	return len(k.sndQueue) + len(k.rcvQueue) + len(k.sndBuf) + len(k.rcvBuf)
}

// ClearSendQueue 清空发送队列（拥塞断开前调用，对应 kcp.snd_queue.Clear()）。
func (k *Kcp) ClearSendQueue() {
	k.sndQueue = k.sndQueue[:0]
}

// wndUnused 对应 ikcp_wnd_unused。
func (k *Kcp) wndUnused() uint32 {
	if uint32(len(k.rcvQueue)) < k.rcvWnd {
		return k.rcvWnd - uint32(len(k.rcvQueue))
	}
	return 0
}

// Receive 对应 ikcp_recv：从状态机取下一条完整消息（分片自动合并）。
// 返回读取字节数；-1 无消息；-2 消息不完整；-3 缓冲区太小。
func (k *Kcp) Receive(buffer []byte) int {
	if len(k.rcvQueue) == 0 {
		return -1
	}

	peeksize := k.PeekSize()
	if peeksize < 0 {
		return -2
	}
	if peeksize > len(buffer) {
		return -3
	}

	recover := uint32(len(k.rcvQueue)) >= k.rcvWnd

	// 合并分片
	offset := 0
	read := 0
	for len(k.rcvQueue) > 0 {
		seg := k.rcvQueue[0]
		k.rcvQueue = k.rcvQueue[1:]

		copy(buffer[offset:], seg.data)
		offset += len(seg.data)
		read += len(seg.data)
		fragment := seg.frg

		if fragment == 0 {
			break
		}
	}

	// rcv_buf -> rcv_queue 搬移连续就绪段
	removed := 0
	for _, seg := range k.rcvBuf {
		if seg.sn == k.rcvNxt && uint32(len(k.rcvQueue)) < k.rcvWnd {
			removed++
			k.rcvQueue = append(k.rcvQueue, seg)
			k.rcvNxt++
		} else {
			break
		}
	}
	k.rcvBuf = k.rcvBuf[removed:]

	// 快速恢复：窗口从满变不满时告知对端
	if uint32(len(k.rcvQueue)) < k.rcvWnd && recover {
		k.probe |= AskTell
	}

	return read
}

// PeekSize 对应 ikcp_peeksize：下一条消息的总大小；-1 表示无消息或分片未齐。
func (k *Kcp) PeekSize() int {
	if len(k.rcvQueue) == 0 {
		return -1
	}
	seq := k.rcvQueue[0]
	if seq.frg == 0 {
		return len(seq.data)
	}
	// frg 倒序编码：队首的 frg 值即总分片数
	if uint32(len(k.rcvQueue)) < seq.frg+1 {
		return -1
	}
	length := 0
	for _, seg := range k.rcvQueue {
		length += len(seg.data)
		if seg.frg == 0 {
			break
		}
	}
	return length
}

// Send 对应 ikcp_send：按 mss 分片入 snd_queue。
// 返回 0 成功；-1 size<0；-2 分片数 >= rcv_wnd。
func (k *Kcp) Send(buffer []byte, offset, size int) int {
	if size < 0 {
		return -1
	}
	len_ := size

	var count int
	if len_ <= int(k.mss) {
		count = 1
	} else {
		count = (len_ + int(k.mss) - 1) / int(k.mss)
	}

	// frg 只有 1 字节，最多 255 分片
	if count > FrgMax {
		panic("kcp2k: send requires more than 255 fragments")
	}
	if uint32(count) >= k.rcvWnd {
		return -2
	}
	if count == 0 {
		count = 1
	}

	for i := 0; i < count; i++ {
		sz := len_
		if sz > int(k.mss) {
			sz = int(k.mss)
		}
		seg := &segment{}
		if len_ > 0 {
			seg.data = append(seg.data, buffer[offset:offset+sz]...)
		}
		seg.frg = uint32(count - i - 1)
		k.sndQueue = append(k.sndQueue, seg)
		offset += sz
		len_ -= sz
	}

	return 0
}

// updateAck 对应 ikcp_update_ack（RFC6298 RTO 计算）。
func (k *Kcp) updateAck(rtt int) {
	if k.rxSrtt == 0 {
		k.rxSrtt = rtt
		k.rxRttval = rtt / 2
	} else {
		delta := rtt - k.rxSrtt
		if delta < 0 {
			delta = -delta
		}
		k.rxRttval = (3*k.rxRttval + delta) / 4
		k.rxSrtt = (7*k.rxSrtt + rtt) / 8
		if k.rxSrtt < 1 {
			k.rxSrtt = 1
		}
	}
	rto := k.rxSrtt + maxInt(int(k.interval), 4*k.rxRttval)
	k.rxRto = clampInt(rto, k.rxMinRto, RtoMax)
}

// shrinkBuf 对应 ikcp_shrink_buf。
func (k *Kcp) shrinkBuf() {
	if len(k.sndBuf) > 0 {
		k.sndUna = k.sndBuf[0].sn
	} else {
		k.sndUna = k.sndNxt
	}
}

// parseAck 对应 ikcp_parse_ack：从发送缓冲移除已确认段。
func (k *Kcp) parseAck(sn uint32) {
	if timeDiff(sn, k.sndUna) < 0 || timeDiff(sn, k.sndNxt) >= 0 {
		return
	}
	for i := 0; i < len(k.sndBuf); i++ {
		seg := k.sndBuf[i]
		if sn == seg.sn {
			k.sndBuf = append(k.sndBuf[:i], k.sndBuf[i+1:]...)
			break
		}
		if timeDiff(sn, seg.sn) < 0 {
			break
		}
	}
}

// parseUna 对应 ikcp_parse_una：移除所有 sn < una 的已发段。
func (k *Kcp) parseUna(una uint32) {
	removed := 0
	for _, seg := range k.sndBuf {
		if seg.sn < una {
			removed++
		} else {
			break
		}
	}
	k.sndBuf = k.sndBuf[removed:]
}

// parseFastack 对应 ikcp_parse_fastack（非 FASTACK_CONSERVE 分支）。
func (k *Kcp) parseFastack(sn uint32, ts uint32) {
	if sn < k.sndUna {
		return
	}
	if sn >= k.sndNxt {
		return
	}
	for _, seg := range k.sndBuf {
		if sn < seg.sn {
			break
		} else if sn != seg.sn {
			seg.fastack++
		}
	}
}

// parseData 对应 ikcp_parse_data。
func (k *Kcp) parseData(newseg *segment) {
	sn := newseg.sn
	if timeDiff(sn, k.rcvNxt+k.rcvWnd) >= 0 || timeDiff(sn, k.rcvNxt) < 0 {
		return
	}
	k.insertSegmentInReceiveBuffer(newseg)
	k.moveReceiveBufferReadySegmentsToQueue()
}

// insertSegmentInReceiveBuffer 按 sn 有序插入 rcv_buf（倒序遍历），重复 sn 丢弃。
func (k *Kcp) insertSegmentInReceiveBuffer(newseg *segment) {
	repeat := false
	i := len(k.rcvBuf) - 1
	for ; i >= 0; i-- {
		seg := k.rcvBuf[i]
		if seg.sn == newseg.sn {
			repeat = true
			break
		}
		if timeDiff(newseg.sn, seg.sn) > 0 {
			break
		}
	}
	if !repeat {
		// 插入到 i+1 位置
		k.rcvBuf = append(k.rcvBuf, nil)
		copy(k.rcvBuf[i+2:], k.rcvBuf[i+1:])
		k.rcvBuf[i+1] = newseg
	}
}

// moveReceiveBufferReadySegmentsToQueue 将 rcv_buf 头部连续就绪段搬入 rcv_queue。
func (k *Kcp) moveReceiveBufferReadySegmentsToQueue() {
	removed := 0
	for _, seg := range k.rcvBuf {
		if seg.sn == k.rcvNxt && uint32(len(k.rcvQueue)) < k.rcvWnd {
			removed++
			k.rcvQueue = append(k.rcvQueue, seg)
			k.rcvNxt++
		} else {
			break
		}
	}
	k.rcvBuf = k.rcvBuf[removed:]
}

// Input 对应 ikcp_input：底层收包入口（一个 UDP 包可含多个 KCP 段）。
// 返回 0 成功；-1 conv 不匹配/数据太小；-2 长度非法；-3 非法 cmd。
func (k *Kcp) Input(data []byte, offset, size int) int {
	prevUna := k.sndUna
	maxack := uint32(0)
	latestTs := uint32(0)
	flag := 0

	if data == nil || size < Overhead {
		return -1
	}

	for {
		if size < Overhead {
			break
		}

		var conv uint32
		var cmd, frg byte
		var wnd uint16
		var ts, sn, una, length uint32
		var n int

		// C# 原文为 offset += Decode32U(...)：decode 返回消耗字节数，必须累加
		conv, n = decode32u(data, offset)
		offset += n
		if conv != k.conv {
			return -1
		}
		cmd, n = decode8u(data, offset)
		offset += n
		frg, n = decode8u(data, offset)
		offset += n
		wnd, n = decode16u(data, offset)
		offset += n
		ts, n = decode32u(data, offset)
		offset += n
		sn, n = decode32u(data, offset)
		offset += n
		una, n = decode32u(data, offset)
		offset += n
		length, n = decode32u(data, offset)
		offset += n

		size -= Overhead

		// 原版将 uint len 强转 int 判负（len 高位为 1 时）
		if int64(length) > int64(size) || length > 0x7fffffff {
			return -2
		}

		if cmd != CmdPush && cmd != CmdAck && cmd != CmdWask && cmd != CmdWins {
			return -3
		}

		k.rmtWnd = uint32(wnd)
		k.parseUna(una)
		k.shrinkBuf()

		switch cmd {
		case CmdAck:
			if timeDiff(k.current, ts) >= 0 {
				k.updateAck(timeDiff(k.current, ts))
			}
			k.parseAck(sn)
			k.shrinkBuf()
			if flag == 0 {
				flag = 1
				maxack = sn
				latestTs = ts
			} else if timeDiff(sn, maxack) > 0 {
				maxack = sn
				latestTs = ts
			}
		case CmdPush:
			if timeDiff(sn, k.rcvNxt+k.rcvWnd) < 0 {
				k.acklist = append(k.acklist, ackItem{serialNumber: sn, timestamp: ts})
				if timeDiff(sn, k.rcvNxt) >= 0 {
					seg := &segment{
						conv: conv,
						cmd:  uint32(cmd),
						frg:  uint32(frg),
						wnd:  uint32(wnd),
						ts:   ts,
						sn:   sn,
						una:  una,
					}
					if length > 0 {
						seg.data = append(seg.data, data[offset:offset+int(length)]...)
					}
					k.parseData(seg)
				}
			}
		case CmdWask:
			k.probe |= AskTell
		case CmdWins:
			// do nothing
		}

		offset += int(length)
		size -= int(length)
	}

	if flag != 0 {
		k.parseFastack(maxack, latestTs)
	}

	// 有新确认时更新拥塞窗口
	if timeDiff(k.sndUna, prevUna) > 0 {
		if k.cwnd < k.rmtWnd {
			if k.cwnd < k.ssthresh {
				k.cwnd++
				k.incr += k.mss
			} else {
				if k.incr < k.mss {
					k.incr = k.mss
				}
				effMss := k.mss
				if effMss == 0 {
					effMss = 1
				}
				k.incr += (effMss*effMss)/k.incr + effMss/16
				if (k.cwnd+1)*k.mss <= k.incr {
					k.cwnd = (k.incr + effMss - 1) / effMss
				}
			}
			if k.cwnd > k.rmtWnd {
				k.cwnd = k.rmtWnd
				k.incr = k.rmtWnd * k.mss
			}
		}
	}

	return 0
}

// makeSpace / flushBuffer 对应 Flush 辅助函数。
func (k *Kcp) makeSpace(size *int, space int) {
	if *size+space > int(k.mtu) {
		k.output(k.buffer, *size)
		*size = 0
	}
}

func (k *Kcp) flushBuffer(size int) {
	if size > 0 {
		k.output(k.buffer, size)
	}
}

// Flush 对应 ikcp_flush：刷 ACK、窗口探测、snd_queue 搬移、发送/重传判定。
func (k *Kcp) Flush() {
	size := 0
	lost := false

	// 必须先调 Update
	if !k.updated {
		return
	}

	seg := &segment{
		conv: k.conv,
		cmd:  CmdAck,
		wnd:  k.wndUnused(),
		una:  k.rcvNxt,
	}

	// 刷 ACK
	for _, ack := range k.acklist {
		k.makeSpace(&size, Overhead)
		seg.sn = ack.serialNumber
		seg.ts = ack.timestamp
		size += seg.encode(k.buffer, size)
	}
	k.acklist = k.acklist[:0]

	// 窗口探测（对端窗口为 0 时）
	if k.rmtWnd == 0 {
		if k.probeWait == 0 {
			k.probeWait = ProbeInit
			k.tsProbe = k.current + k.probeWait
		} else {
			if timeDiff(k.current, k.tsProbe) >= 0 {
				if k.probeWait < ProbeInit {
					k.probeWait = ProbeInit
				}
				k.probeWait += k.probeWait / 2
				if k.probeWait > ProbeLimit {
					k.probeWait = ProbeLimit
				}
				k.tsProbe = k.current + k.probeWait
				k.probe |= AskSend
			}
		}
	} else {
		k.tsProbe = 0
		k.probeWait = 0
	}

	if k.probe&AskSend != 0 {
		seg.cmd = CmdWask
		k.makeSpace(&size, Overhead)
		size += seg.encode(k.buffer, size)
	}
	if k.probe&AskTell != 0 {
		seg.cmd = CmdWins
		k.makeSpace(&size, Overhead)
		size += seg.encode(k.buffer, size)
	}
	k.probe = 0

	// 有效发送窗口
	cwnd := minUint(k.sndWnd, k.rmtWnd)
	if !k.nocwnd {
		cwnd = minUint(k.cwnd, cwnd)
	}

	// snd_queue -> snd_buf（滑动窗口空位内）
	for timeDiff(k.sndNxt, k.sndUna+cwnd) < 0 {
		if len(k.sndQueue) == 0 {
			break
		}
		newseg := k.sndQueue[0]
		k.sndQueue = k.sndQueue[1:]

		newseg.conv = k.conv
		newseg.cmd = CmdPush
		newseg.wnd = seg.wnd
		newseg.ts = k.current
		newseg.sn = k.sndNxt
		k.sndNxt++
		newseg.una = k.rcvNxt
		newseg.resendts = k.current
		newseg.rto = k.rxRto
		newseg.fastack = 0
		newseg.xmit = 0
		k.sndBuf = append(k.sndBuf, newseg)
	}

	resent := uint32(0xffffffff)
	if k.fastresend > 0 {
		resent = uint32(k.fastresend)
	}
	rtomin := uint32(0)
	if k.nodelay == 0 {
		rtomin = uint32(k.rxRto) >> 3
	}

	// 遍历发送缓冲判断 needsend
	change := 0
	for _, s := range k.sndBuf {
		needsend := false

		if s.xmit == 0 {
			// 首传
			needsend = true
			s.xmit++
			s.rto = k.rxRto
			s.resendts = k.current + uint32(s.rto) + rtomin
		} else if timeDiff(k.current, s.resendts) >= 0 {
			// RTO 超时重传
			needsend = true
			s.xmit++
			k.xmit++
			if k.nodelay == 0 {
				s.rto += maxInt(int(s.rto), k.rxRto)
			} else {
				step := int(s.rto)
				if k.nodelay >= 2 {
					step = k.rxRto
				}
				s.rto += step / 2
			}
			s.resendts = k.current + uint32(s.rto)
			lost = true
		} else if s.fastack >= resent {
			// 快速重传（受 fastlimit 限制）
			if s.xmit <= uint32(k.fastlimit) || k.fastlimit <= 0 {
				needsend = true
				s.xmit++
				s.fastack = 0
				s.resendts = k.current + uint32(s.rto)
				change++
			}
		}

		if needsend {
			s.ts = k.current
			s.wnd = seg.wnd
			s.una = k.rcvNxt

			need := Overhead + len(s.data)
			k.makeSpace(&size, need)

			size += s.encode(k.buffer, size)
			if len(s.data) > 0 {
				copy(k.buffer[size:], s.data)
				size += len(s.data)
			}

			// dead_link：重传达上限仍无 ACK
			if s.xmit >= k.deadLink {
				k.state = -1
			}
		}
	}

	k.flushBuffer(size)

	// ssthresh 更新
	if change > 0 {
		inflight := k.sndNxt - k.sndUna
		k.ssthresh = inflight / 2
		if k.ssthresh < ThreshMin {
			k.ssthresh = ThreshMin
		}
		k.cwnd = k.ssthresh + resent
		k.incr = k.cwnd * k.mss
	}
	if lost {
		k.ssthresh = cwnd / 2
		if k.ssthresh < ThreshMin {
			k.ssthresh = ThreshMin
		}
		k.cwnd = 1
		k.incr = k.mss
	}
	if k.cwnd < 1 {
		k.cwnd = 1
		k.incr = k.mss
	}
}

// Update 对应 ikcp_update：注入当前毫秒时间并按 interval 驱动 Flush。
func (k *Kcp) Update(currentTimeMilliSeconds uint32) {
	k.current = currentTimeMilliSeconds

	if !k.updated {
		k.updated = true
		k.tsFlush = k.current
	}

	slap := timeDiff(k.current, k.tsFlush)

	// 10s 硬上限：无论间隔强制 flush
	if slap >= 10000 || slap < -10000 {
		k.tsFlush = k.current
		slap = 0
	}

	if slap >= 0 {
		k.tsFlush += k.interval
		if k.current >= k.tsFlush {
			k.tsFlush = k.current + k.interval
		}
		k.Flush()
	}
}

// Check 对应 ikcp_check：返回下次应调 Update 的时间（毫秒）。
func (k *Kcp) Check(current uint32) uint32 {
	tsFlush := k.tsFlush
	tmPacket := 0x7fffffff

	if !k.updated {
		return current
	}
	if timeDiff(current, tsFlush) >= 10000 || timeDiff(current, tsFlush) < -10000 {
		tsFlush = current
	}
	if timeDiff(current, tsFlush) >= 0 {
		return current
	}
	tmFlush := timeDiff(tsFlush, current)

	for _, seg := range k.sndBuf {
		diff := timeDiff(seg.resendts, current)
		if diff <= 0 {
			return current
		}
		if diff < tmPacket {
			tmPacket = diff
		}
	}

	minimal := tmPacket
	if tmFlush < minimal {
		minimal = tmFlush
	}
	if uint32(minimal) >= k.interval {
		minimal = int(k.interval)
	}

	return current + uint32(minimal)
}

// SetMtu 对应 ikcp_setmtu。
func (k *Kcp) SetMtu(mtu uint32) error {
	if mtu < 50 || mtu < Overhead {
		return errors.New("kcp2k: MTU must be higher than 50 and higher than OVERHEAD")
	}
	k.buffer = make([]byte, (mtu+Overhead)*3)
	k.mtu = mtu
	k.mss = mtu - Overhead
	return nil
}

// SetInterval 对应 ikcp_interval，钳位 [10,5000]。
func (k *Kcp) SetInterval(interval uint32) {
	if interval > 5000 {
		interval = 5000
	} else if interval < 10 {
		interval = 10
	}
	k.interval = interval
}

// SetNoDelay 对应 ikcp_nodelay。nodelay≠0 时 rxMinRto=RTO_NDL；
// resend≥0 设置 fastresend；nocwnd=true 关闭拥塞控制。
func (k *Kcp) SetNoDelay(nodelay uint32, interval uint32, resend int, nocwnd bool) {
	k.nodelay = nodelay
	if nodelay != 0 {
		k.rxMinRto = RtoNdl
	} else {
		k.rxMinRto = RtoMin
	}
	if interval >= 0 {
		if interval > 5000 {
			interval = 5000
		} else if interval < 10 {
			interval = 10
		}
		k.interval = interval
	}
	if resend >= 0 {
		k.fastresend = resend
	}
	k.nocwnd = nocwnd
}

// SetWindowSize 对应 ikcp_wndsize；接收窗口强制下限 WndRcv（kcp2k 行为）。
func (k *Kcp) SetWindowSize(sendWindow, receiveWindow uint32) {
	if sendWindow > 0 {
		k.sndWnd = sendWindow
	}
	if receiveWindow > 0 {
		if receiveWindow > WndRcv {
			k.rcvWnd = receiveWindow
		} else {
			k.rcvWnd = WndRcv
		}
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minUint(a, b uint32) uint32 {
	if a < b {
		return a
	}
	return b
}
