package registry

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strings"
	"sync"

	"github.com/tidwall/gjson"
)

// The gateway providers NeoPi serves across several wire APIs per model:
// OpenCode Go and Zen (chat completions, Responses, Anthropic Messages and
// Gemini generateContent behind one key) and the Vercel AI Gateway (Anthropic
// Messages for every model).
const (
	GatewayOpenCodeGo  = "opencode-go"
	GatewayOpenCodeZen = "opencode-zen"
	GatewayVercel      = "vercel-ai-gateway"
)

// gatewayNeoPiJSON is NeoPi's resolved gateway catalogs, exported by
// testdata/capture_gateway_npi_profiles.ts: its model discovery run over a
// recorded live listing (providers), its bundled catalog (bundled) and the
// rows it builds for ids it has no reference for (templates).
//
//go:embed models/gateway_neopi.json.gz
var gatewayNeoPiGzip []byte

type gatewayCatalog struct {
	// providers are the rows NeoPi's discovery built for the recorded listing.
	providers map[string][]NeoPiModel
	// bundled is the seed NeoPi serves until discovery answers; it doubles as
	// the reference rows discovery draws a listed id's API and metadata from.
	bundled map[string][]NeoPiModel
	// dropped are listed ids NeoPi's variant collapse folds away, so they
	// never become models of their own.
	dropped map[string][]string
	// listed are the values the recorded listing reported per id.
	listed map[string]map[string]gatewayListed
	// templates are the rows NeoPi builds for ids no reference describes.
	templates map[string]map[string]NeoPiModel
}

var loadGatewayCatalog = sync.OnceValue(func() *gatewayCatalog {
	reader, err := gzip.NewReader(bytes.NewReader(gatewayNeoPiGzip))
	if err != nil {
		panic(fmt.Sprintf("registry: gateway_neopi.json.gz: %v", err))
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		panic(fmt.Sprintf("registry: gateway_neopi.json.gz: %v", err))
	}
	var export struct {
		Providers map[string][]NeoPiModel             `json:"providers"`
		Bundled   map[string][]NeoPiModel             `json:"bundled"`
		Dropped   map[string][]string                 `json:"dropped"`
		Listed    map[string]map[string]gatewayListed `json:"listed"`
		Templates map[string]map[string]NeoPiModel    `json:"templates"`
	}
	if err = json.Unmarshal(data, &export); err != nil {
		panic(fmt.Sprintf("registry: gateway_neopi.json.gz: %v", err))
	}
	return &gatewayCatalog{providers: export.Providers, bundled: export.Bundled, listed: export.Listed, dropped: export.Dropped, templates: export.Templates}
})

// gatewayListed is what the recorded listing reported for one id.
type gatewayListed struct {
	Name          string   `json:"name"`
	ContextWindow int      `json:"contextWindow"`
	MaxTokens     int      `json:"maxTokens"`
	Tags          []string `json:"tags"`
}

// GatewayProviders lists the provider ids GatewayProviderFor can return.
func GatewayProviders() []string {
	return []string{GatewayOpenCodeGo, GatewayOpenCodeZen, GatewayVercel}
}

// GatewayProviderFor identifies which gateway an openai-compatibility entry
// is, by its endpoint: opencode.ai (Go under a /go path, Zen otherwise) or
// ai-gateway.vercel.sh. It returns "" for any other entry.
func GatewayProviderFor(baseURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return ""
	}
	switch strings.ToLower(parsed.Hostname()) {
	case "opencode.ai":
		segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		if slices.Contains(segments, "go") {
			return GatewayOpenCodeGo
		}
		return GatewayOpenCodeZen
	case "ai-gateway.vercel.sh":
		return GatewayVercel
	}
	return ""
}

// GatewayDiscoveryBaseURL is the endpoint NeoPi lists a gateway's models at.
func GatewayDiscoveryBaseURL(provider string) string {
	switch provider {
	case GatewayOpenCodeGo:
		return "https://opencode.ai/inference/go/v1"
	case GatewayOpenCodeZen:
		return "https://opencode.ai/zen/v1"
	case GatewayVercel:
		return "https://ai-gateway.vercel.sh/v1"
	}
	return ""
}

