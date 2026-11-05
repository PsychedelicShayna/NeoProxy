package executor

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"time"

	metaauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/meta"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
	"golang.org/x/sync/singleflight"
)

var metaRefreshGroup singleflight.Group

const (
	metaSubscriptionUsageEventType = "response.subscription_usage"
)

// Refresh mints the subscription's Model API key when a sign-in has none.
// NeoPi's muse-code credential never expires and rejects refresh grants; its
// after-exchange hook "reuses an already-minted subscription key instead of
// re-minting", because the key endpoint is aggressively rate limited and Meta
// returns the same key for the account. A credential that already carries a
// key is therefore returned unchanged.
func (e *NeoPiResponsesExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if e.Identifier() != "meta" {
		return auth, nil
	}
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	if auth == nil {
		return nil, statusErr{code: http.StatusInternalServerError, msg: "meta executor: auth is nil"}
	}
	dcaToken := extractDCAToken(auth)
	if _, token := metaCreds(auth); token != "" {
		return auth, nil
	}
	if dcaToken == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "meta executor: missing API key or DCA token"}
	}
	mintRes, err, _ := metaRefreshGroup.Do(dcaToken, func() (any, error) {
		return metaauth.NewMetaAuthWithProxyURL(e.cfg, auth.ProxyURL).MintAPIKey(ctx, dcaToken)
	})
	if err != nil {
		return nil, metaMintError(err)
	}
	minted, ok := mintRes.(*metaauth.MintedKeyResponse)
	if !ok || minted == nil || minted.APIKey == "" {
		return nil, statusErr{code: http.StatusBadGateway, msg: "meta executor: mint API key returned empty key"}
	}
	applyMintedKey(auth, minted, dcaToken)
	return auth, nil
}

// metaMintError keeps the key exchange's HTTP status on the error so a
// subscription that is inactive or unpaid is not retried as a transport fault.
func metaMintError(err error) error {
	if keyErr, ok := err.(*metaauth.KeyError); ok && keyErr.Status > 0 {
		return statusErr{code: keyErr.Status, msg: keyErr.Message}
	}
	return err
}

// applyMintedKey installs a minted key on the candidate auth, in its
// attributes, metadata and (when it has one) token storage.
func applyMintedKey(auth *cliproxyauth.Auth, minted *metaauth.MintedKeyResponse, dcaToken string) {
	baseURL, _ := metaCreds(auth)
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["base_url"] = baseURL
	auth.Metadata["api_key"] = minted.APIKey
	auth.Metadata["access_token"] = minted.APIKey
	auth.Metadata["dca_token"] = dcaToken
	delete(auth.Metadata, "expired")
	if email := minted.NormalizedEmail(); email != "" {
		auth.Metadata["email"] = email
	}
	if id := minted.AccountID(); id != "" {
		auth.Metadata["account_id"] = id
	}
	if tier := minted.SubsTierName; tier != "" {
		auth.Metadata["subs_tier_name"] = tier
	} else {
		delete(auth.Metadata, "subs_tier_name")
	}
	if minted.SubsTierID != "" {
		auth.Metadata["subs_tier_id"] = minted.SubsTierID
	} else {
		delete(auth.Metadata, "subs_tier_id")
	}
	auth.Metadata["type"] = "meta"
	nowStr := time.Now().Format(time.RFC3339)
	auth.Metadata["last_refresh"] = nowStr
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	auth.Attributes["base_url"] = baseURL
	auth.Attributes["api_key"] = minted.APIKey
	auth.Attributes["access_token"] = minted.APIKey
	// Auth.Clone does not clone Storage. Keep the candidate isolated from live
	// login storage; file-loaded records already persist through Metadata.
	if existing, ok := auth.Storage.(*metaauth.MetaTokenStorage); ok && existing != nil {
		storage := *existing
		storage.APIKey = minted.APIKey
		storage.AccessToken = minted.APIKey
		storage.DCAToken = dcaToken
		storage.Expired = ""
		storage.BaseURL = baseURL
		storage.LastRefresh = nowStr
		storage.Metadata = auth.Metadata
		if email := minted.NormalizedEmail(); email != "" {
			storage.Email = email
		}
		if id := minted.AccountID(); id != "" {
			storage.AccountID = id
		}
		auth.Storage = &storage
	}
	// Only the manager may accept and persist the candidate.
	auth.LastRefreshedAt = time.Now()
}

// ShouldPrepareRequestAuth reports true when a Meta auth has a device token but no usable API key.
func (e *NeoPiResponsesExecutor) ShouldPrepareRequestAuth(auth *cliproxyauth.Auth) bool {
	if e.Identifier() != "meta" || auth == nil || cliproxyauth.IsConfigAPIKeyAuth(auth) {
		return false
	}
	_, token := metaCreds(auth)
	return token == "" && extractDCAToken(auth) != ""
}

// PrepareRequestAuth mints the missing key before request execution, allowing
// the conductor to atomically install and persist the credential in the manager.
func (e *NeoPiResponsesExecutor) PrepareRequestAuth(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil || !e.ShouldPrepareRequestAuth(auth) {
		return auth, nil
	}
	return e.Refresh(ctx, auth)
}

