package neopool

import (
	"regexp"
	"strings"

	zaiauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/zai"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// ProviderPlan is a subscription tier or budget with its default monthly value.
type ProviderPlan struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	MonthlyUSD float64 `json:"monthlyUSD"`
}

// ProviderSubscription describes the existing engine sign-in for a provider.
// Flow is "browser" (provider page in a new tab, redirect back), "device"
// (provider page plus a short code the person confirms) or "code" (provider
// page in a new tab; the provider cannot redirect back, so the person pastes
// the final address or code into NeoProxy).
type ProviderSubscription struct {
	Flow  string   `json:"flow"`
	Note  string   `json:"note"`
	Modes []string `json:"modes,omitempty"`
	// Site names where the person signs in when that isn't the vendor
	// (Cursor, not Anysphere); the dashboard falls back to Vendor.
	Site string `json:"site,omitempty"`
}

// ProviderAPIKey describes how a pay-as-you-go key is validated and routed.
// Style selects the model-listing protocol: openai, anthropic or gemini.
type ProviderAPIKey struct {
	Style           string `json:"style"`
	BaseURL         string `json:"baseUrl"`
	EditableBaseURL bool   `json:"editableBaseUrl"`
	KeyHint         string `json:"keyHint"`
	GetKeyURL       string `json:"getKeyUrl"`
	KeyOptional     bool   `json:"keyOptional,omitempty"`
}

// ProviderInfo is one entry of the dashboard's provider catalogue. Hue and
// Chroma are OKLCH components (L≈0.74); Chroma 0 renders a neutral grey/silver.
type ProviderInfo struct {
	ID           string                `json:"id"`
	Name         string                `json:"name"`
	Vendor       string                `json:"vendor"`
	Hue          float64               `json:"hue"`
	Chroma       float64               `json:"chroma"`
	Methods      []string              `json:"methods"`
	Subscription *ProviderSubscription `json:"subscription,omitempty"`
	APIKey       *ProviderAPIKey       `json:"apiKey,omitempty"`
	Plans        []ProviderPlan        `json:"plans"`
	// engine is the native engine provider for API keys; empty means the key is
	// routed through an openai-compatibility entry named after the catalogue ID.
	engine string
	// regions are regional endpoints of one product whose keys each authenticate
	// at exactly one of them; validation keeps the first that accepts the key.
	// APIKey.BaseURL is the first region.
	regions []string
	// skipModels are model-id prefixes the provider lists but chat clients
	// cannot use (image, audio, video and embedding SKUs).
	skipModels []string
	// upstreamErrors seeds that openai-compatibility entry's error
	// classification with provider quirks.
	upstreamErrors []config.RequestScopedErrorRule
	// dialect is the upstream API of every model (chat-completions when empty);
	// Responses-only gateways set "responses".
	dialect string
	// dialectRules route models whose model-list entry does not advertise its
	// API, by id prefix; the first match wins and unmatched ids stay on dialect.
	dialectRules []dialectRule
	// sessionHeader is the request header that must carry a session id.
	sessionHeader string
	// headers are static request headers for the provider's entry.
	headers map[string]string
}

// dialectRule routes model ids starting with prefix to dialect.
type dialectRule struct {
	prefix  string
	dialect string
}

const providerChroma = 0.13

// tintChroma tints a provider whose brand is monochrome (Cursor, OpenCode Zen,
// Vercel) so tiles sharing one pool stay apart; xAI and custom servers keep
// pure grey (Chroma 0).
const tintChroma = 0.045

