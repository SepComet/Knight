---
name: knight-swarm-delivery
description: >-
  Orchestrate end-to-end Knight milestone delivery using a parallel Swarm
  (Go server + Godot C# client), automated multi-instance E2E testing with
  screenshots via knight-dev MCP, an independent architecture review report,
  and automatic P0/P1 remediation with re-verification. Use when implementing
  a Knight milestone (M1-M6) or major cross-stack feature that needs Swarm
  development, live multi-client verification, and formal architecture review.
---

# Knight Swarm 端到端交付与评审工作流

适用于 Knight（Go 服务端 + Godot 4.7.2 .NET 客户端）的里程碑级或跨端功能开发，严格按 **Swarm 并行开发 → 联调验证与 Docker 部署 → `knight-dev` 多实例自动实测与截图 → 独立架构评审报告 → 自动修复 P0/P1 并复验 → 文档同步** 六阶段执行。

## 阶段一：Swarm 蜂群并行开发

1. 加载 `native_swarm` 与 `swarm_pane` 技能，调用 `swarm{action:"title"}` 命名任务，并添加 `monitor`（绝不省略）。
2. 按目录绝对隔离拆分作者 Worker，杜绝两人同时编辑同一文件：
   - **服务端作者**（如 `server-dev`）：仅负责 `Knight-Server/`（必要时含 `Knight-Tools/`）。
   - **客户端作者**（如 `client-dev`）：仅负责 `Knight-Client/` 与 `Knight-Client.Tests/`。
   - **跨端契约先行**：若涉及 `Knight-Proto/knight.proto`、地图几何、碰撞参数或配置 ID，要求服务端/客户端作者第一时间在消息板互相对齐后再写实现。
3. 在客户端 Brief 中显式强调 `AGENTS.md` 硬约束：
   - 命名空间一律以 `SepCore.*` 开头（禁止 `Knight.Client.*`）。
   - 跨层级 Godot 场景节点引用一律使用唯一名 `%Name`（禁止树路径）。
4. 写好 `.system_generated/swarm/plan.json` 供 Swarm UI 展示。

## 阶段二：单测、互通测试与 Docker 服务端部署

1. 双端作者报 `[done]` 后，派发只读 `verifier` 执行基础验证：
   - 服务端：`go test -race -count=1 ./...`（含 MySQL 集成测试）。
   - 客户端：`dotnet build Knight-Client/Knight-Client.csproj`（0 error / 0 warning）与 `dotnet test Knight-Client.Tests/Knight-Client.Tests.csproj`。
2. 确认 Docker 容器化服务端已重建为最新代码：
   - 优先调用 `knight-dev` MCP 的 `deploy` 工具（或执行 `cd Knight-Server && docker compose up --build -d`），确认 `GET http://127.0.0.1:37780/healthz` 返回 `{"status":"ok","db":"up"}`。
3. 针对真实 Docker 服务端跑通 `KNIGHT_INTEROP_TARGET=127.0.0.1:37777 dotnet test --filter ServerInteropTests`。

## 阶段三：`knight-dev` MCP 多实例实机自动测试与截图产出

1. 派发 `e2e-verifier` 使用 `knight-dev` MCP 多实例工具对真实 Godot `.NET` 窗口执行端到端自动化测试（全程零手工点击）：
   - **`clients_launch`**：拉起 2～4 个真实窗口（自动隔离 `--instance-id` 与 `--dev-agent-port`）。
   - **复用专用机器人账号**：默认使用已预注册的测试号 **`10001`、`10002`、`10003`、`10004`**（密码与账号一致），避免重复注册污染数据库；如需测试注册链路再临时生成带时间戳账号。
   - **`client_state` + `client_act`**：实时观察界面/流程/实体坐标，驱动登录 → 建房/进房 → 准备 → 开局 → 并发输入（`set_input` 支持后台失焦窗口）并断言核心行为（位移、碰撞、同步一致性、弱网/战斗指标等）。
   - **`client_screenshot`**：在关键验收节点为各窗口截图（保存 PNG）。
   - **`clients_stop`**：测试结束必须优雅关闭全部窗口，并确认无残留进程与端口占用。
2. **截图交付**：将关键验收截图拷贝到当前会话的 Artifact 目录（`<appDataDir>/brain/<conversation-id>/`），以便在最终报告或回复中直接展示。
3. 若 E2E 发现缺陷，直接 `@` 对应作者修复、重建 Docker/客户端并复测至全绿。

## 阶段四：独立架构评审与报告产出

1. 在所有代码实现、单元测试与多实例 E2E 均通过后，**单派一名独立只读 Agent `arch-reviewer`**（严禁修改源码）。
2. `arch-reviewer` 必须对照 `AGENTS.md`、`Docs/开发计划-毕设里程碑.md`、`Docs/protocol.md` 与 `Knight-Proto/knight.proto`，从以下 5 个维度给出带**文件路径与行号**的深度评审：
   - **服务端架构**：模块边界、依赖方向、并发模型/锁粒度/死锁与资源泄漏、Tick 性能与背压、面向后续里程碑的可扩展性。
   - **客户端架构**：纯 C# 逻辑层与 Godot 节点表现层解耦度、Procedure 生命周期与断线/切场景健壮性、后续里程碑接入友好度。
   - **双端契约与协议一致性**：数值/几何/坐标系对齐、协议边界条件、文档版本号同步。
   - **工程规范与测试质量**：`AGENTS.md` 合规性、单测/E2E 覆盖盲区。
   - **分级问题清单**：严格按 **P0（阻断） / P1（重要架构或健壮性隐患） / P2（优化与演进建议）** 分类，给出影响分析与具体修复建议。
3. 将完整中文评审报告写入当前会话 Artifact 目录：`<appDataDir>/brain/<conversation-id>/architecture-review-<topic>.md`。

## 阶段五：自动修复 P0/P1 并复验

1. 若评审报告中存在 **任意 P0 或 P1 项**，无需中断等待用户指令，直接按目录隔离派发修复 Worker（如 `server-p1`、`client-p1`）逐项修复，并为每项补充回归单元测试（P2 项保留在报告中供后续里程碑演进参考）。
2. 修复完成后派发只读 `p1-verifier` 执行全量复验：
   - 重跑服务端 `go test -race -count=1 ./...`、客户端 `dotnet build` / `dotnet test`；
   - 若服务端有改动，重新部署 Docker 服务端并重跑 `ServerInteropTests`；
   - 重跑 `knight-dev` MCP 多实例 E2E 实测并更新最终验收截图，最后 `clients_stop` 清理进程。
3. 在 `<appDataDir>/brain/<conversation-id>/architecture-review-<topic>.md` 末尾追加 **“P0/P1 修复与复验结论”** 章节，将完整评审报告（含可点击链接与最终验收截图）呈现给用户。

## 阶段六：文档同步与 Swarm 收尾

1. 更新 `Docs/开发计划-毕设里程碑.md` 勾选状态（及受影响的 `Docs/protocol.md`）。
2. 向 Swarm 消息板发一条不含 `@` 的 `[done] <摘要>` 让 `monitor` 静默，并 `kill` 已完成的工作者（保留 `monitor`）。
