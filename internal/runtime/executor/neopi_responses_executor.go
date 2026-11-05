package executor

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	metaauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/meta"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/openaiwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tiktoken-go/tokenizer"
)

var (
	_ cliproxyauth.ProviderExecutor    = (*NeoPiResponsesExecutor)(nil)
	_ cliproxyauth.RequestAuthPreparer = (*NeoPiResponsesExecutor)(nil)
)

// neoPiResponsesDialect is the wire dialect the proxy's thinking and payload
// rules treat the Responses body as: the Codex-shaped Responses form the
// translators emit, which NeoPi's builder then reshapes.
const neoPiResponsesDialect = "codex"

// neoPiResponsesMuseVersion is x-api-version, which NeoPi's muse-code
// transport adds to every Model API request (registry/muse-code.ts).
const neoPiResponsesMuseVersion = "1.0.0"

// NeoPiResponsesExecutor serves the providers NeoPi runs on its generic
// openai-responses transport: Meta (a Model API key or a Muse subscription)
// and the key-only Sakana and Abliteration gateways. Requests are shaped by
// internal/openaiwire from the NeoPi catalog row of the requested model, so the
// upstream sees what NeoPi sends. The executor is registered under each
// provider's own id; the auth decides which NeoPi provider (and catalog) a
// request belongs to.
type NeoPiResponsesExecutor struct {
	cfg        *config.Config
	identifier string
}

// NewNeoPiResponsesExecutor creates the executor for one provider id: "meta",
// or the openai-compatibility entry name of a Sakana/Abliteration key.
func NewNeoPiResponsesExecutor(identifier string, cfg *config.Config) *NeoPiResponsesExecutor {
	return &NeoPiResponsesExecutor{cfg: cfg, identifier: strings.ToLower(strings.TrimSpace(identifier))}
}

// UsesConfig reports whether the executor was built for cfg.
func (e *NeoPiResponsesExecutor) UsesConfig(cfg *config.Config) bool { return e != nil && e.cfg == cfg }

// Identifier returns the provider id the executor is registered under.
func (e *NeoPiResponsesExecutor) Identifier() string {
	if e == nil || e.identifier == "" {
		return "meta"
	}
	return e.identifier
}

// ForAPIKey binds configuration once after credential selection.
func (e NeoPiResponsesExecutor) ForAPIKey() cliproxyauth.ProviderExecutor {
	e.cfg = e.cfg.ForAPIKey()
	return &e
}

// SupportsApplyPatch reports the actual executor contract, independent of its provider name.
func (e *NeoPiResponsesExecutor) SupportsApplyPatch() bool { return e != nil }

// neoPiResponsesProviderOf resolves the NeoPi provider an auth is served as:
// a Meta sign-in is Muse Code, a Meta key is the Model API, and a key at a
// Sakana or Abliteration address is that gateway. The empty string is a
// provider this executor has no catalog for.
func neoPiResponsesProviderOf(identifier string, auth *cliproxyauth.Auth) string {
	if identifier == "meta" || auth != nil && strings.EqualFold(strings.TrimSpace(auth.Provider), "meta") {
		if auth != nil && (auth.AuthKind() == cliproxyauth.AuthKindOAuth || extractDCAToken(auth) != "") && !cliproxyauth.IsConfigAPIKeyAuth(auth) {
			return registry.ResponsesMuseCode
		}
		return registry.ResponsesMeta
	}
	if auth != nil {
		if provider := registry.ResponsesProviderForBaseURL(auth.Attributes["base_url"]); provider != "" {
			return provider
		}
	}
	return ""
}

func (e *NeoPiResponsesExecutor) neoPiResponsesProvider(auth *cliproxyauth.Auth) string {
	return neoPiResponsesProviderOf(e.Identifier(), auth)
}

