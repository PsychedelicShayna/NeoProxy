package neopool_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopool"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// keyEngine is the production key path: the vault pool, the management
// engine writing the config file, and its reload synthesizing and
// registering credentials with the auth manager.
type keyEngine struct {
	manager   *coreauth.Manager
	pool      *neopool.Store
	http      *neopool.HTTP
	engine    *management.Handler
	cfgPath   string
	reloadErr error
}

func newKeyEngine(t *testing.T) *keyEngine {
	t.Helper()
	identity, err := neo.Open(filepath.Join(t.TempDir(), "neo.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if errClose := identity.Close(); errClose != nil {
			t.Error(errClose)
		}
	})
	if _, err = identity.DB().Exec(`INSERT INTO neo_users(id,handle,name,role,state,created_at) VALUES('owner-a','owner-a','Owner A','owner','active',0)`); err != nil {
		t.Fatal(err)
	}
	k := &keyEngine{manager: coreauth.NewManager(nil, nil, nil)}
	if k.pool, err = neopool.New(identity.DB(), k.manager); err != nil {
		t.Fatal(err)
	}
	k.cfgPath = filepath.Join(t.TempDir(), "config.toml")
	if err = os.WriteFile(k.cfgPath, []byte("port = 8317\nauth-dir = 'auths'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	k.engine = management.NewHandler(&config.Config{}, k.cfgPath, k.manager)
	k.http = neopool.NewHTTP(k.pool, k.engine)
	// The production service supplies this reload callback. Exercise its actual
	// existing synthesis/registration mechanism rather than a second key engine.
	k.engine.SetConfigReloadHook(func(ctx context.Context, cfg *config.Config) {
		auths, errSynth := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{Config: cfg, Now: time.Now(), IDGenerator: synthesizer.NewStableIDGenerator()})
		if errSynth != nil {
			k.reloadErr = errSynth
			return
		}
		for _, a := range auths {
			if _, errRegister := k.manager.Register(ctx, a); errRegister != nil {
				k.reloadErr = errRegister
				return
			}
		}
	})
	return k
}

func TestOwnedAPIKeyUsesExistingEngineConfigurationAndStableAuthIdentity(t *testing.T) {
	k := newKeyEngine(t)
	manager, pool, engine, cfgPath := k.manager, k.pool, k.engine, k.cfgPath
	ctx := neo.WithPrincipal(context.Background(), neo.Principal{UserID: "owner-a", Role: neo.RoleOwner, Session: true})
	record, err := engine.CreateAccountAPIKey(ctx, coreauth.AccountKey{Provider: "codex", APIKey: "synthetic-provider-key-not-a-real-secret"})
	if err != nil {
		t.Fatal(err)
	}
	reloadErr := k.reloadErr
	if reloadErr != nil {
		t.Fatal(reloadErr)
	}
	registered, ok := manager.GetByID(record.ID)
	if !ok || registered.Attributes["api_key"] != "synthetic-provider-key-not-a-real-secret" {
		t.Fatal("existing engine auth manager did not receive credential")
	}
	snapshot, err := pool.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range snapshot.Accounts {
		if a.ID == neopool.AccountID(record.ID) {
			found = true
			if a.OwnerID != "owner-a" || a.EngineAuthID != record.ID {
				t.Fatalf("login completion attribution: %+v", a)
			}
		}
	}
	if !found {
		t.Fatal("actual engine credential absent from inventory")
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "synthetic-provider-key-not-a-real-secret") {
		t.Fatal("credential not persisted in existing engine config")
	}
	if _, err = engine.CreateAccountAPIKey(ctx, coreauth.AccountKey{Provider: "codex", APIKey: "synthetic-provider-key-not-a-real-secret"}); !errors.Is(err, coreauth.ErrAccountKeyConflict) {
		t.Fatalf("duplicate key silently adopted: %v", err)
	}
	if _, err = engine.CreateAccountAPIKey(ctx, coreauth.AccountKey{Provider: "https://arbitrary-upstream.invalid", APIKey: "another-key"}); !errors.Is(err, coreauth.ErrAccountKeyInvalid) {
		t.Fatalf("owner chose arbitrary upstream: %v", err)
	}
	if _, err = engine.CreateAccountAPIKey(context.Background(), coreauth.AccountKey{Provider: "codex", APIKey: "another-key"}); !errors.Is(err, coreauth.ErrAccountKeyInvalid) {
		t.Fatalf("operator request claimed owner without session: %v", err)
	}
}

// TestOneGitLabTokenServesBothGitLabProviders adds one personal access token
// as GitLab Duo's key and as GitLab Duo Agent's, as NeoPi's GITLAB_TOKEN
// serves both: two accounts, the agent's a native credential of its own,
// and removing it leaves GitLab Duo's.
func TestOneGitLabTokenServesBothGitLabProviders(t *testing.T) {
	k := newKeyEngine(t)
	principal := neo.Principal{UserID: "owner-a", Role: neo.RoleOwner, Session: true}
	ctx := neo.WithPrincipal(context.Background(), principal)
	const token = "glpat-synthetic-not-a-real-secret"
	for _, provider := range []string{"gitlab-duo", "gitlab-duo-agent"} {
		if _, err := k.http.AddAPIKey(ctx, principal, neopool.APIKeyRequest{Provider: provider, APIKey: token}); err != nil {
			t.Fatalf("add %s key: %v", provider, err)
		}
	}
	if k.reloadErr != nil {
		t.Fatal(k.reloadErr)
	}
	var duo, agent *coreauth.Auth
	for _, a := range k.manager.List() {
		if a.Attributes["api_key"] != token {
			continue
		}
		if a.Provider == "gitlab-duo-agent" {
			agent = a
		} else {
			duo = a
		}
	}
	if duo == nil || agent == nil || duo.Attributes["base_url"] != "https://gitlab.com" || agent.Attributes["base_url"] != "https://gitlab.com" || duo.Attributes["compat_name"] == "" {
		t.Fatalf("accounts: duo %+v, agent %+v", duo, agent)
	}
	if _, err := k.http.AddAPIKey(ctx, principal, neopool.APIKeyRequest{Provider: "gitlab-duo-agent", APIKey: token}); err == nil {
		t.Fatal("the agent took the same token twice")
	}
	if err := k.engine.DeleteAccountCredential(ctx, agent.ID); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(k.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "[[api-keys.gitlab-duo-agent]]") || !strings.Contains(string(data), token) {
		t.Fatalf("config after removing the agent's key:\n%s", data)
	}
}
