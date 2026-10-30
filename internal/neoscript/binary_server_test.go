//go:build linux && amd64

package neoscript

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopool"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neoroute"
)

// This opt-in check executes the actual cmd/server artifact, not a test main or
// an injected evaluator. All credentials and upstream answers are local fixtures;
// this is script wiring/protocol proof, not slice E's genuine provider inference.
func TestInstalledServerScriptSimulationAndInference(t *testing.T) {
	binary := os.Getenv("NEOPROXY_SCRIPT_TEST_SERVER")
	if binary == "" {
		t.Skip("set NEOPROXY_SCRIPT_TEST_SERVER to the built cmd/server artifact")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	scratch := t.TempDir()
	upstreamModels := make(chan string, 4)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") != "local-script-fixture-key" {
			t.Error("configured local account was not used")
			w.WriteHeader(401)
			return
		}
		upstreamModels <- request.Model
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"local-script-response","object":"chat.completion","created":1,"model":%q,"choices":[{"index":0,"message":{"role":"assistant","content":"local script hook reached %s"},"finish_reason":"stop"}],"usage":{"prompt_tokens":8,"completion_tokens":4,"total_tokens":12}}`, request.Model, request.Model)
	}))
	defer upstream.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err = listener.Close(); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf(`host: 127.0.0.1
port: %d
auth-dir: %q
remote-management:
  secret-key: local-script-operator-secret
  disable-control-panel: true
  disable-auto-update-panel: true
request-retry: 0
openai-compatibility:
  - name: script-local
    base-url: %q
    api-key-entries:
      - api-key: local-script-fixture-key
    models:
      - name: model-a
        alias: model-a
      - name: model-b
        alias: model-b
