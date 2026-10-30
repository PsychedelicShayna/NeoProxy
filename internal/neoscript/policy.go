package neoscript

import (
	"fmt"
	"math"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neoroute"
	lua "github.com/yuin/gopher-lua"
)

type policySnapshot struct {
	Stages   []neoroute.ScriptStage   `json:"stages"`
	Accounts []neoroute.ScriptAccount `json:"accounts"`
}

type policyAccount struct {
	value   lua.LValue
	windows []neoroute.ScriptWindow
}

// Calls share one disposable VM and its immutable snapshot. Only plain typed
// results cross the process boundary; functions never enter the routing process.
func evaluatePolicies(state *lua.LState, input policySnapshot, root lua.LValue, readonly map[*lua.LTable]*lua.LTable) ([]neoroute.ScriptPolicyResult, bool, error) {
	var results []neoroute.ScriptPolicyResult
	configured := false
	stages := state.GetField(root, "stages")
	accounts := state.GetField(root, "accounts")
	accountValues := make(map[string]policyAccount, len(input.Accounts))
	for i, account := range input.Accounts {
		accountValues[account.AccountID] = policyAccount{value: state.GetTable(accounts, lua.LNumber(i+1)), windows: account.Windows}
	}
	for i, stage := range input.Stages {
		if stage.Lua == nil {
			continue
		}
		configured = true
		if stage.Skipped != "" || len(stage.Candidates) == 0 {
			continue
		}
		stageValue := state.GetTable(stages, lua.LNumber(i+1))
		contextFor := func(account lua.LValue, candidate *neoroute.Candidate) lua.LValue {
			backing := state.NewTable()
			for _, field := range []string{"asked", "protocol", "userId", "request"} {
				backing.RawSetString(field, state.GetField(root, field))
			}
			backing.RawSetString("stage", stageValue)
			backing.RawSetString("allotment", lua.LNumber(stage.Allotment))
			if candidate != nil {
				backing.RawSetString("account", account)
				backing.RawSetString("used", lua.LNumber(candidate.Used))
				backing.RawSetString("budget", lua.LNumber(candidate.Budget))
				// The budget window is a local configured meter, never an invented
				// native entitlement. Missing/native unknown windows stay unknown.
				window := lua.LValue(lua.LNil)
				for j, w := range accountValues[candidate.AccountID].windows {
					if w.Kind == stage.Window {
						window = state.GetTable(state.GetField(account, "windows"), lua.LNumber(j+1))
						break
					}
				}
				if window == lua.LNil {
					window = toLua(state, map[string]any{"kind": stage.Window, "known": false}, readonly)
				}
				backing.RawSetString("window", window)
			}
			return readonlyTable(state, backing, readonly)
		}
		result := neoroute.ScriptPolicyResult{StageID: stage.StageID, Accounts: make([]neoroute.ScriptAccountPolicy, 0, len(stage.Candidates))}
		if stage.Lua.Cap != "" {
			value, err := callPolicy(state, stage.Lua.Cap, contextFor(lua.LNil, nil), stage.StageID)
			if err != nil {
				return nil, configured, err
			}
			fraction, err := policyFraction(value, stage.StageID, stage.Lua.Cap)
			if err != nil {
				return nil, configured, err
			}
			result.Cap = &fraction
		}
		for _, candidate := range stage.Candidates {
			account, ok := accountValues[candidate.AccountID]
			if !ok {
				return nil, configured, fmt.Errorf("stage %q policy account missing from snapshot", stage.StageID)
			}
			ctx := contextFor(account.value, &candidate)
			policy := neoroute.ScriptAccountPolicy{AccountID: candidate.AccountID, Eligible: true}
			if stage.Lua.Eligible != "" {
				value, err := callPolicy(state, stage.Lua.Eligible, ctx, stage.StageID)
				if err != nil {
					return nil, configured, err
				}
				eligible, ok := value.(lua.LBool)
				if !ok {
					return nil, configured, fmt.Errorf("stage %q function %q must return a boolean (got %s)", stage.StageID, stage.Lua.Eligible, value.Type())
				}
				policy.Eligible = bool(eligible)
			}
			// Evaluate every configured function, even for a false condition, so
			// invalid policies cannot hide behind today's account eligibility.
			if stage.Lua.PerAccount != "" {
				value, err := callPolicy(state, stage.Lua.PerAccount, ctx, stage.StageID)
				if err != nil {
					return nil, configured, err
				}
				fraction, err := policyFraction(value, stage.StageID, stage.Lua.PerAccount)
				if err != nil {
					return nil, configured, err
				}
				policy.Limit = &fraction
			}
			result.Accounts = append(result.Accounts, policy)
		}
		results = append(results, result)
	}
	return results, configured, nil
}

func callPolicy(state *lua.LState, name string, ctx lua.LValue, stage string) (lua.LValue, error) {
	fn, ok := state.GetGlobal(name).(*lua.LFunction)
	if !ok {
		return nil, fmt.Errorf("stage %q Lua function %q is not defined", stage, name)
	}
	top := state.GetTop()
	if err := state.CallByParam(lua.P{Fn: fn, NRet: lua.MultRet, Protect: true}, ctx); err != nil {
		return nil, fmt.Errorf("stage %q function %q: %w", stage, name, err)
	}
	if state.GetTop() != top+1 {
		state.SetTop(top)
		return nil, fmt.Errorf("stage %q function %q must return exactly one value", stage, name)
	}
	value := state.Get(-1)
	state.Pop(1)
	return value, nil
}

func policyFraction(value lua.LValue, stage, name string) (float64, error) {
	number, ok := value.(lua.LNumber)
	fraction := float64(number)
	if !ok || math.IsNaN(fraction) || math.IsInf(fraction, 0) || fraction < 0 || fraction > 1 {
		return 0, fmt.Errorf("stage %q function %q must return a finite number in 0..1 (window fraction; 0.05 = 5%%)", stage, name)
	}
	return fraction, nil
}
