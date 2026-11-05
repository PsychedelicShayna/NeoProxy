package neopool

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func testJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(`{"alg":"none"}`)) + "." + enc.EncodeToString(payload) + ".sig"
}

func TestDetectPlanFromCredentialContents(t *testing.T) {
	codexToken := testJWT(t, map[string]any{"email": "a@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro"}})
	noClaim := testJWT(t, map[string]any{"email": "a@example.com"})
	cases := []struct {
		name, provider, want string
		auth                 *coreauth.Auth
	}{
		{"codex id_token claim", "codex", "pro", &coreauth.Auth{Metadata: map[string]any{"id_token": codexToken}, Attributes: map[string]string{"plan_type": "free"}}},
		{"codex file name suffix beats engine default", "codex", "plus", &coreauth.Auth{FileName: "codex-1a2b3c4d-a@example.com-plus.json", Metadata: map[string]any{"id_token": noClaim}, Attributes: map[string]string{"plan_type": "free"}}},
		{"codex business attribute", "codex", "business", &coreauth.Auth{Attributes: map[string]string{"plan_type": "business"}}},
		{"claude max 20x tier", "claude", "max20x", &coreauth.Auth{Metadata: map[string]any{"subscription_type": "max", "rate_limit_tier": "default_claude_max_20x"}}},
		{"claude max without tier", "claude", "max5x", &coreauth.Auth{Metadata: map[string]any{"subscription_type": "max"}}},
		{"claude pro", "claude", "pro", &coreauth.Auth{Metadata: map[string]any{"subscriptionType": "pro"}}},
		{"grok heavy", "xai", "supergrok-heavy", &coreauth.Auth{Metadata: map[string]any{"plan": "SuperGrok Heavy"}}},
		{"google ultra", "antigravity", "google-ai-ultra", &coreauth.Auth{Metadata: map[string]any{"tier": "g1-ultra-tier"}}},
		{"unknown", "kimi", "", &coreauth.Auth{}},
	}
	for _, tc := range cases {
		if got := detectPlan(tc.provider, tc.auth); got != tc.want {
			t.Errorf("%s: detectPlan = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestResolvePlanDefaultsAndOverrides(t *testing.T) {
	codexToken := testJWT(t, map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "team"}})
	sub := &coreauth.Auth{Metadata: map[string]any{"id_token": codexToken}}
	if got := resolvePlan("codex", "subscription", sub, planOverride{}); got != (AccessPlan{ID: "team", Name: "ChatGPT Team", MonthlyUSD: 30, Source: "detected"}) {
		t.Fatalf("detected plan = %+v", got)
	}
	if got := resolvePlan("kimi", "subscription", &coreauth.Auth{}, planOverride{}); got.MonthlyUSD != 20 || got.Source != "default" {
		t.Fatalf("unknown subscription = %+v", got)
	}
	if got := resolvePlan("openrouter", "apiKey", &coreauth.Auth{}, planOverride{}); got != (AccessPlan{ID: "apiKey", Name: "Pay-as-you-go key", MonthlyUSD: 20, Source: "default"}) {
		t.Fatalf("api key default = %+v", got)
	}
	planID, monthly := "pro", 150.0
	if got := resolvePlan("codex", "subscription", sub, planOverride{PlanID: &planID}); got.ID != "pro" || got.MonthlyUSD != 200 || got.Source != "admin" {
		t.Fatalf("admin plan = %+v", got)
	}
	if got := resolvePlan("codex", "subscription", sub, planOverride{MonthlyUSD: &monthly}); got.ID != "team" || got.MonthlyUSD != 150 || got.Source != "admin" {
		t.Fatalf("admin monthly = %+v", got)
	}
	for provider, plans := range map[string][]string{"codex": {"free", "plus", "pro", "team", "business", "enterprise"}, "claude": {"pro", "max5x", "max20x"}, "xai": {"supergrok", "supergrok-heavy"}, "antigravity": {"google-ai-pro", "google-ai-ultra"}, "cursor": {"plus", "premium", "super", "ultra", "pro", "pro-plus"}} {
		for _, id := range plans {
			if !validPlanID(provider, id) {
				t.Errorf("%s plan %s missing from catalogue", provider, id)
			}
		}
	}
	want := map[string]float64{"codex/free": 0, "codex/plus": 20, "codex/pro": 200, "codex/team": 30, "codex/business": 30, "codex/enterprise": 60, "claude/pro": 20, "claude/max5x": 100, "claude/max20x": 200, "xai/supergrok": 30, "xai/supergrok-heavy": 300, "antigravity/google-ai-pro": 20, "antigravity/google-ai-ultra": 250, "zai/lite": 18, "zai/pro": 80, "zai/max": 168,
		"cursor/plus": 8, "cursor/premium": 30, "cursor/super": 100, "cursor/ultra": 200, "cursor/pro": 20, "cursor/pro-plus": 60}
	for key, usd := range want {
		parts := strings.SplitN(key, "/", 2)
		if plan, ok := catalogPlan(parts[0], parts[1]); !ok || plan.MonthlyUSD != usd {
			t.Errorf("%s = %+v, want %v", key, plan, usd)
		}
	}
}

