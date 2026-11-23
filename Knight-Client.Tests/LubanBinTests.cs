#nullable enable
using System;
using SepCore.Definition;
using Xunit;

namespace SepCore.Tests;

// bin 载荷 → 生成代码:按 CharacterConfig schema 字段顺序手工打包一行,
// 锁定 ByteBuf 线格式与生成构造函数的读取顺序的对齐(三条流水线管线的核心咬合点)。
public class LubanBinTests
{
    private static ByteBuf BuildCharacterTableBin()
    {
        var buf = new ByteBuf();
        buf.WriteSize(1);      // 行数
        buf.WriteInt(1001);    // Id
        buf.WriteString("角色1");
        buf.WriteBool(true);   // InitiallyOwned
        buf.WriteInt(120);     // MaxHp
        buf.WriteInt(40);      // MaxMp
        buf.WriteInt(12);      // Speed
        buf.WriteInt(101);     // SkillActionId
        buf.WriteInt(0);       // WeaponItemId
        buf.WriteString("sprite1");
        return buf;
    }

    [Fact]
    public void TbCharacterConfig_ReadsBin_InSchemaOrder()
    {
        var table = new TbCharacterConfig(BuildCharacterTableBin());

        Assert.Single(table.DataList);
        var cfg = table.Get(1001);
        Assert.Equal("角色1", cfg.Name);
        Assert.True(cfg.InitiallyOwned);
        Assert.Equal(120, cfg.MaxHp);
        Assert.Equal(12, cfg.Speed);
        Assert.Same(cfg, table.DataList[0]);
        Assert.Null(table.GetOrDefault(9999));
    }

    [Fact]
    public void TbCharacterConfig_TruncatedBin_Throws()
    {
        var buf = new ByteBuf();
        buf.WriteSize(2); // 声称 2 行,实际 0 行数据

        Assert.ThrowsAny<Exception>(() => new TbCharacterConfig(buf));
    }
}
