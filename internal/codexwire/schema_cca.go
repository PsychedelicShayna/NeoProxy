package codexwire

import (
	"encoding/json"
	"math"
	"regexp"

	"github.com/tidwall/gjson"
)

// NeoPi's normalizeSchemaForCCA: normalizeSchema with the Cloud Code Assist
// option set, applied to every Antigravity tool schema (Claude tools run it on
// the wire schema, Gemini tools on the normalizeSchemaForGoogle output). The
// walker is shared with the Google option set (schema_normalize.go); this file
// holds the CCA-only passes: object/same-type/mixed-type combiner collapsing,
// the residual-combiner fixpoint, nullable extraction, the residual
// incompatibility gate and the meta-validation fallback.

var ccaNormalizeOptions = schemaNormalizeOptions{
	stripNullableKeyword:       true,
	mergeObjectCombiners:       true,
	collapseSameTypeCombiners:  true,
	collapseMixedTypeCombiners: true,
}

// ccaFallbackSchema is CLOUD_CODE_ASSIST_CLAUDE_FALLBACK_SCHEMA.
func ccaFallbackSchema() *jmap {
	schema := newJmap()
	schema.set("type", "object")
	schema.set("properties", newJmap())
	return schema
}

var ccaSharedSchemaKeys = setOf("title", "description", "default", "examples")

// NormalizeSchemaForCCA is normalizeSchemaForCCA over a JSON schema: what
// NeoPi sends as `parameters` for Claude tools on Antigravity.
func NormalizeSchemaForCCA(schema json.RawMessage) json.RawMessage {
	return encodeTree(normalizeSchemaTreeForCCA(decodeTree(gjson.ParseBytes(schema))))
}

// NormalizeSchemaForGoogleThenCCA is normalizeSchemaForCCA(normalizeSchemaForGoogle(schema)):
// what NeoPi sends as `parameters` for Gemini tools on Antigravity
// (normalizeAntigravityTools). The Google result stays a tree between the
// passes, as in NeoPi, so keys that do not survive JSON encoding still count.
func NormalizeSchemaForGoogleThenCCA(schema json.RawMessage) json.RawMessage {
	google := normalizeSchemaTreeForGoogle(decodeTree(gjson.ParseBytes(schema)))
	return encodeTree(normalizeSchemaTreeForCCA(google))
}

func normalizeSchemaTreeForCCA(schema any) any {
	upgraded := upgradeSchema(schema)
	normalized := normalizeSchemaNode(dereferenceSchema(upgraded), ccaNormalizeOptions, false, true)
	normalized = stripResidualCombiners(normalized)
	normalized, _ = normalizeNullableProperties(normalized, false, false)
	if hasResidualSchemaIncompatibilities(normalized, false) || !isValidJSONSchema(normalized) {
		return ccaFallbackSchema()
	}
	return normalized
}

// copySchemaWithout is copySchemaWithout: a shallow copy minus one key.
func copySchemaWithout(schema *jmap, key string) *jmap {
	out := schema.clone()
	out.del(key)
	return out
}

// ---------------------------------------------------------------------------
// equality.ts
// ---------------------------------------------------------------------------

// sameField is areJsonValuesEqual(a[key], b[key]) with undefined distinct from null.
func sameField(left, right *jmap, key string) bool {
	return left.has(key) == right.has(key) && jsonEqual(left.get(key), right.get(key))
}

// mergeCompatibleEnumSchemas unions two enum schemas that agree on `type` and every
// non-enum field; nil otherwise.
func mergeCompatibleEnumSchemas(existing, incoming any) *jmap {
	left, leftOK := asMap(existing)
	right, rightOK := asMap(incoming)
	if !leftOK || !rightOK {
		return nil
	}
	leftEnum, leftIsArray := left.get("enum").([]any)
	rightEnum, rightIsArray := right.get("enum").([]any)
	if !leftIsArray || !rightIsArray || !sameField(left, right, "type") {
		return nil
	}
	nonEnumCount := func(node *jmap) int {
		count := 0
		for _, key := range node.keys {
			if key != "enum" {
				count++
			}
		}
		return count
	}
	if nonEnumCount(left) != nonEnumCount(right) {
		return nil
	}
	for _, key := range left.keys {
		if key != "enum" && !(right.has(key) && jsonEqual(left.get(key), right.get(key))) {
			return nil
		}
	}
	merged := append([]any(nil), leftEnum...)
	for _, value := range rightEnum {
		merged = pushEnumValue(merged, value)
	}
	out := left.clone()
	out.set("enum", merged)
	return out
}

