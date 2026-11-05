package marineris

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

type redirect struct{ target *url.URL }

func (r redirect) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set("X-Capture-Url", req.URL.String())
	clone.URL.Scheme, clone.URL.Host = r.target.Scheme, r.target.Host
	return http.DefaultTransport.RoundTrip(clone)
}

// The login's key check and the model listing send NeoPi's requests byte for
// byte and report its errors; the address normalization matches what the
// extension registered and persisted.
func TestMatchesNeoPiExtensionCapture(t *testing.T) {
	data, err := os.ReadFile("testdata/neopi_marineris_auth_capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Scenarios []struct {
			Name    string            `json:"name"`
			Env     map[string]string `json:"env"`
			Replies []struct {
				Status int             `json:"status"`
				Body   json.RawMessage `json:"body"`
			} `json:"replies"`
			Prompts       []string `json:"prompts"`
			Registrations []struct {
				BaseURL string `json:"baseUrl"`
			} `json:"registrations"`
			Result        json.RawMessage `json:"result"`
			Error         string          `json:"error"`
			ConfigWritten *struct {
				BaseURL string `json:"baseUrl"`
			} `json:"configWritten"`
			Requests []map[string]json.RawMessage `json:"requests"`
		} `json:"scenarios"`
	}
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range capture.Scenarios {
		if !strings.HasPrefix(scenario.Name, "login-") {
			continue
		}
		t.Run(scenario.Name, func(t *testing.T) {
			var requests []map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				headers := map[string]any{}
				for name, values := range r.Header {
					lower := strings.ToLower(name)
					if lower != "x-capture-url" {
						headers[lower] = strings.Join(values, ", ")
					}
				}
				requests = append(requests, map[string]any{"method": r.Method, "url": r.Header.Get("X-Capture-Url"), "headers": headers, "body": string(body)})
				reply := scenario.Replies[0]
				if reply.Status == 0 {
					reply.Status = http.StatusOK
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(reply.Status)
				_, _ = w.Write(reply.Body)
			}))
			defer server.Close()
			target, _ := url.Parse(server.URL)
			client := &http.Client{Transport: redirect{target: target}}
			address := ""
			if len(scenario.Prompts) > 0 {
				address = scenario.Prompts[0]
			}
			if base := scenario.Env["MARINERIS_BASE_URL"]; base != "" && address == "" {
				address = base
			}
			key := ""
			if len(scenario.Prompts) > 1 {
				key = scenario.Prompts[1]
			}
			base, _, err := Validate(context.Background(), client, address, key)
			if scenario.Error != "" {
				if err == nil || err.Error() != scenario.Error {
					t.Errorf("error = %v, want %q", err, scenario.Error)
				}
			} else {
				if err != nil {
					t.Fatalf("login failed: %v", err)
				}
				var wantKey string
				_ = json.Unmarshal(scenario.Result, &wantKey)
				if strings.TrimSpace(key) != wantKey {
					t.Errorf("key = %q, want %q", strings.TrimSpace(key), wantKey)
				}
				if want := scenario.Registrations[0].BaseURL; scenario.ConfigWritten != nil {
					want = scenario.ConfigWritten.BaseURL
					if base != want {
						t.Errorf("address = %s, want %s", base, want)
					}
				}
			}
			if len(requests) != len(scenario.Requests) {
				t.Fatalf("requests = %d, want %d", len(requests), len(scenario.Requests))
			}
			for i, want := range scenario.Requests {
				var wantHeaders map[string]any
				_ = json.Unmarshal(want["headers"], &wantHeaders)
				delete(wantHeaders, "host")
				delete(wantHeaders, "x-capture-url")
				var wantMethod, wantURL, wantBody string
				_ = json.Unmarshal(want["method"], &wantMethod)
				_ = json.Unmarshal(want["url"], &wantURL)
				_ = json.Unmarshal(want["body"], &wantBody)
				gotJSON, _ := json.Marshal(map[string]any{"method": requests[i]["method"], "url": requests[i]["url"], "headers": requests[i]["headers"], "body": requests[i]["body"]})
				wantJSON, _ := json.Marshal(map[string]any{"method": wantMethod, "url": wantURL, "headers": wantHeaders, "body": wantBody})
				if string(gotJSON) != string(wantJSON) {
					t.Errorf("request %d:\n got %s\nwant %s", i, gotJSON, wantJSON)
				}
			}
		})
	}
}

func TestNormalizeBaseURL(t *testing.T) {
	for in, want := range map[string]string{
		"10.0.0.7:9000/":                 "http://10.0.0.7:9000/v1",
		"https://gw.example.invalid/v1/": "https://gw.example.invalid/v1",
		"  http://127.0.0.1:18801  ":     "http://127.0.0.1:18801/v1",
		"gw.local/v1":                    "http://gw.local/v1",
	} {
		if got := NormalizeBaseURL(in); got != want {
			t.Errorf("NormalizeBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}
