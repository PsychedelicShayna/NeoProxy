package registry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

// This file ports NeoPi's pi-catalog discovery/antigravity.ts
// (fetchAntigravityDiscoveryModels) and the merge the model manager applies
// afterwards: fetchAvailableModels normalization, the collapse pass, the
// bundled seed merge, NeoPi's authoritative-roster retention and
// collapseBuiltVariants.

const (
	// antigravityDefaultContextWindow and antigravityDefaultMaxTokens are
	// DEFAULT_CONTEXT_WINDOW / DEFAULT_MAX_TOKENS.
	antigravityDefaultContextWindow = 200_000
	antigravityDefaultMaxTokens     = 64_000
)

// antigravityDiscoveryDenylist is ANTIGRAVITY_DISCOVERY_DENYLIST.
var antigravityDiscoveryDenylist = map[string]bool{
	"chat_20706":     true,
	"chat_23310":     true,
	"gemini-2.5-pro": true,
}

// antigravityThinkingLevels is ANTIGRAVITY_THINKING_LEVELS: the roster's SKU
// thinking level → the wire's thinkingLevel.
var antigravityThinkingLevels = map[float64]string{1: "LOW", 2: "MEDIUM", 3: "HIGH"}

// antigravityAPIModel is AntigravityDiscoveryApiModel after schema validation:
// every field is present only when the payload carried the declared type.
type antigravityAPIModel struct {
	DisplayName        string
	HasDisplayName     bool
	SupportsImages     bool
	SupportsThinking   bool
	ThinkingBudget     float64
	HasThinkingBudget  bool
	ThinkingLevel      float64
	HasThinkingLevel   bool
	MaxTokens          float64
	HasMaxTokens       bool
	MaxOutputTokens    float64
	HasMaxOutputTokens bool
	Model              string
	IsInternal         bool
	Disabled           bool
}

// antigravityRoster is the parsed fetchAvailableModels payload.
type antigravityRoster struct {
	IDs    []string
	Models map[string]antigravityAPIModel
}

// antigravityParseRoster decodes a fetchAvailableModels body the way NeoPi's
// response schema does: a body that is not a JSON object (or array) is an
// invalid response; a models value that is not an object is an empty roster.
func antigravityParseRoster(payload []byte) (antigravityRoster, error) {
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		// NeoPi's response schema declares every key optional, so a JSON array
		// validates as an object with no keys: an empty roster, not a failure.
		return antigravityRoster{Models: map[string]antigravityAPIModel{}}, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	keys, raw, err := antigravityDecodeObject(decoder)
	if err != nil {
		return antigravityRoster{}, err
	}
	roster := antigravityRoster{Models: map[string]antigravityAPIModel{}}
	for _, key := range keys {
		if key != "models" {
			continue
		}
		modelKeys, modelRaw, err := antigravityDecodeObject(json.NewDecoder(bytes.NewReader(raw[key])))
		if err != nil {
			continue
		}
		for _, modelID := range modelKeys {
			fields, ok := antigravityParseAPIModel(modelRaw[modelID])
			if !ok {
				continue
			}
			roster.IDs = append(roster.IDs, modelID)
			roster.Models[modelID] = fields
		}
	}
	return roster, nil
}

// antigravityDecodeObject reads a JSON object's keys in NeoPi's iteration
// order: integer-like keys ascending, then insertion order.
func antigravityDecodeObject(decoder *json.Decoder) ([]string, map[string]json.RawMessage, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, nil, err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return nil, nil, fmt.Errorf("antigravity: discovery payload is not a JSON object")
	}
	var keys []string
	raw := map[string]json.RawMessage{}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, nil, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, nil, fmt.Errorf("antigravity: discovery payload has a non-string key")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, nil, err
		}
		if _, seen := raw[key]; !seen {
			keys = append(keys, key)
		}
		raw[key] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, nil, err
	}
	return antigravityOrderLikeJS(keys), raw, nil
}

// antigravityOrderLikeJS is JavaScript's own-property order for a JSON object:
// array-index keys ascending first, then the rest in insertion order.
func antigravityOrderLikeJS(keys []string) []string {
	indexKeys := make([]string, 0, len(keys))
	otherKeys := make([]string, 0, len(keys))
	for _, key := range keys {
		if antigravityIsArrayIndex(key) {
			indexKeys = append(indexKeys, key)
		} else {
			otherKeys = append(otherKeys, key)
		}
	}
	sort.SliceStable(indexKeys, func(i, j int) bool {
		left, _ := strconv.ParseUint(indexKeys[i], 10, 64)
		right, _ := strconv.ParseUint(indexKeys[j], 10, 64)
		return left < right
	})
	return append(indexKeys, otherKeys...)
}

