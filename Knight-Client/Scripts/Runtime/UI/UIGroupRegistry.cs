#nullable enable
using System.Collections.Generic;

namespace SepCore.Runtime.UI;

/// <summary>
/// 界面组登记表:UIGroupName → CanvasLayer 深度(数值越大越靠上)。
/// UIFormConfig.UIGroupName 必须在此登记,未登记的组在打开时报错;
/// 新增界面组时在此加一行。
/// </summary>
public static class UIGroupRegistry
{
    public static IReadOnlyDictionary<string, int> Depths { get; } = new Dictionary<string, int>
    {
        ["Default"] = 0,        // 常规界面(大厅/设置/关于)
        ["Backpack"] = 10,      // 背包类
        ["Battle"] = 20,        // 战斗界面
        ["Input"] = 30,         // 输入层(虚拟摇杆等)
        ["NetworkStatus"] = 40, // 网络状态提示(常显)
        ["Overlay"] = 90,       // 顶层覆盖(转场/拖拽道具)
    };
}
