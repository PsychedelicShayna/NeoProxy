package codexwire

import (
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/tidwall/gjson"
)

// jmap is a JSON object with JavaScript's insertion order, the shape NeoPi's
// schema passes mutate. Values are *jmap, []any, string, float64, bool or nil.
type jmap struct {
	keys []string
	vals map[string]any
}

func newJmap() *jmap { return &jmap{vals: map[string]any{}} }

func (m *jmap) has(key string) bool { _, ok := m.vals[key]; return ok }
func (m *jmap) get(key string) any  { return m.vals[key] }

// arrayIndexKey reports whether key is a canonical JavaScript array index
// (a decimal uint32 below 2^32-1 without leading zeros), which objects
// enumerate first in ascending order.
func arrayIndexKey(key string) (uint64, bool) {
	if key == "" || len(key) > 10 || (key[0] == '0' && len(key) > 1) {
		return 0, false
	}
	var n uint64
	for i := 0; i < len(key); i++ {
		if key[i] < '0' || key[i] > '9' {
			return 0, false
		}
		n = n*10 + uint64(key[i]-'0')
	}
	return n, n < 1<<32-1
}

func (m *jmap) set(key string, value any) {
	if _, ok := m.vals[key]; !ok {
		position := len(m.keys)
		if index, isIndex := arrayIndexKey(key); isIndex {
			position = 0
			for position < len(m.keys) {
				existing, existingIsIndex := arrayIndexKey(m.keys[position])
				if !existingIsIndex || existing > index {
					break
				}
				position++
			}
		}
		m.keys = append(m.keys, "")
		copy(m.keys[position+1:], m.keys[position:])
		m.keys[position] = key
	}
	m.vals[key] = value
}

func (m *jmap) del(key string) {
	if _, ok := m.vals[key]; !ok {
		return
	}
	delete(m.vals, key)
	for i, existing := range m.keys {
		if existing == key {
			m.keys = append(m.keys[:i:i], m.keys[i+1:]...)
			break
		}
	}
}

// clone is a shallow `{ ...m }`.
func (m *jmap) clone() *jmap {
	out := &jmap{keys: append([]string(nil), m.keys...), vals: make(map[string]any, len(m.vals))}
	for key, value := range m.vals {
		out.vals[key] = value
	}
	return out
}

func decodeTree(value gjson.Result) any {
	switch {
	case value.IsObject():
		out := newJmap()
		value.ForEach(func(key, child gjson.Result) bool {
			out.set(key.String(), decodeTree(child))
			return true
		})
		return out
	case value.IsArray():
		out := []any{}
		for _, child := range value.Array() {
			out = append(out, decodeTree(child))
		}
		return out
	case value.Type == gjson.String:
		return value.String()
	case value.Type == gjson.Number:
		return value.Float()
	case value.Type == gjson.True:
		return true
	case value.Type == gjson.False:
		return false
	}
	return nil
}

func encodeTree(value any) json.RawMessage {
	switch typed := value.(type) {
	case *jmap:
		fields := make([]jsjson.Field, 0, len(typed.keys))
		for _, key := range typed.keys {
			fields = append(fields, jsjson.Field{Name: key, Value: encodeTree(typed.vals[key])})
		}
		return jsjson.Object(fields...)
	case []any:
		out := []byte{'['}
		for i, child := range typed {
			if i > 0 {
				out = append(out, ',')
			}
			if encoded := encodeTree(child); len(encoded) > 0 {
				out = append(out, encoded...)
			} else {
				out = append(out, "null"...)
			}
		}
		return append(out, ']')
	case float64:
		return json.RawMessage(jsjson.Number(typed))
	case jsUndefined:
		return nil
	case nil:
		return json.RawMessage("null")
	}
	return jsjson.Marshal(value)
}

func asMap(value any) (*jmap, bool) {
	m, ok := value.(*jmap)
	return m, ok
}

// jsonEqual is areJsonValuesEqual: deep equality, key order ignored.
func jsonEqual(left, right any) bool {
	switch a := left.(type) {
	case *jmap:
		b, ok := right.(*jmap)
		if !ok || len(a.keys) != len(b.keys) {
			return false
		}
		for _, key := range a.keys {
			if !b.has(key) || !jsonEqual(a.vals[key], b.vals[key]) {
				return false
			}
		}
		return true
	case []any:
		b, ok := right.([]any)
		if !ok || len(a) != len(b) {
			return false
		}
		for i := range a {
			if !jsonEqual(a[i], b[i]) {
				return false
			}
		}
		return true
	}
	return left == right
}

// ---------------------------------------------------------------------------
// draft.ts: upgradeJsonSchemaTo202012
// ---------------------------------------------------------------------------

var (
	draft07URIs = map[string]bool{
		"http://json-schema.org/draft-07/schema#": true, "https://json-schema.org/draft-07/schema#": true,
		"http://json-schema.org/draft-07/schema": true, "https://json-schema.org/draft-07/schema": true,
	}
	upgradeMapKeys      = map[string]bool{"properties": true, "patternProperties": true, "dependentSchemas": true}
	upgradeNonSchemaKey = map[string]bool{"const": true, "default": true, "enum": true, "example": true, "examples": true, "required": true, "dependentRequired": true, "type": true}
)

