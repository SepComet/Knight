package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// lookExecutable 解析外部命令：先 PATH，再常见安装路径（本机 go/git 不一定在 PATH 上）。
func lookExecutable(name string, fallbacks ...string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	for _, cand := range fallbacks {
		if info, err := os.Stat(cand); err == nil && !info.IsDir() {
			return cand, nil
		}
	}
	return "", fmt.Errorf("找不到可执行文件 %s：PATH 与常见安装目录均未命中，请安装或通过环境变量指定", name)
}

func lookGo() (string, error) {
	if p := os.Getenv("KNIGHT_GO_PATH"); p != "" {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p, nil
		}
	}
	localAppData := os.Getenv("LOCALAPPDATA")
	return lookExecutable("go",
		filepath.Join(`C:\Program Files\Go`, "bin", "go.exe"),
		filepath.Join(`C:\Go`, "bin", "go.exe"),
		filepath.Join(localAppData, "Programs", "Go", "bin", "go.exe"),
	)
}

func lookDotnet() (string, error) {
	return lookExecutable("dotnet", `C:\Program Files\dotnet\dotnet.exe`)
}

func lookGit() (string, error) {
	return lookExecutable("git", `C:\Program Files\Git\cmd\git.exe`, `C:\Program Files\Git\bin\git.exe`)
}

// runCaptured 在 dir 下执行命令，返回合并后的 stdout+stderr 与退出错误。
// timeout<=0 表示用默认 5 分钟。
func runCaptured(dir string, timeoutSec float64, name string, args ...string) (string, error) {
	if timeoutSec <= 0 {
		timeoutSec = 300
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec*float64(time.Second)))
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	output := buf.String()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return output, fmt.Errorf("命令超时（%.0f 秒）：%s", timeoutSec, name)
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return output, fmt.Errorf("命令退出码 %d：%s", exitErr.ExitCode(), name)
		}
		return output, err
	}
	return output, nil
}
