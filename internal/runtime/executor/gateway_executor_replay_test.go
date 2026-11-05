package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// gatewayRecorder is the HTTP transport the executor sends through: it
// records the request as it would leave NeoProxy and answers 400, so nothing
// reaches the network and no retry follows.
type gatewayRecorder struct {
	mu   sync.Mutex
	sent []*http.Request
	body [][]byte
}

func (r *gatewayRecorder) RoundTrip(request *http.Request) (*http.Response, error) {
	data, _ := io.ReadAll(request.Body)
	r.mu.Lock()
	r.sent = append(r.sent, request.Clone(context.Background()))
	r.body = append(r.body, data)
	r.mu.Unlock()
	return &http.Response{StatusCode: http.StatusBadRequest, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{"error":{"message":"recorded"}}`)), Request: request}, nil
}

// gatewayDownstream is the dialect a client sends NeoProxy for each captured
// API: the gateway's Anthropic rows take Messages, its Responses rows take
// Responses, and the Chat Completions and Google rows take Chat Completions.
func gatewayDownstream(api string) sdktranslator.Format {
	switch api {
	case "anthropic-messages":
		return sdktranslator.FormatClaude
	case "openai-responses":
		return sdktranslator.FormatOpenAIResponse
	}
	return sdktranslator.FormatOpenAI
}

// Every captured NeoPi request leaves the gateway executor as NeoPi sent it:
// method, full URL, Host, every header and the body, on all four APIs.
// connection is transport-owned (Bun stamps keep-alive; Go's transport owns
// it), so it alone is not compared.
func TestGatewayExecutorReplaysNeoPiRequests(t *testing.T) {
	capture := readGatewayCapture(t)
	recorder := &gatewayRecorder{}
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(recorder))
	executors := map[string]*GatewayExecutor{}
	for _, fixture := range capture.Fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			provider, row := gatewayRow(t, fixture.Row)
			exec := executors[provider]
			if exec == nil {
				exec = NewGatewayExecutor("openai-compatible-"+provider, provider, &config.Config{})
				executors[provider] = exec
			}
			var downstream bytes.Buffer
			if err := json.Compact(&downstream, fixture.Downstream); err != nil {
				t.Fatal(err)
			}
			auth := &cliproxyauth.Auth{ID: "replay-" + provider, Provider: "openai-compatible-" + provider, Attributes: map[string]string{"api_key": capture.APIKey}}
			recorder.mu.Lock()
			recorder.sent, recorder.body = nil, nil
			recorder.mu.Unlock()
			// A client conveys its conversation the way its dialect does:
			// Messages clients in X-Claude-Code-Session-Id, OpenAI-dialect
			// clients (NeoPi among them) as prompt_cache_key.
			format := gatewayDownstream(fixture.API)
			payload := downstream.Bytes()
			headers := http.Header{}
			if format == sdktranslator.FormatClaude {
				headers.Set("X-Claude-Code-Session-Id", capture.SessionID)
			} else if !gjson.GetBytes(payload, "prompt_cache_key").Exists() {
				payload, _ = sjson.SetBytes(payload, "prompt_cache_key", capture.SessionID)
			}
			// Routing hands the executor the model the account registered:
			// here the very row NeoPi's capture resolved.
			info := &registry.ModelInfo{ID: row.ID, NeoPi: row, MaxCompletionTokens: row.MaxTokens, ContextLength: row.ContextWindow}
			if row.Thinking != nil {
				info.Thinking = &registry.ThinkingSupport{Levels: row.Thinking.Efforts, ZeroAllowed: true}
			}
			_, _ = exec.Execute(ctx, auth, cliproxyexecutor.Request{Model: row.ID, Payload: payload, Metadata: map[string]any{"cliproxy.resolved_oauth_model_info": info}}, cliproxyexecutor.Options{
				SourceFormat:    format,
				OriginalRequest: payload,
				Headers:         headers,
			})
			if len(recorder.sent) != 1 {
				t.Fatalf("executor sent %d requests, want 1", len(recorder.sent))
			}
			sent, body := recorder.sent[0], recorder.body[0]
			if sent.Method != http.MethodPost {
				t.Errorf("method = %s, want POST", sent.Method)
			}
			if sent.URL.String() != fixture.RequestedURL {
				t.Errorf("url = %s, want %s", sent.URL, fixture.RequestedURL)
			}
			host := sent.Host
			if host == "" {
				host = sent.URL.Host
			}
			if want := fixture.Headers["host"]; host != want {
				t.Errorf("host = %s, want %s", host, want)
			}
			if want := fixture.Headers["content-length"]; strconv.FormatInt(sent.ContentLength, 10) != want {
				t.Errorf("content-length = %d, want %s", sent.ContentLength, want)
			}
			if string(body) != fixture.RawBody {
				t.Errorf("body:\n got %s\nwant %s", body, fixture.RawBody)
			}
			gatewayHeaderParity(t, sent.Header, fixture.Headers)
		})
	}
}