var (
	chatGPTPlans = []ProviderPlan{{"free", "ChatGPT Free", 0}, {"plus", "ChatGPT Plus", 20}, {"pro", "ChatGPT Pro", 200}, {"team", "ChatGPT Team", 30}, {"business", "ChatGPT Business", 30}, {"enterprise", "ChatGPT Enterprise", 60}}
	claudePlans  = []ProviderPlan{{"pro", "Claude Pro", 20}, {"max5x", "Claude Max 5x", 100}, {"max20x", "Claude Max 20x", 200}}
	grokPlans    = []ProviderPlan{{"supergrok", "SuperGrok", 30}, {"supergrok-heavy", "SuperGrok Heavy", 300}}
	googlePlans  = []ProviderPlan{{"google-ai-pro", "Google AI Pro", 20}, {"google-ai-ultra", "Google AI Ultra", 250}}
	// GLM Coding Plan list prices (monthly billing); ids match the level Z.ai's
	// quota API reports.
	zaiPlans = []ProviderPlan{{"lite", "GLM Coding Plan Lite", 18}, {"pro", "GLM Coding Plan Pro", 80}, {"max", "GLM Coding Plan Max", 168}}
	// Cursor's plans on The X Subscription, then the legacy plans existing subscribers keep
	// (cursor.com/help/account-and-billing/pricing).
	cursorPlans = []ProviderPlan{{"plus", "Cursor Plus", 8}, {"premium", "Cursor Premium", 30}, {"super", "Cursor Super", 100}, {"ultra", "Cursor Ultra", 200}, {"pro", "Cursor Pro (legacy)", 20}, {"pro-plus", "Cursor Pro+ (legacy)", 60}}
)

// QwenCloud Token Plan endpoints. Keys are region-locked: a China (Beijing) key
// is rejected by the International (Singapore) endpoint and vice versa.
const (
	qwenTokenPlanIntlURL = "https://token-plan.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1"
	qwenTokenPlanCNURL   = "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1"
)

// Default monthly values when nothing more specific is known.
const (
	defaultSubscriptionUSD = 20
	defaultAPIKeyUSD       = 20
)

// OpenCode's model lists name each model's API (chat-completions, responses,
// messages, generate-content) and its base URL. These family rules only cover
// ids listed without that metadata. Every OpenCode inference request must carry
// x-opencode-session, or the gateway answers 400 MissingSessionID.
var openCodeDialectRules = []dialectRule{
	{"claude-", "messages"},
	{"gpt-", "responses"},
	{"grok-", "responses"},
	{"muse-", "responses"},
}

const openCodeSessionHeader = "x-opencode-session"

// openCodeHeaders replace the engine's default OpenAI-compatible User-Agent,
// which opencode.ai's Cloudflare front rejects (error 1010, HTTP 403).
var openCodeHeaders = map[string]string{"User-Agent": "NeoProxy"}

func openAIKey(base, hint, getKey string) *ProviderAPIKey {
	return &ProviderAPIKey{Style: "openai", BaseURL: base, KeyHint: hint, GetKeyURL: getKey}
}

func browser(note string) *ProviderSubscription {
	return &ProviderSubscription{Flow: "browser", Note: note}
}
func device(note string) *ProviderSubscription {
	return &ProviderSubscription{Flow: "device", Note: note}
}

// at sets where the person signs in, for sites not named after the vendor.
func (s *ProviderSubscription) at(site string) *ProviderSubscription {
	s.Site = site
	return s
}