// GatewayBundledModels is NeoPi's bundled catalog of a gateway as registry
// models owned by one openai-compatibility entry. An account serves it until
// its discovery answers.
func GatewayBundledModels(provider, ownedBy string) []*ModelInfo {
	rows := loadGatewayCatalog().bundled[provider]
	out := make([]*ModelInfo, 0, len(rows))
	for i := range rows {
		out = append(out, gatewayModelInfo(&rows[i], ownedBy))
	}
	return out
}

// GatewayNeoPiModel resolves an id against NeoPi's catalogs of a gateway: the
// rows its discovery built, then its bundled rows. Nil when NeoPi has neither.
func GatewayNeoPiModel(provider, id string) *NeoPiModel {
	catalog := loadGatewayCatalog()
	for _, rows := range [][]NeoPiModel{catalog.providers[provider], catalog.bundled[provider]} {
		for i := range rows {
			if rows[i].ID == id {
				return rows[i].Clone()
			}
		}
	}
	return nil
}

func gatewayModelInfo(model *NeoPiModel, ownedBy string) *ModelInfo {
	info := &ModelInfo{ID: model.ID, MetadataModelID: model.ID, Object: "model", OwnedBy: ownedBy, Type: "openai-compatibility",
		DisplayName: model.Name, ContextLength: model.ContextWindow, MaxContextLength: model.ContextWindow, MaxCompletionTokens: model.MaxTokens,
		SupportedInputModalities: slices.Clone(model.Input), NeoPi: model.Clone()}
	if model.Reasoning && model.Thinking != nil && len(model.Thinking.Efforts) > 0 {
		info.Thinking = &ThinkingSupport{Levels: slices.Clone(model.Thinking.Efforts), ZeroAllowed: !model.Thinking.RequiresEffort}
	}
	return info
}

// gatewayRoute is one api-routes rule of NeoPi's behavior.kdl.
type gatewayRoute struct {
	api      string
	exact    []string
	prefixes []string
}

// gatewayAPIRoutes are NeoPi's api-routes pins for the OpenCode gateways
// (compat/rules/runtime/behavior.kdl), in rule order.
var gatewayAPIRoutes = map[string][]gatewayRoute{
	GatewayOpenCodeZen: {
		{api: "google-generative-ai", exact: []string{"gemini-4-argon"}},
		{api: "anthropic-messages", exact: []string{"union-alpha"}},
		{api: "openai-responses", prefixes: []string{"muse-spark-"}, exact: []string{"muse-spark-1.3-contributor-free", "gpt-6-astra"}},
		{api: "openai-completions", exact: []string{"minimax-m3", "minimax-m3-free"}},
	},
	GatewayOpenCodeGo: {
		{api: "google-generative-ai", exact: []string{"gemini-4-argon", "gemini-4-argon-contributor"}},
		{api: "anthropic-messages", exact: []string{"union-alpha"}},
		{api: "openai-responses", prefixes: []string{"muse-spark-"}, exact: []string{"deepseek-v4-flash", "muse-spark-1.2", "muse-spark-1.2-contributor", "muse-spark-1.3", "muse-spark-1.3-contributor"}},
		{api: "openai-completions", exact: []string{"minimax-m2.7", "minimax-m3", "minimax-m3-free", "qwen3.5-plus", "qwen3.6-plus"}},
	},
}

// gatewayAPIRouteFor is apiRouteFor: the first rule naming the id.
func gatewayAPIRouteFor(provider, id string) string {
	lower := strings.ToLower(id)
	for _, route := range gatewayAPIRoutes[provider] {
		if slices.Contains(route.exact, id) || slices.Contains(route.exact, lower) {
			return route.api
		}
		for _, prefix := range route.prefixes {
			if strings.HasPrefix(id, prefix) || strings.HasPrefix(lower, prefix) {
				return route.api
			}
		}
	}
	return ""
}

// openCodeBaseModelID strips a billing-variant suffix (openCodeBaseModelId).
func openCodeBaseModelID(id string) (string, bool) {
	for _, suffix := range []string{"-contributor", "-free"} {
		if strings.HasSuffix(id, suffix) && len(id) > len(suffix) {
			return strings.TrimSuffix(id, suffix), true
		}
	}
	return "", false
}

