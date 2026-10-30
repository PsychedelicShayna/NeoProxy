package neopool

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type Store struct {
	db                *sql.DB
	manager           *coreauth.Manager
	mu                sync.Mutex
	changed           chan struct{}
	now               func() time.Time
	sourceMu          sync.Mutex
	sourceLifecycleMu sync.Mutex
	sourceFactory     func(string) coreauth.ProviderExecutor
	sourcePublish     func(context.Context, Source) error
	sourceCancel      context.CancelFunc
	sourceDone        chan struct{}
}

func New(db *sql.DB, manager *coreauth.Manager) (*Store, error) {
	if db == nil {
		return nil, fmt.Errorf("pool database is required")
	}
	s := &Store{db: db, manager: manager, changed: make(chan struct{}), now: time.Now}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`
 CREATE TABLE IF NOT EXISTS neo_pool_schema (version INTEGER NOT NULL);
 INSERT INTO neo_pool_schema(version) SELECT 1 WHERE NOT EXISTS (SELECT 1 FROM neo_pool_schema);
 CREATE TABLE IF NOT EXISTS neo_accounts (id TEXT PRIMARY KEY, engine_auth_id TEXT NOT NULL UNIQUE, provider_id TEXT NOT NULL, owner_id TEXT REFERENCES neo_users(id));
 CREATE TABLE IF NOT EXISTS neo_provider_exposure (id TEXT PRIMARY KEY, exposed INTEGER NOT NULL CHECK(exposed IN (0,1)));
 CREATE TABLE IF NOT EXISTS neo_model_exposure (id TEXT PRIMARY KEY, exposed INTEGER NOT NULL CHECK(exposed IN (0,1)));
 CREATE TABLE IF NOT EXISTS neo_account_windows (account_id TEXT NOT NULL REFERENCES neo_accounts(id), kind TEXT NOT NULL CHECK(kind IN ('5h','day','week','month')), capacity INTEGER NOT NULL CHECK(capacity>0), anchor INTEGER NOT NULL, PRIMARY KEY(account_id,kind));
 CREATE TABLE IF NOT EXISTS neo_pool_attempts (
 id TEXT PRIMARY KEY, request_spec TEXT NOT NULL, settlement_spec TEXT, request_id TEXT NOT NULL, user_id TEXT NOT NULL REFERENCES neo_users(id), virtual_model_id TEXT NOT NULL, stage_id TEXT NOT NULL,
 account_id TEXT NOT NULL REFERENCES neo_accounts(id), owner_id TEXT NOT NULL REFERENCES neo_users(id), window_kind TEXT NOT NULL, starts_at INTEGER NOT NULL, resets_at INTEGER NOT NULL, capacity INTEGER NOT NULL,
 reserved INTEGER NOT NULL, tokens INTEGER NOT NULL DEFAULT 0, input_tokens INTEGER NOT NULL DEFAULT 0, output_tokens INTEGER NOT NULL DEFAULT 0, cached_tokens INTEGER NOT NULL DEFAULT 0,
 estimated INTEGER NOT NULL DEFAULT 0, state TEXT NOT NULL CHECK(state IN ('reserved','settled','released')), per_account REAL NOT NULL, cap REAL NOT NULL, shared INTEGER NOT NULL, created_at INTEGER NOT NULL);
 CREATE INDEX IF NOT EXISTS neo_pool_usage ON neo_pool_attempts(virtual_model_id,stage_id,window_kind,account_id,starts_at,user_id);
 CREATE TABLE IF NOT EXISTS neo_pool_logins (state TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES neo_users(id), provider_id TEXT NOT NULL, created_at INTEGER NOT NULL);
 CREATE TABLE IF NOT EXISTS neo_pool_sources (
 id TEXT PRIMARY KEY, label TEXT NOT NULL, url TEXT NOT NULL UNIQUE, bearer TEXT NOT NULL,
 owner_id TEXT NOT NULL REFERENCES neo_users(id), admin_id TEXT NOT NULL REFERENCES neo_users(id), enabled INTEGER NOT NULL DEFAULT 1,
 status TEXT NOT NULL DEFAULT 'pending', checked_at INTEGER NOT NULL DEFAULT 0,
 account_count INTEGER NOT NULL DEFAULT 0, model_count INTEGER NOT NULL DEFAULT 0, revision INTEGER NOT NULL DEFAULT 0);
 `)
	if err != nil {
		return nil, fmt.Errorf("migrate account pools: %w", err)
	}
	var version int
	if err = tx.QueryRow(`SELECT version FROM neo_pool_schema`).Scan(&version); err != nil {
		return nil, err
	}
	if version != 1 {
		return nil, fmt.Errorf("unsupported pool schema version %d", version)
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return s, nil
}

