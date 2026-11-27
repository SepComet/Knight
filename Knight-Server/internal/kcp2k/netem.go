package kcp2k

import (
	"container/heap"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"
)

// NetEmDirection 控制弱网注入方向。
type NetEmDirection string

const (
	NetEmDirectionBoth     NetEmDirection = "both"
	NetEmDirectionOutbound NetEmDirection = "outbound"
	NetEmDirectionInbound  NetEmDirection = "inbound"
)

// 弱网参数安全边界（防止误配置导致服务假死或内存暴涨）。
const (
	NetEmMaxDelayMs          = 2000
	NetEmMaxJitterMs         = 1000
	NetEmMaxLossPercent      = 50.0
	defaultNetEmMaxQueue     = 4096
	netEmOverflowLogInterval = 5 * time.Second
)

// ErrNetEmClosed 在 NetEm 已关闭后调用 SetConfig 时返回。
var ErrNetEmClosed = errors.New("netem is closed")

// NetEmConfig 运行时弱网模拟配置。全部为 0 表示关闭（零开销透传）。
type NetEmConfig struct {
	DelayMs     int            `json:"delay_ms"`
	JitterMs    int            `json:"jitter_ms"`
	LossPercent float64        `json:"loss_percent"`
	Direction   NetEmDirection `json:"direction"`
}

// Normalized 返回规范化后的配置（空方向补全为 both）。
func (c NetEmConfig) Normalized() NetEmConfig {
	if c.Direction == "" {
		c.Direction = NetEmDirectionBoth
	}
	return c
}

// Enabled 判断是否启用了任何弱网注入项。
func (c NetEmConfig) Enabled() bool {
	return c.DelayMs > 0 || c.JitterMs > 0 || c.LossPercent > 0
}

// Validate 严格校验弱网参数范围。
func (c NetEmConfig) Validate() error {
	if c.DelayMs < 0 || c.DelayMs > NetEmMaxDelayMs {
		return fmt.Errorf("delay_ms must be in [0, %d]", NetEmMaxDelayMs)
	}
	if c.JitterMs < 0 || c.JitterMs > NetEmMaxJitterMs {
		return fmt.Errorf("jitter_ms must be in [0, %d]", NetEmMaxJitterMs)
	}
	if math.IsNaN(c.LossPercent) || math.IsInf(c.LossPercent, 0) || c.LossPercent < 0 || c.LossPercent > NetEmMaxLossPercent {
		return fmt.Errorf("loss_percent must be in [0, %.0f]", NetEmMaxLossPercent)
	}
	switch c.Direction {
	case "", NetEmDirectionBoth, NetEmDirectionOutbound, NetEmDirectionInbound:
		return nil
	default:
		return errors.New("direction must be one of: both, outbound, inbound")
	}
}

func (d NetEmDirection) matches(target NetEmDirection) bool {
	return d == NetEmDirectionBoth || d == target
}

// NetEmStats 弱网注入统计（用于监控与自动化测试断言）。
type NetEmStats struct {
	TotalPackets           uint64 `json:"total_packets"`
	DroppedPackets         uint64 `json:"dropped_packets"`
	OverflowDroppedPackets uint64 `json:"overflow_dropped_packets"`
	DelayedPackets         uint64 `json:"delayed_packets"`
	InboundPackets         uint64 `json:"inbound_packets"`
	OutboundPackets        uint64 `json:"outbound_packets"`
	QueuedPackets          int    `json:"queued_packets"`
}

// NetEmSnapshot 当前弱网配置与运行统计快照。
type NetEmSnapshot struct {
	Enabled     bool           `json:"enabled"`
	DelayMs     int            `json:"delay_ms"`
	JitterMs    int            `json:"jitter_ms"`
	LossPercent float64        `json:"loss_percent"`
	Direction   NetEmDirection `json:"direction"`
	Stats       NetEmStats     `json:"stats"`
}

// NetEm 线程安全的运行时可控弱网注入器。
//
// 性能与资源保证：
//   - 全 0 配置时，快速路径仅一次原子读（无锁、无拷贝、无 goroutine、无 timer）；
//   - 仅在首次产生延迟包时懒启动单个最小堆调度 goroutine；
//   - Close() 幂等清空全部排队延迟包并等待调度 goroutine 退出，杜绝停机泄漏与 panic。
type NetEm struct {
	enabled atomic.Bool
	closed  atomic.Bool

	totalPackets           atomic.Uint64
	droppedPackets         atomic.Uint64
	overflowDroppedPackets atomic.Uint64
	delayedPackets         atomic.Uint64
	inboundPackets         atomic.Uint64
	outboundPackets        atomic.Uint64
	queuedPackets          atomic.Int64

	mu              sync.Mutex
	cfg             NetEmConfig
	rng             *rand.Rand
	queue           delayedHeap
	nextSeq         uint64
	maxQueue        int
	lastOverflowLog time.Time
	workerStarted   bool
	stopClosed      bool
	doneCh          chan struct{}

	wakeCh chan struct{}
	stopCh chan struct{}
	nowFn  func() time.Time
}

