package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// FetchOpenRouterModels lists an OpenRouter account's chat roster the way
// NeoPi's openrouter model manager does (fetchOpenAICompatibleModels over
// GET /models, then resolveProviderModels' merge with the bundle): rows that
// advertise tool support, merged over the bundled row of the same id. The
// roster is public, but NeoPi still sends the account's bearer.
func FetchOpenRouterModels(ctx context.Context, cfg *config.Config, a *cliproxyauth.Auth) ([]*registry.ModelInfo, error) {
	base := registry.OpenRouterDefaultBaseURL
	key := ""
	if a != nil {
		if configured := strings.TrimSpace(a.Attributes["base_url"]); configured != "" {
			base = strings.TrimRight(configured, "/")
		}
		key = openRouterAPIKey(a)
	}
	// A configured base URL may carry credentials, and discovery failures are
	// logged, so errors never quote it.
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return nil, errors.New("openrouter: GET /models: invalid base URL")
	}
	request.Header.Set("Accept", "application/json")
	if key != "" {
		request.Header.Set("Authorization", "Bearer "+key)
	}
	client := helps.NewProxyAwareHTTPClient(ctx, cfg, a, 0)
	response, err := client.Do(request)
	if err != nil {
		var transport *url.Error
		if errors.As(err, &transport) {
			err = transport.Err
		}
		return nil, fmt.Errorf("openrouter: GET /models failed: %w", err)
	}
	defer func() {
		_ = response.Body.Close()
	}()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("openrouter: GET /models returned %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if !json.Valid(body) {
		return nil, fmt.Errorf("openrouter: GET /models returned invalid JSON")
	}
	return registry.OpenRouterDiscovered(body, a.Label)
}
