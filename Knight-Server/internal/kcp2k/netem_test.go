package kcp2k

import (
	"bytes"
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNetEmConfigValidation(t *testing.T) {
	valid := []NetEmConfig{
		{},
		{DelayMs: 0, JitterMs: 0, LossPercent: 0, Direction: NetEmDirectionBoth},
		{DelayMs: NetEmMaxDelayMs, JitterMs: NetEmMaxJitterMs, LossPercent: NetEmMaxLossPercent, Direction: NetEmDirectionOutbound},
		{DelayMs: 150, JitterMs: 20, LossPercent: 3.5, Direction: NetEmDirectionInbound},
	}
	for _, cfg := range valid {
		if err := cfg.Validate(); err != nil {
			t.Fatalf("expected valid config %+v, got err: %v", cfg, err)
		}
	}

	if got := (NetEmConfig{}).Normalized().Direction; got != NetEmDirectionBoth {
		t.Fatalf("normalized empty direction = %q, want %q", got, NetEmDirectionBoth)
	}

	invalid := []NetEmConfig{
		{DelayMs: -1},
		{DelayMs: NetEmMaxDelayMs + 1},
		{JitterMs: -1},
		{JitterMs: NetEmMaxJitterMs + 1},
		{LossPercent: -0.1},
		{LossPercent: NetEmMaxLossPercent + 0.1},
		{LossPercent: math.NaN()},
		{LossPercent: math.Inf(1)},
		{Direction: "sideways"},
	}
	for _, cfg := range invalid {
		if err := cfg.Validate(); err == nil {
			t.Fatalf("expected error for invalid config %+v, got nil", cfg)
		}
	}
}

func TestNetEmZeroConfigPassthrough(t *testing.T) {
	ne := NewNetEm()
	defer ne.Close()

	var inCount, outCount int
	payload := []byte("hello-kcp")
	ne.HandleInbound(func() { inCount++ })
	ne.HandleOutbound(payload, func(b []byte) {
		outCount++
		if !bytes.Equal(b, payload) {
			t.Fatalf("outbound payload mismatch: %q", b)
		}
	})

	if inCount != 1 || outCount != 1 {
		t.Fatalf("in=%d out=%d, want 1, 1", inCount, outCount)
	}

	snap := ne.Snapshot()
	if snap.Enabled {
		t.Fatal("expected Enabled=false for zero config")
	}
	if snap.Stats.TotalPackets != 2 || snap.Stats.InboundPackets != 1 || snap.Stats.OutboundPackets != 1 {
		t.Fatalf("unexpected stats: %+v", snap.Stats)
	}
	if snap.Stats.DroppedPackets != 0 || snap.Stats.DelayedPackets != 0 || snap.Stats.QueuedPackets != 0 {
		t.Fatalf("expected zero drops/delays/queued, got %+v", snap.Stats)
	}

	ne.mu.Lock()
	started := ne.workerStarted
	ne.mu.Unlock()
	if started {
		t.Fatal("zero-config passthrough must not start scheduler goroutine")
	}
}

func TestNetEmLossRateAndDirection(t *testing.T) {
	ne := NewNetEmWithSeed(42)
	defer ne.Close()

	if err := ne.SetConfig(NetEmConfig{
		LossPercent: 25.0,
		Direction:   NetEmDirectionOutbound,
	}); err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}

	const n = 4000
	var inDelivered, outDelivered int
	for i := 0; i < n; i++ {
		ne.HandleInbound(func() { inDelivered++ })
		ne.HandleOutbound([]byte{1, 2, 3}, func([]byte) { outDelivered++ })
	}

	// Inbound must not be affected when direction=outbound.
	if inDelivered != n {
		t.Fatalf("inbound delivered = %d, want %d", inDelivered, n)
	}

	dropped := n - outDelivered
	rate := float64(dropped) / float64(n) * 100.0
	if rate < 22.0 || rate > 28.0 {
		t.Fatalf("outbound loss rate = %.2f%% (dropped=%d/%d), want ~25%%", rate, dropped, n)
	}

	stats := ne.Stats()
	if stats.DroppedPackets != uint64(dropped) {
		t.Fatalf("stats.DroppedPackets = %d, want %d", stats.DroppedPackets, dropped)
	}
	if stats.TotalPackets != 2*n {
		t.Fatalf("stats.TotalPackets = %d, want %d", stats.TotalPackets, 2*n)
	}

	ne.ResetStats()
	if s := ne.Stats(); s.TotalPackets != 0 || s.DroppedPackets != 0 {
		t.Fatalf("expected stats reset to 0, got %+v", s)
	}
}

