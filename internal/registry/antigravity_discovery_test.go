package registry

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"sort"
	"testing"
)

// TestAntigravityBundledMatchesNeoPi requires NeoPi's bundled google-antigravity
// seed (capture_antigravity_npi_models.ts) to decode to the same records.
func TestAntigravityBundledMatchesNeoPi(t *testing.T) {
	data, err := os.ReadFile("models/antigravity_neopi.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Models []json.RawMessage `json:"models"`
	}
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	want := map[string]map[string]any{}
	for _, raw := range capture.Models {
		record := antigravityCaptureRecord(t, raw)
		if kind, _ := record["kind"].(string); kind != "" && kind != "chat" {
			continue
		}
		want[record["id"].(string)] = record
	}
	got := map[string]map[string]any{}
	for _, info := range AntigravityBundledModels() {
		if info.Type == OpenAIImageModelType {
			continue // covered by TestAntigravityBundledImageModels
		}
		got[info.ID] = antigravityRecordMap(t, info.NeoPi)
	}
	antigravityCompareRecords(t, want, got)
}

// TestAntigravityDiscoveryMatchesNeoPi replays every scripted roster through
// the port and requires NeoPi's own model-manager output
// (capture_antigravity_npi_discovery.ts) row for row.
func TestAntigravityDiscoveryMatchesNeoPi(t *testing.T) {
	compressed, err := os.Open("testdata/antigravity_npi_discovery.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = compressed.Close() }()
	reader, err := gzip.NewReader(compressed)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Scenarios []struct {
			Name     string            `json:"name"`
			Endpoint string            `json:"endpoint"`
			Payload  json.RawMessage   `json:"payload"`
			Models   []json.RawMessage `json:"models"`
		} `json:"scenarios"`
	}
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	if len(capture.Scenarios) == 0 {
		t.Fatal("capture has no scenarios")
	}
	for _, scenario := range capture.Scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			want := make([]map[string]any, 0, len(scenario.Models))
			for _, raw := range scenario.Models {
				record := antigravityCaptureRecord(t, raw)
				if kind, _ := record["kind"].(string); kind != "" && kind != "chat" {
					continue
				}
				want = append(want, record)
			}
			models, err := antigravityResolvedRoster(scenario.Payload, scenario.Endpoint)
			if err != nil {
				t.Fatal(err)
			}
			got := make([]map[string]any, 0, len(models))
			for i := range models {
				got = append(got, antigravityRecordMap(t, antigravityRecord(&models[i])))
			}
			if len(got) != len(want) {
				t.Fatalf("discovered %d chat models, want %d", len(got), len(want))
			}
			for i := range want {
				if !reflect.DeepEqual(got[i], want[i]) {
					antigravityDiffRecord(t, i, want[i], got[i])
					t.FailNow()
				}
			}
		})
	}
}

// antigravityDiffRecord reports the fields that differ between two records.
func antigravityDiffRecord(t *testing.T, index int, want, got map[string]any) {
	t.Helper()
	keys := make([]string, 0, len(want)+len(got))
	seen := map[string]bool{}
	for _, record := range []map[string]any{want, got} {
		for key := range record {
			if !seen[key] {
				seen[key] = true
				keys = append(keys, key)
			}
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		if reflect.DeepEqual(want[key], got[key]) {
			continue
		}
		wantJSON, _ := json.Marshal(want[key])
		gotJSON, _ := json.Marshal(got[key])
		t.Errorf("model %d (%v) %s:\n got %s\nwant %s", index, want["id"], key, gotJSON, wantJSON)
	}
}

func antigravityCaptureRecord(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var record map[string]any
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	return record
}

func antigravityRecordMap(t *testing.T, model *NeoPiModel) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(model)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err = json.Unmarshal(encoded, &record); err != nil {
		t.Fatal(err)
	}
	return record
}

func antigravityCompareRecords(t *testing.T, want, got map[string]map[string]any) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("decoded %d models, want %d", len(got), len(want))
	}
	mismatches := 0
	for id, record := range want {
		if !reflect.DeepEqual(got[id], record) {
			if mismatches++; mismatches <= 10 {
				gotJSON, _ := json.Marshal(got[id])
				wantJSON, _ := json.Marshal(record)
				t.Errorf("%s:\n got %s\nwant %s", id, gotJSON, wantJSON)
			}
		}
	}
	for id := range got {
		if _, ok := want[id]; !ok && mismatches <= 10 {
			t.Errorf("unexpected model %s", id)
		}
	}
}
