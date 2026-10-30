package neopool

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
)

func TestHTTPEnforcesOwnedInventoryAndMutationAuthority(t *testing.T) {
	f := newFixture(t)
	f.assign(t, 0, "owner-a")
	f.assign(t, 1, "owner-b")
	f.meter(t, 0, "day", 100)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handlers := NewHTTP(f.pool, nil)
	handlers.SetAccountVisibility(func(_ context.Context, _ neo.Principal) ([]string, error) { return []string{f.accounts[0].ID}, nil })
	handlers.Register(router.Group("/v8/neo", neo.NewHTTP(f.identity).SessionMiddleware()))
	tokens := map[string]string{}
	for _, id := range []string{"admin", "owner-a", "owner-b", "user-a"} {
		sum := sha256.Sum256([]byte(id))
		token := hex.EncodeToString(sum[:])
		hash := sha256.Sum256([]byte(token))
		if _, err := f.identity.DB().Exec(`INSERT INTO neo_sessions(token_hash,user_id,expires_at,created_at) VALUES(?,?,?,0)`, hex.EncodeToString(hash[:]), id, int64(1<<62)); err != nil {
			t.Fatal(err)
		}
		tokens[id] = token
	}
	request := func(user, method, path, body string, csrf bool) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if user != "" {
			req.AddCookie(&http.Cookie{Name: neo.SessionCookie, Value: tokens[user]})
		}
		if csrf {
			req.Header.Set("X-Neoproxy-CSRF", "1")
		}
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	w := request("owner-a", "GET", "/v8/neo/vault", "", false)
	if w.Code != 200 {
		t.Fatalf("vault: %d %s", w.Code, w.Body.String())
	}
	var vault struct {
		Accounts []Account `json:"accounts"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &vault); err != nil {
		t.Fatal(err)
	}
	if len(vault.Accounts) != 1 || vault.Accounts[0].ID != f.accounts[0].ID || vault.Accounts[0].OwnerID != "owner-a" {
		t.Fatalf("owner read leaked another account: %s", w.Body.String())
	}
	checks := []struct {
		user, method, path, body string
		csrf                     bool
		want                     int
	}{
		{"owner-a", "PATCH", "/v8/neo/vault/accounts/" + f.accounts[1].ID, `{"ownerId":"owner-a"}`, true, 403},
		{"owner-a", "PUT", "/v8/neo/vault/accounts/" + f.accounts[1].ID + "/windows/day", `{"capacityTokens":100,"startsAt":0}`, true, 403},
		{"owner-a", "PATCH", "/v8/neo/vault/providers/codex", `{"exposed":false}`, true, 403},
		{"admin", "PATCH", "/v8/neo/vault/providers/codex", `{"exposed":false}`, false, 403},
		{"user-a", "GET", "/v8/neo/owner/stats?userId=owner-b", "", false, 403},
		{"", "GET", "/v8/neo/vault", "", false, 401},
		{"", "GET", "/v8/neo/vault/sources", "", false, 401},
		{"user-a", "GET", "/v8/neo/vault/sources", "", false, 403},
		{"owner-a", "POST", "/v8/neo/vault/sources", `{"label":"forged","url":"http://127.0.0.1","bearer":"injected","enabled":true}`, true, 403},
		{"owner-a", "PUT", "/v8/neo/vault/sources/other", `{"enabled":false}`, true, 403},
		{"owner-a", "DELETE", "/v8/neo/vault/sources/other", "", true, 403},
		{"user-a", "POST", "/v8/neo/vault/sources/other/sync", `{}`, true, 403},
		{"admin", "POST", "/v8/neo/vault/sources", `{"label":"forged","url":"http://127.0.0.1","bearer":"injected","enabled":true}`, false, 403},
	}
	for _, check := range checks {
		w = request(check.user, check.method, check.path, check.body, check.csrf)
		if w.Code != check.want {
			t.Errorf("%s %s %s: got %d %s want %d", check.user, check.method, check.path, w.Code, w.Body.String(), check.want)
		}
	}
	w = request("user-a", "GET", "/v8/neo/vault", "", false)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &vault); err != nil {
		t.Fatal(err)
	}
	if len(vault.Accounts) != 1 || vault.Accounts[0].OwnerID != "" || len(vault.Accounts[0].Windows) != 0 {
		t.Fatalf("member exposed owner quota/identity: %s", w.Body.String())
	}
	w = request("owner-a", "GET", "/v8/neo/owner/stats?userId=owner-b", "", false)
	if w.Code != 200 || bytes.Contains(w.Body.Bytes(), []byte(f.accounts[1].ID)) {
		t.Fatalf("owner stats accepted forged owner filter: %d %s", w.Code, w.Body.String())
	}
}
