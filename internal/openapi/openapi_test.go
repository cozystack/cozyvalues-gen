package openapi

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const monitoringYAML = `
## @param host {string} The hostname used to access the grafana externally
host: ""
`

const monitoringSchema = `{
  "title": "Chart Values",
  "type": "object",
  "properties": {
    "host": {
      "description": "The hostname used to access the grafana externally",
      "type": "string"
    }
  }
}`

func writeTempFile(content string) string {
	f, err := os.CreateTemp("", "values-*.yaml")
	if err != nil {
		panic(err)
	}
	_, err = f.WriteString(content)
	if err != nil {
		panic(err)
	}
	if err := f.Close(); err != nil {
		panic(err)
	}
	return f.Name()
}

func TestEndToEndSchemaGeneration(t *testing.T) {
	rows, err := Parse(writeTempFile(monitoringYAML))
	require.NoError(t, err)
	root := Build(rows)
	var parsed interface{}
	require.NoError(t, yaml.Unmarshal([]byte(monitoringYAML), &parsed))
	aliases := map[string]*Node{}
	for _, n := range root.Child {
		aliases[n.Name] = n
	}
	PopulateDefaults(root, parsed, aliases)
	tmp, goFile, err := WriteGeneratedGoAndStub(root, "values")
	require.NoError(t, err)
	defer os.RemoveAll(tmp)
	crdBytes, err := CG(filepath.Dir(goFile))
	require.NoError(t, err)
	outPath := filepath.Join(tmp, "values.schema.json")
	require.NoError(t, WriteValuesSchema(crdBytes, outPath))
	got, err := os.ReadFile(outPath)
	require.NoError(t, err)
	var gotObj, wantObj interface{}
	require.NoError(t, json.Unmarshal(got, &gotObj))
	require.NoError(t, json.Unmarshal([]byte(monitoringSchema), &wantObj))
	require.Equal(t, wantObj, gotObj)
}

func TestDefaultsPropagation(t *testing.T) {
	yamlData := `
parent:
  child:
    field: value
`
	rows := []Raw{
		{K: kParam, Path: []string{"parent"}, TypeExpr: "parent"},
		{K: kField, Path: []string{"parent", "child"}, TypeExpr: "child"},
		{K: kField, Path: []string{"child", "field"}, TypeExpr: "string"},
	}
	root := Build(rows)
	var parsed interface{}
	require.NoError(t, yaml.Unmarshal([]byte(yamlData), &parsed))
	aliases := map[string]*Node{"parent": root.Child["parent"], "child": root.Child["child"]}
	PopulateDefaults(root, parsed, aliases)
	require.Equal(t, "field: value\n", root.Child["parent"].Child["child"].DefaultVal)
}

func TestCollectUndefined(t *testing.T) {
	rows := []Raw{
		{K: kParam, Path: []string{"a"}, TypeExpr: "customType"},
	}
	root := Build(rows)
	undef := CollectUndefined(root)
	require.Contains(t, undef, "customType")
}

func TestFormatDefault(t *testing.T) {
	require.Equal(t, `"str"`, formatDefault("str", "string"))
	require.Equal(t, "{1,2}", formatDefault("[1,2]", "[]int"))
	require.Equal(t, `{"a":1}`, formatDefault("{a: 1}", "map[string]int"))
}

func TestCamelCaseConversion(t *testing.T) {
	require.Equal(t, "CamelCase", camel("camel_case"))
	require.Equal(t, "FieldName", camel("field-name"))
}

