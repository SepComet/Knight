# AGENTS.md

## 项目概览

- 多人在线合作 PvE 地牢射击游戏（类《元气骑士》），目标平台 Android，最多 4 人联机。
- 客户端 Godot 4.7.2 (.NET/C#)，位于 `Knight-Client/`；服务端 Go，位于 `Knight-Server/`；通信 Protobuf + KCP。
- 需求以 `Docs/SRS-需求规格说明书.md` 为准；协议契约唯一源 `Knight-Proto/knight.proto`。

## 约束

改动与本文件冲突时，以本文件为准，或先更新本文件再动代码。

### 命名空间

- 一律以 `SepCore` 开头，**不接 Client**，直接接业务划分名：
  - 运行时框架：`SepCore.Runtime.*`（如 `SepCore.Runtime.Fsm`、`SepCore.Runtime.ObjectPool`、`SepCore.Runtime.Component`）
  - 流程：`SepCore.Procedure`
  - 生成代码（Luban 表 / 协议）：`SepCore.Definition`
  - 测试：`SepCore.Tests`
- 新建文件必须写显式命名空间；禁止出现 `Knight.Client.*` 命名空间（程序集名是 Knight-Client，但命名空间与程序集名无关）。
- 生成流水线（Luban / protoc）的 namespace 配置同样遵守本约束。

### 场景节点引用

- 代码里跨层级引用节点一律用**唯一名**（`%Name`，节点勾选 Access as Unique Name / `.tscn` 里 `unique_name_in_owner = true`）：
  - `GetNode<Label>("%Status")`
- 禁止写节点树路径（如 `Center/VBox/Status`），避免调整节点结构时引用大面积失效。
- 引用越多的节点越要设唯一名；兄弟层级内的直接 `GetNode("Child")` 可豁免，但一旦被跨层级引用必须改为唯一名。
