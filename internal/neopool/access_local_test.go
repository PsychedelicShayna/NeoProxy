package neopool_test

import (
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopool"
)

// A local vLLM server is added by an administrator at the address they name.
// An empty key stands for NeoPi's no-auth placeholder, nothing is probed, and
// no model list is stored: discovery reads the server itself.
func TestLocalServerKeyUsesNeoPiPlaceholder(t *testing.T) {
	f := newAccessFixture(t)
	t.Cleanup(neopool.SetAccessTransportForTest(refuseTransport{t}))
	itemOf(t, f.json("admin", "POST", "/v8/neo/access/api-key", `{"provider":"vllm","apiKey":"","baseUrl":"http://gpu-box.lan:8000/v1"}`, 201))
	cfg, err := os.ReadFile(f.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"name = 'vllm'", "base-url = 'http://gpu-box.lan:8000/v1'", "api-key = 'vllm-local'"} {
		if !strings.Contains(string(cfg), want) {
			t.Fatalf("engine config lacks %q:\n%s", want, cfg)
		}
	}
	if strings.Contains(string(cfg), "models:") {
		t.Fatalf("local server stored a model list:\n%s", cfg)
	}

	w := f.do("owner-a", "POST", "/v8/neo/access/api-key", []byte(`{"provider":"lm-studio","apiKey":"","baseUrl":"http://10.0.0.5:1234/v1"}`), "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("owner named a server: %d %s", w.Code, w.Body.String())
	}
}
