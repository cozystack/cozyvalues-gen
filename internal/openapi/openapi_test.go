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
## @param {string} host - The hostname used to access the grafana externally
host: ""
`

const monitoringSchema = `{
  "title": "Chart Values",
  "type": "object",
  "properties": {
    "host": {
      "description": "The hostname used to access the grafana externally",
      "type": "string",
      "default": ""
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
		{K: kTypedef, Path: []string{"Child"}, TypeExpr: "struct", Description: ""},
		{K: kField, Path: []string{"Child", "field"}, TypeExpr: "string", Description: ""},
		{K: kTypedef, Path: []string{"Parent"}, TypeExpr: "struct", Description: ""},
		{K: kField, Path: []string{"Parent", "child"}, TypeExpr: "Child", Description: ""},
		{K: kParam, Path: []string{"parent"}, TypeExpr: "Parent", Description: ""},
	}
	root := Build(rows)
	var parsed interface{}
	require.NoError(t, yaml.Unmarshal([]byte(yamlData), &parsed))
	aliases := map[string]*Node{"Parent": root.Child["Parent"], "Child": root.Child["Child"]}
	PopulateDefaults(root, parsed, aliases)

	// object field default collapsed to {}
	require.Equal(t, "{}", root.Child["parent"].DefaultVal)

	// nested field default propagated to the alias type
	require.Contains(t, root.Child["Child"].Child, "field")
	require.Equal(t, "value", root.Child["Child"].Child["field"].DefaultVal)
}

func TestCollectUndefined(t *testing.T) {
	rows := []Raw{
		{K: kParam, Path: []string{"a"}, TypeExpr: "customType", Description: ""},
	}
	root := Build(rows)
	undef := CollectUndefined(root)
	require.Contains(t, undef, "customType")
}

func TestCollectUndefinedWithEmptyStruct(t *testing.T) {
	rows := []Raw{
		{K: kTypedef, Path: []string{"UploadLocal"}, TypeExpr: "struct", Description: ""},
		{K: kTypedef, Path: []string{"Source"}, TypeExpr: "struct", Description: ""},
		{K: kField, Path: []string{"Source", "upload"}, TypeExpr: "UploadLocal", Description: ""},
		{K: kParam, Path: []string{"source"}, TypeExpr: "Source", Description: ""},
	}
	root := Build(rows)
	undef := CollectUndefined(root)
	require.NotContains(t, undef, "UploadLocal", "empty struct UploadLocal should be considered defined")
	require.NotContains(t, undef, "Source", "struct Source should be considered defined")
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
## @typedef {struct} MetricsStorage - Metrics storage configuration
## @field {string} name="5m" - Name
## @field {string} retentionPeriod - Retention

## @param {[]MetricsStorage} metricsStorages - Metrics storage
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
## @typedef {struct} EmptyDir - Empty directory
## @field {string} placeholder - Placeholder

## @typedef {struct} MetricsStorage - Metrics storage configuration
## @field {string} name - Name
## @field {string} retentionPeriod="5m" - Retention

## @typedef {struct} Bar - Bar configuration
## @field {EmptyDir} emptyDir - Empty directory configuration

## @param {[]MetricsStorage} metricsStorages - Metrics storage
metricsStorages:
- name: shortterm
  retentionPeriod: "3d"
- name: longterm
  retentionPeriod: "14d"

## @param {Bar} foo
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
## @typedef {struct} MetricsStorage - Metrics storage
## @field {string} name - Name
## @field {string} retentionPeriod - Retention

## @param {[]MetricsStorage} metricsStorages - Metrics storage
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
## @param {hostname} serverHost - public host
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
## @param {uri} apiURL - External URL
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
		{K: kParam, Path: []string{"email"}, TypeExpr: "email", Description: ""},
		{K: kParam, Path: []string{"host"}, TypeExpr: "hostname", Description: ""},
	}
	root := Build(rows)
	require.Empty(t, CollectUndefined(root))
}

func TestNoAliasStructsGenerated(t *testing.T) {
	const valuesYAML = `
## @param {quantity} size - disk size
size: "4Gi"
## @param {duration} ttl - ttl for job
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
## @typedef {struct} Asdaa - Asdaa type
## @field {int64} foaa

## @param {Asdaa} foaao
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
## @typedef {struct} Emptyobject - Empty object type
## @field {string} placeholder

## @param {object} rawData - arbitrary JSON
rawData: {}

## @param {Emptyobject} cfg - nothing inside
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
## @param {Bar} foo - reference to undeclared type
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
## @typedef {struct} Bar - Bar type
## @field {string} baz

## @param {Bar} foo
foo: {}
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
## @typedef {struct} Config - Config type
## @field {*Merge} merge
## @field {Resolver} resolver

## @param {Config} config
config:
  merge: {}
  resolver: {}
`
	tmp := writeTempFile(yaml)
	defer os.Remove(tmp)

	rows, err := Parse(tmp)
	require.NoError(t, err)
	root := Build(rows)

	_, _, err = (&gen{pkg: "values"}).Generate(root)
	require.Error(t, err)
	require.Contains(t, err.Error(), "Merge")
	require.Contains(t, err.Error(), "Resolver")
}

