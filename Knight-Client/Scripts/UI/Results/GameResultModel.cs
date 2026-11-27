#nullable enable
using System;
using System.Collections.Generic;
using System.Linq;

namespace SepCore.UI.Results;

public sealed record PlayerResult(
    string Id,
    string Name,
    string Weapons,
    long TotalDamage,
    uint Kills = 0,
    int DamageTaken = 0,
    bool Alive = true);

public sealed class GameResultModel
{
    public ulong MatchId { get; private set; }
    public bool HasOutcome { get; private set; }
    public bool IsVictory { get; private set; }
    public uint DurationSec { get; private set; }
    public IReadOnlyList<PlayerResult> Players { get; private set; } = Array.Empty<PlayerResult>();

    public string TitleText => !HasOutcome ? "本局结算" : (IsVictory ? "地牢通关 · 胜利!" : "全员阵亡 · 失败");

    public string SummaryText
    {
        get
        {
            if (!HasOutcome)
                return "";
            var sec = DurationSec;
            var duration = $"{sec / 60:00}:{sec % 60:00}";
            return MatchId > 0 ? $"对局 #{MatchId} · 耗时 {duration}" : $"耗时 {duration}";
        }
    }

    public void SetMatchSummary(ulong matchId, bool isVictory, uint durationSec)
    {
        MatchId = matchId;
        HasOutcome = true;
        IsVictory = isVictory;
        DurationSec = durationSec;
    }

    public void SetResults(IEnumerable<PlayerResult> players)
    {
        var snapshot = players.Take(5).ToArray();
        if (snapshot.Length > 4 || snapshot.Select(p => p.Id).Distinct().Count() != snapshot.Length)
            throw new ArgumentException("Results must contain unique players and no more than four.", nameof(players));
        Players = Array.AsReadOnly(snapshot);
    }
}
