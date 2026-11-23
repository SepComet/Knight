package main

import (
	"strings"
	"testing"
)

func TestRenderCheckCompact_ListsViolationsAndWarnings(t *testing.T) {
	report := &checkReport{
		Groups: map[string]any{
			"tables":            groupResult(map[string]any{"registry_count": 13}, []string{"表 TbX：DataTable 缺少产物 tbx.tres"}),
			"entity_prefabs":    groupResult(map[string]any{"checked": 14}, []string{"实体 ItemEntity：场景不存在 ItemEntity.tscn"}),
			"generated_markers": map[string]any{"checked": 35, "violations": []any{}},
		},
		Warnings: []string{"客户端 Luban 组件尚未创建（期望 Knight-Client/Scripts/Runtime/Luban/LubanComponent.cs）"},
	}
	out := renderCheckCompact(report)
	for _, want := range []string{
		"[check][entity_prefabs] 实体 ItemEntity：场景不存在 ItemEntity.tscn",
		"[check][tables] 表 TbX：DataTable 缺少产物 tbx.tres",
		"[check][警告] 客户端 Luban 组件尚未创建",
		"[check] 共 2 个违规",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("紧凑输出缺少 %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "generated_markers") {
		t.Fatalf("干净组不应出现在输出中\n%s", out)
	}
}

func TestRenderCheckCompact_CleanIsSilent(t *testing.T) {
	clean := &checkReport{
		Groups: map[string]any{
			"tables":          groupResult(map[string]any{"registry_count": 13}, nil),
			"entity_prefabs":  groupResult(map[string]any{"checked": 14}, nil),
		},
	}
	if out := renderCheckCompact(clean); out != "" {
		t.Fatalf("干净时应无输出，实际:\n%s", out)
	}
}
