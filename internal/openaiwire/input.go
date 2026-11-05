package openaiwire

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf16"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/codexwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/tidwall/gjson"
)

const (
	orphanOutputLimit         = 16000
	orphanToolCallPlaceholder = "[No tool output recorded: the tool call was interrupted before it produced a result.]"
)

// buildInput is buildResponsesInput for a downstream transcript. The proxy
// holds no NeoPi message objects, so every downstream item is native history
// for this provider: NeoPi replays such items through
// sanitizeOpenAIResponsesHistoryItemsForReplay and
// adaptResponsesReplayItemsForModel, then repairs orphan outputs and calls and
// hoists messages wedged inside tool batches.
func buildInput(raw []json.RawMessage, compat *registry.NeoPiCompat, replay reasoningReplay) []json.RawMessage {
	if compat.FilterReasoningHistory {
		filtered := raw[:0:0]
		for _, item := range raw {
			if gjson.GetBytes(item, "type").String() != "reasoning" {
				filtered = append(filtered, item)
			}
		}
		raw = filtered
	}
	items := codexwire.SanitizeHistoryReplay(raw, compat.SupportsImageDetailOriginal)
	items = adaptReplayItems(items)
	items = ensureRequiredReasoningReplay(items, replay)
	items = repairOrphanToolOutputs(items)
	items = repairOrphanToolCalls(items)
	return hoistInterleavedToolBatchMessages(items)
}

func toolCallKind(itemType string) string {
	switch itemType {
	case "function_call":
		return "function"
	case "custom_tool_call":
		return "custom"
	case "computer_call":
		return "computer"
	}
	return ""
}

func toolOutputKind(itemType string) string {
	switch itemType {
	case "function_call_output":
		return "function"
	case "custom_tool_call_output":
		return "custom"
	case "computer_call_output":
		return "computer"
	}
	return ""
}

func callID(item json.RawMessage) string {
	if id := gjson.GetBytes(item, "call_id"); id.Type == gjson.String {
		return id.String()
	}
	return ""
}

func assistantNote(text string) json.RawMessage {
	return jsjson.Object(
		jsjson.Field{Name: "type", Value: jsjson.Marshal("message")},
		jsjson.Field{Name: "role", Value: jsjson.Marshal("assistant")},
		jsjson.Field{Name: "content", Value: jsjson.Marshal(text)},
	)
}

// adaptReplayItems is adaptResponsesReplayItemsForModel for a model without
// freeform custom tools or native computer use. A downstream custom tool keeps
// its declared name: the client's name is both NeoPi's internal and wire name.
func adaptReplayItems(items []json.RawMessage) []json.RawMessage {
	out := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		parsed := gjson.ParseBytes(item)
		switch kind := parsed.Get("type").String(); kind {
		case "custom_tool_call":
			fields := []jsjson.Field{{Name: "type", Value: jsjson.Marshal("function_call")}}
			if id := parsed.Get("id"); id.Type == gjson.String && id.String() != "" {
				fields = append(fields, jsjson.Field{Name: "id", Value: jsjson.Marshal(id.String())})
			}
			// JSON.stringify({ input }) drops an undefined input.
			arguments := jsjson.Object()
			if value := parsed.Get("input"); value.Exists() {
				arguments = jsjson.Object(jsjson.Field{Name: "input", Value: jsjson.Normalize([]byte(value.Raw))})
			}
			fields = append(fields,
				jsjson.Field{Name: "call_id", Value: json.RawMessage(rawOrUndefined(parsed.Get("call_id")))},
				jsjson.Field{Name: "name", Value: json.RawMessage(rawOrUndefined(parsed.Get("name")))},
				jsjson.Field{Name: "arguments", Value: jsjson.Marshal(string(arguments))},
			)
			if namespace := parsed.Get("namespace"); namespace.Exists() && namespace.String() != "" {
				fields = append(fields, jsjson.Field{Name: "namespace", Value: json.RawMessage(namespace.Raw)})
			}
			out = append(out, jsjson.Object(fields...))
		case "custom_tool_call_output":
			out = append(out, jsjson.Object(
				jsjson.Field{Name: "type", Value: jsjson.Marshal("function_call_output")},
				jsjson.Field{Name: "call_id", Value: json.RawMessage(rawOrUndefined(parsed.Get("call_id")))},
				jsjson.Field{Name: "output", Value: json.RawMessage(rawOrUndefined(parsed.Get("output")))},
			))
		case "computer_call", "computer_call_output":
			id := callID(item)
			if id == "" {
				id = "unknown"
			}
			label := "result"
			if kind == "computer_call" {
				label = "call"
			}
			out = append(out, assistantNote(fmt.Sprintf("[Previous computer %s; call_id=%s]: %s", label, id, item)))
		default:
			out = append(out, item)
		}
	}
	return out
}

