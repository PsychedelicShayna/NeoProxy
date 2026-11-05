package meta

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

type captureRequest struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

type captureReply struct {
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body"`
}

type authCapture struct {
	Logins []struct {
		Name    string                     `json:"name"`
		Replies map[string]json.RawMessage `json:"replies"`
		Result  *struct {
			Access    string `json:"access"`
			AccountID string `json:"accountId"`
			Email     string `json:"email"`
		} `json:"result"`
		Error    string           `json:"error"`
		Requests []captureRequest `json:"requests"`
	} `json:"logins"`
	Usage []struct {
		Name     string                     `json:"name"`
		Replies  map[string]json.RawMessage `json:"replies"`
		Previous *struct {
			Limits []struct {
				ID     string `json:"id"`
				Label  string `json:"label"`
				Window struct {
					DurationMs int64 `json:"durationMs"`
					ResetsAt   int64 `json:"resetsAt"`
				} `json:"window"`
				Amount struct {
					Used float64 `json:"used"`
				} `json:"amount"`
			} `json:"limits"`
		} `json:"previous"`
		Report *struct {
			Limits []struct {
				ID    string `json:"id"`
				Label string `json:"label"`
				Scope struct {
					AccountID string `json:"accountId"`
					Tier      string `json:"tier"`
				} `json:"scope"`
				Window struct {
					DurationMs int64 `json:"durationMs"`
					ResetsAt   int64 `json:"resetsAt"`
				} `json:"window"`
				Amount struct {
					Used float64 `json:"used"`
				} `json:"amount"`
				Notes []string `json:"notes"`
			} `json:"limits"`
			Metadata struct {
				Email string `json:"email"`
			} `json:"metadata"`
		} `json:"report"`
		Error    string           `json:"error"`
		Requests []captureRequest `json:"requests"`
	} `json:"usage"`
}

func loadAuthCapture(t *testing.T) authCapture {
	t.Helper()
	raw, err := os.ReadFile("testdata/neopi_meta_auth_capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture authCapture
	if err = json.Unmarshal(raw, &capture); err != nil {
		t.Fatal(err)
	}
	return capture
}

// replayTransport answers each request path with its scripted replies in order
// (the last repeats) and records what it was sent, in the capture's shape.
type replayTransport struct {
	replies  map[string][]captureReply
	counters map[string]int
	requests []captureRequest
}

func newReplayTransport(t *testing.T, scripted map[string]json.RawMessage) *replayTransport {
	t.Helper()
	transport := &replayTransport{replies: map[string][]captureReply{}, counters: map[string]int{}}
	for path, raw := range scripted {
		var list []captureReply
		if err := json.Unmarshal(raw, &list); err != nil {
			var single captureReply
			if err = json.Unmarshal(raw, &single); err != nil {
				t.Fatal(err)
			}
			list = []captureReply{single}
		}
		for i := range list {
			var compact bytes.Buffer
			if err := json.Compact(&compact, list[i].Body); err != nil {
				t.Fatal(err)
			}
			list[i].Body = compact.Bytes()
		}
		transport.replies[path] = list
	}
	return transport
}

var framingHeaders = map[string]bool{"host": true, "content-length": true, "accept-encoding": true, "connection": true}

func (r *replayTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body := ""
	if req.Body != nil {
		data, _ := io.ReadAll(req.Body)
		body = string(data)
	}
	headers := map[string]string{}
	for name, values := range req.Header {
		if lower := strings.ToLower(name); !framingHeaders[lower] {
			headers[lower] = strings.Join(values, ", ")
		}
	}
	r.requests = append(r.requests, captureRequest{Method: req.Method, URL: req.URL.String(), Headers: headers, Body: body})
	replies := r.replies[req.URL.Path]
	if len(replies) == 0 {
		return &http.Response{StatusCode: 599, Body: io.NopCloser(strings.NewReader("unexpected")), Header: http.Header{}, Request: req}, nil
	}
	index := r.counters[req.URL.Path]
	if index >= len(replies) {
		index = len(replies) - 1
	}
	r.counters[req.URL.Path]++
	status := replies[index].Status
	if status == 0 {
		status = 200
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(replies[index].Body)), Header: http.Header{"Content-Type": {"application/json"}}, Request: req}, nil
}

func authWith(transport http.RoundTripper) *MetaAuth {
	auth := NewMetaAuth(nil)
	auth.httpClient = &http.Client{Transport: transport, Timeout: 30 * time.Second}
	return auth
}

func compareRequests(t *testing.T, got, want []captureRequest) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%d requests, NeoPi sent %d\ngot  %+v\nwant %+v", len(got), len(want), got, want)
	}
	for i := range want {
		source := captureRequest{Method: want[i].Method, URL: want[i].URL, Headers: map[string]string{}, Body: want[i].Body}
		for name, value := range want[i].Headers {
			if !framingHeaders[name] {
				source.Headers[name] = value
			}
		}
		if !reflect.DeepEqual(got[i], source) {
			t.Errorf("request %d\ngot    %+v\nsource %+v", i, got[i], source)
		}
	}
}

