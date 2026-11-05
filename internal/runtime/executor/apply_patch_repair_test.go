package executor

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// task6RepairSource completes tool input without implicitly completing the response.
func task6RepairSource(mode string) [][]byte {
	if mode == "empty" {
		return nil
	}
	item := `{"type":"function_call","id":"a","call_id":"c","name":"apply_patch","arguments":"{\"input\":\"valid patch\"}"}`
	events := [][]byte{[]byte(`{"type":"response.created","response":{"id":"r"}}`)}
	if mode == "args-eof" || mode == "done" {
		events = append(events,
			[]byte(`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"a","call_id":"c","name":"apply_patch","arguments":""}}`),
			[]byte(`{"type":"response.function_call_arguments.done","output_index":0,"item_id":"a","call_id":"c","arguments":"{\"input\":\"valid patch\"}"}`))
	} else {
		events = append(events, []byte(`{"type":"response.output_item.done","output_index":0,"item":`+item+`}`))
	}
	if mode == "done" {
		events = append(events, []byte("[DONE]"))
	} else if strings.HasPrefix(mode, "response.") {
		events = append(events, []byte(fmt.Sprintf(`{"type":%q,"response":{"id":"r","output":[%s],"usage":{"input_tokens":5,"output_tokens":3}}}`, mode, item)))
	}
	return events
}

func task6RepairSSE(events [][]byte) string {
	var body strings.Builder
	for _, event := range events {
		body.WriteString("data: ")
		body.Write(event)
		body.WriteString("\n\n")
	}
	return body.String()
}

func task6RepairExecutor(provider string) cliproxyauth.ProviderExecutor {
	switch provider {
	case "meta":
		return NewNeoPiResponsesExecutor("meta", &config.Config{})
	default:
		return NewXAIExecutor(&config.Config{})
	}
}

func TestApplyPatchRepairResponsesSourceTerminalHTTP(t *testing.T) {
	for _, provider := range []string{"xai", "meta"} {
		for _, mode := range []string{"args-eof", "item-eof", "empty", "done", "response.completed", "response.incomplete", "response.done"} {
			t.Run(provider+"/"+mode, func(t *testing.T) {
				body := task6RepairSSE(task6RepairSource(mode))
				if strings.HasPrefix(mode, "response.") {
					body += "data: [DONE]\n\ndata: [DONE]\n\n"
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, body)
				}))
				defer server.Close()
				exec := task6RepairExecutor(provider)
				auth := &cliproxyauth.Auth{ID: t.Name(), Provider: provider, Attributes: map[string]string{"api_key": "test", "base_url": server.URL}}
				req := cliproxyexecutor.Request{Model: "grok-4", Payload: []byte(task6PatchRequest)}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}
				if !strings.HasPrefix(mode, "response.") {
					checkUsage := task6CaptureFailureUsage(t, auth.ID)
					stream, errExecuteStream := exec.ExecuteStream(t.Context(), auth, req, opts)
					if errExecuteStream != nil {
						t.Fatal(errExecuteStream)
					}
					assertTask6FailedStream(t, stream.Chunks)
					checkUsage()
					// Exercise the gateway too, but do not rely on its final HTTP validator.
					auth.ID += "-gateway"
					checkGatewayUsage := task6CaptureFailureUsage(t, auth.ID)
					result := task6Gateway(t, exec, auth, true)
					output := result.Body.String()
					if strings.Count(output, "event: response.failed") != 1 || strings.Contains(output, "event: error") || strings.Contains(output, "response.completed") || strings.Contains(output, "[DONE]") {
						t.Errorf("gateway failure contract: %d %s", result.Code, output)
					}
					checkGatewayUsage()
					return
				}
				stream, errExecuteStream := exec.ExecuteStream(t.Context(), auth, req, opts)
				if errExecuteStream != nil {
					t.Fatal(errExecuteStream)
				}
				var output []byte
				for chunk := range stream.Chunks {
					if chunk.Err != nil {
						t.Error(chunk.Err)
					}
					output = append(output, chunk.Payload...)
				}
				if bytes.Contains(output, []byte("response.failed")) || !bytes.Contains(output, []byte("valid patch")) || bytes.Count(output, []byte("[DONE]")) != 1 {
					t.Fatalf("legal source terminal/first DONE lost: %s", output)
				}
			})
		}
	}
}

func task6RepairChunkType(payload []byte) string {
	payload = bytes.TrimSpace(payload)
	if bytes.HasPrefix(payload, []byte("data:")) {
		payload = bytes.TrimSpace(payload[5:])
	}
	return gjson.GetBytes(payload, "type").String()
}

