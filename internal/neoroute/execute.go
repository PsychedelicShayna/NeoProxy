package neoroute

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopool"
	"github.com/tidwall/gjson"
)

// RequestRecord is the persisted authorized explanation, not an upstream log.
type RequestRecord struct {
	ID        string   `json:"id"`
	At        int64    `json:"at"`
	UserID    string   `json:"userId"`
	KeyID     *string  `json:"keyId"`
	Client    string   `json:"client"`
	Asked     string   `json:"asked"`
	Decision  Decision `json:"decision"`
	Tokens    Tokens   `json:"tokens"`
	LatencyMs int64    `json:"latencyMs"`
	Outcome   string   `json:"outcome"`
	Stream    bool     `json:"stream"`
}

type runState struct {
	record   RequestRecord
	started  time.Time
	reserved int64
	charged  int64
	known    Tokens
	after    int
	failed   []TrailStep
	blocked  map[string]map[string]bool
	finishMu sync.Mutex
	finished bool
}

// containsMedia inspects structured media blocks rather than searching text or
// treating an empty gjson projection as a present non-text message.
func containsMedia(value gjson.Result) bool {
	if !value.IsObject() && !value.IsArray() {
		return false
	}
	if value.IsObject() {
		switch value.Get("type").String() {
		case "image", "image_url", "input_image", "image_file",
			"audio", "input_audio", "output_audio",
			"video", "input_video", "file", "input_file", "document":
			return true
		}
	}
	found := false
	value.ForEach(func(key, child gjson.Result) bool {
		switch key.String() {
		case "image_url", "input_audio", "audio", "video_url", "file_data":
			if child.Exists() && child.Type != gjson.Null {
				found = true
				return false
			}
		case "modalities":
			for _, modality := range child.Array() {
				if modality.String() != "text" {
					found = true
					return false
				}
			}
		}
		found = containsMedia(child)
		return !found
	})
	return found
}

// upperBound reserves a conservative token bound, never a request-count quota.
// Text bytes bound tokenizer input; media needs the catalogue context ceiling.
func upperBound(req Request, model neopool.Model) (int64, error) {
	var maximum int64
	for _, key := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens"} {
		v := gjson.GetBytes(req.Body, key)
		if v.Exists() {
			n := v.Int()
			if v.Type != gjson.Number || n <= 0 || v.Float() != float64(n) {
				return 0, fmt.Errorf("output token bound must be a positive integer")
			}
			if n > maximum {
				maximum = n
			}
		}
	}
	if maximum > 0 && !containsMedia(gjson.ParseBytes(req.Body)) {
		if maximum > math.MaxInt64-int64(len(req.Body)) {
			return 0, fmt.Errorf("token bound overflow")
		}
		return int64(len(req.Body)) + maximum, nil
	}
	if model.Context > 0 {
		return int64(model.Context), nil
	}
	return 0, fmt.Errorf("budgeted execution requires an output token bound or a known model context ceiling")
}
func (s *Store) admit(ctx context.Context, run *runState, p neo.Principal, tokens int64) error {
	now := s.now()
	day := now.UTC().Truncate(24 * time.Hour).UnixMilli()
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var rpm, total int64
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM neoroute_admissions WHERE user_id=? AND at>?`, p.UserID, now.Add(-time.Minute).UnixMilli()).Scan(&rpm); err != nil {
		return err
	}
	if err = tx.QueryRowContext(ctx, `SELECT coalesce(sum(tokens),0) FROM neoroute_admissions WHERE user_id=? AND day=?`, p.UserID, day).Scan(&total); err != nil {
		return err
	}
	if (p.Limits.RPM != nil && rpm >= *p.Limits.RPM) || (p.Limits.TokensPerDay != nil && (total >= *p.Limits.TokensPerDay || tokens > *p.Limits.TokensPerDay-total)) {
		return userLimitError()
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO neoroute_admissions(id,user_id,at,day,tokens) VALUES(?,?,?,?,?)`, run.record.ID, p.UserID, now.UnixMilli(), day, tokens)
	if err != nil {
		return err
	}
	run.reserved = tokens
	return tx.Commit()
}

