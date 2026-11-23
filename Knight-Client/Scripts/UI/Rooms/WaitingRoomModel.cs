#nullable enable
using System;
using System.Collections.Generic;
using System.Linq;
namespace SepCore.UI.Rooms;
public sealed record WaitingPlayer(string Id, string Name, string Role = "", bool IsReady = false);
/// <summary>UI-only waiting-room snapshot. It does not represent server authority.</summary>
public sealed class WaitingRoomModel
{
    public string RoomCode { get; private set; } = "";
    public string LocalPlayerId { get; private set; } = "";
    public bool IsHost { get; private set; }
    public IReadOnlyList<WaitingPlayer> Players { get; private set; } = Array.Empty<WaitingPlayer>();
    public bool LocalReady => Players.FirstOrDefault(p => p.Id == LocalPlayerId)?.IsReady == true;
    public bool CanStart => IsHost && Players.Count > 0 && Players.Any(p => p.Id == LocalPlayerId) && Players.All(p => p.IsReady);
    public void SetPlayers(string roomCode, string localPlayerId, bool isHost, IEnumerable<WaitingPlayer> players)
    {
        var snapshot = players.Take(5).ToArray();
        if (snapshot.Length > 4 || snapshot.Select(p => p.Id).Distinct().Count() != snapshot.Length)
            throw new ArgumentException("Waiting-room players must be unique and no more than four.", nameof(players));
        RoomCode = roomCode; LocalPlayerId = localPlayerId; IsHost = isHost; Players = Array.AsReadOnly(snapshot);
    }
    public bool SetReady(bool ready)
    {
        if (!Players.Any(p => p.Id == LocalPlayerId) || LocalReady == ready) return false;
        Players = Array.AsReadOnly(Players.Select(p => p.Id == LocalPlayerId ? p with { IsReady = ready } : p).ToArray());
        return true;
    }
}
