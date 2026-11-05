package codexwire

import (
	"encoding/json"
	"net/url"
	"strings"

	"github.com/tidwall/gjson"
)

// NeoPi's normalizeSchema with the normalizeSchemaForGoogle option set: the
// tool-schema subset Google's Schema proto accepts (Gemini backends, Devin's
// Gemini lanes). Option-gated branches that set never reaches are omitted.

var googleUnsupportedSchemaFields = setOf("$schema", "$id", "$anchor", "$ref", "$defs", "$dynamicRef", "$dynamicAnchor",
	"examples", "prefixItems", "unevaluatedProperties", "unevaluatedItems", "patternProperties", "additionalProperties",
	"propertyNames", "minItems", "maxItems", "uniqueItems", "contains", "minContains", "maxContains", "additionalItems",
	"minLength", "maxLength", "minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf", "pattern",
	"format", "contentEncoding", "contentMediaType", "contentSchema", "dependencies", "dependentSchemas",
	"dependentRequired", "x-mcp-header", "deprecated", "readOnly", "writeOnly", "$comment")

var liftableToDescriptionFields = setOf("pattern", "format", "minLength", "maxLength", "minimum", "maximum",
	"exclusiveMinimum", "exclusiveMaximum", "multipleOf", "minItems", "maxItems", "uniqueItems", "minProperties",
	"maxProperties", "default", "examples")

var ccaTypeSpecificKeys = map[string]map[string]bool{
	"array": setOf("items", "prefixItems", "contains", "minContains", "maxContains", "minItems", "maxItems", "uniqueItems",
		"unevaluatedItems"),
	"object": setOf("properties", "required", "additionalProperties", "patternProperties", "propertyNames", "minProperties",
		"maxProperties", "dependentRequired", "dependentSchemas", "unevaluatedProperties"),
	"string":  setOf("minLength", "maxLength", "pattern", "format", "contentEncoding", "contentMediaType"),
	"number":  setOf("minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf"),
	"integer": setOf("minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf"),
	"boolean": {},
	"null":    {},
}

var allCCATypeSpecificKeys = func() map[string]bool {
	out := map[string]bool{}
	for _, keys := range ccaTypeSpecificKeys {
		for key := range keys {
			out[key] = true
		}
	}
	return out
}()

var snakeToCamelRenames = map[string]string{"additional_properties": "additionalProperties", "any_of": "anyOf",
	"prefix_items": "prefixItems", "property_ordering": "propertyOrdering"}

var (
	normalizeSubschemaValueKeys  = setOf("items", "additionalItems", "unevaluatedItems", "not", "if", "then", "else", "contains", "propertyNames", "contentSchema")
	normalizeBooleanOrSchemaKeys = setOf("additionalProperties", "unevaluatedProperties")
	normalizeSubschemaArrayKeys  = setOf("anyOf", "oneOf", "allOf", "prefixItems")
	normalizeSubschemaMapKeys    = setOf("properties", "patternProperties", "dependencies", "dependentSchemas", "$defs", "definitions")
)

func setOf(keys ...string) map[string]bool {
	out := make(map[string]bool, len(keys))
	for _, key := range keys {
		out[key] = true
	}
	return out
}

// normalizeOptions is the subset of NeoPi's NormalizeSchemaOptions that the
// Go-ported provider option sets (Google, Moonshot) differ on.
type normalizeOptions struct {
	permissiveBooleans   bool // coerceBooleanSubschemas "permissive": false becomes {} instead of {not:{}}
	unsupported          map[string]bool
	renameFields         bool // normalizeFieldNames
	collapseNull         bool // collapseNullFields
	stripNullable        bool // stripNullableKeyword
	autoPropertyOrdering bool
	ensureObjectProps    bool // ensureObjectProperties
	stringEnumsOnly      bool
	foldOneOf            bool // foldOneOfIntoAnyOf
	dropNonScalarEnum    bool
}

var googleNormalizeOptions = &normalizeOptions{
	unsupported:          googleUnsupportedSchemaFields,
	renameFields:         true,
	collapseNull:         true,
	autoPropertyOrdering: true,
	ensureObjectProps:    true,
	stringEnumsOnly:      true,
}

// NormalizeSchemaForGoogle is normalizeSchemaForGoogle over a JSON schema.
func NormalizeSchemaForGoogle(schema json.RawMessage) json.RawMessage {
	return normalizeSchemaWith(schema, googleNormalizeOptions)
}

