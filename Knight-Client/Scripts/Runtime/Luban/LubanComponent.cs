#nullable enable
using System;
using Godot;
using SepCore.Definition;
using SepCore.Runtime.Component;

namespace SepCore.Runtime.Luban;

/// <summary>
/// 数据表组件(全局组件):负责 Luban 数据表初始化。
/// 读取 DataTable/*.tres 的 <c>metadata/bin</c> 载荷(json2tres 嵌入的 Luban bin,
/// base64)直接喂给生成的 <see cref="Tables"/> 构造函数 —— 类型与字段顺序由生成代码
/// 保证,客户端零反射。JSON 行(同一 tres 的 metadata/data)仅供编辑器/工具直读。
/// 由启动流水线的 LoadTablesState 在合适时机调用 <see cref="LoadTables"/>。
/// </summary>
public partial class LubanComponent : GlobalComponent
{
    public static LubanComponent? Instance { get; private set; }

    /// <summary>已装配的数据表聚合;未加载时为 null。</summary>
    public Tables? Tables { get; private set; }

    /// <summary>数据表是否已加载。</summary>
    public bool IsLoaded => Tables != null;

    public override void _Ready()
    {
        Instance = this;
    }

    public override void _ExitTree()
    {
        if (Instance == this)
            Instance = null;
    }

    /// <summary>加载全部数据表(幂等:已加载时直接返回)。缺表/缺 bin 载荷都会抛异常。</summary>
    public void LoadTables()
    {
        if (Tables != null)
            return;

        Tables = new Tables(LoadTableBin);
        GD.Print($"[Luban] 数据表加载完成:{typeof(Tables).GetProperties().Length} 张表");
    }

    /// <summary>读单张表 tres 的 metadata/bin(base64)并解码为 ByteBuf。</summary>
    private static ByteBuf LoadTableBin(string tableName)
    {
        var path = $"res://DataTable/{tableName}.tres";
        var resource = ResourceLoader.Load<Resource>(path)
            ?? throw new InvalidOperationException($"数据表缺失:{path}");
        if (!resource.HasMeta("bin"))
            throw new InvalidOperationException($"数据表缺少 metadata/bin 载荷,需重跑 gen_cli 流水线:{path}");

        var binBase64 = resource.GetMeta("bin").AsString();
        try
        {
            return new ByteBuf(Convert.FromBase64String(binBase64));
        }
        catch (FormatException e)
        {
            throw new InvalidOperationException($"数据表 bin 载荷 base64 解码失败:{path}", e);
        }
    }
}
