package readme

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct{}

type Meta struct {
	Sections []*Section
}

type Section struct {
	Name       string
	Parameters []ParamMeta
}

type ParamMeta struct {
	Name         string
	TypeOriginal string
	TypeName     string
	Description  string
}

type FieldMeta struct {
	ParentTypeName string
	Name           string
	Type           string
	Description    string
}

type ParamToRender struct {
	Path        string
	Description string
	Type        string
	Value       string
}

var valuesMap map[string]interface{}
var typeFields map[string][]FieldMeta

func defaultConfig() *Config { return &Config{} }

func createValuesObject(path string) (map[string]interface{}, error) {
	data, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var vals map[string]interface{}
	if err := yaml.Unmarshal(data, &vals); err != nil {
		return nil, err
	}
	return vals, nil
}

func parseMetadataComments(path string) (*Meta, error) {
	data, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}
	typeFields = make(map[string][]FieldMeta)
	var sections []*Section
	var current *Section

	sectionRe := regexp.MustCompile(`^##\s+@section\s+(.*)$`)
	paramRe := regexp.MustCompile(`^##\s+@param\s+(\w+)\s+\{([^}]+)\}\s+(.*)$`)
	fieldRe := regexp.MustCompile(`^##\s+@field\s+([\w\.]+)\s+\{([^}]+)\}\s+(.*)$`)

	for _, line := range strings.Split(string(data), "\n") {
		if m := sectionRe.FindStringSubmatch(line); m != nil {
			sec := &Section{Name: m[1]}
			sections = append(sections, sec)
			current = sec
			continue
		}
		if m := paramRe.FindStringSubmatch(line); m != nil {
			name, typ, desc := m[1], m[2], m[3]
			typeName := typ
			if strings.HasPrefix(typ, "[]") {
				typeName = typ[2:]
			} else if strings.HasPrefix(typ, "map[") {
				if idx := strings.LastIndex(typ, "]"); idx != -1 && idx+1 < len(typ) {
					typeName = typ[idx+1:]
				}
			}
			pm := ParamMeta{Name: name, TypeOriginal: typ, TypeName: typeName, Description: desc}
			if current != nil {
				current.Parameters = append(current.Parameters, pm)
			} else {
				if len(sections) == 0 {
					sections = append(sections, &Section{Name: "Parameters"})
				}
				sections[0].Parameters = append(sections[0].Parameters, pm)
			}
		} else if m := fieldRe.FindStringSubmatch(line); m != nil {
			qual, typ, desc := m[1], m[2], m[3]
			parts := strings.SplitN(qual, ".", 2)
			root := parts[0]
			field := ""
			if len(parts) == 2 {
				field = parts[1]
			}
			typeFields[root] = append(typeFields[root], FieldMeta{
				ParentTypeName: root,
				Name:           field,
				Type:           typ,
				Description:    desc,
			})
		}
	}
	return &Meta{Sections: sections}, nil
}

func buildAliasMap(params []ParamMeta) map[string]string { return map[string]string{} }

func combine(vals map[string]interface{}) { valuesMap = vals }

func buildParamsToRender(params []ParamMeta) []ParamToRender {
	var out []ParamToRender
	for _, pm := range params {
		rawVal, exists := valuesMap[pm.Name]
		typDisplay := normalizeType(pm.TypeOriginal)

		// if the type is not primitive, always show placeholder
		useVal := exists && (isPrimitive(pm.TypeOriginal) || strings.HasPrefix(pm.TypeOriginal, "*"))
		val := valueString(rawVal, useVal, pm.TypeOriginal)

		out = append(out, ParamToRender{
			Path:        pm.Name,
			Description: pm.Description,
			Type:        typDisplay,
			Value:       val,
		})
		out = append(out, traverseParam(pm, rawVal, exists)...)
	}
	return out
}

func traverseParam(pm ParamMeta, rawVal interface{}, exists bool) []ParamToRender {
	var rows []ParamToRender
	typ := pm.TypeOriginal
	if strings.HasPrefix(typ, "[]") {
		etype := pm.TypeName
		items := []interface{}{}
		if exists {
			if s, ok := rawVal.([]interface{}); ok {
				items = s
			}
		}
		if len(items) > 0 {
			for i, it := range items {
				rows = append(rows, traverseByType(fmt.Sprintf("%s[%d]", pm.Name, i), it, etype)...)
			}
		} else {
			rows = append(rows, traverseByType(fmt.Sprintf("%s[i]", pm.Name), map[string]interface{}{}, pm.TypeName)...)
		}
	} else if strings.HasPrefix(typ, "map[") {
		if exists {
			if m, ok := rawVal.(map[string]interface{}); ok && len(m) > 0 {
				for k, v := range m {
					rows = append(rows, traverseByType(fmt.Sprintf("%s[%s]", pm.Name, k), v, pm.TypeName)...)
				}
			} else {
				// if the map is empty, add a placeholder
				rows = append(rows, traverseByType(fmt.Sprintf("%s[name]", pm.Name), map[string]interface{}{}, pm.TypeName)...)
			}
		} else {
			rows = append(rows, traverseByType(fmt.Sprintf("%s[name]", pm.Name), map[string]interface{}{}, pm.TypeName)...)
		}
	} else {
		rows = append(rows, traverseByType(pm.Name, rawVal, pm.TypeName)...)
	}
	return rows
}