func schemaNeedsUpgrade(value any) bool {
	switch typed := value.(type) {
	case []any:
		for _, entry := range typed {
			if schemaNeedsUpgrade(entry) {
				return true
			}
		}
		return false
	case *jmap:
		for _, key := range typed.keys {
			entry := typed.vals[key]
			switch {
			case key == "$schema":
				if uri, ok := entry.(string); ok && draft07URIs[uri] {
					return true
				}
				continue
			case key == "definitions" || key == "dependencies" || key == "additionalItems" || key == "nullable":
				return true
			case key == "$ref":
				if ref, ok := entry.(string); ok && strings.HasPrefix(ref, "#/definitions/") {
					return true
				}
				continue
			case key == "items":
				if _, ok := entry.([]any); ok {
					return true
				}
			}
			if key == "$defs" || upgradeMapKeys[key] {
				if m, ok := asMap(entry); ok {
					for _, name := range m.keys {
						if schemaNeedsUpgrade(m.vals[name]) {
							return true
						}
					}
				}
				continue
			}
			if upgradeNonSchemaKey[key] {
				continue
			}
			if schemaNeedsUpgrade(entry) {
				return true
			}
		}
	}
	return false
}

func upgradeSchema(value any) any {
	if !schemaNeedsUpgrade(value) {
		return value
	}
	return upgradeSchemaImpl(value)
}

func objectMap(target *jmap, key string) *jmap {
	if existing, ok := asMap(target.get(key)); ok {
		return existing
	}
	next := newJmap()
	target.set(key, next)
	return next
}

func combineSchemas(left, right any) any {
	if left == nil || left == true {
		return right
	}
	if right == nil || right == true {
		return left
	}
	if left == false || right == false {
		return false
	}
	if jsonEqual(left, right) {
		return left
	}
	out := newJmap()
	out.set("allOf", []any{left, right})
	return out
}

func upgradeSchemaImpl(value any) any {
	if array, ok := value.([]any); ok {
		out := make([]any, len(array))
		for i, entry := range array {
			out[i] = upgradeSchemaImpl(entry)
		}
		return out
	}
	source, ok := asMap(value)
	if !ok {
		return value
	}
	result := newJmap()
	mergeMap := func(key string, from *jmap) {
		target := objectMap(result, key)
		for _, name := range from.keys {
			target.set(name, upgradeSchemaImpl(from.vals[name]))
		}
	}
	for _, key := range source.keys {
		entry := source.vals[key]
		switch {
		case key == "definitions" || key == "$defs":
			if m, isMap := asMap(entry); isMap {
				mergeMap("$defs", m)
			}
		case upgradeMapKeys[key]:
			if m, isMap := asMap(entry); isMap {
				mergeMap(key, m)
			} else {
				result.set(key, entry)
			}
		case upgradeNonSchemaKey[key]:
			result.set(key, entry)
		case key == "dependencies" || key == "additionalItems" || key == "nullable":
		case key == "$schema":
			if uri, isString := entry.(string); isString && draft07URIs[uri] {
				result.set("$schema", "https://json-schema.org/draft/2020-12/schema")
			} else {
				result.set("$schema", entry)
			}
		case key == "$ref":
			if ref, isString := entry.(string); isString {
				if strings.HasPrefix(ref, "#/definitions/") {
					ref = "#/$defs/" + strings.TrimPrefix(ref, "#/definitions/")
				}
				result.set("$ref", ref)
			} else {
				result.set(key, upgradeSchemaImpl(entry))
			}
		default:
			if _, isTuple := entry.([]any); key == "items" && isTuple {
				continue
			}
			result.set(key, upgradeSchemaImpl(entry))
		}
	}
	if tuple, isTuple := source.get("items").([]any); isTuple {
		converted := upgradeSchemaImpl(tuple).([]any)
		merged := converted
		if existing, isArray := result.get("prefixItems").([]any); isArray {
			merged = append([]any(nil), existing...)
			for i, item := range converted {
				if i < len(merged) {
					merged[i] = combineSchemas(merged[i], item)
				} else {
					merged = append(merged, item)
				}
			}
		}
		result.set("prefixItems", merged)
		if additional := source.get("additionalItems"); source.has("additionalItems") && additional != true {
			result.set("items", upgradeSchemaImpl(additional))
		} else {
			result.del("items")
		}
	}
	if dependencies, isMap := asMap(source.get("dependencies")); isMap {
		for _, key := range dependencies.keys {
			converted := upgradeSchemaImpl(dependencies.vals[key])
			if deps, isArray := converted.([]any); isArray {
				required := objectMap(result, "dependentRequired")
				switch existing := required.get(key).(type) {
				case nil:
					if !required.has(key) {
						required.set(key, deps)
					}
				case []any:
					merged := append([]any(nil), existing...)
					for _, dep := range deps {
						found := false
						for _, have := range merged {
							if jsonEqual(have, dep) {
								found = true
								break
							}
						}
						if !found {
							merged = append(merged, dep)
						}
					}
					required.set(key, merged)
				}
			} else {
				schemas := objectMap(result, "dependentSchemas")
				schemas.set(key, combineSchemas(schemas.get(key), converted))
			}
		}
	}
	if source.get("nullable") == true {
		return makeNullable(result)
	}
	return result
}

