package openapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/format"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/cozystack/cozyvalues-gen/internal/patterns"
	"go.etcd.io/etcd/version"
	"gopkg.in/yaml.v3"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/controller-tools/pkg/crd"
	crdmarkers "sigs.k8s.io/controller-tools/pkg/crd/markers"
	"sigs.k8s.io/controller-tools/pkg/loader"
	"sigs.k8s.io/controller-tools/pkg/markers"
	sigyaml "sigs.k8s.io/yaml"
)

/* -------------------------------------------------------------------------- */
/*  Annotation parsing                                                         */
/* -------------------------------------------------------------------------- */

// WarnWriter receives diagnostic warnings emitted by Parse. It defaults to
// os.Stderr; tests can swap it out to capture output. The parser is not
// concurrency-safe to begin with (it's a CLI helper, single-pass per file),
// so a package-level writer is fine.
var WarnWriter io.Writer = os.Stderr

func emitWarn(file string, lineNum int, msg string) {
	fmt.Fprintf(WarnWriter, "cozyvalues-gen: %s:%d: warning: %s\n", filepath.Base(file), lineNum, msg)
}

type kind int

const (
	kParam kind = iota
	kField
	kTypedef
	kEnum
)

const (
	aliasQuantity    = "quantity"
	aliasDuration    = "duration"
	aliasTime        = "time"
	aliasObject      = "object"
	aliasEmptyObject = "emptyobject"
	aliasResources   = "resources"
	aliasRequest     = "request"
	aliasLimit       = "limit"
	aliasIntOrString = "intOrString"
)

type Raw struct {
	K           kind
	Path        []string
	TypeExpr    string
	Enums       []string
	DefaultVal  string
	Description string
	OmitEmpty   bool // Field marked with [name] for omitempty

	// Validation constraints
	Minimum          *float64
	Maximum          *float64
	ExclusiveMinimum bool
	ExclusiveMaximum bool
	MinLength        *int64
	MaxLength        *int64
	Pattern          string
	MinItems         *int64
	MaxItems         *int64

	// Immutable, when true, emits a CEL XValidation rule (self == oldSelf)
	// at the property level. CAVEAT: per K8s x-kubernetes-validations
	// semantics the rule only fires when the property is present in both
	// oldSelf and self, so on optional/omitempty/pointer fields it
	// enforces "immutable once set" rather than "immutable from creation".
	// Make the field required (no `[name]` brackets, no pointer) for full
	// "immutable from creation" semantics.
	Immutable bool

	// Extensions holds arbitrary OpenAPI/JSON-Schema vendor extensions
	// (keys starting with "x-") attached via @x-<keyword> directives.
	// These are emitted verbatim into the JSON schema only, never into Go types.
	Extensions map[string]interface{}
}

// JSDoc-like syntax patterns (using shared patterns from internal/patterns)
var (
	reParam     = regexp.MustCompile(patterns.ParamPattern)
	reField     = regexp.MustCompile(patterns.FieldPattern)
	reTypedef   = regexp.MustCompile(patterns.TypedefPattern)
	reEnum      = regexp.MustCompile(patterns.EnumPattern)
	reEnumValue = regexp.MustCompile(patterns.EnumValuePattern)

	// Validation constraint patterns
	reMinimum          = regexp.MustCompile(patterns.MinimumPattern)
	reMaximum          = regexp.MustCompile(patterns.MaximumPattern)
	reExclusiveMinimum = regexp.MustCompile(patterns.ExclusiveMinimumPattern)
	reExclusiveMaximum = regexp.MustCompile(patterns.ExclusiveMaximumPattern)
	reMinLength        = regexp.MustCompile(patterns.MinLengthPattern)
	reMaxLength        = regexp.MustCompile(patterns.MaxLengthPattern)
	rePattern          = regexp.MustCompile(patterns.RegexPatternPattern)
	reMinItems         = regexp.MustCompile(patterns.MinItemsPattern)
	reMaxItems         = regexp.MustCompile(patterns.MaxItemsPattern)
	reImmutable        = regexp.MustCompile(patterns.ImmutablePattern)

	// Vendor extension directive (@x-<keyword>)
	reVendorExtension = regexp.MustCompile(patterns.VendorExtensionPattern)

	allConstraintREs = []*regexp.Regexp{
		reMinimum, reMaximum, reExclusiveMinimum, reExclusiveMaximum,
		reMinLength, reMaxLength, rePattern, reMinItems, reMaxItems,
		reImmutable,
	}
)

// matchesAnyConstraint reports whether the line is recognised by any
// constraint annotation regex. Used by Parse to decide whether to emit a
// "constraint placed after YAML value" warning before the actual matcher
// blocks consume the line. Keeping this in one place avoids drift when a
// new constraint regex is added.
func matchesAnyConstraint(line string) bool {
	for _, re := range allConstraintREs {
		if re.MatchString(line) {
			return true
		}
	}
	return false
}

// additional string-format aliases
// see https://github.com/go-openapi/strfmt/blob/master/README.md
var stringFormats = []string{
	"bsonobjectid",
	"uri",
	"email",
	"hostname",
	"ipv4",
	"ipv6",
	"cidr",
	"mac",
	"uuid",
	"uuid3",
	"uuid4",
	"uuid5",
	"isbn",
	"isbn10",
	"isbn13",
	"creditcard",
	"ssn",
	"hexcolor",
	"rgbcolor",
	"byte",
	"password",
	"date",
}

func isStringFormat(s string) bool {
	for _, f := range stringFormats {
		if s == f {
			return true
		}
	}
	return false
}

