// Package openaiwire ports NeoPi's OpenAI-family request builders
// (packages/ai/src/providers/openai-responses.ts and openai-shared.ts) for
// providers whose models NeoPi resolves to the generic Responses API, such as
// xai-oauth. A downstream Responses body maps onto NeoPi's stream options and
// native-history input; the builder then produces the exact body NeoPi sends.
package openaiwire

import (
	"encoding/json"
	"math"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/codexwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/tidwall/gjson"
)

// openAIMaxOutputTokens is OPENAI_MAX_OUTPUT_TOKENS.
const openAIMaxOutputTokens = 64000

// Context is the conversation a downstream Responses request carries: its
// instructions, its input items (replayed as native history) and its tools.
type Context struct {
	SystemPrompts []string
	Input         []json.RawMessage
	Tools         []json.RawMessage
}

// Options are the OpenAIResponsesOptions NeoProxy derives from a downstream body.
type Options struct {
	SessionID string
	// Effort is the agent-clamped thinking level; empty when none was requested.
	Effort string
	// DisableReasoning is a downstream `reasoning.effort: "none"`.
	DisableReasoning bool
	// ReasoningSummary is the downstream summary; SummarySet records presence.
	ReasoningSummary string
	SummarySet       bool
	Temperature      *float64
	TopP             *float64
	MaxTokens        *float64
	TextVerbosity    string
	ServiceTier      string
	ToolChoice       json.RawMessage
	Include          []string
	// DisableStrictTools is the strict-tools fallback: a provider that rejected
	// strict schemas gets every tool without the flag.
	DisableStrictTools bool
}

