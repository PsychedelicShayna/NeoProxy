package executor

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// GatewayExecutor serves an openai-compatibility account whose endpoint is a
// multi-wire gateway NeoPi knows: OpenCode Go, OpenCode Zen or the Vercel AI
// Gateway. Each model is sent on the API NeoPi routes it to, with NeoPi's
// endpoint, headers, body and stream handling for that API.
type GatewayExecutor struct {
	*ClaudeExecutor
	cfg *config.Config
	// gateway is NeoPi's provider id (registry.GatewayOpenCodeGo, …).
	gateway string
	// counter estimates tokens locally; NeoPi never asks the gateway to count.
	counter *OpenAICompatExecutor
	// completions, responses and google speak NeoPi's openai-completions,
	// openai-responses and google-generative-ai routes; all are the shared
	// NeoPi engines.
	completions *NeoPiCompletionsExecutor
	responses   *NeoPiResponsesExecutor
	google      *NeoPiGoogleExecutor
}

// NewGatewayExecutor binds a gateway executor to an openai-compatibility
// provider key.
func NewGatewayExecutor(provider, gateway string, cfg *config.Config) *GatewayExecutor {
	messages := NewClaudeExecutor(cfg)
	messages.provider = provider
	messages.thirdParty = gatewayThirdPartyProvider(gateway)
	messages.thirdParty.bearer = gatewayAPIKey
	return &GatewayExecutor{
		ClaudeExecutor: messages, cfg: cfg, gateway: gateway, counter: NewOpenAICompatExecutor(provider, cfg),
		completions: NewNeoPiCompletionsExecutor(provider, cfg, gatewayCompletionsProfile(gateway)),
		responses:   gatewayResponsesExecutor(provider, gateway, cfg),
		google:      NewNeoPiGoogleExecutor(provider, cfg, gatewayGoogleHooks(gateway)),
	}
}

// gatewayThirdPartyProvider is what the Anthropic Messages route needs from a
// gateway: its catalog row, its key, X-Api-Key auth on OpenCode, the
// conversation header, and the Vercel gateway's app attribution.
func gatewayThirdPartyProvider(gateway string) *neoPiThirdPartyProvider {
	return &neoPiThirdPartyProvider{
		catalog:       gatewayCatalogRow(gateway),
		apiKeyHeader:  gateway == registry.GatewayOpenCodeGo || gateway == registry.GatewayOpenCodeZen,
		sessionHeader: gatewaySessionHeader(gateway),
		headers:       gatewayIdentityHeaders(gateway),
	}
}

// UsesConfig reports whether the executor was built for cfg.
func (e *GatewayExecutor) UsesConfig(cfg *config.Config) bool { return e != nil && e.cfg == cfg }

// UsesGateway reports whether the executor serves gateway.
func (e *GatewayExecutor) UsesGateway(gateway string) bool { return e != nil && e.gateway == gateway }

// Gateway is NeoPi's provider id this executor serves.
func (e *GatewayExecutor) Gateway() string { return e.gateway }

// SupportsNativeToolSearch keeps gateways out of Anthropic's native tool search.
func (e *GatewayExecutor) SupportsNativeToolSearch(*cliproxyauth.Auth, string) bool { return false }

// Refresh is a no-op: a gateway key never expires.
func (e *GatewayExecutor) Refresh(_ context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	return auth, nil
}

// HttpRequest sends an arbitrary request with the account's bearer key.
func (e *GatewayExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, request *http.Request) (*http.Response, error) {
	if request == nil {
		return nil, fmt.Errorf("gateway executor: request is nil")
	}
	if key := gatewayAPIKey(auth); key != "" {
		request.Header.Set("Authorization", "Bearer "+key)
	}
	if auth != nil {
		util.ApplyCustomHeadersFromAttrs(request, auth.Attributes)
	}
	return helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(request)
}

// CountTokens estimates locally from the Chat Completions form.
func (e *GatewayExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.counter.CountTokens(ctx, auth, req, opts)
}

