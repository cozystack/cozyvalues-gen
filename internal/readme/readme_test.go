package readme

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
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
## @field metricsStorage.name {string default="5d"} Name
## @field metricsStorage.retentionPeriod {string} Retention
metricsStorages:
- name: shortterm
  retentionPeriod: "3d"
- name: longterm
  retentionPeriod: "14d"
`
	table := renderTableFromValues(t, yamlContent)
	if !strings.Contains(table, "`metricsStorages`") || !strings.Contains(table, "`[...]`") {
		t.Errorf("expected root array with `[...]`, got:\n%s", table)
	}
	if !strings.Contains(table, "`[]object`") {
		t.Errorf("expected type to be `[]object`, got:\n%s", table)
	}
	if !strings.Contains(table, "`metricsStorages[i].name`") || !strings.Contains(table, "`\"\"`") {
		t.Errorf("expected element name shortterm with [i], got:\n%s", table)
	}
	if !strings.Contains(table, "`metricsStorages[i].retentionPeriod`") || !strings.Contains(table, "`5d`") {
		t.Errorf("expected element retention 5d with [i], got:\n%s", table)
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
	if !strings.Contains(table, "`databases`") || !strings.Contains(table, "`{...}`") {
		t.Errorf("expected databases map with `{...}`, got:\n%s", table)
	}
	if !strings.Contains(table, "`map[string]object`") {
		t.Errorf("expected type to be `map[string]object`, got:\n%s", table)
	}
	if !strings.Contains(table, "`databases[name].roles.admin`") || !strings.Contains(table, "`[]`") {
		t.Errorf("expected nested roles.admin array without users, got:\n%s", table)
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
	require.Equal(t, "quantity", normalizeType("quantity"))
	require.Equal(t, "*quantity", normalizeType("*quantity"))
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

func TestIntegerLists(t *testing.T) {
	yamlContent := `
## @param intList {[]int} A required list of integers, empty.
intList:

## @param intListSingle {[]int} A list of integers with one value.
intListSingle:
  - 80

## @param intListMultiple {[]int} A list of integers with one value.
intListMultiple:
  - 80
  - 8080

## @param intListNullable {[]*int} A nullable list of integers, empty.
intListNullable:

## @param intListNullableSingle {[]*int} A nullable list of integers with one value.
intListNullableSingle:
  - 80

## @param intListNullableMultiple {[]*int} A nullable list of integers with multiple values.
intListNullableMultiple:
  - 80
  - 8080
`
	table := renderTableFromValues(t, yamlContent)
	if !strings.Contains(table, "`intList`") || !strings.Contains(table, "`[]`") {
		t.Errorf("expected empty intList as [] got:\n%s", table)
	}
	if !strings.Contains(table, "`intListSingle`") || !strings.Contains(table, "`[80]`") {
		t.Errorf("expected intListSingle with 80 got:\n%s", table)
	}
	if !strings.Contains(table, "`intListMultiple`") || !strings.Contains(table, "`[80, 8080]`") {
		t.Errorf("expected intListMultiple with 2 values got:\n%s", table)
	}
	if !strings.Contains(table, "`intListNullableMultiple`") || !strings.Contains(table, "`[80, 8080]`") {
		t.Errorf("expected intListNullableMultiple with 2 values got:\n%s", table)
	}
}

func TestStringLists(t *testing.T) {
	yamlContent := `
## @param stringList {[]string} A required list of strings, empty.
stringList:

## @param stringListSingle {[]string} A required list of strings with one value.
stringListSingle:
  - "user1"

## @param stringListMultiple {[]string} A required list of strings with multiple values.
stringListMultiple:
  - "user1"
  - "user2"

## @param stringListNullable {[]*string} A nullable list of strings, empty.
stringListNullable:

## @param stringListNullableSingle {[]*string} A nullable list of strings with one value.
stringListNullableSingle:
  - "user1"

## @param stringListNullableMultiple {[]*string} A nullable list of strings with multiple values.
stringListNullableMultiple:
  - "user1"
  - "user2"
`
	table := renderTableFromValues(t, yamlContent)
	if !strings.Contains(table, "`stringListMultiple`") || !strings.Contains(table, "`[user1, user2]`") {
		t.Errorf("expected stringListMultiple with 2 values got:\n%s", table)
	}
	if !strings.Contains(table, "`stringListNullableSingle`") || !strings.Contains(table, "`[user1]`") {
		t.Errorf("expected stringListNullableSingle with 1 value got:\n%s", table)
	}
}

func TestBasicTypes(t *testing.T) {
	yamlContent := `
## @param testInt {int} Integer variable
testInt:
## @param testIntDefault {int} Integer variable with default value
testIntDefault: 10
## @param testBoolTrue {bool} Boolean variable, defaults to true
testBoolTrue: true
## @param testStringDefault {string} String variable with default value
testStringDefault: "hello"
`
	table := renderTableFromValues(t, yamlContent)
	if !strings.Contains(table, "`testInt`") || !strings.Contains(table, "`0`") {
		t.Errorf("expected testInt as 0 got:\n%s", table)
	}
	if !strings.Contains(table, "`testIntDefault`") || !strings.Contains(table, "`10`") {
		t.Errorf("expected testIntDefault as 10 got:\n%s", table)
	}
	if !strings.Contains(table, "`testBoolTrue`") || !strings.Contains(table, "`true`") {
		t.Errorf("expected testBoolTrue as true got:\n%s", table)
	}
	if !strings.Contains(table, "`testStringDefault`") || !strings.Contains(table, "`hello`") {
		t.Errorf("expected testStringDefault as hello got:\n%s", table)
	}
}

func TestQuantities(t *testing.T) {
	yamlContent := `
## @param quantityDefaultCpuShare {quantity} A quantity default with vCPU share.
quantityDefaultCpuShare: "100m"
## @param quantityNullableDefaultRam {*quantity} A nullable quantity with a default RAM size.
quantityNullableDefaultRam: "500MiB"
`
	table := renderTableFromValues(t, yamlContent)
	if !strings.Contains(table, "`quantityDefaultCpuShare`") || !strings.Contains(table, "`100m`") {
		t.Errorf("expected quantityDefaultCpuShare as 100m got:\n%s", table)
	}
	if !strings.Contains(table, "`quantityNullableDefaultRam`") || !strings.Contains(table, "`500MiB`") {
		t.Errorf("expected quantityNullableDefaultRam as 500MiB got:\n%s", table)
	}
}

func TestComplexObjectFields(t *testing.T) {
	yamlContent := `
## @param foo {foo} Configuration for foo
## @field foo.db {fooDB} Field with custom type declared locally
## @field fooDB.volume {string} Sub-field declared with path relative to custom type
foo:
  db:
    volume: "10Gi"
`
	table := renderTableFromValues(t, yamlContent)
	if !strings.Contains(table, "`foo.db.volume`") || !strings.Contains(table, "`10Gi`") {
		t.Errorf("expected foo.db.volume as 10Gi got:\n%s", table)
	}
}

func TestTemplateVar(t *testing.T) {
	yamlContent := `
## @param test {test} Test variable
test:
`
	table := renderTableFromValues(t, yamlContent)
	if !strings.Contains(table, "`test`") || !strings.Contains(table, "`{}`") {
		t.Errorf("expected test as {} got:\n%s", table)
	}
}

func TestNullableDefaultsRenderedAsNull(t *testing.T) {
	yamlContent := `
## @param testIntNullable {*int} Integer variable, nullable
testIntNullable:

## @param testBoolNullable {*bool} Boolean variable, nullable
testBoolNullable:

## @param testStringNullable {*string} String variable, nullable
testStringNullable:

## @param quantityNullable {*quantity} A nullable quantity value.
quantityNullable:

## @param intListNullable {*[]int} A nullable list of integers, empty.
intListNullable:

## @param stringListNullable {*[]string} A nullable list of strings, empty.
stringListNullable:
`
	table := renderTableFromValues(t, yamlContent)
	expected := []string{
		"`testIntNullable`", "`null`",
		"`testBoolNullable`", "`null`",
		"`testStringNullable`", "`null`",
		"`quantityNullable`", "`null`",
		"`intListNullable`", "`null`",
		"`stringListNullable`", "`null`",
	}
	for i := 0; i < len(expected); i += 2 {
		if !strings.Contains(table, expected[i]) || !strings.Contains(table, expected[i+1]) {
			t.Errorf("expected %s to be rendered as %s, got:\n%s", expected[i], expected[i+1], table)
		}
	}
}

func TestValidationUnknownField(t *testing.T) {
	yamlContent := `
## @param foo {foo} Foo object
## @field foo.db {fooDB} Database
## @field fooDB.size {string} Size
foo:
  db:
    sie: 10Gi
`
	path := writeTempFile(t, yamlContent)
	defer os.Remove(path)
	vals, _ := createValuesObject(path)
	meta, _ := parseMetadataComments(path)
	var params []ParamMeta
	for _, s := range meta.Sections {
		params = append(params, s.Parameters...)
	}
	err := validateValues(params, typeFields, vals)
	if err == nil || !strings.Contains(err.Error(), "foo.db.sie") {
		t.Errorf("expected error about unknown field foo.db.sie, got: %v", err)
	}
}

func TestNestedFieldAnnotations(t *testing.T) {
	yamlContent := `
## @section Alerta configuration
## @param alerta {alerta} Configuration for Alerta
## @field alerta.storage {string} Persistent Volume size for alerta database
## @field alerta.storageClassName {string} StorageClass used to store the data
## @field alerta.resources {*alertaResources} Resources configuration for alerta
## @field alertaResources.limits {*resources} Resources limits for alerta
## @field alertaResources.requests {*resources} Resources requests for alerta
alerta:
  storage: 10Gi
  storageClassName: ""
  resources:
    limits:
      cpu: "1"
      memory: 1Gi
    requests:
      cpu: 100m
      memory: 256Mi
  alerts:
    ## @field alerta.alerts {alerts} Configuration for alerts
    ## @field alerts.telegram {telegramAlerts} Configuration for Telegram alerts
    telegram:
      ## @field telegramAlerts.token {string} Telegram token for your bot
      ## @field telegramAlerts.chatID {string} Specify multiple ID's separated by comma
      ## @field telegramAlerts.disabledSeverity {string} List of severity without alerts
      token: "abc"
      chatID: "123"
      disabledSeverity: "warn"
`
	table := renderTableFromValues(t, yamlContent)
	if !strings.Contains(table, "`alerta.alerts.telegram.token`") || !strings.Contains(table, "`abc`") {
		t.Errorf("expected nested telegram token field with abc got:\n%s", table)
	}
	if !strings.Contains(table, "`alerta.alerts.telegram.chatID`") || !strings.Contains(table, "`123`") {
		t.Errorf("expected nested telegram chatID field with 123 got:\n%s", table)
	}
	if !strings.Contains(table, "`alerta.alerts.telegram.disabledSeverity`") || !strings.Contains(table, "`warn`") {
		t.Errorf("expected nested telegram disabledSeverity field with warn got:\n%s", table)
	}
}

func TestNormalizeTypePrimitives(t *testing.T) {
	cases := map[string]string{
		"string":             "string",
		"quantity":           "quantity",
		"duration":           "duration",
		"time":               "time",
		"object":             "object",
		"*quantity":          "*quantity",
		"[]quantity":         "[]quantity",
		"map[string]time":    "map[string]time",
		"map[string]Unknown": "map[string]object", // non-primitive fallback
	}

	for in, want := range cases {
		if got := normalizeType(in); got != want {
			t.Fatalf("normalizeType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPointerObjectRendersAsEmptyObject(t *testing.T) {
	yaml := `
## @param resources {*resources} Resource configuration for etcd
## @field resources.cpu {*quantity} CPU
## @field resources.memory {*quantity} Memory
resources:
  cpu: 4
  memory: 1Gi
`
	table := renderTableFromValues(t, yaml)
	if !strings.Contains(table, "`resources`") || !strings.Contains(table, "`{}`") {
		t.Fatalf("pointer-to-object param should render `{}`, got:\n%s", table)
	}
}

func TestParamWithoutDescription(t *testing.T) {
	yaml := `
## @param foo {string}
foo: ""
`
	path := writeTempFile(t, yaml)
	defer os.Remove(path)

	meta, err := parseMetadataComments(path)
	require.NoError(t, err)
	require.Len(t, meta.Sections, 1)
	require.Equal(t, "foo", meta.Sections[0].Parameters[0].Name)
	require.Equal(t, "", meta.Sections[0].Parameters[0].Description)
}

func TestAliasObjectDisplaysEmptyBraces(t *testing.T) {
	yaml := `
## @param foaao {asdaa}
## @field foaao.foaa {int64} some field
foaao:
  aaa: 1
`
	table := renderTableFromValues(t, yaml)
	require.Contains(t, table, "`foaao`")
	require.Contains(t, table, "`{}`", "alias object should render {} not raw JSON")
}
