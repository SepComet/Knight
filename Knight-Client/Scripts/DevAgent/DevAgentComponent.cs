#nullable enable
using System;
using System.Collections.Concurrent;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Net;
using System.Text;
using System.Text.Json;
using System.Text.Json.Serialization;
using System.Threading;
using System.Threading.Tasks;
using Godot;
using SepCore.Battle;
using SepCore.Gameplay.Player;
using SepCore.Procedure;
using SepCore.Runtime.Component;
using SepCore.Runtime.Network;
using SepCore.Runtime.UI;
using SepCore.UI;

namespace SepCore.DevAgent;

/// <summary>
/// Lightweight dev-only localhost HTTP control autoload for multi-instance automated testing.
/// Disabled with zero overhead when <c>--dev-agent-port</c> is not provided.
/// </summary>
public partial class DevAgentComponent : GlobalComponent
{
    private const int MaxUiItemsPerCategory = 64;
    private static readonly TimeSpan MainThreadTimeout = TimeSpan.FromSeconds(5);
    private static readonly TimeSpan ScreenshotTimeout = TimeSpan.FromSeconds(10);

    private static readonly JsonSerializerOptions JsonOptions = new()
    {
        PropertyNameCaseInsensitive = true,
        DefaultIgnoreCondition = JsonIgnoreCondition.Never,
    };

    private DevAgentConfig _config = new(0, "");
    private HttpListener? _listener;
    private CancellationTokenSource? _cts;
    private readonly ConcurrentQueue<Action> _mainThreadQueue = new();
    private volatile bool _shuttingDown;

    public override void _Ready()
    {
        _config = DevAgentConfig.Parse(OS.GetCmdlineUserArgs());
        if (!_config.IsEnabled)
        {
            SetProcess(false);
            return;
        }

        ProcessMode = ProcessModeEnum.Always;
        _shuttingDown = false;
        _cts = new CancellationTokenSource();

        var prefix = $"http://127.0.0.1:{_config.Port}/";
        try
        {
            _listener = new HttpListener();
            _listener.Prefixes.Add(prefix);
            _listener.Start();
            GD.Print($"[DevAgent] Listening on {prefix} (instance_id={_config.InstanceId})");
            _ = Task.Run(() => AcceptLoopAsync(_cts.Token));
        }
        catch (Exception ex)
        {
            GD.PushError($"[DevAgent] Failed to start listener on {prefix}: {ex.Message}");
            StopServer();
            SetProcess(false);
        }
    }

    public override void _Process(double delta)
    {
        while (_mainThreadQueue.TryDequeue(out var action))
        {
            action();
        }
    }

    public override void _ExitTree()
    {
        StopServer();
    }

    private void StopServer()
    {
        _shuttingDown = true;

        var cts = Interlocked.Exchange(ref _cts, null);
        if (cts != null)
        {
            try { cts.Cancel(); }
            catch (ObjectDisposedException) { }
            cts.Dispose();
        }

        var listener = Interlocked.Exchange(ref _listener, null);
        if (listener != null)
        {
            try
            {
                if (listener.IsListening)
                    listener.Stop();
            }
            catch (ObjectDisposedException) { }
            catch (HttpListenerException) { }

            try { listener.Close(); }
            catch (ObjectDisposedException) { }
        }

        while (_mainThreadQueue.TryDequeue(out _))
        {
        }
    }

    private async Task AcceptLoopAsync(CancellationToken ct)
    {
        while (!ct.IsCancellationRequested && !_shuttingDown)
        {
            var listener = _listener;
            if (listener == null || !listener.IsListening)
                break;

            HttpListenerContext context;
            try
            {
                context = await listener.GetContextAsync().WaitAsync(ct).ConfigureAwait(false);
            }
            catch (OperationCanceledException)
            {
                break;
            }
            catch (HttpListenerException)
            {
                break;
            }
            catch (ObjectDisposedException)
            {
                break;
            }
            catch (Exception ex)
            {
                if (!_shuttingDown)
                    GD.PushWarning($"[DevAgent] Accept error: {ex.Message}");
                continue;
            }

            _ = Task.Run(() => HandleRequestAsync(context, ct), CancellationToken.None);
        }
    }

