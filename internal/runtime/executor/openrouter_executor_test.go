package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

const openRouterTestModel = "anthropic/claude-haiku-4.5"

func openRouterTestAuth(base string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		Provider:   "openai-compatibility",
		Attributes: map[string]string{"api_key": "sk-or-v1-test", "base_url": base},
	}
}

func writeOpenRouterResponses(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, event := range []string{
		`{"type":"response.created","response":{"id":"resp_1","status":"in_progress","output":[]}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","status":"in_progress","content":[]}}`,
		`{"type":"response.content_part.added","item_id":"msg_1","output_index":0,"content_index":0,"part":{"type":"output_text","text":"","annotations":[]}}`,
		`{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"` + text + `"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"` + text + `","annotations":[]}]}}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"` + text + `","annotations":[]}]}],"usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}}`,
	} {
		_, _ = w.Write([]byte("event: " + gjson.Get(event, "type").String() + "\ndata: " + event + "\n\n"))
	}
}

func openRouterTestRequest() (cliproxyexecutor.Request, cliproxyexecutor.Options) {
	payload := []byte(`{"model":"` + openRouterTestModel + `","messages":[{"role":"user","content":"hello"}]}`)
	return cliproxyexecutor.Request{Model: openRouterTestModel, Payload: payload},
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai"), OriginalRequest: payload}
}

// The request leaves as NeoPi's openrouter provider sends it: Responses at
// /responses with its attribution headers and bearer.
func TestOpenRouterExecutorStreamUsesNeoPiResponses(t *testing.T) {
	var mu sync.Mutex
	var gotPath, gotAuth, gotReferer, gotTitle, gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotPath, gotAuth, gotReferer, gotTitle, gotBody = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("HTTP-Referer"), r.Header.Get("X-OpenRouter-Title"), string(raw)
		mu.Unlock()
		writeOpenRouterResponses(w, "streamed")
	}))
	defer server.Close()

	exec := NewOpenRouterExecutor("openrouter", &config.Config{})
	req, opts := openRouterTestRequest()
	opts.Stream = true
	result, err := exec.ExecuteStream(context.Background(), openRouterTestAuth(server.URL), req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	var out strings.Builder
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		out.Write(chunk.Payload)
	}
	mu.Lock()
	defer mu.Unlock()
	if gotPath != "/responses" || gotAuth != "Bearer sk-or-v1-test" {
		t.Fatalf("path %q auth %q", gotPath, gotAuth)
	}
	if gotReferer == "" || gotTitle == "" {
		t.Fatalf("attribution headers missing: referer %q title %q", gotReferer, gotTitle)
	}
	if gjson.Get(gotBody, "model").String() != openRouterTestModel || !gjson.Get(gotBody, "stream").Bool() || gjson.Get(gotBody, "input.0.content.0.text").String() != "hello" {
		t.Fatalf("body = %s", gotBody)
	}
	if !strings.Contains(out.String(), "streamed") {
		t.Fatalf("client stream = %s", out.String())
	}
}

func TestOpenRouterExecutorExecuteTranslatesCompletedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeOpenRouterResponses(w, "complete")
	}))
	defer server.Close()

	exec := NewOpenRouterExecutor("openrouter", &config.Config{})
	req, opts := openRouterTestRequest()
	resp, err := exec.Execute(context.Background(), openRouterTestAuth(server.URL), req, opts)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := gjson.GetBytes(resp.Payload, "choices.0.message.content").String(); got != "complete" {
		t.Fatalf("content = %q in %s", got, resp.Payload)
	}
}

// A routed upstream's own words ride in error.metadata.raw and reach the client.
func TestOpenRouterExecutorSurfacesRoutedUpstreamError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"message":"Provider returned error","code":403,"metadata":{"raw":"upstream says no","provider_name":"Anthropic"}}}`))
	}))
	defer server.Close()

	exec := NewOpenRouterExecutor("openrouter", &config.Config{})
	req, opts := openRouterTestRequest()
	_, err := exec.Execute(context.Background(), openRouterTestAuth(server.URL), req, opts)
	var se statusErr
	if !errors.As(err, &se) || se.StatusCode() != http.StatusForbidden {
		t.Fatalf("error = %#v", err)
	}
	if !strings.Contains(se.Error(), "upstream says no") {
		t.Fatalf("message lacks routed detail: %s", se.Error())
	}
}

// A 429 keeps the provider's Retry-After for the proxy's cooldown.
func TestOpenRouterStatusErrKeepsRetryAfter(t *testing.T) {
	headers := http.Header{"Retry-After": []string{"7"}}
	se := openRouterStatusErr(http.StatusTooManyRequests, headers, []byte(`{"error":{"message":"Provider returned error","code":429}}`))
	if se.RetryAfter() == nil || se.RetryAfter().Seconds() != 7 {
		t.Fatalf("retryAfter = %v", se.RetryAfter())
	}
}

func TestOpenRouterExecutorRejectsModelOutsideNeoPiCatalog(t *testing.T) {
	exec := NewOpenRouterExecutor("openrouter", &config.Config{})
	req, opts := openRouterTestRequest()
	req.Model = "nope/not-a-model"
	if _, err := exec.Execute(context.Background(), openRouterTestAuth("https://openrouter.ai/api/v1"), req, opts); err == nil || !strings.Contains(err.Error(), "not in NeoPi's OpenRouter catalog") {
		t.Fatalf("error = %v", err)
	}
}

// NeoPi retries once without strict tools when the routed provider answers the
// opaque "Provider returned error" (or names strict schemas), then remembers
// the refusal for the model.
func TestOpenRouterExecutorRetriesWithoutStrictTools(t *testing.T) {
	var mu sync.Mutex
	var strictFlags []bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		strict := gjson.GetBytes(raw, "tools.0.strict").Bool()
		mu.Lock()
		strictFlags = append(strictFlags, strict)
		mu.Unlock()
		if strict {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Provider returned error","code":400}}`))
			return
		}
		writeOpenRouterResponses(w, "relaxed")
	}))
	defer server.Close()

	exec := NewOpenRouterExecutor("openrouter", &config.Config{})
	payload := []byte(`{"model":"` + openRouterTestModel + `","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"tools":[{"type":"function","name":"lookup","description":"Look up.","parameters":{"type":"object","properties":{"term":{"type":"string"}},"required":["term"]}}]}`)
	req := cliproxyexecutor.Request{Model: openRouterTestModel, Payload: payload}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response"), OriginalRequest: payload}
	auth := openRouterTestAuth(server.URL)
	for call := 0; call < 2; call++ {
		resp, err := exec.Execute(context.Background(), auth, req, opts)
		if err != nil {
			t.Fatalf("Execute() #%d error = %v", call, err)
		}
		if got := gjson.GetBytes(resp.Payload, "output.0.content.0.text").String(); got != "relaxed" {
			t.Fatalf("content = %q in %s", got, resp.Payload)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(strictFlags) != 3 || !strictFlags[0] || strictFlags[1] || strictFlags[2] {
		t.Fatalf("strict flags per request = %v, want [true false false]", strictFlags)
	}
}
