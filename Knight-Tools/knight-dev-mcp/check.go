package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type checkReport struct {
	Groups map[string]any `json:"groups"`
	Warnings []string     `json:"warnings,omitempty"`
	Summary  string       `json:"summary"`
}

func toolConsistencyCheck(args map[string]any) (string, error) {
	report, err := runConsistencyCheck()
	if err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// runConsistencyCheck 供 MCP 工具（完整 JSON）与 CLI check 子命令（紧凑输出）共用。
func runConsistencyCheck() (*checkReport, error) {
	report := &checkReport{Groups: map[string]any{}}

	tableGroup, tableViolations, tableWarnings := checkLubanTables()
	report.Groups["tables"] = tableGroup
	report.Warnings = append(report.Warnings, tableWarnings...)

	markerGroup, markerViolations := checkGeneratedMarkers()
	report.Groups["generated_markers"] = markerGroup

	entityGroup, entityViolations, entityWarnings := checkEntityPrefabs()
	report.Groups["entity_prefabs"] = entityGroup
	report.Warnings = append(report.Warnings, entityWarnings...)

	totalViolations := len(tableViolations) + len(markerViolations) + len(entityViolations)
	report.Summary = fmt.Sprintf("%d 组检查：%d 个违规，%d 个警告", len(report.Groups), totalViolations, len(report.Warnings))
	return report, nil
}

// ---------- 组 1：Luban 表名多处一致 ----------

type lubanTableRow struct {
	FullName  string
	ValueType string
	Input     string
	Output    string
	Mode      string
}

func checkLubanTables() (map[string]any, []string, []string) {
	violations := []string{}
	warnings := []string{}

	genDir := filepath.Join(repoRoot, "Knight-Client", "Scripts", "Base", "Gen", "DataTable")
	dataDir := filepath.Join(repoRoot, "Knight-Client", "DataTable")
	datasDir := filepath.Join(repoRoot, "Knight-Design", "Datas")
	tablesXlsx := filepath.Join(datasDir, "__tables__.xlsx")
	tablesCs := filepath.Join(genDir, "Tables.cs")
	lubanComponentCs := filepath.Join(repoRoot, "Knight-Client", "Scripts", "Runtime", "Luban", "LubanComponent.cs")

	rows, err := readXlsxSheet(tablesXlsx)
	if err != nil {
		v := []string{"读取 __tables__.xlsx 失败: " + err.Error() + "（若 Excel 正开着该文件，请先关闭）"}
		return groupResult(nil, v), v, nil
	}
	tables, err := parseTablesRegistry(rows)
	if err != nil {
		v := []string{"解析 __tables__.xlsx 注册行失败: " + err.Error()}
		return groupResult(nil, v), v, nil
	}

	tablesCsData, err := os.ReadFile(tablesCs)
	if err != nil {
		v := []string{"读取 Tables.cs 失败: " + err.Error()}
		return groupResult(nil, v), v, nil
	}
	if _, statErr := os.Stat(lubanComponentCs); statErr != nil {
		// 客户端尚未写 Luban 加载组件：仅提示，其余检查照常。
		warnings = append(warnings, "客户端 Luban 组件尚未创建（期望 Knight-Client/Scripts/Runtime/Luban/LubanComponent.cs）")
	}

	loaderArgs := captureAll(`loader\("([^"]+)"\)`, string(tablesCsData))
	newClasses := captureAll(`new\s+(Tb\w+)\s*\(`, string(tablesCsData))
	// 注：TableNames 描述符数组检查已随反射工厂方案移除 —— bin 路径下数据与生成代码
	// 同源同步，客户端无需表清单；改为校验每个 tres 携带 metadata/bin 载荷（见下方 expectedStems 循环）。

	dataStems := map[string]bool{}
	if entries, err := os.ReadDir(dataDir); err == nil {
		for _, e := range entries {
			if e.IsDir() || strings.HasSuffix(e.Name(), ".meta") {
				continue
			}
			dataStems[strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))] = true
		}
	} else {
		violations = append(violations, "读取 DataTable 目录失败: "+err.Error())
	}

	genTbFiles := map[string]bool{}
	if entries, err := os.ReadDir(genDir); err == nil {
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasPrefix(name, "Tb") || !strings.HasSuffix(name, ".cs") {
				continue
			}
			genTbFiles[strings.TrimSuffix(name, ".cs")] = true
		}
	}

	expectedStems := map[string]bool{}
	registryLowers := map[string]bool{}
	registryNames := map[string]bool{}
	for _, row := range tables {
		if row.FullName == "" {
			continue
		}
		registryNames[row.FullName] = true
		lower := strings.ToLower(row.FullName)
		registryLowers[lower] = true

		if _, ok := genTbFiles[row.FullName]; !ok {
			violations = append(violations, fmt.Sprintf("表 %s：缺少生成类文件 Gen/%s.cs", row.FullName, row.FullName))
		}
		if row.ValueType != "" && row.ValueType[0] >= 'A' && row.ValueType[0] <= 'Z' {
			if _, err := os.Stat(filepath.Join(genDir, row.ValueType+".cs")); err != nil {
				violations = append(violations, fmt.Sprintf("表 %s：缺少 value_type 行类文件 Gen/%s.cs", row.FullName, row.ValueType))
			}
		}
		stem := row.Output
		if stem == "" {
			stem = lower
		}
		stem = strings.TrimSuffix(stem, ".json")
		expectedStems[stem] = true
		if !dataStems[stem] {
			violations = append(violations, fmt.Sprintf("表 %s：DataTable 缺少产物 %s.tres", row.FullName, stem))
		} else {
			// bin 路径下 tres 必须同时携带 JSON 行与 base64 bin 载荷。
			tresData, rerr := os.ReadFile(filepath.Join(dataDir, stem+".tres"))
			if rerr != nil {
				violations = append(violations, fmt.Sprintf("表 %s：读取 %s.tres 失败: %v", row.FullName, stem, rerr))
			} else if !bytes.Contains(tresData, []byte("metadata/bin")) {
				violations = append(violations, fmt.Sprintf("表 %s：%s.tres 缺少 metadata/bin 载荷（需重跑 gen_cli 流水线）", row.FullName, stem))
			}
		}
		if !contains(loaderArgs, lower) {
			violations = append(violations, fmt.Sprintf("表 %s：Tables.cs 构造参数缺少 loader(\"%s\")", row.FullName, lower))
		}
		if !contains(newClasses, row.FullName) {
			violations = append(violations, fmt.Sprintf("表 %s：Tables.cs 缺少 new %s( 构造", row.FullName, row.FullName))
		}
		if row.Input != "" {
			for _, part := range strings.Split(row.Input, ",") {
				inputName := strings.TrimSpace(strings.SplitN(part, "@", 2)[0])
				if inputName == "" {
					continue
				}
				if _, err := os.Stat(filepath.Join(datasDir, inputName)); err != nil {
					violations = append(violations, fmt.Sprintf("表 %s：input 工作簿不存在 Datas/%s", row.FullName, inputName))
				}
			}
		}
	}

	for _, arg := range loaderArgs {
		if !registryLowers[arg] {
			violations = append(violations, fmt.Sprintf("Tables.cs 构造参数 loader(\"%s\") 在 __tables__.xlsx 中没有对应注册行", arg))
		}
	}
	var genTbSorted []string
	for name := range genTbFiles {
		genTbSorted = append(genTbSorted, name)
	}
	sort.Strings(genTbSorted)
	for _, name := range genTbSorted {
		if !registryNames[name] {
			violations = append(violations, fmt.Sprintf("Gen/%s.cs 未在 __tables__.xlsx 注册（stale 生成文件或表名不一致）", name))
		}
	}
	var dataSorted []string
	for stem := range dataStems {
		dataSorted = append(dataSorted, stem)
	}
	sort.Strings(dataSorted)
	for _, stem := range dataSorted {
		if expectedStems[stem] {
			continue
		}
		violations = append(violations, fmt.Sprintf("DataTable/%s.* 无对应注册表（stale 产物或表名不一致）", stem))
	}

	return groupResult(map[string]any{
		"registry_count": len(tables),
		"loader_args":    len(loaderArgs),
	}, violations), violations, warnings
}

