package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/antigravitywire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/googlewire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// antigravityCapture is testdata/antigravity_npi_capture.json: NeoPi's OWN
// streamGoogleGeminiCli (capture_antigravity_npi.ts) run against a loopback
// Cloud Code Assist fake.
type antigravityCapture struct {
	Project     string                         `json:"project"`
	Credentials map[string]any                 `json:"credentials"`
	Models      map[string]registry.NeoPiModel `json:"models"`
	Fixtures    []struct {
		Name       string                  `json:"name"`
		Model      string                  `json:"model"`
		Script     []antigravityScriptStep `json:"script"`
		Downstream json.RawMessage         `json:"downstream"`
		Requests   []struct {
			Method  string            `json:"method"`
			Path    string            `json:"path"`
			Headers map[string]string `json:"headers"`
			Body    string            `json:"body"`
		} `json:"requests"`
		Reply *struct {
			Content    []json.RawMessage `json:"content"`
			StopReason string            `json:"stopReason"`
			Usage      struct {
				Input       int64 `json:"input"`
				Output      int64 `json:"output"`
				CacheRead   int64 `json:"cacheRead"`
				TotalTokens int64 `json:"totalTokens"`
				Reasoning   int64 `json:"reasoningTokens"`
			} `json:"usage"`
		} `json:"reply"`
		Error *struct {
			StopReason   string `json:"stopReason"`
			ErrorMessage string `json:"errorMessage"`
			Thrown       string `json:"thrown"`
		} `json:"error"`
	} `json:"fixtures"`
}

type antigravityScriptStep struct {
	Status int               `json:"status"`
	Body   string            `json:"body"`
	Chunks []json.RawMessage `json:"chunks"`
}

type antigravitySent struct {
	method  string
	path    string
	headers map[string]string
	body    string
}

var (
	antigravityUUID = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	antigravitySess = regexp.MustCompile(`"sessionId":"-\d+"`)
)

// normalizeAntigravityBody removes the random identity a capture cannot
// reproduce: envelope UUIDs and the random session id.
func normalizeAntigravityBody(body string) string {
	body = antigravityUUID.ReplaceAllString(body, "<uuid>")
	return antigravitySess.ReplaceAllString(body, `"sessionId":"<session>"`)
}