// TestMuseCodeLoginMatchesNeoPi replays NeoPi's own muse-code device sign-in
// (testdata/capture_neopi_meta_auth.ts): every request it sends and the
// credential or failure it ends in.
func TestMuseCodeLoginMatchesNeoPi(t *testing.T) {
	for _, scenario := range loadAuthCapture(t).Logins {
		t.Run(scenario.Name, func(t *testing.T) {
			transport := newReplayTransport(t, scenario.Replies)
			auth := authWith(transport)
			ctx := context.Background()
			var bundle *MetaAuthBundle
			dcr, err := auth.StartDeviceFlow(ctx)
			if err == nil {
				bundle, err = auth.WaitForAuthorization(ctx, dcr)
			}
			compareRequests(t, transport.requests, scenario.Requests)
			if scenario.Error != "" {
				if err == nil || err.Error() != scenario.Error {
					t.Fatalf("error %v, NeoPi %q", err, scenario.Error)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			storage := auth.CreateTokenStorage(bundle)
			wrapped := `{"oauthAccessToken":"` + storage.DCAToken + `","apiKey":"` + storage.APIKey + `"}`
			if wrapped != scenario.Result.Access {
				t.Errorf("credential %s, NeoPi %s", wrapped, scenario.Result.Access)
			}
			if storage.AccountID != scenario.Result.AccountID || storage.Email != scenario.Result.Email {
				t.Errorf("identity %q %q, NeoPi %q %q", storage.AccountID, storage.Email, scenario.Result.AccountID, scenario.Result.Email)
			}
			if storage.AccessToken != storage.APIKey {
				t.Errorf("requests must use the minted key, got %q", storage.AccessToken)
			}
		})
	}
}

// TestMuseCodeUsageMatchesNeoPi replays NeoPi's own subscription usage fetcher.
func TestMuseCodeUsageMatchesNeoPi(t *testing.T) {
	for _, scenario := range loadAuthCapture(t).Usage {
		t.Run(scenario.Name, func(t *testing.T) {
			transport := newReplayTransport(t, scenario.Replies)
			auth := authWith(transport)
			var previous *Usage
			if scenario.Previous != nil {
				previous = &Usage{}
				for _, limit := range scenario.Previous.Limits {
					previous.Limits = append(previous.Limits, UsageLimit{ID: limit.ID, Label: limit.Label, UsedPercent: limit.Amount.Used, DurationMs: limit.Window.DurationMs, ResetsAtMs: limit.Window.ResetsAt})
				}
			}
			usage, err := auth.FetchUsage(context.Background(), "dca:account-token-fake", previous, "cred@example.invalid", "cred-account", time.Now())
			compareRequests(t, transport.requests, scenario.Requests)
			if scenario.Error != "" {
				var usageErr *UsageError
				if err == nil || !asUsageError(err, &usageErr) || strings.TrimSpace(strconvStatus(usageErr.Status)+" "+usageErr.Message) != scenario.Error {
					t.Fatalf("error %v, NeoPi %q", err, scenario.Error)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if scenario.Report == nil {
				if usage != nil {
					t.Fatalf("report %+v, NeoPi reports nothing", usage)
				}
				return
			}
			if usage == nil || len(usage.Limits) != len(scenario.Report.Limits) {
				t.Fatalf("limits %+v, NeoPi %+v", usage, scenario.Report.Limits)
			}
			for i, want := range scenario.Report.Limits {
				got := usage.Limits[i]
				if got.ID != want.ID || got.Label != want.Label || got.UsedPercent != want.Amount.Used || got.DurationMs != want.Window.DurationMs ||
					got.ResetsAtMs != want.Window.ResetsAt || !reflect.DeepEqual(got.Notes, want.Notes) {
					t.Errorf("limit %d\ngot  %+v\nwant %+v", i, got, want)
				}
				if usage.AccountID != want.Scope.AccountID || usage.Tier != want.Scope.Tier {
					t.Errorf("limit %d scope %q/%q, NeoPi %q/%q", i, usage.AccountID, usage.Tier, want.Scope.AccountID, want.Scope.Tier)
				}
			}
			if usage.Email != scenario.Report.Metadata.Email {
				t.Errorf("email %q, NeoPi %q", usage.Email, scenario.Report.Metadata.Email)
			}
		})
	}
}

func asUsageError(err error, target **UsageError) bool {
	usageErr, ok := err.(*UsageError)
	if ok {
		*target = usageErr
	}
	return ok
}

func strconvStatus(status int) string {
	raw, _ := json.Marshal(status)
	return string(raw)
}

// A passed weekly reset rolls forward by whole weeks and reads as unused.
func TestIdleUsageRollsPassedWeeklyReset(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	reset := now.Add(-10 * 24 * time.Hour).UnixMilli()
	limits := buildIdleLimits(&Usage{Limits: []UsageLimit{
		{ID: "300m", Label: "5 Hours", DurationMs: 300 * 60_000},
		{ID: weeklyLimitID, Label: "Weekly", UsedPercent: 40, DurationMs: weekMs, ResetsAtMs: reset},
	}}, now)
	if len(limits) != 2 || limits[1].UsedPercent != 0 || limits[1].ResetsAtMs != reset+2*weekMs {
		t.Fatalf("limits %+v", limits)
	}
	if limits[1].Notes[0] != "Reset since the last reported usage" {
		t.Errorf("note %q", limits[1].Notes[0])
	}
}
