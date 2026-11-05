package openaiwire

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/codexwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// convertTools is openai-responses convertTools for downstream tools. Function
// tools take NeoPi's schema path and are quarantined when their schema would
// make the provider reject the whole turn; other declared tools are forwarded
// unchanged. It also returns the function names that survived.
func convertTools(tools []json.RawMessage, compat *registry.NeoPiCompat, strictMode bool) ([]json.RawMessage, map[string]bool) {
	out := make([]json.RawMessage, 0, len(tools))
	emitted := map[string]bool{}
	for _, raw := range tools {
		tool := gjson.ParseBytes(raw)
		if tool.Get("type").String() != "function" {
			out = append(out, jsjson.Normalize(raw))
			continue
		}
		name := tool.Get("name").String()
		explicitFalse := tool.Get("strict").Type == gjson.False
		strict := strictMode && !explicitFalse
		parameters, effectiveStrict := codexwire.ToolParametersSchema(tool.Get("parameters"), strict, compat.RejectRootObjectUnion)
		if violation := findStrictToolSchemaViolation(gjson.ParseBytes(parameters), "#", compat.RejectRootObjectUnion); violation != "" {
			log.Warnf("Tool %q omitted from the openai-responses request: its parameter schema is invalid for this provider at %s (an enum/const value cannot match its declared type). Other tools are unaffected.", name, violation)
			continue
		}
		fields := []jsjson.Field{
			{Name: "type", Value: jsjson.Marshal("function")},
			{Name: "name", Value: jsjson.Marshal(name)},
			{Name: "description", Value: jsjson.Marshal(tool.Get("description").String())},
			{Name: "parameters", Value: parameters},
		}
		if effectiveStrict {
			fields = append(fields, jsjson.Field{Name: "strict", Value: jsjson.Marshal(true)})
		} else if strictMode && explicitFalse {
			fields = append(fields, jsjson.Field{Name: "strict", Value: jsjson.Marshal(false)})
		}
		out = append(out, jsjson.Object(fields...))
		emitted[name] = true
	}
	return out, emitted
}

// mapToolChoice is mapOpenAIResponsesToolChoiceForTools over the tools that
// survived quarantine.
func mapToolChoice(choice json.RawMessage, emitted map[string]bool, compat *registry.NeoPiCompat) json.RawMessage {
	if !compat.SupportsToolChoice {
		return nil
	}
	if isForcedToolChoice(choice) && !compat.SupportsForcedToolChoice {
		return jsjson.Marshal("auto")
	}
	parsed := gjson.ParseBytes(choice)
	if parsed.Type == gjson.String {
		return jsjson.Marshal(parsed.String())
	}
	if parsed.Get("type").String() != "function" {
		return jsjson.Normalize(choice)
	}
	name := parsed.Get("name").String()
	if !emitted[name] {
		return nil
	}
	return jsjson.Object(
		jsjson.Field{Name: "type", Value: jsjson.Marshal("function")},
		jsjson.Field{Name: "name", Value: jsjson.Marshal(name)},
	)
}

var schemaTypeNames = map[string]bool{"string": true, "number": true, "integer": true, "boolean": true, "object": true, "array": true, "null": true}

func declaredTypes(node gjson.Result) []string {
	value := node.Get("type")
	if value.Type == gjson.String {
		if schemaTypeNames[value.String()] {
			return []string{value.String()}
		}
		return nil
	}
	var out []string
	for _, entry := range value.Array() {
		if entry.Type == gjson.String && schemaTypeNames[entry.String()] {
			out = append(out, entry.String())
		}
	}
	return out
}

func valueMatchesType(value gjson.Result, kind string) bool {
	switch kind {
	case "string":
		return value.Type == gjson.String
	case "number":
		return value.Type == gjson.Number
	case "integer":
		return value.Type == gjson.Number && value.Num == math.Trunc(value.Num) && !math.IsInf(value.Num, 0)
	case "boolean":
		return value.Type == gjson.True || value.Type == gjson.False
	case "null":
		return value.Type == gjson.Null
	case "object":
		return value.IsObject()
	case "array":
		return value.IsArray()
	}
	return true
}

func matchesAny(value gjson.Result, types []string) bool {
	for _, kind := range types {
		if valueMatchesType(value, kind) {
			return true
		}
	}
	return false
}

// findStrictToolSchemaViolation returns the JSON-pointer-ish path of the first
// enum/const-vs-type contradiction (or, for xAI, a leftover object-root union
// with a non-object branch), or "" when the schema is safe to emit.
func findStrictToolSchemaViolation(schema gjson.Result, path string, rejectRootObjectUnion bool) string {
	if schema.IsArray() {
		for i, entry := range schema.Array() {
			if hit := findStrictToolSchemaViolation(entry, fmt.Sprintf("%s/%d", path, i), rejectRootObjectUnion); hit != "" {
				return hit
			}
		}
		return ""
	}
	if !schema.IsObject() {
		return ""
	}
	types := declaredTypes(schema)
	if len(types) > 0 {
		if enum := schema.Get("enum"); enum.IsArray() {
			for _, value := range enum.Array() {
				if !matchesAny(value, types) {
					return path + "/enum"
				}
			}
		}
		if constant := schema.Get("const"); constant.Exists() && !matchesAny(constant, types) {
			return path + "/const"
		}
	}
	if rejectRootObjectUnion && path == "#" {
		typedObject := false
		for _, kind := range types {
			typedObject = typedObject || kind == "object"
		}
		if typedObject || schema.Get("properties").IsObject() {
			for _, key := range []string{"anyOf", "oneOf"} {
				branches := schema.Get(key)
				if !branches.IsArray() || len(branches.Array()) == 0 {
					continue
				}
				for _, branch := range branches.Array() {
					objectBranch := false
					if branch.IsObject() {
						for _, kind := range declaredTypes(branch) {
							objectBranch = objectBranch || kind == "object"
						}
					}
					if !objectBranch {
						return path + "/" + key
					}
				}
			}
		}
	}
	for _, key := range []string{"properties", "patternProperties", "$defs", "definitions", "dependentSchemas"} {
		if sub := schema.Get(gjsonKey(key)); sub.IsObject() {
			var hit string
			sub.ForEach(func(name, child gjson.Result) bool {
				hit = findStrictToolSchemaViolation(child, path+"/"+key+"/"+name.String(), rejectRootObjectUnion)
				return hit == ""
			})
			if hit != "" {
				return hit
			}
		}
	}
	for _, key := range []string{"items", "contains", "not", "if", "then", "else", "propertyNames", "additionalProperties", "unevaluatedProperties", "unevaluatedItems"} {
		if child := schema.Get(gjsonKey(key)); child.Exists() {
			if hit := findStrictToolSchemaViolation(child, path+"/"+key, rejectRootObjectUnion); hit != "" {
				return hit
			}
		}
	}
	for _, key := range []string{"anyOf", "oneOf", "allOf", "prefixItems"} {
		if child := schema.Get(key); child.IsArray() {
			if hit := findStrictToolSchemaViolation(child, path+"/"+key, rejectRootObjectUnion); hit != "" {
				return hit
			}
		}
	}
	return ""
}

// gjsonKey escapes a literal object key for a gjson path.
func gjsonKey(key string) string {
	return strings.NewReplacer("$", `\$`, ".", `\.`, "*", `\*`, "?", `\?`).Replace(key)
}
