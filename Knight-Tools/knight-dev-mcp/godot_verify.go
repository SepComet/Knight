package main

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// godot_verify：Godot 客户端验证。check 只查环境状态秒回；
// compile 用 dotnet build 检查编译；tests 用 dotnet test（xUnit，TRX 结果解析），
// 一条命令同时得出编译+测试双结论。与编辑器是否运行无强互斥，仅在检测到
// Godot 进程时提示程序集可能被占用（MSB3021/3027 文件锁）。

var (
	clientProjRel = filepath.Join("Knight-Client", "Knight-Client.csproj")
	testsProjRel  = filepath.Join("Knight-Client.Tests", "Knight-Client.Tests.csproj")
)

var dotnetCompileErrMarkers = []string{
	"error CS",
	"error MSB",
	"error NU",
	"error NETSDK",
	"Build FAILED",
}

// ---------- TRX（Visual Studio 测试结果）解析 ----------

type trxCase struct {
	TestName string `xml:"testName,attr"`
	Outcome  string `xml:"outcome,attr"`
	Duration string `xml:"duration,attr"`
	Output   *struct {
		ErrorInfo *struct {
			Message    string `xml:"Message"`
			StackTrace string `xml:"StackTrace"`
		} `xml:"ErrorInfo"`
	} `xml:"Output"`
}

type trxRun struct {
	Counters struct {
		Total        string `xml:"total,attr"`
		Executed     string `xml:"executed,attr"`
		Passed       string `xml:"passed,attr"`
		Failed       string `xml:"failed,attr"`
		Inconclusive string `xml:"inconclusive,attr"`
		NotExecuted  string `xml:"notExecuted,attr"`
	} `xml:"ResultSummary>Counters"` // 注意：真实 TRX 的 Counters 在 ResultSummary 下
	Results []trxCase `xml:"Results>UnitTestResult"`
}

// readProjectGodotFeatures 从 project.godot 取 config/features（如 PackedStringArray("4.7", "C#")）。
func readProjectGodotFeatures(clientDir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(clientDir, "project.godot"))
	if err != nil {
		return "", fmt.Errorf("读不到 project.godot: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "config/features=") {
			v := strings.TrimSpace(strings.TrimPrefix(line, "config/features="))
			if v != "" {
				return v, nil
			}
		}
	}
	return "", errors.New("project.godot 中没有 config/features 行")
}

// resolveGodotExe 依次尝试：显式参数、KNIGHT_GODOT_PATH 环境变量、常见安装目录扫描。
// 找不到时列出本机已装版本，便于调用方指定。compile/tests 实际只依赖 dotnet，
// Godot 路径主要用于 mode=check 的环境报告。
func resolveGodotExe(explicit string) (string, error) {
	if explicit != "" {
		if info, err := os.Stat(explicit); err == nil && !info.IsDir() {
			return explicit, nil
		}
		return "", fmt.Errorf("godot_path 指定的文件不存在: %s", explicit)
	}
	if p := os.Getenv("KNIGHT_GODOT_PATH"); p != "" {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p, nil
		}
		return "", fmt.Errorf("KNIGHT_GODOT_PATH 指定的文件不存在: %s", p)
	}

	roots := []string{
		`D:\Godot`,
		`C:\Program Files\Godot`,
		`D:\Program Files\Godot`,
		`C:\Godot`,
	}
	installed := []string{}
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			if exe, ok := findGodotExeInDir(filepath.Join(root, e.Name())); ok {
				installed = append(installed, exe)
			}
		}
	}
	if len(installed) == 0 {
		return "", fmt.Errorf("常见安装目录（%s）未找到 Godot.exe，且 KNIGHT_GODOT_PATH 未设置；可通过 godot_path 参数指定",
			strings.Join(roots, ", "))
	}
	return installed[0], nil
}

// findGodotExeInDir 在 Godot 版本目录（exe 位于顶层）里找引擎可执行文件，优先 _console.exe（CLI 输出可见）。
func findGodotExeInDir(dir string) (string, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	console, main := "", ""
	for _, f := range entries {
		if f.IsDir() || !strings.HasPrefix(f.Name(), "Godot") || !strings.HasSuffix(f.Name(), ".exe") {
			continue
		}
		if strings.HasSuffix(f.Name(), "_console.exe") {
			console = filepath.Join(dir, f.Name())
		} else {
			main = filepath.Join(dir, f.Name())
		}
	}
	if console != "" {
		return console, true
	}
	return main, main != ""
}

