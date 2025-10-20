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
