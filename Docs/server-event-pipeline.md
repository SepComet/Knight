# 服务端事件管线

服务端内部的状态变化通知管线（Go）。命令（请求→应答）与事件（状态变化→多方通知）分离，分工与客户端一致：客户端里消息走 `NetworkClient.Subscribe<T>` 类型化订阅，连接生命周期等状态变化经 `NetworkEventBridge` 汇入 `EventBus`；服务端里请求走 `session.Router` 命令管线，状态变化汇入 `internal/event` 事件总线。

## 定位与分工

| 管线 | 形态 | 语义 | 服务端承载 | 客户端对照 |
| --- | --- | --- | --- | --- |
| 命令管线 | 点对点 | 请求必须应答（成功数据或 Error） | `session.Router` 接口 → 各业务域 `Route()` | `NetworkClient.Subscribe<RoomStatePush>` |
| 事件管线 | 发布订阅 | 通知型、fire-and-forget、可多方订阅 | `internal/event.Bus` | `EventBus` + `NetworkEventBridge` |

事件只对服务端内部消费方广播（战绩落库、日志、未来的成就/匹配），**不改变客户端推送**：客户端状态同步仍走 `RoomStatePush` 全量广播与 `GameStart` 通知，两条管线互不干扰。

## 包与 API

新建 `Knight-Server/internal/event`（叶子包，不依赖其他内部包）。

```go
// ID 声明一个事件；T 为 payload 类型，编译期绑定，发布/订阅时 T 不匹配无法通过编译。
// 无 payload 的事件用 event.ID[struct{}]。
type ID[T any] struct {
    name string // 日志/诊断用，形如 "session.ready"
}

// 声明事件（集中在本包，契约文件角色，对齐客户端 GameEventType.cs）：
var (
    SessionReady  = Declare[SessionReadyArgs]("session.ready")
    SessionEnd    = Declare[SessionEndArgs]("session.end")
    RoomCreated   = Declare[RoomCreatedArgs]("lobby.room_created")
    // ...
)

// Bus 事件总线。构造注入（cmd 装配一次），不用包级单例，测试各建各的。
func NewBus() *Bus

// 订阅：handler 按订阅顺序同步调用，重复订阅会收到多次。
// 返回退订闭包（见“与客户端的差异”），幂等。
func Subscribe[T any](b *Bus, id *ID[T], handler func(T)) (unsubscribe func())

// 发布：同步逐个调用订阅者；无人订阅为空操作。
func Publish[T any](b *Bus, id *ID[T], arg T)
```

payload 结构体与对应的 `ID` 一同集中声明在本包（如 `SessionReadyArgs{UID, Name, ConnID}`），作为订阅/发布双方的契约。

### 语义约定（移植自客户端 EventBus）

- 同步派发：处理器运行在发布方 goroutine，发布顺序即派发顺序，不做优先级。
- 派发前对处理器列表做快照：派发中退订（含自己）不影响本次派发，被退订者仍收到本次事件；派发中新增的订阅从下一次事件开始生效。
- 退订幂等；无人订阅的 Publish 为空操作。

### 与客户端的差异（Go 语言约束所致）

| 客户端 | 服务端 | 原因 |
| --- | --- | --- |
| 枚举键 + 派发时按签名转型，不匹配抛异常 | `ID[T]` 泛型键，编译期绑定 payload | Go 泛型免费提供编译期保证，运行期不再可能签名不匹配 |
| `Subscribe(type, Action<T>)` / `bus.Publish(type, arg)` 方法 | 包级泛型函数 `event.Subscribe(b, id, h)` | Go 方法定义不能带类型参数，泛型只能落在包级函数上 |
| `Unsubscribe(type, handler)` 按回调引用退订 | `Subscribe` 返回退订闭包 | Go 函数值不可比较，无法按引用查找回调；闭包退订反而更适合“作用域退订”（对局结束时一次 `unsub()`） |
| 退订移除全部重复项 | 每次退订只移除自己那次订阅 | 闭包天然精确对应一次注册 |

## 并发模型

