package neoroute

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopool"
)

func accountAllowed(p neo.Principal, a neopool.Account) bool {
	if a.OwnerID == "" {
		return false
	}
	if p.Role == "owner" && a.OwnerID != p.UserID {
		return false
	}
	return p.AllowsAccount(a.ID)
}
func accountSupports(a neopool.Account, model string) bool { return slices.Contains(a.ModelIDs, model) }
func providerVisible(snap neopool.Snapshot, id string) bool {
	for _, p := range snap.Providers {
		if p.ID == id {
			return p.Exposed
		}
	}
	return false
}
func exposedModel(snap neopool.Snapshot, id string) (neopool.Model, bool) {
	for _, m := range snap.Models {
		if m.ID == id {
			return m, m.Exposed && providerVisible(snap, m.ProviderID)
		}
	}
	return neopool.Model{}, false
}
func windowFor(a neopool.Account, kind string, now int64) (neopool.Window, bool) {
	for _, w := range a.Windows {
		if w.Kind == kind && w.Known && w.Capacity > 0 && w.StartsAt <= now && w.ResetsAt > now {
			return w, true
		}
	}
	return neopool.Window{}, false
}
func accountByID(snap neopool.Snapshot, id string) (neopool.Account, bool) {
	for _, a := range snap.Accounts {
		if a.ID == id {
			return a, true
		}
	}
	return neopool.Account{}, false
}
func grantFor(p neo.Principal, vm string) (float64, bool) {
	for _, g := range p.Grants {
		if g.VirtualModel == vm && finiteFraction(g.Allotment) {
			return g.Allotment, true
		}
	}
	return 0, false
}

func (s *Store) refresh(ctx context.Context, p neo.Principal) (neo.Principal, error) {
	if p.KeyID != "" {
		next, err := s.identity.RefreshPrincipal(ctx, p)
		return next, authorityError(err)
	}
	next, err := s.identity.PrincipalForUser(ctx, p.UserID)
	if err != nil {
		return neo.Principal{}, authorityError(err)
	}
	next.Session = p.Session
	next.AccountIDs = p.AccountIDs
	return next, nil
}

func authorityError(err error) error {
	if err == nil {
		return nil
	}
	status, code, message := 0, "", ""
	switch {
	case errors.Is(err, neo.ErrSuspended):
		status, code, message = 403, "neoproxy_suspended", "User is suspended"
	case errors.Is(err, neo.ErrUnauthenticated), errors.Is(err, neo.ErrNotFound):
		status, code, message = 401, "neoproxy_unauthorized", "Inference key is no longer authorized"
	case errors.Is(err, neo.ErrForbidden), errors.Is(err, neopool.ErrForbidden):
		status, code, message = 403, "neoproxy_forbidden", "Account scope is no longer authorized"
	default:
		return err
	}
	return &Error{Status: status, Code: code, Decision: Decision{Outcome: "error", Reason: code, Message: message, Trail: []TrailStep{}}}
}

type routeState struct {
	principal neo.Principal
	snapshot  neopool.Snapshot
	usage     []neopool.Usage
	vm        *VirtualModel
	cursors   map[string]int64
	blocked   map[string]map[string]bool
	at        int64
}

func (s *Store) state(ctx context.Context, asked string) (routeState, error) {
	p, ok := neo.PrincipalFromContext(ctx)
	if !ok {
		return routeState{}, &Error{Status: 401, Code: "neoproxy_unauthorized", Decision: Decision{Outcome: "error", Reason: "unauthorized", Trail: []TrailStep{}}}
	}
	refreshed, err := s.refresh(ctx, p)
	if err != nil {
		return routeState{}, err
	}
	p = refreshed
	snap, err := s.pools.Snapshot(ctx)
	if err != nil {
		return routeState{}, err
	}
	state := routeState{principal: p, snapshot: snap, at: s.now().UnixMilli(), cursors: make(map[string]int64)}
	selector := strings.TrimPrefix(asked, "neoproxy/")
	if strings.HasPrefix(asked, "neoproxy/") && !strings.Contains(selector, "/") {
		vm, errVM := s.VirtualModel(ctx, selector)
		if errVM != nil && !errors.Is(errVM, sql.ErrNoRows) {
			return state, errVM
		}
		if errVM == nil {
			state.vm = &vm
			for _, st := range vm.Stages {
				n, errCursor := s.cursor(ctx, vm.ID, st.ID)
				if errCursor != nil {
					return state, errCursor
				}
				state.cursors[st.ID] = n
			}
		}
	}
	if state.vm != nil {
		state.usage, err = s.pools.Usage(ctx, "")
		if err != nil {
			return state, err
		}
	}
	return state, nil
}

