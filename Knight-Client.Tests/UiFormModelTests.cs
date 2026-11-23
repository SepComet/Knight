using System;
using SepCore.UI.Auth;
using SepCore.UI.Rooms;
using Xunit;

namespace SepCore.Tests;

public class UiFormModelTests
{
    [Fact]
    public void RegistrationRequiresMatchingPasswordButLoginDoesNot()
    {
        Assert.NotEmpty(AuthValidation.Validate(AuthMode.Login, " ", "abc", ""));
        Assert.NotEmpty(AuthValidation.Validate(AuthMode.Register, "user", "abc", "different"));
        Assert.Empty(AuthValidation.Validate(AuthMode.Login, "user", "abc", ""));
        Assert.Empty(AuthValidation.Validate(AuthMode.Register, "user", "abc", "abc"));
    }

    [Fact]
    public void RefreshPreservesValidSelectionAndDropsAFullOrRemovedRoom()
    {
        var model = new RoomBrowserModel();
        var room = new RoomSummary("a", "1001", 1, 4, "等待中", true);
        model.ReplaceRooms(new[] { room });
        model.Select("a");
        model.ReplaceRooms(new[] { room with { PlayerCount = 2 } });
        Assert.Equal("a", model.SelectedId);
        model.ReplaceRooms(new[] { room with { PlayerCount = 4 } });
        Assert.Null(model.SelectedId);
        model.Select("a");
        Assert.Null(model.Selected);
        model.ReplaceRooms(Array.Empty<RoomSummary>());
        Assert.Null(model.SelectedId);
    }

    [Fact]
    public void InvalidRoomSnapshotDoesNotMutatePreviousSelection()
    {
        var model = new RoomBrowserModel();
        var room = new RoomSummary("a", "1001", 1, 4, "等待中", false);
        model.ReplaceRooms(new[] { room });
        model.Select("a");
        Assert.Throws<ArgumentException>(() => model.ReplaceRooms(new[] { room, room }));
        Assert.Equal("a", model.SelectedId);
        Assert.Single(model.Rooms);
        Assert.False(RoomBrowserModel.CanJoin(room with { PlayerCount = -1 }));
        Assert.False(RoomBrowserModel.CanJoin(room with { IsJoinable = false }));
    }
}
