package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultLaunchCount     = 2
	maxLaunchCount         = 4
	defaultBasePort        = 19100
	defaultWaitReadySec    = 20.0
	clientHTTPTimeout      = 10 * time.Second
	clientHealthTimeout    = 2 * time.Second
	clientStopQuitTimeout  = 1500 * time.Millisecond
	clientStopWaitGrace    = 2 * time.Second
	clientLogTailMaxLines  = 40
	clientLogTailMaxBytes  = 4096
)

var defaultWindowPositions = []string{
	"60,80",
	"1040,80",
	"60,640",
	"1040,640",
}

type managedClient struct {
	ID        string `json:"id"`
	PID       int    `json:"pid"`
	Port      int    `json:"port"`
	LogPath   string `json:"log_path"`
	Position  string `json:"position,omitempty"`
	StartedAt string `json:"started_at"`
}

func (c managedClient) baseURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", c.Port)
}

type clientRegistry struct {
	Instances []managedClient `json:"instances"`
}

type clientManager struct {
	mu           sync.Mutex
	rootFn       func() string
	cache        map[string]managedClient
	runningProcs map[int]chan struct{}
	httpClient   *http.Client
	healthClient *http.Client
	quitClient   *http.Client
}

func newClientManager(rootFn func() string) *clientManager {
	return &clientManager{
		rootFn:       rootFn,
		cache:        make(map[string]managedClient),
		runningProcs: make(map[int]chan struct{}),
		httpClient:   &http.Client{Timeout: clientHTTPTimeout},
		healthClient: &http.Client{Timeout: clientHealthTimeout},
		quitClient:   &http.Client{Timeout: clientStopQuitTimeout},
	}
}

var defaultClientMgr = newClientManager(func() string { return repoRoot })

func (m *clientManager) devClientsDir() string {
	return filepath.Join(m.rootFn(), ".tmp", "dev-clients")
}

func (m *clientManager) registryPath() string {
	return filepath.Join(m.devClientsDir(), "registry.json")
}

func (m *clientManager) screenshotsDir() string {
	return filepath.Join(m.devClientsDir(), "screenshots")
}

func (m *clientManager) loadRegistryLocked() (map[string]managedClient, error) {
	path := m.registryPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			m.cache = make(map[string]managedClient)
			return m.copyCacheLocked(), nil
		}
		return nil, fmt.Errorf("读取实例注册表失败 (%s): %w", path, err)
	}
	var reg clientRegistry
	if len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, &reg); err != nil {
			return nil, fmt.Errorf("解析实例注册表失败 (%s): %w", path, err)
		}
	}
	next := make(map[string]managedClient, len(reg.Instances))
	for _, inst := range reg.Instances {
		if inst.ID != "" {
			next[inst.ID] = inst
		}
	}
	m.cache = next
	return m.copyCacheLocked(), nil
}

func (m *clientManager) copyCacheLocked() map[string]managedClient {
	out := make(map[string]managedClient, len(m.cache))
	for k, v := range m.cache {
		out[k] = v
	}
	return out
}

func (m *clientManager) saveRegistryLocked(instances map[string]managedClient) error {
	dir := m.devClientsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建 dev-clients 目录失败: %w", err)
	}
	list := make([]managedClient, 0, len(instances))
	for _, inst := range instances {
		list = append(list, inst)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Port != list[j].Port {
			return list[i].Port < list[j].Port
		}
		return list[i].ID < list[j].ID
	})
	data, err := json.MarshalIndent(clientRegistry{Instances: list}, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化实例注册表失败: %w", err)
	}
	data = append(data, '\n')
	path := m.registryPath()
	tmp := fmt.Sprintf("%s.tmp.%d", path, time.Now().UnixNano())
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("写入临时注册表失败: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("替换注册表文件失败: %w", err)
	}
	m.cache = make(map[string]managedClient, len(instances))
	for k, v := range instances {
		m.cache[k] = v
	}
	return nil
}

func (m *clientManager) getAllInstances() ([]managedClient, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	reg, err := m.loadRegistryLocked()
	if err != nil {
		return nil, err
	}
	list := make([]managedClient, 0, len(reg))
	for _, inst := range reg {
		list = append(list, inst)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Port != list[j].Port {
			return list[i].Port < list[j].Port
		}
		return list[i].ID < list[j].ID
	})
	return list, nil
}