func directDecision(asked, model string, state routeState, vm string, trail []TrailStep, checkAccess bool) Decision {
	d := Decision{Outcome: "error", VirtualModel: vm, Trail: trail}
	m, ok := exposedModel(state.snapshot, model)
	if !ok {
		d.Reason = "unknown-model"
		return d
	}
	if m.VirtualOnly {
		d.Reason = "no-provider-access"
		d.Message = "This model is available only through a metered virtual-model stage"
		return d
	}
	if checkAccess && !slices.Contains(state.principal.ProviderAccess, m.ProviderID) {
		d.Reason = "no-provider-access"
		d.Trail = append(d.Trail, TrailStep{Kind: "denied", ProviderID: m.ProviderID})
		return d
	}
	for _, a := range state.snapshot.Accounts {
		if a.ProviderID == m.ProviderID && a.State == "healthy" && accountAllowed(state.principal, a) && accountSupports(a, model) {
			d.Outcome = "ok"
			d.Model = model
			d.AccountID = a.ID
			d.Direct = checkAccess
			if checkAccess {
				d.Trail = append(d.Trail, TrailStep{Kind: "direct", Model: model, AccountID: a.ID})
			}
			return d
		}
	}
	d.Reason = "no-healthy-account"
	return d
}
func stageUsage(state routeState, vm string, st Stage, a neopool.Account, w neopool.Window) float64 {
	total := float64(0)
	for _, u := range state.usage {
		if u.VirtualModelID == vm && u.StageID == st.ID && u.AccountID == a.ID && u.Window == st.Window && u.StartsAt == w.StartsAt && u.ResetsAt == w.ResetsAt && (st.Per == "all" || u.UserID == state.principal.UserID) {
			total += float64(u.Tokens+u.Reserved) / float64(w.Capacity)
		}
	}
	return total
}
func evaluateStage(state routeState, vm VirtualModel, st Stage, index int, allot float64) (TrailStep, []neopool.Account, int64) {
	trail := TrailStep{Kind: "stage", Index: index, StageID: st.ID, Model: st.Model, Cap: st.Cap * allot, Candidates: []Candidate{}}
	m, visible := exposedModel(state.snapshot, st.Model)
	var candidates []neopool.Account
	unhealthy := !visible
	var reset int64
	for _, a := range state.snapshot.Accounts {
		if a.ProviderID != m.ProviderID || !accountAllowed(state.principal, a) || (!st.Accounts.All && !slices.Contains(st.Accounts.IDs, a.ID)) {
			continue
		}
		w, known := windowFor(a, st.Window, state.at)
		if a.CooldownUntil > state.at && (reset == 0 || a.CooldownUntil < reset) {
			reset = a.CooldownUntil
		}
		used := float64(0)
		if known {
			used = stageUsage(state, vm.ID, st, a, w)
			trail.Total += used
			if reset == 0 || w.ResetsAt < reset {
				reset = w.ResetsAt
			}
		}
		if a.State != "healthy" || !visible || !known || !accountSupports(a, st.Model) || w.UsedTokens >= w.Capacity {
			unhealthy = true
			continue
		}
		if used < st.PerAccount*allot && !state.blocked[st.ID][a.ID] {
			candidates = append(candidates, a)
			trail.Candidates = append(trail.Candidates, Candidate{AccountID: a.ID, Used: used, Budget: st.PerAccount * allot})
		}
	}
	if trail.Total >= trail.Cap {
		trail.Skipped = "cap"
		trail.Candidates = []Candidate{}
		return trail, nil, reset
	}
	if len(candidates) == 0 {
		trail.Skipped = "per-account"
		if unhealthy {
			trail.Skipped = "unhealthy"
		}
		return trail, nil, reset
	}
	return trail, candidates, reset
}

