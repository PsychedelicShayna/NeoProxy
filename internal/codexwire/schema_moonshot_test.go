package codexwire

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/tidwall/gjson"
)

// normalizeSchemaForMoonshot reproduces NeoPi's output byte for byte over
// schemas reaching every branch of the Moonshot option set
// (testdata/capture_moonshot_schema.ts), both over the tool wire schema and
// over the raw parameters.
func TestNormalizeSchemaForMoonshotMatchesNeoPi(t *testing.T) {
	data, err := os.ReadFile("testdata/moonshot_schema_capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Cases []struct {
			Name       string          `json:"name"`
			Parameters json.RawMessage `json:"parameters"`
			Wire       *string         `json:"wire"`
			Moonshot   *string         `json:"moonshot"`
			Raw        string          `json:"raw"`
		} `json:"cases"`
	}
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	if len(capture.Cases) < 40 {
		t.Fatalf("capture has %d cases, want at least 40", len(capture.Cases))
	}
	for _, tc := range capture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			if tc.Wire != nil {
				wire := ToolWireSchema(gjson.ParseBytes(tc.Parameters))
				if string(wire) != *tc.Wire {
					t.Fatalf("wire:\n got %s\nwant %s", wire, *tc.Wire)
				}
				if got := NormalizeSchemaForMoonshot(wire); tc.Moonshot == nil || string(got) != *tc.Moonshot {
					t.Errorf("moonshot:\n got %s\nwant %v", got, tc.Moonshot)
				}
			}
			if got := NormalizeSchemaForMoonshot(tc.Parameters); string(got) != tc.Raw {
				t.Errorf("raw:\n got %s\nwant %s", got, tc.Raw)
			}
		})
	}
}
