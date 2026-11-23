#nullable enable
using System;
using System.Collections.Generic;
using System.Linq;

namespace SepCore.UI.Rooms;

public sealed record RoomSummary(string Id, string Number, int PlayerCount, int Capacity,
    string Status, bool HasPassword, bool IsJoinable = true);
public enum RoomPasswordMode { Create, Join }
public sealed record RoomIntent(RoomPasswordMode Mode, string? RoomId, string Password);

public sealed class RoomBrowserModel
{
    public IReadOnlyList<RoomSummary> Rooms { get; private set; } = Array.Empty<RoomSummary>();
    public string? SelectedId { get; private set; }
    public RoomSummary? Selected => Rooms.FirstOrDefault(room => room.Id == SelectedId && CanJoin(room));
    public static bool CanJoin(RoomSummary room) => room.IsJoinable && room.Capacity > 0 &&
        room.PlayerCount >= 0 && room.PlayerCount < room.Capacity;

    public void ReplaceRooms(IEnumerable<RoomSummary> rooms)
    {
        var snapshot = rooms.ToArray();
        if (snapshot.Any(room => string.IsNullOrWhiteSpace(room.Id)) ||
            snapshot.Select(room => room.Id).Distinct().Count() != snapshot.Length)
            throw new ArgumentException("Room identifiers must be nonempty and unique.", nameof(rooms));
        Rooms = Array.AsReadOnly(snapshot);
        if (Selected == null) SelectedId = null;
    }

    public void Select(string id) => SelectedId = Rooms.Any(room => room.Id == id && CanJoin(room)) ? id : null;
}
