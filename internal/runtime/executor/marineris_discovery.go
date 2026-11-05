package executor

import (
	"context"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/marineris"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// MarinerisDiscoveryTimeout is the extension's REQUEST_TIMEOUT_MS.
const MarinerisDiscoveryTimeout = 10 * time.Second

// marinerisEndpoint is the gateway address and key of an account.
func marinerisEndpoint(auth *cliproxyauth.Auth) (baseURL, key string) {
	if auth == nil {
		return "", ""
	}
	baseURL = strings.TrimSpace(auth.Attributes["base_url"])
	key = strings.TrimSpace(auth.Attributes["api_key"])
	if baseURL == "" {
		baseURL, _ = auth.Metadata["base_url"].(string)
	}
	if key == "" {
		key, _ = auth.Metadata["api_key"].(string)
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = marineris.DefaultBaseURL
	}
	return marineris.NormalizeBaseURL(baseURL), strings.TrimSpace(key)
}

// FetchMarinerisModels is the extension's fetchDynamicModels: the gateway's
// /v1/models listing for the account's key, each row mapped by the extension's
// mapModel and resolved as NeoPi's catalog build does. NeoPi lists nothing
// for an account with no key.
func FetchMarinerisModels(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth) ([]*registry.ModelInfo, error) {
	baseURL, key := marinerisEndpoint(auth)
	if key == "" {
		return nil, nil
	}
	rows, err := marineris.ModelRows(ctx, helps.NewProxyAwareHTTPClient(ctx, cfg, auth, 0), baseURL, key)
	if err != nil {
		return nil, err
	}
	return registry.MarinerisModels(rows, baseURL, neoPiOwner(auth, marineris.Provider)), nil
}
