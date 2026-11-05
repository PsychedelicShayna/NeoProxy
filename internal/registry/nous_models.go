package registry

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

// nousNeoPiJSON is how NeoPi resolves the models its Nous Portal extension
// registers, exported by internal/auth/nous/testdata/capture_neopi_nous_profiles.ts:
// the extension's bundled snapshot as resolved records, NeoPi's answer for
// unknown models per vendor namespace, and the extension's enrichment table.
//
//go:embed models/nous_neopi.json
var nousNeoPiJSON []byte

// NousInferenceURL is the base URL NeoPi registers the provider at.
const NousInferenceURL = "https://inference-api.nousresearch.com/v1"

// nousThinkingOrder is the extension's THINKING_EFFORTS_ORDER.
var nousThinkingOrder = []string{"minimal", "low", "medium", "high", "xhigh", "max"}

type nousProfile struct {
	Compat         *NeoPiCompletionsCompat `json:"compat"`
	RequiresEffort bool                    `json:"requiresEffort"`
}

// nousProfilePair names the profile of a non-reasoning and a reasoning
// model, and the effort ladder NeoPi's policy gives a reasoning model whose
// spec carries none.
type nousProfilePair struct {
	Off      string         `json:"off"`
	On       string         `json:"on"`
	Thinking *NeoPiThinking `json:"thinking"`
}

type nousEnrichment struct {
	Reasoning *bool `json:"reasoning"`
	// Thinking is nil when absent or null: the extension reads both as none.
	Thinking  *NeoPiThinking `json:"thinking"`
	Input     []string       `json:"input"`
	MaxTokens *int           `json:"maxTokens"`
}

type nousSnapshotModel struct {
	NeoPiModel
	ProfileID string `json:"profile"`
	// ThinkingAdded is what the policy adds to the row's own ladder.
	ThinkingAdded *NeoPiThinking `json:"thinkingAdded"`
}

var nousCatalog = func() *struct {
	profiles   map[string]nousProfile
	fallback   nousProfilePair
	namespaces map[string]nousProfilePair
	models     []nousSnapshotModel
	byID       map[string]*nousSnapshotModel
	enrichment map[string]nousEnrichment
} {
	var export struct {
		Profiles   map[string]nousProfile     `json:"profiles"`
		Default    nousProfilePair            `json:"default"`
		Namespaces map[string]nousProfilePair `json:"namespaces"`
		Models     []nousSnapshotModel        `json:"models"`
		Enrichment map[string]nousEnrichment  `json:"enrichment"`
	}
	if err := json.Unmarshal(nousNeoPiJSON, &export); err != nil {
		panic(fmt.Sprintf("registry: nous_neopi.json: %v", err))
	}
	catalog := &struct {
		profiles   map[string]nousProfile
		fallback   nousProfilePair
		namespaces map[string]nousProfilePair
		models     []nousSnapshotModel
		byID       map[string]*nousSnapshotModel
		enrichment map[string]nousEnrichment
	}{profiles: export.Profiles, fallback: export.Default, namespaces: export.Namespaces, models: export.Models, enrichment: export.Enrichment,
		byID: make(map[string]*nousSnapshotModel, len(export.Models))}
	for i := range catalog.models {
		catalog.byID[catalog.models[i].ID] = &catalog.models[i]
	}
	return catalog
}()

// NousBundledModels is NeoPi's bundled Nous Portal roster, which the
// extension serves until discovery answers, as registry models.
func NousBundledModels(ownedBy string) []*ModelInfo {
	out := make([]*ModelInfo, 0, len(nousCatalog.models))
	for i := range nousCatalog.models {
		record := nousCatalog.models[i].NeoPiModel.Clone()
		completeNousModel(record, nousCatalog.profiles[nousCatalog.models[i].ProfileID])
		out = append(out, neoPiExtensionModelInfo(record, ownedBy))
	}
	return out
}

// NousBundledNeoPiModel resolves an id against the bundled roster.
func NousBundledNeoPiModel(id string) *NeoPiModel {
	snapshot := nousCatalog.byID[id]
	if snapshot == nil {
		return nil
	}
	record := snapshot.NeoPiModel.Clone()
	completeNousModel(record, nousCatalog.profiles[snapshot.ProfileID])
	return record
}

func completeNousModel(model *NeoPiModel, profile nousProfile) {
	model.Provider, model.BaseURL, model.API = "nous-portal", NousInferenceURL, "openai-completions"
	if profile.Compat != nil {
		compat := *profile.Compat
		model.CompletionsCompat = &compat
	}
	if model.Thinking != nil && profile.RequiresEffort {
		model.Thinking.RequiresEffort = true
	}
}

