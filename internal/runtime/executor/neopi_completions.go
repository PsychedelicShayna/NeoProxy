package executor

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/codexwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/tidwall/gjson"
)

// NeoPi's OpenAI Chat Completions request builder (ai/src/providers/
// openai-completions.ts buildParams over convertMessages and convertTools),
// driven by the compat NeoPi resolves for the model: for a bundled row the
// catalog snapshot in internal/registry, for a discovered id the compat NeoPi
// derives from its rules for that provider.
const (
	// neoPiCompletionsMaxOutputTokens is OPENAI_MAX_OUTPUT_TOKENS, the ceiling
	// for hosts that advertise no output cap of their own.
	neoPiCompletionsMaxOutputTokens = 64000
	// neoPiOpenAIToolCallIDMaxLength is OPENAI_TOOL_CALL_ID_MAX_LENGTH.
	neoPiOpenAIToolCallIDMaxLength = 40
	// neoPiAssistantAfterToolResult bridges a user turn that lands right after
	// tool results on hosts that reject it.
	neoPiAssistantAfterToolResult = "I have processed the tool results."
	// neoPiMissingToolResult is the synthetic result NeoPi files for a call
	// whose result never landed.
	neoPiMissingToolResult = "No result provided"
	// neoPiAssistantContentPlaceholder fills an assistant turn that would
	// otherwise carry no content beside its tool calls.
	neoPiAssistantContentPlaceholder = "."
	// neoPiSyntheticReasoningPlaceholder is what hosts that accept a
	// placeholder get for a tool-call turn with no reasoning.
	neoPiSyntheticReasoningPlaceholder = "."
)

// neoPiCompletionsCall is the request-side state NeoPi's stream options carry
// that the downstream body does not.
type neoPiCompletionsCall struct {
	// SessionID is NeoPi's options.sessionId: the conversation id behind a
	// session header and the prompt cache key.
	SessionID string
	// OpenRouterVariant is the routing suffix for wireModelIdMode "openrouter".
	OpenRouterVariant string
}

// neoPiCompletionTool is one downstream function tool.
type neoPiCompletionTool struct {
	name        string
	description string
	parameters  gjson.Result
	// strict and hasStrict are the caller's explicit strict flag.
	strict    bool
	hasStrict bool
}

// neoPiCompletionCall is one assistant tool call.
type neoPiCompletionCall struct {
	id        string
	name      string
	arguments string
}

// neoPiCompletionsOptions is the caller side of NeoPi's
// OpenAICompletionsOptions, read from the downstream body.
type neoPiCompletionsOptions struct {
	reasoning         string
	disableReasoning  bool
	temperature       *float64
	topP              *float64
	topK              *float64
	minP              *float64
	presencePenalty   *float64
	repetitionPenalty *float64
	frequencyPenalty  *float64
	maxTokens         *float64
	maxTokensExplicit bool
	stop              []string
	toolChoice        json.RawMessage
}

// neoPiCompletionsReasoning is resolveOpenAICompatPolicy's verdict for the
// chat-completions endpoint.
type neoPiCompletionsReasoning struct {
	enabled     bool
	disabled    bool
	disableMode string
	wireEffort  string
	// omitEffort strips the effort field for hosts that read no dial.
	omitEffort bool
}