    private async Task HandleRequestAsync(HttpListenerContext context, CancellationToken ct)
    {
        var method = context.Request.HttpMethod.ToUpperInvariant();
        var path = (context.Request.Url?.AbsolutePath ?? "/").TrimEnd('/');
        if (string.IsNullOrEmpty(path))
            path = "/";

        try
        {
            switch (path)
            {
                case "/health":
                    if (method != "GET")
                    {
                        await WriteJsonAsync(context.Response, 405, new ErrorResponse(false, "Method not allowed")).ConfigureAwait(false);
                        return;
                    }
                    await WriteJsonAsync(context.Response, 200, new HealthResponse(true, _config.InstanceId, System.Environment.ProcessId)).ConfigureAwait(false);
                    return;

                case "/state":
                    if (method != "GET")
                    {
                        await WriteJsonAsync(context.Response, 405, new ErrorResponse(false, "Method not allowed")).ConfigureAwait(false);
                        return;
                    }
                    var state = await RunOnMainThreadAsync(CaptureStateOnMainThread, MainThreadTimeout, ct).ConfigureAwait(false);
                    await WriteJsonAsync(context.Response, 200, state).ConfigureAwait(false);
                    return;

                case "/action":
                    if (method != "POST")
                    {
                        await WriteJsonAsync(context.Response, 405, new ErrorResponse(false, "Method not allowed")).ConfigureAwait(false);
                        return;
                    }
                    await HandleActionAsync(context, ct).ConfigureAwait(false);
                    return;

                case "/screenshot":
                    if (method != "POST")
                    {
                        await WriteJsonAsync(context.Response, 405, new ErrorResponse(false, "Method not allowed")).ConfigureAwait(false);
                        return;
                    }
                    await HandleScreenshotAsync(context, ct).ConfigureAwait(false);
                    return;

                default:
                    await WriteJsonAsync(context.Response, 404, new ErrorResponse(false, $"Unknown endpoint: {path}")).ConfigureAwait(false);
                    return;
            }
        }
        catch (TimeoutException)
        {
            await TryWriteErrorAsync(context.Response, 504, "Main thread execution timed out").ConfigureAwait(false);
        }
        catch (OperationCanceledException)
        {
            await TryWriteErrorAsync(context.Response, 503, "DevAgent is shutting down").ConfigureAwait(false);
        }
        catch (Exception ex)
        {
            await TryWriteErrorAsync(context.Response, 500, ex.Message).ConfigureAwait(false);
        }
    }

    private async Task HandleActionAsync(HttpListenerContext context, CancellationToken ct)
    {
        var body = await ReadBodyAsync(context.Request).ConfigureAwait(false);
        ActionRequest? req;
        try
        {
            req = string.IsNullOrWhiteSpace(body)
                ? null
                : JsonSerializer.Deserialize<ActionRequest>(body, JsonOptions);
        }
        catch (JsonException ex)
        {
            await WriteJsonAsync(context.Response, 400, new ErrorResponse(false, $"Invalid JSON: {ex.Message}")).ConfigureAwait(false);
            return;
        }

        if (req == null || string.IsNullOrWhiteSpace(req.Type))
        {
            await WriteJsonAsync(context.Response, 400, new ErrorResponse(false, "Missing required field: type")).ConfigureAwait(false);
            return;
        }

        var actionType = req.Type.Trim().ToLowerInvariant();
        if (actionType == "quit")
        {
            await WriteJsonAsync(context.Response, 200, new ActionResult(true, "quit", null, null, null, null, null)).ConfigureAwait(false);
            _mainThreadQueue.Enqueue(() => GetTree()?.Quit(0));
            return;
        }

        var (statusCode, payload) = await RunOnMainThreadAsync(
            () => ExecuteActionOnMainThread(actionType, req),
            MainThreadTimeout,
            ct).ConfigureAwait(false);

        await WriteJsonAsync(context.Response, statusCode, payload).ConfigureAwait(false);
    }

