// Package marineris is a port of NeoPi's Marineris provider extension
// (extensions/marineris): the gateway address normalization, the key check and
// the /v1/models listing, with the same request shape and error messages.
// Parity is pinned by testdata/capture_neopi_marineris_auth.ts, which runs the
// extension itself against a loopback server.
package marineris

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

const (
	// Provider is the catalogue id and the engine provider.
	Provider = "marineris"
	// DefaultBaseURL is the extension's DEFAULT_BASE_URL.
	DefaultBaseURL = "http://192.168.50.50:8801/v1"
	// requestTimeout is the extension's REQUEST_TIMEOUT_MS.
	requestTimeout = 10 * time.Second
	// userAgent is Bun's fetch default, which the extension's requests carry.
	userAgent = "Bun/1.3.14"
)

var (
	trailingSlashes = regexp.MustCompile(`/+$`)
	hasScheme       = regexp.MustCompile(`^https?://`)
)

// HTTPError is the gateway's non-2xx answer to the model listing.
type HTTPError struct{ Status int }

func (e *HTTPError) Error() string {
	return fmt.Sprintf("Marineris /v1/models returned HTTP %d", e.Status)
}

// NormalizeBaseURL is the extension's normalizeBaseUrl: an http(s) address
// that ends in /v1, defaulting the scheme to http.
func NormalizeBaseURL(value string) string {
	trimmed := trailingSlashes.ReplaceAllString(strings.TrimSpace(value), "")
	if !hasScheme.MatchString(trimmed) {
		trimmed = "http://" + trimmed
	}
	if strings.HasSuffix(trimmed, "/v1") {
		return trimmed
	}
	return trimmed + "/v1"
}

// ModelRows is fetchModels' request: GET <baseURL>/models with the gateway
// key, returning the raw catalog rows.
func ModelRows(ctx context.Context, client *http.Client, baseURL, key string) ([]json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("User-Agent", userAgent)
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &HTTPError{Status: resp.StatusCode}
	}
	decoded, err := util.DecodeResponseBody(resp.Body, resp.Header.Get("Content-Encoding"))
	if err != nil {
		return nil, err
	}
	defer decoded.Close()
	raw, err := io.ReadAll(io.LimitReader(decoded, 32<<20))
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Data []json.RawMessage `json:"data"`
	}
	if err = json.Unmarshal(raw, &envelope); err != nil || envelope.Data == nil {
		return nil, errors.New("Marineris returned an invalid model catalog")
	}
	return envelope.Data, nil
}

// Validate is the key check of the extension's login: the address is
// normalized, the key trimmed and non-empty, and the gateway must list at
// least one model for it. It returns the normalized address and the models.
func Validate(ctx context.Context, client *http.Client, baseURL, key string) (string, []*registry.ModelInfo, error) {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultBaseURL
	}
	baseURL = NormalizeBaseURL(baseURL)
	key = strings.TrimSpace(key)
	if key == "" {
		return baseURL, nil, errors.New("No gateway key entered")
	}
	rows, err := ModelRows(ctx, client, baseURL, key)
	if err != nil {
		return baseURL, nil, err
	}
	models := registry.MarinerisModels(rows, baseURL, Provider)
	if len(models) == 0 {
		return baseURL, nil, errors.New("Key accepted but no models are visible to it; check its gates")
	}
	return baseURL, models, nil
}
