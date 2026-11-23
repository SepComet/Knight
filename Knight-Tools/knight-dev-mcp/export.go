package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// toolLubanExport 复刻 Knight-Design/gen_cli.bat 的完整行为：
// 1) 读 path.txt 决定输出根（默认 ../Knight-Client）；2) dotnet Luban.dll 导 C#（cs-bin，可清理输出目录）；
// 3) dotnet Luban.dll 只导 json（cleanUpOutputDir=false，防误删 DataTable 下的非 Luban 文件）；
// 4) dotnet Luban.dll 导 bin——.tres 的 metadata/bin 载荷来源，客户端 LubanComponent 装载依赖它；
// 5) json2tres 把 json+bin 合成 .tres 后两者一并删除——tres 是唯一数据产物
//    （bat/sh 经 json2tres 子命令做同样的事）；
// 6) 报告 git 视角下 Gen/DataTable 与 DataTable 的变更。
func toolLubanExport(args map[string]any) (string, error) {
	designDir := filepath.Join(repoRoot, "Knight-Design")
	lubanDll := filepath.Join(designDir, "Tools", "Luban", "Luban.dll")
	confFile := filepath.Join(designDir, "luban.conf")
	customTemplates := filepath.Join(designDir, "CustomTemplates")
	// 与脚本一致：pathValidator.rootDir = Knight-Client/Textures，
	// SpriteConfig.atlasPath（string#path=unity）按此根校验资源存在。
	assetRoot := filepath.Join(repoRoot, "Knight-Client", "Textures")

	for _, p := range []string{lubanDll, confFile, customTemplates} {
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("缺少 Luban 运行要件 %s（Luban 工具链需先就位）", p)
		}
	}

	outputRoot, err := resolveOutputRoot(designDir)
	if err != nil {
		return "", err
	}
	genDir := filepath.Join(outputRoot, "Scripts", "Base", "Gen", "DataTable")
	dataDir := filepath.Join(outputRoot, "DataTable")
	if err := os.MkdirAll(genDir, 0o755); err != nil {
		return "", fmt.Errorf("创建 Gen 输出目录失败: %w", err)
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return "", fmt.Errorf("创建 DataTables 输出目录失败: %w", err)
	}

	dotnet, err := lookDotnet()
	if err != nil {
		return "", err
	}

	var sb strings.Builder

	// 第一步：C# 代码导出。Gen 目录只有生成代码，允许 Luban 清理陈旧输出。
	start := time.Now()
	_, err = runCaptured(designDir, 300, dotnet,
		lubanDll, "-t", "client", "-c", "cs-bin",
		"--conf", confFile,
		"--customTemplateDir", customTemplates,
		"-x", "cs-bin.outputCodeDir="+genDir,
		"-x", "pathValidator.rootDir="+assetRoot,
		"-x", "lineEnding=lf")
	if err != nil {
		return "", fmt.Errorf("Luban C# 导出失败（%.1fs）:\n%s", time.Since(start).Seconds(), tailLines(err.Error(), 60))
	}
	fmt.Fprintf(&sb, "[1/3] C# 代码导出 (cs-bin): OK (%.1fs)\n", time.Since(start).Seconds())

	// 第二步：json 数据导出（作中转，转完即删）；DataTable 下可能有非 Luban 文件，禁止清理输出目录。
	start = time.Now()
	dataOutput, err := runCaptured(designDir, 300, dotnet,
		lubanDll, "-t", "client", "-d", "json",
		"--conf", confFile,
		"-x", "json.outputDataDir="+dataDir,
		"-x", "pathValidator.rootDir="+assetRoot,
		"-x", "cleanUpOutputDir=false")
	if err != nil {
		return "", fmt.Errorf("Luban 数据导出失败（%.1fs）:\n%s\n--- 上一步输出尾部 ---\n%s",
			time.Since(start).Seconds(), tailLines(err.Error(), 60), tailLines(dataOutput, 20))
	}
	fmt.Fprintf(&sb, "[2/3] 数据导出 (json): OK (%.1fs)\n", time.Since(start).Seconds())

	// 第三步：bin 数据导出。tres 的 metadata/bin 载荷来源（客户端 LubanComponent 零反射装载依赖它），
	// 与 gen_cli.bat 一致：cs-bin → json → bin 三连，缺 bin 会导致 json2tres 全表失败。
	start = time.Now()
	binOutput, err := runCaptured(designDir, 300, dotnet,
		lubanDll, "-t", "client", "-d", "bin",
		"--conf", confFile,
		"-x", "bin.outputDataDir="+dataDir,
		"-x", "pathValidator.rootDir="+assetRoot,
		"-x", "cleanUpOutputDir=false")
	if err != nil {
		return "", fmt.Errorf("Luban bin 导出失败（%.1fs）:\n%s\n--- 上一步输出尾部 ---\n%s",
			time.Since(start).Seconds(), tailLines(err.Error(), 60), tailLines(dataOutput, 20))
	}
	fmt.Fprintf(&sb, "[3/3] 数据导出 (bin→tres 载荷): OK (%.1fs)\n", time.Since(start).Seconds())

	converted, convErr := convertJsonToTres(dataDir)
	if convErr != nil {
		fmt.Fprintf(&sb, "json→tres 转换: 部分失败（已转换 %d 个）: %v\n", converted, convErr)
	} else {
		fmt.Fprintf(&sb, "json→tres: 转换 %d 个（json/bin 中转后不落盘，Godot 直接 load .tres）\n", converted)
	}

	changedFiles, gitErr := gitStatusOf(repoRoot, genDir, dataDir)
	if gitErr != nil {
		fmt.Fprintf(&sb, "Git 变更: （跳过——%v）\n", gitErr)
	} else if len(changedFiles) == 0 {
		sb.WriteString("Git 变更: 无（导出结果与工作区一致）\n")
	} else {
		fmt.Fprintf(&sb, "Git 变更（%d 个文件）:\n", len(changedFiles))
		for _, f := range changedFiles {
			sb.WriteString("  " + f + "\n")
		}
	}

	if tail := tailLines(binOutput, 10); tail != "" {
		sb.WriteString("--- Luban bin 导出输出尾部 ---\n" + tail + "\n")
	}
	return sb.String(), nil
}

