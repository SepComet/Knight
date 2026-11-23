# kcp2k（vendor 源码）

- 来源：https://github.com/MirrorNetworking/kcp2k
- 版本：V1.41，commit `66efda6686f649838d42f078fbaabf56ac449de4`（2024-10-12）
- 许可：MIT（见同目录 LICENSE）
- 命名空间：保留上游 `kcp2k`（同 `Scripts/ThirdParty/Luban` 的 vendor 惯例）

## 目录

- `highlevel/`：KcpClient / KcpServer / KcpPeer / KcpConfig 等传输层封装
  （自带 bare-ping 握手 + cookie 防伪造 + 死链/超时检测，全部帧驱动单线程）
- `kcp/`：原始 KCP 协议的纯 C# 移植（Kcp.cs 等）

## 相对上游的改动

- 移除 `kcp/AssemblyInfo.cs`（`InternalsVisibleTo("kcp2k.Tests")`，仅上游测试需要）。
- 其余文件原样拷贝，无代码改动。

## 线协议注意

kcp2k 不是"裸 KCP"：每个 UDP 包 = 1 字节通道 + 4 字节 cookie + 载荷，
载荷再按 KcpHeader（Hello/Ping/Pong/Data/Disconnect）区分。
**Go 服务端（xtaci/kcp-go）直接对接时必须实现同样的帧格式**，
见 Docs/protocol.md §12 待定项 #2。
