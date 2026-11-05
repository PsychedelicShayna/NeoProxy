package registry

import (
	"errors"
	"strings"
)

// antigravitySpec is a ModelSpec<"google-gemini-cli"> for google-antigravity as
// discovery normalization, the bundled seed and collapsing build it.
type antigravitySpec struct {
	ID                string
	Name              string
	BaseURL           string
	Reasoning         bool
	Input             []string
	ContextWindow     int
	MaxTokens         int
	RequestModelID    string
	HasRequestModelID bool
	// Thinking is the spec's explicit thinking surface (collapse family
	// metadata); buildModel resolves it into the model's policy thinking.
	Thinking     *NeoPiThinking
	WireProfiles map[string]NeoPiAntigravityWireProfile
	// Kind is the catalog kind; "" means chat.
	Kind string
	// Cost is the resolved per-million rate card, read only by the thinking
	// pair rule's same-price gate.
	Cost antigravityCost
}

// antigravityCost is ModelCost's token legs.
type antigravityCost struct{ input, output, cacheRead, cacheWrite float64 }

// antigravityModel is a built Model: the spec after buildModel has materialized
// identity, the Google compat record, thinking and catalog corrections.
type antigravityModel struct {
	antigravitySpec
	IdentityClass string
	Compat        NeoPiGeminiCompat
}

// NeoPi's MODEL_KINDS, the values a catalog `kind` may take.
var antigravityModelKinds = map[string]bool{
	"chat": true, "tiny": true, "image": true, "tts": true, "stt": true,
	"search": true, "judge": true, "embedding": true, "rerank": true, "video": true,
}

// antigravityPolicyModelID is policyModelId: the id a spec resolves identity
// and rules under, following its wire id when its own id names no model.
func antigravityPolicyModelID(spec antigravitySpec) string {
	wireID := spec.RequestModelID
	if !spec.HasRequestModelID || wireID == spec.ID {
		return spec.ID
	}
	if antigravityClassifyModel(antigravityProvider, spec.ID).Class != "unknown" {
		return spec.ID
	}
	if antigravityClassifyModel(antigravityProvider, wireID).Class == "unknown" {
		return spec.ID
	}
	return wireID
}

// antigravityBuildModel is buildModel for a google-antigravity row.
func antigravityBuildModel(spec antigravitySpec) (antigravityModel, error) {
	model := antigravityModel{antigravitySpec: spec}
	// withoutUnverifiedThinking: a derived unverified ladder is not authored
	// input, so re-entering resolution starts from the neutral row.
	policy := spec
	if policy.Thinking != nil && policy.Thinking.Unverified {
		policy.Thinking = nil
		policy.Reasoning = false
	}
	identity := antigravityClassifyModel(antigravityProvider, antigravityPolicyModelID(policy))
	target := antigravityTarget{
		Class:     identity.Class,
		Family:    identity.Family,
		Revision:  identity.Revision,
		Model:     antigravityPolicyModelID(policy),
		Reasoning: policy.Reasoning,
	}
	axes, err := resolveAntigravityCascade(target)
	if err != nil {
		return antigravityModel{}, err
	}
	compat := antigravityGoogleCompat(axes.Wire)
	thinking, err := antigravityResolveThinking(policy, identity, axes, compat)
	if err != nil {
		return antigravityModel{}, err
	}
	model.IdentityClass = identity.Class
	model.Compat = compat
	model.Thinking = thinking
	// A reviewed `thinking-upgrade-neutral` policy can repair a stale
	// `reasoning: false` discovery default.
	model.Reasoning = spec.Reasoning || thinking != nil
	if err := antigravityApplyCatalog(&model, axes.Catalog); err != nil {
		return antigravityModel{}, err
	}
	return model, nil
}

