# cozyvalues-gen

Tiny helper that turns **JSDoc-annotated `values.yaml`** files into:

1. **Go structs** – strongly typed, IDE-friendly.
2. **CustomResourceDefinitions (CRD)** – produced by `controller-gen`.
3. **values.schema.json** – OpenAPI schema extracted from the CRD.
4. **README.md** – auto-updated `## Parameters` section.

The chain "_structs → CRD → OpenAPI_" re-uses the same code that Kubernetes itself relies on, so you get **maximum type compatibility** for free.

## Syntax Overview

Uses **JSDoc-like syntax** for clean, readable type annotations:

---

## How it works

```mermaid
graph LR
    A[values.yaml] --> B(Go structs)
    B --> C[CRD YAML]
    C --> D[values.schema.json]
    A --> E[README.md]
```

- Annotate your `values.yaml` (see [examples](examples) for the exact syntax).
- Run cozyvalues-gen; it parses the comments and spits out Go code.
- controller-gen compiles that code, emitting a CRD.
- The tool trims the CRD down to a Helm-compatible schema.

### Usage (one-liner)

```
cozyvalues-gen \
  --values values.yaml \
  --schema values.schema.json
```

See `cozyvalues-gen` -h for all flags.

## Installation

### Homebrew (macOS and Linux)

```bash
brew tap cozystack/tap
brew install cozyvalues-gen
```

