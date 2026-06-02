package openapi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// schemaBytesFor generates the values.schema.json for the given annotated
// values and returns the raw file contents (key order preserved).
func schemaBytesFor(t *testing.T, yaml string) string {
	t.Helper()

	tmp := writeTempFile(yaml)
	defer os.Remove(tmp)

	rows, err := Parse(tmp)
	require.NoError(t, err)

	root := Build(rows)
	tmpDir, goFile, err := WriteGeneratedGoAndStub(root, "values", "values.helm.io", "v1alpha1")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	crdBytes, err := CG(filepath.Dir(goFile))
	require.NoError(t, err)

	schemaPath := filepath.Join(tmpDir, "values.schema.json")
	require.NoError(t, WriteValuesSchemaWithOrder(crdBytes, schemaPath, root))

	schemaBytes, err := os.ReadFile(schemaPath)
	require.NoError(t, err)
	return string(schemaBytes)
}

// schemaProps generates the values.schema.json for the given annotated values
// and returns the decoded top-level "properties" map for inspection.
func schemaProps(t *testing.T, yaml string) map[string]any {
	t.Helper()

	var schema map[string]any
	require.NoError(t, json.Unmarshal([]byte(schemaBytesFor(t, yaml)), &schema))

	props, ok := schema["properties"].(map[string]any)
	require.True(t, ok, "schema must have properties")
	return props
}

// TestParseVendorExtension verifies the @x-<keyword> directive is parsed and
// attached to the preceding @param with its YAML-parsed value.
func TestParseVendorExtension(t *testing.T) {
	const yaml = `
## @param {string} instanceType - Virtual Machine instance type.
## @x-cozystack-options {source: instancetype}
instanceType: "u1.medium"
`
	tmp := writeTempFile(yaml)
	defer os.Remove(tmp)

	rows, err := Parse(tmp)
	require.NoError(t, err)

	var param *Raw
	for i := range rows {
		if rows[i].K == kParam && rows[i].Path[0] == "instanceType" {
			param = &rows[i]
		}
	}
	require.NotNil(t, param, "instanceType param should exist")
	require.Equal(t, map[string]interface{}{
		"x-cozystack-options": map[string]interface{}{"source": "instancetype"},
	}, param.Extensions)
}

// TestVendorExtensionDoesNotCollideWithBuiltins ensures the @x- directive does
// not swallow built-in directives like @param/@minimum (none start with "x-").
func TestVendorExtensionDoesNotCollideWithBuiltins(t *testing.T) {
	const yaml = `
## @param {int} replicas - Replicas.
## @minimum 1
## @x-foo bar
replicas: 1
`
	tmp := writeTempFile(yaml)
	defer os.Remove(tmp)

	rows, err := Parse(tmp)
	require.NoError(t, err)

	var param *Raw
	for i := range rows {
		if rows[i].K == kParam && rows[i].Path[0] == "replicas" {
			param = &rows[i]
		}
	}
	require.NotNil(t, param)
	require.NotNil(t, param.Minimum)
	require.Equal(t, float64(1), *param.Minimum)
	require.Equal(t, map[string]interface{}{"x-foo": "bar"}, param.Extensions)
}

// TestVendorExtensionOnParamInSchema checks an x-* key with an object value is
// emitted onto a top-level property in the JSON schema.
func TestVendorExtensionOnParamInSchema(t *testing.T) {
	const yaml = `
## @param {string} instanceType - Virtual Machine instance type.
## @x-cozystack-options {source: instancetype}
instanceType: "u1.medium"
`
	props := schemaProps(t, yaml)

	it := props["instanceType"].(map[string]any)
	require.Equal(t, map[string]any{"source": "instancetype"}, it["x-cozystack-options"])
	require.Equal(t, "string", it["type"])
}

// TestVendorExtensionValueTypes checks object, array and scalar string values.
func TestVendorExtensionValueTypes(t *testing.T) {
	const yaml = `
## @param {string} a - A.
## @x-obj {source: gpu, kind: pci}
a: ""

## @param {string} b - B.
## @x-list [{a: 1}, {b: 2}]
b: ""

## @param {string} c - C.
## @x-str "hello world"
c: ""
`
	props := schemaProps(t, yaml)

	a := props["a"].(map[string]any)
	require.Equal(t, map[string]any{"source": "gpu", "kind": "pci"}, a["x-obj"])

	b := props["b"].(map[string]any)
	require.Equal(t, []any{
		map[string]any{"a": float64(1)},
		map[string]any{"b": float64(2)},
	}, b["x-list"])

	c := props["c"].(map[string]any)
	require.Equal(t, "hello world", c["x-str"])
}

// TestMultipleVendorExtensionsOnOneField checks several @x-* lines on one field.
func TestMultipleVendorExtensionsOnOneField(t *testing.T) {
	const yaml = `
## @param {int} count - Count.
## @x-foo-scalar 42
## @x-foo-flag true
## @x-foo-obj {a: 1}
count: 1
`
	props := schemaProps(t, yaml)

	c := props["count"].(map[string]any)
	require.Equal(t, float64(42), c["x-foo-scalar"])
	require.Equal(t, true, c["x-foo-flag"])
	require.Equal(t, map[string]any{"a": float64(1)}, c["x-foo-obj"])
}

