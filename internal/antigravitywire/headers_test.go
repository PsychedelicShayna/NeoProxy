package antigravitywire

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// headersCapture is testdata/antigravity_headers_capture.json: NeoPi's OWN
// wire/gemini-headers.ts (capture_antigravity_headers.ts).
type headersCapture struct {
	HostPlatform string `json:"hostPlatform"`
	Manifests    []struct {
		YAML    string  `json:"yaml"`
		Version *string `json:"version"`
	} `json:"manifests"`
	UserAgents []struct {
		Env       map[string]string `json:"env"`
		UserAgent string            `json:"userAgent"`
	} `json:"userAgents"`
	Ensure []struct {
		Name     string            `json:"name"`
		Env      map[string]string `json:"env"`
		Requests []struct {
			URL          string `json:"url"`
			CacheControl string `json:"cacheControl"`
			UserAgent    string `json:"userAgent"`
		} `json:"requests"`
		AfterFirst struct {
			Requests  int    `json:"requests"`
			Version   string `json:"version"`
			UserAgent string `json:"userAgent"`
		} `json:"afterFirst"`
		AfterSecond struct {
			Requests int    `json:"requests"`
			Version  string `json:"version"`
		} `json:"afterSecond"`
	} `json:"ensure"`
}

func loadHeadersCapture(t *testing.T) headersCapture {
	t.Helper()
	data, err := os.ReadFile("testdata/antigravity_headers_capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture headersCapture
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	return capture
}

// clearIdentityEnv empties the identity overrides for the test's lifetime.
func clearIdentityEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{"PI_AI_ANTIGRAVITY_VERSION", "PI_AI_ANTIGRAVITY_CL", "PI_AI_ANTIGRAVITY_OS", "PI_AI_ANTIGRAVITY_ARCH"} {
		t.Setenv(name, "")
	}
}

func TestParseManifestVersionMatchesNeoPi(t *testing.T) {
	for _, manifest := range loadHeadersCapture(t).Manifests {
		want := ""
		if manifest.Version != nil {
			want = *manifest.Version
		}
		if got := ParseManifestVersion(manifest.YAML); got != want {
			t.Errorf("ParseManifestVersion(%q) = %q, NeoPi %q", manifest.YAML, got, want)
		}
	}
}

func TestUserAgentMatchesNeoPi(t *testing.T) {
	capture := loadHeadersCapture(t)
	for _, scenario := range capture.UserAgents {
		clearIdentityEnv(t)
		for name, value := range scenario.Env {
			t.Setenv(name, value)
		}
		_, hasOS := scenario.Env["PI_AI_ANTIGRAVITY_OS"]
		_, hasArch := scenario.Env["PI_AI_ANTIGRAVITY_ARCH"]
		hostDefault := !hasOS || scenario.Env["PI_AI_ANTIGRAVITY_OS"] == "" || !hasArch || scenario.Env["PI_AI_ANTIGRAVITY_ARCH"] == ""
		if hostDefault && runtime.GOOS != capture.HostPlatform {
			continue
		}
		versionPinned := scenario.Env["PI_AI_ANTIGRAVITY_VERSION"] != ""
		if !versionPinned && Version() != DefaultVersion {
			continue
		}
		if got := UserAgent(); got != scenario.UserAgent {
			t.Errorf("UserAgent() under %v = %q, NeoPi %q", scenario.Env, got, scenario.UserAgent)
		}
	}
}

func TestSetClientHeadersMatchesNeoPi(t *testing.T) {
	clearIdentityEnv(t)
	t.Setenv("PI_AI_ANTIGRAVITY_VERSION", "2.26.0")
	header := http.Header{}
	SetClientHeaders(header, "")
	if got := header.Get("User-Agent"); got != UserAgent() {
		t.Fatalf("User-Agent = %q, want %q", got, UserAgent())
	}
	if got := header.Get("Accept-Encoding"); got != "gzip" {
		t.Fatalf("Accept-Encoding = %q, want gzip", got)
	}
	override := http.Header{}
	SetClientHeaders(override, "custom/1")
	if got := override.Get("User-Agent"); got != "custom/1" {
		t.Fatalf("User-Agent override = %q", got)
	}
}

type manifestTransport struct {
	mu       sync.Mutex
	answers  []manifestAnswer
	requests []*http.Request
}

