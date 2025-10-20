package openapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"go.etcd.io/etcd/version"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/controller-tools/pkg/crd"
	crdmarkers "sigs.k8s.io/controller-tools/pkg/crd/markers"
	"sigs.k8s.io/controller-tools/pkg/loader"
	"sigs.k8s.io/controller-tools/pkg/markers"
	sigyaml "sigs.k8s.io/yaml"
)

/* -------------------------------------------------------------------------- */
/*  Annotation parsing                                                         */
/* -------------------------------------------------------------------------- */

type kind int

const (
	kParam kind = iota
	kField
	kTypedef
	kEnum
)

const (
	aliasQuantity    = "quantity"
	aliasDuration    = "duration"
	aliasTime        = "time"
	aliasObject      = "object"
	aliasEmptyObject = "emptyobject"
	aliasResources   = "resources"
	aliasRequest     = "request"
	aliasLimit       = "limit"
)

type Raw struct {
	K           kind
	Path        []string
	TypeExpr    string
	Enums       []string
	DefaultVal  string
	Description string
	OmitEmpty   bool // Field marked with [name] for omitempty
}

// JSDoc-like syntax patterns
var (
	// Default value pattern: accepts quoted strings, JSON objects/arrays, booleans, numbers (with decimals), null, or simple tokens
	// Matches: "foo bar", 'text', {"a":1}, [1,2], true, false, null, -3.5, 42, simpleToken
	defaultValuePattern = `(?:"[^"]*"|'[^']*'|\{[^}]*\}|\[[^\]]*\]|true|false|null|-?\d+(?:\.\d+)?|\S+)`

	// ## @param {type} name - description or ## @param {type} [name]=defaultValue - description
	reParam = regexp.MustCompile(`^#{1,}\s+@param\s+\{([^}]+)\}\s+(\[?\w+\]?)(?:=(` + defaultValuePattern + `))?(?:\s+-\s+(.*))?$`)
	// ## @field {type} [name] - description  or  ## @field {type} name=defaultValue - description
	reField = regexp.MustCompile(`^#{1,}\s+@(?:field|property)\s+\{([^}]+)\}\s+(\[?\w+\]?)(?:=(` + defaultValuePattern + `))?(?:\s+-\s+(.*))?$`)
	// ## @typedef {struct|object} TypeName - description
	reTypedef = regexp.MustCompile(`^#{1,}\s+@typedef\s+\{(struct|object)\}\s+(\w+)(?:\s+-\s+(.*))?$`)
	// ## @enum {type} EnumName
	reEnum = regexp.MustCompile(`^#{1,}\s+@enum\s+\{([^}]+)\}\s+(\w+)(?:\s+-\s+(.*))?$`)
	// ## @value valueName
	reEnumValue = regexp.MustCompile(`^#{1,}\s+@value\s+(\w+)(?:\s+-\s+(.*))?$`)
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

func Parse(file string) ([]Raw, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(data), "\n")

	var out []Raw
	var currentEnum *Raw
	var enumValues []string

	for _, line := range lines {
		line = strings.TrimSpace(line)

		// Check for enum value
		if m := reEnumValue.FindStringSubmatch(line); m != nil && currentEnum != nil {
			enumValues = append(enumValues, m[1])
			continue
		}

		// Check for @param
		if m := reParam.FindStringSubmatch(line); m != nil {
			// If we were collecting enum values, finalize the enum
			if currentEnum != nil {
				currentEnum.Enums = enumValues
				out = append(out, *currentEnum)
				currentEnum = nil
				enumValues = nil
			}

			typeExpr := strings.TrimSpace(m[1])
			nameRaw := m[2]
			name := strings.Trim(nameRaw, "[]")
			omitEmpty := strings.HasPrefix(nameRaw, "[") && strings.HasSuffix(nameRaw, "]")
			defaultVal := ""
			if len(m) > 3 && m[3] != "" {
				defaultVal = m[3]
			}
			desc := ""
			if len(m) > 4 {
				desc = m[4]
			}

			r := Raw{
				K:           kParam,
				Path:        []string{name},
				TypeExpr:    typeExpr,
				DefaultVal:  defaultVal,
				Description: desc,
				OmitEmpty:   omitEmpty,
			}
			out = append(out, r)
			continue
		}

		// Check for @typedef
		if m := reTypedef.FindStringSubmatch(line); m != nil {
			// If we were collecting enum values, finalize the enum
			if currentEnum != nil {
				currentEnum.Enums = enumValues
				out = append(out, *currentEnum)
				currentEnum = nil
				enumValues = nil
			}

			typeName := m[2]
			desc := ""
			if len(m) > 3 {
				desc = m[3]
			}

			r := Raw{
				K:           kTypedef,
				Path:        []string{typeName},
				TypeExpr:    "struct",
				Description: desc,
			}
			out = append(out, r)
			continue
		}

		// Check for @enum
		if m := reEnum.FindStringSubmatch(line); m != nil {
			// If we were collecting enum values, finalize the previous enum
			if currentEnum != nil {
				currentEnum.Enums = enumValues
				out = append(out, *currentEnum)
			}

			baseType := strings.TrimSpace(m[1])
			enumName := m[2]
			desc := ""
			if len(m) > 3 {
				desc = m[3]
			}

			// Start collecting enum values
			currentEnum = &Raw{
				K:           kEnum,
				Path:        []string{enumName},
				TypeExpr:    baseType,
				Description: desc,
			}
			enumValues = []string{}
			continue
		}

		// Check for @field or @property
		if m := reField.FindStringSubmatch(line); m != nil {
			// If we were collecting enum values, finalize the enum
			if currentEnum != nil {
				currentEnum.Enums = enumValues
				out = append(out, *currentEnum)
				currentEnum = nil
				enumValues = nil
			}

			typeExpr := strings.TrimSpace(m[1])
			fieldNameRaw := m[2]
			fieldName := strings.Trim(fieldNameRaw, "[]")
			omitEmpty := strings.HasPrefix(fieldNameRaw, "[") && strings.HasSuffix(fieldNameRaw, "]")
			defaultVal := ""
			if len(m) > 3 && m[3] != "" {
				defaultVal = m[3]
			}
			desc := ""
			if len(m) > 4 {
				desc = m[4]
			}

			// Find the parent type by looking at recent typedefs
			var parentType string
			for i := len(out) - 1; i >= 0; i-- {
				if out[i].K == kTypedef {
					parentType = out[i].Path[0]
					break
				}
			}

			if parentType != "" {
				r := Raw{
					K:           kField,
					Path:        []string{parentType, fieldName},
					TypeExpr:    typeExpr,
					DefaultVal:  defaultVal,
					Description: desc,
					OmitEmpty:   omitEmpty,
				}
				out = append(out, r)
			}
			continue
		}
	}

	// Finalize any pending enum
	if currentEnum != nil {
		currentEnum.Enums = enumValues
		out = append(out, *currentEnum)
	}

	return out, nil
}

