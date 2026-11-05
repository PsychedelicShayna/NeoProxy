package cliproxy

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// neoPiDiscovery caches the roster a NeoPi-ported provider discovers per
// account. NeoPi treats discovery as authoritative and serves its bundled
// seed until it answers, so registration reads the cached listing and starts
// a background fetch when it is missing or stale; a successful fetch
// re-registers the account.
type neoPiDiscovery struct {
	provider string
	// ttl is how long a listing stays fresh; retry backs off after a failure.
	ttl, retry, timeout time.Duration
	bundled             func() []*ModelInfo
	fetch               func(context.Context, *config.Config, *coreauth.Auth) ([]*ModelInfo, error)
	// match selects the accounts the discovery serves when provider alone
	// cannot (keys at an address, one provider with two NeoPi catalogs).
	match func(*coreauth.Auth) bool

	mu      sync.Mutex
	entries map[string]*neoPiDiscoveryEntry
}

type neoPiDiscoveryEntry struct {
	models    []*ModelInfo
	fetchedAt time.Time
	failedAt  time.Time
	inFlight  bool
}

// neoPiDiscoverySweepEvery is how often stale listings are refetched for
// accounts that never re-register (imported, refresh-locked ones).
const neoPiDiscoverySweepEvery = 15 * time.Minute

var (
	// kimiDiscovery lists GET /coding/v1/models.
	kimiDiscovery = &neoPiDiscovery{provider: "kimi", ttl: 6 * time.Hour, retry: 5 * time.Minute, timeout: 30 * time.Second,
		bundled: registry.KimiBundledModels, fetch: executor.FetchKimiModels, entries: map[string]*neoPiDiscoveryEntry{}}
	// devinDiscovery calls GetCliModelConfigs: NeoPi's model manager default
	// 2h cache TTL and its 15s discovery bound.
	devinDiscovery = &neoPiDiscovery{provider: "devin", ttl: 2 * time.Hour, retry: 5 * time.Minute, timeout: 15 * time.Second,
		bundled: registry.DevinBundledModels, fetch: executor.FetchDevinModels, entries: map[string]*neoPiDiscoveryEntry{}}

	// museCodeDiscovery lists a Meta sign-in's models: NeoPi's muse-code
	// manager (authoritative `/v1/models` over the subscription key).
	museCodeDiscovery = newResponsesDiscovery(registry.ResponsesMuseCode, "meta")
	// metaAPIKeyDiscovery lists a Meta Model API key's models.
	metaAPIKeyDiscovery = newResponsesDiscovery(registry.ResponsesMeta, "meta")
	// sakanaDiscovery and abliterationDiscovery list the openai-compatibility
	// keys at those gateways, NeoPi's authoritative `/v1/models` managers.
	sakanaDiscovery       = newResponsesDiscovery(registry.ResponsesSakana, "")
	abliterationDiscovery = newResponsesDiscovery(registry.ResponsesAbliterate, "")

	// nousDiscovery is the Nous Portal extension's fetchDynamicModels: its
	// 15s bound, serving the extension's bundled snapshot until it answers.
	nousDiscovery = &neoPiDiscovery{provider: "nous-portal", ttl: 2 * time.Hour, retry: 5 * time.Minute, timeout: executor.NousDiscoveryTimeout,
		match: func(a *coreauth.Auth) bool {
			return a.Provider == "nous-portal" || neoPiCompatProvider(a) == "nous-portal"
		},
		bundled: func() []*ModelInfo { return registry.NousBundledModels("nous-portal") },
		fetch:   executor.FetchNousModels, entries: map[string]*neoPiDiscoveryEntry{}}
	// marinerisDiscovery is the Marineris extension's fetchDynamicModels: the
	// gateway lists what the key is scoped to and there is no bundled seed.
	marinerisDiscovery = &neoPiDiscovery{provider: "marineris", ttl: 2 * time.Hour, retry: 5 * time.Minute, timeout: executor.MarinerisDiscoveryTimeout,
		match:   func(a *coreauth.Auth) bool { return neoPiCompatProvider(a) == "marineris" },
		bundled: func() []*ModelInfo { return nil }, fetch: executor.FetchMarinerisModels, entries: map[string]*neoPiDiscoveryEntry{}}

	neoPiDiscoveries = []*neoPiDiscovery{kimiDiscovery, devinDiscovery, museCodeDiscovery, metaAPIKeyDiscovery, sakanaDiscovery, abliterationDiscovery, nousDiscovery, marinerisDiscovery}
)

// newResponsesDiscovery is the discovery of one NeoPi Responses provider: the
// model manager's 2h cache and 10s discovery bound over executor.FetchResponsesModels.
// provider is the engine provider its accounts carry, or "" for the
// openai-compatibility keys served by address.
func newResponsesDiscovery(neoPiProvider, provider string) *neoPiDiscovery {
	return &neoPiDiscovery{provider: provider, ttl: 2 * time.Hour, retry: 5 * time.Minute, timeout: 10 * time.Second,
		match: func(a *coreauth.Auth) bool { return executor.ResponsesProviderOfAuth(a) == neoPiProvider },
		bundled: func() []*ModelInfo {
			owner, modelType := "meta", "meta"
			if provider == "" {
				owner, modelType = neoPiProvider, "openai-compatibility"
			}
			return registry.ResponsesModels(neoPiProvider, owner, modelType, nil)
		},
		fetch: executor.FetchResponsesModels, entries: map[string]*neoPiDiscoveryEntry{}}
}