// normalizeSchemaWith is normalizeSchema: upgrade, dereference, then the node walk.
func normalizeSchemaWith(schema json.RawMessage, options *normalizeOptions) json.RawMessage {
	upgraded := upgradeSchema(decodeTree(gjson.ParseBytes(schema)))
	return encodeTree(options.node(dereferenceSchema(upgraded), false, true))
}

// classifyNormalizeChild is classifySchemaChild: "schema", "map" or "".
func classifyNormalizeChild(key string, value any, insideSchemaMap bool) string {
	if insideSchemaMap {
		return "schema"
	}
	if renamed, ok := snakeToCamelRenames[key]; ok {
		key = renamed
	}
	if normalizeSubschemaMapKeys[key] {
		return "map"
	}
	if normalizeSubschemaValueKeys[key] || normalizeSubschemaArrayKeys[key] {
		return "schema"
	}
	if _, isMap := asMap(value); normalizeBooleanOrSchemaKeys[key] && isMap {
		return "schema"
	}
	return ""
}

func hasUnrepresentableGoogleEnum(value any, insideSchemaMap bool) bool {
	if items, ok := value.([]any); ok {
		for _, item := range items {
			if hasUnrepresentableGoogleEnum(item, false) {
				return true
			}
		}
		return false
	}
	node, ok := asMap(value)
	if !ok {
		return false
	}
	if insideSchemaMap {
		for _, key := range node.keys {
			if hasUnrepresentableGoogleEnum(node.vals[key], false) {
				return true
			}
		}
		return false
	}
	if enum, isArray := node.get("enum").([]any); isArray {
		if len(enum) == 0 {
			return true
		}
		for _, entry := range enum {
			if _, isText := entry.(string); !isText {
				return true
			}
		}
	}
	if node.has("const") {
		if _, isText := node.get("const").(string); !isText {
			return true
		}
	}
	for _, key := range node.keys {
		if kind := classifyNormalizeChild(key, node.vals[key], false); kind != "" && hasUnrepresentableGoogleEnum(node.vals[key], kind == "map") {
			return true
		}
	}
	return false
}

func applySnakeCaseRenames(obj *jmap) *jmap {
	rename := false
	for _, key := range obj.keys {
		if _, ok := snakeToCamelRenames[key]; ok {
			rename = true
			break
		}
	}
	if !rename {
		return obj
	}
	out := newJmap()
	for _, key := range obj.keys {
		if renamed, ok := snakeToCamelRenames[key]; ok {
			out.set(renamed, obj.vals[key])
		} else if !out.has(key) {
			out.set(key, obj.vals[key])
		}
	}
	return out
}

func preHandleNullFields(obj *jmap) *jmap {
	if obj.get("type") == "null" {
		out := newJmap()
		for _, key := range obj.keys {
			if key != "type" {
				out.set(key, obj.vals[key])
			}
		}
		out.set("nullable", true)
		return out
	}
	variants, ok := obj.get("anyOf").([]any)
	if !ok {
		return obj
	}
	sawNull := false
	var kept []any
	for _, variant := range variants {
		if node, isMap := asMap(variant); isMap && node.get("type") == "null" {
			sawNull = true
			continue
		}
		kept = append(kept, variant)
	}
	if !sawNull {
		return obj
	}
	out := obj.clone()
	out.set("nullable", true)
	switch {
	case len(kept) == 0:
		out.del("anyOf")
	case len(kept) == 1:
		if only, isMap := asMap(kept[0]); isMap {
			out.del("anyOf")
			for _, key := range only.keys {
				if !out.has(key) {
					out.set(key, only.vals[key])
				}
			}
			return out
		}
		out.set("anyOf", kept)
	default:
		out.set("anyOf", kept)
	}
	return out
}

func splitTypeArrayIntoAnyOf(obj *jmap) *jmap {
	typeList, ok := obj.get("type").([]any)
	if !ok {
		return obj
	}
	if _, hasAnyOf := obj.get("anyOf").([]any); hasAnyOf {
		return obj
	}
	var types []string
	hasNull := false
	for _, entry := range typeList {
		text, isText := entry.(string)
		if !isText {
			continue
		}
		if text == "null" {
			hasNull = true
			continue
		}
		duplicate := false
		for _, existing := range types {
			duplicate = duplicate || existing == text
		}
		if !duplicate {
			types = append(types, text)
		}
	}
	if len(types) < 2 {
		return obj
	}
	branches := make([]*jmap, len(types))
	for i, kind := range types {
		branches[i] = newJmap()
		branches[i].set("type", kind)
	}
	out := newJmap()
	for _, key := range obj.keys {
		if key == "type" {
			continue
		}
		value := obj.vals[key]
		if !allCCATypeSpecificKeys[key] {
			out.set(key, value)
			continue
		}
		for i, kind := range types {
			if ccaTypeSpecificKeys[kind][key] {
				branches[i].set(key, value)
			}
		}
	}
	if hasNull {
		out.set("nullable", true)
	}
	anyOf := make([]any, len(branches))
	for i, branch := range branches {
		anyOf[i] = branch
	}
	out.set("anyOf", anyOf)
	return out
}