// TestAntigravityExecutorReplaysNeoPiRequests sends each captured conversation
// as the Claude request a client gives NeoProxy and requires the requests NeoPi
// sent — path, headers and body — answered the way the capture's fake answered.
func TestAntigravityExecutorReplaysNeoPiRequests(t *testing.T) {
	t.Setenv("PI_AI_ANTIGRAVITY_VERSION", "2.26.0")
	data, err := os.ReadFile("testdata/antigravity_npi_capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture antigravityCapture
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	var (
		mu     sync.Mutex
		sent   []antigravitySent
		script []antigravityScriptStep
		served int
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := readAll(r)
		headers := map[string]string{}
		for name, values := range r.Header {
			headers[strings.ToLower(name)] = strings.Join(values, ", ")
		}
		mu.Lock()
		sent = append(sent, antigravitySent{method: r.Method, path: r.URL.RequestURI(), headers: headers, body: body})
		step := script[min(served, len(script)-1)]
		served++
		mu.Unlock()
		if step.Status != 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(step.Status)
			_, _ = w.Write([]byte(step.Body))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, chunk := range step.Chunks {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
		}
	}))
	defer server.Close()

	exec := NewAntigravityExecutor(&config.Config{})
	for _, fixture := range capture.Fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			mu.Lock()
			sent, script, served = nil, fixture.Script, 0
			mu.Unlock()
			model := capture.Models[fixture.Model]
			model.BaseURL = server.URL
			exec.sources = pinnedAntigravitySources()
			auth := &cliproxyauth.Auth{ID: "antigravity-capture-" + fixture.Name, Provider: "antigravity",
				Metadata: map[string]any{
					"access_token": capture.Credentials["token"],
					"project_id":   capture.Project,
					"email":        capture.Credentials["email"],
				},
				Attributes: map[string]string{"base_url": server.URL}}
			downstream := compact(fixture.Downstream)
			info := antigravityTestModelInfo(&model)
			events, execErr := antigravityStreamEvents(exec, auth, &model, info, downstream)
			if fixture.Error != nil {
				if execErr == nil {
					t.Fatalf("expected an error like %q", fixture.Error.ErrorMessage)
				}
				if fixture.Error.ErrorMessage != "" && !strings.Contains(execErr.Error(), fixture.Error.ErrorMessage) {
					t.Fatalf("error %v does not contain NeoPi's error %q", execErr, fixture.Error.ErrorMessage)
				}
			} else if execErr != nil {
				t.Fatalf("execute: %v", execErr)
			}
			mu.Lock()
			got := append([]antigravitySent(nil), sent...)
			mu.Unlock()
			if len(got) != len(fixture.Requests) {
				t.Fatalf("sent %d requests, NeoPi sent %d", len(got), len(fixture.Requests))
			}
			for i, want := range fixture.Requests {
				if got[i].method != want.Method {
					t.Errorf("request %d method = %s, want %s", i, got[i].method, want.Method)
				}
				if got[i].path != want.Path {
					t.Errorf("request %d path = %s, want %s", i, got[i].path, want.Path)
				}
				for _, name := range []string{"accept", "accept-encoding", "authorization", "connection", "content-type", "user-agent", "anthropic-beta"} {
					if gotValue, wantValue := got[i].headers[name], want.Headers[name]; gotValue != wantValue {
						t.Errorf("request %d header %s = %q, want %q", i, name, gotValue, wantValue)
					}
				}
				if gotBody, wantBody := normalizeAntigravityBody(got[i].body), normalizeAntigravityBody(want.Body); gotBody != wantBody {
					t.Errorf("request %d body differs\n got: %s\nwant: %s", i, gotBody, wantBody)
				}
			}
			if fixture.Reply == nil {
				return
			}
			message, errAggregate := neoPiAggregateMessage(antigravityTranscript(events))
			if errAggregate != nil {
				t.Fatalf("aggregate: %v", errAggregate)
			}
			assertAntigravityReply(t, message, fixture.Reply.Content, fixture.Reply.StopReason)
			for path, want := range map[string]int64{
				"usage.input_tokens":            fixture.Reply.Usage.Input,
				"usage.output_tokens":           fixture.Reply.Usage.Output,
				"usage.cache_read_input_tokens": fixture.Reply.Usage.CacheRead,
			} {
				if got := gjson.GetBytes(message, path).Int(); got != want {
					t.Errorf("%s = %d, want %d", path, got, want)
				}
			}
		})
	}
}

// antigravityStreamEvents runs the executor's streaming path and returns the
// Claude events it emitted.
func antigravityStreamEvents(exec *AntigravityExecutor, auth *cliproxyauth.Auth, model *registry.NeoPiModel, info *registry.ModelInfo,
	downstream []byte) ([][]byte, error) {
	result, err := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{Model: model.ID, Payload: downstream,
		Metadata: map[string]any{"cliproxy.resolved_oauth_model_info": info}},
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, OriginalRequest: downstream,
			Headers: http.Header{"X-Claude-Code-Session-Id": {"antigravity-capture-session"}}})
	if err != nil {
		return nil, err
	}
	var events [][]byte
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			return events, chunk.Err
		}
		payload := bytes.TrimSpace(chunk.Payload)
		if !bytes.HasPrefix(payload, []byte("data:")) {
			continue
		}
		events = append(events, bytes.TrimSpace(bytes.TrimPrefix(payload, []byte("data:"))))
	}
	return events, nil
}

// antigravityTranscript renders events as the Claude SSE transcript the
// aggregator reads.
func antigravityTranscript(events [][]byte) []byte {
	var transcript bytes.Buffer
	for _, event := range events {
		transcript.Write(neoPiFrame(event))
	}
	return transcript.Bytes()
}

