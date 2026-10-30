package neoroute

import (
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopool"
)

func scriptWindow(w neopool.Window, now int64) ScriptWindow {
	known := w.Known && (w.ResetsAt == 0 || w.ResetsAt > now)
	// Unknown is not an available fraction. Keep the flag even when the last
	// reported value remains useful for explanation.
	return ScriptWindow{Kind: w.Kind, Known: known, Used: w.Used, ResetsAt: w.ResetsAt}
}

// Validate every reduction at the authority boundary, including hooks other than
// the process evaluator. A hook cannot add candidates or omit configured checks.
func validatePolicies(proposal ScriptProposal, input ScriptInput) error {
	if (proposal.StageID == "") != (proposal.AccountID == "") {
		return fmt.Errorf("script selector requires both stageId and accountId")
	}
	configured := false
	expected := make(map[string]ScriptStage)
	for _, stage := range input.Stages {
		configured = configured || stage.Lua != nil
		if stage.Lua != nil && stage.Skipped == "" && len(stage.Candidates) > 0 {
			expected[stage.StageID] = stage
		}
	}
	if proposal.StageID == "" && !configured {
		return fmt.Errorf("script must return an eligible stage/account selector or configured Lua policy results")
	}
	if len(proposal.Policies) != len(expected) {
		return fmt.Errorf("Lua policy returned incomplete stage results")
	}
	for _, result := range proposal.Policies {
		stage, ok := expected[result.StageID]
		if !ok {
			return fmt.Errorf("Lua policy returned unexpected or duplicate stage %q", result.StageID)
		}
		delete(expected, result.StageID)
		if (result.Cap != nil) != (stage.Lua.Cap != "") || (result.Cap != nil && !finiteFraction(*result.Cap)) {
			return fmt.Errorf("stage %q Lua cap must be a finite fraction in 0..1", result.StageID)
		}
		if len(result.Accounts) != len(stage.Candidates) {
			return fmt.Errorf("stage %q Lua policy returned incomplete account results", result.StageID)
		}
		accounts := make(map[string]bool, len(stage.Candidates))
		for _, candidate := range stage.Candidates {
			accounts[candidate.AccountID] = true
		}
		for _, account := range result.Accounts {
			if !accounts[account.AccountID] {
				return fmt.Errorf("stage %q Lua policy account is outside eligible scope", result.StageID)
			}
			delete(accounts, account.AccountID)
			if stage.Lua.Eligible == "" && !account.Eligible {
				return fmt.Errorf("stage %q has no Lua eligibility function", result.StageID)
			}
			if (account.Limit != nil) != (stage.Lua.PerAccount != "") || (account.Limit != nil && !finiteFraction(*account.Limit)) {
				return fmt.Errorf("stage %q Lua perAccount must be a finite fraction in 0..1", result.StageID)
			}
		}
	}
	return nil
}

// Apply only to the freshly authorized static candidates. Missing results for a
// newly eligible account fail closed for that account, never widen the snapshot.
func applyPolicy(tr TrailStep, candidates []neopool.Account, st Stage, allot float64, proposal *ScriptProposal) (TrailStep, []neopool.Account) {
	if st.Lua == nil || tr.Skipped != "" {
		return tr, candidates
	}
	var result *ScriptPolicyResult
	if proposal != nil {
		for i := range proposal.Policies {
			if proposal.Policies[i].StageID == st.ID {
				result = &proposal.Policies[i]
				break
			}
		}
	}
	if result == nil {
		tr.Skipped = "lua-policy"
		tr.Candidates = []Candidate{}
		return tr, nil
	}
	tr.Policy = result
	if result.Cap != nil {
		tr.Cap = min(tr.Cap, *result.Cap*allot)
	}
	policies := make(map[string]ScriptAccountPolicy, len(result.Accounts))
	for _, policy := range result.Accounts {
		policies[policy.AccountID] = policy
	}
	// Both slices were freshly built by evaluateStage; compact them in place.
	kept := candidates[:0]
	budgets := tr.Candidates[:0]
	for i, account := range candidates {
		policy, ok := policies[account.ID]
		if !ok || !policy.Eligible {
			continue
		}
		candidate := tr.Candidates[i]
		if policy.Limit != nil {
			candidate.Budget = min(candidate.Budget, *policy.Limit*allot)
		}
		if candidate.Used < candidate.Budget {
			kept = append(kept, account)
			budgets = append(budgets, candidate)
		}
	}
	tr.Candidates = budgets
	if tr.Total >= tr.Cap {
		tr.Skipped = "lua-cap"
		tr.Candidates = []Candidate{}
		return tr, nil
	}
	if len(kept) == 0 {
		tr.Skipped = "lua-policy"
	}
	return tr, kept
}
