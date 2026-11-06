package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	kiloauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kilo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// runKiloSignIn completes a Kilo device sign-in whose poll answers with
// status and body, and returns the session's final status.
func runKiloSignIn(t *testing.T, h *Handler, state string, status int, body string) (string, bool) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/codes/KILO-1234" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	previous := kiloDeviceAuthBaseURL
	kiloDeviceAuthBaseURL = server.URL
	defer func() { kiloDeviceAuthBaseURL = previous }()

	RegisterOAuthSession(state, kiloauth.Provider)
	t.Cleanup(func() { CompleteOAuthSession(state) })
	ctx := coreauth.WithAccountLoginOwner(context.Background(), "user-1")
	h.completeKiloSignIn(ctx, server.Client(), state, &kiloauth.DeviceCode{Code: "KILO-1234", VerificationURL: "https://app.kilo.ai/device", ExpiresIn: 600})
	_, final, ok := GetOAuthSession(state)
	return final, ok
}

// An approved Kilo sign-in adds the gateway token to the "kilo" entry at the
// gateway, with no stored model list; a second sign-in of the same token is
// refused rather than duplicated.
func TestCompleteKiloSignInAddsGatewayKey(t *testing.T) {
	h, cfgPath, _, bound := newZaiTestHandler(t, "port = 8317\n")
	approved := `{"status":"approved","token":"kilo-gateway-token"}`
	if status, _ := runKiloSignIn(t, h, "kilo-ok", http.StatusOK, approved); status != "" {
		t.Fatalf("session status = %q, want completed", status)
	}
	if len(*bound) != 1 || (*bound)[0].Attributes["compat_name"] != "kilo" || (*bound)[0].Attributes["base_url"] != kiloauth.BaseURL {
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
	if entry.Name != "kilo" || entry.BaseURL != kiloauth.BaseURL || len(entry.APIKeyEntries) != 1 || entry.APIKeyEntries[0].APIKey != "kilo-gateway-token" || len(entry.Models) != 0 {
		t.Fatalf("kilo entry = %+v", entry)
	}

	if status, ok := runKiloSignIn(t, h, "kilo-again", http.StatusOK, approved); !ok || !strings.Contains(status, "already in the pool") {
		t.Fatalf("repeat sign-in status = %q (exists %v)", status, ok)
	}
	if len(*bound) != 1 {
		t.Fatalf("repeat sign-in bound another credential: %+v", *bound)
	}
}

func TestCompleteKiloSignInDenied(t *testing.T) {
	h, cfgPath, _, bound := newZaiTestHandler(t, "port = 8317\n")
	status, ok := runKiloSignIn(t, h, "kilo-denied", http.StatusForbidden, `{}`)
	if !ok || status != "Authorization was denied" {
		t.Fatalf("status = %q (exists %v)", status, ok)
	}
	if len(*bound) != 0 {
		t.Fatal("credential bound for a denied sign-in")
	}
	raw, _ := os.ReadFile(cfgPath)
	if strings.Contains(string(raw), "openai-compatibility") {
		t.Fatalf("config changed for a denied sign-in:\n%s", raw)
	}
}