func hasNullType(value any) bool {
	if value == "null" {
		return true
	}
	if types, ok := value.([]any); ok {
		for _, entry := range types {
			if entry == "null" {
				return true
			}
		}
	}
	return false
}

func makeNullable(schema *jmap) *jmap {
	switch typed := schema.get("type").(type) {
	case string:
		if typed != "null" {
			schema.set("type", []any{typed, "null"})
		}
		return schema
	case []any:
		if !hasNullType(typed) {
			schema.set("type", append(append([]any(nil), typed...), "null"))
		}
		return schema
	}
	if variants, ok := schema.get("anyOf").([]any); ok {
		for _, variant := range variants {
			if m, isMap := asMap(variant); isMap && hasNullType(m.get("type")) {
				return schema
			}
		}
		null := newJmap()
		null.set("type", "null")
		schema.set("anyOf", append(append([]any(nil), variants...), null))
		return schema
	}
	null := newJmap()
	null.set("type", "null")
	wrapper := newJmap()
	wrapper.set("anyOf", []any{schema, null})
	return wrapper
}

// ---------------------------------------------------------------------------
// wire.ts: postProcessJsonSchema
// ---------------------------------------------------------------------------

var (
	nullableScalarTypes   = map[string]bool{"string": true, "number": true, "integer": true, "boolean": true}
	schemaDefiningSibling = map[string]bool{
		"$ref": true, "additionalProperties": true, "allOf": true, "anyOf": true, "const": true, "contains": true,
		"enum": true, "if": true, "items": true, "not": true, "oneOf": true, "patternProperties": true,
		"prefixItems": true, "properties": true, "propertyNames": true, "then": true, "else": true,
		"unevaluatedItems": true, "unevaluatedProperties": true,
	}
	wireValueKeys = []string{"additionalProperties", "unevaluatedProperties", "unevaluatedItems", "items", "contains", "propertyNames", "if", "then", "else", "not"}
	wireMapKeys   = []string{"properties", "patternProperties", "$defs", "definitions"}
	wireArrayKeys = []string{"anyOf", "oneOf", "allOf", "prefixItems"}
	arkKeyComment = regexp.MustCompile(`^(.*?)\s*/\*\*\s*((?s:.*?))\s*\*/\s*$`)
)

func postProcessSchema(schema any) any {
	walkSchema(schema)
	normalizeArkPropertyComments(schema)
	normalizeEmptySchemas(schema)
	return schema
}

func hasSchemaDefiningSibling(schema *jmap) bool {
	for _, key := range schema.keys {
		if key != "anyOf" && schemaDefiningSibling[key] {
			return true
		}
	}
	return false
}

func rewriteNullableScalarAnyOf(schema *jmap) {
	if hasSchemaDefiningSibling(schema) {
		return
	}
	variants, ok := schema.get("anyOf").([]any)
	if !ok || len(variants) != 2 {
		return
	}
	var scalar *jmap
	sawNull := false
	for _, variant := range variants {
		m, isMap := asMap(variant)
		if !isMap {
			return
		}
		if m.get("type") == "null" && len(m.keys) == 1 {
			if sawNull {
				return
			}
			sawNull = true
			continue
		}
		kind, isString := m.get("type").(string)
		if !isString || !nullableScalarTypes[kind] || scalar != nil {
			return
		}
		scalar = m
	}
	if !sawNull || scalar == nil {
		return
	}
	schema.del("anyOf")
	for _, key := range scalar.keys {
		if key == "type" || key == "enum" || key == "const" || schema.has(key) {
			continue
		}
		schema.set(key, scalar.vals[key])
	}
	if scalar.has("const") {
		schema.set("enum", []any{scalar.get("const"), nil})
	} else if values, isArray := scalar.get("enum").([]any); isArray {
		hasNull := false
		for _, value := range values {
			if value == nil {
				hasNull = true
			}
		}
		if hasNull {
			schema.set("enum", values)
		} else {
			schema.set("enum", append(append([]any(nil), values...), nil))
		}
	}
	schema.set("type", []any{scalar.get("type"), "null"})
}

func homogeneousEnumScalarType(values []any) string {
	inferred := ""
	for _, value := range values {
		var scalar string
		switch value.(type) {
		case string:
			scalar = "string"
		case bool:
			scalar = "boolean"
		case float64:
			scalar = "number"
		default:
			return ""
		}
		if inferred == "" {
			inferred = scalar
		} else if inferred != scalar {
			return ""
		}
	}
	return inferred
}