// runProcessCapture 执行命令并返回合并输出与退出码；超时时退出码为 -1。
// doneFile 非空时启用「结果即收工」：该文件出现（测试/检查结论已落盘）后给 graceSec
// 秒宽限，进程仍未自然退出则主动 Kill 并照常返回——退出码不再代表结论，以落盘文件为准。
// earlyReap 标记本次是否因收尾挂起被 Kill。
func runProcessCapture(name string, args []string, timeoutSec float64, doneFile string, graceSec float64) (output string, exitCode int, runErr error, earlyReap bool) {
	ctxDone := make(chan struct{})
	cmd := exec.Command(name, args...)
	var buf strings.Builder
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Start(); err != nil {
		return buf.String(), -1, err, false
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.AfterFunc(time.Duration(timeoutSec*float64(time.Second)), func() {
		_ = cmd.Process.Kill()
		close(ctxDone)
	})
	defer timer.Stop()

	reaped := make(chan struct{})
	if doneFile != "" {
		go func() {
			ticker := time.NewTicker(500 * time.Millisecond)
			defer ticker.Stop()
			for {
				if _, err := os.Stat(doneFile); err == nil {
					select {
					case <-time.After(time.Duration(graceSec * float64(time.Second))):
						// Kill 失败说明进程已在宽限窗口内自然退出，不算提前收工。
						if killErr := cmd.Process.Kill(); killErr == nil {
							close(reaped)
						}
						return
					case <-ctxDone:
						return
					}
				}
				select {
				case <-ticker.C:
				case <-ctxDone:
					return
				}
			}
		}()
	}

	err := <-done
	exitCode = 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			return buf.String(), -1, err, false
		}
	}
	// 超时优先于提前收工报告：若全局超时先触发，结论按超时算。
	select {
	case <-ctxDone:
		return buf.String(), exitCode, fmt.Errorf("%s 超时被终止（%.0f 秒）", name, timeoutSec), false
	default:
	}
	select {
	case <-reaped:
		return buf.String(), exitCode, nil, true
	default:
	}
	return buf.String(), exitCode, nil, false
}

// verifyVerdict 写入 Knight-Client/.godot/last-godot-verify.json 的结论快照。
// MCP 客户端超时丢响应时，调用方可直接读该文件取回最近一次 compile/tests 结论。
type verifyVerdict struct {
	Mode        string   `json:"mode"`
	FinishedAt  string   `json:"finishedAt"`
	DurationSec float64  `json:"durationSec"`
	Status      string   `json:"status"` // ok / test-failed / compile-error / timeout / inconclusive
	Summary     string   `json:"summary"`
	Total       string   `json:"total,omitempty"`
	Passed      string   `json:"passed,omitempty"`
	Failed      string   `json:"failed,omitempty"`
	Skipped     string   `json:"skipped,omitempty"`
	Failures    []string `json:"failures,omitempty"` // 失败用例 testName（上限 50 个）
	EarlyReap   bool     `json:"earlyReap,omitempty"`
	LogPath     string   `json:"logPath"`
	ResultsPath string   `json:"resultsPath,omitempty"`
}

// writeVerifyVerdict 结论落盘到固定路径；返回路径，失败返回空串（不影响主输出）。
func writeVerifyVerdict(clientDir string, v verifyVerdict) string {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return ""
	}
	path := filepath.Join(clientDir, ".godot", "last-godot-verify.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return ""
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return ""
	}
	return path
}

// grepCompileErrors 从 dotnet 输出中摘出编译错误行（去重保序）。
func grepCompileErrors(log string) []string {
	seen := map[string]bool{}
	var lines []string
	for _, line := range strings.Split(log, "\n") {
		line = strings.TrimRight(line, "\r")
		hit := false
		for _, m := range dotnetCompileErrMarkers {
			if strings.Contains(line, m) {
				hit = true
				break
			}
		}
		if hit && !seen[line] {
			seen[line] = true
			lines = append(lines, line)
		}
	}
	return lines
}

