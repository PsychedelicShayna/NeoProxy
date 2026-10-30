package neoroute

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopool"
)

// EnsureSourceModels creates missing source routes from one inventory snapshot.
// Existing names belong to the administrator: discovery never replaces their
// definitions, IDs, grants, scripts or draw cursors.
func (s *Store) EnsureSourceModels(ctx context.Context, source neopool.Source) error {
	principal, ok := neo.PrincipalFromContext(ctx)
	if !ok || !principal.IsAdmin() {
		return fmt.Errorf("organization administrator session required")
	}
	snapshot, err := s.pools.Snapshot(ctx)
	if err != nil {
		return err
	}
	providers := make(map[string]bool)
	for _, account := range snapshot.Accounts {
		if account.SourceID == source.ID {
			providers[account.ProviderID] = true
		}
	}
	if len(providers) == 0 {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT name FROM neoroute_virtual_models`)
	if err != nil {
		return err
	}
	existing := make(map[string]bool)
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		existing[name] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}

	// Build validation lookup tables only when new routes actually exist.
	var inventory *validationInventory
	updatedBy := principal.UserID
	created := false
	for _, model := range snapshot.Models {
		if !model.VirtualOnly || !providers[model.ProviderID] {
			continue
		}
		// Inventory already computed this source's stable served name.
		name := strings.TrimPrefix(model.Served, "neoproxy/")
		if existing[name] {
			continue
		}
		if inventory == nil {
			indexed := indexValidationInventory(snapshot)
			inventory = &indexed
			// Do not query the identity store while holding its SQLite transaction.
			// The trusted principal ID remains a stable attribution label.
		}
		definition := VirtualModel{
			Name:        name,
			Description: model.Name + " via " + source.Label,
			Stages: []Stage{{
				ID: "source", Model: model.ID, Accounts: AccountScope{All: true},
				Draw: "least-used", PerAccount: 1, Cap: 1, Window: "day", Per: "user",
			}},
			Terminal: Terminal{Kind: "limit", Message: "This shared model is unavailable for your key right now (outside its account scope or local budget). Ask your NeoProxy admin."},
		}
		if err = validateIndexed(definition, *inventory, principal); err != nil {
			return err
		}
		definition.ID = "vm_" + uuid.NewString()
		definition.Served = "neoproxy/" + name
		definition.UpdatedAt = s.now().UnixMilli()
		definition.UpdatedBy = updatedBy
		body, errMarshal := json.Marshal(definition)
		if errMarshal != nil {
			return errMarshal
		}
		result, errInsert := tx.ExecContext(ctx, `INSERT INTO neoroute_virtual_models(id,name,body) VALUES(?,?,?) ON CONFLICT(name) DO NOTHING`, definition.ID, name, body)
		if errInsert != nil {
			return fmt.Errorf("create source virtual model: %w", errInsert)
		}
		count, errCount := result.RowsAffected()
		if errCount != nil {
			return errCount
		}
		created = created || count != 0
		existing[name] = true
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if created {
		s.notifyLocked()
	}
	return nil
}
