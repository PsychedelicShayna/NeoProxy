package neopool_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopool"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type accessFixture struct {
	t        *testing.T
	router   *gin.Engine
	http     *neopool.HTTP
	tokens   map[string]string
	cfgPath  string
	authDir  string
	manager  *coreauth.Manager
	identity *neo.Store
	upstream *httptest.Server
	seen     []string
	// loginHook is the ownership hook the access API installs on the engine;
	// engine sign-ins call it when a provider login completes.
	loginHook coreauth.PostAuthHook
}

const (
	goodKey   = "sk-good-synthetic-key-0001"
	routerKey = "sk-or-v1-synthetic-key-0002"
	openaiKey = "sk-proj-synthetic-key-0003"
)

func newAccessFixture(t *testing.T) *accessFixture {
	t.Helper()
	f := &accessFixture{t: t, tokens: map[string]string{}}
	f.upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.seen = append(f.seen, r.Method+" "+r.URL.Path)
		switch r.Header.Get("Authorization") {
		case "Bearer " + goodKey, "Bearer " + routerKey, "Bearer " + openaiKey:
		default:
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"Incorrect API key provided: sk-bad"}}`))
			return
		}
		if r.URL.Path == "/v1/auth/key" {
			// OpenRouter's key check (NeoPi's paste-key validate-url).
			_, _ = w.Write([]byte(`{"data":{"label":"synthetic"}}`))
			return
		}
		if r.URL.Path != "/v1/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"fake-b"},{"id":"fake-a"}]}`))
	}))
	t.Cleanup(f.upstream.Close)
	t.Cleanup(neopool.SetCatalogBaseURLForTest(func(string) string { return f.upstream.URL + "/v1" }))

	identity, err := neo.Open(filepath.Join(t.TempDir(), "neo.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = identity.Close() })
	f.identity = identity
	for _, row := range []struct{ id, role string }{{"admin", "admin"}, {"owner-a", "owner"}, {"owner-b", "owner"}, {"member-a", "member"}} {
		if _, err = identity.DB().Exec(`INSERT INTO neo_users(id,handle,name,role,state,created_at) VALUES(?,?,?,?,?,0)`, row.id, row.id, strings.ToUpper(row.id), row.role, "active"); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(row.id))
		token := hex.EncodeToString(sum[:])
		hash := sha256.Sum256([]byte(token))
		if _, err = identity.DB().Exec(`INSERT INTO neo_sessions(token_hash,user_id,expires_at,created_at) VALUES(?,?,?,0)`, hex.EncodeToString(hash[:]), row.id, int64(1<<62)); err != nil {
			t.Fatal(err)
		}
		f.tokens[row.id] = token
	}
	f.manager = coreauth.NewManager(nil, nil, nil)
	pool, err := neopool.New(identity.DB(), f.manager)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	f.authDir = filepath.Join(dir, "auths")
	if err = os.MkdirAll(f.authDir, 0o700); err != nil {
		t.Fatal(err)
	}
	f.cfgPath = filepath.Join(dir, "config.yaml")
	if err = os.WriteFile(f.cfgPath, []byte("port: 8317\nauth-dir: "+f.authDir+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	engine := management.NewHandler(&config.Config{AuthDir: f.authDir}, f.cfgPath, f.manager)
	// The production service reconciles config credentials on reload; mirror it.
	engine.SetConfigReloadHook(func(ctx context.Context, cfg *config.Config) {
		auths, errSynth := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{Config: cfg, Now: time.Now(), IDGenerator: synthesizer.NewStableIDGenerator()})
		if errSynth != nil {
			t.Error(errSynth)
			return
		}
		live := map[string]bool{}
		for _, a := range auths {
			live[a.ID] = true
			if _, ok := f.manager.GetByID(a.ID); ok {
				_, errSynth = f.manager.Update(ctx, a)
			} else {
				_, errSynth = f.manager.Register(ctx, a)
			}
			if errSynth != nil {
				t.Error(errSynth)
			}
		}
		for _, a := range f.manager.List() {
			if a.AuthSourceKind() == coreauth.AuthSourceConfig && !live[a.ID] {
				f.manager.Remove(ctx, a.ID)
			}
		}
	})
	gin.SetMode(gin.TestMode)
	f.router = gin.New()
	f.http = neopool.NewHTTP(pool, &hookCapture{Handler: engine, f: f})
	f.http.Register(f.router.Group("/v8/neo", neo.NewHTTP(identity).SessionMiddleware()))
	return f
}

type hookCapture struct {
	*management.Handler
	f *accessFixture
}

func (h *hookCapture) SetAccountOwnershipHook(hook coreauth.PostAuthHook) {
	h.f.loginHook = hook
	h.Handler.SetAccountOwnershipHook(hook)
}

func (f *accessFixture) do(user, method, path string, body []byte, contentType string) *httptest.ResponseRecorder {
	f.t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.AddCookie(&http.Cookie{Name: neo.SessionCookie, Value: f.tokens[user]})
	req.Header.Set("X-Neoproxy-CSRF", "1")
	if contentType == "" {
		contentType = "application/json"
	}
	req.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	return w
}

func (f *accessFixture) json(user, method, path, body string, want int) []byte {
	f.t.Helper()
	w := f.do(user, method, path, []byte(body), "")
	if w.Code != want {
		f.t.Fatalf("%s %s %s: %d %s (want %d)", user, method, path, w.Code, w.Body.String(), want)
	}
	return w.Body.Bytes()
}

func (f *accessFixture) upload(user, name, content, ownerID string) *httptest.ResponseRecorder {
	f.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", name)
	if err != nil {
		f.t.Fatal(err)
	}
	_, _ = part.Write([]byte(content))
	if ownerID != "" {
		_ = mw.WriteField("ownerId", ownerID)
	}
	_ = mw.Close()
	return f.do(user, "POST", "/v8/neo/access/file", buf.Bytes(), mw.FormDataContentType())
}

func itemOf(t *testing.T, raw []byte) neopool.AccessItem {
	t.Helper()
	var body struct {
		Item neopool.AccessItem `json:"item"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	return body.Item
}

func listOf(t *testing.T, raw []byte) neopool.AccessList {
	t.Helper()
	var list neopool.AccessList
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	return list
}

func assertNoSecrets(t *testing.T, raw []byte, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("response leaks a secret %q: %s", secret, raw)
		}
	}
}

func TestAccessAPIKeysValidateRouteAndStayScoped(t *testing.T) {
	f := newAccessFixture(t)

	w := f.do("admin", "POST", "/v8/neo/access/api-key", []byte(`{"provider":"custom","apiKey":"sk-bad-key-000000000","baseUrl":"`+f.upstream.URL+`/v1"}`), "")
	if w.Code != 400 || !strings.Contains(w.Body.String(), "rejected this key") || strings.Contains(w.Body.String(), "Incorrect API key") {
		t.Fatalf("bad key: %d %s", w.Code, w.Body.String())
	}
	if w = f.do("owner-a", "POST", "/v8/neo/access/api-key", []byte(`{"provider":"custom","apiKey":"`+goodKey+`","baseUrl":"`+f.upstream.URL+`/v1"}`), ""); w.Code != 403 {
		t.Fatalf("owner chose an upstream URL: %d %s", w.Code, w.Body.String())
	}
	if w = f.do("member-a", "POST", "/v8/neo/access/api-key", []byte(`{"provider":"openrouter","apiKey":"`+routerKey+`"}`), ""); w.Code != 403 {
		t.Fatalf("member added access: %d %s", w.Code, w.Body.String())
	}

	custom := itemOf(t, f.json("admin", "POST", "/v8/neo/access/api-key", `{"provider":"custom","apiKey":"`+goodKey+`","baseUrl":"`+f.upstream.URL+`/v1/","label":"Office LLM"}`, 201))
	if custom.Kind != "apiKey" || custom.Provider != "custom" || custom.Label != "Office LLM" || custom.OwnerID != "admin" || custom.Plan.ID != "apiKey" || custom.Plan.MonthlyUSD != 20 || custom.Plan.Source != "default" || custom.Refresh != "none" || !strings.HasPrefix(custom.ID, "acct_") {
		t.Fatalf("custom item: %+v", custom)
	}
	router := itemOf(t, f.json("owner-a", "POST", "/v8/neo/access/api-key", `{"provider":"openrouter","apiKey":"`+routerKey+`"}`, 201))
	if router.Provider != "openrouter" || router.OwnerID != "owner-a" || router.Label != "OpenRouter key ending in 0002" {
		t.Fatalf("openrouter item: %+v", router)
	}
	native := itemOf(t, f.json("admin", "POST", "/v8/neo/access/api-key", `{"provider":"codex","apiKey":"`+openaiKey+`","ownerId":"owner-b","monthlyUSD":75}`, 201))
	if native.Provider != "codex" || native.OwnerID != "owner-b" || native.Plan.MonthlyUSD != 75 || native.Plan.Source != "admin" {
		t.Fatalf("native codex key item: %+v", native)
	}
	f.json("admin", "POST", "/v8/neo/access/api-key", `{"provider":"openrouter","apiKey":"`+routerKey+`"}`, 409)

	cfg, err := os.ReadFile(f.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"name: custom-office-llm", "name: openrouter", "base-url: " + f.upstream.URL + "/v1", "name: fake-a", "name: fake-b", "api-key: " + openaiKey} {
		if !strings.Contains(string(cfg), want) {
			t.Fatalf("engine config lacks %q:\n%s", want, cfg)
		}
	}

	raw := f.json("admin", "GET", "/v8/neo/access", "", 200)
	assertNoSecrets(t, raw, goodKey, routerKey, openaiKey)
	list := listOf(t, raw)
	if len(list.Items) != 3 || list.Totals.Items != 3 || list.Totals.MonthlyUSD != 115 || len(list.People) != 4 {
		t.Fatalf("admin list: %s", raw)
	}
	providers := map[string]neopool.ProviderInfo{}
	for _, p := range list.Providers {
		providers[p.ID] = p
	}
	if providers["gemini"].Subscription != nil || providers["codex"].Subscription == nil || providers["custom"].APIKey == nil || !providers["custom"].APIKey.EditableBaseURL || providers["openrouter"].Hue != 300 {
		t.Fatalf("catalogue: %s", raw)
	}
	for _, item := range list.Items {
		if len(item.Usage.Buckets) != 20 || item.Usage.Buckets[19].T <= item.Usage.Buckets[0].T {
			t.Fatalf("usage buckets: %+v", item.Usage)
		}
	}

	owner := listOf(t, f.json("owner-a", "GET", "/v8/neo/access", "", 200))
	if len(owner.Items) != 1 || owner.Items[0].ID != router.ID || len(owner.People) != 1 || owner.People[0].ID != "owner-a" {
		t.Fatalf("owner sees other contributions: %+v", owner)
	}
	if len(owner.Items[0].Windows) != 0 || owner.Items[0].ProviderWindows == nil {
		t.Fatalf("unconfigured windows: %+v", owner.Items[0])
	}
	// Budget windows keep using the pool account ID through the existing route.
	f.json("owner-a", "PUT", "/v8/neo/vault/accounts/"+router.ID+"/windows/day", `{"capacityTokens":1000,"startsAt":0}`, 204)
	metered := listOf(t, f.json("owner-a", "GET", "/v8/neo/access", "", 200)).Items[0]
	if len(metered.Windows) != 1 || metered.Windows[0].Kind != "day" || metered.Windows[0].Source != "configured-metered" || metered.Windows[0].Capacity != 1000 || !metered.Windows[0].Known {
		t.Fatalf("configured window missing from access item: %+v", metered.Windows)
	}
	if strings.Contains(strings.Join(owner.Items[0].Actions, ","), "setPlan") || !strings.Contains(strings.Join(owner.Items[0].Actions, ","), "remove") {
		t.Fatalf("owner actions: %v", owner.Items[0].Actions)
	}
	if member := listOf(t, f.json("member-a", "GET", "/v8/neo/access", "", 200)); len(member.Items) != 0 {
		t.Fatalf("member sees pool items: %+v", member)
	}

	// Patch: plan and owner are administrative; label/disable belong to the contributor.
	f.json("owner-a", "PATCH", "/v8/neo/access/"+router.ID, `{"monthlyUSD":1}`, 403)
	f.json("owner-a", "PATCH", "/v8/neo/access/"+native.ID, `{"label":"mine now"}`, 404)
	patched := itemOf(t, f.json("admin", "PATCH", "/v8/neo/access/"+router.ID, `{"planId":"apiKey","monthlyUSD":55,"label":"Team router"}`, 200))
	if patched.Plan.MonthlyUSD != 55 || patched.Plan.Source != "admin" || patched.Label != "Team router" {
		t.Fatalf("admin patch: %+v", patched)
	}
	f.json("admin", "PATCH", "/v8/neo/access/"+router.ID, `{"planId":"ultra-mega"}`, 400)
	paused := itemOf(t, f.json("owner-a", "PATCH", "/v8/neo/access/"+router.ID, `{"disabled":true}`, 200))
	if paused.Health != "disabled" {
		t.Fatalf("pause: %+v", paused)
	}
	if cfg, _ = os.ReadFile(f.cfgPath); !strings.Contains(string(cfg), "disabled: true") {
		t.Fatalf("pause not persisted in engine config:\n%s", cfg)
	}
	loaded, err := config.LoadConfig(f.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	pausedOnDisk := false
	for _, compat := range loaded.OpenAICompatibility {
		for _, key := range compat.APIKeyEntries {
			pausedOnDisk = pausedOnDisk || (compat.Name == "openrouter" && key.Disabled)
		}
	}
	if !pausedOnDisk {
		t.Fatalf("pause lost when the engine reloads its config: %+v", loaded.OpenAICompatibility)
	}
	if resumed := itemOf(t, f.json("owner-a", "PATCH", "/v8/neo/access/"+router.ID, `{"disabled":false}`, 200)); resumed.Health != "healthy" {
		t.Fatalf("resume: %+v", resumed)
	}
	pausedNative := itemOf(t, f.json("admin", "PATCH", "/v8/neo/access/"+native.ID, `{"disabled":true}`, 200))
	if pausedNative.Health != "disabled" {
		t.Fatalf("native key pause: %+v", pausedNative)
	}
	reassigned := itemOf(t, f.json("admin", "PATCH", "/v8/neo/access/"+native.ID, `{"ownerId":"owner-a","planId":null,"monthlyUSD":null}`, 200))
	if reassigned.OwnerID != "owner-a" || reassigned.Plan.Source != "default" {
		t.Fatalf("reassign/clear: %+v", reassigned)
	}

	// Delete removes the engine credential; other people cannot see or remove it.
	f.json("owner-b", "DELETE", "/v8/neo/access/"+custom.ID, "", 404)
	f.json("admin", "DELETE", "/v8/neo/access/"+custom.ID, "", 204)
	if cfg, _ = os.ReadFile(f.cfgPath); strings.Contains(string(cfg), "custom-office-llm") {
		t.Fatalf("custom entry survived delete:\n%s", cfg)
	}
	if after := listOf(t, f.json("admin", "GET", "/v8/neo/access", "", 200)); len(after.Items) != 2 {
		t.Fatalf("after delete: %+v", after.Items)
	}
}

func jwt(claims map[string]any) string {
	payload, _ := json.Marshal(claims)
	return base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func TestAccessFileUploadDetectsProviderPlanAndOwner(t *testing.T) {
	f := newAccessFixture(t)
	idToken := jwt(map[string]any{"email": "pro-user@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro", "chatgpt_account_id": "acct-9"}})
	access := jwt(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	codexCLI := `{"OPENAI_API_KEY":null,"tokens":{"id_token":"` + idToken + `","access_token":"` + access + `","refresh_token":"rt-codex-secret","account_id":"acct-9"},"last_refresh":"2026-11-01T00:00:00Z"}`

	if w := f.upload("member-a", "auth.json", codexCLI, ""); w.Code != 403 {
		t.Fatalf("member upload: %d %s", w.Code, w.Body.String())
	}
	if w := f.upload("owner-a", "auth.json", codexCLI, "owner-b"); w.Code != 403 {
		t.Fatalf("owner uploaded for someone else: %d %s", w.Code, w.Body.String())
	}
	w := f.upload("admin", "auth.json", codexCLI, "owner-b")
	if w.Code != 201 {
		t.Fatalf("codex upload: %d %s", w.Code, w.Body.String())
	}
	assertNoSecrets(t, w.Body.Bytes(), "rt-codex-secret", access, idToken)
	item := itemOf(t, w.Body.Bytes())
	if item.Provider != "codex" || item.Kind != "subscription" || item.Refresh != "auto" || item.OwnerID != "owner-b" || item.Label != "pro-user@example.com" || item.Plan != (neopool.AccessPlan{ID: "pro", Name: "ChatGPT Pro", MonthlyUSD: 200, Source: "detected"}) {
		t.Fatalf("codex upload item: %+v", item)
	}
	entries, err := filepath.Glob(filepath.Join(f.authDir, "*.json"))
	if err != nil || len(entries) != 1 || !strings.HasSuffix(entries[0], "-pro-user@example.com-pro.json") {
		t.Fatalf("engine auth dir: %v %v", entries, err)
	}

	claudeCode := `{"claudeAiOauth":{"accessToken":"sk-ant-oat-secret","refreshToken":"sk-ant-ort-secret","expiresAt":` + jsonInt(time.Now().Add(time.Hour).UnixMilli()) + `,"subscriptionType":"max","rateLimitTier":"default_claude_max_20x"}}`
	w = f.upload("owner-a", ".credentials.json", claudeCode, "")
	if w.Code != 201 {
		t.Fatalf("claude upload: %d %s", w.Code, w.Body.String())
	}
	assertNoSecrets(t, w.Body.Bytes(), "sk-ant-oat-secret", "sk-ant-ort-secret")
	claude := itemOf(t, w.Body.Bytes())
	if claude.Provider != "claude" || claude.Kind != "subscription" || claude.OwnerID != "owner-a" || claude.Plan.ID != "max20x" || claude.Plan.MonthlyUSD != 200 {
		t.Fatalf("claude upload item: %+v", claude)
	}

	// Download is admin-only and returns the engine auth file as an attachment.
	if acts := strings.Join(item.Actions, ","); !strings.Contains(acts, "refresh") || !strings.Contains(acts, "download") {
		t.Fatalf("admin actions on a sign-in file: %v", item.Actions)
	}
	if acts := strings.Join(claude.Actions, ","); !strings.Contains(acts, "refresh") || strings.Contains(acts, "download") {
		t.Fatalf("owner actions on own sign-in file: %v", claude.Actions)
	}
	dl := f.do("admin", "GET", "/v8/neo/access/"+item.ID+"/file", nil, "")
	if dl.Code != 200 || !strings.Contains(dl.Header().Get("Content-Disposition"), `attachment; filename="codex-`) || !strings.HasSuffix(strings.TrimSuffix(dl.Header().Get("Content-Disposition"), `"`), "-pro.json") || !strings.Contains(dl.Body.String(), "rt-codex-secret") {
		t.Fatalf("download: %d %v %s", dl.Code, dl.Header(), dl.Body.String())
	}
	f.json("owner-b", "GET", "/v8/neo/access/"+item.ID+"/file", "", 403)
	key := itemOf(t, f.json("admin", "POST", "/v8/neo/access/api-key", `{"provider":"openrouter","apiKey":"`+routerKey+`"}`, 201))
	if w = f.do("admin", "GET", "/v8/neo/access/"+key.ID+"/file", nil, ""); w.Code != 409 || !strings.Contains(w.Body.String(), "API key") || strings.Contains(w.Body.String(), routerKey) {
		t.Fatalf("api key download: %d %s", w.Code, w.Body.String())
	}
	if w = f.do("admin", "POST", "/v8/neo/access/"+key.ID+"/refresh", nil, ""); w.Code != 409 {
		t.Fatalf("api key refresh: %d %s", w.Code, w.Body.String())
	}
	f.json("admin", "DELETE", "/v8/neo/access/"+key.ID, "", 204)
	// The fixture engine has no provider executors, so a forced refresh fails
	// without any network call and must say so instead of pretending success.
	f.json("owner-b", "POST", "/v8/neo/access/"+claude.ID+"/refresh", "", 404)
	if w = f.do("owner-a", "POST", "/v8/neo/access/"+claude.ID+"/refresh", nil, ""); w.Code != 502 || !strings.Contains(w.Body.String(), "did not renew") {
		t.Fatalf("refresh without executor: %d %s", w.Code, w.Body.String())
	}
	locked := &coreauth.Auth{ID: "neopi-codex-locked.json", FileName: "neopi-codex-locked.json", Provider: "codex", Status: coreauth.StatusActive,
		Attributes: map[string]string{"path": filepath.Join(f.authDir, "neopi-codex-locked.json")},
		Metadata:   map[string]any{"type": "codex", "access_token": "at-locked-secret", coreauth.MetadataRefreshLocked: true, coreauth.MetadataImportedFrom: map[string]any{"source": "neopi", "profile": "/p", "credential_id": "c1", "imported_at": float64(1)}}}
	if _, err = f.manager.Register(context.Background(), locked); err != nil {
		t.Fatal(err)
	}
	lockedID := neopool.AccountID(locked.ID)
	for _, path := range []string{"/refresh", "/file"} {
		method := "POST"
		if path == "/file" {
			method = "GET"
		}
		if w = f.do("admin", method, "/v8/neo/access/"+lockedID+path, nil, ""); w.Code != 409 || !strings.Contains(w.Body.String(), "refresh locked") || strings.Contains(w.Body.String(), "at-locked-secret") {
			t.Fatalf("locked %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	f.manager.Remove(context.Background(), locked.ID)

	if w = f.upload("admin", "notes.json", `{"hello":"world"}`, ""); w.Code != 400 || !strings.Contains(w.Body.String(), "~/.codex/auth.json") {
		t.Fatalf("unknown format: %d %s", w.Code, w.Body.String())
	}

	raw := f.json("admin", "GET", "/v8/neo/access", "", 200)
	assertNoSecrets(t, raw, "rt-codex-secret", "sk-ant-oat-secret", "sk-ant-ort-secret", access)
	if list := listOf(t, raw); list.Totals.MonthlyUSD != 400 || list.Totals.Items != 2 {
		t.Fatalf("totals: %+v", list.Totals)
	}

	f.json("owner-a", "DELETE", "/v8/neo/access/"+item.ID, "", 404)
	f.json("owner-a", "DELETE", "/v8/neo/access/"+claude.ID, "", 204)
	f.json("admin", "DELETE", "/v8/neo/access/"+item.ID, "", 204)
	if entries, err = filepath.Glob(filepath.Join(f.authDir, "*.json")); err != nil || len(entries) != 0 {
		t.Fatalf("credential files survived delete: %v %v", entries, err)
	}
	if list := listOf(t, f.json("admin", "GET", "/v8/neo/access", "", 200)); len(list.Items) != 0 {
		t.Fatalf("deleted items still listed: %+v", list.Items)
	}
}

func jsonInt(v int64) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

// A completed engine sign-in calls the ownership hook with the initiating owner
// before the credential is saved; the saved credential then reaches the engine
// manager. The new access must be listed with its owner and detected plan.
func TestSubscriptionSignInAppearsWithOwnerAndDetectedPlan(t *testing.T) {
	f := newAccessFixture(t)
	if f.loginHook == nil {
		t.Fatal("access API did not install the engine ownership hook")
	}
	idToken := jwt(map[string]any{"email": "signed-in@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus"}})
	record := &coreauth.Auth{ID: "codex-signed-in@example.com-plus.json", FileName: "codex-signed-in@example.com-plus.json", Provider: "codex", Status: coreauth.StatusActive,
		Attributes: map[string]string{"path": filepath.Join(f.authDir, "codex-signed-in@example.com-plus.json"), "plan_type": "plus"},
		Metadata:   map[string]any{"type": "codex", "email": "signed-in@example.com", "id_token": idToken, "access_token": "at-login-secret", "refresh_token": "rt-login-secret"}}
	ctx := coreauth.WithAccountLoginOwner(context.Background(), "owner-a")
	if err := f.loginHook(ctx, record); err != nil {
		t.Fatal(err)
	}
	if _, err := f.manager.Register(ctx, record); err != nil {
		t.Fatal(err)
	}
	raw := f.json("owner-a", "GET", "/v8/neo/access", "", 200)
	assertNoSecrets(t, raw, "at-login-secret", "rt-login-secret", idToken)
	list := listOf(t, raw)
	if len(list.Items) != 1 {
		t.Fatalf("signed-in access missing: %s", raw)
	}
	item := list.Items[0]
	if item.OwnerID != "owner-a" || item.Kind != "subscription" || item.Provider != "codex" || item.Label != "signed-in@example.com" || item.Plan.ID != "plus" || item.Plan.Source != "detected" || item.Plan.MonthlyUSD != 20 {
		t.Fatalf("signed-in item: %+v", item)
	}
	if other := listOf(t, f.json("owner-b", "GET", "/v8/neo/access", "", 200)); len(other.Items) != 0 {
		t.Fatalf("another owner sees the sign-in: %+v", other.Items)
	}
}
