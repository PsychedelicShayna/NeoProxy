package cliproxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	internalregistry "github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// The dashboard's "paste an API key" flow stores OpenAI-compatible providers as
// openai-compatibility entries via management.CreateAccountAPIKey. This proves
// the engine then lists the entry's models and routes a request upstream.
func TestAccountOpenAICompatibilityKeyListsModelsAndRoutes(t *testing.T) {
	var mu sync.Mutex
	var gotAuth, gotModel, gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &payload)
		mu.Lock()
		gotAuth, gotModel, gotPath = r.Header.Get("Authorization"), payload.Model, r.URL.Path
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","model":"fake-model","choices":[{"index":0,"message":{"role":"assistant","content":"routed"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte("port = 8317\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := coreauth.NewManager(nil, nil, nil)
	handler := management.NewHandler(&config.Config{AuthDir: dir}, cfgPath, manager)
	handler.SetAccountOwnershipHook(func(context.Context, *coreauth.Auth) error { return nil })
	var reloaded *config.Config
	handler.SetConfigReloadHook(func(_ context.Context, cfg *config.Config) { reloaded = cfg })

	ctx := coreauth.WithAccountLoginOwner(context.Background(), "owner")
	record, err := handler.CreateAccountAPIKey(ctx, coreauth.AccountKey{Provider: "openai-compatibility", CompatName: "custom-fake", BaseURL: upstream.URL + "/v1", APIKey: "route-key", Models: []string{"fake-model"}})
	if err != nil {
		t.Fatal(err)
	}
	if reloaded == nil {
		t.Fatal("engine config was not reloaded")
	}
	saved, errRead := os.ReadFile(cfgPath)
	if errRead != nil {
		t.Fatal(errRead)
	}
	persisted, errDecode := config.DecodeConfig(saved, config.ConfigFormatTOML)
	if errDecode != nil {
		t.Fatalf("account creation did not persist native TOML: %v", errDecode)
	}
	if len(persisted.OpenAICompatibility) != 1 || persisted.OpenAICompatibility[0].Name != "custom-fake" {
		t.Fatalf("persisted compatibility config = %+v", persisted.OpenAICompatibility)
	}
	auths, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{Config: reloaded, Now: time.Now(), IDGenerator: synthesizer.NewStableIDGenerator()})
	if err != nil || len(auths) != 1 || auths[0].ID != record.ID {
		t.Fatalf("synthesized auths = %+v, %v", auths, err)
	}
	auth := auths[0]

	service := &Service{cfg: reloaded, coreManager: manager}
	manager.SetConfig(reloaded)
	if _, err = manager.Register(ctx, auth); err != nil {
		t.Fatal(err)
	}
	reg := internalregistry.GetGlobalRegistry()
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	service.registerExecutorForAuth(auth, true)
	service.registerModelsForAuth(ctx, auth)
	listed := false
	for _, m := range reg.GetModelsForClient(auth.ID) {
		if m != nil && m.ID == "fake-model" {
			listed = true
		}
	}
	if !listed {
		t.Fatalf("engine did not list the entry's model: %+v", reg.GetModelsForClient(auth.ID))
	}

	resp, err := manager.Execute(ctx, []string{auth.Provider}, cliproxyexecutor.Request{
		Model:   "fake-model",
		Payload: []byte(`{"model":"fake-model","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if gotAuth != "Bearer route-key" || gotModel != "fake-model" || gotPath != "/v1/chat/completions" || !strings.Contains(string(resp.Payload), "routed") {
		t.Fatalf("upstream saw auth=%q model=%q path=%q; response %s", gotAuth, gotModel, gotPath, resp.Payload)
	}

	// Pausing keeps the key configured but synthesizes a disabled credential.
	manager2Auth, _ := manager.GetByID(auth.ID)
	if manager2Auth == nil {
		t.Fatal("registered auth missing")
	}
	if err = handler.SetAccountDisabled(ctx, auth.ID, true); err != nil {
		t.Fatal(err)
	}
	paused, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{Config: reloaded, Now: time.Now(), IDGenerator: synthesizer.NewStableIDGenerator()})
	if err != nil || len(paused) != 1 || !paused[0].Disabled || paused[0].ID != auth.ID {
		t.Fatalf("paused synthesis = %+v, %v", paused, err)
	}
}
