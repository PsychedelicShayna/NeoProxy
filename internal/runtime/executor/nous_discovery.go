package executor

import (
	"context"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/nous"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// nousCredential is the bearer NeoPi's extension uses for an account: a
// pasted inference key, or the sign-in's access token.
func nousCredential(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if key := strings.TrimSpace(auth.Attributes["api_key"]); key != "" {
		return key
	}
	for _, field := range []string{"api_key", "access_token"} {
		if value, _ := auth.Metadata[field].(string); strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// neoPiOwner is the name the account's models are owned by: its
// openai-compatibility entry, else the provider.
func neoPiOwner(auth *cliproxyauth.Auth, provider string) string {
	if auth != nil {
		if name := strings.TrimSpace(auth.Attributes["compat_name"]); name != "" {
			return name
		}
	}
	return provider
}

// FetchNousModels is the extension's fetchDynamicModels: GET /v1/models with
// the account's credential, every row mapped and resolved as NeoPi does. An
// account with no credential serves NeoPi's bundled snapshot, as NeoPi does.
func FetchNousModels(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth) ([]*registry.ModelInfo, error) {
	token := nousCredential(auth)
	if token == "" {
		return registry.NousBundledModels(neoPiOwner(auth, nous.Provider)), nil
	}
	client := nous.NewClient(cfg, "")
	client.SetHTTPClient(helps.NewProxyAwareHTTPClient(ctx, cfg, auth, 0))
	rows, err := client.FetchModelRows(ctx, token)
	if err != nil {
		// The extension swallows a failed listing and serves its bundled
		// snapshot, replacing whatever an earlier listing gave.
		log.WithError(err).Warnf("nous-portal: model discovery failed for %s, serving NeoPi's bundled snapshot", auth.ID)
		return registry.NousBundledModels(neoPiOwner(auth, nous.Provider)), nil
	}
	return registry.NousModels(rows, neoPiOwner(auth, nous.Provider)), nil
}

// NousDiscoveryTimeout is the extension's REQUEST_TIMEOUT_MS.
const NousDiscoveryTimeout = 15 * time.Second
