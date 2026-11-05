package registry

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// The rows the capture script fed NeoPi's Marineris extension map to the
// models NeoPi's catalog build resolves.
func TestMarinerisModelsMatchNeoPiCapture(t *testing.T) {
	data, err := os.ReadFile("../auth/marineris/testdata/neopi_marineris_auth_capture.json")
	if err != nil {
		t.Fatal(err)
	}
	type builtModel struct {
		Built struct {
			ID            string          `json:"id"`
			Name          string          `json:"name"`
			API           string          `json:"api"`
			Reasoning     bool            `json:"reasoning"`
			Input         []string        `json:"input"`
			ContextWindow int             `json:"contextWindow"`
			MaxTokens     int             `json:"maxTokens"`
			Thinking      *NeoPiThinking  `json:"thinking"`
			Compat        json.RawMessage `json:"compat"`
		} `json:"built"`
	}
	var capture struct {
		Rows      []json.RawMessage `json:"rows"`
		Scenarios []struct {
			Name          string          `json:"name"`
			Result        json.RawMessage `json:"result"`
			Registrations []struct {
				BaseURL string `json:"baseUrl"`
			} `json:"registrations"`
		} `json:"scenarios"`
	}
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, scenario := range capture.Scenarios {
		if scenario.Name != "discovery-live" && scenario.Name != "discovery-public-gateway" {
			continue
		}
		checked++
		var want []builtModel
		if err = json.Unmarshal(scenario.Result, &want); err != nil {
			t.Fatal(err)
		}
		got := MarinerisModels(capture.Rows, scenario.Registrations[0].BaseURL, "marineris")
		if len(got) != len(want) {
			t.Fatalf("models = %d, want %d", len(got), len(want))
		}
		for i, expected := range want {
			model := got[i].NeoPi
			if model.ID != expected.Built.ID || model.Name != expected.Built.Name || model.API != expected.Built.API || model.Reasoning != expected.Built.Reasoning ||
				model.ContextWindow != expected.Built.ContextWindow || model.MaxTokens != expected.Built.MaxTokens || !reflect.DeepEqual(model.Input, expected.Built.Input) {
				t.Errorf("%s: got %+v, want %+v", expected.Built.ID, *model, expected.Built)
			}
			if !reflect.DeepEqual(model.Thinking, expected.Built.Thinking) {
				t.Errorf("%s: thinking = %+v, want %+v", expected.Built.ID, model.Thinking, expected.Built.Thinking)
			}
			var wantCompat any
			if model.API == "openai-responses" {
				var compat NeoPiCompat
				_ = json.Unmarshal(expected.Built.Compat, &compat)
				wantCompat = &compat
				if !reflect.DeepEqual(model.Compat, wantCompat) {
					t.Errorf("%s: responses compat differs", expected.Built.ID)
				}
			} else {
				var compat NeoPiCompletionsCompat
				_ = json.Unmarshal(expected.Built.Compat, &compat)
				if !reflect.DeepEqual(model.CompletionsCompat, &compat) {
					t.Errorf("%s: completions compat differs", expected.Built.ID)
				}
			}
		}
	}
	if checked != 2 {
		t.Fatalf("checked %d discovery scenarios, want 2", checked)
	}
}
