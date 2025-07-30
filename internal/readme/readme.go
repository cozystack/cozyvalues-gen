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
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	// YAML node tree
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, err
	}

	typeFields = make(map[string][]FieldMeta)
	var sections []*Section
	var current *Section

	sectionRe := regexp.MustCompile(`^#{1,}\s+@section\s+(.*)$`)
	paramRe := regexp.MustCompile(`^#{1,}\s+@param\s+(\w+)\s+\{([^}]+)\}\s+(.*)$`)
	fieldRe := regexp.MustCompile(`^#{1,}\s+@field\s+([\w\.]+)\s+\{([^}]+)\}\s*(.*)$`)

	// First pass: extract all @section, @param, @field from comments
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		if m := sectionRe.FindStringSubmatch(line); m != nil {
			sec := &Section{Name: m[1]}
			sections = append(sections, sec)
			current = sec
			continue
		}
		if m := paramRe.FindStringSubmatch(line); m != nil {
			name, typ, desc := m[1], m[2], m[3]
			typeName := resolveTypeName(typ)
			pm := ParamMeta{Name: name, TypeOriginal: typ, TypeName: typeName, Description: desc}
			if current != nil {
				current.Parameters = append(current.Parameters, pm)
			} else {
				if len(sections) == 0 {
					sections = append(sections, &Section{Name: "Parameters"})
				}
				sections[0].Parameters = append(sections[0].Parameters, pm)
			}
		}
	}

	// Second pass: parse tree and collect inline @field annotations
	var walk func(node *yaml.Node, path []string)
	walk = func(node *yaml.Node, path []string) {
		if node.Kind == yaml.MappingNode {
			for i := 0; i < len(node.Content); i += 2 {
				key := node.Content[i]
				val := node.Content[i+1]
				name := key.Value

				// Look for comment with @field on this key node
				comments := key.HeadComment + "\n" + key.LineComment + "\n" + key.FootComment
				for _, line := range strings.Split(comments, "\n") {
					if m := fieldRe.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
						qual := m[1]
						typ := m[2]
						desc := m[3]

						// allow both relative and full form
						var rootType, field string
						if strings.Contains(qual, ".") {
							parts := strings.SplitN(qual, ".", 2)
							rootType, field = parts[0], parts[1]
						} else {
							rootType = qual
							field = ""
						}
						typeFields[rootType] = append(typeFields[rootType], FieldMeta{
							ParentTypeName: rootType,
							Name:           field,
							Type:           typ,
							Description:    desc,
						})
					}
				}

				// Recurse into next value
				if val.Kind == yaml.MappingNode {
					walk(val, append(path, name))
				}
			}
		}
	}

	if len(root.Content) > 0 {
		walk(root.Content[0], nil)
	}

	return &Meta{Sections: sections}, nil
}

func resolveTypeName(typ string) string {
	if strings.HasPrefix(typ, "[]") {
		return typ[2:]
	}
	if strings.HasPrefix(typ, "map[") {
		if idx := strings.LastIndex(typ, "]"); idx != -1 && idx+1 < len(typ) {
			return typ[idx+1:]
		}
	}
	if strings.HasPrefix(typ, "*") {
		return typ[1:]
	}
	return typ
}

func buildAliasMap(params []ParamMeta) map[string]string { return map[string]string{} }

func combine(vals map[string]interface{}) { valuesMap = vals }

func buildParamsToRender(params []ParamMeta) []ParamToRender {
	var out []ParamToRender
	for _, pm := range params {
		typDisplay := normalizeType(pm.TypeOriginal)

		val := defaultValueForType(pm.TypeOriginal)

		if isPrimitive(pm.TypeOriginal) || strings.HasPrefix(pm.TypeOriginal, "*") || strings.Contains(pm.TypeOriginal, "quantity") || (strings.HasPrefix(pm.TypeOriginal, "[]") && isPrimitive(pm.TypeName)) {
			rawVal, exists := valuesMap[pm.Name]
			val = valueString(rawVal, exists, pm.TypeOriginal)
		}

		out = append(out, ParamToRender{
			Path:        pm.Name,
			Description: pm.Description,
			Type:        typDisplay,
			Value:       val,
		})
		out = append(out, traverseParam(pm, valuesMap[pm.Name], true)...)
	}
	return out
}

