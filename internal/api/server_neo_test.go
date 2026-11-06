package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopool"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func neoTestIdentity(t *testing.T) (*neo.Store, neo.User, string, string) {
	t.Helper()
	store, err := neo.Open(filepath.Join(t.TempDir(), "identity.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if errClose := store.Close(); errClose != nil {
			t.Error(errClose)
		}
	})
	session, token, err := store.Bootstrap(t.Context(), "captain", "Captain", "correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}
	u, err := store.User(t.Context(), session.UserID)
	if err != nil {
		t.Fatal(err)
	}
	key, secret, err := store.MintKey(t.Context(), neo.Principal{UserID: u.ID, Role: neo.RoleAdmin, Session: true}, u.ID, "consumer", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if key.ID == "" {
		t.Fatal("key has no ID")
	}
	return store, u, token, secret
}
func TestNeoConsumerKeysCannotRegisterWorkersOrEnterOperatorRoutes(t *testing.T) {
	t.Setenv("NEOPROXY_DATA_DIR", "")
	store, _, _, secret := neoTestIdentity(t)
	server := newTestServerWithOptions(t, WithNeoStore(store), WithRequestLoggerFactory(nil))
	workerCalled := false
	server.AttachWebsocketRoute("/v1/ws", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { workerCalled = true; w.WriteHeader(204) }))
	for _, tc := range []struct{ method, path, header, value string }{
		{"GET", "/v1/ws", "Authorization", "Bearer " + secret},
		{"GET", "/v1/ws", "X-Api-Key", secret},
		{"GET", "/v1/ws?key=" + secret, "", ""},
		{"POST", "/v1/realtime/client_secrets", "Authorization", "Bearer " + secret},
		{"GET", "/v8/management/config", "Authorization", "Bearer " + secret},
		{"GET", "/v0/resource/plugins/anything", "Authorization", "Bearer " + secret},
		{"GET", "/v8/neo/users", "Authorization", "Bearer " + secret},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		if tc.header != "" {
			req.Header.Set(tc.header, tc.value)
		}
		out := httptest.NewRecorder()
		server.engine.ServeHTTP(out, req)
		if out.Code != 403 {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, out.Code, out.Body.String())
		}
	}
	if workerCalled {
		t.Fatal("consumer registered an AI Studio backend worker")
	}
	// Preserve existing explicitly unauthenticated websocket-worker behavior; this
	// test guards only the newly introduced untrusted consumer authority.
	out := httptest.NewRecorder()
	server.engine.ServeHTTP(out, httptest.NewRequest("GET", "/v1/ws", nil))
	if !workerCalled || out.Code != 204 {
		t.Fatal("legacy worker path changed", out.Code)
	}
}
func TestNeoAdminSessionsUseRealEngineManagementWithoutSecret(t *testing.T) {
	t.Setenv("NEOPROXY_DATA_DIR", "")
	t.Setenv("MANAGEMENT_PASSWORD", "operator-secret")
	store, admin, token, _ := neoTestIdentity(t)
	server := newTestServerWithOptions(t, WithNeoStore(store), WithRequestLoggerFactory(nil))
	if err := os.WriteFile(server.configFilePath, []byte("port = 0\napi-keys = ['test-key']\n"), 0600); err != nil {
		t.Fatal(err)
	}
	request := func(token, secret string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/v8/management/config", nil)
		req.RemoteAddr = "127.0.0.1:3000"
		if token != "" {
			req.AddCookie(&http.Cookie{Name: neo.SessionCookie, Value: token})
		}
		if secret != "" {
			req.Header.Set("Authorization", "Bearer "+secret)
		}
		out := httptest.NewRecorder()
		server.engine.ServeHTTP(out, req)
		return out
	}
	if out := request(token, ""); out.Code != 200 || strings.Contains(out.Body.String(), "operator-secret") {
		t.Fatalf("admin Engine operation: %d %s", out.Code, out.Body.String())
	}
	for _, role := range []string{neo.RoleOwner, neo.RoleMember} {
		u, err := store.CreateUser(t.Context(), neo.UserInput{Handle: role, Role: role, State: "active", Password: "correct-horse-battery"})
		if err != nil {
			t.Fatal(err)
		}
		_, memberToken, err := store.Login(t.Context(), u.Handle, "correct-horse-battery")
		if err != nil {
			t.Fatal(err)
		}
		if out := request(memberToken, ""); out.Code != 403 {
			t.Fatalf("%s gained management: %d", role, out.Code)
		}
	}
	if out := request("", "operator-secret"); out.Code != 200 {
		t.Fatalf("operator maintenance broke: %d %s", out.Code, out.Body.String())
	}
	// The Engine sees only current server-side session authority.
	if err := store.ChangePassword(t.Context(), admin.ID, "", "replacement-password", true); err != nil {
		t.Fatal(err)
	}
	if out := request(token, ""); out.Code != 401 {
		t.Fatalf("password-reset session retained Engine access: %d", out.Code)
	}
}

