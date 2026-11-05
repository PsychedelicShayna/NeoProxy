package cliproxy

import (
	"sort"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// seedAntigravityDiscovery stores a fresh roster for id in the shared cache,
// as a completed discovery fetch would, so registration reads it without a
// network call.
func seedAntigravityDiscovery(t *testing.T, id string, models []*ModelInfo) {
	t.Helper()
	antigravityDiscovery.mu.Lock()
	antigravityDiscovery.entries[id] = &neoPiDiscoveryEntry{models: models, fetchedAt: time.Now()}
	antigravityDiscovery.mu.Unlock()
	t.Cleanup(func() { forgetNeoPiDiscovery(id) })
}

func registeredModelIDs(auth *coreauth.Auth) []string {
	var ids []string
	for _, model := range GlobalModelRegistry().GetModelsForClient(auth.ID) {
		if model != nil {
			ids = append(ids, model.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

// TestRegisterModelsForAuth_AntigravityServesDiscoveredRoster requires an
// account to serve exactly the roster NeoPi's discovery resolved for it: the
// bundled seed rows its fetchAvailableModels answer omits are not registered.
func TestRegisterModelsForAuth_AntigravityServesDiscoveredRoster(t *testing.T) {
	const payload = `{"models":{` +
		`"gemini-3.1-pro-low":{"displayName":"Gemini 3.1 Pro (Low)","supportsImages":true,"supportsThinking":true,"thinkingLevel":1,"maxTokens":1048576,"maxOutputTokens":65535,"model":"MODEL_PLACEHOLDER_M36"},` +
		`"gemini-3.1-pro-high":{"displayName":"Gemini 3.1 Pro (High)","supportsImages":true,"supportsThinking":true,"thinkingLevel":3,"maxTokens":1048576,"maxOutputTokens":65535,"model":"MODEL_PLACEHOLDER_M37"}}}`
	discovered, err := registry.AntigravityDiscoveredModels([]byte(payload), "https://daily-cloudcode-pa.googleapis.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(discovered) == 0 || len(discovered) >= len(registry.AntigravityBundledModels()) {
		t.Fatalf("fixture roster has %d models against %d bundled: it cannot show the seed being replaced", len(discovered), len(registry.AntigravityBundledModels()))
	}
	want := make([]string, 0, len(discovered))
	for _, model := range discovered {
		want = append(want, model.ID)
	}
	sort.Strings(want)

	service := &Service{cfg: &config.Config{}}
	auth := &coreauth.Auth{ID: t.Name(), Provider: "antigravity", Status: coreauth.StatusActive, Attributes: map[string]string{"auth_kind": "oauth"}}
	seedAntigravityDiscovery(t, auth.ID, discovered)
	GlobalModelRegistry().UnregisterClient(auth.ID)
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })

	service.registerModelsForAuth(t.Context(), auth)

	got := registeredModelIDs(auth)
	if len(got) != len(want) {
		t.Fatalf("registered %v, want NeoPi's discovered roster %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("registered %v, want NeoPi's discovered roster %v", got, want)
		}
	}
	for _, model := range GlobalModelRegistry().GetModelsForClient(auth.ID) {
		if model.NeoPi == nil || model.NeoPi.API != "google-gemini-cli" {
			t.Fatalf("%s carries no NeoPi google-gemini-cli record: %+v", model.ID, model.NeoPi)
		}
	}
}

// TestRegisterModelsForAuth_AntigravityServesBundledSeedUntilDiscovered
// requires the bundled seed while the account's roster is unknown, as NeoPi
// serves it until discovery answers.
func TestRegisterModelsForAuth_AntigravityServesBundledSeedUntilDiscovered(t *testing.T) {
	// Discovery runs in the background against an unreachable endpoint; the
	// pinned version keeps it from asking the update manifest.
	t.Setenv("PI_AI_ANTIGRAVITY_VERSION", "2.26.0")
	service := &Service{cfg: &config.Config{}}
	auth := &coreauth.Auth{ID: t.Name(), Provider: "antigravity", Status: coreauth.StatusActive,
		Attributes: map[string]string{"auth_kind": "oauth", "base_url": "http://127.0.0.1:1"},
		Metadata:   map[string]any{"access_token": "token"}}
	forgetNeoPiDiscovery(auth.ID)
	t.Cleanup(func() { forgetNeoPiDiscovery(auth.ID) })
	GlobalModelRegistry().UnregisterClient(auth.ID)
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })

	service.registerModelsForAuth(t.Context(), auth)

	want := make([]string, 0)
	for _, model := range registry.AntigravityBundledModels() {
		want = append(want, model.ID)
	}
	sort.Strings(want)
	got := registeredModelIDs(auth)
	if len(got) != len(want) {
		t.Fatalf("registered %d models, want the %d bundled ones", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("registered %v, want bundled %v", got, want)
		}
	}
}
