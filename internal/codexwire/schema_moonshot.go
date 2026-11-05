package codexwire

import "encoding/json"

// moonshotUnsupportedSchemaFields is isMoonshotUnsupportedSchemaField:
// NON_STRUCTURAL_SCHEMA_KEYS minus `default` (an MFJS meta-data field, kept)
// plus the tuple form `prefixItems`.
var moonshotUnsupportedSchemaFields = setOf("format", "pattern", "minLength", "maxLength", "minimum", "maximum",
	"exclusiveMinimum", "exclusiveMaximum", "minItems", "maxItems", "uniqueItems", "multipleOf", "$schema", "examples",
	"title", "$comment", "if", "then", "else", "not", "unevaluatedProperties", "unevaluatedItems", "patternProperties",
	"propertyNames", "contains", "minContains", "maxContains", "dependentRequired", "dependentSchemas",
	"contentEncoding", "contentMediaType", "contentSchema", "deprecated", "readOnly", "writeOnly", "minProperties",
	"maxProperties", "$dynamicRef", "$dynamicAnchor", "prefixItems")

var moonshotNormalizeOptions = &normalizeOptions{
	permissiveBooleans: true,
	unsupported:        moonshotUnsupportedSchemaFields,
	stripNullable:      true,
	foldOneOf:          true,
	dropNonScalarEnum:  true,
}

// NormalizeSchemaForMoonshot is normalizeSchemaForMoonshot: tool parameters
// reshaped into Moonshot Flavored JSON Schema (api.moonshot.ai, api.kimi.com).
// It shares NormalizeSchemaForGoogle's engine; the option set pins permissive
// boolean subschemas, stripped `nullable`, residual `oneOf` folded into
// `anyOf`, non-scalar enums dropped and `title`/validation keywords spilled
// into the description. Input is expected to be a tool wire schema (the output
// of ToolWireSchema).
func NormalizeSchemaForMoonshot(schema json.RawMessage) json.RawMessage {
	return normalizeSchemaWith(schema, moonshotNormalizeOptions)
}
