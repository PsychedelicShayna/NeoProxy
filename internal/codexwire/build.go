package codexwire

import (
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf16"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/tidwall/gjson"
)

// FromResponses maps a downstream Responses body onto NeoPi's Context and
// stream options. Fields NeoPi never forwards (sampling, output caps, store,
// stream, previous_response_id) are dropped here, exactly as NeoPi drops them.
func FromResponses(body []byte, model *registry.CodexModel) (Context, Options) {
	root := gjson.ParseBytes(body)
	var ctx Context
	if instructions := root.Get("instructions"); instructions.Type == gjson.String {
		// normalizeSystemPrompts: toWellFormed, then drop blank prompts.
		if prompt := strings.ToValidUTF8(instructions.String(), "\uFFFD"); strings.TrimSpace(prompt) != "" {
			ctx.SystemPrompts = []string{prompt}
		}
	}
	switch input := root.Get("input"); {
	case input.IsArray():
		for _, item := range input.Array() {
			ctx.Input = append(ctx.Input, json.RawMessage(item.Raw))
		}
	case input.Type == gjson.String:
		ctx.Input = []json.RawMessage{jsjson.Object(
			jsjson.Field{Name: "role", Value: jsjson.Marshal("user")},
			jsjson.Field{Name: "content", Value: jsjson.Marshal([]json.RawMessage{jsjson.Object(
				jsjson.Field{Name: "type", Value: jsjson.Marshal("input_text")},
				jsjson.Field{Name: "text", Value: jsjson.Marshal(input.String())},
			)})},
		)}
	}
	for _, tool := range root.Get("tools").Array() {
		ctx.Tools = append(ctx.Tools, json.RawMessage(tool.Raw))
	}
	var opts Options
	opts.PromptCacheKey = root.Get("prompt_cache_key").String()
	reasoning := root.Get("reasoning")
	if effort := reasoning.Get("effort"); effort.Type == gjson.String {
		if effort.String() == "none" {
			opts.Effort = "none"
		} else {
			opts.Effort = clampEffort(model, effort.String())
		}
	}
	switch summary := reasoning.Get("summary"); {
	case summary.Type == gjson.Null && summary.Exists():
		opts.SummaryNull = true
	case summary.Type == gjson.String:
		opts.Summary = summary.String()
	}
	if context := reasoning.Get("context"); context.Type == gjson.String {
		opts.ReasoningContext = context.String()
	}
	if verbosity := root.Get("text.verbosity"); verbosity.Type == gjson.String {
		opts.TextVerbosity = verbosity.String()
	}
	for _, value := range root.Get("include").Array() {
		if value.Type == gjson.String {
			opts.Include = append(opts.Include, value.String())
		}
	}
	if tier := root.Get("service_tier"); tier.Type == gjson.String {
		opts.ServiceTier = tier.String()
	}
	if choice := root.Get("tool_choice"); choice.Exists() && choice.Type != gjson.Null {
		opts.ToolChoice = json.RawMessage(choice.Raw)
	}
	root.Get("client_metadata").ForEach(func(key, value gjson.Result) bool {
		if value.Type == gjson.String {
			opts.ClientMetadata = append(opts.ClientMetadata, jsjson.Field{Name: key.String(), Value: jsjson.Marshal(value.String())})
		}
		return true
	})
	return ctx, opts
}

// NormalizePromptCacheKey is normalizeOpenAIPromptCacheKey: well-formed text
// up to 64 UTF-16 units, otherwise a stable hashed key.
func NormalizePromptCacheKey(value string) string {
	if value == "" {
		return ""
	}
	wellFormed := strings.ToValidUTF8(value, "\uFFFD")
	if len(utf16.Encode([]rune(wellFormed))) <= 64 {
		return wellFormed
	}
	return "pc_" + bunHash36(wellFormed)
}

