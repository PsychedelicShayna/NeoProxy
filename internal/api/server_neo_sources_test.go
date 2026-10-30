package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopool"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neoroute"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

// The upstream is a credential-specific authorization/fault fixture, not a model
// echo. The consumer traverses the real session/key, VM, reservation, manager,
// translation, source executor and usage-settlement layers over HTTP.
func TestNeoPiSourceHTTPThreeWiresPinScopeAndMetering(t *testing.T) {
	t.Setenv("NEOPROXY_DATA_DIR", "")
	identity, adminUser, _, _ := neoTestIdentity(t)
	server := newTestServerWithOptions(t, WithNeoStore(identity), WithRequestLoggerFactory(nil))
	if err := server.neoStartupError(); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	admin := neo.Principal{UserID: adminUser.ID, Role: neo.RoleAdmin, Session: true}
	adminCtx := neo.WithPrincipal(ctx, admin)
	const firstModel = "unlisted-native/first-model"
	const otherModel = "unlisted-native/other-model"
	server.cfg.Payload = config.PayloadConfig{
		Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: firstModel, Protocol: "openai"}}, Params: map[string]interface{}{"temperature": .2}}},
		Filter:   []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: firstModel, Protocol: "openai"}}, Params: []string{"stream_options.include_usage"}}},
	}
	var mu sync.Mutex
	bearer := "private-gateway-one"
	active := true
	pins := []string{}
	var invalidPayload bool
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+bearer {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/models":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"object": "list", "data": []map[string]interface{}{{"id": firstModel, "context_length": 128000}, {"id": otherModel}}})
		case "/v1/neoproxy/accounts":
			accounts := []map[string]interface{}{{"id": "row-b", "provider": "unlisted-native", "label": "second account", "models": []string{firstModel, otherModel}, "state": "healthy", "windows": []interface{}{}}}
			if active {
				accounts = append(accounts, map[string]interface{}{"id": "row-a", "provider": "unlisted-native", "label": "first account", "models": []string{firstModel}, "state": "healthy", "windows": []map[string]interface{}{{"kind": "native-five-hour", "known": true, "used": .4, "resetsAt": time.Now().Add(time.Hour).UnixMilli()}}})
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"object": "list", "data": accounts})
		case "/v1/chat/completions":
			pin := r.Header.Get("X-Neopi-Credential-Id")
			pins = append(pins, pin)
			body, _ := io.ReadAll(r.Body)
			if pin != "row-a" || !active || gjson.GetBytes(body, "model").String() != firstModel {
				w.WriteHeader(http.StatusForbidden)
				_, _ = io.WriteString(w, `{"error":{"message":"credential does not authorize this model"}}`)
				return
			}
			if gjson.GetBytes(body, "temperature").Float() != .2 || gjson.GetBytes(body, "stream_options.include_usage").Exists() {
				invalidPayload = true
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if gjson.GetBytes(body, "stream").Bool() {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-source\",\"object\":\"chat.completion.chunk\",\"model\":\""+firstModel+"\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"scoped fixture result\"},\"finish_reason\":null}]}\n\n")
				_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-source\",\"object\":\"chat.completion.chunk\",\"model\":\""+firstModel+"\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":3,\"total_tokens\":10}}\n\ndata: [DONE]\n\n")
				return
			}
			_, _ = io.WriteString(w, `{"id":"chatcmpl-source","object":"chat.completion","created":1,"model":"`+firstModel+`","choices":[{"index":0,"message":{"role":"assistant","content":"scoped fixture result"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer gateway.Close()
	input := neopool.SourceInput{Label: "Office NeoPi", URL: gateway.URL, Bearer: bearer, OwnerID: admin.UserID, Enabled: true}
	source, err := server.neoPool.SaveSource(adminCtx, "", input)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if errDelete := server.neoPool.DeleteSource(adminCtx, source.ID); errDelete != nil {
			t.Error(errDelete)
		}
	}()
	if err = server.neoPool.SyncSource(ctx, source.ID); err != nil {
		t.Fatal(err)
	}
	snapshot, err := server.neoPool.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var first, second neopool.Account
	for _, a := range snapshot.Accounts {
		if a.Label == "first account" {
			first = a
		} else if a.Label == "second account" {
			second = a
		}
	}
	if first.ID == "" || second.ID == "" || first.ID == second.ID || len(first.Windows) != 0 {
		t.Fatalf("individual source accounts or unknown local budgets lost: %+v", snapshot.Accounts)
	}
	for _, a := range []neopool.Account{first, second} {
		if err = server.neoPool.ConfigureWindow(ctx, a.ID, admin.UserID, "day", 100000, time.Now().Add(-time.Hour).UnixMilli(), true); err != nil {
			t.Fatal(err)
		}
	}
	vm, err := server.neoRoute.VirtualModel(ctx, neopool.SourceVirtualName(source.ID, firstModel))
	if err != nil {
		t.Fatal(err)
	}
	other, err := server.neoRoute.VirtualModel(ctx, neopool.SourceVirtualName(source.ID, otherModel))
	if err != nil {
		t.Fatal(err)
	}
	if len(vm.Stages) != 1 || vm.Stages[0].Model != first.ModelIDs[0] || vm.Terminal.Kind != "limit" || !strings.Contains(vm.Description, "Office NeoPi") || !strings.Contains(vm.Description, firstModel) {
		t.Fatalf("source did not create a first-class metered VM: %+v", vm)
	}
	user, err := identity.CreateUser(ctx, neo.UserInput{Handle: "source-client", Role: neo.RoleService, State: "active", ProviderAccess: []string{first.ProviderID}, Grants: []neo.Grant{{VirtualModel: vm.ID, Allotment: 1}, {VirtualModel: other.ID, Allotment: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	_, secret, err := identity.MintKey(ctx, admin, user.ID, "first-row-only", []string{first.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	private := httptest.NewServer(server.engine)
	defer private.Close()
	request := func(method, path, body string) (int, http.Header, []byte) {
		t.Helper()
		req, errRequest := http.NewRequestWithContext(ctx, method, private.URL+path, strings.NewReader(body))
		if errRequest != nil {
			t.Fatal(errRequest)
		}
		req.Header.Set("Authorization", "Bearer "+secret)
		req.Header.Set("Content-Type", "application/json")
		// This untrusted client pin must never override the router's selected row.
		req.Header.Set("X-Neopi-Credential-Id", "row-b")
		response, errRequest := private.Client().Do(req)
		if errRequest != nil {
			t.Fatal(errRequest)
		}
		defer response.Body.Close()
		data, errRead := io.ReadAll(response.Body)
		if errRead != nil {
			t.Fatal(errRead)
		}
		return response.StatusCode, response.Header, data
	}
	status, _, data := request("GET", "/v1/models", "")
	if status != http.StatusOK || !strings.Contains(string(data), vm.Served) || strings.Contains(string(data), first.ProviderID+"/") {
		t.Fatalf("source catalogue escaped stored VM/grant projection: %d %s", status, data)
	}
	for _, protocol := range []struct{ path, content string }{
		{"/v1/chat/completions", `"messages":[{"role":"user","content":"hello"}],"max_tokens":32`},
		{"/v1/responses", `"input":[{"role":"user","content":"hello"}],"max_output_tokens":32`},
		{"/v1/messages", `"messages":[{"role":"user","content":"hello"}],"max_tokens":32`},
	} {
		for _, stream := range []bool{false, true} {
			body := `{"model":"` + vm.Served + `",` + protocol.content
			if stream {
				body += `,"stream":true`
			}
			body += `}`
			status, headers, data := request("POST", protocol.path, body)
			if status != http.StatusOK || !strings.Contains(string(data), "scoped fixture result") {
				t.Fatalf("%s stream=%v status=%d body=%s", protocol.path, stream, status, data)
			}
			var raw []byte
			if err = identity.DB().QueryRowContext(ctx, `SELECT body FROM neoroute_requests WHERE id=?`, headers.Get("X-NeoProxy-Request-ID")).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var record neoroute.RequestRecord
			if err = json.Unmarshal(raw, &record); err != nil {
				t.Fatal(err)
			}
			if record.Decision.AccountID != first.ID || record.Decision.Model != first.ModelIDs[0] || record.Tokens.In != 7 || record.Tokens.Out != 3 {
				t.Fatalf("wrong selected source account or measured charge: %s", raw)
			}
		}
	}
	mu.Lock()
	if invalidPayload || len(pins) != 6 {
		mu.Unlock()
		t.Fatal("source executor did not preserve the final payload-config barrier")
	}
	for _, pin := range pins {
		if pin != "row-a" {
			mu.Unlock()
			t.Fatal("client pin or retry escaped the selected account")
		}
	}
	mu.Unlock()
	for _, asked := range []string{"neoproxy/" + first.ModelIDs[0], firstModel, other.Served} {
		status, _, data = request("POST", "/v1/chat/completions", `{"model":"`+asked+`","messages":[{"role":"user","content":"scope check"}],"max_tokens":32}`)
		if status < 400 {
			t.Fatalf("direct selector/model entitlement/key scope escaped: %s %d %s", asked, status, data)
		}
	}
	var inputTokens, outputTokens, attempts int64
	if err = identity.DB().QueryRowContext(ctx, `SELECT COALESCE(sum(input_tokens),0),COALESCE(sum(output_tokens),0),count(*) FROM neo_pool_attempts WHERE account_id=? AND state='settled'`, first.ID).Scan(&inputTokens, &outputTokens, &attempts); err != nil {
		t.Fatal(err)
	}
	if inputTokens != 42 || outputTokens != 18 || attempts != 6 {
		t.Fatalf("observed usage not attributed exactly once: in=%d out=%d attempts=%d", inputTokens, outputTokens, attempts)
	}
	if err = identity.DB().QueryRowContext(ctx, `SELECT count(*) FROM neo_pool_attempts WHERE account_id=?`, second.ID).Scan(&attempts); err != nil || attempts != 0 {
		t.Fatal("out-of-key-scope sibling received a metered attempt")
	}
	// Gateway bearer rotation preserves account IDs, configured meters, edited
	// routes and grants. No broker/provider refresh material is imported.
	vm.Description = "Administrator-customized source route"
	if _, err = server.neoRoute.SaveVirtualModel(adminCtx, vm, false); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	bearer = "private-gateway-two"
	mu.Unlock()
	input.Bearer = "private-gateway-two"
	if _, err = server.neoPool.SaveSource(adminCtx, source.ID, input); err != nil {
		t.Fatal(err)
	}
	if err = server.neoPool.SyncSource(ctx, source.ID); err != nil {
		t.Fatal(err)
	}
	persisted, err := server.neoRoute.VirtualModel(ctx, vm.ID)
	if err != nil || persisted.Description != vm.Description || persisted.Stages[0].ID != vm.Stages[0].ID {
		t.Fatal("catalogue refresh overwrote the administrator's stored route")
	}
	status, _, data = request("POST", "/v1/chat/completions", `{"model":"`+vm.Served+`","messages":[{"role":"user","content":"after rotation"}],"max_tokens":32}`)
	if status != http.StatusOK {
		t.Fatalf("rotation broke a granted source route: %d %s", status, data)
	}
	// A manager retry can retain its original auth/principal clone. Revoking its
	// key must still fail before a fresh gateway request is dispatched.
	_, retrySecret, err := identity.MintKey(ctx, admin, user.ID, "retry-revocation", []string{first.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	retryPrincipal, err := identity.AuthenticateKey(ctx, retrySecret)
	if err != nil {
		t.Fatal(err)
	}
	selected, ok := server.handlers.AuthManager.GetByID(first.EngineAuthID)
	if !ok {
		t.Fatal("selected source account disappeared")
	}
	sourceExecutor, ok := server.handlers.AuthManager.Executor(first.ProviderID)
	if !ok {
		t.Fatal("source executor disappeared")
	}
	if err = identity.RevokeKey(ctx, admin, user.ID, retryPrincipal.KeyID); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	beforeRevokedKey := len(pins)
	mu.Unlock()
	_, err = sourceExecutor.Execute(neo.WithPrincipal(ctx, retryPrincipal), selected, coreexecutor.Request{Model: firstModel}, coreexecutor.Options{Metadata: map[string]interface{}{coreexecutor.PinnedAuthMetadataKey: selected.ID}})
	if err == nil {
		t.Fatal("retained retry principal escaped inference-key revocation")
	}
	mu.Lock()
	if len(pins) != beforeRevokedKey {
		mu.Unlock()
		t.Fatal("revoked inference key dispatched to the gateway")
	}
	mu.Unlock()
	// A logout at NeoPi is immediately rejected by the upstream pin, even before
	// discovery runs; there is no silent fallback to another broker credential.
	mu.Lock()
	active = false
	before := len(pins)
	mu.Unlock()
	status, _, _ = request("POST", "/v1/chat/completions", `{"model":"`+vm.Served+`","messages":[{"role":"user","content":"revoked"}],"max_tokens":32}`)
	if status < 400 {
		t.Fatal("revoked broker credential continued serving")
	}
	mu.Lock()
	for _, pin := range pins[before:] {
		if pin != "row-a" {
			mu.Unlock()
			t.Fatal("revoked pin escaped to a sibling during retries")
		}
	}
	mu.Unlock()
	if err = server.neoPool.SyncSource(ctx, source.ID); err != nil {
		t.Fatal(err)
	}
	status, _, _ = request("POST", "/v1/chat/completions", `{"model":"`+vm.Served+`","messages":[{"role":"user","content":"after logout reconciliation"}],"max_tokens":32}`)
	if status < 400 {
		t.Fatal("logged-out account remained in the selectable pool")
	}
}
