package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAntigravityManualCode(t *testing.T) {
	const state = "0123456789abcdef"
	for _, tc := range []struct {
		name  string
		input string
		code  string
		ok    bool
	}{
		{"redirect url", "http://127.0.0.1:51121/oauth-callback?code=4%2F0Acode&state=" + state, "4/0Acode", true},
		{"redirect url without state", "http://127.0.0.1:51121/oauth-callback?code=4%2F0Acode", "4/0Acode", true},
		{"query string", "code=4%2F0Acode&state=" + state, "4/0Acode", true},
		{"bare code", "4/0AbareCode", "4/0AbareCode", true},
		{"bare code with state", "4/0AbareCode#" + state, "4/0AbareCode", true},
		{"foreign state is ignored", "http://127.0.0.1:51121/oauth-callback?code=x&state=forged", "", false},
		{"bare code with foreign state is ignored", "4/0AbareCode#forged", "", false},
		{"denial is ignored", "http://127.0.0.1:51121/oauth-callback?error=access_denied&state=" + state, "", false},
		{"empty", "  ", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, ok := antigravityManualCode(tc.input, state)
			if code != tc.code || ok != tc.ok {
				t.Fatalf("antigravityManualCode(%q) = %q, %v; want %q, %v", tc.input, code, ok, tc.code, tc.ok)
			}
		})
	}
}

// TestAntigravityCallbackServerSettlesLikeNeoPi covers the loopback handler:
// forged callbacks answer with the failure page but never settle the login, a
// denial carrying our state does, and the first genuine callback wins.
func TestAntigravityCallbackServerSettlesLikeNeoPi(t *testing.T) {
	const state = "0123456789abcdef"
	server := &antigravityCallbackServer{results: make(chan antigravityCallbackResult, 1), expectedState: state}
	get := func(target string) int {
		recorder := httptest.NewRecorder()
		server.handle(recorder, httptest.NewRequest(http.MethodGet, target, nil))
		return recorder.Code
	}
	pending := func() bool {
		select {
		case result := <-server.results:
			t.Fatalf("login settled by a forged callback: %+v", result)
			return true
		default:
			return false
		}
	}

	if status := get("/other?code=x&state=" + state); status != http.StatusNotFound {
		t.Fatalf("other path status = %d, want 404", status)
	}
	if status := get("/oauth-callback?code=x&state=forged"); status != http.StatusInternalServerError {
		t.Fatalf("forged state status = %d, want 500", status)
	}
	if status := get("/oauth-callback?error=access_denied&state=forged"); status != http.StatusInternalServerError {
		t.Fatalf("forged denial status = %d, want 500", status)
	}
	if status := get("/oauth-callback?state=" + state); status != http.StatusInternalServerError {
		t.Fatalf("missing code status = %d, want 500", status)
	}
	if pending() {
		t.Fatal("unexpected settled result")
	}

	if status := get("/oauth-callback?error=access_denied&error_description=User+said+no&state=" + state); status != http.StatusInternalServerError {
		t.Fatalf("genuine denial status = %d, want 500", status)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := server.wait(ctx, time.Second)
	if err != nil || result.Error != "Authorization failed: User said no" {
		t.Fatalf("denial result = %+v, %v", result, err)
	}

	if status := get("/oauth-callback?code=4%2F0Acode&state=" + state); status != http.StatusOK {
		t.Fatalf("genuine callback status = %d, want 200", status)
	}
	result, err = server.wait(ctx, time.Second)
	if err != nil || result.Code != "4/0Acode" || result.Error != "" {
		t.Fatalf("code result = %+v, %v", result, err)
	}
}
