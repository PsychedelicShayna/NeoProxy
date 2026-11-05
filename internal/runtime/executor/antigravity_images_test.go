package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type antigravityImageRewrite func(*http.Request) (*http.Response, error)

func (f antigravityImageRewrite) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

var antigravityImageRequestID = regexp.MustCompile(`"requestId":"agent-\d+-[0-9a-z]{9}"`)

// TestAntigravityExecutorImagesReplayNeoPi sends each captured NeoPi image
// generation as the OpenAI Images request a client gives NeoProxy and requires
// the upstream requests NeoPi sent, answered the way the capture's fake
// answered: the same discovery, endpoint ladder, envelope, images and usage.
func TestAntigravityExecutorImagesReplayNeoPi(t *testing.T) {
	t.Setenv("PI_AI_ANTIGRAVITY_VERSION", "2.26.0")
	t.Setenv("PI_AI_ANTIGRAVITY_OS", "linux")
	t.Setenv("PI_AI_ANTIGRAVITY_ARCH", "amd64")
	t.Setenv("PI_AI_ANTIGRAVITY_CL", "")
	data, err := os.ReadFile("../antigravitywire/testdata/antigravity_images_capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Credentials struct {
			Token     string `json:"token"`
			ProjectID string `json:"projectId"`
		} `json:"credentials"`
		Scenarios []struct {
			Name  string `json:"name"`
			Model struct {
				ID             string `json:"id"`
				BaseURL        string `json:"baseUrl"`
				RequestModelID string `json:"requestModelId"`
			} `json:"model"`
			Request struct {
				Prompt      string `json:"prompt"`
				InputImages []struct {
					Data     string `json:"data"`
					MimeType string `json:"mimeType"`
				} `json:"inputImages"`
				AspectRatio string `json:"aspectRatio"`
				ImageSize   string `json:"imageSize"`
				Count       *int   `json:"count"`
			} `json:"request"`
			Steps []struct {
				Status int    `json:"status"`
				Body   string `json:"body"`
				SSE    bool   `json:"sse"`
			} `json:"steps"`
			Requests []struct {
				Method  string            `json:"method"`
				URL     string            `json:"url"`
				Headers map[string]string `json:"headers"`
				Body    string            `json:"body"`
			} `json:"requests"`
			Result *struct {
				Images []struct {
					Data     string `json:"data"`
					MimeType string `json:"mimeType"`
				} `json:"images"`
				Usage struct {
					Input  float64 `json:"input"`
					Output float64 `json:"output"`
				} `json:"usage"`
			} `json:"result"`
			Error *struct {
				Name    string `json:"name"`
				Message string `json:"message"`
				Status  *int   `json:"status"`
			} `json:"error"`
		} `json:"scenarios"`
	}
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}

	replayed := 0
	exec := NewAntigravityExecutor(&config.Config{})
	for _, scenario := range capture.Scenarios {
		// The bundled record is the gemini-3-pro-image row with no wire id; the
		// wire package replays the rest. A zero count is not a valid Images API n.
		if scenario.Model.ID != "gemini-3-pro-image" || scenario.Model.RequestModelID != "" ||
			(scenario.Request.Count != nil && *scenario.Request.Count == 0) {
			continue
		}
		replayed++
		t.Run(scenario.Name, func(t *testing.T) {
			var (
				mu   sync.Mutex
				sent []struct {
					method, url, body string
					headers           map[string]string
				}
				next int
			)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				status, body, sse := 599, "capture script exhausted", false
				if next < len(scenario.Steps) {
					step := scenario.Steps[next]
					status, body, sse = step.Status, step.Body, step.SSE
				}
				next++
				mu.Unlock()
				contentType := "application/json"
				if sse {
					contentType = "text/event-stream"
				}
				w.Header().Set("Content-Type", contentType)
				w.WriteHeader(status)
				_, _ = io.WriteString(w, body)
			}))
			defer server.Close()
			target, errParse := url.Parse(server.URL)
			if errParse != nil {
				t.Fatal(errParse)
			}
			rewrite := antigravityImageRewrite(func(req *http.Request) (*http.Response, error) {
				original := req.URL.String()
				var body []byte
				if req.Body != nil {
					body, _ = io.ReadAll(req.Body)
					_ = req.Body.Close()
					req.Body = io.NopCloser(bytes.NewReader(body))
					req.ContentLength = int64(len(body))
				}
				headers := map[string]string{}
				for name, values := range req.Header {
					if !strings.EqualFold(name, "Content-Length") {
						headers[strings.ToLower(name)] = strings.Join(values, ", ")
					}
				}
				mu.Lock()
				sent = append(sent, struct {
					method, url, body string
					headers           map[string]string
				}{req.Method, original, string(body), headers})
				mu.Unlock()
				req.URL.Scheme, req.URL.Host, req.Host = target.Scheme, target.Host, target.Host
				return server.Client().Transport.RoundTrip(req)
			})
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(rewrite)) //nolint:staticcheck // the executors read this exact key

			// The Images API body a client sends for NeoPi's request.
			body := []byte(`{"model":"gemini-3-pro-image"}`)
			body, _ = sjson.SetBytes(body, "prompt", scenario.Request.Prompt)
			if scenario.Request.AspectRatio != "" {
				body, _ = sjson.SetBytes(body, "aspect_ratio", scenario.Request.AspectRatio)
			}
			if scenario.Request.ImageSize != "" {
				body, _ = sjson.SetBytes(body, "image_size", scenario.Request.ImageSize)
			}
			if scenario.Request.Count != nil {
				body, _ = sjson.SetBytes(body, "n", *scenario.Request.Count)
			}
			path := "/v1/images/generations"
			for index, image := range scenario.Request.InputImages {
				path = "/v1/images/edits"
				body, _ = sjson.SetBytes(body, "images."+string(rune('0'+index))+".image_url", "data:"+image.MimeType+";base64,"+image.Data)
			}
			auth := &cliproxyauth.Auth{ID: "antigravity-image", Provider: "antigravity",
				Metadata:   map[string]any{"access_token": capture.Credentials.Token, "project_id": capture.Credentials.ProjectID},
				Attributes: map[string]string{}}
			if base := scenario.Model.BaseURL; base != "https://daily-cloudcode-pa.googleapis.com" {
				auth.Attributes["base_url"] = base
			}
			opts := cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FromString("openai-image"),
				Headers:      http.Header{"Content-Type": []string{"application/json"}},
				Metadata:     map[string]any{cliproxyexecutor.RequestPathMetadataKey: path},
			}
			response, errExec := exec.Execute(ctx, auth, cliproxyexecutor.Request{Model: "gemini-3-pro-image", Payload: body}, opts)

			mu.Lock()
			got := sent
			mu.Unlock()
			if len(got) != len(scenario.Requests) {
				t.Fatalf("requests = %d, want %d", len(got), len(scenario.Requests))
			}
			for index, want := range scenario.Requests {
				actual := got[index]
				if actual.method != want.Method || actual.url != want.URL {
					t.Errorf("request %d = %s %s, want %s %s", index, actual.method, actual.url, want.Method, want.URL)
				}
				normalized := func(text string) string {
					return antigravityImageRequestID.ReplaceAllString(text, `"requestId":"<id>"`)
				}
				if normalized(actual.body) != normalized(want.Body) {
					t.Errorf("request %d body:\n got %s\nwant %s", index, actual.body, want.Body)
				}
				wantHeaders := map[string]string{}
				for name, value := range want.Headers {
					if name != "content-length" && name != "host" {
						wantHeaders[name] = value
					}
				}
				if len(actual.headers) != len(wantHeaders) {
					t.Errorf("request %d headers:\n got %v\nwant %v", index, actual.headers, wantHeaders)
				}
				for name, value := range wantHeaders {
					if actual.headers[name] != value {
						t.Errorf("request %d header %s = %q, want %q", index, name, actual.headers[name], value)
					}
				}
			}

			if scenario.Error != nil {
				if errExec == nil {
					t.Fatalf("error = nil, want %q", scenario.Error.Message)
				}
				var status statusErr
				if !errors.As(errExec, &status) {
					if scenario.Error.Name == "SyntaxError" {
						return
					}
					t.Fatalf("error %v (%T) carries no status", errExec, errExec)
				}
				switch scenario.Error.Name {
				case "ValidationError":
					if status.code != http.StatusBadRequest || status.msg != "Invalid request: "+scenario.Error.Message {
						t.Errorf("error = %d %q", status.code, status.msg)
					}
				case "ImageApiError":
					if scenario.Error.Status == nil || status.code != *scenario.Error.Status || status.msg != scenario.Error.Message {
						t.Errorf("error = %d %q, want %v %q", status.code, status.msg, scenario.Error.Status, scenario.Error.Message)
					}
				}
				return
			}
			if errExec != nil {
				t.Fatalf("unexpected error: %v", errExec)
			}
			parsed := gjson.ParseBytes(response.Payload)
			wantImages := scenario.Result.Images
			if got := len(parsed.Get("data").Array()); got != len(wantImages) {
				t.Fatalf("images = %d, want %d: %s", got, len(wantImages), response.Payload)
			}
			for index, image := range wantImages {
				if parsed.Get("data."+string(rune('0'+index))+".b64_json").String() != image.Data {
					t.Errorf("image %d data mismatch", index)
				}
			}
			if parsed.Get("usage.input_tokens").Float() != scenario.Result.Usage.Input ||
				parsed.Get("usage.output_tokens").Float() != scenario.Result.Usage.Output {
				t.Errorf("usage = %s, want %v/%v", parsed.Get("usage"), scenario.Result.Usage.Input, scenario.Result.Usage.Output)
			}
		})
	}
	if replayed == 0 {
		t.Fatal("no capture scenario was replayed")
	}
}

