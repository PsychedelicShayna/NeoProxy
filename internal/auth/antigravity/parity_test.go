package antigravity

import (
	"bytes"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
)

// scriptedResponse is one captured NeoPi response.
type scriptedResponse struct {
	Status int
	Body   string
}

// capturedRequest is one request on the wire, as the capture recorded it.
type capturedRequest struct {
	Method  string
	URL     string
	Headers map[string]string
	Body    string
}

// captureClient serves scripted responses over loopback and records every
// request with its ORIGINAL URL, so a port to Google's hosts is replayed
// against httptest.
type captureClient struct {
	t         *testing.T
	server    *httptest.Server
	responses []scriptedResponse
	mu        sync.Mutex
	next      int
	requests  []capturedRequest
}

func newCaptureClient(t *testing.T, responses []scriptedResponse) *captureClient {
	t.Helper()
	client := &captureClient{t: t, responses: responses}
	client.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		client.mu.Lock()
		var next scriptedResponse
		if client.next < len(client.responses) {
			next = client.responses[client.next]
		} else {
			next = scriptedResponse{Status: 599, Body: "capture script exhausted"}
		}
		client.next++
		client.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(next.Status)
		_, _ = io.WriteString(w, next.Body)
	}))
	t.Cleanup(client.server.Close)
	return client
}

func (c *captureClient) Close() { c.server.Close() }

// HTTPClient returns a client whose transport records and rewrites to loopback.
func (c *captureClient) HTTPClient() *http.Client {
	target, err := url.Parse(c.server.URL)
	if err != nil {
		c.t.Fatal(err)
	}
	base := c.server.Client().Transport
	return &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		original := *req.URL
		var body []byte
		if req.Body != nil {
			body, _ = io.ReadAll(req.Body)
			_ = req.Body.Close()
			req.Body = io.NopCloser(bytes.NewReader(body))
			req.ContentLength = int64(len(body))
		}
		headers := map[string]string{}
		for name, values := range req.Header {
			if strings.EqualFold(name, "Content-Length") {
				continue
			}
			headers[strings.ToLower(name)] = strings.Join(values, ", ")
		}
		c.mu.Lock()
		c.requests = append(c.requests, capturedRequest{Method: req.Method, URL: original.String(), Headers: headers, Body: string(body)})
		c.mu.Unlock()

		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.Host = target.Host
		return base.RoundTrip(req)
	})}
}

func (c *captureClient) Requests() []capturedRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]capturedRequest(nil), c.requests...)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// readCapture loads a capture JSON file.
func readCapture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// compareRequests checks the wire requests against the capture.
func compareRequests(t *testing.T, got []capturedRequest, want []struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("requests = %d, want %d\n%+v", len(got), len(want), got)
	}
	for index := range want {
		expected := want[index]
		actual := got[index]
		if actual.Method != expected.Method || actual.URL != expected.URL {
			t.Errorf("request %d = %s %s, want %s %s", index, actual.Method, actual.URL, expected.Method, expected.URL)
		}
		if actual.Body != expected.Body {
			t.Errorf("request %d body:\n got %q\nwant %q", index, actual.Body, expected.Body)
		}
		expectedHeaders := map[string]string{}
		for name, value := range expected.Headers {
			if name != "content-length" && name != "host" {
				expectedHeaders[name] = value
			}
		}
		if len(actual.Headers) != len(expectedHeaders) {
			t.Errorf("request %d headers:\n got %v\nwant %v", index, actual.Headers, expectedHeaders)
		}
		for name, value := range expectedHeaders {
			if actual.Headers[name] != value {
				t.Errorf("request %d header %s = %q, want %q", index, name, actual.Headers[name], value)
			}
		}
	}
}

// equalJSON compares two JSON values, tolerating float noise.
func equalJSON(t *testing.T, got, want any) {
	t.Helper()
	if !jsonEqualWithTolerance(got, want) {
		t.Fatalf("value mismatch:\n got %#v\nwant %#v", got, want)
	}
}

func jsonEqualWithTolerance(got, want any) bool {
	switch expected := want.(type) {
	case map[string]any:
		actual, ok := got.(map[string]any)
		if !ok || len(actual) != len(expected) {
			return false
		}
		for key, value := range expected {
			actualValue, found := actual[key]
			if !found || !jsonEqualWithTolerance(actualValue, value) {
				return false
			}
		}
		return true
	case []any:
		actual, ok := got.([]any)
		if !ok || len(actual) != len(expected) {
			return false
		}
		for index := range expected {
			if !jsonEqualWithTolerance(actual[index], expected[index]) {
				return false
			}
		}
		return true
	case float64:
		actual, ok := got.(float64)
		if !ok {
			return false
		}
		if expected == actual {
			return true
		}
		scale := math.Max(math.Abs(expected), math.Abs(actual))
		return math.Abs(expected-actual) <= 1e-9*math.Max(scale, 1)
	case nil:
		return got == nil
	default:
		return got == want
	}
}
