package registry

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

// Responses providers: the openai-responses hosts NeoPi serves through its
// generic Responses transport and a per-provider catalog.
const (
	// ResponsesMeta is NeoPi's `meta` provider: a pay-as-you-go Model API key.
	ResponsesMeta = "meta"
	// ResponsesMuseCode is NeoPi's `muse-code` provider: the Muse subscription,
	// signed in on a Meta account and served through a minted Model API key.
	ResponsesMuseCode   = "muse-code"
	ResponsesSakana     = "sakana"
	ResponsesAbliterate = "abliteration"
)

// responsesNeoPiJSON is NeoPi's resolved catalogs for the Responses lane,
// exported by runtime/executor/testdata/capture_responses_npi_profiles.ts
// --registry: each provider's seed rows (catalog KDL through buildModel) and
// the rows a roster id without a seed becomes, by category.
//
//go:embed models/responses_neopi.json
var responsesNeoPiJSON []byte

type responsesNeoPiExport struct {
	Seeds     map[string][]NeoPiModel          `json:"seeds"`
	Templates map[string]map[string]NeoPiModel `json:"templates"`
}

var responsesNeoPiCatalogs = func() responsesNeoPiExport {
	var export responsesNeoPiExport
	if err := json.Unmarshal(responsesNeoPiJSON, &export); err != nil {
		panic(fmt.Sprintf("registry: responses_neopi.json: %v", err))
	}
	return export
}()

// DiscoveredResponsesModel is one `/v1/models` entry as NeoPi's
// fetchOpenAICompatibleModels reads it.
type DiscoveredResponsesModel struct {
	ID                  string
	Name                string
	ContextLength       int
	MaxCompletionTokens int
}

// ResponsesProviderBaseURL is the endpoint NeoPi's catalog binds to a Responses provider.
func ResponsesProviderBaseURL(provider string) string {
	if seeds := responsesNeoPiCatalogs.Seeds[provider]; len(seeds) > 0 {
		return seeds[0].BaseURL
	}
	return ""
}

// responsesAuthoritative is dynamicModelsAuthoritative: a successful roster
// replaces the seed instead of extending it. Meta's PAYG manager keeps its
// seed rows (ids "the bundle has not been regenerated with" included).
func responsesAuthoritative(provider string) bool {
	return provider != ResponsesMeta
}

// IsResponsesExcluded is NeoPi's exclude-models rule for Meta: image, voice
// and speech-model ids never reach the chat catalog.
func IsResponsesExcluded(provider, id string) bool {
	if provider != ResponsesMeta && provider != ResponsesMuseCode {
		return false
	}
	for _, prefix := range []string{"muse-image-", "muse-voice-", "sam-"} {
		if strings.HasPrefix(id, prefix) {
			return true
		}
	}
	return false
}

// ResponsesSeed returns NeoPi's bundled rows for provider, cloned.
func ResponsesSeed(provider string) []NeoPiModel {
	seeds := responsesNeoPiCatalogs.Seeds[provider]
	out := make([]NeoPiModel, len(seeds))
	for i := range seeds {
		out[i] = *seeds[i].Clone()
	}
	return out
}

var (
	museSparkLineage = regexp.MustCompile(`^(?:.*[-/])?muse-spark-(\d+)\.(\d+)(?:\.(\d+))?(-contributor)?$`)
	sakanaFuguID     = regexp.MustCompile(`(?i)^fugu(?:$|-)`)
)

// museLineageRow is museSparkLineageSpec: a Muse Spark revision the seed lacks
// inherits the newest seeded revision of its billing tier, keeps the seed's
// limits and prices, drops the reviewed `max` ladder for the provider's
// five-tier one, and takes a name derived from its revision.
func museLineageRow(provider, id string) *NeoPiModel {
	match := museSparkLineage.FindStringSubmatch(id)
	if match == nil {
		return nil
	}
	major, _ := strconv.Atoi(match[1])
	minor, _ := strconv.Atoi(match[2])
	patch := 0
	if match[3] != "" {
		patch, _ = strconv.Atoi(match[3])
	}
	revision := fmt.Sprintf("%d.%d", major, minor)
	if patch != 0 {
		revision = fmt.Sprintf("%d.%d.%d", major, minor, patch)
	}
	contributor := match[4] != ""
	var template *NeoPiModel
	for i := range responsesNeoPiCatalogs.Seeds[provider] {
		seed := &responsesNeoPiCatalogs.Seeds[provider][i]
		seedMatch := museSparkLineage.FindStringSubmatch(seed.ID)
		if seedMatch == nil || (seedMatch[4] != "") != contributor {
			continue
		}
		if template == nil || museRevisionLess(museSparkLineage.FindStringSubmatch(template.ID), seedMatch) {
			template = seed
		}
	}
	if template == nil {
		return nil
	}
	row := template.Clone()
	row.ID = id
	row.Name = "Muse Spark " + revision
	if contributor {
		row.Name += " (C)"
	}
	row.Thinking = &NeoPiThinking{Efforts: []string{"minimal", "low", "medium", "high", "xhigh"}, Mode: "effort"}
	return row
}