func TestCatalogOnlyOffersWorkingSignIns(t *testing.T) {
	working := map[string]bool{"codex": true, "claude": true, "antigravity": true, "xai": true, "grok-build": true, "kimi": true, "devin": true, "meta": true, "zai": true, "openrouter": true, "cursor": true, "nous-portal": true}
	for _, p := range providerCatalog {
		hasSub := p.Subscription != nil
		if hasSub != working[p.ID] {
			t.Errorf("%s subscription offered=%v, engine handler=%v", p.ID, hasSub, working[p.ID])
		}
		if (p.APIKey != nil) != strings.Contains(strings.Join(p.Methods, ","), "apiKey") {
			t.Errorf("%s methods disagree with apiKey info", p.ID)
		}
	}
	for _, id := range []string{"gemini-cli", "qwen", "iflow"} {
		if _, ok := catalogProvider(id); ok {
			t.Errorf("%s has no engine sign-in and must not be listed", id)
		}
	}
	if catalogProviderID("openai-compatible-openrouter", "openrouter-2") != "openrouter" || catalogProviderID("x", "custom-office") != "custom" || catalogProviderID("anthropic", "") != "claude" {
		t.Fatal("engine provider mapping")
	}
	compat := func(name, base string) *coreauth.Auth {
		return &coreauth.Auth{Provider: "openai-compatible-" + name, Attributes: map[string]string{"compat_name": name, "base_url": base}}
	}
	for _, tc := range []struct {
		auth       *coreauth.Auth
		kind, want string
	}{
		// A Hugging Face key imported before it had its own catalogue entry.
		{compat("custom-90ab", "https://router.huggingface.co/v1/"), "imported", "huggingface"},
		{compat("nvidia", "https://integrate.api.nvidia.com/v1"), "apiKey", "nvidia"},
		{compat("marineris-2", "http://127.0.0.1:18801/v1"), "apiKey", "marineris"},
		// An editable address never identifies a provider by URL alone.
		{compat("custom-gateway", "http://192.168.50.50:8801/v1"), "apiKey", "custom"},
		{compat("custom-office", "http://10.0.0.5/v1"), "apiKey", "custom"},
		{&coreauth.Auth{Provider: "neopi", Attributes: map[string]string{"neopi_provider": "vercel-ai-gateway"}}, "neopi", "vercel-ai-gateway"},
	} {
		if got := accessProviderID(tc.auth, tc.kind); got != tc.want {
			t.Errorf("accessProviderID(%v) = %q, want %q", tc.auth.Attributes, got, tc.want)
		}
	}
}

func TestDetectCredentialFileFormats(t *testing.T) {
	id := testJWT(t, map[string]any{"email": "dev@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus", "chatgpt_account_id": "acct-1"}})
	codexCLI := `{"OPENAI_API_KEY":null,"tokens":{"id_token":"` + id + `","access_token":"at-secret","refresh_token":"rt-secret","account_id":"acct-1"},"last_refresh":"2026-11-01T00:00:00Z"}`
	file, err := detectCredentialFile("auth.json", []byte(codexCLI))
	if err != nil {
		t.Fatal(err)
	}
	var native map[string]any
	if err = json.Unmarshal(file.Data, &native); err != nil {
		t.Fatal(err)
	}
	if file.Format != "codex-cli" || file.Provider != "codex" || native["type"] != "codex" || native["refresh_token"] != "rt-secret" || native["email"] != "dev@example.com" || native["plan_type"] != "plus" || !strings.HasSuffix(file.Name, "-dev@example.com-plus.json") {
		t.Fatalf("codex cli conversion: %+v %v", file, native)
	}

	claudeCode := `{"claudeAiOauth":{"accessToken":"sk-ant-oat-secret","refreshToken":"sk-ant-ort-secret","expiresAt":1893456000000,"scopes":["user:inference"],"subscriptionType":"max","rateLimitTier":"default_claude_max_5x"}}`
	file, err = detectCredentialFile(".credentials.json", []byte(claudeCode))
	if err != nil {
		t.Fatal(err)
	}
	native = nil
	if err = json.Unmarshal(file.Data, &native); err != nil {
		t.Fatal(err)
	}
	if file.Provider != "claude" || native["type"] != "claude" || native["subscription_type"] != "max" || native["rate_limit_tier"] != "default_claude_max_5x" || native["expired"] != "2030-01-01T00:00:00Z" || !strings.HasPrefix(file.Name, "claude-code-") {
		t.Fatalf("claude code conversion: %+v %v", file, native)
	}

	file, err = detectCredentialFile("../../evil name.json", []byte(`{"type":"antigravity","access_token":"x"}`))
	if err != nil || file.Format != "native" || file.Provider != "antigravity" || strings.ContainsAny(file.Name, "/ ") {
		t.Fatalf("native file: %+v %v", file, err)
	}
	file, err = detectCredentialFile("auth.json", []byte(`{"OPENAI_API_KEY":"sk-proj-only"}`))
	if err != nil || file.APIKey != "sk-proj-only" || file.Provider != "codex" {
		t.Fatalf("api-key-only codex file: %+v %v", file, err)
	}
	for _, bad := range []string{`[1,2]`, `{"hello":"world"}`, `not json`} {
		_, err = detectCredentialFile("x.json", []byte(bad))
		var accessError *AccessError
		if !errors.As(err, &accessError) || accessError.Status != 400 || !strings.Contains(accessError.Message, "Codex CLI") {
			t.Errorf("%s: want explained 400, got %v", bad, err)
		}
	}
}