func TestAntigravityImageCallShapesNeoPiRequest(t *testing.T) {
	opts := func(path string) cliproxyexecutor.Options {
		return cliproxyexecutor.Options{
			SourceFormat: sdktranslator.FromString("openai-image"),
			Headers:      http.Header{"Content-Type": []string{"application/json"}},
			Metadata:     map[string]any{cliproxyexecutor.RequestPathMetadataKey: path},
		}
	}
	call, err := antigravityPrepareImageCall(cliproxyexecutor.Request{Payload: []byte(
		`{"model":"gemini-3-pro-image","prompt":"a fox","n":2,"size":"1792x1024","response_format":"url"}`)}, opts("/v1/images/generations"))
	if err != nil {
		t.Fatal(err)
	}
	if call.request.Prompt != "a fox" || call.request.Count == nil || *call.request.Count != 2 ||
		call.request.AspectRatio != "16:9" || call.request.ImageSize != "" || call.responseFormat != "url" ||
		call.streamPrefix != "image_generation" {
		t.Fatalf("call = %+v", call)
	}
	if _, err = antigravityPrepareImageCall(cliproxyexecutor.Request{Payload: []byte(`{"prompt":"x"}`)}, opts("/v1/images/edits")); err == nil {
		t.Fatal("an edit without an image must be rejected")
	}
	if _, err = antigravityPrepareImageCall(cliproxyexecutor.Request{Payload: []byte(`{"prompt":"  "}`)}, opts("/v1/images/generations")); err == nil {
		t.Fatal("an empty prompt must be rejected")
	}
	if _, err = antigravityPrepareImageCall(cliproxyexecutor.Request{Payload: []byte(`{"prompt":"x","image":"https://example.com/a.png"}`)}, opts("/v1/images/edits")); err == nil {
		t.Fatal("a remote image URL must be rejected")
	}
	for size, want := range map[string]string{"1024x1024": "1:1", "1024x1536": "2:3", "1536x1024": "3:2", "auto": "", "": "", "1920x1080": "16:9"} {
		if got := antigravityAspectRatioFromSize(size); got != want {
			t.Errorf("aspect ratio for %q = %q, want %q", size, got, want)
		}
	}
}
