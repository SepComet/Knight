// Package tick 实现固定步长模拟循环（Docs/server-tick-loop.md）：
// 游戏域（64~127 权威模拟）的时间轴，独立于 gate goroutine。
// 命令管线与事件管线解决"响应"与"通知"，模拟循环解决"持续演进"——
// 对局状态按固定间隔推进，而不是随消息到达才变化。
//
// 线程模型：单个模拟 goroutine 轮询全部注册的 Tickable（与 gate goroutine
// 拥有全部 peer 同构）。跨线程交互只有 ops channel 一条通道：
// Add/Remove/Post 均提交为操作，在 tick 边界串行消费（对齐 kcp2k 的 ops 模式），
// 模拟单元表只在模拟 goroutine 上读写，无锁。出方向直接使用已有的
// event.Publish（总线线程安全）与 kcp2k.Send（ops 提交）。
//
// 时间语义：dt 固定为 1/Rate，不随实际执行时间伸缩、不追帧——tick 迟到时
// 模拟时间落后于墙钟（负载哨兵：单次 tick 超过 dt 记 Warn）。
// 模拟逻辑内部一律以 tick 计数与 dt 表达时间，禁止读墙钟，
// 这是 Step 模式确定性测试的前提。
package tick

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"
)

const (
	minRate = 1
	maxRate = 120
)

// Tickable 一个模拟单元（后续一局对局实现一个）。
// 实现须为可比较类型（通常取指针），Remove 按相等性移除。
type Tickable interface {
	// Tick 推进一个固定步长。dt 恒为 1/Rate，不随执行时间变化。
	Tick(dt time.Duration)
}

// Config 模拟循环配置。
type Config struct {
	Rate int // 每秒 tick 数（KNIGHT_TICK_RATE），1~120
}

// Runner 固定步长模拟循环。Run（真实时钟）与 Step（测试手动步进）二选一，
// 不得并用——两者都会驱动同一单元表，并用会重复步进。
type Runner struct {
	dt  time.Duration
	ops chan func() // Add/Remove/Post 统一走此队列，tick 边界消费

	stopCh    chan struct{}
	closeOnce sync.Once

	mu      sync.Mutex
	stopped bool          // 置位后提交为空操作；由 Stop 与 Run 退出路径幂等置位
	runDone chan struct{} // Run 运行期间非 nil，Run 完全退出后关闭；供 Stop 同步等待

	units           []Tickable // 仅在模拟 goroutine 上读写（ops 操作与 tickOnce）
	inStep          bool       // 仅在模拟 goroutine 上读写：stepUnits 遍历期间置位
	pendingRemovals []Tickable // 仅在模拟 goroutine 上读写：stepUnits 期间的延迟移除队列
}

// NewRunner 构造并校验配置。
func NewRunner(cfg Config) (*Runner, error) {
	if cfg.Rate < minRate || cfg.Rate > maxRate {
		return nil, fmt.Errorf("tick: rate=%d 超出合法范围 %d~%d（KNIGHT_TICK_RATE）", cfg.Rate, minRate, maxRate)
	}
	return &Runner{
		dt:     time.Second / time.Duration(cfg.Rate),
		ops:    make(chan func(), 1024),
		stopCh: make(chan struct{}),
	}, nil
}

// Dt 固定步长（诊断/测试用）。
func (r *Runner) Dt() time.Duration { return r.dt }

// ---------------------------------------------------------------- 驱动

// Run 真实时钟模式：按 1/rate 周期步进，ctx 取消或 Stop 后返回。
// 固定 dt 不追帧：处理超时时 time.Ticker 自行丢拍，模拟时间落后于墙钟。
func (r *Runner) Run(ctx context.Context) {
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return
	}
	done := make(chan struct{})
	r.runDone = done
	r.mu.Unlock()

	defer func() {
		r.markStopped()
		close(done)
	}()

	ticker := time.NewTicker(r.dt)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stopCh:
			return
		case <-ticker.C:
			start := time.Now()
			r.tickOnce()
			// 负载哨兵：仅告警，不追帧
			if elapsed := time.Since(start); elapsed > r.dt {
				slog.Warn("tick overran interval", "elapsed", elapsed, "dt", r.dt, "units", len(r.units))
			}
		}
	}
}

