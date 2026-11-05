package executor

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/googlewire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// geminiCapture is testdata/gemini_npi_capture.json.gz: NeoPi's OWN
// streamSimple run over its bundled google rows against a loopback server, one
// request per scenario, beside the downstream Chat Completions request a
// client sends NeoProxy for it.
type geminiCapture struct {
	APIKey    string `json:"apiKey"`
	SessionID string `json:"sessionId"`
	Fixtures  []struct {
		Name         string            `json:"name"`
		Model        string            `json:"model"`
		RequestedURL string            `json:"requestedURL"`
		Headers      map[string]string `json:"headers"`
		RawBody      string            `json:"rawBody"`
		Downstream   json.RawMessage   `json:"downstream"`
	} `json:"fixtures"`
}

// Every captured NeoPi request is reproduced byte for byte: URL, headers and
// body, for each thinking mode, tool, image, tool-result and retry shape.
func TestGeminiNeoPiRequestParity(t *testing.T) {
	file, err := os.Open("testdata/gemini_npi_capture.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	var capture geminiCapture
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	if len(capture.Fixtures) == 0 {
		t.Fatal("capture has no fixtures")
	}
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": capture.APIKey}}
	for _, fixture := range capture.Fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			row := registry.GoogleNeoPiModelFor(fixture.Model)
			if row == nil || row.ID != fixture.Model {
				t.Fatalf("row = %+v, want model %s", row, fixture.Model)
			}
			// Translators hand the shaper compact JSON.
			var downstream bytes.Buffer
			if err = json.Compact(&downstream, fixture.Downstream); err != nil {
				t.Fatal(err)
			}
			wire := neoPiGoogleWireModel(row)
			neoPiContext, options, err := googlewire.FromOpenAIChat(downstream.Bytes(), wire)
			if err != nil {
				t.Fatal(err)
			}
			resolved, err := googlewire.MapOptions(wire, options)
			if err != nil {
				t.Fatal(err)
			}
			body, err := googlewire.BuildBody(wire, neoPiContext, resolved, nil)
			if err != nil {
				t.Fatal(err)
			}
			url := geminiNeoPiBaseURL(auth, row) + "/models/" + row.ID + ":streamGenerateContent?alt=sse"
			if url != fixture.RequestedURL {
				t.Errorf("url = %s, want %s", url, fixture.RequestedURL)
			}
			if string(body) != fixture.RawBody {
				t.Errorf("body:\n got %s\nwant %s", body, fixture.RawBody)
			}
			headers := neoPiGoogleHeaders(context.Background(), auth, nil, capture.APIKey)
			// `connection` is transport-owned: Bun's fetch stamps keep-alive on
			// HTTP/1.1, Go's transport manages it and forbids it on the HTTP/2 the
			// Gemini API is served over.
			want := map[string]string{}
			for name, value := range fixture.Headers {
				if name != "host" && name != "content-length" && name != "connection" {
					want[name] = value
				}
			}
			got := map[string]string{}
			for name, values := range headers {
				got[strings.ToLower(name)] = strings.Join(values, ", ")
			}
			for name, value := range want {
				if got[name] != value {
					t.Errorf("header %s = %q, want %q", name, got[name], value)
				}
			}
			delete(got, "connection")
			for name := range got {
				if _, ok := want[name]; !ok {
					t.Errorf("extra header %s = %q", name, got[name])
				}
			}
			var _ http.Header = headers
		})
	}
}