func task6RepairReadStream(t *testing.T, stream *cliproxyexecutor.StreamResult, failure bool) {
	t.Helper()
	failed, cleanErrors, completed := 0, 0, 0
	validPatch := false
	for chunk := range stream.Chunks {
		validPatch = validPatch || bytes.Contains(chunk.Payload, []byte("valid patch"))
		if chunk.Err != nil {
			cleanErrors++
			if failure {
				assertTask6PatchError(t, chunk.Err)
			} else {
				t.Error(chunk.Err)
			}
		}
		switch task6RepairChunkType(chunk.Payload) {
		case "response.failed":
			failed++
		case "response.completed", "response.done", "response.incomplete":
			completed++
		}
		if bytes.Contains(chunk.Payload, []byte("RAW_SECRET")) || (failure && bytes.Contains(chunk.Payload, []byte("[DONE]"))) {
			t.Errorf("invalid source leaked: %s", chunk.Payload)
		}
	}
	if failure && (failed != 1 || cleanErrors != 1 || completed != 0) {
		t.Fatalf("failure counts: failed=%d errors=%d completed=%d", failed, cleanErrors, completed)
	}
	if !failure && (failed != 0 || cleanErrors != 0 || completed != 1 || !validPatch) {
		t.Fatalf("success counts: failed=%d errors=%d completed=%d", failed, cleanErrors, completed)
	}
}

type task6RepairLifecycle struct {
	mu    sync.Mutex
	close func() error
	ended chan string
	once  sync.Once
}

func (l *task6RepairLifecycle) Bind(closeResource func() error) error {
	l.mu.Lock()
	l.close = closeResource
	l.mu.Unlock()
	return nil
}
func (l *task6RepairLifecycle) End(reason string) { l.once.Do(func() { l.ended <- reason }) }
func (*task6RepairLifecycle) Retain()             {}
func (l *task6RepairLifecycle) detach() error {
	l.mu.Lock()
	closeResource := l.close
	l.mu.Unlock()
	if closeResource == nil {
		return errors.New("lifecycle was not bound")
	}
	return closeResource()
}

func TestApplyPatchRepairGatewayInitializesGinTestMode(t *testing.T) {
	// Simulate an isolated fixture starting in Gin's default mode, not suite order.
	gin.SetMode(gin.DebugMode)
	defer gin.SetMode(gin.TestMode)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, task6ProviderFixture("custom-compat", "nonstream", "apply_patch"))
	}))
	defer server.Close()
	exec := NewOpenAICompatExecutor("custom-compat", &config.Config{})
	auth := &cliproxyauth.Auth{ID: t.Name(), Provider: exec.Identifier(), Attributes: map[string]string{"api_key": "test", "base_url": server.URL}}
	result := task6Gateway(t, exec, auth, false)
	if result.Code != http.StatusBadGateway {
		t.Fatalf("expected fixture error path, got %d", result.Code)
	}
	if gin.Mode() != gin.TestMode {
		t.Fatalf("shared gateway fixture left Gin in %q", gin.Mode())
	}
}

func TestApplyPatchRepairResponsesSourceTerminalNonStream(t *testing.T) {
	for _, provider := range []string{"xai", "meta"} {
		for _, mode := range []string{"args-eof", "item-eof", "empty", "done"} {
			t.Run(provider+"/"+mode, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					_, _ = io.WriteString(w, task6RepairSSE(task6RepairSource(mode)))
				}))
				defer server.Close()
				exec := task6RepairExecutor(provider)
				auth := &cliproxyauth.Auth{ID: t.Name(), Provider: provider, Attributes: map[string]string{"api_key": "test", "base_url": server.URL}}
				checkUsage := task6CaptureFailureUsage(t, auth.ID)
				response, errExecute := exec.Execute(t.Context(), auth, cliproxyexecutor.Request{Model: "grok-4", Payload: []byte(task6PatchRequest)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
				if len(response.Payload) != 0 {
					t.Errorf("failed source returned payload: %s", response.Payload)
				}
				assertTask6PatchError(t, errExecute)
				checkUsage()
				auth.ID += "-gateway"
				checkGatewayUsage := task6CaptureFailureUsage(t, auth.ID)
				result := task6Gateway(t, exec, auth, false)
				if result.Code != http.StatusBadGateway || !strings.Contains(result.Body.String(), "Invalid apply_patch tool arguments received from upstream.") {
					t.Errorf("non-stream gateway did not return clean 502: %d %s", result.Code, result.Body)
				}
				checkGatewayUsage()
			})
		}
	}
}

func TestApplyPatchRepairOrdinaryEmptyHTTPPassthrough(t *testing.T) {
	for _, provider := range []string{"xai", "meta"} {
		t.Run(provider, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			defer server.Close()
			exec := task6RepairExecutor(provider)
			auth := &cliproxyauth.Auth{Provider: provider, Attributes: map[string]string{"api_key": "test", "base_url": server.URL}}
			_, errExecute := exec.Execute(t.Context(), auth, cliproxyexecutor.Request{Model: "grok-4", Payload: []byte(`{"input":"ordinary","tools":[{"type":"function","name":"apply_patch"}]}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
			if status, okStatus := errExecute.(interface{ StatusCode() int }); !okStatus || status.StatusCode() != http.StatusRequestTimeout {
				t.Fatalf("ordinary disconnect changed: %v", errExecute)
			}
		})
	}
}