// decide is the canonical pure policy shared by simulation and execution. The
// excluded prefix represents real failed attempts, not fabricated health changes.
func decide(req Request, state routeState, after int, proposal *ScriptProposal) Decision {
	trail := make([]TrailStep, 0)
	if !strings.HasPrefix(req.Asked, "neoproxy/") {
		return Decision{Outcome: "error", Reason: "unknown-model", Trail: []TrailStep{{Kind: "no-model", Asked: req.Asked}}}
	}
	selector := strings.TrimPrefix(req.Asked, "neoproxy/")
	if strings.Contains(selector, "/") {
		return directDecision(req.Asked, selector, state, "", trail, true)
	}
	if state.vm == nil {
		return Decision{Outcome: "error", Reason: "unknown-model", Trail: []TrailStep{{Kind: "no-model", Asked: req.Asked}}}
	}
	vm := *state.vm
	allot, granted := grantFor(state.principal, vm.ID)
	if !granted {
		return Decision{Outcome: "error", VirtualModel: vm.ID, Reason: "not-granted", Trail: []TrailStep{{Kind: "denied", VirtualModel: vm.ID}}}
	}
	var resetsAt int64
	for index, st := range vm.Stages {
		tr, candidates, reset := evaluateStage(state, vm, st, index, allot)
		tr, candidates = applyPolicy(tr, candidates, st, allot, proposal)
		if reset > 0 && (resetsAt == 0 || reset < resetsAt) {
			resetsAt = reset
		}
		if index <= after {
			tr.Skipped = "upstream-error"
			tr.Candidates = nil
			trail = append(trail, tr)
			continue
		}
		if tr.Skipped != "" {
			trail = append(trail, tr)
			continue
		}
		chosen := 0
		switch st.Draw {
		case "least-used":
			for i := range candidates {
				if tr.Candidates[i].Used < tr.Candidates[chosen].Used {
					chosen = i
				}
			}
		case "alternate":
			chosen = int(state.cursors[st.ID] % int64(len(candidates)))
		}
		if proposal != nil && proposal.StageID != "" {
			if proposal.StageID != st.ID {
				tr.Skipped = "script"
				trail = append(trail, tr)
				continue
			}
			chosen = -1
			for i, a := range candidates {
				if a.ID == proposal.AccountID {
					chosen = i
					break
				}
			}
			if chosen < 0 {
				return Decision{Outcome: "error", VirtualModel: vm.ID, Reason: "invalid-script-proposal", Trail: trail}
			}
		}
		tr.Chosen = candidates[chosen].ID
		trail = append(trail, tr)
		return Decision{Outcome: "ok", VirtualModel: vm.ID, StageIndex: &index, StageID: st.ID, Model: st.Model, AccountID: candidates[chosen].ID, Effort: st.Effort, Trail: trail}
	}
	if proposal != nil && proposal.StageID != "" {
		return Decision{Outcome: "error", VirtualModel: vm.ID, Reason: "invalid-script-proposal", Trail: trail}
	}
	trail = append(trail, TrailStep{Kind: "terminal", Terminal: &vm.Terminal})
	switch vm.Terminal.Kind {
	case "passthrough":
		return directDecision(req.Asked, vm.Terminal.Model, state, vm.ID, trail, false)
	case "queue":
		return Decision{Outcome: "queued", VirtualModel: vm.ID, Reason: "queue", Message: vm.Terminal.Message, ResetsAt: resetsAt, Trail: trail}
	default:
		return Decision{Outcome: "limited", VirtualModel: vm.ID, Reason: "limit", Message: vm.Terminal.Message, ResetsAt: resetsAt, Trail: trail}
	}
}
func (s *Store) choose(ctx context.Context, req Request, state routeState, after int) (Decision, routeState, error) {
	var proposal *ScriptProposal
	if state.vm != nil && state.vm.Script != "" {
		if _, ok := grantFor(state.principal, state.vm.ID); !ok {
			return decide(req, state, after, nil), state, nil
		}
		hook := s.scriptHook()
		if hook == nil {
			return Decision{}, state, scriptError(state.vm.ID, "script evaluator unavailable")
		}
		if len(req.Body) > ScriptMaxInput {
			return Decision{}, state, scriptError(state.vm.ID, "routing script input exceeds 256 KiB")
		}
		allot, _ := grantFor(state.principal, state.vm.ID)
		input := ScriptInput{Source: state.vm.Script, Asked: req.Asked, Protocol: req.Protocol, UserID: state.principal.UserID, VirtualModel: *state.vm, Stages: make([]ScriptStage, 0, len(state.vm.Stages))}
		input.Request = append(json.RawMessage(nil), req.Body...)
		for i, st := range state.vm.Stages {
			tr, _, _ := evaluateStage(state, *state.vm, st, i, allot)
			if i <= after {
				tr.Skipped = "upstream-error"
				tr.Candidates = nil
			}
			input.Stages = append(input.Stages, ScriptStage{StageID: st.ID, Model: st.Model, Candidates: tr.Candidates, Total: tr.Total, Cap: tr.Cap, Skipped: tr.Skipped, Lua: st.Lua, Allotment: allot, Window: st.Window})
		}
		for _, a := range state.snapshot.Accounts {
			if !accountAllowed(state.principal, a) {
				continue
			}
			account := ScriptAccount{AccountID: a.ID, ProviderID: a.ProviderID, State: a.State, SourceID: a.SourceID, Windows: make([]ScriptWindow, 0, len(a.Windows)), ProviderWindows: make([]ScriptWindow, 0, len(a.ProviderWindows))}
			for _, w := range a.Windows {
				account.Windows = append(account.Windows, scriptWindow(w, state.at))
			}
			for _, w := range a.ProviderWindows {
				account.ProviderWindows = append(account.ProviderWindows, scriptWindow(w, state.at))
			}
			input.Accounts = append(input.Accounts, account)
		}
		eligible := false
		for _, stage := range input.Stages {
			if stage.Skipped == "" && len(stage.Candidates) > 0 {
				eligible = true
				break
			}
		}
		if !eligible {
			return decide(req, state, after, nil), state, nil
		}
		p, err := hook.Evaluate(ctx, input)
		if err != nil {
			return Decision{}, state, scriptError(state.vm.ID, err.Error())
		}
		if err = validatePolicies(p, input); err != nil {
			return Decision{}, state, scriptError(state.vm.ID, err.Error())
		}
		proposal = &p
		// Evaluation is outside every database transaction. Reload authority and
		// inventory after it: a script cannot freeze a grant or account's health.
		fresh, err := s.state(ctx, req.Asked)
		if err != nil {
			return Decision{}, state, err
		}
		if fresh.vm == nil || !reflect.DeepEqual(fresh.vm, state.vm) {
			return Decision{}, fresh, scriptError(state.vm.ID, "virtual model changed during script evaluation")
		}
		fresh.blocked = state.blocked
		state = fresh
	}
	return decide(req, state, after, proposal), state, nil
}
func (s *Store) decision(ctx context.Context, req Request, state routeState, after int) (Decision, error) {
	d, _, err := s.choose(ctx, req, state, after)
	return d, err
}
func scriptError(vm, message string) *Error {
	if len(message) > 1024 {
		message = message[:1024]
	}
	return &Error{Status: 422, Code: "neoproxy_script_error", Decision: Decision{Outcome: "error", VirtualModel: vm, Reason: "script-error", Message: strings.ToValidUTF8(message, ""), Trail: []TrailStep{}}}
}
func decisionError(d Decision) error {
	switch d.Outcome {
	case "ok":
		return nil
	case "limited":
		return &Error{Status: 429, Code: "neoproxy_limit", Decision: d}
	case "queued":
		return &Error{Status: 429, Code: "neoproxy_limit", Decision: d}
	}
	status := 403
	if d.Reason == "unknown-model" {
		status = 404
	}
	if d.Reason == "no-healthy-account" {
		status = 503
	}
	if d.Reason == "invalid-script-proposal" {
		status = 422
	}
	return &Error{Status: status, Code: "neoproxy_" + d.Reason, Decision: d}
}
func (s *Store) Simulate(ctx context.Context, req Request) (Decision, error) {
	state, err := s.state(ctx, req.Asked)
	if err != nil {
		return Decision{}, err
	}
	limited, err := s.userLimited(ctx, state.principal)
	if err != nil {
		return Decision{}, err
	}
	if limited {
		return userLimitError().Decision, nil
	}
	return s.decision(ctx, req, state, -1)
}
func (s *Store) Models(ctx context.Context) ([]string, error) {
	state, err := s.state(ctx, "")
	if err != nil {
		return nil, err
	}
	vms, err := s.VirtualModels(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0)
	for _, vm := range vms {
		if _, ok := grantFor(state.principal, vm.ID); ok {
			out = append(out, vm.Served)
		}
	}
	for _, m := range state.snapshot.Models {
		if !m.VirtualOnly && m.Exposed && providerVisible(state.snapshot, m.ProviderID) && slices.Contains(state.principal.ProviderAccess, m.ProviderID) {
			for _, a := range state.snapshot.Accounts {
				if a.ProviderID == m.ProviderID && accountAllowed(state.principal, a) && accountSupports(a, m.ID) {
					out = append(out, m.Served)
					break
				}
			}
		}
	}
	return out, nil
}