    private async Task HandleScreenshotAsync(HttpListenerContext context, CancellationToken ct)
    {
        var body = await ReadBodyAsync(context.Request).ConfigureAwait(false);
        ScreenshotRequest? req = null;
        if (!string.IsNullOrWhiteSpace(body))
        {
            try
            {
                req = JsonSerializer.Deserialize<ScreenshotRequest>(body, JsonOptions);
            }
            catch (JsonException ex)
            {
                await WriteJsonAsync(context.Response, 400, new ErrorResponse(false, $"Invalid JSON: {ex.Message}")).ConfigureAwait(false);
                return;
            }
        }

        var targetPath = string.IsNullOrWhiteSpace(req?.Path)
            ? Path.Combine(
                Path.GetTempPath(),
                $"knight_devagent_{(string.IsNullOrEmpty(_config.InstanceId) ? "client" : _config.InstanceId)}_{DateTimeOffset.UtcNow.ToUnixTimeMilliseconds()}.png")
            : Path.GetFullPath(req.Path.Trim());

        var parentDir = Path.GetDirectoryName(targetPath);
        if (!string.IsNullOrEmpty(parentDir))
            Directory.CreateDirectory(parentDir);

        var (statusCode, payload) = await RunOnMainThreadAsync(
            () => CaptureScreenshotOnMainThread(targetPath),
            ScreenshotTimeout,
            ct).ConfigureAwait(false);

        await WriteJsonAsync(context.Response, statusCode, payload).ConfigureAwait(false);
    }

    private Task<T> RunOnMainThreadAsync<T>(Func<T> work, TimeSpan timeout, CancellationToken ct)
    {
        if (_shuttingDown)
            return Task.FromCanceled<T>(ct.IsCancellationRequested ? ct : new CancellationToken(true));

        var tcs = new TaskCompletionSource<T>(TaskCreationOptions.RunContinuationsAsynchronously);
        _mainThreadQueue.Enqueue(() =>
        {
            if (_shuttingDown)
            {
                tcs.TrySetCanceled();
                return;
            }

            try
            {
                tcs.TrySetResult(work());
            }
            catch (Exception ex)
            {
                tcs.TrySetException(ex);
            }
        });

        return tcs.Task.WaitAsync(timeout, ct);
    }

    // ---------------------------------------------------------------- Main-Thread Handlers

    private StateResponse CaptureStateOnMainThread()
    {
        var tree = GetTree();
        var currentScene = tree?.CurrentScene;
        var scenePath = currentScene?.SceneFilePath ?? "";
        var procedureName = ProcedureComponent.Instance?.CurrentProcedureType?.Name ?? "";

        var netClient = NetworkComponent.Instance?.Client;
        var networkState = new NetworkStateDto(
            netClient?.State.ToString() ?? "Disconnected",
            netClient?.Uid ?? 0UL,
            netClient?.Name ?? "");

        RoomStateDto? roomState = null;
        var uiApp = UIComponent.Instance?.GetNodeOrNull<UiApplication>("UiApplication");
        if (uiApp?.CurrentRoom is { } room)
        {
            var members = new List<RoomMemberDto>(room.Members.Count);
            foreach (var m in room.Members)
                members.Add(new RoomMemberDto(m.Uid, m.Name, m.Ready));

            roomState = new RoomStateDto(
                room.RoomId,
                room.State.ToString(),
                room.OwnerUid,
                members);
        }

        BattleStateDto? battleState = null;
        if (currentScene is BattleWorld world && world.ActiveSession is { } session)
        {
            var entities = new List<BattleEntityDto>(session.Entities.Count);
            foreach (var entity in session.Entities.Values.OrderBy(e => e.EntityId))
            {
                Vector2 godotPos;
                if (world.PlayerNodes.TryGetValue(entity.EntityId, out var motor) && GodotObject.IsInstanceValid(motor))
                    godotPos = motor.GlobalPosition;
                else
                    godotPos = world.ActiveMap.WorldToGodot(entity.PosX, entity.PosY);

                entities.Add(new BattleEntityDto(
                    entity.EntityId,
                    entity.PlayerUid,
                    entity.Type.ToString(),
                    entity.PosX,
                    entity.PosY,
                    godotPos.X,
                    godotPos.Y,
                    entity.Facing,
                    entity.State.ToString(),
                    entity.Hp));
            }

            battleState = new BattleStateDto(
                session.CurrentMapId,
                session.LastSnapshotTick,
                session.LastAckedInputSeq,
                world.LocalPlayerEntityId,
                entities);
        }

        var uiState = CollectUiState(tree?.Root);
        return new StateResponse(
            true,
            _config.InstanceId,
            scenePath,
            procedureName,
            networkState,
            roomState,
            battleState,
            uiState);
    }

