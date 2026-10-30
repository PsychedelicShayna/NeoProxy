//go:build linux && amd64

package neoscript

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neoroute"
)

func TestPoolPolicySimulationAndReservedExecutionAgree(t *testing.T) {
	f := realRoutingFixture(t)
	vm := f.model()
	vm.Stages[0].Lua = &neoroute.LuaPolicy{Eligible: "allow", PerAccount: "allocation", Cap: "stage_cap"}
	vm.Script = fmt.Sprintf(`
function allow(ctx) return ctx.request.input == ctx.account.accountId end
function allocation(ctx) return ctx.account.accountId == %q and 0.125 or 0.25 end
function stage_cap(ctx) return 0.375 end
`, f.accounts[0])
	vm = f.save(t, vm, true)
	f.grant(t, vm.ID, true)
	for i, account := range f.accounts {
		body, _ := json.Marshal(map[string]any{"input": account, "max_output_tokens": 32})
		request := neoroute.Request{Asked: vm.Served, Protocol: "openai-response", Body: body}
		decision, err := f.routes.Simulate(f.memberContext(t), request)
		if err != nil || decision.Outcome != "ok" || decision.AccountID != account {
			t.Fatalf("condition did not route account %d: %+v %v", i, decision, err)
		}
		want := 0.125
		if i == 1 {
			want = 0.25
		}
		if decision.Trail[0].Cap != 0.375 || len(decision.Trail[0].Candidates) != 1 || decision.Trail[0].Candidates[0].Budget != want {
			t.Fatalf("numeric allocations not applied to simulation: %+v", decision)
		}
		called := false
		_, err = f.routes.Run(f.memberContext(t), request, func(ctx context.Context, selection neoroute.Selection) (*neoroute.Execution, error) {
			called = true
			if selection.AuthID != f.engineIDs[account] || selection.Decision.AccountID != decision.AccountID || selection.Decision.Trail[0].Cap != decision.Trail[0].Cap || selection.Decision.Trail[0].Candidates[0].Budget != want {
				t.Fatalf("real reservation/rechecks diverged from simulation: %+v", selection.Decision)
			}
			return &neoroute.Execution{Body: []byte(`{"test":"execution seam"}`)}, nil
		})
		if err != nil || !called {
			t.Fatalf("reserved execution did not use policy: %v", err)
		}
	}
	// False is a routing decision, not a malformed selector or a truthy number.
	request := neoroute.Request{Asked: vm.Served, Protocol: "openai-response", Body: []byte(`{"input":"exclude-every-account","max_output_tokens":32}`)}
	decision, err := f.routes.Simulate(f.memberContext(t), request)
	if err != nil || decision.Outcome != "limited" || decision.Trail[0].Skipped != "lua-policy" {
		t.Fatalf("false condition ignored: %+v %v", decision, err)
	}
	_, err = f.routes.Run(f.memberContext(t), request, func(context.Context, neoroute.Selection) (*neoroute.Execution, error) {
		t.Fatal("a false condition dispatched an upstream request")
		return nil, nil
	})
	if err == nil {
		t.Fatal("false policy was not enforced in execution")
	}
}

func TestPoolPolicyCannotWidenInferenceKeyScope(t *testing.T) {
	f := realRoutingFixture(t)
	vm := f.model()
	vm.Stages[0].Accounts = neoroute.AccountScope{IDs: []string{f.accounts[1]}}
	vm.Stages[0].Lua = &neoroute.LuaPolicy{Eligible: "allow", PerAccount: "allocation"}
	vm.Script = `function allow(ctx) return true end; function allocation(ctx) return 1 end`
	vm = f.save(t, vm, true)
	f.grant(t, vm.ID, true)
	_, secret, err := f.identity.MintKey(context.Background(), f.admin, f.member.ID, "restricted-pool-policy", []string{f.accounts[0]}, nil)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := f.identity.AuthenticateKey(context.Background(), secret)
	if err != nil {
		t.Fatal(err)
	}
	ctx := neo.WithPrincipal(context.Background(), principal)
	request := neoroute.Request{Asked: vm.Served, Protocol: "openai-response", Body: []byte(`{"input":"ordinary request","max_output_tokens":32}`)}
	decision, err := f.routes.Simulate(ctx, request)
	if err != nil || decision.Outcome == "ok" {
		t.Fatalf("true/1 callbacks widened the inference key: %+v %v", decision, err)
	}
	_, err = f.routes.Run(ctx, request, func(context.Context, neoroute.Selection) (*neoroute.Execution, error) {
		t.Fatal("an out-of-scope account was dispatched")
		return nil, nil
	})
	if err == nil {
		t.Fatal("out-of-scope execution was admitted")
	}
}