// normalizeJsonToLF 已移除：json 现在只是中转产物，经 convertJsonToTres 转成 .tres 后即删除，
// LF 由 tres 写出保证（buildTres 固定 \n）。

// resolveOutputRoot 与脚本一致：优先 LUBAN_OUTPUT_ROOT 环境变量，否则取 path.txt 首行；
// 绝对路径直用，相对路径相对 Knight-Design 解析。
func resolveOutputRoot(designDir string) (string, error) {
	input := os.Getenv("LUBAN_OUTPUT_ROOT")
	if input == "" {
		raw, err := os.ReadFile(filepath.Join(designDir, "path.txt"))
		if err != nil {
			return "", fmt.Errorf("读取 path.txt 失败: %w", err)
		}
		line := strings.TrimSpace(strings.SplitN(string(raw), "\n", 2)[0])
		line = strings.TrimRight(line, "\r")
		if line == "" {
			return "", fmt.Errorf("path.txt 首行为空，无法确定输出目录")
		}
		input = line
	}
	if strings.HasPrefix(input, "/") || filepath.IsAbs(input) || (len(input) >= 2 && input[1] == ':') {
		return filepath.Abs(input)
	}
	return filepath.Abs(filepath.Join(designDir, input))
}

// gitStatusOf 返回两个目录下发生变更的文件（porcelain 格式，仓库相对路径）。
func gitStatusOf(root string, dirs ...string) ([]string, error) {
	git, err := lookGit()
	if err != nil {
		return nil, err
	}
	relDirs := make([]string, 0, len(dirs))
	for _, d := range dirs {
		rel, err := filepath.Rel(root, d)
		if err != nil {
			return nil, err
		}
		relDirs = append(relDirs, filepath.ToSlash(rel))
	}
	output, err := runCaptured(root, 60, git, append([]string{"status", "--porcelain", "--"}, relDirs...)...)
	if err != nil {
		return nil, fmt.Errorf("git status 失败: %v", err)
	}
	var files []string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimRight(line, "\r\n ")
		if line == "" {
			continue
		}
		// porcelain 格式：XY <path>，path 可能带引号（含中文时）。
		if len(line) > 3 {
			files = append(files, line)
		}
	}
	return files, nil
}

func tailLines(s string, n int) string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