func Parse(file string) ([]Raw, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(data), "\n")

	var out []Raw
	var currentEnum *Raw
	var enumValues []string
	var lastAnnotated *Raw // Track last @param or @field to accumulate constraints
	// lastAnnotatedYAMLPassed becomes true the first time a non-comment YAML
	// line is seen while lastAnnotated is still active. Subsequent constraint
	// matches still attach to lastAnnotated (preserving pre-existing
	// behaviour for every constraint family), but emit a warning so the
	// misplacement is observable instead of silent.
	var lastAnnotatedYAMLPassed bool

	// finalizeLastAnnotated appends the last annotated item to output if it exists
	finalizeLastAnnotated := func() {
		if lastAnnotated != nil {
			out = append(out, *lastAnnotated)
			lastAnnotated = nil
			lastAnnotatedYAMLPassed = false
		}
	}

	for i, raw := range lines {
		lineNum := i + 1
		line := strings.TrimSpace(raw)

		// A non-empty YAML-content line marks the boundary between the
		// preceding @param's accumulation window and any trailing text.
		// We don't drop lastAnnotated here (that would silently change
		// behaviour for late @minimum/@maximum/etc. across the codebase);
		// we only flag that subsequent constraint matches are now after
		// the YAML value, so the warning path below can light up.
		if line != "" && !strings.HasPrefix(line, "#") {
			if lastAnnotated != nil {
				lastAnnotatedYAMLPassed = true
			}
		}

		// Check for enum value
		if m := reEnumValue.FindStringSubmatch(line); m != nil && currentEnum != nil {
			var value string
			// Extract value from the appropriate capture group
			if len(m) > 2 && m[2] != "" {
				// Double-quoted value
				value = m[2]
			} else if len(m) > 3 && m[3] != "" {
				// Single-quoted value
				value = m[3]
			} else if len(m) > 4 && m[4] != "" {
				// Unquoted value
				value = m[4]
			} else {
				// Fallback to first group (shouldn't happen, but safe)
				value = m[1]
				value = strings.Trim(value, `"'`)
			}
			enumValues = append(enumValues, value)
			continue
		}

		// Check for validation constraints (apply to lastAnnotated @param or @field).
		// Note: @section and other README-only annotations are not recognized here,
		// so constraints continue to accumulate on the preceding @param/@field.
		// This is intentional — @section is a README concept, not OpenAPI.
		if lastAnnotated != nil {
			paramName := strings.Join(lastAnnotated.Path, ".")
			// If we've already crossed the YAML value line of lastAnnotated,
			// the attachment still goes through (preserving pre-existing
			// behaviour for every constraint family) but emit one warning so
			// the misplacement is observable to the chart author.
			if lastAnnotatedYAMLPassed && matchesAnyConstraint(line) {
				emitWarn(file, lineNum, fmt.Sprintf(
					"constraint on this line still attaches to %q across an intervening YAML value line; move it to immediately after the @param/@field header for clarity",
					paramName,
				))
				lastAnnotatedYAMLPassed = false // only warn once per misplacement run
			}
			if m := reMinimum.FindStringSubmatch(line); m != nil {
				val, err := strconv.ParseFloat(m[1], 64)
				if err != nil {
					return nil, fmt.Errorf("invalid @minimum value %q for %q: %w", m[1], paramName, err)
				}
				lastAnnotated.Minimum = &val
				continue
			}
			if m := reMaximum.FindStringSubmatch(line); m != nil {
				val, err := strconv.ParseFloat(m[1], 64)
				if err != nil {
					return nil, fmt.Errorf("invalid @maximum value %q for %q: %w", m[1], paramName, err)
				}
				lastAnnotated.Maximum = &val
				continue
			}
			if reExclusiveMinimum.MatchString(line) {
				lastAnnotated.ExclusiveMinimum = true
				continue
			}
			if reExclusiveMaximum.MatchString(line) {
				lastAnnotated.ExclusiveMaximum = true
				continue
			}
			if m := reMinLength.FindStringSubmatch(line); m != nil {
				val, err := strconv.ParseInt(m[1], 10, 64)
				if err != nil {
					return nil, fmt.Errorf("invalid @minLength value %q for %q: %w", m[1], paramName, err)
				}
				lastAnnotated.MinLength = &val
				continue
			}
			if m := reMaxLength.FindStringSubmatch(line); m != nil {
				val, err := strconv.ParseInt(m[1], 10, 64)
				if err != nil {
					return nil, fmt.Errorf("invalid @maxLength value %q for %q: %w", m[1], paramName, err)
				}
				lastAnnotated.MaxLength = &val
				continue
			}
			if m := rePattern.FindStringSubmatch(line); m != nil {
				lastAnnotated.Pattern = strings.TrimSpace(m[1])
				continue
			}
			if m := reMinItems.FindStringSubmatch(line); m != nil {
				val, err := strconv.ParseInt(m[1], 10, 64)
				if err != nil {
					return nil, fmt.Errorf("invalid @minItems value %q for %q: %w", m[1], paramName, err)
				}
				lastAnnotated.MinItems = &val
				continue
			}
			if m := reMaxItems.FindStringSubmatch(line); m != nil {
				val, err := strconv.ParseInt(m[1], 10, 64)
				if err != nil {
					return nil, fmt.Errorf("invalid @maxItems value %q for %q: %w", m[1], paramName, err)
				}
				lastAnnotated.MaxItems = &val
				continue
			}
			if reImmutable.MatchString(line) {
				lastAnnotated.Immutable = true
				continue
			}
			if m := reVendorExtension.FindStringSubmatch(line); m != nil {
				key := m[1]
				var value interface{}
				if err := yaml.Unmarshal([]byte(m[2]), &value); err != nil {
					return nil, fmt.Errorf("invalid @%s value %q for %q: %w", key, m[2], paramName, err)
				}
				if lastAnnotated.Extensions == nil {
					lastAnnotated.Extensions = map[string]interface{}{}
				}
				lastAnnotated.Extensions[key] = value
				continue
			}
		}

		// Check for @param
		if m := reParam.FindStringSubmatch(line); m != nil {
			// Finalize previous param and enum
			finalizeLastAnnotated()
			if currentEnum != nil {
				currentEnum.Enums = enumValues
				out = append(out, *currentEnum)
				currentEnum = nil
				enumValues = nil
			}

			typeExpr := strings.TrimSpace(m[1])
			nameRaw := m[2]
			name := strings.Trim(nameRaw, "[]")
			omitEmpty := strings.HasPrefix(nameRaw, "[") && strings.HasSuffix(nameRaw, "]")
			defaultVal := ""
			if len(m) > 3 && m[3] != "" {
				defaultVal = m[3]
			}
			desc := ""
			if len(m) > 4 {
				desc = m[4]
			}

			r := Raw{
				K:           kParam,
				Path:        []string{name},
				TypeExpr:    typeExpr,
				DefaultVal:  defaultVal,
				Description: desc,
				OmitEmpty:   omitEmpty,
			}
			lastAnnotated = &r // Don't append yet, wait for constraints
			continue
		}

		// Check for @typedef
		if m := reTypedef.FindStringSubmatch(line); m != nil {
			// Finalize previous param and enum
			finalizeLastAnnotated()
			if currentEnum != nil {
				currentEnum.Enums = enumValues
				out = append(out, *currentEnum)
				currentEnum = nil
				enumValues = nil
			}

			typeName := m[1]
			desc := ""
			if len(m) > 2 {
				desc = m[2]
			}

			r := Raw{
				K:           kTypedef,
				Path:        []string{typeName},
				TypeExpr:    "struct",
				Description: desc,
			}
			out = append(out, r)
			continue
		}

		// Check for @enum
		if m := reEnum.FindStringSubmatch(line); m != nil {
			// Finalize previous param and enum
			finalizeLastAnnotated()
			if currentEnum != nil {
				currentEnum.Enums = enumValues
				out = append(out, *currentEnum)
			}

			baseType := strings.TrimSpace(m[1])
			enumName := m[2]
			desc := ""
			if len(m) > 3 {
				desc = m[3]
			}

			// Start collecting enum values
			currentEnum = &Raw{
				K:           kEnum,
				Path:        []string{enumName},
				TypeExpr:    baseType,
				Description: desc,
			}
			enumValues = []string{}
			continue
		}

		// Check for @field or @property
		if m := reField.FindStringSubmatch(line); m != nil {
			// Finalize previous param and enum
			finalizeLastAnnotated()
			if currentEnum != nil {
				currentEnum.Enums = enumValues
				out = append(out, *currentEnum)
				currentEnum = nil
				enumValues = nil
			}

			typeExpr := strings.TrimSpace(m[1])
			fieldNameRaw := m[2]
			fieldName := strings.Trim(fieldNameRaw, "[]")
			omitEmpty := strings.HasPrefix(fieldNameRaw, "[") && strings.HasSuffix(fieldNameRaw, "]")
			defaultVal := ""
			if len(m) > 3 && m[3] != "" {
				defaultVal = m[3]
			}
			desc := ""
			if len(m) > 4 {
				desc = m[4]
			}

			// Find the parent type by looking at recent typedefs
			var parentType string
			for i := len(out) - 1; i >= 0; i-- {
				if out[i].K == kTypedef {
					parentType = out[i].Path[0]
					break
				}
			}

			if parentType != "" {
				r := Raw{
					K:           kField,
					Path:        []string{parentType, fieldName},
					TypeExpr:    typeExpr,
					DefaultVal:  defaultVal,
					Description: desc,
					OmitEmpty:   omitEmpty,
				}
				lastAnnotated = &r // Don't append yet, wait for constraints
			}
			continue
		}
	}

	// Finalize any pending param and enum
	finalizeLastAnnotated()
	if currentEnum != nil {
		currentEnum.Enums = enumValues
		out = append(out, *currentEnum)
	}

	return out, nil
}

