package codexwire

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/tidwall/gjson"
)

// normalizeSchemaForCCA reproduces NeoPi's output byte for byte over schemas
// reaching every branch of the CCA option set, through both Antigravity
// pipelines (testdata/capture_cca_schema.ts): Claude tools run it on the wire
// schema, Gemini tools on the normalizeSchemaForGoogle output. The Google-only
// result is checked too, since the walker is shared.
func TestNormalizeSchemaForCCAMatchesNeoPi(t *testing.T) {
	data, err := os.ReadFile("testdata/cca_schema_capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Cases []struct {
			Name       string          `json:"name"`
			Parameters json.RawMessage `json:"parameters"`
			Wire       string          `json:"wire"`
			Google     string          `json:"google"`
			Claude     string          `json:"claude"`
			Gemini     string          `json:"gemini"`
		} `json:"cases"`
	}
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	if len(capture.Cases) < 60 {
		t.Fatalf("capture has %d cases, want at least 60", len(capture.Cases))
	}
	for _, tc := range capture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			wire := ToolWireSchema(gjson.ParseBytes(tc.Parameters))
			if string(wire) != tc.Wire {
				t.Fatalf("wire:\n got %s\nwant %s", wire, tc.Wire)
			}
			google := NormalizeSchemaForGoogle(wire)
			if string(google) != tc.Google {
				t.Errorf("google:\n got %s\nwant %s", google, tc.Google)
			}
			if got := NormalizeSchemaForCCA(wire); string(got) != tc.Claude {
				t.Errorf("claude:\n got %s\nwant %s", got, tc.Claude)
			}
			if got := NormalizeSchemaForGoogleThenCCA(wire); string(got) != tc.Gemini {
				t.Errorf("gemini:\n got %s\nwant %s", got, tc.Gemini)
			}
		})
	}
}