func anyOfVariants(schema any) []any {
	if node, ok := asMap(schema); ok {
		if variants, isArray := node.get("anyOf").([]any); isArray {
			return variants
		}
	}
	return []any{schema}
}

// mergePropertySchemas widens two schemas for one property name into their union.
func mergePropertySchemas(existing, incoming any) any {
	if jsonEqual(existing, incoming) {
		return existing
	}
	if merged := mergeCompatibleEnumSchemas(existing, incoming); merged != nil {
		return merged
	}
	union := append([]any(nil), anyOfVariants(existing)...)
	for _, variant := range anyOfVariants(incoming) {
		union = pushEnumValue(union, variant)
	}
	if len(union) == 1 {
		return union[0]
	}
	out := newJmap()
	out.set("anyOf", union)
	return out
}

// ---------------------------------------------------------------------------
// Combiner collapsing (applyNodePostProcessing, stripResidualCombiners)
// ---------------------------------------------------------------------------

func stringEntries(value any) []string {
	var out []string
	if items, ok := value.([]any); ok {
		for _, item := range items {
			if text, isText := item.(string); isText {
				out = append(out, text)
			}
		}
	}
	return out
}

// mergeObjectCombinerVariants folds all-object-shaped combiner branches into the
// node's own properties; the schema itself when any branch is not object-shaped.
func mergeObjectCombinerVariants(schema *jmap, combiner string) *jmap {
	raw, ok := schema.get(combiner).([]any)
	if !ok || len(raw) == 0 {
		return schema
	}
	variants := make([]*jmap, 0, len(raw))
	for _, entry := range raw {
		node, isMap := asMap(entry)
		if !isMap {
			return schema
		}
		_, hasProperties := asMap(node.get("properties"))
		_, hasRequired := node.get("required").([]any)
		hasObjectShape := hasProperties || hasRequired || node.has("additionalProperties")
		if !node.has("type") && !hasObjectShape {
			return schema
		}
		if node.has("type") && node.get("type") != "object" {
			return schema
		}
		if node.has("properties") && !hasProperties {
			return schema
		}
		if node.has("required") && !hasRequired {
			return schema
		}
		variants = append(variants, node)
	}

	merged := newJmap()
	own, _ := asMap(schema.get("properties"))
	if own != nil {
		for _, name := range own.keys {
			merged.set(name, own.vals[name])
		}
	}
	for _, variant := range variants {
		properties, isMap := asMap(variant.get("properties"))
		if !isMap {
			continue
		}
		for _, name := range properties.keys {
			if merged.has(name) {
				merged.set(name, mergePropertySchemas(merged.get(name), properties.vals[name]))
			} else {
				merged.set(name, properties.vals[name])
			}
		}
	}

	next := copySchemaWithout(schema, combiner)
	next.set("type", "object")
	next.set("properties", merged)

	safeRequired := map[string]bool{}
	if combiner == "allOf" {
		for _, variant := range variants {
			for _, name := range stringEntries(variant.get("required")) {
				safeRequired[name] = merged.has(name)
			}
		}
	} else {
		var intersection []string
		for i, variant := range variants {
			required := stringEntries(variant.get("required"))
			if i == 0 {
				intersection = required
				continue
			}
			present := map[string]bool{}
			for _, name := range required {
				present[name] = true
			}
			kept := intersection[:0:0]
			for _, name := range intersection {
				if present[name] {
					kept = append(kept, name)
				}
			}
			intersection = kept
		}
		for _, name := range intersection {
			safeRequired[name] = merged.has(name)
		}
	}
	for _, name := range stringEntries(schema.get("required")) {
		if own != nil && own.has(name) && merged.has(name) {
			safeRequired[name] = true
		}
	}
	ordered := []any{}
	for _, name := range merged.keys {
		if safeRequired[name] {
			ordered = append(ordered, name)
		}
	}
	if len(ordered) > 0 {
		next.set("required", ordered)
	} else {
		next.del("required")
	}
	return next
}