// TestVendorExtensionOnTypedefFieldPropagates verifies an x-* key declared on a
// typedef @field appears wherever the type is used: directly, via []Type
// (items.properties) and via map[string]Type (additionalProperties.properties).
func TestVendorExtensionOnTypedefFieldPropagates(t *testing.T) {
	const yaml = `
## @typedef {struct} GPU - GPU device configuration.
## @field {string} name - The name of the GPU resource to attach.
## @x-cozystack-options {source: gpu}

## @param {GPU} gpu - Single GPU.
gpu: {}

## @param {[]GPU} gpus - GPU list.
gpus: []

## @param {map[string]GPU} gpuMap - GPU map.
gpuMap: {}
`
	props := schemaProps(t, yaml)

	want := map[string]any{"source": "gpu"}

	// Direct use: gpu.properties.name
	gpu := props["gpu"].(map[string]any)
	gpuName := gpu["properties"].(map[string]any)["name"].(map[string]any)
	require.Equal(t, want, gpuName["x-cozystack-options"])

	// Array use: gpus.items.properties.name
	gpus := props["gpus"].(map[string]any)
	items := gpus["items"].(map[string]any)
	itemName := items["properties"].(map[string]any)["name"].(map[string]any)
	require.Equal(t, want, itemName["x-cozystack-options"])

	// Map use: gpuMap.additionalProperties.properties.name
	gpuMap := props["gpuMap"].(map[string]any)
	ap := gpuMap["additionalProperties"].(map[string]any)
	apName := ap["properties"].(map[string]any)["name"].(map[string]any)
	require.Equal(t, want, apName["x-cozystack-options"])
}

// TestVendorExtensionNotInGoTypes ensures vendor extensions never leak into the
// generated Go types — they are schema-only.
func TestVendorExtensionNotInGoTypes(t *testing.T) {
	const yaml = `
## @typedef {struct} GPU - GPU device configuration.
## @field {string} name - The name of the GPU resource to attach.
## @x-cozystack-options {source: gpu}

## @param {string} instanceType - Instance type.
## @x-cozystack-options {source: instancetype}
instanceType: "u1.medium"

## @param {[]GPU} gpus - GPU list.
gpus: []
`
	tmp := writeTempFile(yaml)
	defer os.Remove(tmp)

	rows, err := Parse(tmp)
	require.NoError(t, err)

	root := Build(rows)
	g := &gen{pkg: "values"}
	formatted, _, err := g.Generate(root)
	require.NoError(t, err)

	require.NotContains(t, string(formatted), "x-", "Go types must not contain vendor extension keys")
	require.NotContains(t, string(formatted), "cozystack", "Go types must not contain extension values")
}

// orderFixtureYAML is a values.yaml whose params carry several constraints
// whose JSONSchemaProps struct-field order differs from alphabetical order
// (e.g. "type" precedes "maxLength"/"minLength"/"pattern", but alphabetically
// "type" would sort last). It is shared by the order-preservation tests below.
const orderFixtureYAML = `
## @param {string} host - The hostname.
## @maxLength 30
## @minLength 3
## @pattern ^[a-z.]+$
host: "example.com"

## @param {int} replicas - Number of replicas.
## @maximum 10
## @minimum 1
replicas: 3
`

// TestSchemaKeyOrderPreservedWithoutExtensions asserts that, for an input with
// NO @x- directive, the generated schema keeps each property's keys in
// JSONSchemaProps struct-field order (NOT alphabetical). This guards against
// the key-ordering regression where round-tripping through a Go map sorted
// every object's keys alphabetically. It fails against the buggy code (which
// emits "type" last) and passes once order is preserved.
func TestSchemaKeyOrderPreservedWithoutExtensions(t *testing.T) {
	got := schemaBytesFor(t, orderFixtureYAML)

	const want = `{
  "title": "Chart Values",
  "type": "object",
  "properties": {
    "host": {
      "description": "The hostname.",
      "type": "string",
      "maxLength": 30,
      "minLength": 3,
      "pattern": "^[a-z.]+$"
    },
    "replicas": {
      "description": "Number of replicas.",
      "type": "integer",
      "maximum": 10,
      "minimum": 1
    }
  }
}
`
	require.Equal(t, want, got, "schema key order must follow struct order, not alphabetical")
}

// TestSchemaKeyOrderWithExtensionInsertedAtEnd asserts that adding a single
// @x- directive to one field produces output identical to the no-extension
// output EXCEPT for the injected x-* key, which is appended as the last key of
// its object. Nothing else is reordered.
func TestSchemaKeyOrderWithExtensionInsertedAtEnd(t *testing.T) {
	const yamlWithExt = `
## @param {string} host - The hostname.
## @maxLength 30
## @minLength 3
## @pattern ^[a-z.]+$
## @x-foo {source: dns}
host: "example.com"

## @param {int} replicas - Number of replicas.
## @maximum 10
## @minimum 1
replicas: 3
`
	got := schemaBytesFor(t, yamlWithExt)

	// The expectation is the no-extension golden with exactly the "x-foo"
	// key appended at the end of the "host" object — pre-existing keys keep
	// their original order and position.
	const want = `{
  "title": "Chart Values",
  "type": "object",
  "properties": {
    "host": {
      "description": "The hostname.",
      "type": "string",
      "maxLength": 30,
      "minLength": 3,
      "pattern": "^[a-z.]+$",
      "x-foo": {
        "source": "dns"
      }
    },
    "replicas": {
      "description": "Number of replicas.",
      "type": "integer",
      "maximum": 10,
      "minimum": 1
    }
  }
}
`
	require.Equal(t, want, got)

	// And the diff versus the no-extension output is exactly the inserted key:
	// stripping the injected x-foo block from the annotated output must
	// reproduce the un-annotated output byte-for-byte.
	const injected = `,
      "x-foo": {
        "source": "dns"
      }`
	require.Equal(t, schemaBytesFor(t, orderFixtureYAML),
		strings.Replace(got, injected, "", 1),
		"removing the injected x-foo key must reproduce the un-annotated output")
}
