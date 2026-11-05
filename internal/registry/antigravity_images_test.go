package registry

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
)

// TestAntigravityBundledImageModels requires NeoPi's bundled image rows
// (kind "image" in capture_antigravity_npi_models.ts) to be served as Images
// API models, and only those: chat rows never carry the image type.
func TestAntigravityBundledImageModels(t *testing.T) {
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
		if kind, _ := record["kind"].(string); kind == "image" {
			delete(record, "kind")
			want[record["id"].(string)] = record
		}
	}
	if len(want) == 0 {
		t.Fatal("NeoPi's seed has no image rows")
	}
	got := map[string]map[string]any{}
	for _, info := range AntigravityBundledModels() {
		_, isImage := want[info.ID]
		if (info.Type == OpenAIImageModelType) != isImage {
			t.Errorf("%s: type %q, image row %v", info.ID, info.Type, isImage)
		}
		if !isImage {
			continue
		}
		if !slices.Equal(info.SupportedOutputModalities, []string{"image"}) {
			t.Errorf("%s output modalities = %v", info.ID, info.SupportedOutputModalities)
		}
		got[info.ID] = antigravityRecordMap(t, info.NeoPi)
		if resolved := AntigravityBundledImageNeoPiModel(info.ID); resolved == nil {
			t.Errorf("%s has no bundled image record", info.ID)
		}
	}
	antigravityCompareRecords(t, want, got)
	if AntigravityBundledNeoPiModel("gemini-3-pro-image") != nil {
		t.Error("the chat resolver must not return image rows")
	}
}

// TestAntigravityDiscoveredImageModels requires a live roster to register its
// image SKUs as Images API models beside its chat models.
func TestAntigravityDiscoveredImageModels(t *testing.T) {
	payload := []byte(`{"models":{
		"gemini-3.1-pro-low":{"displayName":"Gemini 3.1 Pro (Low)","supportsImages":true,"supportsThinking":true,"thinkingLevel":1,"maxTokens":1048576,"maxOutputTokens":65535},
		"gemini-3.1-flash-image":{"displayName":"Nano Banana 2","supportsImages":true,"maxTokens":65536,"maxOutputTokens":65536}
	},"imageGenerationModelIds":["gemini-3.1-flash-image"]}`)
	infos, err := AntigravityDiscoveredModels(payload, "https://daily-cloudcode-pa.googleapis.com")
	if err != nil {
		t.Fatal(err)
	}
	var images, chats []string
	for _, info := range infos {
		if info.Type == OpenAIImageModelType {
			images = append(images, info.ID)
			if info.NeoPi == nil || info.NeoPi.API != "google-gemini-cli" {
				t.Errorf("%s image row has no NeoPi record: %+v", info.ID, info.NeoPi)
			}
			continue
		}
		chats = append(chats, info.ID)
	}
	if !slices.Contains(images, "gemini-3.1-flash-image") {
		t.Fatalf("image rows = %v, want the advertised gemini-3.1-flash-image", images)
	}
	if slices.Contains(chats, "gemini-3.1-flash-image") || len(chats) == 0 {
		t.Fatalf("chat rows = %v", chats)
	}
}
