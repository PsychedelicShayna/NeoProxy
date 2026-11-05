package registry

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/tidwall/gjson"
)

// openRouterNeoPiGZ is NeoPi's resolved OpenRouter chat catalog (the `openrouter`
// provider's bundled rows on api "openrouter"), exported by
// runtime/executor/testdata/capture_openrouter_npi_profiles.ts.
//
//go:embed models/openrouter_neopi.json.gz
var openRouterNeoPiGZ []byte

// OpenRouterDefaultBaseURL is NeoPi's OpenRouter endpoint.
const OpenRouterDefaultBaseURL = "https://openrouter.ai/api/v1"

var openRouterBundled = sync.OnceValue(func() []NeoPiModel {
	reader, err := gzip.NewReader(bytes.NewReader(openRouterNeoPiGZ))
	if err != nil {
		panic(fmt.Sprintf("registry: openrouter_neopi.json.gz: %v", err))
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		panic(fmt.Sprintf("registry: openrouter_neopi.json.gz: %v", err))
	}
	var export struct {
		Models     []NeoPiModel `json:"models"`
		NonChatIDs []string     `json:"nonChatIds"`
	}
	if err = json.Unmarshal(data, &export); err != nil {
		panic(fmt.Sprintf("registry: openrouter_neopi.json.gz: %v", err))
	}
	openRouterNonChatIDs = make(map[string]bool, len(export.NonChatIDs))
	for _, id := range export.NonChatIDs {
		openRouterNonChatIDs[id] = true
	}
	return export.Models
})

// openRouterNonChatIDs are the bundle's runner rows (image, video, embedding,
// rerank, decisions, transcription): NeoPi's merge never replaces them with a
// discovered chat row.
var openRouterNonChatIDs = map[string]bool{}

// IsOpenRouterBaseURL reports whether rawURL points at OpenRouter's API host,
// which NeoPi detects by host (hosts.ts urlMarkers "openrouter.ai").
func IsOpenRouterBaseURL(rawURL string) bool {
	return strings.Contains(strings.ToLower(rawURL), "openrouter.ai")
}

func (m NeoPiModel) openRouterModelInfo(ownedBy string) *ModelInfo {
	model := m.Clone()
	info := &ModelInfo{ID: model.ID, MetadataModelID: model.ID, Object: "model", OwnedBy: ownedBy, Type: "openai-compatibility",
		DisplayName: model.Name, ContextLength: model.ContextWindow, MaxContextLength: model.ContextWindow, MaxCompletionTokens: model.MaxTokens,
		SupportedInputModalities: slices.Clone(model.Input), NeoPi: model}
	if model.Reasoning && model.Thinking != nil {
		info.Thinking = &ThinkingSupport{Levels: slices.Clone(model.Thinking.Efforts), ZeroAllowed: !model.Thinking.RequiresEffort}
	}
	return info
}

// OpenRouterBundledModels is NeoPi's bundled OpenRouter chat catalog as
// registry models owned by one account's openai-compatibility entry. NeoPi
// serves it until discovery answers.
func OpenRouterBundledModels(ownedBy string) []*ModelInfo {
	bundled := openRouterBundled()
	out := make([]*ModelInfo, 0, len(bundled))
	for i := range bundled {
		out = append(out, bundled[i].openRouterModelInfo(ownedBy))
	}
	return out
}

// OpenRouterNeoPiModel resolves a chat model id to NeoPi's bundled record; nil
// when the bundle lacks it (a discovered id resolves through the account's
// registered model instead).
func OpenRouterNeoPiModel(id string) *NeoPiModel {
	bundled := openRouterBundled()
	for i := range bundled {
		if bundled[i].ID == id {
			return bundled[i].Clone()
		}
	}
	return nil
}

// authorPrefix, noiseTags and cleanModelName port catalog/src/utils.ts: the
// gateway author prefix and model-extrinsic decorations buildModel drops.
var (
	authorPrefix = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9 .+&'-]{0,23}: `)
	noiseTags    = regexp.MustCompile(`\s*\((?:latest|Antigravity|\$+|>?\d+% off|retires [^)]*)\)`)
	multiSpace   = regexp.MustCompile(` {2,}`)
)

