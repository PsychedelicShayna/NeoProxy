package neopool

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func attachSourceExecutor(t *testing.T, pool *Store, manager *coreauth.Manager) {
	t.Helper()
	pool.ConfigureSources(func(provider string) coreauth.ProviderExecutor {
		return executor.NewNeoPiExecutor(provider, nil, pool.PrepareSourceAuth)
	}, nil)
	t.Cleanup(func() {
		for _, a := range manager.List() {
			if a.Attributes["neopi_source_id"] != "" {
				registry.GetGlobalRegistry().UnregisterClient(a.ID)
			}
		}
	})
}

func TestNeoPiSourceAdvertisesOnlyChatModels(t *testing.T) {
	f := newFixture(t)
	attachSourceExecutor(t, f.pool, f.manager)
	ctx := t.Context()
	admin := neo.WithPrincipal(ctx, neo.Principal{UserID: "admin", Role: neo.RoleAdmin, Session: true})
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"native/chat"},{"id":"native/image","kind":"image"},{"id":"native/embedding","kind":"embedding"}]}`))
		case "/v1/neoproxy/accounts":
			_, _ = w.Write([]byte(`{"data":[{"id":"10","provider":"native","label":"mixed catalogue","models":["native/chat","native/image","native/embedding"],"state":"healthy","windows":[]}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer gateway.Close()
	source, err := f.pool.SaveSource(admin, "", SourceInput{Label: "Mixed catalogue", URL: gateway.URL, Bearer: "gateway-bearer", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.pool.SyncSource(ctx, source.ID); err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.pool.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var imported []Model
	for _, model := range snapshot.Models {
		if model.ProviderID == sourceProvider(source.ID, "native") {
			imported = append(imported, model)
		}
	}
	if len(imported) != 1 || imported[0].Name != "native/chat" {
		t.Fatalf("chat-only source advertised unsupported image or embedding routes: %+v", imported)
	}
}

func TestNeoPiSourceLifecycleOwnershipPersistenceAndQuotaSeparation(t *testing.T) {
	f := newFixture(t)
	attachSourceExecutor(t, f.pool, f.manager)
	ctx := t.Context()
	admin := neo.WithPrincipal(ctx, neo.Principal{UserID: "admin", Role: neo.RoleAdmin, Session: true})
	var mu sync.Mutex
	bearer := "gateway-private-one"
	used := .25
	rows := []sourceCredential{
		{ID: "10", Provider: "new-native-provider", Label: "first account", Models: []string{"new-native-provider/model"}, State: "healthy", Windows: []sourceWindow{{Kind: "native-five-hour", Known: true, Used: &used, ResetsAt: f.at.Add(1).UnixMilli()}}},
		{ID: "11", Provider: "new-native-provider", Label: "second account", Models: []string{"new-native-provider/model"}, State: "healthy"},
	}
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+bearer {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"private provider details must not escape"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/models":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": []*registry.ModelInfo{{ID: "new-native-provider/model", ContextLength: 123456}}})
		case "/v1/neoproxy/accounts":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": rows})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer gateway.Close()
	in := SourceInput{Label: "Office broker", URL: gateway.URL + "/v1/", Bearer: bearer, OwnerID: "owner-a", Enabled: true}
	source, err := f.pool.SaveSource(admin, "", in)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.pool.SyncSource(ctx, source.ID); err != nil {
		t.Fatal(err)
	}
	accounts := func(pool *Store) []Account {
		t.Helper()
		snapshot, errSnapshot := pool.Snapshot(ctx)
		if errSnapshot != nil {
			t.Fatal(errSnapshot)
		}
		out := []Account{}
		for _, a := range snapshot.Accounts {
			if a.SourceID == source.ID {
				out = append(out, a)
			}
		}
		return out
	}
	imported := accounts(f.pool)
	if len(imported) != 2 || imported[0].ID == imported[1].ID {
		t.Fatalf("individual broker rows were collapsed: %+v", imported)
	}
	firstID := AccountID(sourceAuthID(source.ID, "10"))
	secondID := AccountID(sourceAuthID(source.ID, "11"))
	for _, a := range imported {
		if a.OwnerID != "owner-a" || a.State != "healthy" || len(a.Windows) != 0 {
			t.Fatalf("wrong default ownership or invented local quota: %+v", a)
		}
		if a.ID == firstID && (len(a.ProviderWindows) != 1 || a.ProviderWindows[0].Used != .25 || a.ProviderWindows[0].Capacity != 0 || a.ProviderWindows[0].Source != "provider-reported") {
			t.Fatalf("provider measurement was not kept separate: %+v", a)
		}
	}
	if err = f.pool.AssignOwner(ctx, firstID, "owner-b"); err != nil {
		t.Fatal(err)
	}
	if err = f.pool.ConfigureWindow(ctx, firstID, "owner-b", "day", 1000, f.at.UnixMilli(), false); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	bearer = "gateway-private-two"
	used = .75
	mu.Unlock()
	in.Bearer = "gateway-private-two"
	if _, err = f.pool.SaveSource(admin, source.ID, in); err != nil {
		t.Fatal(err)
	}
	if err = f.pool.SyncSource(ctx, source.ID); err != nil {
		t.Fatal(err)
	}
	for _, a := range accounts(f.pool) {
		if a.ID == firstID && (a.OwnerID != "owner-b" || len(a.Windows) != 1 || a.Windows[0].Capacity != 1000 || a.Windows[0].Used != 0 || a.ProviderWindows[0].Used != .75) {
			t.Fatalf("refresh replaced owner, ID or local meter: %+v", a)
		}
		if a.ID != firstID && a.ID != secondID {
			t.Fatalf("refresh changed account identity: %+v", a)
		}
	}
	list, err := f.pool.Sources(admin)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(list)
	if strings.Contains(string(encoded), "gateway-private") || len(list) != 1 || list[0].Status != "connected" || list[0].AccountCount != 2 || list[0].URL != gateway.URL {
		t.Fatalf("unsafe or incorrect source status: %s", encoded)
	}
	// Reopen the same private state using a fresh engine manager. Neither provider
	// secrets nor engine auth files are needed to recover the selectable accounts.
	reopened, err := neo.Open(f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	manager := coreauth.NewManager(nil, nil, nil)
	restarted, err := New(reopened.DB(), manager)
	if err != nil {
		t.Fatal(err)
	}
	attachSourceExecutor(t, restarted, manager)
	if err = restarted.SyncSource(ctx, source.ID); err != nil {
		t.Fatal(err)
	}
	if got := accounts(restarted); len(got) != 2 {
		t.Fatalf("persisted source did not recover: %+v", got)
	}
	selected, ok := manager.GetByID(sourceAuthID(source.ID, "10"))
	if !ok || selected.Attributes["api_key"] != "" || selected.Metadata["refresh_token"] != nil {
		t.Fatal("source registration imported credentials instead of a pinned reference")
	}
	owner := neo.WithPrincipal(ctx, neo.Principal{UserID: "owner-b", Role: neo.RoleOwner, AccountIDs: []string{firstID}})
	prepared, err := restarted.PrepareSourceAuth(owner, selected, "new-native-provider/model")
	if err != nil || prepared.Attributes["api_key"] != "gateway-private-two" || prepared.Attributes["neopi_credential_id"] != "10" {
		t.Fatal("current source bearer and exact credential were not resolved")
	}
	if _, err = restarted.PrepareSourceAuth(ctx, selected, "new-native-provider/model"); !errors.Is(err, ErrForbidden) {
		t.Fatal("legacy/unscoped execution bypassed the virtual-model authority boundary")
	}
	if _, err = restarted.PrepareSourceAuth(owner, selected, "different-provider/model"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("selected model escaped the credential's provider catalogue")
	}
	// Broker logout, addition and block are reconciled without restarting. Existing
	// local owners/meters remain in history, but stale selected clones cannot run.
	mu.Lock()
	rows = []sourceCredential{
		{ID: "11", Provider: "new-native-provider", Models: []string{"new-native-provider/model"}, State: "blocked"},
		{ID: "12", Provider: "new-native-provider", Models: []string{"new-native-provider/model"}, State: "healthy"},
	}
	mu.Unlock()
	if err = restarted.SyncSource(ctx, source.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = restarted.PrepareSourceAuth(owner, selected, "new-native-provider/model"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("logged-out credential remained executable")
	}
	for _, a := range accounts(restarted) {
		if a.ID == firstID || a.ID == secondID && a.State == "healthy" {
			t.Fatalf("revoked/blocked account remained eligible: %+v", a)
		}
	}
	mu.Lock()
	bearer = "server-revoked-the-bearer"
	mu.Unlock()
	if err = restarted.SyncSource(ctx, source.ID); err == nil {
		t.Fatal("revoked source bearer was accepted")
	}
	if got := accounts(restarted); len(got) != 0 {
		t.Fatalf("failed discovery retained selectable stale accounts: %+v", got)
	}
	list, err = restarted.Sources(admin)
	if err != nil || list[0].Status != "gateway returned HTTP 401" {
		t.Fatalf("source failure was not represented safely: %+v %v", list, err)
	}
	// Unrelated native credentials (including an existing serving lease) survive.
	if len(f.manager.List()) < 2 {
		t.Fatal("source lifecycle removed unrelated native accounts")
	}
}

func TestNeoPiSourceAuthorityAndDestinationChanges(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	admin := neo.WithPrincipal(ctx, neo.Principal{UserID: "admin", Role: neo.RoleAdmin, Session: true})
	in := SourceInput{Label: "source", URL: "http://127.0.0.1:8000", Bearer: "private-source-bearer", OwnerID: "owner-a", Enabled: true}
	for _, role := range []string{neo.RoleOwner, neo.RoleMember, neo.RoleService} {
		principal := neo.WithPrincipal(ctx, neo.Principal{UserID: "owner-a", Role: role, Session: true})
		if _, err := f.pool.SaveSource(principal, "", in); !errors.Is(err, ErrForbidden) {
			t.Fatalf("%s injected a source credential", role)
		}
		if _, err := f.pool.Sources(principal); !errors.Is(err, ErrForbidden) {
			t.Fatalf("%s read source connection details", role)
		}
	}
	source, err := f.pool.SaveSource(admin, "", in)
	if err != nil {
		t.Fatal(err)
	}
	in.URL = "http://127.0.0.1:8001"
	in.Bearer = ""
	if _, err = f.pool.SaveSource(admin, source.ID, in); err == nil {
		t.Fatal("existing secret was silently moved to another destination")
	}
	for _, bad := range []string{"file:///private", "https://user:secret@host", "https://host?bearer=secret", "https://host/#secret"} {
		in.URL = bad
		if _, err = f.pool.SaveSource(admin, source.ID, in); err == nil {
			t.Fatalf("unsafe secret-bearing gateway URL accepted: %s", bad)
		}
	}
}

func TestNeoPiSourceDiscoveryDoesNotFollowBearerRedirects(t *testing.T) {
	f := newFixture(t)
	attachSourceExecutor(t, f.pool, f.manager)
	var disclosures atomic.Int64
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { disclosures.Add(1) }))
	defer destination.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	ctx := neo.WithPrincipal(t.Context(), neo.Principal{UserID: "admin", Role: neo.RoleAdmin, Session: true})
	source, err := f.pool.SaveSource(ctx, "", SourceInput{Label: "redirect", URL: redirect.URL, Bearer: "private", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.pool.SyncSource(ctx, source.ID); err == nil || disclosures.Load() != 0 {
		t.Fatal("gateway redirect escaped the configured source origin")
	}
}

func TestNeoPiSourceDisableDisconnectAndLateDiscoveryFailClosed(t *testing.T) {
	f := newFixture(t)
	attachSourceExecutor(t, f.pool, f.manager)
	ctx := t.Context()
	admin := neo.WithPrincipal(ctx, neo.Principal{UserID: "admin", Role: neo.RoleAdmin, Session: true})
	var hold atomic.Bool
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/models":
			if hold.Load() {
				close(entered)
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
			}
			_, _ = w.Write([]byte(`{"data":[{"id":"test-provider/model"}]}`))
		case "/v1/neoproxy/accounts":
			_, _ = w.Write([]byte(`{"data":[{"id":"row","provider":"test-provider","models":["test-provider/model"],"state":"healthy","windows":[]}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer gateway.Close()
	defer unblock()
	in := SourceInput{Label: "revocable", URL: gateway.URL, Bearer: "private-bearer", OwnerID: "owner-a", Enabled: true}
	source, err := f.pool.SaveSource(admin, "", in)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.pool.SyncSource(ctx, source.ID); err != nil {
		t.Fatal(err)
	}
	authID := sourceAuthID(source.ID, "row")
	selected, ok := f.manager.GetByID(authID)
	if !ok {
		t.Fatal("discovered account was not registered")
	}
	hold.Store(true)
	synced := make(chan error, 1)
	go func() { synced <- f.pool.SyncSource(ctx, source.ID) }()
	<-entered
	// A stalled network discovery cannot retain the mutation lock. Disabling
	// revokes immediately, and the old discovery revision cannot revive it.
	in.Enabled = false
	if _, err = f.pool.SaveSource(admin, source.ID, in); err != nil {
		t.Fatal(err)
	}
	if _, ok = f.manager.GetByID(authID); ok {
		t.Fatal("disable retained selectable source account")
	}
	owner := neo.WithPrincipal(ctx, neo.Principal{UserID: "owner-a", Role: neo.RoleOwner})
	if _, err = f.pool.PrepareSourceAuth(owner, selected, "test-provider/model"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("disabled source accepted a stale selected auth")
	}
	unblock()
	if err = <-synced; !errors.Is(err, ErrConflict) {
		t.Fatalf("late discovery did not reject its obsolete revision: %v", err)
	}
	hold.Store(false)
	in.Enabled = true
	if _, err = f.pool.SaveSource(admin, source.ID, in); err != nil {
		t.Fatal(err)
	}
	if err = f.pool.SyncSource(ctx, source.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok = f.manager.GetByID(authID); !ok {
		t.Fatal("reenabling did not restore the stable source account")
	}
	if err = f.pool.DeleteSource(admin, source.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.PrepareSourceAuth(owner, selected, "test-provider/model"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("disconnect retained inference authority")
	}
	sources, err := f.pool.Sources(admin)
	if err != nil || len(sources) != 0 || len(f.manager.List()) != 2 {
		t.Fatalf("disconnect lost unrelated native accounts or retained source: %v %v", sources, err)
	}
	var preserved int
	if err = f.identity.DB().QueryRowContext(ctx, `SELECT count(*) FROM neo_accounts WHERE id=? AND owner_id='owner-a'`, AccountID(authID)).Scan(&preserved); err != nil || preserved != 1 {
		t.Fatal("disconnect erased durable ownership/history identity")
	}
}
