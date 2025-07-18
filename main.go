// values-gen: Generate Go structs, CRDs and Helm-compatible JSON Schemas
//
//	from “## @param / @field” comments inside a values.yaml file.
//
// Controller-gen ≥ v0.16 is required for CRD generation.
package main

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

	"github.com/cozystack/cozyvalues-gen/readme"
	"github.com/spf13/pflag"
	crdmarkers "sigs.k8s.io/controller-tools/pkg/crd/markers"

	"go.etcd.io/etcd/version"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/controller-tools/pkg/crd"
	"sigs.k8s.io/controller-tools/pkg/loader"
	"sigs.k8s.io/controller-tools/pkg/markers"
	sigyaml "sigs.k8s.io/yaml"
)

/* -------------------------------------------------------------------------- */
/*  Annotation parsing                                                         */
/* -------------------------------------------------------------------------- */

// kind distinguishes ## @param from ## @field.
type kind int

const (
	kParam kind = iota
	kField
)

// raw holds a single annotation line in machine-friendly form.
type raw struct {
	k           kind
	path        []string
	typeExpr    string
	enums       []string
	defaultVal  string
	description string
}

var (
	inValues  string
	module    string
	outGo     string
	outCRD    string
	outSchema string
	outReadme string
)

var (
	re       = regexp.MustCompile(`^##\s+@(param|field)\s+([^\s]+)\s+\{([^}]+)\}\s+(.+)$`)
	reAttr   = regexp.MustCompile(`(\w+):"([^"]*)"`)
	reYamlKV = regexp.MustCompile(`^\s*([A-Za-z0-9_-]+)\s*:\s*(.+?)\s*$`)
	reSlice  = regexp.MustCompile(`^\s*\[\](\w+)$`)
	reMap    = regexp.MustCompile(`^\s*map\s*\[string\]\s*(\w+)$`)
)

