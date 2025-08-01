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
)

type Raw struct {
	K           kind
	Path        []string
	TypeExpr    string
	Enums       []string
	DefaultVal  string
	Description string
}

var (
	re       = regexp.MustCompile(`^#{1,}\s+@(param|field)\s+([^\s]+)\s+\{([^}]+)\}\s*(.*)$`)
	reAttr   = regexp.MustCompile(`(\w+):"([^"]*)"`)
	reYamlKV = regexp.MustCompile(`^\s*([A-Za-z0-9_-]+)\s*:\s*(.+?)\s*$`)
	// []foo, []*foo, []mypkg.Foo …
	reSlice = regexp.MustCompile(`^\s*$begin:math:display$$end:math:display$\s*(\*?[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)?)\s*$`)
	// map[string]foo, map[string]*foo …
	reMap = regexp.MustCompile(`^\s*map\s*$begin:math:display$\\s*string\\s*$end:math:display$\s*(\*?[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)?)\s*$`)
)

func Parse(file string) ([]Raw, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(data), "\n")

	var out []Raw
	attrRe := regexp.MustCompile(`(\w+)\s*(?:=|:)"([^"]*)"`)

	for i := 0; i < len(lines); i++ {
		m := re.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}

		k := kParam
		if m[1] == "field" {
			k = kField
		}

		fields := strings.Fields(strings.TrimSpace(m[3]))
		if len(fields) == 0 {
			continue
		}
		typPart := fields[0]
		attrPart := strings.Join(fields[1:], " ")

		var enums []string
		var def string
		for _, am := range attrRe.FindAllStringSubmatch(attrPart, -1) {
			switch am[1] {
			case "enum":
				enums = strings.Split(am[2], ",")
			case "default":
				def = am[2]
			}
		}

		segments := strings.FieldsFunc(m[2], func(r rune) bool {
			return r == '/' || r == '.'
		})
		r := Raw{
			K:           k,
			Path:        segments,
			TypeExpr:    typPart,
			Enums:       enums,
			DefaultVal:  def,
			Description: strings.TrimSpace(m[4]),
		}

		if r.DefaultVal == "" && i+1 < len(lines) {
			next := strings.TrimSpace(lines[i+1])
			if !strings.HasPrefix(next, "##") {
				if ym := reYamlKV.FindStringSubmatch(next); ym != nil &&
					ym[1] == r.Path[len(r.Path)-1] {
					r.DefaultVal = strings.Trim(ym[2], `"'`)
				}
			}
		}
		out = append(out, r)
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
		cur := root
		for i, seg := range r.Path {
			cur = ensure(cur, seg)
			if i == len(r.Path)-1 {
				if r.K == kParam {
					cur.IsParam = true
				}
				cur.TypeExpr = r.TypeExpr
				cur.Comment = r.Description
				cur.Enums = r.Enums
				if r.DefaultVal != "" {
					cur.DefaultVal = r.DefaultVal
				}
			}
		}

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
	return root
}

/* -------------------------------------------------------------------------- */
/*  Generator helpers                                                          */
/* -------------------------------------------------------------------------- */

type gen struct {
	pkg string
	buf bytes.Buffer
	imp map[string]struct{}
}

func (g *gen) addImp(p string) {
	if g.imp == nil {
		g.imp = map[string]struct{}{}
	}
	g.imp[p] = struct{}{}
}

