package tick_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"knight-server/internal/tick"
)

// recUnit 记录每次收到的 dt；可注入 panic 与睡眠（负载哨兵测试）。
type recUnit struct {
	name  string
	dts   []time.Duration
	panic bool
	sleep time.Duration
}

func (u *recUnit) Tick(dt time.Duration) {
	if u.sleep > 0 {
		time.Sleep(u.sleep)
	}
	if u.panic {
		panic("boom:" + u.name)
	}
	u.dts = append(u.dts, dt)
}

func newRunner(t *testing.T, rate int) *tick.Runner {
	t.Helper()
	r, err := tick.NewRunner(tick.Config{Rate: rate})
	if err != nil {
		t.Fatalf("new runner: %v", err)
	}
	return r
}

func TestNewRunnerValidatesRate(t *testing.T) {
	for _, rate := range []int{0, -1, 121, 1000} {
		if _, err := tick.NewRunner(tick.Config{Rate: rate}); err == nil {
			t.Fatalf("rate=%d 应拒绝启动", rate)
		}
	}
	for _, rate := range []int{1, 30, 120} {
		if _, err := tick.NewRunner(tick.Config{Rate: rate}); err != nil {
			t.Fatalf("rate=%d 应合法: %v", rate, err)
		}
	}
}

func TestStepDeliversFixedDt(t *testing.T) {
	r := newRunner(t, 30)
	u := &recUnit{name: "a"}
	r.Add(u)
	r.Step(3)

	if len(u.dts) != 3 {
		t.Fatalf("ticks = %d, want 3", len(u.dts))
	}
	// dt 恒为 1/rate，不随执行时间伸缩
	for i, dt := range u.dts {
		if dt != time.Second/30 {
			t.Fatalf("tick %d dt = %v, want %v", i, dt, time.Second/30)
		}
	}
}

func TestRegistrationOrderAndOncePerTick(t *testing.T) {
	r := newRunner(t, 30)
	var order []string
	u1 := &seqUnit{tag: "1", sink: &order}
	u2 := &seqUnit{tag: "2", sink: &order}
	r.Add(u1)
	r.Add(u2)
	r.Step(2)

	if len(order) != 4 {
		t.Fatalf("order = %v, want 4 entries", order)
	}
	want := []string{"1", "2", "1", "2"} // 注册顺序，每 tick 一次
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order[%d] = %q, want %q（完整序列 %v）", i, order[i], want[i], order)
		}
	}
}

type seqUnit struct {
	tag  string
	sink *[]string
}

func (s *seqUnit) Tick(time.Duration) { *s.sink = append(*s.sink, s.tag) }

func TestAddRemoveBoundaries(t *testing.T) {
	r := newRunner(t, 30)
	a := &recUnit{name: "a"}
	b := &recUnit{name: "b"}

	r.Add(a)
	r.Step(1)
	if len(a.dts) != 1 {
		t.Fatalf("a ticks = %d, want 1", len(a.dts))
	}

	// Add 提交后于下一个 tick 边界参与
	r.Add(b)
	r.Step(1)
	if len(b.dts) != 1 || len(a.dts) != 2 {
		t.Fatalf("a = %d, b = %d, want a 2 b 1", len(a.dts), len(b.dts))
	}

	// Remove 后同一 tick 内不再调用
	r.Remove(a)
	r.Step(1)
	if len(a.dts) != 2 {
		t.Fatalf("removed unit still ticked: a = %d", len(a.dts))
	}
	if len(b.dts) != 2 {
		t.Fatalf("b ticks = %d, want 2", len(b.dts))
	}

	// 幂等与未知单元
	r.Remove(a)
	r.Remove(b)
	r.Step(1)
	if len(b.dts) != 2 {
		t.Fatalf("b ticks = %d, want 2", len(b.dts))
	}
}

func TestPostRunsAtTickBoundary(t *testing.T) {
	r := newRunner(t, 30)
	ran := 0
	r.Post(func() { ran++ })

	// Post 在 tick 边界、单元 Tick 之前执行
	u := &recUnit{name: "u"}
	seenByUnit := 0
	wrap := &observeUnit{inner: u, observe: func() { seenByUnit = ran }}
	r.Add(wrap)
	r.Step(1)
	if ran != 1 || seenByUnit != 1 {
		t.Fatalf("ran = %d seenByUnit = %d, want 1/1", ran, seenByUnit)
	}

	// Tick 内 Post：下一个 tick 边界才执行
	r.Post(func() { ran += 10 })
	r.Step(0)
	if ran != 1 {
		t.Fatalf("ran = %d, want 1（Step(0) 不应推进）", ran)
	}
	r.Step(1)
	if ran != 11 {
		t.Fatalf("ran = %d, want 11", ran)
	}
}

type observeUnit struct {
	inner   *recUnit
	observe func()
}

func (o *observeUnit) Tick(dt time.Duration) {
	o.observe()
	o.inner.Tick(dt)
}

func TestPanicIsolated(t *testing.T) {
	r := newRunner(t, 30)
	bad := &recUnit{name: "bad", panic: true}
	good := &recUnit{name: "good"}
	r.Add(bad)
	r.Add(good)

	r.Step(1)
	if len(good.dts) != 1 {
		t.Fatalf("panic 后后续单元仍应执行: good = %d", len(good.dts))
	}
	r.Step(1)
	if len(good.dts) != 2 || len(bad.dts) != 0 {
		t.Fatalf("good = %d bad = %d, want 2/0（bad 每 tick 都 panic）", len(good.dts), len(bad.dts))
	}
}

func TestRunStopsOnContextCancel(t *testing.T) {
	r := newRunner(t, 100) // dt = 10ms，快速产生 tick
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		r.Run(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run 未在 ctx 取消后退出")
	}
}

func TestRunStopsOnStop(t *testing.T) {
	r := newRunner(t, 100)
	done := make(chan struct{})
	go func() {
		r.Run(context.Background())
		close(done)
	}()

	time.Sleep(30 * time.Millisecond)
	r.Stop()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run 未在 Stop 后退出")
	}
}

func TestSubmitAfterStopIsNoop(t *testing.T) {
	r := newRunner(t, 30)
	r.Stop()
	r.Stop() // 幂等

	ran := false
	r.Post(func() { ran = true }) // 不得阻塞或 panic
	u := &recUnit{name: "u"}
	r.Add(u)
	r.Step(1)
	if ran || len(u.dts) != 0 {
		t.Fatalf("停止后的提交应为空操作: ran=%v ticks=%d", ran, len(u.dts))
	}
}

// TestTickOverrunWarns 负载哨兵：单次 tick 处理超过 dt 时记 Warn。
func TestTickOverrunWarns(t *testing.T) {
	capture := &captureHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(capture))
	t.Cleanup(func() { slog.SetDefault(prev) })

	r := newRunner(t, 50) // dt = 20ms
	slow := &recUnit{name: "slow", sleep: 35 * time.Millisecond}
	r.Add(slow)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	r.Run(ctx) // 每 tick 35ms > 20ms，必触发哨兵

	if capture.overruns == 0 {
		t.Fatal("慢单元应触发 tick overran interval 告警")
	}
}

type captureHandler struct {
	overruns int
}

func (h *captureHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, rec slog.Record) error {
	if rec.Message == "tick overran interval" {
		h.overruns++
	}
	return nil
}

func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *captureHandler) WithGroup(string) slog.Handler { return h }
