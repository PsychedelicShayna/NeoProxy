package registry

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/tidwall/gjson"
)

// The Go resolver for rows NeoPi has no bundled reference for must equal
// NeoPi's own discovery + buildModel over the same probe listing
// (testdata/gemini_npi_profiles.json, from capture_gemini_npi_profiles.ts).
func TestGoogleDiscoveredModelMatchesNeoPi(t *testing.T) {
	data, err := os.ReadFile("../runtime/executor/testdata/gemini_npi_profiles.json")
	if err != nil {
		t.Fatal(err)
	}
	probe, err := os.ReadFile("../runtime/executor/testdata/gemini_models_probe.json")
	if err != nil {
		t.Fatal(err)
	}
	var want struct {
		Discovered []struct {
			NeoPiModel
			GoogleCompat *NeoPiGoogleCompat `json:"googleCompat"`
		} `json:"discovered"`
	}
	if err = json.Unmarshal(data, &want); err != nil {
		t.Fatal(err)
	}
	got := map[string]NeoPiModel{}
	for _, item := range gjson.GetBytes(probe, "models").Array() {
		if model, ok := GoogleDiscoveredModel(item); ok {
			got[model.ID] = model
		}
	}
	if len(got) != len(want.Discovered) {
		t.Fatalf("discovered %d rows, NeoPi %d", len(got), len(want.Discovered))
	}
	for _, expected := range want.Discovered {
		actual, ok := got[expected.ID]
		if !ok {
			t.Errorf("%s: not discovered", expected.ID)
			continue
		}
		if actual.Name != expected.Name || actual.Reasoning != expected.Reasoning || !reflect.DeepEqual(actual.Input, expected.Input) ||
			actual.ContextWindow != expected.ContextWindow || actual.MaxTokens != expected.MaxTokens {
			t.Errorf("%s: row = %+v, want %+v", expected.ID, actual, expected.NeoPiModel)
		}
		if !reflect.DeepEqual(actual.Thinking, expected.Thinking) {
			t.Errorf("%s: thinking = %+v, want %+v", expected.ID, actual.Thinking, expected.Thinking)
		}
		if !reflect.DeepEqual(actual.GoogleCompat, expected.GoogleCompat) {
			t.Errorf("%s: compat = %+v, want %+v", expected.ID, actual.GoogleCompat, expected.GoogleCompat)
		}
	}
}
