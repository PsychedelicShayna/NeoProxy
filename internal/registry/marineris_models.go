package registry

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/tidwall/gjson"
)

// marinerisNeoPiJSON is how NeoPi resolves the models its Marineris extension
// registers, exported by internal/auth/marineris/testdata/capture_neopi_marineris_profiles.ts:
// the resolved request compat per model class, plus every xAI and Codex id
// NeoPi's catalog knows.
//
//go:embed models/marineris_neopi.json
var marinerisNeoPiJSON []byte

// marinerisHost is one gateway class's catalog: NeoPi resolves a different
// compat for a local-network gateway than for a public one.
type marinerisHost struct {
	families []marinerisClass
	byID     map[string]*marinerisResolved
}

var marinerisCatalog = func() *struct {
	profiles map[string]marinerisProfile
	local    marinerisHost
	public   marinerisHost
} {
	type exported struct {
		Classes []marinerisClass    `json:"classes"`
		Models  []marinerisResolved `json:"models"`
	}
	var export struct {
		Profiles map[string]marinerisProfile `json:"profiles"`
		Hosts    map[string]exported         `json:"hosts"`
	}
	if err := json.Unmarshal(marinerisNeoPiJSON, &export); err != nil {
		panic(fmt.Sprintf("registry: marineris_neopi.json: %v", err))
	}
	build := func(host exported) marinerisHost {
		out := marinerisHost{byID: make(map[string]*marinerisResolved, len(host.Models))}
		out.families = slices.Clone(host.Classes)
		// Longest family first, so the most specific rule wins.
		slices.SortStableFunc(out.families, func(a, b marinerisClass) int { return len(b.Prefix) - len(a.Prefix) })
		for i := range host.Models {
			out.byID[host.Models[i].ID] = &host.Models[i]
		}
		return out
	}
	return &struct {
		profiles map[string]marinerisProfile
		local    marinerisHost
		public   marinerisHost
	}{profiles: export.Profiles, local: build(export.Hosts["local"]), public: build(export.Hosts["public"])}
}()

type marinerisProfile struct {
	// Compat is ResolvedOpenAICompat for completions rows and
	// ResolvedOpenAIResponsesCompat for responses rows.
	Compat         json.RawMessage `json:"compat"`
	RequiresEffort bool            `json:"requiresEffort"`
}

// marinerisClass is NeoPi's answer for an unknown model of a family: an id
// starting with Prefix, mapped to the given API and reasoning flag.
type marinerisClass struct {
	Prefix    string `json:"prefix"`
	API       string `json:"api"`
	Reasoning bool   `json:"reasoning"`
	Profile   string `json:"profile"`
}

type marinerisResolved struct {
	ID      string `json:"id"`
	API     string `json:"api"`
	Profile string `json:"profile"`
}

var (
	marinerisReasoningID    = regexp.MustCompile(`reasoning`)
	marinerisNonReasoningID = regexp.MustCompile(`non-reasoning`)
)

