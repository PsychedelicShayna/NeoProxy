package executor

import (
	"encoding/json"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/tidwall/gjson"
)

// neoPiCompletionsRecognition lists the properties NeoPi reads a replayed
// assistant turn's reasoning out of, most specific first.
var neoPiCompletionsReasoningFields = []string{"reasoning_content", "reasoning", "reasoning_text"}

// neoPiCompletionsMessages is NeoPi's convertMessages for a Chat Completions
// downstream body: transformMessages' replay repairs (malformed calls, closed
// tool call/result windows) followed by the host's own message shape.
func neoPiCompletionsMessages(root gjson.Result, row *registry.NeoPiModel, compat *registry.NeoPiCompletionsCompat) (json.RawMessage, error) {
	input := root.Get("messages")
	if !input.IsArray() {
		return json.RawMessage(`[]`), nil
	}
	messages := neoPiParseCompletionMessages(input)
	messages = neoPiSanitizeMalformedToolCalls(messages)
	messages = neoPiRepairToolCallWindows(messages)
	supportsImages := neoPiCompletionsVisionSupported(row, compat)
	return jsjson.Marshal(neoPiConvertCompletionsMessages(messages, row, compat, supportsImages)), nil
}

// neoPiCompletionsVisionSupported is isOpenAICompletionsVisionSupported: the
// row declares image input and the host does not strip it.
func neoPiCompletionsVisionSupported(row *registry.NeoPiModel, compat *registry.NeoPiCompletionsCompat) bool {
	if compat.StripImageInput {
		return false
	}
	for _, input := range row.Input {
		if input == "image" {
			return true
		}
	}
	return false
}

// neoPiCompletionMessage is one downstream Chat Completions message.
type neoPiCompletionMessage struct {
	role string
	// text is the string content (or the joined text of an array content);
	// parts holds an array content's parts.
	text    string
	parts   []gjson.Result
	hasText bool
	// reasoningField and reasoning carry an assistant turn's replayed reasoning.
	reasoningField string
	reasoning      string
	toolCalls      []neoPiCompletionCall
	// toolCallID is a tool message's target call; toolName its declared name.
	toolCallID string
	toolName   string
	isError    bool
}

// neoPiParseCompletionMessages reads the downstream transcript, normalizing
// each message the way the OpenAI client's TypeScript types describe it.
func neoPiParseCompletionMessages(input gjson.Result) []neoPiCompletionMessage {
	list := input.Array()
	out := make([]neoPiCompletionMessage, 0, len(list))
	for _, raw := range list {
		message := neoPiCompletionMessage{role: raw.Get("role").String()}
		content := raw.Get("content")
		switch {
		case content.Type == gjson.String:
			message.text, message.hasText = content.String(), true
		case content.IsArray():
			message.parts = content.Array()
			texts := make([]string, 0, len(message.parts))
			for _, part := range message.parts {
				if part.Get("type").String() == "text" {
					texts = append(texts, part.Get("text").String())
				}
			}
			message.text, message.hasText = strings.Join(texts, ""), true
		}
		switch message.role {
		case "assistant":
			for _, field := range neoPiCompletionsReasoningFields {
				if value := raw.Get(field); value.Type == gjson.String && value.String() != "" {
					message.reasoningField, message.reasoning = field, value.String()
					break
				}
			}
			for _, call := range raw.Get("tool_calls").Array() {
				message.toolCalls = append(message.toolCalls, neoPiCompletionCall{
					id:        call.Get("id").String(),
					name:      call.Get("function.name").String(),
					arguments: call.Get("function.arguments").String(),
				})
			}
		case "tool", "function":
			message.toolCallID = raw.Get("tool_call_id").String()
			if message.toolCallID == "" {
				message.toolCallID = raw.Get("tool_call_id").String()
			}
			message.toolName = raw.Get("name").String()
		}
		out = append(out, message)
	}
	out = neoPiAttachToolNames(out)
	return out
}

// neoPiAttachToolNames fills each tool message's tool name from the assistant
// turn that issued the call, which is what NeoPi's ToolResultMessage carries.
func neoPiAttachToolNames(messages []neoPiCompletionMessage) []neoPiCompletionMessage {
	names := map[string]string{}
	for i := range messages {
		if messages[i].role != "assistant" {
			continue
		}
		for _, call := range messages[i].toolCalls {
			if call.id != "" && call.name != "" {
				names[call.id] = call.name
			}
		}
	}
	for i := range messages {
		if messages[i].role != "tool" || messages[i].toolName != "" {
			continue
		}
		messages[i].toolName = names[messages[i].toolCallID]
	}
	return messages
}