func TestArrayWithDefaultsInOpenAPI(t *testing.T) {
	yamlContent := `
## @param metricsStorages {[]metricsStorage} Metrics storage
## @field metricsStorage.name {string default="5m"} Name
## @field metricsStorage.retentionPeriod {string} Retention
metricsStorages:
- name: shortterm
  retentionPeriod: "3d"
- name: longterm
  retentionPeriod: "14d"
`
	tmpfile := writeTempFile(yamlContent)
	rows, err := Parse(tmpfile)
	require.NoError(t, err)
	root := Build(rows)
	tmpdir, gofile, err := WriteGeneratedGoAndStub(root, "values")
	require.NoError(t, err)
	defer os.RemoveAll(tmpdir)
	crdBytes, err := CG(filepath.Dir(gofile))
	require.NoError(t, err)
	outfile := filepath.Join(tmpdir, "schema.json")
	err = WriteValuesSchema(crdBytes, outfile)
	require.NoError(t, err)
	schemaData, err := os.ReadFile(outfile)
	require.NoError(t, err)
	if !strings.Contains(string(schemaData), `"default": "5m"`) {
		t.Errorf("expected default 5m for metricsStorages.retentionPeriod, got:\n%s", string(schemaData))
	}
	if !strings.Contains(string(schemaData), `"metricsStorages"`) {
		t.Errorf("expected metricsStorages field in schema, got:\n%s", string(schemaData))
	}
}

func TestComplexNestedTypes(t *testing.T) {
	yamlContent := `
## @field emptyDir.placeholder {string}  # declare alias, no real fields
## @param metricsStorages {[]metricsStorage} Metrics storage
## @field metricsStorage.name {string} Name
## @field metricsStorage.retentionPeriod {string default="5m"} Retention
metricsStorages:
- name: shortterm
  retentionPeriod: "3d"
- name: longterm
  retentionPeriod: "14d"

## @param foo {bar}
## @field bar.emptyDir {emptyDir} Empty directory configuration
foo:
  emptyDir: {}
`
	tmpfile := writeTempFile(yamlContent)
	rows, err := Parse(tmpfile)
	require.NoError(t, err)
	root := Build(rows)
	tmpdir, gofile, err := WriteGeneratedGoAndStub(root, "values")
	require.NoError(t, err)
	defer os.RemoveAll(tmpdir)
	crdBytes, err := CG(filepath.Dir(gofile))
	require.NoError(t, err)
	outfile := filepath.Join(tmpdir, "schema.json")
	err = WriteValuesSchema(crdBytes, outfile)
	require.NoError(t, err)
	schemaData, err := os.ReadFile(outfile)
	require.NoError(t, err)
	if !strings.Contains(string(schemaData), `"metricsStorages"`) {
		t.Errorf("expected metricsStorages field in schema, got:\n%s", string(schemaData))
	}
	if !strings.Contains(string(schemaData), `"foo"`) {
		t.Errorf("expected foo field in schema, got:\n%s", string(schemaData))
	}
}

func extractStructs(code string) map[string]int {
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, "", code, parser.AllErrors)
	if err != nil {
		panic(err)
	}
	structs := make(map[string]int)
	ast.Inspect(node, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return true
		}
		fields := 0
		if st.Fields != nil {
			fields = len(st.Fields.List)
		}
		structs[ts.Name.Name] = fields
		return true
	})
	return structs
}

func extractTypeRefs(code string) map[string]bool {
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, "", code, parser.AllErrors)
	if err != nil {
		panic(err)
	}
	refs := make(map[string]bool)
	ast.Inspect(node, func(n ast.Node) bool {
		ft, ok := n.(*ast.Field)
		if !ok {
			return true
		}
		switch t := ft.Type.(type) {
		case *ast.Ident:
			refs[t.Name] = true
		case *ast.StarExpr:
			if id, ok := t.X.(*ast.Ident); ok {
				refs[id.Name] = true
			}
		case *ast.ArrayType:
			if id, ok := t.Elt.(*ast.Ident); ok {
				refs[id.Name] = true
			}
		case *ast.MapType:
			if id, ok := t.Value.(*ast.Ident); ok {
				refs[id.Name] = true
			}
		}
		return true
	})
	return refs
}

func TestNoUnusedOrMissingStructs(t *testing.T) {
	yamlContent := `
## @param metricsStorages {[]metricsStorage} Metrics storage
## @field metricsStorage.name {string} Name
## @field metricsStorage.retentionPeriod {string} Retention
metricsStorages:
- name: shortterm
  retentionPeriod: "3d"
`
	rows, err := Parse(writeTempFile(yamlContent))
	require.NoError(t, err)
	root := Build(rows)
	g := &gen{pkg: "values"}
	formatted, _, err := g.Generate(root)
	require.NoError(t, err)
	code := string(formatted)
	structs := extractStructs(code)
	refs := extractTypeRefs(code)
	for name, fields := range structs {
		if name == "Config" || name == "ConfigSpec" || name == "quantity" {
			continue
		}
		_, isRef := refs[name]
		if fields == 0 && !isRef {
			t.Errorf("unused empty struct %s found", name)
		}
		if fields > 0 && !isRef && !strings.HasSuffix(name, "Spec") {
			t.Errorf("struct %s is defined but not referenced", name)
		}
	}
}