    private (int StatusCode, object Payload) ExecuteActionOnMainThread(string actionType, ActionRequest req)
    {
        return actionType switch
        {
            "click_button" => ExecuteClickButton(req),
            "set_text" => ExecuteSetText(req),
            "set_input" => ExecuteSetInput(req),
            "clear_input" => ExecuteClearInput(),
            _ => (400, new ErrorResponse(false, $"Unsupported action type: {actionType}")),
        };
    }

    private (int StatusCode, object Payload) ExecuteClickButton(ActionRequest req)
    {
        var hasName = !string.IsNullOrWhiteSpace(req.Name);
        var hasText = !string.IsNullOrWhiteSpace(req.Text);
        if (!hasName && !hasText)
            return (400, new ErrorResponse(false, "click_button requires name or text"));

        var buttons = new List<BaseButton>();
        CollectVisibleNodes(GetTree()?.Root, buttons, b => !b.Disabled);

        BaseButton? matched = null;
        if (hasName)
        {
            var targetName = req.Name!.Trim();
            matched = buttons.FirstOrDefault(b =>
                string.Equals(b.Name.ToString(), targetName, StringComparison.Ordinal)
                && (!hasText || MatchesButtonText(b, req.Text!.Trim(), exactOnly: false)));
        }
        else if (hasText)
        {
            var targetText = req.Text!.Trim();
            matched = buttons.FirstOrDefault(b => MatchesButtonText(b, targetText, exactOnly: true))
                ?? buttons.FirstOrDefault(b => MatchesButtonText(b, targetText, exactOnly: false));
        }

        if (matched == null)
            return (404, new ErrorResponse(false, $"No visible enabled button matched (name='{req.Name}', text='{req.Text}')"));

        if (matched.ToggleMode)
            matched.ButtonPressed = !matched.ButtonPressed;
        matched.EmitSignal(BaseButton.SignalName.Pressed);

        return (200, new ActionResult(
            true,
            "click_button",
            matched.Name.ToString(),
            GetButtonText(matched),
            matched.GetPath().ToString(),
            null,
            null));
    }

    private (int StatusCode, object Payload) ExecuteSetText(ActionRequest req)
    {
        var hasName = !string.IsNullOrWhiteSpace(req.Name);
        var hasPlaceholder = !string.IsNullOrWhiteSpace(req.Placeholder);
        if (!hasName && !hasPlaceholder)
            return (400, new ErrorResponse(false, "set_text requires name or placeholder"));

        var value = req.Text ?? "";
        var inputs = new List<Control>();
        CollectVisibleNodes(GetTree()?.Root, inputs, n => n is LineEdit or TextEdit);

        Control? matched = null;
        if (hasName)
        {
            var targetName = req.Name!.Trim();
            matched = inputs.FirstOrDefault(c => string.Equals(c.Name.ToString(), targetName, StringComparison.Ordinal));
        }
        else if (hasPlaceholder)
        {
            var targetPlaceholder = req.Placeholder!.Trim();
            matched = inputs.FirstOrDefault(c => MatchesPlaceholder(c, targetPlaceholder, exactOnly: true))
                ?? inputs.FirstOrDefault(c => MatchesPlaceholder(c, targetPlaceholder, exactOnly: false));
        }

        if (matched == null)
            return (404, new ErrorResponse(false, $"No visible text input matched (name='{req.Name}', placeholder='{req.Placeholder}')"));

        if (matched is LineEdit lineEdit)
        {
            lineEdit.Text = value;
            lineEdit.EmitSignal(LineEdit.SignalName.TextChanged, value);
        }
        else if (matched is TextEdit textEdit)
        {
            textEdit.Text = value;
            textEdit.EmitSignal(TextEdit.SignalName.TextChanged);
        }

        return (200, new ActionResult(
            true,
            "set_text",
            matched.Name.ToString(),
            value,
            matched.GetPath().ToString(),
            null,
            null));
    }

