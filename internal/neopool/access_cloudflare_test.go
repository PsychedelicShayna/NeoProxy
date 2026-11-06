package neopool_test

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopiimport"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopool"
)

// A Cloudflare AI Gateway token is stored as NeoPi's login credential with the
// account and gateway it was entered with; nothing is probed, and a missing or
// conflicting id is refused with NeoPi's reason.
func TestCloudflareKeyCarriesAccountAndGateway(t *testing.T) {
	f := newAccessFixture(t)
	t.Cleanup(neopool.SetAccessTransportForTest(refuseTransport{t}))
	item := itemOf(t, f.json("owner-a", "POST", "/v8/neo/access/api-key", `{"provider":"cloudflare-ai-gateway","apiKey":"cfut_0123456789abcd","fields":{"accountId":" acct0123456789abcdef0123456789ab ","gatewayId":"default"}}`, 201))
	if want := "Cloudflare AI Gateway key ending in abcd (gateway default)"; item.Label != want {
		t.Errorf("label = %q, want %q", item.Label, want)
	}
	cfg, err := os.ReadFile(f.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if want := `api-key = '{"token":"cfut_0123456789abcd","accountId":"acct0123456789abcdef0123456789ab","gatewayId":"default"}'`; !strings.Contains(string(cfg), want) {
		t.Fatalf("engine config lacks %s:\n%s", want, cfg)
	}

	for body, reason := range map[string]string{
		`{"provider":"cloudflare-ai-gateway","apiKey":"cfut_1","fields":{"gatewayId":"default"}}`:                                                           "Cloudflare account ID is required",
		`{"provider":"cloudflare-ai-gateway","apiKey":"cfut_1","fields":{"accountId":"acct"}}`:                                                              "Cloudflare AI Gateway ID is required",
		`{"provider":"cloudflare-ai-gateway","apiKey":"{\"token\":\"cfut_1\",\"gatewayId\":\"prod\"}","fields":{"accountId":"acct","gatewayId":"staging"}}`: "names AI Gateway ID prod, not staging",
		`{"provider":"cloudflare-ai-gateway","apiKey":"{\"gatewayId\":\"prod\"}","fields":{"accountId":"acct","gatewayId":"prod"}}`:                         "Paste the Cloudflare AI Gateway token",
	} {
		w := f.do("owner-a", "POST", "/v8/neo/access/api-key", []byte(body), "")
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), reason) {
			t.Errorf("%s: %d %s", body, w.Code, w.Body.String())
		}
	}
}

// NeoPi's Cloudflare login credential imports with its ids; a plain token,
// which NeoPi routes by CLOUDFLARE_* environment ids, is not offered.
func TestCloudflareImportCarriesAccountAndGateway(t *testing.T) {
	f := newAccessFixture(t)
	t.Cleanup(neopool.SetAccessTransportForTest(refuseTransport{t}))
	profile, _ := neoPiVault(t,
		[3]any{"cloudflare-ai-gateway", "api_key", map[string]any{"key": `{"token":"cfut_imported-wxyz","accountId":"acct0123456789abcdef0123456789ab","gatewayId":"prod"}`}},
		[3]any{"cloudflare-ai-gateway", "api_key", map[string]any{"key": "cfut_plain-0123"}},
	)
	var listed struct {
		Credentials []neopiimport.Candidate `json:"credentials"`
	}
	if err := json.Unmarshal(f.json("admin", "GET", "/v8/neo/access/import/neopi?profile="+profile, "", 200), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Credentials) != 2 || !listed.Credentials[0].Importable || listed.Credentials[1].Importable ||
		listed.Credentials[0].Label != "…wxyz (gateway prod)" ||
		!strings.Contains(listed.Credentials[1].Reason, "does not name its account and gateway") {
		t.Fatalf("candidates = %+v", listed.Credentials)
	}
	f.json("admin", "POST", "/v8/neo/access/import/neopi", `{"profile":"`+profile+`","credentialIds":["1"],"ownerId":"owner-a"}`, 200)
	cfg, err := os.ReadFile(f.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if want := `api-key = '{"token":"cfut_imported-wxyz","accountId":"acct0123456789abcdef0123456789ab","gatewayId":"prod"}'`; !strings.Contains(string(cfg), want) {
		t.Fatalf("engine config lacks %s:\n%s", want, cfg)
	}
}