func TestNetEmDelayJitterAndCopyIsolation(t *testing.T) {
	ne := NewNetEmWithSeed(7)
	defer ne.Close()

	if err := ne.SetConfig(NetEmConfig{
		DelayMs:   40,
		JitterMs:  15,
		Direction: NetEmDirectionOutbound,
	}); err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}

	const count = 12
	type result struct {
		elapsed time.Duration
		data    []byte
	}
	ch := make(chan result, count)

	// Reuse a single buffer (just like Peer.rawSendBuffer) to prove NetEm copies before delaying.
	reusedBuf := make([]byte, 8)
	for i := 0; i < count; i++ {
		reusedBuf[0] = byte(i + 1)
		reusedBuf[1] = byte(0xA0 + i)
		sentAt := time.Now()
		ne.HandleOutbound(reusedBuf[:2], func(payload []byte) {
			ch <- result{elapsed: time.Since(sentAt), data: append([]byte(nil), payload...)}
		})
		// Immediately mutate reusedBuf after HandleOutbound returns.
		reusedBuf[0] = 0xFF
		reusedBuf[1] = 0xFF
	}

	seen := make(map[byte]bool, count)
	var minElapsed = time.Hour
	var maxElapsed time.Duration
	for i := 0; i < count; i++ {
		select {
		case res := <-ch:
			if len(res.data) != 2 || res.data[0] == 0xFF {
				t.Fatalf("buffer was corrupted by caller mutation: %x", res.data)
			}
			if res.data[1] != byte(0xA0+int(res.data[0]-1)) {
				t.Fatalf("payload mismatch: %x", res.data)
			}
			seen[res.data[0]] = true
			if res.elapsed < minElapsed {
				minElapsed = res.elapsed
			}
			if res.elapsed > maxElapsed {
				maxElapsed = res.elapsed
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for delayed packet %d/%d", i+1, count)
		}
	}

	if len(seen) != count {
		t.Fatalf("received %d unique packets, want %d", len(seen), count)
	}
	// Expected range [25ms, 55ms] plus OS timer scheduling slack.
	if minElapsed < 20*time.Millisecond {
		t.Fatalf("minElapsed = %v, expected >= 20ms", minElapsed)
	}
	if maxElapsed > 500*time.Millisecond {
		t.Fatalf("maxElapsed = %v, unexpectedly large", maxElapsed)
	}

	stats := ne.Stats()
	if stats.DelayedPackets != count {
		t.Fatalf("DelayedPackets = %d, want %d", stats.DelayedPackets, count)
	}
}

func TestNetEmConcurrentSafetyAndClose(t *testing.T) {
	ne := NewNetEmWithSeed(99)
	if err := ne.SetConfig(NetEmConfig{
		DelayMs:     250,
		JitterMs:    50,
		LossPercent: 10,
		Direction:   NetEmDirectionBoth,
	}); err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}

	var deliveredAfterClose atomic.Int32
	var closedFlag atomic.Bool

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				if i%25 == 0 {
					_ = ne.SetConfig(NetEmConfig{
						DelayMs:     200 + (id+i)%100,
						JitterMs:    20,
						LossPercent: 5,
						Direction:   NetEmDirectionBoth,
					})
					_ = ne.Snapshot()
				}
				ne.HandleInbound(func() {
					if closedFlag.Load() {
						deliveredAfterClose.Add(1)
					}
				})
				ne.HandleOutbound([]byte("pkt"), func([]byte) {
					if closedFlag.Load() {
						deliveredAfterClose.Add(1)
					}
				})
			}
		}(g)
	}

	time.Sleep(15 * time.Millisecond)
	ne.Close()
	closedFlag.Store(true)
	// Idempotent second Close.
	ne.Close()
	wg.Wait()

	if got := ne.Stats().QueuedPackets; got != 0 {
		t.Fatalf("QueuedPackets after Close = %d, want 0", got)
	}
	time.Sleep(30 * time.Millisecond)
	if got := deliveredAfterClose.Load(); got != 0 {
		t.Fatalf("callbacks fired after Close: %d", got)
	}
}

