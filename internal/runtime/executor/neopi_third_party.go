package executor

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/anthropicnpi"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// applyThirdPartyIdentity applies NeoPi's client auth choice and conversation
// header: endpoints that validate X-Api-Key never see an Authorization header,
// and the conversation id rides next to the protocol's own session header.
func applyThirdPartyIdentity(headers http.Header, provider *neoPiThirdPartyProvider, bearer, session string) {
	if provider == nil {
		return
	}
	if provider.apiKeyHeader {
		headers.Del("Authorization")
		if bearer != "" && bearer != "N/A" {
			headers.Set("X-Api-Key", bearer)
		}
	}
	if provider.sessionHeader != "" && session != "" {
		headers.Set(provider.sessionHeader, session)
	}
}

// prepareThirdParty shapes an Anthropic Messages request for a model NeoPi
// serves over Anthropic Messages on another provider's endpoint, with that
// provider's bearer and identity headers.
func (e *ClaudeExecutor) prepareThirdParty(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*neoPiPrepared, error) {
	provider := e.thirdParty
	bearer := strings.TrimSpace(provider.bearer(auth))
	if bearer == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: e.Identifier() + ": account has no credential"}
	}
	model := thinking.ParseSuffix(req.Model).ModelName
	row := provider.catalog(req, model)
	if row == nil || row.API != "anthropic-messages" {
		return nil, statusErr{code: http.StatusBadRequest, msg: fmt.Sprintf("%s: model %s is not served over Anthropic Messages", e.Identifier(), model)}
	}
	original := req.Payload
	if len(opts.OriginalRequest) > 0 {
		original = opts.OriginalRequest
	}
	originalTranslated, translated, err := helps.TranslateRequestPairReturningError(ctx, opts.Headers, e.cfg, opts.SourceFormat, sdktranslator.FormatClaude, model, original, req.Payload, true, helps.APIKeyModelIsCompat(req))
	if err != nil {
		return nil, err
	}
	if translated, err = helps.ApplyRequestThinking(translated, req, opts, opts.SourceFormat.String(), "claude", e.Identifier()); err != nil {
		return nil, err
	}
	session := neoPiSession(opts, translated)
	if session == "" {
		// NeoPi's streamSimple always carries an inference session id.
		session = helps.ProviderSessionUUID(e.Identifier(), req.Metadata, opts.Metadata)
	}
	identity := neoPiWireIdentity{SessionID: session, APIKey: true}
	base := strings.TrimRight(row.BaseURL, "/")
	profile := neoPiThirdPartyProfile(row)
	if provider.headers != nil {
		profile.thirdParty.headers = provider.headers()
	}
	body, headers, err := projectNeoPiAnthropic(translated, model, base, "", identity, profile)
	if err != nil {
		return nil, err
	}
	finalize := helps.NewPayloadFinalizer(e.cfg, e.Identifier(), model, "claude", "", originalTranslated, req, opts)
	body = finalize(body)
	neoPiApplyHeaders(headers, auth, opts.Headers, &anthropicnpi.Credential{APIKey: bearer}, base, ctx)
	applyThirdPartyIdentity(headers, provider, bearer, session)
	return &neoPiPrepared{auth: auth, body: body, translated: translated, headers: headers, url: base + "/v1/messages", model: model, identity: identity, base: base, finalize: finalize}, nil
}
