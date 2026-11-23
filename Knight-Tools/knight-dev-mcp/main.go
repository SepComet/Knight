// knight-dev-mcp：Knight（Godot 客户端 + Go 服务端）开发工作流 MCP server（stdio）。
// 提供六个工具：luban_export（Luban 配置导出，json+bin 中转合成 Godot .tres）、server_test（Go 服务端测试）、
// consistency_check（Luban 表一致性/生成文件头/实体场景存在性预检）、
// deploy（Docker 栈一键部署）、smoke（HTTP 账号冒烟 healthz+注册/登录）、
// godot_verify（dotnet 编译检查与 xUnit 测试）。
// 另带 json2tres CLI 子命令（供 gen_cli.bat/sh 调用）：knight-dev-mcp json2tres <json目录>。
// 仅依赖 Go 标准库，构建：go build -o bin/knight-dev-mcp.exe .
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

const serverName = "knight-dev"
const serverVersion = "0.6.0"

const maxContentChars = 48000

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// 工具串行执行：Luban 导出与 go test 都是重活，避免并发互踩。
var toolMu sync.Mutex

var repoRoot string

func main() {
	if len(os.Args) > 1 {
		os.Exit(runCLI(os.Args[1:]))
	}

	repoRoot = resolveRepoRoot()
	if repoRoot == "" {
		fmt.Fprintln(os.Stderr, "[knight-dev-mcp] 无法定位仓库根目录：请通过 KNIGHT_REPO_ROOT 环境变量指定（应包含 Knight-Proto 与 Knight-Server）")
		os.Exit(1)
	}

	reader := bufio.NewReaderSize(os.Stdin, 1<<20)
	enc := json.NewEncoder(os.Stdout)
	for {
		line, err := reader.ReadBytes('\n')
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			handleLine(trimmed, enc)
		}
		if err != nil {
			if err != io.EOF {
				fmt.Fprintln(os.Stderr, "[knight-dev-mcp] stdin 读取错误:", err)
			}
			return
		}
	}
}

func handleLine(line []byte, enc *json.Encoder) {
	var req rpcRequest
	if err := json.Unmarshal(line, &req); err != nil {
		writeError(enc, nil, -32700, "parse error: "+err.Error())
		return
	}
	if req.Method == "" {
		return
	}
	// 通知（无 id）不回复。
	if len(req.ID) == 0 || string(req.ID) == "null" {
		return
	}

	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		version := p.ProtocolVersion
		if version == "" {
			version = "2025-06-18"
		}
		writeResult(enc, req.ID, map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": serverName, "version": serverVersion},
		"instructions": "Knight 仓库开发工具：服务端改动用 server_test 跑测试；客户端代码改完用 godot_verify 验证" +
				"（mode=check 秒回环境状态；mode=compile 只查编译；mode=tests 编译+xUnit 测试一条命令完成）；" +
				"luban_export 与 consistency_check 服务于 Luban 配置流水线；" +
				"deploy 与 smoke 服务于服务端容器栈（HTTP 冒烟默认 127.0.0.1:37780）",
		})
	case "ping":
		writeResult(enc, req.ID, map[string]any{})
	case "tools/list":
		writeResult(enc, req.ID, map[string]any{"tools": toolDefinitions()})
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			writeError(enc, req.ID, -32602, "invalid params: "+err.Error())
			return
		}
		tool, ok := findTool(p.Name)
		if !ok {
			writeError(enc, req.ID, -32602, "unknown tool: "+p.Name)
			return
		}
		var args map[string]any
		if len(p.Arguments) > 0 {
			if err := json.Unmarshal(p.Arguments, &args); err != nil {
				writeError(enc, req.ID, -32602, "invalid arguments: "+err.Error())
				return
			}
		}
		toolMu.Lock()
		text, toolErr := runToolSafely(tool, args)
		toolMu.Unlock()
		if toolErr != nil {
			writeResult(enc, req.ID, map[string]any{
				"content": []map[string]any{{"type": "text", "text": truncate(toolErr.Error(), maxContentChars)}},
				"isError": true,
			})
			return
		}
		writeResult(enc, req.ID, map[string]any{
			"content": []map[string]any{{"type": "text", "text": truncate(text, maxContentChars)}},
			"isError": false,
		})
	default:
		writeError(enc, req.ID, -32601, "method not found: "+req.Method)
	}
}

// runCLI 提供脱离 MCP 会话的子命令（供 gen_cli.bat/sh 与手工调用）；
// 不带参数时保持原有 stdio MCP server 行为。
func runCLI(args []string) int {
	switch args[0] {
	case "json2tres":
		dir := "."
		if len(args) > 1 {
			dir = args[1]
		}
		n, err := convertJsonToTres(dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[json2tres] 失败（已转换 %d 个）: %v\n", n, err)
			return 1
		}
		fmt.Printf("[json2tres] 完成: %d 个 json → tres（json 已删除）\n", n)
		return 0
	case "check":
		repoRoot = resolveRepoRoot()
		if repoRoot == "" {
			fmt.Fprintln(os.Stderr, "[check] 无法定位仓库根目录：请通过 KNIGHT_REPO_ROOT 环境变量指定")
			return 1
		}
		report, err := runConsistencyCheck()
		if err != nil {
			fmt.Fprintf(os.Stderr, "[check] 执行失败: %v\n", err)
			return 1
		}
		// 有缺失/警告才逐行输出，干净时不输出任何内容。
		fmt.Print(renderCheckCompact(report))
		return 0
	default:
		fmt.Fprintf(os.Stderr, "未知子命令 %q；用法: knight-dev-mcp json2tres <json目录> | check（无参数时作为 stdio MCP server 运行）\n", args[0])
		return 2
	}
}

func runToolSafely(tool tool, args map[string]any) (text string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("tool %s panicked: %v", tool.name, r)
		}
	}()
	return tool.fn(args)
}

func writeResult(enc *json.Encoder, id json.RawMessage, result any) {
	_ = enc.Encode(rpcResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func writeError(enc *json.Encoder, id json.RawMessage, code int, msg string) {
	_ = enc.Encode(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}})
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n...[输出已截断]"
}

// resolveRepoRoot 依次尝试：KNIGHT_REPO_ROOT 环境变量、cwd 向上找（含 Knight-Proto/knight.proto + Knight-Server）、
// 可执行文件所在目录向上找。
func resolveRepoRoot() string {
	if v := os.Getenv("KNIGHT_REPO_ROOT"); v != "" && isRepoRoot(v) {
		return v
	}
	for _, base := range []string{cwdOrEmpty(), executableDirOrEmpty()} {
		dir := base
		for dir != "" {
			if isRepoRoot(dir) {
				return dir
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	return ""
}

func isRepoRoot(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "Knight-Proto", "knight.proto"))
	if err != nil || info.IsDir() {
		return false
	}
	info, err = os.Stat(filepath.Join(dir, "Knight-Server"))
	return err == nil && info.IsDir()
}

func cwdOrEmpty() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	return dir
}

func executableDirOrEmpty() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Dir(exe)
}