func extractDCAToken(a *cliproxyauth.Auth) string {
	if a == nil || cliproxyauth.IsConfigAPIKeyAuth(a) {
		return ""
	}
	if a.Attributes != nil {
		if d := strings.TrimSpace(a.Attributes["dca_token"]); d != "" {
			return d
		}
		if t := strings.TrimSpace(a.Attributes["access_token"]); strings.HasPrefix(t, "dca:") {
			return t
		}
	}
	if a.Metadata != nil {
		if d, ok := a.Metadata["dca_token"].(string); ok && strings.TrimSpace(d) != "" {
			return strings.TrimSpace(d)
		}
		if t, ok := a.Metadata["access_token"].(string); ok && strings.HasPrefix(strings.TrimSpace(t), "dca:") {
			return strings.TrimSpace(t)
		}
	}
	if a.Storage != nil {
		if ms, ok := a.Storage.(*metaauth.MetaTokenStorage); ok && ms != nil {
			if ms.DCAToken != "" {
				return ms.DCAToken
			}
			if strings.HasPrefix(ms.AccessToken, "dca:") {
				return ms.AccessToken
			}
		}
	}
	return ""
}

func metaCreds(a *cliproxyauth.Auth) (baseURL, token string) {
	baseURL = metaauth.DefaultAPIBaseURL
	if a == nil {
		return baseURL, ""
	}

	if a.Attributes != nil {
		if b := strings.TrimSpace(a.Attributes["base_url"]); b != "" {
			baseURL = b
		}
		if k := strings.TrimSpace(a.Attributes["api_key"]); k != "" && !strings.HasPrefix(k, "dca:") {
			token = k
		} else if t := strings.TrimSpace(a.Attributes["access_token"]); t != "" && !strings.HasPrefix(t, "dca:") {
			token = t
		}
	}
	if a.Metadata != nil {
		if baseURL == metaauth.DefaultAPIBaseURL {
			if b, ok := a.Metadata["base_url"].(string); ok && strings.TrimSpace(b) != "" {
				baseURL = strings.TrimSpace(b)
			} else if b, ok := a.Metadata["api_base_url"].(string); ok && strings.TrimSpace(b) != "" {
				baseURL = strings.TrimSpace(b)
			}
		}
		if token == "" {
			if k, ok := a.Metadata["api_key"].(string); ok && strings.TrimSpace(k) != "" && !strings.HasPrefix(strings.TrimSpace(k), "dca:") {
				token = strings.TrimSpace(k)
			} else if t, ok := a.Metadata["access_token"].(string); ok && strings.TrimSpace(t) != "" && !strings.HasPrefix(strings.TrimSpace(t), "dca:") {
				token = strings.TrimSpace(t)
			}
		}
	}
	if token == "" && a.Storage != nil {
		if ms, ok := a.Storage.(*metaauth.MetaTokenStorage); ok && ms != nil {
			if ms.APIKey != "" {
				token = ms.APIKey
			} else if ms.AccessToken != "" && !strings.HasPrefix(ms.AccessToken, "dca:") {
				token = ms.AccessToken
			}
			if ms.BaseURL != "" && baseURL == metaauth.DefaultAPIBaseURL {
				baseURL = ms.BaseURL
			}
		}
	}
	return baseURL, token
}

func parseMetaRetryAfter(statusCode int, errorBody []byte, now time.Time) *time.Duration {
	if statusCode != http.StatusTooManyRequests || len(errorBody) == 0 {
		return nil
	}
	if resetsAt := gjson.GetBytes(errorBody, "error.resets_at").Int(); resetsAt > 0 {
		resetAtTime := time.Unix(resetsAt, 0)
		if resetAtTime.After(now) {
			retryAfter := resetAtTime.Sub(now)
			return &retryAfter
		}
	}
	return nil
}

type metaRateLimitError struct {
	statusErr
	credentialScoped bool
}

func (e metaRateLimitError) Unwrap() error {
	return e.statusErr
}

func (e metaRateLimitError) IsCredentialScoped() bool {
	return e.credentialScoped
}

func isMetaSubscriptionQuota(statusCode int, body []byte) bool {
	if statusCode != http.StatusTooManyRequests || len(body) == 0 {
		return false
	}
	msg := strings.ToLower(gjson.GetBytes(body, "error.message").String())
	if strings.Contains(msg, "subscription quota") || strings.Contains(msg, "quota exhausted") {
		return true
	}
	return false
}

func isMetaSubscriptionUsageEvent(eventType string, eventData []byte) bool {
	if eventType == metaSubscriptionUsageEventType {
		return true
	}
	if eventType == "" && len(eventData) > 0 && gjson.GetBytes(eventData, "subscription").IsObject() {
		return true
	}
	return false
}

func wrapMetaUpstreamError(statusCode int, body []byte) error {
	se := statusErr{code: statusCode, msg: string(body)}
	if statusCode == http.StatusTooManyRequests {
		if retryAfter := parseMetaRetryAfter(statusCode, body, time.Now()); retryAfter != nil {
			se.retryAfter = retryAfter
		}
		if isMetaSubscriptionQuota(statusCode, body) {
			return metaRateLimitError{statusErr: se, credentialScoped: true}
		}
	}
	return se
}

func metaStreamEventError(eventData []byte) error {
	eventType := gjson.GetBytes(bytes.TrimSpace(eventData), "type").String()
	if eventType != "error" && eventType != "response.failed" {
		return nil
	}
	statusCode := http.StatusBadGateway
	if code := int(gjson.GetBytes(eventData, "error.code").Int()); code >= 400 && code <= 599 {
		statusCode = code
	}
	return wrapMetaUpstreamError(statusCode, eventData)
}
