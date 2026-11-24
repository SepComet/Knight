package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func serverPort(t *testing.T, ts *httptest.Server) int {
	t.Helper()
	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("parse httptest url %q: %v", ts.URL, err)
	}
	p, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("parse port %q: %v", u.Port(), err)
	}
	return p
}

func seedRegistry(t *testing.T, mgr *clientManager, instances ...managedClient) {
	t.Helper()
	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	m := make(map[string]managedClient, len(instances))
	for _, inst := range instances {
		m[inst.ID] = inst
	}
	if err := mgr.saveRegistryLocked(m); err != nil {
		t.Fatalf("saveRegistryLocked: %v", err)
	}
}

func TestToolsRegistered(t *testing.T) {
	want := []string{
		"clients_launch",
		"clients_list",
		"client_state",
		"client_act",
		"client_screenshot",
		"clients_stop",
	}
	for _, name := range want {
		if _, ok := findTool(name); !ok {
			t.Errorf("tool %q not registered in allTools()", name)
		}
	}
}

func TestRegistryPersistenceAcrossManagers(t *testing.T) {
	tmp := t.TempDir()
	mgr1 := newClientManager(func() string { return tmp })
	seedRegistry(t, mgr1,
		managedClient{ID: "p1", PID: 12345, Port: 19100, LogPath: "/tmp/p1.log", Position: "60,80", StartedAt: "2026-01-01T00:00:00Z"},
		managedClient{ID: "p2", PID: 12346, Port: 19101, LogPath: "/tmp/p2.log", Position: "1040,80", StartedAt: "2026-01-01T00:00:00Z"},
	)

	// 模拟 MCP 进程重启：新建独立 clientManager 从磁盘加载。
	mgr2 := newClientManager(func() string { return tmp })
	list, err := mgr2.getAllInstances()
	if err != nil {
		t.Fatalf("getAllInstances: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("got %d instances, want 2", len(list))
	}
	if list[0].ID != "p1" || list[0].Port != 19100 || list[1].ID != "p2" || list[1].Port != 19101 {
		t.Fatalf("unexpected persisted instances: %+v", list)
	}
}

func TestClientsListStatuses(t *testing.T) {
	healthySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "instance_id": "p1", "pid": os.Getpid()})
	}))
	defer healthySrv.Close()

	unhealthySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"ok":false}`))
	}))
	defer unhealthySrv.Close()

	tmp := t.TempDir()
	mgr := newClientManager(func() string { return tmp })
	seedRegistry(t, mgr,
		managedClient{ID: "p1", PID: os.Getpid(), Port: serverPort(t, healthySrv), LogPath: filepath.Join(tmp, "p1.log")},
		managedClient{ID: "p2", PID: os.Getpid(), Port: serverPort(t, unhealthySrv), LogPath: filepath.Join(tmp, "p2.log")},
		managedClient{ID: "p3", PID: 99999999, Port: 1, LogPath: filepath.Join(tmp, "p3.log")},
	)

	out, err := mgr.list(nil)
	if err != nil {
		t.Fatalf("list error: %v", err)
	}
	var resp struct {
		OK        bool `json:"ok"`
		Count     int  `json:"count"`
		Instances []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Port   int    `json:"port"`
		} `json:"instances"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("unmarshal list output: %v\n%s", err, out)
	}
	if !resp.OK || resp.Count != 3 {
		t.Fatalf("unexpected list header: %+v", resp)
	}
	statusByID := map[string]string{}
	for _, it := range resp.Instances {
		statusByID[it.ID] = it.Status
	}
	if statusByID["p1"] != "running" {
		t.Errorf("p1 status = %q, want running", statusByID["p1"])
	}
	if statusByID["p2"] != "unhealthy" {
		t.Errorf("p2 status = %q, want unhealthy", statusByID["p2"])
	}
	if statusByID["p3"] != "exited" {
		t.Errorf("p3 status = %q, want exited", statusByID["p3"])
	}
}

