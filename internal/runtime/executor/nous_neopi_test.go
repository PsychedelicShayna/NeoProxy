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
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// nousCapture is testdata/neopi_nous/<provider>.fixtures.json.gz: NeoPi's OWN
// streamSimple run over the models its Nous Portal or Marineris extension
// registers, against a loopback server, beside the downstream request a
// client sends NeoProxy for each scenario.
type nousCapture struct {
	Provider  string            `json:"provider"`
	APIKey    string            `json:"apiKey"`
	SessionID string            `json:"sessionId"`
	Rows      []json.RawMessage `json:"rows"`
	Fixtures  []struct {
		Name         string            `json:"name"`
		Model        string            `json:"model"`
		Method       string            `json:"method"`
		RequestedURL string            `json:"requestedURL"`
		Headers      map[string]string `json:"headers"`
		RawBody      string            `json:"rawBody"`
		Downstream   json.RawMessage   `json:"downstream"`
		Error        string            `json:"error"`
	} `json:"fixtures"`
}

func readNousCapture(t *testing.T, path string) nousCapture {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	reader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	var capture nousCapture
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	if len(capture.Fixtures) == 0 {
		t.Fatalf("%s has no fixtures", path)
	}
	return capture
}

// nousFixtureRow resolves the model a fixture ran on the way NeoProxy does:
// the bundled snapshot for Nous Portal, the discovery rows' mapping for
// Marineris.
func nousFixtureRow(t *testing.T, capture nousCapture, model, base string) *registry.NeoPiModel {
	t.Helper()
	for _, row := range capture.Rows {
		if gjson.GetBytes(row, "id").String() == model {
			return registry.MarinerisMapModel(gjson.ParseBytes(row), base)
		}
	}
	return nil
}

// nousRecorder is the HTTP transport the executors send through: it records
// the request as it would leave NeoProxy and answers 400, so nothing reaches
// the network and no retry follows.
type nousRecorder struct {
	sent []*http.Request
	body [][]byte
}