// NormalizeOpenRouterSessionID is normalizeOpenRouterResponsesSessionId:
// well-formed text up to 256 UTF-16 units, otherwise a stable hashed id.
func NormalizeOpenRouterSessionID(value string) string {
	if value == "" {
		return ""
	}
	wellFormed := strings.ToValidUTF8(value, "\uFFFD")
	if len(utf16.Encode([]rune(wellFormed))) <= 256 {
		return wellFormed
	}
	return "session_" + bunHash36(wellFormed)
}

// shouldSendServiceTier is NeoPi's gate for openai-codex-responses models.
func shouldSendServiceTier(tier string, model *registry.CodexModel) bool {
	if tier == "" || tier == "auto" {
		return false
	}
	if tier != "default" {
		var advertised []string
		listed := model.ServiceTiers != nil
		if listed {
			advertised = *model.ServiceTiers
		}
		contains := func(value string) bool {
			for _, candidate := range advertised {
				if candidate == value {
					return true
				}
			}
			return false
		}
		if tier == "ultrafast" || tier == "flex" {
			return contains(tier)
		}
		if listed && len(advertised) > 0 {
			return contains(tier)
		}
	}
	// openai-codex answers every remaining tier at the provider level.
	return true
}

func compactGrammarDefinition(syntax, definition string) string {
	if syntax != "lark" {
		return definition
	}
	isSpace := func(r rune) bool { return unicode.IsSpace(r) || r == '\uFEFF' }
	var lines []string
	for _, line := range strings.Split(definition, "\n") {
		line = strings.TrimSuffix(line, "\r")
		uncommented := strings.TrimRightFunc(stripLarkLineComment(line), isSpace)
		if strings.TrimFunc(uncommented, isSpace) != "" {
			lines = append(lines, uncommented)
		}
	}
	return strings.Join(lines, "\n")
}

func stripLarkLineComment(line string) string {
	var inString byte
	inRegex, escaped := false, false
	for i := 0; i < len(line); i++ {
		char := line[i]
		if escaped {
			escaped = false
			continue
		}
		if char == '\\' {
			escaped = true
			continue
		}
		if inString != 0 {
			if char == inString {
				inString = 0
			}
			continue
		}
		if inRegex {
			if char == '/' {
				inRegex = false
			}
			continue
		}
		if char == '/' && i+1 < len(line) && line[i+1] == '/' {
			return line[:i]
		}
		if char == '"' || char == '\'' {
			inString = char
			continue
		}
		if char == '/' {
			inRegex = true
		}
	}
	return line
}

// convertTools is convertOpenAICodexResponsesTools over downstream Responses
// tool declarations. NeoPi only emits function and freeform custom tools; any
// other declared tool is forwarded unchanged.
func convertTools(tools []json.RawMessage) []json.RawMessage {
	out := make([]json.RawMessage, 0, len(tools))
	for _, raw := range tools {
		tool := gjson.ParseBytes(raw)
		switch tool.Get("type").String() {
		case "function":
			parameters, strict := ToolParametersSchema(tool.Get("parameters"), tool.Get("strict").Type == gjson.True, false)
			fields := []jsjson.Field{
				{Name: "type", Value: jsjson.Marshal("function")},
				{Name: "name", Value: jsjson.Marshal(tool.Get("name").String())},
				{Name: "description", Value: jsjson.Marshal(tool.Get("description").String())},
				{Name: "parameters", Value: parameters},
			}
			if strict {
				fields = append(fields, jsjson.Field{Name: "strict", Value: jsjson.Marshal(true)})
			} else if tool.Get("strict").Type == gjson.False {
				fields = append(fields, jsjson.Field{Name: "strict", Value: jsjson.Marshal(false)})
			}
			out = append(out, jsjson.Object(fields...))
		case "custom":
			format := tool.Get("format")
			if format.Get("type").String() != "grammar" {
				out = append(out, jsjson.Normalize(raw))
				continue
			}
			syntax := format.Get("syntax").String()
			out = append(out, jsjson.Object(
				jsjson.Field{Name: "type", Value: jsjson.Marshal("custom")},
				jsjson.Field{Name: "name", Value: jsjson.Marshal(tool.Get("name").String())},
				jsjson.Field{Name: "description", Value: jsjson.Marshal(tool.Get("description").String())},
				jsjson.Field{Name: "format", Value: jsjson.Object(
					jsjson.Field{Name: "type", Value: jsjson.Marshal("grammar")},
					jsjson.Field{Name: "syntax", Value: jsjson.Marshal(syntax)},
					jsjson.Field{Name: "definition", Value: jsjson.Marshal(compactGrammarDefinition(syntax, format.Get("definition").String()))},
				)},
			))
		default:
			out = append(out, jsjson.Normalize(raw))
		}
	}
	return out
}