/* -------------------------------------------------------------------------- */
/*  In-memory tree with implicit types                                         */
/* -------------------------------------------------------------------------- */

type Node struct {
	Name       string
	IsParam    bool
	TypeExpr   string
	Enums      []string
	DefaultVal string
	Comment    string
	OmitEmpty  bool
	Parent     *Node
	Child      map[string]*Node
}

func newNode(name string, p *Node) *Node {
	return &Node{Name: name, Parent: p, Child: map[string]*Node{}}
}

func ensure(root *Node, name string) *Node {
	if n, ok := root.Child[name]; ok {
		return n
	}
	n := newNode(name, root)
	root.Child[name] = n
	return n
}

func Build(rows []Raw) *Node {
	root := newNode("Config", nil)
	isPrim := func(s string) bool { return isPrimitive(strings.TrimPrefix(s, "*")) }
	addImplicit := func(name string) {
		if name == "" || isPrim(name) || strings.HasPrefix(name, "[]") || strings.HasPrefix(name, "map[") {
			return
		}
		ensure(root, name)
	}

	for _, r := range rows {
		if r.K == kTypedef {
			// Create a node for the typedef
			cur := ensure(root, r.Path[0])
			cur.Comment = r.Description
			cur.TypeExpr = "struct"
			continue
		}

		if r.K == kEnum {
			// Create a node for the enum with enum values
			cur := ensure(root, r.Path[0])
			cur.Comment = r.Description
			cur.TypeExpr = r.TypeExpr
			cur.Enums = r.Enums
			continue
		}

		if r.K == kParam {
			cur := ensure(root, r.Path[0])
			cur.IsParam = true
			cur.TypeExpr = r.TypeExpr
			cur.Comment = r.Description
			cur.Enums = r.Enums
			cur.OmitEmpty = r.OmitEmpty
			if r.DefaultVal != "" {
				cur.DefaultVal = r.DefaultVal
			}

			// Add implicit types
			te := strings.TrimSpace(r.TypeExpr)
			switch {
			case strings.HasPrefix(te, "[]"):
				base := strings.TrimPrefix(strings.TrimSpace(te[2:]), "*")
				addImplicit(base)
			case strings.HasPrefix(te, "map[") && strings.Contains(te, "]"):
				idx := strings.Index(te, "]")
				base := strings.TrimPrefix(strings.TrimSpace(te[idx+1:]), "*")
				addImplicit(base)
			default:
				addImplicit(strings.TrimPrefix(te, "*"))
			}
			continue
		}

		if r.K == kField {
			// Field belongs to a type
			if len(r.Path) < 2 {
				continue
			}
			parentName := r.Path[0]
			fieldName := r.Path[1]

			parent := ensure(root, parentName)
			field := ensure(parent, fieldName)
			field.TypeExpr = r.TypeExpr
			field.Comment = r.Description
			field.Enums = r.Enums
			field.OmitEmpty = r.OmitEmpty
			if r.DefaultVal != "" {
				field.DefaultVal = r.DefaultVal
			}

			// Add implicit types
			te := strings.TrimSpace(r.TypeExpr)
			switch {
			case strings.HasPrefix(te, "[]"):
				base := strings.TrimPrefix(strings.TrimSpace(te[2:]), "*")
				addImplicit(base)
			case strings.HasPrefix(te, "map[") && strings.Contains(te, "]"):
				idx := strings.Index(te, "]")
				base := strings.TrimPrefix(strings.TrimSpace(te[idx+1:]), "*")
				addImplicit(base)
			default:
				addImplicit(strings.TrimPrefix(te, "*"))
			}
		}
	}
	return root
}