func TestServerNetEmIntegration(t *testing.T) {
	cfg := DefaultConfig()
	cfg.FastResend = 2

	received := make(chan []byte, 8)
	srv, err := Listen("127.0.0.1:0", cfg, ServerCallbacks{
		OnData: func(connID string, message []byte, ch Channel) {
			received <- append([]byte(nil), message...)
		},
	})
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}
	go srv.Run()
	defer srv.Stop()

	// Inject 30ms one-way delay on both directions (~60ms RTT) + 5% loss.
	if err := srv.NetEm().SetConfig(NetEmConfig{
		DelayMs:     30,
		JitterMs:    5,
		LossPercent: 5,
		Direction:   NetEmDirectionBoth,
	}); err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}

	connected := make(chan struct{}, 1)
	cli, err := Dial(cfg, srv.LocalAddr().String(), ClientCallbacks{
		OnConnected: func() {
			select {
			case connected <- struct{}{}:
			default:
			}
		},
	})
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer cli.Disconnect()

	start := time.Now()
	deadline := time.After(3 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	// Pump client until handshake completes under NetEm.
HandshakeLoop:
	for {
		now := uint32(time.Since(start).Milliseconds())
		cli.TickIncoming(now)
		cli.TickOutgoing(now)
		select {
		case <-connected:
			break HandshakeLoop
		case <-deadline:
			t.Fatal("timed out waiting for handshake under netem")
		case <-ticker.C:
		}
	}

	// Send payload and verify delivery under NetEm.
	want := []byte("m2-netem-ping")
	sentAt := time.Now()
	cli.Send(want, ChannelReliable)

RecvLoop:
	for {
		now := uint32(time.Since(start).Milliseconds())
		cli.TickIncoming(now)
		cli.TickOutgoing(now)
		select {
		case got := <-received:
			if !bytes.Equal(got, want) {
				t.Fatalf("received %q, want %q", got, want)
			}
			if elapsed := time.Since(sentAt); elapsed < 20*time.Millisecond {
				t.Fatalf("expected delayed delivery >= 20ms, got %v", elapsed)
			}
			break RecvLoop
		case <-deadline:
			t.Fatal("timed out waiting for data under netem")
		case <-ticker.C:
		}
	}

	snap := srv.NetEm().Snapshot()
	if !snap.Enabled || snap.Stats.DelayedPackets == 0 || snap.Stats.TotalPackets == 0 {
		t.Fatalf("expected non-zero netem stats, got %+v", snap)
	}

	// Stop server while delay is still active to ensure clean shutdown without panic/leak.
	srv.Stop()
}

func TestNetEmOverflowDroppedStats(t *testing.T) {
	ne := NewNetEmWithSeed(11)
	defer ne.Close()

	ne.mu.Lock()
	ne.maxQueue = 4
	ne.mu.Unlock()

	if err := ne.SetConfig(NetEmConfig{
		DelayMs:     200,
		LossPercent: 0,
		Direction:   NetEmDirectionBoth,
	}); err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}

	const total = 10
	for i := 0; i < total; i++ {
		if i%2 == 0 {
			ne.HandleInbound(func() {})
		} else {
			ne.HandleOutbound([]byte("pkt"), func([]byte) {})
		}
	}

	stats := ne.Stats()
	if stats.OverflowDroppedPackets == 0 {
		t.Fatalf("expected OverflowDroppedPackets > 0, got %+v", stats)
	}
	if stats.OverflowDroppedPackets != uint64(total-4) {
		t.Fatalf("OverflowDroppedPackets = %d, want %d (stats=%+v)", stats.OverflowDroppedPackets, total-4, stats)
	}
	if stats.DroppedPackets != 0 {
		t.Fatalf("DroppedPackets = %d, want 0 (overflow must not mix into simulated drops)", stats.DroppedPackets)
	}
	if stats.DelayedPackets != 4 {
		t.Fatalf("DelayedPackets = %d, want 4", stats.DelayedPackets)
	}

	ne.ResetStats()
	if after := ne.Stats(); after.OverflowDroppedPackets != 0 || after.DroppedPackets != 0 {
		t.Fatalf("ResetStats did not clear overflow/dropped counters: %+v", after)
	}
}