// neoPiCompletionsRequest is NeoPi's buildParams for one openai-completions
// row: the body NeoPi posts upstream, serialized the way its HTTP client does.
func neoPiCompletionsRequest(input []byte, row *registry.NeoPiModel, call neoPiCompletionsCall) ([]byte, error) {
	if !gjson.ValidBytes(input) {
		return nil, fmt.Errorf("invalid Chat Completions JSON")
	}
	if row == nil || row.CompletionsCompat == nil {
		return nil, fmt.Errorf("model has no NeoPi completions compat")
	}
	root := gjson.ParseBytes(input)
	options := neoPiCompletionsOptionsFrom(root)
	neoPiNormalizeMandatoryReasoning(row, &options)
	if options.maxTokens == nil && row.MaxTokens > 0 {
		limit := float64(row.MaxTokens)
		options.maxTokens = &limit
	}
	// A host that cannot express a choice never receives one, so it is absent
	// from the reasoning decision as well.
	if !row.CompletionsCompat.SupportsToolChoice {
		options.toolChoice = nil
	}

	base := row.CompletionsCompat
	compat := base
	body := neoPiOrderedObject{}
	body.set("model", jsjson.Marshal(neoPiCompletionsWireModelID(row, options, call)))
	body.set("messages", []byte(`[]`))
	body.set("stream", jsjson.Marshal(true))
	if compat.SupportsUsageInStreaming {
		body.set("stream_options", []byte(`{"include_usage":true}`))
	}
	if compat.SupportsStore {
		body.set("store", jsjson.Marshal(false))
	}
	if compat.SupportsSamplingParams {
		neoPiSetNumber(&body, "temperature", options.temperature)
		neoPiSetNumber(&body, "top_p", options.topP)
		neoPiSetNumber(&body, "top_k", options.topK)
		neoPiSetNumber(&body, "min_p", options.minP)
		if compat.SupportsPenaltyAndStopParams {
			neoPiSetNumber(&body, "presence_penalty", options.presencePenalty)
			neoPiSetNumber(&body, "repetition_penalty", options.repetitionPenalty)
			neoPiSetNumber(&body, "frequency_penalty", options.frequencyPenalty)
		}
	}
	if len(options.stop) > 0 && compat.SupportsPenaltyAndStopParams {
		if len(options.stop) == 1 {
			body.set("stop", jsjson.Marshal(options.stop[0]))
		} else {
			body.set("stop", jsjson.Marshal(options.stop[:min(len(options.stop), 4)]))
		}
	}

	toolNames := neoPiCompletionsTools(&body, root, compat)
	choice := neoPiCompletionsToolChoice(&body, row, options, compat, toolNames)
	neoPiCompletionsOutputTokens(&body, row, compat, options)
	if neoPiIsZaiReasoningEffortDialect(compat) && compat.SupportsReasoningEffort && len(toolNames) > 0 {
		body.set("tool_stream", jsjson.Marshal(true))
	}
	compat, reasoning := neoPiResolveCompletionsReasoning(row, compat, options, choice)
	neoPiApplyCompletionsReasoning(&body, compat, reasoning)
	neoPiApplyGatewayRouting(&body, compat)
	neoPiApplyExtraBody(&body, compat)
	if compat.SupportsPromptCacheKey {
		if key := codexwire.NormalizePromptCacheKey(call.SessionID); key != "" {
			body.set("prompt_cache_key", jsjson.Marshal(key))
		}
	}

	messages, err := neoPiCompletionsMessages(root, row, compat)
	if err != nil {
		return nil, err
	}
	body.set("messages", messages)
	return body.bytes(), nil
}

// neoPiCompletionsOptionsFrom reads the caller options a Chat Completions
// client expresses on the wire.
func neoPiCompletionsOptionsFrom(root gjson.Result) neoPiCompletionsOptions {
	options := neoPiCompletionsOptions{
		temperature:       neoPiOptionalNumber(root.Get("temperature")),
		topP:              neoPiOptionalNumber(root.Get("top_p")),
		topK:              neoPiOptionalNumber(root.Get("top_k")),
		minP:              neoPiOptionalNumber(root.Get("min_p")),
		presencePenalty:   neoPiOptionalNumber(root.Get("presence_penalty")),
		repetitionPenalty: neoPiOptionalNumber(root.Get("repetition_penalty")),
		frequencyPenalty:  neoPiOptionalNumber(root.Get("frequency_penalty")),
	}
	if effort := root.Get("reasoning_effort"); effort.Type == gjson.String {
		value := strings.TrimSpace(effort.String())
		switch strings.ToLower(value) {
		case "", "off", "none", "disabled":
			options.disableReasoning = value != ""
		default:
			options.reasoning = value
		}
	} else if root.Get("reasoning.effort").Type == gjson.String {
		options.reasoning = strings.TrimSpace(root.Get("reasoning.effort").String())
	}
	// stream.ts defaults the caller's cap to the model's, so a client that
	// sends none still posts NeoPi's clamped model cap; only an explicit cap
	// routes OpenRouter-style omission.
	for _, name := range []string{"max_completion_tokens", "max_tokens"} {
		if value := neoPiOptionalNumber(root.Get(name)); value != nil && *value > 0 {
			options.maxTokens = value
			options.maxTokensExplicit = true
			break
		}
	}
	if stop := root.Get("stop"); stop.Exists() && stop.Type != gjson.Null {
		if stop.IsArray() {
			for _, entry := range stop.Array() {
				if entry.Type == gjson.String {
					options.stop = append(options.stop, entry.String())
				}
			}
		} else if stop.Type == gjson.String {
			options.stop = []string{stop.String()}
		}
	}
	if choice := root.Get("tool_choice"); choice.Exists() && choice.Type != gjson.Null {
		switch {
		case choice.Type == gjson.String:
			switch choice.String() {
			case "auto", "none", "required":
				options.toolChoice = jsjson.Marshal(choice.String())
			}
		case choice.IsObject():
			if name := choice.Get("function.name").String(); name != "" {
				options.toolChoice = jsjson.Object(
					jsjson.Field{Name: "type", Value: jsjson.Marshal("function")},
					jsjson.Field{Name: "function", Value: jsjson.Object(jsjson.Field{Name: "name", Value: jsjson.Marshal(name)})},
				)
			}
		}
	}
	return options
}