/* -------------------------------------------------------------------------- */
/*  In-memory tree with implicit types                                         */
/* -------------------------------------------------------------------------- */

type Node struct {
	Name          string
	IsParam       bool
	TypeExpr      string
	Enums         []string
	DefaultVal    string
	HasDefaultVal bool // Set to true when DefaultVal is populated from YAML (even if empty)
	Comment       string
	OmitEmpty     bool
	Parent        *Node
	Child         map[string]*Node
	Order         int

	// Validation constraints
	Minimum          *float64
	Maximum          *float64
	ExclusiveMinimum bool
	ExclusiveMaximum bool
	MinLength        *int64
	MaxLength        *int64
	Pattern          string
	MinItems         *int64
	MaxItems         *int64

	// Immutable, when true, emits a CEL XValidation rule (self == oldSelf)
	// at the property level. CAVEAT: per K8s x-kubernetes-validations
	// semantics the rule only fires when the property is present in both
	// oldSelf and self, so on optional/omitempty/pointer fields it
	// enforces "immutable once set" rather than "immutable from creation".
	// Make the field required (no `[name]` brackets, no pointer) for full
	// "immutable from creation" semantics.
	Immutable bool

	// Extensions holds arbitrary OpenAPI/JSON-Schema vendor extensions
	// (keys starting with "x-"). Emitted into the JSON schema only.
	Extensions map[string]interface{}
}

func newNode(name string, p *Node) *Node {
	return &Node{Name: name, Parent: p, Child: map[string]*Node{}}
}

func ensure(root *Node, name string) *Node {
	if n, ok := root.Child[name]; ok {
		return n
	}
	n := newNode(name, root)
	root.Child[name] = n
	return n
}

// copyConstraints transfers validation constraints from Raw to Node.
func copyConstraints(node *Node, raw *Raw) {
	node.Minimum = raw.Minimum
	node.Maximum = raw.Maximum
	node.ExclusiveMinimum = raw.ExclusiveMinimum
	node.ExclusiveMaximum = raw.ExclusiveMaximum
	node.MinLength = raw.MinLength
	node.MaxLength = raw.MaxLength
	node.Pattern = raw.Pattern
	node.MinItems = raw.MinItems
	node.MaxItems = raw.MaxItems
	node.Immutable = raw.Immutable
	node.Extensions = raw.Extensions
}

func Build(rows []Raw) *Node {
	root := newNode("Config", nil)
	orderCounter := 0
	isPrim := func(s string) bool { return isPrimitive(strings.TrimPrefix(s, "*")) }
	addImplicit := func(name string) {
		if name == "" || isPrim(name) || strings.HasPrefix(name, "[]") || strings.HasPrefix(name, "map[") {
			return
		}
		ensure(root, name)
	}

	for _, r := range rows {
		if r.K == kTypedef {
			// Create a node for the typedef
			cur := ensure(root, r.Path[0])
			cur.Comment = r.Description
			cur.TypeExpr = "struct"
			continue
		}

		if r.K == kEnum {
			// Create a node for the enum with enum values
			cur := ensure(root, r.Path[0])
			cur.Comment = r.Description
			cur.TypeExpr = r.TypeExpr
			cur.Enums = r.Enums
			continue
		}

		if r.K == kParam {
			cur := ensure(root, r.Path[0])
			cur.IsParam = true
			cur.Order = orderCounter
			orderCounter++
			cur.TypeExpr = r.TypeExpr
			cur.Comment = r.Description
			cur.Enums = r.Enums
			cur.OmitEmpty = r.OmitEmpty
			if r.DefaultVal != "" {
				cur.DefaultVal = r.DefaultVal
				cur.HasDefaultVal = true
			}
			copyConstraints(cur, &r)

			// Add implicit types
			te := strings.TrimSpace(r.TypeExpr)
			switch {
			case strings.HasPrefix(te, "[]"):
				base := strings.TrimPrefix(strings.TrimSpace(te[2:]), "*")
				addImplicit(base)
			case strings.HasPrefix(te, "map[") && strings.Contains(te, "]"):
				idx := strings.Index(te, "]")
				base := strings.TrimPrefix(strings.TrimSpace(te[idx+1:]), "*")
				addImplicit(base)
			default:
				addImplicit(strings.TrimPrefix(te, "*"))
			}
			continue
		}

		if r.K == kField {
			// Field belongs to a type
			if len(r.Path) < 2 {
				continue
			}
			parentName := r.Path[0]
			fieldName := r.Path[1]

			parent := ensure(root, parentName)
			field := ensure(parent, fieldName)
			field.TypeExpr = r.TypeExpr
			field.Comment = r.Description
			field.Enums = r.Enums
			field.OmitEmpty = r.OmitEmpty
			if r.DefaultVal != "" {
				field.DefaultVal = r.DefaultVal
				field.HasDefaultVal = true
			}
			copyConstraints(field, &r)

			// Add implicit types
			te := strings.TrimSpace(r.TypeExpr)
			switch {
			case strings.HasPrefix(te, "[]"):
				base := strings.TrimPrefix(strings.TrimSpace(te[2:]), "*")
				addImplicit(base)
			case strings.HasPrefix(te, "map[") && strings.Contains(te, "]"):
				idx := strings.Index(te, "]")
				base := strings.TrimPrefix(strings.TrimSpace(te[idx+1:]), "*")
				addImplicit(base)
			default:
				addImplicit(strings.TrimPrefix(te, "*"))
			}
		}
	}
	return root
}

/* -------------------------------------------------------------------------- */
/*  Generator helpers                                                          */
/* -------------------------------------------------------------------------- */

type gen struct {
	pkg         string
	groupName   string
	versionName string
	buf         bytes.Buffer
	imp         map[string]string
	ff          map[string]bool
	def         map[string]bool
}

