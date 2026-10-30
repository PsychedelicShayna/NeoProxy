package neoroute

import (
	"math"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopool"
)

func TestPoolPolicyReductionsRespectGrantAndStaticCeilings(t *testing.T) {
	state := policyState()
	state.principal.Grants[0].Allotment = 0.5
	state.principal.AccountIDs = []string{"b"}
	state.vm.Stages[0].Lua = &LuaPolicy{Eligible: "allow", PerAccount: "allocation", Cap: "cap"}
	one := 1.0
	proposal := &ScriptProposal{Policies: []ScriptPolicyResult{{StageID: "s0", Cap: &one, Accounts: []ScriptAccountPolicy{{AccountID: "a", Eligible: true, Limit: &one}, {AccountID: "b", Eligible: true, Limit: &one}}}}}
	decision := decide(Request{Asked: state.vm.Served}, state, -1, proposal)
	if decision.AccountID != "b" || decision.Trail[0].Cap != 0.4 || len(decision.Trail[0].Candidates) != 1 || decision.Trail[0].Candidates[0].Budget != 0.25 {
		t.Fatalf("Lua widened a grant, hard ceiling or fresh account scope: %+v", decision)
	}
	// Revoked authority and unavailable denominators still win over true / 1.
	state.snapshot.Accounts[1].Windows = nil
	if decision = decide(Request{Asked: state.vm.Served}, state, -1, proposal); decision.Outcome == "ok" {
		t.Fatalf("Lua invented a denominator for an unmetered account: %+v", decision)
	}
	state.principal.Grants = nil
	if decision = decide(Request{Asked: state.vm.Served}, state, -1, proposal); decision.Reason != "not-granted" {
		t.Fatalf("Lua widened a revoked grant: %+v", decision)
	}
}

func TestPoolPolicyHookResultsCannotAddScopeOrInvalidLimits(t *testing.T) {
	input := ScriptInput{Stages: []ScriptStage{{StageID: "s0", Lua: &LuaPolicy{Eligible: "allow", PerAccount: "allocation", Cap: "cap"}, Candidates: []Candidate{{AccountID: "a"}}}}}
	one := 1.0
	valid := func() ScriptProposal {
		return ScriptProposal{Policies: []ScriptPolicyResult{{StageID: "s0", Cap: &one, Accounts: []ScriptAccountPolicy{{AccountID: "a", Eligible: true, Limit: &one}}}}}
	}
	if err := validatePolicies(valid(), input); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func(*ScriptProposal)
	}{
		{"omitted policy", func(p *ScriptProposal) { p.Policies = nil }},
		{"other stage", func(p *ScriptProposal) { p.Policies[0].StageID = "other" }},
		{"other account", func(p *ScriptProposal) { p.Policies[0].Accounts[0].AccountID = "outside-key-scope" }},
		{"duplicate account", func(p *ScriptProposal) {
			p.Policies[0].Accounts = append(p.Policies[0].Accounts, p.Policies[0].Accounts[0])
		}},
		{"missing allocation", func(p *ScriptProposal) { p.Policies[0].Accounts[0].Limit = nil }},
		{"nonfinite allocation", func(p *ScriptProposal) { n := math.NaN(); p.Policies[0].Accounts[0].Limit = &n }},
		{"infinite cap", func(p *ScriptProposal) { n := math.Inf(1); p.Policies[0].Cap = &n }},
		{"over whole window", func(p *ScriptProposal) { n := 2.0; p.Policies[0].Cap = &n }},
	} {
		t.Run(test.name, func(t *testing.T) {
			proposal := valid()
			test.change(&proposal)
			if err := validatePolicies(proposal, input); err == nil {
				t.Fatal("invalid hook reduction escaped the authority boundary")
			}
		})
	}
}

func TestPoolPolicySnapshotKeepsUnknownAndExpiredQuotaUnknown(t *testing.T) {
	for _, window := range []neopool.Window{
		{Kind: "5h", Known: false, Used: 0},
		{Kind: "5h", Known: true, Used: 0.2, ResetsAt: 1000},
	} {
		if got := scriptWindow(window, 2000); got.Known {
			t.Fatalf("unknown/expired native quota became available headroom: %+v", got)
		}
	}
	if got := scriptWindow(neopool.Window{Kind: "5h", Known: true, Used: 0.2, ResetsAt: 3000}, 2000); !got.Known || got.Used != 0.2 {
		t.Fatalf("fresh native report lost: %+v", got)
	}
}

func TestPoolPolicyVirtualOnlyModelsHaveNoUnmeteredBypass(t *testing.T) {
	state := policyState()
	state.snapshot.Models[0].VirtualOnly = true
	state.principal.ProviderAccess = []string{"primary"}
	if d := decide(Request{Asked: "neoproxy/primary/model"}, state, -1, nil); d.Outcome == "ok" {
		t.Fatalf("virtual-only source model was served directly: %+v", d)
	}
	// A normal metered VM stage remains usable.
	if d := decide(Request{Asked: state.vm.Served}, state, -1, nil); d.Outcome != "ok" || d.StageID != "s0" {
		t.Fatalf("metered source VM became unusable: %+v", d)
	}
	state.vm.Stages[0].Cap = 0
	state.vm.Terminal = Terminal{Kind: "passthrough", Model: "primary/model"}
	if d := decide(Request{Asked: state.vm.Served}, state, -1, nil); d.Outcome == "ok" {
		t.Fatalf("virtual-only source escaped via budget-free terminal: %+v", d)
	}
}

func TestPoolPolicyConfigurationRequiresSourceAndSimpleNames(t *testing.T) {
	for _, test := range []struct {
		name   string
		policy *LuaPolicy
		source string
	}{
		{"missing source", &LuaPolicy{Eligible: "allow"}, ""},
		{"empty policy", &LuaPolicy{}, "function allow(ctx) return true end"},
		{"call instead of name", &LuaPolicy{Eligible: "allow()"}, "function allow(ctx) return true end"},
		{"property instead of name", &LuaPolicy{Cap: "policy.cap"}, "policy = {}"},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := policyState()
			state.vm.Script, state.vm.Stages[0].Lua = test.source, test.policy
			if err := Validate(*state.vm, state.snapshot, state.principal); err == nil {
				t.Fatal("unusable named policy configuration was saved")
			}
		})
	}
}

func TestPoolPolicyHookCannotSilentlyDropSelector(t *testing.T) {
	input := ScriptInput{Stages: []ScriptStage{{StageID: "s0", Candidates: []Candidate{{AccountID: "a"}}}}}
	for _, proposal := range []ScriptProposal{{}, {StageID: "s0"}, {AccountID: "a"}} {
		if err := validatePolicies(proposal, input); err == nil {
			t.Fatal("malformed selector silently became built-in routing")
		}
	}
}
