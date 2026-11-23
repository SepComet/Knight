#nullable enable
using System;
using System.Collections.Generic;
using Godot;
using SepCore.Definition;
using SepCore.Runtime.Component;
using SepCore.Runtime.Luban;

namespace SepCore.Runtime.UI;

/// <summary>
/// UI 栈组件(全局组件,MVC 机制层):分组 CanvasLayer 容器 + 组内实例栈 + 视图原语。
/// <list type="bullet">
/// <item><see cref="Open"/>/<see cref="Close"/> 仅转发给注册的 <see cref="UIController"/>,
/// 刷新/重开等策略由控制器决定;</item>
/// <item>组件强制配置约束(AllowMultiInstance)并按组栈序触发 PauseCoveredUIForm 覆盖暂停;</item>
/// <item>界面场景按约定位于 res://Prefabs/UI/{AssetName}.tscn,根节点必须是 UIForm 子类;
/// PackedScene 常驻缓存,实例关闭即销毁。</item>
/// </list>
/// 控制器在启动流水线(数据表加载完成后)经 <see cref="Register"/> 注册。
/// </summary>
public partial class UIComponent : GlobalComponent
{
    public static UIComponent? Instance { get; private set; }

    private readonly Dictionary<UIFormType, UIController> _controllers = new();
    private readonly Dictionary<string, CanvasLayer> _layers = new();
    private readonly Dictionary<string, List<UIForm>> _stacks = new();          // 组内栈:尾 = 最顶
    private readonly Dictionary<UIFormType, List<UIForm>> _viewsByType = new(); // 创建序:尾 = 最新
    private readonly Dictionary<string, PackedScene> _sceneCache = new();

    /// <summary>视图实例创建并 OnOpen 后触发。</summary>
    public event Action<UIForm>? FormOpened;

    /// <summary>视图 OnClose 后、销毁前触发。</summary>
    public event Action<UIForm>? FormClosed;

    public override void _Ready()
    {
        Instance = this;

        foreach (var (groupName, depth) in UIGroupRegistry.Depths)
        {
            var layer = new CanvasLayer { Name = groupName, Layer = depth };
            AddChild(layer);
            _layers[groupName] = layer;
            _stacks[groupName] = new List<UIForm>();
        }
        GD.Print($"[UI] 界面组就绪:{string.Join("/", _layers.Keys)}");
    }

    public override void _ExitTree()
    {
        if (Instance == this)
            Instance = null;
    }

    // —— 控制器注册 ——

    /// <summary>注册控制器并注入组件与配置(需在数据表加载后调用,重复注册抛异常)。</summary>
    public void Register(UIFormType formId, UIController controller)
    {
        ArgumentNullException.ThrowIfNull(controller);
        if (!_controllers.TryAdd(formId, controller))
            throw new InvalidOperationException($"界面控制器重复注册:{formId}");
        controller.Attach(this, formId, GetConfig(formId));
    }

    // —— 公开入口:仅转发,不含"是否已打开"的判断 ——

    /// <summary>打开请求:转发给控制器,由其决定刷新已有视图还是重开。</summary>
    public void Open(UIFormType formId, object? userData = null)
        => RequireController(formId).OnOpenRequest(userData);

    /// <summary>关闭请求:转发给控制器(默认实现为销毁当前视图,控制器可覆写)。</summary>
    public void Close(UIFormType formId)
        => RequireController(formId).OnCloseRequest();

    // —— 视图原语(供控制器组合) ——

    /// <summary>
    /// 创建视图:加载场景 → 实例化 → 挂到所属组层 → OnOpen → 入栈。
    /// 非多实例界面已打开时抛异常(控制器应走刷新路径)。
    /// </summary>
    public UIForm CreateView(UIFormType formId, object? userData = null)
    {
        var config = GetConfig(formId);
        if (!config.AllowMultiInstance && FindView(formId) != null)
            throw new InvalidOperationException($"界面 {formId} 不允许多实例,当前已打开(控制器应走刷新路径)");

        var form = InstantiateForm(config);
        form.Config = config;
        RequireLayer(config.UIGroupName).AddChild(form);
        form.OnOpen(userData);

        RequireStack(config.UIGroupName).Add(form);
        ViewListOf(formId).Add(form);
        RefreshPauseState(config.UIGroupName);
        FormOpened?.Invoke(form);
        return form;
    }