/* -------------------------------------------------------------------------- */
/*  helpers                                                                    */
/* -------------------------------------------------------------------------- */

/* -------------------------------------------------------------------------- */
/*  table-driven check for isStringFormat                                      */
/* -------------------------------------------------------------------------- */

func TestIsStringFormat(t *testing.T) {
	for _, f := range stringFormats {
		require.Truef(t, isStringFormat(f), "expected %s to be recognised", f)
	}
	require.False(t, isStringFormat("not_a_format"))
}

/* -------------------------------------------------------------------------- */
/*  +kubebuilder:validation:Format injected into generated Go code            */
/* -------------------------------------------------------------------------- */

func TestEmitFieldAddsFormatAnnotation(t *testing.T) {
	const valuesYAML = `
## @param serverHost {hostname} public host
serverHost: "grafana.example.com"
`
	rows, err := Parse(writeTempFile(valuesYAML))
	require.NoError(t, err)
	root := Build(rows)

	g := &gen{pkg: "values"}
	formatted, _, err := g.Generate(root)
	require.NoError(t, err)
	code := string(formatted)

	require.Contains(t, code, "// +kubebuilder:validation:Format=hostname",
		"validation format annotation not found in generated code")
}

/* -------------------------------------------------------------------------- */
/*  resulting JSON-Schema contains “format”                                   */
/* -------------------------------------------------------------------------- */

func TestSchemaContainsStringFormat(t *testing.T) {
	const yamlContent = `
## @param apiURL {uri} External URL
apiURL: ""
`
	tmpValues := writeTempFile(yamlContent)
	rows, _ := Parse(tmpValues)
	root := Build(rows)

	// generate stub project & CRD → schema
	tmpDir, goFile, err := WriteGeneratedGoAndStub(root, "values")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	crdBytes, err := CG(filepath.Dir(goFile))
	require.NoError(t, err)

	outSchema := filepath.Join(tmpDir, "values.schema.json")
	require.NoError(t, WriteValuesSchema(crdBytes, outSchema))

	raw, err := os.ReadFile(outSchema)
	require.NoError(t, err)

	var schema map[string]any
	require.NoError(t, json.Unmarshal(raw, &schema))

	props := schema["properties"].(map[string]any)
	apiURL := props["apiURL"].(map[string]any)

	require.Equal(t, "string", apiURL["type"])
	require.Equal(t, "uri", apiURL["format"])
}

/* -------------------------------------------------------------------------- */
/*  resolve() special aliases                                                 */
/* -------------------------------------------------------------------------- */

func TestResolveSpecialAliases(t *testing.T) {
	g := &gen{pkg: "values"}

	require.Equal(t, "metav1.Duration", g.resolve("duration"))
	require.Equal(t, "resource.Quantity", g.resolve("quantity"))
	require.Equal(t, "metav1.Time", g.resolve("time"))

	// ensure imports were recorded
	require.Contains(t, g.imp, "k8s.io/apimachinery/pkg/apis/meta/v1")
	require.Contains(t, g.imp, "k8s.io/apimachinery/pkg/api/resource")
}

/* -------------------------------------------------------------------------- */
/*  CollectUndefined ignores recognised formats                               */
/* -------------------------------------------------------------------------- */

func TestCollectUndefinedWithFormats(t *testing.T) {
	rows := []Raw{
		{K: kParam, Path: []string{"email"}, TypeExpr: "email"},
		{K: kParam, Path: []string{"host"}, TypeExpr: "hostname"},
	}
	root := Build(rows)
	require.Empty(t, CollectUndefined(root))
}

