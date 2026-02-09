// Package patterns provides shared regex patterns for annotation parsing.
package patterns

// DefaultValuePattern matches default values in annotations.
// Accepts: quoted strings, JSON objects/arrays, booleans, numbers (with decimals), null, or simple tokens.
// Examples: "foo bar", 'text', {"a":1}, [1,2], true, false, null, -3.5, 42, simpleToken
const DefaultValuePattern = `(?:"[^"]*"|'[^']*'|\{[^}]*\}|\[[^\]]*\]|true|false|null|-?\d+(?:\.\d+)?|\S+)`

// ParamPattern is the full regex pattern for @param annotations.
// Groups: 1=type, 2=name (with optional brackets), 3=default value, 4=description
const ParamPattern = `^#{1,}\s+@param\s+\{([^}]+)\}\s+(\[?\w+\]?)(?:=(` + DefaultValuePattern + `))?(?:\s+-\s+(.*))?$`

// FieldPattern is the full regex pattern for @field/@property annotations.
// Groups: 1=type, 2=name (with optional brackets), 3=default value, 4=description
// Note: Fields do NOT support dotted paths - they belong to a parent @typedef.
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

// Validation constraint patterns

// MinimumPattern matches @minimum annotations with numeric value.
// Groups: 1=numeric value (int or float, possibly negative)
const MinimumPattern = `^#{1,}\s+@minimum\s+(-?\d+(?:\.\d+)?)\s*$`

// MaximumPattern matches @maximum annotations with numeric value.
// Groups: 1=numeric value (int or float, possibly negative)
const MaximumPattern = `^#{1,}\s+@maximum\s+(-?\d+(?:\.\d+)?)\s*$`

// ExclusiveMinimumPattern matches @exclusiveMinimum flag annotation.
// No groups - presence indicates true
const ExclusiveMinimumPattern = `^#{1,}\s+@exclusiveMinimum\s*$`

// ExclusiveMaximumPattern matches @exclusiveMaximum flag annotation.
// No groups - presence indicates true
const ExclusiveMaximumPattern = `^#{1,}\s+@exclusiveMaximum\s*$`

// MinLengthPattern matches @minLength annotations with integer value.
// Groups: 1=integer value
const MinLengthPattern = `^#{1,}\s+@minLength\s+(\d+)\s*$`

// MaxLengthPattern matches @maxLength annotations with integer value.
// Groups: 1=integer value
const MaxLengthPattern = `^#{1,}\s+@maxLength\s+(\d+)\s*$`

// RegexPatternPattern matches @pattern annotations with regex value.
// Groups: 1=regex pattern (everything after @pattern and whitespace)
const RegexPatternPattern = `^#{1,}\s+@pattern\s+(.+)$`

// MinItemsPattern matches @minItems annotations with integer value.
// Groups: 1=integer value
const MinItemsPattern = `^#{1,}\s+@minItems\s+(\d+)\s*$`

// MaxItemsPattern matches @maxItems annotations with integer value.
// Groups: 1=integer value
const MaxItemsPattern = `^#{1,}\s+@maxItems\s+(\d+)\s*$`