// Step 测试模式：手动步进 steps 次，时钟为 tick 计数，不依赖真实时间。
// 不得与 Run 并用。
func (r *Runner) Step(steps int) {
	for i := 0; i < steps; i++ {
		r.tickOnce()
	}
}

// Stop 请求退出 Run 并等待正在执行的 tickOnce 与 Run 完全退出（幂等；
// 未在 Run 时调用则立即返回，此后提交均为空操作）。
func (r *Runner) Stop() {
	r.mu.Lock()
	r.stopped = true
	done := r.runDone
	r.mu.Unlock()
	r.closeOnce.Do(func() { close(r.stopCh) })
	if done != nil {
		<-done
	}
}

// markStopped 幂等置停：置位提交闸门并关闭 stopCh（唤醒可能阻塞的提交方）。
func (r *Runner) markStopped() {
	r.mu.Lock()
	r.stopped = true
	r.mu.Unlock()
	r.closeOnce.Do(func() { close(r.stopCh) })
}

// ---------------------------------------------------------------- 提交（任意 goroutine）

// Add 注册模拟单元：在下一个 tick 边界起参与步进。
func (r *Runner) Add(t Tickable) { r.submit(func() { r.units = append(r.units, t) }) }

// Remove 注销模拟单元：同一 tick 内不再调用；不存在时为幂等空操作。
// 跨 goroutine 调用安全；若已在模拟 goroutine（Post 回调或 Tick 方法）内，请改用 RemoveNow 避免向自身 ops 通道投递死锁。
func (r *Runner) Remove(t Tickable) {
	r.submit(func() {
		r.removeUnit(t)
	})
}

// RemoveNow 在模拟 goroutine 内部（Post 回调或 Tick 方法内）同步注销模拟单元，
// 不经 ops 通道，从根本上避免消费者自投递满队列死锁。禁止从模拟 goroutine 以外调用。
func (r *Runner) RemoveNow(t Tickable) {
	if r.inStep {
		r.pendingRemovals = append(r.pendingRemovals, t)
		return
	}
	r.removeUnit(t)
}

func (r *Runner) removeUnit(t Tickable) {
	for i, u := range r.units {
		if u == t {
			copy(r.units[i:], r.units[i+1:])
			r.units[len(r.units)-1] = nil
			r.units = r.units[:len(r.units)-1]
			return
		}
	}
}

// Post 提交一次性任务（输入投递等），在下一个 tick 边界、单元 Tick 之前执行。
func (r *Runner) Post(fn func()) {
	if fn == nil {
		panic("tick: Post 函数不可为空")
	}
	r.submit(fn)
}

// submit 提交操作；已停止时空操作，Stop 时唤醒阻塞中的提交方。
func (r *Runner) submit(fn func()) {
	r.mu.Lock()
	stopped := r.stopped
	r.mu.Unlock()
	if stopped {
		return
	}
	select {
	case r.ops <- fn:
	case <-r.stopCh:
	}
}

// ---------------------------------------------------------------- 模拟 goroutine

// tickOnce 一个完整 tick：先消费 ops（Add/Remove/Post 在单元 Tick 前生效），再按注册顺序步进。
func (r *Runner) tickOnce() {
	for {
		select {
		case fn := <-r.ops:
			fn()
		default:
			r.stepUnits()
			return
		}
	}
}

// stepUnits 按注册顺序派发，逐单元 recover：单个对局的缺陷不拖垮时间轴，
// 进程级 fail-fast 边界不变（循环骨架本身无 recover）。
func (r *Runner) stepUnits() {
	r.inStep = true
	for _, u := range r.units {
		if r.isPendingRemoval(u) {
			continue
		}
		r.callUnit(u)
	}
	r.inStep = false
	if len(r.pendingRemovals) > 0 {
		for _, t := range r.pendingRemovals {
			r.removeUnit(t)
		}
		clear(r.pendingRemovals)
		r.pendingRemovals = r.pendingRemovals[:0]
	}
}

func (r *Runner) isPendingRemoval(t Tickable) bool {
	for _, p := range r.pendingRemovals {
		if p == t {
			return true
		}
	}
	return false
}

func (r *Runner) callUnit(u Tickable) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("tick unit panic", "unit", fmt.Sprintf("%T", u), "panic", rec, "stack", string(debug.Stack()))
		}
	}()
	u.Tick(r.dt)
}
