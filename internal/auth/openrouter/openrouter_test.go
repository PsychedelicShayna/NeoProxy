package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
)

// signInCapture is testdata/neopi_openrouter_auth_capture.json: NeoPi's OWN
// openrouter flow against synthetic replies.
type signInCapture struct {
	Scenarios []struct {
		Name        string `json:"name"`
		Code        string `json:"code"`
		RedirectURI string `json:"redirectUri"`
		Auth        struct {
			URL string `json:"url"`
		} `json:"auth"`
		Credential *struct {
			Access string `json:"access"`
		} `json:"credential"`
		Error    string                       `json:"error"`
		Token    *signInReply                 `json:"token"`
		Validate *signInReply                 `json:"validate"`
		Requests []map[string]json.RawMessage `json:"requests"`
	} `json:"scenarios"`
}

type signInReply struct {
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body"`
}

var (
	defaultToken    = signInReply{Body: json.RawMessage(`{"key":"sk-or-v1-fake","user_id":"user_fake"}`)}
	defaultValidate = signInReply{Body: json.RawMessage(`{"data":{"label":"fake"}}`)}
)

type redirect struct{ target *url.URL }

func (r redirect) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme, clone.URL.Host = r.target.Scheme, r.target.Host
	return http.DefaultTransport.RoundTrip(clone)
}

func loadCapture(t *testing.T) signInCapture {
	t.Helper()
	data, err := os.ReadFile("testdata/neopi_openrouter_auth_capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture signInCapture
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	return capture
}

// Every scenario NeoPi's own flow ran must put the same URL, headers and body on
// the wire from the port, and fail or succeed with the same words.
func TestSignInMatchesNeoPiCapture(t *testing.T) {
	for _, scenario := range loadCapture(t).Scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			type seen struct {
				method, url, body string
				header            http.Header
			}
			var got []seen
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				got = append(got, seen{r.Method, "https://openrouter.ai" + r.URL.RequestURI(), string(raw), r.Header.Clone()})
				reply := defaultToken
				if scenario.Token != nil {
					reply = *scenario.Token
				}
				if strings.HasSuffix(r.URL.Path, "/auth/key") {
					reply = defaultValidate
					if scenario.Validate != nil {
						reply = *scenario.Validate
					}
				}
				status := reply.Status
				if status == 0 {
					status = http.StatusOK
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				var compact bytes.Buffer
				_ = json.Compact(&compact, reply.Body)
				_, _ = w.Write(compact.Bytes())
			}))
			defer server.Close()
			target, _ := url.Parse(server.URL)
			svc := NewService(&http.Client{Transport: redirect{target}})

			const verifier = "verifier-fixed"
			wantURL, _ := url.Parse(scenario.Auth.URL)
			gotURL, _ := url.Parse(svc.AuthorizationURL("CHALLENGE"))
			gotQuery, wantQuery := gotURL.Query(), wantURL.Query()
			wantQuery.Set("code_challenge", "CHALLENGE")
			if gotURL.Scheme+"://"+gotURL.Host+gotURL.Path != wantURL.Scheme+"://"+wantURL.Host+wantURL.Path || gotQuery.Encode() != wantQuery.Encode() {
				t.Fatalf("authorize URL %s, NeoPi %s", gotURL, wantURL)
			}
			if names := strings.Split(strings.TrimPrefix(gotURL.RawQuery, ""), "&"); len(names) != 3 || !strings.HasPrefix(names[0], "callback_url=") || !strings.HasPrefix(names[1], "code_challenge=") || !strings.HasPrefix(names[2], "code_challenge_method=") {
				t.Fatalf("authorize params out of NeoPi's order: %s", gotURL.RawQuery)
			}

			code := scenario.Code
			if code == "" {
				code = "the-code"
			}
			var key string
			var err error
			if IsOpenRouterKey(code) {
				err = svc.ValidateKey(context.Background(), code)
				key = code
			} else {
				code, _, _ = strings.Cut(code, "#")
				key, err = svc.ExchangeCode(context.Background(), code, verifier)
			}
			if scenario.Error != "" {
				if err == nil || err.Error() != scenario.Error && !strings.HasSuffix(err.Error(), scenario.Error) {
					t.Fatalf("error %v, NeoPi %q", err, scenario.Error)
				}
			} else if err != nil || scenario.Credential == nil || key != scenario.Credential.Access {
				t.Fatalf("key %q error %v, NeoPi %+v", key, err, scenario.Credential)
			}

			if len(got) != len(scenario.Requests) {
				t.Fatalf("%d requests, NeoPi sent %d", len(got), len(scenario.Requests))
			}
			for i, want := range scenario.Requests {
				var method, wantURL, body string
				var headers map[string]string
				_ = json.Unmarshal(want["method"], &method)
				_ = json.Unmarshal(want["url"], &wantURL)
				_ = json.Unmarshal(want["body"], &body)
				_ = json.Unmarshal(want["headers"], &headers)
				if got[i].method != method || got[i].url != wantURL {
					t.Errorf("request %s %s, NeoPi %s %s", got[i].method, got[i].url, method, wantURL)
				}
				if body != "" {
					// NeoPi's verifier is random per flow; the rest of the body is fixed.
					var parsed map[string]string
					_ = json.Unmarshal([]byte(body), &parsed)
					parsed["code_verifier"] = verifier
					ordered := `{"code":"` + parsed["code"] + `","code_verifier":"` + verifier + `","code_challenge_method":"` + parsed["code_challenge_method"] + `"}`
					if got[i].body != ordered {
						t.Errorf("body %s, NeoPi %s", got[i].body, ordered)
					}
				} else if got[i].body != "" {
					t.Errorf("unexpected body %s", got[i].body)
				}
				framing := map[string]bool{"host": true, "content-length": true, "accept-encoding": true, "connection": true}
				wantHeaders := http.Header{}
				for name, value := range headers {
					if !framing[name] {
						wantHeaders.Set(name, value)
					}
				}
				gotHeaders := got[i].header.Clone()
				for name := range framing {
					gotHeaders.Del(name)
				}
				if !reflect.DeepEqual(gotHeaders, wantHeaders) {
					t.Errorf("headers %v, NeoPi %v", gotHeaders, wantHeaders)
				}
			}
		})
	}
}
