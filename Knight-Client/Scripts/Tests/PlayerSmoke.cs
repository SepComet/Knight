#nullable enable
using System;
using System.Linq;
using System.Threading.Tasks;
using Godot;
using SepCore.Gameplay.Player;

namespace SepCore.Tests;

/// <summary>Engine-level movement checks. Run the accompanying scene with --headless.</summary>
public partial class PlayerSmoke : Node2D
{
    private const float Step = 1f / 30;
    private int _checks;

    public override async void _Ready()
    {
        try
        {
            await Run();
            GD.Print($"[PlayerSmoke] PASS: {_checks} checks");
            GetTree().Quit();
        }
        catch (Exception error)
        {
            GD.PushError($"[PlayerSmoke] FAIL: {error}");
            GetTree().Quit(1);
        }
    }

    private async Task Run()
    {
        var input = new PlayerInputBuffer { Movement = new Vector2(0.1f, 0), AttackHeld = true };
        input.RequestInteraction();
        var command = input.Consume(0);
        Check(command.InteractPressed && !input.Consume(0).InteractPressed, "short interaction survives sampling and is consumed once");
        Check(command.Movement.IsZeroApprox(), "joystick deadzone suppresses small input");
        input.Movement = new Vector2(0.5f, 0.5f);
        Check(Mathf.Abs(input.Consume(0).Movement.Length() - 1) < 0.001f, "joystick outside deadzone produces full-speed direction");
        Check(!input.Consume(0, false).AttackHeld, "no valid target suppresses automatic attack");
        input.RequestInteraction();
        input.Reset();
        command = input.Consume(0);
        Check(!command.AttackHeld && !command.InteractPressed && command.Movement.IsZeroApprox(), "reset clears held and latched input");

        var motor = new PlayerMotor { MoveSpeed = 96, CollisionLayer = 2, CollisionMask = 1 };
        motor.AddChild(new CollisionShape2D { Shape = new CircleShape2D { Radius = 4 } });
        AddChild(motor);
        motor.SetPhysicsProcess(false);
        await ToSignal(GetTree(), SceneTree.SignalName.PhysicsFrame);
        await ToSignal(GetTree(), SceneTree.SignalName.PhysicsFrame);

        Advance(motor, Vector2.Right);
        Check(Mathf.Abs(motor.Position.X - 96) < 0.01f, "cardinal speed is 96 px/sec");
        motor.Teleport(Vector2.Zero);
        Advance(motor, Vector2.One);
        Check(Mathf.Abs(motor.Position.Length() - 96) < 0.01f, "diagonal movement is normalized");
        var before = motor.Position;
        motor.Simulate(new(Vector2.Zero, 1.2f, true, false), Step);
        Check(motor.Position.IsEqualApprox(before) && motor.Velocity.IsZeroApprox(), "zero input stops immediately");
        Check(Mathf.Abs(motor.FacingRadians - 1.2f) < 0.001f, "aim changes independently of movement");
        motor.Alive = false;
        motor.Simulate(new(Vector2.Right, 0, true, false), Step);
        Check(motor.Position.IsEqualApprox(before) && motor.Velocity.IsZeroApprox(), "dead player cannot move");
        motor.Alive = true;
        motor.Teleport(Vector2.Zero);
        Check(motor.LastMovement.IsZeroApprox() && motor.Velocity.IsZeroApprox(), "teleport resets movement");

        var samples = 0;
        motor.InputSource = () => { samples++; return new(Vector2.Right, 0, false, false); };
        for (var i = 0; i < 60; i++) motor._PhysicsProcess(1d / 60);
        Check(samples == 30 && Mathf.Abs(motor.Position.X - 96) < 0.01f,
            "60 physics updates produce 30 fixed input samples and one second travel");
        motor.InputSource = null;
        motor.Teleport(Vector2.Zero);

        var near = new Node2D { Position = new Vector2(10, 0) };
        var far = new Node2D { Position = new Vector2(100, 0) };
        AddChild(near);
        AddChild(far);
        near.AddToGroup("attackable");
        far.AddToGroup("attackable");
        Check(PlayerAim.NearestTarget(GetTree(), Vector2.Zero) == near, "automatic aim selects nearest target");
        near.Hide();
        Check(PlayerAim.NearestTarget(GetTree(), Vector2.Zero) == far, "hidden targets are excluded");
        near.RemoveFromGroup("attackable");
        far.RemoveFromGroup("attackable");
        Check(PlayerAim.NearestTarget(GetTree(), Vector2.Zero) == null, "no attackable targets yields null");

        var wall = new StaticBody2D { Position = new Vector2(40, 0), CollisionLayer = 1 };
        wall.AddChild(new CollisionShape2D { Shape = new RectangleShape2D { Size = new Vector2(10, 500) } });
        AddChild(wall);
        await ToSignal(GetTree(), SceneTree.SignalName.PhysicsFrame);
        await ToSignal(GetTree(), SceneTree.SignalName.PhysicsFrame);
        Advance(motor, Vector2.Right);
        Check(motor.Position.X <= 31.1f && motor.Position.X >= 30, "wall blocks player without crossing");
        motor.Teleport(Vector2.Zero);
        Advance(motor, Vector2.One);
        Check(motor.Position.X <= 31.1f && motor.Position.Y > 60, "diagonal movement slides along wall");

        motor.Teleport(Vector2.Zero);
        motor.Simulate(new(new Vector2(float.NaN, 1), float.NaN, false, false), Step);
        Check(motor.Position.IsFinite() && motor.Position.IsZeroApprox(), "nonfinite input cannot corrupt position");
        before = motor.Position;
        motor.Simulate(new(Vector2.Right, 0, false, false), float.NaN);
        Check(motor.Position.IsEqualApprox(before), "invalid simulation duration is ignored");

        var prefab = GD.Load<PackedScene>("res://Prefabs/Entity/Player.tscn").Instantiate<PlayerMotor>();
        prefab.Position = new Vector2(-100, -100);
        AddChild(prefab);
        prefab.SetPhysicsProcess(false);
        var view = prefab.GetChildren().OfType<PlayerPresentation>().Single();
        view.SetProcess(false);
        var sprite = prefab.GetNode<Sprite2D>("%BodySprite");
        prefab.Simulate(new(Vector2.Right, 0, false, false), Step);
        view._Process(0.4);
        Check(sprite.Hframes == 4 && sprite.Vframes == 4 && sprite.Frame is >= 0 and < 16,
            "player prefab displays valid directional walking frame");
        prefab.Alive = false;
        view._Process(0.1);
        Check(sprite.Hframes == 1 && sprite.Vframes == 1 && sprite.Frame == 0,
            "death changes sprite grid and resets frame safely");
        prefab.Alive = true;
        prefab.Simulate(new(Vector2.Zero, Mathf.Pi, false, false), Step);
        view._Process(0.1);
        Check(sprite.Hframes == 4 && sprite.Vframes == 1 && sprite.Frame == 2,
            "alive idle restores directional sprite grid after death");
    }

    private static void Advance(PlayerMotor motor, Vector2 movement)
    {
        for (var i = 0; i < 30; i++) motor.Simulate(new(movement, 0, false, false), Step);
    }

    private void Check(bool condition, string description)
    {
        if (!condition) throw new InvalidOperationException(description);
        _checks++;
        GD.Print($"[PlayerSmoke] OK: {description}");
    }
}