    private (int StatusCode, object Payload) ExecuteSetInput(ActionRequest req)
    {
        var localInput = FindActiveLocalInput();
        if (localInput == null)
            return (404, new ErrorResponse(false, "No active LocalPlayerInput found in current scene"));

        var move = new Vector2(req.MoveX ?? 0f, req.MoveY ?? 0f);
        float? aimRadians = req.AimDeg.HasValue && float.IsFinite(req.AimDeg.Value)
            ? Mathf.DegToRad(req.AimDeg.Value)
            : null;
        double? durationSeconds = req.DurationMs is > 0
            ? req.DurationMs.Value / 1000.0
            : null;

        localInput.SetSyntheticInput(
            move,
            aimRadians,
            req.Attack ?? false,
            req.Interact ?? false,
            durationSeconds);

        return (200, new ActionResult(true, "set_input", null, null, null, null, null));
    }

    private (int StatusCode, object Payload) ExecuteClearInput()
    {
        var localInput = FindActiveLocalInput();
        localInput?.ClearSyntheticInput();
        return (200, new ActionResult(true, "clear_input", null, null, null, localInput != null, null));
    }

    private (int StatusCode, object Payload) CaptureScreenshotOnMainThread(string targetPath)
    {
        var texture = GetViewport()?.GetTexture();
        if (texture == null)
            return (500, new ErrorResponse(false, "Viewport texture is unavailable"));

        using var image = texture.GetImage();
        if (image == null)
            return (500, new ErrorResponse(false, "Failed to capture viewport image"));

        var err = image.SavePng(targetPath);
        if (err != Godot.Error.Ok)
            return (500, new ErrorResponse(false, $"SavePng failed: {err}"));

        return (200, new ScreenshotResponse(true, targetPath, image.GetWidth(), image.GetHeight()));
    }

    private LocalPlayerInput? FindActiveLocalInput()
    {
        var currentScene = GetTree()?.CurrentScene;
        if (currentScene is BattleWorld world && world.LocalInput != null && GodotObject.IsInstanceValid(world.LocalInput))
            return world.LocalInput;

        return currentScene != null ? FindFirstNode<LocalPlayerInput>(currentScene) : null;
    }

    private static T? FindFirstNode<T>(Node root) where T : Node
    {
        if (root is T match)
            return match;

        var count = root.GetChildCount();
        for (var i = 0; i < count; i++)
        {
            var found = FindFirstNode<T>(root.GetChild(i));
            if (found != null)
                return found;
        }

        return null;
    }

    private static UiStateDto CollectUiState(Node? root)
    {
        var buttons = new List<UiButtonDto>();
        var inputs = new List<UiInputDto>();
        var labels = new List<UiLabelDto>();

        if (root != null)
            TraverseVisibleUi(root, buttons, inputs, labels);

        return new UiStateDto(buttons, inputs, labels);
    }

    private static void TraverseVisibleUi(
        Node node,
        List<UiButtonDto> buttons,
        List<UiInputDto> inputs,
        List<UiLabelDto> labels)
    {
        if (!IsNodeVisibleBranch(node))
            return;

        if (node is BaseButton btn && buttons.Count < MaxUiItemsPerCategory)
        {
            buttons.Add(new UiButtonDto(
                btn.Name.ToString(),
                GetButtonText(btn),
                btn.GetPath().ToString(),
                btn.Disabled));
        }
        else if (node is LineEdit lineEdit && inputs.Count < MaxUiItemsPerCategory)
        {
            inputs.Add(new UiInputDto(
                lineEdit.Name.ToString(),
                lineEdit.PlaceholderText,
                lineEdit.Text,
                lineEdit.GetPath().ToString()));
        }
        else if (node is TextEdit textEdit && inputs.Count < MaxUiItemsPerCategory)
        {
            inputs.Add(new UiInputDto(
                textEdit.Name.ToString(),
                textEdit.PlaceholderText,
                textEdit.Text,
                textEdit.GetPath().ToString()));
        }
        else if (node is Label label && labels.Count < MaxUiItemsPerCategory)
        {
            labels.Add(new UiLabelDto(
                label.Name.ToString(),
                label.Text));
        }

        var count = node.GetChildCount();
        for (var i = 0; i < count; i++)
            TraverseVisibleUi(node.GetChild(i), buttons, inputs, labels);
    }

