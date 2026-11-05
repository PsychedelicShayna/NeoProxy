package registry

import (
	"compress/gzip"
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

type openRouterDiscoveryCapture struct {
	Resolved []NeoPiModel    `json:"resolved"`
	Roster   json.RawMessage `json:"roster"`
}

func loadOpenRouterDiscoveryCapture(t *testing.T) openRouterDiscoveryCapture {
	t.Helper()
	file, err := os.Open("../runtime/executor/testdata/openrouter_npi_discovery.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	var capture openRouterDiscoveryCapture
	if err = json.NewDecoder(reader).Decode(&capture); err != nil {
		t.Fatal(err)
	}
	return capture
}

// openRouterKnownDivergences lists the fields this port cannot reproduce
// without NeoPi's compiled class-rule tree and models.dev catalog metrics:
// per-family effort ladders and mandatory-reasoning flags for rows whose live
// discovery omits `supported_efforts`, the reasoning-capability upgrade Kimi
// rows get from the axes, and the catalog-metric context windows codex rows
// carry. Every entry is metadata the request builder does not read; the wire
// body and headers for those ids still match the captures.
var openRouterKnownDivergences = map[string]map[string]bool{
	"deepseek/deepseek-v3.2":             {"thinking": true},
	"moonshotai/kimi-k2":                 {"compat": true},
	"moonshotai/kimi-k2-0905":            {"compat": true},
	"moonshotai/kimi-k3.1-pro":           {"thinking": true},
	"openai/gpt-5.1-codex":               {"contextWindow": true},
	"openai/gpt-5.1-codex-max":           {"contextWindow": true},
	"openai/gpt-5.1-codex-mini":          {"contextWindow": true},
	"openai/gpt-5.2-codex":               {"contextWindow": true},
	"openai/gpt-5.3-codex":               {"contextWindow": true},
	"qwen/qwen3-235b-a22b-thinking-2507": {"thinking": true},
}

// TestOpenRouterDiscoveredMatchesNeoPi runs NeoPi's own model manager (fetch,
// merge with the bundle, buildModel) over a recorded live /models roster and
// pins the registry rows NeoProxy derives from the same roster.
func TestOpenRouterDiscoveredMatchesNeoPi(t *testing.T) {
	capture := loadOpenRouterDiscoveryCapture(t)
	infos, err := OpenRouterDiscovered(capture.Roster, "openrouter")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*NeoPiModel{}
	for _, info := range infos {
		got[info.ID] = info.NeoPi
	}
	if len(got) != len(capture.Resolved) {
		t.Errorf("rows: got %d, NeoPi resolved %d", len(got), len(capture.Resolved))
	}
	mismatches := 0
	for i := range capture.Resolved {
		want := &capture.Resolved[i]
		row := got[want.ID]
		if row == nil {
			t.Errorf("%s: missing from the registry roster", want.ID)
			continue
		}
		check := func(field string, have, expected any) {
			if openRouterKnownDivergences[want.ID][field] {
				return
			}
			if !reflect.DeepEqual(have, expected) {
				mismatches++
				if mismatches <= 40 {
					t.Errorf("%s %s: got %v, NeoPi %v", want.ID, field, have, expected)
				}
			}
		}
		check("name", row.Name, want.Name)
		check("reasoning", row.Reasoning, want.Reasoning)
		check("input", row.Input, want.Input)
		check("contextWindow", row.ContextWindow, want.ContextWindow)
		check("maxTokens", row.MaxTokens, want.MaxTokens)
		check("thinking", row.Thinking, want.Thinking)
		check("compat", row.Compat, want.Compat)
	}
}
