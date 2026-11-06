package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func TestModelsMultiAgentClientConfigAndReload(t *testing.T) {
	for _, source := range []string{"local", "home"} {
		t.Run(source, func(t *testing.T) {
			const modelID = "config-multi-agent-synthetic"
			const clientID = "config-multi-agent-models"
			modelRegistry := registry.GetGlobalRegistry()
			modelRegistry.RegisterClient(clientID, "codex", []*registry.ModelInfo{{ID: modelID}})
			t.Cleanup(func() { modelRegistry.UnregisterClient(clientID) })
			server := newTestServer(t)
			engine := server.engine
			if source == "home" {
				server.cfg.Home.Enabled = true
				client := newHomeCatalogClient(t, `{"codex":[{"id":"config-multi-agent-synthetic"}]}`)
				previousHome := home.Current()
				home.SetCurrent(client)
				t.Cleanup(func() { home.SetCurrent(previousHome) })
				engine = gin.New()
				engine.GET("/v1/models", server.unifiedModelsHandler(nil, nil))
			}
			for _, tc := range []struct {
				name string
				raw  string
				want bool
			}{
				{"omitted", "[client]\n", false},
				{"enabled", "[client.codex]\noptimize-multi-agent-v2 = true\n", true},
				{"disabled", "[client.codex]\noptimize-multi-agent-v2 = false\n", false},
				{"legacy enabled", "[providers.codex]\noptimize-multi-agent-v2 = true\n", true},
				{"new false wins", "[client.codex]\noptimize-multi-agent-v2 = false\n[oauth.providers.codex]\noptimize-multi-agent-v2 = true\n", false},
				{"oauth alias enabled", "[oauth.providers.codex]\noptimize-multi-agent-v2 = true\n", true},
				{"removed", "[client]\n", false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					parsed, errParse := config.ParseConfigBytes([]byte(tc.raw))
					if errParse != nil {
						t.Fatalf("parse client config: %v", errParse)
					}
					updatedCfg := *server.cfg
					updatedCfg.Client.Codex.OptimizeMultiAgentV2 = parsed.Client.Codex.OptimizeMultiAgentV2
					server.UpdateClients(&updatedCfg)
					if server.handlers.Cfg.Client.Codex.OptimizeMultiAgentV2 != tc.want {
						t.Fatal("reload did not publish client setting to API handlers")
					}
					request := httptest.NewRequest(http.MethodGet, "/v1/models?client_version=cpa", nil)
					request.Header.Set("Authorization", "Bearer test-key")
					recorder := httptest.NewRecorder()
					engine.ServeHTTP(recorder, request)
					if recorder.Code != http.StatusOK {
						t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
					}
					var response struct {
						Models []map[string]any `json:"models"`
					}
					if errUnmarshal := json.Unmarshal(recorder.Body.Bytes(), &response); errUnmarshal != nil {
						t.Fatalf("decode catalog: %v", errUnmarshal)
					}
					for _, entry := range response.Models {
						if entry["slug"] != modelID {
							continue
						}
						var want any
						if tc.want {
							want = "v2"
						}
						if value, present := entry["multi_agent_version"]; !present || value != want {
							t.Fatalf("multi_agent_version = %#v (present %t), want %#v", value, present, want)
						}
						return
					}
					t.Fatalf("missing model %s", modelID)
				})
			}
		})
	}
}