    private static void CollectVisibleNodes<T>(Node? node, List<T> results, Func<T, bool> predicate)
        where T : Node
    {
        if (node == null || !IsNodeVisibleBranch(node))
            return;

        if (node is T typed && predicate(typed))
            results.Add(typed);

        var count = node.GetChildCount();
        for (var i = 0; i < count; i++)
            CollectVisibleNodes(node.GetChild(i), results, predicate);
    }

    private static bool IsNodeVisibleBranch(Node node)
    {
        if (node is CanvasItem canvasItem && !canvasItem.Visible)
            return false;
        if (node is CanvasLayer canvasLayer && !canvasLayer.Visible)
            return false;
        if (node is Window window && !window.Visible)
            return false;
        return true;
    }

    private static string GetButtonText(BaseButton button)
        => button is Button b ? b.Text ?? "" : "";

    private static bool MatchesButtonText(BaseButton button, string target, bool exactOnly)
    {
        var text = GetButtonText(button);
        if (string.IsNullOrEmpty(text))
            return false;

        return exactOnly
            ? string.Equals(text, target, StringComparison.Ordinal)
            : text.Contains(target, StringComparison.OrdinalIgnoreCase);
    }

    private static bool MatchesPlaceholder(Control control, string target, bool exactOnly)
    {
        var placeholder = control switch
        {
            LineEdit le => le.PlaceholderText ?? "",
            TextEdit te => te.PlaceholderText ?? "",
            _ => "",
        };

        if (string.IsNullOrEmpty(placeholder))
            return false;

        return exactOnly
            ? string.Equals(placeholder, target, StringComparison.Ordinal)
            : placeholder.Contains(target, StringComparison.OrdinalIgnoreCase);
    }

    // ---------------------------------------------------------------- HTTP I/O Helpers

    private static async Task<string> ReadBodyAsync(HttpListenerRequest request)
    {
        if (!request.HasEntityBody)
            return "";

        using var reader = new StreamReader(request.InputStream, request.ContentEncoding ?? Encoding.UTF8);
        return await reader.ReadToEndAsync().ConfigureAwait(false);
    }

    private static async Task WriteJsonAsync(HttpListenerResponse response, int statusCode, object payload)
    {
        var bytes = JsonSerializer.SerializeToUtf8Bytes(payload, JsonOptions);
        response.StatusCode = statusCode;
        response.ContentType = "application/json; charset=utf-8";
        response.ContentLength64 = bytes.Length;
        await response.OutputStream.WriteAsync(bytes).ConfigureAwait(false);
        response.OutputStream.Close();
    }

    private static async Task TryWriteErrorAsync(HttpListenerResponse response, int statusCode, string message)
    {
        try
        {
            await WriteJsonAsync(response, statusCode, new ErrorResponse(false, message)).ConfigureAwait(false);
        }
        catch
        {
            // Client already disconnected or response already closed.
        }
    }

    // ---------------------------------------------------------------- DTOs

    private sealed record ErrorResponse(
        [property: JsonPropertyName("ok")] bool Ok,
        [property: JsonPropertyName("error")] string Error);

    private sealed record HealthResponse(
        [property: JsonPropertyName("ok")] bool Ok,
        [property: JsonPropertyName("instance_id")] string InstanceId,
        [property: JsonPropertyName("pid")] int Pid);

    private sealed record NetworkStateDto(
        [property: JsonPropertyName("state")] string State,
        [property: JsonPropertyName("uid")] ulong Uid,
        [property: JsonPropertyName("name")] string Name);

    private sealed record RoomMemberDto(
        [property: JsonPropertyName("uid")] ulong Uid,
        [property: JsonPropertyName("name")] string Name,
        [property: JsonPropertyName("ready")] bool Ready);

    private sealed record RoomStateDto(
        [property: JsonPropertyName("room_id")] uint RoomId,
        [property: JsonPropertyName("state")] string State,
        [property: JsonPropertyName("owner_uid")] ulong OwnerUid,
        [property: JsonPropertyName("members")] List<RoomMemberDto> Members);