// NousModels maps a /v1/models envelope's rows the way the extension's
// fetchDynamicModels does (mapRow over every row, ids unioned) and resolves
// each as NeoPi's catalog would.
func NousModels(rows []json.RawMessage, ownedBy string) []*ModelInfo {
	byID := make(map[string]*ModelInfo, len(rows))
	order := make([]string, 0, len(rows))
	for _, raw := range rows {
		model := NousMapRow(gjson.ParseBytes(raw))
		if model == nil {
			continue
		}
		if _, seen := byID[model.ID]; !seen {
			order = append(order, model.ID)
		}
		byID[model.ID] = neoPiExtensionModelInfo(model, ownedBy)
	}
	out := make([]*ModelInfo, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out
}

// NousMapRow is the extension's mapRow: one /v1/models row as the model NeoPi
// registers, or nil for a row it skips.
func NousMapRow(row gjson.Result) *NeoPiModel {
	if !row.IsObject() {
		return nil
	}
	id := nousString(row.Get("id"))
	if id == "" || strings.HasSuffix(id, ":batch") {
		return nil
	}
	if outputs := row.Get("architecture.output_modalities"); outputs.IsArray() {
		hasText := false
		for _, mode := range outputs.Array() {
			hasText = hasText || mode.Type == gjson.String && mode.String() == "text"
		}
		if !hasText {
			return nil
		}
	}
	extra := nousEnrichmentFor(id)
	contextWindow := nousPositiveInteger(row.Get("context_length"))
	if contextWindow == 0 {
		contextWindow = 131072
	}
	var supported []string
	for _, parameter := range row.Get("supported_parameters").Array() {
		if parameter.Type == gjson.String {
			supported = append(supported, parameter.String())
		}
	}
	reasoning := slices.Contains(supported, "reasoning") || extra.Reasoning != nil && *extra.Reasoning
	thinking := nousThinkingFromRow(row.Get("reasoning"))
	if thinking == nil {
		thinking = extra.Thinking.Clone()
	}
	input := []string{"text"}
	if modality := nousString(row.Get("architecture.modality")); strings.Contains(modality, "image") {
		input = []string{"text", "image"}
	} else if extra.Input != nil {
		input = slices.Clone(extra.Input)
	}
	maxTokens := nousPositiveInteger(row.Get("top_provider.max_completion_tokens"))
	if maxTokens == 0 && extra.MaxTokens != nil {
		maxTokens = nousPositiveInteger(gjson.ParseBytes([]byte(strconv.Itoa(*extra.MaxTokens))))
	}
	if maxTokens == 0 {
		maxTokens = min(65536, contextWindow/4)
	}
	name := nousString(row.Get("name"))
	if name == "" {
		name = id
	}
	name = cleanNeoPiModelName(name)
	model := &NeoPiModel{ID: id, Name: name, API: "openai-completions", Provider: "nous-portal", BaseURL: NousInferenceURL,
		Reasoning: reasoning, Input: input, ContextWindow: contextWindow, MaxTokens: maxTokens, Thinking: thinking}
	resolveNousModel(model)
	return model
}

// resolveNousModel applies NeoPi's catalog policy (buildModel) to a mapped
// row: the request compat and the effort ladder's resolved fields. A model of
// the bundled snapshot resolves as NeoPi resolved it; an unseen one as NeoPi
// resolves an unknown model of its vendor.
func resolveNousModel(model *NeoPiModel) {
	var profile nousProfile
	pair := nousCatalog.fallback
	if namespace := strings.SplitN(strings.TrimPrefix(model.ID, "~"), "/", 2); len(namespace) == 2 {
		if specific, ok := nousCatalog.namespaces[namespace[0]]; ok {
			pair = specific
		}
	}
	snapshot := nousCatalog.byID[model.ID]
	switch {
	case snapshot != nil:
		profile = nousCatalog.profiles[snapshot.ProfileID]
	case model.Reasoning:
		profile = nousCatalog.profiles[pair.On]
	default:
		profile = nousCatalog.profiles[pair.Off]
	}
	switch {
	case !model.Reasoning:
		model.Thinking = nil
	case model.Thinking == nil:
		if snapshot != nil && snapshot.Thinking != nil {
			model.Thinking = snapshot.Thinking.Clone()
		} else {
			model.Thinking = pair.Thinking.Clone()
		}
	case snapshot != nil && snapshot.ThinkingAdded != nil:
		if added := snapshot.ThinkingAdded; len(added.EffortMap) > 0 {
			model.Thinking.EffortMap = maps.Clone(added.EffortMap)
		}
		if model.Thinking.DefaultLevel == "" && slices.Contains(model.Thinking.Efforts, snapshot.ThinkingAdded.DefaultLevel) {
			model.Thinking.DefaultLevel = snapshot.ThinkingAdded.DefaultLevel
		}
	}
	completeNousModel(model, profile)
}

// neoPiExtensionModelInfo is one model a NeoPi provider extension registers
// as a registry model owned by ownedBy. Models that always think (requiresEffort)
// cannot be turned off.
func neoPiExtensionModelInfo(model *NeoPiModel, ownedBy string) *ModelInfo {
	info := &ModelInfo{ID: model.ID, MetadataModelID: model.ID, Object: "model", OwnedBy: ownedBy, Type: "openai-compatibility", Name: model.ID,
		DisplayName: model.Name, ContextLength: model.ContextWindow, MaxContextLength: model.ContextWindow, MaxCompletionTokens: model.MaxTokens,
		SupportedInputModalities: slices.Clone(model.Input), NeoPi: model.Clone()}
	if model.Reasoning && model.Thinking != nil && len(model.Thinking.Efforts) > 0 {
		info.Thinking = &ThinkingSupport{Levels: slices.Clone(model.Thinking.Efforts), ZeroAllowed: !model.Thinking.RequiresEffort}
	}
	return info
}

func nousEnrichmentFor(id string) nousEnrichment {
	if extra, ok := nousCatalog.enrichment[id]; ok {
		return extra
	}
	for _, suffix := range []string{":free", ":thinking"} {
		if base, ok := strings.CutSuffix(id, suffix); ok {
			return nousCatalog.enrichment[base]
		}
	}
	return nousEnrichment{}
}

// nousThinkingFromRow is thinkingFromRow: the row's supported efforts in the
// extension's order, with its default when that is one of them.
func nousThinkingFromRow(reasoning gjson.Result) *NeoPiThinking {
	var supported []string
	for _, effort := range reasoning.Get("supported_efforts").Array() {
		if effort.Type == gjson.String {
			supported = append(supported, effort.String())
		}
	}
	var efforts []string
	for _, effort := range nousThinkingOrder {
		if slices.Contains(supported, effort) {
			efforts = append(efforts, effort)
		}
	}
	if len(efforts) == 0 {
		return nil
	}
	thinking := &NeoPiThinking{Mode: "effort", Efforts: efforts}
	if def := nousString(reasoning.Get("default_effort")); slices.Contains(efforts, def) {
		thinking.DefaultLevel = def
	}
	return thinking
}

// nousString is the extension's string(): a non-blank string, trimmed.
func nousString(value gjson.Result) string {
	if value.Type != gjson.String {
		return ""
	}
	return strings.TrimSpace(value.String())
}

// nousPositiveInteger is positiveInteger(): a positive finite number or
// numeric string, floored; 0 when absent.
func nousPositiveInteger(value gjson.Result) int {
	var n float64
	switch value.Type {
	case gjson.Number:
		n = value.Float()
	case gjson.String:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(value.String()), 64)
		if err != nil {
			return 0
		}
		n = parsed
	default:
		return 0
	}
	if math.IsNaN(n) || math.IsInf(n, 0) || n <= 0 {
		return 0
	}
	return int(math.Floor(n))
}

var (
	// neoPiAuthorPrefix and neoPiNoiseTags are pi-catalog's cleanModelName
	// patterns: the gateway author prefix and model-extrinsic decorations.
	neoPiAuthorPrefix = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9 .+&'-]{0,23}: `)
	neoPiNoiseTags    = regexp.MustCompile(`\s*\((?:latest|Antigravity|\$+|>?\d+% off|retires [^)]*)\)`)
	neoPiSpaceRuns    = regexp.MustCompile(` {2,}`)
)

// cleanNeoPiModelName is pi-catalog's cleanModelName.
func cleanNeoPiModelName(name string) string {
	cleaned := strings.TrimSpace(neoPiSpaceRuns.ReplaceAllString(neoPiNoiseTags.ReplaceAllString(neoPiAuthorPrefix.ReplaceAllString(name, ""), ""), " "))
	if cleaned == "" {
		return name
	}
	return cleaned
}