// rawOrUndefined keeps a present value; an absent one serializes as null,
// which JSON.stringify would drop. Downstream custom items always carry both.
func rawOrUndefined(value gjson.Result) string {
	if value.Exists() {
		return value.Raw
	}
	return "null"
}

// repairOrphanToolOutputs is repairOrphanResponsesToolOutputs: an output with
// no preceding call becomes an assistant note.
func repairOrphanToolOutputs(input []json.RawMessage) []json.RawMessage {
	preceding := map[string]bool{}
	out := make([]json.RawMessage, 0, len(input))
	for _, item := range input {
		kind := gjson.GetBytes(item, "type").String()
		id := callID(item)
		if callKind := toolCallKind(kind); callKind != "" && id != "" {
			preceding[callKind+"\x00"+id] = true
		}
		outputKind := toolOutputKind(kind)
		if outputKind == "" || id == "" || preceding[outputKind+"\x00"+id] {
			out = append(out, item)
			continue
		}
		toolName := "tool"
		if outputKind == "computer" {
			toolName = "computer"
		}
		text := ""
		switch output := gjson.GetBytes(item, "output"); {
		case output.Type == gjson.String:
			text = output.String()
		case !output.Exists() || output.Type == gjson.Null:
		default:
			text = string(jsjson.Normalize([]byte(output.Raw)))
		}
		if units := utf16.Encode([]rune(text)); len(units) > orphanOutputLimit {
			text = string(utf16.Decode(units[:orphanOutputLimit])) + "\n...[truncated]"
		}
		out = append(out, assistantNote(fmt.Sprintf("[Orphan %s result; call_id=%s]: %s", toolName, id, text)))
	}
	return out
}

// repairOrphanToolCalls is repairOrphanResponsesToolCalls: a call with no
// later output gains a placeholder output (computer calls become a note).
func repairOrphanToolCalls(input []json.RawMessage) []json.RawMessage {
	later := map[string]bool{}
	orphan := map[int]bool{}
	for index := len(input) - 1; index >= 0; index-- {
		kind := gjson.GetBytes(input[index], "type").String()
		id := callID(input[index])
		if outputKind := toolOutputKind(kind); outputKind != "" && id != "" {
			later[outputKind+"\x00"+id] = true
		}
		if callKind := toolCallKind(kind); callKind != "" && id != "" && !later[callKind+"\x00"+id] {
			orphan[index] = true
		}
	}
	if len(orphan) == 0 {
		return input
	}
	out := make([]json.RawMessage, 0, len(input)+len(orphan))
	for index, item := range input {
		if !orphan[index] {
			out = append(out, item)
			continue
		}
		kind := toolCallKind(gjson.GetBytes(item, "type").String())
		id := callID(item)
		if kind == "computer" {
			out = append(out, assistantNote(fmt.Sprintf("[Computer call interrupted before a screenshot was recorded; call_id=%s]", id)))
			continue
		}
		outputType := "function_call_output"
		if kind == "custom" {
			outputType = "custom_tool_call_output"
		}
		out = append(out, item, jsjson.Object(
			jsjson.Field{Name: "type", Value: jsjson.Marshal(outputType)},
			jsjson.Field{Name: "call_id", Value: jsjson.Marshal(id)},
			jsjson.Field{Name: "output", Value: jsjson.Marshal(orphanToolCallPlaceholder)},
		))
	}
	return out
}

