package executor

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

const neoPiResponsesTestSSE = "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"status\":\"in_progress\",\"output\":[]}}\n\n" +
	"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"Hello from Muse.\",\"annotations\":[]}]}}\n\n" +
	"event: response.subscription_usage\ndata: {\"type\":\"response.subscription_usage\",\"subscription\":{\"used_percent\":3}}\n\n" +
	"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"output\":[{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"Hello from Muse.\",\"annotations\":[]}]}],\"usage\":{\"input_tokens\":3,\"output_tokens\":4,\"total_tokens\":7}}}\n\n"

type neoPiResponsesRecorded struct {
	mu      sync.Mutex
	path    string
	headers http.Header
	body    string
}

func neoPiResponsesServer(t *testing.T) (*httptest.Server, *neoPiResponsesRecorded) {
	t.Helper()
	recorded := &neoPiResponsesRecorded{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		recorded.mu.Lock()
		recorded.path, recorded.headers, recorded.body = r.URL.Path, r.Header.Clone(), string(body)
		recorded.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, neoPiResponsesTestSSE)
	}))
	t.Cleanup(server.Close)
	return server, recorded
}

// A Meta Model API key is served as NeoPi's `meta` provider: its Responses
// body, the key as bearer, and nothing of Muse Code's transport.
func TestNeoPiResponsesExecutorServesMetaKeyAsNeoPi(t *testing.T) {
	server, recorded := neoPiResponsesServer(t)
	exec := NewNeoPiResponsesExecutor("meta", &config.Config{})
	auth := &cliproxyauth.Auth{ID: "meta-key", Provider: "meta", Attributes: map[string]string{"api_key": "LLM|payg", "base_url": server.URL, "auth_kind": "apikey"}}
	req := cliproxyexecutor.Request{Model: "muse-spark-1.3", Payload: []byte(`{"model":"muse-spark-1.3","input":"hi","reasoning":{"effort":"high"},"prompt_cache_key":"cache-1"}`)}
	resp, err := exec.Execute(t.Context(), auth, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(resp.Payload), "Hello from Muse.") || strings.Contains(string(resp.Payload), "subscription_usage") {
		t.Fatalf("payload %s", resp.Payload)
	}
	if recorded.path != "/responses" {
		t.Errorf("path %s", recorded.path)
	}
	if got := recorded.headers.Get("Authorization"); got != "Bearer LLM|payg" {
		t.Errorf("Authorization %q", got)
	}
	if got := recorded.headers.Get("User-Agent"); got != "Bun/1.3.14" {
		t.Errorf("User-Agent %q", got)
	}
	if recorded.headers.Get("x-api-version") != "" {
		t.Errorf("Model API requests must not carry Muse Code's x-api-version")
	}
	want := `{"model":"muse-spark-1.3","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"stream":true,"prompt_cache_key":"cache-1","store":false,"include":["reasoning.encrypted_content"],"reasoning":{"effort":"high","summary":"auto"}}`
	if recorded.body != want {
		t.Errorf("body\ngot  %s\nwant %s", recorded.body, want)
	}
}

// A Meta sign-in is NeoPi's `muse-code`: the minted key as bearer and x-api-version.
func TestNeoPiResponsesExecutorServesMuseSignInAsNeoPi(t *testing.T) {
	server, recorded := neoPiResponsesServer(t)
	exec := NewNeoPiResponsesExecutor("meta", &config.Config{})
	auth := &cliproxyauth.Auth{ID: "meta-oauth.json", Provider: "meta",
		Attributes: map[string]string{"api_key": "LLM|minted", "dca_token": "dca:account", "base_url": server.URL, "auth_kind": "oauth"},
		Metadata:   map[string]any{"type": "meta", "auth_kind": "oauth", "api_key": "LLM|minted", "dca_token": "dca:account"}}
	req := cliproxyexecutor.Request{Model: "muse-wonder-full-1.0", Payload: []byte(`{"model":"muse-wonder-full-1.0","input":"hi","reasoning":{"effort":"max"}}`)}
	result, err := exec.ExecuteStream(t.Context(), auth, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
	if err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
		output.Write(chunk.Payload)
	}
	if !strings.Contains(output.String(), "Hello from Muse.") || strings.Contains(output.String(), "subscription_usage") {
		t.Fatalf("stream %s", output.String())
	}
	if got := recorded.headers.Get("Authorization"); got != "Bearer LLM|minted" {
		t.Errorf("Authorization %q", got)
	}
	if got := recorded.headers.Get("x-api-version"); got != "1.0.0" {
		t.Errorf("x-api-version %q", got)
	}
	if !strings.Contains(recorded.body, `"model":"muse-wonder-full-1.0"`) || !strings.Contains(recorded.body, `"reasoning":{"effort":"max","summary":"auto"}`) {
		t.Errorf("body %s", recorded.body)
	}
}

func TestNeoPiResponsesExecutorRefusesModelOutsideNeoPiCatalog(t *testing.T) {
	exec := NewNeoPiResponsesExecutor("meta", &config.Config{})
	auth := &cliproxyauth.Auth{ID: "meta-key", Provider: "meta", Attributes: map[string]string{"api_key": "LLM|payg", "base_url": "http://127.0.0.1:1", "auth_kind": "apikey"}}
	req := cliproxyexecutor.Request{Model: "muse-image-1", Payload: []byte(`{"model":"muse-image-1","input":"hi"}`)}
	if _, err := exec.Execute(t.Context(), auth, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}); err == nil || !strings.Contains(err.Error(), "NeoPi's catalog") {
		t.Fatalf("expected a catalog refusal, got %v", err)
	}
}