func collapseConstUnionAnyOf(schema *jmap) {
	if hasSchemaDefiningSibling(schema) || schema.has("type") {
		return
	}
	variants, ok := schema.get("anyOf").([]any)
	if !ok || len(variants) < 2 {
		return
	}
	var values []any
	branchDescription, described := "", 0
	for _, variant := range variants {
		m, isMap := asMap(variant)
		if !isMap || !m.has("const") {
			return
		}
		for _, key := range m.keys {
			if key != "const" && key != "description" {
				return
			}
		}
		if description, isString := m.get("description").(string); isString {
			if described == 0 {
				branchDescription = description
			} else if description != branchDescription {
				return
			}
			described++
		}
		values = append(values, m.get("const"))
	}
	if described != 0 && described != len(variants) {
		return
	}
	if root, isString := schema.get("description").(string); described == len(variants) && isString && root != branchDescription {
		return
	}
	scalar := homogeneousEnumScalarType(values)
	if scalar == "" {
		return
	}
	schema.del("anyOf")
	schema.set("type", scalar)
	schema.set("enum", values)
	if _, isString := schema.get("description").(string); !isString && described > 0 {
		schema.set("description", branchDescription)
	}
}

func walkSchema(node any) {
	switch typed := node.(type) {
	case []any:
		for _, child := range typed {
			walkSchema(child)
		}
	case *jmap:
		rewriteNullableScalarAnyOf(typed)
		if values, isArray := typed.get("enum").([]any); !typed.has("type") && isArray {
			if scalar := homogeneousEnumScalarType(values); scalar != "" {
				typed.set("type", scalar)
			}
		}
		collapseConstUnionAnyOf(typed)
		for _, key := range append([]string(nil), typed.keys...) {
			walkSchema(typed.vals[key])
		}
	}
}

func parseArkObjectKey(key string) (string, string) {
	match := arkKeyComment.FindStringSubmatch(key)
	if match == nil {
		return key, ""
	}
	return strings.TrimSpace(match[1]), strings.TrimSpace(match[2])
}

func withArkKeyDescription(schema any, description string) any {
	if description == "" {
		return schema
	}
	if m, ok := asMap(schema); ok {
		if _, isString := m.get("description").(string); !isString {
			m.set("description", description)
		}
		return m
	}
	wrapper := newJmap()
	wrapper.set("anyOf", []any{schema})
	wrapper.set("description", description)
	return wrapper
}

func normalizeArkPropertyComments(node any) {
	switch typed := node.(type) {
	case []any:
		for _, child := range typed {
			normalizeArkPropertyComments(child)
		}
		return
	case *jmap:
		if properties, ok := asMap(typed.get("properties")); ok {
			if required, isArray := typed.get("required").([]any); isArray {
				names := make([]any, len(required))
				for i, entry := range required {
					if name, isString := entry.(string); isString {
						names[i], _ = parseArkObjectKey(name)
					} else {
						names[i] = entry
					}
				}
				typed.set("required", names)
			}
			for _, key := range append([]string(nil), properties.keys...) {
				name, description := parseArkObjectKey(key)
				schema := properties.get(key)
				if description != "" {
					schema = withArkKeyDescription(schema, description)
					properties.del(key)
					properties.set(name, schema)
				}
				normalizeArkPropertyComments(schema)
			}
		}
		for _, key := range wireValueKeys {
			if typed.has(key) {
				normalizeArkPropertyComments(typed.get(key))
			}
		}
		for _, key := range wireMapKeys {
			if key == "properties" {
				continue
			}
			if m, ok := asMap(typed.get(key)); ok {
				for _, name := range m.keys {
					normalizeArkPropertyComments(m.vals[name])
				}
			}
		}
		for _, key := range wireArrayKeys {
			if array, ok := typed.get(key).([]any); ok {
				for _, child := range array {
					normalizeArkPropertyComments(child)
				}
			}
		}
	}
}

func isEmptyMap(value any) bool {
	m, ok := asMap(value)
	return ok && len(m.keys) == 0
}

func normalizeEmptySchemas(node any) {
	switch typed := node.(type) {
	case []any:
		for _, child := range typed {
			normalizeEmptySchemas(child)
		}
	case *jmap:
		for _, key := range wireValueKeys {
			if typed.has(key) && isEmptyMap(typed.get(key)) {
				typed.set(key, true)
			}
		}
		for _, key := range wireMapKeys {
			if m, ok := asMap(typed.get(key)); ok {
				for _, name := range m.keys {
					if isEmptyMap(m.vals[name]) {
						m.vals[name] = true
					}
				}
			}
		}
		for _, key := range wireArrayKeys {
			if array, ok := typed.get(key).([]any); ok {
				for i := range array {
					if isEmptyMap(array[i]) {
						array[i] = true
					}
				}
			}
		}
		for _, key := range typed.keys {
			normalizeEmptySchemas(typed.vals[key])
		}
	}
}

// ---------------------------------------------------------------------------
// normalize.ts: tryEnforceStrictSchema
// ---------------------------------------------------------------------------