func TestNoAliasStructsGenerated(t *testing.T) {
	const valuesYAML = `
## @param size {quantity} disk size
size: "4Gi"
## @param ttl {duration} ttl for job
ttl: "5m"
`

	rows, err := Parse(writeTempFile(valuesYAML))
	require.NoError(t, err)
	root := Build(rows)

	g := &gen{pkg: "values"}
	formatted, _, err := g.Generate(root)
	require.NoError(t, err)

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "", formatted, 0)
	require.NoError(t, err)

	found := make(map[string]bool)
	ast.Inspect(file, func(n ast.Node) bool {
		if ts, ok := n.(*ast.TypeSpec); ok {
			if _, ok := ts.Type.(*ast.StructType); ok {
				found[ts.Name.Name] = true
			}
		}
		return true
	})

	// Ensure alias structs are absent
	require.False(t, found["Quantity"], "unexpected empty struct Quantity")
	require.False(t, found["Duration"], "unexpected empty struct Duration")
}

func TestAliasFieldResolution(t *testing.T) {
	const yamlContent = `
## @param foaao {asdaa}
## @field foaao.foaa {int64}
foaao:
  aaa: 1
`
	rows, err := Parse(writeTempFile(yamlContent))
	require.NoError(t, err)
	root := Build(rows)

	g := &gen{pkg: "values"}
	code, _, err := g.Generate(root)
	require.NoError(t, err)

	structs := extractStructs(string(code))
	require.Contains(t, structs, "Asdaa", "alias struct missing")
	require.Equal(t, 1, structs["Asdaa"], "Asdaa should have one field")
	require.Contains(t, extractTypeRefs(string(code)), "Asdaa",
		"ConfigSpec must reference Asdaa")
}

func TestObjectAliases(t *testing.T) {
	const yaml = `
## @field emptyobject.placeholder {string}  # declare alias, no real fields
## @param rawData {object} arbitrary JSON
rawData: {}

## @param cfg {emptyobject} nothing inside
cfg: {}
`
	rows, _ := Parse(writeTempFile(yaml))
	root := Build(rows)

	g := &gen{pkg: "values"}
	src, _, err := g.Generate(root)
	require.NoError(t, err)
	code := string(src)

	// Emptyobject → named struct
	require.Contains(t, code, "type Emptyobject struct {")
	require.Contains(t, code, "Emptyobject `json:\"cfg\"`")

	// object → k8sRuntime.RawExtension
	require.Contains(t, code, "k8sRuntime.RawExtension `json:\"rawData\"`",
		"object alias not resolved")

	// ----- build CRD & JSON-schema ----------------------------------

	tmpDir, goFile, err := WriteGeneratedGoAndStub(root, "values")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	crdBytes, err := CG(filepath.Dir(goFile))
	require.NoError(t, err)

	schemaPath := filepath.Join(tmpDir, "values.schema.json")
	require.NoError(t, WriteValuesSchema(crdBytes, schemaPath))

	raw, err := os.ReadFile(schemaPath)
	require.NoError(t, err)
	var schema map[string]any
	require.NoError(t, json.Unmarshal(raw, &schema))

	// .properties.rawData.x-kubernetes-preserve-unknown-fields must be true
	props := schema["properties"].(map[string]any)
	rawData := props["rawData"].(map[string]any)
	require.Equal(t, true, rawData["x-kubernetes-preserve-unknown-fields"],
		"`object` alias should preserve unknown fields")
}

func TestUndefinedTypeReference(t *testing.T) {
	const yaml = `
## @param foo {Bar} reference to undeclared type
foo: {}
`
	rows, _ := Parse(writeTempFile(yaml))
	root := Build(rows)

	_, _, err := (&gen{pkg: "values"}).Generate(root)
	require.Error(t, err)
	require.Contains(t, err.Error(), "Bar")
}

func TestDefinedTypeNoError(t *testing.T) {
	const yaml = `
## @param foo {Bar}
foo: {}
## @field Bar.baz {string}
`
	rows, err := Parse(writeTempFile(yaml))
	require.NoError(t, err)
	root := Build(rows)

	g := &gen{pkg: "values"}
	_, _, err = g.Generate(root)

	require.NoError(t, err, "type Bar is declared, should not error")
}

