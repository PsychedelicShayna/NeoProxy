package codexwire

import (
	"encoding/json"

	"github.com/tidwall/gjson"
)

// ToolParametersSchemaCompletions is NeoPi's Chat Completions tool-schema path:
// toolWireSchema, flattenExclusiveRequiredRootUnion when the host rejects
// object-root unions, then adaptSchemaForStrict. It reports whether strict mode
// held after adaptation.
func ToolParametersSchemaCompletions(parameters gjson.Result, strict, flattenRootUnion bool) (json.RawMessage, bool) {
	wire := postProcessSchema(upgradeSchema(decodeTree(parameters)))
	if flattenRootUnion {
		if root, ok := asMap(wire); ok {
			wire = flattenExclusiveRequiredRootUnion(root)
		}
	}
	upgraded := upgradeSchema(wire)
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
