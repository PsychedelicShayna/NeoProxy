package openaiwire

import "testing"

func TestStrictToolsRetryPolicy(t *testing.T) {
	or := StrictToolsRetry{OpenRouter: true}
	grammar := StrictToolsRetry{GrammarTooLarge: true}
	for _, tc := range []struct {
		name   string
		policy StrictToolsRetry
		err    *HTTPError
		want   bool
	}{
		{"opaque openrouter 400", or, &HTTPError{Status: 400, Body: []byte(`{"error":{"message":"Provider returned error","code":400}}`)}, true},
		{"openrouter with routed words", or, &HTTPError{Status: 400, Body: []byte(`{"error":{"message":"Provider returned error","metadata":{"raw":"bad temperature"}}}`)}, false},
		{"opaque but not openrouter", StrictToolsRetry{}, &HTTPError{Status: 400, Body: []byte(`{"error":{"message":"Provider returned error"}}`)}, false},
		{"strict named", StrictToolsRetry{}, &HTTPError{Status: 400, Body: []byte(`{"error":{"message":"Invalid schema for function 'x': strict mode"}}`)}, true},
		{"structured outputs unsupported", StrictToolsRetry{}, &HTTPError{Status: 422, Body: []byte(`{"error":{"message":"structured outputs are not supported"}}`)}, true},
		{"grammar too large flagged", grammar, &HTTPError{Status: 400, Body: []byte(`{"error":{"type":"invalid_request_error","message":"The compiled grammar is too large"}}`)}, true},
		{"grammar too large unflagged", StrictToolsRetry{}, &HTTPError{Status: 400, Body: []byte(`{"error":{"type":"invalid_request_error","message":"The compiled grammar is too large"}}`)}, false},
		{"server error", or, &HTTPError{Status: 500, Body: []byte(`{"error":{"message":"Provider returned error"}}`)}, false},
		{"unrelated 400", or, &HTTPError{Status: 400, Body: []byte(`{"error":{"message":"max_tokens too large"}}`)}, false},
	} {
		if got := tc.policy.shouldRetry(tc.err); got != tc.want {
			t.Errorf("%s: shouldRetry = %v, want %v", tc.name, got, tc.want)
		}
	}
}