/* -------------------------------------------------------------------------- */
/*  Generator helpers                                                          */
/* -------------------------------------------------------------------------- */

type gen struct {
	pkg string
	buf bytes.Buffer
	imp map[string]string
	ff  map[string]bool
	def map[string]bool
}

func (g *gen) addImpAlias(path, alias string) {
	if g.imp == nil {
		g.imp = map[string]string{}
	}
	if _, ok := g.imp[path]; !ok {
		g.imp[path] = alias
	}
}

func (g *gen) addImp(path string) { g.addImpAlias(path, "") }

func isPrimitive(t string) bool {
	if isStringFormat(t) {
		return true
	}
	switch t {
	case "string", "bool", "int", "int32", "int64", "float32", "float64",
		aliasQuantity, aliasDuration, aliasTime,
		aliasObject, aliasResources, aliasRequest, aliasLimit:
		return true
	default:
		return false
	}
}

func camel(in string) string {
	need := true
	var b strings.Builder
	for _, r := range in {
		if r == '_' || r == '-' {
			need = true
			continue
		}
		if need {
			b.WriteRune(unicode.ToUpper(r))
			need = false
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (g *gen) resolve(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "string"
	}
	if isStringFormat(raw) {
		return "string"
	}

	if strings.HasPrefix(raw, "[]") {
		base := strings.TrimPrefix(strings.TrimSpace(raw[2:]), "*")
		return "[]" + g.resolve(base)
	}
	if strings.HasPrefix(raw, "map[") && strings.Contains(raw, "]") {
		idx := strings.Index(raw, "]")
		base := strings.TrimPrefix(strings.TrimSpace(raw[idx+1:]), "*")
		return "map[string]" + g.resolve(base)
	}

	switch raw {
	case aliasQuantity:
		g.addImpAlias("k8s.io/apimachinery/pkg/api/resource", "resource")
		return "resource.Quantity"
	case aliasDuration:
		g.addImpAlias("k8s.io/apimachinery/pkg/apis/meta/v1", "metav1")
		return "metav1.Duration"
	case aliasTime:
		g.addImpAlias("k8s.io/apimachinery/pkg/apis/meta/v1", "metav1")
		return "metav1.Time"
	case aliasObject:
		g.addImpAlias("k8s.io/apimachinery/pkg/runtime", "k8sRuntime")
		return "k8sRuntime.RawExtension"
	}

	// context-aware resolution for well-known object-ish aliases
	if raw == "resources" || raw == "request" || raw == "limit" {
		if g.def[raw] || g.def[camel(raw)] {
			c := camel(raw)
			if c == "Config" || c == "ConfigSpec" {
				return "Values" + c
			}
			return c
		}
		g.addImpAlias("k8s.io/apimachinery/pkg/runtime", "k8sRuntime")
		return "k8sRuntime.RawExtension"
	}

	if isPrimitive(raw) || raw == "any" {
		return raw
	}
	if idx := strings.LastIndex(raw, "."); idx != -1 {
		g.addImpAlias(raw[:idx], "")
		out := raw[idx+1:]
		c := camel(out)
		if c == "Config" || c == "ConfigSpec" {
			return "Values" + c
		}
		return c
	}
	c := camel(raw)
	if c == "Config" || c == "ConfigSpec" {
		return "Values" + c
	}
	return c
}

func (g *gen) goType(n *Node) string {
	raw := strings.TrimSpace(n.TypeExpr)
	if raw == "" {
		if len(n.Child) > 0 {
			c := camel(n.Name)
			if c == "Config" || c == "ConfigSpec" {
				return "Values" + c
			}
			return c
		}
		return "string"
	}
	if strings.HasPrefix(raw, "*") {
		return "*" + g.resolve(strings.TrimPrefix(raw, "*"))
	}
	if strings.HasPrefix(raw, "[]") {
		base := strings.TrimPrefix(strings.TrimSpace(raw[2:]), "*")
		return "[]" + g.resolve(base)
	}
	if strings.HasPrefix(raw, "map[") && strings.Contains(raw, "]") {
		idx := strings.Index(raw, "]")
		base := strings.TrimPrefix(strings.TrimSpace(raw[idx+1:]), "*")
		return "map[string]" + g.resolve(base)
	}
	return g.resolve(raw)
}

func quoteEnums(vals []string) string {
	for i, v := range vals {
		vals[i] = fmt.Sprintf("%q", v)
	}
	return strings.Join(vals, ";")
}

/* -------------------------------------------------------------------------- */
/*  Struct emitter                                                             */
/* -------------------------------------------------------------------------- */

func (g *gen) writeEnum(n *Node) {
	if len(n.Enums) == 0 {
		return
	}

	name := camel(n.Name)
	if name == "Config" || name == "ConfigSpec" {
		name = "Values" + name
	}

	// Get base type
	baseType := n.TypeExpr
	if baseType == "" {
		baseType = "string"
	}

	g.buf.WriteString(fmt.Sprintf("// +kubebuilder:validation:Enum=%s\n", quoteEnums(n.Enums)))
	g.buf.WriteString(fmt.Sprintf("type %s %s\n\n", name, baseType))
}

func (g *gen) writeStruct(n *Node) {
	if strings.HasPrefix(n.Name, "[]") || strings.HasPrefix(n.Name, "map[") {
		return
	}

	if n.Parent == nil {
		g.addImp("k8s.io/apimachinery/pkg/apis/meta/v1")

		g.buf.WriteString("type Config struct {\n")
		g.buf.WriteString("    v1.TypeMeta   `json:\",inline\"`\n")
		g.buf.WriteString("    v1.ObjectMeta `json:\"metadata,omitempty\"`\n")
		g.buf.WriteString("    Spec              ConfigSpec `json:\"spec,omitempty\"`\n")
		g.buf.WriteString("}\n\n")

		g.buf.WriteString("type ConfigSpec struct {\n")
		keys := sortedKeys(n.Child)
		for _, k := range keys {
			c := n.Child[k]
			if !c.IsParam {
				continue
			}
			g.emitField(c)
		}
		g.buf.WriteString("}\n\n")

		for _, c := range n.Child {
			g.writeStruct(c)
		}
		return
	}

	if len(n.Child) == 0 && strings.TrimSpace(n.TypeExpr) != "" {
		return
	}

	name := camel(n.Name)
	if name == "Config" || name == "ConfigSpec" {
		name = "Values" + name
	}

	g.buf.WriteString(fmt.Sprintf("type %s struct {\n", name))
	keys := sortedKeys(n.Child)
	for _, k := range keys {
		g.emitField(n.Child[k])
	}
	g.buf.WriteString("}\n\n")

	for _, c := range n.Child {
		g.writeStruct(c)
	}
}

func (g *gen) emitField(c *Node) {
	field := camel(c.Name)
	typ := g.goType(c)

	if c.Comment != "" {
		g.buf.WriteString("    // " + c.Comment + "\n")
	}

	if f := strings.TrimPrefix(strings.TrimSpace(c.TypeExpr), "*"); isStringFormat(f) &&
		!strings.HasPrefix(typ, "[]") && !strings.HasPrefix(typ, "map[") {
		g.buf.WriteString("    // +kubebuilder:validation:Format=" + f + "\n")
	}

	if len(c.Enums) > 0 {
		g.buf.WriteString("    // +kubebuilder:validation:Enum=" + quoteEnums(c.Enums) + "\n")
	}

	if c.DefaultVal != "" {
		if def := formatDefault(c.DefaultVal, typ); def != "" {
			g.buf.WriteString("    // +kubebuilder:default:=" + def + "\n")
		}
	}

	tag := "`json:\"" + c.Name
	// Add omitempty for: slices, maps, pointers, or fields explicitly marked with []
	if strings.HasPrefix(typ, "[]") || strings.HasPrefix(typ, "map[") || strings.HasPrefix(typ, "*") || c.OmitEmpty {
		tag += ",omitempty"
	}
	tag += "\"`"
	g.buf.WriteString(fmt.Sprintf("    %s %s %s\n", field, typ, tag))
}

func (g *gen) ensureFreeFormTypeFor(fieldOwner, fieldName string) string {
	if g.ff == nil {
		g.ff = map[string]bool{}
	}
	typeName := camel(fieldOwner) + camel(fieldName) + "FreeForm"
	if typeName == "Config" || typeName == "ConfigSpec" {
		typeName = "Values" + typeName
	}
	g.ff[typeName] = true
	return typeName
}

func (g *gen) Generate(root *Node) ([]byte, []byte, error) {
	if undef := CollectUndefined(root); len(undef) > 0 {
		return nil, nil, fmt.Errorf("undefined types: %s", strings.Join(undef, ", "))
	}
	g.buf.WriteString("// +kubebuilder:object:generate=true\n")
	g.buf.WriteString("// +kubebuilder:object:root=true\n")
	g.buf.WriteString("// +groupName=values.helm.io\n\n")
	g.buf.WriteString("// +versionName=v1alpha1\n\n")
	g.buf.WriteString("// Code generated by values-gen. DO NOT EDIT.\n")
	g.buf.WriteString("package " + g.pkg + "\n\n")

	// collect defined complex types (have children)
	g.def = map[string]bool{}
	var walk func(n *Node)
	walk = func(n *Node) {
		for name, c := range n.Child {
			if name != "" && len(c.Child) > 0 {
				g.def[name] = true
				g.def[camel(name)] = true
			}
			walk(c)
		}
	}
	walk(root)

	g.writeStruct(root)

	// Generate enum types
	for _, c := range root.Child {
		if len(c.Enums) > 0 {
			g.writeEnum(c)
		}
	}

	var src []byte
	if len(g.imp) > 0 {
		var imp bytes.Buffer
		imp.WriteString("import (\n")
		for _, p := range sortedKeys(g.imp) {
			if a := g.imp[p]; a != "" {
				imp.WriteString("    " + a + " \"" + p + "\"\n")
			} else {
				imp.WriteString("    \"" + p + "\"\n")
			}
		}
		imp.WriteString(")\n\n")
		head := "package " + g.pkg + "\n\n"
		src = g.buf.Bytes()
		src = []byte(strings.Replace(string(src), head, head+imp.String(), 1))
	} else {
		src = g.buf.Bytes()
	}

	formatted, err := format.Source(src)
	if err != nil {
		return nil, src, fmt.Errorf("formatting failed: %w", err)
	}
	return formatted, src, nil
}

func sortedKeys[M ~map[K]V, K comparable, V any](m M) []K {
	keys := make([]K, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return fmt.Sprint(keys[i]) < fmt.Sprint(keys[j])
	})
	return keys
}

/* -------------------------------------------------------------------------- */
/*  Temporary project scaffolding                                              */
/* -------------------------------------------------------------------------- */

func WriteGeneratedGoAndStub(root *Node, module string) (tmpdir, goFilePath string, err error) {
	tmpdir, err = os.MkdirTemp("", "values-gen-*")
	if err != nil {
		return "", "", err
	}

	goMod := `module fake.local/generated

go 1.20

require (
	k8s.io/apimachinery v0.0.0
)

replace k8s.io/apimachinery => ./k8s.io/apimachinery
`
	if err := os.WriteFile(filepath.Join(tmpdir, "go.mod"), []byte(goMod), 0o644); err != nil {
		return "", "", err
	}

	pkgDir := filepath.Join(tmpdir, module)
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		return "", "", err
	}

	g := &gen{pkg: module}
	formatted, raw, err := g.Generate(root)
	goFilePath = filepath.Join(pkgDir, "values_generated.go")
	if err != nil {
		_ = os.WriteFile(goFilePath, raw, 0o644)
		return tmpdir, goFilePath, fmt.Errorf("write generated (unformatted): %w", err)
	}
	if err := os.WriteFile(goFilePath, formatted, 0o644); err != nil {
		return "", "", err
	}

	stubModuleDir := filepath.Join(tmpdir, "k8s.io/apimachinery")
	if err := os.MkdirAll(filepath.Join(stubModuleDir, "pkg/apis/meta/v1"), 0o755); err != nil {
		return "", "", err
	}
	stubMod := `module k8s.io/apimachinery

go 1.20
`
	if err := os.WriteFile(filepath.Join(stubModuleDir, "go.mod"), []byte(stubMod), 0o644); err != nil {
		return "", "", err
	}

	/* ---------- stub k8s.io/apimachinery/pkg/apis/meta/v1 ---------- */

	stubCode := `package v1

type TypeMeta struct{}
type ObjectMeta struct{}

// Duration is a stub so that go/types can resolve metav1.Duration.
// Real validation is injected by controller-tools KnownPackages.
type Duration struct{}
type Time struct{}
type MicroTime struct{}
type Fields map[string]interface{}
`

	stubPath := filepath.Join(stubModuleDir, "pkg/apis/meta/v1/doc.go")
	if err := os.WriteFile(stubPath, []byte(stubCode), 0o644); err != nil {
		return "", "", err
	}

	/* ---------- stub k8s.io/apimachinery/pkg/api/resource ---------- */

	resDir := filepath.Join(stubModuleDir, "pkg/api/resource")
	if err := os.MkdirAll(resDir, 0o755); err != nil {
		return "", "", err
	}
	stubQty := `package resource
type Quantity struct{}
`
	if err := os.WriteFile(filepath.Join(resDir, "doc.go"), []byte(stubQty), 0o644); err != nil {
		return "", "", err
	}

	/* ---------- stub k8s.io/apimachinery/pkg/util/intstr ---------- */

	intstrDir := filepath.Join(stubModuleDir, "pkg/util/intstr")
	if err := os.MkdirAll(intstrDir, 0o755); err != nil {
		return "", "", err
	}
	stubIS := `package intstr
type IntOrString struct{}
`
	if err := os.WriteFile(filepath.Join(intstrDir, "doc.go"), []byte(stubIS), 0o644); err != nil {
		return "", "", err
	}

	/* --------- stub for apiextensions-apiserver/pkg/apis/apiextensions/v1 --- */
	rtDir := filepath.Join(stubModuleDir, "pkg/runtime")
	if err := os.MkdirAll(rtDir, 0o755); err != nil {
		return "", "", err
	}
	stubRE := `package runtime
type RawExtension struct{}
`
	if err := os.WriteFile(filepath.Join(rtDir, "doc.go"), []byte(stubRE), 0o644); err != nil {
		return "", "", err
	}

	return tmpdir, goFilePath, nil
}