// NewGen creates a new generator instance
func NewGen(pkg, groupName, versionName string) *gen {
	return &gen{pkg: pkg, groupName: groupName, versionName: versionName}
}

func (g *gen) addImpAlias(path, alias string) {
	if g.imp == nil {
		g.imp = map[string]string{}
	}
	if _, ok := g.imp[path]; !ok {
		g.imp[path] = alias
	}
}

func (g *gen) addImp(path string) { g.addImpAlias(path, "") }

func isPrimitive(t string) bool {
	if isStringFormat(t) {
		return true
	}
	switch t {
	case "string", "bool", "int", "int32", "int64", "float32", "float64",
		aliasQuantity, aliasDuration, aliasTime,
		aliasObject, aliasResources, aliasRequest, aliasLimit, aliasIntOrString:
		return true
	default:
		return false
	}
}

func camel(in string) string {
	need := true
	var b strings.Builder
	for _, r := range in {
		if r == '_' || r == '-' {
			need = true
			continue
		}
		if need {
			b.WriteRune(unicode.ToUpper(r))
			need = false
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (g *gen) resolve(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "string"
	}
	if isStringFormat(raw) {
		return "string"
	}

	if strings.HasPrefix(raw, "[]") {
		base := strings.TrimPrefix(strings.TrimSpace(raw[2:]), "*")
		return "[]" + g.resolve(base)
	}
	if strings.HasPrefix(raw, "map[") && strings.Contains(raw, "]") {
		idx := strings.Index(raw, "]")
		base := strings.TrimPrefix(strings.TrimSpace(raw[idx+1:]), "*")
		return "map[string]" + g.resolve(base)
	}

	switch raw {
	case aliasQuantity:
		g.addImpAlias("k8s.io/apimachinery/pkg/api/resource", "resource")
		return "resource.Quantity"
	case aliasDuration:
		g.addImpAlias("k8s.io/apimachinery/pkg/apis/meta/v1", "metav1")
		return "metav1.Duration"
	case aliasTime:
		g.addImpAlias("k8s.io/apimachinery/pkg/apis/meta/v1", "metav1")
		return "metav1.Time"
	case aliasObject:
		g.addImpAlias("k8s.io/apimachinery/pkg/runtime", "k8sRuntime")
		return "k8sRuntime.RawExtension"
	case aliasIntOrString:
		g.addImpAlias("k8s.io/apimachinery/pkg/util/intstr", "intstr")
		return "intstr.IntOrString"
	}

	// context-aware resolution for well-known object-ish aliases
	if raw == "resources" || raw == "request" || raw == "limit" {
		if g.def[raw] || g.def[camel(raw)] {
			c := camel(raw)
			if c == "Config" || c == "ConfigSpec" {
				return "Values" + c
			}
			return c
		}
		g.addImpAlias("k8s.io/apimachinery/pkg/runtime", "k8sRuntime")
		return "k8sRuntime.RawExtension"
	}

	if isPrimitive(raw) || raw == "any" {
		return raw
	}
	if idx := strings.LastIndex(raw, "."); idx != -1 {
		g.addImpAlias(raw[:idx], "")
		out := raw[idx+1:]
		c := camel(out)
		if c == "Config" || c == "ConfigSpec" {
			return "Values" + c
		}
		return c
	}
	c := camel(raw)
	if c == "Config" || c == "ConfigSpec" {
		return "Values" + c
	}
	return c
}

func (g *gen) goType(n *Node) string {
	raw := strings.TrimSpace(n.TypeExpr)
	// For nodes that are containers (have children) and TypeExpr is empty or "struct",
	// return the camelCased node name as the struct type
	if (raw == "" || raw == "struct") && len(n.Child) > 0 {
		c := camel(n.Name)
		if c == "Config" || c == "ConfigSpec" {
			return "Values" + c
		}
		return c
	}
	if raw == "" {
		return "string"
	}
	if strings.HasPrefix(raw, "*") {
		return "*" + g.resolve(strings.TrimPrefix(raw, "*"))
	}
	if strings.HasPrefix(raw, "[]") {
		base := strings.TrimPrefix(strings.TrimSpace(raw[2:]), "*")
		return "[]" + g.resolve(base)
	}
	if strings.HasPrefix(raw, "map[") && strings.Contains(raw, "]") {
		idx := strings.Index(raw, "]")
		base := strings.TrimPrefix(strings.TrimSpace(raw[idx+1:]), "*")
		return "map[string]" + g.resolve(base)
	}
	return g.resolve(raw)
}

func quoteEnums(vals []string) string {
	quoted := make([]string, len(vals))
	for i, v := range vals {
		quoted[i] = fmt.Sprintf("%q", v)
	}
	return strings.Join(quoted, ";")
}

/* -------------------------------------------------------------------------- */
/*  Struct emitter                                                             */
/* -------------------------------------------------------------------------- */

func (g *gen) writeEnum(n *Node) {
	if len(n.Enums) == 0 {
		return
	}

	name := camel(n.Name)
	if name == "Config" || name == "ConfigSpec" {
		name = "Values" + name
	}

	// Get base type
	baseType := n.TypeExpr
	if baseType == "" {
		baseType = "string"
	}

	g.buf.WriteString(fmt.Sprintf("// +kubebuilder:validation:Enum=%s\n", quoteEnums(n.Enums)))
	g.buf.WriteString(fmt.Sprintf("type %s %s\n\n", name, baseType))
}

func (g *gen) writeStruct(n *Node) {
	if strings.HasPrefix(n.Name, "[]") || strings.HasPrefix(n.Name, "map[") {
		return
	}

	if n.Parent == nil {
		g.addImp("k8s.io/apimachinery/pkg/apis/meta/v1")

		g.buf.WriteString("// +kubebuilder:object:root=true\n")
		g.buf.WriteString("type Config struct {\n")
		g.buf.WriteString("    v1.TypeMeta   `json:\",inline\"`\n")
		g.buf.WriteString("    v1.ObjectMeta `json:\"metadata,omitempty\"`\n")
		g.buf.WriteString("    Spec              ConfigSpec `json:\"spec,omitempty\"`\n")
		g.buf.WriteString("}\n\n")

		g.buf.WriteString("type ConfigSpec struct {\n")
		keys := sortedKeysByOrder(n.Child)
		for _, k := range keys {
			c := n.Child[k]
			if !c.IsParam {
				continue
			}
			g.emitField(c)
		}
		g.buf.WriteString("}\n\n")

		for _, k := range sortedKeys(n.Child) {
			g.writeStruct(n.Child[k])
		}
		return
	}

	if len(n.Child) == 0 && strings.TrimSpace(n.TypeExpr) != "" && strings.TrimSpace(n.TypeExpr) != "struct" {
		return
	}

	name := camel(n.Name)
	if name == "Config" || name == "ConfigSpec" {
		name = "Values" + name
	}

	g.buf.WriteString(fmt.Sprintf("type %s struct {\n", name))
	keys := sortedKeysByOrder(n.Child)
	for _, k := range keys {
		g.emitField(n.Child[k])
	}
	g.buf.WriteString("}\n\n")

	for _, k := range sortedKeys(n.Child) {
		g.writeStruct(n.Child[k])
	}
}

func (g *gen) emitField(c *Node) {
	field := camel(c.Name)
	typ := g.goType(c)

	if c.Comment != "" {
		g.buf.WriteString("    // " + c.Comment + "\n")
	}

	if f := strings.TrimPrefix(strings.TrimSpace(c.TypeExpr), "*"); isStringFormat(f) &&
		!strings.HasPrefix(typ, "[]") && !strings.HasPrefix(typ, "map[") {
		g.buf.WriteString("    // +kubebuilder:validation:Format=" + f + "\n")
	}

	if len(c.Enums) > 0 {
		g.buf.WriteString("    // +kubebuilder:validation:Enum=" + quoteEnums(c.Enums) + "\n")
	}

	// Emit default value if it was explicitly set (even if empty string)
	if c.HasDefaultVal && c.DefaultVal != "" {
		if def := formatDefault(c.DefaultVal, typ); def != "" {
			g.buf.WriteString("    // +kubebuilder:default:=" + def + "\n")
		}
	} else if c.HasDefaultVal && c.DefaultVal == "" {
		// Empty string default value - still emit it
		baseType := strings.TrimPrefix(typ, "*")
		if baseType == "string" || baseType == "quantity" {
			g.buf.WriteString("    // +kubebuilder:default:=\"\"\n")
		}
	}

	// Emit validation constraints
	if c.Minimum != nil {
		g.buf.WriteString(fmt.Sprintf("    // +kubebuilder:validation:Minimum=%v\n", *c.Minimum))
	}
	if c.Maximum != nil {
		g.buf.WriteString(fmt.Sprintf("    // +kubebuilder:validation:Maximum=%v\n", *c.Maximum))
	}
	if c.ExclusiveMinimum {
		g.buf.WriteString("    // +kubebuilder:validation:ExclusiveMinimum=true\n")
	}
	if c.ExclusiveMaximum {
		g.buf.WriteString("    // +kubebuilder:validation:ExclusiveMaximum=true\n")
	}
	if c.MinLength != nil {
		g.buf.WriteString(fmt.Sprintf("    // +kubebuilder:validation:MinLength=%d\n", *c.MinLength))
	}
	if c.MaxLength != nil {
		g.buf.WriteString(fmt.Sprintf("    // +kubebuilder:validation:MaxLength=%d\n", *c.MaxLength))
	}
	if c.Pattern != "" {
		g.buf.WriteString(fmt.Sprintf("    // +kubebuilder:validation:Pattern=%q\n", c.Pattern))
	}
	if c.MinItems != nil {
		g.buf.WriteString(fmt.Sprintf("    // +kubebuilder:validation:MinItems=%d\n", *c.MinItems))
	}
	if c.MaxItems != nil {
		g.buf.WriteString(fmt.Sprintf("    // +kubebuilder:validation:MaxItems=%d\n", *c.MaxItems))
	}
	if c.Immutable {
		// "is immutable" without the "after creation" qualifier: for
		// optional/omitempty/pointer fields the rule actually enforces
		// "immutable once set", so the legacy phrasing was misleading for
		// the operator who sees the admission error.
		g.buf.WriteString(fmt.Sprintf(
			"    // +kubebuilder:validation:XValidation:rule=\"self == oldSelf\",message=%q\n",
			c.Name+" is immutable",
		))
	}

	tag := "`json:\"" + c.Name
	// Add omitempty for: slices, maps, pointers, or fields explicitly marked with []
	if strings.HasPrefix(typ, "[]") || strings.HasPrefix(typ, "map[") || strings.HasPrefix(typ, "*") || c.OmitEmpty {
		tag += ",omitempty"
	}
	tag += "\"`"
	g.buf.WriteString(fmt.Sprintf("    %s %s %s\n", field, typ, tag))
}

func (g *gen) ensureFreeFormTypeFor(fieldOwner, fieldName string) string {
	if g.ff == nil {
		g.ff = map[string]bool{}
	}
	typeName := camel(fieldOwner) + camel(fieldName) + "FreeForm"
	if typeName == "Config" || typeName == "ConfigSpec" {
		typeName = "Values" + typeName
	}
	g.ff[typeName] = true
	return typeName
}

func (g *gen) Generate(root *Node) ([]byte, []byte, error) {
	if undef := CollectUndefined(root); len(undef) > 0 {
		return nil, nil, fmt.Errorf("undefined types: %s", strings.Join(undef, ", "))
	}
	g.buf.WriteString("// Code generated by values-gen. DO NOT EDIT.\n")
	g.buf.WriteString("// +kubebuilder:object:generate=true\n")
	g.buf.WriteString("// +groupName=" + g.groupName + "\n")
	g.buf.WriteString("// +versionName=" + g.versionName + "\n")
	g.buf.WriteString("package " + g.pkg + "\n\n")

	// collect defined complex types (have children)
	g.def = map[string]bool{}
	var walk func(n *Node)
	walk = func(n *Node) {
		for name, c := range n.Child {
			if name != "" && len(c.Child) > 0 {
				g.def[name] = true
				g.def[camel(name)] = true
			}
			walk(c)
		}
	}
	walk(root)

	g.writeStruct(root)

	// Generate enum types
	for _, k := range sortedKeys(root.Child) {
		c := root.Child[k]
		if len(c.Enums) > 0 {
			g.writeEnum(c)
		}
	}

	var src []byte
	if len(g.imp) > 0 {
		var imp bytes.Buffer
		imp.WriteString("import (\n")
		for _, p := range sortedKeys(g.imp) {
			if a := g.imp[p]; a != "" {
				imp.WriteString("    " + a + " \"" + p + "\"\n")
			} else {
				imp.WriteString("    \"" + p + "\"\n")
			}
		}
		imp.WriteString(")\n\n")
		head := "package " + g.pkg + "\n\n"
		src = g.buf.Bytes()
		src = []byte(strings.Replace(string(src), head, head+imp.String(), 1))
	} else {
		src = g.buf.Bytes()
	}

	formatted, err := format.Source(src)
	if err != nil {
		return nil, src, fmt.Errorf("formatting failed: %w", err)
	}
	return formatted, src, nil
}

func sortedKeys[M ~map[K]V, K comparable, V any](m M) []K {
	keys := make([]K, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return fmt.Sprint(keys[i]) < fmt.Sprint(keys[j])
	})
	return keys
}

func sortedKeysByOrder(nodes map[string]*Node) []string {
	keys := make([]string, 0, len(nodes))
	for k := range nodes {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		nodeI := nodes[keys[i]]
		nodeJ := nodes[keys[j]]
		if nodeI.Order != 0 || nodeJ.Order != 0 {
			return nodeI.Order < nodeJ.Order
		}
		return keys[i] < keys[j]
	})
	return keys
}

