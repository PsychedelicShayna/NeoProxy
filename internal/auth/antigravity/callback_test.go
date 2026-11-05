package antigravity

import (
	"encoding/json"
	"testing"
)

// TestParseCallbackInputMatchesNeoPi replays
// testdata/neopi_antigravity_callback_capture.json: NeoPi's own
// parseCallbackInput over pasted redirect URLs, query strings and bare codes.
func TestParseCallbackInputMatchesNeoPi(t *testing.T) {
	var capture struct {
		Cases []struct {
			Input   string  `json:"input"`
			Code    *string `json:"code"`
			State   *string `json:"state"`
			HasCode bool    `json:"hasCode"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(readCapture(t, "neopi_antigravity_callback_capture.json"), &capture); err != nil {
		t.Fatal(err)
	}
	if len(capture.Cases) == 0 {
		t.Fatal("capture has no cases")
	}
	same := func(got, want *string) bool {
		if got == nil || want == nil {
			return got == nil && want == nil
		}
		return *got == *want
	}
	show := func(value *string) string {
		if value == nil {
			return "<undefined>"
		}
		return *value
	}
	for _, c := range capture.Cases {
		got := ParseCallbackInput(c.Input)
		if !same(got.Code, c.Code) || !same(got.State, c.State) {
			t.Errorf("ParseCallbackInput(%q) = code %q state %q, want code %q state %q",
				c.Input, show(got.Code), show(got.State), show(c.Code), show(c.State))
		}
	}
}