func mergeSchemaDescriptions(existing, incoming any) string {
	current, isText := existing.(string)
	if !isText {
		if text, incomingText := incoming.(string); incomingText {
			return text
		}
		return ""
	}
	text, incomingText := incoming.(string)
	if !incomingText || text == "" || current == text {
		return current
	}
	if current == "" {
		return text
	}
	return current + "\n\n" + text
}

// collapseMixedTypeCombinerVariants folds a combiner of distinct-typed scalar
// branches into the first non-null type; the schema itself when the branches do
// not fit.
func collapseMixedTypeCombinerVariants(schema *jmap, combiner string) *jmap {
	raw, ok := schema.get(combiner).([]any)
	if !ok || len(raw) == 0 {
		return schema
	}
	seen := map[string]bool{}
	var variantTypes []string
	merged := newJmap()
	for _, entry := range raw {
		node, isMap := asMap(entry)
		if !isMap {
			return schema
		}
		variantType, isText := node.get("type").(string)
		if !isText || seen[variantType] {
			return schema
		}
		allowed, known := ccaTypeSpecificKeys[variantType]
		if !known {
			return schema
		}
		for _, key := range node.keys {
			if key == "type" {
				continue
			}
			if !allowed[key] && !ccaSharedSchemaKeys[key] {
				return schema
			}
			value := node.vals[key]
			if merged.has(key) && !jsonEqual(merged.get(key), value) {
				if key != "description" {
					return schema
				}
				merged.set(key, mergeSchemaDescriptions(merged.get(key), value))
				continue
			}
			merged.set(key, value)
		}
		seen[variantType] = true
		variantTypes = append(variantTypes, variantType)
	}
	allObject := true
	for _, kind := range variantTypes {
		allObject = allObject && kind == "object"
	}
	if len(variantTypes) < 2 || allObject {
		return schema
	}
	next := copySchemaWithout(schema, combiner)
	chosen := variantTypes[0]
	for _, kind := range variantTypes {
		if kind != "null" {
			chosen = kind
			break
		}
	}
	next.set("type", chosen)
	chosenAllowed := ccaTypeSpecificKeys[chosen]
	dropForeignTypeKeywords(next, chosen)
	for _, key := range merged.keys {
		if !chosenAllowed[key] && !ccaSharedSchemaKeys[key] {
			continue
		}
		value := merged.vals[key]
		if next.has(key) && !jsonEqual(next.get(key), value) {
			if key != "description" {
				return schema
			}
			next.set(key, mergeSchemaDescriptions(next.get(key), value))
			continue
		}
		if !next.has(key) {
			next.set(key, value)
		}
	}
	return next
}

// collapseSameTypeCombinerVariants folds a combiner whose branches all share one
// `type` into a single schema, unioning enum members when every branch is an enum.
func collapseSameTypeCombinerVariants(schema *jmap, combiner string) *jmap {
	raw, ok := schema.get(combiner).([]any)
	if !ok || len(raw) == 0 {
		return schema
	}
	commonType := ""
	variants := make([]*jmap, 0, len(raw))
	for i, entry := range raw {
		node, isMap := asMap(entry)
		if !isMap {
			return schema
		}
		kind, isText := node.get("type").(string)
		if !isText {
			return schema
		}
		if i == 0 {
			commonType = kind
		} else if kind != commonType {
			return schema
		}
		variants = append(variants, node)
	}
	first := variants[0]
	enumVariants := 0
	for _, variant := range variants {
		if _, isArray := variant.get("enum").([]any); isArray {
			enumVariants++
		}
	}
	collapsed := first
	switch {
	case enumVariants == len(variants):
		merged := first
		for i := 1; i < len(variants) && merged != nil; i++ {
			merged = mergeCompatibleEnumSchemas(merged, variants[i])
		}
		if merged == nil {
			return schema
		}
		collapsed = merged
	case enumVariants > 0:
		for _, variant := range variants {
			if _, isArray := variant.get("enum").([]any); !isArray {
				collapsed = variant
				break
			}
		}
	}
	next := copySchemaWithout(schema, combiner)
	for _, key := range collapsed.keys {
		if !next.has(key) {
			next.set(key, collapsed.vals[key])
		}
	}
	return next
}

