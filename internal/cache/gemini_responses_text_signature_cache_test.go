package cache

import (
	"strings"
	"testing"
)

func geminiResponsesTextSignatureTestItem(signature string) []byte {
	return []byte(`{"type":"thought_signature","targetKind":"text","targetHash":"abc","thoughtSignature":"` + signature + `","ignored":true}`)
}

func TestGeminiResponsesTextSignatureCacheRoundTrip(t *testing.T) {
	ClearGeminiResponsesTextSignatureCache()
	t.Cleanup(ClearGeminiResponsesTextSignatureCache)
	signature := strings.Repeat("s", 32)
	if !CacheGeminiResponsesTextSignatureItems("gemini-test", "msg-1", [][]byte{geminiResponsesTextSignatureTestItem(signature)}) {
		t.Fatal("cache rejected a valid signature")
	}
	items, ok := GetGeminiResponsesTextSignatureItems("gemini-test", "msg-1")
	if !ok || len(items) != 1 {
		t.Fatalf("items = %q, ok = %v", items, ok)
	}
	want := `{"type":"thought_signature","thoughtSignature":"` + signature + `","targetKind":"text","targetHash":"abc"}`
	if string(items[0]) != want {
		t.Fatalf("normalized item = %s, want %s", items[0], want)
	}
	if _, ok := GetGeminiResponsesTextSignatureItems("other-model", "msg-1"); ok {
		t.Fatal("entry leaked across models")
	}
	if _, ok := GetGeminiResponsesTextSignatureItems("gemini-test", "msg-2"); ok {
		t.Fatal("entry leaked across messages")
	}
}

func TestGeminiResponsesTextSignatureCacheRejectsUnusableSignatures(t *testing.T) {
	ClearGeminiResponsesTextSignatureCache()
	t.Cleanup(ClearGeminiResponsesTextSignatureCache)
	for _, signature := range []string{"", "short", "skip_thought_signature_validator"} {
		if CacheGeminiResponsesTextSignatureItems("gemini-test", "msg-1", [][]byte{geminiResponsesTextSignatureTestItem(signature)}) {
			t.Fatalf("cache accepted signature %q", signature)
		}
	}
	if CacheGeminiResponsesTextSignatureItems("", "msg-1", [][]byte{geminiResponsesTextSignatureTestItem(strings.Repeat("s", 32))}) {
		t.Fatal("cache accepted an empty model")
	}
}