func cleanModelName(name string) string {
	cleaned := strings.TrimSpace(multiSpace.ReplaceAllString(noiseTags.ReplaceAllString(authorPrefix.ReplaceAllString(name, ""), ""), " "))
	if cleaned == "" {
		return name
	}
	return cleaned
}

// openRouterThinkingEfforts is THINKING_EFFORTS, the ladder order of NeoPi's
// effort axis.
var openRouterThinkingEfforts = []string{"minimal", "low", "medium", "high", "xhigh", "max"}

// openRouterDefaultEfforts is the ladder buildModel gives a reasoning row whose
// discovery reports none.
var openRouterDefaultEfforts = []string{"minimal", "low", "medium", "high"}

func toPositiveNumber(value gjson.Result) (int, bool) {
	var parsed float64
	switch value.Type {
	case gjson.Number:
		parsed = value.Float()
	case gjson.String:
		text := strings.TrimSpace(value.String())
		if text == "" {
			return 0, false
		}
		number, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return 0, false
		}
		parsed = number
	default:
		return 0, false
	}
	if parsed > 0 {
		return int(parsed), true
	}
	return 0, false
}

// perMillion is parseFloat(String(x ?? "0")) * 1_000_000.
func perMillion(value gjson.Result) float64 {
	if !value.Exists() || value.Type == gjson.Null {
		return 0
	}
	number, err := strconv.ParseFloat(strings.TrimSpace(value.String()), 64)
	if err != nil {
		return 0
	}
	return number * 1_000_000
}

// OpenRouterDiscovered maps a live GET /models roster the way NeoPi does
// (openrouterModelManagerOptions' chat fetch, then resolveProviderModels'
// merge with the bundle): rows that advertise tool support become chat
// models, merged over the bundled row of the same id; bundled ids the roster
// omits stay (the provider's discovery is not authoritative).
func OpenRouterDiscovered(roster []byte, ownedBy string) ([]*ModelInfo, error) {
	entries := gjson.GetBytes(roster, "data")
	if !entries.IsArray() {
		return nil, fmt.Errorf("openrouter: /models response has no data array")
	}
	bundled := openRouterBundled()
	byID := make(map[string]*NeoPiModel, len(bundled))
	for i := range bundled {
		byID[bundled[i].ID] = &bundled[i]
	}
	var order []string
	merged := make(map[string]*NeoPiModel, len(bundled))
	for i := range bundled {
		order = append(order, bundled[i].ID)
		merged[bundled[i].ID] = &bundled[i]
	}
	listed := map[string]bool{}
	for _, entry := range entries.Array() {
		id := entry.Get("id")
		if id.Type != gjson.String || id.String() == "" {
			continue
		}
		params := entry.Get("supported_parameters")
		if !params.IsArray() || !jsonArrayHas(params, "tools") {
			continue
		}
		listed[id.String()] = true
		if openRouterNonChatIDs[id.String()] {
			continue
		}
		reference := byID[id.String()]
		row := openRouterDiscoveredRow(entry, reference)
		if existing := merged[row.ID]; existing != nil {
			merged[row.ID] = mergeOpenRouterRow(existing, row, entry)
			continue
		}
		order = append(order, row.ID)
		merged[row.ID] = openRouterNewRow(row, entry, bundled)
	}
	out := make([]*ModelInfo, 0, len(order))
	for _, id := range order {
		out = append(out, merged[id].openRouterModelInfo(ownedBy))
	}
	return out, nil
}

func jsonArrayHas(array gjson.Result, want string) bool {
	for _, item := range array.Array() {
		if item.Type == gjson.String && item.String() == want {
			return true
		}
	}
	return false
}

