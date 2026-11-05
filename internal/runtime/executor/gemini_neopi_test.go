package executor

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/googlewire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// geminiCapture is testdata/gemini_npi_capture.json.gz: NeoPi's OWN
// streamSimple run over its bundled google rows against a loopback server, one
// request per scenario, beside the downstream Chat Completions request a
// client sends NeoProxy for it.
type geminiCapture struct {
	APIKey    string `json:"apiKey"`
	SessionID string `json:"sessionId"`
	Fixtures  []struct {
		Name         string            `json:"name"`
		Model        string            `json:"model"`
		RequestedURL string            `json:"requestedURL"`
		Headers      map[string]string `json:"headers"`
		RawBody      string            `json:"rawBody"`
		Downstream   json.RawMessage   `json:"downstream"`
	} `json:"fixtures"`
}

// Every captured NeoPi request is reproduced byte for byte: URL, headers and
// body, for each thinking mode, tool, image, tool-result and retry shape.
func TestGeminiNeoPiRequestParity(t *testing.T) {
	file, err := os.Open("testdata/gemini_npi_capture.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	var capture geminiCapture
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	if len(capture.Fixtures) == 0 {
		t.Fatal("capture has no fixtures")
	}
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": capture.APIKey}}
	for _, fixture := range capture.Fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			row := registry.GoogleNeoPiModelFor(fixture.Model)
			if row == nil || row.ID != fixture.Model {
				t.Fatalf("row = %+v, want model %s", row, fixture.Model)
			}
			// Translators hand the shaper compact JSON.
			var downstream bytes.Buffer
			if err = json.Compact(&downstream, fixture.Downstream); err != nil {
				t.Fatal(err)
			}
			wire := neoPiGoogleWireModel(row)
			neoPiContext, options, err := googlewire.FromOpenAIChat(downstream.Bytes(), wire)
			if err != nil {
				t.Fatal(err)
			}
			resolved, err := googlewire.MapOptions(wire, options)
			if err != nil {
				t.Fatal(err)
			}
			body, err := googlewire.BuildBody(wire, neoPiContext, resolved, nil)
			if err != nil {
				t.Fatal(err)
			}
			url := geminiNeoPiBaseURL(auth, row) + "/models/" + row.ID + ":streamGenerateContent?alt=sse"
			if url != fixture.RequestedURL {
				t.Errorf("url = %s, want %s", url, fixture.RequestedURL)
			}
			if string(body) != fixture.RawBody {
				t.Errorf("body:\n got %s\nwant %s", body, fixture.RawBody)
			}
			headers := neoPiGoogleHeaders(context.Background(), auth, nil, capture.APIKey)
			// `connection` is transport-owned: Bun's fetch stamps keep-alive on
			// HTTP/1.1, Go's transport manages it and forbids it on the HTTP/2 the
			// Gemini API is served over.
			want := map[string]string{}
			for name, value := range fixture.Headers {
				if name != "host" && name != "content-length" && name != "connection" {
					want[name] = value
				}
			}
			got := map[string]string{}
			for name, values := range headers {
				got[strings.ToLower(name)] = strings.Join(values, ", ")
			}
			for name, value := range want {
				if got[name] != value {
					t.Errorf("header %s = %q, want %q", name, got[name], value)
				}
			}
			delete(got, "connection")
			for name := range got {
				if _, ok := want[name]; !ok {
					t.Errorf("extra header %s = %q", name, got[name])
				}
			}
			var _ http.Header = headers
		})
	}
}

func geminiTestExecutor(t *testing.T, handler http.HandlerFunc) (*NeoPiGoogleExecutor, *cliproxyauth.Auth) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "gemini", Attributes: map[string]string{"api_key": "AIzaTest", "base_url": server.URL}}
	return NewGeminiExecutor(&config.Config{}), auth
}

