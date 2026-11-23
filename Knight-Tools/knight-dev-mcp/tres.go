package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// json→Godot .tres 适配：Luban 原生只出 json/bin，Godot 侧改用 .tres 资源
// （type=Resource + metadata 双载荷，无脚本依赖）：
//   - metadata/data：JSON 行数组，供编辑器/工具直读；
//   - metadata/bin： Luban bin（base64），供客户端 LubanComponent 直接喂给生成的
//     ByteBuf 构造函数——类型与字段顺序由生成代码保证，客户端零反射。
// .tres 随 res:// 自动进 PCK 导出。

// convertJsonToTres 把 dataDir 下 Luban 导出的 tbxxx.json 逐个转成同名 .tres，
// 同名 tbxxx.bytes（Luban bin）以 base64 嵌入后与 json 一并删除。
// bytes 缺失视为流水线错误（客户端装载依赖 bin 载荷）。
// 单个文件失败不影响其余转换，返回首个错误供调用方报告。
func convertJsonToTres(dataDir string) (converted int, err error) {
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return 0, fmt.Errorf("读取 DataTable 目录失败: %w", err)
	}
	var firstErr error
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		full := filepath.Join(dataDir, e.Name())
		base := strings.TrimSuffix(e.Name(), ".json")
		binB64, berr := readBinBase64(filepath.Join(dataDir, base+".bytes"))
		if berr != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", e.Name(), berr)
			}
			continue
		}
		rows, perr := readJSONArray(full)
		if perr != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("解析 %s 失败: %w", e.Name(), perr)
			}
			continue
		}
		tresPath := strings.TrimSuffix(full, ".json") + ".tres"
		if werr := os.WriteFile(tresPath, []byte(buildTres(rows, binB64)), 0o644); werr != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("写出 %s 失败: %w", filepath.Base(tresPath), werr)
			}
			continue
		}
		for _, leftover := range []string{full, filepath.Join(dataDir, base+".bytes")} {
			if rerr := os.Remove(leftover); rerr != nil && !os.IsNotExist(rerr) && firstErr == nil {
				firstErr = fmt.Errorf("删除 %s 失败: %w", filepath.Base(leftover), rerr)
			}
		}
		converted++
	}
	return converted, firstErr
}

// readBinBase64 读取 Luban bin 产物并编码为 base64。
func readBinBase64(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("缺少 bin 产物（需 Luban -d bin 导出）: %w", err)
	}
	return base64.StdEncoding.EncodeToString(data), nil
}

// readJSONArray 解析整文件为 JSON 数组；数字用 json.Number 保留原文，避免 int/float 精度漂移。
func readJSONArray(path string) ([]any, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	dec := json.NewDecoder(bufio.NewReader(f))
	dec.UseNumber()
	var rows []any
	if err := dec.Decode(&rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// buildTres 生成自包含的 Godot 文本资源：data 行数组 + base64 bin 双载荷。
func buildTres(rows []any, binBase64 string) string {
	var sb strings.Builder
	sb.WriteString(`[gd_resource type="Resource" format=3]` + "\n\n[resource]\n")
	if len(rows) == 0 {
		sb.WriteString("metadata/data = []\n")
	} else {
		parts := make([]string, 0, len(rows))
		for _, row := range rows {
			parts = append(parts, tresValue(row))
		}
		// 顶层数组逐行一个 Dictionary，便于 diff；行内嵌套结构保持单行紧凑。
		sb.WriteString("metadata/data = [\n" + strings.Join(parts, ",\n") + "\n]\n")
	}
	sb.WriteString("metadata/bin = " + tresString(binBase64) + "\n")
	return sb.String()
}

// tresValue 把解码后的 JSON 值写成 Godot Variant 文本。
// Dictionary 键排序输出，保证产物对 git 稳定。
func tresValue(v any) string {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(t))
		for _, k := range keys {
			parts = append(parts, tresString(k)+": "+tresValue(t[k]))
		}
		return "{ " + strings.Join(parts, ", ") + " }"
	case []any:
		parts := make([]string, 0, len(t))
		for _, item := range t {
			parts = append(parts, tresValue(item))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case string:
		return tresString(t)
	case json.Number:
		return t.String()
	case bool:
		if t {
			return "true"
		}
		return "false"
	case nil:
		return "null"
	default:
		return fmt.Sprintf("%v", v)
	}
}

// tresString 按 Godot VariantParser 规则转义字符串（UTF-8 原样输出）。
func tresString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