/* -------------------------------------------------------------------------- */
/*  Controller-gen wrapper                                                     */
/* -------------------------------------------------------------------------- */

func CG(pkgDir string) ([]byte, error) {
	roots, err := loader.LoadRoots(pkgDir)
	if err != nil {
		return nil, fmt.Errorf("loader: %w", err)
	}

	reg := &markers.Registry{}
	if err := crdmarkers.Register(reg); err != nil {
		return nil, fmt.Errorf("register markers: %w", err)
	}

	parser := &crd.Parser{
		Collector:                  &markers.Collector{Registry: reg},
		Checker:                    &loader.TypeChecker{},
		IgnoreUnexportedFields:     false,
		AllowDangerousTypes:        true,
		GenerateEmbeddedObjectMeta: false,
	}
	crd.AddKnownTypes(parser)

	for _, r := range roots {
		parser.NeedPackage(r)
	}

	metaPkg := crd.FindMetav1(roots)
	if metaPkg == nil {
		return nil, fmt.Errorf("cannot locate metav1 import")
	}
	kinds := crd.FindKubeKinds(parser, metaPkg)
	if len(kinds) == 0 {
		return nil, fmt.Errorf("no Kubernetes kinds found in %s", pkgDir)
	}

	var buf bytes.Buffer
	for _, gk := range kinds {
		parser.NeedCRDFor(gk, nil)
		crdRaw := parser.CustomResourceDefinitions[gk]

		if crdRaw.ObjectMeta.Annotations == nil {
			crdRaw.ObjectMeta.Annotations = map[string]string{}
		}
		crdRaw.ObjectMeta.Annotations["controller-gen.kubebuilder.io/version"] = version.Version
		crd.FixTopLevelMetadata(crdRaw)

		data, err := sigyaml.Marshal(&crdRaw)
		if err != nil {
			return nil, err
		}
		if buf.Len() > 0 {
			buf.WriteString("---\n")
		}
		buf.Write(data)
	}
	return buf.Bytes(), nil
}

