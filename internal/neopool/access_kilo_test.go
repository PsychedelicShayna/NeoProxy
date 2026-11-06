package neopool_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopool"
)

type refuseTransport struct{ t *testing.T }

func (r refuseTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.t.Errorf("unexpected key check: %s %s", req.Method, req.URL)
	return nil, errors.New("no network in this test")
}

// A NeoPi Kilo sign-in imports as a Kilo Gateway account: the token is kept
// as the gateway key without a check (NeoPi has none) and without a stored
// model list, and the account is named Kilo, not a custom server.
func TestKiloImportFromNeoPiVault(t *testing.T) {
	f := newAccessFixture(t)
	t.Cleanup(neopool.SetAccessTransportForTest(refuseTransport{t}))
	const token = "kilo-synthetic-gateway-token-0099"
	profile, _ := neoPiVault(t, [3]any{"kilo", "oauth", map[string]any{"access": token, "refresh": "", "expires": 4102444800000}})
	raw := f.json("admin", "POST", "/v8/neo/access/import/neopi", `{"profile":"`+profile+`","credentialIds":["1"]}`, 200)
	var result struct {
		Items []neopool.AccessItem `json:"items"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].Provider != "kilo" || result.Items[0].Kind != "apiKey" {
		t.Fatalf("import result = %s", raw)
	}
	cfg, err := os.ReadFile(f.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"name = 'kilo'", "base-url = 'https://api.kilo.ai/api/gateway'", "api-key = '" + token + "'"} {
		if !strings.Contains(string(cfg), want) {
			t.Fatalf("engine config lacks %q:\n%s", want, cfg)
		}
	}
	if strings.Contains(string(cfg), "models:") {
		t.Fatalf("Kilo account stored a model list:\n%s", cfg)
	}
}