func neoPiOptionalNumber(value gjson.Result) *float64 {
	if !value.Exists() || value.Type != gjson.Number {
		return nil
	}
	number := value.Float()
	return &number
}

func neoPiSetNumber(body *neoPiOrderedObject, name string, value *float64) {
	if value == nil {
		return
	}
	body.set(name, []byte(jsjson.Number(*value)))
}

// neoPiResolveCompletionsReasoning is NeoPi's resolveOpenAICompatPolicy for the
// chat-completions endpoint: which compat view applies (the row's whenThinking
// view once thinking is engaged), whether thinking reaches the wire, and at
// which effort.
func neoPiResolveCompletionsReasoning(row *registry.NeoPiModel, base *registry.NeoPiCompletionsCompat, options neoPiCompletionsOptions, emitted json.RawMessage) (*registry.NeoPiCompletionsCompat, neoPiCompletionsReasoning) {
	modelSupported := row.Reasoning
	forced := neoPiIsForcedToolChoice(emitted)
	anyChoice := len(emitted) > 0
	suppressed := ""
	switch {
	case base.DisableReasoningOnForcedToolChoice && base.SupportsForcedToolChoice && forced:
		suppressed = "forced-tool-choice"
	case base.DisableReasoningOnToolChoice && anyChoice:
		suppressed = "tool-choice"
	}
	requested := strings.TrimSpace(options.reasoning)
	requestedAndAllowed := requested != "" && !options.disableReasoning && modelSupported
	disableReason := ""
	if options.disableReasoning {
		disableReason = "caller"
	} else if suppressed != "" {
		disableReason = suppressed
	}
	enabled := requestedAndAllowed && disableReason == ""
	if enabled && base.ReasoningDisableMode == "zai-thinking-disabled" {
		if neoPiMapReasoningEffort(row, base, neoPiNormalizeReasoningEffort(row, base, requested)) == "none" {
			enabled = false
		}
	}
	compat := base
	if enabled && base.WhenThinking != nil {
		compat = base.WhenThinking
	}
	omit := compat.OmitReasoningEffort || !compat.SupportsReasoningEffort
	wireEffort := ""
	if enabled {
		wireEffort = neoPiMapReasoningEffort(row, compat, neoPiNormalizeReasoningEffort(row, compat, requested))
	}
	disabledWithoutRequest := modelSupported && requested == "" && !options.disableReasoning && neoPiImplicitDisableWhenNotRequested(compat.ReasoningDisableMode)
	disabled := (modelSupported && disableReason == "caller") || suppressed != "" || disabledWithoutRequest
	if disabled && compat.SupportsReasoningEffort && !omit {
		switch {
		case compat.ReasoningDisableMode == "none-effort":
			wireEffort = "none"
		case modelSupported && disableReason == "caller" && requested == "" && compat.ReasoningDisableMode == "lowest-effort":
			if floor := neoPiMinimumEffort(row); floor != "" {
				wireEffort = neoPiMapReasoningEffort(row, compat, floor)
			}
		}
	}
	return compat, neoPiCompletionsReasoning{
		enabled:     enabled,
		disabled:    disabled,
		disableMode: compat.ReasoningDisableMode,
		wireEffort:  wireEffort,
		omitEffort:  omit,
	}
}

// neoPiNormalizeMandatoryReasoning is normalizeMandatoryReasoningOptions:
// endpoints that always think reject an off or effort-less request, so it is
// clamped to the model's floor effort instead.
func neoPiNormalizeMandatoryReasoning(row *registry.NeoPiModel, options *neoPiCompletionsOptions) {
	if !row.Reasoning || row.Thinking == nil || !row.Thinking.RequiresEffort || row.Thinking.SuppressWhenOff {
		return
	}
	if options.reasoning != "" && !options.disableReasoning {
		return
	}
	floor := neoPiDefaultSupportedEffort(row)
	if floor == "" {
		return
	}
	options.reasoning, options.disableReasoning = floor, false
}

// neoPiDefaultSupportedEffort is defaultSupportedEffort: the floor effort,
// preferring the tier the model's default wire id already serves.
func neoPiDefaultSupportedEffort(row *registry.NeoPiModel) string {
	routing := row.Thinking.EffortRouting
	if routing != nil && row.RequestModelID != "" {
		for _, effort := range neoPiEffortOrder {
			if neoPiOffersEffort(row, effort) && routing[effort] == row.RequestModelID {
				return effort
			}
		}
	}
	return neoPiMinimumEffort(row)
}