/* -------------------------------------------------------------------------- */
/*  Helm values JSON Schema                                                   */
/* -------------------------------------------------------------------------- */

func WriteValuesSchema(crdBytes []byte, outPath string) error {
	docs := bytes.Split(crdBytes, []byte("\n---"))
	if len(docs) == 0 {
		return fmt.Errorf("empty CRD data")
	}

	var obj apiextv1.CustomResourceDefinition
	if err := sigyaml.Unmarshal(docs[0], &obj); err != nil {
		return err
	}
	if len(obj.Spec.Versions) == 0 {
		return fmt.Errorf("CRD has no versions")
	}

	specSchema := obj.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"]
	out := struct {
		Title      string                              `json:"title"`
		Type       string                              `json:"type"`
		Properties map[string]apiextv1.JSONSchemaProps `json:"properties"`
	}{
		Title:      "Chart Values",
		Type:       "object",
		Properties: specSchema.Properties,
	}

	buf, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(outPath, buf, 0o644)
}

/* -------------------------------------------------------------------------- */
/*  Defaults, validation helpers                                              */
/* -------------------------------------------------------------------------- */

func PopulateDefaults(n *Node, y interface{}, aliases map[string]*Node) {
	switch v := y.(type) {
	case map[string]interface{}:
		for k, yval := range v {
			child, ok := n.Child[k]
			if !ok || yval == nil {
				continue
			}

			switch vv := yval.(type) {
			case string, int, int64, float64, bool:
				if child.DefaultVal == "" {
					child.DefaultVal = fmt.Sprintf("%v", vv)
				}

			case map[string]interface{}:
				isObj := len(child.Child) > 0
				var alias *Node
				if te := strings.TrimPrefix(child.TypeExpr, "*"); te != "" {
					if a, ok := aliases[te]; ok {
						alias = a
						if len(a.Child) > 0 {
							isObj = true
						}
					}
				}

				if isObj {
					if child.DefaultVal == "" {
						child.DefaultVal = "{}"
					}
					if alias != nil {
						PopulateDefaults(alias, vv, aliases)
					} else {
						PopulateDefaults(child, vv, aliases)
					}
					continue
				}

				if child.DefaultVal == "" {
					if b, err := sigyaml.Marshal(vv); err == nil {
						child.DefaultVal = string(b)
					} else {
						child.DefaultVal = "{}"
					}
				}

			default:
				if child.DefaultVal == "" {
					if b, err := sigyaml.Marshal(vv); err == nil {
						child.DefaultVal = string(b)
					} else {
						child.DefaultVal = fmt.Sprintf("%v", vv)
					}
				}
			}
		}
	}
}