// neoPiSanitizeMalformedToolCalls drops assistant calls whose id or name the
// target cannot replay, with the results that answer them, and any assistant
// turn left with nothing to say (transform-messages sanitizeMalformedToolCalls).
func neoPiSanitizeMalformedToolCalls(messages []neoPiCompletionMessage) []neoPiCompletionMessage {
	malformed := false
	for _, message := range messages {
		if message.role != "assistant" {
			continue
		}
		for _, call := range message.toolCalls {
			if neoPiMalformedToolCall(call) {
				malformed = true
				break
			}
		}
	}
	if !malformed {
		return messages
	}
	// One queue of verdicts per id, cleared at every turn boundary, so a
	// malformed call's result never consumes a later valid call's result.
	queues := map[string][]bool{}
	out := make([]neoPiCompletionMessage, 0, len(messages))
	for _, message := range messages {
		switch message.role {
		case "assistant":
			queues = map[string][]bool{}
			kept := make([]neoPiCompletionCall, 0, len(message.toolCalls))
			for _, call := range message.toolCalls {
				drop := neoPiMalformedToolCall(call)
				queues[call.id] = append(queues[call.id], drop)
				if !drop {
					kept = append(kept, call)
				}
			}
			message.toolCalls = kept
			if len(message.toolCalls) == 0 && !message.hasText {
				continue
			}
			out = append(out, message)
		case "tool":
			if queue := queues[message.toolCallID]; len(queue) > 0 {
				drop := queue[0]
				queues[message.toolCallID] = queue[1:]
				if len(queues[message.toolCallID]) == 0 {
					delete(queues, message.toolCallID)
				}
				if drop {
					continue
				}
			}
			out = append(out, message)
		default:
			queues = map[string][]bool{}
			out = append(out, message)
		}
	}
	return out
}

// neoPiMalformedToolCall is isMalformedToolCall with NeoPi's default tool-name
// limits (1..128 characters of [A-Za-z0-9_-]).
func neoPiMalformedToolCall(call neoPiCompletionCall) bool {
	if strings.TrimSpace(call.id) == "" {
		return true
	}
	if call.name == "" || len(call.name) > 128 {
		return true
	}
	for i := 0; i < len(call.name); i++ {
		value := call.name[i]
		if value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9' || value == '_' || value == '-' {
			continue
		}
		return true
	}
	return false
}

// neoPiRepairToolCallWindows is transformMessages' second pass: every
// surviving call is answered by exactly one result in its own window, a real
// one when one is still in the transcript and a synthetic marker otherwise.
func neoPiRepairToolCallWindows(messages []neoPiCompletionMessage) []neoPiCompletionMessage {
	// Results still available for a later call, by call id.
	available := map[string][]int{}
	consumed := make([]bool, len(messages))
	for i, message := range messages {
		if message.role == "tool" {
			available[message.toolCallID] = append(available[message.toolCallID], i)
		}
	}
	take := func(id string, after int) (neoPiCompletionMessage, bool) {
		for _, index := range available[id] {
			if consumed[index] || index <= after {
				continue
			}
			consumed[index] = true
			return messages[index], true
		}
		return neoPiCompletionMessage{}, false
	}
	declared := map[string]bool{}
	out := make([]neoPiCompletionMessage, 0, len(messages))
	var pending []neoPiCompletionCall
	pendingStart := -1
	flush := func() {
		for _, call := range pending {
			if declared[call.id] {
				continue
			}
			if real, ok := take(call.id, pendingStart); ok {
				out = append(out, real)
				declared[call.id] = true
				continue
			}
			out = append(out, neoPiCompletionMessage{
				role:       "tool",
				toolCallID: call.id,
				toolName:   call.name,
				text:       neoPiMissingToolResult,
				hasText:    true,
				isError:    true,
			})
			declared[call.id] = true
		}
		pending = nil
	}
	for i, message := range messages {
		switch message.role {
		case "assistant":
			flush()
			if len(message.toolCalls) > 0 {
				pending = message.toolCalls
				pendingStart = i
			}
			out = append(out, message)
		case "tool":
			if declared[message.toolCallID] || consumed[i] {
				continue
			}
			consumed[i] = true
			out = append(out, message)
			declared[message.toolCallID] = true
		default:
			flush()
			out = append(out, message)
		}
	}
	flush()
	return out
}

