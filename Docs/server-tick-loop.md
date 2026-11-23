# 服务端固定步长模拟循环

游戏域（64~127，30Hz 权威模拟）的地基设施：一条独立于 gate goroutine 的固定步长时间轴。命令管线与事件管线解决"响应"与"通知"，模拟循环解决"持续演进"——对局状态按固定间隔推进，而不是随消息到达才变化。

## 定位

| 线程 | 职责 | 现状 |
| --- | --- | --- |
| gate goroutine | 传输、会话、命令分发、大厅 | 已有（kcp2k 单循环） |
| net/http goroutine | 账号/战绩 | 已有 |
| **模拟 goroutine（本设计）** | 对局状态按固定步长推进 | 新建 `internal/tick` |

## 帧率配置

- 环境变量 `KNIGHT_TICK_RATE`，**全局单值，不写死 30**；默认 30，合法范围 1~120（超出拒绝启动）。
- 步长 `dt = time.Second / rate`，由 Runner 统一计算并传给模拟逻辑。
- **时间语义：改帧率不改变游戏速度**——模拟逻辑拿到 dt，位移/冷却等一律 `速度 × dt`；测试中也可用非 30 的帧率验证逻辑本身。
- dt 固定不随实际执行时间伸缩（见"时间语义"）。

## 线程模型

**单个模拟 goroutine 轮询全部注册的模拟单元**（与 gate goroutine 拥有全部 peer 同构）：

- 全局事件顺序在模拟侧保持全序，跨对局无同步问题；4 人合作 PvE 的单局模拟开销很小，单循环足够。将来若 CPU 成为瓶颈，把单局拆成独立 goroutine 即可——Runner API 不变。
- **跨线程交互只有两条通道，均无锁**：
  - 进：`Add`/`Remove`/`Post` 经 ops channel 提交，模拟循环在 tick 边界串行消费（对齐 kcp2k 的 ops 模式，kcp2k/server.go:56）。游戏域后续的 InputCmd 走同一机制：game.Route（gate goroutine）→ 对局自有的输入队列 → 模拟线程在 tick 内消费。
  - 出：模拟逻辑内 `event.Publish`（总线线程安全）与 `kcp2k.Send`（ops 提交，天然线程安全）直接可用。
- 事件顺序注意：模拟源事件与 gate 源事件之间不保证全局顺序（事件管线文档已有此约定），消费方不得跨源假设先后。

## API（internal/tick）

```go
// Tickable 一个模拟单元（后续一局对局实现一个）。
type Tickable interface {
    Tick(dt time.Duration)
}

type Config struct {
    Rate int // 每秒 tick 数（KNIGHT_TICK_RATE），1~120
}

func NewRunner(cfg Config) (*Runner, error) // 校验 Rate

// 真实时钟模式：按 1/rate 周期步进，ctx 取消或 Stop 退出。
func (r *Runner) Run(ctx context.Context)
func (r *Runner) Stop()

// 跨线程提交（任意 goroutine 可调，tick 边界执行）：
func (r *Runner) Add(t Tickable)      // 注册模拟单元（下一 tick 起参与）
func (r *Runner) Remove(t Tickable)   // 注销（同一 tick 内不再调用）
func (r *Runner) Post(fn func())      // 一次性任务（输入投递等）

// 测试模式：手动步进，时钟为 tick 计数 × dt，不依赖真实时间（对齐 kcp2k 的 Step 模式）。
func (r *Runner) Step(steps int)
```

- 派发顺序 = 注册顺序，不做优先级；同一单元同一 tick 恰好一次 `Tick(dt)`。
- `Step` 同样先消费 ops，测试中 Add/Remove 与步进可交错编排。

## 时间语义

- **固定 dt，不追帧**：tick 迟到（系统负载、GC）时模拟时间落后于墙钟，不做补偿性连发。理由：追帧会改变"每 tick 计算量"，破坏确定性；单局模拟负载极小，落后只可能在极端故障时出现。
- **落后检测**：单次 tick 处理耗时超过 dt 时 `slog.Warn`（带耗时与注册单元数），作为未来负载问题的哨兵，不做其他动作。
- 模拟逻辑内部一律以 tick 计数与 dt 表达时间（冷却、计时器），禁止直接读墙钟——这是保证 `Step` 模式确定性测试的前提。

## 错误策略

与事件总线一致：**逐 Tickable recover + slog.Error（带单元标识与堆栈）**，继续推进其余单元。单个对局的缺陷不应拖垮全部对局的时间轴；进程级 fail-fast 边界不变（gate/模拟循环骨架本身无 recover）。

## 生命周期

- main.go 装配：`runner := tick.NewRunner(...)` → `go runner.Run(ctx)`；空闲时（无注册单元）开销仅为一根 ticker，可先行接线验证缝隙。
- 关停顺序与启动相反：先 `runner.Stop()` 再 `gate.Stop()`——对局可能还在经 gate 发包，传输层最后关。

## 改造点

1. `internal/config`：新增 `TickRate`（env `KNIGHT_TICK_RATE`，默认 30），校验 1~120，含测试。
2. 新建 `internal/tick`（Runner + 单测）。
3. `cmd/knight-server/main.go`：构造 Runner、启动与关停接线（当前无单元注册，纯缝隙验证）。
4. 游戏域落地时的用法（非本次范围）：`game.Route` 收 InputCmd → `Post`/对局输入队列；lobby 订阅 `GameStarted` → `Add(match)`；对局结束 → `Remove(match)` + 结算事件。

## 测试

- `internal/tick` 单测：Rate 校验边界；`Step` 模式下 dt 恒定、注册顺序派发、Add/Remove 生效边界（Add 后下一 tick 参与、Remove 当 tick 生效）、Post 在 tick 边界执行、panic 隔离；真实模式下 ctx 取消退出；落后告警触发（用注入的慢 Tickable）。
- config 测试：默认值、越界拒绝。

## 非目标

- 不做对局/房间概念：Runner 只认 Tickable，游戏语义归游戏域。
- 不做 catch-up 追帧、优先级、单元级时间预算。
- 不做跨机 tick 同步（单进程权威服务器，无此需求）。