func dropForeignTypeKeywords(schema *jmap, kind string) {
	allowed := ccaTypeSpecificKeys[kind]
	for _, key := range append([]string(nil), schema.keys...) {
		if allCCATypeSpecificKeys[key] && !allowed[key] {
			schema.del(key)
		}
	}
}

func inferJSONType(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case []any:
		return "array"
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "boolean"
	case *jmap:
		return "object"
	}
	return ""
}

func pushEnumValue(values []any, value any) []any {
	for _, existing := range values {
		if jsonEqual(existing, value) {
			return values
		}
	}
	return append(values, value)
}

type spillEntry struct {
	key   string
	value any
}

// spillToDescription demotes stripped keywords into the node's description.
func spillToDescription(node *jmap, spill []spillEntry) {
	if len(spill) == 0 {
		return
	}
	parts := make([]string, len(spill))
	for i, entry := range spill {
		parts[i] = entry.key + ": " + string(encodeTree(entry.value))
	}
	formatted := "{" + strings.Join(parts, ", ") + "}"
	if existing, ok := node.get("description").(string); ok && existing != "" {
		node.set("description", existing+"\n\n"+formatted)
		return
	}
	node.set("description", formatted)
}

func (o *normalizeOptions) node(value any, insideSchemaMap, booleanIsSubschema bool) any {
	switch typed := value.(type) {
	case []any:
		out := make([]any, len(typed))
		for i, entry := range typed {
			out[i] = o.node(entry, insideSchemaMap, booleanIsSubschema)
		}
		return out
	case bool:
		if !booleanIsSubschema {
			return typed
		}
		if typed || o.permissiveBooleans {
			return newJmap()
		}
		not := newJmap()
		not.set("not", newJmap())
		return not
	case *jmap:
		return o.object(typed, insideSchemaMap, booleanIsSubschema)
	}
	return value
}

func (o *normalizeOptions) child(key string, entry any, insideSchemaMap bool) any {
	kind := classifyNormalizeChild(key, entry, insideSchemaMap)
	if kind == "" {
		return entry
	}
	return o.node(entry, kind == "map", kind == "schema")
}

