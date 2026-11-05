package antigravitywire

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
	"time"
)

// imagesCapture is testdata/antigravity_images_capture.json: NeoPi's OWN
// generateAntigravityImage (capture_antigravity_images.ts) against a scripted
// loopback Cloud Code Assist.
type imagesCapture struct {
	Now         int64  `json:"now"`
	RandomID    string `json:"randomId"`
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
			Text  string `json:"text"`
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

type imageSent struct {
	method, url, body string
	headers           map[string]string
}

var imageRequestIDField = regexp.MustCompile(`"requestId":"([^"]+)"`)

func TestGenerateImageMatchesNeoPi(t *testing.T) {
	t.Setenv("PI_AI_ANTIGRAVITY_VERSION", "2.26.0")
	t.Setenv("PI_AI_ANTIGRAVITY_OS", "linux")
	t.Setenv("PI_AI_ANTIGRAVITY_ARCH", "amd64")
	t.Setenv("PI_AI_ANTIGRAVITY_CL", "")
	data, err := os.ReadFile("testdata/antigravity_images_capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture imagesCapture
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	if len(capture.Scenarios) == 0 {
		t.Fatal("capture has no scenarios")
	}

	for _, scenario := range capture.Scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			var (
				mu   sync.Mutex
				sent []imageSent
				next int
			)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				step := struct {
					Status int
					Body   string
					SSE    bool
				}{Status: 599, Body: "capture script exhausted"}
				if next < len(scenario.Steps) {
					captured := scenario.Steps[next]
					step.Status, step.Body, step.SSE = captured.Status, captured.Body, captured.SSE
				}
				next++
				mu.Unlock()
				contentType := "application/json"
				if step.SSE {
					contentType = "text/event-stream"
				}
				w.Header().Set("Content-Type", contentType)
				w.WriteHeader(step.Status)
				_, _ = io.WriteString(w, step.Body)
			}))
			defer server.Close()
			target, err := url.Parse(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
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
					if strings.EqualFold(name, "Content-Length") {
						continue
					}
					headers[strings.ToLower(name)] = strings.Join(values, ", ")
				}
				mu.Lock()
				sent = append(sent, imageSent{method: req.Method, url: original, headers: headers, body: string(body)})
				mu.Unlock()
				req.URL.Scheme, req.URL.Host, req.Host = target.Scheme, target.Host, target.Host
				return server.Client().Transport.RoundTrip(req)
			})}

			requestID := capture.RandomID
			for _, request := range scenario.Requests {
				if match := imageRequestIDField.FindStringSubmatch(request.Body); match != nil {
					requestID = match[1]
					break
				}
			}
			request := ImageRequest{
				Prompt:      scenario.Request.Prompt,
				AspectRatio: scenario.Request.AspectRatio,
				ImageSize:   scenario.Request.ImageSize,
				Count:       scenario.Request.Count,
			}
			for _, image := range scenario.Request.InputImages {
				request.InputImages = append(request.InputImages, ImageInput{Data: image.Data, MimeType: image.MimeType})
			}
			result, err := GenerateImage(context.Background(), client, ImageParams{
				ModelID:        scenario.Model.ID,
				RequestModelID: scenario.Model.RequestModelID,
				BaseURL:        scenario.Model.BaseURL,
				Token:          capture.Credentials.Token,
				ProjectID:      capture.Credentials.ProjectID,
				RequestID:      requestID,
			}, request)

			mu.Lock()
			got := append([]imageSent(nil), sent...)
			mu.Unlock()
			if len(got) != len(scenario.Requests) {
				t.Fatalf("requests = %d, want %d: %+v", len(got), len(scenario.Requests), got)
			}
			for index, want := range scenario.Requests {
				actual := got[index]
				if actual.method != want.Method || actual.url != want.URL {
					t.Errorf("request %d = %s %s, want %s %s", index, actual.method, actual.url, want.Method, want.URL)
				}
				if actual.body != want.Body {
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
				if err == nil {
					t.Fatalf("error = nil, want %s: %s", scenario.Error.Name, scenario.Error.Message)
				}
				switch scenario.Error.Name {
				case "ValidationError":
					var invalid *ImageValidationError
					if !errors.As(err, &invalid) || invalid.Message != scenario.Error.Message {
						t.Fatalf("error = %v (%T), want validation %q", err, err, scenario.Error.Message)
					}
				case "ImageApiError":
					var api *ImageAPIError
					if !errors.As(err, &api) || api.Message != scenario.Error.Message || scenario.Error.Status == nil || api.Status != *scenario.Error.Status {
						t.Fatalf("error = %v (%T), want API error %d %q", err, err, valueOr(scenario.Error.Status), scenario.Error.Message)
					}
				case "SyntaxError":
					if !errors.Is(err, ErrMalformedEvent) {
						t.Fatalf("error = %v, want a malformed event", err)
					}
				default:
					t.Fatalf("unhandled captured error %s: %s (got %v)", scenario.Error.Name, scenario.Error.Message, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			want := scenario.Result
			if want == nil {
				t.Fatal("scenario without result or error")
			}
			if len(result.Images) != len(want.Images) {
				t.Fatalf("images = %+v, want %+v", result.Images, want.Images)
			}
			for index, image := range want.Images {
				if result.Images[index].Data != image.Data || result.Images[index].MimeType != image.MimeType {
					t.Errorf("image %d = %+v, want %+v", index, result.Images[index], image)
				}
			}
			if result.Text != want.Text {
				t.Errorf("text = %q, want %q", result.Text, want.Text)
			}
			if result.Usage.Input != want.Usage.Input || result.Usage.Output != want.Usage.Output {
				t.Errorf("usage = %+v, want %v/%v", result.Usage, want.Usage.Input, want.Usage.Output)
			}
		})
	}
}

func valueOr(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestImageRequestIDShape(t *testing.T) {
	id := ImageRequestID(time.UnixMilli(1_800_000_000_000))
	if !regexp.MustCompile(`^agent-1800000000000-[0-9a-z]{9}$`).MatchString(id) {
		t.Fatalf("request id = %q", id)
	}
	if other := ImageRequestID(time.UnixMilli(1_800_000_000_000)); other == id {
		t.Fatalf("request ids repeat: %q", id)
	}
}

func TestImageEndpointsDeduplicate(t *testing.T) {
	for _, tc := range []struct {
		base string
		want []string
	}{
		{DailyEndpoint, []string{DailyEndpoint, SandboxEndpoint}},
		{DailyEndpoint + "///", []string{DailyEndpoint, SandboxEndpoint}},
		{SandboxEndpoint, []string{SandboxEndpoint, DailyEndpoint}},
		{"https://proxy.example.com/", []string{"https://proxy.example.com", DailyEndpoint, SandboxEndpoint}},
	} {
		got := ImageEndpoints(tc.base)
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("ImageEndpoints(%q) = %v, want %v", tc.base, got, tc.want)
		}
	}
}