func TestObjectFieldsAreAllowedFreeFormInOpenAPI(t *testing.T) {
	const yaml = `
## @typedef {struct} Config - Config type
## @field {object} merge
## @field {object} resolver

## @param {Config} config
config:
  merge: {}
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
## @param {map[string]Label} labels
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
	require.Contains(t, err.Error(), "Label")
}

func TestMapStringObjectIsAllowed(t *testing.T) {
	const yaml = `
## @param {map[string]object} labels
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
## @typedef {struct} Config - Config type
## @field {object} merge
## @field {object} resolver

## @param {Config} config
config:
  merge: {}
  resolver: {}

## @param {map[string]object} labels
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
## @typedef {struct} Gpu - GPU configuration
## @field {string} name - Name of GPU

## @typedef {struct} Resources - Resources configuration
## @field {*quantity} cpu - CPU available to each worker node
## @field {*quantity} memory - Memory (RAM) available to each worker node

## @typedef {struct} Node - Node configuration
## @field {int} minReplicas - Minimum amount of replicas
## @field {int} maxReplicas - Maximum amount of replicas
## @field {string} instanceType - Virtual machine instance type
## @field {quantity} ephemeralStorage - Ephemeral storage size
## @field {[]string} roles - List of node's roles
## @field {Resources} resources - Resources available to each worker node
## @field {*[]Gpu} gpus - List of GPUs to attach

## @param {map[string]Node} nodeGroups - Worker nodes configuration
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
## @typedef {struct} Gpu - GPU configuration
## @field {string} name

## @typedef {struct} Resources - Resources configuration
## @field {*quantity} cpu

## @typedef {struct} Node - Node configuration
## @field {int} minReplicas=0
## @field {int} maxReplicas=10
## @field {string} instanceType="u1.medium"
## @field {quantity} ephemeralStorage="20Gi"
## @field {[]string} roles={}
## @field {Resources} resources={}
## @field {[]Gpu} gpus={"name":"nvidia.com/AD102GL_L40S"}

## @param {map[string]Node} nodeGroups - Worker nodes configuration
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
## @typedef {struct} Backup - Backup configuration
## @field {object} settings={} - Freeform settings

## @param {Backup} backup - Backup configuration
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
## @typedef {struct} Resources - Resources configuration
## @field {*quantity} cpu - CPU available
## @field {*quantity} memory - Memory available

## @typedef {struct} ApiServer - API Server configuration
## @field {Resources} resources

## @typedef {struct} ControlPlane - Control Plane configuration
## @field {ApiServer} apiServer

## @param {ControlPlane} controlPlane
controlPlane:
  apiServer:
    resources: {}
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

/* -------------------------------------------------------------------------- */
/*  Dotted Path Support (Nested Structures for Umbrella Charts)               */
/* -------------------------------------------------------------------------- */

// TestParseDottedPath_SingleLevel verifies backward compatibility:
// a simple param without dots should produce a single-element Path.
func TestParseDottedPath_SingleLevel(t *testing.T) {
	const yaml = `
## @param {int} replicas - Number of replicas
replicas: 3
`
	rows, err := Parse(writeTempFile(yaml))
	require.NoError(t, err)
	require.Len(t, rows, 1)

	r := rows[0]
	require.Equal(t, kParam, r.K)
	require.Equal(t, []string{"replicas"}, r.Path, "single-level param must have one-element Path")
	require.Equal(t, "int", r.TypeExpr)
	require.Equal(t, "Number of replicas", r.Description)
}

// TestParseDottedPath_TwoLevels verifies parsing of two-level dotted path.
func TestParseDottedPath_TwoLevels(t *testing.T) {
	const yaml = `
## @param {int} qdrant.replicaCount - Number of Qdrant replicas
qdrant:
  replicaCount: 1
`
	rows, err := Parse(writeTempFile(yaml))
	require.NoError(t, err)
	require.Len(t, rows, 1)

	r := rows[0]
	require.Equal(t, kParam, r.K)
	require.Equal(t, []string{"qdrant", "replicaCount"}, r.Path,
		"two-level dotted path must split into two Path elements")
	require.Equal(t, "int", r.TypeExpr)
	require.Equal(t, "Number of Qdrant replicas", r.Description)
}

// TestParseDottedPath_ThreeLevels verifies parsing of three-level dotted path.
func TestParseDottedPath_ThreeLevels(t *testing.T) {
	const yaml = `
## @param {quantity} qdrant.persistence.size - Storage size
qdrant:
  persistence:
    size: 10Gi
`
	rows, err := Parse(writeTempFile(yaml))
	require.NoError(t, err)
	require.Len(t, rows, 1)

	r := rows[0]
	require.Equal(t, []string{"qdrant", "persistence", "size"}, r.Path,
		"three-level dotted path must split into three Path elements")
	require.Equal(t, "quantity", r.TypeExpr)
}

// TestParseDottedPath_DeepNesting verifies parsing of deeply nested paths (5 levels).
func TestParseDottedPath_DeepNesting(t *testing.T) {
	const yaml = `
## @param {string} a.b.c.d.e - Deep nested value
a:
  b:
    c:
      d:
        e: "deep"
`
	rows, err := Parse(writeTempFile(yaml))
	require.NoError(t, err)
	require.Len(t, rows, 1)

	r := rows[0]
	require.Equal(t, []string{"a", "b", "c", "d", "e"}, r.Path,
		"five-level dotted path must split into five Path elements")
}

// TestParseDottedPath_Optional verifies optional parameter with dotted path.
func TestParseDottedPath_Optional(t *testing.T) {
	const yaml = `
## @param {string} [qdrant.persistence.storageClassName] - StorageClass name
qdrant:
  persistence:
    storageClassName: ""
`
	rows, err := Parse(writeTempFile(yaml))
	require.NoError(t, err)
	require.Len(t, rows, 1)

	r := rows[0]
	require.Equal(t, []string{"qdrant", "persistence", "storageClassName"}, r.Path)
	require.True(t, r.OmitEmpty, "optional param with [] must have OmitEmpty=true")
}

// TestParseDottedPath_WithDefault verifies param with dotted path and default value.
func TestParseDottedPath_WithDefault(t *testing.T) {
	const yaml = `
## @param {int} qdrant.port=6333 - Qdrant port
qdrant:
  port: 6333
`
	rows, err := Parse(writeTempFile(yaml))
	require.NoError(t, err)
	require.Len(t, rows, 1)

	r := rows[0]
	require.Equal(t, []string{"qdrant", "port"}, r.Path)
	require.Equal(t, "6333", r.DefaultVal)
}

// TestParseDottedPath_OptionalWithDefault verifies optional param with default.
func TestParseDottedPath_OptionalWithDefault(t *testing.T) {
	const yaml = `
## @param {string} [qdrant.config.path]=/data - Config path
qdrant:
  config:
    path: /data
`
	rows, err := Parse(writeTempFile(yaml))
	require.NoError(t, err)
	require.Len(t, rows, 1)

	r := rows[0]
	require.Equal(t, []string{"qdrant", "config", "path"}, r.Path)
	require.True(t, r.OmitEmpty)
	require.Equal(t, "/data", r.DefaultVal)
}

// TestParseDottedPath_WithUnderscores verifies paths containing underscores.
func TestParseDottedPath_WithUnderscores(t *testing.T) {
	const yaml = `
## @param {string} my_app.my_config.my_value - Value with underscores
my_app:
  my_config:
    my_value: "test"
`
	rows, err := Parse(writeTempFile(yaml))
	require.NoError(t, err)
	require.Len(t, rows, 1)

	r := rows[0]
	require.Equal(t, []string{"my_app", "my_config", "my_value"}, r.Path,
		"underscores in path segments must be preserved")
}

// TestParseDottedPath_WithNumbers verifies paths containing numbers.
func TestParseDottedPath_WithNumbers(t *testing.T) {
	const yaml = `
## @param {int} v1.config2.port3 - Port
v1:
  config2:
    port3: 8080
`
	rows, err := Parse(writeTempFile(yaml))
	require.NoError(t, err)
	require.Len(t, rows, 1)

	r := rows[0]
	require.Equal(t, []string{"v1", "config2", "port3"}, r.Path,
		"numbers in path segments must be preserved")
}

// TestParseDottedPath_MultipleParams verifies multiple dotted path params in one file.
func TestParseDottedPath_MultipleParams(t *testing.T) {
	const yaml = `
## @param {int} qdrant.replicaCount - Replicas
## @param {quantity} qdrant.persistence.size - Storage size
## @param {string} [qdrant.persistence.storageClassName] - StorageClass
## @param {bool} qdrant.apiKey - Enable API key
qdrant:
  replicaCount: 1
  persistence:
    size: 10Gi
    storageClassName: ""
  apiKey: true
`
	rows, err := Parse(writeTempFile(yaml))
	require.NoError(t, err)
	require.Len(t, rows, 4)

	require.Equal(t, []string{"qdrant", "replicaCount"}, rows[0].Path)
	require.Equal(t, []string{"qdrant", "persistence", "size"}, rows[1].Path)
	require.Equal(t, []string{"qdrant", "persistence", "storageClassName"}, rows[2].Path)
	require.True(t, rows[2].OmitEmpty)
	require.Equal(t, []string{"qdrant", "apiKey"}, rows[3].Path)
}

/* -------------------------------------------------------------------------- */
/*  Build() with Dotted Paths                                                 */
/* -------------------------------------------------------------------------- */

// TestBuildDottedPath_CreatesIntermediateNodes verifies that Build creates
// intermediate struct nodes for dotted paths.
func TestBuildDottedPath_CreatesIntermediateNodes(t *testing.T) {
	rows := []Raw{
		{K: kParam, Path: []string{"qdrant", "persistence", "size"}, TypeExpr: "quantity", Description: "Storage size"},
	}

	root := Build(rows)

	// Root should have "qdrant" child
	require.Contains(t, root.Child, "qdrant", "root must have 'qdrant' child")
	qdrant := root.Child["qdrant"]
	require.Equal(t, "struct", qdrant.TypeExpr, "intermediate node must be struct")

	// Qdrant should have "persistence" child
	require.Contains(t, qdrant.Child, "persistence", "qdrant must have 'persistence' child")
	persistence := qdrant.Child["persistence"]
	require.Equal(t, "struct", persistence.TypeExpr, "intermediate node must be struct")

	// Persistence should have "size" child
	require.Contains(t, persistence.Child, "size", "persistence must have 'size' child")
	size := persistence.Child["size"]
	require.Equal(t, "quantity", size.TypeExpr, "leaf node must have specified type")
	require.True(t, size.IsParam, "leaf node must be marked as param")
	require.Equal(t, "Storage size", size.Comment)
}

// TestBuildDottedPath_SharedPrefix verifies that multiple params with shared
// prefix reuse intermediate nodes.
func TestBuildDottedPath_SharedPrefix(t *testing.T) {
	rows := []Raw{
		{K: kParam, Path: []string{"qdrant", "replicaCount"}, TypeExpr: "int", Description: "Replicas"},
		{K: kParam, Path: []string{"qdrant", "apiKey"}, TypeExpr: "bool", Description: "API key"},
	}

	root := Build(rows)

	// Only one "qdrant" node should exist
	require.Contains(t, root.Child, "qdrant")
	qdrant := root.Child["qdrant"]

	// Qdrant should have both children
	require.Len(t, qdrant.Child, 2, "qdrant must have exactly 2 children")
	require.Contains(t, qdrant.Child, "replicaCount")
	require.Contains(t, qdrant.Child, "apiKey")
}

// TestBuildDottedPath_DeeplySharedPrefix verifies reuse of deeply nested shared paths.
func TestBuildDottedPath_DeeplySharedPrefix(t *testing.T) {
	rows := []Raw{
		{K: kParam, Path: []string{"qdrant", "persistence", "size"}, TypeExpr: "quantity", Description: "Size"},
		{K: kParam, Path: []string{"qdrant", "persistence", "storageClassName"}, TypeExpr: "string", Description: "StorageClass"},
		{K: kParam, Path: []string{"qdrant", "replicaCount"}, TypeExpr: "int", Description: "Replicas"},
	}

	root := Build(rows)

	qdrant := root.Child["qdrant"]
	require.Len(t, qdrant.Child, 2, "qdrant must have 2 children: persistence and replicaCount")

	persistence := qdrant.Child["persistence"]
	require.Len(t, persistence.Child, 2, "persistence must have 2 children: size and storageClassName")
}

// TestBuildDottedPath_PreservesProperties verifies all param properties are set on leaf node.
func TestBuildDottedPath_PreservesProperties(t *testing.T) {
	rows := []Raw{
		{
			K:           kParam,
			Path:        []string{"app", "config", "value"},
			TypeExpr:    "string",
			Description: "Config value",
			OmitEmpty:   true,
			DefaultVal:  "default",
		},
	}

	root := Build(rows)
	leaf := root.Child["app"].Child["config"].Child["value"]

	require.Equal(t, "string", leaf.TypeExpr)
	require.Equal(t, "Config value", leaf.Comment)
	require.True(t, leaf.OmitEmpty)
	require.Equal(t, "default", leaf.DefaultVal)
	require.True(t, leaf.HasDefaultVal)
	require.True(t, leaf.IsParam)
}

/* -------------------------------------------------------------------------- */
/*  Dotted Paths with @typedef/@field (Mixed Usage)                           */
/* -------------------------------------------------------------------------- */

// TestBuildDottedPath_MixedWithTypedef verifies dotted paths work alongside @typedef.
func TestBuildDottedPath_MixedWithTypedef(t *testing.T) {
	rows := []Raw{
		// Traditional @typedef approach
		{K: kTypedef, Path: []string{"Persistence"}, TypeExpr: "struct", Description: "Storage config"},
		{K: kField, Path: []string{"Persistence", "size"}, TypeExpr: "quantity", Description: "Size"},
		// Dotted path approach
		{K: kParam, Path: []string{"qdrant", "persistence", "storageClassName"}, TypeExpr: "string", Description: "StorageClass"},
		// Using typedef as type
		{K: kParam, Path: []string{"redis", "persistence"}, TypeExpr: "Persistence", Description: "Redis storage"},
	}

	root := Build(rows)

	// Typedef should create its own node
	require.Contains(t, root.Child, "Persistence")
	require.Contains(t, root.Child["Persistence"].Child, "size")

	// Dotted path should create nested structure
	require.Contains(t, root.Child, "qdrant")
	require.Contains(t, root.Child["qdrant"].Child, "persistence")
	require.Contains(t, root.Child["qdrant"].Child["persistence"].Child, "storageClassName")

	// Redis should use Persistence type
	require.Contains(t, root.Child, "redis")
	require.Equal(t, "Persistence", root.Child["redis"].Child["persistence"].TypeExpr)
}

// TestBuildDottedPath_BackwardCompatibility ensures existing @typedef/@field still works.
func TestBuildDottedPath_BackwardCompatibility(t *testing.T) {
	// This is the traditional approach that must continue to work
	const yaml = `
## @typedef {struct} Persistence - Storage configuration
## @field {quantity} size - PVC size
## @field {string} [storageClassName] - StorageClass name

## @typedef {struct} Qdrant - Qdrant configuration
## @field {int} replicaCount - Number of replicas
## @field {Persistence} persistence - Storage settings

## @param {Qdrant} qdrant - Qdrant subchart values
qdrant:
  replicaCount: 1
  persistence:
    size: 10Gi
    storageClassName: ""
`
	rows, err := Parse(writeTempFile(yaml))
	require.NoError(t, err)

	root := Build(rows)

	// Verify structure created by @typedef
	require.Contains(t, root.Child, "Persistence")
	require.Contains(t, root.Child, "Qdrant")
	require.Contains(t, root.Child, "qdrant")

	// Qdrant typedef fields
	require.Contains(t, root.Child["Qdrant"].Child, "replicaCount")
	require.Contains(t, root.Child["Qdrant"].Child, "persistence")
}

/* -------------------------------------------------------------------------- */
/*  End-to-End: Dotted Paths → JSON Schema                                    */
/* -------------------------------------------------------------------------- */

// TestDottedPath_EndToEndSchemaGeneration verifies full pipeline from dotted
// path annotations to JSON Schema with nested objects.
func TestDottedPath_EndToEndSchemaGeneration(t *testing.T) {
	const yamlContent = `
## @param {int} qdrant.replicaCount - Number of Qdrant replicas
## @param {quantity} qdrant.persistence.size - Storage size
## @param {string} [qdrant.persistence.storageClassName] - StorageClass name
## @param {bool} qdrant.apiKey - Enable API key authentication
qdrant:
  replicaCount: 1
  persistence:
    size: 10Gi
    storageClassName: ""
  apiKey: true
`
	tmpfile := writeTempFile(yamlContent)
	rows, err := Parse(tmpfile)
	require.NoError(t, err)

	root := Build(rows)

	// Populate defaults from YAML
	var parsed interface{}
	require.NoError(t, yaml.Unmarshal([]byte(yamlContent), &parsed))
	aliases := map[string]*Node{}
	for _, n := range root.Child {
		aliases[n.Name] = n
	}
	PopulateDefaults(root, parsed, aliases)

	// Generate Go and CRD
	tmpdir, gofile, err := WriteGeneratedGoAndStub(root, "values")
	require.NoError(t, err)
	defer os.RemoveAll(tmpdir)

	crdBytes, err := CG(filepath.Dir(gofile))
	require.NoError(t, err)

	outfile := filepath.Join(tmpdir, "schema.json")
	require.NoError(t, WriteValuesSchema(crdBytes, outfile))

	raw, err := os.ReadFile(outfile)
	require.NoError(t, err)

	var schema map[string]any
	require.NoError(t, json.Unmarshal(raw, &schema))

	// Verify nested structure in schema
	props := schema["properties"].(map[string]any)
	require.Contains(t, props, "qdrant", "schema must have qdrant property")

	qdrant := props["qdrant"].(map[string]any)
	require.Equal(t, "object", qdrant["type"])

	qdrantProps := qdrant["properties"].(map[string]any)
	require.Contains(t, qdrantProps, "replicaCount")
	require.Contains(t, qdrantProps, "persistence")
	require.Contains(t, qdrantProps, "apiKey")

	// Verify deeply nested persistence
	persistence := qdrantProps["persistence"].(map[string]any)
	require.Equal(t, "object", persistence["type"])

	persistenceProps := persistence["properties"].(map[string]any)
	require.Contains(t, persistenceProps, "size")
	require.Contains(t, persistenceProps, "storageClassName")

	// Verify types
	replicaCount := qdrantProps["replicaCount"].(map[string]any)
	require.Equal(t, "integer", replicaCount["type"])

	apiKey := qdrantProps["apiKey"].(map[string]any)
	require.Equal(t, "boolean", apiKey["type"])
}

// TestDottedPath_MultipleSubcharts verifies schema generation for multiple subcharts.
func TestDottedPath_MultipleSubcharts(t *testing.T) {
	const yaml = `
## @param {int} qdrant.replicas - Qdrant replicas
## @param {int} redis.replicas - Redis replicas
## @param {int} postgres.replicas - Postgres replicas
qdrant:
  replicas: 1
redis:
  replicas: 3
postgres:
  replicas: 2
`
	tmpfile := writeTempFile(yaml)
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

	var schema map[string]any
	require.NoError(t, json.Unmarshal(raw, &schema))

	props := schema["properties"].(map[string]any)

	// All three subcharts must be present
	for _, name := range []string{"qdrant", "redis", "postgres"} {
		require.Contains(t, props, name, "schema must have %s property", name)
		sub := props[name].(map[string]any)
		require.Equal(t, "object", sub["type"])
		subProps := sub["properties"].(map[string]any)
		require.Contains(t, subProps, "replicas")
	}
}

/* -------------------------------------------------------------------------- */
/*  Complex Types with Dotted Paths                                           */
/* -------------------------------------------------------------------------- */

// TestDottedPath_ArrayType verifies array types work with dotted paths.
func TestDottedPath_ArrayType(t *testing.T) {
	const yaml = `
## @param {[]string} app.config.tags - List of tags
app:
  config:
    tags:
    - production
    - backend
`
	rows, err := Parse(writeTempFile(yaml))
	require.NoError(t, err)

	root := Build(rows)
	leaf := root.Child["app"].Child["config"].Child["tags"]
	require.Equal(t, "[]string", leaf.TypeExpr)
}

// TestDottedPath_MapType verifies map types work with dotted paths.
func TestDottedPath_MapType(t *testing.T) {
	const yaml = `
## @param {map[string]string} app.config.labels - Labels
app:
  config:
    labels:
      app: myapp
`
	rows, err := Parse(writeTempFile(yaml))
	require.NoError(t, err)

	root := Build(rows)
	leaf := root.Child["app"].Child["config"].Child["labels"]
	require.Equal(t, "map[string]string", leaf.TypeExpr)
}

// TestDottedPath_PointerType verifies pointer types work with dotted paths.
func TestDottedPath_PointerType(t *testing.T) {
	const yaml = `
## @param {*string} app.config.optional - Optional value
app:
  config:
    optional: null
`
	rows, err := Parse(writeTempFile(yaml))
	require.NoError(t, err)

	root := Build(rows)
	leaf := root.Child["app"].Child["config"].Child["optional"]
	require.Equal(t, "*string", leaf.TypeExpr)
}

// TestDottedPath_ConflictWithExplicitParam tests behavior when both an explicit param
// and a dotted path param target the same node.
// The dotted path should override intermediate node properties but preserve the param status.
func TestDottedPath_ConflictWithExplicitParam(t *testing.T) {
	const yaml = `
## @param {object} qdrant - Qdrant configuration
## @param {int} qdrant.replicaCount - Number of replicas
qdrant:
  replicaCount: 3
`
	tmp := writeTempFile(yaml)
	defer os.Remove(tmp)

	rows, err := Parse(tmp)
	require.NoError(t, err)

	root := Build(rows)

	// qdrant should be a param with object type (from explicit param)
	require.True(t, root.Child["qdrant"].IsParam)
	require.Equal(t, "object", root.Child["qdrant"].TypeExpr)

	// qdrant.replicaCount should also exist as a param
	require.True(t, root.Child["qdrant"].Child["replicaCount"].IsParam)
	require.Equal(t, "int", root.Child["qdrant"].Child["replicaCount"].TypeExpr)
}

// TestDottedPath_ExplicitParamOverridesDottedPath verifies that when a dotted path
// creates an intermediate node as "struct", a later explicit param with different type wins.
func TestDottedPath_ExplicitParamOverridesDottedPath(t *testing.T) {
	const yaml = `
## @param {int} qdrant.replicaCount - Number of replicas
## @param {Qdrant} qdrant - Qdrant configuration
qdrant:
  replicaCount: 3
`
	tmp := writeTempFile(yaml)
	defer os.Remove(tmp)

	rows, err := Parse(tmp)
	require.NoError(t, err)

	root := Build(rows)

	// qdrant was first created as implicit struct, then overridden by explicit Qdrant param
	require.True(t, root.Child["qdrant"].IsParam)
	require.Equal(t, "Qdrant", root.Child["qdrant"].TypeExpr)

	// qdrant.replicaCount should still exist
	require.True(t, root.Child["qdrant"].Child["replicaCount"].IsParam)
}

// TestDottedPath_ConflictLeafAndIntermediate tests conflict when same name is both
// a leaf param and an intermediate node for another dotted path.
func TestDottedPath_ConflictLeafAndIntermediate(t *testing.T) {
	const yaml = `
## @param {string} qdrant - Qdrant as string
## @param {int} qdrant.replicas - Replicas count
qdrant:
  replicas: 3
`
	tmp := writeTempFile(yaml)
	defer os.Remove(tmp)

	rows, err := Parse(tmp)
	require.NoError(t, err)

	root := Build(rows)

	// qdrant should exist and be a param (from first @param)
	require.NotNil(t, root.Child["qdrant"])
	require.True(t, root.Child["qdrant"].IsParam)
	// The type should be preserved from explicit @param, not overwritten to "struct"
	require.Equal(t, "string", root.Child["qdrant"].TypeExpr)

	// qdrant.replicas should also exist as child
	require.NotNil(t, root.Child["qdrant"].Child["replicas"])
	require.True(t, root.Child["qdrant"].Child["replicas"].IsParam)
}

// TestParseDottedPath_InvalidPaths verifies that malformed paths are rejected by regex.
func TestParseDottedPath_InvalidPaths(t *testing.T) {
	// Path with leading dot - regex should not match
	const yaml1 = `
## @param {string} .invalid - Leading dot
value: test
`
	rows, err := Parse(writeTempFile(yaml1))
	require.NoError(t, err)
	require.Empty(t, rows, "leading dot path should not be parsed")

	// Path with trailing dot
	const yaml2 = `
## @param {string} invalid. - Trailing dot
value: test
`
	rows2, err := Parse(writeTempFile(yaml2))
	require.NoError(t, err)
	require.Empty(t, rows2, "trailing dot path should not be parsed")

	// Path with consecutive dots
	const yaml3 = `
## @param {string} a..b - Consecutive dots
value: test
`
	rows3, err := Parse(writeTempFile(yaml3))
	require.NoError(t, err)
	require.Empty(t, rows3, "consecutive dots path should not be parsed")
}

// =============================================================================
// Validation Constraints Tests
// =============================================================================

// TestParseMinimumMaximum tests parsing @minimum and @maximum constraints.
func TestParseMinimumMaximum(t *testing.T) {
	const yaml = `
## @param {int} replicas - Number of replicas
## @minimum 1
## @maximum 100
replicas: 3
`
	tmp := writeTempFile(yaml)
	defer os.Remove(tmp)

	rows, err := Parse(tmp)
	require.NoError(t, err)
	require.Len(t, rows, 1)

	r := rows[0]
	require.Equal(t, kParam, r.K)
	require.Equal(t, []string{"replicas"}, r.Path)

	// Validate constraints
	require.NotNil(t, r.Minimum, "Minimum should be set")
	require.NotNil(t, r.Maximum, "Maximum should be set")
	require.Equal(t, 1.0, *r.Minimum)
	require.Equal(t, 100.0, *r.Maximum)
}

// TestParseMinimumMaximum_Float tests @minimum/@maximum with float values.
func TestParseMinimumMaximum_Float(t *testing.T) {
	const yaml = `
## @param {number} ratio - Ratio value
## @minimum -0.5
## @maximum 1.5
ratio: 0.5
`
	tmp := writeTempFile(yaml)
	defer os.Remove(tmp)

	rows, err := Parse(tmp)
	require.NoError(t, err)
	require.Len(t, rows, 1)

	r := rows[0]
	require.NotNil(t, r.Minimum)
	require.NotNil(t, r.Maximum)
	require.Equal(t, -0.5, *r.Minimum)
	require.Equal(t, 1.5, *r.Maximum)
}

// TestParseExclusiveMinMax tests @exclusiveMinimum/@exclusiveMaximum flags.
func TestParseExclusiveMinMax(t *testing.T) {
	const yaml = `
## @param {int} port - Port number
## @minimum 0
## @exclusiveMinimum
## @maximum 65536
## @exclusiveMaximum
port: 8080
`
	tmp := writeTempFile(yaml)
	defer os.Remove(tmp)

	rows, err := Parse(tmp)
	require.NoError(t, err)
	require.Len(t, rows, 1)

	r := rows[0]
	require.NotNil(t, r.Minimum)
	require.NotNil(t, r.Maximum)
	require.Equal(t, 0.0, *r.Minimum)
	require.Equal(t, 65536.0, *r.Maximum)
	require.True(t, r.ExclusiveMinimum)
	require.True(t, r.ExclusiveMaximum)
}

// TestParseMinMaxLength tests @minLength/@maxLength for strings.
func TestParseMinMaxLength(t *testing.T) {
	const yaml = `
## @param {string} name - Release name
## @minLength 1
## @maxLength 63
name: "myapp"
`
	tmp := writeTempFile(yaml)
	defer os.Remove(tmp)

	rows, err := Parse(tmp)
	require.NoError(t, err)
	require.Len(t, rows, 1)

	r := rows[0]
	require.NotNil(t, r.MinLength, "MinLength should be set")
	require.NotNil(t, r.MaxLength, "MaxLength should be set")
	require.Equal(t, int64(1), *r.MinLength)
	require.Equal(t, int64(63), *r.MaxLength)
}

// TestParsePattern tests @pattern for regex validation.
func TestParsePattern(t *testing.T) {
	const yaml = `
## @param {string} name - DNS-compatible name
## @pattern ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$
name: "myapp"
`
	tmp := writeTempFile(yaml)
	defer os.Remove(tmp)

	rows, err := Parse(tmp)
	require.NoError(t, err)
	require.Len(t, rows, 1)

	r := rows[0]
	require.Equal(t, `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`, r.Pattern)
}

// TestParseMinMaxItems tests @minItems/@maxItems for arrays.
func TestParseMinMaxItems(t *testing.T) {
	const yaml = `
## @param {[]string} tags - List of tags
## @minItems 1
## @maxItems 10
tags: []
`
	tmp := writeTempFile(yaml)
	defer os.Remove(tmp)

	rows, err := Parse(tmp)
	require.NoError(t, err)
	require.Len(t, rows, 1)

	r := rows[0]
	require.NotNil(t, r.MinItems, "MinItems should be set")
	require.NotNil(t, r.MaxItems, "MaxItems should be set")
	require.Equal(t, int64(1), *r.MinItems)
	require.Equal(t, int64(10), *r.MaxItems)
}

// TestParseConstraints_MultipleParams tests that constraints apply only to immediate param.
func TestParseConstraints_MultipleParams(t *testing.T) {
	const yaml = `
## @param {int} replicas - Number of replicas
## @minimum 1
replicas: 3

## @param {int} workers - Number of workers
## @minimum 0
## @maximum 16
workers: 4
`
	tmp := writeTempFile(yaml)
	defer os.Remove(tmp)

	rows, err := Parse(tmp)
	require.NoError(t, err)
	require.Len(t, rows, 2)

	// First param: replicas
	r1 := rows[0]
	require.Equal(t, []string{"replicas"}, r1.Path)
	require.NotNil(t, r1.Minimum)
	require.Equal(t, 1.0, *r1.Minimum)
	require.Nil(t, r1.Maximum, "replicas should not have maximum")

	// Second param: workers
	r2 := rows[1]
	require.Equal(t, []string{"workers"}, r2.Path)
	require.NotNil(t, r2.Minimum)
	require.NotNil(t, r2.Maximum)
	require.Equal(t, 0.0, *r2.Minimum)
	require.Equal(t, 16.0, *r2.Maximum)
}

// TestParseConstraints_SectionDoesNotResetConstraints verifies that @section
// (which is a README concept, not OpenAPI) doesn't interrupt constraint accumulation.
func TestParseConstraints_SectionDoesNotResetConstraints(t *testing.T) {
	const yaml = `
## @param {int} port - Port number
## @minimum 1
## @section Database Settings
## @maximum 65535
port: 5432
`
	tmp := writeTempFile(yaml)
	defer os.Remove(tmp)

	rows, err := Parse(tmp)
	require.NoError(t, err)
	require.Len(t, rows, 1)

	// @section is ignored in openapi parsing, constraints apply to port
	r := rows[0]
	require.Equal(t, []string{"port"}, r.Path)
	require.NotNil(t, r.Minimum, "minimum should be set")
	require.NotNil(t, r.Maximum, "maximum should be set despite @section in between")
	require.Equal(t, 1.0, *r.Minimum)
	require.Equal(t, 65535.0, *r.Maximum)
}

// TestBuildWithConstraints tests that Build() transfers constraints from Raw to Node.
func TestBuildWithConstraints(t *testing.T) {
	min := 1.0
	max := 100.0
	minLen := int64(1)
	maxLen := int64(63)
	minItems := int64(1)
	maxItems := int64(10)
	pattern := `^[a-z]+$`

	rows := []Raw{
		{
			K:                kParam,
			Path:             []string{"replicas"},
			TypeExpr:         "int",
			Minimum:          &min,
			Maximum:          &max,
			ExclusiveMinimum: true,
		},
		{
			K:         kParam,
			Path:      []string{"name"},
			TypeExpr:  "string",
			MinLength: &minLen,
			MaxLength: &maxLen,
			Pattern:   pattern,
		},
		{
			K:        kParam,
			Path:     []string{"tags"},
			TypeExpr: "[]string",
			MinItems: &minItems,
			MaxItems: &maxItems,
		},
	}

	root := Build(rows)

	// Test replicas constraints
	replicas := root.Child["replicas"]
	require.NotNil(t, replicas)
	require.NotNil(t, replicas.Minimum)
	require.NotNil(t, replicas.Maximum)
	require.Equal(t, 1.0, *replicas.Minimum)
	require.Equal(t, 100.0, *replicas.Maximum)
	require.True(t, replicas.ExclusiveMinimum)

	// Test name constraints
	name := root.Child["name"]
	require.NotNil(t, name)
	require.NotNil(t, name.MinLength)
	require.NotNil(t, name.MaxLength)
	require.Equal(t, int64(1), *name.MinLength)
	require.Equal(t, int64(63), *name.MaxLength)
	require.Equal(t, pattern, name.Pattern)

	// Test tags constraints
	tags := root.Child["tags"]
	require.NotNil(t, tags)
	require.NotNil(t, tags.MinItems)
	require.NotNil(t, tags.MaxItems)
	require.Equal(t, int64(1), *tags.MinItems)
	require.Equal(t, int64(10), *tags.MaxItems)
}

// TestGenerateKubebuilderValidationMarkers tests kubebuilder markers generation.
func TestGenerateKubebuilderValidationMarkers(t *testing.T) {
	min := 1.0
	max := 100.0
	minLen := int64(1)
	maxLen := int64(63)
	minItems := int64(1)
	maxItems := int64(10)
	pattern := `^[a-z0-9-]+$`

	rows := []Raw{
		{
			K:                kParam,
			Path:             []string{"replicas"},
			TypeExpr:         "int",
			Minimum:          &min,
			Maximum:          &max,
			ExclusiveMinimum: true,
			ExclusiveMaximum: true,
		},
		{
			K:         kParam,
			Path:      []string{"name"},
			TypeExpr:  "string",
			MinLength: &minLen,
			MaxLength: &maxLen,
			Pattern:   pattern,
		},
		{
			K:        kParam,
			Path:     []string{"tags"},
			TypeExpr: "[]string",
			MinItems: &minItems,
			MaxItems: &maxItems,
		},
	}

	root := Build(rows)
	g := &gen{pkg: "values"}
	code, _, err := g.Generate(root)
	require.NoError(t, err)

	src := string(code)

	// Test numeric constraints for replicas
	require.Contains(t, src, "+kubebuilder:validation:Minimum=1", "should have Minimum marker")
	require.Contains(t, src, "+kubebuilder:validation:Maximum=100", "should have Maximum marker")
	require.Contains(t, src, "+kubebuilder:validation:ExclusiveMinimum=true", "should have ExclusiveMinimum marker")
	require.Contains(t, src, "+kubebuilder:validation:ExclusiveMaximum=true", "should have ExclusiveMaximum marker")

	// Test string constraints for name
	require.Contains(t, src, "+kubebuilder:validation:MinLength=1", "should have MinLength marker")
	require.Contains(t, src, "+kubebuilder:validation:MaxLength=63", "should have MaxLength marker")
	require.Contains(t, src, "+kubebuilder:validation:Pattern=", "should have Pattern marker")

	// Test array constraints for tags
	require.Contains(t, src, "+kubebuilder:validation:MinItems=1", "should have MinItems marker")
	require.Contains(t, src, "+kubebuilder:validation:MaxItems=10", "should have MaxItems marker")
}

// TestEndToEndValidationConstraintsInSchema tests full pipeline: YAML → JSON Schema.
func TestEndToEndValidationConstraintsInSchema(t *testing.T) {
	const yaml = `
## @param {int} replicas - Number of replicas
## @minimum 1
## @maximum 10
replicas: 3

## @param {string} name - DNS-compatible name
## @minLength 1
## @maxLength 63
## @pattern ^[a-z0-9-]+$
name: "myapp"

## @param {[]string} tags - List of tags
## @minItems 1
## @maxItems 5
tags:
  - default
`
	tmp := writeTempFile(yaml)
	defer os.Remove(tmp)

	// Parse → Build → Generate
	rows, err := Parse(tmp)
	require.NoError(t, err)

	root := Build(rows)
	tmpDir, goFile, err := WriteGeneratedGoAndStub(root, "values")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// Use controller-gen to generate CRD
	crdBytes, err := CG(filepath.Dir(goFile))
	require.NoError(t, err)

	// Write schema and read it back as JSON for easy inspection
	schemaPath := filepath.Join(tmpDir, "values.schema.json")
	require.NoError(t, WriteValuesSchema(crdBytes, schemaPath))
	schemaBytes, err := os.ReadFile(schemaPath)
	require.NoError(t, err)

	// Parse schema JSON
	var schema map[string]any
	err = json.Unmarshal(schemaBytes, &schema)
	require.NoError(t, err)

	specPropsProps := schema["properties"].(map[string]any)

	// Check replicas constraints
	replicas := specPropsProps["replicas"].(map[string]any)
	require.Equal(t, float64(1), replicas["minimum"], "replicas should have minimum=1")
	require.Equal(t, float64(10), replicas["maximum"], "replicas should have maximum=10")

	// Check name constraints
	name := specPropsProps["name"].(map[string]any)
	require.Equal(t, float64(1), name["minLength"], "name should have minLength=1")
	require.Equal(t, float64(63), name["maxLength"], "name should have maxLength=63")
	require.Equal(t, "^[a-z0-9-]+$", name["pattern"], "name should have pattern")

	// Check tags constraints
	tags := specPropsProps["tags"].(map[string]any)
	require.Equal(t, float64(1), tags["minItems"], "tags should have minItems=1")
	require.Equal(t, float64(5), tags["maxItems"], "tags should have maxItems=5")
}

// TestFieldPatternStillWorks verifies @field annotations work after patterns refactor.
func TestFieldPatternStillWorks(t *testing.T) {
	const yaml = `
## @typedef {struct} Database - Database config
## @field {string} host - Database host
## @field {int} port - Database port
## @param {Database} database - Database settings
database:
  host: localhost
  port: 5432
`
	tmp := writeTempFile(yaml)
	defer os.Remove(tmp)

	rows, err := Parse(tmp)
	require.NoError(t, err)

	// Should have: typedef Database, 2 fields, 1 param
	var typedefCount, fieldCount, paramCount int
	for _, r := range rows {
		switch r.K {
		case kTypedef:
			typedefCount++
			require.Equal(t, []string{"Database"}, r.Path)
		case kField:
			fieldCount++
		case kParam:
			paramCount++
			require.Equal(t, []string{"database"}, r.Path)
		}
	}
	require.Equal(t, 1, typedefCount, "should have 1 typedef")
	require.Equal(t, 2, fieldCount, "should have 2 fields")
	require.Equal(t, 1, paramCount, "should have 1 param")
}

// TestFieldConstraints verifies @field annotations can have validation constraints.
func TestFieldConstraints(t *testing.T) {
	const yaml = `
## @typedef {struct} Database - Database config
## @field {string} host - Database host
## @minLength 1
## @maxLength 253
## @pattern ^[a-z0-9.-]+$
## @field {int} port - Database port
## @minimum 1
## @maximum 65535
## @param {Database} database - Database settings
database:
  host: localhost
  port: 5432
`
	tmp := writeTempFile(yaml)
	defer os.Remove(tmp)

	rows, err := Parse(tmp)
	require.NoError(t, err)

	// Find the host and port fields
	var hostField, portField *Raw
	for i := range rows {
		if rows[i].K == kField {
			if rows[i].Path[1] == "host" {
				hostField = &rows[i]
			} else if rows[i].Path[1] == "port" {
				portField = &rows[i]
			}
		}
	}

	require.NotNil(t, hostField, "should have host field")
	require.NotNil(t, portField, "should have port field")

	// Verify host field constraints
	require.NotNil(t, hostField.MinLength)
	require.Equal(t, int64(1), *hostField.MinLength)
	require.NotNil(t, hostField.MaxLength)
	require.Equal(t, int64(253), *hostField.MaxLength)
	require.Equal(t, "^[a-z0-9.-]+$", hostField.Pattern)

	// Verify port field constraints
	require.NotNil(t, portField.Minimum)
	require.Equal(t, 1.0, *portField.Minimum)
	require.NotNil(t, portField.Maximum)
	require.Equal(t, 65535.0, *portField.Maximum)
}

// TestFieldConstraintsInBuild verifies constraints propagate through Build().
func TestFieldConstraintsInBuild(t *testing.T) {
	minLen := int64(1)
	maxLen := int64(253)
	min := 1.0
	max := 65535.0
	rows := []Raw{
		{K: kTypedef, Path: []string{"Database"}, TypeExpr: "struct"},
		{K: kField, Path: []string{"Database", "host"}, TypeExpr: "string", MinLength: &minLen, MaxLength: &maxLen, Pattern: "^[a-z]+$"},
		{K: kField, Path: []string{"Database", "port"}, TypeExpr: "int", Minimum: &min, Maximum: &max},
		{K: kParam, Path: []string{"database"}, TypeExpr: "Database"},
	}

	root := Build(rows)

	// Find Database type
	dbType := root.Child["Database"]
	require.NotNil(t, dbType)

	// Verify host field constraints in Node
	hostNode := dbType.Child["host"]
	require.NotNil(t, hostNode)
	require.NotNil(t, hostNode.MinLength)
	require.Equal(t, int64(1), *hostNode.MinLength)
	require.NotNil(t, hostNode.MaxLength)
	require.Equal(t, int64(253), *hostNode.MaxLength)
	require.Equal(t, "^[a-z]+$", hostNode.Pattern)

	// Verify port field constraints in Node
	portNode := dbType.Child["port"]
	require.NotNil(t, portNode)
	require.NotNil(t, portNode.Minimum)
	require.Equal(t, 1.0, *portNode.Minimum)
	require.NotNil(t, portNode.Maximum)
	require.Equal(t, 65535.0, *portNode.Maximum)
}
