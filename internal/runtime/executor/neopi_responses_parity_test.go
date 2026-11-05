package executor

import (
	"encoding/json"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/openaiwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

type responsesCapture struct {
	Credentials map[string]string `json:"credentials"`
	SessionID   string            `json:"sessionId"`
	Fixtures    []struct {
		Name         string `json:"name"`
		RequestedURL string `json:"requestedURL"`
		Scenario     struct {
			Provider   string          `json:"provider"`
			Model      string          `json:"model"`
			Downstream json.RawMessage `json:"downstream"`
		} `json:"scenario"`
		Requests []struct {
			Method  string            `json:"method"`
			Path    string            `json:"path"`
			Headers map[string]string `json:"headers"`
			RawBody string            `json:"rawBody"`
		} `json:"requests"`
	} `json:"fixtures"`
}

// bearerFor is the key NeoPi's transport puts on the wire for a stored credential:
// the muse-code credential is the JSON bearer of the device token and the key.
func bearerFor(provider, credential string) string {
	if provider == registry.ResponsesMuseCode {
		var wrapped struct {
			APIKey string `json:"apiKey"`
		}
		_ = json.Unmarshal([]byte(credential), &wrapped)
		return wrapped.APIKey
	}
	return credential
}

// TestNeoPiResponsesMatchesNeoPiSource replays the scenarios captured from
// NeoPi's own streamOpenAIResponses (testdata/capture_responses_npi.ts):
// method, URL, headers and body bytes of every downstream turn for Meta, Muse
// Code, Sakana and Abliteration.
func TestNeoPiResponsesMatchesNeoPiSource(t *testing.T) {
	raw, err := os.ReadFile("testdata/responses_npi_requests.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture responsesCapture
	if err = json.Unmarshal(raw, &capture); err != nil {
		t.Fatal(err)
	}
	if len(capture.Fixtures) < 90 {
		t.Fatalf("only %d fixtures", len(capture.Fixtures))
	}
	framing := map[string]bool{"host": true, "content-length": true, "accept-encoding": true, "connection": true}
	for _, fixture := range capture.Fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			provider := fixture.Scenario.Provider
			model := registry.ResponsesNeoPiModel(provider, fixture.Scenario.Model, nil)
			if model == nil {
				t.Fatalf("model %s/%s missing from the catalog", provider, fixture.Scenario.Model)
			}
			ctx, opts := openaiwire.FromResponses(fixture.Scenario.Downstream, model)
			opts.SessionID = capture.SessionID
			request := openaiwire.ShapeResponses(model, ctx, opts, bearerFor(provider, capture.Credentials[provider]))
			(&NeoPiResponsesExecutor{identifier: "meta"}).applyTransportHeaders(request.Header, nil, model, nil)
			want := fixture.Requests[0]
			if request.URL != fixture.RequestedURL {
				t.Errorf("URL %s; source %s", request.URL, fixture.RequestedURL)
			}
			if string(request.Body) != want.RawBody {
				t.Errorf("body\ngot    %s\nsource %s", request.Body, want.RawBody)
			}
			got := map[string]string{}
			for name, values := range request.Header {
				got[strings.ToLower(name)] = strings.Join(values, ", ")
			}
			source := map[string]string{}
			for name, value := range want.Headers {
				if !framing[name] {
					source[name] = value
				}
			}
			if !reflect.DeepEqual(got, source) {
				t.Errorf("headers\ngot    %v\nsource %v", got, source)
			}
			if want.Method != http.MethodPost {
				t.Errorf("source method %s", want.Method)
			}
		})
	}
}
