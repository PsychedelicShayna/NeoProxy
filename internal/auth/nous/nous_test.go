package nous

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type captureReply struct {
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body"`
	Raw    *string         `json:"raw"`
}

type captureScenario struct {
	Name    string                    `json:"name"`
	Replies map[string][]captureReply `json:"replies"`
	Events  []struct {
		OnAuth *struct {
			URL          string `json:"url"`
			Instructions string `json:"instructions"`
		} `json:"onAuth"`
		OnProgress string `json:"onProgress"`
	} `json:"events"`
	Result   json.RawMessage              `json:"result"`
	Error    string                       `json:"error"`
	Delays   []int64                      `json:"delays"`
	Requests []map[string]json.RawMessage `json:"requests"`
}

type redirect struct{ target *url.URL }

func (r redirect) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set("X-Capture-Url", req.URL.String())
	clone.URL.Scheme, clone.URL.Host = r.target.Scheme, r.target.Host
	return http.DefaultTransport.RoundTrip(clone)
}

const captureEpoch = 1_700_000_000_000

// Every sign-in, refresh and discovery request is NeoPi's extension's, byte
// for byte, and every outcome matches: credentials, polling delays, errors.
func TestMatchesNeoPiExtensionCapture(t *testing.T) {
	data, err := os.ReadFile("testdata/neopi_nous_auth_capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Scenarios []captureScenario `json:"scenarios"`
	}
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range capture.Scenarios {
		// A pasted key never reaches the portal, and without a key NeoPi serves its snapshot.
		if scenario.Name == "pasted-key" || scenario.Name == "discovery-without-key-is-snapshot" {
			continue
		}
		t.Run(scenario.Name, func(t *testing.T) {
			var mu sync.Mutex
			var requests []map[string]any
			counters := map[string]int{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				original := r.Header.Get("X-Capture-Url")
				headers := map[string]any{}
				for name, values := range r.Header {
					lower := strings.ToLower(name)
					if lower != "x-capture-url" && lower != "content-length" {
						headers[lower] = strings.Join(values, ", ")
					}
				}
				mu.Lock()
				requests = append(requests, map[string]any{"method": r.Method, "url": original, "headers": headers, "body": string(body)})
				parsed, _ := url.Parse(original)
				key := r.Method + " " + parsed.Scheme + "://" + parsed.Host + parsed.Path
				replies := scenario.Replies[key]
				index := counters[key]
				counters[key]++
				mu.Unlock()
				if len(replies) == 0 {
					w.WriteHeader(599)
					return
				}
				reply := replies[min(index, len(replies)-1)]
				if reply.Status == 0 {
					reply.Status = http.StatusOK
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(reply.Status)
				if reply.Raw != nil {
					_, _ = w.Write([]byte(*reply.Raw))
					return
				}
				_, _ = w.Write(reply.Body)
			}))
			defer server.Close()
			target, _ := url.Parse(server.URL)
			client := NewClientWithHTTP(&http.Client{Transport: redirect{target: target}})
			clock := time.UnixMilli(captureEpoch)
			var delays []int64
			client.Now = func() time.Time { return clock }
			client.Sleep = func(_ context.Context, d time.Duration) error {
				delays = append(delays, d.Milliseconds())
				clock = clock.Add(d)
				return nil
			}
			ctx := context.Background()
			var token *Token
			var progress int
			switch {
			case strings.HasPrefix(scenario.Name, "refresh"):
				token, err = client.Refresh(ctx, "refresh-1")
			case strings.HasPrefix(scenario.Name, "discovery-"):
				_, err = client.FetchModelRows(ctx, "sk-nous-key")
				if scenario.Name == "discovery-live" && err != nil {
					t.Fatalf("discovery failed: %v", err)
				}
			default:
				var device *DeviceCode
				if device, err = client.RequestDeviceCode(ctx); err == nil {
					for _, event := range scenario.Events {
						if event.OnAuth != nil && (device.VerificationURL != event.OnAuth.URL || event.OnAuth.Instructions != "Enter code "+device.UserCode+" at portal.nousresearch.com") {
							t.Errorf("verification = %s / %s, want %+v", device.VerificationURL, device.UserCode, *event.OnAuth)
						}
					}
					token, err = client.WaitForToken(ctx, device, func(message string) {
						progress++
						if message != "Waiting for Nous Portal authorization…" {
							t.Errorf("progress = %q", message)
						}
					})
				}
			}
			isDiscoveryFallback := strings.HasPrefix(scenario.Name, "discovery-") && scenario.Name != "discovery-live"
			switch {
			case isDiscoveryFallback:
				// NeoPi swallows the failure and serves its snapshot; the request is what is pinned here.
			case scenario.Error != "":
				if err == nil || err.Error() != scenario.Error {
					t.Errorf("error = %v, want %q", err, scenario.Error)
				}
			case err != nil:
				t.Fatalf("failed: %v", err)
			case token != nil:
				var want struct {
					Access  string `json:"access"`
					Refresh string `json:"refresh"`
					Expires int64  `json:"expires"`
				}
				if err = json.Unmarshal(scenario.Result, &want); err != nil {
					t.Fatal(err)
				}
				if token.AccessToken != want.Access || token.RefreshToken != want.Refresh || token.ExpiresAt.UnixMilli() != want.Expires {
					t.Errorf("credential = %+v, want %+v", *token, want)
				}
			}
			if len(scenario.Delays) != len(delays) {
				t.Errorf("delays = %v, want %v", delays, scenario.Delays)
			} else {
				for i := range delays {
					if delays[i] != scenario.Delays[i] {
						t.Errorf("delays = %v, want %v", delays, scenario.Delays)
					}
				}
			}
			var polls int
			for _, event := range scenario.Events {
				if event.OnProgress != "" {
					polls++
				}
			}
			if progress != polls {
				t.Errorf("progress events = %d, want %d", progress, polls)
			}
			if len(requests) != len(scenario.Requests) {
				t.Fatalf("requests = %d, want %d", len(requests), len(scenario.Requests))
			}
			for i, want := range scenario.Requests {
				var wantHeaders map[string]any
				_ = json.Unmarshal(want["headers"], &wantHeaders)
				delete(wantHeaders, "host")
				delete(wantHeaders, "content-length")
				var wantMethod, wantURL, wantBody string
				_ = json.Unmarshal(want["method"], &wantMethod)
				_ = json.Unmarshal(want["url"], &wantURL)
				_ = json.Unmarshal(want["body"], &wantBody)
				gotJSON, _ := json.Marshal(map[string]any{"method": requests[i]["method"], "url": requests[i]["url"], "headers": requests[i]["headers"], "body": requests[i]["body"]})
				wantJSON, _ := json.Marshal(map[string]any{"method": wantMethod, "url": wantURL, "headers": wantHeaders, "body": wantBody})
				if string(gotJSON) != string(wantJSON) {
					t.Errorf("request %d:\n got %s\nwant %s", i, gotJSON, wantJSON)
				}
			}
		})
	}
}