/* -------------------------------------------------------------------------- */
/*  Temporary project scaffolding                                              */
/* -------------------------------------------------------------------------- */

func WriteGeneratedGoAndStub(root *Node, module, groupName, versionName string) (tmpdir, goFilePath string, err error) {
	tmpdir, err = os.MkdirTemp("", "values-gen-*")
	if err != nil {
		return "", "", err
	}

	goMod := `module fake.local/generated

go 1.20

require (
	k8s.io/apimachinery v0.0.0
)

replace k8s.io/apimachinery => ./k8s.io/apimachinery
`
	if err := os.WriteFile(filepath.Join(tmpdir, "go.mod"), []byte(goMod), 0o644); err != nil {
		return "", "", err
	}

	pkgDir := filepath.Join(tmpdir, module)
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		return "", "", err
	}

	g := &gen{pkg: module, groupName: groupName, versionName: versionName}
	formatted, raw, err := g.Generate(root)
	goFilePath = filepath.Join(pkgDir, "values_generated.go")
	if err != nil {
		_ = os.WriteFile(goFilePath, raw, 0o644)
		return tmpdir, goFilePath, fmt.Errorf("write generated (unformatted): %w", err)
	}
	if err := os.WriteFile(goFilePath, formatted, 0o644); err != nil {
		return "", "", err
	}

	stubModuleDir := filepath.Join(tmpdir, "k8s.io/apimachinery")
	if err := os.MkdirAll(filepath.Join(stubModuleDir, "pkg/apis/meta/v1"), 0o755); err != nil {
		return "", "", err
	}
	stubMod := `module k8s.io/apimachinery

go 1.20
`
	if err := os.WriteFile(filepath.Join(stubModuleDir, "go.mod"), []byte(stubMod), 0o644); err != nil {
		return "", "", err
	}

	/* ---------- stub k8s.io/apimachinery/pkg/apis/meta/v1 ---------- */

	stubCode := `package v1

type TypeMeta struct{}
type ObjectMeta struct{}

// Duration is a stub so that go/types can resolve metav1.Duration.
// Real validation is injected by controller-tools KnownPackages.
type Duration struct{}
type Time struct{}
type MicroTime struct{}
type Fields map[string]interface{}
`

	stubPath := filepath.Join(stubModuleDir, "pkg/apis/meta/v1/doc.go")
	if err := os.WriteFile(stubPath, []byte(stubCode), 0o644); err != nil {
		return "", "", err
	}

	/* ---------- stub k8s.io/apimachinery/pkg/api/resource ---------- */

	resDir := filepath.Join(stubModuleDir, "pkg/api/resource")
	if err := os.MkdirAll(resDir, 0o755); err != nil {
		return "", "", err
	}
	stubQty := `package resource
type Quantity struct{}
`
	if err := os.WriteFile(filepath.Join(resDir, "doc.go"), []byte(stubQty), 0o644); err != nil {
		return "", "", err
	}

	/* ---------- stub k8s.io/apimachinery/pkg/util/intstr ---------- */

	intstrDir := filepath.Join(stubModuleDir, "pkg/util/intstr")
	if err := os.MkdirAll(intstrDir, 0o755); err != nil {
		return "", "", err
	}
	stubIS := `package intstr
type IntOrString struct{}
`
	if err := os.WriteFile(filepath.Join(intstrDir, "doc.go"), []byte(stubIS), 0o644); err != nil {
		return "", "", err
	}

	/* --------- stub for apiextensions-apiserver/pkg/apis/apiextensions/v1 --- */
	rtDir := filepath.Join(stubModuleDir, "pkg/runtime")
	if err := os.MkdirAll(rtDir, 0o755); err != nil {
		return "", "", err
	}
	stubRE := `package runtime
type RawExtension struct{}
`
	if err := os.WriteFile(filepath.Join(rtDir, "doc.go"), []byte(stubRE), 0o644); err != nil {
		return "", "", err
	}

	return tmpdir, goFilePath, nil
}

