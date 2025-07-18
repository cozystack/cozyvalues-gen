// internal/readme/readme.go
//
// Public entry:
//
//	readme.UpdateParametersSection(valuesPath, readmePath string) error
//
// Generates / updates the “Parameters” table in README.md.
package readme

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"regexp"
	"sort"
	"strings"

	yaml "gopkg.in/yaml.v3"
)

/* -------------------------------------------------------------------------- */
/*  Defaults & helpers                                                        */
/* -------------------------------------------------------------------------- */

type Config struct {
	Comments struct{ Format string } `json:"comments"`
	Tags     struct {
		Param, Section, DescriptionStart, DescriptionEnd, Skip, Extra string
	} `json:"tags"`
	Regexp    struct{ ParamsSectionTitle string } `json:"regexp"`
	Modifiers struct {
		Array, Object, String, Nullable, Default string
	} `json:"modifiers"`
}

func defaultConfig() *Config {
	cfg := &Config{}
	cfg.Comments.Format = "##"
	cfg.Tags.Param = "@param"
	cfg.Tags.Section = "@section"
	cfg.Tags.DescriptionStart = "@descriptionStart"
	cfg.Tags.DescriptionEnd = "@descriptionEnd"
	cfg.Tags.Skip = "@skip"
	cfg.Tags.Extra = "@extra"
	cfg.Modifiers.Array = "array"
	cfg.Modifiers.Object = "object"
	cfg.Modifiers.String = "string"
	cfg.Modifiers.Nullable = "nullable"
	cfg.Modifiers.Default = "default"
	cfg.Regexp.ParamsSectionTitle = "Parameters"
	return cfg
}

func isPrimitive(t string) bool {
	switch t {
	case "string", "bool", "boolean", "int", "int32", "int64",
		"float32", "float64", "number", "integer", "nil":
		return true
	default:
		return false
	}
}

/* -------------------------------------------------------------------------- */
/*  Parameter model                                                           */
/* -------------------------------------------------------------------------- */

type Parameter struct {
	Name, Description string
	Type, DisplayType string
	Value             interface{}
	Modifiers         []string
	Section           string
	Validate, Readme  bool
	Schema            bool
}

func NewParameter(name string) *Parameter {
	return &Parameter{Name: name, Validate: true, Readme: true, Schema: true}
}

func (p *Parameter) HasModifier(m string) bool {
	for _, x := range p.Modifiers {
		if x == m {
			return true
		}
	}
	return false
}
func (p *Parameter) Extra() bool { return !p.Validate && p.Readme }
func (p *Parameter) Skip() bool  { return !p.Validate && !p.Readme }

/* -------------------------------------------------------------------------- */
/*  YAML flatten & type inference                                             */
/* -------------------------------------------------------------------------- */

func flattenYAML(prefix string, in interface{}, out map[string]interface{}) {
	switch v := in.(type) {
	case map[string]interface{}:
		if len(v) == 0 {
			if prefix != "" {
				out[prefix] = v
			}
			return
		}
		for k, val := range v {
			key := k
			if prefix != "" {
				key = prefix + "." + k
			}
			flattenYAML(key, val, out)
		}
	case []interface{}:
		if len(v) == 0 {
			if prefix != "" {
				out[prefix] = v
			}
			return
		}
		for i, val := range v {
			key := fmt.Sprintf("%s[%d]", prefix, i)
			flattenYAML(key, val, out)
		}
	default:
		out[prefix] = v
	}
}

func inferType(v interface{}) string {
	switch v.(type) {
	case nil:
		return "nil"
	case string:
		return "string"
	case bool:
		return "boolean"
	case int, int64:
		return "integer"
	case float64:
		return "number"
	case []interface{}:
		return "array"
	case map[string]interface{}:
		return "object"
	default:
		return "unknown"
	}
}