func traverseByType(path string, raw interface{}, typeName string) []ParamToRender {
	var rows []ParamToRender
	m := map[string]interface{}{}
	if mm, ok := raw.(map[string]interface{}); ok {
		m = mm
	}
	for _, fm := range typeFields[typeName] {
		if fm.Name == "" {
			continue
		}
		val, ok := m[fm.Name]
		rows = append(rows, ParamToRender{
			Path:        path + "." + fm.Name,
			Description: fm.Description,
			Type:        normalizeType(fm.Type),
			Value:       valueString(val, ok, fm.Type),
		})
		childType := deriveTypeName(fm.Type)
		if _, has := typeFields[childType]; has {
			childRaw := map[string]interface{}{}
			if ok {
				if mm2, ok2 := val.(map[string]interface{}); ok2 {
					childRaw = mm2
				}
			}
			rows = append(rows, traverseByType(path+"."+fm.Name, childRaw, childType)...)
		}
	}
	return rows
}

func deriveTypeName(t string) string {
	if strings.HasPrefix(t, "[]") {
		return t[2:]
	}
	if strings.HasPrefix(t, "*") {
		return t[1:]
	}
	if strings.HasPrefix(t, "map[") {
		if idx := strings.LastIndex(t, "]"); idx != -1 && idx+1 < len(t) {
			return t[idx+1:]
		}
	}
	return t
}

func normalizeType(t string) string {
	if idx := strings.IndexAny(t, " \t"); idx != -1 {
		t = t[:idx]
	}

	if strings.HasSuffix(t, "quantity") {
		if strings.HasPrefix(t, "*") {
			return "*string"
		}
		return "string"
	}

	if strings.HasPrefix(t, "*") {
		base := strings.TrimPrefix(t, "*")
		if !isPrimitive(base) && !strings.HasPrefix(base, "[]") && !strings.HasPrefix(base, "map[") {
			return "*object"
		}
		return t
	}

	if !isPrimitive(t) && !strings.HasPrefix(t, "[]") && !strings.HasPrefix(t, "map[") {
		return "object"
	}

	return t
}

func valueString(raw interface{}, exists bool, t string) string {
	if !exists {
		switch {
		case strings.HasPrefix(t, "[]"):
			return "`[]`"
		case strings.HasPrefix(t, "map["):
			return "`{}`"
		case strings.HasPrefix(t, "*"):
			return "`null`"
		case t == "string" || t == "quantity":
			return "`\"\"`"
		case t == "int":
			return "`0`"
		case t == "bool":
			return "`false`"
		default:
			return "`{}`"
		}
	}
	switch v := raw.(type) {
	case string:
		if v == "" {
			return "`\"\"`"
		}
		return fmt.Sprintf("`%s`", v)
	case bool:
		return fmt.Sprintf("`%t`", v)
	case int, int64:
		return fmt.Sprintf("`%d`", v)
	case float64:
		if v == float64(int(v)) {
			return fmt.Sprintf("`%d`", int(v))
		}
		return fmt.Sprintf("`%f`", v)
	case []interface{}, map[string]interface{}:
		if b, err := json.Marshal(v); err == nil {
			return fmt.Sprintf("`%s`", string(b))
		}
		return "`{}`"
	default:
		return "`{}`"
	}
}

func isPrimitive(t string) bool {
	base := strings.TrimPrefix(t, "*")
	switch base {
	case "string", "int", "bool", "float64":
		return true
	}
	return false
}

func markdownTable(rows []ParamToRender) string {
	data := [][]string{{"Name", "Description", "Type", "Value"}}
	for _, r := range rows {
		data = append(data, []string{
			fmt.Sprintf("`%s`", r.Path),
			r.Description,
			fmt.Sprintf("`%s`", r.Type),
			r.Value,
		})
	}
	widths := make([]int, len(data[0]))
	for _, row := range data {
		for i, cell := range row {
			if len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}
	var sb strings.Builder
	for i, row := range data {
		sb.WriteString("|")
		for j, cell := range row {
			sb.WriteString(" " + cell + strings.Repeat(" ", widths[j]-len(cell)) + " |")
		}
		sb.WriteString("\n")
		if i == 0 {
			sb.WriteString("|")
			for _, w := range widths {
				sb.WriteString(" " + strings.Repeat("-", w) + " |")
			}
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

func renderSection(sec *Section) string {
	rows := buildParamsToRender(sec.Parameters)
	return fmt.Sprintf("### %s\n\n%s\n", sec.Name, markdownTable(rows))
}

func UpdateParametersSection(valuesPath, readmePath string) error {
	vals, err := createValuesObject(valuesPath)
	if err != nil {
		return fmt.Errorf("read values: %w", err)
	}
	meta, err := parseMetadataComments(valuesPath)
	if err != nil {
		return fmt.Errorf("parse comments: %w", err)
	}
	combine(vals)

	var sb strings.Builder
	for _, s := range meta.Sections {
		sb.WriteString(renderSection(s))
		sb.WriteString("\n")
	}
	newContent := sb.String()

	contentBytes, err := os.ReadFile(readmePath)
	if err != nil {
		return fmt.Errorf("read README: %w", err)
	}
	lines := strings.Split(string(contentBytes), "\n")
	start, end := -1, len(lines)
	re := regexp.MustCompile(`^(##+) Parameters`)
	level := ""
	for i, l := range lines {
		if m := re.FindStringSubmatch(l); m != nil {
			start = i + 1
			level = m[1]
			break
		}
	}
	if start == -1 {
		return fmt.Errorf("Parameters section not found")
	}
	sameLevel := regexp.MustCompile("^" + level + "[^#]")
	for i := start; i < len(lines); i++ {
		if sameLevel.MatchString(lines[i]) {
			end = i
			break
		}
	}
	newLines := append([]string{}, lines[:start]...)
	newLines = append(newLines, strings.Split(newContent, "\n")...)
	newLines = append(newLines, lines[end:]...)
	return os.WriteFile(readmePath, []byte(strings.Join(newLines, "\n")), 0644)
}
