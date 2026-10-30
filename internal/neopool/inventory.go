package neopool

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func (s *Store) Snapshot(ctx context.Context) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := Snapshot{Providers: []Provider{}, Accounts: []Account{}, Models: []Model{}}
	if s.manager == nil {
		return out, nil
	}
	auths := s.manager.List()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	providers := map[string]Provider{}
	models := map[string]Model{}
	now := s.now().UnixMilli()
	for _, a := range auths {
		if a == nil || a.ID == "" || a.Provider == "" {
			continue
		}
		id := AccountID(a.ID)
		if _, err = tx.ExecContext(ctx, `INSERT INTO neo_accounts(id,engine_auth_id,provider_id) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET provider_id=excluded.provider_id`, id, a.ID, a.Provider); err != nil {
			return out, err
		}
		var owner sql.NullString
		if err = tx.QueryRowContext(ctx, `SELECT owner_id FROM neo_accounts WHERE id=?`, id).Scan(&owner); err != nil {
			return out, err
		}
		account := Account{ID: id, EngineAuthID: a.ID, ProviderID: a.Provider, OwnerID: owner.String, Label: "account …" + id[len(id)-6:], State: "expired", ModelIDs: []string{}, Windows: []Window{}}
		if sourceID := a.Attributes["neopi_source_id"]; sourceID != "" {
			account.SourceID = sourceID
			account.Label = a.Label
			account.Plan = "NeoPi gateway"
			if err = json.Unmarshal([]byte(a.Attributes["neopi_windows"]), &account.ProviderWindows); err != nil {
				return out, fmt.Errorf("invalid cached provider windows")
			}
		}
		state, next := coreauth.CredentialHealth(a, "", s.now())
		account.State = state
		if !next.IsZero() {
			account.CooldownUntil = next.UnixMilli()
		}
		p, exists := providers[a.Provider]
		if !exists {
			p = Provider{ID: a.Provider, Name: a.Provider, Vendor: a.Provider, Kind: "oauth", Exposed: true}
			if a.Attributes["api_key"] != "" || a.Attributes["source"] == "config" {
				p.Kind = "api-key"
			}
			if account.SourceID != "" {
				p.Kind = "neopi-gateway"
				p.Vendor = a.Attributes["neopi_provider"]
				p.SourceLabel = a.Attributes["neopi_source_label"]
				p.Name = a.Attributes["neopi_source_label"] + " · " + p.Vendor
			}
			var exposed bool
			err = tx.QueryRowContext(ctx, `SELECT exposed FROM neo_provider_exposure WHERE id=?`, p.ID).Scan(&exposed)
			if err == nil {
				p.Exposed = exposed
			} else if err != sql.ErrNoRows {
				return out, err
			}
			providers[p.ID] = p
		}
		for _, info := range registry.GetGlobalRegistry().GetModelsForClient(a.ID) {
			if info == nil || info.ID == "" {
				continue
			}
			modelID := a.Provider + "/" + info.ID
			account.ModelIDs = append(account.ModelIDs, modelID)
			if _, exists = models[modelID]; exists {
				continue
			}
			m := Model{ID: modelID, ProviderID: a.Provider, Name: info.ID, Display: info.DisplayName, Served: "neoproxy/" + modelID, Tags: []string{}, Context: info.ContextLength, Exposed: true}
			if account.SourceID != "" {
				m.VirtualOnly = true
				m.Served = "neoproxy/" + SourceVirtualName(account.SourceID, info.ID)
			}
			if m.Context == 0 {
				m.Context = info.InputTokenLimit
			}
			if m.Display == "" {
				m.Display = info.ID
			}
			var exposed bool
			err = tx.QueryRowContext(ctx, `SELECT exposed FROM neo_model_exposure WHERE id=?`, modelID).Scan(&exposed)
			if err == nil {
				m.Exposed = exposed
			} else if err != sql.ErrNoRows {
				return out, err
			}
			models[modelID] = m
		}
		sort.Strings(account.ModelIDs)
		rows, errRows := tx.QueryContext(ctx, `SELECT kind,capacity,anchor FROM neo_account_windows WHERE account_id=? ORDER BY kind`, id)
		if errRows != nil {
			return out, errRows
		}
		for rows.Next() {
			var w Window
			var anchor int64
			if err = rows.Scan(&w.Kind, &w.Capacity, &anchor); err != nil {
				rows.Close()
				return out, err
			}
			w.StartsAt, w.ResetsAt, err = windowBounds(w.Kind, anchor, now)
			if err != nil {
				continue
			}
			w.Known = true
			w.Source = "configured-metered"
			w.Unit = "tokens"
			account.Windows = append(account.Windows, w)
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return out, err
		}
		rows.Close()
		for i := range account.Windows {
			w := &account.Windows[i]
			if err = tx.QueryRowContext(ctx, `SELECT COALESCE(sum(CASE WHEN state='reserved' THEN reserved ELSE tokens END),0) FROM neo_pool_attempts WHERE account_id=? AND created_at>=? AND created_at<? AND state!='released'`, id, w.StartsAt, w.ResetsAt).Scan(&w.UsedTokens); err != nil {
				return out, err
			}
			w.Used = min(1, float64(w.UsedTokens)/float64(w.Capacity))
		}
		out.Accounts = append(out.Accounts, account)
	}
	for _, p := range providers {
		out.Providers = append(out.Providers, p)
	}
	for _, m := range models {
		out.Models = append(out.Models, m)
	}
	sort.Slice(out.Accounts, func(i, j int) bool { return out.Accounts[i].ID < out.Accounts[j].ID })
	sort.Slice(out.Providers, func(i, j int) bool { return out.Providers[i].ID < out.Providers[j].ID })
	sort.Slice(out.Models, func(i, j int) bool { return out.Models[i].ID < out.Models[j].ID })
	if err = tx.Commit(); err != nil {
		return out, err
	}
	return out, nil
}

