// Package nous is a port of NeoPi's Nous Portal provider extension
// (extensions/nous-portal): the device-code sign-in, the refresh grant and
// model discovery, with the same endpoints, request shapes, timing and error
// messages. Parity is pinned by testdata/capture_neopi_nous_auth.ts, which
// runs the extension itself against a loopback server.
package nous

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

const (
	// Provider is the engine provider (auth-file "type") and catalogue id.
	Provider = "nous-portal"
	// PortalURL hosts the OAuth endpoints and the verification page.
	PortalURL = "https://portal.nousresearch.com"
	// InferenceURL is the OpenAI-compatible inference API.
	InferenceURL = "https://inference-api.nousresearch.com/v1"
	// ClientID and Scope are those of Nous's Hermes client.
	ClientID = "hermes-cli"
	Scope    = "inference:invoke"

	deviceGrant = "urn:ietf:params:oauth:grant-type:device_code"
	// requestTimeout is the extension's REQUEST_TIMEOUT_MS.
	requestTimeout = 15 * time.Second
	// userAgent is Bun's fetch default, which the extension's requests carry.
	userAgent = "Bun/1.3.14"
	// verificationHost is the only host a verification URL may point at.
	verificationHost = "portal.nousresearch.com"
	maxInterval      = 30
)

// DeviceCode is an issued device authorization.
type DeviceCode struct {
	DeviceCode      string
	UserCode        string
	VerificationURL string
	ExpiresIn       int
	Interval        int
}

// Token is an access token with its refresh token and absolute expiry.
type Token struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

// Client talks to the portal and inference API. The endpoints are fields so
// tests can point them at a fake upstream.
type Client struct {
	http         *http.Client
	PortalURL    string
	InferenceURL string
	// Sleep waits between device-code polls; tests replace it.
	Sleep func(ctx context.Context, d time.Duration) error
	// Now stamps token expiries; tests replace it.
	Now func() time.Time
}

// NewClient builds a proxy-aware client. proxyURL overrides cfg's proxy.
func NewClient(cfg *config.Config, proxyURL string) *Client {
	var sdk config.SDKConfig
	if cfg != nil {
		sdk = cfg.SDKConfig
		if strings.TrimSpace(proxyURL) == "" {
			proxyURL = cfg.ProxyURL
		}
	}
	sdk.ProxyURL = strings.TrimSpace(proxyURL)
	return NewClientWithHTTP(util.SetProxy(&sdk, &http.Client{}))
}

// NewClientWithHTTP builds a client over an existing transport.
func NewClientWithHTTP(client *http.Client) *Client {
	if client == nil {
		client = &http.Client{}
	}
	return &Client{http: client, PortalURL: PortalURL, InferenceURL: InferenceURL, Sleep: sleep, Now: time.Now}
}