// parseTablesRegistry 从 __tables__.xlsx 首个 sheet 的行中提取注册行：
// 表头行是含 full_name 的行，其下每行一条注册（跳过 # 开头的注释行与空行）。
func parseTablesRegistry(rows [][]string) ([]lubanTableRow, error) {
	headerIdx := -1
	col := map[string]int{}
	for i, row := range rows {
		for _, cell := range row {
			if strings.TrimSpace(cell) == "full_name" {
				headerIdx = i
				for k, c := range row {
					col[strings.TrimSpace(c)] = k
				}
				break
			}
		}
		if headerIdx >= 0 {
			break
		}
	}
	if headerIdx < 0 {
		return nil, fmt.Errorf("未找到 full_name 表头行")
	}
	get := func(row []string, key string) string {
		idx, ok := col[key]
		if !ok || idx >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[idx])
	}
	var tables []lubanTableRow
	for _, row := range rows[headerIdx+1:] {
		first := ""
		for _, c := range row {
			if strings.TrimSpace(c) != "" {
				first = strings.TrimSpace(c)
				break
			}
		}
		if first == "" || strings.HasPrefix(first, "#") {
			continue
		}
		tables = append(tables, lubanTableRow{
			FullName:  get(row, "full_name"),
			ValueType: get(row, "value_type"),
			Input:     get(row, "input"),
			Output:    get(row, "output"),
			Mode:      get(row, "mode"),
		})
	}
	return tables, nil
}

