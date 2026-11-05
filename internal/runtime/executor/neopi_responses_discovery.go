package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/openaiwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// FetchResponsesModels lists an account's models as NeoPi's provider model
// manager does: GET <base>/models with the account's key (Muse Code adds its
// x-api-version), mapped through the provider's seed rows and rules. The
// listing is authoritative for Muse Code, Sakana and Abliteration; Meta's
// Model API keeps its seed rows beside it.
func FetchResponsesModels(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth) ([]*registry.ModelInfo, error) {
	provider := neoPiResponsesProviderOf("", auth)
	if provider == "" {
		return nil, fmt.Errorf("responses models: %s is not a NeoPi Responses provider", authProviderName(auth))
	}
	baseURL, token := neoPiResponsesCredsOf("", auth)
	if token == "" {
		return nil, fmt.Errorf("%s models: credential has no key", provider)
	}
	if baseURL == "" {
		baseURL = registry.ResponsesProviderBaseURL(provider)
	}
	target := strings.TrimRight(baseURL, "/") + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	if provider == registry.ResponsesMuseCode {
		req.Header.Set("x-api-version", neoPiResponsesMuseVersion)
	}
	req.Header.Set("User-Agent", openaiwire.BunUserAgent)
	resp, err := helps.NewProxyAwareHTTPClient(ctx, cfg, auth, 0).Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s models: %w", provider, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s models: %s returned HTTP %d", provider, target, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, fmt.Errorf("%s models: read %s: %w", provider, target, err)
	}
	roster := registry.ParseResponsesRoster(body)
	if roster == nil {
		return nil, fmt.Errorf("%s models: %s did not return a model list", provider, target)
	}
	ownedBy, modelType := "meta", "meta"
	if !strings.EqualFold(strings.TrimSpace(authProviderName(auth)), "meta") {
		ownedBy, modelType = compatOwner(auth), "openai-compatibility"
	}
	return registry.ResponsesModels(provider, ownedBy, modelType, roster), nil
}

func authProviderName(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	return auth.Provider
}

// compatOwner is the openai-compatibility entry name a key belongs to.
func compatOwner(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if name := strings.TrimSpace(auth.Attributes["compat_name"]); name != "" {
		return name
	}
	return strings.TrimSpace(auth.Provider)
}

// ResponsesProviderOfAuth names the NeoPi Responses provider an account is
// served as ("meta", "muse-code", "sakana", "abliteration"), or "" for any other.
func ResponsesProviderOfAuth(auth *cliproxyauth.Auth) string {
	return neoPiResponsesProviderOf("", auth)
}