func TestUnknownComplexTypesInFieldAreRejected(t *testing.T) {
	const yaml = `
## @param config {config}
config:
  ## @field config.merge {*merge}
  merge: {}
  ## @field config.resolver {resolver}
  resolver: {}
`
	tmp := writeTempFile(yaml)
	defer os.Remove(tmp)

	rows, err := Parse(tmp)
	require.NoError(t, err)
	root := Build(rows)

	_, _, err = (&gen{pkg: "values"}).Generate(root)
	require.Error(t, err)
	require.Contains(t, err.Error(), "merge")
	require.Contains(t, err.Error(), "resolver")
}

func TestObjectFieldsAreAllowedFreeFormInOpenAPI(t *testing.T) {
	const yaml = `
## @param config {config}
config:
  ## @field config.merge {object}
  merge: {}
  ## @field config.resolver {object}
  resolver: {}
`
	tmp := writeTempFile(yaml)
	defer os.Remove(tmp)

	rows, err := Parse(tmp)
	require.NoError(t, err)
	root := Build(rows)

	tmpDir, goFile, err := WriteGeneratedGoAndStub(root, "values")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	crdBytes, err := CG(filepath.Dir(goFile))
	require.NoError(t, err)

	outSchema := filepath.Join(tmpDir, "values.schema.json")
	require.NoError(t, WriteValuesSchema(crdBytes, outSchema))

	raw, err := os.ReadFile(outSchema)
	require.NoError(t, err)

	var schema map[string]any
	require.NoError(t, json.Unmarshal(raw, &schema))

	props := schema["properties"].(map[string]any)
	cfg, ok := props["config"].(map[string]any)
	require.True(t, ok, "config property missing in schema")
	cfgProps, ok := cfg["properties"].(map[string]any)
	require.True(t, ok, "config.properties missing in schema")

	for _, k := range []string{"merge", "resolver"} {
		sub, ok := cfgProps[k].(map[string]any)
		require.Truef(t, ok, "%s not found under config", k)
		require.Equal(t, "object", sub["type"])
		require.Equal(t, true, sub["x-kubernetes-preserve-unknown-fields"])
	}
}

func TestMapStringUnknownValueTypeIsRejected(t *testing.T) {
	const yaml = `
## @param labels {map[string]label}
labels:
  app:
    key: value
`
	tmp := writeTempFile(yaml)
	defer os.Remove(tmp)

	rows, err := Parse(tmp)
	require.NoError(t, err)
	root := Build(rows)

	_, _, err = (&gen{pkg: "values"}).Generate(root)
	require.Error(t, err)
	require.Contains(t, err.Error(), "label")
}

func TestMapStringObjectIsAllowed(t *testing.T) {
	const yaml = `
## @param labels {map[string]object}
labels:
  app:
    tier: web
`
	tmp := writeTempFile(yaml)
	defer os.Remove(tmp)

	rows, err := Parse(tmp)
	require.NoError(t, err)
	root := Build(rows)

	tmpDir, goFile, err := WriteGeneratedGoAndStub(root, "values")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	crdBytes, err := CG(filepath.Dir(goFile))
	require.NoError(t, err)

	outSchema := filepath.Join(tmpDir, "values.schema.json")
	require.NoError(t, WriteValuesSchema(crdBytes, outSchema))

	raw, err := os.ReadFile(outSchema)
	require.NoError(t, err)

	var schema map[string]any
	require.NoError(t, json.Unmarshal(raw, &schema))

	props := schema["properties"].(map[string]any)
	lbls := props["labels"].(map[string]any)
	require.Equal(t, "object", lbls["type"])
	addProps := lbls["additionalProperties"].(map[string]any)
	require.Equal(t, true, addProps["x-kubernetes-preserve-unknown-fields"])
}