// ---------- 组 2：生成文件头标记 ----------

func checkGeneratedMarkers() (map[string]any, []string) {
	missing := []string{}
	checked := 0

	genDir := filepath.Join(repoRoot, "Knight-Client", "Scripts", "Base", "Gen")

	// Gen 下按来源分了 Proto/DataTable 子目录，递归扫描全部生成 .cs（含协议与数据表定义）。
	// 注：不再扫 Scripts/UI——Godot 下 UI View 全部手写，不存在"生成的 Form/View"约定。
	_ = filepath.WalkDir(genDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".cs") {
			return nil
		}
		checked++
		if !hasAutoGeneratedMarker(path) {
			missing = append(missing, filepath.ToSlash(path)+" 缺少 <auto-generated> 标记")
		}
		return nil
	})

	return map[string]any{"checked": checked, "violations": missing}, missing
}

func hasAutoGeneratedMarker(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	if len(data) > 4096 {
		data = data[:4096]
	}
	return bytes.Contains(data, []byte("<auto-generated"))
}

// ---------- 组 3：实体场景（.tscn）存在性 ----------

// entityPrefabRoot EntityConfig 场景列（tscnPath，兼容旧列名 prefabPath）相对此目录解析
// （Godot 约定实体资源为 .tscn 场景，不再是 Unity 的 .prefab；实际目录是单数 Entity，
// 与 Prefabs/UI 平级）。Luban 侧该列只是 string 不校验，存在性在这里判。
var entityPrefabRoot = filepath.Join("Knight-Client", "Prefabs", "Entity")

