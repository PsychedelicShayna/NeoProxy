package executor

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/nous"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// NousExecutor serves a Nous Portal account the way NeoPi's Nous Portal
// extension does: the model NeoPi's catalog resolves for the account's own
// /v1/models listing, over NeoPi's openai-completions provider at
// inference-api.nousresearch.com, authenticated with the account's pasted
// inference key or its sign-in's access token. The sign-in's token refresh is
// the extension's refresh grant.
type NousExecutor struct {
	*NeoPiCompletionsExecutor
	cfg *config.Config
}

// NewNousExecutor builds the executor registered under provider: "nous-portal"
// for sign-ins, or the name of a key account's openai-compatibility entry.
func NewNousExecutor(provider string, cfg *config.Config) *NousExecutor {
	profile := NeoPiCompletionsProfile{
		Provider: nous.Provider,
		// Until discovery answers an account serves the extension's snapshot.
		Row:    func(_, model string) *registry.NeoPiModel { return registry.NousBundledNeoPiModel(model) },
		APIKey: nousCredential,
	}
	return &NousExecutor{NeoPiCompletionsExecutor: NewNeoPiCompletionsExecutor(provider, cfg, profile), cfg: cfg}
}

// Refresh runs the extension's refresh grant. A pasted key never expires, and
// an imported sign-in carries no refresh token (NeoPi stays its refresher), so
// both are returned untouched.
func (e *NousExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	if auth == nil {
		return nil, fmt.Errorf("nous executor: auth is nil")
	}
	refreshToken, _ := auth.Metadata["refresh_token"].(string)
	if strings.TrimSpace(refreshToken) == "" {
		return auth, nil
	}
	client := nous.NewClient(e.cfg, auth.ProxyURL)
	client.SetHTTPClient(helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0))
	token, err := client.Refresh(ctx, strings.TrimSpace(refreshToken))
	if err != nil {
		return nil, err
	}
	next := auth.Clone()
	if next.Metadata == nil {
		next.Metadata = map[string]any{}
	}
	next.Metadata["access_token"] = token.AccessToken
	next.Metadata["refresh_token"] = token.RefreshToken
	next.Metadata["expired"] = token.ExpiresAt.UTC().Format(time.RFC3339)
	next.Metadata["last_refresh"] = time.Now().UTC().Format(time.RFC3339)
	next.LastRefreshedAt = time.Now()
	return next, nil
}
