package antigravity

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// TestLoginAndRefreshMatchesNeoPi replays
// testdata/neopi_antigravity_auth_capture.json: NeoPi's own google-antigravity
// oauth-code rule and refresh grant with PKCE bytes and the clock pinned. Each
// scenario must produce the same authorize URL, the same requests on the wire,
// the same projected credential and the same error wording.
func TestLoginAndRefreshMatchesNeoPi(t *testing.T) {
	var capture struct {
		Now         int64  `json:"now"`
		RedirectURI string `json:"redirectUri"`
		State       string `json:"state"`
		Callback    struct {
			Port     int    `json:"port"`
			Path     string `json:"path"`
			Hostname string `json:"hostname"`
		} `json:"callback"`
		Scenarios []struct {
			Name   string  `json:"name"`
			Mode   string  `json:"mode"`
			Code   *string `json:"code"`
			Stored *struct {
				Access    string  `json:"access"`
				Refresh   string  `json:"refresh"`
				Expires   float64 `json:"expires"`
				ProjectID string  `json:"projectId"`
			} `json:"stored"`
			AdvanceMs  float64 `json:"advanceMs"`
			ClockReads int     `json:"clockReads"`
			Responses  []struct {
				Status int    `json:"status"`
				Body   string `json:"body"`
			} `json:"responses"`
			Authorize *struct {
				URL string `json:"url"`
			} `json:"authorize"`
			Requests []struct {
				Method  string            `json:"method"`
				URL     string            `json:"url"`
				Headers map[string]string `json:"headers"`
				Body    string            `json:"body"`
			} `json:"requests"`
			Credentials *struct {
				Access    string  `json:"access"`
				Refresh   string  `json:"refresh"`
				Expires   float64 `json:"expires"`
				Email     string  `json:"email"`
				ProjectID string  `json:"projectId"`
			} `json:"credentials"`
			Error *struct {
				Message string `json:"message"`
				Kind    string `json:"kind"`
				Status  *int   `json:"status"`
			} `json:"error"`
		} `json:"scenarios"`
	}
	if err := json.Unmarshal(readCapture(t, "neopi_antigravity_auth_capture.json"), &capture); err != nil {
		t.Fatal(err)
	}
	if want := RedirectURI(CallbackPort); capture.RedirectURI != want {
		t.Fatalf("NeoPi redirect URI %s, Go login advertises %s", capture.RedirectURI, want)
	}
	// NeoPi's capture pins crypto.getRandomValues to (i*7+3) & 0xff.
	raw := make([]byte, 96)
	for i := range raw {
		raw[i] = byte(i*7 + 3)
	}
	pkce := pkceFromBytes(raw)

	for _, scenario := range capture.Scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			responses := make([]scriptedResponse, len(scenario.Responses))
			for index, response := range scenario.Responses {
				responses[index] = scriptedResponse{Status: response.Status, Body: response.Body}
			}
			client := newCaptureClient(t, responses)
			reads := 0
			slept := []time.Duration{}
			flow := &Flow{
				HTTP: client.HTTPClient(),
				Now: func() time.Time {
					value := capture.Now
					if scenario.AdvanceMs > 0 {
						value += int64(scenario.AdvanceMs) * int64(reads)
						reads++
					}
					return time.UnixMilli(value)
				},
				Sleep: func(_ context.Context, d time.Duration) error {
					slept = append(slept, d)
					return nil
				},
			}

			var credential *Credential
			var err error
			switch scenario.Mode {
			case "refresh":
				if scenario.Stored == nil {
					t.Fatal("refresh scenario without stored credentials")
				}
				credential, err = flow.refresh(context.Background(), scenario.Stored.Refresh, scenario.Stored.ProjectID)
			default:
				code := "4/0Acode"
				if scenario.Code != nil {
					code = *scenario.Code
				}
				if scenario.Authorize != nil {
					if got := BuildAuthURL(capture.State, pkce.Challenge, capture.RedirectURI); got != scenario.Authorize.URL {
						t.Errorf("authorize URL:\n got %s\nwant %s", got, scenario.Authorize.URL)
					}
				}
				credential, err = flow.exchange(context.Background(), code, capture.RedirectURI, pkce.Verifier)
			}

			compareRequests(t, client.Requests(), scenario.Requests)
			if scenario.AdvanceMs > 0 && reads != scenario.ClockReads {
				t.Errorf("clock reads = %d, want %d", reads, scenario.ClockReads)
			}
			if scenario.Error != nil {
				if err == nil {
					t.Fatalf("error = nil, want %q", scenario.Error.Message)
				}
				if err.Error() != scenario.Error.Message {
					t.Errorf("error =\n %q\nwant %q", err.Error(), scenario.Error.Message)
				}
				oauthErr, ok := err.(*OAuthError)
				if !ok {
					t.Fatalf("error type %T, want *OAuthError", err)
				}
				if oauthErr.Kind != scenario.Error.Kind {
					t.Errorf("error kind = %q, want %q", oauthErr.Kind, scenario.Error.Kind)
				}
				wantStatus := 0
				if scenario.Error.Status != nil {
					wantStatus = *scenario.Error.Status
				}
				if oauthErr.Status != wantStatus {
					t.Errorf("error status = %d, want %d", oauthErr.Status, wantStatus)
				}
				if credential != nil {
					t.Errorf("credential = %+v, want none", credential)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			want := scenario.Credentials
			if want == nil {
				t.Fatal("scenario without credential or error")
			}
			if credential.Access != want.Access || credential.Refresh != want.Refresh ||
				credential.Email != want.Email || credential.ProjectID != want.ProjectID {
				t.Errorf("credential = %+v, want %s/%s/%s/%s", credential, want.Access, want.Refresh, want.Email, want.ProjectID)
			}
			if got := float64(credential.ExpiresAt.UnixMilli()); got != want.Expires {
				t.Errorf("expires = %.0f, want %.0f", got, want.Expires)
			}
		})
	}
}
