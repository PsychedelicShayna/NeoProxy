package neopool

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/openaiwire"
)

// validateResponsesKey is NeoPi's `validate "models-endpoint"` for Meta, Sakana
// and Abliteration (validateApiKeyAgainstModelsEndpoint): GET <base>/models with
// the bearer, fetch's default Accept and Bun's User-Agent. Any 2xx accepts the
// key; the roster is not read, and an empty one is not a failure. The account
// then serves NeoPi's catalog for the provider, so no model list is kept.
func validateResponsesKey(ctx context.Context, base, key string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/models", nil)
	if err != nil {
		return accessErr(http.StatusBadRequest, "The base URL is not a valid address.")
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", openaiwire.BunUserAgent)
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := accessHTTPClient.Do(req)
	if err != nil {
		return accessErr(http.StatusBadGateway, "Could not reach %s to check the key. Try again in a moment.", req.URL.Host)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return &AccessError{Status: http.StatusBadRequest, keyRejected: true, Message: fmt.Sprintf("The provider rejected this key (HTTP %d). Check that the whole key was pasted and that it is still active.", resp.StatusCode)}
	case resp.StatusCode == http.StatusTooManyRequests:
		return accessErr(http.StatusServiceUnavailable, "%s is rate-limiting requests right now (HTTP 429); the key itself was not rejected. Wait a minute and try again.", req.URL.Host)
	default:
		return accessErr(http.StatusBadGateway, "The provider answered HTTP %d when checking the key. Try again in a moment.", resp.StatusCode)
	}
}
