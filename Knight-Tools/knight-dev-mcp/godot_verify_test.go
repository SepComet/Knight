package main

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runProcessCapture 的「结果即收工」路径：
// doneFile 出现后宽限窗口内进程自然退出 → 不算提前收工；
// 宽限窗口结束仍未退出 → Kill 且 earlyReap=true。
// 不依赖 Godot/dotnet：用 ping 充当长任务进程（Windows 自带）。
func TestRunProcessCaptureEarlyReap(t *testing.T) {
	dir := t.TempDir()
	doneFile := filepath.Join(dir, "results.trx")

	// 用例 1：进程存活远超宽限窗口，doneFile 出现 → Kill 收工。
	// ping -n 60 约跑 60 秒，远长于 1 秒宽限 + 短超时。
	go func() {
		time.Sleep(1500 * time.Millisecond) // 先让进程跑起来，再落结果文件
		_ = os.WriteFile(doneFile, []byte("<dummy/>"), 0o644)
	}()
	start := time.Now()
	_, exitCode, runErr, earlyReap := runProcessCapture("ping", []string{"-n", "60", "127.0.0.1"}, 60, doneFile, 1)
	elapsed := time.Since(start)
	if runErr != nil {
		t.Fatalf("case1 runErr = %v", runErr)
	}
	if !earlyReap {
		t.Fatalf("case1 earlyReap = false, want true（进程存活超宽限窗口，应被 Kill）")
	}
	if elapsed > 10*time.Second {
		t.Fatalf("case1 elapsed = %s，收工应在数秒内完成", elapsed)
	}
	_ = exitCode // 被 Kill 的进程退出码不代表结论，此处不作断言

	// 用例 2：doneFile 出现前进程已自然退出 → earlyReap=false，正常返回。
	_ = os.Remove(doneFile)
	_, _, runErr2, earlyReap2 := runProcessCapture("ping", []string{"-n", "1", "127.0.0.1"}, 60, doneFile, 1)
	if runErr2 != nil {
		t.Fatalf("case2 runErr = %v", runErr2)
	}
	if earlyReap2 {
		t.Fatalf("case2 earlyReap = true, want false（进程自然退出）")
	}
}

func TestParseTrxResults(t *testing.T) {
	// 首字符是 UTF-8 BOM（dotnet test 产物如此），必须先剥掉再解析。
	trx := "\xef\xbb\xbf" + `<?xml version="1.0" encoding="UTF-8"?>
<TestRun>
  <Results>
    <UnitTestResult testName="Knight.Client.Tests.ProtoGeneratedSmokeTests.GeneratedMsgId_IsAccessible" outcome="Passed" duration="00:00:00.001" />
    <UnitTestResult testName="Knight.Client.Tests.ProtoGeneratedSmokeTests.FailingCase" outcome="Failed" duration="00:00:00.002">
      <Output>
        <ErrorInfo>
          <Message>Assert.Equal() Failure: Values differ</Message>
          <StackTrace>   at Knight.Client.Tests.FailingCase()</StackTrace>
        </ErrorInfo>
      </Output>
    </UnitTestResult>
  </Results>
  <ResultSummary>
    <Counters total="2" executed="2" passed="1" failed="1" error="0" inconclusive="0" notExecuted="0" />
  </ResultSummary>
</TestRun>`
	var run trxRun
	if err := xml.Unmarshal([]byte(trx), &run); err != nil {
		t.Fatalf("TRX 解析失败: %v", err)
	}
	c := run.Counters
	if c.Total != "2" || c.Passed != "1" || c.Failed != "1" || c.NotExecuted != "0" {
		t.Fatalf("计数解析错误: %+v", c)
	}
	var failed []trxCase
	for _, tc := range run.Results {
		if strings.EqualFold(tc.Outcome, "Failed") {
			failed = append(failed, tc)
		}
	}
	if len(failed) != 1 {
		t.Fatalf("失败用例数 = %d, want 1", len(failed))
	}
	if failed[0].TestName != "Knight.Client.Tests.ProtoGeneratedSmokeTests.FailingCase" {
		t.Fatalf("testName = %q", failed[0].TestName)
	}
	if failed[0].Output == nil || failed[0].Output.ErrorInfo == nil ||
		failed[0].Output.ErrorInfo.Message != "Assert.Equal() Failure: Values differ" {
		t.Fatalf("ErrorInfo.Message 解析错误: %+v", failed[0].Output)
	}
}

func TestGrepCompileErrors(t *testing.T) {
	log := strings.Join([]string{
		"Program.cs(1,1): error CS1002: ; expected",
		"MSBUILD : error MSB1025: internal failure",
		"NuGet warning NU1603: ...", // 无 "error NU" 前缀的 NuGet 提示不应命中
		"Knight-Client.Tests : error NU1201: project not compatible",
		"warning CS0168: variable declared but never used",
		"Build FAILED.",
		"Build FAILED.",
	}, "\n")
	lines := grepCompileErrors(log)
	want := []string{
		"Program.cs(1,1): error CS1002: ; expected",
		"MSBUILD : error MSB1025: internal failure",
		"Knight-Client.Tests : error NU1201: project not compatible",
		"Build FAILED.", // 去重保序
	}
	if len(lines) != len(want) {
		t.Fatalf("命中行数 = %d, want %d: %v", len(lines), len(want), lines)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("lines[%d] = %q, want %q", i, lines[i], want[i])
		}
	}
}

func TestResolveGodotExeExplicit(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "Godot_v4.7.2-stable_mono_win64.exe")
	if err := os.WriteFile(fake, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	exe, err := resolveGodotExe(fake)
	if err != nil || exe != fake {
		t.Fatalf("显式路径应直接返回: %q, %v", exe, err)
	}
	if _, err := resolveGodotExe(filepath.Join(dir, "missing.exe")); err == nil {
		t.Fatalf("不存在的显式路径应报错")
	}
}

func TestResolveGodotExeEnv(t *testing.T) {
	t.Setenv("KNIGHT_GODOT_PATH", filepath.Join(t.TempDir(), "missing.exe"))
	if _, err := resolveGodotExe(""); err == nil || !strings.Contains(err.Error(), "KNIGHT_GODOT_PATH") {
		t.Fatalf("环境变量指向不存在文件时应报错并提及变量名: %v", err)
	}
}

func TestReadProjectGodotFeatures(t *testing.T) {
	dir := t.TempDir()
	content := "config_version=5\n\n[application]\n\nconfig/name=\"Knight\"\n" +
		"config/features=PackedStringArray(\"4.7\", \"C#\")\n\n[dotnet]\n\nproject/assembly_name=\"Knight-Client\"\n"
	if err := os.WriteFile(filepath.Join(dir, "project.godot"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	features, err := readProjectGodotFeatures(dir)
	if err != nil || !strings.Contains(features, "4.7") || !strings.Contains(features, "C#") {
		t.Fatalf("features 解析错误: %q, %v", features, err)
	}
	if _, err := readProjectGodotFeatures(t.TempDir()); err == nil {
		t.Fatalf("缺 project.godot 应报错")
	}
}
