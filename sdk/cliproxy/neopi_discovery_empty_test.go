package cliproxy

import (
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// A successful empty roster registers no models for the cycle but is retried at
// the failure cadence, not cached for the full TTL (model-manager.ts).
func TestNeoPiDiscoveryRetriesEmptyRosterEarly(t *testing.T) {
	d := &neoPiDiscovery{provider: "x", ttl: 2 * time.Hour, retry: 5 * time.Minute, bundled: func() []*ModelInfo { return []*ModelInfo{{ID: "seed"}} },
		entries: map[string]*neoPiDiscoveryEntry{}}
	auth := &coreauth.Auth{ID: "a", Provider: "x"}
	now := time.Now()
	d.entries["a"] = &neoPiDiscoveryEntry{models: []*ModelInfo{}, fetchedAt: now}
	if models, stale := d.models(auth, now.Add(time.Minute)); len(models) != 0 || stale {
		t.Fatalf("within the retry window: %d models, stale=%v", len(models), stale)
	}
	if models, stale := d.models(auth, now.Add(6*time.Minute)); len(models) != 0 || !stale {
		t.Fatalf("after the retry window: %d models, stale=%v; want empty and stale", len(models), stale)
	}
	d.entries["a"] = &neoPiDiscoveryEntry{models: []*ModelInfo{{ID: "m"}}, fetchedAt: now}
	if _, stale := d.models(auth, now.Add(6*time.Minute)); stale {
		t.Fatal("a non-empty roster keeps the full TTL")
	}
}