// openRouterDiscoveredRow is the chat spec openrouterModelManagerOptions maps a
// roster entry to (mapWithBundledReference plus the row's own fields).
func openRouterDiscoveredRow(entry gjson.Result, reference *NeoPiModel) *NeoPiModel {
	id := entry.Get("id").String()
	params := entry.Get("supported_parameters")
	name := strings.TrimSpace(entry.Get("name").String())
	if name == "" {
		if reference != nil {
			name = reference.Name
		} else {
			name = id
		}
	}
	row := &NeoPiModel{ID: id, Name: name, API: "openrouter", Provider: "openrouter", BaseURL: OpenRouterDefaultBaseURL, Reasoning: jsonArrayHas(params, "reasoning")}
	if reference != nil {
		row.ContextWindow, row.MaxTokens = reference.ContextWindow, reference.MaxTokens
	}
	if value, ok := toPositiveNumber(entry.Get("context_length")); ok {
		row.ContextWindow = value
	}
	if value, ok := toPositiveNumber(entry.Get("top_provider.max_completion_tokens")); ok {
		row.MaxTokens = value
	}
	if modalities := entry.Get("architecture.input_modalities"); modalities.IsArray() {
		row.Input = []string{"text"}
		if jsonArrayHas(modalities, "image") {
			row.Input = []string{"text", "image"}
		}
	} else if strings.Contains(entry.Get("architecture.modality").String(), "image") {
		row.Input = []string{"text", "image"}
	} else {
		row.Input = []string{"text"}
	}
	row.Thinking = openRouterThinking(entry)
	return row
}

// openRouterThinking is mapOpenRouterThinking.
func openRouterThinking(entry gjson.Result) *NeoPiThinking {
	supported := entry.Get("reasoning.supported_efforts")
	if !supported.IsArray() {
		return nil
	}
	var efforts []string
	for _, effort := range openRouterThinkingEfforts {
		if jsonArrayHas(supported, effort) {
			efforts = append(efforts, effort)
		}
	}
	if len(efforts) == 0 {
		return nil
	}
	thinking := &NeoPiThinking{Efforts: efforts, Mode: "effort"}
	if level := entry.Get("reasoning.default_effort"); level.Type == gjson.String && slices.Contains(efforts, level.String()) {
		thinking.DefaultLevel = level.String()
	}
	if entry.Get("reasoning.mandatory").Type == gjson.True {
		thinking.RequiresEffort = true
	}
	return thinking
}

// preferDiscoveryLimit keeps a discovery limit unless it is unusable or the
// 4096 placeholder under a larger known limit.
func preferDiscoveryLimit(discovery, fallback int) int {
	if discovery <= 0 {
		return fallback
	}
	if discovery == 4096 && fallback > discovery {
		return fallback
	}
	return discovery
}

// mergeOpenRouterRow is mergeDynamicModel with no authoritative axes over the
// bundled row: the discovery name and limits win, costless fields keep the
// bundle, input and reasoning are additive.
func mergeOpenRouterRow(existing, row *NeoPiModel, entry gjson.Result) *NeoPiModel {
	out := existing.Clone()
	name := strings.TrimSpace(row.Name)
	if name != "" && !(name == row.ID && existing.Name != row.ID) {
		out.Name = cleanModelName(name)
	}
	out.Reasoning = existing.Reasoning || row.Reasoning
	if slices.Contains(existing.Input, "image") || slices.Contains(row.Input, "image") {
		out.Input = []string{"text", "image"}
	} else {
		out.Input = []string{"text"}
	}
	out.ContextWindow = preferDiscoveryLimit(row.ContextWindow, existing.ContextWindow)
	out.MaxTokens = preferDiscoveryLimit(row.MaxTokens, existing.MaxTokens)
	// The row's ladder is a catalog fact buildModel re-derives from model
	// identity, so the bundled row's class-derived thinking stands; discovery
	// only contributes the ladder it reports and any mandatory-reasoning flag.
	switch {
	case existing.Thinking != nil:
		thinking := *existing.Thinking.Clone()
		if row.Thinking != nil {
			if len(row.Thinking.Efforts) > 0 {
				thinking.Efforts = row.Thinking.Efforts
			}
			if row.Thinking.DefaultLevel != "" {
				thinking.DefaultLevel = row.Thinking.DefaultLevel
			}
			thinking.RequiresEffort = thinking.RequiresEffort || row.Thinking.RequiresEffort
		}
		out.Thinking = &thinking
	case row.Thinking != nil:
		out.Thinking = row.Thinking
	case out.Reasoning:
		out.Thinking = &NeoPiThinking{Efforts: slices.Clone(openRouterDefaultEfforts), Mode: "effort"}
	}
	if out.Compat != nil {
		// OpenRouter's Responses route demands the reasoning item back on every
		// reasoning turn that calls a tool (resolveOpenAIResponsesPolicy).
		out.Compat.RequiresReasoningContentForToolCalls = out.Reasoning
		// DeepSeek-family reasoning turns must carry real reasoning, not the
		// synthetic placeholder (resolveOpenAIResponsesPolicy).
		if out.Reasoning && strings.HasPrefix(out.ID, "deepseek/") {
			out.Compat.AllowsSyntheticReasoningContentForToolCalls = false
		}
		if !jsonArrayHas(entry.Get("supported_parameters"), "tool_choice") {
			out.Compat.SupportsToolChoice = false
		}
	}
	return out
}