// neoPiShouldDropAutoToolChoice is shouldDropAutoToolChoiceForReasoning.
func neoPiShouldDropAutoToolChoice(options neoPiCompletionsOptions, compat *registry.NeoPiCompletionsCompat) bool {
	return compat.DisableReasoningOnToolChoice && options.reasoning != "" && !options.disableReasoning
}

func neoPiImplicitDisableWhenNotRequested(mode string) bool {
	switch mode {
	case "zai-thinking-disabled", "qwen-enable-thinking-false", "qwen-template-false", "chat-template-thinking-false":
		return true
	}
	return false
}

// neoPiMinimumEffort is minimumSupportedEffort: the lowest ladder level.
func neoPiMinimumEffort(row *registry.NeoPiModel) string {
	if row.Thinking == nil || len(row.Thinking.Efforts) == 0 {
		return ""
	}
	for _, effort := range neoPiEffortOrder {
		for _, offered := range row.Thinking.Efforts {
			if offered == effort {
				return effort
			}
		}
	}
	return row.Thinking.Efforts[0]
}

// neoPiMapReasoningEffort is mapOpenAIReasoningEffort: the compat override
// first, then the model's own effort map, else the level itself.
func neoPiMapReasoningEffort(row *registry.NeoPiModel, compat *registry.NeoPiCompletionsCompat, effort string) string {
	if mapped := compat.ReasoningEffortMap[effort]; mapped != "" {
		return mapped
	}
	if row.Thinking != nil {
		if mapped := row.Thinking.EffortMap[effort]; mapped != "" {
			return mapped
		}
	}
	return effort
}

// neoPiNormalizeReasoningEffort is stream.ts resolveOpenAiReasoningEffort: a
// requested level the model does not offer is refused before dispatch (NeoPi
// throws), so NeoProxy clamps to the nearest offered level and keeps the
// request usable.
func neoPiNormalizeReasoningEffort(row *registry.NeoPiModel, compat *registry.NeoPiCompletionsCompat, effort string) string {
	if effort == "" || !row.Reasoning || row.Thinking == nil {
		return ""
	}
	if neoPiOffersEffort(row, effort) {
		return effort
	}
	if mapped := compat.ReasoningEffortMap[effort]; mapped != "" && neoPiOffersEffort(row, mapped) {
		return mapped
	}
	if row.Thinking.EffortMap[effort] != "" {
		return effort
	}
	return codexwire.ClampEffort(row.Thinking.Efforts, effort)
}

func neoPiOffersEffort(row *registry.NeoPiModel, effort string) bool {
	if row.Thinking == nil {
		return false
	}
	for _, offered := range row.Thinking.Efforts {
		if offered == effort {
			return true
		}
	}
	return false
}

// neoPiApplyCompletionsReasoning is applyChatCompletionsCompatPolicy: the
// dialect's thinking and effort fields for the resolved verdict.
func neoPiApplyCompletionsReasoning(body *neoPiOrderedObject, compat *registry.NeoPiCompletionsCompat, reasoning neoPiCompletionsReasoning) {
	if compat.QwenPreserveThinking {
		if compat.ThinkingFormat == "qwen" {
			body.set("preserve_thinking", jsjson.Marshal(true))
		}
		body.setNested("chat_template_kwargs", "preserve_thinking", jsjson.Marshal(true))
	}
	if !reasoning.enabled {
		if !reasoning.disabled {
			return
		}
		// Hosts whose off value is a level (or "none") still take the field.
		if reasoning.disableMode == "lowest-effort" || reasoning.disableMode == "none-effort" {
			if reasoning.wireEffort != "" {
				body.set("reasoning_effort", jsjson.Marshal(reasoning.wireEffort))
				return
			}
		}
		neoPiEncodeDisabledReasoning(body, reasoning.disableMode)
		return
	}
	switch reasoning.disableMode {
	case "zai-thinking-disabled":
		if reasoning.wireEffort == "none" {
			neoPiEncodeDisabledReasoning(body, reasoning.disableMode)
			return
		}
		body.set("thinking", jsjson.Object(jsjson.Field{Name: "type", Value: jsjson.Marshal("enabled")}))
		if compat.ThinkingKeep != "" {
			body.setNested("thinking", "keep", jsjson.Marshal(compat.ThinkingKeep))
		}
		if compat.SupportsReasoningEffort && reasoning.wireEffort != "" {
			body.set("reasoning_effort", jsjson.Marshal(reasoning.wireEffort))
		}
	case "qwen-enable-thinking-false":
		body.set("enable_thinking", jsjson.Marshal(true))
		if compat.QwenTemplateReasoningEffort && reasoning.wireEffort != "" {
			body.set("reasoning_effort", jsjson.Marshal(reasoning.wireEffort))
			body.setNested("chat_template_kwargs", "reasoning_effort", jsjson.Marshal(reasoning.wireEffort))
		}
	case "qwen-template-false":
		body.setNested("chat_template_kwargs", "enable_thinking", jsjson.Marshal(true))
		if compat.QwenTemplateReasoningEffort && reasoning.wireEffort != "" {
			body.setNested("chat_template_kwargs", "reasoning_effort", jsjson.Marshal(reasoning.wireEffort))
		}
	case "chat-template-thinking-false":
		body.setNested("chat_template_kwargs", "thinking", jsjson.Marshal(true))
		if reasoning.wireEffort != "" {
			body.setNested("chat_template_kwargs", "reasoning_effort", jsjson.Marshal(reasoning.wireEffort))
		}
	case "openrouter-enabled-false", "cline-enabled-false":
		if reasoning.wireEffort != "" {
			body.set("reasoning", jsjson.Object(jsjson.Field{Name: "effort", Value: jsjson.Marshal(reasoning.wireEffort)}))
		}
	default:
		if !reasoning.omitEffort && reasoning.wireEffort != "" {
			body.set("reasoning_effort", jsjson.Marshal(reasoning.wireEffort))
		}
	}
}

