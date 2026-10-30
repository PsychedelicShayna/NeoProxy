package neoroute

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopool"
)

// Pool is the shared inventory and transactional quota ledger, not a second store.
type Pool interface {
	Snapshot(context.Context) (neopool.Snapshot, error)
	Usage(context.Context, string) ([]neopool.Usage, error)
	Reserve(context.Context, neopool.Reservation) error
	ReserveAndAdvance(context.Context, neopool.Reservation, func(*sql.Tx) error) error
	Settle(context.Context, neopool.Settlement) error
	Release(context.Context, string) error
	Changes() <-chan struct{}
}

type Store struct {
	identity    *neo.Store
	db          *sql.DB
	pools       Pool
	mu          sync.Mutex
	selectionMu sync.Mutex
	hook        ScriptHook
	changed     chan struct{}
	now         func() time.Time
}

func New(identity *neo.Store, pools Pool) (*Store, error) {
	if identity == nil || pools == nil {
		return nil, fmt.Errorf("routing requires shared identity store and pool ledger")
	}
	s := &Store{identity: identity, db: identity.DB(), pools: pools, changed: make(chan struct{}), now: time.Now}
	if err := s.migrate(context.Background()); err != nil {
		return nil, fmt.Errorf("migrate routing: %w", err)
	}
	return s, nil
}

func (s *Store) migrate(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `
 CREATE TABLE IF NOT EXISTS neoroute_schema(version INTEGER NOT NULL);
 INSERT INTO neoroute_schema(version) SELECT 0 WHERE NOT EXISTS (SELECT 1 FROM neoroute_schema);
 `); err != nil {
		return err
	}
	var version int
	if err = tx.QueryRowContext(ctx, `SELECT version FROM neoroute_schema`).Scan(&version); err != nil {
		return err
	}
	if version != 0 && version != 1 {
		return fmt.Errorf("unsupported routing schema version %d", version)
	}
	if version == 0 {
		// IF NOT EXISTS adopts the initial unversioned C schema without changing
		// existing data. The version and all DDL commit or roll back together.
		if _, err = tx.ExecContext(ctx, `
 CREATE TABLE IF NOT EXISTS neoroute_virtual_models(id TEXT PRIMARY KEY,name TEXT NOT NULL UNIQUE,body BLOB NOT NULL);
 CREATE TABLE IF NOT EXISTS neoroute_cursors(vm_id TEXT NOT NULL,stage_id TEXT NOT NULL,cursor INTEGER NOT NULL DEFAULT 0,PRIMARY KEY(vm_id,stage_id),FOREIGN KEY(vm_id) REFERENCES neoroute_virtual_models(id) ON DELETE CASCADE);
 CREATE TABLE IF NOT EXISTS neoroute_requests(id TEXT PRIMARY KEY,user_id TEXT NOT NULL,key_id TEXT NOT NULL,at INTEGER NOT NULL,outcome TEXT NOT NULL,asked TEXT NOT NULL,body BLOB NOT NULL);
 CREATE INDEX IF NOT EXISTS neoroute_requests_user_at ON neoroute_requests(user_id,at DESC);
 CREATE TABLE IF NOT EXISTS neoroute_admissions(id TEXT PRIMARY KEY,user_id TEXT NOT NULL,at INTEGER NOT NULL,day INTEGER NOT NULL,tokens INTEGER NOT NULL);
 CREATE INDEX IF NOT EXISTS neoroute_admissions_user_at ON neoroute_admissions(user_id,at);
 UPDATE neoroute_schema SET version=1;
 `); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) SetScriptHook(h ScriptHook) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hook = h
	s.notifyLocked()
}
func (s *Store) notifyLocked()            { close(s.changed); s.changed = make(chan struct{}) }
func (s *Store) changes() <-chan struct{} { s.mu.Lock(); defer s.mu.Unlock(); return s.changed }
func (s *Store) scriptHook() ScriptHook   { s.mu.Lock(); defer s.mu.Unlock(); return s.hook }

func (s *Store) VirtualModels(ctx context.Context) ([]VirtualModel, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT body FROM neoroute_virtual_models ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]VirtualModel, 0)
	for rows.Next() {
		var raw []byte
		var v VirtualModel
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) VirtualModel(ctx context.Context, id string) (VirtualModel, error) {
	var raw []byte
	var v VirtualModel
	err := s.db.QueryRowContext(ctx, `SELECT body FROM neoroute_virtual_models WHERE id=? OR name=?`, id, id).Scan(&raw)
	if err != nil {
		return v, err
	}
	err = json.Unmarshal(raw, &v)
	return v, err
}