func TestClientStateSingleAndAll(t *testing.T) {
	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/state" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":          true,
			"instance_id": "p1",
			"scene":       "Lobby",
			"procedure":   "ProcedureLobby",
		})
	}))
	defer srv1.Close()

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/state" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":          true,
			"instance_id": "p2",
			"scene":       "Battle",
			"procedure":   "ProcedureBattle",
		})
	}))
	defer srv2.Close()

	tmp := t.TempDir()
	mgr := newClientManager(func() string { return tmp })
	seedRegistry(t, mgr,
		managedClient{ID: "p1", PID: 0, Port: serverPort(t, srv1)},
		managedClient{ID: "p2", PID: 0, Port: serverPort(t, srv2)},
	)

	// Single
	singleOut, err := mgr.state(map[string]any{"id": "p1"})
	if err != nil {
		t.Fatalf("state(p1): %v", err)
	}
	var single map[string]any
	if err := json.Unmarshal([]byte(singleOut), &single); err != nil {
		t.Fatalf("unmarshal single state: %v", err)
	}
	if single["scene"] != "Lobby" || single["instance_id"] != "p1" {
		t.Fatalf("unexpected single state: %+v", single)
	}

	// All (parallel)
	allOut, err := mgr.state(map[string]any{"id": "all"})
	if err != nil {
		t.Fatalf("state(all): %v", err)
	}
	var allResp struct {
		OK     bool                      `json:"ok"`
		States map[string]map[string]any `json:"states"`
	}
	if err := json.Unmarshal([]byte(allOut), &allResp); err != nil {
		t.Fatalf("unmarshal all state: %v", err)
	}
	if !allResp.OK || len(allResp.States) != 2 {
		t.Fatalf("unexpected allResp: %+v", allResp)
	}
	if allResp.States["p1"]["scene"] != "Lobby" || allResp.States["p2"]["scene"] != "Battle" {
		t.Fatalf("unexpected states content: %+v", allResp.States)
	}

	// Unknown ID
	if _, err := mgr.state(map[string]any{"id": "missing"}); err == nil {
		t.Fatal("expected error for unknown instance id")
	}
}

func TestClientActSingleAndBatchWithWait(t *testing.T) {
	var mu sync.Mutex
	var received []map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/action" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		received = append(received, body)
		mu.Unlock()

		if body["text"] == "fail_me" {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "button not found"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "echo_type": body["type"]})
	}))
	defer srv.Close()

	tmp := t.TempDir()
	mgr := newClientManager(func() string { return tmp })
	seedRegistry(t, mgr, managedClient{ID: "p1", Port: serverPort(t, srv)})

	// 1) 单动作
	singleOut, err := mgr.act(map[string]any{
		"id":   "p1",
		"type": "click_button",
		"text": "开始游戏",
	})
	if err != nil {
		t.Fatalf("single act error: %v", err)
	}
	var singleResp map[string]any
	if err := json.Unmarshal([]byte(singleOut), &singleResp); err != nil {
		t.Fatalf("unmarshal single act: %v", err)
	}
	if singleResp["ok"] != true {
		t.Fatalf("expected ok=true, got %+v", singleResp)
	}

	// 2) 批量动作含 wait
	t0 := time.Now()
	batchOut, err := mgr.act(map[string]any{
		"id": "p1",
		"actions": []any{
			map[string]any{"type": "set_text", "placeholder": "用户名", "text": "alice"},
			map[string]any{"type": "wait", "duration_ms": float64(40)},
			map[string]any{"type": "set_input", "move_x": 1.0, "move_y": 0.0, "attack": true, "duration_ms": float64(100)},
		},
	})
	elapsed := time.Since(t0)
	if err != nil {
		t.Fatalf("batch act error: %v", err)
	}
	if elapsed < 35*time.Millisecond {
		t.Errorf("expected wait action to sleep >=35ms, elapsed=%v", elapsed)
	}

	var batchResp struct {
		OK        bool             `json:"ok"`
		Completed int              `json:"completed"`
		Total     int              `json:"total"`
		Results   []map[string]any `json:"results"`
	}
	if err := json.Unmarshal([]byte(batchOut), &batchResp); err != nil {
		t.Fatalf("unmarshal batch act: %v", err)
	}
	if !batchResp.OK || batchResp.Completed != 3 || batchResp.Total != 3 {
		t.Fatalf("unexpected batchResp: %+v", batchResp)
	}
	if batchResp.Results[1]["type"] != "wait" {
		t.Errorf("expected second result to be wait, got %+v", batchResp.Results[1])
	}

	// 验证服务器只收到 3 次 HTTP 请求（1 单动作 + 2 批量非 wait 动作）
	mu.Lock()
	gotCount := len(received)
	mu.Unlock()
	if gotCount != 3 {
		t.Fatalf("server received %d actions, want 3 (wait must run on MCP side)", gotCount)
	}

	// 3) 批量中遇到失败应提前中止
	failOut, err := mgr.act(map[string]any{
		"id": "p1",
		"actions": []any{
			map[string]any{"type": "click_button", "text": "fail_me"},
			map[string]any{"type": "click_button", "text": "should_not_run"},
		},
	})
	if err != nil {
		t.Fatalf("fail batch returned unexpected err: %v", err)
	}
	var failResp struct {
		OK        bool `json:"ok"`
		Completed int  `json:"completed"`
		Total     int  `json:"total"`
	}
	_ = json.Unmarshal([]byte(failOut), &failResp)
	if failResp.OK || failResp.Completed != 1 || failResp.Total != 2 {
		t.Fatalf("expected early stop on failure, got %+v", failResp)
	}
}