// godotEditorRunning 用 tasklist 探测 Godot 编辑器进程（IMAGENAME 通配 Godot*）。
// 仅提示用：dotnet build/test 与编辑器并行通常无冲突，仅程序集被占用时才需关编辑器。
func godotEditorRunning() bool {
	out, err := exec.Command("tasklist", "/FI", "IMAGENAME eq Godot*", "/NH").CombinedOutput()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "Godot")
}

func projStatus(path string) string {
	if _, err := os.Stat(path); err == nil {
		return "存在 ✓"
	}
	return "不存在 ✗"
}

func toolGodotVerify(args map[string]any) (string, error) {
	mode := argString(args, "mode", "check")
	if mode != "check" && mode != "compile" && mode != "tests" {
		return "", fmt.Errorf("mode 仅支持 check / compile / tests，收到 %q", mode)
	}
	clientDir := filepath.Join(repoRoot, "Knight-Client")

	var sb strings.Builder
	fmt.Fprintf(&sb, "Godot 校验（mode=%s）\n项目: %s\n", mode, clientDir)

	if mode == "check" {
		if features, err := readProjectGodotFeatures(clientDir); err != nil {
			fmt.Fprintf(&sb, "工程版本: 解析失败（%v）\n", err)
		} else {
			fmt.Fprintf(&sb, "工程版本: %s\n", features)
		}
		if exe, err := resolveGodotExe(argString(args, "godot_path", "")); err != nil {
			fmt.Fprintf(&sb, "Godot 路径: 解析失败（%v）\n", err)
		} else {
			fmt.Fprintf(&sb, "Godot 路径: %s\n", exe)
		}
		if dotnet, err := lookDotnet(); err != nil {
			fmt.Fprintf(&sb, "dotnet: 未找到（%v）\n", err)
		} else {
			fmt.Fprintf(&sb, "dotnet: %s\n", dotnet)
		}
		fmt.Fprintf(&sb, "主工程: %s\n", projStatus(filepath.Join(repoRoot, clientProjRel)))
		fmt.Fprintf(&sb, "测试工程: %s\n", projStatus(filepath.Join(repoRoot, testsProjRel)))
		if godotEditorRunning() {
			sb.WriteString("编辑器: 检测到 Godot 进程 → compile/tests 仍可直接跑；若出现 MSB3021/3027 程序集占用错误，先关闭编辑器。\n")
		} else {
			sb.WriteString("编辑器: 未检测到 Godot 进程 → mode=tests 一次完成编译检查+xUnit 测试（约十几秒）；只想查编译用 mode=compile。\n")
		}
		return sb.String(), nil
	}

	dotnet, err := lookDotnet()
	if err != nil {
		return "", err
	}

	tmpDir, err := os.MkdirTemp("", "knight-godot-verify-")
	if err != nil {
		return "", fmt.Errorf("创建临时目录失败: %w", err)
	}
	logPath := filepath.Join(tmpDir, "godot-verify.log")

	editorHint := ""
	if godotEditorRunning() {
		editorHint = "\n提示: 检测到 Godot 编辑器进程，程序集可能被占用（MSB3021/3027）；失败时先关闭编辑器重试。\n"
	}

	if mode == "compile" {
		timeoutSec := argNumber(args, "timeout_sec", 300)
		projPath := filepath.Join(repoRoot, clientProjRel)
		if _, err := os.Stat(projPath); err != nil {
			return "", fmt.Errorf("主工程不存在: %s", projPath)
		}
		fmt.Fprintf(&sb, "正在 dotnet build 主工程...\n%s", editorHint)
		start := time.Now()
		output, runErr := runCaptured(repoRoot, timeoutSec, dotnet, "build", clientProjRel, "--nologo")
		elapsed := time.Since(start).Seconds()
		_ = os.WriteFile(logPath, []byte(output), 0o644)

		errLines := grepCompileErrors(output)
		verdictStatus, verdictSummary := "inconclusive", "未命中错误标记，但退出码异常——请人工查看日志确认。"
		switch {
		case len(errLines) > 0:
			verdictStatus, verdictSummary = "compile-error", fmt.Sprintf("编译失败（日志命中 %d 处错误标记）。", len(errLines))
		case runErr == nil:
			verdictStatus, verdictSummary = "ok", "编译通过（日志无编译错误标记）。"
		case runErr != nil && strings.Contains(runErr.Error(), "超时"):
			verdictStatus, verdictSummary = "timeout", "编译检查超时被终止。"
		}
		fmt.Fprintf(&sb, "结论: %s（耗时 %.1fs）\n", verdictSummary, elapsed)
		if runErr != nil && verdictStatus != "timeout" {
			fmt.Fprintf(&sb, "进程级错误: %v\n", runErr)
		}
		if len(errLines) > 0 {
			sb.WriteString("== 编译错误（前 40 行）==\n")
			for i, l := range errLines {
				if i >= 40 {
					fmt.Fprintf(&sb, "...（其余 %d 行见日志）\n", len(errLines)-40)
					break
				}
				sb.WriteString(l + "\n")
			}
		}
		verdictPath := writeVerifyVerdict(clientDir, verifyVerdict{
			Mode: mode, FinishedAt: time.Now().Format(time.RFC3339), DurationSec: elapsed,
			Status: verdictStatus, Summary: verdictSummary, LogPath: logPath,
		})
		fmt.Fprintf(&sb, "日志: %s\n", logPath)
		if verdictPath != "" {
			fmt.Fprintf(&sb, "结论快照: %s（MCP 响应超时可直接读该文件取回结论）\n", verdictPath)
		}
		return sb.String(), nil
	}

	// mode == tests：dotnet test（隐含编译），TRX 落盘即为结论已定。
	testsProjPath := filepath.Join(repoRoot, testsProjRel)
	if _, err := os.Stat(testsProjPath); err != nil {
		return "", fmt.Errorf("测试工程不存在: %s（mode=tests 需要 Knight-Client.Tests）", testsProjPath)
	}
	timeoutSec := argNumber(args, "timeout_sec", 600)
	trxPath := filepath.Join(tmpDir, "results.trx")
	testArgs := []string{
		"test", testsProjPath,
		"--nologo",
		"--logger", "trx;LogFileName=" + filepath.Base(trxPath),
		"--results-directory", tmpDir,
	}
	if f := argString(args, "test_filter", ""); f != "" {
		testArgs = append(testArgs, "--filter", f)
	}
	fmt.Fprintf(&sb, "正在 dotnet test（编译+xUnit 测试一条命令完成）...\n%s", editorHint)
	start := time.Now()
	output, exitCode, runErr, earlyReap := runProcessCapture(dotnet, testArgs, timeoutSec, trxPath, 10)
	elapsed := time.Since(start).Seconds()
	_ = os.WriteFile(logPath, []byte(output), 0o644)
	fmt.Fprintf(&sb, "退出码: %d | 耗时 %.1fs\n", exitCode, elapsed)
	if runErr != nil {
		fmt.Fprintf(&sb, "进程级错误: %v\n", runErr)
	}
	if earlyReap {
		fmt.Fprintf(&sb, "注意: TRX 已写出但 dotnet test 收尾挂起，进程已被提前收工（退出码不代表测试结论，以 TRX 为准）。\n")
	}

	var run trxRun
	xmlOK := false
	if data, err := os.ReadFile(trxPath); err == nil {
		// TRX 带 UTF-8 BOM，encoding/xml 不跳过它会直接解析失败。
		data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
		if err := xml.Unmarshal(data, &run); err == nil && run.Counters.Total != "" {
			xmlOK = true
		}
	}

	// dotnet test 隐含编译：能跑出 TRX 即编译已通过；日志命中错误标记则测试必然没跑。
	errLines := grepCompileErrors(output)
	verdictFailures := []string{}
	if len(errLines) > 0 {
		verdict := "——测试未运行"
		if xmlOK {
			verdict = ""
		}
		fmt.Fprintf(&sb, "编译: 失败（日志命中 %d 处错误标记）%s\n== 编译错误（前 40 行）==\n", len(errLines), verdict)
		for i, l := range errLines {
			if i >= 40 {
				fmt.Fprintf(&sb, "...（其余 %d 行见日志）\n", len(errLines)-40)
				break
			}
			sb.WriteString(l + "\n")
		}
	} else {
		sb.WriteString("编译: 通过\n")
	}
	if xmlOK {
		c := run.Counters
		fmt.Fprintf(&sb, "测试汇总: %s 执行, %s 通过, %s 失败, %s 跳过%s\n",
			c.Total, c.Passed, c.Failed, c.NotExecuted, inconclusiveSuffix(c.Inconclusive))
		var failedCases []trxCase
		for _, tc := range run.Results {
			if strings.EqualFold(tc.Outcome, "Failed") {
				failedCases = append(failedCases, tc)
				if len(verdictFailures) < 50 {
					verdictFailures = append(verdictFailures, tc.TestName)
				}
			}
		}
		if len(failedCases) > 0 {
			sb.WriteString("\n== 失败用例（最多列 10 个）==\n")
			for i, tc := range failedCases {
				if i >= 10 {
					fmt.Fprintf(&sb, "...（其余 %d 个失败用例见 TRX）\n", len(failedCases)-10)
					break
				}
				fmt.Fprintf(&sb, "--- %s (%s) ---\n", tc.TestName, tc.Duration)
				if tc.Output != nil && tc.Output.ErrorInfo != nil {
					msg := strings.TrimSpace(tc.Output.ErrorInfo.Message)
					if len(msg) > 800 {
						msg = msg[:800] + "..."
					}
					if msg != "" {
						sb.WriteString(msg + "\n")
					}
					if st := tailLines(strings.TrimSpace(tc.Output.ErrorInfo.StackTrace), 6); st != "" {
						sb.WriteString(strings.ReplaceAll(st, "\n", "\n  ") + "\n")
					}
				}
			}
		}
		switch {
		case c.Failed != "0":
			sb.WriteString("\n存在失败用例。\n")
		case earlyReap || (runErr == nil && exitCode == 0):
			sb.WriteString("\n全部通过。\n")
		default:
			sb.WriteString("\n测试通过但进程退出码异常——请查看日志确认。\n")
		}
	} else if len(errLines) == 0 {
		sb.WriteString("未拿到可解析的 TRX——日志未见编译错误，测试没跑起来（环境问题）。日志尾部：\n")
		sb.WriteString(tailLines(output, 40) + "\n")
	}
	verdictStatus, verdictSummary := "inconclusive", "测试没跑起来（未拿到 TRX，日志也未见编译错误）。"
	switch {
	case len(errLines) > 0 && !xmlOK:
		verdictStatus, verdictSummary = "compile-error", fmt.Sprintf("编译失败，测试未运行（日志命中 %d 处错误标记）。", len(errLines))
	case xmlOK && run.Counters.Failed != "0":
		verdictStatus = "test-failed"
		verdictSummary = fmt.Sprintf("%s 执行, %s 通过, %s 失败。", run.Counters.Total, run.Counters.Passed, run.Counters.Failed)
	case xmlOK:
		verdictStatus = "ok"
		verdictSummary = fmt.Sprintf("%s 执行全部通过。", run.Counters.Total)
	case runErr != nil:
		verdictStatus = "timeout"
		verdictSummary = "测试超时被终止，未拿到 TRX。"
	}
	verdictPath := writeVerifyVerdict(clientDir, verifyVerdict{
		Mode: mode, FinishedAt: time.Now().Format(time.RFC3339), DurationSec: elapsed,
		Status: verdictStatus, Summary: verdictSummary,
		Total: run.Counters.Total, Passed: run.Counters.Passed, Failed: run.Counters.Failed, Skipped: run.Counters.NotExecuted,
		Failures: verdictFailures, EarlyReap: earlyReap,
		LogPath: logPath, ResultsPath: trxPath,
	})
	fmt.Fprintf(&sb, "日志: %s\n结果 TRX: %s\n", logPath, trxPath)
	if verdictPath != "" {
		fmt.Fprintf(&sb, "结论快照: %s（MCP 响应超时可直接读该文件取回结论）\n", verdictPath)
	}
	return sb.String(), nil
}

func inconclusiveSuffix(v string) string {
	if v != "" && v != "0" {
		return ", " + v + " 不确定"
	}
	return ""
}