var virtualName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
var luaFunctionName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

func finiteFraction(n float64) bool { return !math.IsNaN(n) && !math.IsInf(n, 0) && n >= 0 && n <= 1 }

type validationInventory struct {
	providers map[string]bool
	models    map[string]*neopool.Model
	served    map[string]bool
	accounts  map[string]*neopool.Account
}

func indexValidationInventory(snap neopool.Snapshot) validationInventory {
	index := validationInventory{
		providers: make(map[string]bool, len(snap.Providers)),
		models:    make(map[string]*neopool.Model, len(snap.Models)),
		served:    make(map[string]bool, len(snap.Models)),
		accounts:  make(map[string]*neopool.Account, len(snap.Accounts)),
	}
	for _, provider := range snap.Providers {
		index.providers[provider.ID] = true
	}
	for i := range snap.Models {
		model := &snap.Models[i]
		index.models[model.ID] = model
		if !model.VirtualOnly {
			index.served[model.Served] = true
		}
	}
	for i := range snap.Accounts {
		account := &snap.Accounts[i]
		index.accounts[account.ID] = account
	}
	return index
}

// Validate checks configuration against the live configured inventory. Hidden or
// temporarily unhealthy accounts remain valid configuration but cannot execute.
func Validate(v VirtualModel, snap neopool.Snapshot, p neo.Principal) error {
	return validateIndexed(v, indexValidationInventory(snap), p)
}

func validateIndexed(v VirtualModel, inventory validationInventory, p neo.Principal) error {
	if !virtualName.MatchString(v.Name) {
		return fmt.Errorf("invalid virtual model name")
	}
	if inventory.providers[v.Name] {
		return fmt.Errorf("virtual name collides with provider")
	}
	models := inventory.models
	// A virtual-only selector names this stored route, not a direct provider model.
	if models[v.Name] != nil || inventory.served["neoproxy/"+v.Name] {
		return fmt.Errorf("virtual name collides with model")
	}
	if len(v.Stages) == 0 || len(v.Stages) > 128 {
		return fmt.Errorf("virtual model requires 1..128 stages")
	}
	if len(v.Description) > 4096 || len(v.Script) > 16384 {
		return fmt.Errorf("virtual model description or script too large")
	}
	seen := make(map[string]bool, len(v.Stages))
	validateModel := func(id string) (string, error) {
		m, ok := models[id]
		if !ok || m.ProviderID == "" || !strings.HasPrefix(id, m.ProviderID+"/") {
			return "", fmt.Errorf("unknown model %q", id)
		}
		return m.ProviderID, nil
	}
	for _, st := range v.Stages {
		if st.ID == "" || len(st.ID) > 128 || seen[st.ID] {
			return fmt.Errorf("stage IDs must be nonempty and unique")
		}
		seen[st.ID] = true
		provider, err := validateModel(st.Model)
		if err != nil {
			return err
		}
		if !finiteFraction(st.PerAccount) || !finiteFraction(st.Cap) {
			return fmt.Errorf("stage budgets must be finite fractions")
		}
		if st.Lua != nil {
			if strings.TrimSpace(v.Script) == "" {
				return fmt.Errorf("stage %q Lua functions require Lua source", st.ID)
			}
			for _, name := range []string{st.Lua.Eligible, st.Lua.PerAccount, st.Lua.Cap} {
				if name != "" && !luaFunctionName.MatchString(name) {
					return fmt.Errorf("stage %q Lua function names must be simple identifiers (at most 64 characters)", st.ID)
				}
			}
			if st.Lua.Eligible == "" && st.Lua.PerAccount == "" && st.Lua.Cap == "" {
				return fmt.Errorf("stage %q Lua policy requires at least one function", st.ID)
			}
		}
		if !slices.Contains([]string{"fill", "least-used", "alternate"}, st.Draw) {
			return fmt.Errorf("invalid draw policy")
		}
		if !slices.Contains([]string{"5h", "day", "week", "month"}, st.Window) {
			return fmt.Errorf("invalid window")
		}
		if st.Per != "user" && st.Per != "all" {
			return fmt.Errorf("invalid budget scope")
		}
		if st.Effort != nil && !slices.Contains([]string{"low", "medium", "high", "max"}, *st.Effort) {
			return fmt.Errorf("invalid effort")
		}
		ids := make(map[string]bool, len(st.Accounts.IDs))
		for _, id := range st.Accounts.IDs {
			if ids[id] {
				return fmt.Errorf("duplicate stage account")
			}
			ids[id] = true
			account := inventory.accounts[id]
			if account == nil || account.ProviderID != provider || !accountAllowed(p, *account) {
				return fmt.Errorf("stage account is outside provider or ownership scope")
			}
		}
	}
	if !slices.Contains([]string{"limit", "queue", "passthrough"}, v.Terminal.Kind) {
		return fmt.Errorf("invalid terminal")
	}
	if len(v.Terminal.Message) > 4096 {
		return fmt.Errorf("terminal message too large")
	}
	if v.Terminal.Kind == "passthrough" {
		if _, err := validateModel(v.Terminal.Model); err != nil {
			return err
		}
		if models[v.Terminal.Model].VirtualOnly {
			return fmt.Errorf("source-backed models require a metered stage, not an unbudgeted passthrough terminal")
		}
	}
	return nil
}

