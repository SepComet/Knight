# 域级复合 Router 与 uid 定向发送

游戏域（64~127）接入前的两个小接缝改造。协议事实（knight.proto MsgId 分配表 / protocol.md §5.1）：1~31 连接会话（session 层自处理）、32~63 大厅、64~95 游戏事件、96~127 高频。

## 一、域级复合 Router

### 现状

`session.Router` 单槽位；lobby.Route 的 `default` 分支硬编码"64~127 下一轮实现"。游戏域落地时将被迫改 lobby 的 switch 或再改 session 的装配。

### 设计

新增 `internal/session/router.go`（Router 接口所在包，不引新包）：

```go
// DomainRoute 一个消息域段到业务 Router 的映射。
type DomainRoute struct {
    Min, Max uint32 // 消息号域段（含端点），对应 knight.proto MsgId 分配表
    Router   Router
}

// NewDomainRouter 按 Envelope.MsgId 的域段分发；无匹配域段时按
// ERR_INVALID_REQUEST "not implemented yet" 应答（不断开，对齐原 no-router 行为）。
// sender 仅用于兜底错误应答（kcp2k.Server 满足；接口形状便于单测打桩）。
func NewDomainRouter(sender Sender, routes ...DomainRoute) Router
```

- 域段线性匹配（条目个位数，无需索引）；`uint32(env.MsgId)` 参与比较。
- **域内未知消息仍由各业务 Router 的 default 分支兜底**（如未来在 32~63 段新增、lobby 未实现的消息）——两层兜底各管各的：域外归 DomainRouter，域内归业务。
- 兜底错误走 `Sender` 小接口（`Send(connID, data, channel)`，与 lobby.Sender 同形），不依赖具体 *kcp2k.Server，单测可用假 sender。
- 0~31 域不注册条目：握手/心跳/错误由 session 层自处理，到得了 Router 的 1~31 消息（异常路径）按未知域段应答错误。

### 装配（main.go）

```go
mgr.SetRouter(session.NewDomainRouter(gate,
    session.DomainRoute{Min: 32, Max: 63, Router: lobbyMgr},
    // 64~127 游戏域随游戏域落地在此追加
))
```

游戏域接入只加一行条目，lobby 与 session 不再改动。

## 二、uid 定向发送

### 现状

uid→connID 映射在 session.Manager（`byUID`），但 lobby 自行在 Member 里存 ConnID 并直接按 connID 发送。游戏域广播 EntityState/Snapshot 时会复制同样的模式，传输层连接标识泄漏进业务层。

### 设计

`session.Manager` 暴露定向发送（复用自己的 byUID 表，不建重复注册表）：

```go
// SendToUID 向 uid 的在线会话发送数据，返回 false = uid 不在线。
// 任意 goroutine 可调（session.mu 仅保护查表，发送经 gate ops）。
func (m *Manager) SendToUID(uid uint64, data []byte, channel kcp2k.Channel) bool
```

消费方（lobby）定义所需的小接口（Go 惯例：接口定义在使用方）：

```go
// UIDSender 按 uid 定向发送（session.Manager 满足）。广播走 uid，
// 消费方不感知 connID。
type UIDSender interface {
    SendToUID(uid uint64, data []byte, channel kcp2k.Channel) bool
}
```

### lobby 改造

- `Member` 删除 `ConnID` 字段；`broadcast` 与开局 GameStart 下发改走 `SendToUID(mem.UID, ...)`。
- **请求应答仍按 connID**（`replyError`，经既有 `Sender`）：应答在语义上指向"发起请求的那条连接"而非"这个用户"，两个接口分工——用户域广播走 `UIDSender`，连接域应答走 `Sender`。
- 同 uid 重复登录被 session 拒绝（无重连替换），uid 在线期间 connID 不变；成员断线由 SessionEnd 事件先于任何广播从房间移除（同一 gate goroutine 串行），`SendToUID` 的 false 返回仅为防御。

### 死锁核对

广播持 `lobby.mu` → `SendToUID` 取 `session.mu`，嵌套方向恒为 lobby→session；session 侧任何持 `session.mu` 路径都不回调 lobby（SessionEnd 发布在释放锁之后）。单向嵌套，无死锁。

## 改造点

1. `internal/session/router.go`：DomainRouter + 假 sender 单测（域段分发、未知域段兜底应答）。
2. `internal/session/manager.go`：`SendToUID` + 集成测试（在线送达、离线返回 false）。
3. `internal/lobby/manager.go`：`Member` 删 ConnID、广播改 SendToUID、构造函数加 `UIDSender` 参数。
4. `cmd/knight-server/main.go`：装配顺序调整——session.Manager 先于 lobby 构造（lobby 需要 mgr 作 UIDSender；原 lobby 在前是因 `Callbacks` 装配，事件管线后已无此依赖），Router 改为 DomainRouter。
5. 测试 rig 同步调整（session 两个 rig、lobby rig 改走 DomainRouter 装配，全栈验证）。

## 非目标

- 不做优先级路由、中间件链、按消息号粒度注册（域段足够）。
- 不建独立的 presence/在线表服务：session.byUID 即权威在线表，事件驱动的重复注册表是过度设计。
