package readme

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	aliasQuantity    = "quantity"
	aliasDuration    = "duration"
	aliasTime        = "time"
	aliasObject      = "object"
	aliasEmptyObject = "emptyobject"
)

// additional string-format aliases
// see https://github.com/go-openapi/strfmt/blob/master/README.md
var stringFormats = []string{
	"bsonobjectid",
	"uri",
	"email",
	"hostname",
	"ipv4",
	"ipv6",
	"cidr",
	"mac",
	"uuid",
	"uuid3",
	"uuid4",
	"uuid5",
	"isbn",
	"isbn10",
	"isbn13",
	"creditcard",
	"ssn",
	"hexcolor",
	"rgbcolor",
	"byte",
	"password",
	"date",
}

func isStringFormat(s string) bool {
	for _, f := range stringFormats {
		if s == f {
			return true
		}
	}
	return false
}

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
	var allParams []ParamMeta

	sectionRe := regexp.MustCompile(`^#{1,}\s+@section\s+(.*)$`)
	// description part is optional
	paramRe := regexp.MustCompile(`^#{1,}\s+@param\s+(\w+)\s+\{([^}]+)\}(?:\s+(.*))?$`)
	fieldRe := regexp.MustCompile(`^#{1,}\s+@field\s+([\w\.]+)\s+\{(.+)\}\s*(.*)$`)

	// ───────────── first pass: @section & @param ─────────────
	lines := strings.Split(string(data), "\n")
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if m := sectionRe.FindStringSubmatch(line); m != nil {
			sec := &Section{Name: m[1]}
			sections = append(sections, sec)
			current = sec
			continue
		}
		if m := paramRe.FindStringSubmatch(line); m != nil {
			name, typ := m[1], m[2]
			desc := ""
			if len(m) > 3 {
				desc = m[3]
			}
			pm := ParamMeta{
				Name:         name,
				TypeOriginal: typ,
				TypeName:     resolveTypeName(typ),
				Description:  desc,
			}
			allParams = append(allParams, pm)
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

	// build param-name → type alias map
	aliasMap := buildAliasMap(allParams)

	seen := map[fieldKey]struct{}{}
	addField := func(parent, name, typ, desc string) {
		if parent == "" || name == "" {
			return
		}
		if real, ok := aliasMap[parent]; ok {
			parent = real
		}
		k := fieldKey{parent: parent, name: name}
		if _, dup := seen[k]; dup {
			return
		}
		typeFields[parent] = append(typeFields[parent], FieldMeta{
			ParentTypeName: parent,
			Name:           name,
			Type:           typ,
			Description:    desc,
		})
		seen[k] = struct{}{}
	}

	// collect from raw lines
	for _, line := range lines {
		if m := fieldRe.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			qual := m[1]
			typ := m[2]
			desc := m[3]

			rootType, field := qual, ""
			if strings.Contains(qual, ".") {
				parts := strings.SplitN(qual, ".", 2)
				rootType, field = parts[0], parts[1]
			}
			addField(rootType, field, typ, desc)
		}
	}

	// ───────────── second pass: traverse YAML & @field ─────────────
	var walk func(node *yaml.Node, path []string)
	walk = func(node *yaml.Node, path []string) {
		if node.Kind != yaml.MappingNode {
			return
		}
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			val := node.Content[i+1]
			name := key.Value

			comments := key.HeadComment + "\n" + key.LineComment + "\n" + key.FootComment
			for _, line := range strings.Split(comments, "\n") {
				if m := fieldRe.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
					qual := m[1]
					typ := m[2]
					desc := m[3]

					rootType, field := qual, ""
					if strings.Contains(qual, ".") {
						parts := strings.SplitN(qual, ".", 2)
						rootType, field = parts[0], parts[1]
					}
					// resolve via alias map (foo → FooType)
					if real, ok := aliasMap[rootType]; ok {
						rootType = real
					}

					typeFields[rootType] = append(typeFields[rootType], FieldMeta{
						ParentTypeName: rootType,
						Name:           field,
						Type:           typ,
						Description:    desc,
					})
				}
			}
			if val.Kind == yaml.MappingNode {
				walk(val, append(path, name))
			}
		}
	}

	if len(root.Content) > 0 {
		walk(root.Content[0], nil)
	}

	return &Meta{Sections: sections}, nil
}