// stripResidualCombiners re-runs the same-type and mixed-type collapses to a
// fixpoint, since object-combiner merging can synthesize anyOf after the child
// walk ran.
func stripResidualCombiners(value any) any {
	return stripResidualCombinersNode(value, false)
}

func stripResidualCombinersNode(value any, insideSchemaMap bool) any {
	if items, ok := value.([]any); ok {
		out := make([]any, len(items))
		for i, entry := range items {
			out[i] = stripResidualCombinersNode(entry, false)
		}
		return out
	}
	node, ok := asMap(value)
	if !ok {
		return value
	}
	result := newJmap()
	for _, key := range node.keys {
		entry := node.vals[key]
		if kind := classifyNormalizeChild(key, entry, insideSchemaMap); kind != "" {
			result.set(key, stripResidualCombinersNode(entry, kind == "map"))
		} else {
			result.set(key, entry)
		}
	}
	if insideSchemaMap {
		return result
	}
	current := result
	for changed := true; changed; {
		changed = false
		for _, combiner := range []string{"anyOf", "oneOf"} {
			if sameType := collapseSameTypeCombinerVariants(current, combiner); sameType != current {
				current, changed = sameType, true
			}
			if mixed := collapseMixedTypeCombinerVariants(current, combiner); mixed != current {
				current, changed = mixed, true
			}
		}
	}
	return current
}

// ---------------------------------------------------------------------------
// Nullable extraction
// ---------------------------------------------------------------------------

// extractNullableUnionSchema lifts a nullable marker out of a property schema:
// `nullable: true`, `type: [T, "null"]`, or a combiner of one schema plus a bare
// `{type:"null"}` branch.
func extractNullableUnionSchema(schema any) (any, bool) {
	node, ok := asMap(schema)
	if !ok {
		return schema, false
	}
	if node.get("nullable") == true {
		return copySchemaWithout(node, "nullable"), true
	}
	if typeList, isArray := node.get("type").([]any); isArray {
		hasNull := false
		var nonNull []string
		for _, kind := range stringEntries(typeList) {
			if kind == "null" {
				hasNull = true
			} else {
				nonNull = append(nonNull, kind)
			}
		}
		if hasNull && len(nonNull) == 1 {
			next := node.clone()
			next.set("type", nonNull[0])
			return next, true
		}
	}
	for _, combiner := range []string{"anyOf", "oneOf"} {
		raw, isArray := node.get(combiner).([]any)
		if !isArray {
			continue
		}
		hasNullVariant := false
		var nonNullVariants []any
		for _, variant := range raw {
			if entry, isMap := asMap(variant); isMap && entry.get("type") == "null" && len(entry.keys) == 1 {
				hasNullVariant = true
				continue
			}
			nonNullVariants = append(nonNullVariants, variant)
		}
		if !hasNullVariant || len(nonNullVariants) != 1 {
			continue
		}
		only, isMap := asMap(nonNullVariants[0])
		if !isMap {
			continue
		}
		next := copySchemaWithout(node, combiner)
		for _, key := range only.keys {
			value := only.vals[key]
			if next.has(key) && !jsonEqual(next.get(key), value) {
				return schema, false
			}
			if !next.has(key) {
				next.set(key, value)
			}
		}
		return next, true
	}
	return schema, false
}