func checkEntityPrefabs() (map[string]any, []string, []string) {
	violations := []string{}
	warnings := []string{}
	checked := 0

	datasDir := filepath.Join(repoRoot, "Knight-Design", "Datas")
	rows, err := readXlsxSheet(filepath.Join(datasDir, "__tables__.xlsx"))
	if err != nil {
		v := []string{"读取 __tables__.xlsx 失败: " + err.Error()}
		return groupResult(nil, v), v, nil
	}
	tables, err := parseTablesRegistry(rows)
	if err != nil {
		v := []string{"解析 __tables__.xlsx 注册行失败: " + err.Error()}
		return groupResult(nil, v), v, nil
	}
	input := ""
	for _, t := range tables {
		if strings.EqualFold(t.FullName, "TbEntityConfig") {
			input = t.Input
			break
		}
	}
	if input == "" {
		warnings = append(warnings, "__tables__.xlsx 中未注册 TbEntityConfig，跳过实体场景检查")
		return groupResult(map[string]any{"checked": 0}, violations), violations, warnings
	}

	tableRows, err := readXlsxSheet(filepath.Join(datasDir, input))
	if err != nil {
		v := []string{"读取 " + input + " 失败: " + err.Error()}
		return groupResult(nil, v), v, nil
	}
	// 表头约定：##var 行定义列名，场景列持 .tscn 相对路径（tscnPath，兼容旧列名 prefabPath）。
	prefabCol := -1
	nameCol := -1
	for i, cell := range tableRows[0] {
		switch strings.TrimSpace(cell) {
		case "tscnPath", "prefabPath":
			prefabCol = i
		case "assetName":
			nameCol = i
		}
	}
	if prefabCol < 0 {
		warnings = append(warnings, input+" 表头没有 tscnPath/prefabPath 列，跳过实体场景检查")
		return groupResult(map[string]any{"checked": 0}, violations), violations, warnings
	}

	root := filepath.Join(repoRoot, entityPrefabRoot)
	for _, row := range tableRows[1:] {
		// 跳过 ##type / ##group / ## 说明等约定行。
		first := ""
		for _, c := range row {
			if strings.TrimSpace(c) != "" {
				first = strings.TrimSpace(c)
				break
			}
		}
		if strings.HasPrefix(first, "##") {
			continue
		}
		if prefabCol >= len(row) || strings.TrimSpace(row[prefabCol]) == "" {
			continue
		}
		name := ""
		if nameCol >= 0 && nameCol < len(row) {
			name = strings.TrimSpace(row[nameCol])
		}
		val := filepath.ToSlash(strings.TrimSpace(row[prefabCol]))
		checked++
		if !strings.EqualFold(filepath.Ext(val), ".tscn") {
			violations = append(violations, fmt.Sprintf("实体 %s：场景路径应指向 .tscn（当前 %s）", name, val))
			continue
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(val))); err != nil {
			violations = append(violations, fmt.Sprintf("实体 %s：场景不存在 %s/%s", name, entityPrefabRoot, val))
		}
	}
	return groupResult(map[string]any{"checked": checked, "root": entityPrefabRoot}, violations), violations, warnings
}

// ---------- 小工具 ----------

// stringSliceFrom 从 group map 里安全取 []string——兼容 JSON 反序列化的 []any 与 Go 原生的 []string。
func stringSliceFrom(v any) []string {
	switch vs := v.(type) {
	case []string:
		return vs
	case []any:
		out := make([]string, 0, len(vs))
		for _, item := range vs {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// renderCheckCompact 把检查结果压成人类可读短输出：有缺失/警告才逐行输出，干净时返回空串。
func renderCheckCompact(report *checkReport) string {
	var sb strings.Builder
	totalViolations := 0
	groupNames := make([]string, 0, len(report.Groups))
	for name := range report.Groups {
		groupNames = append(groupNames, name)
	}
	sort.Strings(groupNames)
	for _, name := range groupNames {
		g, ok := report.Groups[name].(map[string]any)
		if !ok {
			continue
		}
		for _, v := range stringSliceFrom(g["violations"]) {
			fmt.Fprintf(&sb, "[check][%s] %s\n", name, v)
			totalViolations++
		}
	}
	for _, w := range report.Warnings {
		fmt.Fprintf(&sb, "[check][警告] %s\n", w)
	}
	if totalViolations > 0 {
		fmt.Fprintf(&sb, "[check] 共 %d 个违规\n", totalViolations)
	}
	return sb.String()
}

func groupResult(extra map[string]any, violations []string) map[string]any {
	result := map[string]any{"violations": violations}
	for k, v := range extra {
		result[k] = v
	}
	return result
}

func captureAll(pattern, s string) []string {
	re := regexp.MustCompile(pattern)
	matches := re.FindAllStringSubmatch(s, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if len(m) > 1 {
			out = append(out, strings.TrimSpace(m[1]))
		}
	}
	return out
}

func contains(list []string, target string) bool {
	for _, v := range list {
		if v == target {
			return true
		}
	}
	return false
}

func sameOrder(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---------- 最小 xlsx 读取（首个 sheet，全列） ----------

// readXlsxSheet 读取 xlsx 首个 sheet，返回按行、按列对齐的文本。
// 只覆盖本项目所需：shared string / str / inlineStr / 数字原样。
func readXlsxSheet(path string) ([][]string, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = zr.Close() }()

	find := func(name string) *zip.File {
		for _, f := range zr.File {
			if f.Name == name {
				return f
			}
		}
		return nil
	}

	shared := map[int]string{}
	if f := find("xl/sharedStrings.xml"); f != nil {
		data, err := readZipFile(f)
		if err != nil {
			return nil, fmt.Errorf("读取 sharedStrings: %w", err)
		}
		shared, err = parseSharedStrings(data)
		if err != nil {
			return nil, fmt.Errorf("解析 sharedStrings: %w", err)
		}
	}

	sheetPath := "xl/worksheets/sheet1.xml"
	if f := find("xl/workbook.xml"); f != nil {
		data, err := readZipFile(f)
		if err == nil {
			if rid := firstSheetRid(data); rid != "" {
				if relFile := find("xl/_rels/workbook.xml.rels"); relFile != nil {
					relData, err := readZipFile(relFile)
					if err == nil {
						if target := relTarget(relData, rid); target != "" {
							if strings.HasPrefix(target, "/") {
								sheetPath = target[1:]
							} else {
								sheetPath = "xl/" + target
							}
						}
					}
				}
			}
		}
	}

	sheetFile := find(sheetPath)
	if sheetFile == nil {
		return nil, fmt.Errorf("xlsx 中未找到 %s", sheetPath)
	}
	data, err := readZipFile(sheetFile)
	if err != nil {
		return nil, err
	}
	return parseSheetRows(data, shared)
}

func readZipFile(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	return io.ReadAll(rc)
}

func parseSharedStrings(data []byte) (map[int]string, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	out := map[int]string{}
	idx := 0
	inT := false
	var cur strings.Builder
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "si":
				cur.Reset()
			case "t":
				inT = true
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inT = false
			case "si":
				out[idx] = cur.String()
				idx++
			}
		case xml.CharData:
			if inT {
				cur.Write(t)
			}
		}
	}
	return out, nil
}

