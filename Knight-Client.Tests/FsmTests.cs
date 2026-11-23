using System;
using System.Collections.Generic;
using SepCore.Runtime.Fsm;
using Xunit;

namespace SepCore.Tests;

public class FsmTests
{
    private sealed class Owner { }

    private abstract class StateBase : IFsmState<Owner>
    {
        private readonly string _name;
        protected readonly List<string> Log;

        protected StateBase(string name, List<string> log)
        {
            _name = name;
            Log = log;
        }

        public virtual void OnEnter(Fsm<Owner> fsm) => Log.Add($"{_name}.Enter");
        public virtual void OnUpdate(Fsm<Owner> fsm, float delta) => Log.Add($"{_name}.Update");
        public virtual void OnExit(Fsm<Owner> fsm) => Log.Add($"{_name}.Exit");
    }

    private sealed class StateA : StateBase
    {
        public StateA(List<string> log) : base("A", log) { }
    }

    private sealed class StateB : StateBase
    {
        public StateB(List<string> log) : base("B", log) { }
    }

    // 已实现状态接口、但从不注册进状态机,用于验证未注册状态的查询/切换
    private sealed class StateC : StateBase
    {
        public StateC(List<string> log) : base("C", log) { }
    }

    // OnUpdate 中当 StateTime 达到 1 秒时自主切换到 B
    private sealed class TimedA : StateBase
    {
        public TimedA(List<string> log) : base("TA", log) { }

        public override void OnUpdate(Fsm<Owner> fsm, float delta)
        {
            base.OnUpdate(fsm, delta);
            if (fsm.StateTime >= 1f)
                fsm.ChangeState<StateB>();
        }
    }

    // OnEnter 内尝试切换状态(应抛异常)
    private sealed class EnterChanger : StateBase
    {
        public EnterChanger(List<string> log) : base("EC", log) { }

        public override void OnEnter(Fsm<Owner> fsm) => fsm.ChangeState<StateB>();
    }

    // OnExit 内尝试切换状态(应抛异常)
    private sealed class ExitChanger : StateBase
    {
        public ExitChanger(List<string> log) : base("XC", log) { }

        public override void OnExit(Fsm<Owner> fsm) => fsm.ChangeState<StateB>();
    }

    [Fact]
    public void Start_EntersInitialState_WithZeroStateTime()
    {
        var log = new List<string>();
        var fsm = Fsm<Owner>.Create(new Owner(), new StateA(log), new StateB(log));

        fsm.Start<StateA>();

        Assert.Equal(typeof(StateA), fsm.CurrentStateType);
        Assert.Equal(0f, fsm.StateTime);
        Assert.Equal(new[] { "A.Enter" }, log);
    }

    [Fact]
    public void Update_AccumulatesStateTime_AndDrivesOnUpdate()
    {
        var log = new List<string>();
        var fsm = Fsm<Owner>.Create(new Owner(), new StateA(log), new StateB(log));
        fsm.Start<StateA>();

        fsm.Update(0.5f);
        fsm.Update(0.25f);

        Assert.Equal(0.75f, fsm.StateTime, 3);
        Assert.Equal(new[] { "A.Enter", "A.Update", "A.Update" }, log);
    }

    [Fact]
    public void Update_BeforeStart_IsNoOp()
    {
        var log = new List<string>();
        var fsm = Fsm<Owner>.Create(new Owner(), new StateA(log), new StateB(log));

        fsm.Update(1f);

        Assert.Null(fsm.CurrentStateType);
        Assert.Equal(0f, fsm.StateTime);
        Assert.Empty(log);
    }

    [Fact]
    public void ChangeState_ExitsCurrent_EntersNext_ResetsTime()
    {
        var log = new List<string>();
        var fsm = Fsm<Owner>.Create(new Owner(), new StateA(log), new StateB(log));
        fsm.Start<StateA>();
        fsm.Update(2f);

        fsm.ChangeState<StateB>();

        Assert.Equal(typeof(StateB), fsm.CurrentStateType);
        Assert.Equal(0f, fsm.StateTime);
        Assert.Equal(new[] { "A.Enter", "A.Update", "A.Exit", "B.Enter" }, log);
    }

    [Fact]
    public void ChangeState_ToSameState_RestartsIt()
    {
        var log = new List<string>();
        var fsm = Fsm<Owner>.Create(new Owner(), new StateA(log), new StateB(log));
        fsm.Start<StateA>();
        fsm.Update(1f);

        fsm.ChangeState<StateA>();

        Assert.Equal(typeof(StateA), fsm.CurrentStateType);
        Assert.Equal(0f, fsm.StateTime);
        Assert.Equal(new[] { "A.Enter", "A.Update", "A.Exit", "A.Enter" }, log);
    }