// normalizeNullableProperties is normalizeNullablePropertiesForCloudCodeAssist:
// nullable properties are extracted into optionality (dropped from `required`).
func normalizeNullableProperties(value any, isPropertySchema, insideSchemaMap bool) (any, bool) {
	if items, ok := value.([]any); ok {
		out := make([]any, len(items))
		for i, entry := range items {
			out[i], _ = normalizeNullableProperties(entry, false, false)
		}
		return out, false
	}
	node, ok := asMap(value)
	if !ok {
		return value, false
	}
	normalized := newJmap()
	for _, key := range node.keys {
		entry := node.vals[key]
		if kind := classifyNormalizeChild(key, entry, insideSchemaMap); kind != "" {
			child, _ := normalizeNullableProperties(entry, false, kind == "map")
			normalized.set(key, child)
		} else {
			normalized.set(key, entry)
		}
	}
	if insideSchemaMap {
		return normalized, false
	}
	if properties, isMap := asMap(normalized.get("properties")); isMap {
		var required []string
		seen := map[string]bool{}
		for _, name := range stringEntries(normalized.get("required")) {
			if !seen[name] {
				seen[name] = true
				required = append(required, name)
			}
		}
		next := newJmap()
		for _, name := range properties.keys {
			property, nullable := normalizeNullableProperties(properties.vals[name], true, false)
			next.set(name, property)
			if nullable {
				kept := required[:0:0]
				for _, existing := range required {
					if existing != name {
						kept = append(kept, existing)
					}
				}
				required = kept
			}
		}
		normalized.set("properties", next)
		if _, isArray := normalized.get("required").([]any); isArray {
			out := make([]any, len(required))
			for i, name := range required {
				out[i] = name
			}
			normalized.set("required", out)
		}
	}
	if !isPropertySchema {
		return normalized, false
	}
	return extractNullableUnionSchema(normalized)
}

// ---------------------------------------------------------------------------
// Residual incompatibilities and validation
// ---------------------------------------------------------------------------

// hasResidualSchemaIncompatibilities is hasResidualSchemaIncompatibilities with
// every check enabled ([type-array, type-null, nullable, combiners, not]).
func hasResidualSchemaIncompatibilities(value any, insideSchemaMap bool) bool {
	if items, ok := value.([]any); ok {
		for _, entry := range items {
			if hasResidualSchemaIncompatibilities(entry, false) {
				return true
			}
		}
		return false
	}
	node, ok := asMap(value)
	if !ok {
		return false
	}
	if !insideSchemaMap {
		if _, isArray := node.get("type").([]any); isArray || node.get("type") == "null" || node.has("nullable") || node.has("not") {
			return true
		}
		for _, combiner := range []string{"allOf", "anyOf", "oneOf"} {
			if _, isArray := node.get(combiner).([]any); isArray {
				return true
			}
		}
	}
	for _, key := range node.keys {
		entry := node.vals[key]
		if kind := classifyNormalizeChild(key, entry, insideSchemaMap); kind != "" && hasResidualSchemaIncompatibilities(entry, kind == "map") {
			return true
		}
	}
	return false
}

var jsonSchemaTypeNames = setOf("string", "number", "integer", "boolean", "object", "array", "null")

// isValidJSONSchema is isValidJsonSchema (meta-validator.ts): a structural
// check of the JSON Schema subset NeoPi forwards. The one deliberate
// difference is the `pattern` check, which compiles with Go's RE2 where NeoPi
// uses `new RegExp`; the CCA option set strips `pattern` from every schema
// position the validator visits, so the check is unreachable for its output.
func isValidJSONSchema(schema any) bool {
	return checkSchemaNode(schema)
}

func isNonNegativeInteger(value any) bool {
	number, ok := value.(float64)
	return ok && !math.IsInf(number, 0) && !math.IsNaN(number) && number == math.Trunc(number) && number >= 0
}

func hasUniqueJSONValues(values []any) bool {
	for i := range values {
		for j := i + 1; j < len(values); j++ {
			if jsonEqual(values[i], values[j]) {
				return false
			}
		}
	}
	return true
}

func checkTypeKeyword(value any) bool {
	if text, ok := value.(string); ok {
		return jsonSchemaTypeNames[text]
	}
	list, ok := value.([]any)
	if !ok || len(list) == 0 {
		return false
	}
	seen := map[string]bool{}
	for _, entry := range list {
		text, isText := entry.(string)
		if !isText || !jsonSchemaTypeNames[text] || seen[text] {
			return false
		}
		seen[text] = true
	}
	return true
}

func checkSchemaArray(value any) bool {
	items, ok := value.([]any)
	if !ok {
		return false
	}
	for _, entry := range items {
		if !checkSchemaNode(entry) {
			return false
		}
	}
	return true
}