// RequestToFormat reports the dialect the selected catalog row is sent in.
func (e *GatewayExecutor) RequestToFormat(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) sdktranslator.Format {
	if row, err := e.routeRow(req); err == nil {
		switch row.API {
		case "openai-completions":
			return e.completions.RequestToFormat(req, opts)
		case "google-generative-ai":
			return e.google.RequestToFormat(req, opts)
		case "openai-responses":
			return sdktranslator.FormatCodex
		}
	}
	return sdktranslator.FormatClaude
}

// gatewayCatalogRow is NeoPi's record of the requested model: the row
// discovery registered for the account, else NeoPi's catalog.
func gatewayCatalogRow(gateway string) func(cliproxyexecutor.Request, string) *registry.NeoPiModel {
	return func(req cliproxyexecutor.Request, model string) *registry.NeoPiModel {
		if info, ok := cliproxyauth.ResolvedModelInfo(req); ok && info != nil && info.NeoPi != nil {
			row := info.NeoPi
			if row.Provider != gateway {
				row = row.Clone()
				row.Provider = gateway
			}
			return row
		}
		return registry.GatewayNeoPiModel(gateway, model)
	}
}

func gatewayAPIKey(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if key := strings.TrimSpace(auth.Attributes["api_key"]); key != "" {
		return key
	}
	key, _ := auth.Metadata["api_key"].(string)
	return strings.TrimSpace(key)
}

// gatewaySessionHeader is the header NeoPi uses to attribute and cache-affinitise
// a gateway request to its conversation (compat.sessionHeader).
func gatewaySessionHeader(gateway string) string {
	if gateway == registry.GatewayOpenCodeGo || gateway == registry.GatewayOpenCodeZen {
		return "x-opencode-session"
	}
	return ""
}

// gatewayIdentityHeaders is applyInferenceHeaders' provider half: the Vercel
// gateway's app attribution. OpenCode's identity is its session header and
// NeoPi's user agent, which the request headers already carry.
func gatewayIdentityHeaders(gateway string) func() http.Header {
	return func() http.Header {
		headers := make(http.Header)
		if gateway == registry.GatewayVercel {
			headers.Set("http-referer", "https://github.com/PsychedelicShayna/neopi")
			headers.Set("x-title", "npi")
		}
		return headers
	}
}

// Execute sends the request on the row's API.
func (e *GatewayExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	opts = e.withSession(auth, req, opts)
	if row, err := e.routeRow(req); err != nil {
		return cliproxyexecutor.Response{}, err
	} else if row.API != "anthropic-messages" {
		return e.executeOther(ctx, auth, req, opts, row)
	}
	return e.ClaudeExecutor.Execute(ctx, auth, req, opts)
}

// ExecuteStream streams the request on the row's API.
func (e *GatewayExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	opts = e.withSession(auth, req, opts)
	if row, err := e.routeRow(req); err != nil {
		return nil, err
	} else if row.API != "anthropic-messages" {
		return e.streamOther(ctx, auth, req, opts, row)
	}
	return e.ClaudeExecutor.ExecuteStream(ctx, auth, req, opts)
}

// withSession gives a request that carries no conversation identity the
// account's install id, as NeoPi does outside a conversation: OpenCode answers
// 400 MissingSessionID to a request without x-opencode-session. The id is
// stable per account, so the gateway's cache affinity holds across requests.
func (e *GatewayExecutor) withSession(auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) cliproxyexecutor.Options {
	if gatewaySessionHeader(e.gateway) == "" || helps.ProviderSessionUUID(e.Identifier(), req.Metadata, opts.Metadata) != "" {
		return opts
	}
	metadata := maps.Clone(opts.Metadata)
	if metadata == nil {
		metadata = make(map[string]any, 1)
	}
	metadata[cliproxyexecutor.ExecutionSessionMetadataKey] = gatewayInstallID(e.gateway, auth)
	opts.Metadata = metadata
	return opts
}

// executeOther sends a non-Anthropic row through the engine that speaks its API.
func (e *GatewayExecutor) executeOther(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, row *registry.NeoPiModel) (cliproxyexecutor.Response, error) {
	engine, err := e.engineFor(row.API)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return engine.Execute(ctx, auth, req, opts)
}