// neoPiResponsesCredsOf returns the endpoint and bearer an auth carries.
func neoPiResponsesCredsOf(identifier string, auth *cliproxyauth.Auth) (baseURL, token string) {
	if identifier == "meta" || auth != nil && strings.EqualFold(strings.TrimSpace(auth.Provider), "meta") {
		return metaCreds(auth)
	}
	if auth == nil {
		return "", ""
	}
	baseURL = strings.TrimSpace(auth.Attributes["base_url"])
	token = strings.TrimSpace(auth.Attributes["api_key"])
	if token == "" {
		if key, ok := auth.Metadata["api_key"].(string); ok {
			token = strings.TrimSpace(key)
		}
	}
	return baseURL, token
}

func (e *NeoPiResponsesExecutor) neoPiResponsesCreds(auth *cliproxyauth.Auth) (baseURL, token string) {
	return neoPiResponsesCredsOf(e.Identifier(), auth)
}

// neoPiResponsesModel returns the NeoPi model a chat request runs on: the
// account's resolved record when it carries an openai-responses one (any
// provider's, endpoint included), else the catalog row of the provider the
// auth is served as. For a catalog provider an address other than the
// catalog's (a configured base URL) wins over the bundled one.
func (e *NeoPiResponsesExecutor) neoPiResponsesModel(auth *cliproxyauth.Auth, req cliproxyexecutor.Request) *registry.NeoPiModel {
	provider := e.neoPiResponsesProvider(auth)
	var model *registry.NeoPiModel
	if info, ok := cliproxyauth.ResolvedModelInfo(req); ok && info != nil && info.NeoPi != nil && info.NeoPi.API == "openai-responses" {
		model = info.NeoPi.Clone()
	} else if provider != "" {
		model = registry.ResponsesNeoPiModel(provider, thinking.ParseSuffix(req.Model).ModelName, nil)
	}
	if model == nil || model.Compat == nil || model.API != "openai-responses" {
		return nil
	}
	if provider != "" {
		if baseURL, _ := e.neoPiResponsesCreds(auth); baseURL != "" && strings.TrimRight(baseURL, "/") != strings.TrimRight(registry.ResponsesProviderBaseURL(provider), "/") {
			model.BaseURL = baseURL
		}
	}
	return model
}

// PrepareRequest injects the provider's credentials into an outgoing request.
func (e *NeoPiResponsesExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	_, token := e.neoPiResponsesCreds(auth)
	if strings.TrimSpace(token) != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	} else {
		req.Header.Del("Authorization")
	}
	if e.neoPiResponsesProvider(auth) == registry.ResponsesMuseCode {
		req.Header.Set("x-api-version", neoPiResponsesMuseVersion)
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", openaiwire.BunUserAgent)
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
	return nil
}

// HttpRequest injects credentials into the request and executes it.
func (e *NeoPiResponsesExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("%s executor: request is nil", e.Identifier())
	}
	if ctx == nil {
		ctx = req.Context()
	}
	prepared, err := e.ensureAuth(ctx, auth)
	if err != nil {
		return nil, err
	}
	httpReq := req.WithContext(ctx)
	if err = e.PrepareRequest(httpReq, prepared); err != nil {
		return nil, err
	}
	return helps.NewProxyAwareHTTPClient(ctx, e.cfg, prepared, 0).Do(httpReq)
}

// CountTokens estimates the request's input locally; no upstream counts tokens.
func (e *NeoPiResponsesExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if errExpand := helps.ExpandResponsesCompactionCapsules(&req, &opts, e.Identifier()); errExpand != nil {
		return cliproxyexecutor.Response{}, statusErr{code: http.StatusBadRequest, msg: errExpand.Error()}
	}
	to := sdktranslator.FormatCodex
	model := e.neoPiResponsesModel(auth, req)
	if model == nil {
		return cliproxyexecutor.Response{}, statusErr{code: http.StatusBadRequest, msg: fmt.Sprintf("%s: model %s is not in NeoPi's catalog", e.Identifier(), req.Model)}
	}
	// The count reads the bytes Execute would send: shaped as NeoPi shapes them,
	// payload rules applied (once).
	_, turn, err := e.shapeNeoPi(ctx, auth, model, req, opts)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	body := turn.request.Body
	enc, err := tokenizer.Get(tokenizer.O200kBase)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("%s executor: tokenizer init failed: %w", e.Identifier(), err)
	}
	count, err := countCodexInputTokens(enc, body)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("%s executor: token counting failed: %w", e.Identifier(), err)
	}
	usageJSON := fmt.Sprintf(`{"response":{"usage":{"input_tokens":%d,"output_tokens":0,"total_tokens":%d}}}`, count, count)
	translated := sdktranslator.TranslateTokenCount(ctx, to, cliproxyexecutor.ResponseFormatOrSource(opts), count, []byte(usageJSON))
	return cliproxyexecutor.Response{Payload: translated}, nil
}

