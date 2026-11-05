package registry

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/tidwall/gjson"
)

// The discovery rows the capture script fed NeoPi's extension map to the
// models NeoPi's catalog build resolves.
func TestNousModelsMatchNeoPiCapture(t *testing.T) {
	data, err := os.ReadFile("../auth/nous/testdata/neopi_nous_auth_capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Scenarios []struct {
			Name     string `json:"name"`
			Requests []struct {
				Body string `json:"body"`
			} `json:"requests"`
			Resolved []struct {
				NeoPiModel
				Compat *NeoPiCompletionsCompat `json:"compat"`
			} `json:"resolved"`
		} `json:"scenarios"`
	}
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range capture.Scenarios {
		if scenario.Name != "discovery-live" {
			continue
		}
		upstream := gjson.GetBytes(data, `scenarios.#(name=="discovery-live").rows`)
		if !upstream.IsArray() {
			t.Fatal("capture has no discovery rows")
		}
		var rows []json.RawMessage
		for _, row := range upstream.Array() {
			rows = append(rows, json.RawMessage(row.Raw))
		}
		got := NousModels(rows, "nous-portal")
		if len(got) != len(scenario.Resolved) {
			t.Fatalf("models = %d, want %d", len(got), len(scenario.Resolved))
		}
		for i, want := range scenario.Resolved {
			info := got[i]
			if info.ID != want.ID {
				t.Fatalf("model %d = %s, want %s", i, info.ID, want.ID)
			}
			model := info.NeoPi
			if model.Name != want.Name || model.API != want.API || model.Reasoning != want.Reasoning || model.ContextWindow != want.ContextWindow || model.MaxTokens != want.MaxTokens {
				t.Errorf("%s: got %+v, want %+v", want.ID, *model, want.NeoPiModel)
			}
			if !reflect.DeepEqual(model.Input, want.Input) {
				t.Errorf("%s: input = %v, want %v", want.ID, model.Input, want.Input)
			}
			if !reflect.DeepEqual(model.Thinking, want.Thinking) {
				t.Errorf("%s: thinking = %+v, want %+v", want.ID, model.Thinking, want.Thinking)
			}
			if !reflect.DeepEqual(model.CompletionsCompat, want.Compat) {
				gotJSON, _ := json.Marshal(model.CompletionsCompat)
				wantJSON, _ := json.Marshal(want.Compat)
				t.Errorf("%s: compat\n got %s\nwant %s", want.ID, gotJSON, wantJSON)
			}
		}
		return
	}
	t.Fatal("capture has no discovery-live scenario")
}

func TestNousBundledRosterIsNeoPiSnapshot(t *testing.T) {
	models := NousBundledModels("nous-portal")
	if len(models) < 200 {
		t.Fatalf("bundled roster has %d models", len(models))
	}
	for _, info := range models {
		if info.NeoPi == nil || info.NeoPi.CompletionsCompat == nil || info.NeoPi.API != "openai-completions" {
			t.Fatalf("%s: incomplete NeoPi record %+v", info.ID, info.NeoPi)
		}
	}
	if NousBundledNeoPiModel("deepseek/deepseek-v4-pro") == nil {
		t.Fatal("snapshot model not resolvable")
	}
}