// FromResponses maps a downstream Responses body onto NeoPi's Context and
// options. Fields NeoPi's agent never forwards (store, stream,
// previous_response_id, metadata) are dropped.
func FromResponses(body []byte, model *registry.NeoPiModel) (Context, Options) {
	root := gjson.ParseBytes(body)
	var ctx Context
	if instructions := root.Get("instructions"); instructions.Type == gjson.String {
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
	ctx.Input = omitUnsupportedImages(ctx.Input, model)
	for _, tool := range root.Get("tools").Array() {
		ctx.Tools = append(ctx.Tools, json.RawMessage(tool.Raw))
	}
	var opts Options
	reasoning := root.Get("reasoning")
	if effort := reasoning.Get("effort"); effort.Type == gjson.String {
		if effort.String() == "none" {
			opts.DisableReasoning = true
		} else if model != nil && model.Reasoning && model.Thinking != nil {
			opts.Effort = codexwire.ClampEffort(model.Thinking.Efforts, effort.String())
		} else {
			// No effort ladder: the agent forwards the level unclamped.
			opts.Effort = effort.String()
		}
	}
	switch summary := reasoning.Get("summary"); {
	case summary.Type == gjson.Null && summary.Exists():
		opts.SummarySet = true
	case summary.Type == gjson.String:
		opts.ReasoningSummary, opts.SummarySet = summary.String(), true
	}
	number := func(path string) *float64 {
		if value := root.Get(path); value.Type == gjson.Number {
			n := value.Float()
			return &n
		}
		return nil
	}
	opts.Temperature, opts.TopP, opts.MaxTokens = number("temperature"), number("top_p"), number("max_output_tokens")
	if verbosity := root.Get("text.verbosity"); verbosity.Type == gjson.String {
		opts.TextVerbosity = verbosity.String()
	}
	if tier := root.Get("service_tier"); tier.Type == gjson.String {
		opts.ServiceTier = tier.String()
	}
	if choice := root.Get("tool_choice"); choice.Exists() && choice.Type != gjson.Null {
		opts.ToolChoice = json.RawMessage(choice.Raw)
	}
	for _, value := range root.Get("include").Array() {
		if value.Type == gjson.String {
			opts.Include = append(opts.Include, value.String())
		}
	}
	// streamSimple's clamp. The Meta, Sakana and Abliteration captures drive
	// streamOpenAIResponses directly, below it, so only the openrouter captures
	// (streamSimple) pin it.
	if model != nil && model.Provider == "openrouter" {
		normalizeMandatoryReasoning(model, &opts)
	}
	return ctx, opts
}

// normalizeMandatoryReasoning is normalizeMandatoryReasoningOptions: a
// reasoning model whose endpoint cannot disable thinking
// (thinking.requiresEffort) sends the lowest supported effort whenever the
// caller asked for none, or asked to disable it.
func normalizeMandatoryReasoning(model *registry.NeoPiModel, opts *Options) {
	if model == nil || !model.Reasoning || model.Thinking == nil || !model.Thinking.RequiresEffort {
		return
	}
	if opts.Effort != "" && !opts.DisableReasoning {
		return
	}
	for _, effort := range []string{"minimal", "low", "medium", "high", "xhigh", "max"} {
		if containsString(model.Thinking.Efforts, effort) {
			opts.Effort, opts.DisableReasoning = effort, false
			return
		}
	}
	if len(model.Thinking.Efforts) > 0 {
		opts.Effort, opts.DisableReasoning = model.Thinking.Efforts[0], false
	}
}

// reasoningPolicy is the reasoning half of resolveOpenAICompatPolicy.
type reasoningPolicy struct {
	modelSupported bool
	requested      string
	wireEffort     string
	enabled        bool
	disabled       bool
	omitEffort     bool
	disableMode    string
	// includeEncrypted is compat.includeEncryptedReasoning.
	includeEncrypted bool
}

func mapEffort(model *registry.NeoPiModel, compat *registry.NeoPiCompat, effort string) string {
	if mapped, ok := compat.ReasoningEffortMap[effort]; ok {
		return mapped
	}
	if model.Thinking != nil {
		if mapped, ok := model.Thinking.EffortMap[effort]; ok {
			return mapped
		}
	}
	return effort
}

func isImplicitDisableWhenNotRequested(mode string) bool {
	switch mode {
	case "zai-thinking-disabled", "qwen-enable-thinking-false", "qwen-template-false", "chat-template-thinking-false":
		return true
	}
	return false
}

// isForcedToolChoice is NeoPi's isForcedToolChoice over a wire tool_choice.
func isForcedToolChoice(choice json.RawMessage) bool {
	parsed := gjson.ParseBytes(choice)
	if parsed.Type == gjson.String {
		return parsed.String() == "required"
	}
	return parsed.IsObject()
}

func resolveReasoningPolicy(model *registry.NeoPiModel, opts Options, toolChoice json.RawMessage, hasTools bool) reasoningPolicy {
	compat := model.Compat
	modelSupported := model.Reasoning
	forced := compat.DisableReasoningOnForcedToolChoice && compat.SupportsForcedToolChoice && toolChoice != nil && isForcedToolChoice(toolChoice)
	anyChoice := !forced && compat.DisableReasoningOnToolChoice && toolChoice != nil
	tools := !forced && !anyChoice && compat.DisableReasoningWithTools && hasTools
	conflict := forced || anyChoice || tools
	callerDisabled := opts.DisableReasoning
	requested := opts.Effort
	requestedAndAllowed := requested != "" && !callerDisabled && modelSupported
	enabledBefore := requestedAndAllowed && !callerDisabled && !conflict
	baseWire := ""
	if enabledBefore {
		baseWire = mapEffort(model, compat, requested)
	}
	disabledByNone := enabledBefore && compat.ReasoningDisableMode == "zai-thinking-disabled" && baseWire == "none"
	enabled := enabledBefore && !disabledByNone
	policy := reasoningPolicy{modelSupported: modelSupported, requested: requested, enabled: enabled,
		omitEffort: compat.OmitReasoningEffort || !compat.SupportsReasoningEffort, disableMode: compat.ReasoningDisableMode,
		includeEncrypted: compat.IncludeEncryptedReasoning}
	if enabled {
		policy.wireEffort = mapEffort(model, compat, requested)
	}
	disabledWithoutRequest := modelSupported && requested == "" && !callerDisabled && isImplicitDisableWhenNotRequested(policy.disableMode)
	policy.disabled = (modelSupported && callerDisabled) || conflict || disabledWithoutRequest || disabledByNone
	if policy.disabled && compat.SupportsReasoningEffort && !policy.omitEffort {
		if policy.disableMode == "none-effort" {
			policy.wireEffort = "none"
		} else if callerDisabled && requested == "" && policy.disableMode == "lowest-effort" {
			if model.Thinking != nil && len(model.Thinking.Efforts) > 0 {
				policy.wireEffort = mapEffort(model, compat, model.Thinking.Efforts[0])
			}
		}
	}
	return policy
}

// BuildResponses is buildParams for a downstream turn: the exact Responses
// body NeoPi streams for model.
func BuildResponses(model *registry.NeoPiModel, ctx Context, opts Options) json.RawMessage {
	compat := model.Compat
	// The first policy pass decides whether replayed turns must carry reasoning.
	first := resolveReasoningPolicy(model, opts, opts.ToolChoice, false)
	canReconstruct := !compat.FilterReasoningHistory || compat.AllowsSyntheticReasoningContentForToolCalls
	input := buildInput(ctx.Input, compat, reasoningReplay{
		allTurns:  first.enabled && compat.RequiresReasoningContentForAllAssistantTurns && canReconstruct,
		toolCalls: first.enabled && compat.RequiresReasoningContentForToolCalls && canReconstruct,
	})
	var instructions string
	if len(ctx.SystemPrompts) > 0 {
		if model.Reasoning && compat.SupportsDeveloperRole {
			developer := make([]json.RawMessage, 0, len(ctx.SystemPrompts)+len(input))
			for _, prompt := range ctx.SystemPrompts {
				developer = append(developer, jsjson.Object(
					jsjson.Field{Name: "role", Value: jsjson.Marshal("developer")},
					jsjson.Field{Name: "content", Value: jsjson.Marshal(prompt)},
				))
			}
			input = append(developer, input...)
		} else {
			instructions = strings.Join(ctx.SystemPrompts, "\n\n")
		}
	}
	promptCacheKey := codexwire.NormalizePromptCacheKey(opts.SessionID)
	params := object{{Name: "model", Value: jsjson.Marshal(model.ID)}, {Name: "input", Value: jsjson.Marshal(input)}}
	if instructions != "" {
		params.set("instructions", jsjson.Marshal(instructions))
	}
	params.set("stream", jsjson.Marshal(true))
	if promptCacheKey != "" {
		params.set("prompt_cache_key", jsjson.Marshal(promptCacheKey))
	}
	// OpenRouter's Responses wire carries the routing session for sticky upstream routing.
	if sessionID := openRouterSessionID(compat, opts.SessionID); sessionID != "" {
		params.set("session_id", jsjson.Marshal(sessionID))
	}
	params.set("store", jsjson.Marshal(false))
	if compat.SupportsObfuscationOptOut {
		params.set("stream_options", jsjson.Object(jsjson.Field{Name: "include_obfuscation", Value: jsjson.Marshal(false)}))
	}
	if len(opts.Include) > 0 {
		params.set("include", jsjson.Marshal(uniqueStrings(opts.Include)))
	}
	if compat.CacheControlFormat == "anthropic" {
		params.set("cache_control", jsjson.Object(jsjson.Field{Name: "type", Value: jsjson.Marshal("ephemeral")}))
	}
	if maxTokens, ok := outputTokens(model, opts.MaxTokens); ok {
		params.set("max_output_tokens", jsjson.Marshal(maxTokens))
	}
	if compat.SupportsSamplingParams {
		if opts.Temperature != nil {
			params.set("temperature", jsjson.Marshal(*opts.Temperature))
		}
		if opts.TopP != nil {
			params.set("top_p", jsjson.Marshal(*opts.TopP))
		}
	}
	if shouldSendServiceTier(opts.ServiceTier, model.Provider) {
		params.set("service_tier", jsjson.Marshal(opts.ServiceTier))
	}
	if opts.TextVerbosity != "" && compat.OfficialEndpoint {
		params.set("text", jsjson.Object(jsjson.Field{Name: "verbosity", Value: jsjson.Marshal(opts.TextVerbosity)}))
	}
	var toolChoice json.RawMessage
	if len(ctx.Tools) > 0 {
		tools, emitted := convertTools(ctx.Tools, compat, compat.SupportsStrictMode && !opts.DisableStrictTools)
		params.set("tools", jsjson.Marshal(tools))
		if opts.ToolChoice != nil {
			if choice := mapToolChoice(opts.ToolChoice, emitted, compat); choice != nil && len(tools) > 0 {
				if gjson.GetBytes(choice, "type").String() == "function" && !compat.SupportsNamedToolChoice {
					name := gjson.GetBytes(choice, "name").String()
					var kept []json.RawMessage
					for _, tool := range tools {
						if gjson.GetBytes(tool, "type").String() == "function" && gjson.GetBytes(tool, "name").String() == name {
							kept = append(kept, tool)
						}
					}
					params.set("tools", jsjson.Marshal(kept))
					toolChoice = jsjson.Marshal("required")
				} else {
					toolChoice = choice
				}
				params.set("tool_choice", toolChoice)
			}
		}
	}
	if toolChoice != nil && gjson.ParseBytes(toolChoice).String() == "auto" && gjson.ParseBytes(toolChoice).Type == gjson.String &&
		compat.DisableReasoningOnToolChoice && model.Reasoning && opts.Effort != "" && !opts.DisableReasoning {
		params.del("tool_choice")
		toolChoice = nil
	}
	applyReasoning(&params, resolveReasoningPolicy(model, opts, toolChoice, len(ctx.Tools) > 0), reasoningSummary(model, opts), func(effort string) string {
		return mapEffort(model, compat, effort)
	})
	return params.bytes()
}

// reasoningSummary is resolveReasoningSummaryOption: hosts that reject
// reasoning.summary get an explicit omission whenever reasoning is requested.
func reasoningSummary(model *registry.NeoPiModel, opts Options) *string {
	if model.Compat.SupportsReasoningSummary {
		if !opts.SummarySet {
			return nil
		}
		summary := opts.ReasoningSummary
		return &summary
	}
	if opts.Effort == "" {
		return nil
	}
	none := ""
	return &none
}

// applyReasoning is applyResponsesCompatPolicy. summary nil is "undefined";
// a pointer to "" is an explicit null (omit).
func applyReasoning(params *object, policy reasoningPolicy, summary *string, mapEffortFn func(string) string) {
	if !policy.modelSupported {
		return
	}
	include := params.get("include")
	var values []string
	for _, value := range include.Array() {
		values = append(values, value.String())
	}
	includeEncrypted := policy.includeEncrypted
	if includeEncrypted {
		if !containsString(values, "reasoning.encrypted_content") {
			values = append(values, "reasoning.encrypted_content")
		}
		params.set("include", jsjson.Marshal(values))
	}
	if policy.disabled {
		if policy.disableMode == "openrouter-enabled-false" {
			params.set("reasoning", jsjson.Object(jsjson.Field{Name: "enabled", Value: jsjson.Marshal(false)}))
			return
		}
		if (policy.disableMode == "lowest-effort" || policy.disableMode == "none-effort") && policy.wireEffort != "" && !policy.omitEffort {
			params.set("reasoning", jsjson.Object(jsjson.Field{Name: "effort", Value: jsjson.Marshal(policy.wireEffort)}))
		}
		return
	}
	if policy.requested == "" && summary == nil {
		return
	}
	if policy.omitEffort {
		if summary != nil && *summary != "" {
			params.set("reasoning", jsjson.Object(jsjson.Field{Name: "summary", Value: jsjson.Marshal(*summary)}))
		}
		return
	}
	requested := policy.requested
	if requested == "" {
		requested = "medium"
	}
	wire := policy.wireEffort
	if wire == "" {
		wire = mapEffortFn(requested)
	}
	fields := []jsjson.Field{{Name: "effort", Value: jsjson.Marshal(wire)}}
	if summary == nil || *summary != "" {
		value := "auto"
		if summary != nil {
			value = *summary
		}
		fields = append(fields, jsjson.Field{Name: "summary", Value: jsjson.Marshal(value)})
	}
	params.set("reasoning", jsjson.Object(fields...))
}

// outputTokens is resolveOpenAIOutputTokenParam followed by
// applyCommonResponsesSamplingParams' clamp.
func outputTokens(model *registry.NeoPiModel, requested *float64) (float64, bool) {
	compat := model.Compat
	var value float64
	switch {
	case requested != nil:
		value = *requested
	case compat.AlwaysSendMaxTokens:
		value = openAIMaxOutputTokens
		if model.MaxTokens > 0 {
			value = float64(model.MaxTokens)
		}
	default:
		return 0, false
	}
	if compat.IsOpenRouterHost && !compat.AlwaysSendMaxTokens && requested == nil {
		return 0, false
	}
	clamp := float64(openAIMaxOutputTokens)
	if compat.ClampOutputToModelMax && model.MaxTokens > 0 {
		clamp = float64(model.MaxTokens)
	}
	modelMax := math.Inf(1)
	if model.MaxTokens > 0 {
		modelMax = float64(model.MaxTokens)
	}
	value = math.Min(value, math.Min(modelMax, clamp))
	if !(value > 0) {
		return 0, false
	}
	return value, true
}

// shouldSendServiceTier is NeoPi's provider gate for non-Codex Responses models.
func shouldSendServiceTier(tier, provider string) bool {
	if tier == "" || tier == "auto" {
		return false
	}
	switch provider {
	case "openai":
		return true
	case "openrouter":
		return tier == "flex" || tier == "scale" || tier == "priority"
	}
	return false
}

func uniqueStrings(values []string) []string {
	var out []string
	for _, value := range values {
		if !containsString(out, value) {
			out = append(out, value)
		}
	}
	return out
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

// openRouterSessionID is getOpenRouterResponsesSessionId: the routing session
// only exists on OpenRouter hosts.
func openRouterSessionID(compat *registry.NeoPiCompat, sessionID string) string {
	if !compat.IsOpenRouterHost {
		return ""
	}
	return codexwire.NormalizeOpenRouterSessionID(sessionID)
}
