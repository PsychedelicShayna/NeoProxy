package antigravitywire

import (
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

// The google-gemini-cli branch of NeoPi's streamSimple (stream.ts
// mapOptionsForApi) with the helpers it reaches: normalizeMandatoryReasoning
// Options, requireSupportedEffort, resolveWireModelId,
// mapEffortToGoogleThinkingLevel, googleThinkingHeadroom and the output-cap
// arithmetic.

const (
	minOutputTokens       = 1024
	outputCapWhenUnknown  = 64_000
	modeGoogleLevel       = "google-level"
	suppressedLevel       = ThinkingLevel("MINIMAL")
	effortMinimal         = "minimal"
	effortLow             = "low"
	effortMedium          = "medium"
	effortHigh            = "high"
	effortXHigh           = "xhigh"
	effortMax             = "max"
	routingOff            = "off"
	thinkingLevelLow      = ThinkingLevel("LOW")
	thinkingLevelMedium   = ThinkingLevel("MEDIUM")
	thinkingLevelHigh     = ThinkingLevel("HIGH")
	thinkingLevelMinimal  = ThinkingLevel("MINIMAL")
	defaultBudgetFallback = 0
)

// effortLadder is THINKING_EFFORTS in canonical order.
var effortLadder = []string{effortMinimal, effortLow, effortMedium, effortHigh, effortXHigh, effortMax}

// googleThinking is GOOGLE_THINKING.
var googleThinking = map[string]int{
	effortMinimal: 1024, effortLow: 4096, effortMedium: 8192, effortHigh: 16384, effortXHigh: 24575, effortMax: 32768,
}

// SimpleOptions are the SimpleStreamOptions fields the branch reads. A nil
// pointer is JavaScript's undefined.
type SimpleOptions struct {
	Temperature, TopP, TopK, PresencePenalty *float64
	// MaxTokens is the caller's output budget; nil falls back to the model's.
	MaxTokens        *float64
	Reasoning        string
	DisableReasoning bool
	ToolChoice       *ToolChoice
	// HideThinkingSummary asks Cloud Code Assist to omit thought summaries.
	HideThinkingSummary bool
	// ThinkingBudgets overrides per-effort budgets (thinkingBudgets).
	ThinkingBudgets map[string]int
}

func supportedEfforts(model *registry.NeoPiModel) []string {
	if !model.Reasoning || model.Thinking == nil {
		return nil
	}
	return model.Thinking.Efforts
}

// minimumSupportedEffort is minimumSupportedEffort.
func minimumSupportedEffort(model *registry.NeoPiModel) string {
	if model.Thinking == nil || len(model.Thinking.Efforts) == 0 {
		return ""
	}
	for _, effort := range effortLadder {
		if slices.Contains(model.Thinking.Efforts, effort) {
			return effort
		}
	}
	return model.Thinking.Efforts[0]
}

// defaultSupportedEffort is defaultSupportedEffort: the effort whose wire
// route is the model's default id, else the lowest supported effort.
func defaultSupportedEffort(model *registry.NeoPiModel) string {
	if model.Thinking != nil && model.Thinking.EffortRouting != nil && model.RequestModelID != "" {
		for _, effort := range effortLadder {
			if slices.Contains(model.Thinking.Efforts, effort) && model.Thinking.EffortRouting[effort] == model.RequestModelID {
				return effort
			}
		}
	}
	return minimumSupportedEffort(model)
}

// resolveWireModelID is resolveWireModelId (effort "" = thinking off).
func resolveWireModelID(model *registry.NeoPiModel, effort string) string {
	key := effort
	if key == "" {
		key = routingOff
	}
	if model.Thinking != nil {
		if routed := model.Thinking.EffortRouting[key]; routed != "" {
			return routed
		}
	}
	if model.RequestModelID != "" {
		return model.RequestModelID
	}
	return model.ID
}

// mapEffortToGoogleThinkingLevel maps an effort to a Gemini thinkingLevel;
// a `minimal` routed onto the same wire id as `low` is sent as LOW.
func mapEffortToGoogleThinkingLevel(effort string, model *registry.NeoPiModel) ThinkingLevel {
	switch effort {
	case effortMinimal:
		if model.Thinking != nil {
			routing := model.Thinking.EffortRouting
			if routing[effortMinimal] != "" && routing[effortMinimal] == routing[effortLow] {
				return thinkingLevelLow
			}
		}
		return thinkingLevelMinimal
	case effortLow:
		return thinkingLevelLow
	case effortMedium:
		return thinkingLevelMedium
	}
	return thinkingLevelHigh
}

func googleThinkingHeadroom(model *registry.NeoPiModel, effort string, custom map[string]int) int {
	if value, ok := custom[effort]; ok && value > 0 {
		return value
	}
	if model.Thinking != nil {
		if value, ok := model.Thinking.EffortBudgets[effort]; ok && value > 0 {
			return value
		}
	}
	return googleThinking[effort]
}

func modelMaxTokens(model *registry.NeoPiModel) float64 {
	if model.MaxTokens <= 0 {
		return math.Inf(1)
	}
	return float64(model.MaxTokens)
}

// maxTokensWithThinkingBudget adds the thinking budget on top of the caller's
// answer budget, bounded by the model cap; with no caller cap a finite total
// stands in.
func maxTokensWithThinkingBudget(base *float64, model *registry.NeoPiModel, budget int) float64 {
	uncapped := float64(outputCapWhenUnknown)
	if base != nil {
		uncapped = *base + float64(budget)
	}
	return math.Min(uncapped, modelMaxTokens(model))
}

// MapSimpleOptions resolves a request's options the way streamSimple does for
// a google-gemini-cli model, including the mandatory-reasoning floor. The
// error is requireSupportedEffort's.
func MapSimpleOptions(model *registry.NeoPiModel, in SimpleOptions) (Options, error) {
	// normalizeMandatoryReasoningOptions
	if model.Reasoning && model.Thinking != nil && model.Thinking.RequiresEffort && !model.Thinking.SuppressWhenOff &&
		!(in.Reasoning != "" && !in.DisableReasoning) {
		if floor := defaultSupportedEffort(model); floor != "" {
			in.Reasoning, in.DisableReasoning = floor, false
		}
	}
	var base *float64
	switch {
	case in.MaxTokens != nil:
		base = in.MaxTokens
	case model.MaxTokens > 0:
		limit := float64(model.MaxTokens)
		base = &limit
	}
	out := Options{Temperature: in.Temperature, TopP: in.TopP, TopK: in.TopK, PresencePenalty: in.PresencePenalty,
		MaxTokens: base, ToolChoice: in.ToolChoice, HideThinkingSummary: in.HideThinkingSummary}

	if in.Reasoning != "" && model.Reasoning && !in.DisableReasoning {
		effort := in.Reasoning
		if !slices.Contains(supportedEfforts(model), effort) {
			return out, fmt.Errorf("Thinking effort %s is not supported by google-antigravity/%s. Supported efforts: %s", effort, model.ID, strings.Join(supportedEfforts(model), ", "))
		}
		if model.Thinking != nil && model.Thinking.Mode == modeGoogleLevel {
			if base != nil {
				capped := math.Min(*base+float64(googleThinkingHeadroom(model, effort, in.ThinkingBudgets)), modelMaxTokens(model))
				out.MaxTokens = &capped
			}
			out.RequestModelID = resolveWireModelID(model, effort)
			out.Thinking = Thinking{Enabled: true, Level: mapEffortToGoogleThinkingLevel(effort, model)}
			return out, nil
		}
		budget, ok := in.ThinkingBudgets[effort]
		if !ok && model.Thinking != nil {
			budget, ok = model.Thinking.EffortBudgets[effort]
		}
		if !ok {
			budget = googleThinking[effort]
		}
		maxTokens := maxTokensWithThinkingBudget(base, model, budget)
		if maxTokens <= float64(budget) {
			budget = int(math.Max(0, maxTokens-minOutputTokens))
		}
		if budget > 0 {
			out.MaxTokens = &maxTokens
			out.RequestModelID = resolveWireModelID(model, effort)
			out.Thinking = Thinking{Enabled: true, Budget: &budget}
			return out, nil
		}
		// Budget clamped to zero: fall through to the thinking-off path.
	}

	out.RequestModelID = resolveWireModelID(model, "")
	out.Thinking = Thinking{}
	if model.Reasoning && model.Thinking != nil && model.Thinking.SuppressWhenOff {
		// Cloud Code Assist re-applies the per-id baked server default when
		// the config is omitted; suppression must be explicit on the wire.
		out.Thinking.Suppress = true
		if model.Thinking.Mode == modeGoogleLevel {
			out.Thinking.SuppressLevel = suppressedLevel
		} else {
			zero := 0
			out.Thinking.SuppressBudget = &zero
		}
	}
	return out, nil
}