// models returns the models to register for a and whether its cached listing
// needs a (re)fetch.
func (d *neoPiDiscovery) models(a *coreauth.Auth, now time.Time) ([]*ModelInfo, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	entry := d.entries[a.ID]
	if entry == nil {
		return d.bundled(), true
	}
	fresh := !entry.fetchedAt.IsZero() && now.Sub(entry.fetchedAt) < d.ttl
	backingOff := !entry.failedAt.IsZero() && now.Sub(entry.failedAt) < d.retry
	stale := !fresh && !backingOff && !entry.inFlight
	if entry.models == nil {
		return d.bundled(), stale
	}
	out := make([]*ModelInfo, 0, len(entry.models))
	for _, model := range entry.models {
		clone := *model
		clone.NeoPi = model.NeoPi.Clone()
		out = append(out, &clone)
	}
	return out, stale
}

// registerNeoPiDiscovered returns a's models for registration, queueing a
// fetch when its listing is stale.
func (s *Service) registerNeoPiDiscovered(d *neoPiDiscovery, a *coreauth.Auth) []*ModelInfo {
	models, stale := d.models(a, time.Now())
	if stale {
		s.queueNeoPiDiscovery(d, a)
	}
	return models
}

// queueNeoPiDiscovery fetches a's listing in the background unless a fetch
// is already running, then re-registers the account with the result.
func (s *Service) queueNeoPiDiscovery(d *neoPiDiscovery, a *coreauth.Auth) {
	if s == nil || a == nil || a.ID == "" {
		return
	}
	cfg := s.currentConfig()
	if cfg != nil && cfg.Home.Enabled {
		return
	}
	d.mu.Lock()
	entry := d.entries[a.ID]
	if entry == nil {
		entry = &neoPiDiscoveryEntry{}
		d.entries[a.ID] = entry
	}
	if entry.inFlight {
		d.mu.Unlock()
		return
	}
	entry.inFlight = true
	d.mu.Unlock()

	snapshot := a.Clone()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), d.timeout)
		defer cancel()
		models, err := d.fetch(ctx, cfg, snapshot)
		d.mu.Lock()
		entry.inFlight = false
		if err != nil {
			entry.failedAt = time.Now()
		} else {
			entry.models, entry.fetchedAt, entry.failedAt = models, time.Now(), time.Time{}
		}
		d.mu.Unlock()
		if err != nil {
			log.WithError(err).Warnf("%s: model discovery failed for %s", d.provider, snapshot.ID)
			return
		}
		log.Infof("%s: discovered %d models for %s", d.provider, len(models), snapshot.ID)
		if s.coreManager == nil {
			return
		}
		if latest, ok := s.coreManager.GetByID(snapshot.ID); ok && latest != nil && !latest.Disabled && d.serves(latest) {
			s.refreshModelRegistrationForAuth(latest)
		}
	}()
}

// forgetNeoPiDiscovery drops the cached listings of a removed account.
func forgetNeoPiDiscovery(id string) {
	for _, d := range neoPiDiscoveries {
		d.mu.Lock()
		delete(d.entries, id)
		d.mu.Unlock()
	}
}

// runNeoPiDiscovery refetches stale listings so accounts that never
// re-register still follow plan changes.
func (s *Service) runNeoPiDiscovery(ctx context.Context) {
	ticker := time.NewTicker(neoPiDiscoverySweepEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if s.coreManager == nil {
			continue
		}
		now := time.Now()
		for _, a := range s.coreManager.List() {
			if a == nil || a.Disabled {
				continue
			}
			for _, d := range neoPiDiscoveries {
				if !d.serves(a) {
					continue
				}
				if _, stale := d.models(a, now); stale {
					s.queueNeoPiDiscovery(d, a)
				}
			}
		}
	}
}

// serves reports whether the discovery lists a's models.
func (d *neoPiDiscovery) serves(a *coreauth.Auth) bool {
	if a == nil {
		return false
	}
	if d.match != nil {
		return d.match(a)
	}
	return a.Provider == d.provider
}

// metaDiscoveryFor selects the NeoPi catalog a Meta account lists: a sign-in
// is Muse Code's, a key is the Model API's.
func metaDiscoveryFor(a *coreauth.Auth) *neoPiDiscovery {
	if museCodeDiscovery.serves(a) {
		return museCodeDiscovery
	}
	return metaAPIKeyDiscovery
}

// neoPiResponsesModelsFor replaces the configured model list of an
// openai-compatibility key at a Sakana or Abliteration address with NeoPi's
// discovered catalog; any other account keeps fallback.
func (s *Service) neoPiResponsesModelsFor(a *coreauth.Auth, fallback []*ModelInfo) []*ModelInfo {
	for _, d := range []*neoPiDiscovery{sakanaDiscovery, abliterationDiscovery} {
		if !d.serves(a) {
			continue
		}
		owner := strings.TrimSpace(a.Attributes["compat_name"])
		if owner == "" {
			owner = strings.TrimSpace(a.Provider)
		}
		models := s.registerNeoPiDiscovered(d, a)
		for _, model := range models {
			model.OwnedBy = owner
		}
		return models
	}
	return fallback
}