    private sealed record BattleEntityDto(
        [property: JsonPropertyName("entity_id")] uint EntityId,
        [property: JsonPropertyName("player_uid")] ulong PlayerUid,
        [property: JsonPropertyName("type")] string Type,
        [property: JsonPropertyName("world_x")] float WorldX,
        [property: JsonPropertyName("world_y")] float WorldY,
        [property: JsonPropertyName("godot_x")] float GodotX,
        [property: JsonPropertyName("godot_y")] float GodotY,
        [property: JsonPropertyName("facing")] float Facing,
        [property: JsonPropertyName("state")] string State,
        [property: JsonPropertyName("hp")] int Hp);

    private sealed record BattleStateDto(
        [property: JsonPropertyName("map_id")] uint MapId,
        [property: JsonPropertyName("last_tick")] uint LastTick,
        [property: JsonPropertyName("last_input_seq")] uint LastInputSeq,
        [property: JsonPropertyName("local_entity_id")] uint LocalEntityId,
        [property: JsonPropertyName("entities")] List<BattleEntityDto> Entities);

    private sealed record UiButtonDto(
        [property: JsonPropertyName("name")] string Name,
        [property: JsonPropertyName("text")] string Text,
        [property: JsonPropertyName("path")] string Path,
        [property: JsonPropertyName("disabled")] bool Disabled);

    private sealed record UiInputDto(
        [property: JsonPropertyName("name")] string Name,
        [property: JsonPropertyName("placeholder")] string Placeholder,
        [property: JsonPropertyName("text")] string Text,
        [property: JsonPropertyName("path")] string Path);

    private sealed record UiLabelDto(
        [property: JsonPropertyName("name")] string Name,
        [property: JsonPropertyName("text")] string Text);

    private sealed record UiStateDto(
        [property: JsonPropertyName("visible_buttons")] List<UiButtonDto> VisibleButtons,
        [property: JsonPropertyName("visible_inputs")] List<UiInputDto> VisibleInputs,
        [property: JsonPropertyName("visible_labels")] List<UiLabelDto> VisibleLabels);

    private sealed record StateResponse(
        [property: JsonPropertyName("ok")] bool Ok,
        [property: JsonPropertyName("instance_id")] string InstanceId,
        [property: JsonPropertyName("scene")] string Scene,
        [property: JsonPropertyName("procedure")] string Procedure,
        [property: JsonPropertyName("network")] NetworkStateDto Network,
        [property: JsonPropertyName("room")] RoomStateDto? Room,
        [property: JsonPropertyName("battle")] BattleStateDto? Battle,
        [property: JsonPropertyName("ui")] UiStateDto Ui);

    private sealed class ActionRequest
    {
        [JsonPropertyName("type")] public string? Type { get; set; }
        [JsonPropertyName("name")] public string? Name { get; set; }
        [JsonPropertyName("text")] public string? Text { get; set; }
        [JsonPropertyName("placeholder")] public string? Placeholder { get; set; }
        [JsonPropertyName("move_x")] public float? MoveX { get; set; }
        [JsonPropertyName("move_y")] public float? MoveY { get; set; }
        [JsonPropertyName("aim_deg")] public float? AimDeg { get; set; }
        [JsonPropertyName("attack")] public bool? Attack { get; set; }
        [JsonPropertyName("interact")] public bool? Interact { get; set; }
        [JsonPropertyName("duration_ms")] public int? DurationMs { get; set; }
    }

    private sealed record ActionResult(
        [property: JsonPropertyName("ok")] bool Ok,
        [property: JsonPropertyName("action")] string Action,
        [property: JsonPropertyName("name"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] string? Name,
        [property: JsonPropertyName("text"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] string? Text,
        [property: JsonPropertyName("path"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] string? Path,
        [property: JsonPropertyName("cleared"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] bool? Cleared,
        [property: JsonPropertyName("error"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] string? Error);

    private sealed class ScreenshotRequest
    {
        [JsonPropertyName("path")] public string? Path { get; set; }
    }

    private sealed record ScreenshotResponse(
        [property: JsonPropertyName("ok")] bool Ok,
        [property: JsonPropertyName("path")] string Path,
        [property: JsonPropertyName("width")] int Width,
        [property: JsonPropertyName("height")] int Height);
}
