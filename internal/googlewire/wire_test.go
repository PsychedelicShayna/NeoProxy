package googlewire

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func gemini3() *Model {
	return &Model{ID: "gemini-3.1-pro-preview", Provider: "google", API: "google-generative-ai", Input: []string{"text", "image"},
		Reasoning: true, MaxTokens: 65536, NameMaxLength: 128,
		Thinking: &Thinking{Efforts: []string{"low", "high"}, Mode: "google-level", RequiresEffort: true},
		Compat:   Compat{MultimodalFunctionResponse: true, RequiresSkipThoughtSignature: true, SupportsFunctionPartID: true}}
}

func body(t *testing.T, model *Model, ctx Context, opts Options) gjson.Result {
	t.Helper()
	resolved, err := MapOptions(model, opts)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := BuildBody(model, ctx, resolved, nil)
	if err != nil {
		t.Fatal(err)
	}
	return gjson.ParseBytes(raw)
}

// A failed tool result keys its text under "error", NeoPi's SDK convention.
func TestToolResultErrorUsesErrorKey(t *testing.T) {
	model := gemini3()
	ctx := Context{Messages: []Message{
		{Role: RoleUser, IsString: true, Text: "go"},
		{Role: RoleAssistant, Provider: "google", API: model.API, Model: model.ID, StopReason: "toolUse",
			Content: []Block{{Type: BlockToolCall, ID: "c1", Name: "lookup", Arguments: json.RawMessage(`{}`)}}},
		{Role: RoleToolResult, ToolCallID: "c1", ToolName: "lookup", IsError: true, Content: []Block{{Type: BlockText, Text: "Boom."}}},
	}}
	got := body(t, model, ctx, Options{})
	if v := got.Get("contents.2.parts.0.functionResponse.response.error").String(); v != "Boom." {
		t.Fatalf("response = %s", got.Get("contents.2.parts.0.functionResponse").Raw)
	}
}

// An orphan tool result becomes a stale-tool-result note naming its tool.
func TestOrphanToolResultBecomesStaleNote(t *testing.T) {
	model := gemini3()
	ctx := Context{Messages: []Message{
		{Role: RoleUser, IsString: true, Text: "hi"},
		{Role: RoleToolResult, ToolCallID: "gone", ToolName: "lookup", IsError: true, Content: []Block{{Type: BlockText, Text: "late"}}},
		{Role: RoleUser, IsString: true, Text: "go on"},
	}}
	got := body(t, model, ctx, Options{})
	want := "<stale-tool-result tool=\"lookup\" id=\"gone\" is-error=\"true\">\nlate\n</stale-tool-result>"
	if v := got.Get("contents.1.parts.0.text").String(); v != want {
		t.Fatalf("note = %q, want %q", v, want)
	}
}

// A call with no result gets NeoPi's synthetic "No result provided" error.
func TestMissingToolResultIsSynthesized(t *testing.T) {
	model := gemini3()
	ctx := Context{Messages: []Message{
		{Role: RoleUser, IsString: true, Text: "go"},
		{Role: RoleAssistant, Provider: "google", API: model.API, Model: model.ID, StopReason: "toolUse",
			Content: []Block{{Type: BlockToolCall, ID: "c1", Name: "lookup", Arguments: json.RawMessage(`{}`)}}},
		{Role: RoleUser, IsString: true, Text: "never mind"},
	}}
	got := body(t, model, ctx, Options{})
	if v := got.Get("contents.2.parts.0.functionResponse.response.error").String(); v != "No result provided" {
		t.Fatalf("contents = %s", got.Get("contents").Raw)
	}
}

// Malformed tool names are dropped with their results.
func TestMalformedToolCallsAreDropped(t *testing.T) {
	model := gemini3()
	ctx := Context{Messages: []Message{
		{Role: RoleUser, IsString: true, Text: "go"},
		{Role: RoleAssistant, Provider: "google", API: model.API, Model: model.ID, StopReason: "toolUse",
			Content: []Block{{Type: BlockToolCall, ID: "c1", Name: "bad name!", Arguments: json.RawMessage(`{}`)}}},
		{Role: RoleToolResult, ToolCallID: "c1", ToolName: "bad name!", Content: []Block{{Type: BlockText, Text: "x"}}},
		{Role: RoleUser, IsString: true, Text: "again"},
	}}
	got := body(t, model, ctx, Options{})
	if n := len(got.Get("contents").Array()); n != 2 {
		t.Fatalf("contents = %s", got.Get("contents").Raw)
	}
}

func TestMapOptionsRejectsUnsupportedEffort(t *testing.T) {
	if _, err := MapOptions(gemini3(), Options{Reasoning: "medium"}); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("err = %v", err)
	}
}

func TestExtractErrorMessageKeepsRPCResidueOnUsageLimit(t *testing.T) {
	text := `{"error":{"code":429,"message":"Quota exceeded.","status":"RESOURCE_EXHAUSTED","details":[{"reason":"RATE_LIMIT_EXCEEDED"}]}}`
	if got := ExtractErrorMessage(text, http.StatusTooManyRequests); got != `Quota exceeded. {"error":{"status":"RESOURCE_EXHAUSTED","details":[{"reason":"RATE_LIMIT_EXCEEDED"}]}}` {
		t.Fatalf("429 = %s", got)
	}
	if got := ExtractErrorMessage(text, http.StatusBadRequest); got != "Quota exceeded." {
		t.Fatalf("400 = %s", got)
	}
	if got := ExtractErrorMessage("", 500); got != "Unknown error" {
		t.Fatalf("empty = %s", got)
	}
	if got := ExtractErrorMessage("not json", 500); got != "not json" {
		t.Fatalf("plain = %s", got)
	}
}

func TestStreamChunkErrorCarriesStatus(t *testing.T) {
	err := StreamChunkError([]byte(`{"error":{"code":429,"message":"Slow down","status":"RESOURCE_EXHAUSTED"}}`))
	if got := err.Error(); got != "Google API stream error: Slow down (RESOURCE_EXHAUSTED)" {
		t.Fatalf("message = %s", got)
	}
	if status := err.(*StreamError).StatusCode(); status != 429 {
		t.Fatalf("status = %d", status)
	}
}

func TestMapUsageSplitsCache(t *testing.T) {
	input, output, cacheRead, reasoning, total := MapUsage(gjson.Parse(`{"promptTokenCount":100,"cachedContentTokenCount":30,"candidatesTokenCount":10,"thoughtsTokenCount":5,"totalTokenCount":115}`))
	if input != 70 || output != 15 || cacheRead != 30 || reasoning != 5 || total != 115 {
		t.Fatalf("usage = %d %d %d %d %d", input, output, cacheRead, reasoning, total)
	}
	// An absent prompt count falls back to total - candidates - thoughts; a cache
	// count above the prompt is clamped so input never goes negative.
	input, _, cacheRead, _, _ = MapUsage(gjson.Parse(`{"cachedContentTokenCount":999,"candidatesTokenCount":10,"totalTokenCount":50}`))
	if input != 0 || cacheRead != 40 {
		t.Fatalf("fallback = %d %d", input, cacheRead)
	}
}

func TestNormalizeToolCallIDReplacesAndCaps(t *testing.T) {
	if got := NormalizeToolCallID("call/odd id|fc_1"); got != "call_odd_id_fc_1" {
		t.Fatalf("got %s", got)
	}
	if got := NormalizeToolCallID(strings.Repeat("a", 80)); len(got) != 64 {
		t.Fatalf("len = %d", len(got))
	}
}