func assertAntigravityReply(t *testing.T, message []byte, wantContent []json.RawMessage, wantStop string) {
	t.Helper()
	var decoded struct {
		Content []struct {
			Type             string          `json:"type"`
			Text             string          `json:"text"`
			TextSignature    string          `json:"textSignature"`
			Thinking         string          `json:"thinking"`
			Signature        string          `json:"signature"`
			ThoughtSignature string          `json:"thoughtSignature"`
			Name             string          `json:"name"`
			Input            json.RawMessage `json:"input"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
	}
	if err := json.Unmarshal(message, &decoded); err != nil {
		t.Fatalf("aggregated message: %v (%s)", err, message)
	}
	want := map[string]string{"stop": "end_turn", "length": "max_tokens", "toolUse": "tool_use"}[wantStop]
	if decoded.StopReason != want {
		t.Errorf("stop_reason = %q, want %q", decoded.StopReason, want)
	}
	if len(decoded.Content) != len(wantContent) {
		t.Fatalf("content blocks = %d, want %d (%s)", len(decoded.Content), len(wantContent), message)
	}
	for i, raw := range wantContent {
		var expected struct {
			Type             string          `json:"type"`
			Text             string          `json:"text"`
			TextSignature    string          `json:"textSignature"`
			Thinking         string          `json:"thinking"`
			ID               string          `json:"id"`
			Name             string          `json:"name"`
			ThoughtSignature string          `json:"thoughtSignature"`
			Args             json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(raw, &expected); err != nil {
			t.Fatalf("expected block %d: %v", i, err)
		}
		got := decoded.Content[i]
		switch expected.Type {
		case "text":
			// NeoPi keeps the whitespace of a retried empty attempt in the
			// delivered text; this port holds pre-commit events and drops them,
			// so the visible text is compared without that prefix.
			if strings.TrimSpace(got.Text) != strings.TrimSpace(expected.Text) {
				t.Errorf("block %d text = %q, want %q", i, got.Text, expected.Text)
			}
			if got.TextSignature != expected.TextSignature {
				t.Errorf("block %d text signature = %q, want %q", i, got.TextSignature, expected.TextSignature)
			}
		case "thinking":
			if got.Thinking != expected.Thinking {
				t.Errorf("block %d thinking = %q, want %q", i, got.Thinking, expected.Thinking)
			}
			if got.Signature == "" {
				t.Errorf("block %d lost its thinking signature", i)
			}
		case "toolCall":
			if got.Name != expected.Name {
				t.Errorf("block %d tool name = %q, want %q", i, got.Name, expected.Name)
			}
			if !bytes.Equal(compact(got.Input), compact(expected.Args)) {
				t.Errorf("block %d tool input = %s, want %s", i, got.Input, expected.Args)
			}
			if got.ThoughtSignature != expected.ThoughtSignature {
				t.Errorf("block %d thought signature = %q, want %q", i, got.ThoughtSignature, expected.ThoughtSignature)
			}
			if expected.ID != "" && got.Type != "tool_use" {
				t.Errorf("block %d type = %q, want tool_use", i, got.Type)
			}
		}
	}
}

func antigravityTestModelInfo(model *registry.NeoPiModel) *registry.ModelInfo {
	info := &registry.ModelInfo{ID: model.ID, Object: "model", OwnedBy: "antigravity", Type: "antigravity", Name: model.ID,
		DisplayName: model.Name, ContextLength: model.ContextWindow, MaxContextLength: model.ContextWindow,
		MaxCompletionTokens: model.MaxTokens, SupportedInputModalities: model.Input, NeoPi: model.Clone()}
	if model.Reasoning && model.Thinking != nil && len(model.Thinking.Efforts) > 0 {
		info.Thinking = &registry.ThinkingSupport{Levels: append([]string(nil), model.Thinking.Efforts...),
			ZeroAllowed: !model.Thinking.RequiresEffort}
	}
	return info
}

func pinnedAntigravitySources() antigravitywire.Sources {
	var counter int
	return antigravitywire.Sources{
		Now: func() time.Time { return time.UnixMilli(1_760_000_000_000) },
		NewUUID: func() string {
			counter++
			return fmt.Sprintf("00000000-0000-4000-8000-%012d", counter)
		},
		RandomInt: func(max *big.Int) *big.Int {
			counter++
			value := big.NewInt(int64(1_000_000_000_000 + counter))
			return value.Mod(value, max)
		},
	}
}

func readAll(r *http.Request) (string, error) {
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(bufio.NewReader(r.Body)); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func compact(raw json.RawMessage) []byte {
	var out bytes.Buffer
	if err := json.Compact(&out, raw); err != nil {
		return raw
	}
	return out.Bytes()
}

func TestAntigravityStreamEmitsEveryChunkAndBlockEnd(t *testing.T) {
	prepared := &antigravityPrepared{model: &registry.NeoPiModel{}, modelName: "model", firstEvent: time.Second}
	var events [][]byte
	run := &antigravityRun{prepared: prepared, emit: func(event []byte) error {
		events = append(events, bytes.Clone(event))
		return nil
	}}
	body := "data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"first\"}]}}]}}\n\n" +
		"data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"second\"}]},\"finishReason\":\"STOP\"}]}}\n\n"
	response := &http.Response{Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}
	outcome, err := run.streamResponse(context.Background(), response)
	if err != nil || !outcome.committed {
		t.Fatalf("stream outcome = %+v, error = %v", outcome, err)
	}
	transcript := string(antigravityTranscript(events))
	for _, want := range []string{`"text":"first"`, `"text":"second"`, `"type":"content_block_stop"`} {
		if !strings.Contains(transcript, want) {
			t.Errorf("stream is missing %s: %s", want, transcript)
		}
	}
}

func TestAntigravityStreamFirstEventTimeout(t *testing.T) {
	for _, encoding := range []string{"", "gzip"} {
		t.Run(encoding, func(t *testing.T) {
			reader, writer := io.Pipe()
			defer writer.Close()
			run := &antigravityRun{prepared: &antigravityPrepared{model: &registry.NeoPiModel{}, firstEvent: 20 * time.Millisecond}}
			_, err := run.streamResponse(context.Background(), &http.Response{Body: reader, Header: http.Header{"Content-Encoding": {encoding}}})
			if err == nil || statusOf(err) != http.StatusGatewayTimeout {
				t.Fatalf("first-event timeout = %v", err)
			}
		})
	}
}

func TestAntigravityPreparedStatePersistsExecutionID(t *testing.T) {
	data, err := os.ReadFile("testdata/antigravity_npi_capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture antigravityCapture
	if err := json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	model := capture.Models["gemini-3.1-pro"]
	model.BaseURL = antigravitywire.DailyEndpoint
	payload := []byte(`{"model":"gemini-3.1-pro","messages":[{"role":"user","content":"hello"}]}`)
	req := cliproxyexecutor.Request{Model: model.ID, Payload: payload, Metadata: map[string]any{"cliproxy.resolved_oauth_model_info": antigravityTestModelInfo(&model)}}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Headers: http.Header{"X-Claude-Code-Session-Id": {t.Name()}}}
	auth := &cliproxyauth.Auth{ID: t.Name(), Metadata: map[string]any{"access_token": "token", "project_id": "project"}}
	exec := NewAntigravityExecutor(&config.Config{})
	exec.sources = pinnedAntigravitySources()
	first, err := exec.prepare(context.Background(), auth, req, opts)
	if err != nil || first.state == nil {
		t.Fatalf("first prepare: state %v, error %v", first, err)
	}
	first.state.SetLastExecutionID("execution-one")
	first.state.SetLastGood(antigravitywire.SandboxEndpoint)
	second, err := exec.prepare(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatal(err)
	}
	if got := gjson.GetBytes(second.body, "request.labels.last_execution_id").String(); got != "execution-one" {
		t.Errorf("last execution ID = %q", got)
	}
	if second.endpoints[0] != antigravitywire.SandboxEndpoint {
		t.Errorf("preferred endpoint = %q", second.endpoints[0])
	}
}

func TestAntigravityAppliesImageContract(t *testing.T) {
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	ctx := googlewire.Context{Messages: []googlewire.Message{{Role: googlewire.RoleUser, Content: []googlewire.Block{{
		Type: googlewire.BlockImage, Data: base64.StdEncoding.EncodeToString(encoded.Bytes()), MimeType: "image/png",
	}}}}}
	model := &registry.NeoPiModel{GeminiCompat: &registry.NeoPiGeminiCompat{MaxImageDimension: 2}}
	antigravityPrepareImages(&ctx, model)
	data, err := base64.StdEncoding.DecodeString(ctx.Messages[0].Content[0].Data)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width > 2 || cfg.Height > 2 {
		t.Fatalf("image dimensions %dx%d, error %v", cfg.Width, cfg.Height, err)
	}
}

func TestAntigravityEmptyResponsesFailOver(t *testing.T) {
	t.Setenv("PI_AI_ANTIGRAVITY_VERSION", "2.26.0")
	var firstCount, secondCount int
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		firstCount++
		fmt.Fprint(w, "data: {\"response\":{\"candidates\":[{\"finishReason\":\"STOP\"}]}}\n\n")
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		secondCount++
		fmt.Fprint(w, "data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"answer\"}]},\"finishReason\":\"STOP\"}]}}\n\n")
	}))
	defer second.Close()
	run := &antigravityRun{
		executor: NewAntigravityExecutor(&config.Config{}),
		prepared: &antigravityPrepared{model: &registry.NeoPiModel{}, credential: antigravityCredential{accessToken: "token"},
			body: []byte("{}"), endpoints: []string{first.URL, second.URL}, firstEvent: time.Second},
		client: first.Client(),
		emit:   func([]byte) error { return nil },
	}
	if err := run.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if firstCount != antigravitywire.MaxEmptyStreamRetries+1 || secondCount != 1 {
		t.Fatalf("attempt counts = %d, %d", firstCount, secondCount)
	}
}

func TestAntigravityRejectsUnsupportedEffort(t *testing.T) {
	model := &registry.NeoPiModel{ID: "test", Reasoning: true, Thinking: &registry.NeoPiThinking{Efforts: []string{"low", "high"}}}
	_, err := antigravitywire.MapSimpleOptions(model, antigravitywire.SimpleOptions{Reasoning: "medium"})
	if err == nil || !strings.Contains(err.Error(), "Thinking effort medium is not supported") {
		t.Fatalf("unsupported effort error = %v", err)
	}
}

func TestAntigravityStatusErrorPreservesBody(t *testing.T) {
	const body = "  untrimmed upstream error  \n"
	if got := antigravityStatusError(http.StatusBadRequest, []byte(body), "").Error(); got != "Cloud Code Assist API error (400): "+body {
		t.Fatalf("status error = %q", got)
	}
	if got := extractGoogleValidationURL(`prefix {"error":{"details":[{"reason":"VALIDATION_REQUIRED","metadata":{"validation_url":"https://example.test"}}]}} trailing`); got != "" {
		t.Fatalf("accepted malformed JSON validation URL %q", got)
	}
}

func TestAntigravityEmptyRetryUsesRedirectTarget(t *testing.T) {
	t.Setenv("PI_AI_ANTIGRAVITY_VERSION", "2.26.0")
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path != "/redirected" {
			http.Redirect(w, r, "/redirected", http.StatusTemporaryRedirect)
			return
		}
		if len(paths) == 2 {
			fmt.Fprint(w, "data: {\"response\":{\"candidates\":[{\"finishReason\":\"STOP\"}]}}\n\n")
			return
		}
		fmt.Fprint(w, "data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"answer\"}]},\"finishReason\":\"STOP\"}]}}\n\n")
	}))
	defer server.Close()
	run := &antigravityRun{
		executor: NewAntigravityExecutor(&config.Config{}),
		prepared: &antigravityPrepared{model: &registry.NeoPiModel{}, credential: antigravityCredential{accessToken: "token"},
			body: []byte("{}"), endpoints: []string{server.URL}, firstEvent: time.Second},
		client: server.Client(),
		emit:   func([]byte) error { return nil },
	}
	if err := run.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(paths, ",") != "/v1internal:streamGenerateContent,/redirected,/redirected" {
		t.Fatalf("request paths = %v", paths)
	}
}
