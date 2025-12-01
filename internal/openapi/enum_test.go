package openapi

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEnumGeneration(t *testing.T) {
	const yamlContent = `
## @enum {string} Size - Size preset
## @value small
## @value medium
## @value large

## @param {Size} size - Size configuration
size: small
`
	tmpfile := writeTempFile(yamlContent)
	defer os.Remove(tmpfile)

	rows, err := Parse(tmpfile)
	require.NoError(t, err)
	root := Build(rows)

	g := &gen{pkg: "values"}
	formatted, _, err := g.Generate(root)
	require.NoError(t, err)
	code := string(formatted)

	require.Contains(t, code, "type Size string", "enum should generate type alias")
	require.Contains(t, code, `+kubebuilder:validation:Enum="small";"medium";"large"`, "enum should have validation marker")
}

func TestEnumInStruct(t *testing.T) {
	const yamlContent = `
## @enum {string} Color - Color options
## @value red
## @value green
## @value blue

## @typedef {struct} Config - Configuration
## @field {Color} color - Selected color
## @field {string} name - Name

## @param {Config} config - Config
config:
  color: red
  name: test
`
	tmpfile := writeTempFile(yamlContent)
	defer os.Remove(tmpfile)

	rows, err := Parse(tmpfile)
	require.NoError(t, err)
	root := Build(rows)

	g := &gen{pkg: "values"}
	formatted, _, err := g.Generate(root)
	require.NoError(t, err)
	code := string(formatted)

	require.Contains(t, code, "type Color string", "enum should be generated")
	require.Contains(t, code, `Color Color `+"`json:\"color\"`", "struct field should use enum type")
}

func TestOmitEmptyOnBracketedFields(t *testing.T) {
	const yamlContent = `
## @typedef {struct} Optional - Optional fields test
## @field {string} [optionalField] - Optional field
## @field {string} requiredField - Required field

## @param {Optional} opt - Optional config
opt: {}
`
	tmpfile := writeTempFile(yamlContent)
	defer os.Remove(tmpfile)

	rows, err := Parse(tmpfile)
	require.NoError(t, err)
	root := Build(rows)

	g := &gen{pkg: "values"}
	formatted, _, err := g.Generate(root)
	require.NoError(t, err)
	code := string(formatted)

	require.Contains(t, code, `OptionalField string `+"`json:\"optionalField,omitempty\"`", "bracketed field should have omitempty")
	require.Contains(t, code, "RequiredField string `json:\"requiredField\"`", "non-bracketed field should not have omitempty")
}

func TestOmitEmptyOnBracketedParams(t *testing.T) {
	const yamlContent = `
## @param {string} [optionalParam] - Optional parameter
optionalParam: ""

## @param {string} requiredParam - Required parameter  
requiredParam: ""
`
	tmpfile := writeTempFile(yamlContent)
	defer os.Remove(tmpfile)

	rows, err := Parse(tmpfile)
	require.NoError(t, err)
	root := Build(rows)

	g := &gen{pkg: "values"}
	formatted, _, err := g.Generate(root)
	require.NoError(t, err)
	code := string(formatted)

	require.Contains(t, code, `OptionalParam string `+"`json:\"optionalParam,omitempty\"`", "bracketed param should have omitempty")
	require.Contains(t, code, "RequiredParam string `json:\"requiredParam\"`", "non-bracketed param should not have omitempty")
}

func TestEnumWithHyphens(t *testing.T) {
	const yamlContent = `
## @enum {string} Mode - Mode for balancer.
## @value tcp
## @value tcp-with-proxy

## @typedef {struct} HttpAndHttps - HTTP and HTTPS configuration.
## @field {Mode} mode - Mode for balancer.

## @param {HttpAndHttps} httpAndHttps - HTTP and HTTPS configuration.
httpAndHttps:
  mode: tcp
`
	tmpfile := writeTempFile(yamlContent)
	defer os.Remove(tmpfile)

	rows, err := Parse(tmpfile)
	require.NoError(t, err)
	root := Build(rows)

	g := &gen{pkg: "values"}
	formatted, _, err := g.Generate(root)
	require.NoError(t, err)
	code := string(formatted)

	// Check that enum with hyphens is parsed correctly
	require.Contains(t, code, "type Mode string", "enum should generate type alias")
	require.Contains(t, code, `+kubebuilder:validation:Enum="tcp";"tcp-with-proxy"`, "enum should have both values including tcp-with-proxy")

	// Check that the enum values are correctly stored
	modeNode := root.Child["Mode"]
	require.NotNil(t, modeNode, "Mode enum node should exist")
	require.Contains(t, modeNode.Enums, "tcp", "Mode enum should contain 'tcp'")
	require.Contains(t, modeNode.Enums, "tcp-with-proxy", "Mode enum should contain 'tcp-with-proxy'")
}

func TestEnumWithQuotesAndDots(t *testing.T) {
	const yamlContent = `
## @enum {string} Version - Kubernetes version
## @value "v1.33"
## @value "v1.32"
## @value "v1.31"
## @value v1.30
## @value 'v1.29'

## @param {Version} version - Kubernetes version
version: "v1.33"
`
	tmpfile := writeTempFile(yamlContent)
	defer os.Remove(tmpfile)

	rows, err := Parse(tmpfile)
	require.NoError(t, err)
	root := Build(rows)

	g := &gen{pkg: "values"}
	formatted, _, err := g.Generate(root)
	require.NoError(t, err)
	code := string(formatted)

	// Check that enum with quotes and dots is parsed correctly
	require.Contains(t, code, "type Version string", "enum should generate type alias")
	require.Contains(t, code, `+kubebuilder:validation:Enum="v1.33";"v1.32";"v1.31";"v1.30";"v1.29"`, "enum should have all values including quoted ones")

	// Check that the enum values are correctly stored (without quotes)
	versionNode := root.Child["Version"]
	require.NotNil(t, versionNode, "Version enum node should exist")
	require.Contains(t, versionNode.Enums, "v1.33", "Version enum should contain 'v1.33'")
	require.Contains(t, versionNode.Enums, "v1.32", "Version enum should contain 'v1.32'")
	require.Contains(t, versionNode.Enums, "v1.31", "Version enum should contain 'v1.31'")
	require.Contains(t, versionNode.Enums, "v1.30", "Version enum should contain 'v1.30'")
	require.Contains(t, versionNode.Enums, "v1.29", "Version enum should contain 'v1.29'")
}
