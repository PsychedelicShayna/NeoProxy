package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/antigravitywire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// antigravityFetchAvailableModelsPath is FETCH_AVAILABLE_MODELS_PATH.
const antigravityFetchAvailableModelsPath = "/v1internal:fetchAvailableModels"

// AntigravityRejectedError reports a definitive credential rejection: the
// account's roster could not be read because upstream answered 401 or 403, so
// the caller must skip this account instead of keeping a stale catalog.
type AntigravityRejectedError struct {
	// Status is the rejected status (401 or 403).
	Status int
}

func (e *AntigravityRejectedError) Error() string {
	return fmt.Sprintf("antigravity models: credential rejected with HTTP %d", e.Status)
}

// FetchAntigravityModels is NeoPi's fetchAntigravityDiscoveryModels over
// NeoProxy's credential store: POST {endpoint}/v1internal:fetchAvailableModels
// with the account's Cloud Code Assist project, falling back from the daily
// host to the sandbox host, then NeoPi's normalization and collapse.
//
// A transient failure is returned as an error so the caller keeps its previous
// catalog; a 401/403 is an AntigravityRejectedError.
func FetchAntigravityModels(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth) ([]*registry.ModelInfo, error) {
	credential := antigravityCredentialOf(auth)
	token := credential.accessToken
	if token == "" {
		return nil, &AntigravityRejectedError{Status: http.StatusUnauthorized}
	}
	endpoints := antigravityDiscoveryEndpoints(auth)
	client := helps.NewProxyAwareHTTPClient(ctx, cfg, auth, 0)
	// fetchAntigravityDiscoveryResponse resolves the latest client version
	// before the roster request whenever no user agent is overridden: the
	// backend gates newer models on it.
	antigravitywire.EnsureVersion(ctx, client)
	projectID := credential.projectID

	rejectedStatus := 0
	transientFailure := false
	for _, endpoint := range endpoints {
		payload, errFetch := antigravityFetchRoster(ctx, client, endpoint, token, projectID)
		if errFetch != nil {
			var rejected *AntigravityRejectedError
			if errors.As(errFetch, &rejected) {
				rejectedStatus = rejected.Status
				continue
			}
			transientFailure = true
			continue
		}
		models, errDecode := registry.AntigravityDiscoveredModels(payload, endpoint)
		if errDecode != nil {
			transientFailure = true
			continue
		}
		return models, nil
	}
	if rejectedStatus != 0 && !transientFailure {
		return nil, &AntigravityRejectedError{Status: rejectedStatus}
	}
	if transientFailure {
		return nil, fmt.Errorf("antigravity models: fetchAvailableModels failed for every endpoint")
	}
	return nil, fmt.Errorf("antigravity models: no discovery endpoint configured")
}

// antigravityFetchRoster is one endpoint's fetchAvailableModels request.
func antigravityFetchRoster(ctx context.Context, client *http.Client, endpoint, token, projectID string) ([]byte, error) {
	body := []byte("{}")
	if projectID != "" {
		encoded, err := json.Marshal(map[string]string{"project": projectID})
		if err != nil {
			return nil, fmt.Errorf("antigravity models: encode request: %w", err)
		}
		body = encoded
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+antigravityFetchAvailableModelsPath, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("antigravity models: build request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	antigravitywire.SetClientHeaders(request.Header, "")
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("antigravity models: %w", err)
	}
	defer func() {
		if errClose := response.Body.Close(); errClose != nil {
			_ = errClose
		}
	}()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<16))
		return nil, &AntigravityRejectedError{Status: response.StatusCode}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("antigravity models: fetchAvailableModels returned HTTP %d", response.StatusCode)
	}
	// The client pins Accept-Encoding: gzip, so Go never decompresses.
	decoded, err := util.DecodeResponseBody(response.Body, response.Header.Get("Content-Encoding"))
	if err != nil {
		return nil, fmt.Errorf("antigravity models: decode response: %w", err)
	}
	defer func() {
		if errClose := decoded.Close(); errClose != nil {
			_ = errClose
		}
	}()
	payload, err := io.ReadAll(decoded)
	if err != nil {
		return nil, fmt.Errorf("antigravity models: read response: %w", err)
	}
	return payload, nil
}

// antigravityDiscoveryEndpoints is the endpoint list discovery walks: a base
// URL on another host replaces it, else the daily host then the sandbox host
// (the same rule the request ladder applies).
func antigravityDiscoveryEndpoints(auth *cliproxyauth.Auth) []string {
	override := strings.TrimRight(strings.TrimSpace(antigravityCredentialOf(auth).baseURL), "/")
	if override != "" && override != antigravitywire.DailyEndpoint && override != antigravitywire.SandboxEndpoint {
		return []string{override}
	}
	return []string{antigravitywire.DailyEndpoint, antigravitywire.SandboxEndpoint}
}