func museRevisionLess(a, b []string) bool {
	for i := 1; i <= 3; i++ {
		x, _ := strconv.Atoi(a[i])
		y, _ := strconv.Atoi(b[i])
		if x != y {
			return x < y
		}
	}
	return false
}

// responsesDiscoveredRow maps one roster entry as the provider's NeoPi
// mapModel does: the seed row (or, for Meta, its lineage row) supplies the
// capability facts; the roster supplies the name and positive limits.
func responsesDiscoveredRow(provider string, entry DiscoveredResponsesModel) NeoPiModel {
	var reference *NeoPiModel
	for i := range responsesNeoPiCatalogs.Seeds[provider] {
		if seed := &responsesNeoPiCatalogs.Seeds[provider][i]; seed.ID == entry.ID {
			reference = seed
			break
		}
	}
	if reference == nil && (provider == ResponsesMeta || provider == ResponsesMuseCode) {
		reference = museLineageRow(provider, entry.ID)
	}
	if reference != nil {
		row := reference.Clone()
		row.ID = entry.ID
		if entry.Name != "" {
			row.Name = entry.Name
		}
		if entry.ContextLength > 0 {
			row.ContextWindow = entry.ContextLength
		}
		if entry.MaxCompletionTokens > 0 {
			row.MaxTokens = entry.MaxCompletionTokens
		}
		return *row
	}
	return *responsesUnseededRow(provider, entry)
}

// responsesUnseededRow is a roster id with no reference: fetchOpenAICompatibleModels'
// defaults (text input, unknown limits) resolved by the provider's category rules.
func responsesUnseededRow(provider string, entry DiscoveredResponsesModel) *NeoPiModel {
	templates := responsesNeoPiCatalogs.Templates[provider]
	var template NeoPiModel
	switch provider {
	case ResponsesMeta, ResponsesMuseCode:
		// Muse Spark and Wonder are bounded lineages: an unrecognized revision
		// is not offered an unverified ladder.
		if strings.Contains(entry.ID, "muse-spark") || strings.Contains(entry.ID, "muse-wonder") {
			template = templates["bounded"]
		} else {
			template = templates["unknown"]
		}
	case ResponsesSakana:
		if sakanaFuguID.MatchString(entry.ID) {
			template = templates["fugu"]
		} else {
			template = templates["plain"]
		}
	default:
		template = templates["unseeded"]
	}
	row := template.Clone()
	row.ID = entry.ID
	row.Name = entry.ID
	if entry.Name != "" {
		row.Name = entry.Name
	}
	return row
}

// ResponsesNeoPiModels is NeoPi's model-manager result for provider over a
// discovered roster: seeds first in declaration order (only those the roster
// lists when the provider is authoritative), then roster-only ids sorted as
// fetchOpenAICompatibleModels sorts them. A nil roster (discovery has not
// answered) is the seed.
func ResponsesNeoPiModels(provider string, listed []DiscoveredResponsesModel) []NeoPiModel {
	seeds := ResponsesSeed(provider)
	if listed == nil {
		return seeds
	}
	byID := map[string]DiscoveredResponsesModel{}
	for _, entry := range listed {
		if entry.ID == "" || IsResponsesExcluded(provider, entry.ID) {
			continue
		}
		byID[entry.ID] = entry
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return localeLess(ids[i], ids[j]) })
	var out []NeoPiModel
	seeded := map[string]bool{}
	for _, seed := range seeds {
		seeded[seed.ID] = true
		entry, listedID := byID[seed.ID]
		switch {
		case listedID:
			out = append(out, responsesDiscoveredRow(provider, entry))
		case !responsesAuthoritative(provider):
			out = append(out, seed)
		}
	}
	for _, id := range ids {
		if !seeded[id] {
			out = append(out, responsesDiscoveredRow(provider, byID[id]))
		}
	}
	return out
}