// normalizeToolChoice is normalizeCodexToolChoice over the declared tools.
func normalizeToolChoice(choice json.RawMessage, tools []json.RawMessage) json.RawMessage {
	parsed := gjson.ParseBytes(choice)
	if parsed.Type == gjson.String {
		return jsjson.Marshal(parsed.String())
	}
	if !parsed.IsObject() {
		return nil
	}
	mapName := func(name string) json.RawMessage {
		for _, raw := range tools {
			tool := gjson.ParseBytes(raw)
			if tool.Get("name").String() != name {
				continue
			}
			kind := "function"
			if tool.Get("type").String() == "custom" {
				kind = "custom"
			}
			return jsjson.Object(jsjson.Field{Name: "type", Value: jsjson.Marshal(kind)}, jsjson.Field{Name: "name", Value: jsjson.Marshal(name)})
		}
		return nil
	}
	switch parsed.Get("type").String() {
	case "function":
		if name := parsed.Get("function.name").String(); name != "" {
			return mapName(name)
		}
		if name := parsed.Get("name").String(); name != "" {
			return mapName(name)
		}
	case "tool":
		if name := parsed.Get("name").String(); name != "" {
			return mapName(name)
		}
	}
	return nil
}

// buildBody is buildTransformedCodexRequestBody followed by the provider's
// access-program request. Effort stabilization is applied by the session.
func buildBody(model *registry.CodexModel, ctx Context, opts Options, accountID string) object {
	items := make([]json.RawMessage, len(ctx.Input))
	for i, item := range ctx.Input {
		items[i] = jsjson.Normalize(item)
	}
	body := object{
		{Name: "model", Value: jsjson.Marshal(model.ID)},
		{Name: "input", Value: jsjson.Marshal(items)},
		{Name: "stream", Value: jsjson.Marshal(true)},
	}
	key := opts.PromptCacheKey
	if key == "" {
		key = opts.SessionID
	}
	if key = NormalizePromptCacheKey(key); key != "" {
		body.set("prompt_cache_key", jsjson.Marshal(key))
	}
	if shouldSendServiceTier(opts.ServiceTier, model) {
		body.set("service_tier", jsjson.Marshal(opts.ServiceTier))
	}
	if len(ctx.Tools) > 0 {
		tools := convertTools(ctx.Tools)
		body.set("tools", jsjson.Marshal(tools))
		if len(opts.ToolChoice) > 0 {
			if choice := normalizeToolChoice(opts.ToolChoice, tools); choice != nil {
				body.set("tool_choice", choice)
			}
		}
	}
	if len(ctx.SystemPrompts) > 0 {
		body.set("instructions", jsjson.Marshal(ctx.SystemPrompts[0]))
	}
	var developerTexts []string
	if len(ctx.SystemPrompts) > 1 {
		developerTexts = ctx.SystemPrompts[1:]
	}
	if len(opts.ClientMetadata) > 0 {
		body.set("client_metadata", jsjson.Object(opts.ClientMetadata...))
	}
	transformRequestBody(&body, model, opts, developerTexts)
	if _, dropped := droppedAccessPrograms.Load(accountID + "\x00" + model.ID); accountID != "" && !dropped {
		if access, ok := model.AccountAccess[accountID]; ok && access.CyberPrograms != nil {
			for _, program := range *access.CyberPrograms {
				if program == "daybreak_blue" {
					body.set("access_programs", jsjson.Object(jsjson.Field{Name: "cyber", Value: jsjson.Marshal("daybreak_blue")}))
					break
				}
			}
		}
	}
	return body
}
