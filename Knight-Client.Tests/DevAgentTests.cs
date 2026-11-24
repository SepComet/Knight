#nullable enable
using System;
using Godot;
using SepCore.DevAgent;
using SepCore.Gameplay.Player;
using Xunit;

namespace SepCore.Tests;

public sealed class DevAgentTests
{
    [Fact]
    public void Config_Parse_NullOrEmptyArgs_DisablesDevAgent()
    {
        var nullCfg = DevAgentConfig.Parse(null);
        Assert.False(nullCfg.IsEnabled);
        Assert.Equal(0, nullCfg.Port);
        Assert.Equal("", nullCfg.InstanceId);

        var emptyCfg = DevAgentConfig.Parse(Array.Empty<string>());
        Assert.False(emptyCfg.IsEnabled);
        Assert.Equal(0, emptyCfg.Port);
        Assert.Equal("", emptyCfg.InstanceId);
    }

    [Fact]
    public void Config_Parse_EqualsSyntax_ParsesPortAndInstanceId()
    {
        var cfg = DevAgentConfig.Parse(new[] { "--instance-id=p1", "--dev-agent-port=19100" });

        Assert.True(cfg.IsEnabled);
        Assert.Equal(19100, cfg.Port);
        Assert.Equal("p1", cfg.InstanceId);
    }

    [Fact]
    public void Config_Parse_SpaceSeparatedSyntax_ParsesPortAndInstanceId()
    {
        var cfg = DevAgentConfig.Parse(new[] { "--unrelated", "foo", "--dev-agent-port", "19101", "--instance-id", "p2" });

        Assert.True(cfg.IsEnabled);
        Assert.Equal(19101, cfg.Port);
        Assert.Equal("p2", cfg.InstanceId);
    }

    [Theory]
    [InlineData("1", 1)]
    [InlineData("65535", 65535)]
    public void Config_Parse_BoundaryValidPorts_Enabled(string rawPort, int expectedPort)
    {
        var cfg = DevAgentConfig.Parse(new[] { $"--dev-agent-port={rawPort}" });

        Assert.True(cfg.IsEnabled);
        Assert.Equal(expectedPort, cfg.Port);
    }

    [Theory]
    [InlineData("0")]
    [InlineData("-1")]
    [InlineData("65536")]
    [InlineData("abc")]
    [InlineData("")]
    public void Config_Parse_InvalidPortValues_Disabled(string rawPort)
    {
        var cfg = DevAgentConfig.Parse(new[] { $"--dev-agent-port={rawPort}", "--instance-id=p1" });

        Assert.False(cfg.IsEnabled);
        Assert.Equal(0, cfg.Port);
        Assert.Equal("p1", cfg.InstanceId);
    }

    [Fact]
    public void Config_Parse_MissingValueAfterFlag_Disabled()
    {
        var cfg = DevAgentConfig.Parse(new[] { "--dev-agent-port" });

        Assert.False(cfg.IsEnabled);
        Assert.Equal(0, cfg.Port);
    }

    [Fact]
    public void Config_Parse_RepeatedPortArg_LastValueWins()
    {
        var disabled = DevAgentConfig.Parse(new[] { "--dev-agent-port=19100", "--dev-agent-port=0" });
        Assert.False(disabled.IsEnabled);
        Assert.Equal(0, disabled.Port);

        var overridden = DevAgentConfig.Parse(new[] { "--dev-agent-port=0", "--dev-agent-port=19105" });
        Assert.True(overridden.IsEnabled);
        Assert.Equal(19105, overridden.Port);
    }

    [Fact]
    public void SyntheticInput_InactiveByDefault()
    {
        var input = new SyntheticInputOverride();

        Assert.False(input.IsActive);
        Assert.False(input.TrySample(1.25f, out _));
    }

    [Fact]
    public void SyntheticInput_SetAndSample_ProducesExpectedCommandAndConsumesInteractOnce()
    {
        var input = new SyntheticInputOverride();
        input.Set(new Vector2(3f, 4f), aimRadians: 0.75f, attackHeld: true, interact: true, durationSeconds: null);

        Assert.True(input.IsActive);
        Assert.Null(input.RemainingSeconds);

        Assert.True(input.TrySample(fallbackAimRadians: 2.0f, out var first));
        Assert.Equal(0.6f, first.Movement.X, 4);
        Assert.Equal(0.8f, first.Movement.Y, 4);
        Assert.Equal(0.75f, first.AimRadians, 4);
        Assert.True(first.AttackHeld);
        Assert.True(first.InteractPressed);

        // Second sample keeps movement/aim/attack, but interact pulse is consumed.
        Assert.True(input.TrySample(fallbackAimRadians: 2.0f, out var second));
        Assert.Equal(first.Movement, second.Movement);
        Assert.True(second.AttackHeld);
        Assert.False(second.InteractPressed);

        // Indefinite override survives Advance until Clear().
        input.Advance(10.0);
        Assert.True(input.IsActive);

        input.Clear();
        Assert.False(input.IsActive);
        Assert.False(input.TrySample(0f, out _));
    }

    [Fact]
    public void SyntheticInput_NullAim_UsesFallbackAimAndSanitizesNonFinite()
    {
        var input = new SyntheticInputOverride();
        input.Set(new Vector2(float.NaN, 0.5f), aimRadians: null, attackHeld: false, interact: false);

        Assert.True(input.TrySample(fallbackAimRadians: -1.5f, out var cmd));
        Assert.Equal(0f, cmd.Movement.X, 4);
        Assert.Equal(0.5f, cmd.Movement.Y, 4);
        Assert.Equal(-1.5f, cmd.AimRadians, 4);
    }

    [Fact]
    public void SyntheticInput_DurationTimer_ExpiresAndClearsAutomatically()
    {
        var input = new SyntheticInputOverride();
        input.Set(new Vector2(1f, 0f), durationSeconds: 0.5);

        Assert.True(input.IsActive);
        Assert.Equal(0.5, input.RemainingSeconds!.Value, 6);

        input.Advance(0.2);
        Assert.True(input.IsActive);
        Assert.Equal(0.3, input.RemainingSeconds!.Value, 6);
        Assert.True(input.TrySample(0f, out var cmd));
        Assert.Equal(new Vector2(1f, 0f), cmd.Movement);

        // Non-positive delta does not alter remaining time.
        input.Advance(0.0);
        input.Advance(-1.0);
        Assert.True(input.IsActive);
        Assert.Equal(0.3, input.RemainingSeconds!.Value, 6);

        // Reaching zero clears the override.
        input.Advance(0.3);
        Assert.False(input.IsActive);
        Assert.Null(input.RemainingSeconds);
        Assert.False(input.TrySample(0f, out _));
    }

    [Theory]
    [InlineData(0.0)]
    [InlineData(-0.5)]
    [InlineData(double.NaN)]
    [InlineData(double.PositiveInfinity)]
    public void SyntheticInput_NonPositiveOrNonFiniteDuration_TreatedAsIndefinite(double duration)
    {
        var input = new SyntheticInputOverride();
        input.Set(new Vector2(0f, 1f), durationSeconds: duration);

        Assert.True(input.IsActive);
        Assert.Null(input.RemainingSeconds);
        input.Advance(5.0);
        Assert.True(input.IsActive);
    }
}
