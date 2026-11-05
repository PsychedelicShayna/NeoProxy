package executor

import "testing"

func TestNeoPiResponsesUsageOrchestration(t *testing.T) {
	// Primary totals exclude orchestration: it is added to the billed buckets.
	separate := []byte(`{"response":{"usage":{"input_tokens":100,"output_tokens":20,"total_tokens":160,"input_tokens_details":{"cached_tokens":10,"orchestration_input_tokens":30,"orchestration_input_cached_tokens":5},"output_tokens_details":{"orchestration_output_tokens":10}}}}`)
	detail, ok := neoPiResponsesUsage(separate)
	if !ok || detail.InputTokens != 130 || detail.OutputTokens != 30 || detail.CachedTokens != 15 || detail.TotalTokens != 160 {
		t.Fatalf("separate: %+v", detail)
	}
	// Primary totals already include it: nothing is added twice.
	inclusive := []byte(`{"response":{"usage":{"input_tokens":130,"output_tokens":30,"total_tokens":160,"input_tokens_details":{"orchestration_input_tokens":30},"output_tokens_details":{"orchestration_output_tokens":10}}}}`)
	detail, ok = neoPiResponsesUsage(inclusive)
	if !ok || detail.InputTokens != 130 || detail.OutputTokens != 30 || detail.TotalTokens != 160 {
		t.Fatalf("inclusive: %+v", detail)
	}
	// No orchestration: the generic reading stands.
	plain := []byte(`{"response":{"usage":{"input_tokens":5,"output_tokens":3,"total_tokens":8}}}`)
	if detail, ok = neoPiResponsesUsage(plain); !ok || detail.InputTokens != 5 || detail.TotalTokens != 8 {
		t.Fatalf("plain: %+v", detail)
	}
}

func TestMetaErrorPolicy(t *testing.T) {
	if err := wrapMetaUpstreamError(404, []byte(`{"error":{"message":"no such model"}}`)); err.(statusErr).retryAfter != nil {
		t.Errorf("a 404 carries no proxy cooldown")
	}
	if _, scoped := wrapMetaUpstreamError(429, []byte(`{"error":{"code":"rate_limit_exceeded","resets_at":4102444800}}`)).(metaRateLimitError); scoped {
		t.Errorf("a plain rate limit is not subscription quota")
	}
	if _, scoped := wrapMetaUpstreamError(429, []byte(`{"error":{"message":"Subscription quota exhausted"}}`)).(metaRateLimitError); !scoped {
		t.Errorf("subscription quota stays credential-scoped")
	}
}