func (r *nousRecorder) RoundTrip(request *http.Request) (*http.Response, error) {
	data, _ := io.ReadAll(request.Body)
	r.sent = append(r.sent, request.Clone(context.Background()))
	r.body = append(r.body, data)
	return &http.Response{StatusCode: http.StatusBadRequest, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{"error":{"message":"recorded"}}`)), Request: request}, nil
}

// Every captured NeoPi request leaves the Nous Portal and Marineris executors
// as NeoPi sent it: method, full URL, Host, Content-Length, every header and
// the body, for each thinking mode, tool, image and tool-result shape, on both
// of Marineris' APIs. connection is transport-owned (Bun stamps keep-alive;
// Go's transport owns it), so it alone is not compared.
func TestNousMarinerisRequestParity(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "neopi_nous", "*.fixtures.json.gz"))
	if err != nil || len(files) != 3 {
		t.Fatalf("captures = %v, err %v", files, err)
	}
	total := 0
	for _, file := range files {
		capture := readNousCapture(t, file)
		t.Run(capture.Provider, func(t *testing.T) {
			recorder := &nousRecorder{}
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(recorder))
			for _, fixture := range capture.Fixtures {
				if fixture.Error != "" {
					t.Fatalf("%s: capture error %s", fixture.Name, fixture.Error)
				}
				t.Run(fixture.Name, func(t *testing.T) {
					base := strings.TrimSuffix(strings.TrimSuffix(fixture.RequestedURL, "/chat/completions"), "/responses")
					var row *registry.NeoPiModel
					var exec cliproxyauth.ProviderExecutor
					auth := &cliproxyauth.Auth{ID: "replay", Provider: capture.Provider, Attributes: map[string]string{"api_key": capture.APIKey}}
					if capture.Provider == "nous-portal" {
						row = registry.NousBundledNeoPiModel(fixture.Model)
						exec = NewNousExecutor("nous-portal", &config.Config{})
					} else {
						row = nousFixtureRow(t, capture, fixture.Model, base)
						auth.Attributes["base_url"] = base
						exec = NewMarinerisExecutor("marineris", &config.Config{})
					}
					if row == nil || row.ID != fixture.Model {
						t.Fatalf("no NeoPi row for %s: %+v", fixture.Model, row)
					}
					var downstream bytes.Buffer
					if err := json.Compact(&downstream, fixture.Downstream); err != nil {
						t.Fatal(err)
					}
					format := sdktranslator.FormatOpenAI
					if row.API == "openai-responses" {
						format = sdktranslator.FormatOpenAIResponse
					}
					recorder.sent, recorder.body = nil, nil
					// Routing hands the executor the model the account registered.
					var info *registry.ModelInfo
					if capture.Provider == "nous-portal" {
						for _, candidate := range registry.NousBundledModels("nous-portal") {
							if candidate.ID == row.ID {
								info = candidate
							}
						}
					} else {
						for _, candidate := range registry.MarinerisModels(capture.Rows, base, "marineris") {
							if candidate.ID == row.ID {
								info = candidate
							}
						}
					}
					if info == nil {
						t.Fatalf("no registered model %s", row.ID)
					}
					// NeoPi's conversation id reaches the executor as the client's prompt_cache_key.
					payload, _ := sjson.SetBytes(downstream.Bytes(), "prompt_cache_key", capture.SessionID)
					_, _ = exec.Execute(ctx, auth, cliproxyexecutor.Request{Model: row.ID, Payload: payload, Metadata: map[string]any{"cliproxy.resolved_oauth_model_info": info}},
						cliproxyexecutor.Options{SourceFormat: format, OriginalRequest: payload})
					if len(recorder.sent) != 1 {
						t.Fatalf("executor sent %d requests, want 1", len(recorder.sent))
					}
					sent, body := recorder.sent[0], recorder.body[0]
					if sent.Method != fixture.Method {
						t.Errorf("method = %s, want %s", sent.Method, fixture.Method)
					}
					if sent.URL.String() != fixture.RequestedURL {
						t.Errorf("url = %s, want %s", sent.URL, fixture.RequestedURL)
					}
					host := sent.Host
					if host == "" {
						host = sent.URL.Host
					}
					if host != fixture.Headers["host"] {
						t.Errorf("host = %s, want %s", host, fixture.Headers["host"])
					}
					if strconv.FormatInt(sent.ContentLength, 10) != fixture.Headers["content-length"] {
						t.Errorf("content-length = %d, want %s", sent.ContentLength, fixture.Headers["content-length"])
					}
					if string(body) != fixture.RawBody {
						t.Errorf("body:\n got %s\nwant %s", body, fixture.RawBody)
					}
					got := map[string]string{}
					for name, values := range sent.Header {
						got[strings.ToLower(name)] = strings.Join(values, ", ")
					}
					for name, value := range fixture.Headers {
						switch name {
						case "host", "content-length", "connection":
							continue
						}
						if got[name] != value {
							t.Errorf("header %s = %q, want %q", name, got[name], value)
						}
					}
					for name, value := range got {
						if _, ok := fixture.Headers[name]; !ok && name != "connection" {
							t.Errorf("extra header %s = %q", name, value)
						}
					}
				})
			}
		})
		total += len(capture.Fixtures)
	}
	t.Logf("%d captured requests replayed", total)
}

// An account's discovered model is sent through the executor with the
// account's key, or a sign-in's access token, as the bearer; a refresh-locked
// import (no refresh token) is left to NeoPi to refresh.
func TestNousExecutorSendsDiscoveredModelWithAccountCredential(t *testing.T) {
	for name, auth := range map[string]*cliproxyauth.Auth{
		"api key": {ID: "key", Provider: "nous-portal", Attributes: map[string]string{"api_key": "sk-nous-key"}},
		"sign-in": {ID: "oauth", Provider: "nous-portal", Metadata: map[string]any{"access_token": "sk-nous-key", "refresh_token": ""}},
	} {
		t.Run(name, func(t *testing.T) {
			var gotPath, gotAuth, gotBody string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				gotPath, gotAuth, gotBody = r.URL.Path, r.Header.Get("Authorization"), string(body)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hi.\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			}))
			defer server.Close()
			row := registry.NousBundledNeoPiModel("deepseek/deepseek-v4-pro")
			row.BaseURL = server.URL + "/v1"
			info := &registry.ModelInfo{ID: row.ID, NeoPi: row}
			req := cliproxyexecutor.Request{Model: row.ID, Payload: []byte(`{"model":"deepseek/deepseek-v4-pro","messages":[{"role":"user","content":"Say hi."}]}`),
				Metadata: map[string]any{"cliproxy.resolved_oauth_model_info": info}}
			exec := NewNousExecutor("nous-portal", nil)
			resp, err := exec.Execute(context.Background(), auth, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
			if err != nil {
				t.Fatal(err)
			}
			if gotPath != "/v1/chat/completions" || gotAuth != "Bearer sk-nous-key" || !strings.Contains(gotBody, `"model":"deepseek/deepseek-v4-pro"`) {
				t.Errorf("request = %s %s %s", gotPath, gotAuth, gotBody)
			}
			if !strings.Contains(string(resp.Payload), "Hi.") {
				t.Errorf("response = %s", resp.Payload)
			}
			if refreshed, err := exec.Refresh(context.Background(), auth); err != nil || refreshed != auth {
				t.Errorf("refresh without a refresh token = %v, %v; want the account untouched", refreshed, err)
			}
		})
	}
}

// A Marineris account serves Codex-class models over NeoPi's openai-responses
// provider at the gateway address it carries, with the gateway key as bearer,
// and Grok-class models over openai-completions at the same address.
func TestMarinerisExecutorRoutesRowsByAPI(t *testing.T) {
	type seen struct{ path, auth, body string }
	var requests []seen
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, seen{r.URL.Path, r.Header.Get("Authorization"), string(body)})
		w.Header().Set("Content-Type", "text/event-stream")
		if strings.HasSuffix(r.URL.Path, "/responses") {
			_, _ = io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"gpt-6.1-sol\",\"output\":[{\"type\":\"message\",\"id\":\"m\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"Hi.\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
			return
		}
		_, _ = io.WriteString(w, "data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hi.\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	auth := &cliproxyauth.Auth{ID: "m", Provider: "openai-compatible-marineris", Attributes: map[string]string{"api_key": "mrn_key", "base_url": server.URL + "/v1", "compat_name": "marineris"}}
	exec := NewMarinerisExecutor("marineris", nil)
	for _, row := range []string{"gpt-6.1-sol", "grok-4.6-reasoning"} {
		model := registry.MarinerisMapModel(gjson.Parse(`{"id":"`+row+`"}`), server.URL+"/v1")
		if model == nil {
			t.Fatalf("%s did not map", row)
		}
		info := &registry.ModelInfo{ID: model.ID, NeoPi: model}
		payload := `{"model":"` + row + `","messages":[{"role":"user","content":"Say hi."}]}`
		source := sdktranslator.FormatOpenAI
		if model.API == "openai-responses" {
			payload = `{"model":"` + row + `","input":[{"role":"user","content":[{"type":"input_text","text":"Say hi."}]}]}`
			source = sdktranslator.FormatOpenAIResponse
		}
		req := cliproxyexecutor.Request{Model: row, Payload: []byte(payload), Metadata: map[string]any{"cliproxy.resolved_oauth_model_info": info}}
		if _, err := exec.Execute(context.Background(), auth, req, cliproxyexecutor.Options{SourceFormat: source}); err != nil {
			t.Fatalf("%s: %v", row, err)
		}
	}
	if len(requests) != 2 || requests[0].path != "/v1/responses" || requests[1].path != "/v1/chat/completions" {
		t.Fatalf("requests = %+v", requests)
	}
	for _, request := range requests {
		if request.auth != "Bearer mrn_key" {
			t.Errorf("%s: authorization = %q", request.path, request.auth)
		}
	}
}