func batchKind(item json.RawMessage) string {
	parsed := gjson.ParseBytes(item)
	kind := parsed.Get("type").String()
	switch {
	case toolCallKind(kind) != "":
		return "call"
	case toolOutputKind(kind) != "":
		return "output"
	case kind == "message" && parsed.Get("role").String() == "assistant":
		return "assistant-message"
	}
	return "other"
}

// hoistInterleavedToolBatchMessages is hoistInterleavedResponsesToolBatchMessages:
// assistant messages wedged inside a call→output batch move before the batch.
func hoistInterleavedToolBatchMessages(items []json.RawMessage) []json.RawMessage {
	kinds := make([]string, len(items))
	for i, item := range items {
		kinds[i] = batchKind(item)
	}
	hasEarlierBatchCall := func(index int, id string) bool {
		for probe := index - 1; probe >= 0; probe-- {
			if kinds[probe] == "other" {
				return false
			}
			if kinds[probe] == "call" && callID(items[probe]) == id {
				return true
			}
		}
		return false
	}
	bucketOf := map[int]int{}
	insertBefore := map[int][]int{}
	for index := range items {
		if kinds[index] != "output" || (index > 0 && kinds[index-1] == "output") {
			continue
		}
		pending := map[string]bool{}
		for probe := index; probe < len(items) && kinds[probe] == "output"; probe++ {
			if id := callID(items[probe]); id != "" {
				pending[id] = true
			}
		}
		start, sawCall := index, false
		var messages []int
	walk:
		for start > 0 {
			switch kinds[start-1] {
			case "call":
				sawCall = true
				if id := callID(items[start-1]); id != "" {
					delete(pending, id)
				}
			case "output":
				id := callID(items[start-1])
				if id == "" || !hasEarlierBatchCall(start-1, id) {
					break walk
				}
				owns := false
				for owned := range pending {
					if hasEarlierBatchCall(start-1, owned) {
						owns = true
						break
					}
				}
				if !owns {
					break walk
				}
				pending[id] = true
			case "assistant-message":
				messages = append(messages, start-1)
			default:
				break walk
			}
			start--
		}
		if !sawCall || len(messages) == 0 {
			continue
		}
		for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
			messages[i], messages[j] = messages[j], messages[i]
		}
		target := insertBefore[start]
		for _, message := range messages {
			if previousStart, ok := bucketOf[message]; ok {
				previous := insertBefore[previousStart]
				for slot, candidate := range previous {
					if candidate == message {
						insertBefore[previousStart] = append(previous[:slot:slot], previous[slot+1:]...)
						break
					}
				}
				if previousStart == start {
					target = insertBefore[start]
				}
			}
			bucketOf[message] = start
			target = append(target, message)
		}
		insertBefore[start] = target
	}
	if len(bucketOf) == 0 {
		return items
	}
	out := make([]json.RawMessage, 0, len(items))
	for index, item := range items {
		for _, message := range insertBefore[index] {
			out = append(out, items[message])
		}
		if _, moved := bucketOf[index]; moved {
			continue
		}
		out = append(out, item)
	}
	return out
}

// syntheticReasoningText is SYNTHETIC_REASONING_REPLAY_PLACEHOLDER.
const syntheticReasoningText = "reasoning unavailable"

// reasoningReplay says which replayed assistant turns must carry a reasoning item.
type reasoningReplay struct {
	allTurns  bool
	toolCalls bool
}

// rewriteField replaces one top-level field of a JSON object in place.
func rewriteField(item json.RawMessage, name string, value json.RawMessage) json.RawMessage {
	var fields object
	gjson.ParseBytes(item).ForEach(func(key, field gjson.Result) bool {
		fields = append(fields, jsjson.Field{Name: key.String(), Value: json.RawMessage(field.Raw)})
		return true
	})
	fields.set(name, value)
	return fields.bytes()
}

