package codexwire

// NeoPi's option-driven normalizeSchema walker (packages/ai/src/utils/schema/normalize.ts),
// shared by the Google option set (schema_google.go) and the Cloud Code Assist one (schema_cca.go).

// schemaNormalizeOptions is the option-driven part of NeoPi's NormalizeSchemaOptions
// that differs between the Google and CCA option sets. The options both sets pin
// to the same value (field-name normalization, type-array scalarization, object
// `properties` backfill, description spill, bare-enum type inference, `standard`
// boolean subschema coercion, the Google unsupported-field list) are always on.
type schemaNormalizeOptions struct {
	collapseNullFields         bool
	stripNullableKeyword       bool
	autoPropertyOrdering       bool
	stringEnumsOnly            bool
	mergeObjectCombiners       bool
	collapseSameTypeCombiners  bool
	collapseMixedTypeCombiners bool
}

var googleNormalizeOptions = schemaNormalizeOptions{
	collapseNullFields:   true,
	autoPropertyOrdering: true,
	stringEnumsOnly:      true,
}

func normalizeSchemaNode(value any, opts schemaNormalizeOptions, insideSchemaMap, booleanIsSubschema bool) any {
	switch typed := value.(type) {
	case []any:
		out := make([]any, len(typed))
		for i, entry := range typed {
			out[i] = normalizeSchemaNode(entry, opts, insideSchemaMap, booleanIsSubschema)
		}
		return out
	case bool:
		if !booleanIsSubschema {
			return typed
		}
		if typed {
			return newJmap()
		}
		not := newJmap()
		not.set("not", newJmap())
		return not
	case *jmap:
		return normalizeSchemaObject(typed, opts, insideSchemaMap, booleanIsSubschema)
	}
	return value
}

func normalizeSchemaChild(key string, entry any, opts schemaNormalizeOptions, insideSchemaMap bool) any {
	kind := classifyNormalizeChild(key, entry, insideSchemaMap)
	if kind == "" {
		return entry
	}
	return normalizeSchemaNode(entry, opts, kind == "map", kind == "schema")
}

func normalizeSchemaObject(value *jmap, opts schemaNormalizeOptions, insideSchemaMap, booleanIsSubschema bool) any {
	obj := value
	if !insideSchemaMap {
		obj = splitTypeArrayIntoAnyOf(applySnakeCaseRenames(obj))
		if opts.collapseNullFields {
			obj = preHandleNullFields(obj)
		}
	}
	result := newJmap()
	var spill []spillEntry
	// skipKey reports whether a keyword is dropped from the output (unsupported
	// field, the stripped `nullable` keyword, or a `not` Google cannot express).
	skipKey := func(key string, entry any) bool {
		if !insideSchemaMap && googleUnsupportedSchemaFields[key] {
			if liftableToDescriptionFields[key] {
				spill = append(spill, spillEntry{key: key, value: entry})
			}
			return true
		}
		if opts.stripNullableKeyword && key == "nullable" {
			return true
		}
		return opts.stringEnumsOnly && !insideSchemaMap && key == "not" && hasUnrepresentableGoogleEnum(entry, false)
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
				if !opts.stripNullableKeyword {
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
			result.set(key, normalizeSchemaChild(key, entry, opts, insideSchemaMap))
		}
		spillToDescription(result, spill)
		return applyNodePostProcessing(result, opts, booleanIsSubschema)
	}

	var constValue any
	hasConst := false
	for _, key := range obj.keys {
		entry := obj.vals[key]
		if !insideSchemaMap && googleUnsupportedSchemaFields[key] {
			if liftableToDescriptionFields[key] {
				spill = append(spill, spillEntry{key: key, value: entry})
			}
			continue
		}
		if opts.stripNullableKeyword && key == "nullable" {
			continue
		}
		if key == "const" {
			constValue, hasConst = entry, true
			continue
		}
		if opts.stringEnumsOnly && !insideSchemaMap && key == "not" && hasUnrepresentableGoogleEnum(entry, false) {
			continue
		}
		result.set(key, normalizeSchemaChild(key, entry, opts, insideSchemaMap))
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
		if len(types) > len(nonNull) && !opts.stripNullableKeyword {
			result.set("nullable", true)
		}
		switch {
		case len(nonNull) > 0:
			result.set("type", nonNull[0])
		case len(types) > 0:
			result.set("type", types[0])
		default:
			// `result.type = nonNull[0] ?? types[0]` leaves an undefined-valued key.
			result.set("type", jsUndefined{})
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
	if opts.collapseNullFields && result.get("type") == "null" {
		result.del("type")
		if !opts.stripNullableKeyword {
			result.set("nullable", true)
		}
	}
	if opts.autoPropertyOrdering && result.get("type") == "object" && !result.has("propertyOrdering") {
		if props, ok := asMap(result.get("properties")); ok && len(props.keys) > 1 {
			ordering := make([]any, len(props.keys))
			for i, key := range props.keys {
				ordering[i] = key
			}
			result.set("propertyOrdering", ordering)
		}
	}
	if result.get("type") == "object" && !result.has("properties") {
		result.set("properties", newJmap())
	}
	spillToDescription(result, spill)
	return applyNodePostProcessing(result, opts, booleanIsSubschema)
}

// jsUndefined is a key whose JavaScript value is undefined: present for `in` and
// hasOwn checks, omitted by JSON.stringify.
type jsUndefined struct{}

// truthy is JavaScript truthiness for a schema keyword value.
func truthy(value any) bool {
	switch typed := value.(type) {
	case nil, jsUndefined:
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

// applyNodePostProcessing runs the combiner collapses the option set enables,
// then (Google) drops an enum that is not all strings from a subschema node.
func applyNodePostProcessing(schema *jmap, opts schemaNormalizeOptions, booleanIsSubschema bool) *jmap {
	current := schema
	for _, combiner := range []string{"anyOf", "oneOf"} {
		if opts.mergeObjectCombiners {
			current = mergeObjectCombinerVariants(current, combiner)
		}
		if opts.collapseMixedTypeCombiners {
			current = collapseMixedTypeCombinerVariants(current, combiner)
		}
		if opts.collapseSameTypeCombiners {
			current = collapseSameTypeCombinerVariants(current, combiner)
		}
	}
	if !opts.stringEnumsOnly || !booleanIsSubschema {
		return current
	}
	enum, ok := current.get("enum").([]any)
	if !ok {
		return current
	}
	allText := len(enum) > 0
	for _, entry := range enum {
		_, isText := entry.(string)
		allText = allText && isText
	}
	if allText {
		return current
	}
	return copySchemaWithout(current, "enum")
}