// neoPiEncodeDisabledReasoning is encodeChatCompletionsDisabledReasoning.
func neoPiEncodeDisabledReasoning(body *neoPiOrderedObject, mode string) {
	body.del("reasoning_effort")
	switch mode {
	case "none-effort":
		body.set("reasoning_effort", jsjson.Marshal("none"))
	case "zai-thinking-disabled":
		body.set("thinking", []byte(`{"type":"disabled"}`))
	case "qwen-enable-thinking-false":
		body.set("enable_thinking", jsjson.Marshal(false))
	case "qwen-template-false":
		body.setNested("chat_template_kwargs", "enable_thinking", jsjson.Marshal(false))
	case "chat-template-thinking-false":
		body.setNested("chat_template_kwargs", "thinking", jsjson.Marshal(false))
	case "openrouter-enabled-false", "cline-enabled-false":
		body.set("reasoning", []byte(`{"enabled":false}`))
	default:
		body.del("reasoning")
	}
}

// neoPiCompletionsWireModelID is resolveOpenAICompletionsModelId: the row's
// wire id for the request's effort tier, then its provider id transform.
func neoPiCompletionsWireModelID(row *registry.NeoPiModel, options neoPiCompletionsOptions, call neoPiCompletionsCall) string {
	requested := ""
	if options.reasoning != "" && !options.disableReasoning && row.Reasoning {
		requested = neoPiNormalizeReasoningEffort(row, row.CompletionsCompat, options.reasoning)
	}
	wireID := row.ID
	if row.Thinking != nil && len(row.Thinking.EffortRouting) > 0 {
		key := requested
		if key == "" {
			key = "off"
		}
		if routed := row.Thinking.EffortRouting[key]; routed != "" {
			wireID = routed
		}
	} else if row.RequestModelID != "" {
		wireID = row.RequestModelID
	}
	switch row.CompletionsCompat.WireModelIDMode {
	case "fireworks":
		return neoPiFireworksWireModelID(wireID)
	case "firepass":
		return neoPiFirepassWireModelID(wireID)
	case "openrouter":
		return neoPiOpenRouterRoutingVariant(wireID, call.OpenRouterVariant)
	}
	return wireID
}

const (
	neoPiFireworksWirePrefix = "accounts/fireworks/models/"
	neoPiFirepassWirePrefix  = "accounts/fireworks/routers/"
)

// neoPiFireworksWireModelID is toFireworksWireModelId: the public id on the
// models namespace, with version dots spelled `p`.
func neoPiFireworksWireModelID(modelID string) string {
	stripped := strings.TrimPrefix(modelID, neoPiFireworksWirePrefix)
	return neoPiFireworksWirePrefix + neoPiDotVersions(stripped)
}

// neoPiFirepassWireModelID is toFirepassWireModelId: Fast serving-path ids
// live under the routers namespace.
func neoPiFirepassWireModelID(modelID string) string {
	stripped := strings.TrimPrefix(modelID, neoPiFirepassWirePrefix)
	return neoPiFirepassWirePrefix + neoPiDotVersions(stripped)
}

// neoPiDotVersions is the VERSION_DOT_PATTERN replacement: a dot between two
// digits becomes `p`.
func neoPiDotVersions(value string) string {
	var out strings.Builder
	out.Grow(len(value))
	for i := 0; i < len(value); i++ {
		if value[i] == '.' && i > 0 && i+1 < len(value) && neoPiIsDigit(value[i-1]) && neoPiIsDigit(value[i+1]) {
			out.WriteByte('p')
			continue
		}
		out.WriteByte(value[i])
	}
	return out.String()
}

