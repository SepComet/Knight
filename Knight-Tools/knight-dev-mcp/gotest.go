package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type goTestEvent struct {
	Action      string  `json:"Action"`
	Package     string  `json:"Package"`
	Test        string  `json:"Test"`
	Elapsed     float64 `json:"Elapsed"`
	Output      string  `json:"Output"`
	FailedBuild string  `json:"FailedBuild"`
}

type goTestCase struct {
	name    string
	pkg     string
	status  string
	elapsed float64
	output  []string
}

func toolServerTest(args map[string]any) (string, error) {
	goExe, err := lookGo()
	if err != nil {
		return "", err
	}
	serverDir := filepath.Join(repoRoot, "Knight-Server")
	if _, err := os.Stat(filepath.Join(serverDir, "go.mod")); err != nil {
		return "", fmt.Errorf("Knight-Server 下找不到 go.mod（服务端骨架尚未落地），server_test 暂不可用")
	}
	packages := argString(args, "packages", "./...")
	runFilter := argString(args, "run", "")
	verbose := argBool(args, "verbose")
	timeoutSec := argNumber(args, "timeout_sec", 300)

	goArgs := []string{"test", "-json"}
	if runFilter != "" {
		goArgs = append(goArgs, "-run", runFilter)
	}
	if verbose {
		goArgs = append(goArgs, "-v")
	}
	goArgs = append(goArgs, packages)

	start := time.Now()
	output, err := runCaptured(serverDir, timeoutSec, goExe, goArgs...)
	elapsed := time.Since(start).Seconds()

	tests := map[string]*goTestCase{} // key: pkg + "::" + test
	pkgOutputs := map[string][]string{}
	pkgStatus := map[string]string{}
	pkgFailedBuild := map[string]string{}
	var order []string

	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	nonJSON := []string{}
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var ev goTestEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			nonJSON = append(nonJSON, line)
			continue
		}
		if ev.Package == "" {
			continue
		}
		if _, seen := pkgStatus[ev.Package]; !seen {
			pkgStatus[ev.Package] = ""
			order = append(order, ev.Package)
		}
		if ev.Test == "" {
			if ev.Output != "" {
				pkgOutputs[ev.Package] = append(pkgOutputs[ev.Package], ev.Output)
			}
			switch ev.Action {
			case "pass", "fail", "skip":
				pkgStatus[ev.Package] = ev.Action
			}
			if ev.FailedBuild != "" {
				pkgFailedBuild[ev.Package] = ev.FailedBuild
			}
			continue
		}
		key := ev.Package + "::" + ev.Test
		tc := tests[key]
		if tc == nil {
			tc = &goTestCase{pkg: ev.Package, name: ev.Test}
			tests[key] = tc
		}
		switch ev.Action {
		case "run":
			tc.status = "run"
		case "pass", "fail", "skip":
			tc.status = ev.Action
			tc.elapsed = ev.Elapsed
		case "output":
			tc.output = append(tc.output, ev.Output)
		}
	}

	var passed, failed, skipped int
	var failingTests []*goTestCase
	for _, tc := range tests {
		switch tc.status {
		case "pass":
			passed++
		case "fail":
			failed++
			failingTests = append(failingTests, tc)
		case "skip":
			skipped++
		}
	}
	sort.Slice(failingTests, func(i, j int) bool { return failingTests[i].pkg+ failingTests[i].name < failingTests[j].pkg+failingTests[j].name })

	var sb strings.Builder
	if err != nil {
		fmt.Fprintf(&sb, "go test 退出异常（%.1fs）\n", elapsed)
	} else {
		fmt.Fprintf(&sb, "go test 完成（%.1fs）\n", elapsed)
	}

	var failedPkgs []string
	for _, pkg := range order {
		if pkgStatus[pkg] == "fail" {
			failedPkgs = append(failedPkgs, pkg)
		}
	}

	fmt.Fprintf(&sb, "汇总: %d 通过, %d 失败, %d 跳过 | 包: %d 个，%d 个失败\n",
		passed, failed, skipped, len(order), len(failedPkgs))

	if len(failedPkgs) > 0 {
		sb.WriteString("\n== 包级失败（含编译错误）==\n")
		for _, pkg := range failedPkgs {
			sb.WriteString("--- " + pkg + " ---\n")
			if fb := pkgFailedBuild[pkg]; fb != "" && fb != pkg {
				sb.WriteString("（编译失败源: " + fb + "）\n")
			}
			sb.WriteString(strings.Join(pkgOutputs[pkg], ""))
		}
	}

	if len(failingTests) > 0 {
		sb.WriteString("\n== 失败用例 ==\n")
		for _, tc := range failingTests {
			fmt.Fprintf(&sb, "--- %s (%.2fs) ---\n", tc.name, tc.elapsed)
			sb.WriteString(strings.Join(tc.output, ""))
		}
	}

	if err == nil && failed == 0 && len(failedPkgs) == 0 {
		sb.WriteString("\n全部通过。\n")
	}
	if len(nonJSON) > 0 {
		sb.WriteString("\n-- 非 JSON 输出（stderr 混入，通常无害）--\n" + tailLines(strings.Join(nonJSON, "\n"), 15) + "\n")
	}
	if err != nil {
		sb.WriteString("\n-- 进程级错误 --\n" + err.Error() + "\n")
		if strings.TrimSpace(output) == "" {
			sb.WriteString("（无任何 go test 输出——检查 Knight-Server 目录与 go 工具链）\n")
		}
	}
	return sb.String(), nil
}