    /// <summary>销毁视图:出栈 → OnClose → 事件 → 释放。重复销毁抛异常。</summary>
    public void DestroyView(UIForm form)
    {
        ArgumentNullException.ThrowIfNull(form);
        var groupName = form.Config.UIGroupName;
        if (!RequireStack(groupName).Remove(form))
            throw new InvalidOperationException($"界面实例不在其组的界面栈中,可能已被销毁:{form.Config.Id}");
        ViewListOf(form.Config.Id).Remove(form);

        form.OnClose();
        FormClosed?.Invoke(form);
        RefreshPauseState(groupName);
        form.QueueFree();
    }

    /// <summary>当前打开的该类型实例(多实例时返回最新创建的);未打开为 null。</summary>
    public UIForm? FindView(UIFormType formId)
    {
        return _viewsByType.TryGetValue(formId, out var views) && views.Count > 0 ? views[^1] : null;
    }

    /// <summary>把实例移到其组栈顶并调整显示顺序(刷新路径常用)。</summary>
    public void BringToTop(UIForm form)
    {
        ArgumentNullException.ThrowIfNull(form);
        var stack = RequireStack(form.Config.UIGroupName);
        if (!stack.Remove(form))
            throw new InvalidOperationException($"界面实例不在其组的界面栈中:{form.Config.Id}");
        stack.Add(form);
        form.GetParent()!.MoveChild(form, -1);
        RefreshPauseState(form.Config.UIGroupName);
    }

    /// <summary>关闭整组界面(机械清场,不经控制器;切组/切场景用)。</summary>
    public void CloseGroup(string groupName)
    {
        var stack = RequireStack(groupName);
        for (var i = stack.Count - 1; i >= 0; i--)
            DestroyView(stack[i]);
    }

    /// <summary>关闭全部界面(切场景清场)。</summary>
    public void CloseAll()
    {
        foreach (var groupName in _stacks.Keys)
            CloseGroup(groupName);
    }

    // —— 内部 ——

    private UIController RequireController(UIFormType formId)
        => _controllers.TryGetValue(formId, out var controller)
            ? controller
            : throw new InvalidOperationException($"界面控制器未注册:{formId}");

    private UIFormConfig GetConfig(UIFormType formId)
    {
        var tables = LubanComponent.Instance?.Tables
            ?? throw new InvalidOperationException("数据表未加载,无法查询界面配置");
        return tables.TbUIFormConfig.GetOrDefault(formId)
            ?? throw new InvalidOperationException($"界面配置缺失:{formId}");
    }

    private UIForm InstantiateForm(UIFormConfig config)
    {
        var path = $"res://Prefabs/UI/{config.AssetName}.tscn";
        if (!_sceneCache.TryGetValue(path, out var scene))
        {
            scene = ResourceLoader.Load<PackedScene>(path)
                ?? throw new InvalidOperationException($"界面场景缺失:{path}");
            _sceneCache[path] = scene;
        }

        var instance = scene.Instantiate();
        if (instance is not UIForm form)
        {
            instance.Free();
            throw new InvalidOperationException(
                $"界面场景根节点必须是 UIForm 子类:{path}(实际 {instance.GetType().Name})");
        }
        return form;
    }

    private List<UIForm> ViewListOf(UIFormType formId)
    {
        if (!_viewsByType.TryGetValue(formId, out var list))
        {
            list = new List<UIForm>();
            _viewsByType[formId] = list;
        }
        return list;
    }

    /// <summary>按组栈序重算覆盖暂停:某界面上方存在 PauseCoveredUIForm 的界面即视为被覆盖。</summary>
    private void RefreshPauseState(string groupName)
    {
        var covered = false;
        var stack = RequireStack(groupName);
        for (var i = stack.Count - 1; i >= 0; i--)
        {
            var form = stack[i];
            form.NotifyStackPause(covered);
            if (form.Config.PauseCoveredUIForm)
                covered = true;
        }
    }

    private CanvasLayer RequireLayer(string groupName)
        => _layers.TryGetValue(groupName, out var layer)
            ? layer
            : throw new InvalidOperationException($"界面组未登记:{groupName}(请加入 UIGroupRegistry)");

    private List<UIForm> RequireStack(string groupName)
        => _stacks.TryGetValue(groupName, out var stack)
            ? stack
            : throw new InvalidOperationException($"界面组未登记:{groupName}(请加入 UIGroupRegistry)");
}