func TestNetEmClosedGuards(t *testing.T) {
	// Case 1: Close when disabled (zero-config fast path).
	neDisabled := NewNetEm()
	neDisabled.Close()

	if err := neDisabled.SetConfig(NetEmConfig{DelayMs: 50}); !errors.Is(err, ErrNetEmClosed) {
		t.Fatalf("SetConfig after Close err = %v, want ErrNetEmClosed", err)
	}
	if neDisabled.enabled.Load() || neDisabled.Snapshot().Enabled {
		t.Fatal("NetEm must remain disabled after Close + SetConfig")
	}

	var delivered int
	neDisabled.HandleInbound(func() { delivered++ })
	neDisabled.HandleOutbound([]byte("abc"), func([]byte) { delivered++ })
	if delivered != 0 {
		t.Fatalf("expected 0 deliveries after Close (disabled fast path), got %d", delivered)
	}

	// Case 2: Close when enabled.
	neEnabled := NewNetEm()
	if err := neEnabled.SetConfig(NetEmConfig{DelayMs: 100, Direction: NetEmDirectionBoth}); err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}
	neEnabled.Close()

	if neEnabled.enabled.Load() || neEnabled.Snapshot().Enabled {
		t.Fatal("NetEm must report Enabled=false after Close")
	}
	if err := neEnabled.SetConfig(NetEmConfig{DelayMs: 80}); !errors.Is(err, ErrNetEmClosed) {
		t.Fatalf("SetConfig after Close err = %v, want ErrNetEmClosed", err)
	}
	if neEnabled.enabled.Load() || neEnabled.Snapshot().Enabled {
		t.Fatal("NetEm must not re-enable after Close")
	}
	neEnabled.HandleInbound(func() { delivered++ })
	neEnabled.HandleOutbound([]byte("xyz"), func([]byte) { delivered++ })
	if delivered != 0 {
		t.Fatalf("expected 0 deliveries after Close (enabled path), got %d", delivered)
	}
}

func TestNetEmHotDisableDrainsInFlightPackets(t *testing.T) {
	ne := NewNetEmWithSeed(77)
	defer ne.Close()

	if err := ne.SetConfig(NetEmConfig{
		DelayMs:   100,
		Direction: NetEmDirectionBoth,
	}); err != nil {
		t.Fatalf("SetConfig(100ms) failed: %v", err)
	}

	const inFlightCount = 10
	inFlightCh := make(chan int, inFlightCount)
	for i := 0; i < inFlightCount; i++ {
		id := i
		if i%2 == 0 {
			ne.HandleInbound(func() {
				inFlightCh <- id
			})
		} else {
			ne.HandleOutbound([]byte{byte(id)}, func(b []byte) {
				inFlightCh <- int(b[0])
			})
		}
	}

	if got := ne.Stats().QueuedPackets; got != inFlightCount {
		t.Fatalf("QueuedPackets before hot-disable = %d, want %d", got, inFlightCount)
	}

	// Hot-switch delay_ms from 100 back to 0 while packets are still queued.
	if err := ne.SetConfig(NetEmConfig{DelayMs: 0, Direction: NetEmDirectionBoth}); err != nil {
		t.Fatalf("SetConfig(0ms) failed: %v", err)
	}
	if ne.Snapshot().Enabled {
		t.Fatal("expected Enabled=false after hot-switch to 0")
	}

	// New packets sent after hot-disable should deliver immediately.
	var immediateDelivered int
	ne.HandleInbound(func() { immediateDelivered++ })
	ne.HandleOutbound([]byte("now"), func([]byte) { immediateDelivered++ })
	if immediateDelivered != 2 {
		t.Fatalf("immediateDelivered = %d, want 2", immediateDelivered)
	}

	// All previously queued in-flight packets must still be delivered completely without loss.
	seen := make(map[int]bool, inFlightCount)
	timeout := time.After(2 * time.Second)
	for i := 0; i < inFlightCount; i++ {
		select {
		case id := <-inFlightCh:
			seen[id] = true
		case <-timeout:
			t.Fatalf("timed out draining in-flight packets after hot-disable: received %d/%d", len(seen), inFlightCount)
		}
	}
	if len(seen) != inFlightCount {
		t.Fatalf("received %d unique in-flight packets, want %d", len(seen), inFlightCount)
	}
	if got := ne.Stats().QueuedPackets; got != 0 {
		t.Fatalf("QueuedPackets after drain = %d, want 0", got)
	}
}
