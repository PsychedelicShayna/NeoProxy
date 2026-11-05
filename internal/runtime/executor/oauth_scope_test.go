package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestV8SharedUpstreamRequestSettingsAffectBothAuthKinds(t *testing.T) {
	// xAI requests are shaped by the NeoPi port, which applies none of these
	// client or upstream rewrites (orphan delegation, x_search injection).
	for _, provider := range []string{"codex"} {
		for _, kind := range []string{cliproxyauth.AuthKindAPIKey, cliproxyauth.AuthKindOAuth} {
			for _, stream := range []bool{false, true} {
				if provider == "codex" && kind == cliproxyauth.AuthKindOAuth {
					// Codex OAuth requests are shaped by the NeoPi provider port, which
					// applies none of NeoProxy's client or upstream body rewrites.
					continue
				}
				t.Run(fmt.Sprintf("%s/%s/stream=%t", provider, kind, stream), func(t *testing.T) {
					captured := make(chan []byte, 1)
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						body, err := io.ReadAll(r.Body)
						if err != nil {
							t.Error(err)
						}
						captured <- body
						w.WriteHeader(http.StatusBadRequest)
						_, _ = w.Write([]byte(`{"error":{"message":"captured request"}}`))
					}))
					defer server.Close()
					cfg, err := config.ParseConfigBytes([]byte(`proxy-url: direct
disable-image-generation: true
client: {codex: {optimize-multi-agent-v2: true}}
upstream:
  codex: {orphan-delegation-compatibility: true}
  xai: {inject-x-search: true}
`))
					if err != nil {
						t.Fatal(err)
					}
					auth := &cliproxyauth.Auth{ID: t.Name(), Provider: provider, Status: cliproxyauth.StatusActive, ProxyURL: "direct", Attributes: map[string]string{"auth_kind": kind, "base_url": server.URL}}
					if kind == cliproxyauth.AuthKindAPIKey {
						auth.Attributes["api_key"] = "test-key"
					} else {
						auth.Metadata = map[string]any{"access_token": "test-token"}
					}
					var executor cliproxyauth.ProviderExecutor = NewCodexExecutor(cfg)
					manager := cliproxyauth.NewManager(nil, nil, nil)
					manager.SetConfig(cfg)
					manager.RegisterExecutor(executor)
					if _, err = manager.Register(context.Background(), auth); err != nil {
						t.Fatal(err)
					}
					registry.GetGlobalRegistry().RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: "gpt-5.4"}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
					req := cliproxyexecutor.Request{Model: "gpt-5.4", Payload: []byte(`{
  "input":[{"type":"function_call_output","name":"create_thread","namespace":"codex_app","output":"<codex_delegation>task</codex_delegation>"}],
  "tools":[{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn_agent","description":"Spawns an agent.","parameters":{"type":"object","properties":{"message":{"type":"string","encrypted":true}}}}]}]
}`)}
					opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Headers: http.Header{"User-Agent": {"codex_cli_rs/0.144.1"}, "X-Openai-Subagent": {"collab_spawn"}}}
					if stream {
						_, err = manager.ExecuteStream(context.Background(), []string{provider}, req, opts)
					} else {
						_, err = manager.Execute(context.Background(), []string{provider}, req, opts)
					}
					if err == nil {
						t.Fatal("expected the test upstream's rejection")
					}
					var body []byte
					select {
					case body = <-captured:
					default:
						t.Fatalf("request did not reach upstream: %v", err)
					}
					if got := gjson.GetBytes(body, "input.0.type").String(); got != "message" {
						t.Fatalf("shared orphan delegation type = %q; body=%s", got, body)
					}
					if got := gjson.GetBytes(body, "tools.0.name").String(); got != "collaboration-optimize" {
						t.Fatalf("client collaboration namespace = %q, want collaboration-optimize", got)
					}
					if !cfg.Client.Codex.OptimizeMultiAgentV2 || !cfg.Codex.OrphanDelegationCompatibility || !cfg.XAI.InjectXSearch {
						t.Fatal("request changed shared configuration")
					}
				})
			}
		}
	}
}

func TestV8ScopedExecutorsPreserveWebsocketStores(t *testing.T) {
	cfg, err := config.ParseConfigBytes([]byte("upstream: {codex: {response-steering: true}, xai: {inject-x-search: true}}\noauth: {providers: {codex: {live-media-relay: {max-sessions: 12}}}}"))
	if err != nil {
		t.Fatal(err)
	}
	codex := NewCodexAutoExecutor(cfg)
	boundCodex := codex.ForAPIKey().(*CodexAutoExecutor)
	if boundCodex.wsExec.store != codex.wsExec.store || !boundCodex.httpExec.cfg.Codex.ResponseSteering || !boundCodex.wsExec.cfg.Codex.ResponseSteering || boundCodex.httpExec.cfg.Codex.LiveMediaRelay.MaxSessions != 0 {
		t.Fatal("scoped Codex executor lost sessions or inherited OAuth settings")
	}
	if codex.httpExec.cfg != cfg || codex.wsExec.cfg != cfg || !cfg.Codex.ResponseSteering {
		t.Fatal("scoping changed the registered executors")
	}
}

// An OpenRouter key selected by the manager is scoped with ForAPIKey; the
// scoped executor must still send NeoPi's Responses request, not the
// promoted Anthropic half's /v1/messages.
func TestOpenRouterAPIKeySelectionKeepsResponsesRouting(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			paths := make(chan string, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select {
				case paths <- r.URL.Path:
				default:
				}
				if r.URL.Path != "/responses" {
					http.NotFound(w, r)
					return
				}
				writeOpenRouterResponses(w, "routed")
			}))
			defer server.Close()
			cfg := &config.Config{}
			auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "openrouter", Status: cliproxyauth.StatusActive,
				Attributes: map[string]string{"auth_kind": cliproxyauth.AuthKindAPIKey, "api_key": "sk-or-v1-test", "base_url": server.URL}}
			manager := cliproxyauth.NewManager(nil, nil, nil)
			manager.SetConfig(cfg)
			manager.RegisterExecutor(NewOpenRouterExecutor("openrouter", cfg))
			if _, err := manager.Register(context.Background(), auth); err != nil {
				t.Fatal(err)
			}
			registry.GetGlobalRegistry().RegisterClient(auth.ID, "openrouter", []*registry.ModelInfo{{ID: openRouterTestModel}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
			req, opts := openRouterTestRequest()
			opts.Stream = stream
			var errTurn error
			if stream {
				result, err := manager.ExecuteStream(context.Background(), []string{"openrouter"}, req, opts)
				errTurn = err
				if err == nil {
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							errTurn = chunk.Err
						}
					}
				}
			} else {
				_, errTurn = manager.Execute(context.Background(), []string{"openrouter"}, req, opts)
			}
			select {
			case path := <-paths:
				if path != "/responses" {
					t.Fatalf("upstream path = %q, want /responses", path)
				}
			default:
				t.Fatalf("request did not reach upstream: %v", errTurn)
			}
			if errTurn != nil {
				t.Fatal(errTurn)
			}
		})
	}
}
