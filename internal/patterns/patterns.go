// Package patterns provides shared regex patterns for annotation parsing.
package patterns

// DefaultValuePattern matches default values in annotations.
// Accepts: quoted strings, JSON objects/arrays, booleans, numbers (with decimals), null, or simple tokens.
// Examples: "foo bar", 'text', {"a":1}, [1,2], true, false, null, -3.5, 42, simpleToken
const DefaultValuePattern = `(?:"[^"]*"|'[^']*'|\{[^}]*\}|\[[^\]]*\]|true|false|null|-?\d+(?:\.\d+)?|\S+)`

// DottedPathPattern matches parameter names with optional dotted paths.
// Supports paths like "name", "qdrant.replicaCount", "[optional.param]".
// Path must start with word char, dots must be followed by word chars (no .foo, foo., or a..b).
// The pattern includes optional square brackets for optional parameters.
const DottedPathPattern = `\[?\w+(?:\.\w+)*\]?`

// ParamPattern is the full regex pattern for @param annotations.
// Groups: 1=type, 2=name (with optional brackets), 3=default value, 4=description
const ParamPattern = `^#{1,}\s+@param\s+\{([^}]+)\}\s+(` + DottedPathPattern + `)(?:=(` + DefaultValuePattern + `))?(?:\s+-\s+(.*))?$`

// FieldPattern is the full regex pattern for @field/@property annotations.
// Groups: 1=type, 2=name (with optional brackets), 3=default value, 4=description
const FieldPattern = `^#{1,}\s+@(?:field|property)\s+\{([^}]+)\}\s+(\[?\w+\]?)(?:=(` + DefaultValuePattern + `))?(?:\s+-\s+(.*))?$`

// TypedefPattern is the regex pattern for @typedef annotations.
// Groups: 1=name, 2=description (kind is non-capturing)
const TypedefPattern = `^#{1,}\s+@typedef\s+\{(?:struct|object)\}\s+(\w+)(?:\s+-\s+(.*))?$`

// EnumPattern is the regex pattern for @enum annotations.
// Groups: 1=type, 2=name, 3=description
const EnumPattern = `^#{1,}\s+@enum\s+\{([^}]+)\}\s+(\w+)(?:\s+-\s+(.*))?$`

// EnumValuePattern is the regex pattern for @value annotations.
// Supports hyphens, underscores, dots, and quoted strings.
// Groups vary based on quoting style
const EnumValuePattern = `^#{1,}\s+@value\s+("([^"]+)"|'([^']+)'|([-\w.]+))(?:\s+-\s+(.*))?$`

// SectionPattern is the regex pattern for @section annotations.
// Groups: 1=section name
const SectionPattern = `^#{1,}\s+@section\s+(.*)$`