func isPrimitive(t string) bool {
	switch t {
	case "string", "bool", "int", "int32", "int64", "float32", "float64",
		"quantity":
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
	if isPrimitive(raw) || raw == "any" {
		return raw
	}
	if strings.HasPrefix(raw, "map[") || strings.HasPrefix(raw, "[]") {
		return raw
	}
	if strings.Contains(raw, ".") {
		if raw == "quantity" {
			return "string"
		}
		if idx := strings.LastIndex(raw, "."); idx != -1 {
			g.addImp(raw[:idx])
			return raw[idx+1:]
		}
	}
	return camel(raw)
}

func (g *gen) goType(n *Node) string {
	raw := strings.TrimSpace(n.TypeExpr)
	if raw == "" {
		if len(n.Child) > 0 {
			return camel(n.Name)
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

		g.buf.WriteString(`
// +kubebuilder:validation:Pattern=` + "`" + `^(\+|-)?(([0-9]+(\.[0-9]*)?)|(\.[0-9]+))(([KMGTPE]i)|[numkMGTPE]|([eE](\+|-)?(([0-9]+(\.[0-9]*)?)|(\.[0-9]+))))?$` + "`" + `
// +kubebuilder:validation:XIntOrString
type quantity string

`)
		return
	}

	if len(n.Child) == 0 && strings.TrimSpace(n.TypeExpr) != "" {
		return
	}

	g.buf.WriteString(fmt.Sprintf("type %s struct {\n", camel(n.Name)))
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
	if len(c.Enums) > 0 {
		g.buf.WriteString("    // +kubebuilder:validation:Enum=" + quoteEnums(c.Enums) + "\n")
	}
	if c.DefaultVal != "" {
		if def := formatDefault(c.DefaultVal, typ); def != "" {
			g.buf.WriteString("    // +kubebuilder:default:=" + def + "\n")
		}
	}
	tag := "`json:\"" + c.Name
	if strings.HasPrefix(typ, "[]") || strings.HasPrefix(typ, "map[") || strings.HasPrefix(typ, "*") {
		tag += ",omitempty"
	}
	tag += "\"`"
	g.buf.WriteString(fmt.Sprintf("    %s %s %s\n", field, typ, tag))
}

func (g *gen) Generate(root *Node) ([]byte, []byte, error) {
	g.buf.WriteString("// +kubebuilder:object:generate=true\n")
	g.buf.WriteString("// +kubebuilder:object:root=true\n")
	g.buf.WriteString("// +groupName=values.helm.io\n\n")
	g.buf.WriteString("// +versionName=v1alpha1\n\n")
	g.buf.WriteString("// Code generated by values-gen. DO NOT EDIT.\n")
	g.buf.WriteString("package " + g.pkg + "\n\n")

	g.writeStruct(root)

	var src []byte
	if len(g.imp) > 0 {
		var imp bytes.Buffer
		imp.WriteString("import (\n")
		for _, k := range sortedKeys(g.imp) {
			imp.WriteString("    \"" + k + "\"\n")
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
	stubCode := `package v1

type TypeMeta struct{}
type ObjectMeta struct{}
`
	stubPath := filepath.Join(stubModuleDir, "pkg/apis/meta/v1/doc.go")
	if err := os.WriteFile(stubPath, []byte(stubCode), 0o644); err != nil {
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
			if !ok {
				continue
			}

			if yval == nil {
				continue
			}

			switch yval.(type) {
			case string, int, int64, float64, bool:
				if child.DefaultVal == "" {
					child.DefaultVal = fmt.Sprintf("%v", yval)
				}
			default:
				if child.DefaultVal == "" {
					serialized, _ := sigyaml.Marshal(yval)
					child.DefaultVal = string(serialized)
				}
				if m, ok := yval.(map[string]interface{}); ok {
					if len(child.Child) > 0 {
						PopulateDefaults(child, m, aliases)
						continue
					}
					te := strings.TrimPrefix(child.TypeExpr, "*")
					if alias, ok := aliases[te]; ok {
						PopulateDefaults(alias, m, aliases)
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
	if t == "string" || t == "quantity" {
		return fmt.Sprintf("%q", val)
	}
	var parsed interface{}
	if err := sigyaml.Unmarshal([]byte(val), &parsed); err == nil {
		return toGoLiteral(parsed)
	}
	return val
}

func CollectUndefined(root *Node) []string {
	var bad []string
	for name, n := range root.Child {
		if isPrimitive(strings.TrimPrefix(name, "*")) ||
			strings.HasPrefix(name, "[]") ||
			strings.HasPrefix(name, "map[") {
			continue
		}
		if !n.IsParam && len(n.Child) == 0 {
			bad = append(bad, name)
		}
	}
	sort.Strings(bad)
	return bad
}