func createValuesObject(path string) ([]*Parameter, error) {
	raw, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var node interface{}
	if err := yaml.Unmarshal(raw, &node); err != nil {
		return nil, err
	}

	m := map[string]interface{}{}
	flattenYAML("", node, m)

	out := make([]*Parameter, 0, len(m))
	for k, v := range m {
		p := NewParameter(k)
		p.Value = v
		p.Type = inferType(v)
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

/* -------------------------------------------------------------------------- */
/*  Metadata parsing                                                          */
/* -------------------------------------------------------------------------- */

// parseAnnotation returns (baseType, enumList, rawTypeExpression)
func parseAnnotation(a string) (string, []string, string) {
	tok := strings.Fields(strings.TrimSpace(a))
	if len(tok) == 0 {
		return "string", nil, "string"
	}
	raw := tok[0]
	base := raw

	switch {
	case strings.HasPrefix(raw, "[]"):
		base = "array"
	case strings.HasPrefix(raw, "map["):
		base = "object"
	case raw == "int":
		base = "integer"
	}

	var enums []string
	for _, t := range tok[1:] {
		if strings.HasPrefix(t, "enum:") {
			list := strings.Trim(t[len("enum:"):], `"`)
			for _, e := range strings.Split(list, ",") {
				enums = append(enums, strings.TrimSpace(e))
			}
		}
	}

	if !isPrimitive(base) && base != "array" && base != "object" {
		base = "object"
	}
	return base, enums, raw
}

/* ---------- UPDATED PARSER: descActive flag keeps only initial comments --- */

func parseMetadataComments(path string) (*Metadata, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	meta := &Metadata{}
	reader := bufio.NewReader(f)
	var current *Section
	descActive := false // true right after @section until first @param/@field

	reSection := regexp.MustCompile(`^\s*##\s*@section\s+(.+)$`)
	reParam := regexp.MustCompile(`^\s*##\s*@param\s+([^\s]+)\s+\{([^\}]+)\}\s*(.*)$`)
	reField := regexp.MustCompile(`^\s*##\s*@field\s+([^\s]+)\s+\{([^\}]+)\}\s*(.*)$`)
	reFree := regexp.MustCompile(`^\s*##\s?(.*)$`)

	for {
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return nil, err
		}
		trim := strings.TrimRight(line, "\r\n")

		switch {
		case reSection.MatchString(trim):
			title := reSection.FindStringSubmatch(trim)[1]
			current = &Section{Name: title}
			meta.AddSection(current)
			descActive = true // start capturing description

		case reParam.MatchString(trim):
			descActive = false
			sm := reParam.FindStringSubmatch(trim)
			name, ann, desc := sm[1], sm[2], sm[3]
			base, enum, raw := parseAnnotation(ann)

			p := NewParameter(name)
			p.Type = base
			p.DisplayType = raw
			p.Description = desc
			if len(enum) > 0 {
				p.Modifiers = append(p.Modifiers, "enum:"+strings.Join(enum, ","))
			}
			if current != nil {
				p.Section = current.Name
				current.Parameters = append(current.Parameters, p)
			}
			meta.AddParameter(p)

		case reField.MatchString(trim):
			descActive = false
			sm := reField.FindStringSubmatch(trim)
			name, ann, desc := sm[1], sm[2], sm[3]
			base, enum, raw := parseAnnotation(ann)

			p := NewParameter(name)
			p.Type = base
			p.DisplayType = raw
			p.Description = desc
			p.Validate = false
			if len(enum) > 0 {
				p.Modifiers = append(p.Modifiers, "enum:"+strings.Join(enum, ","))
			}
			if current != nil {
				p.Section = current.Name
				current.Parameters = append(current.Parameters, p)
			}
			meta.AddParameter(p)

		case current != nil && descActive && reFree.MatchString(trim):
			txt := reFree.FindStringSubmatch(trim)[1]
			if strings.TrimSpace(txt) != "" {
				current.DescriptionLines = append(current.DescriptionLines, txt)
			}
		}

		if err == io.EOF {
			break
		}
	}
	return meta, nil
}

/* ========================================================================== */
/*  Alias map (singular → placeholder)                                        */
/* ========================================================================== */

type aliasMap map[string]string

func buildAliasMap(params []*Parameter) aliasMap {
	am := aliasMap{}
	for _, p := range params {
		raw := strings.TrimPrefix(p.DisplayType, "*")

		switch {
		case strings.HasPrefix(raw, "map[string]"):
			t := strings.TrimSpace(strings.TrimPrefix(raw, "map[string]"))
			t = strings.TrimPrefix(t, "*")
			if t != "" {
				am[t] = p.Name + "[name]"
			}

		case strings.HasPrefix(raw, "[]"):
			t := strings.TrimPrefix(raw, "[]")
			t = strings.TrimPrefix(t, "*")
			if t != "" {
				am[t] = p.Name + "[]"
			}

		default:
			if !isPrimitive(raw) {
				am[raw] = p.Name
			}
		}
	}

	for typ, ph := range am {
		if strings.Contains(ph, "/") || strings.Contains(ph, ".") {
			am[typ] = displayPath(ph, am)
		}
	}
	return am
}

/* ========================================================================== */
/*  Path renderer                                                             */
/* ========================================================================== */

var reArrayIdx = regexp.MustCompile(`$begin:math:display$\\d+$end:math:display$`)

func displayPath(raw string, aliases aliasMap) string {
	segs := strings.Split(strings.ReplaceAll(raw, "/", "."), ".")
	if len(segs) == 0 {
		return raw
	}
	first := segs[0]
	out := []string{}
	if ph, ok := aliases[first]; ok {
		out = append(out, ph)
	} else {
		out = append(out, first)
	}
	out = append(out, segs[1:]...)
	return reArrayIdx.ReplaceAllString(strings.Join(out, "."), "[]")
}

/* ========================================================================== */
/*  README data structures                                                    */
/* ========================================================================== */

type Section struct {
	Name             string
	DescriptionLines []string
	Parameters       []*Parameter
}

// only first non-empty description line
func (s *Section) Description() string {
	for _, l := range s.DescriptionLines {
		if strings.TrimSpace(l) != "" {
			return l
		}
	}
	return ""
}

type Metadata struct {
	Sections   []*Section
	Parameters []*Parameter
}

func (m *Metadata) AddSection(sec *Section)   { m.Sections = append(m.Sections, sec) }
func (m *Metadata) AddParameter(p *Parameter) { m.Parameters = append(m.Parameters, p) }

/* ========================================================================== */
/*  Key-consistency validator                                                 */
/* ========================================================================== */

func checkKeys(realFlat map[string]interface{}, meta []*Parameter) error {
	realSet := map[string]struct{}{}
	for k := range realFlat {
		realSet[k] = struct{}{}
	}
	skip := map[string]struct{}{}
	for _, p := range meta {
		if p.Type == "object" || p.Type == "array" {
			skip[p.Name] = struct{}{}
			continue
		}
		for rk := range realSet {
			if rk != p.Name && (strings.HasPrefix(rk, p.Name+".") || strings.HasPrefix(rk, p.Name+"[")) {
				skip[p.Name] = struct{}{}
				break
			}
		}
	}
	isSkipped := func(path string) bool {
		for pref := range skip {
			if path == pref || strings.HasPrefix(path, pref+".") || strings.HasPrefix(path, pref+"[") {
				return true
			}
		}
		return false
	}
	metaSet := map[string]struct{}{}
	for _, p := range meta {
		if p.Validate {
			metaSet[p.Name] = struct{}{}
		}
	}

	var missing, orphan []string
	for rk := range realSet {
		if !isSkipped(rk) {
			if _, ok := metaSet[rk]; !ok {
				missing = append(missing, rk)
			}
		}
	}
	for mk := range metaSet {
		if _, ok := realSet[mk]; !ok && !isSkipped(mk) {
			orphan = append(orphan, mk)
		}
	}
	sort.Strings(missing)
	sort.Strings(orphan)
	if len(missing)+len(orphan) == 0 {
		return nil
	}

	var sb strings.Builder
	for _, k := range missing {
		sb.WriteString("missing metadata: " + k + "\n")
	}
	for _, k := range orphan {
		sb.WriteString("orphan metadata: " + k + "\n")
	}
	return errors.New(sb.String())
}

/* ========================================================================== */
/*  Markdown table renderer                                                   */
/* ========================================================================== */

func tidyType(raw, base string) string {
	t := strings.TrimPrefix(raw, "*")
	switch {
	case raw == "":
		return base
	case strings.HasPrefix(raw, "[]"):
		return raw
	case strings.HasPrefix(raw, "map["):
		return raw
	case strings.HasPrefix(raw, "*") && isPrimitive(t):
		return raw
	case isPrimitive(raw):
		return raw
	default:
		return base // collapse structs to object
	}
}

func markdownTable(params []*Parameter, aliases aliasMap) string {
	rows := [][]string{{"Name", "Description", "Type", "Value"}}
	for _, p := range params {
		name := fmt.Sprintf("`%s`", displayPath(p.Name, aliases))
		typ := fmt.Sprintf("`%s`", tidyType(p.DisplayType, p.Type))
		val := ""
		if !p.Extra() {
			switch vv := p.Value.(type) {
			case string:
				val = fmt.Sprintf("`%s`", vv)
			default:
				b, _ := json.Marshal(vv)
				val = fmt.Sprintf("`%s`", string(b))
			}
		}
		rows = append(rows, []string{name, p.Description, typ, val})
	}
	w := make([]int, len(rows[0]))
	for _, r := range rows {
		for i, c := range r {
			if l := len(c); l > w[i] {
				w[i] = l
			}
		}
	}
	var b strings.Builder
	for i, r := range rows {
		b.WriteString("|")
		for j, c := range r {
			b.WriteString(" " + c + strings.Repeat(" ", w[j]-len(c)) + " |")
		}
		b.WriteString("\n")
		if i == 0 {
			b.WriteString("|")
			for _, ww := range w {
				b.WriteString(" " + strings.Repeat("-", ww) + " |")
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

/* ========================================================================== */
/*  README render helpers                                                     */
/* ========================================================================== */

func renderSection(sec *Section, h string, aliases aliasMap) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s %s\n\n", h, sec.Name))
	if d := sec.Description(); d != "" {
		b.WriteString(d + "\n\n")
	}
	if len(sec.Parameters) > 0 {
		b.WriteString(markdownTable(sec.Parameters, aliases))
	}
	return b.String()
}

func renderReadmeTable(secs []*Section, h string, aliases aliasMap) string {
	var b strings.Builder
	for _, s := range secs {
		b.WriteString("\n" + renderSection(s, h, aliases))
	}
	return b.String()
}

func insertReadmeTable(readmePath string, sections []*Section, cfg *Config, aliases aliasMap) error {
	raw, err := ioutil.ReadFile(readmePath)
	if err != nil {
		return err
	}
	lines := strings.Split(string(raw), "\n")
	start := -1
	hPrefix := "##"
	reStart := regexp.MustCompile(fmt.Sprintf(`^(##+) %s`, cfg.Regexp.ParamsSectionTitle))
	for i, l := range lines {
		if m := reStart.FindStringSubmatch(l); m != nil {
			start = i + 1
			hPrefix = m[1] + "#"
			break
		}
	}
	if start == -1 {
		return errors.New("Parameters section not found")
	}
	end := len(lines)
	sameLevel := regexp.MustCompile(fmt.Sprintf(`^%s\s`, strings.Repeat("#", len(hPrefix)-1)))
	for i := start; i < len(lines); i++ {
		if sameLevel.MatchString(lines[i]) {
			end = i
			break
		}
	}

	newTable := renderReadmeTable(sections, hPrefix, aliases)
	newLines := append([]string{}, lines[:start]...)
	newLines = append(newLines, strings.Split(newTable, "\n")...)
	newLines = append(newLines, lines[end:]...)
	return ioutil.WriteFile(readmePath, []byte(strings.Join(newLines, "\n")), 0644)
}

/* ========================================================================== */
/*  Merge & modifiers                                                         */
/* ========================================================================== */

func combine(values, meta []*Parameter) {
	for _, p := range meta {
		if p.Extra() {
			continue
		}
		for _, v := range values {
			if v.Name == p.Name && p.Value == nil {
				p.Value = v.Value
				break
			}
		}
	}
}

func applyModifiers(p *Parameter, cfg *Config) {
	if len(p.Modifiers) == 0 {
		return
	}
	nullableLast := p.HasModifier(cfg.Modifiers.Nullable) && p.Modifiers[len(p.Modifiers)-1] == cfg.Modifiers.Nullable
	for _, m := range p.Modifiers {
		switch m {
		case cfg.Modifiers.Array:
			p.Type = "array"
			if !nullableLast {
				p.Value = []interface{}{}
			}
		case cfg.Modifiers.Object:
			p.Type = "object"
			if !nullableLast {
				p.Value = map[string]interface{}{}
			}
		case cfg.Modifiers.String:
			p.Type = "string"
			if !nullableLast {
				p.Value = ""
			}
		case cfg.Modifiers.Nullable:
			if p.Value == nil {
				p.Value = "nil"
			}
		default:
			if strings.HasPrefix(m, cfg.Modifiers.Default+":") {
				p.Value = strings.TrimSpace(strings.TrimPrefix(m, cfg.Modifiers.Default+":"))
			}
		}
	}
}
func buildParamsToRender(ps []*Parameter, cfg *Config) []*Parameter {
	out := []*Parameter{}
	for _, p := range ps {
		if p.Skip() {
			continue
		}
		applyModifiers(p, cfg)
		out = append(out, p)
	}
	return out
}

/* ========================================================================== */
/*  Public entry point                                                        */
/* ========================================================================== */

// UpdateParametersSection parses values.yaml and rewrites the Parameters
// section in README.md with a fresh, clean table.
func UpdateParametersSection(valuesPath, readmePath string) error {
	cfg := defaultConfig()

	vals, err := createValuesObject(valuesPath)
	if err != nil {
		return err
	}

	flat := map[string]interface{}{}
	for _, p := range vals {
		flat[p.Name] = p.Value
	}

	meta, err := parseMetadataComments(valuesPath)
	if err != nil {
		return err
	}
	if err := checkKeys(flat, meta.Parameters); err != nil {
		return err
	}

	aliases := buildAliasMap(meta.Parameters)
	combine(vals, meta.Parameters)
	for _, s := range meta.Sections {
		s.Parameters = buildParamsToRender(s.Parameters, cfg)
	}

	return insertReadmeTable(readmePath, meta.Sections, cfg, aliases)
}