func resolveTypeName(typ string) string {
	if idx := strings.IndexAny(typ, " \t"); idx != -1 {
		typ = typ[:idx]
	}
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

func buildAliasMap(params []ParamMeta) map[string]string {
	out := make(map[string]string)
	for _, p := range params {
		out[p.Name] = p.TypeName
	}
	return out
}

func combine(vals map[string]interface{}) { valuesMap = vals }

// place below helpers (before buildParamsToRender)

var reAnnoDefault = regexp.MustCompile(`\bdefault\s*=\s*(?:"([^"]*)"|(\{[^}]*\}|$begin:math:display$[^$end:math:display$]*\]|true|false|-?\d+(?:\.\d+)?))`)

func extractAnnotationDefault(typeExpr string) (string, bool) {
	m := reAnnoDefault.FindStringSubmatch(typeExpr)
	if m == nil {
		return "", false
	}
	if m[1] != "" {
		return m[1], true
	}
	return m[2], true
}

func renderAnnotationDefault(val string) string {
	s := strings.TrimSpace(val)
	switch s {
	case "{}":
		return "`{}`"
	case "[]":
		return "`[]`"
	}
	var parsed interface{}
	if err := yaml.Unmarshal([]byte(s), &parsed); err == nil {
		return valueString(parsed, true, "")
	}
	return fmt.Sprintf("`%s`", strings.Trim(s, `"`))
}

// ---------------------------------------------------------------------------
//
//	BuildParamsToRender – table rows for README
//
// ---------------------------------------------------------------------------
func buildParamsToRender(params []ParamMeta) []ParamToRender {
	var out []ParamToRender

	for _, pm := range params {
		orig := pm.TypeOriginal
		baseForKind := strings.TrimPrefix(orig, "*")
		baseType := deriveTypeName(orig)

		isArray := strings.HasPrefix(baseForKind, "[]")
		isMap := strings.HasPrefix(baseForKind, "map[")
		isPtr := strings.HasPrefix(orig, "*")

		isArrayPrim := isArray && isPrimitive(baseType)
		isPtrPrim := isPtr && isPrimitive(baseType)

		val := defaultValueForType(orig)

		switch {
		case isArrayPrim:
			raw, exists := valuesMap[pm.Name]
			if !exists {
				if def, ok := extractAnnotationDefault(orig); ok {
					val = renderAnnotationDefault(def)
				} else {
					val = defaultValueForType(orig)
				}
			} else {
				val = valueString(raw, exists, orig)
			}

		case isArray:
			if raw, ok := valuesMap[pm.Name].([]interface{}); ok {
				if len(raw) > 0 {
					val = "`[...]`"
				} else {
					val = "`[]`"
				}
			} else if def, ok := extractAnnotationDefault(orig); ok {
				val = renderAnnotationDefault(def)
			} else {
				val = "`[]`"
			}

		case isMap:
			if !isPrimitive(baseType) && len(typeFields[baseType]) > 0 {
				val = "`{...}`"
			} else if def, ok := extractAnnotationDefault(orig); ok {
				val = renderAnnotationDefault(def)
			} else {
				val = "`{}`"
			}

		case isPtrPrim:
			raw, exists := valuesMap[pm.Name]
			if !exists {
				if def, ok := extractAnnotationDefault(orig); ok {
					val = renderAnnotationDefault(def)
				} else {
					val = "`null`"
				}
			} else {
				val = valueString(raw, exists, orig)
			}

		case isPtr && (strings.HasPrefix(baseForKind, "[]") || strings.HasPrefix(baseForKind, "map[")):
			raw, exists := valuesMap[pm.Name]
			if !exists {
				if def, ok := extractAnnotationDefault(orig); ok {
					val = renderAnnotationDefault(def)
				} else {
					val = "`null`"
				}
			} else if strings.HasPrefix(baseForKind, "[]") {
				if arr, ok := raw.([]interface{}); ok && len(arr) > 0 {
					val = "`[...]`"
				} else {
					val = "`[]`"
				}
			} else {
				val = "`{}`"
			}

		case isPtr && !isPtrPrim:
			if def, ok := extractAnnotationDefault(orig); ok {
				val = renderAnnotationDefault(def)
			} else {
				val = "`null`"
			}

		case !isPrimitive(baseType) && len(typeFields[baseType]) > 0:
			if def, ok := extractAnnotationDefault(orig); ok {
				val = renderAnnotationDefault(def)
			} else {
				val = "`{}`"
			}

		default:
			raw, exists := valuesMap[pm.Name]
			if !exists {
				if def, ok := extractAnnotationDefault(orig); ok {
					val = renderAnnotationDefault(def)
				} else {
					val = defaultValueForType(orig)
				}
			} else {
				val = valueString(raw, exists, orig)
			}
		}

		out = append(out, ParamToRender{
			Path:        pm.Name,
			Description: pm.Description,
			Type:        normalizeType(orig),
			Value:       val,
		})
		out = append(out, traverseParam(pm, valuesMap[pm.Name], true)...)
	}
	return out
}

func traverseByType(path string, raw interface{}, typeName string) []ParamToRender {
	var rows []ParamToRender
	m := map[string]interface{}{}
	if mm, ok := raw.(map[string]interface{}); ok {
		m = mm
	}

	rowSeen := map[string]struct{}{}

	ensureSynthFromDefault := func(parentPath, ft string) []ParamToRender {
		var out []ParamToRender
		if def, ok := extractAnnotationDefault(ft); ok {
			var parsed interface{}
			if err := yaml.Unmarshal([]byte(def), &parsed); err == nil {
				if mm, ok := parsed.(map[string]interface{}); ok {
					keys := make([]string, 0, len(mm))
					for k := range mm {
						keys = append(keys, k)
					}
					sort.Strings(keys)
					for _, k := range keys {
						val := mm[k]
						typ := "object"
						switch val.(type) {
						case string:
							typ = "string"
						case bool:
							typ = "bool"
						case int, int64, float64:
							typ = "int"
						}
						out = append(out, ParamToRender{
							Path:        parentPath + "." + k,
							Description: "",
							Type:        normalizeType(typ),
							Value:       valueString(val, true, typ),
						})
					}
				}
			}
		}
		return out
	}

	for _, fm := range typeFields[typeName] {
		if fm.Name == "" {
			continue
		}
		key := path + "." + fm.Name + "\x00" + normalizeType(fm.Type)
		if _, ok := rowSeen[key]; ok {
			continue
		}

		val, okVal := m[fm.Name]

		hasPtr := strings.HasPrefix(fm.Type, "*")
		ft := strings.TrimPrefix(fm.Type, "*")
		baseType := deriveTypeName(ft)

		isArray := strings.HasPrefix(ft, "[]")
		isMap := strings.HasPrefix(ft, "map[")
		isArrayOfPrimitives := isArray && isPrimitive(baseType)
		isDirectPrimitive := isPrimitive(baseType) || strings.Contains(baseType, "quantity")

		value := defaultValueForType(fm.Type)

		switch {
		case isArray:
			if okVal {
				if isArrayOfPrimitives {
					value = valueString(val, okVal, ft)
				} else {
					if arr, ok := val.([]interface{}); ok && len(arr) > 0 {
						value = "`[...]`"
					} else {
						value = "`[]`"
					}
				}
			} else if def, ok := extractAnnotationDefault(fm.Type); ok {
				value = renderAnnotationDefault(def)
			} else if hasPtr {
				value = "`null`"
			} else {
				value = "`[]`"
			}

		case isMap:
			if okVal {
				value = "`{}`"
			} else if def, ok := extractAnnotationDefault(fm.Type); ok {
				value = renderAnnotationDefault(def)
			} else if hasPtr {
				value = "`null`"
			} else {
				value = "`{}`"
			}

		case isDirectPrimitive:
			if okVal {
				value = valueString(val, okVal, fm.Type)
			} else if def, ok := extractAnnotationDefault(fm.Type); ok {
				value = renderAnnotationDefault(def)
			}

		default:
			if hasPtr {
				if def, ok := extractAnnotationDefault(fm.Type); ok {
					value = renderAnnotationDefault(def)
				} else {
					value = "`null`"
				}
			} else if def, ok := extractAnnotationDefault(fm.Type); ok {
				value = renderAnnotationDefault(def)
			} else {
				value = "`{}`"
			}
		}

		rows = append(rows, ParamToRender{
			Path:        path + "." + fm.Name,
			Description: fm.Description,
			Type:        normalizeType(fm.Type),
			Value:       value,
		})
		rowSeen[key] = struct{}{}

		switch {
		case strings.HasPrefix(ft, "[]"):
			elt := deriveTypeName(ft)
			if _, has := typeFields[elt]; has {
				rows = append(rows, traverseByType(path+"."+fm.Name+"[i]", map[string]interface{}{}, elt)...)
			} else {
				rows = append(rows, ensureSynthFromDefault(path+"."+fm.Name+"[i]", fm.Type)...)
			}
		case strings.HasPrefix(ft, "map["):
			elt := deriveTypeName(ft)
			if _, has := typeFields[elt]; has {
				rows = append(rows, traverseByType(path+"."+fm.Name+"[name]", map[string]interface{}{}, elt)...)
			}
		default:
			child := deriveTypeName(ft)
			if _, has := typeFields[child]; has {
				childRaw := map[string]interface{}{}
				if okVal {
					if mm2, ok2 := val.(map[string]interface{}); ok2 {
						childRaw = mm2
					}
				}
				rows = append(rows, traverseByType(path+"."+fm.Name, childRaw, child)...)
			}
		}
	}
	return rows
}

func isDeepEmpty(v interface{}) bool {
	switch t := v.(type) {
	case nil:
		return true
	case map[string]interface{}:
		if len(t) == 0 {
			return true
		}
		for _, vv := range t {
			if !isDeepEmpty(vv) {
				return false
			}
		}
		return true
	case []interface{}:
		if len(t) == 0 {
			return true
		}
		for _, vv := range t {
			if !isDeepEmpty(vv) {
				return false
			}
		}
		return true
	default:
		// any primitive present means “not empty”
		return false
	}
}

func traverseParam(pm ParamMeta, rawVal interface{}, exists bool) []ParamToRender {
	var rows []ParamToRender

	torig := pm.TypeOriginal
	t := strings.TrimPrefix(torig, "*") // treat *[]T and *map[...]T like collections
	if strings.HasPrefix(t, "[]") {
		elt := deriveTypeName(t) // element type name (e.g., "gpu")
		if !isPrimitive(elt) {
			rows = append(rows, traverseByType(fmt.Sprintf("%s[i]", pm.Name), map[string]interface{}{}, elt)...)
		}
		return rows
	}
	if strings.HasPrefix(t, "map[") {
		elt := deriveTypeName(t) // value type
		rows = append(rows, traverseByType(fmt.Sprintf("%s[name]", pm.Name), map[string]interface{}{}, elt)...)
		return rows
	}

	// scalar/object param
	base := deriveTypeName(torig)
	rows = append(rows, traverseByType(pm.Name, rawVal, base)...)
	return rows
}

func deriveTypeName(t string) string {
	if idx := strings.IndexAny(t, " \t"); idx != -1 {
		t = t[:idx]
	}
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

	// normalize emptyobject for display
	if t == aliasEmptyObject {
		return "object"
	}
	if t == "*"+aliasEmptyObject {
		return "*object"
	}

	// collapse pointer-to-collection types: *[]T → []T, *map[...]V → map[...]V
	if strings.HasPrefix(t, "*") {
		base := strings.TrimPrefix(t, "*")
		if strings.HasPrefix(base, "[]") || strings.HasPrefix(base, "map[") {
			return normalizeType(base)
		}
		// pointer to non-primitive, non-collection → *object
		if !isPrimitive(base) && !strings.HasPrefix(base, "[]") && !strings.HasPrefix(base, "map[") {
			return "*object"
		}
		// pointer to primitive (or special primitives) stays as-is
		return "*" + base
	}

	// if underlying base is primitive, preserve it verbatim (after emptyobject normalization above)
	base := deriveTypeName(t)
	if isPrimitive(base) {
		// show emptyobject as object in composite forms too
		if base == aliasEmptyObject {
			return strings.ReplaceAll(t, aliasEmptyObject, "object")
		}
		return t
	}

	// Handle array types
	if strings.HasPrefix(t, "[]") {
		base := deriveTypeName(t)
		if !isPrimitive(base) {
			return "[]object"
		}
		// []emptyobject -> []object
		if base == aliasEmptyObject {
			return "[]object"
		}
		return "[]" + base
	}

	// Handle map types
	if strings.HasPrefix(t, "map[") {
		// map[string]emptyobject -> map[string]object
		if deriveTypeName(t) == aliasEmptyObject {
			return "map[string]object"
		}
		return "map[string]object"
	}

	// non-primitive scalar becomes object
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

type fieldKey struct {
	parent string
	name   string
}

func isPrimitive(t string) bool {
	base := strings.TrimPrefix(t, "*")
	if isStringFormat(base) {
		return true
	}
	switch base {
	case "string", "bool", "int", "int32", "int64",
		"float32", "float64",
		aliasQuantity, aliasDuration, aliasTime, aliasObject, aliasEmptyObject:
		return true
	default:
		return false
	}
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
	return fmt.Sprintf("\n### %s\n\n%s", sec.Name, markdownTable(rows))
}

func validateValues(params []ParamMeta, typeFields map[string][]FieldMeta, values map[string]interface{}) error {
	paramMap := make(map[string]ParamMeta, len(params))
	for _, p := range params {
		paramMap[p.Name] = p
	}

	var checkValue func(path string, val interface{}, typ string) error
	checkValue = func(path string, val interface{}, typ string) error {
		if strings.HasPrefix(typ, "map[") {
			child := deriveTypeName(typ)
			if m, ok := val.(map[string]interface{}); ok {
				keys := make([]string, 0, len(m))
				for k := range m {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					if err := checkValue(path+"."+k, m[k], child); err != nil {
						return err
					}
				}
			}
			return nil
		}

		if strings.HasPrefix(typ, "[]") {
			child := deriveTypeName(typ)
			if arr, ok := val.([]interface{}); ok {
				for i, v := range arr {
					if err := checkValue(fmt.Sprintf("%s[%d]", path, i), v, child); err != nil {
						return err
					}
				}
			}
			return nil
		}

		if strings.HasPrefix(typ, "*") {
			return checkValue(path, val, strings.TrimPrefix(typ, "*"))
		}

		base := deriveTypeName(typ)

		if isPrimitive(base) || strings.Contains(base, "quantity") {
			return nil
		}

		fields, has := typeFields[base]
		if !has {
			return fmt.Errorf("type '%s' referenced at '%s' has no schema", base, path)
		}

		valMap, ok := val.(map[string]interface{})
		if !ok {
			return nil
		}

		allowed := make(map[string]FieldMeta, len(fields))
		for _, f := range fields {
			allowed[f.Name] = f
		}

		keys := make([]string, 0, len(valMap))
		for k := range valMap {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		for _, k := range keys {
			v := valMap[k]
			fm, exists := allowed[k]
			if !exists {
				return fmt.Errorf("field '%s.%s' is not defined in schema", path, k)
			}
			if err := checkValue(path+"."+k, v, fm.Type); err != nil {
				return err
			}
		}
		return nil
	}

	topKeys := make([]string, 0, len(values))
	for k := range values {
		topKeys = append(topKeys, k)
	}
	sort.Strings(topKeys)

	for _, k := range topKeys {
		v := values[k]
		pm, exists := paramMap[k]
		if !exists {
			return fmt.Errorf("parameter '%s' is not defined in schema", k)
		}
		if err := checkValue(k, v, pm.TypeOriginal); err != nil {
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
	// every pointer (primitive *or* object) renders as null
	if strings.HasPrefix(t, "*") {
		return "`null`"
	}

	base := strings.TrimPrefix(t, "*")

	switch {
	case strings.HasPrefix(base, "[]"):
		return "`[]`"
	case strings.HasPrefix(base, "map["):
		return "`{}`"
	case base == "string", base == aliasQuantity:
		return "`\"\"`"
	case base == "int":
		return "`0`"
	case base == "bool":
		return "`false`"
	default:
		return "`{}`"
	}
}