func userLimitError() *Error {
	return &Error{Status: 429, Code: "neoproxy_user_limit", Decision: Decision{Outcome: "limited", Reason: "user-limit", Message: "User request or daily token ceiling reached", Trail: []TrailStep{}}}
}
func (s *Store) extendAdmission(ctx context.Context, run *runState, p neo.Principal, bound int64) error {
	if run.charged > math.MaxInt64-bound {
		return fmt.Errorf("request token bound overflow")
	}
	target := run.charged + bound
	day := s.now().UTC().Truncate(24 * time.Hour).UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var others int64
	if err = tx.QueryRowContext(ctx, `SELECT coalesce(sum(tokens),0) FROM neoroute_admissions WHERE user_id=? AND day=? AND id<>?`, p.UserID, day, run.record.ID).Scan(&others); err != nil {
		return err
	}
	if p.Limits.TokensPerDay != nil && target > *p.Limits.TokensPerDay-others {
		return userLimitError()
	}
	if _, err = tx.ExecContext(ctx, `UPDATE neoroute_admissions SET day=?,tokens=? WHERE id=?`, day, target, run.record.ID); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	run.reserved = target
	return nil
}
func (s *Store) userLimited(ctx context.Context, p neo.Principal) (bool, error) {
	var rpm, tokens int64
	now := s.now()
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM neoroute_admissions WHERE user_id=? AND at>?`, p.UserID, now.Add(-time.Minute).UnixMilli()).Scan(&rpm); err != nil {
		return false, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT coalesce(sum(tokens),0) FROM neoroute_admissions WHERE user_id=? AND day=?`, p.UserID, now.UTC().Truncate(24*time.Hour).UnixMilli()).Scan(&tokens); err != nil {
		return false, err
	}
	return (p.Limits.RPM != nil && rpm >= *p.Limits.RPM) || (p.Limits.TokensPerDay != nil && tokens >= *p.Limits.TokensPerDay), nil
}
func (s *Store) finish(run *runState, d Decision) error {
	run.finishMu.Lock()
	defer run.finishMu.Unlock()
	if run.finished {
		return nil
	}
	ctx := context.Background()
	run.record.Decision = d
	run.record.Outcome = d.Outcome
	run.record.LatencyMs = s.now().Sub(run.started).Milliseconds()
	run.record.Tokens = run.known
	raw, err := json.Marshal(run.record)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE neoroute_admissions SET tokens=? WHERE id=?`, run.charged, run.record.ID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO neoroute_requests(id,user_id,key_id,at,outcome,asked,body) VALUES(?,?,?,?,?,?,?)`, run.record.ID, run.record.UserID, stringValue(run.record.KeyID), run.record.At, d.Outcome, run.record.Asked, raw); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	run.finished = true
	return nil
}
func stringValue(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
func (s *Store) waitEligible(ctx context.Context, changed, poolChanged, identityChanged <-chan struct{}, reset int64) error {
	var timer *time.Timer
	var clock <-chan time.Time
	if reset > 0 {
		delay := time.UnixMilli(reset).Sub(s.now())
		if delay < 0 {
			delay = 0
		}
		timer = time.NewTimer(delay)
		clock = timer.C
		defer timer.Stop()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-changed:
		return nil
	case <-poolChanged:
		return nil
	case <-identityChanged:
		return nil
	case <-clock:
		return nil
	}
}
func (s *Store) reserve(ctx context.Context, req Request, run *runState, state routeState, d Decision, bound int64) (Selection, error) {
	a, ok := accountByID(state.snapshot, d.AccountID)
	if !ok {
		return Selection{}, fmt.Errorf("selected account disappeared")
	}
	id := "attempt_" + uuid.NewString()
	reservation := neopool.Reservation{ID: id, RequestID: run.record.ID, UserID: state.principal.UserID, VirtualModelID: d.VirtualModel, StageID: d.StageID, AccountID: d.AccountID, Tokens: bound, BudgetFree: d.StageID == ""}
	if state.principal.Role == "owner" {
		reservation.ScopeOwnerID = state.principal.UserID
	}
	if d.StageIndex != nil {
		st := state.vm.Stages[*d.StageIndex]
		allot, _ := grantFor(state.principal, state.vm.ID)
		reservation.Window = st.Window
		// Use the exact bounded Lua/static budgets that produced this decision.
		// The pool ledger atomically checks them against current in-flight usage.
		reservation.PerAccount = st.PerAccount * allot
		reservation.Cap = st.Cap * allot
		for _, step := range d.Trail {
			if step.Kind != "stage" || step.StageID != d.StageID {
				continue
			}
			reservation.Cap = min(reservation.Cap, step.Cap)
			for _, candidate := range step.Candidates {
				if candidate.AccountID == d.AccountID {
					reservation.PerAccount = min(reservation.PerAccount, candidate.Budget)
				}
			}
		}
		reservation.Shared = st.Per == "all"
		for _, account := range state.snapshot.Accounts {
			if account.ProviderID == a.ProviderID && accountAllowed(state.principal, account) && (st.Accounts.All || contains(st.Accounts.IDs, account.ID)) {
				reservation.AccountIDs = append(reservation.AccountIDs, account.ID)
			}
		}
	}
	var advance func(*sql.Tx) error
	if d.StageIndex != nil && state.vm.Stages[*d.StageIndex].Draw == "alternate" {
		expected := state.cursors[d.StageID]
		advance = func(tx *sql.Tx) error {
			result, err := tx.ExecContext(ctx, `INSERT INTO neoroute_cursors(vm_id,stage_id,cursor) VALUES(?,?,?) ON CONFLICT(vm_id,stage_id) DO UPDATE SET cursor=excluded.cursor WHERE neoroute_cursors.cursor=?`, state.vm.ID, d.StageID, (expected+1)%2147483647, expected)
			if err != nil {
				return err
			}
			n, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if n != 1 {
				return neopool.ErrConflict
			}
			return nil
		}
	}
	if err := s.pools.ReserveAndAdvance(ctx, reservation, advance); err != nil {
		return Selection{}, err
	}
	model, ok := exposedModel(state.snapshot, d.Model)
	if !ok {
		_ = s.pools.Release(context.Background(), id)
		return Selection{}, fmt.Errorf("model disappeared")
	}
	return Selection{Asked: req.Asked, Provider: model.ProviderID, Model: model.Name, AuthID: a.EngineAuthID, Effort: d.Effort, RequestID: run.record.ID, AttemptID: id, Decision: d}, nil
}
func contains(ids []string, id string) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}
func (s *Store) settle(run *runState, selection Selection, capture *neopool.AttemptUsage, bound int64) error {
	settlement, err := capture.Settlement(selection.Decision.AccountID)
	if err != nil {
		return err
	}
	if err = s.pools.Settle(context.Background(), settlement); err != nil {
		return err
	}
	if settlement.UsageKnown {
		run.charged += settlement.InputTokens + settlement.OutputTokens
		run.known.In += settlement.InputTokens
		run.known.Out += settlement.OutputTokens
		run.known.Cached += settlement.CachedTokens
	} else {
		run.charged += bound
	}
	return nil
}
func addRequestHeader(execution *Execution, id string) {
	if execution.Headers == nil {
		execution.Headers = make(http.Header)
	}
	execution.Headers.Set("X-NeoProxy-Request-ID", id)
}
func (s *Store) Run(ctx context.Context, req Request, execute ExecuteFunc) (execution *Execution, err error) {
	if execute == nil {
		return nil, fmt.Errorf("routing executor unavailable")
	}
	p, ok := neo.PrincipalFromContext(ctx)
	if !ok {
		return nil, &Error{Status: 401, Code: "neoproxy_unauthorized", Decision: Decision{Outcome: "error", Reason: "unauthorized"}}
	}
	run := &runState{record: RequestRecord{ID: "req_" + uuid.NewString(), At: s.now().UnixMilli(), UserID: p.UserID, Asked: req.Asked, Client: req.Client, Stream: req.Stream}, started: s.now(), after: -1, blocked: make(map[string]map[string]bool)}
	if p.KeyID != "" {
		key := p.KeyID
		run.record.KeyID = &key
	}
	defer func() {
		if err != nil {
			d := Decision{Outcome: "error", Reason: "routing-error", Trail: run.failed}
			var policy *Error
			if errors.As(err, &policy) {
				d = policy.Decision
			}
			if errFinish := s.finish(run, d); errFinish != nil {
				err = errors.Join(err, errFinish)
			}
		}
	}()
	return s.run(ctx, req, execute, run, false, false)
}
func (s *Store) run(ctx context.Context, req Request, execute ExecuteFunc, run *runState, admitted, queueInline bool) (*Execution, error) {
	for {
		if err := ctx.Err(); err != nil {
			d := Decision{Outcome: "error", Reason: "cancelled", Trail: run.failed}
			if errFinish := s.finish(run, d); errFinish != nil {
				return nil, errors.Join(err, errFinish)
			}
			return nil, err
		}
		changed, poolChanged, identityChanged := s.changes(), s.pools.Changes(), s.identity.Changes()
		state, err := s.state(ctx, req.Asked)
		if err != nil {
			return nil, err
		}
		state.blocked = run.blocked
		if !admitted {
			if err = s.admit(ctx, run, state.principal, 0); err != nil {
				return nil, err
			}
			admitted = true
		}
		d, err := s.decision(ctx, req, state, run.after)
		if err != nil {
			return nil, err
		}
		if d.Outcome == "queued" {
			if err = s.extendAdmission(ctx, run, state.principal, 0); err != nil {
				return nil, err
			}
			if req.Stream && !queueInline {
				return s.queueStream(ctx, req, execute, run), nil
			}
			if err = s.waitEligible(ctx, changed, poolChanged, identityChanged, d.ResetsAt); err != nil {
				d.Outcome = "error"
				d.Reason = "cancelled"
				_ = s.finish(run, mergeFailures(d, run.failed))
				return nil, err
			}
			run.after = -1
			run.blocked = make(map[string]map[string]bool)
			continue
		}
		if d.Outcome != "ok" {
			d = mergeFailures(d, run.failed)
			if err = s.finish(run, d); err != nil {
				return nil, err
			}
			return nil, decisionError(d)
		}
		model, _ := exposedModel(state.snapshot, d.Model)
		bound, err := upperBound(req, model)
		if err != nil {
			if d.StageID != "" || state.principal.Limits.TokensPerDay != nil {
				return nil, err
			}
			bound = 0
		}
		// C serializes cursor selection/reservation. The shared ledger independently
		// enforces budgets transactionally across every router and in-flight attempt.
		s.selectionMu.Lock()
		fresh, errFresh := s.state(ctx, req.Asked)
		fresh.blocked = run.blocked
		if errFresh == nil {
			d, fresh, errFresh = s.choose(ctx, req, fresh, run.after)
		}
		var selection Selection
		if errFresh == nil && d.Outcome == "ok" {
			model, _ = exposedModel(fresh.snapshot, d.Model)
			bound, errFresh = upperBound(req, model)
			if errFresh != nil && d.StageID == "" && fresh.principal.Limits.TokensPerDay == nil {
				bound = 0
				errFresh = nil
			}
			if errFresh == nil {
				errFresh = s.extendAdmission(ctx, run, fresh.principal, bound)
			}
			if errFresh == nil {
				selection, errFresh = s.reserve(ctx, req, run, fresh, d, bound)
			}
		}
		s.selectionMu.Unlock()
		if errFresh != nil {
			if errors.Is(errFresh, neopool.ErrConflict) {
				continue
			}
			if d.StageIndex != nil && (errors.Is(errFresh, neopool.ErrBudget) || errors.Is(errFresh, neopool.ErrUnavailable)) {
				if run.blocked[d.StageID] == nil {
					run.blocked[d.StageID] = make(map[string]bool)
				}
				run.blocked[d.StageID][d.AccountID] = true
				continue
			}
			d.Outcome = "error"
			d.Reason = "reservation-error"
			var policy *Error
			if errors.As(errFresh, &policy) {
				d = policy.Decision
			}
			_ = s.finish(run, d)
			return nil, errFresh
		}
		if d.Outcome != "ok" {
			continue
		}
		attemptCtx, capture := neopool.WithAttempt(ctx, selection.AttemptID)
		attemptCtx, cancelAttempt := context.WithCancel(attemptCtx)
		execution, errExecute := execute(attemptCtx, selection)
		if errExecute == nil && execution == nil {
			errExecute = fmt.Errorf("engine returned nil execution")
		}
		var first Chunk
		if errExecute == nil && req.Stream {
			if execution.Chunks == nil {
				errExecute = fmt.Errorf("engine returned nil stream")
			} else {
				for errExecute == nil && len(first.Data) == 0 {
					select {
					case <-ctx.Done():
						errExecute = ctx.Err()
					case chunk, open := <-execution.Chunks:
						if !open {
							errExecute = fmt.Errorf("upstream stream ended before output")
						} else {
							first = chunk
							errExecute = chunk.Err
						}
					}
				}
			}
		}
		if errExecute != nil {
			cancelAttempt()
			errSettle := s.settle(run, selection, capture, bound)
			if errSettle != nil {
				return nil, errors.Join(errExecute, errSettle)
			}
			if ctx.Err() != nil {
				d.Outcome = "error"
				d.Reason = "cancelled"
				_ = s.finish(run, d)
				return nil, ctx.Err()
			}
			if d.StageIndex == nil {
				d.Outcome = "error"
				d.Reason = "upstream-error"
				if err = s.finish(run, d); err != nil {
					return nil, err
				}
				return nil, errExecute
			}
			status := 502
			var upstreamStatus interface{ StatusCode() int }
			if errors.As(errExecute, &upstreamStatus) {
				status = upstreamStatus.StatusCode()
			}
			run.after = *d.StageIndex
			run.failed = append(run.failed, TrailStep{Kind: "stage", Index: *d.StageIndex, StageID: d.StageID, Model: d.Model, Chosen: d.AccountID, Skipped: "upstream-error", FailureStatus: status})
			continue
		}
		d = mergeFailures(d, run.failed)
		addRequestHeader(execution, run.record.ID)
		if !req.Stream {
			cancelAttempt()
			if err = s.settle(run, selection, capture, bound); err != nil {
				return nil, err
			}
			if err = s.finish(run, d); err != nil {
				return nil, err
			}
			return execution, nil
		}
		out := make(chan Chunk)
		go func() {
			defer close(out)
			defer cancelAttempt()
			outcome := d
			defer func() {
				if errSettle := s.settle(run, selection, capture, bound); errSettle != nil {
					outcome.Outcome = "error"
					outcome.Reason = "settlement-error"
					select {
					case out <- Chunk{Err: errSettle}:
					case <-ctx.Done():
					}
				}
				if errFinish := s.finish(run, outcome); errFinish != nil {
					select {
					case out <- Chunk{Err: errFinish}:
					case <-ctx.Done():
					}
				}
			}()
			select {
			case out <- first:
			case <-ctx.Done():
				outcome.Outcome = "error"
				outcome.Reason = "cancelled"
				return
			}
			for {
				select {
				case <-ctx.Done():
					outcome.Outcome = "error"
					outcome.Reason = "cancelled"
					return
				case chunk, open := <-execution.Chunks:
					if !open {
						return
					}
					if chunk.Err != nil {
						outcome.Outcome = "error"
						outcome.Reason = "upstream-error"
					}
					select {
					case out <- chunk:
					case <-ctx.Done():
						outcome.Outcome = "error"
						outcome.Reason = "cancelled"
						return
					}
					if chunk.Err != nil {
						return
					}
				}
			}
		}()
		return &Execution{Headers: execution.Headers, Chunks: out}, nil
	}
}
func mergeFailures(d Decision, failed []TrailStep) Decision {
	var prior []TrailStep
	for _, f := range failed {
		replaced := false
		for i := range d.Trail {
			if d.Trail[i].Kind == "stage" && d.Trail[i].StageID == f.StageID && d.Trail[i].Skipped == "upstream-error" {
				d.Trail[i] = f
				replaced = true
				break
			}
		}
		if !replaced {
			prior = append(prior, f)
		}
	}
	if len(prior) > 0 {
		d.Trail = append(prior, d.Trail...)
	}
	return d
}
func (s *Store) queueStream(ctx context.Context, req Request, execute ExecuteFunc, run *runState) *Execution {
	out := make(chan Chunk)
	headers := make(http.Header)
	headers.Set("X-NeoProxy-Request-ID", run.record.ID)
	type result struct {
		execution *Execution
		err       error
	}
	ready := make(chan result, 1)
	go func() {
		execution, err := s.run(ctx, req, execute, run, true, true)
		if err != nil {
			d := Decision{Outcome: "error", Reason: "routing-error", Trail: run.failed}
			var policy *Error
			if errors.As(err, &policy) {
				d = policy.Decision
			}
			if errFinish := s.finish(run, d); errFinish != nil {
				err = errors.Join(err, errFinish)
			}
		}
		ready <- result{execution, err}
	}()
	go func() {
		defer close(out)
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				select {
				case out <- Chunk{Data: []byte(": neoproxy queued\n\n")}:
				case <-ctx.Done():
					return
				}
			case result := <-ready:
				if result.err != nil {
					select {
					case out <- Chunk{Err: result.err}:
					case <-ctx.Done():
					}
					return
				}
				for {
					select {
					case <-ctx.Done():
						return
					case chunk, open := <-result.execution.Chunks:
						if !open {
							return
						}
						select {
						case out <- chunk:
						case <-ctx.Done():
							return
						}
					}
				}
			}
		}
	}()
	return &Execution{Headers: headers, Chunks: out}
}