// SetHTTPClient replaces the transport (executors pass their proxy-aware one).
func (c *Client) SetHTTPClient(client *http.Client) {
	if client != nil {
		c.http = client
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// formEncode is URLSearchParams.toString for ordered fields: JavaScript keeps
// insertion order, so url.Values (sorted) would change the bytes.
func formEncode(pairs ...string) string {
	var out strings.Builder
	for i := 0; i+1 < len(pairs); i += 2 {
		if i > 0 {
			out.WriteByte('&')
		}
		formEscape(&out, pairs[i])
		out.WriteByte('=')
		formEscape(&out, pairs[i+1])
	}
	return out.String()
}

func formEscape(out *strings.Builder, value string) {
	const hex = "0123456789ABCDEF"
	for i := 0; i < len(value); i++ {
		b := value[i]
		switch {
		case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9', b == '*', b == '-', b == '.', b == '_':
			out.WriteByte(b)
		case b == ' ':
			out.WriteByte('+')
		default:
			out.WriteByte('%')
			out.WriteByte(hex[b>>4])
			out.WriteByte(hex[b&15])
		}
	}
}

// do sends req with the extension's headers and its 15 s timeout, and returns
// the decoded body. An explicit Accept-Encoding disables Go's transparent
// decompression, so the body is decoded here.
func (c *Client) do(ctx context.Context, req *http.Request, header http.Header) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req = req.WithContext(ctx)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("User-Agent", userAgent)
	for key, values := range header {
		req.Header[key] = values
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	decoded, err := util.DecodeResponseBody(resp.Body, resp.Header.Get("Content-Encoding"))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	defer decoded.Close()
	raw, err := io.ReadAll(io.LimitReader(decoded, 32<<20))
	return resp.StatusCode, raw, err
}

func (c *Client) postForm(ctx context.Context, path, body string, header http.Header) (int, []byte, error) {
	req, err := http.NewRequest(http.MethodPost, c.PortalURL+path, strings.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.do(ctx, req, header)
}

// record is the extension's record(): a JSON object, else empty.
func record(raw []byte) map[string]any {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return map[string]any{}
	}
	if obj, ok := value.(map[string]any); ok {
		return obj
	}
	return map[string]any{}
}

// str is the extension's string(): a non-blank string, trimmed.
func str(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return strings.TrimSpace(s)
}

// positiveInteger is the extension's positiveInteger(): a positive finite
// number, or a numeric string, floored.
func positiveInteger(value any) (int, bool) {
	var n float64
	switch v := value.(type) {
	case float64:
		n = v
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return 0, false
		}
		n = parsed
	default:
		return 0, false
	}
	if math.IsNaN(n) || math.IsInf(n, 0) || n <= 0 {
		return 0, false
	}
	return int(math.Floor(n)), true
}

func positive(m map[string]any, key string, fallback int) int {
	if n, ok := positiveInteger(m[key]); ok {
		return n
	}
	return fallback
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// HTTPError is a portal or inference answer the extension turns into an
// error; Status is the HTTP status behind the message.
type HTTPError struct {
	Status  int
	Message string
}

func (e *HTTPError) Error() string { return e.Message }

// responseError is the extension's responseError(): "<what> (<status>)",
// then the error code and description when the body carries them.
func responseError(status int, body map[string]any, what string) error {
	msg := fmt.Sprintf("%s (%d)", what, status)
	if code := str(body, "error"); code != "" {
		msg += ": " + code
	}
	if desc := firstNonEmpty(str(body, "error_description"), str(body, "message")); desc != "" {
		msg += ": " + desc
	}
	return &HTTPError{Status: status, Message: msg}
}

// RequestDeviceCode starts a device authorization.
func (c *Client) RequestDeviceCode(ctx context.Context) (*DeviceCode, error) {
	status, raw, err := c.postForm(ctx, "/api/oauth/device/code", formEncode("client_id", ClientID, "scope", Scope), nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status > 299 {
		return nil, responseError(status, record(raw), "Device-code request failed")
	}
	body := record(raw)
	verification := firstNonEmpty(str(body, "verification_uri_complete"), str(body, "verification_uri"))
	parsed, errParse := url.Parse(verification)
	if verification == "" || errParse != nil || parsed.Scheme != "https" || parsed.Hostname() != verificationHost {
		return nil, errors.New("Nous Portal returned an unexpected verification URL")
	}
	dc := &DeviceCode{DeviceCode: str(body, "device_code"), UserCode: str(body, "user_code"), VerificationURL: parsed.String(),
		ExpiresIn: positive(body, "expires_in", 600), Interval: positive(body, "interval", 5)}
	if dc.DeviceCode == "" || dc.UserCode == "" {
		return nil, errors.New("Nous Portal returned an incomplete device code")
	}
	return dc, nil
}

// WaitForToken polls until the person approves, the code expires or ctx ends.
// progress, when set, is called before each poll like the extension's
// onProgress.
func (c *Client) WaitForToken(ctx context.Context, dc *DeviceCode, progress func(string)) (*Token, error) {
	deadline := c.Now().Add(time.Duration(dc.ExpiresIn) * time.Second)
	interval := min(dc.Interval, maxInterval)
	for c.Now().Before(deadline) {
		if progress != nil {
			progress("Waiting for Nous Portal authorization…")
		}
		status, raw, err := c.postForm(ctx, "/api/oauth/token",
			formEncode("grant_type", deviceGrant, "client_id", ClientID, "device_code", dc.DeviceCode), nil)
		if err != nil {
			return nil, err
		}
		if status >= 200 && status <= 299 {
			return c.tokenFrom(record(raw), "")
		}
		body := record(raw)
		switch str(body, "error") {
		case "authorization_pending":
		case "slow_down":
			interval = min(interval+1, maxInterval)
		default:
			return nil, responseError(status, body, "Device authorization failed")
		}
		if err = c.Sleep(ctx, time.Duration(interval)*time.Second); err != nil {
			return nil, err
		}
	}
	return nil, errors.New("Nous Portal device authorization expired")
}

// Refresh exchanges a refresh token; the portal reads it from a header.
func (c *Client) Refresh(ctx context.Context, refreshToken string) (*Token, error) {
	status, raw, err := c.postForm(ctx, "/api/oauth/token", formEncode("grant_type", "refresh_token", "client_id", ClientID),
		http.Header{"X-Nous-Refresh-Token": {refreshToken}})
	if err != nil {
		return nil, err
	}
	if status < 200 || status > 299 {
		return nil, responseError(status, record(raw), "Token refresh failed")
	}
	return c.tokenFrom(record(raw), refreshToken)
}

// tokenFrom is the extension's credentials(): a missing refresh token keeps
// the previous one, and the expiry is brought forward by min(120 s, ttl/10).
func (c *Client) tokenFrom(body map[string]any, previousRefresh string) (*Token, error) {
	access := str(body, "access_token")
	refresh := firstNonEmpty(str(body, "refresh_token"), previousRefresh)
	if access == "" {
		return nil, errors.New("Nous Portal token response omitted access_token")
	}
	if refresh == "" {
		return nil, errors.New("Nous Portal token response omitted refresh_token")
	}
	ttl := positive(body, "expires_in", 3600)
	skew := min(120, ttl/10)
	return &Token{AccessToken: access, RefreshToken: refresh, ExpiresAt: c.Now().Add(time.Duration(ttl-skew) * time.Second)}, nil
}

// FetchModelRows is the extension's fetchJson(): GET /models with the bearer
// credential, returning the raw catalog rows.
func (c *Client) FetchModelRows(ctx context.Context, credential string) ([]json.RawMessage, error) {
	req, err := http.NewRequest(http.MethodGet, c.InferenceURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+credential)
	status, raw, err := c.do(ctx, req, nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status > 299 {
		return nil, responseError(status, record(raw), "Model discovery failed")
	}
	var envelope struct {
		Data []json.RawMessage `json:"data"`
	}
	if err = json.Unmarshal(raw, &envelope); err != nil || envelope.Data == nil {
		return nil, errors.New("Nous Portal returned an invalid model catalog")
	}
	return envelope.Data, nil
}
