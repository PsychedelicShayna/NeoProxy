package openaiwire

import (
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
)

var (
	// strictRejectionPattern is the message family shouldRetryWithoutStrictTools
	// reads as a provider refusing strict tool schemas.
	strictRejectionPattern = regexp.MustCompile(`(?i)wrong_api_format|mixed values for 'strict'|tool[s]?\b.*strict|\bstrict\b.*tool|tool parameters? schema|invalid schema for function|structured[_ -]?outputs?\b[^\n]*(?:not (?:supported|available|enabled)|unsupported)|(?:not support|unsupported)[^\n]*structured[_ -]?outputs?\b`)
	// genericProviderError is OpenRouter's opaque routed-provider failure.
	genericProviderError = regexp.MustCompile(`(?i)^(?:400\s+)?Provider returned error$`)
	invalidRequestError  = regexp.MustCompile(`(?i)invalid_request_error`)
	compiledGrammar      = regexp.MustCompile(`(?i)compiled grammar`)
	grammarTooLarge      = regexp.MustCompile(`(?i)too large`)
)

// StrictToolsRetry is the policy of NeoPi's openai-responses loop for a failed
// attempt: retry once without strict tools when the request carried them.
type StrictToolsRetry struct {
	// GrammarTooLarge is compat.retryWithoutStrictOnGrammarError.
	GrammarTooLarge bool
	// OpenRouter enables the opaque "Provider returned error" heuristic.
	OpenRouter bool
}

// strictToolsApplied is params.tools.some(t => t.strict === true).
func strictToolsApplied(body []byte) bool {
	for _, tool := range gjson.GetBytes(body, "tools").Array() {
		if tool.Get("strict").Type == gjson.True {
			return true
		}
	}
	return false
}

// shouldRetry is isCompiledGrammarTooLargeStrictError (gated by the compat
// flag) or shouldRetryWithoutStrictTools over a captured HTTP failure.
func (p StrictToolsRetry) shouldRetry(err *HTTPError) bool {
	if err.Status != 400 && err.Status != 422 {
		return false
	}
	message := err.Error()
	parts := strings.TrimSpace(message)
	if body := strings.TrimSpace(string(err.Body)); body != "" {
		parts += "\n" + body
	}
	if p.GrammarTooLarge && err.Status == 400 && invalidRequestError.MatchString(parts) && compiledGrammar.MatchString(parts) && grammarTooLarge.MatchString(parts) {
		return true
	}
	if strictRejectionPattern.MatchString(parts) {
		return true
	}
	if !p.OpenRouter || !genericProviderError.MatchString(strings.TrimSpace(message)) {
		return false
	}
	// OpenRouter hides the routed provider's own words in error.metadata.raw;
	// a generic failure with none of them is the strict-schema signature.
	raw := gjson.GetBytes(err.Body, "error.metadata.raw")
	if raw.Exists() {
		if raw.Type == gjson.String {
			return strings.TrimSpace(raw.String()) == ""
		}
		return raw.Type == gjson.Null
	}
	return true
}
