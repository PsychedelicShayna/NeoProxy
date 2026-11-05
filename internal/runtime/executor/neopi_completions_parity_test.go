package executor

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// neoPiCompletionsCapture is one provider's testdata capture: NeoPi's OWN
// streamSimple run over its bundled rows against a loopback server, one
// request per scenario, beside the downstream body a client sends NeoProxy.
type neoPiCompletionsCapture struct {
	Provider  string `json:"provider"`
	APIKey    string `json:"apiKey"`
	SessionID string `json:"sessionId"`
	Fixtures  []struct {
		Name         string            `json:"name"`
		Model        string            `json:"model"`
		RequestedURL string            `json:"requestedURL"`
		Headers      map[string]string `json:"headers"`
		RawBody      string            `json:"rawBody"`
		Downstream   json.RawMessage   `json:"downstream"`
		Error        string            `json:"error"`
	} `json:"fixtures"`
}

// TestNeoPiCompletionsRequestParity replays every captured request: URL,
// headers and body must match NeoPi's own byte for byte.
func TestNeoPiCompletionsRequestParity(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "neopi_completions", "*.fixtures.json.gz"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no captures: %v", err)
	}
	total := 0
	for _, file := range files {
		capture := readNeoPiCompletionsCapture(t, file)
		provider := capture.Provider
		t.Run(provider, func(t *testing.T) {
			auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": capture.APIKey}}
			for _, fixture := range capture.Fixtures {
				if fixture.Error != "" {
					t.Errorf("%s: NeoPi capture failed: %s", fixture.Name, fixture.Error)
					continue
				}
				t.Run(fixture.Name, func(t *testing.T) {
					row := registry.NeoPiCompletionsModel(provider, fixture.Model)
					if row == nil || row.CompletionsCompat == nil {
						t.Fatalf("no NeoPi row for %s", fixture.Model)
					}
					if row.ID != fixture.Model {
						t.Fatalf("row = %s, want %s", row.ID, fixture.Model)
					}
					var downstream bytes.Buffer
					if err := json.Compact(&downstream, fixture.Downstream); err != nil {
						t.Fatal(err)
					}
					body, err := neoPiCompletionsRequest(downstream.Bytes(), row, neoPiCompletionsCall{SessionID: capture.SessionID})
					if err != nil {
						t.Fatal(err)
					}
					url := strings.TrimRight(row.BaseURL, "/") + "/chat/completions"
					if url != fixture.RequestedURL {
						t.Errorf("url = %s, want %s", url, fixture.RequestedURL)
					}
					if string(body) != fixture.RawBody {
						t.Errorf("body:\n got %s\nwant %s", body, fixture.RawBody)
					}
					headers := neoPiCompletionsHeaders(context.Background(), auth, nil, capture.APIKey, row)
					want := map[string]string{}
					for name, value := range fixture.Headers {
						if name == "host" || name == "content-length" {
							continue
						}
						want[name] = value
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
					for name, value := range got {
						if _, ok := want[name]; !ok {
							t.Errorf("extra header %s = %q", name, value)
						}
					}
				})
			}
		})
		total += len(capture.Fixtures)
	}
	t.Logf("%d captured requests replayed", total)
}

func readNeoPiCompletionsCapture(t *testing.T, path string) neoPiCompletionsCapture {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	reader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	var capture neoPiCompletionsCapture
	if err := json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	if len(capture.Fixtures) == 0 {
		t.Fatalf("%s has no fixtures", path)
	}
	return capture
}

func TestNeoPiCompletionsTimeoutDisabled(t *testing.T) {
	zero := 0
	row := &registry.NeoPiModel{CompletionsCompat: &registry.NeoPiCompletionsCompat{StreamFirstEventTimeoutMs: &zero}}
	if value := neoPiCompletionsHeaders(context.Background(), nil, nil, "key", row).Get("X-Stainless-Timeout"); value != "" {
		t.Fatalf("disabled timeout sent %q", value)
	}
}

func TestNeoPiCompletionsFinishedEOF(t *testing.T) {
	input := `data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}` + "\n\n"
	var lines []string
	err := neoPiScanCompletions(context.Background(), nil, "test", strings.NewReader(input), func(line []byte) error {
		lines = append(lines, string(line))
		return nil
	})
	if err != nil || len(lines) != 2 || lines[1] != "data: [DONE]" {
		t.Fatalf("lines = %q, error = %v", lines, err)
	}
}