// Execute runs a non-streaming request through NeoPi's transport.
func (e *NeoPiResponsesExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	ctx = helps.EnsureSessionContext(ctx, opts, req.Payload)
	if opts.Alt == "responses/compact" {
		return cliproxyexecutor.Response{}, statusErr{code: http.StatusNotImplemented, msg: "/responses/compact not supported"}
	}
	if errExpand := helps.ExpandResponsesCompactionCapsules(&req, &opts, e.Identifier()); errExpand != nil {
		return cliproxyexecutor.Response{}, statusErr{code: http.StatusBadRequest, msg: errExpand.Error()}
	}
	if helps.HasResponsesCompactionTriggerRequest(req, opts) {
		return e.executeCompaction(ctx, auth, req, opts)
	}
	prepared, err := e.ensureAuth(ctx, auth)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	model := e.neoPiResponsesModel(prepared, req)
	if model == nil {
		return cliproxyexecutor.Response{}, statusErr{code: http.StatusBadRequest, msg: fmt.Sprintf("%s: model %s is not in NeoPi's catalog", e.Identifier(), req.Model)}
	}
	return e.executeNeoPi(ctx, prepared, model, req, opts)
}

// ExecuteStream runs a streaming request through NeoPi's transport.
func (e *NeoPiResponsesExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	ctx = helps.EnsureSessionContext(ctx, opts, req.Payload)
	if opts.Alt == "responses/compact" {
		return nil, statusErr{code: http.StatusNotImplemented, msg: "/responses/compact not supported"}
	}
	if errExpand := helps.ExpandResponsesCompactionCapsules(&req, &opts, e.Identifier()); errExpand != nil {
		return nil, statusErr{code: http.StatusBadRequest, msg: errExpand.Error()}
	}
	if helps.HasResponsesCompactionTriggerRequest(req, opts) {
		return e.executeCompactionStream(ctx, auth, req, opts)
	}
	prepared, err := e.ensureAuth(ctx, auth)
	if err != nil {
		return nil, err
	}
	model := e.neoPiResponsesModel(prepared, req)
	if model == nil {
		return nil, statusErr{code: http.StatusBadRequest, msg: fmt.Sprintf("%s: model %s is not in NeoPi's catalog", e.Identifier(), req.Model)}
	}
	return e.streamNeoPi(ctx, prepared, model, req, opts)
}

// ensureAuth returns the auth with a usable bearer: a Muse sign-in whose key
// was never minted (login stored only the device token) mints it first.
func (e *NeoPiResponsesExecutor) ensureAuth(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return nil, statusErr{code: http.StatusUnauthorized, msg: e.Identifier() + " executor: missing auth"}
	}
	_, token := e.neoPiResponsesCreds(auth)
	if token == "" && e.Identifier() == "meta" {
		if dca := extractDCAToken(auth); dca != "" {
			refreshed, err := e.Refresh(ctx, auth)
			if err != nil {
				return nil, err
			}
			auth = refreshed
			_, token = e.neoPiResponsesCreds(auth)
		}
	}
	if token == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: e.Identifier() + " executor: missing API key or access token"}
	}
	return auth, nil
}

// metaAPIBase is the Model API base the catalog binds for Meta.
func metaAPIBase() string { return strings.TrimRight(metaauth.DefaultAPIBaseURL, "/") }
