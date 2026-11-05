package antigravity

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/tidwall/gjson"
)

// neoPiTimeout is OAUTH_REQUEST_TIMEOUT_MS / DEFAULT_REQUEST_TIMEOUT_MS: the
// per-request ceiling for the sign-in phase, so a hung endpoint surfaces a
// login error instead of hanging.
const neoPiTimeout = 30 * time.Second

// OAuthError is NeoPi's AIError.OAuthError: the message it would show, plus
// the kind and HTTP status it carried.
type OAuthError struct {
	Kind    string
	Status  int
	Message string
}

func (e *OAuthError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func oauthError(kind string, message string) *OAuthError {
	return &OAuthError{Kind: kind, Message: message}
}

// Credential is NeoPi's projected OAuthCredentials for this provider.
type Credential struct {
	Access    string
	Refresh   string
	ProjectID string
	Email     string
	AccountID string
	ExpiresAt time.Time
}

// Flow runs NeoPi's google-antigravity sign-in and refresh.
type Flow struct {
	// HTTP is the client every request goes through; nil uses http.DefaultClient.
	HTTP *http.Client
	// Progress mirrors NeoPi's onProgress messages; nil ignores them.
	Progress func(message string)
	// Now is the clock the credential expiry is computed from; nil is time.Now.
	Now func() time.Time
	// Sleep waits between onboarding polls; nil sleeps for real.
	Sleep func(ctx context.Context, d time.Duration) error
}

func (f *Flow) client() *http.Client {
	if f != nil && f.HTTP != nil {
		return f.HTTP
	}
	return http.DefaultClient
}

func (f *Flow) now() time.Time {
	if f != nil && f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

func (f *Flow) sleep(ctx context.Context, d time.Duration) error {
	if f != nil && f.Sleep != nil {
		return f.Sleep(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (f *Flow) report(message string) {
	if f != nil && f.Progress != nil {
		f.Progress(message)
	}
}

// Exchange is exchangeToken: the authorization-code grant, the credential
// projection, the userinfo email and the project hook.
func (f *Flow) exchange(ctx context.Context, code, redirectURI, verifier string) (*Credential, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Providers may echo `code#state`; the fragment wins over the callback state.
	if fragment := strings.IndexByte(code, '#'); fragment >= 0 {
		code = code[:fragment]
	}
	body, err := f.postToken(ctx, [][2]string{
		{"grant_type", "authorization_code"},
		{"client_id", ClientID},
		{"client_secret", ClientSecret},
		{"code", code},
		{"redirect_uri", redirectURI},
		{"code_verifier", verifier},
	}, "token-exchange")
	if err != nil {
		return nil, err
	}
	credential, err := mapCredential(body, f.now())
	if err != nil {
		return nil, err
	}
	credential.Email = f.fetchUserInfo(ctx, credential.Access)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rawRefresh, _ := jsonPathString(body.result, "refresh_token")
	if rawRefresh == "" {
		return nil, oauthError("validation", "No refresh token received. Please try again.")
	}
	projectID, err := f.DiscoverProject(ctx, credential.Access)
	if err != nil {
		if validationURL := extractGoogleValidationURL(err.Error()); validationURL != "" {
			return nil, oauthError("validation", formatGoogleValidationRequired(validationURL, "sign in again", credential.Email))
		}
		return nil, err
	}
	credential.ProjectID = projectID
	return credential, nil
}

// Refresh is the compiled refresh rule: the refresh-token grant plus the
// project hook's refresh phase, which keeps the stored project id and never
// touches the network.
func (f *Flow) refresh(ctx context.Context, refreshToken, projectID string) (*Credential, error) {
	if projectID == "" {
		return nil, oauthError("configuration", Provider+" credentials are missing projectId; sign in again")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	body, err := f.postToken(ctx, [][2]string{
		{"grant_type", "refresh_token"},
		{"client_id", ClientID},
		{"client_secret", ClientSecret},
		{"refresh_token", refreshToken},
	}, "token-refresh")
	if err != nil {
		return nil, err
	}
	credential, err := mapCredential(body, f.now())
	if err != nil {
		return nil, err
	}
	// A refresh response that omits the refresh token keeps the stored one.
	if credential.Refresh == "" {
		credential.Refresh = refreshToken
	}
	credential.ProjectID = projectID
	return credential, nil
}

// setBunHeaders is Bun's default fetch identity, which NeoPi's plain token and
// userinfo requests carry (they declare no headers beyond Authorization and
// Content-Type).
func setBunHeaders(header http.Header) {
	header.Set("User-Agent", bunUserAgent)
	header.Set("Accept", "*/*")
	header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	header.Set("Connection", "keep-alive")
}

// Exchange runs NeoPi's authorization-code sign-in: the token exchange, the
// userinfo email and the project hook.
func Exchange(ctx context.Context, client *http.Client, code, redirectURI, codeVerifier string) (*Credential, error) {
	return (&Flow{HTTP: client}).exchange(ctx, code, redirectURI, codeVerifier)
}

// Refresh runs NeoPi's refresh grant against a stored credential. The stored
// project id is required (the hook keeps it) and the stored refresh token is
// kept when Google does not rotate it.
func Refresh(ctx context.Context, client *http.Client, refreshToken, storedProjectID string) (*Credential, error) {
	return (&Flow{HTTP: client}).refresh(ctx, refreshToken, storedProjectID)
}

// DiscoverProject resolves an account's Cloud Code Assist project with its
// access token, onboarding the free tier when the account has no tier yet.
func DiscoverProject(ctx context.Context, client *http.Client, accessToken string) (string, error) {
	return (&Flow{HTTP: client}).DiscoverProject(ctx, accessToken)
}

// postToken is postTokenRequest: a form-encoded POST whose body is parsed as
// JSON when it is JSON, with NeoPi's error wording for a rejected response.
func (f *Flow) postToken(ctx context.Context, params [][2]string, errorKind string) (tokenBody, error) {
	requestCtx, cancel := context.WithTimeout(ctx, neoPiTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, TokenURL, strings.NewReader(formEncode(params)))
	if err != nil {
		return tokenBody{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setBunHeaders(req.Header)
	resp, err := f.client().Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return tokenBody{}, ctxErr
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return tokenBody{}, oauthError("timeout", fmt.Sprintf("Timed out waiting for %s", TokenURL))
		}
		return tokenBody{}, oauthError(errorKind, fmt.Sprintf("%s request to %s failed: %v", Provider, TokenURL, err))
	}
	defer func() { _ = resp.Body.Close() }()
	text, err := readBody(resp)
	if err != nil {
		return tokenBody{}, oauthError(errorKind, fmt.Sprintf("%s request to %s failed: %v", Provider, TokenURL, err))
	}
	body := newTokenBody(text)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		label := "token exchange"
		if errorKind == "token-refresh" {
			label = "token refresh"
		}
		return tokenBody{}, &OAuthError{
			Kind:    errorKind,
			Status:  resp.StatusCode,
			Message: fmt.Sprintf("%s %s failed: %d %s", Provider, label, resp.StatusCode, sliceUTF16(text, 500)),
		}
	}
	return body, nil
}

// tokenBody is a token response as postTokenRequest keeps it: parsed JSON when
// the body is JSON, the raw text otherwise.
type tokenBody struct {
	result gjson.Result
	text   string
	parsed bool
}

func newTokenBody(text string) tokenBody {
	body := tokenBody{text: text}
	if text != "" && gjson.Valid(text) {
		body.result = gjson.Parse(text)
		body.parsed = true
	}
	return body
}

// mapCredential is mapCredentials for the rule's credential projection: the
// access token, the refresh token (kept from the previous credential when the
// response omits it), and `expires` from expires_in less the 5-minute skew.
func mapCredential(body tokenBody, now time.Time) (*Credential, error) {
	access, ok := jsonPathString(body.result, "access_token")
	if !ok || access == "" {
		return nil, oauthError("validation", Provider+" token response missing access token: "+excerptBody(body))
	}
	seconds := body.result.Get("expires_in")
	if seconds.Type != gjson.Number || math.IsInf(seconds.Num, 0) || math.IsNaN(seconds.Num) {
		return nil, oauthError("validation", Provider+" token response missing expires_in")
	}
	expiresMs := float64(now.UnixMilli()) + seconds.Num*1000 - expirySkewMs
	refresh, _ := jsonPathString(body.result, "refresh_token")
	return &Credential{Access: access, Refresh: refresh, ExpiresAt: time.UnixMilli(int64(math.Floor(expiresMs)))}, nil
}

// fetchUserInfo is applyUserinfo: a bearer GET whose failure leaves the email
// unset, as NeoPi does.
func (f *Flow) fetchUserInfo(ctx context.Context, accessToken string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, UserInfoURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	setBunHeaders(req.Header)
	resp, err := f.client().Do(req)
	if err != nil {
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ""
	}
	text, err := readBody(resp)
	if err != nil {
		return ""
	}
	email, _ := jsonPathString(parseJSON(text), "email")
	return email
}

// jsonPathString is jsonPath + scalarString: a dot path read from a parsed
// body, rendered as a non-empty string.
func jsonPathString(body gjson.Result, path string) (string, bool) {
	if path == "" {
		return "", false
	}
	return scalarString(body.Get(path))
}

// parseJSON is JSON.parse: an invalid document parses to nothing.
func parseJSON(text string) gjson.Result {
	if text == "" || !gjson.Valid(text) {
		return gjson.Result{}
	}
	return gjson.Parse(text)
}

// excerptBody is (typeof body === "string" ? body : JSON.stringify(body)).slice(0, 500).
func excerptBody(body tokenBody) string {
	if !body.parsed {
		return sliceUTF16(body.text, 500)
	}
	text := ""
	switch body.result.Type {
	case gjson.String:
		text = body.result.Str
	case gjson.Null:
		text = ""
	default:
		text = jsStringify(body.result)
	}
	return sliceUTF16(text, 500)
}

// readBody decodes a response body honouring its content encoding.
func readBody(resp *http.Response) (string, error) {
	decoded, err := util.DecodeResponseBody(resp.Body, resp.Header.Get("Content-Encoding"))
	if err != nil {
		return "", err
	}
	defer func() { _ = decoded.Close() }()
	raw, err := io.ReadAll(decoded)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