func neoPiIsDigit(value byte) bool { return value >= '0' && value <= '9' }

// neoPiOpenRouterRoutingVariant is applyOpenRouterRoutingVariant.
func neoPiOpenRouterRoutingVariant(modelID, variant string) string {
	if variant == "" || strings.LastIndex(modelID, ":") > strings.LastIndex(modelID, "/") {
		return modelID
	}
	return modelID + ":" + variant
}

// neoPiCompletionsOutputTokens is resolveOpenAIOutputTokenParam.
func neoPiCompletionsOutputTokens(body *neoPiOrderedObject, row *registry.NeoPiModel, compat *registry.NeoPiCompletionsCompat, options neoPiCompletionsOptions) {
	requested := 0.0
	hasRequested := false
	if options.maxTokens != nil {
		requested, hasRequested = *options.maxTokens, true
	} else if compat.AlwaysSendMaxTokens {
		hasRequested = true
		requested = float64(row.MaxTokens)
		if row.MaxTokens == 0 {
			requested = neoPiCompletionsMaxOutputTokens
		}
	}
	if !hasRequested {
		return
	}
	if compat.IsOpenRouterHost && !compat.AlwaysSendMaxTokens && !options.maxTokensExplicit {
		return
	}
	limit := requested
	if row.MaxTokens > 0 && float64(row.MaxTokens) < limit {
		limit = float64(row.MaxTokens)
	}
	clamp := neoPiCompletionsOutputClamp(row, compat)
	if clamp > 0 && clamp < limit {
		limit = clamp
	}
	if limit <= 0 {
		return
	}
	field := compat.MaxTokensField
	if field == "" {
		field = "max_tokens"
	}
	body.set(field, []byte(jsjson.Number(limit)))
}

// neoPiCompletionsOutputClamp is resolveOpenAICompletionsOutputClamp: the
// provider's output ceiling, else NeoPi's generic one.
func neoPiCompletionsOutputClamp(row *registry.NeoPiModel, compat *registry.NeoPiCompletionsCompat) float64 {
	if neoPiIsZaiReasoningEffortDialect(compat) || compat.ClampOutputToModelMax {
		if row.MaxTokens > 0 {
			return float64(row.MaxTokens)
		}
	}
	return neoPiCompletionsMaxOutputTokens
}

// neoPiIsZaiReasoningEffortDialect is isZaiReasoningEffortDialect.
func neoPiIsZaiReasoningEffortDialect(compat *registry.NeoPiCompletionsCompat) bool {
	return compat != nil && compat.ThinkingFormat == "zai" && compat.ZaiReasoningEffortDialect
}

// neoPiCompletionsTools is convertTools: every offered tool in NeoPi's wire
// shape, with the strict mode its compat asks for. It returns the names that
// survived, which tool_choice validates against.
func neoPiCompletionsTools(body *neoPiOrderedObject, root gjson.Result, compat *registry.NeoPiCompletionsCompat) []string {
	tools, offered := neoPiParseTools(root)
	if len(tools) == 0 {
		if !offered && neoPiHistoryHasToolCalls(root.Get("messages")) {
			// Some hosts require `tools` whenever the history carries a call.
			body.set("tools", []byte(`[]`))
		}
		return nil
	}
	type adapted struct {
		tool       neoPiCompletionTool
		base       json.RawMessage
		parameters json.RawMessage
		strict     bool
	}
	prepared := make([]adapted, 0, len(tools))
	allStrict := true
	for _, tool := range tools {
		strict := compat.SupportsStrictMode && !(tool.hasStrict && !tool.strict)
		parameters, effective := neoPiCompletionsToolSchema(tool.parameters, strict, compat)
		if !effective {
			allStrict = false
		}
		prepared = append(prepared, adapted{tool: tool, base: codexwire.ToolWireSchema(tool.parameters), parameters: parameters, strict: effective})
	}
	resolved := "mixed"
	switch compat.ToolStrictMode {
	case "none":
		resolved = "none"
	case "all_strict":
		resolved = "none"
		if allStrict {
			resolved = "all_strict"
		}
	}
	wire := make([]json.RawMessage, 0, len(prepared))
	names := make([]string, 0, len(prepared))
	for _, entry := range prepared {
		includeStrict := resolved == "all_strict" || (resolved == "mixed" && entry.strict)
		includeExplicitFalse := !includeStrict && entry.tool.hasStrict && !entry.tool.strict && resolved == "mixed" && compat.SupportsStrictMode
		parameters := entry.base
		if includeStrict {
			parameters = entry.parameters
		}
		if compat.ToolSchemaFlavor == "moonshot-mfjs" {
			parameters = codexwire.NormalizeSchemaForMoonshot(parameters)
		}
		function := []jsjson.Field{
			{Name: "name", Value: jsjson.Marshal(entry.tool.name)},
			{Name: "description", Value: jsjson.Marshal(entry.tool.description)},
			{Name: "parameters", Value: parameters},
		}
		if includeStrict || includeExplicitFalse {
			function = append(function, jsjson.Field{Name: "strict", Value: jsjson.Marshal(includeStrict)})
		}
		wire = append(wire, jsjson.Object(
			jsjson.Field{Name: "type", Value: jsjson.Marshal("function")},
			jsjson.Field{Name: "function", Value: jsjson.Object(function...)},
		))
		names = append(names, entry.tool.name)
	}
	body.set("tools", jsjson.Marshal(wire))
	return names
}