// providerCatalog lists only sign-ins whose engine handler exists
// (management StartOAuthV8: claude, codex, antigravity, kimi, xai, grok-build, devin, meta, zai, cursor, nous-portal).
// gemini-cli, qwen and iflow have no handler and are therefore key-only or absent.
var providerCatalog = []ProviderInfo{
	{ID: "codex", Name: "ChatGPT / Codex", Vendor: "OpenAI", Hue: 165, Chroma: providerChroma, Methods: []string{"subscription", "apiKey"},
		Subscription: &ProviderSubscription{Flow: "browser", Modes: []string{"browser", "device"}, Note: "Use OpenAI's browser sign-in, or enter a device code on OpenAI's verification page. NeoProxy keeps the sign-in refreshed."},
		APIKey:       &ProviderAPIKey{Style: "openai", BaseURL: "https://api.openai.com/v1", KeyHint: "sk-...", GetKeyURL: "https://platform.openai.com/api-keys"},
		Plans:        chatGPTPlans, engine: "codex"},
	{ID: "claude", Name: "Claude", Vendor: "Anthropic", Hue: 45, Chroma: providerChroma, Methods: []string{"subscription", "apiKey"},
		Subscription: browser("Opens Anthropic's sign-in in a new tab. If this page doesn't continue after you approve, copy the address your browser ends up on and paste it here. NeoProxy keeps the sign-in refreshed."),
		APIKey:       &ProviderAPIKey{Style: "anthropic", BaseURL: "https://api.anthropic.com", KeyHint: "sk-ant-...", GetKeyURL: "https://console.anthropic.com/settings/keys"},
		Plans:        claudePlans, engine: "claude"},
	{ID: "gemini", Name: "Gemini", Vendor: "Google", Hue: 255, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: &ProviderAPIKey{Style: "gemini", BaseURL: "https://generativelanguage.googleapis.com", KeyHint: "AIza...", GetKeyURL: "https://aistudio.google.com/apikey"},
		Plans:  []ProviderPlan{}, engine: "gemini"},
	{ID: "antigravity", Name: "Antigravity", Vendor: "Google", Hue: 255, Chroma: providerChroma, Methods: []string{"subscription"},
		Subscription: browser("Opens Google's sign-in in a new tab. NeoProxy keeps the sign-in refreshed."),
		Plans:        googlePlans},
	{ID: "xai", Name: "Grok / xAI", Vendor: "xAI", Hue: 0, Chroma: 0, Methods: []string{"subscription", "apiKey"},
		Subscription: device("Opens x.ai in a new tab and shows a code to confirm there. Requests go to the official xAI API."),
		APIKey:       openAIKey("https://api.x.ai/v1", "xai-...", "https://console.x.ai/"),
		Plans:        grokPlans, engine: "xai"},
	// Same xAI sign-in and token as xai, served through the Grok CLI chat-proxy,
	// which has its own speed, usage limits and model list.
	{ID: "grok-build", Name: "Grok Build", Vendor: "xAI", Hue: 250, Chroma: tintChroma, Methods: []string{"subscription"},
		Subscription: device("Opens x.ai in a new tab and shows a code to confirm there. Requests go to Grok Build, the Grok CLI's endpoint, with its own speed, usage limits and models."),
		Plans:        grokPlans},
	{ID: "kimi", Name: "Kimi", Vendor: "Moonshot AI", Hue: 340, Chroma: providerChroma, Methods: []string{"subscription"},
		Subscription: device("Opens Kimi in a new tab and shows a code to confirm there.").at("Kimi"),
		Plans:        []ProviderPlan{}},
	{ID: "devin", Name: "Devin", Vendor: "Cognition", Hue: 205, Chroma: providerChroma, Methods: []string{"subscription"},
		Subscription: browser("Opens Devin's sign-in in a new tab.").at("Devin"),
		Plans:        []ProviderPlan{}},
	{ID: "meta", Name: "Meta AI", Vendor: "Meta", Hue: 280, Chroma: providerChroma, Methods: []string{"subscription", "apiKey"},
		Subscription: device("Opens Meta in a new tab and shows a code to confirm there."),
		APIKey:       openAIKey("https://api.meta.ai/v1", "LLM|...", "https://developer.meta.com/ai/"),
		Plans:        []ProviderPlan{}, engine: "meta"},
	{ID: "openrouter", Name: "OpenRouter", Vendor: "OpenRouter", Hue: 300, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://openrouter.ai/api/v1", "sk-or-v1-...", "https://openrouter.ai/settings/keys"), Plans: []ProviderPlan{}},
	{ID: "deepseek", Name: "DeepSeek", Vendor: "DeepSeek", Hue: 230, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://api.deepseek.com/v1", "sk-...", "https://platform.deepseek.com/api_keys"), Plans: []ProviderPlan{}},
	{ID: "groq", Name: "Groq", Vendor: "Groq", Hue: 25, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://api.groq.com/openai/v1", "gsk_...", "https://console.groq.com/keys"), Plans: []ProviderPlan{}},
	{ID: "mistral", Name: "Mistral", Vendor: "Mistral AI", Hue: 70, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://api.mistral.ai/v1", "32 letters and digits", "https://console.mistral.ai/api-keys"), Plans: []ProviderPlan{}},
	{ID: "together", Name: "Together AI", Vendor: "Together AI", Hue: 120, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://api.together.xyz/v1", "tgp_v1_...", "https://api.together.ai/settings/api-keys"), Plans: []ProviderPlan{}},
	{ID: "fireworks", Name: "Fireworks AI", Vendor: "Fireworks AI", Hue: 15, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://api.fireworks.ai/inference/v1", "fw_...", "https://fireworks.ai/account/api-keys"), Plans: []ProviderPlan{}},
	{ID: "cerebras", Name: "Cerebras", Vendor: "Cerebras", Hue: 95, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://api.cerebras.ai/v1", "csk-...", "https://cloud.cerebras.ai/"), Plans: []ProviderPlan{}},
	{ID: "moonshot", Name: "Moonshot", Vendor: "Moonshot AI", Hue: 340, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://api.moonshot.ai/v1", "sk-...", "https://platform.moonshot.ai/console/api-keys"), Plans: []ProviderPlan{}},
	// Z.ai keys and sign-ins both use the GLM Coding Plan endpoint, as NeoPi
	// does; the general api/paas/v4 endpoint bills pay-as-you-go balance instead.
	{ID: "zai", Name: "Z.ai", Vendor: "Zhipu AI", Hue: 190, Chroma: providerChroma, Methods: []string{"subscription", "apiKey"},
		Subscription: (&ProviderSubscription{Flow: "code", Note: "Opens Z.ai's sign-in in a new tab. Z.ai then hands off to its ZCode app, so copy that zcode:// address (or the code in it) and paste it here. NeoProxy creates or reuses the durable API key NeoPi uses, named \"oh-my-pi\"; it never expires."}).at("Z.ai"),
		APIKey:       openAIKey(zaiauth.CodingPlanBaseURL, "32 hex digits, a dot, then more", "https://z.ai/manage-apikey/apikey-list"),
		Plans:        zaiPlans},
	{ID: "nanogpt", Name: "NanoGPT", Vendor: "NanoGPT", Hue: 150, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://nano-gpt.com/api/v1", "sk-nano-...", "https://nano-gpt.com/api"), Plans: []ProviderPlan{}},
	{ID: "alibaba-token-plan", Name: "QwenCloud Token Plan", Vendor: "Qwen", Hue: 267, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey:     openAIKey(qwenTokenPlanIntlURL, "sk-sp-... (International or China key)", "https://home.qwencloud.com/billing/subscription/token-plan-individual"),
		Plans:      []ProviderPlan{{"lite", "Token Plan Lite", 6}, {"essential", "Token Plan Essential", 10}, {"standard", "Token Plan Standard", 18}, {"pro", "Token Plan Pro", 68}},
		regions:    []string{qwenTokenPlanIntlURL, qwenTokenPlanCNURL},
		skipModels: []string{"fun-asr", "happyhorse-", "qwen-audio-", "qwen-image-", "text-embedding-", "wan"}},
	{ID: "opencode-go", Name: "OpenCode Go", Vendor: "OpenCode", Hue: 242, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey:       openAIKey("https://opencode.ai/zen/go/v1", "oc_... (OpenCode Go key)", "https://opencode.ai/auth"),
		Plans:        []ProviderPlan{{"go", "Go", 10}, {"go-plus", "Go Plus", 40}, {"go-teams", "Go Teams (per member)", 20}},
		dialectRules: openCodeDialectRules, sessionHeader: openCodeSessionHeader, headers: openCodeHeaders},
	{ID: "opencode-zen", Name: "OpenCode Zen", Vendor: "OpenCode", Hue: 160, Chroma: tintChroma, Methods: []string{"apiKey"},
		APIKey:       openAIKey("https://opencode.ai/zen/v1", "oc_... (OpenCode Zen key)", "https://opencode.ai/auth"),
		Plans:        []ProviderPlan{},
		dialectRules: openCodeDialectRules, sessionHeader: openCodeSessionHeader, headers: openCodeHeaders},
	{ID: "sakana", Name: "Sakana AI", Vendor: "Sakana AI", Hue: 357, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://api.sakana.ai/v1", "sk-...", "https://console.sakana.ai/api-keys"), Plans: []ProviderPlan{},
		dialect: "responses"},
	{ID: "abliteration", Name: "Abliteration", Vendor: "Abliteration", Hue: 57, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://api.abliteration.ai/v1", "ak_...", "https://abliteration.ai/console"), Plans: []ProviderPlan{},
		dialect: "responses"},
	{ID: "huggingface", Name: "Hugging Face", Vendor: "Hugging Face", Hue: 85, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://router.huggingface.co/v1", "hf_...", "https://huggingface.co/settings/tokens/new?ownUserPermissions=inference.serverless.write&tokenType=fineGrained"), Plans: []ProviderPlan{}},
	// NVIDIA's free catalogue keeps listing models an account cannot call
	// (404 "Function ... Not found for account") or that reached end of life
	// (410 Gone). Those concern the requested model only, never the whole key.
	{ID: "nvidia", Name: "NVIDIA NIM", Vendor: "NVIDIA", Hue: 135, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://integrate.api.nvidia.com/v1", "nvapi-...", "https://build.nvidia.com/settings/api-keys"), Plans: []ProviderPlan{},
		upstreamErrors: []config.RequestScopedErrorRule{
			{Status: 404, Match: []string{"Not Found", "not found"}, Action: "stop"},
			{Status: 410, Match: []string{"Gone", "end of life"}, Action: "stop"},
		}},
	{ID: "vercel-ai-gateway", Name: "Vercel AI Gateway", Vendor: "Vercel", Hue: 300, Chroma: tintChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://ai-gateway.vercel.sh/v1", "vck_...", "https://vercel.com/d?to=%2F%5Bteam%5D%2F%7E%2Fai-gateway%2Fapi-keys&title=AI+Gateway+API+Keys"), Plans: []ProviderPlan{}},
	{ID: "nous-portal", Name: "Nous Portal", Vendor: "Nous Research", Hue: 320, Chroma: providerChroma, Methods: []string{"subscription", "apiKey"},
		Subscription: device("Opens the Nous Portal in a new tab and shows a code to confirm there. NeoProxy keeps the sign-in refreshed.").at("Nous Portal"),
		APIKey:       openAIKey("https://inference-api.nousresearch.com/v1", "sk-...", "https://portal.nousresearch.com/"), Plans: []ProviderPlan{}},
	// Marineris is a self-hosted gateway, so its address is chosen per key;
	// the default is the gateway's own documented listen address.
	{ID: "marineris", Name: "Marineris", Vendor: "Marineris gateway", Hue: 35, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: &ProviderAPIKey{Style: "openai", BaseURL: "http://192.168.50.50:8801/v1", EditableBaseURL: true, KeyHint: "mrn_... (issued by the gateway's administrator)"},
		Plans:  []ProviderPlan{}},
	{ID: "cursor", Name: "Cursor", Vendor: "Anysphere", Hue: 75, Chroma: tintChroma, Methods: []string{"subscription"},
		Subscription: browser("Opens Cursor's sign-in in a new tab; NeoProxy finishes once you approve it there and keeps the sign-in refreshed.").at("Cursor"),
		Plans:        cursorPlans},
	{ID: "custom", Name: "OpenAI-compatible server", Vendor: "Any", Hue: 0, Chroma: 0, Methods: []string{"apiKey"},
		APIKey: &ProviderAPIKey{Style: "openai", BaseURL: "http://localhost:11434/v1", EditableBaseURL: true, KeyHint: "Leave empty for local servers such as Ollama or LM Studio", KeyOptional: true},
		Plans:  []ProviderPlan{}},
}

// catalogBaseURL resolves a catalogue provider's fixed API base URL. Tests may
// replace it to point a known provider at a fake upstream.
var catalogBaseURL = func(id string) string {
	if p, ok := catalogProvider(id); ok && p.APIKey != nil {
		return p.APIKey.BaseURL
	}
	return ""
}

// catalogRegions resolves a catalogue provider's regional endpoints (nil for
// single-endpoint providers). Tests may replace it like catalogBaseURL.
var catalogRegions = func(id string) []string {
	p, _ := catalogProvider(id)
	return p.regions
}

func catalogProvider(id string) (ProviderInfo, bool) {
	for _, p := range providerCatalog {
		if p.ID == id {
			return p, true
		}
	}
	return ProviderInfo{}, false
}

func catalogPlan(provider, planID string) (ProviderPlan, bool) {
	p, ok := catalogProvider(provider)
	if !ok {
		return ProviderPlan{}, false
	}
	for _, plan := range p.Plans {
		if plan.ID == planID {
			return plan, true
		}
	}
	return ProviderPlan{}, false
}

var compatSuffix = regexp.MustCompile(`-\d+$`)

// accessProviderID is the catalogue ID an engine credential is shown under.
// NeoPi-served credentials keep NeoPi's provider name; custom OpenAI-compatible
// entries whose address is a known provider's fixed API (for example keys
// imported before that provider had its own entry) show as that provider.
func accessProviderID(a *coreauth.Auth, kind string) string {
	if kind == "neopi" {
		return catalogProviderID(a.Attributes["neopi_provider"], "")
	}
	id := catalogProviderID(a.Provider, a.Attributes["compat_name"])
	if id != "custom" {
		return id
	}
	base := strings.TrimRight(strings.TrimSpace(a.Attributes["base_url"]), "/")
	for _, p := range providerCatalog {
		if p.engine == "" && p.APIKey != nil && !p.APIKey.EditableBaseURL && base != "" && strings.EqualFold(p.APIKey.BaseURL, base) {
			return p.ID
		}
	}
	return id
}

// catalogProviderID maps an engine provider (or a NeoPi provider name) to the
// catalogue ID shown to people. Unknown engine providers keep their own ID.
func catalogProviderID(engineProvider, compatName string) string {
	if compatName = strings.ToLower(strings.TrimSpace(compatName)); compatName != "" {
		base := compatSuffix.ReplaceAllString(strings.TrimPrefix(compatName, "openai-compatible-"), "")
		if p, ok := catalogProvider(base); ok && p.engine == "" && p.APIKey != nil && base != "custom" {
			return base
		}
		return "custom"
	}
	switch id := strings.ToLower(strings.TrimSpace(engineProvider)); id {
	case "codex", "openai", "openai-codex", "chatgpt":
		return "codex"
	case "claude", "anthropic", "claude-code":
		return "claude"
	case "gemini", "gemini-cli", "google", "vertex", "gemini-interactions", "aistudio", "google-gemini":
		return "gemini"
	case "antigravity", "google-antigravity":
		return "antigravity"
	case "xai", "grok":
		return "xai"
	case "kimi", "kimi-code", "kimi-coding":
		return "kimi"
	case "meta", "meta-ai":
		return "meta"
	default:
		return id
	}
}

// providerList returns the catalogue plus a neutral entry for every engine
// provider present in items but unknown to the catalogue.
func providerList(items []AccessItem) []ProviderInfo {
	out := make([]ProviderInfo, 0, len(providerCatalog)+2)
	out = append(out, providerCatalog...)
	seen := map[string]bool{}
	for _, p := range providerCatalog {
		seen[p.ID] = true
	}
	for _, item := range items {
		if seen[item.Provider] {
			continue
		}
		seen[item.Provider] = true
		out = append(out, ProviderInfo{ID: item.Provider, Name: item.Provider, Vendor: item.Provider, Methods: []string{}, Plans: []ProviderPlan{}})
	}
	return out
}