// NewNetEm 创建默认关闭（全 0）的弱网注入器。
func NewNetEm() *NetEm {
	return newNetEm(rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64())))
}

// NewNetEmWithSeed 创建使用固定随机种子的弱网注入器（便于确定性单元测试）。
func NewNetEmWithSeed(seed uint64) *NetEm {
	return newNetEm(rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)))
}

func newNetEm(rng *rand.Rand) *NetEm {
	return &NetEm{
		cfg:      NetEmConfig{Direction: NetEmDirectionBoth},
		rng:      rng,
		maxQueue: defaultNetEmMaxQueue,
		wakeCh:   make(chan struct{}, 1),
		stopCh:   make(chan struct{}),
		nowFn:    time.Now,
	}
}

// SetConfig 运行时热修改弱网参数。校验失败或 NetEm 已关闭时保持原配置不变并返回错误。
func (n *NetEm) SetConfig(cfg NetEmConfig) error {
	if n.closed.Load() {
		return ErrNetEmClosed
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	cfg = cfg.Normalized()
	n.mu.Lock()
	if n.closed.Load() {
		n.mu.Unlock()
		return ErrNetEmClosed
	}
	n.cfg = cfg
	n.enabled.Store(cfg.Enabled())
	n.mu.Unlock()
	return nil
}

// Config 返回当前配置副本。
func (n *NetEm) Config() NetEmConfig {
	n.mu.Lock()
	cfg := n.cfg
	n.mu.Unlock()
	return cfg
}

// Stats 返回当前注入统计。
func (n *NetEm) Stats() NetEmStats {
	return NetEmStats{
		TotalPackets:           n.totalPackets.Load(),
		DroppedPackets:         n.droppedPackets.Load(),
		OverflowDroppedPackets: n.overflowDroppedPackets.Load(),
		DelayedPackets:         n.delayedPackets.Load(),
		InboundPackets:         n.inboundPackets.Load(),
		OutboundPackets:        n.outboundPackets.Load(),
		QueuedPackets:          int(n.queuedPackets.Load()),
	}
}

// Snapshot 返回当前配置与统计的组合视图。
func (n *NetEm) Snapshot() NetEmSnapshot {
	cfg := n.Config()
	return NetEmSnapshot{
		Enabled:     !n.closed.Load() && cfg.Enabled(),
		DelayMs:     cfg.DelayMs,
		JitterMs:    cfg.JitterMs,
		LossPercent: cfg.LossPercent,
		Direction:   cfg.Direction,
		Stats:       n.Stats(),
	}
}

// ResetStats 清零累计包计数器（不影响排队中的延迟包计数）。
func (n *NetEm) ResetStats() {
	n.totalPackets.Store(0)
	n.droppedPackets.Store(0)
	n.overflowDroppedPackets.Store(0)
	n.delayedPackets.Store(0)
	n.inboundPackets.Store(0)
	n.outboundPackets.Store(0)
}

// HandleInbound 处理入站 UDP 数据报（调用方已持有独立缓冲区）。
func (n *NetEm) HandleInbound(deliver func()) {
	if n.closed.Load() {
		return
	}
	n.totalPackets.Add(1)
	n.inboundPackets.Add(1)
	if !n.enabled.Load() {
		if n.closed.Load() {
			return
		}
		deliver()
		return
	}
	drop, delay := n.decide(NetEmDirectionInbound)
	if drop {
		return
	}
	if delay <= 0 {
		if n.closed.Load() {
			return
		}
		deliver()
		return
	}
	n.schedule(delay, deliver)
}

// HandleOutbound 处理出站 UDP 数据报。
// 仅在需要延迟发送时才拷贝 data（避免覆盖 Peer 复用的 rawSendBuffer）；直发路径零拷贝。
func (n *NetEm) HandleOutbound(data []byte, deliver func([]byte)) {
	if n.closed.Load() {
		return
	}
	n.totalPackets.Add(1)
	n.outboundPackets.Add(1)
	if !n.enabled.Load() {
		if n.closed.Load() {
			return
		}
		deliver(data)
		return
	}
	drop, delay := n.decide(NetEmDirectionOutbound)
	if drop {
		return
	}
	if delay <= 0 {
		if n.closed.Load() {
			return
		}
		deliver(data)
		return
	}
	cp := append([]byte(nil), data...)
	n.schedule(delay, func() {
		deliver(cp)
	})
}

// Close 停止调度器、丢弃未发出的延迟包并等待调度 goroutine 退出（幂等）。
func (n *NetEm) Close() {
	n.closed.Store(true)

	n.mu.Lock()
	n.enabled.Store(false)
	if n.stopClosed {
		done := n.doneCh
		n.mu.Unlock()
		if done != nil {
			<-done
		}
		return
	}
	n.stopClosed = true
	clear(n.queue)
	n.queue = nil
	n.queuedPackets.Store(0)
	close(n.stopCh)
	done := n.doneCh
	n.mu.Unlock()

	if done != nil {
		<-done
	}
}

func (n *NetEm) decide(dir NetEmDirection) (drop bool, delay time.Duration) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.closed.Load() {
		return true, 0
	}
	if !n.cfg.Enabled() || !n.cfg.Direction.matches(dir) {
		return false, 0
	}
	if n.cfg.LossPercent > 0 && n.rng.Float64()*100.0 < n.cfg.LossPercent {
		n.droppedPackets.Add(1)
		return true, 0
	}
	delayMs := n.cfg.DelayMs
	if n.cfg.JitterMs > 0 {
		delta := n.rng.IntN(2*n.cfg.JitterMs+1) - n.cfg.JitterMs
		delayMs += delta
		if delayMs < 0 {
			delayMs = 0
		}
	}
	if delayMs <= 0 {
		return false, 0
	}
	return false, time.Duration(delayMs) * time.Millisecond
}

