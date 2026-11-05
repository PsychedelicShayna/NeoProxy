// Package openrouter is NeoPi's OpenRouter sign-in (auth rule openrouter):
// a PKCE authorization-code flow against openrouter.ai/auth with no client
// registration and no state echo, whose exchange yields a durable `sk-or-` key,
// plus the pasted-key path validated against /api/v1/auth/key. OpenRouter keys
// never expire, so nothing is refreshed.
package openrouter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/tidwall/gjson"
)

const (
	// BaseURL is OpenRouter's API root.
	BaseURL = "https://openrouter.ai/api/v1"
	// AuthorizeURL is OpenRouter's PKCE authorize endpoint.
	AuthorizeURL = "https://openrouter.ai/auth"
	// KeysURL exchanges an authorization code for a durable key.
	KeysURL = BaseURL + "/auth/keys"
	// KeyURL authenticates a key; NeoPi validates a pasted key against it.
	KeyURL = BaseURL + "/auth/key"
	// RedirectURI is NeoPi's loopback callback. The person pastes the address
	// their browser lands on back into NeoProxy.
	RedirectURI = "http://localhost:54549/callback"
	// KeyPrefix marks a pasted OpenRouter key.
	KeyPrefix = "sk-or-"
	// authProvider names the auth rule in NeoPi's exchange errors.
	authProvider = "openrouter"
	// authLabel names the provider in NeoPi's API key validation errors.
	authLabel = "OpenRouter"
	// userAgent is Bun's fetch default, which NeoPi's sign-in requests carry.
	userAgent      = "Bun/1.3.14"
	requestTimeout = 30 * time.Second
)

// IsOpenRouterKey reports whether a pasted secret is an OpenRouter key rather
// than an authorization code.
func IsOpenRouterKey(value string) bool {
	return strings.HasPrefix(strings.TrimSpace(value), KeyPrefix)
}

// Service performs the sign-in exchanges. Endpoints are fields so tests can
// point them at a fake upstream.
type Service struct {
	client       *http.Client
	AuthorizeURL string
	KeysURL      string
	KeyURL       string
}

// NewService returns a Service using client (http.DefaultClient when nil).
// Every request is bounded by NeoPi's 30 s sign-in timeout.
func NewService(client *http.Client) *Service {
	if client == nil {
		client = http.DefaultClient
	}
	return &Service{client: client, AuthorizeURL: AuthorizeURL, KeysURL: KeysURL, KeyURL: KeyURL}
}

// AuthorizationURL is generateAuthUrl for a rule with standard=false: only the
// declared authorize params, in declaration order.
func (s *Service) AuthorizationURL(challenge string) string {
	query := "callback_url=" + url.QueryEscape(RedirectURI) +
		"&code_challenge=" + url.QueryEscape(challenge) +
		"&code_challenge_method=S256"
	return s.AuthorizeURL + "?" + query
}

// ExchangeCode is exchangeToken's code grant: a JSON body of the authorize
// params minus the redirect, from which credential.access "key" reads the
// durable key.
func (s *Service) ExchangeCode(ctx context.Context, code, verifier string) (string, error) {
	body := jsjson.Object(
		jsjson.Field{Name: "code", Value: jsjson.Marshal(code)},
		jsjson.Field{Name: "code_verifier", Value: jsjson.Marshal(verifier)},
		jsjson.Field{Name: "code_challenge_method", Value: jsjson.Marshal("S256")},
	)
	status, raw, err := s.send(ctx, http.MethodPost, s.KeysURL, "", body)
	if err != nil {
		return "", fmt.Errorf("%s request to %s failed: %w", authProvider, s.KeysURL, err)
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("%s token exchange failed: %d %s", authProvider, status, prefix(string(raw), 500))
	}
	key := strings.TrimSpace(gjson.GetBytes(raw, "key").String())
	if key == "" {
		return "", fmt.Errorf("%s token response missing access token: %s", authProvider, prefix(string(raw), 500))
	}
	return key, nil
}

// ValidationError is a key check the server answered with a non-200 status.
type ValidationError struct {
	Status int
	msg    string
}

func (e *ValidationError) Error() string { return e.msg }

// ValidateKey is paste-key validate-url: GET /auth/key authenticates the key.
// /models answers 200 for any bearer, so only this endpoint proves one.
func (s *Service) ValidateKey(ctx context.Context, key string) error {
	status, raw, err := s.send(ctx, http.MethodGet, s.KeyURL, key, nil)
	if err != nil {
		return fmt.Errorf("%s key validation failed: %w", authLabel, err)
	}
	if status == http.StatusOK {
		return nil
	}
	if text := strings.TrimSpace(string(raw)); text != "" {
		return &ValidationError{Status: status, msg: fmt.Sprintf("%s API key validation failed (%d): %s", authLabel, status, prefix(text, 500))}
	}
	return &ValidationError{Status: status, msg: fmt.Sprintf("%s API key validation failed (%d)", authLabel, status)}
}

// send performs one sign-in request with NeoPi's headers and timeout.
func (s *Service) send(ctx context.Context, method, target, bearer string, body []byte) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	var reader io.Reader
	if body != nil {
		reader = strings.NewReader(string(body))
	}
	request, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Accept", "*/*")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("User-Agent", userAgent)
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	response, err := s.client.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer func() {
		_ = response.Body.Close()
	}()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return response.StatusCode, nil, err
	}
	return response.StatusCode, raw, nil
}

func prefix(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

// ErrNoVerifier reports a sign-in session with no pending PKCE verifier.
var ErrNoVerifier = errors.New("openrouter: sign-in session has no pending PKCE verifier")
