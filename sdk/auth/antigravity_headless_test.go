package auth_test

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	antigravityauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/antigravity"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
)

type mockRoundTripper func(req *http.Request) (*http.Response, error)

func (m mockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return m(req)
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestAntigravityConstantsAndURLBuilder(t *testing.T) {
	if auth.AntigravityDefaultCallbackPort != 51121 {
		t.Fatalf("expected callback port 51121, got %d", auth.AntigravityDefaultCallbackPort)
	}
	expectedDefaultURI := "http://127.0.0.1:51121/oauth-callback"
	if got := auth.AntigravityDefaultCallbackURI(); got != expectedDefaultURI {
		t.Fatalf("expected default callback URI %q, got %q", expectedDefaultURI, got)
	}
	authURL := auth.BuildAntigravityAuthURL("state-123", "challenge-xyz", "")
	if !strings.HasPrefix(authURL, antigravityauth.AuthorizeURL+"?") {
		t.Fatalf("auth URL = %q, want the Google authorize endpoint", authURL)
	}
	// NeoPi's parameter order: client_id, response_type, redirect_uri, scope,
	// PKCE challenge, state, then the rule's authorize params.
	escapedScopes := make([]string, 0, len(antigravityauth.Scopes))
	for _, scope := range antigravityauth.Scopes {
		escapedScopes = append(escapedScopes, url.QueryEscape(scope))
	}
	expected := "client_id=" + antigravityauth.ClientID +
		"&response_type=code" +
		"&redirect_uri=http%3A%2F%2F127.0.0.1%3A51121%2Foauth-callback" +
		"&scope=" + strings.Join(escapedScopes, "+") +
		"&code_challenge=challenge-xyz" +
		"&code_challenge_method=S256" +
		"&state=state-123" +
		"&access_type=offline" +
		"&prompt=consent"
	if !strings.HasSuffix(authURL, "?"+expected) {
		t.Fatalf("auth URL =\n%s\nwant suffix\n%s", authURL, expected)
	}
	if !strings.Contains(authURL, "%2Fauth%2Faicode") {
		t.Fatalf("auth URL must carry the aicode scope: %s", authURL)
	}
}

func TestCompleteAntigravityOAuth(t *testing.T) {
	var calls []string
	client := &http.Client{Transport: mockRoundTripper(func(req *http.Request) (*http.Response, error) {
		calls = append(calls, req.Method+" "+req.URL.String())
		switch {
		case req.URL.String() == antigravityauth.TokenURL:
			return jsonResponse(http.StatusOK, `{"access_token":"ya29.access","refresh_token":"1//refresh","expires_in":3599,"token_type":"Bearer"}`), nil
		case strings.HasPrefix(req.URL.String(), "https://www.googleapis.com/oauth2/v1/userinfo"):
			return jsonResponse(http.StatusOK, `{"email":"pilot@example.com"}`), nil
		case strings.HasSuffix(req.URL.String(), ":loadCodeAssist"):
			return jsonResponse(http.StatusOK, `{"currentTier":{"id":"free-tier"},"paidTier":{"id":"g1-pro-tier"},"cloudaicompanionProject":"proj-1"}`), nil
		case strings.HasSuffix(req.URL.String(), ":retrieveUserQuotaSummary"):
			return jsonResponse(http.StatusOK, `{"groups":[{"displayName":"Gemini","buckets":[{"bucketId":"gemini-5h","displayName":"5h","window":"5h","remainingFraction":0.5,"resetTime":"2027-01-15T13:00:00Z"}]}]}`), nil
		}
		t.Fatalf("unexpected request: %s", req.URL)
		return nil, nil
	})}
	record, err := auth.CompleteAntigravityOAuth(context.Background(), "4/0Acode", auth.AntigravityDefaultCallbackURI(), "verifier", client)
	if err != nil {
		t.Fatal(err)
	}
	if record.Provider != "antigravity" || record.FileName != "antigravity-pilot@example.com.json" {
		t.Fatalf("record = %+v", record)
	}
	for key, want := range map[string]string{
		"access_token":  "ya29.access",
		"refresh_token": "1//refresh",
		"email":         "pilot@example.com",
		"project_id":    "proj-1",
		"type":          "antigravity",
	} {
		if got, _ := record.Metadata[key].(string); got != want {
			t.Errorf("metadata[%s] = %q, want %q", key, got, want)
		}
	}
	// A paid tier skips onboarding, discovery re-reads the project once, and the
	// login records the account's usage report.
	if len(calls) != 5 {
		t.Fatalf("calls = %v, want token, userinfo, two loadCodeAssist calls and the quota summary", calls)
	}
	if record.Quota.Signals["google_remaining_percent"] != "50%" {
		t.Fatalf("quota signals = %v, want a google counter watermark", record.Quota.Signals)
	}
}

func TestCompleteAntigravityOAuthProjectFailure(t *testing.T) {
	client := &http.Client{Transport: mockRoundTripper(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.URL.String() == antigravityauth.TokenURL:
			return jsonResponse(http.StatusOK, `{"access_token":"ya29.access","refresh_token":"1//refresh","expires_in":3599}`), nil
		case strings.HasPrefix(req.URL.String(), "https://www.googleapis.com/oauth2/v1/userinfo"):
			return jsonResponse(http.StatusOK, `{"email":"pilot@example.com"}`), nil
		default:
			return jsonResponse(http.StatusOK, `{"ineligibleTiers":[{"tierId":"free-tier","reasonMessage":"Not available in your region"}]}`), nil
		}
	})}
	_, err := auth.CompleteAntigravityOAuth(context.Background(), "4/0Acode", auth.AntigravityDefaultCallbackURI(), "verifier", client)
	if err == nil || !strings.Contains(err.Error(), "Not available in your region") {
		t.Fatalf("error = %v, want the free-tier ineligibility reason", err)
	}
}

func TestFetchAntigravityProjectID(t *testing.T) {
	client := &http.Client{Transport: mockRoundTripper(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Authorization") != "Bearer ya29.access" {
			t.Fatalf("authorization = %q", req.Header.Get("Authorization"))
		}
		if !strings.HasPrefix(req.Header.Get("User-Agent"), "antigravity/hub/") {
			t.Fatalf("user agent = %q", req.Header.Get("User-Agent"))
		}
		return jsonResponse(http.StatusOK, `{"currentTier":{"id":"free-tier"},"paidTier":{"id":"g1-pro-tier"},"cloudaicompanionProject":"proj-9"}`), nil
	})}
	projectID, err := auth.FetchAntigravityProjectID(context.Background(), "ya29.access", client)
	if err != nil || projectID != "proj-9" {
		t.Fatalf("project = %q, err = %v", projectID, err)
	}
}