func antigravityIsArrayIndex(key string) bool {
	if key == "" || len(key) > 10 {
		return false
	}
	if key == "0" {
		return true
	}
	if key[0] < '1' || key[0] > '9' {
		return false
	}
	for i := 1; i < len(key); i++ {
		if key[i] < '0' || key[i] > '9' {
			return false
		}
	}
	value, err := strconv.ParseUint(key, 10, 64)
	return err == nil && value < 1<<32-1
}

// antigravityParseAPIModel is AntigravityDiscoveryApiModelSchema: only fields
// of the declared type survive, everything else is dropped.
func antigravityParseAPIModel(raw json.RawMessage) (antigravityAPIModel, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	keys, values, err := antigravityDecodeObject(decoder)
	if err != nil {
		return antigravityAPIModel{}, false
	}
	var model antigravityAPIModel
	for _, key := range keys {
		value := values[key]
		switch key {
		case "displayName":
			model.DisplayName, model.HasDisplayName = antigravityStringField(value)
		case "supportsImages":
			model.SupportsImages, _ = antigravityBoolField(value)
		case "supportsThinking":
			model.SupportsThinking, _ = antigravityBoolField(value)
		case "thinkingBudget":
			model.ThinkingBudget, model.HasThinkingBudget = antigravityNumberField(value)
		case "thinkingLevel":
			model.ThinkingLevel, model.HasThinkingLevel = antigravityNumberField(value)
		case "maxTokens":
			model.MaxTokens, model.HasMaxTokens = antigravityNumberField(value)
		case "maxOutputTokens":
			model.MaxOutputTokens, model.HasMaxOutputTokens = antigravityNumberField(value)
		case "model":
			model.Model, _ = antigravityStringField(value)
		case "isInternal":
			model.IsInternal, _ = antigravityBoolField(value)
		case "disabled":
			model.Disabled, _ = antigravityBoolField(value)
		}
	}
	return model, true
}

func antigravityStringField(raw json.RawMessage) (string, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '"' {
		return "", false
	}
	var text string
	if err := json.Unmarshal(trimmed, &text); err != nil {
		return "", false
	}
	return text, true
}

func antigravityBoolField(raw json.RawMessage) (bool, bool) {
	switch string(bytes.TrimSpace(raw)) {
	case "true":
		return true, true
	case "false":
		return false, true
	}
	return false, false
}

func antigravityNumberField(raw json.RawMessage) (float64, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || (trimmed[0] != '-' && (trimmed[0] < '0' || trimmed[0] > '9')) {
		return 0, false
	}
	var number json.Number
	if err := json.Unmarshal(trimmed, &number); err != nil {
		return 0, false
	}
	value, err := number.Float64()
	if err != nil {
		return 0, false
	}
	return value, true
}

// antigravityPositiveNumber is toPositiveNumber: a finite positive value, else
// the fallback.
func antigravityPositiveNumber(value float64, present bool, fallback int) int {
	if !present || !(value > 0) {
		return fallback
	}
	return int(value)
}

// antigravityWireProfileOf is antigravityWireProfileOf: the request constants
// the native client derives from one roster row.
func antigravityWireProfileOf(model antigravityAPIModel) (NeoPiAntigravityWireProfile, bool) {
	var profile NeoPiAntigravityWireProfile
	empty := true
	if model.Model != "" {
		profile.ModelEnum, empty = model.Model, false
	}
	if model.HasMaxOutputTokens && model.MaxOutputTokens > 0 {
		profile.MaxOutputTokens, empty = int(model.MaxOutputTokens), false
	}
	if model.HasThinkingLevel {
		if level, ok := antigravityThinkingLevels[model.ThinkingLevel]; ok {
			profile.ThinkingLevel, empty = level, false
		}
	}
	if model.HasThinkingBudget {
		budget := int(model.ThinkingBudget)
		profile.ThinkingBudget, empty = &budget, false
	}
	return profile, !empty
}