func TestClientScreenshot(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/screenshot" {
			http.NotFound(w, r)
			return
		}
		var body struct {
			Path string `json:"path"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotPath = body.Path
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":     true,
			"path":   body.Path,
			"width":  1280,
			"height": 720,
		})
	}))
	defer srv.Close()

	tmp := t.TempDir()
	mgr := newClientManager(func() string { return tmp })
	seedRegistry(t, mgr, managedClient{ID: "p1", Port: serverPort(t, srv)})

	// 默认路径
	out, err := mgr.screenshot(map[string]any{"id": "p1"})
	if err != nil {
		t.Fatalf("screenshot default path: %v", err)
	}
	expectedDir := filepath.Join(tmp, ".tmp", "dev-clients", "screenshots")
	if !strings.HasPrefix(gotPath, expectedDir) || !strings.HasSuffix(gotPath, ".png") {
		t.Fatalf("unexpected default screenshot path: %q", gotPath)
	}
	if _, err := os.Stat(expectedDir); err != nil {
		t.Fatalf("screenshots dir not created: %v", err)
	}
	if !strings.Contains(out, `"width": 1280`) {
		t.Fatalf("unexpected screenshot output: %s", out)
	}

	// 指定路径
	customPath := filepath.Join(tmp, "custom", "shot.png")
	_, err = mgr.screenshot(map[string]any{"id": "p1", "path": customPath})
	if err != nil {
		t.Fatalf("screenshot custom path: %v", err)
	}
	if gotPath != customPath {
		t.Fatalf("gotPath=%q, want %q", gotPath, customPath)
	}
}

func TestClientsStopQuitAndRegistryUpdate(t *testing.T) {
	var mu sync.Mutex
	quitReceived := map[string]bool{}

	makeSrv := func(id string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && r.URL.Path == "/action" {
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				if body["type"] == "quit" {
					mu.Lock()
					quitReceived[id] = true
					mu.Unlock()
					_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
					return
				}
			}
			http.NotFound(w, r)
		}))
	}

	srv1 := makeSrv("p1")
	defer srv1.Close()
	srv2 := makeSrv("p2")
	defer srv2.Close()

	tmp := t.TempDir()
	mgr := newClientManager(func() string { return tmp })
	seedRegistry(t, mgr,
		managedClient{ID: "p1", PID: 0, Port: serverPort(t, srv1)},
		managedClient{ID: "p2", PID: 0, Port: serverPort(t, srv2)},
	)

	// 仅停止 p1
	if _, err := mgr.stop(map[string]any{"id": "p1"}); err != nil {
		t.Fatalf("stop(p1): %v", err)
	}
	if !quitReceived["p1"] || quitReceived["p2"] {
		t.Fatalf("unexpected quitReceived after stop(p1): %+v", quitReceived)
	}
	rem, err := mgr.getAllInstances()
	if err != nil {
		t.Fatalf("getAllInstances: %v", err)
	}
	if len(rem) != 1 || rem[0].ID != "p2" {
		t.Fatalf("expected only p2 remaining, got %+v", rem)
	}

	// 省略 id 停止全部剩余实例
	if _, err := mgr.stop(map[string]any{}); err != nil {
		t.Fatalf("stop(all): %v", err)
	}
	if !quitReceived["p2"] {
		t.Fatalf("expected p2 to receive quit")
	}
	rem, _ = mgr.getAllInstances()
	if len(rem) != 0 {
		t.Fatalf("expected empty registry after stop all, got %+v", rem)
	}
}

func TestResolveGodotMonoPath(t *testing.T) {
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mkFile := func(rel string) string {
		p := filepath.Join(tmp, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("bin"), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}

	explicitFile := mkFile("explicit/godot-mono")
	envFile := mkFile("env/godot-mono")

	// 1) explicit 优先级最高
	got, err := resolveGodotMonoWith(explicitFile, godotMonoResolverDeps{
		goos:     "darwin",
		homeDir:  tmp,
		envPath:  envFile,
		lookPath: func(string) (string, error) { return "", errors.New("not found") },
		stat:     os.Stat,
		readDir:  os.ReadDir,
	})
	if err != nil || got != explicitFile {
		t.Fatalf("explicit priority failed: got=%q err=%v", got, err)
	}

	// 2) KNIGHT_GODOT_PATH 次之
	got, err = resolveGodotMonoWith("", godotMonoResolverDeps{
		goos:     "darwin",
		homeDir:  tmp,
		envPath:  envFile,
		lookPath: func(string) (string, error) { return "", errors.New("not found") },
		stat:     os.Stat,
		readDir:  os.ReadDir,
	})
	if err != nil || got != envFile {
		t.Fatalf("env priority failed: got=%q err=%v", got, err)
	}

	// 3) macOS 自动检测顺序：~/bin/godot-mono -> ~/Desktop/Godot_mono.app -> PATH
	macHome := filepath.Join(tmp, "mac_home")
	desktopMono := filepath.Join(macHome, "Desktop", "Godot_mono.app", "Contents", "MacOS", "Godot")
	_ = os.MkdirAll(filepath.Dir(desktopMono), 0o755)
	_ = os.WriteFile(desktopMono, []byte("bin"), 0o755)

	got, err = resolveGodotMonoWith("", godotMonoResolverDeps{
		goos:     "darwin",
		homeDir:  macHome,
		lookPath: func(string) (string, error) { return "/usr/local/bin/godot-mono", nil },
		stat: func(p string) (os.FileInfo, error) {
			if p == "/Applications/Godot_mono.app/Contents/MacOS/Godot" {
				return nil, os.ErrNotExist
			}
			return os.Stat(p)
		},
		readDir: os.ReadDir,
	})
	if err != nil || got != desktopMono {
		t.Fatalf("macOS desktop mono detection failed: got=%q want=%q err=%v", got, desktopMono, err)
	}

	// ~/bin/godot-mono 优先于 Desktop
	homeBinMono := filepath.Join(macHome, "bin", "godot-mono")
	_ = os.MkdirAll(filepath.Dir(homeBinMono), 0o755)
	_ = os.WriteFile(homeBinMono, []byte("bin"), 0o755)

	got, err = resolveGodotMonoWith("", godotMonoResolverDeps{
		goos:     "darwin",
		homeDir:  macHome,
		lookPath: func(string) (string, error) { return "/usr/local/bin/godot-mono", nil },
		stat:     os.Stat,
		readDir:  os.ReadDir,
	})
	if err != nil || got != homeBinMono {
		t.Fatalf("macOS ~/bin/godot-mono priority failed: got=%q want=%q err=%v", got, homeBinMono, err)
	}

	// 4) Windows：必须只选 mono 且优先 _console.exe，绝不选非 mono
	winRoot := filepath.Join(tmp, "win_godot")
	_ = mkFile(filepath.Join("win_godot", "Godot_v4.7_win64", "Godot_v4.7_win64_console.exe"))
	monoConsole := mkFile(filepath.Join("win_godot", "Godot_v4.7_mono_win64", "Godot_v4.7_mono_win64_console.exe"))
	_ = mkFile(filepath.Join("win_godot", "Godot_v4.7_mono_win64", "Godot_v4.7_mono_win64.exe"))

	exe, ok := findGodotMonoWindowsInRoot(winRoot, os.ReadDir)
	if !ok || exe != monoConsole {
		t.Fatalf("windows mono detection failed: ok=%v got=%q want=%q", ok, exe, monoConsole)
	}

	// 仅有非 mono 时必须返回 false
	nonMonoRoot := filepath.Join(tmp, "win_non_mono")
	_ = mkFile(filepath.Join("win_non_mono", "Godot_v4.7_win64", "Godot_v4.7_win64_console.exe"))
	if exe, ok := findGodotMonoWindowsInRoot(nonMonoRoot, os.ReadDir); ok {
		t.Fatalf("must not pick non-mono godot, got %q", exe)
	}

	// 5) 符号链接必须解析为真实路径（避免 macOS 上 ~/bin/godot-mono 导致 .NET 模块找不到 GodotSharp）
	if runtime.GOOS != "windows" {
		symHome := filepath.Join(tmp, "sym_home")
		realAppBin := filepath.Join(symHome, "Desktop", "Godot_mono.app", "Contents", "MacOS", "Godot")
		if err := os.MkdirAll(filepath.Dir(realAppBin), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(realAppBin, []byte("bin"), 0o755); err != nil {
			t.Fatal(err)
		}
		symLink := filepath.Join(symHome, "bin", "godot-mono")
		if err := os.MkdirAll(filepath.Dir(symLink), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(realAppBin, symLink); err != nil {
			t.Fatal(err)
		}

		got, err := resolveGodotMonoWith("", godotMonoResolverDeps{
			goos:         "darwin",
			homeDir:      symHome,
			lookPath:     func(string) (string, error) { return "", errors.New("not found") },
			stat:         os.Stat,
			readDir:      os.ReadDir,
			evalSymlinks: filepath.EvalSymlinks,
		})
		if err != nil || got != realAppBin {
			t.Fatalf("symlink resolution failed: got=%q want=%q err=%v", got, realAppBin, err)
		}
	}
}

func TestParseInstanceIDs(t *testing.T) {
	ids, err := parseInstanceIDs(map[string]any{})
	if err != nil || len(ids) != 2 || ids[0] != "p1" || ids[1] != "p2" {
		t.Fatalf("default ids failed: %v %v", ids, err)
	}

	ids, err = parseInstanceIDs(map[string]any{"count": float64(3)})
	if err != nil || len(ids) != 3 || ids[2] != "p3" {
		t.Fatalf("count=3 failed: %v %v", ids, err)
	}

	ids, err = parseInstanceIDs(map[string]any{"ids": []any{"host", "guest"}})
	if err != nil || len(ids) != 2 || ids[0] != "host" || ids[1] != "guest" {
		t.Fatalf("custom ids failed: %v %v", ids, err)
	}

	for _, bad := range []map[string]any{
		{"count": float64(0)},
		{"count": float64(5)},
		{"ids": []any{"all"}},
		{"ids": []any{"p1", "p1"}},
		{"ids": []any{"p1", "p2"}, "count": float64(3)},
	} {
		if _, err := parseInstanceIDs(bad); err == nil {
			t.Errorf("expected error for args %+v", bad)
		}
	}
}

func TestClientsLaunchTimeoutIncludesLogTail(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake binary test runs on unix")
	}

	tmp := t.TempDir()
	clientDir := filepath.Join(tmp, "Knight-Client")
	if err := os.MkdirAll(clientDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// 构造假 godot-mono 脚本：打印收到的参数并立即退出
	fakeGodot := filepath.Join(tmp, "fake-godot-mono.sh")
	script := "#!/bin/sh\necho \"FAKE_GODOT_ARGS: $*\"\nexit 1\n"
	if err := os.WriteFile(fakeGodot, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	mgr := newClientManager(func() string { return tmp })
	_, err := mgr.launch(map[string]any{
		"count":          float64(1),
		"ids":            []any{"p1"},
		"godot_path":     fakeGodot,
		"base_port":      float64(19250),
		"wait_ready_sec": float64(1),
	})
	if err == nil {
		t.Fatal("expected launch error when fake godot exits immediately")
	}
	msg := err.Error()
	if !strings.Contains(msg, "log_tail") {
		t.Fatalf("expected log_tail in error output:\n%s", msg)
	}
	if !strings.Contains(msg, "--instance-id=p1") || !strings.Contains(msg, "--dev-agent-port=19250") || !strings.Contains(msg, "--position 60,80") {
		t.Fatalf("expected launch args in log tail:\n%s", msg)
	}

	// 验证注册表已落盘
	list, err := mgr.getAllInstances()
	if err != nil || len(list) != 1 || list[0].ID != "p1" || list[0].Port != 19250 {
		t.Fatalf("expected persisted instance in registry, got %+v err=%v", list, err)
	}
}

func TestClientsStopKillsUnresponsiveProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep helper runs on unix")
	}

	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	pid := cmd.Process.Pid

	tmp := t.TempDir()
	mgr := newClientManager(func() string { return tmp })
	mgr.watchProcess(pid, cmd)
	seedRegistry(t, mgr, managedClient{ID: "stubborn", PID: pid, Port: 1})

	out, err := mgr.stop(map[string]any{"id": "stubborn"})
	if err != nil {
		t.Fatalf("stop stubborn: %v", err)
	}
	if !strings.Contains(out, `"method": "killed"`) {
		t.Fatalf("expected killed method, got:\n%s", out)
	}
	if mgr.isProcessAlive(pid) {
		t.Fatalf("expected pid %d to be dead after kill", pid)
	}
}
