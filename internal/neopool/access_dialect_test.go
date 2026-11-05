package neopool

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// An OpenCode-style model list names each model's API; NeoProxy routes by it,
// keeps advertised base URLs only on the gateway's own host and leaves
// undescribed ids on chat completions.
func TestModelRoutesFollowTheAdvertisedAPI(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/zen/go/v1/models" || r.Header.Get("Authorization") != "Bearer test-key" {
			http.Error(w, "unexpected", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[
			{"id":"glm-5.3","api":{"chat-completions":{"base_url":"` + server.URL + `/zen/go/v1"}}},
			{"id":"qwen3.8-flash","api":{"messages":{"base_url":"` + server.URL + `/inference/go/anthropic/v1"}}},
			{"id":"grok-4.7","api":{"responses":{"base_url":"https://elsewhere.example/v1"}}},
			{"id":"gemini-4-argon","api":{"generate-content":{"base_url":"` + server.URL + `/inference/go/google/v1beta"}}},
			{"id":"jev-1.13","api":{"typesafe":{"base_url":"` + server.URL + `/zen"}}},
			{"id":"claude-haiku-5-5"},
			{"id":"kimi-k3","api":"not-an-object"}
		]}`))
	}))
	defer server.Close()

	info := ProviderInfo{ID: "advertising-gateway", Name: "Advertising Gateway", APIKey: &ProviderAPIKey{Style: "openai"}}
	base := server.URL + "/zen/go/v1"
	got, models, routes, err := validateCatalogKey(context.Background(), info, base, "", "test-key")
	if err != nil || got != base {
		t.Fatalf("validateCatalogKey = %q, %v", got, err)
	}
	wantModels := []string{"claude-haiku-5-5", "gemini-4-argon", "glm-5.3", "grok-4.7", "kimi-k3", "qwen3.8-flash"}
	if len(models) != len(wantModels) {
		t.Fatalf("models = %v, want %v", models, wantModels)
	}
	for i := range wantModels {
		if models[i] != wantModels[i] {
			t.Fatalf("models = %v, want %v", models, wantModels)
		}
	}
	want := map[string]modelRoute{
		"qwen3.8-flash":  {dialect: "messages", baseURL: server.URL + "/inference/go/anthropic/v1"},
		"grok-4.7":       {dialect: "responses"},
		"gemini-4-argon": {dialect: "generate-content", baseURL: server.URL + "/inference/go/google/v1beta"},
	}
	if len(routes) != len(want) {
		t.Fatalf("routes = %+v, want %+v", routes, want)
	}
	for id, route := range want {
		if routes[id] != route {
			t.Errorf("route %s = %+v, want %+v", id, routes[id], route)
		}
	}
}

func TestResponsesOnlyProvidersDeclareTheirDialect(t *testing.T) {
	for _, id := range []string{"sakana", "abliteration"} {
		if info, ok := catalogProvider(id); !ok || info.dialect != "responses" || info.engine != "" {
			t.Errorf("%s = %+v", id, info)
		}
	}
}
