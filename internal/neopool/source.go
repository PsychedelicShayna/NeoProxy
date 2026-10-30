package neopool

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Source contains only safe connection status. The gateway bearer is stored in
// the same private SQLite file as account ownership; it is never returned.
type Source struct {
	ID           string `json:"id"`
	Label        string `json:"label"`
	URL          string `json:"url"`
	OwnerID      string `json:"ownerId"`
	AdminID      string `json:"-"`
	Enabled      bool   `json:"enabled"`
	Status       string `json:"status"`
	CheckedAt    int64  `json:"checkedAt"`
	AccountCount int    `json:"accountCount"`
	ModelCount   int    `json:"modelCount"`
	revision     int64
}

type SourceInput struct {
	Label   string `json:"label"`
	URL     string `json:"url"`
	Bearer  string `json:"bearer"`
	OwnerID string `json:"ownerId"`
	Enabled bool   `json:"enabled"`
}

type sourceWindow struct {
	Kind     string   `json:"kind"`
	Known    bool     `json:"known"`
	Used     *float64 `json:"used"`
	ResetsAt int64    `json:"resetsAt"`
}

type sourceCredential struct {
	ID       string         `json:"id"`
	Provider string         `json:"provider"`
	Label    string         `json:"label"`
	Models   []string       `json:"models"`
	State    string         `json:"state"`
	Windows  []sourceWindow `json:"windows"`
}

// A wire-only type prevents ModelInfo's custom decoder from consuming kind.
type sourceCatalogModelInfo registry.ModelInfo

type sourceCatalogModel struct {
	sourceCatalogModelInfo
	Kind string `json:"kind"`
}

func sourceAdmin(ctx context.Context) (neo.Principal, error) {
	p, ok := neo.PrincipalFromContext(ctx)
	if !ok || !p.Session || p.Role != neo.RoleAdmin {
		return p, ErrForbidden
	}
	return p, nil
}

func normalizeSourceURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("gateway URL must be an HTTP(S) origin or base path without credentials, query or fragment")
	}
	u.Path = strings.TrimSuffix(strings.TrimRight(u.Path, "/"), "/v1")
	return strings.TrimRight(u.String(), "/"), nil
}

func (s *Store) Sources(ctx context.Context) ([]Source, error) {
	if _, err := sourceAdmin(ctx); err != nil {
		return nil, err
	}
	return s.sources(ctx)
}

