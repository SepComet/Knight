// Package event 实现服务端事件总线（Docs/server-event-pipeline.md）：
// 状态变化的发布订阅管线，与命令管线（session.Router）分工——
// 请求必须应答，事件 fire-and-forget、可多方订阅。
//
// 语义约定（对齐客户端 EventBus）：
//   - 同步派发：处理器运行在发布方 goroutine，发布顺序即派发顺序，不做优先级；
//   - 派发前对处理器列表做快照：派发中退订（含自己）不影响本次派发，
//     被退订者仍收到本次事件；派发中新增的订阅从下一次事件开始生效；
//   - 退订幂等；无人订阅的 Publish 为空操作。
//
// 与客户端的差异：payload 经 ID[T] 编译期绑定（运行期不存在签名不匹配）；
// Go 方法定义不能带类型参数，订阅/发布为包级泛型函数；Go 函数值不可比较，
// 退订改为 Subscribe 返回的闭包（精确移除自己那次注册）。
//
// 并发：内部互斥锁只保护订阅表，派发在锁外执行，任意 goroutine 发布均安全
//（跨源发布不保证全局顺序，仅单源内保序）。处理器 panic 被逐个 recover
// 并记录日志，不影响后续处理器与发布方——事件管线解耦次要消费方
//（战绩/成就等），单个订阅者的缺陷不应拖垮发布方 goroutine（gate 主循环）；
// 进程级 fail-fast 边界不变（kcp2k 回调链路本身无 recover）。
package event

import (
	"log/slog"
	"runtime/debug"
	"sync"
)

// ID 声明一个事件及其 payload 契约。T 为 payload 类型（无 payload 用 struct{}）；
// 发布/订阅时 T 由 ID 推断，payload 类型不匹配无法通过编译。
// 每次 Declare 产生独立事件（同名不合并——name 仅用于日志）。
type ID[T any] struct {
	name string
}

// Declare 声明事件，用于包级 var（集中契约见 events.go）。
func Declare[T any](name string) *ID[T] { return &ID[T]{name: name} }

// Name 事件名（日志/诊断用）。
func (id *ID[T]) Name() string { return id.name }

// subscription 一次注册的句柄；退订闭包按指针精确移除。
type subscription struct {
	fn func(arg any)
}

// Bus 事件总线。构造注入（cmd 装配一次），不用包级单例，测试各建各的。
type Bus struct {
	mu       sync.Mutex
	handlers map[any][]*subscription // *ID[T] → 订阅列表（订阅顺序即派发顺序）
}

// NewBus 构造事件总线。
func NewBus() *Bus {
	return &Bus{handlers: make(map[any][]*subscription)}
}

// Subscribe 订阅事件。handler 按订阅顺序同步调用，重复订阅会收到多次。
// 返回退订闭包：幂等，仅移除本次注册（每次订阅精确对应一次退订）。
func Subscribe[T any](b *Bus, id *ID[T], handler func(T)) (unsubscribe func()) {
	if b == nil || id == nil || handler == nil {
		panic("event: Subscribe 参数不可为空")
	}
	sub := &subscription{fn: func(arg any) { handler(arg.(T)) }}
	b.mu.Lock()
	b.handlers[id] = append(b.handlers[id], sub)
	b.mu.Unlock()
	return func() { b.remove(id, sub) }
}

// Publish 发布事件：同步逐个调用订阅者（订阅顺序），无人订阅为空操作。
func Publish[T any](b *Bus, id *ID[T], arg T) {
	if b == nil || id == nil {
		panic("event: Publish 参数不可为空")
	}
	b.mu.Lock()
	list := b.handlers[id]
	if len(list) == 0 {
		b.mu.Unlock()
		return
	}
	// 快照：派发中的退订/订阅不影响本次枚举
	snapshot := make([]*subscription, len(list))
	copy(snapshot, list)
	b.mu.Unlock()

	for _, sub := range snapshot {
		dispatch(id, sub, arg)
	}
}

// remove 移除一次订阅；幂等。列表清空时删除键，避免长期运行下字典膨胀。
func (b *Bus) remove(id any, sub *subscription) {
	b.mu.Lock()
	defer b.mu.Unlock()
	list := b.handlers[id]
	for i, s := range list {
		if s == sub {
			list = append(list[:i], list[i+1:]...)
			break
		}
	}
	if len(list) == 0 {
		delete(b.handlers, id)
		return
	}
	b.handlers[id] = list
}

// dispatch 调用单个处理器，逐处理器 recover（理由见包注释）。
func dispatch[T any](id *ID[T], sub *subscription, arg T) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("event handler panic", "event", id.name, "panic", r, "stack", string(debug.Stack()))
		}
	}()
	sub.fn(arg)
}
