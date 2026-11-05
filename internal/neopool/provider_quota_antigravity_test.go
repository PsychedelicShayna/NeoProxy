package neopool

import (
	"encoding/json"
	"os"
	"testing"

	antigravityauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/antigravity"
)

// TestAntigravityUsageWindows projects NeoPi's Antigravity reports, built from
// the responses NeoPi's usage capture was taken against, onto the windows
// routing gates on.
func TestAntigravityUsageWindows(t *testing.T) {
	data, err := os.ReadFile("../auth/antigravity/testdata/neopi_antigravity_usage_capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Fixtures []struct {
			Name   string          `json:"name"`
			Report json.RawMessage `json:"report"`
		} `json:"fixtures"`
	}
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	type window struct {
		used     float64
		resetsAt int64
	}
	want := map[string]map[string]window{
		// Per-counter limits collapse to the most consumed window of each kind;
		// the shared third-party bucket drives both 5h and week.
		"grouped-buckets": {"5h": {0.95, 1_800_003_600_000}, "day": {0, 0}, "week": {1, 1_800_259_200_000}},
		// The legacy fallback's unlabelled and unknown windows are not projected.
		"flat-buckets": {"5h": {0.75, 1_800_001_800_000}, "week": {0.6, 1_800_172_800_000}},
	}
	for _, fixture := range capture.Fixtures {
		expected, ok := want[fixture.Name]
		if !ok {
			continue
		}
		t.Run(fixture.Name, func(t *testing.T) {
			var usage antigravityauth.Usage
			if errDecode := json.Unmarshal(fixture.Report, &usage); errDecode != nil {
				t.Fatal(errDecode)
			}
			windows := antigravityUsageWindows(&usage)
			if len(windows) != len(expected) {
				t.Fatalf("windows = %+v, want kinds %v", windows, expected)
			}
			for _, w := range windows {
				exp, ok := expected[w.Kind]
				if !ok || !w.Known || w.Used != exp.used || w.ResetsAt != exp.resetsAt {
					t.Errorf("%s window = %+v, want used %v resets %d", w.Kind, w, exp.used, exp.resetsAt)
				}
			}
		})
	}
}