var (
	nonStructuralSchemaKeys = map[string]bool{
		"format": true, "pattern": true, "minLength": true, "maxLength": true, "minimum": true, "maximum": true,
		"exclusiveMinimum": true, "exclusiveMaximum": true, "minItems": true, "maxItems": true, "uniqueItems": true,
		"multipleOf": true, "$schema": true, "examples": true, "default": true, "title": true, "$comment": true,
		"if": true, "then": true, "else": true, "not": true, "unevaluatedProperties": true, "unevaluatedItems": true,
		"patternProperties": true, "propertyNames": true, "contains": true, "minContains": true, "maxContains": true,
		"dependentRequired": true, "dependentSchemas": true, "contentEncoding": true, "contentMediaType": true,
		"contentSchema": true, "deprecated": true, "readOnly": true, "writeOnly": true, "minProperties": true,
		"maxProperties": true, "$dynamicRef": true, "$dynamicAnchor": true,
	}
	combinatorKeys    = []string{"anyOf", "allOf", "oneOf"}
	errStrictUnusable = errors.New("schema is not representable in strict mode")
)

func isCombinatorKey(key string) bool {
	return key == "anyOf" || key == "allOf" || key == "oneOf"
}

// tryEnforceStrictSchema returns the strict schema, or the upgraded schema
// and false when strict mode cannot represent it.
func tryEnforceStrictSchema(schema *jmap) (any, bool) {
	upgraded := upgradeSchema(schema)
	root, ok := asMap(upgraded)
	if !ok {
		return upgraded, false
	}
	if hasUnrepresentableStrictObjectMap(root) {
		return upgraded, false
	}
	strict := &strictSanitizer{root: root, cache: map[*jmap]*jmap{}, seen: map[*jmap]bool{}}
	sanitized := strict.sanitize(root)
	enforced, err := enforceStrictSchema(sanitized, map[*jmap]*jmap{}, map[*jmap]bool{})
	if err != nil {
		return upgraded, false
	}
	return enforced, true
}

func isUnrepresentableStrictBranch(value any) bool {
	_, isBool := value.(bool)
	return isBool || isEmptyMap(value)
}

func hasUnrepresentableStrictObjectMap(schema *jmap) bool {
	if patterns, ok := asMap(schema.get("patternProperties")); ok && len(patterns.keys) > 0 {
		return true
	}
	if additional := schema.get("additionalProperties"); additional == true {
		return true
	} else if _, ok := asMap(additional); ok {
		return true
	}
	check := func(value any) bool {
		if isUnrepresentableStrictBranch(value) {
			return true
		}
		m, ok := asMap(value)
		return ok && hasUnrepresentableStrictObjectMap(m)
	}
	if properties, ok := asMap(schema.get("properties")); ok {
		for _, key := range properties.keys {
			if check(properties.vals[key]) {
				return true
			}
		}
	}
	items := schema.get("items")
	if isUnrepresentableStrictBranch(items) {
		return true
	}
	if m, ok := asMap(items); ok {
		if hasUnrepresentableStrictObjectMap(m) {
			return true
		}
	} else if array, isArray := items.([]any); isArray {
		for _, entry := range array {
			if check(entry) {
				return true
			}
		}
	}
	if array, ok := schema.get("prefixItems").([]any); ok {
		for _, entry := range array {
			if check(entry) {
				return true
			}
		}
	}
	for _, key := range combinatorKeys {
		if array, ok := schema.get(key).([]any); ok {
			for _, entry := range array {
				if check(entry) {
					return true
				}
			}
		}
	}
	for _, key := range []string{"$defs", "definitions"} {
		if defs, ok := asMap(schema.get(key)); ok {
			for _, name := range defs.keys {
				if check(defs.vals[name]) {
					return true
				}
			}
		}
	}
	return false
}

func primitiveTypeOf(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "boolean"
	}
	return ""
}

func inferStrictPrimitiveType(node *jmap) string {
	var values []any
	if array, ok := node.get("enum").([]any); ok {
		values = array
	} else if node.has("const") {
		values = []any{node.get("const")}
	}
	inferred := ""
	for _, value := range values {
		kind := primitiveTypeOf(value)
		if kind == "" {
			return ""
		}
		if inferred == "" {
			inferred = kind
		} else if inferred != kind {
			return ""
		}
	}
	return inferred
}

func typeAccepts(kind string, value any) bool {
	switch kind {
	case "null":
		return value == nil
	case "string":
		_, ok := value.(string)
		return ok
	case "number":
		_, ok := value.(float64)
		return ok
	case "integer":
		number, ok := value.(float64)
		return ok && number == math.Trunc(number) && !math.IsInf(number, 0)
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "object":
		_, ok := asMap(value)
		return ok
	}
	return true
}

