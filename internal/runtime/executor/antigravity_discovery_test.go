package executor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/antigravitywire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// antigravityWireCapture is testdata/antigravity_npi_discovery_wire.json:
// NeoPi's OWN fetchAntigravityDiscoveryModels over a scripted fetch
// (capture_antigravity_npi_discovery_wire.ts).
type antigravityWireCapture struct {
	Scenarios []struct {
		Name           string   `json:"name"`
		ProjectID      *string  `json:"projectId"`
		RejectedStatus *int     `json:"rejectedStatus"`
		Models         []string `json:"models"`
		Requests       []struct {
			Method        string `json:"method"`
			URL           string `json:"url"`
			Authorization string `json:"authorization"`
			ContentType   string `json:"contentType"`
			UserAgent     string `json:"userAgent"`
			AcceptEnc     string `json:"acceptEncoding"`
			Body          string `json:"body"`
		} `json:"requests"`
	} `json:"scenarios"`
}

// antigravityWireAnswers is one scenario's answer per endpoint; the
// capture script scripted the same shapes.
var antigravityWireAnswers = map[string][]antigravityWireAnswer{
	"daily answers":          {{body: antigravityWireRoster}},
	"no project":             {{body: antigravityWireRoster}},
	"daily 500 then sandbox": {{status: 500}, {body: antigravityWireRoster}},
	"daily 401 then sandbox": {{status: 401}, {body: antigravityWireRoster}},
	"both rejected 403":      {{status: 403}, {status: 403}},
	"daily 401 sandbox 500":  {{status: 401}, {status: 500}},
	"daily invalid json":     {{body: "{not json"}, {body: antigravityWireRoster}},
	"daily non-object json":  {{body: "[]"}, {body: antigravityWireRoster}},
	"daily network error":    {{fail: true}, {body: antigravityWireRoster}},
	"both network errors":    {{fail: true}, {fail: true}},
	"empty roster":           {{body: "{}"}},
	"daily 429 then sandbox": {{status: 429}, {body: antigravityWireRoster}},
}

type antigravityWireAnswer struct {
	status int
	body   string
	fail   bool
}

// antigravityWireRoster is the ROSTER the capture script serves.
const antigravityWireRoster = `{"models":{` +
	`"gemini-3.1-pro-low":{"displayName":"Gemini 3.1 Pro (Low)","supportsImages":true,"supportsThinking":true,"thinkingLevel":1,"maxTokens":1048576,"maxOutputTokens":65535,"model":"MODEL_PLACEHOLDER_M36","apiProvider":"API_PROVIDER_GOOGLE_VERTEX","modelProvider":"MODEL_PROVIDER_GOOGLE"},` +
	`"gemini-pro-agent":{"displayName":"Gemini Pro Agent","supportsImages":true,"supportsThinking":true,"thinkingBudget":10001,"maxTokens":1048576,"maxOutputTokens":65535,"model":"MODEL_PLACEHOLDER_M16"},` +
	`"claude-sonnet-4-6-thinking":{"displayName":"Claude Sonnet 4.6 (Thinking)","supportsImages":true,"supportsThinking":true,"thinkingBudget":1024,"maxTokens":250000,"maxOutputTokens":64000},` +
	`"chat_20706":{"displayName":"denylisted"},` +
	`"internal-row":{"displayName":"internal","isInternal":true},` +
	`"gemini-4-argon-disabled":{"displayName":"unavailable","disabled":true}},` +
	`"agentModelSorts":[{"groups":[{"modelIds":["gemini-3.1-pro-low"]}]}],` +
	`"imageGenerationModelIds":["gemini-3-pro-image"]}`

type antigravityWireTransport struct {
	answers  []antigravityWireAnswer
	requests []*http.Request
	bodies   []string
}

func (transport *antigravityWireTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	body := ""
	if request.Body != nil {
		raw, _ := io.ReadAll(request.Body)
		body = string(raw)
	}
	transport.requests = append(transport.requests, request)
	transport.bodies = append(transport.bodies, body)
	index := -1
	switch request.URL.Scheme + "://" + request.URL.Host {
	case antigravitywire.DailyEndpoint:
		index = 0
	case antigravitywire.SandboxEndpoint:
		index = 1
	}
	if index < 0 || index >= len(transport.answers) {
		return &http.Response{StatusCode: 599, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
	}
	answer := transport.answers[index]
	if answer.fail {
		return nil, errors.New("network down")
	}
	status := answer.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(answer.body)), Request: request}, nil
}