// neoPiConvertCompletionsMessages encodes the repaired transcript in the
// host's shape: system prompts, developer role support, reasoning replay field
// and the tool-call/result pairing rules each compat declares.
func neoPiConvertCompletionsMessages(messages []neoPiCompletionMessage, row *registry.NeoPiModel, compat *registry.NeoPiCompletionsCompat, supportsImages bool) []json.RawMessage {
	params := make([]json.RawMessage, 0, len(messages)+4)
	systemRole := "system"
	if row.Reasoning && compat.SupportsDeveloperRole {
		systemRole = "developer"
	}
	systems := make([]string, 0, 2)
	start := 0
	for start < len(messages) && (messages[start].role == "system" || messages[start].role == "developer") {
		if strings.TrimSpace(messages[start].text) != "" {
			systems = append(systems, messages[start].text)
		}
		start++
	}
	if len(systems) > 0 {
		if compat.SupportsMultipleSystemMessages {
			for _, prompt := range systems {
				params = append(params, jsjson.Object(jsjson.Field{Name: "role", Value: jsjson.Marshal(systemRole)}, jsjson.Field{Name: "content", Value: jsjson.Marshal(prompt)}))
			}
		} else {
			params = append(params, jsjson.Object(jsjson.Field{Name: "role", Value: jsjson.Marshal(systemRole)}, jsjson.Field{Name: "content", Value: jsjson.Marshal(strings.Join(systems, "\n\n"))}))
		}
	}

	lastRole := ""
	for i := start; i < len(messages); i++ {
		message := messages[i]
		if compat.RequiresAssistantAfterToolResult && lastRole == "tool" && (message.role == "user" || message.role == "developer") {
			params = append(params, jsjson.Object(jsjson.Field{Name: "role", Value: jsjson.Marshal("assistant")}, jsjson.Field{Name: "content", Value: jsjson.Marshal(neoPiAssistantAfterToolResult)}))
			lastRole = "assistant"
		}
		switch message.role {
		case "user", "developer":
			role := "user"
			if message.role == "developer" && compat.SupportsDeveloperRole {
				role = "developer"
			}
			if !message.hasText {
				continue
			}
			if len(message.parts) == 0 {
				if strings.TrimSpace(message.text) == "" {
					continue
				}
				params = append(params, jsjson.Object(jsjson.Field{Name: "role", Value: jsjson.Marshal(role)}, jsjson.Field{Name: "content", Value: jsjson.Marshal(message.text)}))
				lastRole = role
				continue
			}
			parts := make([]json.RawMessage, 0, len(message.parts))
			omitted := false
			for _, part := range message.parts {
				switch part.Get("type").String() {
				case "text":
					text := part.Get("text").String()
					if strings.TrimSpace(text) == "" {
						continue
					}
					parts = append(parts, jsjson.Object(jsjson.Field{Name: "type", Value: jsjson.Marshal("text")}, jsjson.Field{Name: "text", Value: jsjson.Marshal(text)}))
				case "image_url":
					if !supportsImages {
						omitted = true
						continue
					}
					parts = append(parts, neoPiImageURLPart(part))
				}
			}
			if omitted {
				parts = append(parts, neoPiTextPart(neoPiNonVisionImagePlaceholder))
			}
			if len(parts) == 0 {
				continue
			}
			contentRole := role
			if role == "developer" && !neoPiAllTextParts(parts) {
				contentRole = "user"
			}
			params = append(params, jsjson.Object(jsjson.Field{Name: "role", Value: jsjson.Marshal(contentRole)}, jsjson.Field{Name: "content", Value: jsjson.Marshal(parts)}))
			lastRole = contentRole
		case "assistant":
			encoded := neoPiAssistantMessage(message, row, compat)
			if encoded == nil {
				continue
			}
			params = append(params, encoded)
			lastRole = "assistant"
		case "tool":
			images := make([]json.RawMessage, 0, 2)
			last := i
			for ; last < len(messages) && messages[last].role == "tool"; last++ {
				tool := messages[last]
				texts := make([]string, 0, 2)
				hasImages := false
				for _, part := range tool.parts {
					switch part.Get("type").String() {
					case "text":
						texts = append(texts, part.Get("text").String())
					case "image_url":
						hasImages = true
						if supportsImages {
							images = append(images, neoPiImageURLPart(part))
						}
					}
				}
				if len(tool.parts) == 0 {
					texts = append(texts, tool.text)
				}
				text := strings.Join(texts, "\n")
				switch {
				case hasImages && !supportsImages:
					if text == "" {
						text = neoPiNonVisionImagePlaceholder
					} else {
						text += "\n" + neoPiNonVisionImagePlaceholder
					}
				case text == "" && hasImages:
					text = neoPiImageOnlyPrefix
				}
				fields := []jsjson.Field{
					{Name: "role", Value: jsjson.Marshal("tool")},
					{Name: "content", Value: jsjson.Marshal(text)},
					{Name: "tool_call_id", Value: jsjson.Marshal(neoPiNormalizeCompletionsToolCallID(tool.toolCallID, compat))},
				}
				if compat.RequiresToolResultName && tool.toolName != "" {
					fields = append(fields, jsjson.Field{Name: "name", Value: jsjson.Marshal(tool.toolName)})
				}
				params = append(params, jsjson.Object(fields...))
			}
			i = last - 1
			if len(images) > 0 {
				if compat.RequiresAssistantAfterToolResult {
					params = append(params, jsjson.Object(jsjson.Field{Name: "role", Value: jsjson.Marshal("assistant")}, jsjson.Field{Name: "content", Value: jsjson.Marshal(neoPiAssistantAfterToolResult)}))
				}
				parts := append([]json.RawMessage{neoPiTextPart(neoPiToolImagesNote)}, images...)
				params = append(params, jsjson.Object(jsjson.Field{Name: "role", Value: jsjson.Marshal("user")}, jsjson.Field{Name: "content", Value: jsjson.Marshal(parts)}))
				lastRole = "user"
			} else {
				lastRole = "tool"
			}
		case "system":
			// A system prompt after the conversation began keeps its role.
			if strings.TrimSpace(message.text) == "" {
				continue
			}
			params = append(params, jsjson.Object(jsjson.Field{Name: "role", Value: jsjson.Marshal("system")}, jsjson.Field{Name: "content", Value: jsjson.Marshal(message.text)}))
			lastRole = "system"
		}
	}
	return params
}

