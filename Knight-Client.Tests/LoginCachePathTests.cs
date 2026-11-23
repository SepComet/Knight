using System;
using SepCore.UI.Auth.Services;
using Xunit;

namespace SepCore.Tests;

public class LoginCachePathTests
{
    [Fact]
    public void DefaultKeepsExistingCache()
        => Assert.Equal("user://login-session.json", LoginCachePath.Resolve(Array.Empty<string>()));

    [Fact]
    public void InstancesHaveSeparatePersistentCaches()
    {
        var first = LoginCachePath.Resolve(new[] { "--instance-id=player1" });
        var second = LoginCachePath.Resolve(new[] { "--instance-id", "player2" });
        Assert.NotEqual(first, second);
        Assert.Equal("user://login-sessions/player1.json", first);
        Assert.Equal(first, LoginCachePath.Resolve(new[] { "--instance-id", "PLAYER1" }));
    }

    [Theory]
    [InlineData("--instance-id")]
    [InlineData("--instance-id=")]
    [InlineData("--instance-id=../other")]
    [InlineData("--instance-id=a/b")]
    public void InvalidInstanceCannotFallBackToSharedCache(string argument)
        => Assert.Throws<ArgumentException>(() => LoginCachePath.Resolve(new[] { argument }));
}
