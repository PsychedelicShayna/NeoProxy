package antigravitywire

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf16"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopidialect"
	"github.com/tidwall/gjson"
)

// Retry policy for the well-known Gemini "empty response" failure (a benign
// STOP carrying only empty text and no tool call): MAX_EMPTY_STREAM_RETRIES
// and EMPTY_STREAM_BASE_DELAY_MS.
const (
	MaxEmptyStreamRetries = 2
	EmptyStreamBaseDelay  = 500 * time.Millisecond
)

// Usage is mapGoogleUsage's result.
type Usage struct {
	Input, Output, CacheRead, TotalTokens, Reasoning int64
}

// mapGoogleUsage maps `usageMetadata` onto Usage: promptTokenCount includes
// cachedContentTokenCount, so input = prompt - cached; a missing prompt count
// falls back to total - candidates - thoughts and cached is clamped to it.
func mapGoogleUsage(metadata gjson.Result) Usage {
	candidates := metadata.Get("candidatesTokenCount").Int()
	thinking := metadata.Get("thoughtsTokenCount").Int()
	total := metadata.Get("totalTokenCount").Int()
	prompt := metadata.Get("promptTokenCount").Int()
	if prompt == 0 {
		prompt = max(0, total-candidates-thinking)
	}
	cacheRead := min(metadata.Get("cachedContentTokenCount").Int(), prompt)
	return Usage{Input: prompt - cacheRead, Output: candidates + thinking, CacheRead: cacheRead, TotalTokens: total, Reasoning: thinking}
}

var toolCallCounter atomic.Int64

// NextToolCallID is nextToolCallId: `${name}_${Date.now()}_${++counter}`.
func NextToolCallID(name string, now time.Time) string {
	return name + "_" + strconv.FormatInt(now.UnixMilli(), 10) + "_" + strconv.FormatInt(toolCallCounter.Add(1), 10)
}

// StopReason is NeoPi's StopReason as the attempt resolves it.
type StopReason string

const (
	StopReasonStop    StopReason = "stop"
	StopReasonLength  StopReason = "length"
	StopReasonToolUse StopReason = "toolUse"
	StopReasonError   StopReason = "error"
)

// StreamError is an in-band or response-level failure of one attempt.
type StreamError struct {
	// Status is the HTTP-like status (0 when the failure carries none).
	Status int
	// Kind is the ProviderResponseError kind: "runtime", "content-blocked", ...
	Kind    string
	Message string
}

func (e *StreamError) Error() string { return e.Message }

type blockKind int

const (
	blockText blockKind = iota
	blockThinking
)

type block struct {
	kind      blockKind
	text      strings.Builder
	signature string
	index     int
}

// Attempt is one streamResponse pass over a Cloud Code Assist response. It
// shapes NeoPi's AssistantMessage events as Anthropic Messages events and
// holds them back until the attempt is known not to be a retriable empty
// response, so a retry never shows the client a stale prefix.
type Attempt struct {
	toolNames map[string]bool
	flashLeak bool
	now       func() time.Time

	healer *neopidialect.Healer

	current        *block
	nextIndex      int
	toolCalls      int
	meaningful     bool
	hasThinking    bool
	started        bool
	strippedLeak   bool
	isBuffering    bool
	textBuffer     string
	bufferedSig    string
	stopReason     StopReason
	errorMessage   string
	sawFinish      bool
	usage          Usage
	lastResponseID string
	model          string
	toolIDs        []string
	heldLimit      int

	pending [][]byte
}

// NewAttempt starts an attempt. toolNames are the declared tool names the
// planning-leak filter recognizes; flashLeak enables NeoPi's flash stream
// leak workaround; model is the client-facing model id.
func NewAttempt(model string, toolNames []string, flashLeak bool, now func() time.Time) *Attempt {
	if now == nil {
		now = time.Now
	}
	names := map[string]bool{}
	for _, name := range toolNames {
		names[name] = true
	}
	return &Attempt{toolNames: names, flashLeak: flashLeak, now: now, healer: neopidialect.NewThinkingHealer(), stopReason: StopReasonStop, model: model}
}

// Started reports whether any content block opened (NeoPi's `started`): once
// true the endpoint is committed downstream and failover is unsafe.
func (a *Attempt) Started() bool { return a.started }

// Meaningful is hasMeaningfulGoogleContent: a tool call or non-whitespace text.
func (a *Attempt) Meaningful() bool { return a.meaningful }

// ThoughtOnly is hasThinkingOutput: thinking text or a signature streamed.
func (a *Attempt) ThoughtOnly() bool { return a.hasThinking }

