package openaiwire

import (
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/codexwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopiversion"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

// NeoPiUserAgent is USER_AGENT of the NeoPi build these ports follow.
const NeoPiUserAgent = "npi/" + neopiversion.Version

// BunUserAgent is the User-Agent Bun's fetch sends when NeoPi's request names
// none: every Responses host but xAI (which sets USER_AGENT itself) sees it.
const BunUserAgent = "Bun/1.3.14"

// firstEventTimeoutSeconds is NeoPi's default first-event watchdog (300s),
// announced to the server as X-Stainless-Timeout.
const firstEventTimeoutSeconds = "300"

// Request is one shaped Responses POST.
type Request struct {
	URL    string
	Header http.Header
	Body   []byte
}

// ShapeResponses builds the request NeoPi's streamOpenAIResponses sends for a
// downstream turn: resolveOpenAIRequestSetup's headers, postOpenAIStream's
// framing headers and buildParams' body.
func ShapeResponses(model *registry.NeoPiModel, ctx Context, opts Options, accessToken string) *Request {
	baseURL := strings.TrimRight(model.BaseURL, "/")
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	header := http.Header{}
	header.Set("Content-Type", "application/json")
	header.Set("Accept", "text/event-stream")
	if name := model.Compat.PromptCacheSessionHeader; name != "" {
		if key := codexwire.NormalizePromptCacheKey(opts.SessionID); key != "" {
			header.Set(name, key)
		}
	}
	if model.Provider == "xai" || model.Provider == "xai-oauth" {
		header.Set("User-Agent", NeoPiUserAgent)
	} else {
		header.Set("User-Agent", BunUserAgent)
	}
	if model.Provider == "openrouter" {
		setOpenRouterHeaders(header)
	}
	header.Set("Authorization", "Bearer "+accessToken)
	header.Set("X-Stainless-Timeout", firstEventTimeoutSeconds)
	return &Request{URL: baseURL + "/responses", Header: header, Body: BuildResponses(model, ctx, opts)}
}

// OpenRouter attribution headers, getOpenRouterHeaders: NeoPi's APP_URL and
// PRODUCT_NAME identify the app, the cache pair opts into OpenRouter's response cache.
const (
	neoPiAppURL         = "https://github.com/PsychedelicShayna/neopi"
	neoPiProductName    = "NeoPi"
	openRouterCacheTTL  = "3600"
	openRouterCategory  = "cli-agent"
	openRouterCacheFlag = "true"
)

func setOpenRouterHeaders(header http.Header) {
	header.Set("User-Agent", NeoPiUserAgent)
	header.Set("HTTP-Referer", neoPiAppURL)
	header.Set("X-OpenRouter-Title", neoPiProductName)
	header.Set("X-OpenRouter-Categories", openRouterCategory)
	header.Set("X-OpenRouter-Cache", openRouterCacheFlag)
	header.Set("X-OpenRouter-Cache-TTL", openRouterCacheTTL)
}
