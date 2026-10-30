package neoroute

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
)

func TestHTTPRequestsUsageAndVirtualsRemainSessionScoped(t *testing.T) {
	f := newRouteFixture(t, "fill")
	execution, err := f.route.Run(f.ctx, boundedRequest(false), successful)
	if err != nil {
		t.Fatal(err)
	}
	id := execution.Headers.Get("X-NeoProxy-Request-ID")
	member, err := f.identity.CreateUser(context.Background(), neo.UserInput{Handle: "member", Role: "member", State: "active", Password: "test-password-123"})
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := f.identity.Login(context.Background(), member.Handle, "test-password-123")
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	group := router.Group("/v8/neo", neo.NewHTTP(f.identity).SessionMiddleware())
	f.route.RegisterRoutes(group)
	for _, test := range []struct {
		path   string
		status int
		body   string
	}{{"/requests?userId=" + f.user.ID, 200, "[]"}, {"/requests/" + id, 404, ""}, {"/usage?userId=" + f.user.ID, 200, "{}"}, {"/virtual-models", 200, "[]"}, {"/virtual-models/" + f.vm.ID, 404, ""}} {
		request := httptest.NewRequest(http.MethodGet, "/v8/neo"+test.path, nil)
		request.AddCookie(&http.Cookie{Name: "neoproxy_session", Value: token})
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		if recorder.Code != test.status {
			t.Fatalf("%s status%d body%s", test.path, recorder.Code, recorder.Body.String())
		}
		if test.body != "" && strings.TrimSpace(recorder.Body.String()) != test.body {
			t.Fatalf("%s leaked scoped data: %s", test.path, recorder.Body.String())
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/v8/neo/simulate", strings.NewReader(`{"asked":"neoproxy/route","userId":"`+f.user.ID+`"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Neoproxy-CSRF", "1")
	request.AddCookie(&http.Cookie{Name: "neoproxy_session", Value: token})
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != 403 {
		t.Fatalf("member simulated another user: %d %s", recorder.Code, recorder.Body.String())
	}
}
func TestModelsRefreshCurrentGrantsAndKeyScope(t *testing.T) {
	f := newRouteFixture(t, "fill")
	models, err := f.route.Models(f.ctx)
	if err != nil || len(models) != 1 || models[0] != "neoproxy/route" {
		t.Fatalf("catalogue %+v %v", models, err)
	}
	_, err = f.identity.UpdateUser(context.Background(), f.user.ID, neo.UserInput{Handle: f.user.Handle, Role: "service", State: "active", ProviderAccess: []string{"primary"}, Grants: []neo.Grant{{VirtualModel: f.vm.ID, Allotment: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	models, err = f.route.Models(f.ctx)
	if err != nil || len(models) != 2 || models[0] != "neoproxy/route" || models[1] != "neoproxy/primary/model" {
		t.Fatalf("current grants/catalogue %+v %v", models, err)
	}
	if err = f.pool.SetExposure(context.Background(), "primary", false, true); err != nil {
		t.Fatal(err)
	}
	models, err = f.route.Models(f.ctx)
	if err != nil || len(models) != 1 || models[0] != "neoproxy/route" {
		t.Fatalf("hidden provider remains direct %+v %v", models, err)
	}
	if err = f.route.DeleteVirtualModel(neo.WithPrincipal(context.Background(), f.admin), f.vm.ID); err != nil {
		t.Fatal(err)
	}
	principal, err := f.identity.RefreshPrincipal(context.Background(), f.principal)
	if err != nil || len(principal.Grants) != 0 {
		t.Fatalf("deleted VM retained grants %+v %v", principal, err)
	}
	models, err = f.route.Models(f.ctx)
	if err != nil || len(models) != 0 {
		t.Fatalf("revoked/deleted models %+v %v", models, err)
	}
}
func TestSimulationHTTPPassesSafeRequestAndHonestScriptError(t *testing.T) {
	f := newRouteFixture(t, "fill")
	f.route.SetScriptHook(testScript{evaluate: func(ctx context.Context, input ScriptInput) (ScriptProposal, error) {
		if input.Protocol != "openai" || !strings.Contains(string(input.Request), "choose-second") {
			return ScriptProposal{}, scriptError(f.vm.ID, "payload-dependent choice missing")
		}
		return ScriptProposal{StageID: "s0", AccountID: f.accounts[1].ID}, nil
	}})
	f.vm.Script = "configured"
	f.save(t)
	_, token, err := f.identity.Login(context.Background(), "captain", "a-test-password-123")
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	f.route.RegisterRoutes(router.Group("/v8/neo", neo.NewHTTP(f.identity).SessionMiddleware()))
	for _, test := range []struct {
		input  string
		status int
	}{{`{"asked":"neoproxy/route","userId":"` + f.user.ID + `","protocol":"openai","request":{"messages":[{"role":"user","content":"choose-second"}]}}`, 200}, {`{"asked":"neoproxy/route","userId":"` + f.user.ID + `","protocol":"openai","request":{}}`, 422}} {
		request := httptest.NewRequest(http.MethodPost, "/v8/neo/simulate", strings.NewReader(test.input))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Neoproxy-CSRF", "1")
		request.AddCookie(&http.Cookie{Name: "neoproxy_session", Value: token})
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		if recorder.Code != test.status {
			t.Fatalf("status%d wanted%d: %s", recorder.Code, test.status, recorder.Body.String())
		}
		if test.status == 200 {
			var d Decision
			if err = json.Unmarshal(recorder.Body.Bytes(), &d); err != nil || d.AccountID != f.accounts[1].ID {
				t.Fatalf("payload choice %+v %v", d, err)
			}
		} else if !strings.Contains(recorder.Body.String(), "neoproxy_script_error") || !strings.Contains(recorder.Body.String(), "payload-dependent choice missing") {
			t.Fatalf("hidden script diagnostic: %s", recorder.Body.String())
		}
	}
}

func TestHTTPPoolPolicyDetailsAreAdministratorOnly(t *testing.T) {
	f := newRouteFixture(t, "fill")
	f.route.SetScriptHook(testScript{evaluate: func(_ context.Context, input ScriptInput) (ScriptProposal, error) {
		cap, limit := 0.2, 0.1
		result := ScriptPolicyResult{StageID: "s0", Cap: &cap}
		for _, candidate := range input.Stages[0].Candidates {
			result.Accounts = append(result.Accounts, ScriptAccountPolicy{AccountID: candidate.AccountID, Eligible: true, Limit: &limit})
		}
		return ScriptProposal{Policies: []ScriptPolicyResult{result}}, nil
	}})
	f.vm.Script = "configured"
	f.vm.Stages[0].Lua = &LuaPolicy{Eligible: "private_condition", PerAccount: "private_allocation", Cap: "private_cap"}
	f.save(t)
	member, err := f.identity.CreateUser(t.Context(), neo.UserInput{
		Handle: "policy-member", Role: neo.RoleMember, State: "active",
		Password: "member-test-password-123", Grants: []neo.Grant{{VirtualModel: f.vm.ID, Allotment: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := f.identity.PrincipalForUser(t.Context(), member.ID)
	if err != nil {
		t.Fatal(err)
	}
	execution, err := f.route.Run(neo.WithPrincipal(t.Context(), principal), boundedRequest(false), successful)
	if err != nil {
		t.Fatal(err)
	}
	id := execution.Headers.Get("X-NeoProxy-Request-ID")
	_, memberToken, err := f.identity.Login(t.Context(), member.Handle, "member-test-password-123")
	if err != nil {
		t.Fatal(err)
	}
	_, adminToken, err := f.identity.Login(t.Context(), "captain", "a-test-password-123")
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	f.route.RegisterRoutes(router.Group("/v8/neo", neo.NewHTTP(f.identity).SessionMiddleware()))
	for _, viewer := range []struct {
		name, token string
		admin       bool
	}{
		{"member", memberToken, false}, {"admin", adminToken, true},
	} {
		for _, endpoint := range []struct{ method, path, body, marker string }{
			{"GET", "/requests", "", `"policy"`},
			{"GET", "/requests/" + id, "", `"policy"`},
			{"POST", "/simulate", `{"asked":"neoproxy/route","userId":"` + member.ID + `"}`, `"policy"`},
			{"GET", "/virtual-models", "", `"private_condition"`},
			{"GET", "/virtual-models/" + f.vm.ID, "", `"private_condition"`},
		} {
			request := httptest.NewRequest(endpoint.method, "/v8/neo"+endpoint.path, strings.NewReader(endpoint.body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-Neoproxy-CSRF", "1")
			request.AddCookie(&http.Cookie{Name: "neoproxy_session", Value: viewer.token})
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), endpoint.marker) != viewer.admin {
				t.Fatalf("%s %s policy projection: %d %s", viewer.name, endpoint.path, recorder.Code, recorder.Body.String())
			}
		}
	}
	// Serving a member projection must never redact the persisted admin record.
	if record := loadRecord(t, f, id); record.Decision.Trail[0].Policy == nil {
		t.Fatal("member projection erased the administrator's stored policy results")
	}
}