func TestNeoOwnerKeyScopeDenialReturnsForbidden(t *testing.T) {
	t.Setenv("NEOPROXY_DATA_DIR", "")
	store, admin, _, _ := neoTestIdentity(t)
	server := newTestServerWithOptions(t, WithNeoStore(store), WithRequestLoggerFactory(nil))
	owner, err := store.CreateUser(t.Context(), neo.UserInput{Handle: "pool-owner", Role: neo.RoleOwner, State: "active", Password: "correct-horse-battery"})
	if err != nil {
		t.Fatal(err)
	}
	for _, account := range []struct{ id, owner string }{{"owned-pool", owner.ID}, {"foreign-pool", admin.ID}} {
		if err := server.neoPool.BindLogin(t.Context(), account.owner, &coreauth.Auth{ID: account.id, Provider: "codex"}); err != nil {
			t.Fatal(err)
		}
	}
	_, token, err := store.Login(t.Context(), owner.Handle, "correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}
	mint := func(label string, ids []string) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(map[string]any{"label": label, "accountIds": ids})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/v8/neo/users/"+owner.ID+"/keys", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Neoproxy-CSRF", "1")
		req.AddCookie(&http.Cookie{Name: neo.SessionCookie, Value: token})
		out := httptest.NewRecorder()
		server.engine.ServeHTTP(out, req)
		return out
	}
	ownID, foreignID := neopool.AccountID("owned-pool"), neopool.AccountID("foreign-pool")
	out := mint("owned", []string{ownID})
	if out.Code != http.StatusCreated {
		t.Fatalf("owned scope: HTTP %d", out.Code)
	}
	var issued struct {
		Key neo.Key `json:"key"`
	}
	if err := json.Unmarshal(out.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		ids  []string
	}{
		{"foreign", []string{foreignID}},
		{"mixed", []string{ownID, foreignID}},
		{"missing", []string{"nonexistent-account"}},
		{"unrestricted", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := mint(tc.name, tc.ids)
			var failure struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(out.Body.Bytes(), &failure); err != nil {
				t.Fatal(err)
			}
			if out.Code != http.StatusForbidden || failure.Error.Code != "neoproxy_forbidden" {
				t.Fatalf("scope denial: HTTP %d, code %q", out.Code, failure.Error.Code)
			}
		})
	}
	req := httptest.NewRequest(http.MethodGet, "/v8/neo/users/"+owner.ID, nil)
	req.AddCookie(&http.Cookie{Name: neo.SessionCookie, Value: token})
	out = httptest.NewRecorder()
	server.engine.ServeHTTP(out, req)
	var listed struct {
		Keys []neo.Key `json:"keys"`
	}
	if err := json.Unmarshal(out.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if out.Code != http.StatusOK || len(listed.Keys) != 1 || listed.Keys[0].ID != issued.Key.ID || listed.Keys[0].Label != "owned" {
		t.Fatalf("scope denial persisted an unauthorized key: HTTP %d, keys %+v", out.Code, listed.Keys)
	}
}
func TestNeoInferenceIdentitySurvivesHandlerParentContext(t *testing.T) {
	store, u, _, secret := neoTestIdentity(t)
	s := &Server{neoStore: store, neoHTTP: neo.NewHTTP(store)}
	s.EnableNeoInference()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(s.neoInferenceMiddleware(), AuthMiddleware(nil))
	handler := handlers.NewBaseAPIHandlers(&config.SDKConfig{}, nil)
	engine.POST("/v1/messages", func(c *gin.Context) {
		parent := neo.WithPrincipal(context.Background(), neo.Principal{UserID: "forged-parent"})
		ctx, cancel := handler.GetContextWithCancel(nil, c, parent)
		defer cancel()
		p, ok := neo.PrincipalFromContext(ctx)
		if !ok || p.UserID != u.ID || p.KeyID == "" || p.KeyID == secret || p.Session || p.IsAdmin() {
			t.Errorf("identity lost or elevated: %#v", p)
		}
		c.Status(204)
	})
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	req.Header.Set("X-Api-Key", secret)
	out := httptest.NewRecorder()
	engine.ServeHTTP(out, req)
	if out.Code != 204 {
		t.Fatalf("status %d %s", out.Code, out.Body.String())
	}
}
func TestNeoDataDirectorySelectionAndExplicitLifecycle(t *testing.T) {
	t.Setenv("NEOPROXY_DATA_DIR", "")
	empty := &Server{}
	empty.initNeo(nil)
	if empty.NeoStore() != nil {
		t.Fatal("empty SDK config created shared database")
	}
	dir := t.TempDir()
	configured := &Server{configFilePath: filepath.Join(dir, "engine.toml")}
	configured.initNeo(nil)
	if err := configured.neoStartupError(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "data", "neoproxy.sqlite")); err != nil {
		t.Fatal(err)
	}
	if err := configured.neoStore.Close(); err != nil {
		t.Fatal(err)
	}
	override := t.TempDir()
	t.Setenv("NEOPROXY_DATA_DIR", override)
	selected := &Server{configFilePath: filepath.Join(dir, "other.toml")}
	selected.initNeo(nil)
	if err := selected.neoStartupError(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(override, "neoproxy.sqlite")); err != nil {
		t.Fatal(err)
	}
	if err := selected.neoStore.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEOPROXY_DATA_DIR", filepath.Join(dir, "not-a-dir"))
	if err := os.WriteFile(filepath.Join(dir, "not-a-dir"), []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	failed := &Server{}
	failed.initNeo(nil)
	if failed.neoStartupError() == nil {
		t.Fatal("database initialization failure hidden")
	}
	store, _, _, _ := neoTestIdentity(t)
	injected := &Server{}
	injected.initNeo(store)
	if injected.neoOwnStore || injected.neoStore != store || injected.neoStartupError() != nil {
		t.Fatal("injected store lifecycle changed")
	}
	if err := injected.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().PingContext(t.Context()); err != nil {
		t.Fatal("server closed caller-owned store", err)
	}
}