> **Note**: This uses our [incubating tap](https://github.com/cozystack/homebrew-tap). The formula will be submitted to official homebrew/core once it reaches 75+ stars and stable adoption.

### Using `go install`

Requires Go toolchain installed and accessible via `$PATH`.

```console
go install github.com/cozystack/cozyvalues-gen@latest
```

The binary lands in `$(go env GOPATH)/bin` (usually `~/go/bin`). Make sure that directory is on your `$PATH`.

### Download a pre‑built release binary

Head over to [https://github.com/cozystack/cozyvalues-gen/releases](https://github.com/cozystack/cozyvalues-gen/releases), grab the archive for your OS/arch, unpack it somewhere on your `$PATH`.

## Annotation Tags

### @param
Defines a top-level parameter:
```yaml
## @param {type} name - description
## @param {string} hostname - Server hostname
hostname: "example.com"

## @param {int} replicas - Number of replicas
replicas: 3
```

### @typedef
Defines a custom type (struct):
```yaml
## @typedef {struct} Database - Database configuration
## @field {string} host - Database host
## @field {int} port - Database port

## @param {Database} database - Database config
database:
  host: localhost
  port: 5432
```

### @field / @property
Defines fields within a typedef (@property is a synonym):
```yaml
## @typedef {struct} User
## @field {string} name - User name
## @field {string} [email] - Email (optional, with omitempty)
## @field {int} age=18 - Age with default value
```

### @enum
Defines an enumeration type:
```yaml
## @enum {string} Size - Size preset
## @value small
## @value medium
## @value large

## @param {Size} size="medium" - Selected size
size: "medium"
```

### Special Syntax

- **Optional fields**: `[fieldName]` adds `omitempty` to JSON tag
- **Pointers**: `{*type}` or `{?type}` (equivalent)
- **Default values**: `fieldName=value` or `fieldName="value"`

### Validation constraints

Stack these tags on the line(s) after a `@param` or `@field`. They emit kubebuilder validation markers on the generated Go struct and JSON-schema entries in `values.schema.json`.

```yaml
## @param {int} port - Port number
## @minimum 1
## @maximum 65535
port: 8080

## @param {string} name - DNS-compatible name
## @minLength 1
## @maxLength 63
## @pattern ^[a-z0-9-]+$
name: ""

## @param {[]string} tags - Tag list
## @minItems 1
## @maxItems 10
tags: []

## @param {string} storageClass - StorageClass used to store the data.
## @immutable
storageClass: ""
```

Available constraints: `@minimum`, `@maximum`, `@exclusiveMinimum`, `@exclusiveMaximum`, `@minLength`, `@maxLength`, `@pattern`, `@minItems`, `@maxItems`, `@immutable`.

**Placement matters.** Every constraint attaches to the most-recent `@param` or `@field` declaration. Place constraints **immediately after** the `@param`/`@field` header and **before** the YAML value line. If a constraint appears after the YAML value (e.g. floating between two `@param` blocks), it still attaches to the preceding `@param` for backwards compatibility, but `cozyvalues-gen` prints

```
cozyvalues-gen: <file>:<line>: warning: constraint on this line still attaches to "<field>" across an intervening YAML value line; move it to immediately after the @param/@field header for clarity
```

so the misplacement is visible in `make generate` output. The warning never fails the build — fix the placement to silence it.

`@immutable` is a flag (no argument). It emits a CEL `XValidation` rule (`self == oldSelf`) at the property level. **Caveat**: per Kubernetes `x-kubernetes-validations` semantics, a property-level rule is evaluated only when the property is present in both `oldSelf` and `self`. For optional/omitempty/pointer fields the rule therefore enforces *immutable once set* rather than *immutable from creation* — the field can still be added on a subsequent update if it was absent on create. To enforce *immutable from creation* make the field required (omit `[name]` brackets and pointer markers).

### Vendor extensions (`@x-...`)

Attach an arbitrary OpenAPI/JSON-Schema **vendor extension keyword** (any key starting with `x-`) to a `@param` or `@field`. The directive name carries the keyword: `## @x-<keyword> <value>` emits `x-<keyword>: <value>` onto that property in the generated JSON schema. The value is parsed as YAML flow, so objects, arrays and scalars are all supported. This is a pure passthrough — no keyword is interpreted or hardcoded.

Vendor extensions are emitted into the **JSON schema only**; they never appear in the generated Go types (extensions are schema metadata, not data shapes).

```yaml
## @param {string} instanceType - Virtual Machine instance type.
## @x-cozystack-options {source: instancetype}
instanceType: "u1.medium"
```

produces:

```json
"instanceType": {
  "type": "string",
  "description": "Virtual Machine instance type.",
  "x-cozystack-options": { "source": "instancetype" }
}
```

When declared on a `@typedef` field, the extension propagates everywhere the type is referenced — including `[]Type` (under `items.properties`) and `map[string]Type` (under `additionalProperties.properties`):

```yaml
## @typedef {struct} GPU - GPU device configuration.
## @field {string} name - The name of the GPU resource to attach.
## @x-cozystack-options {source: gpu}
```

Multiple `@x-*` lines may be attached to a single field (each with a distinct key). Values may be objects (`{a: 1}`), arrays (`[{a: 1}]`) or scalars (`42`, `true`, `"text"`). Injected `x-*` keys are appended after the property's existing keys, so adding a vendor extension never reorders the rest of the schema.

## Supported value types

| Annotation token               | Go type                                    | JSON Schema             | Examples                             |
| ------------------------------ | ------------------------------------------ | ----------------------- | ------------------------------------ |
| `string`                       | `string`                                   | string                  | `"hello"`                            |
| `bool`                         | `bool`                                     | boolean                 | `true`                               |
| `int`, `int32`, `int64`        | matching signed integer                    | number                  | `42`                                 |
| `float32`, `float64`           | matching float                             | number                  | `3.14`                               |
| `quantity`                     | `resource.Quantity`                        | string                  | `"500Mi"`, `"100m"`, `"4Gi"`         |
| `duration`                     | `metav1.Duration`                          | string                  | `"5m"`, `"1h30m"`                    |
| `time`                         | `metav1.Time`                              | string (RFC 3339)       | `"2025-08-07T12:00:00Z"`             |
| `object`                       | `k8sRuntime.RawExtension`                  | any JSON/YAML           | `{"aaa": 123, "foo": "bar"}`         |
| `emptyobject`                  | empty struct (`struct{}`) **–** no fields  | `{}`                    | `{}`                                 |
| `intOrString`                  | `intstr.IntOrString`                       | `anyOf` int or string   | `100`, `"100"` (PostgreSQL params)   |
| `*<primitive>`                 | pointer to that primitive (`nil` allowed)  | primitive or `null`     | `"asd"`, `null` …                    |
| `<CustomType>`                 | generated struct                           | object                  | declared from `@field` annotations   |
| `*<CustomType>`                | pointer to generated struct                | object or `null`        | `null`                               |
| `[]<T>`                        | slice / YAML sequence                      | list                    | `[]string`, `[]*int`, `[]CustomType` |
| `map[string]<T>`               | map / YAML mapping                         | object                  | keys are always **strings**          |

### String-format aliases

These tokens map to a plain `string` field, **plus** `format: "<alias>"` in the OpenAPI schema:

| `format`        | Example value                          |
| --------------- | -------------------------------------- |
| `bsonobjectid`  | `507f1f77bcf86cd799439011`             |
| `uri`           | `https://grafana.example.com`          |
| `email`         | `user@example.com`                     |
| `hostname`      | `db.internal`                          |
| `ipv4`          | `192.168.0.10`                         |
| `ipv6`          | `2001:db8::1`                          |
| `cidr`          | `10.0.0.0/24`                          |
| `mac`           | `00:1A:2B:3C:4D:5E`                    |
| `uuid`, `uuid3`, `uuid4`, `uuid5` | `550e8400-e29b-41d4-a716-446655440000` |
| `isbn`, `isbn10`, `isbn13`        | `9783161484100`      |
| `creditcard`    | `4242 4242 4242 4242`                  |
| `ssn`           | `123-45-6789`                          |
| `hexcolor`      | `#ff8800`                              |
| `rgbcolor`      | `rgb(255,136,0)`                       |
| `byte`          | `SGVsbG8=` (base64)                    |
| `password`      | *(hidden by most UIs)*                 |
| `date`          | `2025-08-07`                           |

> **Tip:** use any alias exactly like a regular type, e.g.  
> ```yaml
> ## @param apiURL {uri} External URL of the API
> apiURL: ""
> ```

---

Created for the Cozystack project. 🚀
