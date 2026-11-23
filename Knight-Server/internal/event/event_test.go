package event_test

import (
	"sync"
	"sync/atomic"
	"testing"

	"knight-server/internal/event"
)

var (
	evInt    = event.Declare[int]("test.int")
	evOther  = event.Declare[int]("test.other")
	evSignal = event.Declare[struct{}]("test.signal")
)

func TestPublishDeliversTypedPayload(t *testing.T) {
	b := event.NewBus()
	var got []int // 派发与订阅同 goroutine，无需加锁
	event.Subscribe(b, evInt, func(v int) { got = append(got, v) })

	event.Publish(b, evInt, 42)
	event.Publish(b, evInt, 7)
	if len(got) != 2 || got[0] != 42 || got[1] != 7 {
		t.Fatalf("got = %v", got)
	}
}

func TestSubscribeWithoutPayload(t *testing.T) {
	b := event.NewBus()
	hits := 0
	event.Subscribe(b, evSignal, func(struct{}) { hits++ })

	event.Publish(b, evSignal, struct{}{})
	if hits != 1 {
		t.Fatalf("hits = %d", hits)
	}
}

func TestEventsAreIsolated(t *testing.T) {
	b := event.NewBus()
	hits := 0
	event.Subscribe(b, evInt, func(int) { hits++ })

	event.Publish(b, evOther, 1) // 不同事件互不影响
	if hits != 0 {
		t.Fatalf("hits = %d", hits)
	}
	event.Publish(b, evInt, 1)
	if hits != 1 {
		t.Fatalf("hits = %d", hits)
	}
}

func TestNoSubscribersIsNoop(t *testing.T) {
	event.Publish(event.NewBus(), evInt, 1)
}

func TestDuplicateSubscriptionDeliveredTwice(t *testing.T) {
	b := event.NewBus()
	hits := 0
	h := func(int) { hits++ }
	event.Subscribe(b, evInt, h)
	event.Subscribe(b, evInt, h)

	event.Publish(b, evInt, 1)
	if hits != 2 {
		t.Fatalf("hits = %d", hits)
	}
}

func TestUnsubscribeRemovesOwnRegistrationOnly(t *testing.T) {
	b := event.NewBus()
	hits := 0
	h := func(int) { hits++ }
	unsub1 := event.Subscribe(b, evInt, h)
	unsub2 := event.Subscribe(b, evInt, h)

	unsub1()
	event.Publish(b, evInt, 1)
	if hits != 1 {
		t.Fatalf("退订应只移除自己那次注册: hits = %d", hits)
	}
	unsub2()
	event.Publish(b, evInt, 1)
	if hits != 1 {
		t.Fatalf("hits = %d", hits)
	}
}

func TestUnsubscribeIdempotent(t *testing.T) {
	b := event.NewBus()
	unsub := event.Subscribe(b, evInt, func(int) {})
	unsub()
	unsub() // 幂等空操作
}

func TestUnsubscribeDuringDispatchStillReceivesCurrentEvent(t *testing.T) {
	b := event.NewBus()
	hits := 0
	var unsub func()
	unsub = event.Subscribe(b, evInt, func(int) {
		hits++
		unsub() // 派发中退订自己
	})

	event.Publish(b, evInt, 1)
	if hits != 1 {
		t.Fatalf("快照语义：本次派发仍应送达, hits = %d", hits)
	}
	event.Publish(b, evInt, 1)
	if hits != 1 {
		t.Fatalf("下一次事件起不再收到, hits = %d", hits)
	}
}

func TestSubscribeDuringDispatchTakesEffectNextEvent(t *testing.T) {
	b := event.NewBus()
	var late []string
	event.Subscribe(b, evInt, func(int) {
		event.Subscribe(b, evInt, func(int) { late = append(late, "late") }) // 派发中新增
	})

	event.Publish(b, evInt, 1)
	if len(late) != 0 {
		t.Fatalf("派发中新增的订阅本次不生效: late = %v", late)
	}
	event.Publish(b, evInt, 1)
	if len(late) != 1 {
		t.Fatalf("下一次事件开始生效: late = %v", late)
	}
}

func TestHandlerPanicDoesNotStopDispatch(t *testing.T) {
	b := event.NewBus()
	hits := 0
	event.Subscribe(b, evInt, func(int) { panic("boom") })
	event.Subscribe(b, evInt, func(int) { hits++ })

	event.Publish(b, evInt, 1)
	if hits != 1 {
		t.Fatalf("panic 后后续处理器仍应执行: hits = %d", hits)
	}
	event.Publish(b, evInt, 1)
	if hits != 2 {
		t.Fatalf("崩溃的处理器下次仍被调用: hits = %d", hits)
	}
}

func TestConcurrentPublishAndChurn(t *testing.T) {
	b := event.NewBus()
	var hits atomic.Int64
	event.Subscribe(b, evInt, func(int) { hits.Add(1) })

	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				event.Publish(b, evInt, i)
			}
		}()
	}
	// 订阅/退订与发布并发，压 -race
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			unsub := event.Subscribe(b, evInt, func(int) {})
			unsub()
		}
	}()
	wg.Wait()

	if hits.Load() != 2000 {
		t.Fatalf("hits = %d, want 2000", hits.Load())
	}
}