// streamOther streams a non-Anthropic row through the engine that speaks its API.
func (e *GatewayExecutor) streamOther(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, row *registry.NeoPiModel) (*cliproxyexecutor.StreamResult, error) {
	engine, err := e.engineFor(row.API)
	if err != nil {
		return nil, err
	}
	return engine.ExecuteStream(ctx, auth, req, opts)
}

// gatewayEngine is the executor that speaks one of NeoPi's OpenAI-family APIs.
type gatewayEngine interface {
	Execute(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
	ExecuteStream(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error)
}

// engineFor selects the executor for a row's API.
func (e *GatewayExecutor) engineFor(api string) (gatewayEngine, error) {
	switch api {
	case "openai-completions":
		return e.completions, nil
	case "openai-responses":
		return e.responses, nil
	case "google-generative-ai":
		return e.google, nil
	}
	return nil, statusErr{code: http.StatusBadRequest, msg: fmt.Sprintf("%s: NeoPi's %s API is not a wire this proxy speaks", e.Identifier(), api)}
}

// routeRow is NeoPi's record of the request's model, erroring when NeoPi's
// catalogs do not serve it.
func (e *GatewayExecutor) routeRow(req cliproxyexecutor.Request) (*registry.NeoPiModel, error) {
	row := gatewayCatalogRow(e.gateway)(req, thinking.ParseSuffix(req.Model).ModelName)
	if row == nil {
		return nil, statusErr{code: http.StatusBadRequest, msg: fmt.Sprintf("%s: model %s is not in NeoPi's %s catalog", e.Identifier(), req.Model, e.gateway)}
	}
	return row, nil
}

// gatewayCompletionsProfile binds NeoPi's Chat Completions engine to a
// gateway: the gateway's own catalog rows, its bearer key and its identity
// headers on top of the engine's.
func gatewayCompletionsProfile(gateway string) NeoPiCompletionsProfile {
	return NeoPiCompletionsProfile{
		Provider: gateway,
		Row:      func(provider, model string) *registry.NeoPiModel { return registry.GatewayNeoPiModel(provider, model) },
		APIKey:   gatewayAPIKey,
		Headers: func(_ context.Context, _ *cliproxyauth.Auth, _ http.Header, _ string, _ *registry.NeoPiModel, headers http.Header, sessionID string) {
			applyGatewayIdentity(headers, gateway, sessionID)
		},
	}
}

// gatewayResponsesExecutor binds NeoPi's Responses engine to a gateway: the
// gateway's own catalog rows, reached through streamSimple.
func gatewayResponsesExecutor(provider, gateway string, cfg *config.Config) *NeoPiResponsesExecutor {
	engine := NewNeoPiResponsesExecutor(provider, cfg)
	engine.Row = func(_ *cliproxyauth.Auth, req cliproxyexecutor.Request) *registry.NeoPiModel {
		return gatewayCatalogRow(gateway)(req, thinking.ParseSuffix(req.Model).ModelName)
	}
	engine.StreamSimple = true
	return engine
}

// gatewayGoogleHooks binds NeoPi's Google engine to a gateway. The endpoint
// is the row's own (OpenCode Go serves generateContent under its /v1 tree and
// Zen under its own Google host path), never the account's base URL.
func gatewayGoogleHooks(gateway string) NeoPiGoogleHooks {
	return NeoPiGoogleHooks{
		Row: gatewayCatalogRow(gateway),
		Key: gatewayAPIKey,
		BaseURL: func(_ *cliproxyauth.Auth, row *registry.NeoPiModel) string {
			return strings.TrimRight(row.BaseURL, "/")
		},
		Headers: func(_ context.Context, _ *cliproxyauth.Auth, _ http.Header, sessionID string) http.Header {
			headers := make(http.Header)
			applyGatewayIdentity(headers, gateway, sessionID)
			return headers
		},
	}
}

// applyGatewayIdentity is NeoPi's applyInferenceHeaders for a gateway backend:
// the Vercel app attribution, and OpenCode's conversation header.
func applyGatewayIdentity(headers http.Header, gateway, sessionID string) {
	for name, values := range gatewayIdentityHeaders(gateway)() {
		headers[name] = values
	}
	if name := gatewaySessionHeader(gateway); name != "" && sessionID != "" {
		headers.Set(name, sessionID)
	}
}
