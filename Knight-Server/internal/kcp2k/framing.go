package kcp2k

import "crypto/rand"

// Config 对应 kcp2k KcpConfig（仅保留 KCP 链路相关字段；套接字缓冲由 Go 运行时管理）。
type Config struct {
	Mtu               int    // 默认 MtuDef(1200)
	NoDelay           bool   // nodelay=1
	Interval          uint32 // 默认 10
	FastResend        int    // 默认 0；游戏协议要求 2
	CongestionWindow  bool   // 默认 false（即 nc=1 关闭拥塞控制）
	SendWindowSize    uint32 // 默认 WndSnd(32)
	ReceiveWindowSize uint32 // 默认 WndRcv(128)
	Timeout           int    // 无入包判定断线的毫秒数，默认 10000
	MaxRetransmits    uint32 // dead_link，默认 Deadlink(20)
}

// DefaultConfig 返回 kcp2k 默认配置。
func DefaultConfig() Config {
	return Config{
		Mtu:               MtuDef,
		NoDelay:           true,
		Interval:          10,
		FastResend:        0,
		CongestionWindow:  false,
		SendWindowSize:    WndSnd,
		ReceiveWindowSize: WndRcv,
		Timeout:           10000,
		MaxRetransmits:    Deadlink,
	}
}

// Channel 对应 kcp2k KcpChannel 枚举：线上第 1 字节。
type Channel byte

const (
	// 注意：0x00 不响应，用于滤除网络噪音。
	ChannelReliable   Channel = 1
	ChannelUnreliable Channel = 2
)

// 可靠消息头（KCP 重组后消息的第 1 字节），对应 KcpHeaderReliable。
const (
	HeaderHello byte = 1 // 空载荷；bare 握手
	HeaderPing  byte = 2 // 载荷 = 4B 发送方时间戳
	HeaderData  byte = 3 // 载荷 = 应用层消息（Envelope 字节）
	HeaderPong  byte = 4 // '4' 而非 '3' 为历史兼容；载荷 = 4B 回显时间戳
)

// 不可靠消息头（对应 KcpHeaderUnreliable），直接放在 UDP 载荷中、不经 KCP。
const (
	HeaderUnreliableData       byte = 4
	HeaderUnreliableDisconnect byte = 5
)

// 封帧元数据大小：channel(1) + cookie(4)。
const (
	ChannelHeaderSize      = 1
	CookieHeaderSize       = 4
	MetadataSizeReliable   = ChannelHeaderSize + CookieHeaderSize // 5
	MetadataSizeUnreliable = ChannelHeaderSize + CookieHeaderSize // 5
)

// ReliableMaxMessageSize 可靠通道单条消息上限（kcp 分片重组，受 frg 255 与接收窗口限制）。
func ReliableMaxMessageSize(mtu int, rcvWnd uint32) int {
	wnd := rcvWnd
	if wnd > FrgMax {
		wnd = FrgMax
	}
	return (mtu-Overhead-MetadataSizeReliable)*(int(wnd)-1) - 1
}

// UnreliableMaxMessageSize 不可靠通道单条消息上限（单数据报直发）。
func UnreliableMaxMessageSize(mtu int) int {
	return mtu - MetadataSizeUnreliable - 1
}

// GenerateCookie 对应 kcp2k Common.GenerateCookie：crypto 级随机 uint32（防 UDP 伪装）。
func GenerateCookie() uint32 {
	var buf [4]byte
	// crypto/rand 失败说明系统熵源异常，直接 panic 是合理选择
	if _, err := rand.Read(buf[:]); err != nil {
		panic("kcp2k: crypto/rand failed: " + err.Error())
	}
	return uint32(buf[0]) | uint32(buf[1])<<8 | uint32(buf[2])<<16 | uint32(buf[3])<<24
}