func narrowEnumToType(schema *jmap, kind string) bool {
	values, ok := schema.get("enum").([]any)
	if !ok {
		return true
	}
	var narrowed []any
	for _, value := range values {
		if typeAccepts(kind, value) {
			narrowed = append(narrowed, value)
		}
	}
	if len(narrowed) == 0 {
		return false
	}
	if len(narrowed) != len(values) {
		schema.set("enum", narrowed)
	}
	return true
}

func resolveStrictRef(root *jmap, ref string) *jmap {
	if !strings.HasPrefix(ref, "#/") {
		return nil
	}
	var cursor any = root
	for _, raw := range strings.Split(ref[2:], "/") {
		m, ok := asMap(cursor)
		if !ok {
			return nil
		}
		cursor = m.get(strings.ReplaceAll(strings.ReplaceAll(raw, "~1", "/"), "~0", "~"))
	}
	m, _ := asMap(cursor)
	return m
}

// strictSanitizer is sanitizeSchemaForStrictMode with its per-call cache and
// cycle guard.
type strictSanitizer struct {
	root  *jmap
	cache map[*jmap]*jmap
	seen  map[*jmap]bool
}

func (s *strictSanitizer) child(value any) any {
	if m, ok := asMap(value); ok {
		return s.sanitize(m)
	}
	return value
}

func (s *strictSanitizer) children(value []any) []any {
	out := make([]any, len(value))
	for i, entry := range value {
		out[i] = s.child(entry)
	}
	return out
}

func (s *strictSanitizer) sanitize(schema *jmap) *jmap {
	if cached := s.cache[schema]; cached != nil {
		return cached
	}
	if s.seen[schema] {
		return newJmap()
	}
	s.seen[schema] = true

	if ref, ok := schema.get("$ref").(string); ok && len(schema.keys) > 1 {
		if resolved := resolveStrictRef(s.root, ref); resolved != nil {
			merged := resolved.clone()
			for _, key := range schema.keys {
				if key != "$ref" {
					merged.set(key, schema.vals[key])
				}
			}
			result := s.sanitize(merged)
			s.cache[schema] = result
			return result
		}
	}
	if allOf, ok := schema.get("allOf").([]any); ok && len(allOf) == 1 {
		if sole, isMap := asMap(allOf[0]); isMap {
			merged := schema.clone()
			merged.del("allOf")
			for _, key := range sole.keys {
				merged.set(key, sole.vals[key])
			}
			result := s.sanitize(merged)
			s.cache[schema] = result
			return result
		}
	}

	if types, ok := schema.get("type").([]any); ok {
		var variantTypes []string
		for _, entry := range types {
			if kind, isString := entry.(string); isString {
				variantTypes = append(variantTypes, kind)
			}
		}
		withoutType := schema.clone()
		withoutType.del("type")
		sanitizedWithoutType := s.sanitize(withoutType)
		if len(variantTypes) == 0 {
			s.cache[schema] = sanitizedWithoutType
			return sanitizedWithoutType
		}
		base := sanitizedWithoutType.clone()
		description, hasDescription := base.get("description"), base.has("description")
		base.del("description")
		var variants []any
		for _, kind := range variantTypes {
			variant := base.clone()
			variant.set("type", kind)
			if kind != "object" {
				variant.del("properties")
				variant.del("required")
				variant.del("additionalProperties")
			}
			if kind != "array" {
				variant.del("items")
			}
			if !narrowEnumToType(variant, kind) {
				continue
			}
			variants = append(variants, s.sanitize(variant))
		}
		if len(variants) == 0 {
			s.cache[schema] = sanitizedWithoutType
			return sanitizedWithoutType
		}
		if len(variants) == 1 {
			sole := variants[0].(*jmap)
			if hasDescription && !sole.has("description") {
				sole.set("description", description)
			}
			s.cache[schema] = sole
			return sole
		}
		result := newJmap()
		result.set("anyOf", variants)
		if hasDescription {
			result.set("description", description)
		}
		s.cache[schema] = result
		return result
	}

	sanitized := newJmap()
	s.cache[schema] = sanitized
	for _, key := range schema.keys {
		value := schema.vals[key]
		if nonStructuralSchemaKeys[key] || key == "type" || key == "const" || key == "nullable" || key == "additionalProperties" {
			continue
		}
		switch {
		case key == "properties":
			if properties, ok := asMap(value); ok {
				out := newJmap()
				for _, name := range properties.keys {
					out.set(name, s.child(properties.vals[name]))
				}
				sanitized.set(key, out)
				continue
			}
		case key == "items":
			if array, ok := value.([]any); ok {
				sanitized.set(key, s.children(array))
			} else {
				sanitized.set(key, s.child(value))
			}
			continue
		case key == "prefixItems" || isCombinatorKey(key):
			if array, ok := value.([]any); ok {
				sanitized.set(key, s.children(array))
				continue
			}
		case key == "$defs" || key == "definitions":
			if defs, ok := asMap(value); ok {
				out := newJmap()
				for _, name := range defs.keys {
					out.set(name, s.child(defs.vals[name]))
				}
				sanitized.set(key, out)
				continue
			}
		case key == "description":
			if text, ok := value.(string); ok && schema.has("default") {
				fallback := schema.get("default")
				formatted, isString := fallback.(string)
				if !isString {
					formatted = string(encodeTree(fallback))
				}
				if !strings.Contains(text, "(default:") {
					text += " (default: " + formatted + ")"
				}
				sanitized.set(key, text)
				continue
			}
		}
		sanitized.set(key, value)
	}
	if schema.has("const") {
		constant := schema.get("const")
		existing, _ := sanitized.get("enum").([]any)
		found := false
		for _, value := range existing {
			if jsonEqual(value, constant) {
				found = true
				break
			}
		}
		if !found {
			existing = append(existing, constant)
		}
		sanitized.set("enum", existing)
	}
	if kind, ok := schema.get("type").(string); ok {
		sanitized.set("type", kind)
	}
	if !sanitized.has("type") {
		if _, ok := asMap(sanitized.get("properties")); ok {
			sanitized.set("type", "object")
		}
	}
	if !sanitized.has("type") && (sanitized.has("items") || sanitized.has("prefixItems")) {
		sanitized.set("type", "array")
	}
	if !sanitized.has("type") {
		if inferred := inferStrictPrimitiveType(sanitized); inferred != "" {
			sanitized.set("type", inferred)
		}
	}
	if schema.get("nullable") == true {
		inner := sanitized.clone()
		inner.del("nullable")
		description, hasDescription := inner.get("description"), inner.has("description")
		inner.del("description")
		null := newJmap()
		null.set("type", "null")
		wrapper := newJmap()
		wrapper.set("anyOf", []any{inner, null})
		if hasDescription {
			wrapper.set("description", description)
		}
		return wrapper
	}
	return sanitized
}

