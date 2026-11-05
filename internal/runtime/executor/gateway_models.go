package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/openaiwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// gatewayInstallID is NeoPi's stable install identity, which every OpenCode
// request carries as x-opencode-session outside a conversation. NeoPi keeps
// one id per install; the proxy derives one per account, so two accounts of
// the same gateway never share a cache affinity handle.
func gatewayInstallID(provider string, auth *cliproxyauth.Auth) string {
	identity := "gateway\x00" + provider
	if auth != nil {
		identity += "\x00" + auth.ID
	}
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("cli-proxy-api:gateway:install\x00"+identity)).String()
}

// gatewayModelListURL is the endpoint NeoPi lists a gateway's models at.
func gatewayModelListURL(gateway string) string {
	return strings.TrimRight(registry.GatewayDiscoveryBaseURL(gateway), "/") + "/models"
}

// FetchGatewayModels lists a gateway account's models the way NeoPi's model
// manager discovers them: GET <base>/models with the account's bearer, NeoPi's
// user agent, and OpenCode's install-id session header.
func FetchGatewayModels(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, gateway string) ([]*registry.ModelInfo, error) {
	key := gatewayAPIKey(auth)
	if key == "" {
		return nil, fmt.Errorf("%s: account has no API key", gateway)
	}
	target := gatewayModelListURL(gateway)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+key)
	// Only OpenCode's discovery carries NeoPi's identity; Vercel's keeps the
	// runtime default.
	if session := gatewaySessionHeader(gateway); session != "" {
		request.Header.Set("User-Agent", openaiwire.NeoPiUserAgent)
		request.Header.Set(session, gatewayInstallID(gateway, auth))
	}
	response, err := helps.NewProxyAwareHTTPClient(ctx, cfg, auth, 0).Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return nil, fmt.Errorf("%s: model list answered HTTP %d", gateway, response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	models := registry.GatewayDiscoveredModels(gateway, body, gateway)
	if models == nil {
		return nil, fmt.Errorf("%s: model list was not a model list", gateway)
	}
	return models, nil
}
