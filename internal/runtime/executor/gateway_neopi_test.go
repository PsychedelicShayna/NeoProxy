package executor

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/anthropicnpi"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// gatewayCapture is testdata/gateway_npi_capture.json.gz: NeoPi's OWN
// streamSimple run over the rows its model discovery resolves for OpenCode Go,
// OpenCode Zen and the Vercel AI Gateway, against a loopback server, one
// request per scenario beside the downstream request a client sends NeoProxy
// for it.
type gatewayCapture struct {
	APIKey    string `json:"apiKey"`
	SessionID string `json:"sessionId"`
	Fixtures  []struct {
		Name         string            `json:"name"`
		Row          string            `json:"row"`
		API          string            `json:"api"`
		RequestedURL string            `json:"requestedURL"`
		Headers      map[string]string `json:"headers"`
		RawBody      string            `json:"rawBody"`
		Downstream   json.RawMessage   `json:"downstream"`
	} `json:"fixtures"`
}

func readGatewayCapture(t *testing.T) gatewayCapture {
	t.Helper()
	data := readGzipFixture(t, "testdata/gateway_npi_capture.json.gz")
	var capture gatewayCapture
	if err := json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	if len(capture.Fixtures) == 0 {
		t.Fatal("capture has no fixtures")
	}
	return capture
}

// gatewayRow splits a captured row key ("opencode-go/claude-haiku-5-5") into
// the gateway provider id and NeoPi's record of the model.
func gatewayRow(t *testing.T, key string) (string, *registry.NeoPiModel) {
	t.Helper()
	for _, provider := range registry.GatewayProviders() {
		if model, ok := strings.CutPrefix(key, provider+"/"); ok {
			row := registry.GatewayNeoPiModel(provider, model)
			if row == nil {
				t.Fatalf("%s: registry has no NeoPi row", key)
			}
			return provider, row
		}
	}
	t.Fatalf("%s: unknown gateway", key)
	return "", nil
}

// Every captured Anthropic Messages request is reproduced byte for byte: URL,
// headers and body, for each thinking mode, tool, image and tool-result shape.
func TestGatewayNeoPiAnthropicParity(t *testing.T) {
	capture := readGatewayCapture(t)
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": capture.APIKey}}
	tested := 0
	for _, fixture := range capture.Fixtures {
		if fixture.API != "anthropic-messages" {
			continue
		}
		tested++
		t.Run(fixture.Name, func(t *testing.T) {
			provider, row := gatewayRow(t, fixture.Row)
			// Translators hand the shaper compact JSON.
			var downstream bytes.Buffer
			if err := json.Compact(&downstream, fixture.Downstream); err != nil {
				t.Fatal(err)
			}
			base := strings.TrimRight(row.BaseURL, "/")
			identity := neoPiWireIdentity{SessionID: capture.SessionID, APIKey: true}
			body, headers, err := projectNeoPiAnthropic(downstream.Bytes(), row.ID, base, "", identity, neoPiThirdPartyProfile(row))
			if err != nil {
				t.Fatal(err)
			}
			neoPiApplyHeaders(headers, auth, nil, &anthropicnpi.Credential{APIKey: capture.APIKey}, base, context.Background())
			applyThirdPartyIdentity(headers, gatewayThirdPartyProvider(provider), capture.APIKey, capture.SessionID)
			if url := base + "/v1/messages"; url != fixture.RequestedURL {
				t.Errorf("url = %s, want %s", url, fixture.RequestedURL)
			}
			if string(body) != fixture.RawBody {
				t.Errorf("body:\n got %s\nwant %s", body, fixture.RawBody)
			}
			want := map[string]string{}
			for name, value := range fixture.Headers {
				if name != "host" && name != "content-length" {
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
			for name := range got {
				if _, ok := want[name]; !ok {
					t.Errorf("extra header %s = %q", name, got[name])
				}
			}
		})
	}
	if tested == 0 {
		t.Fatal("capture has no Anthropic Messages fixtures")
	}
}

func readGzipFixture(t *testing.T, path string) []byte {
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
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