/* -------------------------------------------------------------------------- */
/*  Controller-gen wrapper                                                     */
/* -------------------------------------------------------------------------- */

func CG(pkgDir string) ([]byte, error) {
	roots, err := loader.LoadRoots(pkgDir)
	if err != nil {
		return nil, fmt.Errorf("loader: %w", err)
	}

	reg := &markers.Registry{}
	if err := crdmarkers.Register(reg); err != nil {
		return nil, fmt.Errorf("register markers: %w", err)
	}

	parser := &crd.Parser{
		Collector:                  &markers.Collector{Registry: reg},
		Checker:                    &loader.TypeChecker{},
		IgnoreUnexportedFields:     false,
		AllowDangerousTypes:        true,
		GenerateEmbeddedObjectMeta: false,
	}
	crd.AddKnownTypes(parser)

	for _, r := range roots {
		parser.NeedPackage(r)
	}

	metaPkg := crd.FindMetav1(roots)
	if metaPkg == nil {
		return nil, fmt.Errorf("cannot locate metav1 import")
	}
	kinds := crd.FindKubeKinds(parser, metaPkg)
	if len(kinds) == 0 {
		return nil, fmt.Errorf("no Kubernetes kinds found in %s", pkgDir)
	}

	var buf bytes.Buffer
	for _, gk := range kinds {
		parser.NeedCRDFor(gk, nil)
		crdRaw := parser.CustomResourceDefinitions[gk]

		if crdRaw.ObjectMeta.Annotations == nil {
			crdRaw.ObjectMeta.Annotations = map[string]string{}
		}
		crdRaw.ObjectMeta.Annotations["controller-gen.kubebuilder.io/version"] = version.Version
		crd.FixTopLevelMetadata(crdRaw)

		data, err := sigyaml.Marshal(&crdRaw)
		if err != nil {
			return nil, err
		}
		if buf.Len() > 0 {
			buf.WriteString("---\n")
		}
		buf.Write(data)
	}
	return buf.Bytes(), nil
}

/* -------------------------------------------------------------------------- */
/*  Helm values JSON Schema                                                   */
/* -------------------------------------------------------------------------- */

// baseTypeName extracts the underlying named type from a type expression,
// stripping pointer, slice and map wrappers (e.g. "[]*GPU" -> "GPU",
// "map[string]NodeGroup" -> "NodeGroup", "*Config" -> "Config").
func baseTypeName(expr string) string {
	expr = strings.TrimSpace(expr)
	expr = strings.TrimPrefix(expr, "*")
	if strings.HasPrefix(expr, "[]") {
		return baseTypeName(expr[2:])
	}
	if strings.HasPrefix(expr, "map[") {
		if i := strings.Index(expr, "]"); i != -1 {
			return baseTypeName(expr[i+1:])
		}
	}
	return expr
}

// structuralChildren returns the child nodes that describe the object
// properties of n. If n declares its own inline children they are used;
// otherwise the node's type expression is resolved against the typedef
// aliases so that extensions on typedef fields propagate wherever the type
// is referenced (including via []Type and map[string]Type).
func structuralChildren(n *Node, aliases map[string]*Node) map[string]*Node {
	if n == nil {
		return nil
	}
	if len(n.Child) > 0 {
		return n.Child
	}
	base := baseTypeName(n.TypeExpr)
	if base == "" {
		return nil
	}
	if alias, ok := aliases[base]; ok && alias != n {
		return alias.Child
	}
	return nil
}

