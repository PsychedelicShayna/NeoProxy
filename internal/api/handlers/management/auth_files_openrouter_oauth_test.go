package management

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	openrouterauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/openrouter"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type fakeOpenRouterOAuthService struct {
	exchangeErr, validateErr error
	key                      string
	gotCode, gotVerifier     string
	validated                string
}

func (f *fakeOpenRouterOAuthService) AuthorizationURL(challenge string) string {
	return "https://openrouter.ai/auth?code_challenge=" + challenge
}
func (f *fakeOpenRouterOAuthService) ExchangeCode(_ context.Context, code, verifier string) (string, error) {
	f.gotCode, f.gotVerifier = code, verifier
	return f.key, f.exchangeErr
}
func (f *fakeOpenRouterOAuthService) ValidateKey(_ context.Context, key string) error {
	f.validated = key
	return f.validateErr
}

func runOpenRouterSignIn(t *testing.T, h *Handler, authDir, state, payload string, svc openRouterOAuthService) (string, bool) {
	t.Helper()
	RegisterOAuthSession(state, "openrouter")
	t.Cleanup(func() { CompleteOAuthSession(state) })
	if err := os.WriteFile(filepath.Join(authDir, ".oauth-openrouter-"+state+".oauth"), []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := coreauth.WithAccountLoginOwner(context.Background(), "user-1")
	h.completeOpenRouterOAuth(ctx, authDir, state, "verifier-1", svc)
	_, status, ok := GetOAuthSession(state)
	return status, ok
}

func TestCompleteOpenRouterOAuthExchangesCodeIntoKey(t *testing.T) {
	h, cfgPath, authDir, bound := newZaiTestHandler(t, "port: 8317\n")
	svc := &fakeOpenRouterOAuthService{key: "sk-or-v1-fixture"}
	status, _ := runOpenRouterSignIn(t, h, authDir, "or-success", `{"state":"or-success","code":"the-code"}`, svc)
	if status != "" {
		t.Fatalf("session status = %q, want completed", status)
	}
	if svc.gotCode != "the-code" || svc.gotVerifier != "verifier-1" {
		t.Fatalf("exchange got code %q verifier %q", svc.gotCode, svc.gotVerifier)
	}
	if len(*bound) != 1 || (*bound)[0].Attributes["compat_name"] != "openrouter" || (*bound)[0].Attributes["base_url"] != openrouterauth.BaseURL {
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
	if entry.Name != "openrouter" || entry.BaseURL != openrouterauth.BaseURL || len(entry.APIKeyEntries) != 1 || entry.APIKeyEntries[0].APIKey != "sk-or-v1-fixture" || len(entry.Models) != 0 {
		t.Fatalf("openrouter entry = %+v", entry)
	}
	if entries, _ := os.ReadDir(authDir); len(entries) != 0 {
		t.Fatalf("callback file left behind: %v", entries)
	}
}

func TestCompleteOpenRouterOAuthPastedKeyIsValidatedNotExchanged(t *testing.T) {
	h, _, authDir, bound := newZaiTestHandler(t, "port: 8317\n")
	svc := &fakeOpenRouterOAuthService{}
	status, _ := runOpenRouterSignIn(t, h, authDir, "or-paste", `{"state":"or-paste","code":"sk-or-v1-pasted"}`, svc)
	if status != "" || svc.validated != "sk-or-v1-pasted" || svc.gotCode != "" {
		t.Fatalf("status %q validated %q exchanged %q", status, svc.validated, svc.gotCode)
	}
	if len(*bound) != 1 {
		t.Fatalf("bound credentials = %+v", *bound)
	}
}

func TestCompleteOpenRouterOAuthFailures(t *testing.T) {
	for _, tc := range []struct {
		name, payload string
		svc           *fakeOpenRouterOAuthService
		want          string
	}{
		{"denied", `{"state":"or-fail","error":"access_denied"}`, &fakeOpenRouterOAuthService{}, "OpenRouter authorization denied"},
		{"state mismatch", `{"state":"other","code":"c"}`, &fakeOpenRouterOAuthService{}, "State code error"},
		{"missing code", `{"state":"or-fail"}`, &fakeOpenRouterOAuthService{}, "Missing authorization code"},
		{"exchange error", `{"state":"or-fail","code":"c"}`, &fakeOpenRouterOAuthService{exchangeErr: errors.New("secret-upstream")}, "Failed to exchange authorization code"},
		{"rejected key", `{"state":"or-fail","code":"sk-or-v1-bad"}`, &fakeOpenRouterOAuthService{validateErr: errors.New("secret-upstream")}, "rejected this key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, cfgPath, authDir, bound := newZaiTestHandler(t, "port: 8317\n")
			status, ok := runOpenRouterSignIn(t, h, authDir, "or-fail", tc.payload, tc.svc)
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