// TestAntigravityDiscoveryReplaysNeoPiWire runs the roster request through
// both endpoints the way NeoPi's fetchAntigravityDiscoveryModels does and
// requires its requests and its verdict on every scripted answer: the first
// usable roster, a rejected credential, or a transient failure.
func TestAntigravityDiscoveryReplaysNeoPiWire(t *testing.T) {
	t.Setenv("PI_AI_ANTIGRAVITY_VERSION", "2.26.0")
	t.Setenv("PI_AI_ANTIGRAVITY_OS", "linux")
	t.Setenv("PI_AI_ANTIGRAVITY_ARCH", "amd64")
	data, err := os.ReadFile("testdata/antigravity_npi_discovery_wire.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture antigravityWireCapture
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	if len(capture.Scenarios) != len(antigravityWireAnswers) {
		t.Fatalf("capture has %d scenarios, test scripts %d", len(capture.Scenarios), len(antigravityWireAnswers))
	}
	for _, scenario := range capture.Scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			answers, ok := antigravityWireAnswers[scenario.Name]
			if !ok {
				t.Fatalf("no scripted answers for %q", scenario.Name)
			}
			transport := &antigravityWireTransport{answers: answers}
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
			auth := &cliproxyauth.Auth{ID: "antigravity-discovery-wire", Provider: "antigravity",
				Metadata: map[string]any{"access_token": "capture-token"}}
			if scenario.ProjectID != nil {
				auth.Metadata["project_id"] = *scenario.ProjectID
			}
			models, errFetch := FetchAntigravityModels(ctx, &config.Config{}, auth)

			if len(transport.requests) != len(scenario.Requests) {
				t.Fatalf("sent %d requests, NeoPi sent %d", len(transport.requests), len(scenario.Requests))
			}
			for i, want := range scenario.Requests {
				request := transport.requests[i]
				if request.Method != want.Method {
					t.Errorf("request %d method = %s, want %s", i, request.Method, want.Method)
				}
				if request.URL.String() != want.URL {
					t.Errorf("request %d url = %s, want %s", i, request.URL.String(), want.URL)
				}
				for name, wantValue := range map[string]string{
					"Authorization":   want.Authorization,
					"Content-Type":    want.ContentType,
					"User-Agent":      want.UserAgent,
					"Accept-Encoding": want.AcceptEnc,
				} {
					if got := request.Header.Get(name); got != wantValue {
						t.Errorf("request %d header %s = %q, want %q", i, name, got, wantValue)
					}
				}
				if transport.bodies[i] != want.Body {
					t.Errorf("request %d body = %q, want %q", i, transport.bodies[i], want.Body)
				}
			}

			var rejected *AntigravityRejectedError
			switch {
			case scenario.RejectedStatus != nil:
				if !errors.As(errFetch, &rejected) || rejected.Status != *scenario.RejectedStatus {
					t.Fatalf("error = %v, want a rejection with status %d", errFetch, *scenario.RejectedStatus)
				}
			case scenario.Models == nil:
				if errFetch == nil || errors.As(errFetch, &rejected) {
					t.Fatalf("error = %v, want a transient failure", errFetch)
				}
			default:
				if errFetch != nil {
					t.Fatalf("fetch: %v", errFetch)
				}
				got := make([]string, 0, len(models))
				for _, model := range models {
					got = append(got, model.ID)
				}
				want := append([]string{}, scenario.Models...)
				sort.Strings(got)
				sort.Strings(want)
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("roster = %v, NeoPi discovered %v", got, want)
				}
			}
		})
	}
}

// TestAntigravityDiscoveryEndpointOverride pins a single discovery endpoint
// when the account names one: NeoPi's `endpoint` option replaces the daily and
// sandbox hosts, trailing slashes trimmed.
func TestAntigravityDiscoveryEndpointOverride(t *testing.T) {
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"base_url": "https://example.invalid/"}}
	got := antigravityDiscoveryEndpoints(auth)
	if want := []string{"https://example.invalid"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("endpoints = %v, want %v", got, want)
	}
	if parsed, err := url.Parse(got[0]); err != nil || parsed.Host != "example.invalid" {
		t.Fatalf("override %q is not a usable endpoint: %v", got[0], err)
	}
	if got := antigravityDiscoveryEndpoints(&cliproxyauth.Auth{}); !reflect.DeepEqual(got, []string{antigravitywire.DailyEndpoint, antigravitywire.SandboxEndpoint}) {
		t.Fatalf("default endpoints = %v", got)
	}
}