// openCodeResolveAPI is openCodeModelManagerOptions' resolveApi for a listed
// id: the route pins (the id, then its billing-variant base), the provider's
// bundled reference, then Responses borrowed from the sibling gateway's or
// the base id's reference, and chat completions otherwise.
func openCodeResolveAPI(provider, id string) string {
	catalog := loadGatewayCatalog()
	base, hasBase := openCodeBaseModelID(id)
	if api := gatewayAPIRouteFor(provider, id); api != "" {
		return api
	}
	if hasBase {
		if api := gatewayAPIRouteFor(provider, base); api != "" {
			return api
		}
	}
	reference := func(p, model string) string {
		for i := range catalog.bundled[p] {
			if catalog.bundled[p][i].ID == model {
				return catalog.bundled[p][i].API
			}
		}
		return ""
	}
	if api := reference(provider, id); api != "" {
		return api
	}
	sibling := GatewayOpenCodeZen
	if provider == GatewayOpenCodeZen {
		sibling = GatewayOpenCodeGo
	}
	hints := []string{reference(sibling, id)}
	if hasBase {
		hints = append(hints, reference(provider, base), reference(sibling, base))
	}
	if slices.Contains(hints, "openai-responses") {
		return "openai-responses"
	}
	return "openai-completions"
}

// gatewayTemplate is the row NeoPi builds for a listed id it has no
// reference for: the template of the API the id routes to.
func gatewayTemplate(provider, api string, reasoning bool) *NeoPiModel {
	catalog := loadGatewayCatalog()
	pick := func(id string) *NeoPiModel {
		if row, ok := catalog.templates[provider][id]; ok {
			return row.Clone()
		}
		return nil
	}
	switch provider {
	case GatewayVercel:
		if reasoning {
			return pick("zz-unknown/alpha-model")
		}
		return pick("zz-unknown/beta-model")
	}
	var row *NeoPiModel
	switch api {
	case "openai-completions":
		row = pick("zz-unknown-model")
	case "openai-responses":
		row = pick("muse-spark-9.9")
	case "anthropic-messages":
		row = pick("union-alpha-free")
	case "google-generative-ai":
		row = pick("gemini-4-argon-free")
	}
	return row
}

// gatewayPositive is toPositiveNumber.
func gatewayPositive(value gjson.Result, fallback int) int {
	if value.Type == gjson.Number && value.Float() > 0 {
		return int(value.Float())
	}
	return fallback
}

// gatewayName is toModelName.
func gatewayName(value gjson.Result, fallback string) string {
	if value.Type != gjson.String {
		return fallback
	}
	if trimmed := strings.TrimSpace(value.String()); trimmed != "" {
		return trimmed
	}
	return fallback
}

// GatewayDiscoveredModels maps a gateway's /models listing onto registry
// models the way NeoPi's model manager does. OpenCode's discovery is
// authoritative, so exactly the listed ids NeoPi keeps become models; the
// Vercel gateway keeps its bundled rows beside its tool-capable listed ones.
// A listed id NeoPi's catalogs already describe keeps that row; an id they
// have never seen is built the way NeoPi builds one, from its API's defaults.
// Nil when the listing carries no model list.
func GatewayDiscoveredModels(provider string, listing []byte, ownedBy string) []*ModelInfo {
	data := gjson.GetBytes(listing, "data")
	if !data.IsArray() {
		return nil
	}
	catalog := loadGatewayCatalog()
	entries := map[string]gjson.Result{}
	for _, entry := range data.Array() {
		id := entry.Get("id").String()
		if id == "" || slices.Contains(catalog.dropped[provider], id) {
			continue
		}
		if provider == GatewayVercel && !vercelToolCapable(entry) {
			continue
		}
		if _, seen := entries[id]; !seen {
			entries[id] = entry
		}
	}
	var rows []*NeoPiModel
	seen := map[string]bool{}
	appendRow := func(row *NeoPiModel) {
		if row != nil && !seen[row.ID] {
			seen[row.ID] = true
			rows = append(rows, row)
		}
	}
	for id, entry := range entries {
		row := catalogRow(catalog, provider, id, true)
		if row != nil {
			gatewayApplyListed(catalog, provider, row, entry)
		} else {
			if provider == GatewayVercel {
				row = vercelDiscoveredModel(entry)
			} else {
				row = openCodeDiscoveredModel(provider, entry)
			}
		}
		appendRow(row)
	}
	if provider == GatewayVercel {
		// The Vercel manager is not authoritative: bundled rows the roster
		// does not list stay as the seed describes them.
		for i := range catalog.bundled[provider] {
			id := catalog.bundled[provider][i].ID
			if _, listed := entries[id]; listed {
				continue
			}
			if row := catalogRow(catalog, provider, id, true); row != nil {
				appendRow(row)
			}
		}
	}
	slices.SortStableFunc(rows, func(a, b *NeoPiModel) int { return strings.Compare(a.ID, b.ID) })
	out := make([]*ModelInfo, 0, len(rows))
	for _, row := range rows {
		out = append(out, gatewayModelInfo(row, ownedBy))
	}
	return out
}