`, port, filepath.Join(scratch, "auth"), upstream.URL+"/v1")
	configPath := filepath.Join(scratch, "config.yaml")
	if err = os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	logFile, err := os.Create(filepath.Join(scratch, "server.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := logFile.Close(); err != nil {
			t.Error(err)
		}
	}()
	processContext, cancel := context.WithCancel(t.Context())
	command := exec.CommandContext(processContext, binary, "--config", configPath, "--local-model", "--no-browser")
	command.Dir = scratch
	command.Env = []string{"HOME=" + scratch, "PATH=/usr/bin:/bin", "NEOPROXY_DATA_DIR=" + filepath.Join(scratch, "data"), "GOMAXPROCS=2", "GOTRACEBACK=none"}
	command.Stdout = logFile
	command.Stderr = logFile
	if err = command.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	defer func() {
		cancel()
		_ = command.Wait()
		if t.Failed() {
			data, err := os.ReadFile(logFile.Name())
			if err == nil {
				t.Logf("isolated server log: %s", data)
			}
		}
	}()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, Timeout: 5 * time.Second}
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	readyContext, readyCancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer readyCancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		request, err := http.NewRequestWithContext(readyContext, "GET", base+"/v8/session", nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(request)
		if err == nil {
			response.Body.Close()
			if response.StatusCode == 401 {
				break
			}
		}
		select {
		case <-readyContext.Done():
			t.Fatal("isolated server did not expose its session API")
		case <-ticker.C:
		}
	}
	call := func(method, path, bearer string, body any, want int) []byte {
		t.Helper()
		var input io.Reader
		if body != nil {
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			input = bytes.NewReader(raw)
		}
		request, err := http.NewRequestWithContext(t.Context(), method, base+path, input)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Neoproxy-CSRF", "1")
		if bearer != "" {
			request.Header.Set("Authorization", "Bearer "+bearer)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %s", method, path, response.StatusCode, want, data)
		}
		return data
	}
	var admin struct {
		UserID string `json:"userId"`
	}
	if err = json.Unmarshal(call("POST", "/v8/session/bootstrap", "local-script-operator-secret", map[string]any{"handle": "script-admin", "password": "local-script-password"}, 201), &admin); err != nil {
		t.Fatal(err)
	}
	var inventory struct {
		Accounts []neopool.Account `json:"accounts"`
	}
	if err = json.Unmarshal(call("GET", "/v8/neo/vault", "", nil, 200), &inventory); err != nil {
		t.Fatal(err)
	}
	if len(inventory.Accounts) != 1 {
		t.Fatalf("local configured account inventory = %+v", inventory.Accounts)
	}
	account := inventory.Accounts[0]
	call("PATCH", "/v8/neo/vault/accounts/"+account.ID, "", map[string]any{"ownerId": admin.UserID}, 204)
	call("PUT", "/v8/neo/vault/accounts/"+account.ID+"/windows/day", "", map[string]any{"capacityTokens": 1000000, "startsAt": time.Now().Add(-time.Hour).UnixMilli()}, 204)
	vm := neoroute.VirtualModel{Name: "scripted", Stages: []neoroute.Stage{}, Terminal: neoroute.Terminal{Kind: "limit", Message: "script fixture exhausted"}}
	for _, suffix := range []string{"a", "b"} {
		vm.Stages = append(vm.Stages, neoroute.Stage{ID: "stage-" + suffix, Model: account.ProviderID + "/model-" + suffix, Accounts: neoroute.AccountScope{All: true}, Draw: "fill", PerAccount: 1, Cap: 1, Window: "day", Per: "user"})
	}
	vm.Script = `assert(input.headers==nil and input.authorization==nil and input.cookies==nil)
 local text
 if input.protocol=="openai-response" then text=input.request.input
 else text=input.request.messages[1].content end
 local index=1
 if text=="choose-b" then index=2 end
 local stage=input.stages[index]
 assert(input.accounts[1].state=="healthy")
 return {stageId=stage.stageId,accountId=stage.candidates[1].accountId}`
	if err = json.Unmarshal(call("POST", "/v8/neo/virtual-models", "", vm, 201), &vm); err != nil {
		t.Fatal(err)
	}
	var member neo.User
	if err = json.Unmarshal(call("POST", "/v8/neo/users", "", neo.UserInput{Handle: "script-member", Role: neo.RoleMember, State: "active", Password: "local-script-password", Grants: []neo.Grant{{VirtualModel: vm.ID, Allotment: 1}}}, 201), &member); err != nil {
		t.Fatal(err)
	}
	var issued struct {
		Secret string `json:"secret"`
	}
	if err = json.Unmarshal(call("POST", "/v8/neo/users/"+member.ID+"/keys", "", map[string]any{"label": "script fixture"}, 201), &issued); err != nil {
		t.Fatal(err)
	}
	if issued.Secret == "" {
		t.Fatal("inference key was not issued")
	}
	for _, protocol := range []struct{ name, path string }{
		{"openai", "/v1/chat/completions"},
		{"openai-response", "/v1/responses"},
		{"claude", "/v1/messages"},
	} {
		for _, suffix := range []string{"a", "b"} {
			body := map[string]any{"model": vm.Served, "messages": []any{map[string]any{"role": "user", "content": "choose-" + suffix}}, "max_tokens": 32}
			if protocol.name == "openai-response" {
				body = map[string]any{"model": vm.Served, "input": "choose-" + suffix, "max_output_tokens": 32}
			}
			var decision neoroute.Decision
			if err = json.Unmarshal(call("POST", "/v8/neo/simulate", "", map[string]any{"asked": vm.Served, "userId": member.ID, "protocol": protocol.name, "request": body}, 200), &decision); err != nil {
				t.Fatal(err)
			}
			if decision.StageID != "stage-"+suffix || decision.AccountID != account.ID {
				t.Fatalf("installed %s evaluator simulation ignored request: %+v", protocol.name, decision)
			}
			data := call("POST", protocol.path, issued.Secret, body, 200)
			if !strings.Contains(string(data), "local script hook reached model-"+suffix) {
				t.Fatalf("installed %s inference did not execute selected model: %s", protocol.name, data)
			}
			select {
			case model := <-upstreamModels:
				if model != "model-"+suffix {
					t.Fatalf("upstream model=%s, want model-%s", model, suffix)
				}
			default:
				t.Fatal("inference returned without actual local upstream dispatch")
			}
		}
	}
	// Exercise numeric/boolean functions through the same installed executable
	// and all consumer protocols, not only the evaluator's typed output.
	for i := range vm.Stages {
		vm.Stages[i].Lua = &neoroute.LuaPolicy{Eligible: "allow_stage", PerAccount: "account_share", Cap: "stage_share"}
	}
	vm.Script = `