func TestUserTypeNamedConfigDoesNotClashAndMapStringObjectStillWorks(t *testing.T) {
	const yaml = `
## @param config {config}
config:
  ## @field config.merge {object}
  merge: {}
  ## @field config.resolver {object}
  resolver: {}

## @param labels {map[string]object}
labels:
  app:
    tier: web
`

	tmp := writeTempFile(yaml)
	defer os.Remove(tmp)

	rows, err := Parse(tmp)
	require.NoError(t, err)
	root := Build(rows)

	// Generate code should not redefine kind "Config"
	src, _, err := (&gen{pkg: "values"}).Generate(root)
	require.NoError(t, err)
	code := string(src)
	require.Contains(t, code, "type ValuesConfig struct {", "user type 'config' must be sanitized to ValuesConfig")
	require.Contains(t, code, "Config ValuesConfig", "ConfigSpec should have field named Config of type ValuesConfig")
	require.Contains(t, code, "`json:\"config\"`", "ConfigSpec.Config should have json tag \"config\"")

	// Build CRD → JSON schema
	tmpDir, goFile, err := WriteGeneratedGoAndStub(root, "values")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	crdBytes, err := CG(filepath.Dir(goFile))
	require.NoError(t, err)

	outSchema := filepath.Join(tmpDir, "values.schema.json")
	require.NoError(t, WriteValuesSchema(crdBytes, outSchema))

	raw, err := os.ReadFile(outSchema)
	require.NoError(t, err)

	var schema map[string]any
	require.NoError(t, json.Unmarshal(raw, &schema))
	props := schema["properties"].(map[string]any)

	// config is present with free-form subfields
	cfg, ok := props["config"].(map[string]any)
	require.True(t, ok, "config property missing")
	cfgProps, ok := cfg["properties"].(map[string]any)
	require.True(t, ok, "config.properties missing")
	for _, k := range []string{"merge", "resolver"} {
		sub, ok := cfgProps[k].(map[string]any)
		require.Truef(t, ok, "%s missing under config", k)
		require.Equal(t, "object", sub["type"])
		require.Equal(t, true, sub["x-kubernetes-preserve-unknown-fields"])
	}

	// labels is map[string]object and should preserve unknown fields
	lbls, ok := props["labels"].(map[string]any)
	require.True(t, ok, "labels property missing")
	require.Equal(t, "object", lbls["type"])
	addProps, ok := lbls["additionalProperties"].(map[string]any)
	require.True(t, ok, "labels.additionalProperties missing")
	require.Equal(t, true, addProps["x-kubernetes-preserve-unknown-fields"])
}

func TestPointerSliceElementTypeIsCamelled(t *testing.T) {
	const yamlContent = `
## @param nodeGroups {map[string]node} Worker nodes configuration
## @field node.minReplicas {int} Minimum amount of replicas
## @field node.maxReplicas {int} Maximum amount of replicas
## @field node.instanceType {string} Virtual machine instance type
## @field node.ephemeralStorage {quantity} Ephemeral storage size
## @field node.roles {[]string} List of node's roles
## @field node.resources {resources} Resources available to each worker node
## @field resources.cpu {*quantity} CPU available to each worker node
## @field resources.memory {*quantity} Memory (RAM) available to each worker node
## @field node.gpus {*[]gpu} List of GPUs to attach
## @field gpu.name {string} Name of GPU
nodeGroups:
  md0:
    minReplicas: 0
    maxReplicas: 10
    instanceType: "u1.medium"
    ephemeralStorage: 20Gi
    roles:
    - ingress-nginx
    resources: {}
    gpus: []
`
	rows, err := Parse(writeTempFile(yamlContent))
	require.NoError(t, err)
	root := Build(rows)

	g := &gen{pkg: "values"}
	formatted, _, err := g.Generate(root)
	require.NoError(t, err)
	code := string(formatted)

	require.Contains(t, code, "type Gpu struct {", "Gpu struct must be generated with camel-cased name")
	require.Contains(t, code, "Gpus *[]Gpu `json:\"gpus,omitempty\"`", "field type must be *[]Gpu, not *[]gpu")
}

