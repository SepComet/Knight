# UI 结构与主题规范

## MVC

脚本位于 `Scripts/UI/`，命名空间 `SepCore.UI.*`，沿用运行时的 UIForm、UIController 和 UIComponent。

- View：按唯一名查找节点、渲染模型、发出用户操作意图，不访问网络和游戏业务。
- Model：保留 UI 状态和不可变数据快照，包含本地校验及可用状态的计算；不保存密码。
- Controller：订阅 View 意图，更新 Model 并刷新 View；重复打开时刷新已有实例，避免重复订阅。
- UiApplication：在数据表加载后注册控制器、连接页面导航。Splash 经 Launch → Loading → ChangeScene 切到 Menu 场景后，由 ProcedureLobby 首次装配 UiApplication 并打开主菜单。控制器跨流程复用，重入大厅不会重复注册；退出大厅统一关闭界面。Menu 仅为场景容器，界面由 UIComponent 管理。

| 场景 | UIFormType / ID | 分组 / 深度 | 视图 / 控制器 |
| --- | --- | --- | --- |
| MainMenu | LobbyForm / 100 | Default / 0 | MainMenuView / MainMenuController |
| LoginForm | LoginForm / 116 | Default / 0 | LoginView / LoginController |
| RoomSelection | RoomSelection / 117 | Default / 0 | RoomSelectionView / RoomSelectionController |
| WaitingRoom | WaitingRoom / 118 | Default / 0 | WaitingRoomView / WaitingRoomController |
| GameHUD | RoundHUDForm / 105 | Battle / 20 | GameHudView / GameHudController |
| TouchControls | JoystickForm / 104 | Input / 30 | TouchControlsView / TouchControlsController |
| GameResult | RoundSettlementForm / 107 | Overlay / 90 | GameResultView / GameResultController |

均为单实例。配置源为 `Knight-Design/Datas/UIFormConfig_界面配置.xlsx`，枚举源为 `__enums__.xlsx` 的 UI 页；修改后运行 `Knight-Design/gen_cli.bat`，不要手改生成代码和数据。

RoomPasswordDialog 是 RoomSelection 的子视图，由同一个控制器管理，不独立登记 UIForm ID。三个条目场景 RoomListItem、WaitingPlayerItem、PlayerResultItem 同样是局部视图。

## 内部通信与当前边界

- 主菜单开始 → 房间选择；返回 → 主菜单；等待房间离开 → 房间选择；结算返回 → 等待房间或主菜单。页面导航会关闭旧界面。
- 主菜单左上角账号按钮管理登录与退出登录。进入大厅先读取 `user://login-session.json`，有匹配服务器的缓存则通过 KCP 握手恢复会话，否则弹出登录/注册对话框；失效令牌清理后重新登录。未登录或登录期间禁用开始游戏。
- 登录/注册切换、必填及密码确认校验已经连接。AuthSubmission 经 AccountSessionManager 调用现有 HTTP 注册/登录接口，注册成功后自动登录；KCP 握手确认身份后才视为登录成功。缓存仅包含用户标识、名称、令牌和服务器范围，不保存密码；退出登录清理缓存并断开连接。关闭对话框或离开大厅会取消未完成的认证，过期异步结果不会恢复旧会话。密码在切换、提交或关闭时清空。
- 房间列表由 RoomSelectionController.SetRooms 接收快照，默认无房间数据。单选后启用加入；满员或不可加入的条目禁用。刷新后移除失效选择；创建密码输入不受列表刷新影响，加入密码弹窗仅在目标失效或密码策略改变时关闭。
- 创建打开设置密码（可留空）；加密房间打开输入密码（不能为空）；公开房间直接发出加入意图。房间意图已接入 RoomSessionClient，经既有 NetworkClient 发送 CreateRoomReq/JoinRoomReq。弹窗内键盘焦点闭环，关闭/取消清空密码并恢复先前焦点，不发送请求。收到包含本人的 RoomStatePush 后更新等待房间并跳转，发送本身不算成功。等待期间禁用重复操作；错误、断线和超时恢复可用状态并提示；未连接时提示先完成登录和连接。
- 等待房间 SetPlayers 填充最多四人快照。准备/取消仅更新本地玩家 UI 状态；房主且所有玩家准备时启用开始按钮。ReadyChanged、StartRequested 均不代表服务器确认或真实开局。
- HUD 的 SetModel 接受状态快照；生命、魔力、护盾作安全取值，关卡按三大关、每关五小关显示。
- 触屏控制器输出移动、攻击按住/释放、换武器和技能意图。移动触点与动作触点独立；隐藏、失焦、暂停、关闭或禁用时清空输入。结算打开时显式关闭 Input 组，防止跨层输入残留。
- 结算 SetResults 接收玩家武器和伤害快照，不计算战斗结果；结果动画尚未接入。
- 设置按钮只发出 SettingsRequested，尚无设置页面。退出按钮经 QuitRequested 交给 ProcedureLobby 调用 SceneTree.Quit；订阅仅在大厅流程期间生效，离开大厅时解除。

Model 和 Controller 持有独立快照；View 内文本输入是暂存值。框架同组覆盖会停用下层视图输入但保留背景，关闭上层后恢复。创建、加入、离开房间已接入现有 KCP 协议，成员信息以服务端 RoomStatePush 为准。准备/开局、战斗或存档业务尚未接入。房间列表已接入 GetRoomListReq/Resp（协议版本 5）：进入房间选择页、在选择页完成连接、点击刷新时拉取；按房间号排序并显示人数、状态和密码标记。最多四人按现有 SRS 映射，满员和非 Waiting 状态不允许加入。请求串行执行，刷新期间禁用重复操作；断线清空旧列表。