func (s *Store) SetExposure(ctx context.Context, id string, exposed, provider bool) error {
	snapshot, err := s.Snapshot(ctx)
	if err != nil {
		return err
	}
	found := false
	if provider {
		for _, p := range snapshot.Providers {
			if p.ID == id {
				found = true
				break
			}
		}
	} else {
		for _, m := range snapshot.Models {
			if m.ID == id {
				found = true
				break
			}
		}
	}
	if !found {
		return ErrUnavailable
	}
	table := "neo_model_exposure"
	if provider {
		table = "neo_provider_exposure"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.db.ExecContext(ctx, `INSERT INTO `+table+`(id,exposed) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET exposed=excluded.exposed`, id, exposed)
	if err == nil {
		s.notifyLocked()
	}
	return err
}

// Stats enforces ownership in SQL, independent of any client-supplied filters.
func (s *Store) Stats(ctx context.Context, userID string, admin bool) ([]AccountStats, error) {
	query := `WITH owned AS (
 SELECT account_id,owner_id FROM neo_pool_attempts WHERE state!='released' UNION SELECT id,COALESCE(owner_id,'') FROM neo_accounts
 ) SELECT o.account_id,o.owner_id,COALESCE(sum(p.input_tokens),0),COALESCE(sum(p.output_tokens),0),COALESCE(sum(p.cached_tokens),0),
 COALESCE(sum(CASE WHEN p.estimated=1 THEN p.tokens ELSE 0 END),0),COALESCE(sum(CASE WHEN p.state='reserved' THEN p.reserved ELSE 0 END),0),
 COALESCE(sum(CASE WHEN p.estimated=1 OR p.state='reserved' THEN 1 ELSE 0 END),0),count(p.id)
 FROM owned o LEFT JOIN neo_pool_attempts p ON p.account_id=o.account_id AND p.owner_id=o.owner_id AND p.state!='released'`
	args := []any{}
	if !admin {
		query += ` WHERE o.owner_id=?`
		args = append(args, userID)
	}
	query += ` GROUP BY o.account_id,o.owner_id ORDER BY o.account_id,o.owner_id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AccountStats{}
	for rows.Next() {
		var row AccountStats
		if err = rows.Scan(&row.AccountID, &row.OwnerID, &row.InputTokens, &row.OutputTokens, &row.CachedTokens, &row.EstimatedTokens, &row.ReservedTokens, &row.UnknownAttempts, &row.Attempts); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func validID(id string) error {
	if strings.TrimSpace(id) == "" || len(id) > 256 {
		return fmt.Errorf("invalid identifier")
	}
	return nil
}