// nodeSubtreeHasExtensions reports whether n or any node reachable from it
// (through inline children or typedef aliases for direct / []Type /
// map[string]Type references) carries a vendor extension. Used to decide
// whether a property needs the order-preserving injection path at all; when
// it does not, the property is emitted via plain json.MarshalIndent so output
// stays byte-identical to the pre-extension generator.
func nodeSubtreeHasExtensions(n *Node, aliases map[string]*Node, seen map[*Node]bool) bool {
	if n == nil || seen[n] {
		return false
	}
	seen[n] = true
	if len(n.Extensions) > 0 {
		return true
	}
	for _, child := range structuralChildren(n, aliases) {
		if nodeSubtreeHasExtensions(child, aliases, seen) {
			return true
		}
	}
	return false
}

// injectExtensions walks an order-preserving JSON schema node alongside its
// corresponding Node, writing any vendor extensions (x-* keys) declared on
// the node and recursively descending into nested object properties, array
// items and map values. Extensions are emitted into the JSON schema only.
//
// Injected x-* keys are appended as the last key(s) of their object so that
// all pre-existing keys keep their original relative order and position. This
// guarantees byte-identical output to the pre-extension generator for any
// schema that carries no extensions.
func injectExtensions(schema *orderedMap, n *Node, aliases map[string]*Node) {
	if schema == nil || n == nil {
		return
	}

	for _, k := range sortedExtensionKeys(n.Extensions) {
		schema.set(k, n.Extensions[k])
	}

	children := structuralChildren(n, aliases)
	if children == nil {
		return
	}

	// Object properties.
	if props, ok := schema.get("properties").(*orderedMap); ok {
		for name, child := range children {
			if sub, ok := props.get(name).(*orderedMap); ok {
				injectExtensions(sub, child, aliases)
			}
		}
	}

	// Array element type ([]Type) — children describe the element struct.
	if items, ok := schema.get("items").(*orderedMap); ok {
		injectExtensionsForElement(items, n, aliases)
	}

	// Map value type (map[string]Type) — children describe the value struct.
	if ap, ok := schema.get("additionalProperties").(*orderedMap); ok {
		injectExtensionsForElement(ap, n, aliases)
	}
}

// injectExtensionsForElement injects extensions into the element schema of an
// array or map, using the children that describe that element's struct.
func injectExtensionsForElement(elem *orderedMap, n *Node, aliases map[string]*Node) {
	children := structuralChildren(n, aliases)
	if children == nil {
		return
	}
	if props, ok := elem.get("properties").(*orderedMap); ok {
		for name, child := range children {
			if sub, ok := props.get(name).(*orderedMap); ok {
				injectExtensions(sub, child, aliases)
			}
		}
	}
}

// sortedExtensionKeys returns the keys of an Extensions map in a deterministic
// (alphabetical) order so that multiple x-* keys on a single field are emitted
// stably across runs.
func sortedExtensionKeys(ext map[string]interface{}) []string {
	if len(ext) == 0 {
		return nil
	}
	keys := make([]string, 0, len(ext))
	for k := range ext {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

/* -------------------------------------------------------------------------- */
/*  Order-preserving JSON object representation                                */
/* -------------------------------------------------------------------------- */

// orderedMap is a JSON object whose key insertion order is preserved across an
// Unmarshal/Marshal round-trip. It is used instead of map[string]interface{}
// (which sorts keys alphabetically on marshal) so that the injected schema
// stays byte-identical to direct json.Marshal of the source struct, except for
// the appended vendor-extension (x-*) keys.
type orderedMap struct {
	keys   []string
	values map[string]interface{}
}

func newOrderedMap() *orderedMap {
	return &orderedMap{values: map[string]interface{}{}}
}

func (m *orderedMap) get(key string) interface{} {
	if m == nil {
		return nil
	}
	return m.values[key]
}

// set inserts or updates a key. New keys are appended, preserving order;
// existing keys keep their original position.
func (m *orderedMap) set(key string, value interface{}) {
	if _, exists := m.values[key]; !exists {
		m.keys = append(m.keys, key)
	}
	m.values[key] = value
}

// UnmarshalJSON decodes a JSON value into ordered structures. Objects become
// *orderedMap (preserving key order), arrays become []interface{} whose object
// elements are also *orderedMap, and scalars decode normally.
func (m *orderedMap) UnmarshalJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()

	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return fmt.Errorf("orderedMap: expected object, got %v", tok)
	}
	return m.decodeObject(dec)
}

func (m *orderedMap) decodeObject(dec *json.Decoder) error {
	m.values = map[string]interface{}{}
	m.keys = nil
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := keyTok.(string)
		if !ok {
			return fmt.Errorf("orderedMap: expected string key, got %v", keyTok)
		}
		val, err := decodeValue(dec)
		if err != nil {
			return err
		}
		m.set(key, val)
	}
	// Consume closing '}'.
	if _, err := dec.Token(); err != nil {
		return err
	}
	return nil
}

// decodeValue decodes the next JSON value from dec, producing *orderedMap for
// objects and []interface{} for arrays.
func decodeValue(dec *json.Decoder) (interface{}, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); ok {
		switch d {
		case '{':
			child := newOrderedMap()
			if err := child.decodeObject(dec); err != nil {
				return nil, err
			}
			return child, nil
		case '[':
			arr := []interface{}{}
			for dec.More() {
				v, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			}
			// Consume closing ']'.
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return arr, nil
		}
	}
	return tok, nil
}

// MarshalJSON emits the object with keys in their preserved insertion order.
func (m *orderedMap) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range m.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		buf.Write(kb)
		buf.WriteByte(':')
		vb, err := json.Marshal(m.values[k])
		if err != nil {
			return nil, err
		}
		buf.Write(vb)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

func WriteValuesSchema(crdBytes []byte, outPath string) error {
	return WriteValuesSchemaWithOrder(crdBytes, outPath, nil)
}