func toGoLiteral(v interface{}) string {
	switch val := v.(type) {
	case map[string]interface{}:
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%q:%s", k, toGoLiteral(val[k])))
		}
		return fmt.Sprintf("{%s}", strings.Join(parts, ","))
	case []interface{}:
		parts := make([]string, 0, len(val))
		for _, elem := range val {
			parts = append(parts, toGoLiteral(elem))
		}
		return fmt.Sprintf("{%s}", strings.Join(parts, ","))
	case string:
		return fmt.Sprintf("%q", val)
	case bool:
		if val {
			return "true"
		}
		return "false"
	case int, int64, float64:
		return fmt.Sprintf("%v", val)
	default:
		return fmt.Sprintf("%v", val)
	}
}

func formatDefault(val, typ string) string {
	t := strings.TrimPrefix(typ, "*")
	if t == camel(aliasEmptyObject) {
		return ""
	}
	if t == "string" || t == "quantity" {
		// Remove quotes if already present
		val = strings.Trim(val, `"`)
		return fmt.Sprintf("%q", val)
	}
	var parsed interface{}
	if err := sigyaml.Unmarshal([]byte(val), &parsed); err == nil {
		return toGoLiteral(parsed)
	}
	return val
}

func CollectUndefined(root *Node) []string {
	defined := map[string]struct{}{}
	referenced := map[string]struct{}{}

	baseOf := func(expr string) string {
		expr = strings.TrimSpace(expr)
		if expr == "" {
			return ""
		}
		if strings.HasPrefix(expr, "*") {
			expr = strings.TrimPrefix(expr, "*")
		}
		if strings.HasPrefix(expr, "[]") {
			expr = strings.TrimSpace(expr[2:])
			if strings.HasPrefix(expr, "*") {
				expr = strings.TrimPrefix(expr, "*")
			}
		}
		if strings.HasPrefix(expr, "map[") {
			if i := strings.Index(expr, "]"); i != -1 {
				expr = strings.TrimSpace(expr[i+1:])
				if strings.HasPrefix(expr, "*") {
					expr = strings.TrimPrefix(expr, "*")
				}
			}
		}
		return expr
	}

	var walk func(n *Node)
	walk = func(n *Node) {
		if n.Parent != nil {
			name := n.Name
			base := strings.TrimPrefix(name, "*")
			if base != "" &&
				base != aliasObject && base != aliasEmptyObject &&
				base != "struct" && base != "object" &&
				!isPrimitive(base) &&
				!strings.HasPrefix(base, "[]") &&
				!strings.HasPrefix(base, "map[") &&
				(len(n.Child) > 0 || len(n.Enums) > 0) {
				defined[base] = struct{}{}
			}
		}
		if b := baseOf(n.TypeExpr); b != "" {
			if b != aliasObject && b != aliasEmptyObject &&
				b != "struct" && b != "object" &&
				b != aliasResources && b != aliasRequest && b != aliasLimit &&
				!isPrimitive(b) &&
				!strings.HasPrefix(b, "[]") &&
				!strings.HasPrefix(b, "map[") {
				referenced[b] = struct{}{}
			}
		}
		for _, c := range n.Child {
			walk(c)
		}
	}
	walk(root)

	var bad []string
	for t := range referenced {
		if _, ok := defined[t]; !ok {
			bad = append(bad, t)
		}
	}
	sort.Strings(bad)
	return bad
}