func (m *clientManager) getInstance(id string) (managedClient, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	reg, err := m.loadRegistryLocked()
	if err != nil {
		return managedClient{}, err
	}
	inst, ok := reg[id]
	if !ok {
		avail := make([]string, 0, len(reg))
		for k := range reg {
			avail = append(avail, k)
		}
		sort.Strings(avail)
		if len(avail) == 0 {
			return managedClient{}, fmt.Errorf("未找到实例 %q：当前没有已注册的客户端实例（请先调用 clients_launch）", id)
		}
		return managedClient{}, fmt.Errorf("未找到实例 %q：当前可用实例为 [%s]", id, strings.Join(avail, ", "))
	}
	return inst, nil
}

func (m *clientManager) watchProcess(pid int, cmd *exec.Cmd) {
	done := make(chan struct{})
	m.mu.Lock()
	m.runningProcs[pid] = done
	m.mu.Unlock()

	go func() {
		_ = cmd.Wait()
		close(done)
	}()
}

func (m *clientManager) isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	m.mu.Lock()
	done, tracked := m.runningProcs[pid]
	m.mu.Unlock()
	if tracked {
		select {
		case <-done:
			return false
		default:
			return true
		}
	}
	return osProcessAlive(pid)
}

func (m *clientManager) waitProcessExit(pid int, timeout time.Duration) bool {
	if pid <= 0 {
		return true
	}
	m.mu.Lock()
	done, tracked := m.runningProcs[pid]
	m.mu.Unlock()
	if tracked {
		select {
		case <-done:
			return true
		case <-time.After(timeout):
			return false
		}
	}
	deadline := time.Now().Add(timeout)
	for {
		if !osProcessAlive(pid) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func killProcessByPID(pid int) error {
	if pid <= 0 {
		return nil
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Kill()
}

// ---------- Godot .NET / Mono 可执行文件解析 ----------

type godotMonoResolverDeps struct {
	goos         string
	homeDir      string
	envPath      string
	lookPath     func(string) (string, error)
	stat         func(string) (os.FileInfo, error)
	readDir      func(string) ([]os.DirEntry, error)
	evalSymlinks func(string) (string, error)
}

func resolveGodotMonoExe(explicit string) (string, error) {
	home, _ := os.UserHomeDir()
	return resolveGodotMonoWith(explicit, godotMonoResolverDeps{
		goos:         runtime.GOOS,
		homeDir:      home,
		envPath:      os.Getenv("KNIGHT_GODOT_PATH"),
		lookPath:     exec.LookPath,
		stat:         os.Stat,
		readDir:      os.ReadDir,
		evalSymlinks: filepath.EvalSymlinks,
	})
}

func resolveGodotMonoWith(explicit string, deps godotMonoResolverDeps) (string, error) {
	isFile := func(p string) bool {
		if p == "" {
			return false
		}
		info, err := deps.stat(p)
		return err == nil && !info.IsDir()
	}
	// 解析符号链接：macOS 上通过 .app 外部的 symlink（如 ~/bin/godot-mono）启动 Godot Mono 时，
	// .NET 模块无法按 symlink 相对路径找到 Contents/Resources/GodotSharp，会卡在 NSAlert。
	resolveLink := func(p string) string {
		eval := deps.evalSymlinks
		if eval == nil {
			eval = filepath.EvalSymlinks
		}
		if realPath, err := eval(p); err == nil && realPath != "" {
			return realPath
		}
		return p
	}

	if explicit != "" {
		if isFile(explicit) {
			return resolveLink(explicit), nil
		}
		return "", fmt.Errorf("godot_path 指定的文件不存在: %s", explicit)
	}
	if deps.envPath != "" {
		if isFile(deps.envPath) {
			return resolveLink(deps.envPath), nil
		}
		return "", fmt.Errorf("KNIGHT_GODOT_PATH 指定的文件不存在: %s", deps.envPath)
	}

	var checked []string
	switch deps.goos {
	case "darwin":
		candidates := []string{}
		if deps.homeDir != "" {
			candidates = append(candidates,
				filepath.Join(deps.homeDir, "bin", "godot-mono"),
				filepath.Join(deps.homeDir, "Desktop", "Godot_mono.app", "Contents", "MacOS", "Godot"),
			)
		}
		candidates = append(candidates, "/Applications/Godot_mono.app/Contents/MacOS/Godot")
		for _, c := range candidates {
			checked = append(checked, c)
			if isFile(c) {
				return resolveLink(c), nil
			}
		}
		checked = append(checked, "PATH:godot-mono")
		if p, err := deps.lookPath("godot-mono"); err == nil && p != "" {
			return resolveLink(p), nil
		}

	case "windows":
		for _, name := range []string{"godot-mono", "godot-mono.exe"} {
			checked = append(checked, "PATH:"+name)
			if p, err := deps.lookPath(name); err == nil && p != "" {
				return resolveLink(p), nil
			}
		}
		if deps.homeDir != "" {
			homeBin := filepath.Join(deps.homeDir, "bin", "godot-mono.exe")
			checked = append(checked, homeBin)
			if isFile(homeBin) {
				return resolveLink(homeBin), nil
			}
		}
		roots := []string{
			`D:\Godot`,
			`C:\Program Files\Godot`,
			`D:\Program Files\Godot`,
			`C:\Godot`,
		}
		for _, root := range roots {
			checked = append(checked, root)
			if exe, ok := findGodotMonoWindowsInRoot(root, deps.readDir); ok {
				return resolveLink(exe), nil
			}
		}

	default:
		if deps.homeDir != "" {
			homeBin := filepath.Join(deps.homeDir, "bin", "godot-mono")
			checked = append(checked, homeBin)
			if isFile(homeBin) {
				return resolveLink(homeBin), nil
			}
		}
		checked = append(checked, "PATH:godot-mono")
		if p, err := deps.lookPath("godot-mono"); err == nil && p != "" {
			return resolveLink(p), nil
		}
	}

	return "", fmt.Errorf("未找到 Godot .NET/mono 可执行文件（已检查: %s）；请通过 godot_path 参数或 KNIGHT_GODOT_PATH 环境变量指定",
		strings.Join(checked, ", "))
}

// findGodotMonoWindowsInRoot 在 Windows Godot 安装根目录中仅查找 mono 版本可执行文件，
// 优先 *_console.exe，绝不退回到非 mono 二进制。
func findGodotMonoWindowsInRoot(root string, readDir func(string) ([]os.DirEntry, error)) (string, bool) {
	entries, err := readDir(root)
	if err != nil {
		return "", false
	}
	// 先检查根目录自身
	if exe, ok := pickGodotMonoExeInDir(root, entries, strings.Contains(strings.ToLower(root), "mono")); ok {
		return exe, true
	}
	// 按名称倒序扫描子目录（优先新版本），先找目录名含 mono 的
	var monoDirs, otherDirs []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sub := filepath.Join(root, e.Name())
		if strings.Contains(strings.ToLower(e.Name()), "mono") {
			monoDirs = append(monoDirs, sub)
		} else {
			otherDirs = append(otherDirs, sub)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(monoDirs)))
	sort.Sort(sort.Reverse(sort.StringSlice(otherDirs)))

	for _, dir := range monoDirs {
		subEntries, err := readDir(dir)
		if err != nil {
			continue
		}
		if exe, ok := pickGodotMonoExeInDir(dir, subEntries, true); ok {
			return exe, true
		}
	}
	for _, dir := range otherDirs {
		subEntries, err := readDir(dir)
		if err != nil {
			continue
		}
		if exe, ok := pickGodotMonoExeInDir(dir, subEntries, false); ok {
			return exe, true
		}
	}
	return "", false
}

func pickGodotMonoExeInDir(dir string, entries []os.DirEntry, dirIsMono bool) (string, bool) {
	var consoleExe, guiExe string
	for _, f := range entries {
		if f.IsDir() {
			continue
		}
		name := f.Name()
		lower := strings.ToLower(name)
		if !strings.HasPrefix(lower, "godot") || !strings.HasSuffix(lower, ".exe") {
			continue
		}
		if !dirIsMono && !strings.Contains(lower, "mono") {
			continue
		}
		full := filepath.Join(dir, name)
		if strings.HasSuffix(lower, "_console.exe") {
			if consoleExe == "" {
				consoleExe = full
			}
		} else if guiExe == "" {
			guiExe = full
		}
	}
	if consoleExe != "" {
		return consoleExe, true
	}
	if guiExe != "" {
		return guiExe, true
	}
	return "", false
}

// ---------- HTTP 辅助 ----------

func doClientJSON(client *http.Client, method, url string, payload any) (int, map[string]any, string, error) {
	var bodyReader io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, "", fmt.Errorf("编码请求体失败: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, url, bodyReader)
	if err != nil {
		return 0, nil, "", err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, "", err
	}
	defer resp.Body.Close()
	rawBytes, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return resp.StatusCode, nil, "", err
	}
	raw := strings.TrimSpace(string(rawBytes))
	var parsed map[string]any
	if len(rawBytes) > 0 {
		_ = json.Unmarshal(rawBytes, &parsed)
	}
	return resp.StatusCode, parsed, raw, nil
}

func probeHealth(client *http.Client, baseURL string) (bool, map[string]any, string) {
	code, parsed, raw, err := doClientJSON(client, http.MethodGet, baseURL+"/health", nil)
	if err != nil {
		return false, nil, err.Error()
	}
	if code != http.StatusOK {
		return false, parsed, fmt.Sprintf("HTTP %d: %s", code, raw)
	}
	if okVal, _ := parsed["ok"].(bool); !okVal {
		return false, parsed, fmt.Sprintf("health 返回 ok=false: %s", raw)
	}
	return true, parsed, ""
}

func readLogTail(path string, maxLines, maxBytes int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("<无法读取日志 %s: %v>", path, err)
	}
	if len(data) == 0 {
		return "<日志为空>"
	}
	if len(data) > maxBytes {
		data = data[len(data)-maxBytes:]
	}
	lines := strings.Split(strings.TrimRight(string(data), "\r\n"), "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return strings.Join(lines, "\n")
}

func marshalPretty(v any) (string, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ---------- 参数解析辅助 ----------

func parseInstanceIDs(args map[string]any) ([]string, error) {
	rawIDs, hasIDs := args["ids"]
	rawCount, hasCount := args["count"]

	var ids []string
	if hasIDs && rawIDs != nil {
		arr, ok := rawIDs.([]any)
		if !ok {
			return nil, errors.New("参数 ids 必须是字符串数组")
		}
		if len(arr) > 0 {
			seen := make(map[string]bool, len(arr))
			for i, item := range arr {
				s, ok := item.(string)
				if !ok {
					return nil, fmt.Errorf("参数 ids[%d] 必须是字符串", i)
				}
				s = strings.TrimSpace(s)
				if err := validateInstanceID(s); err != nil {
					return nil, err
				}
				if seen[s] {
					return nil, fmt.Errorf("参数 ids 包含重复项: %q", s)
				}
				seen[s] = true
				ids = append(ids, s)
			}
		}
	}

	if len(ids) > 0 {
		if len(ids) > maxLaunchCount {
			return nil, fmt.Errorf("最多启动 %d 个实例，当前 ids 数量为 %d", maxLaunchCount, len(ids))
		}
		if hasCount && rawCount != nil {
			c, err := parseIntArg(rawCount, "count")
			if err != nil {
				return nil, err
			}
			if c != len(ids) {
				return nil, fmt.Errorf("参数 count=%d 与 ids 长度 %d 不一致", c, len(ids))
			}
		}
		return ids, nil
	}

	count := defaultLaunchCount
	if hasCount && rawCount != nil {
		c, err := parseIntArg(rawCount, "count")
		if err != nil {
			return nil, err
		}
		count = c
	}
	if count < 1 || count > maxLaunchCount {
		return nil, fmt.Errorf("参数 count 必须在 1..%d 之间，当前为 %d", maxLaunchCount, count)
	}
	ids = make([]string, count)
	for i := 0; i < count; i++ {
		ids[i] = fmt.Sprintf("p%d", i+1)
	}
	return ids, nil
}

func validateInstanceID(id string) error {
	if id == "" {
		return errors.New("实例 id 不能为空")
	}
	if id == "all" {
		return errors.New("实例 id 不能为保留字 \"all\"")
	}
	if strings.ContainsAny(id, `/\:*?"<>| `) || strings.Contains(id, "..") {
		return fmt.Errorf("实例 id %q 包含非法字符", id)
	}
	return nil
}

func parseIntArg(v any, name string) (int, error) {
	switch n := v.(type) {
	case float64:
		if n != float64(int(n)) {
			return 0, fmt.Errorf("参数 %s 必须是整数，当前为 %v", name, v)
		}
		return int(n), nil
	case int:
		return n, nil
	case json.Number:
		i, err := strconv.Atoi(string(n))
		if err != nil {
			return 0, fmt.Errorf("参数 %s 必须是整数: %w", name, err)
		}
		return i, nil
	default:
		return 0, fmt.Errorf("参数 %s 必须是整数", name)
	}
}

// ---------- 6 个 MCP 工具实现 ----------

func toolClientsLaunch(args map[string]any) (string, error) {
	return defaultClientMgr.launch(args)
}

func toolClientsList(args map[string]any) (string, error) {
	return defaultClientMgr.list(args)
}

func toolClientState(args map[string]any) (string, error) {
	return defaultClientMgr.state(args)
}

func toolClientAct(args map[string]any) (string, error) {
	return defaultClientMgr.act(args)
}

func toolClientScreenshot(args map[string]any) (string, error) {
	return defaultClientMgr.screenshot(args)
}

func toolClientsStop(args map[string]any) (string, error) {
	return defaultClientMgr.stop(args)
}

func (m *clientManager) launch(args map[string]any) (string, error) {
	ids, err := parseInstanceIDs(args)
	if err != nil {
		return "", err
	}

	basePort := defaultBasePort
	if rawPort, ok := args["base_port"]; ok && rawPort != nil {
		p, err := parseIntArg(rawPort, "base_port")
		if err != nil {
			return "", err
		}
		basePort = p
	}
	if basePort < 1024 || basePort+len(ids)-1 > 65535 {
		return "", fmt.Errorf("base_port %d 超出合法端口范围 (1024..%d)", basePort, 65535-len(ids)+1)
	}

	waitReadySec := argNumber(args, "wait_ready_sec", defaultWaitReadySec)
	godotPath, err := resolveGodotMonoExe(argString(args, "godot_path", ""))
	if err != nil {
		return "", err
	}

	clientDir := filepath.Join(m.rootFn(), "Knight-Client")
	if info, err := os.Stat(clientDir); err != nil || !info.IsDir() {
		return "", fmt.Errorf("找不到客户端工程目录: %s", clientDir)
	}
	if err := os.MkdirAll(m.devClientsDir(), 0o755); err != nil {
		return "", fmt.Errorf("创建日志目录失败: %w", err)
	}

	// 若目标 id 已存在且进程存活，先停止旧实例避免端口冲突与孤儿进程。
	m.mu.Lock()
	existingReg, err := m.loadRegistryLocked()
	m.mu.Unlock()
	if err != nil {
		return "", err
	}
	for _, id := range ids {
		if old, ok := existingReg[id]; ok {
			_ = m.stopSingleInstance(old)
		}
	}

	startTime := time.Now()
	launched := make([]managedClient, 0, len(ids))
	for i, id := range ids {
		port := basePort + i
		pos := defaultWindowPositions[i%len(defaultWindowPositions)]
		logPath := filepath.Join(m.devClientsDir(), id+".log")

		logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return "", fmt.Errorf("创建实例 %s 日志文件失败 (%s): %w", id, logPath, err)
		}

		cmdArgs := []string{
			"--path", clientDir,
			"--position", pos,
			"--",
			fmt.Sprintf("--instance-id=%s", id),
			fmt.Sprintf("--dev-agent-port=%d", port),
		}
		cmd := exec.Command(godotPath, cmdArgs...)
		cmd.Dir = clientDir
		cmd.Stdout = logFile
		cmd.Stderr = logFile

		if err := cmd.Start(); err != nil {
			_ = logFile.Close()
			return "", fmt.Errorf("启动实例 %s 失败: %w", id, err)
		}
		_ = logFile.Close()
		pid := cmd.Process.Pid
		m.watchProcess(pid, cmd)

		launched = append(launched, managedClient{
			ID:        id,
			PID:       pid,
			Port:      port,
			LogPath:   logPath,
			Position:  pos,
			StartedAt: time.Now().Format(time.RFC3339),
		})
	}

	// 持久化注册表（即使后续就绪超时也保留，便于 clients_list / clients_stop 排查与清理）。
	m.mu.Lock()
	reg, err := m.loadRegistryLocked()
	if err != nil {
		m.mu.Unlock()
		return "", err
	}
	for _, inst := range launched {
		reg[inst.ID] = inst
	}
	if err := m.saveRegistryLocked(reg); err != nil {
		m.mu.Unlock()
		return "", err
	}
	m.mu.Unlock()

	// 轮询所有已启动实例的 /health 直到全部就绪或超时。
	deadline := time.Now().Add(time.Duration(waitReadySec * float64(time.Second)))
	ready := make([]bool, len(launched))
	healthInfo := make([]map[string]any, len(launched))
	lastErr := make([]string, len(launched))

	for {
		allDone := true
		for i, inst := range launched {
			if ready[i] {
				continue
			}
			ok, h, errMsg := probeHealth(m.healthClient, inst.baseURL())
			if ok {
				ready[i] = true
				healthInfo[i] = h
				lastErr[i] = ""
				continue
			}
			lastErr[i] = errMsg
			if !m.isProcessAlive(inst.PID) {
				lastErr[i] = "进程已提前退出"
				continue
			}
			allDone = false
		}
		if allDone || time.Now().After(deadline) {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}

	allReady := true
	items := make([]map[string]any, 0, len(launched))
	for i, inst := range launched {
		item := map[string]any{
			"id":       inst.ID,
			"pid":      inst.PID,
			"port":     inst.Port,
			"position": inst.Position,
			"log_path": inst.LogPath,
			"ready":    ready[i],
		}
		if ready[i] {
			item["health"] = healthInfo[i]
		} else {
			allReady = false
			item["error"] = lastErr[i]
			item["log_tail"] = readLogTail(inst.LogPath, clientLogTailMaxLines, clientLogTailMaxBytes)
		}
		items = append(items, item)
	}

	out := map[string]any{
		"ok":          allReady,
		"godot_path":  godotPath,
		"elapsed_sec": float64(int(time.Since(startTime).Seconds()*10)) / 10.0,
		"instances":   items,
	}
	text, err := marshalPretty(out)
	if err != nil {
		return "", err
	}
	if !allReady {
		return "", fmt.Errorf("部分实例在 %.0fs 内未就绪:\n%s", waitReadySec, text)
	}
	return text, nil
}

func (m *clientManager) list(_ map[string]any) (string, error) {
	instances, err := m.getAllInstances()
	if err != nil {
		return "", err
	}

	type listItem struct {
		ID        string         `json:"id"`
		PID       int            `json:"pid"`
		Port      int            `json:"port"`
		Status    string         `json:"status"`
		LogPath   string         `json:"log_path"`
		Position  string         `json:"position,omitempty"`
		StartedAt string         `json:"started_at,omitempty"`
		Health    map[string]any `json:"health,omitempty"`
		Error     string         `json:"error,omitempty"`
	}

	results := make([]listItem, len(instances))
	var wg sync.WaitGroup
	for i, inst := range instances {
		wg.Add(1)
		go func(idx int, c managedClient) {
			defer wg.Done()
			ok, h, errMsg := probeHealth(m.healthClient, c.baseURL())
			status := "running"
			if !ok {
				if m.isProcessAlive(c.PID) {
					status = "unhealthy"
				} else {
					status = "exited"
				}
			}
			item := listItem{
				ID:        c.ID,
				PID:       c.PID,
				Port:      c.Port,
				Status:    status,
				LogPath:   c.LogPath,
				Position:  c.Position,
				StartedAt: c.StartedAt,
			}
			if ok {
				item.Health = h
			} else {
				item.Error = errMsg
			}
			results[idx] = item
		}(i, inst)
	}
	wg.Wait()

	return marshalPretty(map[string]any{
		"ok":        true,
		"count":     len(results),
		"instances": results,
	})
}

func (m *clientManager) state(args map[string]any) (string, error) {
	id := strings.TrimSpace(argString(args, "id", ""))
	if id == "" {
		return "", errors.New("参数 id 不能为空（指定实例 id 或 \"all\"）")
	}

	if id == "all" {
		instances, err := m.getAllInstances()
		if err != nil {
			return "", err
		}
		states := make(map[string]any, len(instances))
		errs := make(map[string]string)
		var mu sync.Mutex
		var wg sync.WaitGroup

		for _, inst := range instances {
			wg.Add(1)
			go func(c managedClient) {
				defer wg.Done()
				st, err := m.fetchState(c)
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					errs[c.ID] = err.Error()
				} else {
					states[c.ID] = st
				}
			}(inst)
		}
		wg.Wait()

		res := map[string]any{
			"ok":     len(errs) == 0,
			"states": states,
		}
		if len(errs) > 0 {
			res["errors"] = errs
		}
		return marshalPretty(res)
	}

	inst, err := m.getInstance(id)
	if err != nil {
		return "", err
	}
	st, err := m.fetchState(inst)
	if err != nil {
		return "", err
	}
	return marshalPretty(st)
}

func (m *clientManager) fetchState(c managedClient) (map[string]any, error) {
	code, parsed, raw, err := doClientJSON(m.httpClient, http.MethodGet, c.baseURL()+"/state", nil)
	if err != nil {
		return nil, fmt.Errorf("请求实例 %s /state 失败: %w", c.ID, err)
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("实例 %s /state 返回 HTTP %d: %s", c.ID, code, raw)
	}
	if parsed == nil {
		return nil, fmt.Errorf("实例 %s /state 返回非 JSON 响应: %s", c.ID, raw)
	}
	return parsed, nil
}

func (m *clientManager) act(args map[string]any) (string, error) {
	id := strings.TrimSpace(argString(args, "id", ""))
	if id == "" {
		return "", errors.New("参数 id 不能为空")
	}
	inst, err := m.getInstance(id)
	if err != nil {
		return "", err
	}

	// 批量模式：actions 数组
	if rawActions, hasActions := args["actions"]; hasActions && rawActions != nil {
		arr, ok := rawActions.([]any)
		if !ok || len(arr) == 0 {
			return "", errors.New("参数 actions 必须是非空对象数组")
		}
		actions := make([]map[string]any, 0, len(arr))
		for i, item := range arr {
			obj, ok := item.(map[string]any)
			if !ok {
				return "", fmt.Errorf("actions[%d] 必须是 JSON 对象", i)
			}
			actType, _ := obj["type"].(string)
			if strings.TrimSpace(actType) == "" {
				return "", fmt.Errorf("actions[%d] 缺少非空字段 type", i)
			}
			actions = append(actions, obj)
		}

		results := make([]map[string]any, 0, len(actions))
		allOK := true
		for i, action := range actions {
			stepRes, ok := m.executeOneAction(inst, action)
			stepRes["index"] = i
			results = append(results, stepRes)
			if !ok {
				allOK = false
				break
			}
		}
		return marshalPretty(map[string]any{
			"ok":        allOK,
			"id":        inst.ID,
			"completed": len(results),
			"total":     len(actions),
			"results":   results,
		})
	}

	// 单动作模式
	actType := strings.TrimSpace(argString(args, "type", ""))
	if actType == "" {
		return "", errors.New("必须提供 type（单动作）或 actions（批量动作数组）")
	}
	payload := make(map[string]any, len(args))
	for k, v := range args {
		if k == "id" || k == "actions" {
			continue
		}
		payload[k] = v
	}
	stepRes, _ := m.executeOneAction(inst, payload)
	stepRes["id"] = inst.ID
	return marshalPretty(stepRes)
}

func (m *clientManager) executeOneAction(inst managedClient, action map[string]any) (map[string]any, bool) {
	actType := strings.TrimSpace(fmt.Sprint(action["type"]))
	if actType == "wait" {
		ms := 0
		if raw, ok := action["duration_ms"]; ok && raw != nil {
			if n, err := parseIntArg(raw, "duration_ms"); err == nil && n > 0 {
				ms = n
			}
		}
		if ms > 0 {
			time.Sleep(time.Duration(ms) * time.Millisecond)
		}
		return map[string]any{
			"ok":          true,
			"type":        "wait",
			"duration_ms": ms,
		}, true
	}

	code, parsed, raw, err := doClientJSON(m.httpClient, http.MethodPost, inst.baseURL()+"/action", action)
	if err != nil {
		return map[string]any{
			"ok":    false,
			"type":  actType,
			"error": err.Error(),
		}, false
	}
	if code != http.StatusOK {
		return map[string]any{
			"ok":       false,
			"type":     actType,
			"http":     code,
			"error":    raw,
			"response": parsed,
		}, false
	}
	if parsed == nil {
		return map[string]any{
			"ok":    false,
			"type":  actType,
			"error": "非 JSON 响应: " + raw,
		}, false
	}
	okVal, _ := parsed["ok"].(bool)
	return map[string]any{
		"ok":       okVal,
		"type":     actType,
		"response": parsed,
	}, okVal
}

func (m *clientManager) screenshot(args map[string]any) (string, error) {
	id := strings.TrimSpace(argString(args, "id", ""))
	if id == "" {
		return "", errors.New("参数 id 不能为空")
	}
	inst, err := m.getInstance(id)
	if err != nil {
		return "", err
	}

	outPath := strings.TrimSpace(argString(args, "path", ""))
	if outPath == "" {
		ts := time.Now().Format("20060102_150405.000")
		outPath = filepath.Join(m.screenshotsDir(), fmt.Sprintf("%s_%s.png", inst.ID, ts))
	}
	absPath, err := filepath.Abs(outPath)
	if err != nil {
		return "", fmt.Errorf("解析截图路径失败: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return "", fmt.Errorf("创建截图目录失败: %w", err)
	}

	code, parsed, raw, err := doClientJSON(m.httpClient, http.MethodPost, inst.baseURL()+"/screenshot", map[string]any{
		"path": absPath,
	})
	if err != nil {
		return "", fmt.Errorf("请求实例 %s /screenshot 失败: %w", inst.ID, err)
	}
	if code != http.StatusOK {
		return "", fmt.Errorf("实例 %s /screenshot 返回 HTTP %d: %s", inst.ID, code, raw)
	}
	if parsed == nil {
		return "", fmt.Errorf("实例 %s /screenshot 返回非 JSON 响应: %s", inst.ID, raw)
	}
	if _, hasID := parsed["instance_id"]; !hasID {
		parsed["instance_id"] = inst.ID
	}
	if _, hasPath := parsed["path"]; !hasPath {
		parsed["path"] = absPath
	}
	return marshalPretty(parsed)
}

func (m *clientManager) stop(args map[string]any) (string, error) {
	targetID := strings.TrimSpace(argString(args, "id", ""))

	m.mu.Lock()
	reg, err := m.loadRegistryLocked()
	m.mu.Unlock()
	if err != nil {
		return "", err
	}

	var targets []managedClient
	if targetID == "" || targetID == "all" {
		for _, inst := range reg {
			targets = append(targets, inst)
		}
		sort.Slice(targets, func(i, j int) bool {
			if targets[i].Port != targets[j].Port {
				return targets[i].Port < targets[j].Port
			}
			return targets[i].ID < targets[j].ID
		})
	} else {
		inst, ok := reg[targetID]
		if !ok {
			return "", fmt.Errorf("未找到实例 %q", targetID)
		}
		targets = []managedClient{inst}
	}

	type stopResult struct {
		ID     string `json:"id"`
		PID    int    `json:"pid"`
		Port   int    `json:"port"`
		Method string `json:"method"`
		Error  string `json:"error,omitempty"`
	}

	stopped := make([]stopResult, len(targets))
	var wg sync.WaitGroup
	for i, inst := range targets {
		wg.Add(1)
		go func(idx int, c managedClient) {
			defer wg.Done()
			method, stopErr := m.stopSingleInstanceWithMethod(c)
			res := stopResult{
				ID:     c.ID,
				PID:    c.PID,
				Port:   c.Port,
				Method: method,
			}
			if stopErr != nil {
				res.Error = stopErr.Error()
			}
			stopped[idx] = res
		}(i, inst)
	}
	wg.Wait()

	// 从注册表移除已停止的实例并持久化。
	m.mu.Lock()
	reg, err = m.loadRegistryLocked()
	if err != nil {
		m.mu.Unlock()
		return "", err
	}
	for _, inst := range targets {
		delete(reg, inst.ID)
	}
	if err := m.saveRegistryLocked(reg); err != nil {
		m.mu.Unlock()
		return "", err
	}
	m.mu.Unlock()

	return marshalPretty(map[string]any{
		"ok":      true,
		"stopped": stopped,
	})
}

func (m *clientManager) stopSingleInstance(inst managedClient) error {
	_, err := m.stopSingleInstanceWithMethod(inst)
	return err
}

func (m *clientManager) stopSingleInstanceWithMethod(inst managedClient) (string, error) {
	if inst.PID > 0 && !m.isProcessAlive(inst.PID) {
		return "already_exited", nil
	}

	code, _, _, httpErr := doClientJSON(m.quitClient, http.MethodPost, inst.baseURL()+"/action", map[string]any{
		"type": "quit",
	})

	if inst.PID <= 0 {
		if httpErr == nil && code == http.StatusOK {
			return "quit", nil
		}
		return "already_exited", nil
	}

	if m.waitProcessExit(inst.PID, clientStopWaitGrace) {
		return "quit", nil
	}

	if err := killProcessByPID(inst.PID); err != nil {
		if !m.isProcessAlive(inst.PID) {
			return "quit", nil
		}
		return "kill_failed", fmt.Errorf("强制终止 PID %d 失败: %w", inst.PID, err)
	}
	_ = m.waitProcessExit(inst.PID, 500*time.Millisecond)
	return "killed", nil
}
