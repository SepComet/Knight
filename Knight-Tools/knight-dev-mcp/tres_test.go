package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildTresRows(t *testing.T) {
	rows := []any{
		map[string]any{
			"id":    json.Number("1001"),
			"name":  "角色1",
			"flag":  true,
			"rate":  json.Number("0.5"),
			"tags":  []any{"a", "b"},
			"extra": nil,
		},
	}
	out := buildTres(rows, "Ymlu")
	for _, want := range []string{
		`[gd_resource type="Resource" format=3]`,
		`[resource]`,
		`metadata/data = [`,
		`"flag": true`,
		`"id": 1001`,
		`"name": "角色1"`,
		`"rate": 0.5`,
		`"tags": ["a", "b"]`,
		`"extra": null`,
		`metadata/bin = "Ymlu"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("tres 缺少 %q\n%s", want, out)
		}
	}
}

func TestBuildTresEmpty(t *testing.T) {
	out := buildTres(nil, "QQ==")
	if !strings.Contains(out, "metadata/data = []") {
		t.Fatalf("空表应输出 []\n%s", out)
	}
	if !strings.Contains(out, `metadata/bin = "QQ=="`) {
		t.Fatalf("空表也应携带 bin 载荷\n%s", out)
	}
}

func TestTresStringEscaping(t *testing.T) {
	got := tresString(`a"b\c` + "\n\t")
	want := `"a\"b\\c\n\t"`
	if got != want {
		t.Fatalf("tresString = %s, want %s", got, want)
	}
}

func TestConvertJsonToTres(t *testing.T) {
	dir := t.TempDir()
	j1 := filepath.Join(dir, "tbone.json")
	// CRLF + 中文 + 嵌套结构，模拟 Luban 原始产物。
	content := "[\r\n  {\r\n    \"id\": 1,\r\n    \"name\": \"勇者\",\r\n    \"pos\": {\"x\": 1.5, \"y\": 2},\r\n    \"note\": \"带\\\"引号\\\"的文本\"\r\n  }\r\n]\r\n"
	if err := os.WriteFile(j1, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	// bin 载荷与 json 配对，转换后一并删除。
	if err := os.WriteFile(filepath.Join(dir, "tbone.bytes"), []byte("bin"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tbtwo.json"), []byte("[]"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tbtwo.bytes"), []byte("bin2"), 0o644); err != nil {
		t.Fatal(err)
	}

	n, err := convertJsonToTres(dir)
	if err != nil {
		t.Fatalf("convertJsonToTres: %v", err)
	}
	if n != 2 {
		t.Fatalf("converted = %d, want 2", n)
	}
	for _, gone := range []string{j1, filepath.Join(dir, "tbone.bytes"),
		filepath.Join(dir, "tbtwo.json"), filepath.Join(dir, "tbtwo.bytes")} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Fatalf("转换后中间产物应被删除: %s", gone)
		}
	}
	tres1, err := os.ReadFile(filepath.Join(dir, "tbone.tres"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(tres1)
	for _, want := range []string{`"name": "勇者"`, `"pos": { "x": 1.5, "y": 2 }`, `"note": "带\"引号\"的文本"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("tres 缺少 %q\n%s", want, s)
		}
	}
	if !strings.Contains(s, `metadata/bin = "`+base64.StdEncoding.EncodeToString([]byte("bin"))+`"`) {
		t.Fatalf("tres 缺少正确的 bin base64 载荷\n%s", s)
	}
	if strings.Contains(s, "\r") {
		t.Fatalf("tres 不应含 CR")
	}
	if _, err := os.Stat(filepath.Join(dir, "tbtwo.tres")); err != nil {
		t.Fatalf("空表 tres 未生成: %v", err)
	}
}

func TestConvertJsonToTresMissingBytes(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tbx.json"), []byte(`[{"id":1}]`), 0o644); err != nil {
		t.Fatal(err)
	}

	n, err := convertJsonToTres(dir)
	if err == nil {
		t.Fatalf("缺 bin 产物应报错")
	}
	if n != 0 {
		t.Fatalf("converted = %d, want 0", n)
	}
	if _, err := os.Stat(filepath.Join(dir, "tbx.json")); err != nil {
		t.Fatalf("失败的 json 应保留")
	}
	if _, err := os.Stat(filepath.Join(dir, "tbx.tres")); !os.IsNotExist(err) {
		t.Fatalf("缺 bin 时不应生成 tres")
	}
}

func TestConvertJsonToTresBadJson(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "tbgood.json")
	if err := os.WriteFile(good, []byte(`[{"id":1}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tbgood.bytes"), []byte("bin"), 0o644); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(dir, "tbbad.json")
	if err := os.WriteFile(bad, []byte("{不是数组"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tbbad.bytes"), []byte("bin"), 0o644); err != nil {
		t.Fatal(err)
	}

	n, err := convertJsonToTres(dir)
	if err == nil {
		t.Fatalf("坏 json 应报错")
	}
	if n != 1 {
		t.Fatalf("converted = %d, want 1（坏文件不应影响其余转换）", n)
	}
	if _, err := os.Stat(bad); err != nil {
		t.Fatalf("解析失败的 json 应保留")
	}
	if _, err := os.Stat(filepath.Join(dir, "tbbad.bytes")); err != nil {
		t.Fatalf("解析失败表的 bytes 应保留")
	}
	if _, err := os.Stat(filepath.Join(dir, "tbgood.tres")); err != nil {
		t.Fatalf("好文件应正常转换")
	}
}