func geminiChatRequest() (cliproxyexecutor.Request, cliproxyexecutor.Options) {
	payload := []byte(`{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	return cliproxyexecutor.Request{Model: "gemini-2.5-flash", Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, Stream: true}
}

func drainGeminiStream(t *testing.T, result *cliproxyexecutor.StreamResult) (string, error) {
	t.Helper()
	var text strings.Builder
	var streamErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
		}
		text.Write(chunk.Payload)
	}
	return text.String(), streamErr
}

// A stream that ends without a finish reason is truncated, as in NeoPi.
func TestGeminiNeoPiStreamWithoutFinishFails(t *testing.T) {
	exec, auth := geminiTestExecutor(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"par\"}]}}]}\n\n")
	})
	req, opts := geminiChatRequest()
	result, err := exec.ExecuteStream(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, streamErr := drainGeminiStream(t, result); streamErr == nil || !strings.Contains(streamErr.Error(), "without a finish reason") {
		t.Fatalf("stream error = %v", streamErr)
	}
}

// A STOP with only whitespace text is retried, then delivered once content arrives.
func TestGeminiNeoPiEmptyResponseIsRetried(t *testing.T) {
	var attempts int
	exec, auth := geminiTestExecutor(t, func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		w.Header().Set("Content-Type", "text/event-stream")
		text := " "
		if attempts == 2 {
			text = "real"
		}
		_, _ = io.WriteString(w, `data: {"candidates":[{"content":{"parts":[{"text":"`+text+`"}]},"finishReason":"STOP"}]}`+"\n\n")
	})
	req, opts := geminiChatRequest()
	result, err := exec.ExecuteStream(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatal(err)
	}
	text, streamErr := drainGeminiStream(t, result)
	if streamErr != nil || attempts != 2 || !strings.Contains(text, "real") {
		t.Fatalf("attempts=%d err=%v text=%s", attempts, streamErr, text)
	}
}

// Three empty answers in a row fail like NeoPi's exhausted retries.
func TestGeminiNeoPiEmptyResponseExhaustsRetries(t *testing.T) {
	var attempts int
	exec, auth := geminiTestExecutor(t, func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"candidates":[{"content":{"parts":[{"text":""}]},"finishReason":"STOP"}]}`+"\n\n")
	})
	req, opts := geminiChatRequest()
	if _, err := exec.Execute(context.Background(), auth, req, opts); err == nil || attempts != 3 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
}

// The request carries NeoPi's headers and a 4xx body surfaces with its status.
func TestGeminiNeoPiSendsKeyHeaderAndSurfacesErrors(t *testing.T) {
	var gotKey, gotAuth, gotAgent string
	exec, auth := geminiTestExecutor(t, func(w http.ResponseWriter, r *http.Request) {
		gotKey, gotAuth, gotAgent = r.Header.Get("x-goog-api-key"), r.Header.Get("Authorization"), r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"code":429,"message":"Quota","status":"RESOURCE_EXHAUSTED"}}`)
	})
	req, opts := geminiChatRequest()
	_, err := exec.Execute(context.Background(), auth, req, opts)
	status, ok := err.(interface{ StatusCode() int })
	if !ok || status.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("err = %T %v", err, err)
	}
	if gotKey != "AIzaTest" || gotAuth != "" || !strings.HasPrefix(gotAgent, "npi/") {
		t.Fatalf("headers key=%q auth=%q agent=%q", gotKey, gotAuth, gotAgent)
	}
}

// Discovery follows NeoPi's paged /v1beta/models listing and overlays the bundle.
func TestFetchGoogleModelsPagesAndOverlays(t *testing.T) {
	var queries []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("pageToken") == "" {
			_, _ = io.WriteString(w, `{"models":[{"name":"models/gemini-9.9-pro","displayName":"Gemini 9.9 Pro","supportedGenerationMethods":["generateContent"],"inputTokenLimit":123456,"outputTokenLimit":4000}],"nextPageToken":"p2"}`)
			return
		}
		_, _ = io.WriteString(w, `{"models":[{"name":"models/imagen-4","supportedGenerationMethods":["predict"]}]}`)
	}))
	defer server.Close()
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "AIzaTest", "base_url": server.URL}}
	models, err := FetchGoogleModels(context.Background(), &config.Config{}, auth)
	if err != nil {
		t.Fatal(err)
	}
	if len(queries) != 2 || !strings.Contains(queries[0], "pageSize=100") || !strings.Contains(queries[0], "key=AIzaTest") || !strings.Contains(queries[1], "pageToken=p2") {
		t.Fatalf("queries = %v", queries)
	}
	found := map[string]*registry.ModelInfo{}
	for _, model := range models {
		found[model.ID] = model
	}
	if found["gemini-3.1-pro-preview"] == nil {
		t.Fatal("bundled rows must survive a listing")
	}
	if row := found["gemini-9.9-pro"]; row == nil || row.NeoPi == nil || row.NeoPi.ContextWindow != 123456 || !row.NeoPi.Reasoning {
		t.Fatalf("discovered row = %+v", row)
	}
	if found["imagen-4"] != nil {
		t.Fatal("non-generateContent models must be skipped")
	}
}
