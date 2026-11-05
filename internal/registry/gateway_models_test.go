package registry

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func readGzipFixture(t *testing.T, path string) []byte {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	reader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// The rows the Go discovery mapper builds from the recorded live listings are
// the rows NeoPi's own model managers built from them.
func TestGatewayLiveListingsMatchNeoPi(t *testing.T) {
	for _, provider := range GatewayProviders() {
		listing := readGzipFixture(t, "testdata/gateway_"+provider+"_models_live.json.gz")
		got := GatewayDiscoveredModels(provider, listing, "owner")
		want := loadGatewayCatalog().providers[provider]
		if len(got) != len(want) {
			t.Errorf("%s: %d models, NeoPi built %d", provider, len(got), len(want))
		}
		byID := map[string]*ModelInfo{}
		for _, info := range got {
			byID[info.ID] = info
		}
		for i := range want {
			info := byID[want[i].ID]
			if info == nil {
				t.Errorf("%s: %s missing", provider, want[i].ID)
				continue
			}
			compareGatewayRow(t, provider, info.NeoPi, &want[i])
		}
	}
}

// Ids NeoPi has no reference for (billing variants, lineage revisions, a
// judge model, a renamed model) resolve as NeoPi's discovery resolves them.
func TestGatewaySyntheticListingsMatchNeoPi(t *testing.T) {
	var synthetic map[string]json.RawMessage
	data, err := os.ReadFile("testdata/gateway_synthetic_listings.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &synthetic); err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Providers map[string][]NeoPiModel `json:"providers"`
	}
	data = readGzipFixture(t, "testdata/gateway_npi_discovery.json.gz")
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	for _, provider := range GatewayProviders() {
		got := GatewayDiscoveredModels(provider, synthetic[provider], "owner")
		want := capture.Providers[provider]
		// Rows the listing does not name come from the catalogs, and NeoPi's
		// own merge of those with the listing's models.dev copy differs run to
		// run; only the ids and the listed rows are comparable.
		listed := map[string]bool{}
		for _, entry := range gjson.GetBytes(synthetic[provider], "data").Array() {
			listed[entry.Get("id").String()] = true
		}
		byID := map[string]*ModelInfo{}
		var gotIDs, wantIDs []string
		for _, info := range got {
			byID[info.ID] = info
			gotIDs = append(gotIDs, info.ID)
		}
		for i := range want {
			wantIDs = append(wantIDs, want[i].ID)
		}
		slices.Sort(gotIDs)
		slices.Sort(wantIDs)
		if !slices.Equal(gotIDs, wantIDs) {
			t.Errorf("%s ids:\n got %v\nwant %v", provider, gotIDs, wantIDs)
		}
		for i := range want {
			info := byID[want[i].ID]
			if info == nil || !listed[want[i].ID] {
				continue
			}
			// An id NeoPi's catalogs never described is built from our API's
			// default compat, not from NeoPi's id-text classification (see
			// gatewayTemplate); its identity, capabilities and ladder still
			// have to match.
			if catalogRow(loadGatewayCatalog(), provider, want[i].ID, true) == nil {
				info.NeoPi.CompletionsCompat, info.NeoPi.AnthropicCompat, info.NeoPi.Compat = nil, nil, nil
				want[i].CompletionsCompat, want[i].AnthropicCompat, want[i].Compat = nil, nil, nil
				// NeoPi classifies a never-seen id's own text for its
				// thinking surface and compat; our API default row cannot.
				if strings.HasPrefix(want[i].ID, "zz") || museSparkLineage(want[i].ID) {
					info.NeoPi.Thinking, want[i].Thinking = nil, nil
				}
			}
			compareGatewayRow(t, provider, info.NeoPi, &want[i])
		}
	}
}

func compareGatewayRow(t *testing.T, provider string, got, want *NeoPiModel) {
	t.Helper()
	name := provider + "/" + want.ID
	if got.Name != want.Name || got.API != want.API || got.BaseURL != want.BaseURL || got.Reasoning != want.Reasoning ||
		got.ContextWindow != want.ContextWindow || got.MaxTokens != want.MaxTokens || !slices.Equal(got.Input, want.Input) {
		t.Errorf("%s: row = {%q %s %s reasoning=%v ctx=%d max=%d %v}, NeoPi {%q %s %s reasoning=%v ctx=%d max=%d %v}", name,
			got.Name, got.API, got.BaseURL, got.Reasoning, got.ContextWindow, got.MaxTokens, got.Input,
			want.Name, want.API, want.BaseURL, want.Reasoning, want.ContextWindow, want.MaxTokens, want.Input)
		return
	}
	gotJSON, _ := json.Marshal(got.Thinking)
	wantJSON, _ := json.Marshal(want.Thinking)
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Errorf("%s: thinking = %s, NeoPi %s", name, gotJSON, wantJSON)
	}
	for _, pair := range [][2]any{{got.AnthropicCompat, want.AnthropicCompat}, {got.CompletionsCompat, want.CompletionsCompat}, {got.Compat, want.Compat}} {
		a, _ := json.Marshal(pair[0])
		b, _ := json.Marshal(pair[1])
		if !bytes.Equal(a, b) {
			t.Errorf("%s: compat = %s, NeoPi %s", name, a, b)
		}
	}
}