// StrippedLeak reports whether the planning-leak filter dropped a leak.
func (a *Attempt) StrippedLeak() bool { return a.strippedLeak }

// StopReason is the attempt's resolved stop reason.
func (a *Attempt) StopReason() StopReason { return a.stopReason }

// ErrorMessage is the finish-reason failure text when StopReason is error.
func (a *Attempt) ErrorMessage() string { return a.errorMessage }

// SawFinish reports whether a finishReason arrived.
func (a *Attempt) SawFinish() bool { return a.sawFinish }

// Usage is the latest usageMetadata mapping.
func (a *Attempt) Usage() Usage { return a.usage }

// ResponseID is the last responseId seen.
func (a *Attempt) ResponseID() string { return a.lastResponseID }

// ToolCalls counts streamed tool calls.
func (a *Attempt) ToolCalls() int { return a.toolCalls }

func (a *Attempt) emit(event string) { a.pending = append(a.pending, []byte(event)) }

func (a *Attempt) endCurrent() {
	block := a.current
	if block == nil {
		return
	}
	if block.kind == blockThinking && block.signature != "" {
		a.emit(fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"signature_delta","signature":%s}}`, block.index, jsjson.Marshal(block.signature)))
	} else if block.kind == blockText && block.signature != "" {
		a.emit(fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"text_delta","text":"","textSignature":%s}}`, block.index, jsjson.Marshal(block.signature)))
	}
	a.emit(fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, block.index))
	a.current = nil
}

func (a *Attempt) startBlock(kind blockKind) *block {
	if a.current != nil && a.current.kind == kind {
		return a.current
	}
	a.endCurrent()
	next := &block{kind: kind, index: a.nextIndex}
	a.nextIndex++
	a.current = next
	a.started = true
	if kind == blockThinking {
		a.emit(fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"thinking","thinking":""}}`, next.index))
	} else {
		a.emit(fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"text","text":""}}`, next.index))
	}
	return next
}

func retainSignature(existing, incoming string) string {
	if incoming != "" {
		return incoming
	}
	return existing
}

func (a *Attempt) emitVisibleText(delta string) {
	if delta == "" {
		return
	}
	block := a.startBlock(blockText)
	block.text.WriteString(delta)
	if strings.TrimSpace(block.text.String()) != "" {
		a.meaningful = true
	}
	a.emit(fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"text_delta","text":%s}}`, block.index, jsjson.Marshal(delta)))
}

func (a *Attempt) emitThinking(delta string) {
	if delta == "" {
		return
	}
	block := a.startBlock(blockThinking)
	a.emitThinkingText(block, delta)
}

func (a *Attempt) feedVisible(text, signature string) {
	for _, event := range a.healer.Feed(text) {
		a.healEvent(event, signature)
	}
}

func (a *Attempt) flushVisible(signature string) {
	for _, event := range a.healer.Flush() {
		a.healEvent(event, signature)
	}
}

func (a *Attempt) healEvent(event neopidialect.HealEvent, signature string) {
	switch event.Kind {
	case neopidialect.KindText:
		a.emitVisibleText(event.Text)
		if a.current != nil && a.current.kind == blockText {
			a.current.signature = retainSignature(a.current.signature, signature)
		}
	case neopidialect.KindThinking:
		a.emitThinking(event.Text)
	}
}

// retainCurrentSignature is retainCurrentBlockThoughtSignature: a
// signature-only empty part signs the open block.
func (a *Attempt) retainCurrentSignature(signature string) {
	if a.current == nil {
		return
	}
	a.current.signature = retainSignature(a.current.signature, signature)
	if a.current.kind == blockThinking && signature != "" {
		a.hasThinking = true
	}
}

// Feed processes one decoded SSE event. A non-nil error ends the attempt (an
// in-band stream error or a blocked prompt).
func (a *Attempt) Feed(chunk []byte) error {
	root := gjson.ParseBytes(chunk)
	if errorValue := root.Get("error"); errorValue.IsObject() || (errorValue.Type == gjson.String && errorValue.Str != "") {
		return inBandError(errorValue)
	}
	response := root.Get("response")
	if !response.Exists() {
		return nil
	}
	if id := response.Get("responseId").String(); id != "" {
		a.lastResponseID = id
	}
	candidates := response.Get("candidates")
	if blockReason := response.Get("promptFeedback.blockReason").String(); (!candidates.IsArray() || len(candidates.Array()) == 0) && blockReason != "" {
		detail := response.Get("promptFeedback.blockReasonMessage").String()
		message := "Request blocked by Google (" + blockReason + ")"
		if detail != "" {
			message += ": " + detail
		}
		return &StreamError{Kind: "content-blocked", Message: message}
	}
	candidate := candidates.Get("0")
	if parts := candidate.Get("content.parts"); parts.IsArray() {
		for _, part := range parts.Array() {
			a.feedPart(part)
		}
	}
	if finish := candidate.Get("finishReason").String(); finish != "" {
		a.sawFinish = true
		mapped := StopReasonError
		switch finish {
		case "STOP":
			mapped = StopReasonStop
		case "MAX_TOKENS":
			mapped = StopReasonLength
		}
		// Only a trailing tool call upgrades benign finishes; error finishes
		// (SAFETY, MALFORMED_FUNCTION_CALL, ...) surface even with tool calls.
		if (mapped == StopReasonStop || mapped == StopReasonLength) && a.toolCalls > 0 {
			a.stopReason = StopReasonToolUse
		} else {
			a.stopReason = mapped
			if mapped == StopReasonError {
				a.errorMessage = "Generation failed with finish reason: " + finish
			}
		}
	}
	if metadata := response.Get("usageMetadata"); metadata.Exists() {
		a.usage = mapGoogleUsage(metadata)
	}
	return nil
}

// inBandError is the in-stream error envelope: `Cloud Code Assist stream
// error: <detail>`, an HTTP-style failure when the code is >= 400.
func inBandError(errorValue gjson.Result) error {
	message, status := errorValue.Get("message").String(), errorValue.Get("status").String()
	detail := "unknown error"
	switch {
	case message != "" && status != "":
		detail = message + " (" + status + ")"
	case message != "":
		detail = message
	case status != "":
		detail = status
	}
	residue := ""
	if details := errorValue.Get("details"); details.IsArray() && len(details.Array()) > 0 {
		residue = " " + string(jsjson.Object(jsjson.Field{Name: "error", Value: jsjson.Object(
			jsjson.Field{Name: "status", Value: statusRaw(status)},
			jsjson.Field{Name: "details", Value: json.RawMessage(details.Raw)})}))
	}
	text := "Cloud Code Assist stream error: " + detail + residue
	if code := errorValue.Get("code"); code.Type == gjson.Number && code.Int() >= 400 {
		return &StreamError{Status: int(code.Int()), Kind: "api", Message: text}
	}
	return &StreamError{Kind: "runtime", Message: text}
}