// MarinerisMapModel is the extension's mapModel for one /v1/models row,
// resolved as NeoPi's catalog build would; baseURL is the gateway address
// (ending in /v1). Nil for a row the extension skips.
func MarinerisMapModel(row gjson.Result, baseURL string) *NeoPiModel {
	if !row.IsObject() {
		return nil
	}
	idResult := row.Get("id")
	if idResult.Type != gjson.String || idResult.String() == "" {
		return nil
	}
	id := idResult.String()
	provider := ""
	switch {
	case row.Get("provider").Exists() && row.Get("provider").Type != gjson.Null:
		provider = row.Get("provider").String()
	case row.Get("owned_by").Exists() && row.Get("owned_by").Type != gjson.Null:
		provider = row.Get("owned_by").String()
	}
	codex := provider == "codex" || strings.HasPrefix(id, "gpt-") || strings.HasPrefix(id, "codex") || strings.HasPrefix(id, "o")
	reasoning := codex || marinerisReasoningID.MatchString(id) && !marinerisNonReasoningID.MatchString(id)
	contextWindow, ok := marinerisNum(row.Get("context_window"))
	if !ok {
		if contextWindow, ok = marinerisNum(row.Get("context_length")); !ok {
			contextWindow = 131072
			if codex {
				contextWindow = 400000
			}
		}
	}
	maxTokens, ok := marinerisNum(row.Get("max_output_tokens"))
	if !ok {
		if maxTokens, ok = marinerisNum(row.Get("max_tokens")); !ok {
			maxTokens = math.Min(128000, math.Floor(contextWindow/4))
		}
	}
	_, pricedImage := marinerisNum(row.Get("prompt_image_token_price"))
	modalities := false
	if list := row.Get("input_modalities"); list.IsArray() {
		for _, mode := range list.Array() {
			modalities = modalities || mode.Type == gjson.String && mode.String() == "image"
		}
	}
	input := []string{"text"}
	if pricedImage || modalities {
		input = []string{"text", "image"}
	}
	name := id
	if value := row.Get("name"); value.Type == gjson.String {
		name = value.String()
	}
	api := "openai-completions"
	if codex {
		api = "openai-responses"
	}
	model := &NeoPiModel{ID: id, Name: cleanNeoPiModelName(name), API: api, Provider: "marineris", BaseURL: strings.TrimRight(baseURL, "/"),
		Reasoning: reasoning, Input: input, ContextWindow: int(contextWindow), MaxTokens: int(maxTokens)}
	if reasoning {
		if codex {
			model.Thinking = &NeoPiThinking{Mode: "effort", Efforts: []string{"low", "medium", "high", "xhigh"}, DefaultLevel: "medium"}
		} else {
			model.Thinking = &NeoPiThinking{Mode: "effort", Efforts: []string{"low", "high"}, DefaultLevel: "high"}
		}
	}
	resolveMarinerisModel(model)
	return model
}

// resolveMarinerisModel applies NeoPi's catalog policy: the request compat of
// the model's family and the effort ladder's resolved fields. Ids NeoPi's catalog knows resolve exactly; an unknown id as NeoPi
// resolves an unknown model of its class.
func resolveMarinerisModel(model *NeoPiModel) {
	var profileID string
	host := &marinerisCatalog.public
	if NeoPiLocalBaseURL(model.BaseURL) {
		host = &marinerisCatalog.local
	}
	if known := host.byID[model.ID]; known != nil && known.API == model.API {
		profileID = known.Profile
	} else {
		for _, family := range host.families {
			if family.API == model.API && family.Reasoning == model.Reasoning && strings.HasPrefix(model.ID, family.Prefix) {
				profileID = family.Profile
				break
			}
		}
	}
	profile := marinerisCatalog.profiles[profileID]
	if model.API == "openai-responses" {
		var compat NeoPiCompat
		if err := json.Unmarshal(profile.Compat, &compat); err == nil {
			model.Compat = &compat
		}
	} else {
		var compat NeoPiCompletionsCompat
		if err := json.Unmarshal(profile.Compat, &compat); err == nil {
			model.CompletionsCompat = &compat
		}
	}
	if model.Thinking != nil && profile.RequiresEffort {
		model.Thinking.RequiresEffort = true
	}
}

// MarinerisModels maps a /v1/models envelope's rows the way the extension's
// fetchModels does.
func MarinerisModels(rows []json.RawMessage, baseURL, ownedBy string) []*ModelInfo {
	out := make([]*ModelInfo, 0, len(rows))
	for _, raw := range rows {
		if model := MarinerisMapModel(gjson.ParseBytes(raw), baseURL); model != nil {
			out = append(out, neoPiExtensionModelInfo(model, ownedBy))
		}
	}
	return out
}

// marinerisNum is the extension's num(): a positive finite JSON number.
func marinerisNum(value gjson.Result) (float64, bool) {
	if value.Type != gjson.Number || math.IsNaN(value.Float()) || math.IsInf(value.Float(), 0) || value.Float() <= 0 {
		return 0, false
	}
	return value.Float(), true
}

var neoPiPrivate172 = regexp.MustCompile(`^172\.(1[6-9]|2[0-9]|3[01])\.`)

// NeoPiLocalBaseURL is pi-catalog's hasLocalLoopbackBaseUrl: whether a base
// URL names a local-network server (loopback, RFC 1918 or .local), which the
// catalog resolves a different compat for.
func NeoPiLocalBaseURL(baseURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Host == "" {
		return false
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	switch host {
	case "localhost", "127.0.0.1", "0.0.0.0", "::1":
		return true
	}
	return strings.HasPrefix(host, "10.") || strings.HasPrefix(host, "192.168.") || neoPiPrivate172.MatchString(host) || strings.HasSuffix(host, ".local")
}