// neoPiAssistantMessage encodes one assistant turn: its visible text, the
// reasoning field the host replays (or the empty fallback it requires), and its
// tool calls in NeoPi's field order.
func neoPiAssistantMessage(message neoPiCompletionMessage, row *registry.NeoPiModel, compat *registry.NeoPiCompletionsCompat) json.RawMessage {
	var content json.RawMessage = []byte("null")
	if message.hasText && message.text != "" {
		content = jsjson.Marshal(message.text)
	}
	fields := []jsjson.Field{
		{Name: "role", Value: jsjson.Marshal("assistant")},
		{Name: "content", Value: content},
	}
	reasoningField := compat.ReasoningContentField
	if reasoningField == "" {
		reasoningField = "reasoning_content"
	}
	hasReasoning := false
	if message.reasoning != "" {
		if wireField := neoPiCompletionsReasoningField(compat, message.reasoningField); wireField != "" {
			fields = append(fields, jsjson.Field{Name: wireField, Value: jsjson.Marshal(message.reasoning)})
			hasReasoning = true
		}
	}
	needsReasoning := compat.RequiresReasoningContentForToolCalls || compat.RequiresReasoningContentForAllAssistantTurns
	if needsReasoning && !hasReasoning && compat.RequiresReasoningContentForToolCalls && !compat.AllowsSyntheticReasoningContentForToolCalls && (len(message.toolCalls) > 0 || compat.RequiresReasoningContentForAllAssistantTurns) {
		// NeoPi sends the configured honest fallback (empty by default) rather
		// than inventing reasoning the host would validate.
		fields = append(fields, jsjson.Field{Name: reasoningField, Value: jsjson.Marshal(compat.SyntheticReasoningContentFallback)})
		hasReasoning = true
	}
	if len(message.toolCalls) > 0 && !hasReasoning && compat.RequiresReasoningContentForToolCalls && compat.AllowsSyntheticReasoningContentForToolCalls && neoPiAllowsSyntheticPlaceholder(compat) {
		// Hosts that accept a placeholder get "." before the calls, exactly
		// where NeoPi appends its synthetic tier.
		fields = append(fields, jsjson.Field{Name: reasoningField, Value: jsjson.Marshal(neoPiSyntheticReasoningPlaceholder)})
		hasReasoning = true
	}
	if len(message.toolCalls) > 0 {
		calls := make([]json.RawMessage, 0, len(message.toolCalls))
		for _, call := range message.toolCalls {
			calls = append(calls, jsjson.Object(
				jsjson.Field{Name: "id", Value: jsjson.Marshal(neoPiNormalizeCompletionsToolCallID(call.id, compat))},
				jsjson.Field{Name: "type", Value: jsjson.Marshal("function")},
				jsjson.Field{Name: "function", Value: jsjson.Object(
					jsjson.Field{Name: "name", Value: jsjson.Marshal(call.name)},
					jsjson.Field{Name: "arguments", Value: jsjson.Marshal(neoPiSerializeToolArguments(call.arguments))},
				)},
			))
		}
		fields = append(fields, jsjson.Field{Name: "tool_calls", Value: jsjson.Marshal(calls)})
	}
	if compat.MistralReasoningContentParts && message.reasoning != "" {
		// Mistral Medium reads thinking from ordered content parts, not a
		// top-level reasoning field.
		parts := []json.RawMessage{jsjson.Object(
			jsjson.Field{Name: "type", Value: jsjson.Marshal("thinking")},
			jsjson.Field{Name: "thinking", Value: jsjson.Marshal([]json.RawMessage{neoPiTextPart(message.reasoning)})},
		)}
		if message.hasText && strings.TrimSpace(message.text) != "" {
			parts = append(parts, neoPiTextPart(message.text))
		}
		fields = fields[:2]
		fields[1].Value = jsjson.Marshal(parts)
		hasReasoning = false
		hasContent := true
		if len(message.toolCalls) == 0 && !hasContent {
			return nil
		}
		return neoPiAssistantObject(fields, message, compat)
	}
	if string(fields[1].Value) == "null" && (hasReasoning || len(message.toolCalls) > 0) {
		fields[1].Value = jsjson.Marshal("")
	}
	hasContent := string(fields[1].Value) != "null" && string(fields[1].Value) != `""`
	if !hasContent && len(message.toolCalls) > 0 && compat.RequiresAssistantContentForToolCalls {
		fields[1].Value = jsjson.Marshal(neoPiAssistantContentPlaceholder)
		hasContent = true
	}
	if !hasContent && len(message.toolCalls) == 0 && !hasReasoning {
		return nil
	}
	return jsjson.Object(fields...)
}

