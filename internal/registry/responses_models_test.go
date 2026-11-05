package registry

import (
	"encoding/json"
	"os"
	"testing"
)

type responsesProfilesFixture struct {
	Seeds     map[string][]NeoPiModel `json:"seeds"`
	Excluded  map[string]map[string]bool
	Discovery []struct {
		Provider  string `json:"provider"`
		Name      string `json:"name"`
		RosterRaw []struct {
			ID                  string `json:"id"`
			Name                string `json:"name"`
			ContextLength       int    `json:"context_length"`
			MaxCompletionTokens int    `json:"max_completion_tokens"`
		} `json:"rosterRaw"`
		Models []NeoPiModel `json:"models"`
	} `json:"discovery"`
}

func loadResponsesProfiles(t *testing.T) responsesProfilesFixture {
	t.Helper()
	raw, err := os.ReadFile("../runtime/executor/testdata/responses_npi_profiles.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture responsesProfilesFixture
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func marshalRow(t *testing.T, row NeoPiModel) string {
	t.Helper()
	raw, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// The embedded seeds are the rows NeoPi's catalog resolves.
func TestResponsesSeedsMatchNeoPi(t *testing.T) {
	fixture := loadResponsesProfiles(t)
	for provider, want := range fixture.Seeds {
		got := ResponsesSeed(provider)
		if len(got) != len(want) {
			t.Fatalf("%s: %d seeds, NeoPi %d", provider, len(got), len(want))
		}
		for i := range want {
			if marshalRow(t, got[i]) != marshalRow(t, want[i]) {
				t.Errorf("%s/%s seed differs", provider, want[i].ID)
			}
		}
	}
}

// Every scripted roster resolves to the rows NeoPi's own model manager returns.
func TestResponsesDiscoveryMatchesNeoPi(t *testing.T) {
	fixture := loadResponsesProfiles(t)
	if len(fixture.Discovery) == 0 {
		t.Fatal("no discovery scenarios")
	}
	for _, scenario := range fixture.Discovery {
		t.Run(scenario.Provider+"/"+scenario.Name, func(t *testing.T) {
			listed := []DiscoveredResponsesModel{}
			for _, entry := range scenario.RosterRaw {
				listed = append(listed, DiscoveredResponsesModel{ID: entry.ID, Name: entry.Name, ContextLength: entry.ContextLength, MaxCompletionTokens: entry.MaxCompletionTokens})
			}
			got := ResponsesNeoPiModels(scenario.Provider, listed)
			if len(got) != len(scenario.Models) {
				ids := func(rows []NeoPiModel) (out []string) {
					for _, row := range rows {
						out = append(out, row.ID)
					}
					return
				}
				t.Fatalf("got %v, NeoPi %v", ids(got), ids(scenario.Models))
			}
			for i := range got {
				if g, w := marshalRow(t, got[i]), marshalRow(t, scenario.Models[i]); g != w {
					t.Errorf("row %d (%s)\ngot  %s\nwant %s", i, scenario.Models[i].ID, g, w)
				}
			}
		})
	}
}

func TestResponsesExclusionMatchesNeoPi(t *testing.T) {
	fixture := loadResponsesProfiles(t)
	for provider, probes := range fixture.Excluded {
		for id, want := range probes {
			if got := IsResponsesExcluded(provider, id); got != want {
				t.Errorf("%s/%s excluded = %v, NeoPi %v", provider, id, got, want)
			}
		}
	}
}