func statusRaw(status string) json.RawMessage {
	if status == "" {
		return nil
	}
	return jsjson.Marshal(status)
}

func (a *Attempt) feedPart(part gjson.Result) {
	text := part.Get("text")
	signature := part.Get("thoughtSignature").String()
	functionCall := part.Get("functionCall")
	switch {
	case text.Exists() && text.Str != "":
		if part.Get("thought").Bool() {
			a.flushVisible("")
			block := a.startBlock(blockThinking)
			block.signature = retainSignature(block.signature, signature)
			a.emitThinkingText(block, text.Str)
		} else {
			switch {
			case a.isBuffering:
				a.textBuffer += text.Str
				a.bufferedSig = retainSignature(a.bufferedSig, signature)
			case a.flashLeak && strings.HasPrefix(trimStartJS(text.Str), "{"):
				a.isBuffering, a.textBuffer, a.bufferedSig = true, text.Str, signature
			default:
				a.feedVisible(text.Str, signature)
			}
			if a.isBuffering {
				buffered := consumePlanningBuffer(a.textBuffer, a.toolNames, false)
				if buffered.kind != planningIncomplete {
					if buffered.kind == planningLeak {
						a.strippedLeak = true
					}
					visibleSignature := a.bufferedSig
					a.isBuffering, a.textBuffer, a.bufferedSig = false, "", ""
					a.feedVisible(buffered.visible, visibleSignature)
				}
			}
		}
	case text.Exists() && text.Str == "" && signature != "" && !functionCall.Exists():
		a.retainCurrentSignature(signature)
	}
	if functionCall.Exists() {
		a.flushVisible("")
		a.endCurrent()
		a.isBuffering, a.textBuffer = false, ""
		a.emitToolCall(functionCall, signature)
	}
}