func (s *Store) sources(ctx context.Context) ([]Source, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,label,url,owner_id,admin_id,enabled,status,checked_at,account_count,model_count FROM neo_pool_sources ORDER BY label,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Source{}
	for rows.Next() {
		var v Source
		if err = rows.Scan(&v.ID, &v.Label, &v.URL, &v.OwnerID, &v.AdminID, &v.Enabled, &v.Status, &v.CheckedAt, &v.AccountCount, &v.ModelCount); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// SaveSource is admin-only: an owner/member cannot inject an arbitrary gateway
// credential or claim remote accounts by submitting a row identifier.
func (s *Store) SaveSource(ctx context.Context, id string, in SourceInput) (Source, error) {
	p, err := sourceAdmin(ctx)
	if err != nil {
		return Source{}, err
	}
	base, err := normalizeSourceURL(in.URL)
	if err != nil {
		return Source{}, err
	}
	in.Label = strings.TrimSpace(in.Label)
	in.Bearer = strings.TrimSpace(in.Bearer)
	if in.Label == "" || len(in.Label) > 128 || strings.ContainsAny(in.Bearer, "\r\n") || len(in.Bearer) > 16384 {
		return Source{}, fmt.Errorf("valid source label and bearer required")
	}
	if in.OwnerID == "" {
		in.OwnerID = p.UserID
	}
	s.sourceMu.Lock()
	defer s.sourceMu.Unlock()
	var owners int
	if err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM neo_users WHERE id=? AND state='active' AND role IN ('admin','owner')`, in.OwnerID).Scan(&owners); err != nil {
		return Source{}, err
	}
	if owners != 1 {
		return Source{}, ErrForbidden
	}
	if id == "" {
		if in.Bearer == "" {
			return Source{}, fmt.Errorf("gateway bearer required")
		}
		id = "src_" + uuid.NewString()
		_, err = s.db.ExecContext(ctx, `INSERT INTO neo_pool_sources(id,label,url,bearer,owner_id,admin_id,enabled) VALUES(?,?,?,?,?,?,?)`, id, in.Label, base, in.Bearer, in.OwnerID, p.UserID, in.Enabled)
	} else {
		var previousURL, bearer string
		if err = s.db.QueryRowContext(ctx, `SELECT url,bearer FROM neo_pool_sources WHERE id=?`, id).Scan(&previousURL, &bearer); err != nil {
			return Source{}, err
		}
		// Never send an existing secret to a newly edited destination implicitly.
		if previousURL != base && in.Bearer == "" {
			return Source{}, fmt.Errorf("enter a gateway bearer when changing the URL")
		}
		if in.Bearer == "" {
			in.Bearer = bearer
		}
		_, err = s.db.ExecContext(ctx, `UPDATE neo_pool_sources SET label=?,url=?,bearer=?,owner_id=?,admin_id=?,enabled=?,status='pending',revision=revision+1 WHERE id=?`, in.Label, base, in.Bearer, in.OwnerID, p.UserID, in.Enabled, id)
	}
	if err != nil {
		return Source{}, err
	}
	s.removeSourceAuths(ctx, id)
	v := Source{ID: id, Label: in.Label, URL: base, OwnerID: in.OwnerID, AdminID: p.UserID, Enabled: in.Enabled, Status: "pending"}
	if !in.Enabled {
		v.Status = "disabled"
		_, err = s.db.ExecContext(ctx, `UPDATE neo_pool_sources SET status='disabled',account_count=0,model_count=0 WHERE id=?`, id)
	}
	s.mu.Lock()
	s.notifyLocked()
	s.mu.Unlock()
	return v, err
}

func (s *Store) DeleteSource(ctx context.Context, id string) error {
	if _, err := sourceAdmin(ctx); err != nil {
		return err
	}
	s.sourceMu.Lock()
	defer s.sourceMu.Unlock()
	result, err := s.db.ExecContext(ctx, `DELETE FROM neo_pool_sources WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrUnavailable
	}
	s.removeSourceAuths(ctx, id)
	s.mu.Lock()
	s.notifyLocked()
	s.mu.Unlock()
	return nil
}

// ConfigureSources wires the existing execution and stored virtual-model seams.
// It must be called before StartSources; neither callback runs under the pool lock.
func (s *Store) ConfigureSources(factory func(string) coreauth.ProviderExecutor, publish func(context.Context, Source) error) {
	s.sourceFactory = factory
	s.sourcePublish = publish
}

// StartSources restores persisted connections and reconciles live broker inventory.
// Cancellation, rather than a network deadline, governs discovery and shutdown.
func (s *Store) StartSources() {
	s.sourceLifecycleMu.Lock()
	defer s.sourceLifecycleMu.Unlock()
	if s.sourceCancel != nil || s.manager == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.sourceCancel = cancel
	s.sourceDone = make(chan struct{})
	done := s.sourceDone
	go func() {
		defer close(done)
		var pending sync.Map
		var jobs sync.WaitGroup
		defer jobs.Wait()
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			list, err := s.sources(ctx)
			if err == nil {
				for _, v := range list {
					if ctx.Err() != nil {
						return
					}
					if v.Enabled {
						if _, running := pending.LoadOrStore(v.ID, struct{}{}); !running {
							jobs.Add(1)
							go func(id string) {
								defer jobs.Done()
								defer pending.Delete(id)
								_ = s.SyncSource(ctx, id)
							}(v.ID)
						}
					}
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (s *Store) StopSources() {
	s.sourceLifecycleMu.Lock()
	defer s.sourceLifecycleMu.Unlock()
	if s.sourceCancel != nil {
		s.sourceCancel()
		<-s.sourceDone
		s.sourceCancel = nil
	}
}

func (s *Store) removeSourceAuths(ctx context.Context, id string) {
	if s.manager == nil {
		return
	}
	for _, a := range s.manager.List() {
		if a.Attributes["neopi_source_id"] != id {
			continue
		}
		registry.GetGlobalRegistry().UnregisterClient(a.ID)
		s.manager.Remove(ctx, a.ID)
	}
}

// gatewayJSON never follows redirects with a bearer and never returns upstream
// bodies as error messages (they can contain secrets or private account labels).
func gatewayJSON(ctx context.Context, base, bearer, path string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return fmt.Errorf("invalid gateway request")
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("gateway connection failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("gateway returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024+1))
	if err != nil || len(data) > 8*1024*1024 {
		return fmt.Errorf("gateway inventory unavailable or too large")
	}
	if err = json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("invalid gateway inventory")
	}
	return nil
}

func sourceProvider(id, provider string) string {
	return "neopi-" + strings.TrimPrefix(AccountID(id), "acct_") + "-" + provider
}

func sourceAuthID(sourceID, credentialID string) string {
	return "neopi:" + sourceID + ":" + credentialID
}

func (s *Store) SyncSource(ctx context.Context, id string) error {
	v, err := s.syncSource(ctx, id)
	if err != nil {
		return err
	}
	if s.sourcePublish != nil && v.Enabled {
		if err = s.sourcePublish(ctx, v); err != nil {
			_, _ = s.db.ExecContext(ctx, `UPDATE neo_pool_sources SET status='connected; virtual model setup failed' WHERE id=? AND revision=?`, id, v.revision)
			return err
		}
	}
	return nil
}

func (s *Store) syncSource(ctx context.Context, id string) (Source, error) {
	var v Source
	var bearer string
	err := s.db.QueryRowContext(ctx, `UPDATE neo_pool_sources SET revision=revision+1 WHERE id=? RETURNING id,label,url,owner_id,admin_id,enabled,bearer,revision`, id).Scan(&v.ID, &v.Label, &v.URL, &v.OwnerID, &v.AdminID, &v.Enabled, &bearer, &v.revision)
	if err != nil {
		return v, err
	}
	if !v.Enabled {
		return v, nil
	}
	var accounts struct {
		Data []sourceCredential `json:"data"`
	}
	var catalog struct {
		Data []*sourceCatalogModel `json:"data"`
	}
	var discoveryErr error
	if s.manager == nil || s.sourceFactory == nil {
		discoveryErr = fmt.Errorf("gateway executor unavailable")
	} else if discoveryErr = gatewayJSON(ctx, v.URL, bearer, "/v1/models", &catalog); discoveryErr == nil {
		discoveryErr = gatewayJSON(ctx, v.URL, bearer, "/v1/neoproxy/accounts", &accounts)
	}
	// Discovery never holds the mutation lock. Disable/disconnect can revoke a
	// stalled source immediately, and late results cannot resurrect an old config.
	s.sourceMu.Lock()
	defer s.sourceMu.Unlock()
	var revision int64
	if err = s.db.QueryRowContext(ctx, `SELECT revision FROM neo_pool_sources WHERE id=? AND enabled=1`, id).Scan(&revision); err != nil || revision != v.revision {
		return v, ErrConflict
	}
	fail := func(cause error) (Source, error) {
		s.removeSourceAuths(ctx, id)
		_, _ = s.db.ExecContext(ctx, `UPDATE neo_pool_sources SET status=?,checked_at=?,account_count=0,model_count=0 WHERE id=?`, cause.Error(), s.now().UnixMilli(), id)
		s.mu.Lock()
		s.notifyLocked()
		s.mu.Unlock()
		return v, cause
	}
	if discoveryErr != nil {
		return fail(discoveryErr)
	}
	if accounts.Data == nil || catalog.Data == nil {
		return fail(fmt.Errorf("invalid gateway inventory"))
	}
	models := map[string]*sourceCatalogModel{}
	for _, m := range catalog.Data {
		if m != nil && m.ID != "" {
			models[m.ID] = m
		}
	}
	seen := map[string]bool{}
	uniqueModels := map[string]bool{}
	// Validate the whole inventory before mutating runtime or ownership.
	for _, a := range accounts.Data {
		if a.ID == "" || strings.ContainsAny(a.ID, "\r\n") || len(a.ID) > 512 || a.Provider == "" || len(a.Provider) > 128 || strings.ContainsAny(a.Provider, "/\r\n ") || seen[a.ID] || (a.State != "healthy" && a.State != "blocked") {
			return fail(fmt.Errorf("invalid gateway account inventory"))
		}
		seen[a.ID] = true
		for _, m := range a.Models {
			if !strings.HasPrefix(m, a.Provider+"/") || models[m] == nil {
				return fail(fmt.Errorf("gateway account catalogue mismatch"))
			}
		}
		for _, w := range a.Windows {
			if w.Kind == "" || w.ResetsAt < 0 || (w.Used != nil && (*w.Used < 0 || *w.Used > 1)) {
				return fail(fmt.Errorf("invalid gateway usage window"))
			}
		}
	}
	seen = map[string]bool{}
	for _, a := range accounts.Data {
		provider := sourceProvider(id, a.Provider)
		if _, exists := s.manager.Executor(provider); !exists {
			s.manager.RegisterExecutor(s.sourceFactory(provider))
		}
		authID := sourceAuthID(id, a.ID)
		seen[authID] = true
		windows := make([]Window, 0, len(a.Windows))
		for _, native := range a.Windows {
			w := Window{Kind: native.Kind, Known: native.Known && native.Used != nil, ResetsAt: native.ResetsAt, Source: "provider-reported", Unit: "fraction"}
			if w.Known {
				w.Used = *native.Used
			}
			windows = append(windows, w)
		}
		raw, _ := json.Marshal(windows)
		label := strings.TrimSpace(a.Label)
		if label == "" {
			label = a.Provider + " account"
		}
		if len(label) > 256 {
			label = label[:256]
		}
		attrs := map[string]string{"neopi_source_id": id, "neopi_source_label": v.Label, "neopi_credential_id": a.ID, "neopi_provider": a.Provider, "neopi_windows": string(raw), "base_url": v.URL + "/v1"}
		// Install durable ownership before publishing runtime inventory. A concurrent
		// snapshot cannot create a newly imported account without its default owner.
		if _, err = s.db.ExecContext(ctx, `INSERT INTO neo_accounts(id,engine_auth_id,provider_id,owner_id) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET provider_id=excluded.provider_id,owner_id=COALESCE(neo_accounts.owner_id,excluded.owner_id)`, AccountID(authID), authID, provider, v.OwnerID); err != nil {
			return fail(fmt.Errorf("save gateway account failed"))
		}
		current, exists := s.manager.GetByID(authID)
		auth := &coreauth.Auth{ID: authID, Provider: provider, Label: label, Status: coreauth.StatusActive, Attributes: attrs}
		if exists {
			auth = current.Clone()
			auth.Label, auth.Attributes = label, attrs
		}
		auth.Disabled = a.State != "healthy"
		if auth.Disabled {
			auth.Status = coreauth.StatusDisabled
		} else if auth.Status == coreauth.StatusDisabled {
			auth.Status = coreauth.StatusActive
		}
		// Runtime-only auths contain no bearer, provider token or refresh token.
		// Preserve engine cooldowns and avoid changing registration epochs on polls.
		if !exists {
			_, err = s.manager.Register(coreauth.WithSkipPersist(ctx), auth)
		} else if current.Disabled != auth.Disabled || current.Label != auth.Label || !maps.Equal(current.Attributes, attrs) {
			_, err = s.manager.Update(coreauth.WithSkipPersist(ctx), auth)
		}
		if err != nil {
			return fail(fmt.Errorf("register gateway account failed"))
		}
		infos := make([]*registry.ModelInfo, 0, len(a.Models))
		for _, m := range a.Models {
			info := models[m]
			// These source VMs use the gateway's chat/Responses/Messages routes.
			// Other gateway modalities require their own execution interfaces.
			if info.Kind != "" && info.Kind != "chat" {
				continue
			}
			copyInfo := registry.ModelInfo(info.sourceCatalogModelInfo)
			infos = append(infos, &copyInfo)
			uniqueModels[m] = true
		}
		if !reflect.DeepEqual(registry.GetGlobalRegistry().GetModelsForClient(authID), infos) {
			registry.GetGlobalRegistry().RegisterClient(authID, provider, infos)
		}
	}
	for _, a := range s.manager.List() {
		if a.Attributes["neopi_source_id"] == id && !seen[a.ID] {
			registry.GetGlobalRegistry().UnregisterClient(a.ID)
			s.manager.Remove(ctx, a.ID)
		}
	}
	v.Status, v.CheckedAt = "connected", s.now().UnixMilli()
	v.AccountCount, v.ModelCount = len(accounts.Data), len(uniqueModels)
	_, err = s.db.ExecContext(ctx, `UPDATE neo_pool_sources SET status=?,checked_at=?,account_count=?,model_count=? WHERE id=?`, v.Status, v.CheckedAt, v.AccountCount, v.ModelCount, id)
	s.mu.Lock()
	s.notifyLocked()
	s.mu.Unlock()
	return v, err
}

// PrepareSourceAuth is rechecked on every execution attempt, including retries.
// It resolves the current source bearer privately and rejects stale credentials,
// cross-provider models and removed source registrations before network dispatch.
func (s *Store) PrepareSourceAuth(ctx context.Context, a *coreauth.Auth, model string) (*coreauth.Auth, error) {
	if a == nil || s.manager == nil {
		return nil, ErrUnavailable
	}
	p, authorized := neo.PrincipalFromContext(ctx)
	if !authorized || !p.AllowsAccount(AccountID(a.ID)) {
		return nil, ErrForbidden
	}
	var ownerID sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT owner_id FROM neo_accounts WHERE id=?`, AccountID(a.ID)).Scan(&ownerID); err != nil {
		return nil, ErrUnavailable
	}
	if !ownerID.Valid || (p.Role == neo.RoleOwner && ownerID.String != p.UserID) {
		return nil, ErrForbidden
	}
	current, ok := s.manager.GetByID(a.ID)
	if !ok || current.Disabled || current.Attributes["neopi_source_id"] == "" || !registry.GetGlobalRegistry().ClientSupportsModel(a.ID, model) {
		return nil, ErrUnavailable
	}
	if !strings.HasPrefix(model, current.Attributes["neopi_provider"]+"/") {
		return nil, ErrUnavailable
	}
	var base, bearer string
	if err := s.db.QueryRowContext(ctx, `SELECT url,bearer FROM neo_pool_sources WHERE id=? AND enabled=1`, current.Attributes["neopi_source_id"]).Scan(&base, &bearer); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUnavailable
		}
		return nil, err
	}
	out := current.Clone()
	out.Attributes["base_url"], out.Attributes["api_key"] = base+"/v1", bearer
	return out, nil
}

// SourceVirtualName is stable across token refresh, catalogue refresh and restart.
// The complete human-readable source/provider/model identity is the description.
func SourceVirtualName(sourceID, model string) string {
	parts := strings.Split(model, "/")
	readable := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		return '-'
	}, strings.ToLower(parts[len(parts)-1]))
	if len(readable) > 15 {
		readable = readable[:15]
	}
	readable = strings.Trim(readable, "-")
	if readable == "" {
		readable = "model"
	}
	return "np-" + readable + "-" + strings.TrimPrefix(AccountID(sourceID+"\x00"+model), "acct_")[:12]
}