连接地址配置位于 `project.godot` 的 `[network]`：`http_base_url` 默认 `http://127.0.0.1:37780/`，`kcp_host` 默认 `127.0.0.1`，`kcp_port` 默认 `37777`。HTTP 请求超时 10 秒，游戏握手超时 15 秒。缓存绑定这组服务器地址，避免把其他环境的令牌发送到当前服务器。

多实例测试时，在 Godot 用户启动参数（`--` 之后）分别传入 `--instance-id=player1`、`--instance-id=player2` 等。每个实例读写 `user://login-sessions/<实例名>.json`，退出登录仅清除自身记录；重新使用同一实例名即可恢复该实例登录。实例名支持 1 至 64 位英文字母、数字、下划线和连字符，不区分大小写。不传参数仍使用原来的 `user://login-session.json`；不同客户端应使用不同实例名。

命令行示例：`Godot.exe --path Knight-Client -- --instance-id=player1`。从编辑器多开时，在“调试 → 自定义运行实例”的各实例启动参数中分别指定不同的实例名。

## 动态列表与节点引用

房间、等待玩家、结算玩家列表只保留一个默认隐藏模板。刷新时移除并释放旧实例，再复制模板；复制排除 Signals，避免复制已绑定的委托；克隆根节点关闭 UniqueNameInOwner，子场景内部字段保留独立的唯一名。

- 房间：`%RoomList` / `%RoomItemTemplate`；条目内 `%RoomCodeLabel`、`%PlayerCountLabel`、`%StatusLabel`、`%PasswordLabel`。
- 等待：`%Players` / `%PlayerTemplate`；条目内 `%PlayerNameLabel`、`%RoleLabel`、`%ReadyStatusLabel`。
- 结算：`%PlayerResults` / `%PlayerResultTemplate`；条目内 `%PlayerNameLabel`、`%WeaponsLabel`、`%DamageLabel`。

跨层级查找使用 `%UniqueName`。模板内部唯一名必须在所属条目中获取；不要从父界面查找另一场景内部的字段。

## 标题与背景

统一主题 KnightUI.tres，字体入口为素材包 `Ui/Font/UIFont.tres`，英文数字由 NormalFont.ttf 提供，中文由去除 ASCII 的 Fusion Pixel 派生字体回退提供。场景不单独覆盖字体资源。普通页边距左右 48、上下 32，常用间距 16。

| 类型 | 字号 | 阴影尺寸 | X / Y 偏移 | 阴影色 |
| --- | --- | --- | --- | --- |
| PrimaryTitle | 36 | 4px | 0 / 2px | #000000，50% |
| SecondaryTitle | 28 | 2px | 0 / 1px | #000000，50% |
| BrandTitle | 64 | 继承一级 | 继承一级 | 继承一级 |

阴影尺寸使用 shadow_outline_size。BrandTitle 用于 Splash 与主菜单。保留编辑器中已调整的本地字体和尺寸参数。

常规矩形背景不做圆角。按钮使用素材包 Theme Wood 下的 button_normal、button_hover、button_pressed、button_disabled.png，不用纯色模拟，也不额外改色。圆形摇杆和操作按钮使用对应圆形素材。

## 按钮视觉

根 Button 只负责交互，原生五种背景均为空、不设置文字和图标。子 Panel 显示背景，直接子 Label 显示文字，Icon 显示图标，Focus 显示焦点边框；视觉节点全部忽略鼠标。ButtonVisual 仅同步原生按钮状态到子面板，不处理业务。

| 类型 | 背景阴影尺寸 | X / Y 偏移 | 颜色 |
| --- | --- | --- | --- |
| PrimaryButton | 4px | 0 / 2px | #000000，50% |
| SecondaryButton | 2px | 0 / 1px | #000000，50% |

Shadow 子面板在贴图后方绘制阴影。主菜单三个按钮使用同一 PrimaryButton；等待房间同排按钮及结算两个返回按钮使用相同层级。按钮尺寸沿用场景当前值，不由文本长度决定。

特殊标题可通过 ButtonVisual.CaptionName 指向直接子 Label，例如密码弹窗的 `%ConfirmLabel`。修改文案时操作子 Label，不使用 Button.Text。

## 验证

运行 `dotnet test Knight-Client.Tests/Knight-Client.Tests.csproj --no-restore` 可验证 UI 模型规则及既有测试。Godot 联动检查另覆盖实际场景加载、控制器注册、页面导航、重复打开、模板刷新、密码清理、准备状态与多点触控；静态检查和模型测试不能代替 Android 真机触控验收。

房间密码按服务端当前实现限制为最多 32 个 UTF-8 字节。网络握手版本同步为协议现有的 5；未修改 knight.proto。请求超过 10 秒未确认会断开当前连接，避免无请求 ID 的旧确认误用于重试。离开大厅时取消未完成请求；房间订阅随 UiApplication 销毁退订。


### 中英文字体分工

主题及项目字体统一引用 Ui/Font/UIFont.tres：NormalFont.ttf 为主字体（英文和数字），KnightCjk10-NoAscii.ttf 为中文回退。后者由 fusion-pixel-10px-monospaced-zh_hans.ttf 派生，去除所有 U+0000–U+007F 映射，保留其余字符及原版权/许可证信息；原始字体不修改。场景不单独覆盖到原始中英混合字体，避免英文和数字意外切换。

运行 `python Knight-Tools/subset-ui-font.py` 可重建派生字体（需要 fontTools）。派生字体采用独立家族名 Knight CJK 10。