func checkSchemaMap(value any) bool {
	node, ok := asMap(value)
	if !ok {
		return false
	}
	for _, key := range node.keys {
		if !checkSchemaNode(node.vals[key]) {
			return false
		}
	}
	return true
}

func checkSchemaNode(value any) bool {
	if _, isBool := value.(bool); isBool {
		return true
	}
	node, ok := asMap(value)
	if !ok {
		return false
	}
	if node.has("type") && !checkTypeKeyword(node.get("type")) {
		return false
	}
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		if node.has(key) && !checkSchemaArray(node.get(key)) {
			return false
		}
	}
	for _, key := range []string{"not", "if", "then", "else"} {
		if node.has(key) && !checkSchemaNode(node.get(key)) {
			return false
		}
	}
	for _, key := range []string{"properties", "patternProperties", "$defs", "definitions"} {
		if node.has(key) && !checkSchemaMap(node.get(key)) {
			return false
		}
	}
	for _, key := range []string{"propertyNames", "contains"} {
		if node.has(key) && !checkSchemaNode(node.get(key)) {
			return false
		}
	}
	if node.has("required") {
		required, isArray := node.get("required").([]any)
		if !isArray {
			return false
		}
		seen := map[string]bool{}
		for _, entry := range required {
			text, isText := entry.(string)
			if !isText || seen[text] {
				return false
			}
			seen[text] = true
		}
	}
	if node.has("items") {
		if _, isArray := node.get("items").([]any); isArray || !checkSchemaNode(node.get("items")) {
			return false
		}
	}
	if node.has("prefixItems") && !checkSchemaArray(node.get("prefixItems")) {
		return false
	}
	if node.has("additionalItems") || node.has("dependencies") {
		return false
	}
	for _, key := range []string{"additionalProperties", "unevaluatedProperties", "unevaluatedItems"} {
		if !node.has(key) {
			continue
		}
		if _, isBool := node.get(key).(bool); !isBool && !checkSchemaNode(node.get(key)) {
			return false
		}
	}
	if node.has("dependentSchemas") && !checkSchemaMap(node.get("dependentSchemas")) {
		return false
	}
	if node.has("dependentRequired") {
		required, isMap := asMap(node.get("dependentRequired"))
		if !isMap {
			return false
		}
		for _, key := range required.keys {
			entries, isArray := required.vals[key].([]any)
			if !isArray {
				return false
			}
			for _, entry := range entries {
				if _, isText := entry.(string); !isText {
					return false
				}
			}
		}
	}
	if node.has("enum") {
		enum, isArray := node.get("enum").([]any)
		if !isArray || len(enum) == 0 || !hasUniqueJSONValues(enum) {
			return false
		}
	}
	for _, key := range []string{"minimum", "maximum", "multipleOf"} {
		if node.has(key) {
			if _, isNumber := node.get(key).(float64); !isNumber {
				return false
			}
		}
	}
	if multiple, isNumber := node.get("multipleOf").(float64); isNumber && multiple <= 0 {
		return false
	}
	for _, key := range []string{"exclusiveMinimum", "exclusiveMaximum"} {
		if node.has(key) {
			_, isNumber := node.get(key).(float64)
			_, isBool := node.get(key).(bool)
			if !isNumber && !isBool {
				return false
			}
		}
	}
	for _, key := range []string{"minLength", "maxLength", "minItems", "maxItems", "minProperties", "maxProperties", "minContains", "maxContains"} {
		if node.has(key) && !isNonNegativeInteger(node.get(key)) {
			return false
		}
	}
	if node.has("uniqueItems") {
		if _, isBool := node.get("uniqueItems").(bool); !isBool {
			return false
		}
	}
	if node.has("pattern") {
		pattern, isText := node.get("pattern").(string)
		if !isText {
			return false
		}
		if _, err := regexp.Compile(pattern); err != nil {
			return false
		}
	}
	if node.has("format") {
		if _, isText := node.get("format").(string); !isText {
			return false
		}
	}
	for _, key := range []string{"nullable", "readOnly", "writeOnly", "deprecated"} {
		if node.has(key) {
			if _, isBool := node.get(key).(bool); !isBool {
				return false
			}
		}
	}
	return true
}
