using SepCore.Gameplay.Player;
using Xunit;

namespace SepCore.Tests;

public sealed class PrimaryActionStateTests
{
    [Fact]
    public void PressDefaultsToAttackWithoutExplicitInteractionPermission()
    {
        var action = new PrimaryActionState();
        Assert.False(action.IsInteractionMode(false));
        action.SetHeld(true, false);
        Assert.True(action.AttackHeld);
        Assert.False(action.ConsumeInteraction());
    }

    [Fact]
    public void AttackPressDoesNotBecomeInteractionWhenPermissionAppears()
    {
        var action = new PrimaryActionState();
        action.SetHeld(true, false);
        action.SetHeld(true, true);
        Assert.True(action.AttackHeld);
        Assert.False(action.IsInteractionMode(true));
        Assert.False(action.ConsumeInteraction());
    }

    [Fact]
    public void ReleaseThenPressWithPermissionRequestsInteractionOnlyOnce()
    {
        var action = new PrimaryActionState();
        action.SetHeld(true, false);
        action.SetHeld(false, true);
        action.SetHeld(true, true);
        Assert.False(action.AttackHeld);
        Assert.True(action.IsInteractionMode(true));
        Assert.True(action.ConsumeInteraction());
        action.SetHeld(true, true);
        Assert.False(action.ConsumeInteraction());
    }

    [Fact]
    public void InteractionPressDoesNotBecomeAttackWhenPermissionDisappears()
    {
        var action = new PrimaryActionState();
        action.SetHeld(true, true);
        action.SetHeld(true, false);
        Assert.True(action.IsInteractionMode(false));
        Assert.False(action.AttackHeld);
        Assert.True(action.ConsumeInteraction());
        Assert.False(action.ConsumeInteraction());
        action.SetHeld(false, false);
        Assert.False(action.IsInteractionMode(false));
        action.SetHeld(true, false);
        Assert.True(action.AttackHeld);
    }

    [Fact]
    public void ShortInteractionPressSurvivesUntilSimulationConsumesIt()
    {
        var action = new PrimaryActionState();
        action.SetHeld(true, true);
        action.SetHeld(false, false);
        Assert.False(action.AttackHeld);
        Assert.True(action.ConsumeInteraction());
        Assert.False(action.ConsumeInteraction());
    }

    [Theory]
    [InlineData(false)]
    [InlineData(true)]
    public void ResetClearsHeldActionAndPendingInteraction(bool permission)
    {
        var action = new PrimaryActionState();
        action.SetHeld(true, permission);
        action.Reset();
        Assert.False(action.AttackHeld);
        Assert.False(action.IsInteractionMode(false));
        Assert.False(action.ConsumeInteraction());
        action.SetHeld(true, false);
        Assert.True(action.AttackHeld);
    }
}
