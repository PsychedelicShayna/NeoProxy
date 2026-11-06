package config

import (
	"encoding/json"
	"testing"
)

func TestDisableImageGenerationMode_UnmarshalTOML(t *testing.T) {

	{
		var w Config
		if err := decodeTOMLTestConfig([]byte("disable-image-generation = false\n"), &w); err != nil {
			t.Fatalf("unmarshal false: %v", err)
		}
		if w.DisableImageGeneration != DisableImageGenerationOff {
			t.Fatalf("false => %v, want %v", w.DisableImageGeneration, DisableImageGenerationOff)
		}
	}

	{
		var w Config
		if err := decodeTOMLTestConfig([]byte("disable-image-generation = true\n"), &w); err != nil {
			t.Fatalf("unmarshal true: %v", err)
		}
		if w.DisableImageGeneration != DisableImageGenerationAll {
			t.Fatalf("true => %v, want %v", w.DisableImageGeneration, DisableImageGenerationAll)
		}
	}

	{
		var w Config
		if err := decodeTOMLTestConfig([]byte("disable-image-generation = 'chat'\n"), &w); err != nil {
			t.Fatalf("unmarshal chat: %v", err)
		}
		if w.DisableImageGeneration != DisableImageGenerationChat {
			t.Fatalf("chat => %v, want %v", w.DisableImageGeneration, DisableImageGenerationChat)
		}
	}

	{
		var w Config
		if err := decodeTOMLTestConfig([]byte("disable-image-generation = 'passthrough'\n"), &w); err != nil {
			t.Fatalf("unmarshal passthrough: %v", err)
		}
		if w.DisableImageGeneration != DisableImageGenerationPassthrough {
			t.Fatalf("passthrough => %v, want %v", w.DisableImageGeneration, DisableImageGenerationPassthrough)
		}
	}
}

func TestDisableImageGenerationMode_UnmarshalJSON(t *testing.T) {
	{
		var v DisableImageGenerationMode
		if err := json.Unmarshal([]byte("false"), &v); err != nil {
			t.Fatalf("unmarshal false: %v", err)
		}
		if v != DisableImageGenerationOff {
			t.Fatalf("false => %v, want %v", v, DisableImageGenerationOff)
		}
	}

	{
		var v DisableImageGenerationMode
		if err := json.Unmarshal([]byte("true"), &v); err != nil {
			t.Fatalf("unmarshal true: %v", err)
		}
		if v != DisableImageGenerationAll {
			t.Fatalf("true => %v, want %v", v, DisableImageGenerationAll)
		}
	}

	{
		var v DisableImageGenerationMode
		if err := json.Unmarshal([]byte(`"chat"`), &v); err != nil {
			t.Fatalf("unmarshal chat: %v", err)
		}
		if v != DisableImageGenerationChat {
			t.Fatalf("chat => %v, want %v", v, DisableImageGenerationChat)
		}
	}

	{
		var v DisableImageGenerationMode
		if err := json.Unmarshal([]byte(`"passthrough"`), &v); err != nil {
			t.Fatalf("unmarshal passthrough: %v", err)
		}
		if v != DisableImageGenerationPassthrough {
			t.Fatalf("passthrough => %v, want %v", v, DisableImageGenerationPassthrough)
		}
	}
}