// neoPiAssistantObject finishes an assistant message whose content is fixed
// (the Mistral content-parts shape) by appending its tool calls.
func neoPiAssistantObject(fields []jsjson.Field, message neoPiCompletionMessage, compat *registry.NeoPiCompletionsCompat) json.RawMessage {
	if len(message.toolCalls) > 0 {
		calls := make([]json.RawMessage, 0, len(message.toolCalls))
		for _, call := range message.toolCalls {
			calls = append(calls, jsjson.Object(
				jsjson.Field{Name: "id", Value: jsjson.Marshal(neoPiNormalizeCompletionsToolCallID(call.id, compat))},
				jsjson.Field{Name: "type", Value: jsjson.Marshal("function")},
				jsjson.Field{Name: "function", Value: jsjson.Object(
					jsjson.Field{Name: "name", Value: jsjson.Marshal(call.name)},
					jsjson.Field{Name: "arguments", Value: jsjson.Marshal(neoPiSerializeToolArguments(call.arguments))},
				)},
			))
		}
		fields = append(fields, jsjson.Field{Name: "tool_calls", Value: jsjson.Marshal(calls)})
	}
	return jsjson.Object(fields...)
}

// neoPiAllowsSyntheticPlaceholder reports whether the host takes NeoPi's "."
// reasoning placeholder: the dialects whose parsers ignore its value.
func neoPiAllowsSyntheticPlaceholder(compat *registry.NeoPiCompletionsCompat) bool {
	switch compat.ThinkingFormat {
	case "openai", "openrouter", "zai":
		return true
	}
	return false
}

