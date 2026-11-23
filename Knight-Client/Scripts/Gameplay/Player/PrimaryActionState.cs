namespace SepCore.Gameplay.Player;

/// <summary>Defaults to attack. Explicit interaction permission is sampled once per press.</summary>
public sealed class PrimaryActionState
{
    private bool _held, _interactionPress, _pendingInteraction;
    public bool AttackHeld => _held && !_interactionPress;
    public bool IsInteractionMode(bool available) => _held ? _interactionPress : available;
    public void SetHeld(bool held, bool interactionAvailable)
    {
        if (held && !_held)
        {
            _interactionPress = interactionAvailable;
            if (_interactionPress) _pendingInteraction = true;
        }
        _held = held;
    }
    public bool ConsumeInteraction()
    {
        var pending = _pendingInteraction;
        _pendingInteraction = false;
        return pending;
    }
    public void Reset() => _held = _interactionPress = _pendingInteraction = false;
}