func isPureAnyOfNode(value any) bool {
	m, ok := asMap(value)
	if !ok {
		return false
	}
	if _, isArray := m.get("anyOf").([]any); !isArray {
		return false
	}
	for _, key := range m.keys {
		if key != "anyOf" && key != "description" {
			return false
		}
	}
	return true
}

func nullSchema() *jmap {
	null := newJmap()
	null.set("type", "null")
	return null
}

// enforceStrictSchema closes objects, requires every property and makes the
// optional ones nullable; it fails for nodes strict mode cannot type.
func enforceStrictSchema(schema *jmap, cache map[*jmap]*jmap, active map[*jmap]bool) (*jmap, error) {
	if active[schema] {
		return nil, errStrictUnusable
	}
	if cached := cache[schema]; cached != nil {
		return cached, nil
	}
	active[schema] = true
	defer delete(active, schema)
	result := schema.clone()
	cache[schema] = result
	enforce := func(value any) (any, error) {
		if m, ok := asMap(value); ok {
			return enforceStrictSchema(m, cache, active)
		}
		return value, nil
	}
	enforceAll := func(values []any) ([]any, error) {
		out := make([]any, len(values))
		for i, value := range values {
			processed, err := enforce(value)
			if err != nil {
				return nil, err
			}
			out[i] = processed
		}
		return out, nil
	}

	if result.get("type") == "object" {
		result.set("additionalProperties", false)
		props, _ := asMap(result.get("properties"))
		if props == nil {
			props = newJmap()
		}
		required := map[string]bool{}
		if names, ok := result.get("required").([]any); ok {
			for _, name := range names {
				if text, isString := name.(string); isString {
					required[text] = true
				}
			}
		}
		strictProperties := newJmap()
		for _, key := range props.keys {
			processed, err := enforce(props.vals[key])
			if err != nil {
				return nil, err
			}
			if required[key] {
				strictProperties.set(key, processed)
				continue
			}
			if m, ok := asMap(processed); ok {
				if variants, isArray := m.get("anyOf").([]any); isArray {
					nullable := false
					for _, variant := range variants {
						if v, isMap := asMap(variant); isMap && v.get("type") == "null" {
							nullable = true
						}
					}
					if nullable {
						strictProperties.set(key, processed)
						continue
					}
				}
			}
			if isPureAnyOfNode(processed) {
				m := processed.(*jmap).clone()
				m.set("anyOf", append(append([]any(nil), m.get("anyOf").([]any)...), nullSchema()))
				strictProperties.set(key, m)
				continue
			}
			if m, ok := asMap(processed); ok {
				if description, isString := m.get("description").(string); isString {
					inner := m.clone()
					inner.del("description")
					wrapper := newJmap()
					wrapper.set("anyOf", []any{inner, nullSchema()})
					wrapper.set("description", description)
					strictProperties.set(key, wrapper)
					continue
				}
			}
			wrapper := newJmap()
			wrapper.set("anyOf", []any{processed, nullSchema()})
			strictProperties.set(key, wrapper)
		}
		result.set("properties", strictProperties)
		names := make([]any, len(strictProperties.keys))
		for i, key := range strictProperties.keys {
			names[i] = key
		}
		result.set("required", names)
	}
	switch items := result.get("items").(type) {
	case []any:
		processed, err := enforceAll(items)
		if err != nil {
			return nil, err
		}
		result.set("items", processed)
	case *jmap:
		processed, err := enforceStrictSchema(items, cache, active)
		if err != nil {
			return nil, err
		}
		result.set("items", processed)
	}
	for _, key := range append([]string{"prefixItems"}, combinatorKeys...) {
		if array, ok := result.get(key).([]any); ok {
			processed, err := enforceAll(array)
			if err != nil {
				return nil, err
			}
			result.set(key, processed)
		}
	}
	if variants, ok := result.get("anyOf").([]any); ok {
		spliced := false
		for _, variant := range variants {
			if isPureAnyOfNode(variant) {
				spliced = true
			}
		}
		if spliced {
			var flattened []any
			for _, variant := range variants {
				if !isPureAnyOfNode(variant) {
					flattened = append(flattened, variant)
					continue
				}
				inner := variant.(*jmap)
				flattened = append(flattened, inner.get("anyOf").([]any)...)
				if description, isString := inner.get("description").(string); isString && !result.has("description") {
					result.set("description", description)
				}
			}
			result.set("anyOf", flattened)
		}
	}
	for _, key := range []string{"$defs", "definitions"} {
		if defs, ok := asMap(result.get(key)); ok {
			next := newJmap()
			for _, name := range defs.keys {
				processed, err := enforce(defs.vals[name])
				if err != nil {
					return nil, err
				}
				next.set(name, processed)
			}
			result.set(key, next)
		}
	}
	if !result.has("type") {
		if inferred := inferStrictPrimitiveType(result); inferred != "" {
			result.set("type", inferred)
		}
	}
	if !result.has("type") && !result.has("$ref") {
		combinator := false
		for _, key := range combinatorKeys {
			if _, ok := result.get(key).([]any); ok {
				combinator = true
			}
		}
		if _, isMap := asMap(result.get("not")); !combinator && !isMap {
			return nil, errStrictUnusable
		}
	}
	return result, nil
}