// neoPiCompletionsReasoningField is convertMessages' thinking replay decision:
// which wire property carries the turn's reasoning, or "" when the host does
// not replay it.
func neoPiCompletionsReasoningField(compat *registry.NeoPiCompletionsCompat, source string) string {
	configured := compat.ReasoningContentField
	if configured == "" {
		configured = "reasoning_content"
	}
	recognized := source == "reasoning_content" || source == "reasoning" || source == "reasoning_text"
	if compat.RequiresReasoningContentForToolCalls {
		if compat.AllowsSyntheticReasoningContentForToolCalls && recognized {
			return source
		}
		if recognized {
			return configured
		}
		return ""
	}
	if compat.ThinkingFormat == "zai" {
		return configured
	}
	if compat.ReplayReasoningContent {
		if recognized {
			return source
		}
		return configured
	}
	return ""
}

// neoPiSerializeToolArguments is serializeToolArguments: the arguments object
// re-serialized the way JSON.stringify writes it, "{}" when it is not one.
func neoPiSerializeToolArguments(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "{}"
	}
	parsed := gjson.Parse(trimmed)
	if !parsed.Exists() || !parsed.IsObject() {
		return "{}"
	}
	return string(jsjson.Normalize([]byte(parsed.Raw)))
}

// neoPiNormalizeCompletionsToolCallID is convertMessages'
// normalizeCompletionsToolCallId: Mistral's fixed 9-character alphabet, the
// OpenAI Responses composite split, and OpenAI's 40-character limit.
func neoPiNormalizeCompletionsToolCallID(id string, compat *registry.NeoPiCompletionsCompat) string {
	if compat.RequiresMistralToolIDs {
		return neoPiNormalizeMistralToolID(id)
	}
	if pipe := strings.Index(id, "|"); pipe > 0 {
		// A cross-model replay converts a Responses composite id to its call half.
		return neoPiNormalizeStrictToolCallID(id[:pipe])
	}
	if compat.UsesOpenAIToolCallIDLimit && len(id) > neoPiOpenAIToolCallIDMaxLength {
		return id[:neoPiOpenAIToolCallIDMaxLength]
	}
	return id
}

// neoPiNormalizeStrictToolCallID is normalizeToolCallId: [A-Za-z0-9_-], capped.
func neoPiNormalizeStrictToolCallID(id string) string {
	var out strings.Builder
	for i := 0; i < len(id); i++ {
		value := id[i]
		switch {
		case value >= 'a' && value <= 'z', value >= 'A' && value <= 'Z', value >= '0' && value <= '9', value == '_', value == '-':
			out.WriteByte(value)
		default:
			out.WriteByte('_')
		}
	}
	normalized := out.String()
	if len(normalized) > 64 {
		normalized = normalized[:64]
	}
	return normalized
}

// neoPiNormalizeMistralToolID is normalizeMistralToolId: nine characters of
// [A-Za-z0-9], padded deterministically.
func neoPiNormalizeMistralToolID(id string) string {
	var out strings.Builder
	for i := 0; i < len(id); i++ {
		value := id[i]
		if value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9' {
			out.WriteByte(value)
		}
	}
	normalized := out.String()
	const padding = "ABCDEFGHI"
	if len(normalized) < 9 {
		normalized += padding[:9-len(normalized)]
	} else if len(normalized) > 9 {
		normalized = normalized[:9]
	}
	return normalized
}

// neoPiImageURLPart encodes an image part for the wire, dropping the
// Responses-only "original" detail Chat Completions cannot express.
func neoPiImageURLPart(part gjson.Result) json.RawMessage {
	image := neoPiOrderedObject{}
	if url := part.Get("image_url.url").String(); url != "" {
		image.set("url", jsjson.Marshal(url))
	}
	if detail := part.Get("image_url.detail").String(); detail != "" && detail != "original" {
		image.set("detail", jsjson.Marshal(detail))
	}
	return jsjson.Object(
		jsjson.Field{Name: "type", Value: jsjson.Marshal("image_url")},
		jsjson.Field{Name: "image_url", Value: image.bytes()},
	)
}

func neoPiTextPart(text string) json.RawMessage {
	return jsjson.Object(jsjson.Field{Name: "type", Value: jsjson.Marshal("text")}, jsjson.Field{Name: "text", Value: jsjson.Marshal(text)})
}

// neoPiAllTextParts reports whether every part is text, which is what a
// developer role accepts.
func neoPiAllTextParts(parts []json.RawMessage) bool {
	for _, part := range parts {
		if gjson.GetBytes(part, "type").String() != "text" {
			return false
		}
	}
	return true
}
