package snowflake

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// TestNewNodeWorkerRange workerID 合法区间 [0, 1023]。
func TestNewNodeWorkerRange(t *testing.T) {
	if _, err := NewNode(-1); err == nil {
		t.Fatal("worker -1 should fail")
	}
	if _, err := NewNode(1024); err == nil {
		t.Fatal("worker 1024 should fail")
	}
	if _, err := NewNode(0); err != nil {
		t.Fatalf("worker 0: %v", err)
	}
	if _, err := NewNode(1023); err != nil {
		t.Fatalf("worker 1023: %v", err)
	}
}

// TestNextUniqueAndMonotonic 真实时钟下并发生成：全局唯一且单节点严格递增。
func TestNextUniqueAndMonotonic(t *testing.T) {
	node, _ := NewNode(7)
	const total = 10000
	const workers = 8

	var mu sync.Mutex
	ids := make([]uint64, 0, total)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range total / workers {
				id, err := node.Next()
				if err != nil {
					t.Errorf("next: %v", err)
					return
				}
				mu.Lock()
				ids = append(ids, id)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if len(ids) != total {
		t.Fatalf("len = %d, want %d", len(ids), total)
	}
	seen := make(map[uint64]bool, total)
	for _, id := range ids {
		if seen[id] {
			t.Fatalf("duplicate id %d", id)
		}
		seen[id] = true
	}
}

// TestNextSequenceOverflow 同毫秒耗尽 4096 个序列后等待下一毫秒，ID 仍严格递增。
func TestNextSequenceOverflow(t *testing.T) {
	base := epoch.Add(time.Second)
	node, _ := NewNode(0)

	// 前 4097 次读钟都停在同一毫秒（第 4097 次 Next 触发序列回绕），
	// 之后时钟进入下一毫秒
	calls := 0
	node.nowFunc = func() time.Time {
		calls++
		if calls <= 4097 {
			return base
		}
		return base.Add(time.Millisecond)
	}

	prev := uint64(0)
	for i := range 4097 {
		id, err := node.Next()
		if err != nil {
			t.Fatalf("next %d: %v", i, err)
		}
		if i > 0 && id <= prev {
			t.Fatalf("id %d not increasing (prev %d)", id, prev)
		}
		prev = id
	}
}

// TestNextClockRollbackWithinLimit 回拨在等待上限内：等时钟追上后再生成，不产出回退 ID。
func TestNextClockRollbackWithinLimit(t *testing.T) {
	base := epoch.Add(time.Second)
	back := base.Add(-3 * time.Millisecond)
	node, _ := NewNode(0)
	node.nowFunc = scriptedClock(base, back, back.Add(time.Millisecond), base)

	first, err := node.Next()
	if err != nil {
		t.Fatalf("first next: %v", err)
	}
	second, err := node.Next()
	if err != nil {
		t.Fatalf("next after rollback: %v", err)
	}
	if second <= first {
		t.Fatalf("second %d should be greater than first %d", second, first)
	}
}

// TestNextClockRollbackBeyondLimit 回拨超过等待上限：拒绝生成，返回 ErrClockRollback。
func TestNextClockRollbackBeyondLimit(t *testing.T) {
	// 时刻均取纪元之后，避免负时长在 uint64 上回绕
	base := epoch.Add(10 * time.Second)
	node, _ := NewNode(0)
	node.nowFunc = scriptedClock(base, base.Add(-8*time.Second))

	if _, err := node.Next(); err != nil {
		t.Fatalf("first next: %v", err)
	}
	_, err := node.Next()
	if !errors.Is(err, ErrClockRollback) {
		t.Fatalf("err = %v, want ErrClockRollback", err)
	}
}

// scriptedClock 依次返回预设时刻（耗尽后停在最后一个）。
func scriptedClock(times ...time.Time) func() time.Time {
	i := 0
	return func() time.Time {
		if i < len(times) {
			t := times[i]
			i++
			return t
		}
		return times[len(times)-1]
	}
}