// neoPiParseTools reads the downstream tools array; offered reports whether
// the client sent a tools key at all.
func neoPiParseTools(root gjson.Result) ([]neoPiCompletionTool, bool) {
	tools := root.Get("tools")
	if !tools.Exists() || tools.Type == gjson.Null {
		return nil, false
	}
	if !tools.IsArray() {
		return nil, true
	}
	out := make([]neoPiCompletionTool, 0, len(tools.Array()))
	for _, entry := range tools.Array() {
		function := entry.Get("function")
		name := function.Get("name").String()
		if name == "" {
			continue
		}
		strictFlag := function.Get("strict")
		if !strictFlag.Exists() {
			strictFlag = entry.Get("strict")
		}
		out = append(out, neoPiCompletionTool{
			name:        name,
			description: function.Get("description").String(),
			parameters:  function.Get("parameters"),
			strict:      strictFlag.Type == gjson.True,
			hasStrict:   strictFlag.Exists() && strictFlag.Type != gjson.Null,
		})
	}
	return out, true
}

// neoPiHistoryHasToolCalls reports whether the transcript already replays a
// tool call.
func neoPiHistoryHasToolCalls(messages gjson.Result) bool {
	if !messages.IsArray() {
		return false
	}
	for _, message := range messages.Array() {
		switch message.Get("role").String() {
		case "tool":
			return true
		case "assistant":
			if calls := message.Get("tool_calls"); calls.IsArray() && len(calls.Array()) > 0 {
				return true
			}
		}
	}
	return false
}

// neoPiCompletionsToolChoice is buildParams' tool_choice handling: the choice
// as the host can express it, downgraded or dropped the way NeoPi repairs it.
func neoPiCompletionsToolChoice(body *neoPiOrderedObject, row *registry.NeoPiModel, options neoPiCompletionsOptions, compat *registry.NeoPiCompletionsCompat, toolNames []string) json.RawMessage {
	choice := options.toolChoice
	if len(choice) == 0 {
		return nil
	}
	parsed := gjson.ParseBytes(choice)
	raw := choice
	forced := neoPiIsForcedToolChoice(raw)
	named := parsed.Get("function.name").String()
	if parsed.IsObject() && !compat.SupportsNamedToolChoice {
		// String-only hosts degrade a named choice to "required"; narrowing the
		// offered tools keeps it pinned to the requested function.
		if named != "" {
			tools := gjson.GetBytes(body.bytes(), "tools")
			narrowed := make([]json.RawMessage, 0, 1)
			for _, tool := range tools.Array() {
				if tool.Get("function.name").String() == named {
					narrowed = append(narrowed, jsjson.Normalize([]byte(tool.Raw)))
				}
			}
			if len(narrowed) > 0 {
				body.set("tools", jsjson.Marshal(narrowed))
			}
		}
		raw = jsjson.Marshal("required")
		forced = true
		named = ""
	}
	if forced && !compat.SupportsForcedToolChoice {
		raw = jsjson.Marshal("auto")
	}
	if named != "" && compat.NativeKimiK3Reasoning && row.Reasoning && options.reasoning != "" && !options.disableReasoning && containsString(toolNames, named) {
		// Native K3 reasoning cannot name a function; keep the hard tool-use
		// contract and let the model choose among the offered tools.
		raw = jsjson.Marshal("required")
		named = ""
	}
	if len(toolNames) == 0 && (parsed.String() == "none" || forced) {
		return nil
	}
	if named != "" && !containsString(toolNames, named) {
		return nil
	}
	// A redundant `auto` is dropped so reasoning survives on hosts that switch
	// thinking off whenever a choice is present.
	if string(raw) == `"auto"` && row.Reasoning && neoPiShouldDropAutoToolChoice(options, compat) {
		return nil
	}
	body.set("tool_choice", raw)
	return raw
}

