using Google.Protobuf;
using SepCore.Definition;
using Xunit;

namespace SepCore.Tests;

// 生成代码可访问性冒烟：保证 Knight-Client 程序集能正常引用并编译生成的 proto 类型
public class ProtoGeneratedSmokeTests
{
    [Fact]
    public void GeneratedMsgId_IsAccessible()
    {
        Assert.True((int)MsgId.MsgHandshakeReq > 0);
    }

    [Fact]
    public void GeneratedHandshakeReq_RoundTripsThroughProtobuf()
    {
        var req = new HandshakeReq { ProtoVersion = 2, Token = "smoke" };
        var parsed = HandshakeReq.Parser.ParseFrom(req.ToByteArray());

        Assert.Equal((uint)2, parsed.ProtoVersion);
        Assert.Equal("smoke", parsed.Token);
    }
}
