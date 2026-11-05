package googlewire

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
)

const (
	minOutputTokens = 1024
	// thinkingEfforts is NeoPi's THINKING_EFFORTS ladder order.
	modeBudget      = "budget"
	modeGoogleLevel = "google-level"
)

var effortLadder = []string{"minimal", "low", "medium", "high", "xhigh", "max"}

// googleThinkingBudgets is GOOGLE_THINKING: the thinking spend assumed for an
// effort when sizing maxOutputTokens.
var googleThinkingBudgets = map[string]int{
	"minimal": 1024, "low": 4096, "medium": 8192, "high": 16384, "xhigh": 24575, "max": 32768,
}

// ToolChoice is NeoPi's mapped Google tool choice: Mode is auto, none or any,
// or "named" with Name for a single-tool allow-list.
type ToolChoice struct {
	Mode string
	Name string
}

// Options are the SimpleStreamOptions fields the google-generative-ai branch
// of streamSimple reads. A nil pointer is JavaScript's undefined.
type Options struct {
	Temperature, TopP, TopK, PresencePenalty *float64
	// MaxTokens is the caller's output cap; nil falls back to the model's.
	MaxTokens *int
	// Reasoning is the requested effort; empty means unset.
	Reasoning        string
	DisableReasoning bool
	ToolChoice       ToolChoice
	ServiceTier      string
	// HideThinkingSummary asks Google to omit thought summaries.
	HideThinkingSummary bool
	CachedContent       string
}

// Thinking is the resolved GoogleOptions.thinking.
type ThinkingOptions struct {
	Enabled bool
	Budget  *int
	Level   string
}

// Resolved is the GoogleOptions streamSimple derives from Options.
type Resolved struct {
	Options
	MaxTokens *int
	Thinking  *ThinkingOptions
	// SendServiceTier reports whether serviceTier reaches the body. NeoPi's
	// thinking-off and google-level branches forward it; the budget branch does
	// not, and the port keeps that.
	SendServiceTier bool
}

func effortIndex(effort string) int {
	for i, value := range effortLadder {
		if value == effort {
			return i
		}
	}
	return -1
}

func supportsEffort(model *Model, effort string) bool {
	if !model.Reasoning || model.Thinking == nil {
		return false
	}
	for _, candidate := range model.Thinking.Efforts {
		if candidate == effort {
			return true
		}
	}
	return false
}

// minimumSupportedEffort is NeoPi's helper of the same name.
func minimumSupportedEffort(model *Model) string {
	if model.Thinking == nil || len(model.Thinking.Efforts) == 0 {
		return ""
	}
	for _, effort := range effortLadder {
		for _, candidate := range model.Thinking.Efforts {
			if candidate == effort {
				return effort
			}
		}
	}
	return model.Thinking.Efforts[0]
}

func levelFor(effort string) string {
	switch effort {
	case "minimal":
		return "MINIMAL"
	case "low":
		return "LOW"
	case "medium":
		return "MEDIUM"
	}
	return "HIGH"
}

func intPtr(value int) *int { return &value }

func googleHeadroom(model *Model, effort string) int {
	if budget := model.Thinking.budget(effort); budget > 0 {
		return budget
	}
	return googleThinkingBudgets[effort]
}

func (t *Thinking) budget(effort string) int {
	if t == nil {
		return 0
	}
	return t.EffortBudgets[effort]
}

// googleMaxOutputTokens is the budget plus thinking headroom bounded by the
// model cap; nil (no cap sent) when the caller has none.
func googleMaxOutputTokens(base *int, model *Model, headroom int) *int {
	if base == nil {
		return nil
	}
	capped := *base + headroom
	if model.MaxTokens > 0 && model.MaxTokens < capped {
		capped = model.MaxTokens
	}
	return &capped
}

func thinkingOff(model *Model) *ThinkingOptions {
	off := &ThinkingOptions{}
	if !model.Reasoning || model.Thinking == nil {
		return off
	}
	t := model.Thinking
	if t.Mode == modeBudget && (!t.RequiresEffort || t.SuppressWhenOff) {
		off.Budget = intPtr(0)
	} else if t.Mode == modeGoogleLevel && t.SuppressWhenOff {
		off.Level = "MINIMAL"
	}
	return off
}