- **发布纪律**：会话/大厅/游戏域事件全部在 kcp2k gate 主循环 goroutine 上发布（`OnConnect/OnData/OnDisconnect` 回调同源），处理器同步执行——与客户端“仅在主线程使用”的纪律同构。业务数据（房间表等）因此天然串行，处理器内无需再加锁。
- **跨 goroutine 发布**：Bus 内部互斥锁 + 快照派发，HTTP（账号域）等 goroutine 发布同样安全。锁只保护订阅表，不在持锁状态下调用处理器。跨源发布不保证全局顺序，仅单源内保序。
- **处理器约束**：禁止阻塞（会拖住 gate 主循环）。重活（如战绩写 MySQL）在处理器内转交独立 goroutine 或 worker，不内联等待。
- **panic 策略**：逐处理器 recover 并以 `slog.Error` 记录事件名与堆栈，继续派发后续处理器。理由：事件管线的存在意义是解耦次要消费方（战绩/成就），单个订阅者的 bug 不应拖垮 gate 主循环导致全会话断线。与现状一致的是 gate 回调本身仍无 recover，进程级 fail-fast 边界不变。

## 事件清单（v1）

会话域（`session.Manager` 发布）：

| 事件 | payload | 发布时机 |
| --- | --- | --- |
| `SessionReady` | `UID, Name, ConnID` | 握手成功、uid 绑定完成（回 HandshakeAck 之后） |
| `SessionEnd` | `UID, ConnID, Reason` | 连接清理时（唯一发布点 `OnKcpDisconnect`，超时踢出经 `gate.Disconnect` 汇入同一路径，不重复发布）。`UID=0` 表示未完成握手。`Reason`：`timeout` / `disconnect` / `rejected` |

大厅域（`lobby.Manager` 发布）：

| 事件 | payload | 发布时机 |
| --- | --- | --- |
| `RoomCreated` | `RoomID, OwnerUID` | 建房 |
| `RoomDestroyed` | `RoomID` | 房间清空销毁 |
| `MemberJoined` | `RoomID, UID, Name` | 加入成功 |
| `MemberLeft` | `RoomID, UID, Reason` | 退房。`Reason`：`leave`（主动）/ `disconnect`（会话结束） |
| `OwnerChanged` | `RoomID, FromUID, ToUID` | 房主继任 |
| `GameStarted` | `RoomID, MapIDs, PlayerUIDs` | 开局。后续战绩记录的订阅落点（`store/matches.go` 已有存储层） |

游戏域（64~127）事件随对局逻辑落地，按同一模式在 `internal/event` 追加声明。

## 改造点

1. `internal/session`：删除 `Callbacks` 结构体，构造函数改为接收 `*event.Bus`；`OnSessionReady`/`OnSessionEnd` 回调点改为 `event.Publish`。`SessionEnd` 增加当前缺失的 Reason（超时踢出、协议拒绝、断开三种路径在 `reject`/`OnKcpDisconnect` 处已知）。
2. `internal/lobby`：构造函数接收 `*event.Bus`，订阅 `SessionEnd` 触发退房（替代 `main.go` 的 `Callbacks{OnSessionEnd: lobbyMgr.OnSessionEnd}` 装配）；`leave()` 增加 reason 参数以区分主动退房与断线，在内部发布 `MemberLeft` 等房间事件。
3. `cmd/knight-server/main.go`：`bus := event.NewBus()`，注入 `session.NewManager` 与 `lobby.NewManager`，删除 `session.Callbacks` 装配代码。
4. 现有测试改造：`session` 测试中原以 `Callbacks` 断言的用例改为订阅总线观测事件；`session/lobby_test.go` 的会话结束联动用例改为订阅 `SessionEnd`。

`Router` 命令管线不动：消息仍按 payload 类型 switch 分发，请求的应答语义（数据或 Error）不适合 fire-and-forget 的事件形态。

## 测试

- `internal/event` 单测：类型化订阅/发布、重复订阅多次送达、派发中退订仍收到本次事件、派发中新增订阅下次生效、退订幂等、无人订阅空操作、处理器 panic 后后续处理器仍执行、`-race` 下双 goroutine 并发发布。
- `internal/session`、`internal/lobby`：构造真实 Bus 订阅观测，断言事件序列与 payload；不再需要回调打桩。

## 非目标

- 不做异步队列/worker 池：当前消息量与单 gate goroutine 模型下无必要，处理器同步串行最易推理。
- 不做跨进程事件（消息队列/持久化订阅）：战绩等持久化消费方在进程内订阅即可。
- 不提供优先级、有序分组、事件回放：客户端总线同样没有，出现真实需求再加。