// antigravityNormalizeRoster is fetchAntigravityDiscoveryModels' spec
// normalization: the denylist, internal and unavailable rows dropped, then
// the effort-tier collapse.
func antigravityNormalizeRoster(roster antigravityRoster, endpoint string) ([]antigravityModel, error) {
	specs := make([]antigravityModel, 0, len(roster.IDs))
	for _, modelID := range roster.IDs {
		model := roster.Models[modelID]
		if antigravityDiscoveryDenylist[modelID] || model.IsInternal || model.Disabled {
			continue
		}
		supportsImages := model.SupportsImages
		spec := antigravityModel{antigravitySpec: antigravitySpec{
			ID:            modelID,
			Name:          modelID,
			BaseURL:       endpoint,
			Reasoning:     model.SupportsThinking,
			Input:         []string{"text"},
			ContextWindow: antigravityPositiveNumber(model.MaxTokens, model.HasMaxTokens, antigravityDefaultContextWindow),
			MaxTokens:     antigravityPositiveNumber(model.MaxOutputTokens, model.HasMaxOutputTokens, antigravityDefaultMaxTokens),
		}}
		if model.HasDisplayName && model.DisplayName != "" {
			spec.Name = model.DisplayName
		}
		if supportsImages {
			spec.Input = []string{"text", "image"}
		}
		if profile, ok := antigravityWireProfileOf(model); ok {
			spec.WireProfiles = map[string]NeoPiAntigravityWireProfile{modelID: profile}
		}
		specs = append(specs, spec)
	}
	collapsed, err := antigravityCollapseVariants(specs)
	if err != nil {
		return nil, err
	}
	sortAntigravityModels(collapsed)
	return collapsed, nil
}

// antigravityCollator is NeoPi's `name.localeCompare` ordering. Bun runs the
// default (English) collation, so the port sorts names the same way.
var antigravityCollator = collate.New(language.English)

// sortAntigravityModels is `collapsed.sort((a, b) => a.name.localeCompare(b.name)
// || a.id.localeCompare(b.id))`.
func sortAntigravityModels(models []antigravityModel) {
	sort.SliceStable(models, func(i, j int) bool {
		if order := antigravityCollator.CompareString(models[i].Name, models[j].Name); order != 0 {
			return order < 0
		}
		return antigravityCollator.CompareString(models[i].ID, models[j].ID) < 0
	})
}

// antigravityExcluded is isExcludedModel("google-antigravity", id).
func antigravityExcluded(modelID string) bool {
	lower := asciiLower(modelID)
	for _, rule := range antigravityRules.ExcludeModels {
		if rule.Provider != antigravityProvider {
			continue
		}
		for _, candidate := range rule.Match.Exact {
			if candidate == lower {
				return true
			}
		}
		for _, prefix := range rule.Match.Prefix {
			if strings.HasPrefix(lower, prefix) {
				return true
			}
		}
		for _, substring := range rule.Match.Substring {
			if strings.Contains(lower, substring) {
				return true
			}
		}
		if len(rule.Match.Token) > 0 && slicesContains(rule.Match.Token, lower) {
			return true
		}
		for _, glob := range rule.Match.Glob {
			if antigravityGlobMatch(glob, lower) {
				return true
			}
		}
	}
	return false
}

// antigravityBuildSpecs is buildDiscoveredModelSpecs: buildModel per row,
// skipping (and reporting) rows NeoPi's rules reject.
func antigravityBuildSpecs(specs []antigravityModel) ([]antigravityModel, error) {
	built := make([]antigravityModel, 0, len(specs))
	var firstErr error
	failures := 0
	for _, spec := range specs {
		model, err := antigravityBuildModel(spec.antigravitySpec)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			failures++
			continue
		}
		built = append(built, model)
	}
	if len(built) == 0 && failures > 0 {
		return nil, firstErr
	}
	return built, nil
}

// antigravityMergeDynamic is mergeDynamicModel: a live roster row's fields
// replace the bundled reference's, except where the bundled row is the only
// source of a capability.
func antigravityMergeDynamic(existing, dynamic antigravityModel) antigravityModel {
	merged := existing
	merged.Name = antigravityPreferDiscoveryName(dynamic.Name, existing.Name, dynamic.ID)
	merged.BaseURL = dynamic.BaseURL
	merged.Reasoning = existing.Reasoning || dynamic.Reasoning
	endpointChanged := existing.BaseURL != dynamic.BaseURL
	if endpointChanged {
		merged.Input = append([]string(nil), dynamic.Input...)
	} else {
		merged.Input = antigravityUnionInput(existing.Input, dynamic.Input)
	}
	merged.ContextWindow = antigravityPreferDiscoveryLimit(dynamic.ContextWindow, existing.ContextWindow)
	merged.MaxTokens = antigravityPreferDiscoveryLimit(dynamic.MaxTokens, existing.MaxTokens)
	merged.Cost = antigravityCost{
		input:      antigravityPreferDiscoveryCost(dynamic.Cost.input, existing.Cost.input),
		output:     antigravityPreferDiscoveryCost(dynamic.Cost.output, existing.Cost.output),
		cacheRead:  antigravityPreferDiscoveryCost(dynamic.Cost.cacheRead, existing.Cost.cacheRead),
		cacheWrite: antigravityPreferDiscoveryCost(dynamic.Cost.cacheWrite, existing.Cost.cacheWrite),
	}
	// A built row always states its thinking and its wire profiles (possibly as
	// absent), so the live row owns both. A default wire id and a catalog kind
	// are stated only when present: the bundled reference keeps its own until
	// discovery supplies one.
	merged.Thinking = dynamic.Thinking
	merged.WireProfiles = dynamic.WireProfiles
	if dynamic.HasRequestModelID {
		merged.RequestModelID, merged.HasRequestModelID = dynamic.RequestModelID, true
	}
	if dynamic.Kind != "" {
		merged.Kind = dynamic.Kind
	}
	return merged
}