func WriteValuesSchemaWithOrder(crdBytes []byte, outPath string, root *Node) error {
	docs := bytes.Split(crdBytes, []byte("\n---"))
	if len(docs) == 0 {
		return fmt.Errorf("empty CRD data")
	}

	var obj apiextv1.CustomResourceDefinition
	if err := sigyaml.Unmarshal(docs[0], &obj); err != nil {
		return err
	}
	if len(obj.Spec.Versions) == 0 {
		return fmt.Errorf("CRD has no versions")
	}

	specSchema := obj.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"]

	if root != nil {
		keys := sortedKeysByOrder(root.Child)

		var buf bytes.Buffer
		buf.WriteString("{\n")
		buf.WriteString("  \"title\": \"Chart Values\",\n")
		buf.WriteString("  \"type\": \"object\",\n")
		buf.WriteString("  \"properties\": {\n")

		first := true
		for _, key := range keys {
			if node, exists := root.Child[key]; exists && node.IsParam {
				if prop, exists := specSchema.Properties[key]; exists {
					if !first {
						buf.WriteString(",\n")
					}
					first = false

					// Fast path: when this property's subtree carries no
					// vendor extensions, emit it via plain MarshalIndent so
					// the output is byte-identical to the pre-extension
					// generator (JSONSchemaProps marshals in struct-field
					// order, not alphabetical). Only properties that actually
					// carry an x-* directive take the order-preserving
					// injection path below.
					if !nodeSubtreeHasExtensions(node, root.Child, map[*Node]bool{}) {
						propJSON, err := json.MarshalIndent(prop, "    ", "  ")
						if err != nil {
							return err
						}
						buf.WriteString(fmt.Sprintf("    \"%s\": %s", key, string(propJSON)))
						continue
					}

					// Injection path: marshal in struct-field order, round-trip
					// through an order-preserving orderedMap (not a Go map,
					// which sorts keys alphabetically), then append the x-*
					// keys declared on this node and nested typedef fields
					// without reordering any existing key.
					rawJSON, err := json.Marshal(prop)
					if err != nil {
						return err
					}
					propMap := newOrderedMap()
					if err := json.Unmarshal(rawJSON, propMap); err != nil {
						return err
					}
					injectExtensions(propMap, node, root.Child)

					compact, err := json.Marshal(propMap)
					if err != nil {
						return err
					}
					var indented bytes.Buffer
					if err := json.Indent(&indented, compact, "    ", "  "); err != nil {
						return err
					}

					buf.WriteString(fmt.Sprintf("    \"%s\": %s", key, indented.String()))
				}
			}
		}

		buf.WriteString("\n  }\n")
		buf.WriteString("}\n")

		return os.WriteFile(outPath, buf.Bytes(), 0o644)
	}

	// Fallback
	out := struct {
		Title      string                              `json:"title"`
		Type       string                              `json:"type"`
		Properties map[string]apiextv1.JSONSchemaProps `json:"properties"`
	}{
		Title:      "Chart Values",
		Type:       "object",
		Properties: specSchema.Properties,
	}

	buf, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(outPath, buf, 0o644)
}

/* -------------------------------------------------------------------------- */
/*  Defaults, validation helpers                                              */
/* -------------------------------------------------------------------------- */

func PopulateDefaults(n *Node, y interface{}, aliases map[string]*Node) {
	switch v := y.(type) {
	case map[string]interface{}:
		for k, yval := range v {
			child, ok := n.Child[k]
			if !ok || yval == nil {
				continue
			}

			switch vv := yval.(type) {
			case string, int, int64, float64, bool:
				if !child.HasDefaultVal {
					child.DefaultVal = fmt.Sprintf("%v", vv)
					child.HasDefaultVal = true
				}

			case map[string]interface{}:
				isObj := len(child.Child) > 0
				var alias *Node
				if te := strings.TrimPrefix(child.TypeExpr, "*"); te != "" {
					if a, ok := aliases[te]; ok {
						alias = a
						if len(a.Child) > 0 {
							isObj = true
						}
					}
				}

				if isObj {
					if !child.HasDefaultVal {
						child.DefaultVal = "{}"
						child.HasDefaultVal = true
					}
					if alias != nil {
						PopulateDefaults(alias, vv, aliases)
					} else {
						PopulateDefaults(child, vv, aliases)
					}
					continue
				}

				if !child.HasDefaultVal {
					if b, err := sigyaml.Marshal(vv); err == nil {
						child.DefaultVal = string(b)
					} else {
						child.DefaultVal = "{}"
					}
					child.HasDefaultVal = true
				}

			default:
				if !child.HasDefaultVal {
					if b, err := sigyaml.Marshal(vv); err == nil {
						child.DefaultVal = string(b)
					} else {
						child.DefaultVal = fmt.Sprintf("%v", vv)
					}
					child.HasDefaultVal = true
				}
			}
		}
	}
}

func toGoLiteral(v interface{}) string {
	switch val := v.(type) {
	case map[string]interface{}:
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%q:%s", k, toGoLiteral(val[k])))
		}
		return fmt.Sprintf("{%s}", strings.Join(parts, ","))
	case []interface{}:
		parts := make([]string, 0, len(val))
		for _, elem := range val {
			parts = append(parts, toGoLiteral(elem))
		}
		return fmt.Sprintf("{%s}", strings.Join(parts, ","))
	case string:
		return fmt.Sprintf("%q", val)
	case bool:
		if val {
			return "true"
		}
		return "false"
	case int, int64, float64:
		return fmt.Sprintf("%v", val)
	default:
		return fmt.Sprintf("%v", val)
	}
}

func formatDefault(val, typ string) string {
	t := strings.TrimPrefix(typ, "*")
	if t == camel(aliasEmptyObject) {
		return ""
	}
	if t == "string" || t == "quantity" {
		// Remove quotes if already present
		val = strings.Trim(val, `"`)
		return fmt.Sprintf("%q", val)
	}
	var parsed interface{}
	if err := sigyaml.Unmarshal([]byte(val), &parsed); err == nil {
		return toGoLiteral(parsed)
	}
	return val
}

func CollectUndefined(root *Node) []string {
	defined := map[string]struct{}{}
	referenced := map[string]struct{}{}

	baseOf := func(expr string) string {
		expr = strings.TrimSpace(expr)
		if expr == "" {
			return ""
		}
		if strings.HasPrefix(expr, "*") {
			expr = strings.TrimPrefix(expr, "*")
		}
		if strings.HasPrefix(expr, "[]") {
			expr = strings.TrimSpace(expr[2:])
			if strings.HasPrefix(expr, "*") {
				expr = strings.TrimPrefix(expr, "*")
			}
		}
		if strings.HasPrefix(expr, "map[") {
			if i := strings.Index(expr, "]"); i != -1 {
				expr = strings.TrimSpace(expr[i+1:])
				if strings.HasPrefix(expr, "*") {
					expr = strings.TrimPrefix(expr, "*")
				}
			}
		}
		return expr
	}

	var walk func(n *Node)
	walk = func(n *Node) {
		if n.Parent != nil {
			name := n.Name
			base := strings.TrimPrefix(name, "*")
			if base != "" &&
				base != aliasObject && base != aliasEmptyObject &&
				base != "struct" && base != "object" &&
				!isPrimitive(base) &&
				!strings.HasPrefix(base, "[]") &&
				!strings.HasPrefix(base, "map[") {
				// Type is defined if it has children, enums, or if it's a typedef (has TypeExpr "struct")
				// This allows empty structs (typedefs without fields) to be considered defined
				if len(n.Child) > 0 || len(n.Enums) > 0 || n.TypeExpr == "struct" {
					defined[base] = struct{}{}
				}
			}
		}
		if b := baseOf(n.TypeExpr); b != "" {
			if b != aliasObject && b != aliasEmptyObject &&
				b != "struct" && b != "object" &&
				b != aliasResources && b != aliasRequest && b != aliasLimit &&
				!isPrimitive(b) &&
				!strings.HasPrefix(b, "[]") &&
				!strings.HasPrefix(b, "map[") {
				referenced[b] = struct{}{}
			}
		}
		for _, c := range n.Child {
			walk(c)
		}
	}
	walk(root)

	var bad []string
	for t := range referenced {
		if _, ok := defined[t]; !ok {
			bad = append(bad, t)
		}
	}
	sort.Strings(bad)
	return bad
}