// antigravityGoogleCompat is resolveGooglePolicy: the detected baseline, the
// cascade's wire axes and (discovery never authors one) the spec's overrides.
func antigravityGoogleCompat(wire map[string]any) NeoPiGeminiCompat {
	var compat NeoPiGeminiCompat
	for _, key := range antigravityRules.GoogleWireKeys {
		value, ok := wire[key]
		if !ok {
			continue
		}
		switch key {
		case "antigravityUsageLabel":
			compat.AntigravityUsageLabel, _ = value.(string)
		case "maxImageDimension":
			compat.MaxImageDimension = int(antigravityNumber(value))
		case "maxImagePayloadBytes":
			compat.MaxImagePayloadBytes = int(antigravityNumber(value))
		case "streamFirstEventTimeoutMs":
			compat.StreamFirstEventTimeoutMs = int(antigravityNumber(value))
		case "requiresSkipThoughtSignatureOnFirstFunctionCall":
			compat.RequiresSkipThoughtSignatureOnFirstFunctionCall = antigravityBool(value)
		case "requiresSkipThoughtSignature":
			compat.RequiresSkipThoughtSignature = antigravityBool(value)
		case "dropUnsignedThinking":
			compat.DropUnsignedThinking = antigravityBool(value)
		case "ccaLegacyParametersSchema":
			compat.CCALegacyParametersSchema = antigravityBool(value)
		case "multimodalFunctionResponse":
			compat.MultimodalFunctionResponse = antigravityBool(value)
		case "flashStreamLeakWorkaround":
			compat.FlashStreamLeakWorkaround = antigravityBool(value)
		case "claudeThinkingBetaHeader":
			compat.ClaudeThinkingBetaHeader = antigravityBool(value)
		case "antigravityClaudeToolMode":
			compat.AntigravityClaudeToolMode = antigravityBool(value)
		case "supportsFunctionPartId":
			compat.SupportsFunctionPartID = antigravityBool(value)
		}
	}
	return compat
}

// antigravityRuleThinking is RuleThinking: the thinking axis narrowed to the
// fields resolveThinkingPolicy reads.
type antigravityRuleThinking struct {
	mode            string
	efforts         []string
	hasEfforts      bool
	defaultLevel    string
	effortMap       map[string]string
	effortBudgets   map[string]int
	requiresEffort  bool
	suppressWhenOff bool
	upgradeNeutral  bool
	unverified      bool
}

// antigravityReadRuleThinking is readRuleThinking.
func antigravityReadRuleThinking(axes map[string]any) antigravityRuleThinking {
	var rule antigravityRuleThinking
	if axes == nil {
		return rule
	}
	if mode, ok := axes["mode"].(string); ok {
		switch mode {
		case "effort", "budget", "google-level", "anthropic-adaptive", "anthropic-budget-effort":
			rule.mode = mode
		}
	}
	if raw, ok := axes["efforts"]; ok {
		efforts, ok := antigravityEffortList(raw)
		if ok {
			rule.efforts, rule.hasEfforts = efforts, true
		}
	}
	if level, ok := axes["defaultLevel"].(string); ok && slicesContains(neoPiThinkingEfforts, level) {
		rule.defaultLevel = level
	}
	if raw, ok := axes["effortMap"].(map[string]string); ok {
		rule.effortMap = raw
	}
	if raw, ok := axes["effortBudgets"].(map[string]int); ok {
		rule.effortBudgets = raw
	}
	if value, ok := axes["requiresEffort"].(bool); ok {
		rule.requiresEffort = value
	}
	if value, ok := axes["suppressWhenOff"].(bool); ok {
		rule.suppressWhenOff = value
	}
	if value, ok := axes["upgradeNeutral"].(bool); ok {
		rule.upgradeNeutral = value
	}
	if value, ok := axes["unverified"].(bool); ok {
		rule.unverified = value
	}
	return rule
}

// antigravityEffortList is effortList: a ladder of known efforts, else nothing.
func antigravityEffortList(value any) ([]string, bool) {
	raw, ok := value.([]string)
	if !ok {
		return nil, false
	}
	for _, effort := range raw {
		if !slicesContains(neoPiThinkingEfforts, effort) {
			return nil, false
		}
	}
	return raw, true
}