// emitThinkingText appends a thought part to its block (the block was just
// started or is the open thinking block).
func (a *Attempt) emitThinkingText(block *block, text string) {
	block.text.WriteString(text)
	if strings.TrimSpace(block.text.String()) != "" || block.signature != "" {
		a.hasThinking = true
	}
	a.emit(fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"thinking_delta","thinking":%s}}`, block.index, jsjson.Marshal(text)))
}

func (a *Attempt) emitToolCall(call gjson.Result, signature string) {
	name := call.Get("name").String()
	id := call.Get("id").String()
	if id == "" || a.usedToolID(id) {
		fallback := name
		if fallback == "" {
			fallback = "tool"
		}
		id = NextToolCallID(fallback, a.now())
	}
	a.rememberToolID(id)
	args := "{}"
	if raw := call.Get("args"); raw.Exists() && raw.Type != gjson.Null {
		args = string(jsjson.Normalize([]byte(raw.Raw)))
	}
	a.toolCalls++
	a.started, a.meaningful = true, true
	index := a.nextIndex
	a.nextIndex++
	signatureField := ""
	if signature != "" {
		signatureField = `,"thoughtSignature":` + string(jsjson.Marshal(signature))
	}
	a.emit(fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"tool_use","id":%s,"name":%s,"input":{}%s}}`, index, jsjson.Marshal(id), jsjson.Marshal(name), signatureField))
	a.emit(fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"input_json_delta","partial_json":%s}}`, index, jsjson.Marshal(args)))
	a.emit(fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, index))
}

func (a *Attempt) usedToolID(id string) bool {
	for _, used := range a.toolIDs {
		if used == id {
			return true
		}
	}
	return false
}

func (a *Attempt) rememberToolID(id string) { a.toolIDs = append(a.toolIDs, id) }

// Finish ends the stream: it releases a buffered planning candidate and held
// markup, and closes the open block.
func (a *Attempt) Finish() {
	if a.isBuffering && a.textBuffer != "" {
		buffered := consumePlanningBuffer(a.textBuffer, a.toolNames, true)
		if buffered.kind != planningIncomplete {
			if buffered.kind == planningLeak {
				a.strippedLeak = true
			}
			a.feedVisible(buffered.visible, a.bufferedSig)
		}
		a.bufferedSig, a.isBuffering, a.textBuffer = "", false, ""
	}
	a.flushVisible(a.bufferedSig)
	a.endCurrent()
}

// CommitReady reports whether the attempt can no longer be retried as an
// empty response: non-whitespace text, thinking or a tool call arrived, or
// enough text is held that streaming it beats waiting for a retry.
func (a *Attempt) CommitReady() bool {
	if a.meaningful || a.hasThinking {
		return true
	}
	held := 0
	for _, event := range a.pending {
		held += len(event)
	}
	return held > a.heldLimit
}

// SetHeldLimit bounds the whitespace an empty-response retry may hold back.
func (a *Attempt) SetHeldLimit(limit int) { a.heldLimit = limit }

// Take returns and clears the events held since the last call. The caller
// releases them once the attempt is accepted, or drops them on a retry.
func (a *Attempt) Take() [][]byte {
	out := a.pending
	a.pending = nil
	return out
}

// DiscardPending drops held events (a retried empty attempt).
func (a *Attempt) DiscardPending() { a.pending = nil }

// MessageStart is the Anthropic message_start event for the response.
func MessageStart(id, model string) string {
	return string(jsjson.Object(
		jsjson.Field{Name: "type", Value: jsjson.Marshal("message_start")},
		jsjson.Field{Name: "message", Value: jsjson.Object(
			jsjson.Field{Name: "id", Value: jsjson.Marshal(id)},
			jsjson.Field{Name: "type", Value: jsjson.Marshal("message")},
			jsjson.Field{Name: "role", Value: jsjson.Marshal("assistant")},
			jsjson.Field{Name: "model", Value: jsjson.Marshal(model)},
			jsjson.Field{Name: "content", Value: json.RawMessage("[]")},
			jsjson.Field{Name: "stop_reason", Value: json.RawMessage("null")},
			jsjson.Field{Name: "stop_sequence", Value: json.RawMessage("null")},
			jsjson.Field{Name: "usage", Value: json.RawMessage(`{"input_tokens":0,"output_tokens":0}`)},
		)},
	))
}

// MessageEnd is the closing message_delta and message_stop pair.
func (a *Attempt) MessageEnd() []string {
	reason := "end_turn"
	switch a.stopReason {
	case StopReasonLength:
		reason = "max_tokens"
	case StopReasonToolUse:
		reason = "tool_use"
	}
	return []string{
		fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":%q,"stop_sequence":null},"usage":{"input_tokens":%d,"output_tokens":%d,"cache_read_input_tokens":%d,"cache_creation_input_tokens":0}}`,
			reason, a.usage.Input, a.usage.Output, a.usage.CacheRead),
		`{"type":"message_stop"}`,
	}
}

