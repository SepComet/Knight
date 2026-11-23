package kcp2k

import (
	"bytes"
	"testing"
)

// kcpPipe 通过内存直连的一对 KCP 实例（output 互相 Input），用于算法级测试。
type kcpPipe struct {
	a, b *Kcp

	aOut [][]byte
	bOut [][]byte
}

func newKcpPair() *kcpPipe {
	p := &kcpPipe{}
	p.a = NewKcp(0, func(buf []byte, size int) {
		p.aOut = append(p.aOut, append([]byte(nil), buf[:size]...))
	})
	p.b = NewKcp(0, func(buf []byte, size int) {
		p.bOut = append(p.bOut, append([]byte(nil), buf[:size]...))
	})
	// 与 peer 层一致的 turbo 配置（游戏协议参数）：nodelay=1, interval=10,
	// resend=2, nc=1。不开 nocwnd 时 cwnd 从 0 起步需等 ACK 爬升，链路无法自举。
	p.a.SetNoDelay(1, 10, 2, true)
	p.b.SetNoDelay(1, 10, 2, true)
	return p
}

// deliver 把双方积攒的数据报交给对端（模拟链路）。
// dropA/dropB 为 true 时丢弃对应方向的数据报（模拟丢包）。
func (p *kcpPipe) deliver() {
	for _, d := range p.aOut {
		p.b.Input(d, 0, len(d))
	}
	p.aOut = nil
	for _, d := range p.bOut {
		p.a.Input(d, 0, len(d))
	}
	p.bOut = nil
}

func TestKcpRoundTrip(t *testing.T) {
	mss := int(MtuDef - Overhead - MetadataSizeReliable - 5) // 与 peer 层一致的 mss=1171
	sizes := []int{1, 100, mss - 1, mss, mss + 1, mss*3 + 7, 100_000}

	for _, size := range sizes {
		p := newKcpPair()
		msg := make([]byte, size)
		for i := range msg {
			msg[i] = byte(i % 251)
		}

		if n := p.a.Send(msg, 0, len(msg)); n != 0 {
			t.Fatalf("size %d: send = %d", size, n)
		}

		// 大消息分片数超过发送窗口（32），需多轮 flush+ACK 滑动窗口送完
		now := uint32(0)
		for i := 0; i < 300; i++ {
			now += 10
			p.a.Update(now)
			p.deliver()
			p.b.Update(now)
			p.deliver()
			if p.b.PeekSize() == size {
				break
			}
		}

		buf := make([]byte, size+16)
		n := p.b.Receive(buf)
		if n != size {
			t.Fatalf("size %d: receive = %d, want %d", size, n, size)
		}
		if !bytes.Equal(buf[:size], msg) {
			t.Fatalf("size %d: payload mismatch", size)
		}
	}
}

func TestKcpOutOfOrderFragments(t *testing.T) {
	p := newKcpPair()
	msg := make([]byte, 5000) // 5 分片
	for i := range msg {
		msg[i] = byte(i % 253)
	}
	if n := p.a.Send(msg, 0, len(msg)); n != 0 {
		t.Fatalf("send = %d", n)
	}

	// flush 后把分片倒序喂给 b
	p.a.Update(0)
	frags := p.aOut
	p.aOut = nil
	for i := len(frags) - 1; i >= 0; i-- {
		if n := p.b.Input(frags[i], 0, len(frags[i])); n != 0 {
			t.Fatalf("input = %d", n)
		}
	}

	p.b.Update(0)
	buf := make([]byte, len(msg)+16)
	n := p.b.Receive(buf)
	if n != len(msg) {
		t.Fatalf("receive = %d, want %d", n, len(msg))
	}
	if !bytes.Equal(buf[:len(msg)], msg) {
		t.Fatal("payload mismatch after out-of-order delivery")
	}
}

func TestKcpConvMismatch(t *testing.T) {
	p := newKcpPair()
	other := NewKcp(12345, func([]byte, int) {})

	msg := []byte("hello")
	if n := p.a.Send(msg, 0, len(msg)); n != 0 {
		t.Fatalf("send = %d", n)
	}
	p.a.Update(0)

	// conv=0 的数据报喂给期望 conv=12345 的实例 → -1
	for _, d := range p.aOut {
		if n := other.Input(d, 0, len(d)); n != -1 {
			t.Fatalf("input with conv mismatch = %d, want -1", n)
		}
	}
}

func TestKcpSendFragmentLimit(t *testing.T) {
	p := newKcpPair()
	// 分片数 >= rcv_wnd(128) → -2
	big := make([]byte, int(p.a.mss)*128)
	if n := p.a.Send(big, 0, len(big)); n != -2 {
		t.Fatalf("send 128 fragments = %d, want -2", n)
	}
}

func TestKcpRetransmit(t *testing.T) {
	// 正向丢包，验证 RTO 重传后消息最终送达
	p := newKcpPair()
	msg := []byte("retransmit me")

	drop := true
	p.a.output = func(buf []byte, size int) {
		if drop {
			return // 模拟丢包
		}
		p.b.Input(buf[:size], 0, size)
	}

	if n := p.a.Send(msg, 0, len(msg)); n != 0 {
		t.Fatalf("send = %d", n)
	}
	p.a.Update(0) // 首传（被丢弃）

	now := uint32(0)
	delivered := false
	for i := 0; i < 60 && !delivered; i++ {
		now += 50
		p.a.Update(now) // RTO 到期触发重传
		if i == 5 {
			drop = false // 恢复链路
		}
		p.b.Update(now)
		buf := make([]byte, 64)
		if n := p.b.Receive(buf); n > 0 {
			if !bytes.Equal(buf[:n], msg) {
				t.Fatal("payload mismatch after retransmit")
			}
			delivered = true
		}
	}
	if !delivered {
		t.Fatal("message never delivered via retransmission")
	}
}

func TestReliableMaxMessageSizeMath(t *testing.T) {
	// (1200-24-5)*(128-1)-1 = 1171*127-1
	if got := ReliableMaxMessageSize(1200, 128); got != 148716 {
		t.Fatalf("reliableMax = %d, want 148716", got)
	}
	if got := UnreliableMaxMessageSize(1200); got != 1194 {
		t.Fatalf("unreliableMax = %d, want 1194", got)
	}
}

func TestGenerateCookie(t *testing.T) {
	seen := map[uint32]bool{}
	for i := 0; i < 100; i++ {
		c := GenerateCookie()
		if seen[c] {
			t.Fatal("cookie collision in 100 samples")
		}
		seen[c] = true
	}
}
