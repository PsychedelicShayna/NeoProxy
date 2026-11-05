// Package meta provides authentication and token management for Meta Muse (api.meta.ai).
// It ports NeoPi's `muse-code` sign-in: the RFC 8628 device authorization grant
// on a Meta account, then the Model API key the subscription mints.
package meta

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	log "github.com/sirupsen/logrus"
)

const (
	// DefaultAPIBaseURL is the default official Meta API base URL.
	DefaultAPIBaseURL = "https://api.meta.ai/v1"
	// AuthHost is the Meta account OAuth authorization server.
	AuthHost = "https://auth.meta.com"
	// DeviceAuthorizationEndpoint is the device authorization request endpoint.
	DeviceAuthorizationEndpoint = AuthHost + "/oidc/device/authorization/"
	// TokenEndpoint is the token issuance and polling endpoint.
	TokenEndpoint = AuthHost + "/oidc/device/token/"
	// MuseKeyURL is where the device token is exchanged for the Model API key.
	MuseKeyURL = "https://api.meta.ai/muse-code/key"
	// ClientID is Meta Muse CLI's official OAuth client ID.
	ClientID = "1031625952748946"
	// DeviceCodeGrantType is the RFC 8628 grant type parameter.
	DeviceCodeGrantType = "urn:ietf:params:oauth:grant-type:device_code"
	// APIVersion is the x-api-version header every muse-code request carries.
	APIVersion = "1.0.0"
	// UserAgent is the User-Agent Bun's fetch sends for NeoPi's requests.
	UserAgent = "Bun/1.3.14"
	// DefaultPollInterval is the default polling interval when unspecified.
	DefaultPollInterval = 5 * time.Second
	// MinPollInterval is MINIMUM_DEVICE_FLOW_INTERVAL_MS.
	MinPollInterval = time.Second
	// SlowDownIncrement is SLOW_DOWN_INTERVAL_INCREMENT_MS.
	SlowDownIncrement = 5 * time.Second
	// MaxPollDuration is the upper bound shown for waiting on user authorization.
	MaxPollDuration = 15 * time.Minute
	// KeyRequestTimeout is REQUEST_TIMEOUT_MS of the key exchange.
	KeyRequestTimeout = 20 * time.Second
	// httpClientTimeout bounds credential-acquisition HTTP calls (DEFAULT_REQUEST_TIMEOUT_MS).
	httpClientTimeout = 30 * time.Second
)

// DeviceCodeResponse represents Meta's device authorization response.
type DeviceCodeResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
	TokenEndpoint           string `json:"-"`
}

// TokenData represents the token response from Meta's OAuth token endpoint.
type TokenData struct {
	AccessToken      string `json:"access_token"`
	TokenType        string `json:"token_type"`
	Error            string `json:"error,omitempty"`
	ErrorDescription string `json:"error_description,omitempty"`
}

// UsageWindow is one subscription usage window of a key response.
type UsageWindow struct {
	UsedPercent        *float64        `json:"used_percent"`
	ResetsAt           json.RawMessage `json:"resets_at"`
	WindowDurationMins *float64        `json:"window_duration_mins"`
}

// SubscriptionUsage is a key response's `subs_usage`.
type SubscriptionUsage struct {
	Window *UsageWindow `json:"window"`
	Weekly *UsageWindow `json:"weekly"`
}

// MintedKeyResponse represents the response of https://api.meta.ai/muse-code/key
// (museCodeKeyResponseSchema).
type MintedKeyResponse struct {
	APIKey                  string
	RequirePaymentActionURL string
	RequirePayment          bool
	ActionURL               string
	UserEmail               string
	UserID                  string
	// IsSubsActive is is_subs_active; SubsActiveKnown records its presence.
	IsSubsActive    bool
	SubsActiveKnown bool
	SubsTierID      string
	SubsTierName    string
	SubsUsage       *SubscriptionUsage
}