// Changes broadcasts committed ledger/inventory mutations; consumers also wake
// at the snapshot's next reset boundary. A channel is never consumed by one waiter.
func (s *Store) Changes() <-chan struct{} { s.mu.Lock(); defer s.mu.Unlock(); return s.changed }
func (s *Store) notifyLocked()            { close(s.changed); s.changed = make(chan struct{}) }

// ScopeOwned validates the entire key scope in SQL, and is also invoked during
// key authentication so ownership changes revoke access immediately.
func (s *Store) ScopeOwned(ctx context.Context, userID string, ids []string) error {
	if len(ids) == 0 {
		return ErrForbidden
	}
	for _, id := range ids {
		var n int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM neo_accounts WHERE id=? AND owner_id=?`, id, userID).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			return ErrForbidden
		}
	}
	return nil
}

// AssignOwner is explicitly administrative. No owner-facing claim-by-ID exists.
func (s *Store) AssignOwner(ctx context.Context, id, ownerID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.db.ExecContext(ctx, `UPDATE neo_accounts SET owner_id=? WHERE id=? AND EXISTS (SELECT 1 FROM neo_users WHERE id=?)`, ownerID, id, ownerID)
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
	s.notifyLocked()
	return nil
}

// BindLogin is only for a completed, authenticated engine login, never an HTTP
// account-ID claim. Existing ownership cannot be replaced by another owner.
func (s *Store) BindLogin(ctx context.Context, userID string, a *coreauth.Auth) error {
	if a == nil || a.ID == "" || userID == "" {
		return ErrForbidden
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.db.ExecContext(ctx, `INSERT INTO neo_accounts(id,engine_auth_id,provider_id,owner_id) SELECT ?,?,?,? WHERE EXISTS (SELECT 1 FROM neo_users WHERE id=? AND state='active' AND role IN ('admin','owner')) ON CONFLICT(id) DO UPDATE SET owner_id=excluded.owner_id WHERE neo_accounts.owner_id IS NULL OR neo_accounts.owner_id=excluded.owner_id`, AccountID(a.ID), a.ID, a.Provider, userID, userID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrForbidden
	}
	s.notifyLocked()
	return nil
}

func windowBounds(kind string, anchor, now int64) (int64, int64, error) {
	start := time.UnixMilli(anchor).UTC()
	at := time.UnixMilli(now).UTC()
	if now < anchor {
		return 0, 0, ErrUnavailable
	}
	var duration time.Duration
	switch kind {
	case "5h":
		duration = 5 * time.Hour
	case "day":
		duration = 24 * time.Hour
	case "week":
		duration = 7 * 24 * time.Hour
	case "month":
		// Calendar-month periods use the original anchor, avoiding drift at month end.
		n := (at.Year()-start.Year())*12 + int(at.Month()-start.Month())
		candidate := meterMonth(start, n)
		for candidate.After(at) {
			n--
			candidate = meterMonth(start, n)
		}
		next := meterMonth(start, n+1)
		return candidate.UnixMilli(), next.UnixMilli(), nil
	default:
		return 0, 0, fmt.Errorf("invalid window kind")
	}
	span := duration.Milliseconds()
	epoch := anchor + (now-anchor)/span*span
	return epoch, epoch + span, nil
}

func meterMonth(anchor time.Time, offset int) time.Time {
	month := time.Date(anchor.Year(), anchor.Month()+time.Month(offset), 1, anchor.Hour(), anchor.Minute(), anchor.Second(), anchor.Nanosecond(), time.UTC)
	last := time.Date(month.Year(), month.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
	return time.Date(month.Year(), month.Month(), min(anchor.Day(), last), anchor.Hour(), anchor.Minute(), anchor.Second(), anchor.Nanosecond(), time.UTC)
}

func (s *Store) ConfigureWindow(ctx context.Context, id, ownerID, kind string, capacity, anchor int64, admin bool) error {
	if capacity <= 0 || capacity > 1<<53 || anchor < 0 {
		return fmt.Errorf("capacity must be positive exact tokens and startsAt nonnegative")
	}
	if _, _, err := windowBounds(kind, anchor, anchor); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current sql.NullString
	if err = tx.QueryRowContext(ctx, `SELECT owner_id FROM neo_accounts WHERE id=?`, id).Scan(&current); err != nil {
		return ErrUnavailable
	}
	if !admin && (!current.Valid || current.String != ownerID) {
		return ErrForbidden
	}
	// A new meter must not retroactively change accounting for an active period.
	var n int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM neo_pool_attempts WHERE account_id=? AND window_kind=? AND resets_at>?`, id, kind, s.now().UnixMilli()).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrConflict
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO neo_account_windows(account_id,kind,capacity,anchor) VALUES(?,?,?,?) ON CONFLICT(account_id,kind) DO UPDATE SET capacity=excluded.capacity,anchor=excluded.anchor`, id, kind, capacity, anchor)
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	s.notifyLocked()
	return nil
}