// antigravityResolveThinking is resolveThinkingPolicy for the
// google-gemini-cli wire.
func antigravityResolveThinking(
	spec antigravitySpec,
	identity antigravityIdentity,
	axes antigravityAxes,
	compat NeoPiGeminiCompat,
) (*NeoPiThinking, error) {
	rule := antigravityReadRuleThinking(axes.Thinking)
	var explicit *NeoPiThinking
	if spec.Thinking != nil && len(spec.Thinking.Efforts) > 0 {
		explicit = spec.Thinking
	}
	// Compat-time reasoning: the spec's own capability report, or a reviewed
	// cascade upgrade the matching contract opted into.
	compatReasoning := spec.Reasoning || (axes.Reasoning && rule.upgradeNeutral)
	unverified := !compatReasoning
	if unverified && (explicit != nil || identity.Class != "unknown" || !rule.unverified) {
		return nil, nil
	}
	if explicit != nil {
		return antigravityFillExplicitThinking(spec, identity, rule, explicit), nil
	}
	if rule.hasEfforts && len(rule.efforts) == 0 {
		return nil, nil
	}
	config := &NeoPiThinking{Mode: rule.mode, Efforts: rule.efforts}
	if config.Mode == "" {
		config.Mode = "budget"
	}
	if !rule.hasEfforts {
		config.Efforts = []string{"minimal", "low", "medium", "high"}
	}
	if len(config.Efforts) == 0 {
		return nil, errors.New("antigravity: model " + spec.ID + " resolved to an empty thinking range")
	}
	if effortMap := antigravityFilterEffortMap(rule.effortMap, config.Efforts); effortMap != nil {
		config.EffortMap = effortMap
	}
	if unverified {
		config.Unverified = true
		return config, nil
	}
	config.DefaultLevel = rule.defaultLevel
	config.EffortBudgets = rule.effortBudgets
	if rule.requiresEffort || antigravityImpliesMandatoryReasoning(identity, spec.ID) {
		config.RequiresEffort = true
	}
	config.SuppressWhenOff = rule.suppressWhenOff
	return config, nil
}

// antigravityFillExplicitThinking is fillExplicitThinking: explicit metadata
// wins, absent fields fill from the rules. NeoPi's `requiresEffort` and
// `suppressWhenOff` are only ever authored as true by the Antigravity rules
// and variant families, so a false here is an absent field.
func antigravityFillExplicitThinking(
	spec antigravitySpec,
	identity antigravityIdentity,
	rule antigravityRuleThinking,
	thinking *NeoPiThinking,
) *NeoPiThinking {
	out := *thinking
	out.Efforts = append([]string(nil), thinking.Efforts...)
	out.EffortRouting = nil
	for effort, wireID := range thinking.EffortRouting {
		if out.EffortRouting == nil {
			out.EffortRouting = make(map[string]string, len(thinking.EffortRouting))
		}
		out.EffortRouting[effort] = wireID
	}
	out.EffortMap = nil
	if thinking.EffortMap == nil {
		out.EffortMap = antigravityFilterEffortMap(rule.effortMap, thinking.Efforts)
	} else {
		out.EffortMap = thinking.EffortMap
	}
	if !out.RequiresEffort && (rule.requiresEffort || antigravityImpliesMandatoryReasoning(identity, spec.ID)) {
		out.RequiresEffort = true
	}
	if out.DefaultLevel == "" && rule.defaultLevel != "" {
		out.DefaultLevel = rule.defaultLevel
	}
	if out.EffortBudgets == nil && rule.effortBudgets != nil {
		out.EffortBudgets = rule.effortBudgets
	}
	return &out
}

// antigravityFilterEffortMap is filterEffortMap.
func antigravityFilterEffortMap(effortMap map[string]string, efforts []string) map[string]string {
	if effortMap == nil {
		return nil
	}
	var filtered map[string]string
	for _, effort := range efforts {
		mapped, ok := effortMap[effort]
		if !ok {
			continue
		}
		if filtered == nil {
			filtered = map[string]string{}
		}
		filtered[effort] = mapped
	}
	return filtered
}

