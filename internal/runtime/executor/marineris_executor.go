package executor

import (
	"context"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// MarinerisExecutor serves a Marineris gateway key the way NeoPi's Marineris
// extension does: every model the gateway lists for the key, each on the API
// the extension assigns it, at the gateway address the account carries. Grok
// rows go over NeoPi's openai-completions provider; Codex-class rows
// (gpt-*, codex*, o*) go over its openai-responses provider.
type MarinerisExecutor struct {
	*NeoPiCompletionsExecutor
	responses cliproxyauth.ProviderExecutor
	cfg       *config.Config
}

// NewMarinerisExecutor builds the executor registered under provider, the name
// of the key account's openai-compatibility entry.
func NewMarinerisExecutor(provider string, cfg *config.Config) *MarinerisExecutor {
	profile := NeoPiCompletionsProfile{
		Provider: "marineris",
		// A gateway lists only what its key is scoped to: there is no bundled row.
		Row: func(_, _ string) *registry.NeoPiModel { return nil },
		// The gateway address is the account's, not the catalog's.
		URL: func(row *registry.NeoPiModel) string {
			return strings.TrimRight(row.BaseURL, "/") + "/chat/completions"
		},
	}
	return &MarinerisExecutor{
		NeoPiCompletionsExecutor: NewNeoPiCompletionsExecutor(provider, cfg, profile),
		responses:                newMarinerisResponses(provider, cfg),
		cfg:                      cfg,
	}
}

// marinerisUsesResponses reports whether the account's record for the
// requested model is an openai-responses row.
func marinerisUsesResponses(req cliproxyexecutor.Request) bool {
	info, ok := cliproxyauth.ResolvedModelInfo(req)
	return ok && info != nil && info.NeoPi != nil && info.NeoPi.API == "openai-responses"
}

// RequestToFormat reports the dialect the model's row is sent in.
func (e *MarinerisExecutor) RequestToFormat(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) sdktranslator.Format {
	if marinerisUsesResponses(req) {
		if resolver, ok := e.responses.(interface {
			RequestToFormat(cliproxyexecutor.Request, cliproxyexecutor.Options) sdktranslator.Format
		}); ok {
			return resolver.RequestToFormat(req, opts)
		}
		return sdktranslator.FormatCodex
	}
	return e.NeoPiCompletionsExecutor.RequestToFormat(req, opts)
}

func (e *MarinerisExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if marinerisUsesResponses(req) {
		return e.responses.Execute(ctx, auth, req, opts)
	}
	return e.NeoPiCompletionsExecutor.Execute(ctx, auth, req, opts)
}

func (e *MarinerisExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if marinerisUsesResponses(req) {
		return e.responses.ExecuteStream(ctx, auth, req, opts)
	}
	return e.NeoPiCompletionsExecutor.ExecuteStream(ctx, auth, req, opts)
}

// Refresh is a no-op: a gateway key never expires.
func (e *MarinerisExecutor) Refresh(_ context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	return auth, nil
}

// HttpRequest sends an arbitrary request with the account's gateway key.
func (e *MarinerisExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	return e.NeoPiCompletionsExecutor.HttpRequest(ctx, auth, req)
}

// newMarinerisResponses is NeoPi's openai-responses provider for the gateway's
// Codex-class rows; it shapes the request from the account's discovered record.
func newMarinerisResponses(provider string, cfg *config.Config) cliproxyauth.ProviderExecutor {
	return NewNeoPiResponsesExecutor(provider, cfg)
}