func antigravityUnionInput(left, right []string) []string {
	out := make([]string, 0, 2)
	for _, modality := range []string{"text", "image"} {
		if slicesContains(left, modality) || slicesContains(right, modality) {
			out = append(out, modality)
		}
	}
	return out
}

// antigravityPreferDiscoveryName is preferDiscoveryName.
func antigravityPreferDiscoveryName(discoveryName, fallbackName, modelID string) string {
	trimmed := strings.TrimSpace(discoveryName)
	if trimmed == "" {
		return fallbackName
	}
	if trimmed == modelID && fallbackName != modelID {
		return fallbackName
	}
	return trimmed
}

// antigravityPreferDiscoveryCost is preferDiscoveryCost: a zero discovery
// price means "not reported", so the bundled rate wins.
func antigravityPreferDiscoveryCost(discovery, fallback float64) float64 {
	if discovery > 0 {
		return discovery
	}
	return fallback
}

// antigravityPreferDiscoveryLimit is preferDiscoveryLimit.
func antigravityPreferDiscoveryLimit(discovery, fallback int) int {
	if discovery <= 0 {
		return fallback
	}
	if discovery == 4096 && fallback > discovery {
		return fallback
	}
	return discovery
}

// antigravityMergeSeed is mergeDynamicModels(bundled, discovered): bundled
// order first, then ids only discovery lists. A non-chat bundled row is an
// authored runner and is never replaced by a discovered chat row.
func antigravityMergeSeed(seed, discovered []antigravityModel) []antigravityModel {
	if len(discovered) == 0 {
		return append([]antigravityModel(nil), seed...)
	}
	if len(seed) == 0 {
		return append([]antigravityModel(nil), discovered...)
	}
	positions := make(map[string]int, len(seed))
	out := make([]antigravityModel, 0, len(seed)+len(discovered))
	for _, model := range seed {
		positions[model.ID] = len(out)
		out = append(out, model)
	}
	for _, model := range discovered {
		index, ok := positions[model.ID]
		if !ok {
			positions[model.ID] = len(out)
			out = append(out, model)
			continue
		}
		if out[index].Kind != "" && out[index].Kind != "chat" {
			continue
		}
		out[index] = antigravityMergeDynamic(out[index], model)
	}
	return out
}

// antigravityRetainDiscovered is retainModelIds: a non-chat row survives
// regardless, a chat row only when the authoritative roster lists it.
func antigravityRetainDiscovered(models, discovered []antigravityModel) []antigravityModel {
	retained := make(map[string]bool, len(discovered))
	for _, model := range discovered {
		retained[model.ID] = true
	}
	out := make([]antigravityModel, 0, len(models))
	for _, model := range models {
		if (model.Kind != "" && model.Kind != "chat") || retained[model.ID] {
			out = append(out, model)
		}
	}
	return out
}

// antigravityRebuild is collapseBuiltVariants: re-run buildModel over every
// freshly created logical row so its thinking and limits are resolved from the
// collapse's routing.
func antigravityRebuild(models []antigravityModel) ([]antigravityModel, error) {
	out := make([]antigravityModel, 0, len(models))
	for _, model := range models {
		rebuilt, err := antigravityBuildModel(model.antigravitySpec)
		if err != nil {
			return nil, err
		}
		out = append(out, rebuilt)
	}
	return out, nil
}