// MapOptions is mapOptionsForApi's google-generative-ai branch, including
// normalizeMandatoryReasoningOptions.
func MapOptions(model *Model, opts Options) (Resolved, error) {
	// Mandatory-reasoning models clamp an absent or disabled effort to the floor.
	if model.Reasoning && model.Thinking != nil && model.Thinking.RequiresEffort && !model.Thinking.SuppressWhenOff &&
		(opts.Reasoning == "" || opts.DisableReasoning) {
		if floor := minimumSupportedEffort(model); floor != "" {
			opts.Reasoning, opts.DisableReasoning = floor, false
		}
	}
	base := opts.MaxTokens
	if base == nil && model.MaxTokens > 0 {
		base = intPtr(model.MaxTokens)
	}
	out := Resolved{Options: opts, MaxTokens: base}
	if opts.Reasoning == "" || !model.Reasoning || opts.DisableReasoning {
		out.Thinking = thinkingOff(model)
		out.SendServiceTier = true
		return out, nil
	}
	effort := opts.Reasoning
	if !supportsEffort(model, effort) {
		return out, fmt.Errorf("Thinking effort %s is not supported by %s/%s. Supported efforts: %s", effort, model.Provider, model.ID, supportedEfforts(model))
	}
	if model.Thinking.Mode == modeGoogleLevel {
		out.MaxTokens = googleMaxOutputTokens(base, model, googleHeadroom(model, effort))
		out.Thinking = &ThinkingOptions{Enabled: true, Level: levelFor(effort)}
		out.SendServiceTier = true
		return out, nil
	}
	requested := -1
	if budget, ok := model.Thinking.EffortBudgets[effort]; ok {
		requested = budget
	}
	headroom := requested
	if requested <= 0 {
		headroom = googleHeadroom(model, effort)
	}
	maxTokens := googleMaxOutputTokens(base, model, headroom)
	budget := requested
	if requested > 0 && maxTokens != nil && *maxTokens <= requested {
		budget = *maxTokens - minOutputTokens
		if budget <= 0 {
			out.Thinking = thinkingOff(model)
			return out, nil
		}
	}
	out.MaxTokens = maxTokens
	out.Thinking = &ThinkingOptions{Enabled: true, Budget: &budget}
	return out, nil
}

func supportedEfforts(model *Model) string {
	if model.Thinking == nil {
		return ""
	}
	return strings.Join(model.Thinking.Efforts, ", ")
}

// shouldSendServiceTier is shouldSendServiceTier for provider google.
func shouldSendServiceTier(tier string) bool { return tier == "flex" || tier == "priority" }

func number(value float64) json.RawMessage { return json.RawMessage(jsjson.Number(value)) }

// BuildBody is buildGoogleGenerateContentParams followed by paramsToWireBody:
// the JSON body NeoPi POSTs to :streamGenerateContent.
func BuildBody(model *Model, ctx Context, resolved Resolved, legacySchema func(json.RawMessage) json.RawMessage) ([]byte, error) {
	var systemParts []json.RawMessage
	for _, prompt := range ctx.SystemPrompts {
		prompt = toWellFormed(prompt)
		if strings.TrimSpace(prompt) != "" {
			systemParts = append(systemParts, jsjson.Object(field("text", str(prompt))))
		}
	}
	contents := ConvertMessages(model, ctx.Messages)
	tools := ConvertTools(model, ctx.Tools, legacySchema)

	fields := []jsjson.Field{field("contents", jsjson.Marshal(contents))}
	if len(systemParts) > 0 {
		fields = append(fields, field("systemInstruction", jsjson.Object(field("parts", jsjson.Marshal(systemParts)))))
	}
	if tools != nil {
		fields = append(fields, field("tools", tools))
	}
	if len(ctx.Tools) > 0 {
		switch choice := resolved.ToolChoice; choice.Mode {
		case "none", "any":
			mode := strings.ToUpper(choice.Mode)
			fields = append(fields, field("toolConfig", jsjson.Object(field("functionCallingConfig", jsjson.Object(field("mode", str(mode)))))))
		case "named":
			fields = append(fields, field("toolConfig", jsjson.Object(field("functionCallingConfig", jsjson.Object(
				field("mode", str("ANY")), field("allowedFunctionNames", jsjson.Marshal([]string{choice.Name})))))))
		}
	}
	if resolved.CachedContent != "" {
		fields = append(fields, field("cachedContent", str(resolved.CachedContent)))
	}
	if model.Provider == "google" && resolved.SendServiceTier && shouldSendServiceTier(resolved.ServiceTier) {
		fields = append(fields, field("serviceTier", str(resolved.ServiceTier)))
	}

	var gen []jsjson.Field
	if resolved.Temperature != nil {
		gen = append(gen, field("temperature", number(*resolved.Temperature)))
	}
	if resolved.MaxTokens != nil {
		gen = append(gen, field("maxOutputTokens", jsjson.Marshal(*resolved.MaxTokens)))
	}
	if resolved.TopP != nil {
		gen = append(gen, field("topP", number(*resolved.TopP)))
	}
	if resolved.TopK != nil {
		gen = append(gen, field("topK", number(*resolved.TopK)))
	}
	if resolved.PresencePenalty != nil {
		gen = append(gen, field("presencePenalty", number(*resolved.PresencePenalty)))
	}
	if thinking := resolved.Thinking; thinking != nil && model.Reasoning && (thinking.Enabled || thinking.Level != "" || thinking.Budget != nil) {
		cfg := []jsjson.Field{field("includeThoughts", jsjson.Marshal(thinking.Enabled && !resolved.HideThinkingSummary))}
		if thinking.Level != "" {
			cfg = append(cfg, field("thinkingLevel", str(thinking.Level)))
		} else if thinking.Budget != nil {
			cfg = append(cfg, field("thinkingBudget", jsjson.Marshal(*thinking.Budget)))
		}
		gen = append(gen, field("thinkingConfig", jsjson.Object(cfg...)))
	}
	if len(gen) > 0 {
		fields = append(fields, field("generationConfig", jsjson.Object(gen...)))
	}
	return jsjson.Object(fields...), nil
}
