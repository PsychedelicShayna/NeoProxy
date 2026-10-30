package cliproxy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestNeoPiSourceExecutorSurvivesConfigReload(t *testing.T) {
	const provider = "neopi-reload-source-zai"
	const model = "zai/glm-source-fixture"
	const bearer = "test-only-gateway-bearer"
	var requests atomic.Int32
	var revoked atomic.Bool
	var expectedTemperature atomic.Int64
	expectedTemperature.Store(2)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+bearer || r.Header.Get("X-Neopi-Credential-Id") != "38" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"message":"missing current source authorization"}}`)
			return
		}
		var body struct {
			Model       string  `json:"model"`
			Temperature float64 `json:"temperature"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Model != model || body.Temperature != float64(expectedTemperature.Load())/10 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"current payload policy not applied"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"source-reload","object":"chat.completion","created":1,"model":"`+model+`","choices":[{"index":0,"message":{"role":"assistant","content":"authorized source reply"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
	}))
	defer gateway.Close()
	configuration := func(temperature float64) *config.Config {
		return &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{
			Models: []config.PayloadModelRule{{Name: model, Protocol: "openai"}},
			Params: map[string]any{"temperature": temperature},
		}}}}
	}
	service := &Service{cfg: configuration(.2), coreManager: coreauth.NewManager(nil, nil, nil)}
	auth := &coreauth.Auth{
		ID: "neopi:source-reload:38", Provider: provider,
		Attributes: map[string]string{"neopi_source_id": "source-reload", "neopi_credential_id": "38", "base_url": gateway.URL + "/v1"},
	}
	service.coreManager.RegisterExecutor(runtimeexecutor.NewNeoPiExecutor(provider, service.cfg, func(_ context.Context, selected *coreauth.Auth, _ string) (*coreauth.Auth, error) {
		if revoked.Load() {
			return nil, errors.New("source authority revoked")
		}
		prepared := selected.Clone()
		prepared.Attributes["api_key"] = bearer
		return prepared, nil
	}))
	payload := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"source reload fixture"}],"temperature":0.9,"max_tokens":4}`)
	request := coreexecutor.Request{Model: model, Payload: payload}
	options := coreexecutor.Options{SourceFormat: sdktranslator.Format("openai"), Metadata: map[string]any{coreexecutor.PinnedAuthMetadataKey: auth.ID}}
	execute := func() error {
		selected, exists := service.coreManager.Executor(provider)
		if !exists {
			return errors.New("source executor missing")
		}
		response, err := selected.Execute(t.Context(), auth, request, options)
		if err == nil && gjson.GetBytes(response.Payload, "choices.0.message.content").String() != "authorized source reply" {
			return errors.New("source response was not translated")
		}
		return err
	}
	if err := execute(); err != nil {
		t.Fatalf("initial source request failed: %v", err)
	}

	service.cfgMu.Lock()
	service.cfg = configuration(.7)
	service.cfgMu.Unlock()
	expectedTemperature.Store(7)
	service.registerExecutorForAuth(auth, true)
	if err := execute(); err != nil {
		t.Fatalf("source request after config reload failed: %v", err)
	}
	if requests.Load() != 2 {
		t.Fatalf("upstream requests = %d, want two authorized requests", requests.Load())
	}

	revoked.Store(true)
	err := execute()
	var status interface{ StatusCode() int }
	if !errors.As(err, &status) || status.StatusCode() != http.StatusForbidden {
		t.Fatalf("revoked source after reload: %v, want HTTP 403", err)
	}
	if requests.Load() != 2 {
		t.Fatal("revoked source authority reached the gateway after reload")
	}
}
