package readme

import (
	"os"
	"strings"
	"testing"
)

func writeTempFile(t *testing.T, content string) string {
	f, err := os.CreateTemp("", "readme-test-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return f.Name()
}

func renderTableFromValues(t *testing.T, valuesYaml string) string {
	path := writeTempFile(t, valuesYaml)
	defer os.Remove(path)
	vals, err := createValuesObject(path)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := parseMetadataComments(path)
	if err != nil {
		t.Fatal(err)
	}
	combine(vals)
	var params []ParamMeta
	for _, s := range meta.Sections {
		params = append(params, s.Parameters...)
	}
	rendered := buildParamsToRender(params)
	return markdownTable(rendered)
}

func TestObjectWithNestedFields(t *testing.T) {
	yamlContent := `
## @param backup {backup} Backup configuration
## @field backup.enabled {bool} Enable regular backups
## @field backup.schedule {string} Cron schedule for automated backups
backup:
  enabled: false
  schedule: "0 2 * * *"
`
	table := renderTableFromValues(t, yamlContent)
	if !strings.Contains(table, "`backup`") || !strings.Contains(table, "`{}`") {
		t.Errorf("expected backup object with `{}`, got:\n%s", table)
	}
	if !strings.Contains(table, "`backup.enabled`") || !strings.Contains(table, "`false`") {
		t.Errorf("expected nested field with value false:\n%s", table)
	}
	if !strings.Contains(table, "`backup.schedule`") || !strings.Contains(table, "`0 2 * * *`") {
		t.Errorf("expected nested field with cron value:\n%s", table)
	}
}

func TestArrayWithValues(t *testing.T) {
	yamlContent := `
## @param metricsStorages {[]metricsStorage} Metrics storage
## @field metricsStorage.name {string} Name
## @field metricsStorage.retentionPeriod {string} Retention
metricsStorages:
- name: shortterm
  retentionPeriod: "3d"
- name: longterm
  retentionPeriod: "14d"
`
	table := renderTableFromValues(t, yamlContent)
	if !strings.Contains(table, "`metricsStorages`") || !strings.Contains(table, "`[]`") {
		t.Errorf("expected root array with `[]`, got:\n%s", table)
	}
	if !strings.Contains(table, "`metricsStorages[0].name`") || !strings.Contains(table, "`shortterm`") {
		t.Errorf("expected first element name shortterm, got:\n%s", table)
	}
	if !strings.Contains(table, "`metricsStorages[1].retentionPeriod`") || !strings.Contains(table, "`14d`") {
		t.Errorf("expected second element retention 14d, got:\n%s", table)
	}
}

func TestEmptyArrayWithZeroValues(t *testing.T) {
	yamlContent := `
## @param logsStorages {[]logsStorage} Logs storage
## @field logsStorage.name {string} Name
## @field logsStorage.retentionPeriod {string} Retention
logsStorages: []
`
	table := renderTableFromValues(t, yamlContent)
	if !strings.Contains(table, "`logsStorages`") || !strings.Contains(table, "`[]`") {
		t.Errorf("expected logsStorages root array with `[]`, got:\n%s", table)
	}
	if !strings.Contains(table, "`logsStorages[i].name`") || !strings.Contains(table, "`\"\"`") {
		t.Errorf("expected placeholder element with empty string, got:\n%s", table)
	}
}

func TestMapWithNestedObjects(t *testing.T) {
	yamlContent := `
## @param databases {map[string]database} Databases
## @field database.roles {*databaseRoles} Roles
## @field databaseRoles.admin {[]string} Admin users
databases:
  myapp:
    roles:
      admin: ["user1","user2"]
`
	table := renderTableFromValues(t, yamlContent)
	if !strings.Contains(table, "`databases`") || !strings.Contains(table, "`{}`") {
		t.Errorf("expected databases map with `{}`, got:\n%s", table)
	}
	if !strings.Contains(table, "`databases[myapp].roles.admin`") || !strings.Contains(table, "`[\"user1\",\"user2\"]`") {
		t.Errorf("expected nested roles.admin array with users, got:\n%s", table)
	}
}

func TestNestedObjectDefaults(t *testing.T) {
	yamlContent := `
## @param postgresql {postgresql} PostgreSQL
## @field postgresql.parameters {postgresqlParameters} Parameters
## @field postgresqlParameters.max_connections {int} Max connections
postgresql: {}
`
	table := renderTableFromValues(t, yamlContent)
	if !strings.Contains(table, "`postgresql.parameters.max_connections`") || !strings.Contains(table, "`0`") {
		t.Errorf("expected zero-value for int field, got:\n%s", table)
	}
}

func TestNormalizeQuantityTypes(t *testing.T) {
	if normalizeType("quantity") != "string" {
		t.Errorf("expected quantity to normalize to string")
	}
	if normalizeType("*quantity") != "*string" {
		t.Errorf("expected *quantity to normalize to *string")
	}
}

func TestStringEnumStripped(t *testing.T) {
	if normalizeType("string enum:\"nano,micro\"") != "string" {
		t.Errorf("expected enum type to be stripped to string")
	}
}

func TestEmptyStringsRenderedAsQuoted(t *testing.T) {
	if valueString("", true, "string") != "`\"\"`" {
		t.Errorf("expected empty string to be rendered as `\"\"`")
	}
}