// parse scans a *.yaml file for annotation lines and returns
// a slice of raw descriptors.
func parse(file string) ([]raw, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(data), "\n")

	var out []raw
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
		for _, am := range reAttr.FindAllStringSubmatch(attrPart, -1) {
			switch am[1] {
			case "enum":
				enums = strings.Split(am[2], ",")
			case "default":
				def = am[2]
			}
		}

		r := raw{
			k:           k,
			path:        strings.Split(m[2], "/"),
			typeExpr:    typPart,
			enums:       enums,
			defaultVal:  def,
			description: strings.TrimSpace(m[4]),
		}

		// Fallback: take default from the next YAML line if present.
		if r.defaultVal == "" && i+1 < len(lines) {
			next := strings.TrimSpace(lines[i+1])
			if !strings.HasPrefix(next, "##") {
				if ym := reYamlKV.FindStringSubmatch(next); ym != nil &&
					ym[1] == r.path[len(r.path)-1] { // only top-level keys
					r.defaultVal = strings.Trim(ym[2], `"'`)
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

// node represents a YAML path segment plus metadata needed for code-gen.
type node struct {
	name       string
	isParam    bool
	typeExpr   string
	enums      []string
	defaultVal string
	comment    string
	parent     *node
	child      map[string]*node
}

// newNode allocates a tree node.
func newNode(name string, p *node) *node {
	return &node{name: name, parent: p, child: map[string]*node{}}
}

// ensure returns an existing child or creates a new one.
func ensure(root *node, name string) *node {
	if n, ok := root.child[name]; ok {
		return n
	}
	n := newNode(name, root)
	root.child[name] = n
	return n
}

// build converts raw annotations into a fully-linked tree,
// creating implicit struct types for map[string]T / []T usages.
func build(rows []raw) *node {
	root := newNode("Config", nil)

	isPrim := func(s string) bool { return isPrimitive(strings.TrimPrefix(s, "*")) }

	addImplicit := func(name string) {
		if name == "" || isPrim(name) {
			return
		}
		ensure(root, name)
	}

	for _, r := range rows {
		cur := root
		for i, seg := range r.path {
			cur = ensure(cur, seg)
			if i == len(r.path)-1 {
				if r.k == kParam {
					cur.isParam = true
				}
				cur.typeExpr = r.typeExpr
				cur.comment = r.description
				cur.enums = r.enums
				if r.defaultVal != "" {
					cur.defaultVal = r.defaultVal
				}
			}
		}

		te := strings.TrimSpace(r.typeExpr)
		switch {
		case reSlice.MatchString(te):
			addImplicit(reSlice.FindStringSubmatch(te)[1])
		case reMap.MatchString(te):
			addImplicit(reMap.FindStringSubmatch(te)[1])
		default:
			addImplicit(strings.TrimPrefix(te, "*"))
		}
	}
	return root
}

/* -------------------------------------------------------------------------- */
/*  Generator helpers                                                          */
/* -------------------------------------------------------------------------- */

// gen accumulates generated Go source and its import set.
type gen struct {
	pkg string
	buf bytes.Buffer
	imp map[string]struct{}
}

// addImp records an import path once.
func (g *gen) addImp(p string) {
	if g.imp == nil {
		g.imp = map[string]struct{}{}
	}
	g.imp[p] = struct{}{}
}

// isPrimitive returns true for built-in Go scalar types.
func isPrimitive(t string) bool {
	switch t {
	case "string", "bool", "int", "int32", "int64", "float32", "float64":
		return true
	default:
		return false
	}
}

// camel converts snake- or kebab-case to CamelCase.
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

// resolve converts a raw type expression (possibly pkg.Type)
// into the identifier used in generated code and adds imports as needed.
func (g *gen) resolve(raw string) string {
	if isPrimitive(raw) {
		return raw
	}
	if strings.Contains(raw, ".") { // external
		switch raw {
		case "resource.Quantity":
			g.addImp("k8s.io/apimachinery/pkg/api/resource")
			return "resource.Quantity"
		case "time.Duration":
			g.addImp("time")
			return "time.Duration"
		default:
			if idx := strings.LastIndex(raw, "."); idx != -1 {
				g.addImp(raw[:idx])
				return raw[idx+1:]
			}
		}
	}
	return camel(raw) // same-package struct
}

// goType resolves a node’s type expression into a valid Go type.
func (g *gen) goType(n *node) string {
	raw := strings.TrimSpace(n.typeExpr)
	switch {
	case raw == "":
		if len(n.child) > 0 {
			return camel(n.name)
		}
		return "string"
	case strings.HasPrefix(raw, "*"):
		return "*" + g.resolve(strings.TrimPrefix(raw, "*"))
	case reSlice.MatchString(raw):
		return "[]" + g.resolve(reSlice.FindStringSubmatch(raw)[1])
	case reMap.MatchString(raw):
		return "map[string]" + g.resolve(reMap.FindStringSubmatch(raw)[1])
	default:
		return g.resolve(raw)
	}
}

// quoteEnums formats enum values for kubebuilder validation markers.
func quoteEnums(vals []string) string {
	for i, v := range vals {
		vals[i] = fmt.Sprintf("%q", v)
	}
	return strings.Join(vals, ";")
}

/* -------------------------------------------------------------------------- */
/*  Struct emitter                                                             */
/* -------------------------------------------------------------------------- */

// writeStruct emits Go struct definitions recursively.
func (g *gen) writeStruct(n *node) {
	if n.parent == nil { // root: Config + ConfigSpec
		g.addImp("k8s.io/apimachinery/pkg/apis/meta/v1")

		g.buf.WriteString("type Config struct {\n")
		g.buf.WriteString("    v1.TypeMeta   `json:\",inline\"`\n")
		g.buf.WriteString("    v1.ObjectMeta `json:\"metadata,omitempty\"`\n")
		g.buf.WriteString("    Spec              ConfigSpec `json:\"spec,omitempty\"`\n")
		g.buf.WriteString("}\n\n")

		g.buf.WriteString("type ConfigSpec struct {\n")
		keys := sortedKeys(n.child)
		for _, k := range keys {
			c := n.child[k]
			if !c.isParam {
				continue
			}
			g.emitField(c)
		}
		g.buf.WriteString("}\n\n")

		for _, c := range n.child {
			g.writeStruct(c)
		}
		return
	}

	if len(n.child) == 0 { // leaf with explicit type
		return
	}

	g.buf.WriteString(fmt.Sprintf("type %s struct {\n", camel(n.name)))
	keys := sortedKeys(n.child)
	for _, k := range keys {
		g.emitField(n.child[k])
	}
	g.buf.WriteString("}\n\n")

	for _, c := range n.child {
		g.writeStruct(c)
	}
}

// emitField writes a single struct field with validation / default markers.
func (g *gen) emitField(c *node) {
	field := camel(c.name)
	typ := g.goType(c)

	if c.comment != "" {
		g.buf.WriteString("    // " + c.comment + "\n")
	}
	if len(c.enums) > 0 {
		g.buf.WriteString("    // +kubebuilder:validation:Enum=" + quoteEnums(c.enums) + "\n")
	}
	if c.defaultVal != "" {
		if def := formatDefault(c.defaultVal, typ); def != "" {
			g.buf.WriteString("    // +kubebuilder:default:=" + def + "\n")
		}
	}

	tag := "`json:\"" + c.name
	if strings.HasPrefix(typ, "[]") || strings.HasPrefix(typ, "map[") || strings.HasPrefix(typ, "*") {
		tag += ",omitempty"
	}
	tag += "\"`"

	g.buf.WriteString(fmt.Sprintf("    %s %s %s\n", field, typ, tag))
}

/* -------------------------------------------------------------------------- */
/*  File generator                                                             */
/* -------------------------------------------------------------------------- */

// generate returns a formatted Go source file that implements all structs.
func (g *gen) generate(root *node) ([]byte, error) {
	g.buf.WriteString("// +kubebuilder:object:generate=true\n")
	g.buf.WriteString("// +kubebuilder:object:root=true\n")
	g.buf.WriteString("// +groupName=values.helm.io\n\n")
	g.buf.WriteString("// +versionName=v1alpha1\n\n")
	g.buf.WriteString("// Code generated by values-gen. DO NOT EDIT.\n")
	g.buf.WriteString("package " + g.pkg + "\n\n")

	g.writeStruct(root)

	// prepend imports if any were collected.
	if len(g.imp) > 0 {
		var imp bytes.Buffer
		imp.WriteString("import (\n")
		for _, k := range sortedKeys(g.imp) {
			imp.WriteString("    \"" + k + "\"\n")
		}
		imp.WriteString(")\n\n")

		head := "package " + g.pkg + "\n\n"
		src := g.buf.Bytes()
		src = []byte(strings.Replace(string(src), head, head+imp.String(), 1))
		return format.Source(src)
	}
	return format.Source(g.buf.Bytes())
}

/* -------------------------------------------------------------------------- */
/*  CLI helpers                                                                */
/* -------------------------------------------------------------------------- */

// sortedKeys returns the map keys in deterministic order.
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
/*  Entry point                                                                */
/* -------------------------------------------------------------------------- */

func init() {
	pflag.StringVarP(&inValues, "values", "v", "values.yaml", "annotated Helm values.yaml")
	pflag.StringVarP(&module, "module", "m", "values", "package name")
	pflag.StringVarP(&outGo, "debug-go", "g", "", "output *.go file")
	pflag.StringVarP(&outCRD, "debug-crd", "c", "", "output CRD YAML")
	pflag.StringVarP(&outSchema, "schema", "s", "", "output values.schema.json")
	pflag.StringVarP(&outReadme, "readme", "r", "", "update README.md Parameters section")
}

// main parses flags, drives the generator and writes requested artifacts.
func main() {
	pflag.Parse()

	if outGo == "" && outCRD == "" && outSchema == "" {
		fmt.Printf("no output specified: use -out-go, -out-crd or -out-schema\n")
		os.Exit(1)
	}

	rows, err := parse(inValues)
	if err != nil {
		fmt.Printf("parse: %v\n", err)
		os.Exit(1)
	}
	tree := build(rows)

	// Pull defaults directly from YAML.
	yamlRaw, _ := os.ReadFile(inValues)
	var yamlRoot map[string]interface{}
	_ = sigyaml.Unmarshal(yamlRaw, &yamlRoot)
	populateDefaults(tree, yamlRoot, tree.child)

	if undef := collectUndefined(tree); len(undef) > 0 {
		fmt.Printf("unknown (empty) types: %s\n", strings.Join(undef, ", "))
		os.Exit(1)
	}

	tmpdir, goFilePath, err := writeGeneratedGoAndStub(tree, module)
	if err != nil {
		fmt.Printf("write generated: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmpdir)

	if outGo != "" {
		code, _ := os.ReadFile(goFilePath)
		_ = os.MkdirAll(filepath.Dir(outGo), 0o755)
		_ = os.WriteFile(outGo, code, 0o644)
		fmt.Printf("write Go structs:\n", outGo)
	}

	var crdBytes []byte
	if outCRD != "" || outSchema != "" {
		crdBytes, err = cg(filepath.Dir(goFilePath))
		if err != nil {
			fmt.Printf("controller-gen: %v\n", err)
			os.Exit(1)
		}
	}

	if outCRD != "" {
		_ = os.MkdirAll(filepath.Dir(outCRD), 0o755)
		_ = os.WriteFile(outCRD, crdBytes, 0o644)
		fmt.Printf("write CRD resource: %s\n", outCRD)
	}
	if outSchema != "" {
		if err := writeValuesSchema(crdBytes, outSchema); err != nil {
			fmt.Printf("values schema: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("write JSON schema: %s\n", outSchema)
	}
	if outReadme != "" {
		if err := readme.UpdateParametersSection(inValues, outReadme); err != nil {
			fmt.Printf("README: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("update README parameters: %s\n", outReadme)
	}
}

/* -------------------------------------------------------------------------- */
/*  Temporary project scaffolding                                              */
/* -------------------------------------------------------------------------- */

// writeGeneratedGoAndStub creates a temp module with generated code
// plus a minimal metav1 stub so that controller-gen can compile it.
func writeGeneratedGoAndStub(root *node, module string) (tmpdir, goFilePath string, err error) {
	tmpdir, err = os.MkdirTemp("", "values-gen-*")
	if err != nil {
		return "", "", err
	}

	// 1. go.mod for the fake root module.
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

	// 2. package directory.
	pkgDir := filepath.Join(tmpdir, module)
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		return "", "", err
	}

	// 3. generate Go code.
	g := &gen{pkg: module}
	code, err := g.generate(root)
	if err != nil {
		return "", "", err
	}
	goFilePath = filepath.Join(pkgDir, "values_generated.go")
	if err := os.WriteFile(goFilePath, code, 0o644); err != nil {
		return "", "", err
	}

	// 4. stub for k8s.io/apimachinery/pkg/apis/meta/v1.
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

// cg runs controller-gen over a single package directory and returns CRD YAML.
func cg(pkgDir string) ([]byte, error) {
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
		crdRaw.ObjectMeta.Annotations["controller-gen.kubebuilder.io/version"] =
			version.Version
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

// writeValuesSchema extracts the “spec” sub-schema from a CRD and writes
// a Helm-compatible values.schema.json file.
func writeValuesSchema(crdBytes []byte, outPath string) error {
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

// populateDefaults walks the YAML map and copies scalar defaults
// into the corresponding tree nodes.
func populateDefaults(n *node, y interface{}, aliases map[string]*node) {
	switch v := y.(type) {
	case map[string]interface{}:
		for k, yval := range v {
			child, ok := n.child[k]
			if !ok {
				continue
			}

			isScalar := func(x interface{}) bool {
				switch x.(type) {
				case string, int, int64, float64, bool:
					return true
				default:
					return false
				}
			}

			if isScalar(yval) && child.defaultVal == "" {
				child.defaultVal = fmt.Sprintf("%v", yval)
				continue
			}

			if m, ok := yval.(map[string]interface{}); ok {
				if len(child.child) > 0 {
					populateDefaults(child, m, aliases)
					continue
				}
				te := strings.TrimPrefix(child.typeExpr, "*")
				if alias, ok := aliases[te]; ok {
					populateDefaults(alias, m, aliases)
				}
			}
		}
	}
}

// formatDefault converts a raw default string into a kubebuilder-ready literal.
func formatDefault(val, typ string) string {
	t := strings.TrimPrefix(typ, "*")
	switch {
	case t == "string":
		return fmt.Sprintf("%q", val)
	case strings.HasPrefix(t, "[]"), strings.HasPrefix(t, "map["):
		return "" // no defaults for composite types
	default:
		return val
	}
}

// collectUndefined returns all implicitly-created types that never
// received any @param/@field definitions.
func collectUndefined(root *node) []string {
	var bad []string
	for name, n := range root.child {
		if isPrimitive(strings.TrimPrefix(name, "*")) ||
			strings.HasPrefix(name, "[]") ||
			strings.HasPrefix(name, "map[") {
			continue
		}
		if !n.isParam && len(n.child) == 0 {
			bad = append(bad, name)
		}
	}
	sort.Strings(bad)
	return bad
}