// --- planning-leak filter (consumePlanningBuffer) ---------------------------

type planningKind int

const (
	planningIncomplete planningKind = iota
	planningPlain
	planningLeak
)

type planningResult struct {
	kind    planningKind
	visible string
}

// trimStartJS is String.prototype.trimStart (ECMAScript whitespace).
func trimStartJS(text string) string {
	return strings.TrimLeftFunc(text, isJSSpace)
}

func isJSSpace(r rune) bool { return unicode.IsSpace(r) || r == '\uFEFF' }

func utf16Len(text string) int {
	n := 0
	for _, r := range text {
		n += len(utf16.Encode([]rune{r}))
	}
	return n
}

func isPlanningLeakPrefix(text string) bool {
	trimmed := trimStartJS(text)
	if !strings.HasPrefix(trimmed, "{") {
		return false
	}
	afterBrace := trimStartJS(trimmed[1:])
	short := utf16Len(trimmed) <= 100
	if afterBrace == "" {
		return short
	}
	if afterBrace[0] != '"' {
		return false
	}
	next := strings.IndexByte(afterBrace[1:], '"')
	if next == -1 {
		return strings.HasPrefix("thought", afterBrace[1:]) && short
	}
	if afterBrace[1:1+next] != "thought" {
		return false
	}
	afterKey := trimStartJS(afterBrace[1+next+1:])
	if afterKey == "" {
		return short
	}
	return afterKey[0] == ':'
}

type leadingObject struct {
	json string
	rest string
}

func splitLeadingJSONObject(text string, honorQuotes bool) (leadingObject, bool) {
	trimmed := trimStartJS(text)
	if !strings.HasPrefix(trimmed, "{") {
		return leadingObject{}, false
	}
	depth := 0
	inString, escaped := false, false
	for index := 0; index < len(trimmed); index++ {
		ch := trimmed[index]
		if honorQuotes {
			if inString {
				switch {
				case escaped:
					escaped = false
				case ch == '\\':
					escaped = true
				case ch == '"':
					inString = false
				}
				continue
			}
			if ch == '"' {
				inString = true
				continue
			}
		}
		switch ch {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return leadingObject{json: trimmed[:index+1], rest: trimmed[index+1:]}, true
			}
		}
	}
	return leadingObject{}, false
}

func containsToolKey(text string, toolNames map[string]bool) bool {
	for name := range toolNames {
		if strings.Contains(text, `"`+name+`"`) {
			return true
		}
	}
	return false
}

func hasLeakSignature(text string) bool {
	return strings.Contains(text, `"_i"`) || strings.Contains(text, `"paths"`) || strings.Contains(text, `"command"`) ||
		(strings.Contains(text, `"path"`) && strings.Contains(text, `"content"`))
}

func consumePlanningBuffer(text string, toolNames map[string]bool, final bool) planningResult {
	if !isPlanningLeakPrefix(text) {
		return planningResult{planningPlain, text}
	}
	leading, ok := splitLeadingJSONObject(text, true)
	if !ok {
		leading, ok = splitLeadingJSONObject(text, false)
	}
	if !ok {
		if final {
			trimmed := strings.TrimFunc(text, isJSSpace)
			if strings.Contains(trimmed, `"thought"`) || containsToolKey(trimmed, toolNames) || hasLeakSignature(trimmed) {
				return planningResult{planningLeak, ""}
			}
			return planningResult{planningPlain, text}
		}
		return planningResult{kind: planningIncomplete}
	}
	var parsed any
	if err := json.Unmarshal([]byte(leading.json), &parsed); err != nil {
		if strings.Contains(leading.json, `"thought"`) || containsToolKey(leading.json, toolNames) || hasLeakSignature(leading.json) {
			return planningResult{planningLeak, leading.rest}
		}
		return planningResult{planningPlain, text}
	}
	if isPlanningLeakObject(parsed, toolNames) {
		return planningResult{planningLeak, leading.rest}
	}
	return planningResult{planningPlain, text}
}

func isPlanningLeakObject(parsed any, toolNames map[string]bool) bool {
	record, ok := parsed.(map[string]any)
	if !ok {
		return false
	}
	_, hasThought := record["thought"].(string)
	call, _ := record["call"].(string)
	isTool := call != "" && toolNames[call]
	_, hasI := record["_i"]
	_, hasPaths := record["paths"]
	_, hasCommand := record["command"]
	_, hasPath := record["path"]
	_, hasContent := record["content"]
	return hasThought || isTool || hasI || hasPaths || hasCommand || (hasPath && hasContent)
}
