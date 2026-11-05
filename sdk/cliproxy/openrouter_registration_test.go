package cliproxy

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	internalregistry "github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Startup and batched registration resolve accounts through the static
// openai-compatibility cache. An OpenRouter entry configured without models
// must still serve NeoPi's OpenRouter roster there, as NeoPi's manager seeds
// it with the bundled roster before discovery answers.
func TestOpenRouterBatchRegistrationServesNeoPiRoster(t *testing.T) {
	cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{
		Name:          "openrouter",
		BaseURL:       "https://openrouter.ai/api/v1",
		APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "sk-or-v1-batch"}},
	}}}
	auths, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{Config: cfg, Now: time.Now(), IDGenerator: synthesizer.NewStableIDGenerator()})
	if err != nil || len(auths) != 1 {
		t.Fatalf("synthesized auths = %+v, %v", auths, err)
	}
	auth := auths[0]
	// Hold the background listing: the test checks the roster served before
	// discovery answers, without reaching openrouter.ai.
	openRouterDiscovery.mu.Lock()
	openRouterDiscovery.entries[auth.ID] = &neoPiDiscoveryEntry{inFlight: true}
	openRouterDiscovery.mu.Unlock()
	t.Cleanup(func() {
		openRouterDiscovery.mu.Lock()
		delete(openRouterDiscovery.entries, auth.ID)
		openRouterDiscovery.mu.Unlock()
	})

	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	service := &Service{cfg: cfg, coreManager: manager}
	ctx := context.Background()
	if _, err = manager.Register(ctx, auth); err != nil {
		t.Fatal(err)
	}
	reg := internalregistry.GetGlobalRegistry()
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	service.registerModelsForAuthWithCache(ctx, auth, service.newOpenAICompatibilityRegistrationCache())

	bundled := map[string]bool{}
	for _, model := range internalregistry.OpenRouterBundledModels("openrouter") {
		bundled[model.ID] = true
	}
	served := reg.GetModelsForClient(auth.ID)
	if len(served) == 0 || len(served) != len(bundled) {
		t.Fatalf("served %d models, want NeoPi's %d bundled OpenRouter rows", len(served), len(bundled))
	}
	for _, model := range served {
		if !bundled[model.ID] {
			t.Fatalf("served %q, not in NeoPi's OpenRouter roster", model.ID)
		}
	}
}
