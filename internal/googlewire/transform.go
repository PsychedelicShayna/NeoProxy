package googlewire

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
)

// maxToolCallIDLength is MAX_TOOL_CALL_ID_LENGTH.
const maxToolCallIDLength = 64

var googleToolNamePattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_.:-]*$`)

// NormalizeToolCallID is normalizeToolCallId: characters outside [A-Za-z0-9_-]
// (counted in UTF-16 units, as JavaScript does) become "_" and the id is
// capped at 64 units.
func NormalizeToolCallID(id string) string {
	var out strings.Builder
	units := 0
	for _, r := range id {
		width := 1
		if r > 0xFFFF {
			width = 2
		}
		for i := 0; i < width && units < maxToolCallIDLength; i++ {
			if width == 1 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
				out.WriteRune(r)
			} else {
				out.WriteByte('_')
			}
			units++
		}
		if units >= maxToolCallIDLength {
			break
		}
	}
	return out.String()
}

// RenderDemotedThinking is renderDemotedThinking: prior reasoning demoted into
// text, wrapped in the target model's own thinking dialect.
func RenderDemotedThinking(modelID, text string) string {
	if text == "" {
		return ""
	}
	text = toWellFormed(text)
	id := strings.ToLower(modelID)
	switch {
	case strings.Contains(id, "claude"):
		return text
	case strings.Contains(id, "gemini"):
		return "```thinking\n" + text + "\n```"
	case strings.Contains(id, "gemma"):
		return "<think>\n" + text + "\n</think>"
	default:
		return "<thinking>\n" + text + "\n</thinking>"
	}
}

func toWellFormed(text string) string { return strings.ToValidUTF8(text, "\uFFFD") }

func isMalformedToolCall(block Block, maxLength int) bool {
	if strings.TrimSpace(block.ID) == "" {
		return true
	}
	return len(utf16.Encode([]rune(block.Name))) > maxLength || !googleToolNamePattern.MatchString(block.Name)
}

// sanitizeMalformedToolCalls drops tool calls whose id or name the wire cannot
// replay, the tool results that answer them, and assistant turns left empty.
func sanitizeMalformedToolCalls(messages []Message, maxLength int) []Message {
	hasMalformed := false
	for _, msg := range messages {
		if msg.Role != RoleAssistant {
			continue
		}
		for _, block := range msg.Content {
			if block.Type == BlockToolCall && isMalformedToolCall(block, maxLength) {
				hasMalformed = true
			}
		}
	}
	if !hasMalformed {
		return messages
	}
	dropQueues := map[string][]bool{}
	var result []Message
	for _, msg := range messages {
		switch msg.Role {
		case RoleAssistant:
			dropQueues = map[string][]bool{}
			var filtered []Block
			for _, block := range msg.Content {
				if block.Type == BlockToolCall {
					malformed := isMalformedToolCall(block, maxLength)
					dropQueues[block.ID] = append(dropQueues[block.ID], malformed)
					if malformed {
						continue
					}
				}
				filtered = append(filtered, block)
			}
			if len(filtered) == 0 {
				continue
			}
			if len(filtered) != len(msg.Content) {
				msg.Content = filtered
			}
			result = append(result, msg)
		case RoleToolResult:
			if queue := dropQueues[msg.ToolCallID]; len(queue) > 0 {
				drop := queue[0]
				if len(queue) == 1 {
					delete(dropQueues, msg.ToolCallID)
				} else {
					dropQueues[msg.ToolCallID] = queue[1:]
				}
				if drop {
					continue
				}
			}
			result = append(result, msg)
		default:
			dropQueues = map[string][]bool{}
			result = append(result, msg)
		}
	}
	return result
}

// shouldDropTruncatedThinkingOnlyAssistant is NeoPi's predicate of the same name.
func shouldDropTruncatedThinkingOnlyAssistant(msg Message) bool {
	truncated := msg.StopReason == "length" || msg.StopReason == "error" || msg.StopReason == "aborted"
	if !truncated {
		return false
	}
	for _, block := range msg.Content {
		if block.Type == BlockToolCall || block.Type == BlockText {
			return false
		}
	}
	return true
}

func appendDuplicateSuffix(id, suffix string, maxLength int) string {
	if strings.Contains(id, "|") {
		parts := strings.Split(id, "|")
		for i, part := range parts {
			parts[i] = appendSegmentDuplicateSuffix(part, suffix, maxLength)
		}
		return strings.Join(parts, "|")
	}
	return appendSegmentDuplicateSuffix(id, suffix, maxLength)
}

func appendSegmentDuplicateSuffix(segment, suffix string, maxLength int) string {
	units := utf16.Encode([]rune(segment))
	if len(units)+len(suffix) <= maxLength {
		return segment + suffix
	}
	budget := max(0, maxLength-len(suffix))
	if budget > len(units) {
		budget = len(units)
	}
	return string(utf16.Decode(units[:budget])) + suffix
}

type indexedResult struct {
	index    int
	msg      Message
	consumed bool
}

// TransformMessages is transformMessages for a google-generative-ai target:
// foreign turns lose signatures and demote thinking to text, tool-call ids are
// normalized and made unique, every call gets exactly one following result
// ("No result provided" when none arrived) and orphan results become a
// stale-tool-result note or are dropped.
func TransformMessages(messages []Message, model *Model) []Message {
	messages = sanitizeMalformedToolCalls(messages, model.NameMaxLength)

	toolCallIDMap := map[string]string{}
	normalized := make([]Message, len(messages))
	for index, msg := range messages {
		switch msg.Role {
		case RoleToolResult:
			if id, ok := toolCallIDMap[msg.ToolCallID]; ok && id != msg.ToolCallID {
				msg.ToolCallID = id
			}
		case RoleAssistant:
			msg = transformAssistant(msg, model, toolCallIDMap)
		}
		normalized[index] = msg
	}

	// deduplicateToolCallIds. Opaque composite ids pair by raw equality and no
	// Responses-family turn exists here, so a pairing key is the id itself.
	seen := map[string]int{}
	pendingRewrites := map[string][]*string{}
	realResults := map[string][]*indexedResult{}
	validToolUse := map[string]bool{}
	transformed := make([]Message, len(normalized))
	for index, msg := range normalized {
		switch msg.Role {
		case RoleToolResult:
			key := msg.ToolCallID
			rewrites := pendingRewrites[key]
			if len(rewrites) == 0 {
				realResults[key] = append(realResults[key], &indexedResult{index: index, msg: msg})
				break
			}
			rewrite := rewrites[0]
			if len(rewrites) == 1 {
				delete(pendingRewrites, key)
			} else {
				pendingRewrites[key] = rewrites[1:]
			}
			if rewrite != nil {
				msg.ToolCallID = *rewrite
			}
			realResults[msg.ToolCallID] = append(realResults[msg.ToolCallID], &indexedResult{index: index, msg: msg})
		case RoleAssistant:
			touched := map[string]bool{}
			content := make([]Block, len(msg.Content))
			copy(content, msg.Content)
			for i, block := range content {
				if block.Type != BlockToolCall {
					continue
				}
				key := block.ID
				if !touched[key] {
					touched[key] = true
					delete(pendingRewrites, key)
				}
				previous := seen[key]
				if previous == 0 {
					seen[key] = 1
					pendingRewrites[key] = append(pendingRewrites[key], nil)
					continue
				}
				dup := previous
				replacement := appendDuplicateSuffix(block.ID, "_dup"+strconv.Itoa(dup), maxToolCallIDLength)
				for seen[replacement] > 0 {
					dup++
					replacement = appendDuplicateSuffix(block.ID, "_dup"+strconv.Itoa(dup), maxToolCallIDLength)
				}
				seen[key] = dup + 1
				seen[replacement] = 1
				value := replacement
				pendingRewrites[key] = append(pendingRewrites[key], &value)
				content[i].ID = replacement
			}
			for _, block := range content {
				if block.Type == BlockToolCall {
					validToolUse[block.ID] = true
				}
			}
			msg.Content = content
		}
		transformed[index] = msg
	}

	takeResult := func(id string, after int) (Message, bool) {
		for _, entry := range realResults[id] {
			if entry.consumed || entry.index <= after {
				continue
			}
			entry.consumed = true
			return entry.msg, true
		}
		return Message{}, false
	}

	const (
		statusResolved = 1
		statusAborted  = 2
	)
	var result []Message
	var pendingCalls []Block
	pendingCallsStart := -1
	pendingAborted := map[string]Block{}
	var pendingAbortedOrder []string
	pendingAbortedActive := false
	pendingAbortedStart := -1
	status := map[string]int{}

	synthetic := func(call Block, text string) Message {
		return Message{Role: RoleToolResult, ToolCallID: call.ID, ToolName: call.Name, IsError: true,
			Content: []Block{{Type: BlockText, Text: text}}}
	}
	flushPending := func() {
		for _, call := range pendingCalls {
			if status[call.ID] != 0 {
				continue
			}
			if real, ok := takeResult(call.ID, pendingCallsStart); ok {
				result = append(result, real)
			} else {
				result = append(result, synthetic(call, "No result provided"))
			}
			status[call.ID] = statusResolved
		}
		pendingCalls = nil
	}
	flushAborted := func() {
		if !pendingAbortedActive {
			return
		}
		for _, id := range pendingAbortedOrder {
			call, ok := pendingAborted[id]
			if !ok || status[call.ID] != 0 {
				continue
			}
			if real, ok := takeResult(call.ID, pendingAbortedStart); ok {
				result = append(result, real)
				status[call.ID] = statusResolved
				continue
			}
			result = append(result, synthetic(call, "aborted"))
			status[call.ID] = statusAborted
		}
		pendingAborted = map[string]Block{}
		pendingAbortedOrder = nil
		pendingAbortedActive = false
	}
	hasOpenPending := func() bool {
		for _, call := range pendingCalls {
			if status[call.ID] == 0 {
				return true
			}
		}
		return len(pendingAborted) > 0
	}

	for i, msg := range transformed {
		switch msg.Role {
		case RoleAssistant:
			flushPending()
			flushAborted()
			if shouldDropTruncatedThinkingOnlyAssistant(messages[i]) {
				continue
			}
			var calls []Block
			for _, block := range msg.Content {
				if block.Type == BlockToolCall {
					calls = append(calls, block)
				}
			}
			if msg.StopReason == "error" || msg.StopReason == "aborted" {
				result = append(result, msg)
				pendingAborted = map[string]Block{}
				pendingAbortedOrder = nil
				for _, call := range calls {
					if _, exists := pendingAborted[call.ID]; !exists {
						pendingAbortedOrder = append(pendingAbortedOrder, call.ID)
					}
					pendingAborted[call.ID] = call
				}
				pendingAbortedActive = true
				pendingAbortedStart = i
				continue
			}
			if len(calls) > 0 {
				pendingCalls = calls
				pendingCallsStart = i
			}
			result = append(result, msg)
		case RoleToolResult:
			key := msg.ToolCallID
			if status[key] != 0 {
				continue
			}
			if _, ok := pendingAborted[key]; ok {
				delete(pendingAborted, key)
				status[key] = statusResolved
				result = append(result, msg)
				continue
			}
			inWindow := false
			for _, call := range pendingCalls {
				if call.ID == key {
					inWindow = true
				}
			}
			if inWindow {
				status[key] = statusResolved
				result = append(result, msg)
				continue
			}
			if !validToolUse[key] {
				if hasOpenPending() {
					continue
				}
				var texts []string
				for _, part := range msg.Content {
					if part.Type == BlockText && strings.TrimSpace(part.Text) != "" {
						texts = append(texts, part.Text)
					}
				}
				if len(texts) > 0 {
					attr := ""
					if msg.IsError {
						attr = ` is-error="true"`
					}
					result = append(result, Message{Role: RoleUser, IsString: true,
						Text: "<stale-tool-result tool=\"" + msg.ToolName + "\" id=\"" + msg.ToolCallID + "\"" + attr + ">\n" + strings.Join(texts, "\n") + "\n</stale-tool-result>"})
				}
			}
		default:
			flushPending()
			flushAborted()
			result = append(result, msg)
		}
	}
	flushPending()
	flushAborted()
	return result
}

func transformAssistant(msg Message, model *Model, idMap map[string]string) Message {
	isSame := msg.Provider == model.Provider && msg.API == model.API && msg.Model == model.ID
	invalidStop := msg.StopReason == "aborted" || msg.StopReason == "error"
	hasCall := false
	for _, block := range msg.Content {
		hasCall = hasCall || block.Type == BlockToolCall
	}
	abandonedToolUse := !invalidStop && msg.StopReason != "toolUse" && hasCall
	last := len(msg.Content) - 1
	var content []Block
	for index, block := range msg.Content {
		switch block.Type {
		case BlockThinking:
			sanitized := block
			if (abandonedToolUse || invalidStop && index == last) && sanitized.Signature != "" {
				sanitized.Signature = ""
			}
			if isSame && sanitized.Signature != "" {
				content = append(content, sanitized)
				continue
			}
			if strings.TrimSpace(sanitized.Text) == "" {
				continue
			}
			if isSame {
				content = append(content, sanitized)
				continue
			}
			content = append(content, Block{Type: BlockText, Text: RenderDemotedThinking(model.ID, sanitized.Text), Demoted: true})
		case BlockText:
			if isSame {
				content = append(content, block)
			} else {
				content = append(content, Block{Type: BlockText, Text: block.Text})
			}
		case BlockToolCall:
			call := block
			if !isSame && call.Signature != "" {
				call.Signature = ""
			}
			if !isSame {
				if id := NormalizeToolCallID(call.ID); id != call.ID {
					idMap[call.ID] = id
					call.ID = id
				}
			}
			content = append(content, call)
		}
	}
	if n := len(content); n > 0 && content[n-1].Type == BlockText && content[n-1].Demoted {
		content[n-1].Text = strings.TrimRight(content[n-1].Text, " \t\n\r\f\v\u00a0\u2028\u2029\ufeff")
	}
	msg.Content = content
	return msg
}