type manifestAnswer struct {
	status int
	body   string
	fail   bool
}

func (transport *manifestTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.mu.Lock()
	transport.requests = append(transport.requests, request)
	index := len(transport.requests) - 1
	transport.mu.Unlock()
	answer := manifestAnswer{status: 599}
	if index < len(transport.answers) {
		answer = transport.answers[index]
	}
	if answer.fail {
		return nil, errors.New("network down")
	}
	status := answer.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(answer.body)), Request: request}, nil
}

func (transport *manifestTransport) count() int {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return len(transport.requests)
}

var ensureAnswers = map[string][]manifestAnswer{
	"manifest answers":          {{body: "version: 2.31.0\npath: Antigravity.dmg\n"}},
	"manifest 500 then 500":     {{status: 500}, {status: 500}},
	"manifest network error":    {{fail: true}, {fail: true}},
	"manifest without version":  {{body: "name: x\n"}, {body: "version: 2.31.0\n"}},
	"manifest invalid version":  {{body: "version: 2.31\n"}, {body: "version: 2.31.0\n"}},
	"env override skips lookup": {{body: "version: 2.31.0\n"}},
}

// TestEnsureVersionMatchesNeoPi scripts the update manifest the way the
// capture did and requires NeoPi's requests, the version it settles on, and
// that a settled or recently failed lookup is not repeated.
func TestEnsureVersionMatchesNeoPi(t *testing.T) {
	capture := loadHeadersCapture(t)
	if len(capture.Ensure) != len(ensureAnswers) {
		t.Fatalf("capture has %d scenarios, test scripts %d", len(capture.Ensure), len(ensureAnswers))
	}
	for _, scenario := range capture.Ensure {
		t.Run(scenario.Name, func(t *testing.T) {
			answers, ok := ensureAnswers[scenario.Name]
			if !ok {
				t.Fatalf("no scripted answers for %q", scenario.Name)
			}
			clearIdentityEnv(t)
			for name, value := range scenario.Env {
				t.Setenv(name, value)
			}
			t.Setenv("PI_AI_ANTIGRAVITY_OS", "linux")
			t.Setenv("PI_AI_ANTIGRAVITY_ARCH", "amd64")
			resetVersionState := func() {
				versionState.mu.Lock()
				versionState.discovered, versionState.failedAt, versionState.inflight = "", time.Time{}, nil
				versionState.mu.Unlock()
			}
			resetVersionState()
			t.Cleanup(resetVersionState)

			transport := &manifestTransport{answers: answers}
			client := &http.Client{Transport: transport}
			EnsureVersion(context.Background(), client)
			if got := transport.count(); got != scenario.AfterFirst.Requests {
				t.Fatalf("first call sent %d requests, NeoPi sent %d", got, scenario.AfterFirst.Requests)
			}
			if got := Version(); got != scenario.AfterFirst.Version {
				t.Errorf("version = %q, NeoPi %q", got, scenario.AfterFirst.Version)
			}
			if got := UserAgent(); got != scenario.AfterFirst.UserAgent {
				t.Errorf("user agent = %q, NeoPi %q", got, scenario.AfterFirst.UserAgent)
			}
			for i, want := range scenario.Requests {
				request := transport.requests[i]
				if request.Method != http.MethodGet || request.URL.String() != want.URL {
					t.Errorf("request %d = %s %s, NeoPi GET %s", i, request.Method, request.URL, want.URL)
				}
				if got := request.Header.Get("Cache-Control"); got != want.CacheControl {
					t.Errorf("request %d Cache-Control = %q, NeoPi %q", i, got, want.CacheControl)
				}
				if got := request.Header.Get("User-Agent"); got != want.UserAgent {
					t.Errorf("request %d User-Agent = %q, NeoPi %q", i, got, want.UserAgent)
				}
			}

			EnsureVersion(context.Background(), client)
			if got := transport.count(); got != scenario.AfterSecond.Requests {
				t.Errorf("second call left %d requests, NeoPi %d", got, scenario.AfterSecond.Requests)
			}
			if got := Version(); got != scenario.AfterSecond.Version {
				t.Errorf("version after second call = %q, NeoPi %q", got, scenario.AfterSecond.Version)
			}
		})
	}
}
