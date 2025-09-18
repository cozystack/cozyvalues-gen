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
}

var (
	re       = regexp.MustCompile(`^#{1,}\s+@(param|field)\s+([^\s]+)\s+\{(.+)\}\s*(.*)$`)
	reAttr   = regexp.MustCompile(`(\w+):"([^"]*)"`)
	reYamlKV = regexp.MustCompile(`^\s*([A-Za-z0-9_-]+)\s*:\s*(.+?)\s*$`)
	// []foo, []*foo, []mypkg.Foo …
	reSlice = regexp.MustCompile(`^\s*$begin:math:display$$end:math:display$\s*(\*?[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)?)\s*$`)
	// map[string]foo, map[string]*foo …
	reMap = regexp.MustCompile(`^\s*map\s*$begin:math:display$\\s*string\\s*$end:math:display$\s*(\*?[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)?)\s*$`)
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
	attrRe := regexp.MustCompile(`(\w+)\s*(?:=|:)\s*(?:"([^"]*)"|(\{[^}]*\}|$begin:math:display$[^$end:math:display$]*\]|true|false|-?\d+(?:\.\d+)?))`)

	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		m := re.FindStringSubmatch(line)
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
			key := am[1]
			val := am[2]
			if val == "" {
				val = am[3]
			}
			switch key {
			case "enum":
				enums = strings.Split(val, ",")
			case "default":
				def = val
			}
		}

		segments := strings.FieldsFunc(m[2], func(r rune) bool { return r == '/' || r == '.' })
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
				if ym := reYamlKV.FindStringSubmatch(next); ym != nil && ym[1] == r.Path[len(r.Path)-1] {
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
		// allow “@field <param>.<field>” even when <param> is declared with alias type
		segments := append([]string(nil), r.Path...)
		if r.K == kField && len(segments) > 1 {
			if pnode, ok := root.Child[segments[0]]; ok {
				alias := strings.TrimPrefix(strings.TrimSpace(pnode.TypeExpr), "*")
				if alias != "" && !isPrim(alias) {
					segments[0] = alias
				}
			}
		}

		cur := root
		for i, seg := range segments {
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
	if strings.HasPrefix(typ, "[]") || strings.HasPrefix(typ, "map[") || strings.HasPrefix(typ, "*") {
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
			if !ok {
				continue
			}
			if yval == nil {
				continue
			}

			switch vv := yval.(type) {
			case string, int, int64, float64, bool:
				// простые типы — кладём литерал как есть (не затираем явный default= из аннотации)
				if child.DefaultVal == "" {
					child.DefaultVal = fmt.Sprintf("%v", vv)
				}

			case map[string]interface{}:
				// составной объект
				// 1) если у узла есть дочерние поля (или он алиас на пользовательский тип),
				//    сериализуем весь объект в YAML и сохраняем в DefaultVal
				// 2) рекурсивно прокидываем значения внутрь, чтобы вложенным полям выставились дефолты
				if child.DefaultVal == "" {
					if b, err := sigyaml.Marshal(vv); err == nil {
						child.DefaultVal = string(b)
					} else {
						child.DefaultVal = "{}"
					}
				}

				// если узел — алиас на тип, прокидываем в него,
				// иначе — в самого ребёнка
				if te := strings.TrimPrefix(child.TypeExpr, "*"); te != "" {
					if alias, ok := aliases[te]; ok {
						PopulateDefaults(alias, vv, aliases)
					} else {
						PopulateDefaults(child, vv, aliases)
					}
				} else {
					PopulateDefaults(child, vv, aliases)
				}

			default:
				// слайсы, числа в интерфейсах и пр. — сериализуем «как есть»
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
				!isPrimitive(base) &&
				!strings.HasPrefix(base, "[]") &&
				!strings.HasPrefix(base, "map[") &&
				len(n.Child) > 0 {
				defined[base] = struct{}{}
			}
		}
		if b := baseOf(n.TypeExpr); b != "" {
			if b != aliasObject && b != aliasEmptyObject &&
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