func TestPoolPolicyFractionalLimitsReachAtomicReservations(t *testing.T) {
	for _, test := range []struct {
		name, script             string
		staticAccount, staticCap float64
	}{
		{"dynamic per-account", `function allocation(ctx) return 0.00001 end; function stage_cap(ctx) return 1 end`, 1, 1},
		{"dynamic cap", `function allocation(ctx) return 1 end; function stage_cap(ctx) return 0.00001 end`, 1, 1},
		{"static account ceiling", `function allocation(ctx) return 1 end; function stage_cap(ctx) return 1 end`, 0.00001, 1},
		{"static cap ceiling", `function allocation(ctx) return 1 end; function stage_cap(ctx) return 1 end`, 1, 0.00001},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := realRoutingFixture(t)
			vm := f.model()
			vm.Stages[0].Lua = &neoroute.LuaPolicy{PerAccount: "allocation", Cap: "stage_cap"}
			vm.Stages[0].PerAccount, vm.Stages[0].Cap = test.staticAccount, test.staticCap
			vm.Script = test.script
			vm = f.save(t, vm, true)
			f.grant(t, vm.ID, true)
			request := neoroute.Request{Asked: vm.Served, Protocol: "openai-response", Body: []byte(`{"input":"fractional request bound","max_output_tokens":128}`)}
			_, err := f.routes.Run(f.memberContext(t), request, func(context.Context, neoroute.Selection) (*neoroute.Execution, error) {
				t.Fatal("request exceeded the 10-token fractional allocation but dispatched")
				return nil, nil
			})
			if err == nil {
				t.Fatal("fractional policy was only displayed, not reserved")
			}
			usage, err := f.pools.Usage(context.Background(), f.member.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, record := range usage {
				if record.Tokens != 0 || record.Reserved != 0 {
					t.Fatalf("rejected allocation leaked usage: %+v", usage)
				}
			}
		})
	}
}

func TestPoolPolicyCapCountsConcurrentReservations(t *testing.T) {
	f := realRoutingFixture(t)
	vm := f.model()
	request := neoroute.Request{Protocol: "openai-response", Body: []byte(`{"input":"concurrent allocation","max_output_tokens":128}`)}
	// Exactly one bound fits under the fractional cap; two never do.
	bound := len(request.Body) + 128
	vm.Script = fmt.Sprintf(`function stage_cap(ctx) return %.12f end`, float64(bound+1)/1000000)
	vm.Stages[0].Lua = &neoroute.LuaPolicy{Cap: "stage_cap"}
	vm = f.save(t, vm, true)
	f.grant(t, vm.ID, true)
	request.Asked = vm.Served
	entered, release := make(chan struct{}), make(chan struct{})
	firstDone := make(chan error, 1)
	ctx, cancel := context.WithCancel(f.memberContext(t))
	defer cancel()
	go func() {
		_, err := f.routes.Run(ctx, request, func(ctx context.Context, _ neoroute.Selection) (*neoroute.Execution, error) {
			close(entered)
			select {
			case <-release:
				return &neoroute.Execution{Body: []byte(`{"test":"held execution seam"}`)}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		})
		firstDone <- err
	}()
	select {
	case <-entered:
	case err := <-firstDone:
		t.Fatalf("first allocation did not reach execution: %v", err)
	}
	defer close(release)
	_, err := f.routes.Run(f.memberContext(t), request, func(context.Context, neoroute.Selection) (*neoroute.Execution, error) {
		t.Error("concurrent request exceeded the Lua stage cap")
		return &neoroute.Execution{Body: []byte(`{}`)}, nil
	})
	if err == nil {
		t.Error("in-flight fractional reservation was ignored")
	}
	// Do not wait for the held request while its release is deferred; cancellation
	// exits its execution seam and still exercises normal settlement/reaping.
	cancel()
	<-firstDone
}
