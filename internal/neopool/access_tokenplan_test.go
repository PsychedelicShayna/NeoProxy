package neopool_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopiimport"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopool"
)

const (
	tokenPlanIntlKey   = "sk-sp-synthetic-intl-0011"
	tokenPlanChinaKey  = "sk-sp-synthetic-china-0022"
	tokenPlanCookieRaw = "login_aliyunid_csrf=synthetic-cookie-secret"
)

// tokenPlanRegion is a fake regional endpoint that accepts only its own key,
// like QwenCloud's region-locked Token Plan endpoints.
type tokenPlanRegion struct {
	*httptest.Server
	mu   sync.Mutex
	keys []string
}

func newTokenPlanRegion(t *testing.T, key string) *tokenPlanRegion {
	t.Helper()
	region := &tokenPlanRegion{}
	region.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		region.mu.Lock()
		region.keys = append(region.keys, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		region.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+key {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"invalid_api_key"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"qwen3.7-plus"},{"id":"glm-5.2"},{"id":"wan2.7-image"},{"id":"qwen-image-3.0-pro"},{"id":"qwen-audio-3.0-asr-flash"},{"id":"deepseek-v4-pro"}]}`))
	}))
	t.Cleanup(region.Close)
	return region
}

func (r *tokenPlanRegion) sawKey(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, k := range r.keys {
		if k == key {
			return true
		}
	}
	return false
}

func TestTokenPlanKeyFindsItsRegionAndSkipsNonChatModels(t *testing.T) {
	f := newAccessFixture(t)
	intl, china := newTokenPlanRegion(t, tokenPlanIntlKey), newTokenPlanRegion(t, tokenPlanChinaKey)
	t.Cleanup(neopool.SetCatalogRegionsForTest(func(id string) []string {
		if id == "alibaba-token-plan" {
			return []string{intl.URL + "/v1", china.URL + "/v1"}
		}
		return nil
	}))

	// NeoPi probes only the selected China region.
	item := itemOf(t, f.json("admin", "POST", "/v8/neo/access/api-key", `{"provider":"alibaba-token-plan","apiKey":"`+tokenPlanChinaKey+`","baseUrl":"`+china.URL+`/v1","planId":"lite"}`, 201))
	if item.Provider != "alibaba-token-plan" || item.Kind != "apiKey" || item.Plan.MonthlyUSD != 6 {
		t.Fatalf("token plan item: %+v", item)
	}
	cfg, err := os.ReadFile(f.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	// NeoPi's sign-in keeps no model list with the key; the account serves
	// NeoPi's catalog and discovery.
	for _, want := range []string{"name = 'alibaba-token-plan'", "base-url = '" + china.URL + "/v1'"} {
		if !strings.Contains(string(cfg), want) {
			t.Fatalf("engine config lacks %q:\n%s", want, cfg)
		}
	}
	for _, unwanted := range []string{"wan2.7-image", "qwen-image", "qwen-audio", "name = 'qwen3.7-plus'", "base-url = '" + intl.URL} {
		if strings.Contains(string(cfg), unwanted) {
			t.Fatalf("engine config has %q:\n%s", unwanted, cfg)
		}
	}

	w := f.do("admin", "POST", "/v8/neo/access/api-key", []byte(`{"provider":"alibaba-token-plan","apiKey":"sk-sp-synthetic-wrong-0033"}`), "")
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "rejected this key") {
		t.Fatalf("key rejected everywhere: %d %s", w.Code, w.Body.String())
	}
}

func TestTokenPlanImportFromNeoPiBundle(t *testing.T) {
	f := newAccessFixture(t)
	intl, china := newTokenPlanRegion(t, tokenPlanIntlKey), newTokenPlanRegion(t, tokenPlanChinaKey)
	custom := newTokenPlanRegion(t, "sk-sp-synthetic-custom-0044")
	t.Cleanup(neopool.SetCatalogRegionsForTest(func(id string) []string {
		if id == "alibaba-token-plan" {
			return []string{intl.URL + "/v1", china.URL + "/v1"}
		}
		return nil
	}))
	bundle := func(token, baseURL string) map[string]any {
		raw, _ := json.Marshal(map[string]string{"token": token, "cookie": tokenPlanCookieRaw, "baseUrl": baseURL})
		return map[string]any{"key": string(raw), "source": "login"}
	}
	profile, _ := neoPiVault(t,
		[3]any{"alibaba-token-plan", "api_key", bundle(tokenPlanChinaKey, china.URL+"/v1")},
		[3]any{"alibaba-token-plan", "api_key", bundle("sk-sp-synthetic-custom-0044", custom.URL+"/v1")},
	)
	raw := f.json("admin", "GET", "/v8/neo/access/import/neopi?profile="+profile, "", 200)
	assertNoSecrets(t, raw, tokenPlanChinaKey, "synthetic-cookie-secret")
	var listed struct {
		Credentials []neopiimport.Candidate `json:"credentials"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		t.Fatal(err)
	}
	if c := listed.Credentials[0]; c.Kind != "apiKey" || c.NeoProxyProvider == nil || *c.NeoProxyProvider != "alibaba-token-plan" || c.Label != "…0022" || !c.Importable {
		t.Fatalf("token plan candidate = %+v", c)
	}

	raw = f.json("admin", "POST", "/v8/neo/access/import/neopi", `{"profile":"`+profile+`","credentialIds":["1","2"]}`, 200)
	var result struct {
		Items  []neopool.AccessItem `json:"items"`
		Failed []struct {
			CredentialID string `json:"credentialId"`
			Error        string `json:"error"`
		} `json:"failed"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 2 || result.Items[0].Provider != "alibaba-token-plan" || len(result.Failed) != 0 {
		t.Fatalf("import result = %s", raw)
	}
	// The bundle's region is tried first, so the key never reaches another region.
	if intl.sawKey(tokenPlanChinaKey) || !china.sawKey(tokenPlanChinaKey) {
		t.Fatal("imported China key was sent to the International region")
	}
	cfg, err := os.ReadFile(f.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	// The bare token is the inference key; the console cookie is stored beside
	// it for quota reporting, as in NeoPi's credential bundle.
	if !strings.Contains(string(cfg), "api-key = '"+tokenPlanChinaKey+"'") || !strings.Contains(string(cfg), "console-cookie = '"+tokenPlanCookieRaw+"'") || strings.Contains(string(cfg), `"token"`) {
		t.Fatalf("engine config does not hold the bare token:\n%s", cfg)
	}
}
