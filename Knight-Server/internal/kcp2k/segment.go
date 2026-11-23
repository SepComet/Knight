// Package kcp2k 是客户端 kcp2k（V1.41，vendor 于 Knight-Client/Scripts/ThirdParty/kcp2k）
// 线上协议的 Go 服务端实现。
//
// 线上格式（与 Knight-Client 完全一致，见 Docs/protocol.md §2）：
//
//	UDP 数据报 = [1B channel][4B cookie LE][载荷]
//	可靠载荷   = 标准 KCP 段流（conv 恒 0，MTU=1200-5），重组后每条消息 = [1B 可靠头][内容]
//	不可靠载荷 = [1B 不可靠头][内容]，不经 KCP
//
// 本包从 C# 源码逐函数移植：kcp.go 对应 kcp/Kcp.cs，peer.go 对应 highlevel/KcpPeer.cs，
// server.go 对应 highlevel/KcpServer.cs + KcpServerConnection.cs，client.go 对应 KcpClient.cs。
package kcp2k

// KCP 算法常量，与 kcp2k kcp/Kcp.cs 逐字一致。
const (
	RtoNdl       = 30    // no delay min rto
	RtoMin       = 100   // normal min rto
	RtoDef       = 200   // default RTO
	RtoMax       = 60000 // maximum RTO
	CmdPush      = 81    // cmd: push data
	CmdAck       = 82    // cmd: ack
	CmdWask      = 83    // cmd: window probe (ask)
	CmdWins      = 84    // cmd: window size (tell)
	AskSend      = 1     // need to send CMD_WASK
	AskTell      = 2     // need to send CMD_WINS
	WndSnd       = 32    // default send window
	WndRcv       = 128   // default receive window. must be >= max fragment size
	MtuDef       = 1200  // default MTU（kcp2k 特有，非原版 C 的 1400）
	Interval     = 100
	Overhead     = 24 // 段头 24 字节：conv(4)+cmd(1)+frg(1)+wnd(2)+ts(4)+sn(4)+una(4)+len(4)
	FrgMax       = 255
	Deadlink     = 20
	ThreshInit   = 2
	ThreshMin    = 2
	ProbeInit    = 7000   // 7 secs to probe window size
	ProbeLimit   = 120000 // up to 120 secs to probe window
	FastackLimit = 5      // max times to trigger fastack
)

// timeDiff 对应 kcp2k Utils.TimeDiff：无符号环绕差值转有符号。
func timeDiff(later, earlier uint32) int {
	return int(int32(later - earlier))
}

// segment 对应 kcp2k Segment.cs。data 即 C# MemoryStream 的内容，len(data) 即 Position。
// C# 版为降低 GC 压力做了对象池；Go 版暂直接分配，规模上来后再引入 sync.Pool。
type segment struct {
	conv     uint32
	cmd      uint32
	frg      uint32 // 分片号倒序编码：N-1..0，0 = 不分片
	wnd      uint32
	ts       uint32
	sn       uint32
	una      uint32
	resendts uint32
	rto      int
	fastack  uint32
	xmit     uint32
	data     []byte
}

// encode 对应 Segment.Encode：写 24 字节段头（全小端），返回写入字节数。
func (s *segment) encode(ptr []byte, offset int) int {
	prev := offset
	offset += encode32u(ptr, offset, s.conv)
	offset += encode8u(ptr, offset, byte(s.cmd))
	offset += encode8u(ptr, offset, byte(s.frg))
	offset += encode16u(ptr, offset, uint16(s.wnd))
	offset += encode32u(ptr, offset, s.ts)
	offset += encode32u(ptr, offset, s.sn)
	offset += encode32u(ptr, offset, s.una)
	offset += encode32u(ptr, offset, uint32(len(s.data)))
	return offset - prev
}

func encode8u(p []byte, offset int, v byte) int {
	p[offset] = v
	return 1
}

func decode8u(p []byte, offset int) (byte, int) {
	return p[offset], 1
}

func encode16u(p []byte, offset int, v uint16) int {
	p[offset] = byte(v)
	p[offset+1] = byte(v >> 8)
	return 2
}

func decode16u(p []byte, offset int) (uint16, int) {
	v := uint16(p[offset]) | uint16(p[offset+1])<<8
	return v, 2
}

func encode32u(p []byte, offset int, v uint32) int {
	p[offset] = byte(v)
	p[offset+1] = byte(v >> 8)
	p[offset+2] = byte(v >> 16)
	p[offset+3] = byte(v >> 24)
	return 4
}

func decode32u(p []byte, offset int) (uint32, int) {
	v := uint32(p[offset]) | uint32(p[offset+1])<<8 | uint32(p[offset+2])<<16 | uint32(p[offset+3])<<24
	return v, 4
}

func clampInt(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}