func traverseParam(pm ParamMeta, rawVal interface{}, exists bool) []ParamToRender {
	var rows []ParamToRender
	typ := pm.TypeOriginal
	if strings.HasPrefix(typ, "[]") {
		etype := pm.TypeName
		if !isPrimitive(etype) {
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
		}
	} else if strings.HasPrefix(typ, "map[") {
		if exists {
			if m, ok := rawVal.(map[string]interface{}); ok && len(m) > 0 {
				for k, v := range m {
					rows = append(rows, traverseByType(fmt.Sprintf("%s[%s]", pm.Name, k), v, pm.TypeName)...)
				}
			} else {
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

		baseType := deriveTypeName(fm.Type)
		isArrayOfPrimitives := strings.HasPrefix(fm.Type, "[]") && isPrimitive(baseType)
		isDirectPrimitive := isPrimitive(baseType) || strings.Contains(baseType, "quantity")

		value := defaultValueForType(fm.Type)
		if isDirectPrimitive || isArrayOfPrimitives {
			value = valueString(val, ok, fm.Type)
		}

		rows = append(rows, ParamToRender{
			Path:        path + "." + fm.Name,
			Description: fm.Description,
			Type:        normalizeType(fm.Type),
			Value:       value,
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
	if !exists || raw == nil {
		return defaultValueForType(t)
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
	case []interface{}:
		if len(v) == 0 {
			return "`[]`"
		}
		parts := []string{}
		for _, item := range v {
			switch vv := item.(type) {
			case string:
				parts = append(parts, vv)
			default:
				parts = append(parts, fmt.Sprintf("%v", vv))
			}
		}
		return fmt.Sprintf("`[%s]`", strings.Join(parts, ", "))
	case map[string]interface{}:
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

func validateValues(params []ParamMeta, typeFields map[string][]FieldMeta, values map[string]interface{}) error {
	paramMap := make(map[string]ParamMeta)
	for _, p := range params {
		paramMap[p.Name] = p
	}

	var check func(path string, val interface{}, typeName string) error
	check = func(path string, val interface{}, typeName string) error {
		if _, has := typeFields[typeName]; !has && !isPrimitive(typeName) && !strings.Contains(typeName, "quantity") &&
			!strings.HasPrefix(typeName, "[]") && !strings.HasPrefix(typeName, "map[") {
			return fmt.Errorf("type '%s' for field '%s' is not defined in schema", typeName, path)
		}

		fields, has := typeFields[typeName]
		if !has {
			return nil
		}
		valMap, ok := val.(map[string]interface{})
		if !ok {
			return nil
		}
		allowed := make(map[string]FieldMeta)
		for _, f := range fields {
			allowed[f.Name] = f
		}
		for k, v := range valMap {
			fm, exists := allowed[k]
			if !exists {
				return fmt.Errorf("field '%s.%s' is not defined in schema", path, k)
			}
			childType := deriveTypeName(fm.Type)
			if err := check(path+"."+k, v, childType); err != nil {
				return err
			}
		}
		return nil
	}

	for k, v := range values {
		pm, exists := paramMap[k]
		if !exists {
			return fmt.Errorf("parameter '%s' is not defined in schema", k)
		}
		childType := deriveTypeName(pm.TypeOriginal)
		if err := check(k, v, childType); err != nil {
			return err
		}
	}

	return nil
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

	params := []ParamMeta{}
	for _, s := range meta.Sections {
		params = append(params, s.Parameters...)
	}
	if err := validateValues(params, typeFields, vals); err != nil {
		return fmt.Errorf("validate values: %w", err)
	}

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

func defaultValueForType(t string) string {
	if strings.HasPrefix(t, "*") {
		return "`null`"
	}

	base := strings.TrimPrefix(t, "*")

	switch {
	case strings.HasPrefix(base, "[]"):
		return "`[]`"
	case strings.HasPrefix(base, "map["):
		return "`{}`"
	case base == "string" || base == "quantity":
		return "`\"\"`"
	case base == "int":
		return "`0`"
	case base == "bool":
		return "`false`"
	default:
		return "`{}`"
	}
}
