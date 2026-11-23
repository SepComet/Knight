using SepCore.UI.Battle;
using Xunit;

namespace SepCore.Tests;

public sealed class BattleUiModelTests
{
    [Theory]
    [InlineData(-10, 100, 0, 100)]
    [InlineData(200, 100, 100, 100)]
    [InlineData(40, 0, 0, 0)]
    [InlineData(40, -5, 0, 0)]
    [InlineData(double.NaN, 100, 0, 100)]
    [InlineData(40, double.PositiveInfinity, 0, 0)]
    public void ResourceDisplayNeverShowsInvalidOrOutOfRangeValues(
        double current, double maximum, double expectedCurrent, double expectedMaximum)
    {
        var resource = new ResourceValue(current, maximum);
        Assert.Equal(expectedCurrent, resource.SafeCurrent);
        Assert.Equal(expectedMaximum, resource.SafeMaximum);
    }

    [Theory]
    [InlineData(1, 3, "1-3")]
    [InlineData(3, 5, "3-5")]
    [InlineData(-1, 0, "1-1")]
    [InlineData(10, 10, "3-5")]
    public void StageTextFollowsThreeByFiveStageContract(int major, int minor, string expected)
    {
        var model = new GameHudModel(new(0, 0), new(0, 0), new(0, 0), major, minor);
        Assert.Equal(expected, model.StageText);
    }
}
