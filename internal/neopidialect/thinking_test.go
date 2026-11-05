package neopidialect

import (
	"encoding/json"
	"os"
	"testing"
)

type healingCapture struct {
	CaseCount int            `json:"caseCount"`
	Cases     []healingCase  `json:"cases"`
	Strippers []stripperCase `json:"strippers"`
	Demotions []demotionCase `json:"demotions"`
}

type healingCase struct {
	Name   string        `json:"name"`
	Chunks []string      `json:"chunks"`
	Feeds  [][]HealEvent `json:"feeds"`
	Flush  []HealEvent   `json:"flush"`
}

type stripperCase struct {
	Name   string   `json:"name"`
	Chunks []string `json:"chunks"`
	Pushes []string `json:"pushes"`
	Flush  string   `json:"flush"`
}

type demotionCase struct {
	Model  string `json:"model"`
	Text   string `json:"text"`
	Output string `json:"output"`
}

func loadThinkingCapture(t *testing.T) healingCapture {
	t.Helper()
	data, err := os.ReadFile("testdata/thinking_healing.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture healingCapture
	if err := json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	if capture.CaseCount != len(capture.Cases) {
		t.Fatalf("capture caseCount=%d, but has %d cases", capture.CaseCount, len(capture.Cases))
	}
	if capture.CaseCount < 80 {
		t.Fatalf("capture has %d scenarios; want at least 80", capture.CaseCount)
	}
	return capture
}

func requireEventsEqual(t *testing.T, name string, step int, want, got []HealEvent) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("%s step %d: got %d events, want %d\n got: %#v\nwant: %#v", name, step, len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s step %d event %d: got %#v, want %#v", name, step, i, got[i], want[i])
		}
	}
}

func TestThinkingHealingMatchesNeoPiCapture(t *testing.T) {
	capture := loadThinkingCapture(t)
	for _, tc := range capture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			healer := NewThinkingHealer()
			if len(tc.Chunks) != len(tc.Feeds) {
				t.Fatalf("capture has %d chunks, but %d feed snapshots", len(tc.Chunks), len(tc.Feeds))
			}
			for i, chunk := range tc.Chunks {
				requireEventsEqual(t, tc.Name, i, tc.Feeds[i], healer.Feed(chunk))
			}
			requireEventsEqual(t, tc.Name, len(tc.Chunks), tc.Flush, healer.Flush())
		})
	}
}

func TestThinkingFenceStripperMatchesNeoPiCapture(t *testing.T) {
	capture := loadThinkingCapture(t)
	for _, tc := range capture.Strippers {
		t.Run(tc.Name, func(t *testing.T) {
			if len(tc.Chunks) != len(tc.Pushes) {
				t.Fatalf("capture has %d chunks, but %d push snapshots", len(tc.Chunks), len(tc.Pushes))
			}
			stripper := NewFenceStripper()
			for i, chunk := range tc.Chunks {
				if got := stripper.Push(chunk); got != tc.Pushes[i] {
					t.Fatalf("push %d: got %q, want %q", i, got, tc.Pushes[i])
				}
			}
			if got := stripper.Flush(); got != tc.Flush {
				t.Fatalf("flush: got %q, want %q", got, tc.Flush)
			}
		})
	}
}

func TestRenderDemotedThinkingMatchesNeoPiCapture(t *testing.T) {
	capture := loadThinkingCapture(t)
	for _, tc := range capture.Demotions {
		got := RenderDemotedThinking(tc.Model, tc.Text)
		if got != tc.Output {
			t.Errorf("model %q text %q: got %q, want %q", tc.Model, tc.Text, got, tc.Output)
		}
	}
}