func TestAnnotationDefaultRawJSON(t *testing.T) {
	const yamlContent = `
## @param nodeGroups {map[string]node} Worker nodes configuration
## @field node.minReplicas {int default=0}
## @field node.maxReplicas {int default=10}
## @field node.instanceType {string default="u1.medium"}
## @field node.ephemeralStorage {quantity default="20Gi"}
## @field node.roles {[]string default={}}
## @field node.resources {resources default={}}
## @field node.gpus {[]gpu default={"name":"nvidia.com/AD102GL_L40S"}}
## @field gpu.name {string}
nodeGroups: {}
`
	tmpfile := writeTempFile(yamlContent)
	rows, err := Parse(tmpfile)
	require.NoError(t, err)
	root := Build(rows)

	tmpdir, gofile, err := WriteGeneratedGoAndStub(root, "values")
	require.NoError(t, err)
	defer os.RemoveAll(tmpdir)

	crdBytes, err := CG(filepath.Dir(gofile))
	require.NoError(t, err)

	outfile := filepath.Join(tmpdir, "schema.json")
	require.NoError(t, WriteValuesSchema(crdBytes, outfile))

	raw, err := os.ReadFile(outfile)
	require.NoError(t, err)

	var js map[string]interface{}
	require.NoError(t, json.Unmarshal(raw, &js))
	compact, err := json.Marshal(js)
	require.NoError(t, err)
	schema := string(compact)

	require.Contains(t, schema, `"default":"u1.medium"`)
	require.Contains(t, schema, `"default":"20Gi"`)
	require.Contains(t, schema, `"default":{}`)
	require.Contains(t, schema, `"default":[]`)
	require.Contains(t, schema, `"default":{"name":"nvidia.com/AD102GL_L40S"}`)
}

func TestAnnotationDefaultEmptyObject(t *testing.T) {
	const yamlContent = `
## @param backup {backup} Backup configuration
## @field backup.settings {object default={}} Freeform settings
backup: {}
`
	tmpfile := writeTempFile(yamlContent)
	rows, err := Parse(tmpfile)
	require.NoError(t, err)
	root := Build(rows)

	tmpdir, gofile, err := WriteGeneratedGoAndStub(root, "values")
	require.NoError(t, err)
	defer os.RemoveAll(tmpdir)

	crdBytes, err := CG(filepath.Dir(gofile))
	require.NoError(t, err)

	outfile := filepath.Join(tmpdir, "schema.json")
	require.NoError(t, WriteValuesSchema(crdBytes, outfile))

	raw, err := os.ReadFile(outfile)
	require.NoError(t, err)
	schema := string(raw)

	require.Contains(t, schema, `"settings"`)
	require.Contains(t, schema, `"default": {}`, "expected explicit default={} to be preserved in schema")
}

func TestResourcesSchemaKeepsCpuAndMemory(t *testing.T) {
	const yaml = `
## @param controlPlane {controlPlane}
controlPlane:
  apiServer:
    resources: {}

## @field controlPlane.apiServer {apiServer}
## @field apiServer.resources {resources}

## @field resources.cpu {*quantity} CPU available
## @field resources.memory {*quantity} Memory available
`
	tmp := writeTempFile(yaml)
	defer os.Remove(tmp)

	rows, err := Parse(tmp)
	require.NoError(t, err)

	root := Build(rows)
	tmpDir, goFile, err := WriteGeneratedGoAndStub(root, "values")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	crdBytes, err := CG(filepath.Dir(goFile))
	require.NoError(t, err)

	outSchema := filepath.Join(tmpDir, "values.schema.json")
	require.NoError(t, WriteValuesSchema(crdBytes, outSchema))

	raw, err := os.ReadFile(outSchema)
	require.NoError(t, err)

	var schema map[string]any
	require.NoError(t, json.Unmarshal(raw, &schema))

	props := schema["properties"].(map[string]any)
	cp := props["controlPlane"].(map[string]any)
	cpProps := cp["properties"].(map[string]any)
	api := cpProps["apiServer"].(map[string]any)
	apiProps := api["properties"].(map[string]any)
	res := apiProps["resources"].(map[string]any)

	require.Equal(t, "object", res["type"])
	require.NotContains(t, res, "x-kubernetes-preserve-unknown-fields",
		"resources must be a structured object, not free-form")

	rp := res["properties"].(map[string]any)
	cpu := rp["cpu"].(map[string]any)
	mem := rp["memory"].(map[string]any)

	require.Equal(t, true, cpu["x-kubernetes-int-or-string"])
	require.Equal(t, true, mem["x-kubernetes-int-or-string"])
}