// responsesTurnBoundary is isResponsesAssistantTurnBoundary.
func responsesTurnBoundary(item json.RawMessage) bool {
	parsed := gjson.ParseBytes(item)
	kind := parsed.Get("type").String()
	if toolOutputKind(kind) != "" || kind == "compaction" {
		return true
	}
	role := parsed.Get("role")
	return role.Exists() && role.String() != "assistant"
}

// ensureRequiredReasoningReplay is ensureRequiredResponsesReasoningReplay
// over the flat transcript: every assistant turn (the items between two turn
// boundaries) that carries content, or a tool call, gets a reasoning item when
// the target requires one, and replayed reasoning gains the required text.
func ensureRequiredReasoningReplay(items []json.RawMessage, replay reasoningReplay) []json.RawMessage {
	if !replay.allTurns && !replay.toolCalls {
		return items
	}
	out := make([]json.RawMessage, 0, len(items))
	turnStart := 0
	for index := 0; index <= len(items); index++ {
		if index < len(items) && !responsesTurnBoundary(items[index]) {
			continue
		}
		hasContent, hasReasoning, hasToolCall := false, false, false
		for turnIndex := turnStart; turnIndex < index; turnIndex++ {
			if gjson.GetBytes(items[turnIndex], "type").String() == "reasoning" {
				hasReasoning = true
				continue
			}
			hasContent = true
			if batchKind(items[turnIndex]) == "call" {
				hasToolCall = true
			}
		}
		required := hasContent && (replay.allTurns || (replay.toolCalls && hasToolCall))
		if required && !hasReasoning {
			out = append(out, syntheticReasoningItem())
		}
		for turnIndex := turnStart; turnIndex < index; turnIndex++ {
			item := items[turnIndex]
			if required && gjson.GetBytes(item, "type").String() == "reasoning" {
				item = withRequiredReasoningText(item)
			}
			out = append(out, item)
		}
		if index < len(items) {
			out = append(out, items[index])
		}
		turnStart = index + 1
	}
	return out
}

func syntheticReasoningItem() json.RawMessage {
	return jsjson.Object(
		jsjson.Field{Name: "type", Value: jsjson.Marshal("reasoning")},
		jsjson.Field{Name: "summary", Value: jsjson.Marshal([]json.RawMessage{})},
		jsjson.Field{Name: "content", Value: jsjson.Marshal([]json.RawMessage{jsjson.Object(
			jsjson.Field{Name: "type", Value: jsjson.Marshal("reasoning_text")},
			jsjson.Field{Name: "text", Value: jsjson.Marshal(syntheticReasoningText)},
		)})},
	)
}

// withRequiredReasoningText fills a replayed reasoning item's missing
// reasoning_text from its summary; items carrying encrypted_content are
// opaque and replay untouched.
func withRequiredReasoningText(item json.RawMessage) json.RawMessage {
	parsed := gjson.ParseBytes(item)
	if parsed.Get("encrypted_content").Type == gjson.String {
		return item
	}
	for _, part := range parsed.Get("content").Array() {
		if part.Get("type").String() == "reasoning_text" && strings.TrimSpace(part.Get("text").String()) != "" {
			return item
		}
	}
	var summaries []string
	for _, part := range parsed.Get("summary").Array() {
		if text := part.Get("text").String(); strings.TrimSpace(text) != "" {
			summaries = append(summaries, text)
		}
	}
	text := strings.Join(summaries, "\n\n")
	if text == "" {
		text = syntheticReasoningText
	}
	return rewriteField(item, "content", jsjson.Marshal([]json.RawMessage{jsjson.Object(
		jsjson.Field{Name: "type", Value: jsjson.Marshal("reasoning_text")},
		jsjson.Field{Name: "text", Value: jsjson.Marshal(text)},
	)}))
}