// UnmarshalJSON reads the schema's fields, keeping `is_subs_active` tri-state.
func (m *MintedKeyResponse) UnmarshalJSON(data []byte) error {
	var raw struct {
		APIKey                  string             `json:"api_key"`
		RequirePaymentActionURL string             `json:"require_payment_action_url"`
		RequirePayment          bool               `json:"require_payment"`
		ActionURL               *string            `json:"action_url"`
		UserEmail               string             `json:"user_email"`
		UserID                  string             `json:"user_id"`
		IsSubsActive            *bool              `json:"is_subs_active"`
		SubsTierID              *string            `json:"subs_tier_id"`
		SubsTierName            *string            `json:"subs_tier_name"`
		SubsUsage               *SubscriptionUsage `json:"subs_usage"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*m = MintedKeyResponse{
		APIKey: raw.APIKey, RequirePaymentActionURL: raw.RequirePaymentActionURL, RequirePayment: raw.RequirePayment,
		UserEmail: raw.UserEmail, UserID: raw.UserID, SubsUsage: raw.SubsUsage,
	}
	if raw.ActionURL != nil {
		m.ActionURL = *raw.ActionURL
	}
	if raw.IsSubsActive != nil {
		m.IsSubsActive, m.SubsActiveKnown = *raw.IsSubsActive, true
	}
	if raw.SubsTierID != nil {
		m.SubsTierID = *raw.SubsTierID
	}
	if raw.SubsTierName != nil {
		m.SubsTierName = *raw.SubsTierName
	}
	return nil
}

// NormalizedEmail is the account email NeoPi stores: trimmed and lowercased.
func (m *MintedKeyResponse) NormalizedEmail() string {
	return strings.ToLower(strings.TrimSpace(m.UserEmail))
}

// AccountID is NeoPi's stable account identity: the user id, else the email.
func (m *MintedKeyResponse) AccountID() string {
	if id := strings.TrimSpace(m.UserID); id != "" {
		return id
	}
	return m.NormalizedEmail()
}

// Tier is the subscription tier label: its name, else its id.
func (m *MintedKeyResponse) Tier() string {
	if name := strings.TrimSpace(m.SubsTierName); name != "" {
		return name
	}
	return strings.TrimSpace(m.SubsTierID)
}

// KeyError is an OAuthError of the muse-code key exchange; Status is the HTTP
// status when the failure was one.
type KeyError struct {
	Status  int
	Message string
}

func (e *KeyError) Error() string { return e.Message }

// MetaAuthBundle packages the token data, minted key, and user metadata.
type MetaAuthBundle struct {
	TokenData *TokenData
	MintedKey *MintedKeyResponse
	Email     string
}

// MetaTokenStorage represents persisted Meta OAuth tokens in auth files.
type MetaTokenStorage struct {
	Type         string `json:"type"`
	AuthKind     string `json:"auth_kind"`
	AccessToken  string `json:"access_token"`
	DCAToken     string `json:"dca_token,omitempty"`
	APIKey       string `json:"api_key,omitempty"`
	AccountID    string `json:"account_id,omitempty"`
	TokenType    string `json:"token_type,omitempty"`
	ExpiresIn    int    `json:"expires_in,omitempty"`
	Expired      string `json:"expired,omitempty"`
	DCAExpired   string `json:"dca_expired,omitempty"`
	DCAExpiresAt int64  `json:"dca_expires_at,omitempty"`
	LastRefresh  string `json:"last_refresh,omitempty"`
	BaseURL      string `json:"base_url,omitempty"`
	Email        string `json:"email,omitempty"`

	Metadata map[string]any `json:"-"`
}

// SetMetadata allows the token store to merge status fields before saving.
func (ts *MetaTokenStorage) SetMetadata(meta map[string]any) {
	ts.Metadata = meta
}

// metaCredentialFields identifies credential and lifecycle fields managed by MetaTokenStorage.
// These fields must never be restored from disk or metadata when omitted from storage.
var metaCredentialFields = map[string]struct{}{
	"type":           {},
	"auth_kind":      {},
	"access_token":   {},
	"token_type":     {},
	"dca_token":      {},
	"api_key":        {},
	"expires_in":     {},
	"expired":        {},
	"dca_expired":    {},
	"dca_expires_at": {},
	"last_refresh":   {},
}

// SaveTokenToFile writes Meta credentials to a JSON auth file.
func (ts *MetaTokenStorage) SaveTokenToFile(authFilePath string) error {
	ts.Type = "meta"
	ts.AuthKind = "oauth"
	if errMkdirAll := os.MkdirAll(filepath.Dir(authFilePath), 0o700); errMkdirAll != nil {
		return fmt.Errorf("meta token storage: create directory: %w", errMkdirAll)
	}

	data := map[string]any{
		"type":         "meta",
		"auth_kind":    "oauth",
		"access_token": ts.AccessToken,
	}
	if ts.DCAToken != "" {
		data["dca_token"] = ts.DCAToken
	}
	if ts.APIKey != "" {
		data["api_key"] = ts.APIKey
	}
	if ts.AccountID != "" {
		data["account_id"] = ts.AccountID
	}
	if ts.TokenType != "" {
		data["token_type"] = ts.TokenType
	}
	if ts.ExpiresIn > 0 {
		data["expires_in"] = ts.ExpiresIn
	}
	if ts.Expired != "" {
		data["expired"] = ts.Expired
	}
	if ts.DCAExpired != "" {
		data["dca_expired"] = ts.DCAExpired
	}
	if ts.DCAExpiresAt > 0 {
		data["dca_expires_at"] = ts.DCAExpiresAt
	}
	if ts.LastRefresh != "" {
		data["last_refresh"] = ts.LastRefresh
	}
	if ts.BaseURL != "" {
		data["base_url"] = ts.BaseURL
	}
	if ts.Email != "" {
		data["email"] = ts.Email
	}
	// Managed saves supply the current metadata, including deliberate deletions.
	// Only login callers without a metadata snapshot inherit settings from disk.
	metadata := ts.Metadata
	if metadata == nil {
		if raw, errRead := os.ReadFile(authFilePath); errRead == nil {
			_ = json.Unmarshal(raw, &metadata)
		}
	}
	for k, v := range metadata {
		if _, isCred := metaCredentialFields[k]; isCred {
			continue
		}
		// Supplied settings take precedence over storage's older snapshot.
		if _, exists := data[k]; !exists || ts.Metadata != nil {
			data[k] = v
		}
	}

	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("meta token storage: encode token file: %w", err)
	}
	file, err := os.CreateTemp(filepath.Dir(authFilePath), ".meta-token-*")
	if err != nil {
		return fmt.Errorf("meta token storage: create token file: %w", err)
	}
	defer func() { _ = os.Remove(file.Name()) }()
	if _, errWrite := file.Write(append(raw, '\n')); errWrite != nil {
		_ = file.Close()
		return fmt.Errorf("meta token storage: write token file: %w", errWrite)
	}
	if errClose := file.Close(); errClose != nil {
		return fmt.Errorf("meta token storage: close token file: %w", errClose)
	}
	if errRename := os.Rename(file.Name(), authFilePath); errRename != nil {
		return fmt.Errorf("meta token storage: replace token file: %w", errRename)
	}

	return nil
}

// MetaAuth manages Meta OAuth operations.
type MetaAuth struct {
	cfg        *config.Config
	httpClient *http.Client
	proxyURL   string
	mintURL    string
}

// SetMintURL overrides the API key minting endpoint (primarily for unit tests).
func (a *MetaAuth) SetMintURL(u string) {
	a.mintURL = strings.TrimSpace(u)
}

// NewMetaAuth creates a new MetaAuth service instance.
func NewMetaAuth(cfg *config.Config) *MetaAuth {
	return NewMetaAuthWithProxyURL(cfg, "")
}

// NewMetaAuthWithProxyURL creates a MetaAuth service instance with an optional proxy URL.
func NewMetaAuthWithProxyURL(cfg *config.Config, proxyURL string) *MetaAuth {
	effectiveProxyURL := strings.TrimSpace(proxyURL)
	var sdkCfg config.SDKConfig
	if cfg != nil {
		sdkCfg = cfg.SDKConfig
		if effectiveProxyURL == "" {
			effectiveProxyURL = strings.TrimSpace(cfg.ProxyURL)
		}
	}
	sdkCfg.ProxyURL = effectiveProxyURL
	return &MetaAuth{
		cfg:        cfg,
		httpClient: util.SetProxy(&sdkCfg, &http.Client{Timeout: httpClientTimeout}),
		proxyURL:   proxyURL,
	}
}

// StartDeviceFlow initiates the device authorization flow with Meta.
func (a *MetaAuth) StartDeviceFlow(ctx context.Context) (*DeviceCodeResponse, error) {
	return a.StartDeviceFlowWithEndpoint(ctx, DeviceAuthorizationEndpoint)
}

// setNeoPiHeaders are the headers NeoPi's muse-code rule declares on every
// request, plus the fetch implementation's own User-Agent.
func setNeoPiHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-api-version", APIVersion)
	req.Header.Set("User-Agent", UserAgent)
}

// StartDeviceFlowWithEndpoint initiates the device authorization flow against a specific endpoint.
func (a *MetaAuth) StartDeviceFlowWithEndpoint(ctx context.Context, endpoint string) (*DeviceCodeResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		endpoint = DeviceAuthorizationEndpoint
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(orderedForm([2]string{"client_id", ClientID})))
	if err != nil {
		return nil, fmt.Errorf("muse-code device authorization request: %w", err)
	}
	setNeoPiHeaders(req)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("muse-code request to %s failed: %w", endpoint, err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("meta device flow: close response body error: %v", errClose)
		}
	}()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("meta device flow: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("muse-code device authorization failed: %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var dcr DeviceCodeResponse
	if err := json.Unmarshal(body, &dcr); err != nil {
		return nil, fmt.Errorf("muse-code device authorization response missing required fields")
	}
	if dcr.UserCode == "" || dcr.DeviceCode == "" || dcr.VerificationURI == "" {
		return nil, fmt.Errorf("muse-code device authorization response missing required fields")
	}
	dcr.TokenEndpoint = TokenEndpoint
	return &dcr, nil
}

// pollDeviceToken is one device-token request: complete (with the token
// body), pending, slow_down, or failed with NeoPi's message.
func (a *MetaAuth) pollDeviceToken(ctx context.Context, tokenEndpoint, deviceCode string) (token *TokenData, slowDown bool, err error) {
	form := orderedForm([2]string{"grant_type", DeviceCodeGrantType}, [2]string{"client_id", ClientID}, [2]string{"device_code", deviceCode})
	req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(form))
	if errReq != nil {
		return nil, false, fmt.Errorf("muse-code request to %s failed: %w", tokenEndpoint, errReq)
	}
	setNeoPiHeaders(req)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, errDo := a.httpClient.Do(req)
	if errDo != nil {
		if ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
		return nil, false, fmt.Errorf("muse-code request to %s failed: %w", tokenEndpoint, errDo)
	}
	defer func() { _ = resp.Body.Close() }()
	body, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return nil, false, fmt.Errorf("muse-code request to %s failed: %w", tokenEndpoint, errRead)
	}
	var decoded TokenData
	_ = json.Unmarshal(body, &decoded)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 && decoded.Error == "" {
		if decoded.AccessToken == "" {
			return nil, false, fmt.Errorf("muse-code token response missing access token")
		}
		return &decoded, false, nil
	}
	switch decoded.Error {
	case "authorization_pending":
		return nil, false, nil
	case "slow_down":
		return nil, true, nil
	case "expired_token":
		return nil, false, fmt.Errorf("muse-code device code expired; restart the login")
	case "access_denied":
		return nil, false, fmt.Errorf("muse-code device authorization was denied")
	}
	detail := decoded.ErrorDescription
	if detail == "" {
		detail = decoded.Error
	}
	return nil, false, fmt.Errorf("%s", strings.TrimSpace(fmt.Sprintf("muse-code device token request failed: %d %s", resp.StatusCode, detail)))
}

// orderedForm encodes pairs in the order given, as URLSearchParams does;
// url.Values.Encode sorts keys, which changes the bytes NeoPi sends.
func orderedForm(pairs ...[2]string) string {
	var out strings.Builder
	for i, pair := range pairs {
		if i > 0 {
			out.WriteByte('&')
		}
		out.WriteString(url.QueryEscape(pair[0]))
		out.WriteByte('=')
		out.WriteString(url.QueryEscape(pair[1]))
	}
	return out.String()
}

func truncate(value string, limit int) string {
	if len(value) > limit {
		return value[:limit]
	}
	return value
}

// WaitForAuthorization polls the token endpoint until the user approves the
// device code (the first poll is immediate, as pollOAuthDeviceCodeFlow's),
// then mints the subscription's Model API key as the muse-code-key
// after-exchange hook does.
func (a *MetaAuth) WaitForAuthorization(ctx context.Context, dcr *DeviceCodeResponse) (*MetaAuthBundle, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if dcr == nil || dcr.DeviceCode == "" {
		return nil, fmt.Errorf("meta auth: missing device code response")
	}
	tokenEndpoint := dcr.TokenEndpoint
	if tokenEndpoint == "" {
		tokenEndpoint = TokenEndpoint
	}
	interval := DefaultPollInterval
	if dcr.Interval > 0 {
		interval = time.Duration(dcr.Interval) * time.Second
	}
	if interval < MinPollInterval {
		interval = MinPollInterval
	}
	var deadline time.Time
	if dcr.ExpiresIn > 0 {
		deadline = time.Now().Add(time.Duration(dcr.ExpiresIn) * time.Second)
	}
	expired := func() bool { return !deadline.IsZero() && !time.Now().Before(deadline) }
	slowDowns := 0
	for !expired() {
		pollCtx, cancel := ctx, context.CancelFunc(func() {})
		if !deadline.IsZero() {
			pollCtx, cancel = context.WithDeadline(ctx, deadline)
		}
		token, slowDown, err := a.pollDeviceToken(pollCtx, tokenEndpoint, dcr.DeviceCode)
		cancel()
		if ctx.Err() != nil {
			return nil, fmt.Errorf("meta auth: authorization timed out or canceled: %w", ctx.Err())
		}
		if err != nil && expired() {
			break
		}
		if err != nil {
			return nil, err
		}
		if token != nil {
			return a.finishLogin(ctx, token, true)
		}
		if expired() {
			break
		}
		if slowDown {
			slowDowns++
			interval += SlowDownIncrement
		}
		wait := interval
		if !deadline.IsZero() {
			if remaining := time.Until(deadline); remaining < wait {
				wait = remaining
			}
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("meta auth: authorization timed out or canceled: %w", ctx.Err())
		case <-time.After(wait):
		}
	}
	if slowDowns > 0 {
		return nil, fmt.Errorf("device flow timed out after one or more slow_down responses; this is often caused by clock drift in WSL or VM environments, so sync or restart the VM clock and try again")
	}
	return nil, fmt.Errorf("device flow timed out")
}

// finishLogin is attachMuseCodeApiKey over a fresh device token: the key is
// minted with onboarding, and an inactive or unpaid subscription fails the
// login.
func (a *MetaAuth) finishLogin(ctx context.Context, token *TokenData, requireIdentity bool) (*MetaAuthBundle, error) {
	payload, err := a.RequestKey(ctx, token.AccessToken, true)
	if err != nil {
		return nil, err
	}
	if payload.SubsActiveKnown && !payload.IsSubsActive {
		return nil, &KeyError{Status: http.StatusForbidden, Message: "invalid_grant: Muse Code subscription is inactive"}
	}
	if strings.TrimSpace(payload.APIKey) == "" {
		actionURL := strings.TrimSpace(payload.ActionURL)
		if actionURL == "" {
			actionURL = strings.TrimSpace(payload.RequirePaymentActionURL)
		}
		if payload.RequirePayment || actionURL != "" {
			if actionURL != "" {
				return nil, &KeyError{Message: "Muse Code subscription is required: " + actionURL}
			}
			return nil, &KeyError{Message: "Muse Code subscription is required"}
		}
		return nil, &KeyError{Message: "Muse Code key response is missing api_key"}
	}
	if requireIdentity && strings.TrimSpace(payload.AccountID()) == "" {
		return nil, &KeyError{Message: "Muse Code key response is missing a stable account identity"}
	}
	return &MetaAuthBundle{TokenData: token, MintedKey: payload, Email: payload.NormalizedEmail()}, nil
}

// MintAPIKey exchanges the device (dca:) token for the subscription's Model
// API key, as a sign-in does. A response that carries no key is an error.
func (a *MetaAuth) MintAPIKey(ctx context.Context, dcaToken string) (*MintedKeyResponse, error) {
	bundle, err := a.finishLogin(ctx, &TokenData{AccessToken: strings.TrimSpace(dcaToken)}, false)
	if err != nil {
		return nil, err
	}
	return bundle.MintedKey, nil
}

// RequestKey is requestMuseCodeKey: POST /muse-code/key with the device token.
// onboard asks Meta to onboard the account, as an interactive login does;
// usage reads pass false.
func (a *MetaAuth) RequestKey(ctx context.Context, accessToken string, onboard bool) (*MintedKeyResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return nil, fmt.Errorf("meta auth: missing dca token")
	}
	reqBody := []byte(`{}`)
	if onboard {
		reqBody = []byte(`{"onboard":true}`)
	}
	mintURL := a.mintURL
	if mintURL == "" {
		// META_MINT_URL redirects the key exchange (tests, staging); NeoPi's own
		// endpoint is the default.
		mintURL = strings.TrimSpace(os.Getenv("META_MINT_URL"))
	}
	if mintURL == "" {
		mintURL = MuseKeyURL
	}
	if onboard {
		// Only the interactive key mint is credential acquisition.
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, KeyRequestTimeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, mintURL, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("meta auth: create mint request: %w", err)
	}
	setNeoPiHeaders(req)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	client := *a.httpClient
	if !onboard {
		// A usage read happens against a connected account, not during
		// credential acquisition: no response deadline applies to it.
		client.Timeout = 0
	}
	// fetch(..., { redirect: "error" }): a redirect is a failure, never followed.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("meta auth: mint request failed: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("meta auth: close mint response body error: %v", errClose)
		}
	}()
	body, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return nil, fmt.Errorf("meta auth: read mint response: %w", errRead)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := fmt.Sprintf("Muse Code key exchange failed: %d", resp.StatusCode)
		if excerpt := strings.TrimSpace(truncate(string(body), 500)); excerpt != "" {
			message += " " + excerpt
		}
		return nil, &KeyError{Status: resp.StatusCode, Message: message}
	}
	var minted MintedKeyResponse
	if err := json.Unmarshal(body, &minted); err != nil {
		return nil, &KeyError{Status: resp.StatusCode, Message: "Muse Code key exchange returned invalid JSON"}
	}
	return &minted, nil
}

// CreateTokenStorage creates a serializable token storage record from the auth bundle.
// The credential never expires: Meta's device response carries no expiry and
// rejects refresh grants, and the minted key is the credential requests use.
func (a *MetaAuth) CreateTokenStorage(bundle *MetaAuthBundle) *MetaTokenStorage {
	if bundle == nil || bundle.TokenData == nil {
		return nil
	}
	storage := &MetaTokenStorage{
		Type:        "meta",
		AuthKind:    "oauth",
		AccessToken: bundle.TokenData.AccessToken,
		DCAToken:    bundle.TokenData.AccessToken,
		TokenType:   bundle.TokenData.TokenType,
		LastRefresh: time.Now().UTC().Format(time.RFC3339),
		BaseURL:     DefaultAPIBaseURL,
		Email:       bundle.Email,
	}
	if key := bundle.MintedKey; key != nil {
		storage.APIKey = key.APIKey
		if key.APIKey != "" {
			storage.AccessToken = key.APIKey
		}
		storage.AccountID = key.AccountID()
		if email := key.NormalizedEmail(); email != "" {
			storage.Email = email
		}
	}
	return storage
}

// CredentialFileName keeps a readable account name and hashes the original identity
// so distinct emails that sanitize identically cannot overwrite each other.
func CredentialFileName(email, sub string) string {
	clean := strings.TrimSpace(email)
	if clean != "" {
		sanitized := strings.Map(func(r rune) rune {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '-' || r == '_' {
				return r
			}
			return '_'
		}, clean)
		// Leave room for the prefix, identity hash and extension on common filesystems.
		if len(sanitized) > 120 {
			sanitized = sanitized[:120]
		}
		hash := sha256.Sum256([]byte(clean))
		return fmt.Sprintf("meta-%s-%s.json", sanitized, hex.EncodeToString(hash[:8]))
	}
	cleanSub := strings.TrimSpace(sub)
	if cleanSub != "" {
		hash := sha256.Sum256([]byte(cleanSub))
		return fmt.Sprintf("meta-%s.json", hex.EncodeToString(hash[:8]))
	}
	return "meta-oauth.json"
}

// NewMetaAuthWithClient is a MetaAuth that sends through client, for callers
// that already hold the credential's proxy-aware HTTP client.
func NewMetaAuthWithClient(client *http.Client) *MetaAuth {
	return &MetaAuth{httpClient: client}
}