// gatewayApplyListed merges a listed id's live values into the recorded row.
// NeoPi's merge lets discovery names win, turns reasoning on (never off),
// widens the modalities to a union and prefers a positive listed limit. Values
// identical to the recording leave the recorded row (which already carries
// NeoPi's catalog-rule limits) untouched.
func gatewayApplyListed(catalog *gatewayCatalog, provider string, row *NeoPiModel, entry gjson.Result) {
	recorded, known := catalog.listed[provider][row.ID]
	name := strings.TrimSpace(entry.Get("name").String())
	if name != "" && name != row.ID && (!known || name != recorded.Name) {
		row.Name = name
	}
	if provider == GatewayVercel {
		for _, tag := range entry.Get("tags").Array() {
			switch tag.String() {
			case "reasoning":
				row.Reasoning = true
			case "vision":
				if !slices.Contains(row.Input, "image") {
					row.Input = append(row.Input, "image")
				}
			}
		}
		if window := entry.Get("context_window"); window.Type == gjson.Number && !known || known && int(window.Int()) != recorded.ContextWindow {
			row.ContextWindow = gatewayPositive(window, row.ContextWindow)
		}
		if max := entry.Get("max_tokens"); max.Type == gjson.Number && !known || known && int(max.Int()) != recorded.MaxTokens {
			row.MaxTokens = gatewayPositive(max, row.MaxTokens)
		}
		// The Vercel mapper caps this lane at 131072 output tokens.
		if row.ID == "meta/muse-spark-1.2-contributor" {
			row.MaxTokens = min(row.MaxTokens, 131072)
		}
		return
	}
	if window := entry.Get("context_length"); window.Type == gjson.Number && (!known || int(window.Int()) != recorded.ContextWindow) {
		row.ContextWindow = gatewayPositive(window, row.ContextWindow)
	}
	if max := entry.Get("max_completion_tokens"); max.Type == gjson.Number && (!known || int(max.Int()) != recorded.MaxTokens) {
		row.MaxTokens = gatewayPositive(max, row.MaxTokens)
	}
}

// catalogRow is the row NeoPi's own catalogs already describe for a listed id:
// its discovery result, else its bundled seed.
func catalogRow(catalog *gatewayCatalog, provider, id string, listed bool) *NeoPiModel {
	if listed {
		for i := range catalog.providers[provider] {
			if catalog.providers[provider][i].ID == id {
				return catalog.providers[provider][i].Clone()
			}
		}
	}
	for i := range catalog.bundled[provider] {
		if catalog.bundled[provider][i].ID == id {
			return catalog.bundled[provider][i].Clone()
		}
	}
	return nil
}

// openCodeBaseURLForAPI is openCodeBaseUrlForApi: OpenCode's Anthropic
// Messages endpoint is the bare base path, everything else its /v1 tree, and
// Zen's native Gemini endpoint is its own host path.
func openCodeBaseURLForAPI(provider, api string) string {
	base := strings.TrimSuffix(GatewayDiscoveryBaseURL(provider), "/v1")
	if api == "anthropic-messages" {
		return base
	}
	if api == "google-generative-ai" && provider == GatewayOpenCodeZen {
		return "https://opencode.ai/inference/google/v1beta"
	}
	return GatewayDiscoveryBaseURL(provider)
}

// openCodeDiscoveredModel is the OpenCode mapModel for a listed id no
// reference describes: options.api, then no capabilities, which is what NeoPi
// builds before its catalog rules classify the id. The row keeps the API's
// default compat so the request builder has the host's wire facts.
func openCodeDiscoveredModel(provider string, entry gjson.Result) *NeoPiModel {
	id := entry.Get("id").String()
	if gatewayTypesafe(provider, id) {
		return nil
	}
	if lineage := museSparkLineageRow(provider, id); lineage != nil {
		lineage.Name = gatewayName(entry.Get("name"), lineage.Name)
		lineage.ContextWindow = gatewayPositive(entry.Get("context_length"), lineage.ContextWindow)
		lineage.MaxTokens = gatewayPositive(entry.Get("max_completion_tokens"), lineage.MaxTokens)
		return lineage
	}
	api := openCodeResolveAPI(provider, id)
	template := gatewayTemplate(provider, api, false)
	if template == nil {
		return nil
	}
	return &NeoPiModel{ID: id, Name: gatewayName(entry.Get("name"), id), API: api, Provider: provider,
		BaseURL: openCodeBaseURLForAPI(provider, api), Input: []string{"text"},
		CompletionsCompat: template.CompletionsCompat, AnthropicCompat: template.AnthropicCompat, Compat: template.Compat}
}

