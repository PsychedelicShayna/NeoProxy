package auth

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// A compat account's discovered NeoPi record wins over any configured model
// list, and is found by its canonical id when the published id carries a
// routing prefix.
func TestResolvedModelInfoPrefersDiscoveredNeoPiRecord(t *testing.T) {
	auth := &Auth{ID: "discovered-neopi", Provider: "openai-compatible-marineris", Attributes: map[string]string{"compat_name": "marineris", "api_key": "k"}}
	record := &registry.NeoPiModel{ID: "grok-4.6", API: "openai-completions"}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{
		{ID: "tenant/grok-4.6", MetadataModelID: "grok-4.6", NeoPi: record},
		{ID: "no-record"},
	})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	for _, model := range []string{"grok-4.6", "tenant/grok-4.6"} {
		req := attachResolvedExecutionModelInfo(&apiKeyModelRoutingSnapshot{}, cliproxyexecutor.Request{Model: model}, auth, model, model, false)
		info, ok := ResolvedModelInfo(req)
		if !ok || info == nil || info.NeoPi == nil || info.NeoPi.ID != record.ID {
			t.Errorf("%s: resolved = %+v, %v; want the discovered NeoPi record", model, info, ok)
		}
	}
	req := attachResolvedExecutionModelInfo(&apiKeyModelRoutingSnapshot{}, cliproxyexecutor.Request{Model: "no-record"}, auth, "no-record", "no-record", false)
	if info, ok := ResolvedModelInfo(req); ok && info != nil && info.NeoPi != nil {
		t.Errorf("model without a record resolved %+v", info)
	}
}
