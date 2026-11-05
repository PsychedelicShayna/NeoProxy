package antigravity

import (
	"strings"

	"github.com/tidwall/gjson"
)

// NeoPi validates both Cloud Code Assist payloads with arktype schemas and
// fails the sign-in with arktype's own summary text. The checks below
// reproduce that text for the shapes the two schemas declare.

// jsonTypeName is arktype's `was …` description of a JSON value. arktype
// treats an array as an object, so an array satisfies an object type whose
// fields are all optional.
func jsonTypeName(value gjson.Result) string {
	switch {
	case !value.Exists():
		return "missing"
	case value.Type == gjson.Null:
		return "null"
	case value.Type == gjson.Number:
		return "a number"
	case value.Type == gjson.String:
		return "a string"
	case value.Type == gjson.True, value.Type == gjson.False:
		return "boolean"
	}
	return "an object"
}

func isObjectLike(value gjson.Result) bool {
	return value.Type == gjson.JSON && (value.IsObject() || value.IsArray())
}

// typeMismatch is `<path> must be <expected> (was <actual>)`.
func typeMismatch(path, expected string, value gjson.Result) string {
	if path == "" {
		return "must be " + expected + " (was " + jsonTypeName(value) + ")"
	}
	return path + " must be " + expected + " (was " + jsonTypeName(value) + ")"
}

func joinPath(prefix, field string) string {
	if prefix == "" {
		return field
	}
	return prefix + "." + field
}

// structField is one declared schema field.
type structField struct {
	name string
	kind string
}

// validateOptionalString checks a `?: string` field.
func validateOptionalString(problems []string, path string, value gjson.Result) []string {
	if value.Type != gjson.String {
		problems = append(problems, typeMismatch(path, "a string", value))
	}
	return problems
}

// validateOptionalNumber checks a `?: number` field.
func validateOptionalNumber(problems []string, path string, value gjson.Result) []string {
	if value.Type != gjson.Number {
		problems = append(problems, typeMismatch(path, "a number", value))
	}
	return problems
}

// validateStructFields validates the present fields of an object-or-array
// value against a schema whose fields are all optional.
func validateStructFields(problems []string, path string, value gjson.Result, fields []structField) []string {
	if !isObjectLike(value) {
		problems = append(problems, typeMismatch(path, "an object", value))
		return problems
	}
	for _, field := range fields {
		fieldValue := value.Get(field.name)
		if !fieldValue.Exists() {
			continue
		}
		fieldPath := joinPath(path, field.name)
		switch field.kind {
		case "string":
			problems = validateOptionalString(problems, fieldPath, fieldValue)
		case "number":
			problems = validateOptionalNumber(problems, fieldPath, fieldValue)
		}
	}
	return problems
}

var tierFields = []structField{{"id", "string"}}

var ineligibleTierFields = []structField{{"tierId", "string"}, {"reasonMessage", "string"}, {"validationUrl", "string"}}

var operationErrorFields = []structField{{"code", "number"}, {"message", "string"}}

// validateLoadCodeAssist is loadCodeAssistResponseSchema.
func validateLoadCodeAssist(payload gjson.Result) []string {
	if !isObjectLike(payload) {
		return []string{typeMismatch("", "an object", payload)}
	}
	var problems []string
	for _, field := range []string{"currentTier", "paidTier"} {
		if value := payload.Get(field); value.Exists() && value.Type != gjson.Null {
			if value.Type != gjson.JSON {
				problems = append(problems, typeMismatch(field, "{ id?: a string } or null", value))
				continue
			}
			problems = validateStructFields(problems, field, value, tierFields)
		}
	}
	if value := payload.Get("allowedTiers"); value.Exists() {
		if !value.IsArray() {
			problems = append(problems, typeMismatch("allowedTiers", "an array", value))
		} else {
			for index, tier := range value.Array() {
				path := "allowedTiers[" + itoa(index) + "]"
				problems = validateStructFields(problems, path, tier, tierFields)
			}
		}
	}
	if value := payload.Get("ineligibleTiers"); value.Exists() {
		if !value.IsArray() {
			problems = append(problems, typeMismatch("ineligibleTiers", "an array", value))
		} else {
			for index, tier := range value.Array() {
				path := "ineligibleTiers[" + itoa(index) + "]"
				problems = validateStructFields(problems, path, tier, ineligibleTierFields)
			}
		}
	}
	if value := payload.Get("cloudaicompanionProject"); value.Exists() {
		problems = validateOptionalString(problems, "cloudaicompanionProject", value)
	}
	return problems
}

// validateOnboardOperation is onboardOperationSchema.
func validateOnboardOperation(payload gjson.Result) []string {
	if !isObjectLike(payload) {
		return []string{typeMismatch("", "an object", payload)}
	}
	var problems []string
	if value := payload.Get("name"); value.Exists() {
		problems = validateOptionalString(problems, "name", value)
	}
	if value := payload.Get("done"); value.Exists() {
		if value.Type != gjson.True && value.Type != gjson.False {
			problems = append(problems, typeMismatch("done", "boolean", value))
		}
	}
	if value := payload.Get("error"); value.Exists() && value.Type != gjson.Null {
		if value.Type != gjson.JSON {
			problems = append(problems, typeMismatch("error", "{ code?: a number, message?: a string } or null", value))
		} else {
			problems = validateStructFields(problems, "error", value, operationErrorFields)
		}
	}
	if value := payload.Get("response"); value.Exists() && value.Type != gjson.Null {
		// `response` has a required `@type`, so an array (which has no
		// properties) fails the whole type rather than a field check.
		if !value.IsObject() {
			problems = append(problems, typeMismatch("response", "{ @type: a string, cloudaicompanionProject?: a string } or null", value))
		} else {
			if atType := value.Get("@type"); !atType.Exists() {
				problems = append(problems, "response.@type must be a string (was missing)")
			} else {
				problems = validateOptionalString(problems, "response.@type", atType)
			}
			if project := value.Get("cloudaicompanionProject"); project.Exists() {
				problems = validateOptionalString(problems, "response.cloudaicompanionProject", project)
			}
		}
	}
	return problems
}

func itoa(value int) string {
	return strings.TrimSpace(jsNumberString(float64(value)))
}

// jsonQuote is JSON.stringify of a string.
func jsonQuote(text string) string {
	var out strings.Builder
	writeJSONString(&out, text)
	return out.String()
}
