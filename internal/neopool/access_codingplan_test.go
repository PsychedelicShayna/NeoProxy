package neopool_test

import (
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopool"
)

// codingPlanUpstream answers every key check with success and records which
// real endpoint each one addressed.
type codingPlanUpstream struct {
	mu   sync.Mutex
	seen []string
}

func (u *codingPlanUpstream) RoundTrip(req *http.Request) (*http.Response, error) {
	u.mu.Lock()
	u.seen = append(u.seen, req.Method+" "+req.URL.String())
	u.mu.Unlock()
	body := `{"data":[{"id":"qwen3.5-plus"}],"choices":[{"message":{"content":"pong"}}]}`
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
}

func (u *codingPlanUpstream) take() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	seen := u.seen
	u.seen = nil
	return seen
}

// NeoPi's Coding Plan prompt decides the key check by the option chosen:
// International and China send a chat probe, Custom lists models even when
// it names a regional endpoint. Only an administrator may choose Custom.
func TestCodingPlanKeyCheckFollowsEndpointChoice(t *testing.T) {
	f := newAccessFixture(t)
	upstream := &codingPlanUpstream{}
	t.Cleanup(neopool.SetAccessTransportForTest(upstream))
	const intl, china = "https://coding-intl.dashscope.aliyuncs.com/v1", "https://coding.dashscope.aliyuncs.com/v1"

	cases := []struct {
		name, body, want string
	}{
		{"china", `{"provider":"alibaba-coding-plan","apiKey":"sk-synthetic-cn","baseUrl":"` + china + `"}`, "POST " + china + "/chat/completions"},
		{"custom-names-intl", `{"provider":"alibaba-coding-plan","apiKey":"sk-synthetic-custom","baseUrl":"` + intl + `/","customBaseUrl":true}`, "GET " + intl + "/models"},
	}
	for _, tc := range cases {
		itemOf(t, f.json("admin", "POST", "/v8/neo/access/api-key", tc.body, 201))
		if seen := upstream.take(); len(seen) != 1 || seen[0] != tc.want {
			t.Fatalf("%s: key checks = %v, want [%s]", tc.name, seen, tc.want)
		}
	}
	cfg, err := os.ReadFile(f.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"base-url = '" + china + "'", "base-url = '" + intl + "'"} {
		if !strings.Contains(string(cfg), want) {
			t.Fatalf("engine config lacks %q:\n%s", want, cfg)
		}
	}

	w := f.do("owner-a", "POST", "/v8/neo/access/api-key", []byte(`{"provider":"alibaba-coding-plan","apiKey":"sk-synthetic-owner","baseUrl":"`+intl+`","customBaseUrl":true}`), "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("owner chose Custom: %d %s", w.Code, w.Body.String())
	}
	if seen := upstream.take(); len(seen) != 0 {
		t.Fatalf("refused Custom still checked the key: %v", seen)
	}
}
