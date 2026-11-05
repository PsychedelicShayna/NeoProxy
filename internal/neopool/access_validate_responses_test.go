package neopool

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
)

type keyReplayTransport struct {
	status   int
	requests []map[string]string
	urls     []string
}

func (k *keyReplayTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	headers := map[string]string{}
	for name, values := range req.Header {
		headers[strings.ToLower(name)] = strings.Join(values, ", ")
	}
	k.requests = append(k.requests, headers)
	k.urls = append(k.urls, req.Method+" "+req.URL.String())
	return &http.Response{StatusCode: k.status, Body: io.NopCloser(strings.NewReader(`{"data":[]}`)), Header: http.Header{}, Request: req}, nil
}

// TestResponsesKeyValidationMatchesNeoPi replays the api-key logins NeoPi's own
// provider definitions run for meta, sakana and abliteration
// (internal/auth/meta/testdata/capture_neopi_meta_auth.ts): one GET of the
// models endpoint with fetch's Accept and Bun's User-Agent, and an outcome that
// depends only on the status.
func TestResponsesKeyValidationMatchesNeoPi(t *testing.T) {
	raw, err := os.ReadFile("../auth/meta/testdata/neopi_meta_auth_capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		APIKeyLogins []struct {
			Provider string `json:"provider"`
			Name     string `json:"name"`
			Error    string `json:"error"`
			Requests []struct {
				Method  string            `json:"method"`
				URL     string            `json:"url"`
				Headers map[string]string `json:"headers"`
			} `json:"requests"`
		} `json:"apiKeyLogins"`
	}
	if err = json.Unmarshal(raw, &capture); err != nil {
		t.Fatal(err)
	}
	if len(capture.APIKeyLogins) != 12 {
		t.Fatalf("%d api-key login captures, want 12", len(capture.APIKeyLogins))
	}
	statuses := map[string]int{"ok": 200, "rejected": 401, "server-error": 503}
	original := accessHTTPClient
	t.Cleanup(func() { accessHTTPClient = original })
	for _, scenario := range capture.APIKeyLogins {
		t.Run(scenario.Provider+"/"+scenario.Name, func(t *testing.T) {
			if scenario.Name == "empty" {
				// NeoPi refuses a blank paste before any request.
				if len(scenario.Requests) != 0 {
					t.Fatalf("NeoPi sent %d requests for a blank key", len(scenario.Requests))
				}
				return
			}
			transport := &keyReplayTransport{status: statuses[scenario.Name]}
			accessHTTPClient = &http.Client{Transport: transport}
			info, ok := catalogProvider(scenario.Provider)
			if !ok {
				t.Fatalf("%s missing from the catalog", scenario.Provider)
			}
			_, models, _, errValidate := validateCatalogKey(t.Context(), info, info.APIKey.BaseURL, "", "pasted-key")
			if len(scenario.Requests) != 1 || len(transport.requests) != 1 {
				t.Fatalf("requests: NeoPi %d, Go %d", len(scenario.Requests), len(transport.requests))
			}
			want := scenario.Requests[0]
			if got := transport.urls[0]; got != want.Method+" "+want.URL {
				t.Errorf("sent %q, NeoPi %q", got, want.Method+" "+want.URL)
			}
			source := map[string]string{}
			for name, value := range want.Headers {
				if name != "host" && name != "accept-encoding" && name != "connection" {
					source[name] = value
				}
			}
			if !reflect.DeepEqual(transport.requests[0], source) {
				t.Errorf("headers\ngot    %v\nsource %v", transport.requests[0], source)
			}
			if (errValidate == nil) != (scenario.Error == "") {
				t.Fatalf("Go error %v, NeoPi %q", errValidate, scenario.Error)
			}
			if scenario.Name == "rejected" {
				var access *AccessError
				if !errors.As(errValidate, &access) || !access.keyRejected {
					t.Errorf("a 401 must read as a rejected key, got %v", errValidate)
				}
			}
			if errValidate == nil && models != nil {
				t.Errorf("NeoPi's check keeps no roster, got %v", models)
			}
		})
	}
}