// object is normalizeSchemaObjectNode. One JS quirk is not reproduced: a `type`
// array without any string member leaves `type: undefined` in place in NeoPi,
// so a later const/enum inference reuses its key slot; here the key is removed
// and re-added at the end.
func (o *normalizeOptions) object(value *jmap, insideSchemaMap, booleanIsSubschema bool) any {
	obj := value
	if !insideSchemaMap {
		if o.renameFields {
			obj = applySnakeCaseRenames(obj)
		}
		obj = splitTypeArrayIntoAnyOf(obj)
		if o.collapseNull {
			obj = preHandleNullFields(obj)
		}
	}
	result := newJmap()
	var spill []spillEntry
	// skipKey reports whether a keyword is dropped from the output, spilling
	// stripped liftable keywords into the description.
	skipKey := func(key string, entry any) bool {
		if !insideSchemaMap && o.unsupported[key] {
			if liftableToDescriptionFields[key] {
				spill = append(spill, spillEntry{key: key, value: entry})
			}
			return true
		}
		if o.stripNullable && key == "nullable" {
			return true
		}
		return o.stringEnumsOnly && !insideSchemaMap && key == "not" && hasUnrepresentableGoogleEnum(entry, false)
	}
	for _, combiner := range []string{"anyOf", "oneOf"} {
		variants, ok := obj.get(combiner).([]any)
		if !ok || len(variants) == 0 {
			continue
		}
		allConst := true
		for _, variant := range variants {
			node, isMap := asMap(variant)
			allConst = allConst && isMap && node.has("const")
		}
		if !allConst {
			continue
		}
		var enum []any
		for _, variant := range variants {
			node, _ := asMap(variant)
			enum = pushEnumValue(enum, node.get("const"))
		}
		result.set("enum", enum)
		sameType := ""
		for i, variant := range variants {
			node, _ := asMap(variant)
			kind, isText := node.get("type").(string)
			if !isText || (i > 0 && kind != sameType) {
				sameType = ""
				break
			}
			sameType = kind
		}
		if sameType != "" {
			result.set("type", sameType)
		} else {
			var inferred []string
			for _, entry := range enum {
				if kind := inferJSONType(entry); kind != "" {
					inferred = append(inferred, kind)
				}
			}
			distinct, nonNull, sawNull := map[string]bool{}, map[string]bool{}, false
			firstNonNull := ""
			for _, kind := range inferred {
				distinct[kind] = true
				if kind == "null" {
					sawNull = true
					continue
				}
				if !nonNull[kind] && firstNonNull == "" {
					firstNonNull = kind
				}
				nonNull[kind] = true
			}
			switch {
			case len(distinct) == 1:
				result.set("type", inferred[0])
			case sawNull && len(nonNull) == 1:
				result.set("type", firstNonNull)
				if !o.stripNullable {
					result.set("nullable", true)
				}
			}
		}
		for _, key := range obj.keys {
			if key == combiner || result.has(key) {
				continue
			}
			entry := obj.vals[key]
			if skipKey(key, entry) {
				continue
			}
			result.set(key, o.child(key, entry, insideSchemaMap))
		}
		spillToDescription(result, spill)
		return o.postProcess(result, booleanIsSubschema)
	}

	var constValue any
	hasConst := false
	for _, key := range obj.keys {
		entry := obj.vals[key]
		if skipKey(key, entry) {
			continue
		}
		if key == "const" {
			constValue, hasConst = entry, true
			continue
		}
		result.set(key, o.child(key, entry, insideSchemaMap))
	}
	if typeList, ok := result.get("type").([]any); ok {
		var types, nonNull []string
		for _, entry := range typeList {
			if text, isText := entry.(string); isText {
				types = append(types, text)
				if text != "null" {
					nonNull = append(nonNull, text)
				}
			}
		}
		if len(types) > len(nonNull) && !o.stripNullable {
			result.set("nullable", true)
		}
		switch {
		case len(nonNull) > 0:
			result.set("type", nonNull[0])
		case len(types) > 0:
			result.set("type", types[0])
		default:
			result.del("type")
		}
		if kind, isText := result.get("type").(string); isText {
			dropForeignTypeKeywords(result, kind)
		}
	}
	if hasConst {
		var enum []any
		if existing, ok := result.get("enum").([]any); ok {
			enum = append(enum, existing...)
		}
		result.set("enum", pushEnumValue(enum, constValue))
		if !truthy(result.get("type")) {
			result.set("type", inferJSONType(constValue))
		}
	}
	if enum, ok := result.get("enum").([]any); ok && len(enum) > 0 && !truthy(result.get("type")) {
		_, anyOf := result.get("anyOf").([]any)
		_, oneOf := result.get("oneOf").([]any)
		if !anyOf && !oneOf {
			first := inferJSONType(enum[0])
			same := first != ""
			for _, entry := range enum[1:] {
				same = same && inferJSONType(entry) == first
			}
			if same {
				result.set("type", first)
			}
		}
	}
	if o.collapseNull && result.get("type") == "null" {
		result.del("type")
		result.set("nullable", true)
	}
	if o.autoPropertyOrdering && result.get("type") == "object" && !result.has("propertyOrdering") {
		if props, ok := asMap(result.get("properties")); ok && len(props.keys) > 1 {
			ordering := make([]any, len(props.keys))
			for i, key := range props.keys {
				ordering[i] = key
			}
			result.set("propertyOrdering", ordering)
		}
	}
	if o.ensureObjectProps && result.get("type") == "object" && !result.has("properties") {
		result.set("properties", newJmap())
	}
	spillToDescription(result, spill)
	return o.postProcess(result, booleanIsSubschema)
}

// truthy is JavaScript truthiness for a schema keyword value.
func truthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return typed != ""
	case bool:
		return typed
	case float64:
		return typed != 0
	}
	return true
}

// postProcess is applyNodePostProcessing for the option sets ported here:
// residual oneOf folds into anyOf, and enums the provider cannot express drop.
func (o *normalizeOptions) postProcess(schema *jmap, booleanIsSubschema bool) *jmap {
	current := schema
	if o.foldOneOf {
		current = foldOneOfIntoAnyOf(current)
	}
	if o.dropNonScalarEnum {
		current = dropNonScalarEnum(current)
	}
	if o.stringEnumsOnly && booleanIsSubschema {
		current = dropNonStringEnum(current)
	}
	return current
}

// foldOneOfIntoAnyOf: MFJS recognizes only anyOf, so a residual oneOf is
// appended to it (taking anyOf's slot when present, the end otherwise).
func foldOneOfIntoAnyOf(schema *jmap) *jmap {
	oneOf, ok := schema.get("oneOf").([]any)
	if !ok {
		return schema
	}
	out := schema.clone()
	out.del("oneOf")
	var merged []any
	if existing, isArray := out.get("anyOf").([]any); isArray {
		merged = append(merged, existing...)
	}
	out.set("anyOf", append(merged, oneOf...))
	return out
}

