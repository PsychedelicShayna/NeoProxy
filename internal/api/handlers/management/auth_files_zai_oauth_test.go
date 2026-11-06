package management

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	zaiauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/zai"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type fakeZaiOAuthService struct {
	exchangeErr, mintErr error
	key                  string
}

func (f *fakeZaiOAuthService) AuthorizationURL(state string) string {
	return "https://chat.z.ai/api/oauth/authorize?state=" + state
}
func (f *fakeZaiOAuthService) ExchangeCode(context.Context, string, string) (string, zaiauth.Account, error) {
	return "oauth-token", zaiauth.Account{Email: "zai@example.test"}, f.exchangeErr
}
func (f *fakeZaiOAuthService) MintAPIKey(context.Context, string) (string, error) {
	return f.key, f.mintErr
}

func newZaiTestHandler(t *testing.T, configTOML string) (*Handler, string, string, *[]*coreauth.Auth) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(configTOML), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AuthDir = filepath.Join(dir, "auths")
	if err = os.MkdirAll(cfg.AuthDir, 0o700); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(cfg, cfgPath, nil)
	bound := []*coreauth.Auth{}
	h.SetAccountOwnershipHook(func(_ context.Context, a *coreauth.Auth) error {
		bound = append(bound, a)
		return nil
	})
	return h, cfgPath, cfg.AuthDir, &bound
}

func runZaiSignIn(t *testing.T, h *Handler, authDir, state, payload string, svc zaiOAuthService) (string, bool) {
	t.Helper()
	RegisterOAuthSession(state, "zai")
	t.Cleanup(func() { CompleteOAuthSession(state) })
	if err := os.WriteFile(filepath.Join(authDir, ".oauth-zai-"+state+".oauth"), []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := coreauth.WithAccountLoginOwner(context.Background(), "user-1")
	h.completeZaiOAuth(ctx, authDir, state, svc)
	_, status, ok := GetOAuthSession(state)
	return status, ok
}

func TestCompleteZaiOAuthAddsCodingPlanKey(t *testing.T) {
	h, cfgPath, authDir, bound := newZaiTestHandler(t, "port = 8317\n")
	const state = "zai-success"
	status, _ := runZaiSignIn(t, h, authDir, state, `{"state":"zai-success","code":"the-code"}`, &fakeZaiOAuthService{key: "fixtureid.fixturesecret"})
	if status != "" {
		t.Fatalf("session status = %q, want completed", status)
	}
	if len(*bound) != 1 || (*bound)[0].Attributes["compat_name"] != "zai" || (*bound)[0].Attributes["base_url"] != zaiauth.CodingPlanBaseURL {
		t.Fatalf("bound credentials = %+v", *bound)
	}
	cfg, err := config.LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.OpenAICompatibility) != 1 {
		t.Fatalf("compat entries = %+v", cfg.OpenAICompatibility)
	}
	entry := cfg.OpenAICompatibility[0]
	if entry.Name != "zai" || entry.BaseURL != zaiauth.CodingPlanBaseURL || len(entry.APIKeyEntries) != 1 || entry.APIKeyEntries[0].APIKey != "fixtureid.fixturesecret" || len(entry.Models) != 0 {
		t.Fatalf("zai entry = %+v", entry)
	}
	if entries, _ := os.ReadDir(authDir); len(entries) != 0 {
		t.Fatalf("callback file left behind: %v", entries)
	}
}

func TestCompleteZaiOAuthKeepsPayAsYouGoEntrySeparate(t *testing.T) {
	h, cfgPath, authDir, _ := newZaiTestHandler(t, "port = 8317\n[[openai-compatibility]]\nname = 'zai'\nbase-url = 'https://api.z.ai/api/paas/v4'\n[[openai-compatibility.api-key-entries]]\napi-key = 'oldkey.secret'\n")
	status, _ := runZaiSignIn(t, h, authDir, "zai-second", `{"state":"zai-second","code":"c"}`, &fakeZaiOAuthService{key: "newid.newsecret"})
	if status != "" {
		t.Fatalf("session status = %q", status)
	}
	cfg, err := config.LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.OpenAICompatibility) != 2 || cfg.OpenAICompatibility[1].Name != "zai-2" || cfg.OpenAICompatibility[1].BaseURL != zaiauth.CodingPlanBaseURL {
		t.Fatalf("compat entries = %+v", cfg.OpenAICompatibility)
	}
}

func TestCompleteZaiOAuthFailures(t *testing.T) {
	for _, tc := range []struct {
		name, payload string
		svc           *fakeZaiOAuthService
		want          string
	}{
		{"denied", `{"state":"zai-fail","error":"access_denied"}`, &fakeZaiOAuthService{}, "Z.ai authorization denied"},
		{"state mismatch", `{"state":"other","code":"c"}`, &fakeZaiOAuthService{}, "State code error"},
		{"missing code", `{"state":"zai-fail"}`, &fakeZaiOAuthService{}, "Missing authorization code"},
		{"exchange error", `{"state":"zai-fail","code":"c"}`, &fakeZaiOAuthService{exchangeErr: errors.New("secret-upstream")}, "Failed to exchange authorization code for tokens"},
		{"no plan key", `{"state":"zai-fail","code":"c"}`, &fakeZaiOAuthService{mintErr: errors.New("secret-upstream")}, "GLM Coding Plan"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, cfgPath, authDir, bound := newZaiTestHandler(t, "port = 8317\n")
			status, ok := runZaiSignIn(t, h, authDir, "zai-fail", tc.payload, tc.svc)
			if !ok || !strings.Contains(status, tc.want) || strings.Contains(status, "secret") {
				t.Fatalf("status = %q (exists %v), want %q", status, ok, tc.want)
			}
			if len(*bound) != 0 {
				t.Fatal("credential bound for a failed sign-in")
			}
			raw, _ := os.ReadFile(cfgPath)
			if strings.Contains(string(raw), "openai-compatibility") {
				t.Fatalf("config changed for a failed sign-in:\n%s", raw)
			}
		})
	}
}