// neoPiIsForcedToolChoice is isForcedToolChoice.
func neoPiIsForcedToolChoice(choice json.RawMessage) bool {
	if len(choice) == 0 {
		return false
	}
	parsed := gjson.ParseBytes(choice)
	if parsed.Type == gjson.String {
		switch parsed.String() {
		case "auto", "none":
			return false
		}
	}
	return true
}

// neoPiApplyGatewayRouting is applyOpenAIGatewayRouting: OpenRouter routes on
// the top-level `provider`, the Vercel AI Gateway on `providerOptions.gateway`.
func neoPiApplyGatewayRouting(body *neoPiOrderedObject, compat *registry.NeoPiCompletionsCompat) {
	if compat.IsOpenRouterHost && compat.OpenRouterRouting != nil {
		fields := make([]jsjson.Field, 0, 2)
		if len(compat.OpenRouterRouting.Only) > 0 {
			fields = append(fields, jsjson.Field{Name: "only", Value: jsjson.Marshal(compat.OpenRouterRouting.Only)})
		}
		if len(compat.OpenRouterRouting.Order) > 0 {
			fields = append(fields, jsjson.Field{Name: "order", Value: jsjson.Marshal(compat.OpenRouterRouting.Order)})
		}
		if len(fields) > 0 {
			body.set("provider", jsjson.Object(fields...))
		}
	}
	routing := compat.VercelGatewayRouting
	if !compat.IsVercelGatewayHost || routing == nil {
		return
	}
	gateway := make([]jsjson.Field, 0, 3)
	if len(routing.Only) > 0 {
		gateway = append(gateway, jsjson.Field{Name: "only", Value: jsjson.Marshal(routing.Only)})
	}
	if len(routing.Order) > 0 {
		gateway = append(gateway, jsjson.Field{Name: "order", Value: jsjson.Marshal(routing.Order)})
	}
	if routing.Caching != "" {
		gateway = append(gateway, jsjson.Field{Name: "caching", Value: jsjson.Marshal(routing.Caching)})
	}
	if len(gateway) > 0 {
		body.set("providerOptions", jsjson.Object(jsjson.Field{Name: "gateway", Value: jsjson.Object(gateway...)}))
	}
}

// neoPiApplyExtraBody is applyOpenAIExtraBody.
func neoPiApplyExtraBody(body *neoPiOrderedObject, compat *registry.NeoPiCompletionsCompat) {
	if len(compat.ExtraBody) > 0 {
		body.merge(compat.ExtraBody)
	}
	if compat.DropThinkingWhenReasoningEffort && body.has("reasoning_effort") {
		body.del("thinking")
	}
}

// neoPiOrderedObject mirrors a JavaScript object: assigning an existing key
// keeps its position, a new key is appended, and deletion removes it.
type neoPiOrderedObject []jsjson.Field

func (o neoPiOrderedObject) index(name string) int {
	for i := range o {
		if o[i].Name == name {
			return i
		}
	}
	return -1
}

func (o neoPiOrderedObject) has(name string) bool { return o.index(name) >= 0 }

func (o *neoPiOrderedObject) set(name string, value json.RawMessage) {
	if i := o.index(name); i >= 0 {
		(*o)[i].Value = value
		return
	}
	*o = append(*o, jsjson.Field{Name: name, Value: value})
}

// setNested assigns a key inside a nested object, the way a JavaScript spread
// over the existing value does.
func (o *neoPiOrderedObject) setNested(parent, name string, value json.RawMessage) {
	nested := neoPiOrderedObject{}
	if i := o.index(parent); i >= 0 {
		gjson.ParseBytes((*o)[i].Value).ForEach(func(key, item gjson.Result) bool {
			nested.set(key.String(), jsjson.Normalize([]byte(item.Raw)))
			return true
		})
	}
	nested.set(name, value)
	o.set(parent, nested.bytes())
}

func (o *neoPiOrderedObject) del(name string) {
	if i := o.index(name); i >= 0 {
		*o = append((*o)[:i], (*o)[i+1:]...)
	}
}

// merge is Object.assign: every field of extra in its own order, values
// re-serialized the way JSON.stringify writes them.
func (o *neoPiOrderedObject) merge(extra map[string]any) {
	ordered := neoPiOrderedObject{}
	gjson.ParseBytes(jsjson.Marshal(extra)).ForEach(func(key, value gjson.Result) bool {
		ordered.set(key.String(), jsjson.Normalize([]byte(value.Raw)))
		return true
	})
	for _, field := range ordered {
		o.set(field.Name, field.Value)
	}
}

func (o neoPiOrderedObject) bytes() json.RawMessage { return jsjson.Object(o...) }