// dropNonScalarEnum: MFJS enums admit only string and number literals.
func dropNonScalarEnum(schema *jmap) *jmap {
	enum, ok := schema.get("enum").([]any)
	if !ok {
		return schema
	}
	for _, entry := range enum {
		switch entry.(type) {
		case string, float64:
		default:
			out := schema.clone()
			out.del("enum")
			return out
		}
	}
	return schema
}

// dropNonStringEnum: Google's enum field takes non-empty string lists only.
func dropNonStringEnum(schema *jmap) *jmap {
	enum, ok := schema.get("enum").([]any)
	if !ok {
		return schema
	}
	allText := len(enum) > 0
	for _, entry := range enum {
		_, isText := entry.(string)
		allText = allText && isText
	}
	if allText {
		return schema
	}
	out := schema.clone()
	out.del("enum")
	return out
}

// dereferenceSchema is dereferenceJsonSchema: local $defs/definitions refs
// inlined, cycles broken with {}, the definition blocks dropped.
func dereferenceSchema(schema any) any {
	root, ok := asMap(schema)
	if !ok || (!root.has("$defs") && !root.has("definitions")) {
		return schema
	}
	return dereferenceNode(root, root, map[string]bool{})
}

var (
	derefMapKeys   = setOf("properties", "patternProperties", "dependencies", "dependentSchemas")
	derefArrayKeys = setOf("anyOf", "oneOf", "allOf", "prefixItems")
	derefValueKeys = setOf("items", "additionalItems", "contains", "contentSchema", "propertyNames", "if", "then", "else", "not",
		"additionalProperties", "unevaluatedItems", "unevaluatedProperties")
)

func dereferenceKeyword(key string, value any, root *jmap, visiting map[string]bool) any {
	if node, ok := asMap(value); ok && derefMapKeys[key] {
		out := newJmap()
		for _, name := range node.keys {
			out.set(name, dereferenceNode(node.vals[name], root, visiting))
		}
		return out
	}
	if items, ok := value.([]any); ok && (derefArrayKeys[key] || key == "items") {
		out := make([]any, len(items))
		for i, item := range items {
			out[i] = dereferenceNode(item, root, visiting)
		}
		return out
	}
	if derefValueKeys[key] {
		return dereferenceNode(value, root, visiting)
	}
	return value
}

func resolveLocalRef(ref string, root *jmap) (*jmap, bool) {
	if !strings.HasPrefix(ref, "#") {
		return nil, false
	}
	pointer, err := url.PathUnescape(ref[1:])
	if err != nil || (pointer != "" && !strings.HasPrefix(pointer, "/")) {
		return nil, false
	}
	tokens := strings.Split(pointer, "/")[1:]
	if len(tokens) != 2 {
		return nil, false
	}
	for i, token := range tokens {
		tokens[i] = strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
	}
	if tokens[0] != "$defs" && tokens[0] != "definitions" {
		return nil, false
	}
	defs, ok := asMap(root.get(tokens[0]))
	if !ok {
		return nil, false
	}
	resolved, ok := asMap(defs.get(tokens[1]))
	return resolved, ok
}

func dereferenceNode(value any, root *jmap, visiting map[string]bool) any {
	node, ok := asMap(value)
	if !ok {
		return value
	}
	if ref, isText := node.get("$ref").(string); isText {
		if visiting[ref] {
			return newJmap()
		}
		resolved, found := resolveLocalRef(ref, root)
		if !found {
			return node
		}
		visiting[ref] = true
		inlined := dereferenceNode(resolved, root, visiting)
		delete(visiting, ref)
		hasSiblings := false
		for _, key := range node.keys {
			if key != "$ref" && key != "$defs" && key != "definitions" {
				hasSiblings = true
				break
			}
		}
		inlinedMap, isMap := asMap(inlined)
		if !hasSiblings || !isMap {
			return inlined
		}
		merged := inlinedMap.clone()
		for _, key := range node.keys {
			if key == "$ref" || key == "$defs" || key == "definitions" {
				continue
			}
			merged.set(key, dereferenceKeyword(key, node.vals[key], root, visiting))
		}
		return merged
	}
	out := newJmap()
	for _, key := range node.keys {
		if key == "$defs" || key == "definitions" {
			continue
		}
		out.set(key, dereferenceKeyword(key, node.vals[key], root, visiting))
	}
	return out
}