// localeLess approximates String.prototype.localeCompare for the ASCII ids
// providers list: case-insensitive first, lowercase before uppercase on ties.
func localeLess(a, b string) bool {
	la, lb := strings.ToLower(a), strings.ToLower(b)
	if la != lb {
		return la < lb
	}
	return a > b
}

// ResponsesNeoPiModel resolves a configured id: the roster-derived row for a
// listed id, the seed for a seeded one, else the unseeded default so a
// configured model stays servable.
func ResponsesNeoPiModel(provider, id string, listed []DiscoveredResponsesModel) *NeoPiModel {
	for _, row := range ResponsesNeoPiModels(provider, listed) {
		if row.ID == id {
			return row.Clone()
		}
	}
	if IsResponsesExcluded(provider, id) {
		return nil
	}
	row := responsesDiscoveredRow(provider, DiscoveredResponsesModel{ID: id})
	return &row
}

// ResponsesModels is ResponsesNeoPiModels as registry models owned by ownedBy.
func ResponsesModels(provider, ownedBy, modelType string, listed []DiscoveredResponsesModel) []*ModelInfo {
	rows := ResponsesNeoPiModels(provider, listed)
	out := make([]*ModelInfo, 0, len(rows))
	for i := range rows {
		model := rows[i].Clone()
		info := &ModelInfo{ID: model.ID, MetadataModelID: model.ID, Object: "model", OwnedBy: ownedBy, Type: modelType, Name: model.ID,
			DisplayName: model.Name, ContextLength: model.ContextWindow, MaxContextLength: model.ContextWindow, MaxCompletionTokens: model.MaxTokens,
			SupportedInputModalities: slices.Clone(model.Input), NeoPi: model}
		if model.Reasoning && model.Thinking != nil {
			info.Thinking = &ThinkingSupport{Levels: slices.Clone(model.Thinking.Efforts), ZeroAllowed: true}
		}
		out = append(out, info)
	}
	return out
}

// ResponsesProviderForBaseURL names the Responses provider whose catalog
// address is baseURL (Sakana, Abliteration; Meta's Model API), or "" when none
// is. Trailing slashes and case do not matter.
func ResponsesProviderForBaseURL(baseURL string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return ""
	}
	for _, provider := range []string{ResponsesSakana, ResponsesAbliterate, ResponsesMeta} {
		if strings.EqualFold(baseURL, strings.TrimRight(ResponsesProviderBaseURL(provider), "/")) {
			return provider
		}
	}
	return ""
}

// ParseResponsesRoster reads a `/models` body as NeoPi's
// extractModelEntries does: a bare array, or an envelope whose data, models,
// result or items member (first that parses) holds the array. Entries without
// a string id are skipped. nil means the body is not a model list.
func ParseResponsesRoster(body []byte) []DiscoveredResponsesModel {
	return rosterFromNode(gjson.ParseBytes(body))
}

func rosterFromNode(node gjson.Result) []DiscoveredResponsesModel {
	if node.IsArray() {
		out := []DiscoveredResponsesModel{}
		for _, entry := range node.Array() {
			id := entry.Get("id")
			if id.Type != gjson.String || id.String() == "" {
				continue
			}
			model := DiscoveredResponsesModel{ID: id.String()}
			if name := entry.Get("name"); name.Type == gjson.String {
				model.Name = name.String()
			}
			if value := entry.Get("context_length"); value.Type == gjson.Number && value.Float() > 0 {
				model.ContextLength = int(value.Float())
			}
			if value := entry.Get("max_completion_tokens"); value.Type == gjson.Number && value.Float() > 0 {
				model.MaxCompletionTokens = int(value.Float())
			}
			out = append(out, model)
		}
		return out
	}
	if !node.IsObject() {
		return nil
	}
	for _, key := range []string{"data", "models", "result", "items"} {
		if candidate := node.Get(key); candidate.Exists() {
			if nested := rosterFromNode(candidate); nested != nil {
				return nested
			}
		}
	}
	return nil
}
