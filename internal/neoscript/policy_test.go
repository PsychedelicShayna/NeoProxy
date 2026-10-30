//go:build linux && amd64

package neoscript

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neoroute"
)

func poolPolicyInput(source string) neoroute.ScriptInput {
	input := exampleInput(source)
	input.Request = json.RawMessage(`{"input":"account-b"}`)
	input.Stages = input.Stages[1:]
	input.Stages[0].Lua = &neoroute.LuaPolicy{Eligible: "allow", PerAccount: "allocation", Cap: "stage_cap"}
	input.Stages[0].Allotment = 0.5
	input.Stages[0].Window = "day"
	input.Accounts = []neoroute.ScriptAccount{
		{AccountID: "account-a", ProviderID: "ordinary-source", State: "healthy", Windows: []neoroute.ScriptWindow{{Kind: "day", Known: true, Used: 0.4}}, ProviderWindows: []neoroute.ScriptWindow{{Kind: "5h", Known: false, Used: 0}}},
		{AccountID: "account-b", ProviderID: "broker-source", SourceID: "source-example", State: "healthy", Windows: []neoroute.ScriptWindow{{Kind: "day", Known: true, Used: 0.1}}, ProviderWindows: []neoroute.ScriptWindow{{Kind: "5h", Known: true, Used: 0.2}}},
	}
	return input
}

const typedPolicySource = `
function allow(ctx)
  assert(ctx.account.credentials == nil and ctx.account.authId == nil)
  assert(ctx.window.kind == "day" and ctx.window.known)
  assert(ctx.allotment == 0.5 and ctx.stage.window == "day")
  return ctx.request.input == ctx.account.accountId
end
function allocation(ctx) return ctx.account.accountId == "account-b" and 0.125 or 0.0625 end
function stage_cap(ctx)
  assert(ctx.account == nil and ctx.window == nil)
  return ctx.stage.total + 0.125
end
`

func TestPoolPolicyBooleanAndNumericCallbacksAreNotSelectors(t *testing.T) {
	input := poolPolicyInput(typedPolicySource)
	proposal, err := New().Evaluate(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if proposal.StageID != "" || proposal.AccountID != "" || len(proposal.Policies) != 1 {
		t.Fatalf("callbacks were mistaken for a selector: %+v", proposal)
	}
	policy := proposal.Policies[0]
	if policy.Cap == nil || *policy.Cap != 0.325 || len(policy.Accounts) != 2 || policy.Accounts[0].Eligible || !policy.Accounts[1].Eligible || policy.Accounts[1].Limit == nil || *policy.Accounts[1].Limit != 0.125 {
		t.Fatalf("typed policy result = %+v", policy)
	}
	// A useful old selector can still choose among the reduced eligible set.
	input.Source += `return {stageId="stage-second", accountId="account-b"}`
	proposal, err = New().Evaluate(context.Background(), input)
	if err != nil || proposal.AccountID != "account-b" || len(proposal.Policies) != 1 {
		t.Fatalf("legacy selector + callbacks = %+v, %v", proposal, err)
	}
}

func TestPoolPolicyInvalidTypesNumbersAndRuntimeFailClosed(t *testing.T) {
	cases := []struct {
		name, source, want string
	}{
		{"numeric condition", `function allow(ctx) return 1 end`, "boolean"},
		{"boolean allocation", `function allocation(ctx) return true end`, "finite number"},
		{"numeric string", `function allocation(ctx) return "0.1" end`, "finite number"},
		{"nonfinite", `function allocation(ctx) return math.huge end`, "finite number"},
		{"nan", `function allocation(ctx) return 0/0 end`, "finite number"},
		{"negative", `function allocation(ctx) return -0.01 end`, "finite number"},
		{"over whole window", `function allocation(ctx) return 1.01 end`, "finite number"},
		{"table allocation", `function allocation(ctx) return {stageId="stage-second",accountId="account-b"} end`, "finite number"},
		{"multiple values", `function allocation(ctx) return 0.1,0.2 end`, "exactly one"},
		{"missing value", `function stage_cap(ctx) end`, "exactly one"},
		{"nonfinite cap", `function stage_cap(ctx) return -math.huge end`, "finite number"},
		{"missing function", `allow = nil`, "not defined"},
		{"runtime error", `function allow(ctx) error("deliberate-condition-failure") end`, "deliberate-condition-failure"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			input := poolPolicyInput(typedPolicySource + "\n" + test.source)
			_, err := New().Evaluate(context.Background(), input)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("invalid callback was not rejected with usable diagnostic: %v", err)
			}
		})
	}
}

func TestPoolPolicyUnknownNativeWindowsAreNotLocalMeters(t *testing.T) {
	input := poolPolicyInput(`
function allow(ctx)
  for _, window in ipairs(ctx.account.providerWindows or {}) do
    if window.kind == "5h" and window.known then return window.used < 0.5 end
  end
  return false
end
function allocation(ctx) return 0.1 end
function stage_cap(ctx) return 0.2 end
`)
	for _, windows := range [][]neoroute.ScriptWindow{nil, {{Kind: "5h", Known: false, Used: 0}}} {
		input.Accounts[0].ProviderWindows = windows
		proposal, err := New().Evaluate(context.Background(), input)
		if err != nil || proposal.Policies[0].Accounts[0].Eligible || !proposal.Policies[0].Accounts[1].Eligible {
			t.Fatalf("unknown native quota was inferred from the known local day meter: %+v %v", proposal, err)
		}
	}
}

func TestPoolPolicyContextIsReadOnlyAndConfined(t *testing.T) {
	for _, mutation := range []string{
		`ctx.request.input="account-a"`,
		`ctx.account.providerWindows[1].used=0`,
		`ctx.window.used=0`,
		`ctx.stage.cap=1`,
		`table.remove(ctx.stage.candidates,1)`,
	} {
		t.Run(mutation, func(t *testing.T) {
			input := poolPolicyInput(typedPolicySource + ` function allow(ctx) ` + mutation + `; return true end`)
			_, err := New().Evaluate(context.Background(), input)
			if err == nil || !strings.Contains(err.Error(), "read-only") {
				t.Fatalf("callback mutated authorized context: %v", err)
			}
		})
	}
	input := poolPolicyInput(typedPolicySource + `
input = {} -- Rebinding the convenience global cannot replace callback arguments.
function allow(ctx)
  assert(os==nil and io==nil and package==nil and require==nil and loadfile==nil)
  return ctx.request.input == ctx.account.accountId
end`)
	proposal, err := New().Evaluate(context.Background(), input)
	if err != nil || proposal.Policies[0].Accounts[0].Eligible || !proposal.Policies[0].Accounts[1].Eligible {
		t.Fatalf("callback did not receive its original confined snapshot: %+v %v", proposal, err)
	}
}