function allow_stage(ctx)
  local text = ctx.request.input
  if ctx.protocol ~= "openai-response" then text = ctx.request.messages[1].content end
  return (ctx.stage.stageId == "stage-a" and text == "choose-a") or
         (ctx.stage.stageId == "stage-b" and text == "choose-b")
end
function account_share(ctx)
  local maximum = ctx.request.max_output_tokens or ctx.request.max_tokens
  return maximum > 1000 and 0.0000001 or 0.01
end
function stage_share(ctx) return 0.02 end`
	call("PUT", "/v8/neo/virtual-models/"+vm.ID, "", vm, 200)
	for _, protocol := range []struct{ name, path string }{
		{"openai", "/v1/chat/completions"},
		{"openai-response", "/v1/responses"},
		{"claude", "/v1/messages"},
	} {
		for _, suffix := range []string{"a", "b"} {
			body := map[string]any{"model": vm.Served, "messages": []any{map[string]any{"role": "user", "content": "choose-" + suffix}}, "max_tokens": 32}
			if protocol.name == "openai-response" {
				body = map[string]any{"model": vm.Served, "input": "choose-" + suffix, "max_output_tokens": 32}
			}
			var decision neoroute.Decision
			if err = json.Unmarshal(call("POST", "/v8/neo/simulate", "", map[string]any{"asked": vm.Served, "userId": member.ID, "protocol": protocol.name, "request": body}, 200), &decision); err != nil {
				t.Fatal(err)
			}
			step := decision.Trail[len(decision.Trail)-1]
			if decision.StageID != "stage-"+suffix || step.Policy == nil || step.Cap != 0.02 || len(step.Candidates) != 1 || step.Candidates[0].Budget != 0.01 {
				t.Fatalf("installed %s typed policy simulation diverged: %+v", protocol.name, decision)
			}
			data := call("POST", protocol.path, issued.Secret, body, 200)
			if !strings.Contains(string(data), "local script hook reached model-"+suffix) {
				t.Fatalf("installed %s callback inference chose another model: %s", protocol.name, data)
			}
			select {
			case model := <-upstreamModels:
				if model != "model-"+suffix {
					t.Fatalf("callback dispatched %s, want model-%s", model, suffix)
				}
			default:
				t.Fatal("callback inference returned without a local upstream dispatch")
			}
		}
	}
	call("POST", "/v1/responses", issued.Secret, map[string]any{"model": vm.Served, "input": "choose-b", "max_output_tokens": 2048}, 429)
	call("POST", "/v1/responses", issued.Secret, map[string]any{"model": vm.Served, "input": "condition-false", "max_output_tokens": 32}, 429)
	vm.Script += "\nfunction account_share(ctx) return true end"
	call("PUT", "/v8/neo/virtual-models/"+vm.ID, "", vm, 200)
	dataTyped := call("POST", "/v1/responses", issued.Secret, map[string]any{"model": vm.Served, "input": "choose-b", "max_output_tokens": 32}, 422)
	if !strings.Contains(string(dataTyped), "finite number") {
		t.Fatalf("installed callback type error was hidden: %s", dataTyped)
	}
	select {
	case model := <-upstreamModels:
		t.Fatalf("rejected boolean/numeric policy silently dispatched %s", model)
	default:
	}
	// A runtime failure must stay visible through inference, never execute the
	// ordinary first stage. This also proves the running server survived workers.
	vm.Script = `error("installed script policy failed")`
	call("PUT", "/v8/neo/virtual-models/"+vm.ID, "", vm, 200)
	data := call("POST", "/v1/chat/completions", issued.Secret, map[string]any{"model": vm.Served, "messages": []any{map[string]any{"role": "user", "content": "choose-b"}}, "max_tokens": 32}, 422)
	if !strings.Contains(string(data), "installed script policy failed") {
		t.Fatalf("inference hid runtime diagnostic: %s", data)
	}
	select {
	case model := <-upstreamModels:
		t.Fatalf("failed script silently dispatched %s", model)
	default:
	}
}
