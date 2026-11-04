# AGENTS.md

## 项目概览

- 多人在线合作 PvE 地牢射击游戏（类《元气骑士》），目标平台 Android，最多 4 人联机。
- 客户端 Unity；服务端 Go；通信 Protobuf + KCP/UDP，固定 Tick 服务端权威模拟。
- 需求以 `Docs/SRS-需求规格说明书.md` 为准。

## Agent skills

### Issue tracker

Issue 与规格以本地 Markdown 形式存放于 `.scratch/`。见 `docs/agents/issue-tracker.md`。

### Domain docs

单上下文：仓库根 `GLOSSARY.md` + `docs/adr/`。见 `docs/agents/domain.md`。