func TestNeoReadinessDoesNotBypassKeyRevocation(t *testing.T) {
	store, u, _, secret := neoTestIdentity(t)
	s := &Server{neoStore: store, neoHTTP: neo.NewHTTP(store)}
	engine := gin.New()
	engine.Use(s.neoInferenceMiddleware())
	called := 0
	engine.GET("/v1/models", func(c *gin.Context) {
		p, ok := neo.PrincipalFromContext(c.Request.Context())
		if !ok || p.UserID != u.ID || p.Session || p.IsAdmin() || len(p.Grants) != 0 || len(p.ProviderAccess) != 0 {
			t.Errorf("readiness granted authority: %#v", p)
		}
		called++
		c.Status(204)
	})
	call := func() int {
		req := httptest.NewRequest("GET", "/v1/models", nil)
		req.Header.Set("Authorization", "Bearer "+secret)
		out := httptest.NewRecorder()
		engine.ServeHTTP(out, req)
		return out.Code
	}
	if status := call(); status != 503 || called != 0 {
		t.Fatal("pre-cutover fallthrough", status, called)
	}
	s.EnableNeoInference()
	if status := call(); status != 204 || called != 1 {
		t.Fatal("identity was not delivered to scoped consumer", status, called)
	}
	p, err := store.AuthenticateKey(t.Context(), secret)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.RevokeKey(t.Context(), neo.Principal{UserID: u.ID, Role: u.Role, Session: true}, u.ID, p.KeyID); err != nil {
		t.Fatal(err)
	}
	if status := call(); status != 401 || called != 1 {
		t.Fatal("readiness bypassed revocation", status, called)
	}
}

func TestNeoSessionDoesNotEnableDeprecatedManagement(t *testing.T) {
	t.Setenv("NEOPROXY_DATA_DIR", "")
	t.Setenv("MANAGEMENT_PASSWORD", "")
	store, _, token, _ := neoTestIdentity(t)
	server := newTestServerWithOptions(t, WithNeoStore(store), WithRequestLoggerFactory(nil))
	if err := os.WriteFile(server.configFilePath, []byte("port = 0\napi-keys = ['test-key']\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path string
		want int
	}{
		{"/v8/management/config", 200},
		{"/v0/management/config", 404},
	} {
		req := httptest.NewRequest("GET", tc.path, nil)
		req.AddCookie(&http.Cookie{Name: neo.SessionCookie, Value: token})
		out := httptest.NewRecorder()
		server.engine.ServeHTTP(out, req)
		if out.Code != tc.want {
			t.Fatalf("%s: %d want %d: %s", tc.path, out.Code, tc.want, out.Body.String())
		}
	}
}
