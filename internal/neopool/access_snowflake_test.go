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

// A Snowflake PAT is added for the account it names and stored as NeoPi's
// structured credential bound to that account; nothing is probed, and an
// account outside snowflakecomputing.com is refused with NeoPi's reason.
func TestSnowflakeKeyIsBoundToItsAccount(t *testing.T) {
	f := newAccessFixture(t)
	t.Cleanup(neopool.SetAccessTransportForTest(refuseTransport{t}))
	item := itemOf(t, f.json("owner-a", "POST", "/v8/neo/access/api-key", `{"provider":"snowflake","apiKey":"pat-0123456789abcd","fields":{"account":"https://app.snowflake.com/MyOrg/My_Account"}}`, 201))
	// The pool names the token's ending and its account, never the stored JSON.
	if want := "Snowflake Cortex key ending in abcd (myorg-my-account.snowflakecomputing.com)"; item.Label != want {
		t.Errorf("label = %q, want %q", item.Label, want)
	}
	cfg, err := os.ReadFile(f.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	want := `api-key = '{"enterpriseUrl":"https://myorg-my-account.snowflakecomputing.com","token":"pat-0123456789abcd"}'`
	if !strings.Contains(string(cfg), want) {
		t.Fatalf("engine config lacks %s:\n%s", want, cfg)
	}

	for body, reason := range map[string]string{
		`{"provider":"snowflake","apiKey":"pat-123","fields":{"account":"https://evil.example.com"}}`:                                               "Paste your Snowflake account identifier",
		`{"provider":"snowflake","apiKey":"pat-123"}`:                                                                                               "Snowflake account is required",
		`{"provider":"snowflake","apiKey":"{\"token\":\"t\",\"enterpriseUrl\":\"https://a-b.snowflakecomputing.com\"}","fields":{"account":"c-d"}}`: "belongs to https://a-b.snowflakecomputing.com",
	} {
		w := f.do("owner-a", "POST", "/v8/neo/access/api-key", []byte(body), "")
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), reason) {
			t.Errorf("%s: %d %s", body, w.Code, w.Body.String())
		}
	}
}

// NeoPi's structured Snowflake key imports bound to the account it names; a
// bare PAT, which NeoPi binds to SNOWFLAKE_ACCOUNT, is not offered.
func TestSnowflakeImportCarriesKeyAccount(t *testing.T) {
	f := newAccessFixture(t)
	t.Cleanup(neopool.SetAccessTransportForTest(refuseTransport{t}))
	profile, _ := neoPiVault(t,
		[3]any{"snowflake", "api_key", map[string]any{"key": `{"token":"pat-imported-wxyz","enterpriseUrl":"https://org-acct.snowflakecomputing.com"}`}},
		[3]any{"snowflake", "api_key", map[string]any{"key": "pat-bare-0123"}},
	)
	var listed struct {
		Credentials []neopiimport.Candidate `json:"credentials"`
	}
	if err := json.Unmarshal(f.json("admin", "GET", "/v8/neo/access/import/neopi?profile="+profile, "", 200), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Credentials) != 2 || !listed.Credentials[0].Importable || listed.Credentials[1].Importable ||
		listed.Credentials[0].Label != "…wxyz (org-acct.snowflakecomputing.com)" ||
		!strings.Contains(listed.Credentials[1].Reason, "does not name its account") {
		t.Fatalf("candidates = %+v", listed.Credentials)
	}
	f.json("admin", "POST", "/v8/neo/access/import/neopi", `{"profile":"`+profile+`","credentialIds":["1"],"ownerId":"owner-a"}`, 200)
	cfg, err := os.ReadFile(f.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if want := `api-key = '{"enterpriseUrl":"https://org-acct.snowflakecomputing.com","token":"pat-imported-wxyz"}'`; !strings.Contains(string(cfg), want) {
		t.Fatalf("engine config lacks %s:\n%s", want, cfg)
	}
}