func (n *NetEm) schedule(delay time.Duration, deliver func()) {
	n.mu.Lock()
	if n.closed.Load() {
		n.mu.Unlock()
		return
	}
	if len(n.queue) >= n.maxQueue {
		totalOverflow := n.overflowDroppedPackets.Add(1)
		now := n.nowFn()
		shouldLog := n.lastOverflowLog.IsZero() || now.Sub(n.lastOverflowLog) >= netEmOverflowLogInterval
		if shouldLog {
			n.lastOverflowLog = now
		}
		maxQ := n.maxQueue
		n.mu.Unlock()
		if shouldLog {
			slog.Warn("netem delay queue overflow, dropping packet",
				"max_queue", maxQ,
				"overflow_dropped_packets", totalOverflow,
			)
		}
		return
	}
	n.nextSeq++
	heap.Push(&n.queue, delayedPacket{
		deliverAt: n.nowFn().Add(delay),
		seq:       n.nextSeq,
		deliver:   deliver,
	})
	n.delayedPackets.Add(1)
	n.queuedPackets.Store(int64(len(n.queue)))
	if !n.workerStarted {
		n.workerStarted = true
		n.doneCh = make(chan struct{})
		go n.runScheduler(n.doneCh)
	}
	n.mu.Unlock()

	select {
	case n.wakeCh <- struct{}{}:
	default:
	}
}

func (n *NetEm) runScheduler(doneCh chan struct{}) {
	defer close(doneCh)

	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

	var ready []func()
	for {
		n.mu.Lock()
		if n.closed.Load() {
			n.mu.Unlock()
			return
		}
		now := n.nowFn()
		ready = ready[:0]
		for len(n.queue) > 0 && !n.queue[0].deliverAt.After(now) {
			pkt := heap.Pop(&n.queue).(delayedPacket)
			ready = append(ready, pkt.deliver)
		}
		n.queuedPackets.Store(int64(len(n.queue)))

		if len(ready) > 0 {
			n.mu.Unlock()
			for _, deliver := range ready {
				select {
				case <-n.stopCh:
					return
				default:
				}
				deliver()
			}
			clear(ready)
			continue
		}

		if len(n.queue) == 0 {
			n.mu.Unlock()
			select {
			case <-n.stopCh:
				return
			case <-n.wakeCh:
			}
			continue
		}

		wait := n.queue[0].deliverAt.Sub(now)
		n.mu.Unlock()
		if wait <= 0 {
			continue
		}
		if timer == nil {
			timer = time.NewTimer(wait)
		} else {
			timer.Reset(wait)
		}
		select {
		case <-n.stopCh:
			return
		case <-n.wakeCh:
			timer.Stop()
		case <-timer.C:
		}
	}
}

type delayedPacket struct {
	deliverAt time.Time
	seq       uint64
	deliver   func()
}

type delayedHeap []delayedPacket

func (h delayedHeap) Len() int { return len(h) }
func (h delayedHeap) Less(i, j int) bool {
	if h[i].deliverAt.Equal(h[j].deliverAt) {
		return h[i].seq < h[j].seq
	}
	return h[i].deliverAt.Before(h[j].deliverAt)
}
func (h delayedHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *delayedHeap) Push(x any) {
	*h = append(*h, x.(delayedPacket))
}

func (h *delayedHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	old[n-1] = delayedPacket{}
	*h = old[:n-1]
	return item
}