// openRouterNewRow resolves compat for an id the bundle lacks. NeoPi's
// buildModel derives it from the model's identity class; the closest bundled
// sibling of the same vendor stands in for that rule tree. [INFERENCE]
func openRouterNewRow(row *NeoPiModel, entry gjson.Result, bundled []NeoPiModel) *NeoPiModel {
	out := *row
	out.Name = cleanModelName(row.Name)
	sibling := openRouterSibling(row.ID, bundled)
	if sibling == nil {
		return &out
	}
	siblingRow := sibling.Clone()
	out.Compat = siblingRow.Compat
	if out.Reasoning && out.Thinking == nil {
		out.Thinking = &NeoPiThinking{Efforts: slices.Clone(openRouterDefaultEfforts), Mode: "effort"}
	}
	if out.Thinking != nil && siblingRow.Thinking != nil {
		out.Thinking.EffortMap = siblingRow.Thinking.EffortMap
		out.Thinking.PrefixBinding = siblingRow.Thinking.PrefixBinding
		// Mandatory reasoning is a lineage rule (Kimi K3 always thinks): an id
		// that extends its sibling's id ("kimi-k3" -> "kimi-k3.1-pro") keeps
		// the sibling's requirement even when the listing reports mandatory
		// false. A neighbour of another line (gpt-6.1-sol / -terra) does not.
		if extendsLineage(row.ID, sibling.ID) {
			out.Thinking.RequiresEffort = out.Thinking.RequiresEffort || siblingRow.Thinking.RequiresEffort
		}
	}
	// A thinking-variant id names the reasoning-locked sibling (NeoPi's
	// impliesMandatoryReasoning), so its effort cannot be turned off.
	if out.Thinking != nil && impliesMandatoryReasoning(row.ID) {
		out.Thinking.RequiresEffort = true
	}
	out.Compat.RequiresReasoningContentForToolCalls = out.Reasoning
	if !jsonArrayHas(entry.Get("supported_parameters"), "tool_choice") {
		out.Compat.SupportsToolChoice = false
	}
	return &out
}

// openRouterSibling is the bundled chat row sharing the longest id prefix
// with id inside the same vendor namespace.
func openRouterSibling(id string, bundled []NeoPiModel) *NeoPiModel {
	vendor, _, _ := strings.Cut(id, "/")
	var best *NeoPiModel
	bestLen := -1
	for i := range bundled {
		candidate := &bundled[i]
		if candidate.Compat == nil {
			continue
		}
		candidateVendor, _, _ := strings.Cut(candidate.ID, "/")
		if candidateVendor != vendor {
			continue
		}
		shared := 0
		for shared < len(id) && shared < len(candidate.ID) && id[shared] == candidate.ID[shared] {
			shared++
		}
		if shared > bestLen {
			best, bestLen = candidate, shared
		}
	}
	if best == nil {
		// No sibling in the vendor namespace: the host's most common profile.
		for i := range bundled {
			if bundled[i].Compat != nil && bundled[i].ID == "aion-labs/aion-2.0" {
				return &bundled[i]
			}
		}
	}
	return best
}

// impliesMandatoryReasoning is NeoPi's identity-derived mandatory-reasoning
// default: a bounded "thinking" token in the id names the reasoning-locked
// variant (`qwen3-…-thinking-2507`, `runtime-thinking-model`).
func impliesMandatoryReasoning(id string) bool {
	_, slug, _ := strings.Cut(id, "/")
	for _, segment := range strings.Split(slug, "-") {
		if segment == "thinking" {
			return true
		}
	}
	return false
}

// extendsLineage reports whether id continues base's id past a version or
// variant separator ("moonshotai/kimi-k3" -> "moonshotai/kimi-k3.1-pro").
func extendsLineage(id, base string) bool {
	if len(id) <= len(base) || !strings.EqualFold(id[:len(base)], base) {
		return false
	}
	switch id[len(base)] {
	case '.', '-', ':', '_':
		return true
	}
	return false
}
