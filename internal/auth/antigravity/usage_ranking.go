package antigravity

import "strings"

// NeoPi ranks an Antigravity credential per backend counter: one quota is
// shared by every model of the same family (Google, Anthropic, OpenAI), and a
// report can carry several windows of each. These helpers are
// quotaTierFor / scopeAntigravityLimitsForModel / antigravityRankingStrategy in
// a form the conductor can use.

// WindowDefaultPrimaryMs and WindowDefaultSecondaryMs are
// antigravityRankingStrategy.windowDefaults: legacy unlabelled quota entries
// fall back to a daily window.
const (
	WindowDefaultPrimaryMs   = dayMs
	WindowDefaultSecondaryMs = dayMs
)

// counterTiers is the compiled `quotaTiers` rule for google-antigravity.
var counterTiers = struct {
	models   map[string]string
	fallback [][2]string
}{
	models: map[string]string{
		"claude-opus-4-6":             "anthropic",
		"gemini-3-pro":                "google",
		"gemma-3":                     "google",
		"tab_flash_lite_preview":      "google",
		"tab_jump_flash_lite_preview": "google",
		"gpt-oss-120b":                "openai",
		"openai/gpt-oss-120b":         "openai",
	},
	fallback: [][2]string{
		{"claude-", "anthropic"},
		{"gemini-", "google"},
		{"gemma-", "google"},
		{"tab_", "google"},
		{"gpt-", "openai"},
		{"openai/", "openai"},
	},
}

// CounterKeyForModel is getAntigravityCounterKeyForModel: the backend counter
// a model draws from, or "" when the model is unknown.
func CounterKeyForModel(modelID string) string {
	if modelID == "" {
		return ""
	}
	if key, ok := counterTiers.models[modelID]; ok {
		return key
	}
	for _, fallback := range counterTiers.fallback {
		if strings.Contains(modelID, fallback[0]) {
			return fallback[1]
		}
	}
	return ""
}

// counterLimits is getAntigravityCounterLimits: the report's limits of one
// backend counter, in report order.
func counterLimits(report *Usage, counterKey string) []UsageLimit {
	if report == nil {
		return nil
	}
	prefix := report.Provider + ":" + counterKey + ":"
	var out []UsageLimit
	for _, limit := range report.Limits {
		if strings.HasPrefix(strings.ToLower(limit.ID), strings.ToLower(prefix)) {
			out = append(out, limit)
		}
	}
	return out
}

// ScopeLimitsForModel is scopeAntigravityLimitsForModel: the active model's
// counter limits, or the legacy default counter when that backend has none.
// An unknown or absent model scopes to nothing, so one exhausted family never
// blocks the whole provider.
func ScopeLimitsForModel(report *Usage, modelID string) []UsageLimit {
	counterKey := CounterKeyForModel(modelID)
	if counterKey == "" {
		return nil
	}
	if limits := counterLimits(report, counterKey); len(limits) > 0 {
		return limits
	}
	return counterLimits(report, "default")
}

// RankingWindowLimit is antigravityRankingStrategy.findWindowLimits().primary:
// the most-pressured limit of the model's counter (the report is sorted
// ascending by remaining fraction), or nil when the model has no scope.
func RankingWindowLimit(report *Usage, modelID string) *UsageLimit {
	if report == nil {
		return nil
	}
	if CounterKeyForModel(modelID) == "" {
		if len(report.Limits) == 0 {
			return nil
		}
		primary := report.Limits[0]
		return &primary
	}
	limits := ScopeLimitsForModel(report, modelID)
	if len(limits) == 0 {
		return nil
	}
	primary := limits[0]
	return &primary
}

// BlockScope is antigravityRankingStrategy.blockScope: the scope a usage block
// is recorded under, per backend counter.
func BlockScope(modelID string) string {
	counterKey := CounterKeyForModel(modelID)
	if counterKey == "" {
		counterKey = "unknown"
	}
	return "counter:" + counterKey
}

// HealableBlockScope is one counter's scope and the limits that heal it.
type HealableBlockScope struct {
	BlockScope string
	Limits     []UsageLimit
}

// HealableBlockScopes is antigravityRankingStrategy.healableBlockScopes: one
// scope per backend counter the report covers, so a 429 that carries a weekly
// retry-after is lifted as soon as that counter's own windows recover.
func HealableBlockScopes(report *Usage) []HealableBlockScope {
	if report == nil {
		return nil
	}
	var keys []string
	seen := map[string]bool{}
	for _, limit := range report.Limits {
		parts := strings.Split(limit.ID, ":")
		if len(parts) < 2 {
			continue
		}
		key := strings.ToLower(parts[1])
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		keys = append(keys, key)
	}
	out := make([]HealableBlockScope, 0, len(keys))
	for _, key := range keys {
		out = append(out, HealableBlockScope{BlockScope: "counter:" + key, Limits: counterLimits(report, key)})
	}
	return out
}
