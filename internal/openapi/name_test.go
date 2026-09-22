package openapi

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestParseName verifies that @name is parsed as its own row kind, that the
// constraint lines following it attach to that row rather than to a values
// key, and that it never becomes a param.
func TestParseName(t *testing.T) {
	const yaml = `
## @name {string} - Cluster name. Worker pools render as "<name>-<pool>".
## @maxLength 32

## @param {string} storageClass - Default StorageClass.
storageClass: replicated
`
	tmp := writeTempFile(yaml)
	defer os.Remove(tmp)

	rows, err := Parse(tmp)
	require.NoError(t, err)

	var name *Raw
	for i := range rows {
		if rows[i].K == kName {
			require.Nil(t, name, "more than one @name row")
			name = &rows[i]
		}
		require.False(t, rows[i].K == kParam && rows[i].Path[0] == "name",
			"@name must not produce a values param")
	}
	require.NotNil(t, name, "no @name row parsed")
	require.Equal(t, "string", name.TypeExpr)
	require.Equal(t, `Cluster name. Worker pools render as "<name>-<pool>".`, name.Description)
	require.NotNil(t, name.MaxLength)
	require.Equal(t, int64(32), *name.MaxLength)

	root := Build(rows)
	require.NotNil(t, root.ResourceName)
	require.Equal(t, int64(32), *root.ResourceName.MaxLength)
	require.NotContains(t, root.Child, "name")
}

// TestParseNameRejectsNonString pins the type token to {string}: metadata.name
// is a string, and a chart declaring anything else is a typo, not a feature.
func TestParseNameRejectsNonString(t *testing.T) {
	const yaml = `
## @name {int} - Cluster name.
`
	tmp := writeTempFile(yaml)
	defer os.Remove(tmp)

	_, err := Parse(tmp)
	require.ErrorContains(t, err, "@name must be declared as {string}")
}

// TestParseNameRejectsDuplicate keeps the resource-name schema single-valued,
// so a second declaration is an error rather than a silent overwrite.
func TestParseNameRejectsDuplicate(t *testing.T) {
	const yaml = `
## @name {string} - First.
## @name {string} - Second.
`
	tmp := writeTempFile(yaml)
	defer os.Remove(tmp)

	_, err := Parse(tmp)
	require.ErrorContains(t, err, "duplicate @name annotation")
}

// TestNameSchemaEmitted checks the generated schema publishes the declaration
// at the root, beside "properties" and not inside it.
func TestNameSchemaEmitted(t *testing.T) {
	const yaml = `
## @name {string} - Cluster name. Worker pools render as "kubernetes-nodes-<name>-<pool>".
## @maxLength 32

## @param {string} storageClass - Default StorageClass.
storageClass: replicated
`
	raw := schemaBytesFor(t, yaml)

	var schema map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &schema))

	nameSchema, ok := schema["x-cozystack-name"].(map[string]any)
	require.True(t, ok, "schema must carry x-cozystack-name at the root: %s", raw)
	require.Equal(t, "string", nameSchema["type"])
	require.Equal(t, float64(32), nameSchema["maxLength"])
	require.Equal(t,
		`Cluster name. Worker pools render as "kubernetes-nodes-<name>-<pool>".`,
		nameSchema["description"])

	props, ok := schema["properties"].(map[string]any)
	require.True(t, ok)
	require.NotContains(t, props, "name")
}

// TestNameSchemaAbsentWithoutDirective guards the byte-for-byte output of
// every chart that does not declare a name schema.
func TestNameSchemaAbsentWithoutDirective(t *testing.T) {
	const yaml = `
## @param {string} storageClass - Default StorageClass.
storageClass: replicated
`
	var schema map[string]any
	require.NoError(t, json.Unmarshal([]byte(schemaBytesFor(t, yaml)), &schema))
	require.NotContains(t, schema, "x-cozystack-name")
}

// TestNameSchemaPatternAndMinLength covers the other string constraints the
// shared accumulator already supports, so they reach the schema too.
func TestNameSchemaPatternAndMinLength(t *testing.T) {
	const yaml = `
## @name {string} - Tenant name.
## @minLength 2
## @pattern ^[a-z][a-z0-9]*$
`
	var schema map[string]any
	require.NoError(t, json.Unmarshal([]byte(schemaBytesFor(t, yaml)), &schema))

	nameSchema, ok := schema["x-cozystack-name"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, float64(2), nameSchema["minLength"])
	require.Equal(t, `^[a-z][a-z0-9]*$`, nameSchema["pattern"])
	require.NotContains(t, nameSchema, "maxLength")
}