func (s *Store) SaveVirtualModel(ctx context.Context, v VirtualModel, create bool) (VirtualModel, error) {
	p, ok := neo.PrincipalFromContext(ctx)
	if !ok || p.Role != "admin" || !p.Session {
		return VirtualModel{}, fmt.Errorf("organization administrator session required")
	}
	snap, err := s.pools.Snapshot(ctx)
	if err != nil {
		return VirtualModel{}, err
	}
	if err = Validate(v, snap, p); err != nil {
		return VirtualModel{}, err
	}
	if v.Script != "" {
		h := s.scriptHook()
		if h == nil {
			return VirtualModel{}, fmt.Errorf("script evaluator unavailable")
		}
		if validator, ok := h.(interface{ Validate(string) error }); ok {
			if err = validator.Validate(v.Script); err != nil {
				return VirtualModel{}, fmt.Errorf("invalid script: %w", err)
			}
		}
	}
	if create {
		v.ID = "vm_" + uuid.NewString()
	} else {
		if _, err = s.VirtualModel(ctx, v.ID); err != nil {
			return VirtualModel{}, err
		}
	}
	v.Served = "neoproxy/" + v.Name
	v.UpdatedAt = s.now().UnixMilli()
	v.UpdatedBy = p.UserID
	if user, errUser := s.identity.User(ctx, p.UserID); errUser == nil {
		v.UpdatedBy = user.Handle
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return VirtualModel{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return VirtualModel{}, err
	}
	defer tx.Rollback()
	if create {
		_, err = tx.ExecContext(ctx, `INSERT INTO neoroute_virtual_models(id,name,body) VALUES(?,?,?)`, v.ID, v.Name, raw)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE neoroute_virtual_models SET name=?,body=? WHERE id=?`, v.Name, raw, v.ID)
	}
	if err != nil {
		return VirtualModel{}, fmt.Errorf("save virtual model (name must be unique): %w", err)
	}
	// Removed stages cannot leave stale draw cursors behind.
	rows, err := tx.QueryContext(ctx, `SELECT stage_id FROM neoroute_cursors WHERE vm_id=?`, v.ID)
	if err != nil {
		return VirtualModel{}, err
	}
	var obsolete []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return VirtualModel{}, err
		}
		if !slices.ContainsFunc(v.Stages, func(st Stage) bool { return st.ID == id }) {
			obsolete = append(obsolete, id)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return VirtualModel{}, err
	}
	for _, id := range obsolete {
		if _, err = tx.ExecContext(ctx, `DELETE FROM neoroute_cursors WHERE vm_id=? AND stage_id=?`, v.ID, id); err != nil {
			return VirtualModel{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return VirtualModel{}, err
	}
	s.notifyLocked()
	return v, nil
}
func (s *Store) DeleteVirtualModel(ctx context.Context, id string) error {
	p, ok := neo.PrincipalFromContext(ctx)
	if !ok || p.Role != "admin" || !p.Session {
		return fmt.Errorf("organization administrator session required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM neoroute_virtual_models WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM neo_grants WHERE virtual_model=?`, id); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	s.notifyLocked()
	return nil
}
func (s *Store) cursor(ctx context.Context, vm, stage string) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT cursor FROM neoroute_cursors WHERE vm_id=? AND stage_id=?`, vm, stage).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return n, err
}