// antigravityImpliesMandatoryReasoning is impliesMandatoryReasoning: a
// thinking-variant id names the reasoning-locked sibling.
func antigravityImpliesMandatoryReasoning(identity antigravityIdentity, modelID string) bool {
	if identity.ThinkingVariant {
		return true
	}
	_, orphan := antigravityStripThinkingSuffix(modelID)
	return orphan
}

// antigravityApplyCatalog is applyCatalogAssignments plus the limit and
// modality corrections of applyCatalogCorrections and capAntigravityWireProfiles.
func antigravityApplyCatalog(model *antigravityModel, catalog map[string]any) error {
	if catalog == nil {
		return nil
	}
	if kind, ok := catalog["kind"].(string); ok && antigravityModelKinds[kind] {
		model.Kind = kind
	}
	model.Cost = antigravityCatalogCost(catalog)
	if limits, ok := catalog["limitsPatch"].(map[string]any); ok {
		if value, ok := limits["contextWindow"]; ok {
			model.ContextWindow = int(antigravityNumber(value))
		}
		if value, ok := limits["maxTokens"]; ok {
			model.MaxTokens = int(antigravityNumber(value))
		}
	}
	if fallback, ok := catalog["maxTokensFallback"]; ok && model.MaxTokens == 0 {
		model.MaxTokens = int(antigravityNumber(fallback))
	}
	ceiling, hasCeiling := catalog["maxTokensCeiling"]
	if hasCeiling {
		if value := antigravityNumber(ceiling); model.MaxTokens > 0 && model.MaxTokens > int(value) {
			model.MaxTokens = int(value)
		}
		antigravityCapWireProfiles(model, int(antigravityNumber(ceiling)))
	}
	if floor, ok := catalog["contextWindowFloor"]; ok {
		if value := int(antigravityNumber(floor)); model.ContextWindow < value {
			model.ContextWindow = value
		}
	}
	if modalities, ok := catalog["inputModalities"].([]string); ok {
		model.Input = append([]string(nil), modalities...)
	}
	return nil
}

// antigravityCapWireProfiles is capAntigravityWireProfiles: the transport
// sends a profile's maxOutputTokens verbatim, so it takes the host ceiling too.
func antigravityCapWireProfiles(model *antigravityModel, ceiling int) {
	capped := false
	for _, profile := range model.WireProfiles {
		if profile.MaxOutputTokens > ceiling {
			capped = true
			break
		}
	}
	if !capped {
		return
	}
	profiles := make(map[string]NeoPiAntigravityWireProfile, len(model.WireProfiles))
	for wireID, profile := range model.WireProfiles {
		if profile.MaxOutputTokens > ceiling {
			profile.MaxOutputTokens = ceiling
		}
		profiles[wireID] = profile
	}
	model.WireProfiles = profiles
}

// antigravityCatalogCost is the `cost-patch`/`cost-fallback` legs of
// applyCatalogCorrections. Dated cards are not carried: they only ever change
// a non-zero price, and the thinking pair rule reads nothing but zero-ness.
func antigravityCatalogCost(catalog map[string]any) antigravityCost {
	var cost antigravityCost
	read := func(patch map[string]any) {
		for key, target := range map[string]*float64{
			"input": &cost.input, "output": &cost.output,
			"cacheRead": &cost.cacheRead, "cacheWrite": &cost.cacheWrite,
		} {
			if value, ok := patch[key]; ok {
				*target = antigravityNumber(value)
			}
		}
	}
	if patch, ok := catalog["costPatch"].(map[string]any); ok {
		read(patch)
	}
	if fallback, ok := catalog["costFallback"].(map[string]any); ok {
		if cost.input == 0 && cost.output == 0 && cost.cacheRead == 0 && cost.cacheWrite == 0 {
			read(fallback)
		}
	}
	return cost
}

func antigravityNumber(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	}
	return 0
}

func antigravityBool(value any) bool {
	typed, _ := value.(bool)
	return typed
}

// antigravityTrimTrailingSlashes is trimTrailingSlashes.
func antigravityTrimTrailingSlashes(value string) string {
	return strings.TrimRight(value, "/")
}