// firstSheetRid 返回 workbook.xml 中第一个 sheet 的 r:id。
func firstSheetRid(data []byte) string {
	dec := xml.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := dec.Token()
		if err != nil {
			return ""
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "sheet" {
			for _, attr := range se.Attr {
				if attr.Name.Local == "id" && strings.Contains(attr.Name.Space, "relationships") {
					return attr.Value
				}
			}
		}
	}
}

func relTarget(data []byte, rid string) string {
	dec := xml.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := dec.Token()
		if err != nil {
			return ""
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "Relationship" {
			var id, target string
			for _, attr := range se.Attr {
				switch attr.Name.Local {
				case "Id":
					id = attr.Value
				case "Target":
					target = attr.Value
				}
			}
			if id == rid {
				return target
			}
		}
	}
}

func parseSheetRows(data []byte, shared map[int]string) ([][]string, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	var rows [][]string
	curRow := map[int]string{}
	curCol := -1
	curT := ""
	inV := false
	inInline := false
	var buf strings.Builder

	flushRow := func() {
		if len(curRow) == 0 {
			return
		}
		maxCol := -1
		for c := range curRow {
			if c > maxCol {
				maxCol = c
			}
		}
		row := make([]string, maxCol+1)
		for c, v := range curRow {
			row[c] = v
		}
		rows = append(rows, row)
	}

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "row":
				curRow = map[int]string{}
			case "c":
				curCol = -1
				curT = ""
				buf.Reset()
				for _, attr := range t.Attr {
					switch attr.Name.Local {
					case "r":
						curCol = colIndexOf(attr.Value)
					case "t":
						curT = attr.Value
					}
				}
			case "v":
				inV = true
				buf.Reset()
			case "is":
				inInline = true
				buf.Reset()
			}
		case xml.CharData:
			if inV || inInline {
				buf.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v":
				inV = false
				if curCol >= 0 {
					curRow[curCol] = cellText(curT, buf.String(), shared)
				}
			case "is":
				inInline = false
				if curCol >= 0 {
					curRow[curCol] = buf.String()
				}
			case "row":
				flushRow()
			}
		}
	}
	return rows, nil
}

func cellText(t, raw string, shared map[int]string) string {
	switch t {
	case "s":
		idx, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			return raw
		}
		if v, ok := shared[idx]; ok {
			return v
		}
		return raw
	default:
		return raw
	}
}

// colIndexOf 把 "B3" 这类单元格引用转成 0 基列号。
func colIndexOf(ref string) int {
	n := 0
	for _, r := range ref {
		switch {
		case r >= 'A' && r <= 'Z':
			n = n*26 + int(r-'A') + 1
		case r >= 'a' && r <= 'z':
			n = n*26 + int(r-'a') + 1
		default:
			return n - 1
		}
	}
	return n - 1
}