// ToolParametersSchema is the Responses function-tool parameter path shared by
// NeoPi's OpenAI providers: toolWireSchema, sanitizeSchemaForOpenAIResponses,
// flattenExclusiveRequiredRootUnion when the provider rejects object-root
// unions, then adaptSchemaForStrict. It reports whether strict mode held.
func ToolParametersSchema(parameters gjson.Result, strict, flattenRootUnion bool) (json.RawMessage, bool) {
	wire := postProcessSchema(upgradeSchema(decodeTree(parameters)))
	sanitized := decodeTree(gjson.ParseBytes(sanitizeResponsesSchema(gjson.ParseBytes(encodeTree(wire)))))
	if flattenRootUnion {
		if root, ok := asMap(sanitized); ok {
			sanitized = flattenExclusiveRequiredRootUnion(root)
		}
	}
	upgraded := upgradeSchema(sanitized)
	if !strict {
		return encodeTree(upgraded), false
	}
	root, ok := asMap(upgraded)
	if !ok {
		return encodeTree(upgraded), false
	}
	enforced, effective := tryEnforceStrictSchema(root)
	return encodeTree(enforced), effective
}

// ToolWireSchema is NeoPi's toolWireSchema: the tool's parameters upgraded to
// draft 2020-12 and post-processed, serialized like JSON.stringify.
func ToolWireSchema(parameters gjson.Result) json.RawMessage {
	return encodeTree(postProcessSchema(upgradeSchema(decodeTree(parameters))))
}

// flattenExclusiveRequiredRootUnion drops an object-root anyOf/oneOf whose
// branches are only typeless required-key fragments (xAI rejects them).
func flattenExclusiveRequiredRootUnion(schema *jmap) any {
	unionKey := ""
	if _, ok := schema.get("anyOf").([]any); ok {
		unionKey = "anyOf"
	} else if _, ok = schema.get("oneOf").([]any); ok {
		unionKey = "oneOf"
	}
	if unionKey == "" {
		return schema
	}
	union := schema.get(unionKey).([]any)
	if len(union) == 0 {
		return schema
	}
	typedObject := schema.get("type") == "object"
	if types, ok := schema.get("type").([]any); ok {
		for _, t := range types {
			typedObject = typedObject || t == "object"
		}
	}
	if _, hasProperties := asMap(schema.get("properties")); !typedObject && !hasProperties {
		return schema
	}
	for _, branch := range union {
		if !isExclusiveRequiredBranch(branch) {
			return schema
		}
	}
	flattened := schema.clone()
	flattened.del(unionKey)
	return flattened
}

func isExclusiveRequiredBranch(value any) bool {
	branch, ok := asMap(value)
	if !ok || branch.has("type") {
		return false
	}
	required, ok := branch.get("required").([]any)
	if !ok || len(required) == 0 {
		return false
	}
	for _, name := range required {
		if text, isText := name.(string); !isText || text == "" {
			return false
		}
	}
	for _, key := range branch.keys {
		if key != "required" && key != "description" && key != "title" {
			return false
		}
	}
	return true
}