// AntigravityResolvedRoster is the whole pipeline for one account: NeoPi's
// discovery normalization and collapse, the bundled seed merge, authoritative
// retention and the post-merge collapse. It returns chat rows only: image and
// other non-chat SKUs are not served by the Cloud Code Assist chat executor.
func antigravityResolvedRoster(payload []byte, endpoint string) ([]antigravityModel, error) {
	rows, err := antigravityResolvedRosterRows(payload, endpoint)
	if err != nil {
		return nil, err
	}
	out := make([]antigravityModel, 0, len(rows))
	for _, model := range rows {
		if model.Kind == "" || model.Kind == "chat" {
			out = append(out, model)
		}
	}
	return out, nil
}

// antigravityResolvedRosterRows is the pipeline's output for every kind: the
// chat rows the chat executor serves and the image rows the Images API serves.
func antigravityResolvedRosterRows(payload []byte, endpoint string) ([]antigravityModel, error) {
	roster, err := antigravityParseRoster(payload)
	if err != nil {
		return nil, err
	}
	discovered, err := antigravityNormalizeRoster(roster, endpoint)
	if err != nil {
		return nil, err
	}
	kept := make([]antigravityModel, 0, len(discovered))
	for _, model := range discovered {
		if antigravityExcluded(model.ID) {
			continue
		}
		kept = append(kept, model)
	}
	built, err := antigravityBuildSpecs(kept)
	if err != nil {
		return nil, err
	}
	seed, err := antigravityBundledRows()
	if err != nil {
		return nil, err
	}
	merged := antigravityRetainDiscovered(antigravityMergeSeed(seed, built), built)
	collapsed, err := antigravityCollapseVariants(merged)
	if err != nil {
		return nil, err
	}
	rebuilt, err := antigravityRebuild(collapsed)
	if err != nil {
		return nil, err
	}
	return rebuilt, nil
}

// AntigravityDiscoveredModels is NeoPi's roster for one account as registry
// models. payload is a fetchAvailableModels response body and endpoint the
// host that answered it (the rows' base URL).
func AntigravityDiscoveredModels(payload []byte, endpoint string) ([]*ModelInfo, error) {
	rows, err := antigravityResolvedRosterRows(payload, endpoint)
	if err != nil {
		return nil, err
	}
	out := make([]*ModelInfo, 0, len(rows))
	for i := range rows {
		if rows[i].Kind == "" || rows[i].Kind == "chat" {
			out = append(out, AntigravityModelInfo(antigravityRecord(&rows[i])))
		}
	}
	// NeoPi serves its image SKUs through generateAntigravityImage.
	return append(out, antigravityImageInfos(rows)...), nil
}

// antigravityRecord is a built row as the NeoPiModel record the registry and
// the executor carry.
func antigravityRecord(model *antigravityModel) *NeoPiModel {
	compat := model.Compat
	compat.IdentityClass = model.IdentityClass
	record := &NeoPiModel{
		ID:            model.ID,
		Name:          antigravityCleanName(model.Name),
		API:           antigravityAPI,
		Provider:      antigravityProvider,
		BaseURL:       model.BaseURL,
		Reasoning:     model.Reasoning,
		Input:         append([]string(nil), model.Input...),
		ContextWindow: model.ContextWindow,
		MaxTokens:     model.MaxTokens,
		Thinking:      model.Thinking.Clone(),
		GeminiCompat:  &compat,
	}
	if model.HasRequestModelID && model.RequestModelID != "" {
		record.RequestModelID = model.RequestModelID
	}
	if len(model.WireProfiles) > 0 {
		record.AntigravityWireProfiles = make(map[string]NeoPiAntigravityWireProfile, len(model.WireProfiles))
		for wireID, profile := range model.WireProfiles {
			if profile.ThinkingBudget != nil {
				budget := *profile.ThinkingBudget
				profile.ThinkingBudget = &budget
			}
			record.AntigravityWireProfiles[wireID] = profile
		}
	}
	return record
}

// antigravityCleanName is cleanModelName: the gateway author prefix and
// model-extrinsic decorations dropped, never an empty name.
func antigravityCleanName(name string) string {
	cleaned := antigravityAuthorPrefix.ReplaceAllString(name, "")
	cleaned = antigravityNoiseTags.ReplaceAllString(cleaned, "")
	cleaned = strings.Join(strings.Fields(cleaned), " ")
	if cleaned == "" {
		return name
	}
	return cleaned
}

var (
	// antigravityAuthorPrefix is AUTHOR_PREFIX.
	antigravityAuthorPrefix = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9 .+&'-]{0,23}: `)
	// antigravityNoiseTags is NOISE_TAGS.
	antigravityNoiseTags = regexp.MustCompile(`\s*\((?:latest|Antigravity|\$+|>?\d+% off|retires [^)]*)\)`)
)
