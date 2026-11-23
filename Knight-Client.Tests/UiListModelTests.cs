using System;
using System.Linq;
using SepCore.UI.Rooms;
using SepCore.UI.Results;
using Xunit;

namespace SepCore.Tests;

public class UiListModelTests
{
    [Fact]
    public void WaitingReadyOnlyChangesLocalPlayerAndRequiresHostToStart()
    {
        var model = new WaitingRoomModel();
        var players = new[] { new WaitingPlayer("local", "Me"), new WaitingPlayer("other", "Other", IsReady: true) };
        model.SetPlayers("123", "local", true, players);
        Assert.False(model.CanStart);
        Assert.True(model.SetReady(true));
        Assert.True(model.CanStart);
        Assert.True(model.Players[1].IsReady);
        Assert.False(players[0].IsReady);
        Assert.False(model.SetReady(true));
        model.SetPlayers("123", "local", false, model.Players);
        Assert.False(model.CanStart);
    }

    [Fact]
    public void MissingLocalPlayerCannotSetReadyOrStart()
    {
        var model = new WaitingRoomModel();
        model.SetPlayers("123", "missing", true, new[] { new WaitingPlayer("other", "Other", IsReady: true) });
        Assert.False(model.SetReady(true));
        Assert.False(model.CanStart);
        model.SetPlayers("123", "local", true, Array.Empty<WaitingPlayer>());
        Assert.False(model.CanStart);
    }

    [Fact]
    public void WaitingRejectsOversizedAndDuplicateSnapshotsWithoutReplacingCurrentState()
    {
        var model = new WaitingRoomModel();
        model.SetPlayers("123", "a", true, new[] { new WaitingPlayer("a", "A") });
        Assert.Throws<ArgumentException>(() => model.SetPlayers("bad", "a", true,
            Enumerable.Range(0, 5).Select(i => new WaitingPlayer(i.ToString(), "P"))));
        Assert.Throws<ArgumentException>(() => model.SetPlayers("bad", "a", true,
            new[] { new WaitingPlayer("a", "A"), new WaitingPlayer("a", "B") }));
        Assert.Equal("123", model.RoomCode);
        Assert.Single(model.Players);
    }

    [Fact]
    public void ResultsCopySnapshotAndRejectInvalidPlayerCounts()
    {
        var model = new GameResultModel();
        var players = new[] { new PlayerResult("a", "A", "Weapon", 10) };
        model.SetResults(players);
        players[0] = new PlayerResult("b", "B", "Other", 20);
        Assert.Equal("a", model.Players[0].Id);
        Assert.Throws<ArgumentException>(() => model.SetResults(Enumerable.Repeat(model.Players[0], 2)));
        Assert.Throws<ArgumentException>(() => model.SetResults(
            Enumerable.Range(0, 5).Select(i => new PlayerResult(i.ToString(), "P", "", 0))));
        Assert.Single(model.Players);
        model.SetResults(Array.Empty<PlayerResult>());
        Assert.Empty(model.Players);
    }
}
