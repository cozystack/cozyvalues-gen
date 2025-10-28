package openapi

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestComplexDefaultValues(t *testing.T) {
	tests := []struct {
		name     string
		yaml     string
		expected string
	}{
		{
			name: "quoted string with spaces",
			yaml: `## @param {string} message="hello world" - Message
message: "hello world"`,
			expected: `"hello world"`,
		},
		{
			name: "single quoted string",
			yaml: `## @param {string} text='single quote' - Text
text: 'single quote'`,
			expected: `'single quote'`,
		},
		{
			name: "JSON object",
			yaml: `## @param {object} config={"key":"value","num":42} - Config
config: {}`,
			expected: `{"key":"value","num":42}`,
		},
		{
			name: "JSON array",
			yaml: `## @param {[]string} items=[1,2,3] - Items
items: []`,
			expected: `[1,2,3]`,
		},
		{
			name: "boolean true",
			yaml: `## @param {bool} enabled=true - Enabled
enabled: true`,
			expected: `true`,
		},
		{
			name: "boolean false",
			yaml: `## @param {bool} disabled=false - Disabled
disabled: false`,
			expected: `false`,
		},
		{
			name: "null value",
			yaml: `## @param {*string} optional=null - Optional
optional:`,
			expected: `null`,
		},
		{
			name: "negative number",
			yaml: `## @param {int} offset=-10 - Offset
offset: -10`,
			expected: `-10`,
		},
		{
			name: "decimal number",
			yaml: `## @param {float64} rate=3.14 - Rate
rate: 3.14`,
			expected: `3.14`,
		},
		{
			name: "negative decimal",
			yaml: `## @param {float64} delta=-2.5 - Delta
delta: -2.5`,
			expected: `-2.5`,
		},
		{
			name: "string with hyphens",
			yaml: `## @param {string} name="my-app-name" - Name
name: "my-app-name"`,
			expected: `"my-app-name"`,
		},
		{
			name: "complex JSON array",
			yaml: `## @param {[]object} items=[{"name":"a"},{"name":"b"}] - Items
items: []`,
			expected: `[{"name":"a"},{"name":"b"}]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpfile := writeTempFile(tt.yaml)
			defer os.Remove(tmpfile)

			rows, err := Parse(tmpfile)
			require.NoError(t, err)
			require.Len(t, rows, 1, "should parse one param")
			require.Equal(t, tt.expected, rows[0].DefaultVal, "default value should match")
		})
	}
}

func TestComplexDefaultsInFields(t *testing.T) {
	const yamlContent = `
## @typedef {struct} Config - Configuration
## @field {string} message="hello world" - Message with spaces
## @field {[]int} ports=[80,443,8080] - Port list
## @field {object} meta={"app":"demo","v":1} - Metadata
## @field {bool} enabled=true - Enabled flag
## @field {float64} rate=-2.5 - Negative decimal
## @field {string} name='single-quoted' - Single quoted

## @param {Config} config - Config
config: {}
`
	tmpfile := writeTempFile(yamlContent)
	defer os.Remove(tmpfile)

	rows, err := Parse(tmpfile)
	require.NoError(t, err)

	expectedDefaults := map[string]string{
		"message": `"hello world"`,
		"ports":   `[80,443,8080]`,
		"meta":    `{"app":"demo","v":1}`,
		"enabled": `true`,
		"rate":    `-2.5`,
		"name":    `'single-quoted'`,
	}

	for _, row := range rows {
		if row.K == kField {
			fieldName := row.Path[1]
			if expected, ok := expectedDefaults[fieldName]; ok {
				require.Equal(t, expected, row.DefaultVal, "field %s should have correct default", fieldName)
			}
		}
	}
}

func TestDefaultValueInGeneratedCode(t *testing.T) {
	const yamlContent = `
## @typedef {struct} Server - Server config
## @field {string} host="api.example.com" - Hostname
## @field {int} timeout=-1 - Timeout (-1 = no timeout)
## @field {[]string} tags=["prod","api"] - Tags

## @param {Server} server - Server
server: {}
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

	require.Contains(t, code, `+kubebuilder:default:="api.example.com"`, "quoted string default should be in code")
	require.Contains(t, code, `+kubebuilder:default:=-1`, "negative number should be in code")
	require.Contains(t, code, `+kubebuilder:default:={"prod","api"}`, "array default should be in code")
}

