# Knight-Server

Knight 多人合作 PvE 地牢射击游戏的服务端（Go + MySQL，Docker 部署）。

需求源：`Docs/SRS-需求规格说明书.md`；协议契约唯一源：`Knight-Proto/knight.proto`；网络协议规范：`Docs/protocol.md`。

## 当前状态

已实现：

- **HTTP 账号接口**（protocol.md §3）：注册（409 重名）/ 登录换 token（bcrypt 加盐哈希，
  token 内存态 24h）/ 用户信息 / 历史战绩，查询接口 Bearer 鉴权
- MySQL：users / matches / match_players 读写（战绩写入接口随结算落地）
- **KCP 游戏通道（37777/UDP）**：客户端 kcp2k（V1.41）线上协议的 Go 服务端实现
  （`internal/kcp2k`——KCP 核心 + channel/cookie 封帧 + Hello 握手 + Ping/Pong 保活 + 超时/死链/拥塞断开）
- **连接生命周期**（`internal/session`）：应用层握手（版本/真实 token/重复登录/满员校验链）、
  心跳超时踢出、uid 绑定、错误应答（协议 §4）
- **房间大厅**（`internal/lobby`，消息域 32~63）：创建/按号加入/退出（房主继任/空房销毁）/
  准备/房主开局（人数校验）→ GameStart{[1,2,3]}，成员变化全量广播 RoomStatePush
- Envelope 编解码与 msg_id/payload 一致性校验（`internal/protocol`，与客户端 EnvelopeCodec 对称）
- Docker / docker-compose 部署

未实现（后续迭代）：

- 30Hz 权威战斗模拟（消息域 64~127：房间切换/快照/输入/子弹/AI/Boss/结算）
  ——当前收到这些消息按 `ERR_INVALID_REQUEST` 应答（不断开）

## 本地运行

依赖：Go ≥ 1.24、MySQL 8（或直接用 compose 里的 mysql 服务）。

```bash
# 1. 起数据库
docker compose up -d mysql

# 2. 起服务端（HTTP 37780 + KCP 37777/udp）
cd Knight-Server
go run ./cmd/knight-server

# 3. 验证
curl http://127.0.0.1:37780/healthz
# => {"status":"ok","db":"up"}

# 4. 注册 + 登录拿 token，客户端用 token 完成握手
curl -X POST http://127.0.0.1:37780/api/v1/register \
  -H 'Content-Type: application/json' -d '{"name":"alice","password":"pass-123"}'
curl -X POST http://127.0.0.1:37780/api/v1/login \
  -H 'Content-Type: application/json' -d '{"name":"alice","password":"pass-123"}'
# => {"uid":"146292...雪花ID字符串","name":"alice","token":"..."}
```

## Docker 一键部署

```bash
cd Knight-Server
docker compose up --build -d
curl http://127.0.0.1:37780/healthz
```

## 配置项（环境变量）

| 变量 | 默认值 | 说明 |
|---|---|---|
| `KNIGHT_HTTP_PORT` | `37780` | HTTP 监听端口 |
| `KNIGHT_KCP_PORT` | `37777` | KCP 游戏通道端口（UDP） |
| `KNIGHT_PROTO_VERSION` | `3` | 握手版本校验值（随 knight.proto PROTO_VERSION 同步） |
| `KNIGHT_SESSION_LIMIT` | `256` | 同时在线会话上限 |
| `KNIGHT_HEARTBEAT_TIMEOUT` | `10s` | 无任何 Envelope 的踢出时限 |
| `KNIGHT_DEV_TOKEN_BYPASS` | `0` | `1` = 接受任意非空 token（仅登录链路不可用时的临时调试） |
| `KNIGHT_TOKEN_TTL` | `24h` | 登录令牌有效期（内存态） |
| `KNIGHT_MAX_PLAYERS` | `4` | 单房间人数上限 |
| `KNIGHT_MIN_PLAYERS_TO_START` | `2` | 开局最少人数（开发期可配 1） |
| `KNIGHT_MAX_ROOMS` | `64` | 同时存在房间数上限 |
| `KNIGHT_MYSQL_DSN` | 空 | 完整 DSN，设置后优先于以下分项 |
| `KNIGHT_MYSQL_HOST` | `127.0.0.1` | MySQL 主机 |
| `KNIGHT_MYSQL_PORT` | `3306` | MySQL 端口 |
| `KNIGHT_MYSQL_USER` | `knight` | 用户名 |
| `KNIGHT_MYSQL_PASSWORD` | `knight` | 密码 |
| `KNIGHT_MYSQL_DB` | `knight` | 库名 |

## 测试

```bash
go test ./...

# MySQL 集成测试（需真实 MySQL）
KNIGHT_TEST_MYSQL_DSN="knight:knight@tcp(127.0.0.1:3306)/knight?parseTime=true" go test ./internal/store/

# 真实客户端互通测试（Knight-Client.Tests，需服务端在跑）：
#   KNIGHT_INTEROP_TARGET=127.0.0.1:37777 dotnet test ../Knight-Client.Tests --filter ServerInteropTests
# 覆盖：真实 token 握手 + 7s 保活；注册→登录→双客户端建房/加入/准备/开局全流程
```

## 目录结构

```
cmd/knight-server/     # 服务端入口（HTTP + KCP 双监听）
internal/kcp2k/        # kcp2k 线上协议 Go 实现（KCP 核心/封帧/peer/server/client）
internal/protocol/     # Envelope 编解码与契约校验
internal/session/      # 连接生命周期：握手/心跳/错误应答/token 校验/业务路由
internal/lobby/        # 房间大厅（创建/加入/退出/准备/开局）
internal/account/      # 账号业务（注册/登录/查询）
internal/auth/         # 登录令牌（内存态）+ bcrypt 密码哈希
internal/config/       # 环境变量配置
internal/store/        # MySQL 接入与迁移
internal/httpapi/      # HTTP 接口（healthz + 账号四接口）
protogen/              # protoc 生成代码（勿手改，Knight-Tools/gen-proto.ps1 生成）
```
