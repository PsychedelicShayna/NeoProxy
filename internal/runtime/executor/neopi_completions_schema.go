package executor

import (
	"encoding/json"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/codexwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/tidwall/gjson"
)

// neoPiCompletionsToolSchema is convertTools' adaptSchemaForStrict step for the
// Chat Completions wire: the tool's parameters upgraded to draft 2020-12,
// strict-enforced when the host takes strict schemas. It reports whether
// strict mode survived adaptation, which decides `strict: true` on the wire.
func neoPiCompletionsToolSchema(parameters gjson.Result, strict bool, compat *registry.NeoPiCompletionsCompat) (json.RawMessage, bool) {
	return codexwire.ToolParametersSchemaCompletions(parameters, strict, compat.RejectRootObjectUnion)
}