    [Fact]
    public void ChangeState_FromOnUpdate_Works()
    {
        var log = new List<string>();
        var fsm = Fsm<Owner>.Create(new Owner(), new TimedA(log), new StateB(log));
        fsm.Start<TimedA>();

        fsm.Update(0.6f);
        Assert.Equal(typeof(TimedA), fsm.CurrentStateType); // 未到阈值,不切换

        fsm.Update(0.4f); // StateTime 达 1 秒,TA 内部切到 B

        Assert.Equal(typeof(StateB), fsm.CurrentStateType);
        Assert.Equal(new[] { "TA.Enter", "TA.Update", "TA.Update", "TA.Exit", "B.Enter" }, log);
    }

    [Fact]
    public void ChangeState_InsideOnEnter_Throws()
    {
        var log = new List<string>();
        var fsm = Fsm<Owner>.Create(new Owner(), new EnterChanger(log), new StateB(log));

        Assert.Throws<InvalidOperationException>(() => fsm.Start<EnterChanger>());
    }

    [Fact]
    public void ChangeState_InsideOnExit_Throws()
    {
        var log = new List<string>();
        var fsm = Fsm<Owner>.Create(new Owner(), new ExitChanger(log), new StateB(log));
        fsm.Start<ExitChanger>();
        fsm.Update(0.1f);

        Assert.Throws<InvalidOperationException>(() => fsm.ChangeState<StateB>());
    }

    [Fact]
    public void ChangeState_ToUnregisteredState_Throws()
    {
        var log = new List<string>();
        var fsm = Fsm<Owner>.Create(new Owner(), new StateA(log), new StateB(log));
        fsm.Start<StateA>();

        Assert.Throws<ArgumentNullException>(() => fsm.ChangeState((Type)null!));
        Assert.Throws<ArgumentException>(() => fsm.ChangeState(typeof(StateC))); // 未注册的状态
    }

    [Fact]
    public void ChangeState_BeforeStart_Throws()
    {
        var log = new List<string>();
        var fsm = Fsm<Owner>.Create(new Owner(), new StateA(log), new StateB(log));

        Assert.Throws<InvalidOperationException>(() => fsm.ChangeState<StateB>());
    }

    [Fact]
    public void Start_Twice_Throws()
    {
        var log = new List<string>();
        var fsm = Fsm<Owner>.Create(new Owner(), new StateA(log), new StateB(log));
        fsm.Start<StateA>();

        Assert.Throws<InvalidOperationException>(() => fsm.Start<StateB>());
    }

    [Fact]
    public void Start_ByRuntimeType_EntersInitialState()
    {
        var log = new List<string>();
        var fsm = Fsm<Owner>.Create(new Owner(), new StateA(log), new StateB(log));

        IFsmState<Owner>[] states = { new StateB(log) }; // 模拟运行时才拿到类型
        fsm.Start(states[0].GetType());

        Assert.Equal(typeof(StateB), fsm.CurrentStateType);
        Assert.Equal(new[] { "B.Enter" }, log);
    }

    [Fact]
    public void Start_UnregisteredType_Throws()
    {
        var log = new List<string>();
        var fsm = Fsm<Owner>.Create(new Owner(), new StateA(log), new StateB(log));

        Assert.Throws<ArgumentNullException>(() => fsm.Start((Type)null!));
        Assert.Throws<ArgumentException>(() => fsm.Start(typeof(StateC)));
    }

    [Fact]
    public void Create_DuplicateStateType_Throws()
    {
        var log = new List<string>();

        Assert.Throws<ArgumentException>(
            () => Fsm<Owner>.Create(new Owner(), new StateA(log), new StateA(log)));
    }

    [Fact]
    public void Shutdown_ExitsCurrent_StopsUpdate_AndAllowsRestart()
    {
        var log = new List<string>();
        var fsm = Fsm<Owner>.Create(new Owner(), new StateA(log), new StateB(log));
        fsm.Start<StateA>();
        fsm.Update(0.5f);

        fsm.Shutdown();

        Assert.Null(fsm.CurrentStateType);
        Assert.Equal(0f, fsm.StateTime);
        Assert.Equal(new[] { "A.Enter", "A.Update", "A.Exit" }, log);

        fsm.Update(1f); // 关机后 Update 为空操作
        Assert.Equal(new[] { "A.Enter", "A.Update", "A.Exit" }, log);

        fsm.Start<StateB>(); // 可重新启动
        Assert.Equal(typeof(StateB), fsm.CurrentStateType);
        Assert.Equal(new[] { "A.Enter", "A.Update", "A.Exit", "B.Enter" }, log);
    }

    [Fact]
    public void Shutdown_BeforeStart_IsNoOp()
    {
        var log = new List<string>();
        var fsm = Fsm<Owner>.Create(new Owner(), new StateA(log), new StateB(log));

        fsm.Shutdown();

        Assert.Empty(log);
    }

    [Fact]
    public void Owner_And_GetState_Work()
    {
        var owner = new Owner();
        var log = new List<string>();
        var stateA = new StateA(log);
        var fsm = Fsm<Owner>.Create(owner, stateA, new StateB(log));

        Assert.Same(owner, fsm.Owner);
        Assert.Same(stateA, fsm.GetState<StateA>());
        Assert.True(fsm.HasState<StateB>());
        Assert.False(fsm.HasState<StateC>()); // StateC 未注册进状态机
    }
}
