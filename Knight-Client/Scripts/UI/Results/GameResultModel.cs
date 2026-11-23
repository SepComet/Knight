#nullable enable
using System;
using System.Collections.Generic;
using System.Linq;
namespace SepCore.UI.Results;
public sealed record PlayerResult(string Id, string Name, string Weapons, long TotalDamage);
public sealed class GameResultModel
{
    public IReadOnlyList<PlayerResult> Players { get; private set; } = Array.Empty<PlayerResult>();
    public void SetResults(IEnumerable<PlayerResult> players)
    {
        var snapshot = players.Take(5).ToArray();
        if (snapshot.Length > 4 || snapshot.Select(p => p.Id).Distinct().Count() != snapshot.Length)
            throw new ArgumentException("Results must contain unique players and no more than four.", nameof(players));
        Players = Array.AsReadOnly(snapshot);
    }
}