// museSparkLineageRow is museSparkLineageSpec for an id the gateway lists but
// the catalogs never did: the newest Muse Spark row of the same billing shape
// becomes it (both gateways resolve the revision from the same meta seeds).
func museSparkLineageRow(provider, id string) *NeoPiModel {
	if !museSparkLineage(id) {
		return nil
	}
	contributor := strings.HasSuffix(id, "-contributor")
	catalog := loadGatewayCatalog()
	sibling := GatewayOpenCodeZen
	if provider == GatewayOpenCodeZen {
		sibling = GatewayOpenCodeGo
	}
	var best *NeoPiModel
	for _, rows := range [][]NeoPiModel{catalog.providers[provider], catalog.bundled[provider], catalog.providers[sibling], catalog.bundled[sibling]} {
		for i := range rows {
			row := &rows[i]
			if row.API != "openai-responses" || !museSparkLineage(row.ID) || strings.HasSuffix(row.ID, "-contributor") != contributor {
				continue
			}
			if best == nil || strings.Compare(museSparkRevision(row.ID), museSparkRevision(best.ID)) > 0 {
				best = row
			}
		}
	}
	if best == nil {
		return nil
	}
	out := best.Clone()
	out.ID = id
	out.BaseURL = openCodeBaseURLForAPI(provider, "openai-responses")
	return out
}

// museSparkRevision orders Muse Spark ids by their revision text.
func museSparkRevision(id string) string {
	return strings.TrimPrefix(id, "muse-spark-")
}

// museSparkLineage is museSparkLineageSpec's gate: a Muse Spark id with a
// revision ("muse-spark-1.3", "muse-spark-9.8-contributor").
func museSparkLineage(id string) bool {
	rest, ok := strings.CutPrefix(id, "muse-spark-")
	return ok && rest != "" && rest[0] >= '0' && rest[0] <= '9'
}

// gatewayTypesafe reports ids NeoPi seeds as System One judge rows (Zen's Jev).
func gatewayTypesafe(provider, id string) bool {
	return provider == GatewayOpenCodeZen && (id == "jev-1.13" || id == "jev-1.13-free")
}

// vercelToolCapable is the Vercel manager's filterModel: entries tagged
// tool-use only.
func vercelToolCapable(entry gjson.Result) bool {
	for _, tag := range entry.Get("tags").Array() {
		if tag.String() == "tool-use" {
			return true
		}
	}
	return false
}

// vercelDiscoveredModel is the Vercel mapModel for a listed id no reference
// describes: the template of its reasoning class, with the listed limits and
// modalities.
func vercelDiscoveredModel(entry gjson.Result) *NeoPiModel {
	tags := entry.Get("tags")
	if !tags.IsArray() {
		return nil
	}
	var hasTool, reasoning, vision bool
	for _, tag := range tags.Array() {
		switch tag.String() {
		case "tool-use":
			hasTool = true
		case "reasoning":
			reasoning = true
		case "vision":
			vision = true
		}
	}
	if !hasTool {
		return nil
	}
	id := entry.Get("id").String()
	row := gatewayTemplate(GatewayVercel, "anthropic-messages", reasoning)
	if row == nil {
		return nil
	}
	row.ID = id
	row.Name = gatewayName(entry.Get("name"), id)
	row.Reasoning = reasoning
	row.Input = []string{"text"}
	if vision {
		row.Input = []string{"text", "image"}
	}
	row.ContextWindow = gatewayPositive(entry.Get("context_window"), row.ContextWindow)
	row.MaxTokens = gatewayPositive(entry.Get("max_tokens"), row.MaxTokens)
	if id == "meta/muse-spark-1.2-contributor" {
		row.MaxTokens = min(row.MaxTokens, 131072)
	}
	return row
}
